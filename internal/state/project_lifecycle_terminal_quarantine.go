package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// QuarantineTerminalProjectSessionRequest identifies one formerly ready session whose complete process scope is unresolved.
type QuarantineTerminalProjectSessionRequest struct {
	ProjectID                 domain.ProjectID
	ExpectedProjectRevision   domain.Sequence
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Operation                 domain.Operation
	RunningPhase              string
	FailurePhase              string
	Problem                   domain.Problem
	At                        time.Time
}

// ProjectRecoveryQuarantine contains the route-free project and actionable failure committed by recovery.
type ProjectRecoveryQuarantine struct {
	Operation OperationRecord
	Project   ProjectRecord
}

// QuarantineTerminalProjectSession atomically withholds a formerly ready project without claiming its unresolved process scope is absent.
func (store *Store) QuarantineTerminalProjectSession(
	ctx context.Context,
	request QuarantineTerminalProjectSessionRequest,
) (ProjectRecoveryQuarantine, error) {
	ctx = normalizeContext(ctx)
	if err := validateQuarantineTerminalProjectSessionRequest(request); err != nil {
		return ProjectRecoveryQuarantine{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecoveryQuarantine{}, err
	}

	var result ProjectRecoveryQuarantine
	err := store.mutations.mutate(ctx, "quarantine terminal project session", func(tx *gorm.DB) error {
		operation, err := enqueueOperationInTransaction(tx, request.Operation, false)
		if err != nil {
			return err
		}
		if operation.Operation.ID != request.Operation.ID {
			return fmt.Errorf("terminal session quarantine intent belongs to operation %q, not %q", operation.Operation.ID, request.Operation.ID)
		}
		history, err := operationHistoryInTransaction(tx, operation)
		if err != nil {
			return err
		}
		if operation.Operation.State == domain.OperationFailed {
			replayed, replayErr := replayQuarantineTerminalProjectSession(tx, operation, history, request)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			return nil
		}
		if operation.Operation.State != domain.OperationQueued {
			return fmt.Errorf("terminal session quarantine operation %q must be queued, got %q", operation.Operation.ID, operation.Operation.State)
		}

		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Revision != request.ExpectedProjectRevision {
			return &ProjectRevisionConflictError{
				ProjectID: request.ProjectID,
				Expected:  request.ExpectedProjectRevision,
				Actual:    project.Revision,
			}
		}
		if project.Project.State != domain.ProjectReady && project.Project.State != domain.ProjectDegraded && project.Project.State != domain.ProjectFailed {
			return fmt.Errorf("project %q must be ready, degraded, or failed before terminal session quarantine, got %q", request.ProjectID, project.Project.State)
		}
		sessionRow, owner, sessionState, generation, err := readExactUnresolvedProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, generation)
		}
		if owner != domain.SessionOwnerHarbor || sessionState != domain.SessionAwaitingAttach {
			return fmt.Errorf("session %q is not a Harbor-owned awaiting-attach recovery boundary", request.SessionID)
		}
		if request.At.Before(project.Project.UpdatedAt) || request.At.Before(sessionRow.UpdatedAt) {
			return fmt.Errorf(
				"terminal session quarantine time %s precedes project %s or session %s",
				request.At.Format(time.RFC3339Nano),
				project.Project.UpdatedAt.Format(time.RFC3339Nano),
				sessionRow.UpdatedAt.Format(time.RFC3339Nano),
			)
		}
		if err := requireNoCompetingProjectOperation(tx, request.ProjectID, request.Operation.ID); err != nil {
			return err
		}

		running, err := transitionOperationInTransaction(
			tx,
			operation.Operation.ID,
			operation.Revision,
			domain.OperationRunning,
			request.RunningPhase,
			request.At,
			nil,
		)
		if err != nil {
			return err
		}
		failed, err := transitionOperationInTransaction(
			tx,
			running.Operation.ID,
			running.Revision,
			domain.OperationFailed,
			request.FailurePhase,
			request.At,
			&request.Problem,
		)
		if err != nil {
			return err
		}
		nextProject := stoppedProjectProjection(project.Project, request.At)
		nextProject.State = domain.ProjectUnavailable
		persistedProject, err := persistLifecycleProject(tx, nextProject)
		if err != nil {
			return err
		}
		persistedSession, _, _, _, err := readExactUnresolvedProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(persistedSession, sessionRow) {
			return corruptStateError("project session", string(request.SessionID), fmt.Errorf("terminal quarantine changed unresolved process authority"))
		}
		result = ProjectRecoveryQuarantine{Operation: failed, Project: persistedProject}
		return nil
	})
	if err != nil {
		return ProjectRecoveryQuarantine{}, fmt.Errorf("quarantine project %q terminal session: %w", request.ProjectID, err)
	}
	return result, nil
}

// validateQuarantineTerminalProjectSessionRequest rejects recovery intent without exact project, session, operation, and problem fences.
func validateQuarantineTerminalProjectSessionRequest(request QuarantineTerminalProjectSessionRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected project revision", request.ExpectedProjectRevision, false); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	if err := request.Operation.Validate(); err != nil {
		return fmt.Errorf("terminal session quarantine operation: %w", err)
	}
	if request.Operation.Kind != domain.OperationKindProjectStart || request.Operation.ProjectID != request.ProjectID || request.Operation.State != domain.OperationQueued {
		return fmt.Errorf("terminal session quarantine requires a queued start operation for project %q", request.ProjectID)
	}
	if !request.Operation.RequestedAt.Equal(request.At) {
		return fmt.Errorf("terminal session quarantine operation time must equal quarantine time")
	}
	if strings.TrimSpace(request.RunningPhase) == "" || strings.TrimSpace(request.FailurePhase) == "" {
		return fmt.Errorf("terminal session quarantine phases are required")
	}
	if err := request.Problem.Validate(); err != nil {
		return err
	}
	return validateStoredTime("terminal session quarantine time", request.At)
}

// replayQuarantineTerminalProjectSession accepts only the exact three-edge recovery operation and retained unresolved session authority.
func replayQuarantineTerminalProjectSession(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request QuarantineTerminalProjectSessionRequest,
) (ProjectRecoveryQuarantine, error) {
	if len(history) != 3 ||
		history[0].State != domain.OperationQueued || history[0].Phase != string(domain.OperationQueued) || !history[0].OccurredAt.Equal(request.At) ||
		history[1].State != domain.OperationRunning || history[1].Phase != request.RunningPhase || !history[1].OccurredAt.Equal(request.At) || history[1].Problem != nil ||
		history[2].State != domain.OperationFailed || history[2].Phase != request.FailurePhase || !history[2].OccurredAt.Equal(request.At) || !operationProblemsEqual(history[2].Problem, &request.Problem) {
		return ProjectRecoveryQuarantine{}, fmt.Errorf("terminal session quarantine retry does not match the exact committed recovery operation")
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectRecoveryQuarantine{}, err
	}
	if !projectMatchesInactiveState(project.Project, domain.ProjectUnavailable, request.At) {
		return ProjectRecoveryQuarantine{}, fmt.Errorf("terminal session quarantine replay does not match the committed route-free projection")
	}
	_, owner, sessionState, generation, err := readExactUnresolvedProjectSession(tx, request.ProjectID, request.SessionID)
	if err != nil {
		return ProjectRecoveryQuarantine{}, err
	}
	if generation != request.ExpectedSessionGeneration || owner != domain.SessionOwnerHarbor || sessionState != domain.SessionAwaitingAttach {
		return ProjectRecoveryQuarantine{}, fmt.Errorf("terminal session quarantine replay does not match the retained unresolved session")
	}
	return ProjectRecoveryQuarantine{Operation: operation, Project: project}, nil
}

// readExactUnresolvedProjectSession accepts either complete process evidence or the legacy fully absent tuple without weakening partial-row validation.
func readExactUnresolvedProjectSession(
	tx *gorm.DB,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (models.ProjectSession, domain.SessionOwner, domain.SessionState, uint64, error) {
	var rows []models.ProjectSession
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return models.ProjectSession{}, "", "", 0, fmt.Errorf("read project session row: %w", err)
	}
	if len(rows) == 0 {
		return models.ProjectSession{}, "", "", 0, &ProjectSessionNotFoundError{ProjectID: projectID, SessionID: sessionID}
	}
	if len(rows) != 1 {
		return models.ProjectSession{}, "", "", 0, corruptStateError("project session", string(projectID), fmt.Errorf("project owns multiple active sessions"))
	}
	row := rows[0]
	session, err := projectSessionFromModel(row)
	if err == nil {
		if session.ProjectID != projectID || session.ID != sessionID {
			return models.ProjectSession{}, "", "", 0, &ProjectSessionNotFoundError{ProjectID: projectID, SessionID: sessionID}
		}
		return row, session.Owner, session.State, session.Generation, nil
	}
	var missing *ProjectSessionProcessEvidenceMissingError
	if !errors.As(err, &missing) {
		return models.ProjectSession{}, "", "", 0, err
	}
	if missing.ProjectID != projectID || missing.SessionID != sessionID {
		return models.ProjectSession{}, "", "", 0, &ProjectSessionNotFoundError{ProjectID: projectID, SessionID: sessionID}
	}
	return row, missing.Owner, missing.State, missing.Generation, nil
}

// readExactMissingProjectProcessEvidence recognizes only one fully absent exact-process tuple on an otherwise valid awaiting-attach session.
func readExactMissingProjectProcessEvidence(
	tx *gorm.DB,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (models.ProjectSession, *ProjectSessionProcessEvidenceMissingError, error) {
	var rows []models.ProjectSession
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return models.ProjectSession{}, nil, fmt.Errorf("read project session row: %w", err)
	}
	if len(rows) == 0 {
		return models.ProjectSession{}, nil, &ProjectSessionNotFoundError{ProjectID: projectID, SessionID: sessionID}
	}
	if len(rows) != 1 {
		return models.ProjectSession{}, nil, corruptStateError("project session", string(projectID), fmt.Errorf("project owns multiple active sessions"))
	}
	row := rows[0]
	_, err := projectSessionFromModel(row)
	var missing *ProjectSessionProcessEvidenceMissingError
	if !errors.As(err, &missing) {
		if err == nil {
			return models.ProjectSession{}, nil, fmt.Errorf("session %q retains exact process evidence", sessionID)
		}
		return models.ProjectSession{}, nil, err
	}
	if missing.ProjectID != projectID || missing.SessionID != sessionID {
		return models.ProjectSession{}, nil, &ProjectSessionNotFoundError{ProjectID: projectID, SessionID: sessionID}
	}
	return row, missing, nil
}
