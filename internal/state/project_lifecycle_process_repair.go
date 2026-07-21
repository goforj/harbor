package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"gorm.io/gorm"
)

// ProcessBackedProjectRuntimeRepairBoundary captures exact process evidence as a fence while native observation remains runtime authority.
type ProcessBackedProjectRuntimeRepairBoundary struct {
	Project                ProjectRecord
	SessionID              domain.SessionID
	SessionGeneration      uint64
	SessionUpdatedAt       time.Time
	RecoveryOperation      OperationRecord
	NetworkRevision        domain.Sequence
	NetworkUpdatedAt       time.Time
	PrimaryLease           identity.Lease
	PrimaryLeaseGeneration uint64
	Process                domain.ProcessEvidence
}

// Validate reports whether the boundary identifies one unavailable project and one complete Harbor process receipt.
func (boundary ProcessBackedProjectRuntimeRepairBoundary) Validate() error {
	if err := validateProjectRuntimeRepairBoundaryAuthority(
		boundary.Project,
		boundary.SessionID,
		boundary.SessionGeneration,
		boundary.SessionUpdatedAt,
		boundary.RecoveryOperation,
		boundary.NetworkRevision,
		boundary.NetworkUpdatedAt,
		boundary.PrimaryLease,
		boundary.PrimaryLeaseGeneration,
	); err != nil {
		return err
	}
	if err := validateProcessBackedProjectRuntimeRecoveryOperation(boundary.Project, boundary.RecoveryOperation); err != nil {
		return err
	}
	return boundary.Process.Validate()
}

// CompleteProcessBackedProjectRuntimeRepairRequest fences native settlement before retiring the exact process-backed session row.
type CompleteProcessBackedProjectRuntimeRepairRequest struct {
	CompleteRetainedProjectRuntimeRepairRequest
	ExpectedProcess domain.ProcessEvidence
}

// ProcessBackedProjectRuntimeRepairBoundary returns durable identity fences without interpreting process health from SQLite.
func (store *Store) ProcessBackedProjectRuntimeRepairBoundary(
	ctx context.Context,
	projectID domain.ProjectID,
) (ProcessBackedProjectRuntimeRepairBoundary, error) {
	ctx = normalizeContext(ctx)
	if err := projectID.Validate(); err != nil {
		return ProcessBackedProjectRuntimeRepairBoundary{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProcessBackedProjectRuntimeRepairBoundary{}, err
	}
	builder, err := store.projects.WithContext(ctx).Builder()
	if err != nil {
		return ProcessBackedProjectRuntimeRepairBoundary{}, fmt.Errorf("open process-backed project runtime repair boundary: %w", err)
	}

	var boundary ProcessBackedProjectRuntimeRepairBoundary
	err = builder.Transaction(func(tx *gorm.DB) error {
		authority, readErr := readProjectRuntimeRepairDurableAuthority(tx, projectID, projectRuntimeRepairSessionProcessBacked)
		if readErr != nil {
			return readErr
		}
		if err := validateProcessBackedProjectRuntimeRecoveryHistory(authority.Project, authority.RecoveryOperation, authority.RecoveryHistory); err != nil {
			return err
		}
		if authority.Process == nil {
			return fmt.Errorf("project %q process-backed repair boundary has no process evidence", projectID)
		}
		boundary = processBackedProjectRuntimeRepairBoundaryFromAuthority(authority)
		if err := boundary.Validate(); err != nil {
			return corruptStateError("process-backed project runtime repair", string(projectID), err)
		}
		return nil
	})
	if err != nil {
		return ProcessBackedProjectRuntimeRepairBoundary{}, fmt.Errorf("read project %q process-backed runtime repair boundary: %w", projectID, err)
	}
	return boundary, nil
}

// CompleteProcessBackedProjectRuntimeRepair retires exact process-backed authority only after native settlement and every fence still match.
func (store *Store) CompleteProcessBackedProjectRuntimeRepair(
	ctx context.Context,
	request CompleteProcessBackedProjectRuntimeRepairRequest,
) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := validateCompleteProcessBackedProjectRuntimeRepairRequest(request); err != nil {
		return ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}

	var result ProjectRecord
	err := store.mutations.mutate(ctx, "complete process-backed project runtime repair", func(tx *gorm.DB) error {
		if err := requireNoCompetingProjectOperation(tx, request.ProjectID, ""); err != nil {
			return err
		}
		authority, err := readProjectRuntimeRepairDurableAuthority(tx, request.ProjectID, projectRuntimeRepairSessionProcessBacked)
		if err != nil {
			return err
		}
		if err := validateProcessBackedProjectRuntimeRecoveryHistory(authority.Project, authority.RecoveryOperation, authority.RecoveryHistory); err != nil {
			return err
		}
		boundary := processBackedProjectRuntimeRepairBoundaryFromAuthority(authority)
		if err := boundary.Validate(); err != nil {
			return err
		}
		if err := validateRetainedProjectRuntimeRepairFences(
			retainedProjectRuntimeRepairBoundaryFromProcess(boundary),
			request.CompleteRetainedProjectRuntimeRepairRequest,
		); err != nil {
			return err
		}
		if boundary.Process != request.ExpectedProcess {
			return fmt.Errorf("project %q process evidence no longer matches the inspected runtime boundary", request.ProjectID)
		}
		if request.At.Before(boundary.Project.Project.UpdatedAt) ||
			request.At.Before(boundary.SessionUpdatedAt) ||
			request.At.Before(boundary.NetworkUpdatedAt) {
			return fmt.Errorf("process-backed runtime repair time precedes a durable inspection fence")
		}
		if err := deleteExactProcessBackedProjectSession(tx, request); err != nil {
			return err
		}
		project := stoppedProjectProjection(boundary.Project.Project, request.At)
		persisted, err := persistLifecycleProject(tx, project)
		if err != nil {
			return err
		}
		result = persisted
		return nil
	})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("complete project %q process-backed runtime repair: %w", request.ProjectID, err)
	}
	return result, nil
}

// validateProcessBackedProjectRuntimeRecoveryOperation recognizes any lifecycle operation quarantined with an unresolved process scope.
func validateProcessBackedProjectRuntimeRecoveryOperation(project ProjectRecord, operation OperationRecord) error {
	if _, err := sequenceToModelInt("process-backed recovery operation revision", operation.Revision, false); err != nil {
		return err
	}
	marker := operation.Operation
	if marker.ProjectID != project.Project.ID ||
		(marker.Kind != domain.OperationKindProjectStart && marker.Kind != domain.OperationKindProjectStop && marker.Kind != domain.OperationKindProjectRestart) ||
		marker.State != domain.OperationFailed ||
		marker.Phase != domain.ProjectRecoveryRequiredPhase ||
		marker.Problem == nil ||
		marker.Problem.Code != domain.ProjectRecoveryAmbiguousLaunchProblemCode ||
		marker.Problem.Retryable {
		return fmt.Errorf("project %q does not own an exact process-backed recovery marker", project.Project.ID)
	}
	if marker.FinishedAt == nil || !marker.FinishedAt.Equal(project.Project.UpdatedAt) {
		return fmt.Errorf("project %q process-backed recovery marker does not match its quarantine projection", project.Project.ID)
	}
	if operation.Revision == domain.MaximumSequence || project.Revision != operation.Revision+1 {
		return fmt.Errorf("project %q process-backed recovery marker does not immediately precede its quarantine projection", project.Project.ID)
	}
	return nil
}

// validateProcessBackedProjectRuntimeRecoveryHistory accepts queued, running, failed lifecycle history with a final recovery quarantine edge.
func validateProcessBackedProjectRuntimeRecoveryHistory(
	project ProjectRecord,
	operation OperationRecord,
	history []OperationTransition,
) error {
	if len(history) != 3 ||
		history[0].State != domain.OperationQueued ||
		history[1].State != domain.OperationRunning ||
		history[2].State != domain.OperationFailed ||
		history[2].Phase != domain.ProjectRecoveryRequiredPhase ||
		history[0].Sequence == domain.MaximumSequence ||
		history[1].Sequence != history[0].Sequence+1 ||
		history[1].Sequence == domain.MaximumSequence ||
		// A running launch commits its starting projection between the running and quarantine transitions.
		(history[2].Sequence != history[1].Sequence+1 && history[2].Sequence != history[1].Sequence+2) ||
		history[2].Sequence == domain.MaximumSequence ||
		history[2].Sequence != operation.Revision ||
		!history[2].OccurredAt.Equal(project.Project.UpdatedAt) ||
		history[0].Problem != nil ||
		history[1].Problem != nil ||
		!operationProblemsEqual(history[2].Problem, operation.Operation.Problem) {
		return fmt.Errorf("project %q process-backed recovery marker does not have the exact quarantine history", project.Project.ID)
	}
	if history[0].OccurredAt.After(history[1].OccurredAt) || history[1].OccurredAt.After(history[2].OccurredAt) {
		return fmt.Errorf("project %q process-backed recovery history is not chronological", project.Project.ID)
	}
	return nil
}

// validateCompleteProcessBackedProjectRuntimeRepairRequest rejects finalization without exact process and durable fences.
func validateCompleteProcessBackedProjectRuntimeRepairRequest(request CompleteProcessBackedProjectRuntimeRepairRequest) error {
	if err := validateCompleteRetainedProjectRuntimeRepairRequest(request.CompleteRetainedProjectRuntimeRepairRequest); err != nil {
		return err
	}
	return request.ExpectedProcess.Validate()
}

// processBackedProjectRuntimeRepairBoundaryFromAuthority builds the process-backed public state projection from one transaction read.
func processBackedProjectRuntimeRepairBoundaryFromAuthority(authority projectRuntimeRepairDurableAuthority) ProcessBackedProjectRuntimeRepairBoundary {
	return ProcessBackedProjectRuntimeRepairBoundary{
		Project: authority.Project, SessionID: authority.SessionID, SessionGeneration: authority.SessionGeneration,
		SessionUpdatedAt: authority.SessionUpdatedAt, RecoveryOperation: authority.RecoveryOperation,
		NetworkRevision: authority.NetworkRevision, NetworkUpdatedAt: authority.NetworkUpdatedAt,
		PrimaryLease: authority.PrimaryLease, PrimaryLeaseGeneration: authority.PrimaryLeaseGeneration,
		Process: *authority.Process,
	}
}

// retainedProjectRuntimeRepairBoundaryFromProcess maps shared durable fences for the existing finalization comparison.
func retainedProjectRuntimeRepairBoundaryFromProcess(boundary ProcessBackedProjectRuntimeRepairBoundary) RetainedProjectRuntimeRepairBoundary {
	return RetainedProjectRuntimeRepairBoundary{
		Project: boundary.Project, SessionID: boundary.SessionID, SessionGeneration: boundary.SessionGeneration,
		SessionUpdatedAt: boundary.SessionUpdatedAt, RecoveryOperation: boundary.RecoveryOperation,
		NetworkRevision: boundary.NetworkRevision, NetworkUpdatedAt: boundary.NetworkUpdatedAt,
		PrimaryLease: boundary.PrimaryLease, PrimaryLeaseGeneration: boundary.PrimaryLeaseGeneration,
	}
}

// deleteExactProcessBackedProjectSession removes no session when any persisted process receipt or lifecycle fence changed.
func deleteExactProcessBackedProjectSession(
	tx *gorm.DB,
	request CompleteProcessBackedProjectRuntimeRepairRequest,
) error {
	process := request.ExpectedProcess
	deleted := tx.Where(
		`session_id = ? AND project_id = ? AND generation = ? AND owner = ? AND state IN ?
			 AND pid = ? AND birth_token = ? AND executable_identity = ? AND argument_digest = ?`,
		string(request.SessionID),
		string(request.ProjectID),
		int(request.ExpectedSessionGeneration),
		string(domain.SessionOwnerHarbor),
		[]string{string(domain.SessionAwaitingAttach), string(domain.SessionStopping)},
		process.PID,
		process.BirthToken,
		process.ExecutableIdentity,
		process.ArgumentDigest,
	).Delete(&models.ProjectSession{})
	if deleted.Error != nil {
		return fmt.Errorf("delete process-backed project session: %w", deleted.Error)
	}
	if deleted.RowsAffected != 1 {
		return fmt.Errorf("project %q process-backed session changed before repair completion", request.ProjectID)
	}
	return nil
}
