package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"gorm.io/gorm"
)

// RetainedProjectRuntimeRepairBoundary captures every durable fact that must remain exact while a legacy runtime is inspected.
type RetainedProjectRuntimeRepairBoundary struct {
	Project                ProjectRecord
	SessionID              domain.SessionID
	SessionGeneration      uint64
	SessionUpdatedAt       time.Time
	RecoveryOperation      OperationRecord
	NetworkRevision        domain.Sequence
	NetworkUpdatedAt       time.Time
	PrimaryLease           identity.Lease
	PrimaryLeaseGeneration uint64
}

// Validate reports whether the boundary identifies one quarantined missing-evidence runtime and its current primary address authority.
func (boundary RetainedProjectRuntimeRepairBoundary) Validate() error {
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
	if err := validateRetainedProjectRuntimeRecoveryOperation(boundary.Project, boundary.RecoveryOperation); err != nil {
		return err
	}
	return nil
}

// validateProjectRuntimeRepairBoundaryAuthority validates the durable identity, lifecycle, network, and lease fences shared by repair boundaries.
func validateProjectRuntimeRepairBoundaryAuthority(
	project ProjectRecord,
	sessionID domain.SessionID,
	sessionGeneration uint64,
	sessionUpdatedAt time.Time,
	recoveryOperation OperationRecord,
	networkRevision domain.Sequence,
	networkUpdatedAt time.Time,
	primaryLease identity.Lease,
	primaryLeaseGeneration uint64,
) error {
	if err := project.Validate(); err != nil {
		return err
	}
	if project.Project.State != domain.ProjectUnavailable {
		return fmt.Errorf("repair project must be unavailable")
	}
	if err := sessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("repair session generation", sessionGeneration, false); err != nil {
		return err
	}
	if err := validateStoredTime("repair session update time", sessionUpdatedAt); err != nil {
		return err
	}
	if sessionUpdatedAt.After(project.Project.UpdatedAt) {
		return fmt.Errorf("repair session update time must not follow project quarantine")
	}
	if _, err := sequenceToModelInt("repair network revision", networkRevision, false); err != nil {
		return err
	}
	if err := validateStoredTime("repair network update time", networkUpdatedAt); err != nil {
		return err
	}
	if err := primaryLease.Validate(); err != nil {
		return err
	}
	if primaryLease.Key.ProjectID != project.Project.ID || primaryLease.Key.Kind() != identity.LeaseKindPrimary {
		return fmt.Errorf("repair primary lease must belong to project %q", project.Project.ID)
	}
	if _, err := unsignedToModelInt("repair primary lease generation", primaryLeaseGeneration, false); err != nil {
		return err
	}
	return recoveryOperation.Operation.Validate()
}

// CompleteRetainedProjectRuntimeRepairRequest fences the durable authority that was revalidated immediately before finalization.
type CompleteRetainedProjectRuntimeRepairRequest struct {
	ProjectID                         domain.ProjectID
	ExpectedProjectRevision           domain.Sequence
	SessionID                         domain.SessionID
	ExpectedSessionGeneration         uint64
	ExpectedSessionUpdatedAt          time.Time
	ExpectedRecoveryOperationID       domain.OperationID
	ExpectedRecoveryOperationRevision domain.Sequence
	ExpectedNetworkRevision           domain.Sequence
	ExpectedNetworkUpdatedAt          time.Time
	ExpectedPrimaryLease              identity.Lease
	ExpectedPrimaryLeaseGeneration    uint64
	At                                time.Time
}

// RetainedProjectRuntimeRepairBoundary returns one transactionally consistent repair target without interpreting host process ownership.
func (store *Store) RetainedProjectRuntimeRepairBoundary(
	ctx context.Context,
	projectID domain.ProjectID,
) (RetainedProjectRuntimeRepairBoundary, error) {
	ctx = normalizeContext(ctx)
	if err := projectID.Validate(); err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, err
	}
	if err := ctx.Err(); err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, err
	}
	builder, err := store.projects.WithContext(ctx).Builder()
	if err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, fmt.Errorf("open retained project runtime repair boundary: %w", err)
	}

	var boundary RetainedProjectRuntimeRepairBoundary
	err = builder.Transaction(func(tx *gorm.DB) error {
		read, readErr := readRetainedProjectRuntimeRepairBoundary(tx, projectID)
		if readErr != nil {
			return readErr
		}
		boundary = read
		return nil
	})
	if err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, fmt.Errorf("read project %q retained runtime repair boundary: %w", projectID, err)
	}
	return boundary, nil
}

// CompleteRetainedProjectRuntimeRepair retires only the exact missing-evidence row after every durable inspection fence still matches.
func (store *Store) CompleteRetainedProjectRuntimeRepair(
	ctx context.Context,
	request CompleteRetainedProjectRuntimeRepairRequest,
) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := validateCompleteRetainedProjectRuntimeRepairRequest(request); err != nil {
		return ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}

	var result ProjectRecord
	err := store.mutations.mutate(ctx, "complete retained project runtime repair", func(tx *gorm.DB) error {
		if err := requireNoCompetingProjectOperation(tx, request.ProjectID, ""); err != nil {
			return err
		}
		boundary, err := readRetainedProjectRuntimeRepairBoundary(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if err := validateRetainedProjectRuntimeRepairFences(boundary, request); err != nil {
			return err
		}
		if request.At.Before(boundary.Project.Project.UpdatedAt) ||
			request.At.Before(boundary.SessionUpdatedAt) ||
			request.At.Before(boundary.NetworkUpdatedAt) {
			return fmt.Errorf("retained runtime repair time precedes a durable inspection fence")
		}
		if err := deleteExactMissingEvidenceProjectSession(tx, request); err != nil {
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
		return ProjectRecord{}, fmt.Errorf("complete project %q retained runtime repair: %w", request.ProjectID, err)
	}
	return result, nil
}

// readRetainedProjectRuntimeRepairBoundary captures exact project, session, marker, and network authority in the caller's transaction.
func readRetainedProjectRuntimeRepairBoundary(
	tx *gorm.DB,
	projectID domain.ProjectID,
) (RetainedProjectRuntimeRepairBoundary, error) {
	authority, err := readProjectRuntimeRepairDurableAuthority(tx, projectID, projectRuntimeRepairSessionMissing)
	if err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, err
	}
	if err := validateRetainedProjectRuntimeRecoveryOperation(authority.Project, authority.RecoveryOperation); err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, err
	}
	if err := validateRetainedProjectRuntimeRecoveryHistory(authority.Project, authority.RecoveryOperation, authority.RecoveryHistory); err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, err
	}
	boundary := RetainedProjectRuntimeRepairBoundary{
		Project:                authority.Project,
		SessionID:              authority.SessionID,
		SessionGeneration:      authority.SessionGeneration,
		SessionUpdatedAt:       authority.SessionUpdatedAt,
		RecoveryOperation:      authority.RecoveryOperation,
		NetworkRevision:        authority.NetworkRevision,
		NetworkUpdatedAt:       authority.NetworkUpdatedAt,
		PrimaryLease:           authority.PrimaryLease,
		PrimaryLeaseGeneration: authority.PrimaryLeaseGeneration,
	}
	if err := boundary.Validate(); err != nil {
		return RetainedProjectRuntimeRepairBoundary{}, corruptStateError("retained project runtime repair", string(projectID), err)
	}
	return boundary, nil
}

type projectRuntimeRepairSessionMode uint8

const (
	projectRuntimeRepairSessionMissing projectRuntimeRepairSessionMode = iota + 1
	projectRuntimeRepairSessionProcessBacked
)

type projectRuntimeRepairDurableAuthority struct {
	Project                ProjectRecord
	SessionID              domain.SessionID
	SessionGeneration      uint64
	SessionUpdatedAt       time.Time
	RecoveryOperation      OperationRecord
	RecoveryHistory        []OperationTransition
	NetworkRevision        domain.Sequence
	NetworkUpdatedAt       time.Time
	PrimaryLease           identity.Lease
	PrimaryLeaseGeneration uint64
	Process                *domain.ProcessEvidence
}

// readProjectRuntimeRepairDurableAuthority reads one process-shaped repair fence without treating SQLite as host-process health.
func readProjectRuntimeRepairDurableAuthority(
	tx *gorm.DB,
	projectID domain.ProjectID,
	mode projectRuntimeRepairSessionMode,
) (projectRuntimeRepairDurableAuthority, error) {
	if err := requireNoCompetingProjectOperation(tx, projectID, ""); err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	highWater, err := readSnapshotSequence(tx)
	if err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	project, err := readLifecycleProject(tx, projectID)
	if err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	if project.Project.State != domain.ProjectUnavailable {
		return projectRuntimeRepairDurableAuthority{}, fmt.Errorf("project %q must be unavailable before runtime repair, got %q", projectID, project.Project.State)
	}
	if err := validateVisibleSequence(highWater, project.Revision, "runtime repair project", nil); err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	if err := validateProjectSequenceOwner(tx, project); err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	sessionRow, sessionID, generation, process, err := readProjectRuntimeRepairSession(tx, projectID, mode)
	if err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	operation, history, err := readRetainedProjectRuntimeRecoveryOperation(tx, projectID)
	if err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	if err := validateVisibleSequence(highWater, operation.Revision, "runtime recovery operation", nil); err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	for _, transition := range history {
		if err := validateVisibleSequence(highWater, transition.Sequence, "runtime recovery transition", nil); err != nil {
			return projectRuntimeRepairDurableAuthority{}, err
		}
	}
	if err := validateOperationHistorySequenceOwners(tx, operation, history); err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	network, lease, leaseGeneration, err := readProjectRuntimeNetworkBoundary(tx, projectID, "runtime repair")
	if err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	if err := validateVisibleSequence(highWater, network.Revision, "runtime repair network", nil); err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	if err := validateNetworkSequenceExclusivity(tx, network.Revision); err != nil {
		return projectRuntimeRepairDurableAuthority{}, err
	}
	return projectRuntimeRepairDurableAuthority{
		Project: project, SessionID: sessionID, SessionGeneration: generation, SessionUpdatedAt: sessionRow.UpdatedAt,
		RecoveryOperation: operation, RecoveryHistory: history, NetworkRevision: network.Revision, NetworkUpdatedAt: network.UpdatedAt,
		PrimaryLease: lease, PrimaryLeaseGeneration: leaseGeneration, Process: process,
	}, nil
}

// readProjectRuntimeRepairSession accepts only one Harbor-owned awaiting-attach session in the selected evidence shape.
func readProjectRuntimeRepairSession(
	tx *gorm.DB,
	projectID domain.ProjectID,
	mode projectRuntimeRepairSessionMode,
) (models.ProjectSession, domain.SessionID, uint64, *domain.ProcessEvidence, error) {
	var rows []models.ProjectSession
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return models.ProjectSession{}, "", 0, nil, fmt.Errorf("read project runtime repair session row: %w", err)
	}
	if len(rows) == 0 {
		return models.ProjectSession{}, "", 0, nil, &ProjectSessionNotFoundError{ProjectID: projectID}
	}
	if len(rows) != 1 {
		return models.ProjectSession{}, "", 0, nil, corruptStateError("project session", string(projectID), fmt.Errorf("project owns multiple active sessions"))
	}
	row := rows[0]
	sessionID := domain.SessionID(row.SessionId)
	sessionRow, owner, sessionState, generation, readErr := readExactUnresolvedProjectSession(tx, projectID, sessionID)
	var missing *ProjectSessionProcessEvidenceMissingError
	if readErr != nil && !errors.As(readErr, &missing) {
		return models.ProjectSession{}, "", 0, nil, readErr
	}
	if readErr != nil && mode == projectRuntimeRepairSessionProcessBacked {
		return models.ProjectSession{}, "", 0, nil, readErr
	}
	if readErr == nil {
		process, processErr := processEvidenceFromModel(sessionRow, durableKey(sessionRow.SessionId, sessionRow.Id))
		if processErr != nil {
			return models.ProjectSession{}, "", 0, nil, processErr
		}
		if mode == projectRuntimeRepairSessionMissing && process != nil {
			return models.ProjectSession{}, "", 0, nil, fmt.Errorf("session %q retains exact process evidence", sessionID)
		}
		if mode == projectRuntimeRepairSessionProcessBacked && process == nil {
			return models.ProjectSession{}, "", 0, nil, &ProjectSessionProcessEvidenceMissingError{
				ProjectID: projectID, SessionID: sessionID, Owner: owner, State: sessionState, Generation: generation, UpdatedAt: sessionRow.UpdatedAt,
			}
		}
		if owner != domain.SessionOwnerHarbor || !isProcessBackedProjectRuntimeRepairSessionState(sessionState) {
			return models.ProjectSession{}, "", 0, nil, fmt.Errorf("session %q is not a Harbor-owned process-backed repair boundary", sessionID)
		}
		return sessionRow, sessionID, generation, process, nil
	}
	if missing.Owner != domain.SessionOwnerHarbor || missing.State != domain.SessionAwaitingAttach {
		return models.ProjectSession{}, "", 0, nil, fmt.Errorf("session %q is not a Harbor-owned awaiting-attach repair boundary", missing.SessionID)
	}
	return row, missing.SessionID, missing.Generation, nil, nil
}

// isProcessBackedProjectRuntimeRepairSessionState accepts the launch and stopping states whose complete process receipt remains actionable.
func isProcessBackedProjectRuntimeRepairSessionState(sessionState domain.SessionState) bool {
	return sessionState == domain.SessionAwaitingAttach || sessionState == domain.SessionStopping
}

// readOnlyMissingEvidenceProjectSession rejects complete, partial, absent, or multiply-owned session authority.
func readOnlyMissingEvidenceProjectSession(
	tx *gorm.DB,
	projectID domain.ProjectID,
) (models.ProjectSession, *ProjectSessionProcessEvidenceMissingError, error) {
	var rows []models.ProjectSession
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return models.ProjectSession{}, nil, fmt.Errorf("read retained project session row: %w", err)
	}
	if len(rows) == 0 {
		return models.ProjectSession{}, nil, &ProjectSessionNotFoundError{ProjectID: projectID}
	}
	if len(rows) != 1 {
		return models.ProjectSession{}, nil, corruptStateError("project session", string(projectID), fmt.Errorf("project owns multiple active sessions"))
	}
	return readExactMissingProjectProcessEvidence(tx, projectID, domain.SessionID(rows[0].SessionId))
}

// readRetainedProjectRuntimeRecoveryOperation returns the latest lifecycle marker and its validated append-only history.
func readRetainedProjectRuntimeRecoveryOperation(
	tx *gorm.DB,
	projectID domain.ProjectID,
) (OperationRecord, []OperationTransition, error) {
	var rows []models.Operation
	if err := tx.
		Where("project_id = ?", string(projectID)).
		Where("kind IN ?", []string{
			string(domain.OperationKindProjectStart),
			string(domain.OperationKindProjectStop),
			string(domain.OperationKindProjectRestart),
			string(domain.OperationKindProjectUnregister),
		}).
		Order("revision DESC").
		Order("id DESC").
		Limit(1).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, nil, fmt.Errorf("read retained runtime recovery operation: %w", err)
	}
	if len(rows) == 0 {
		return OperationRecord{}, nil, &ProjectLifecycleOperationNotFoundError{ProjectID: projectID}
	}
	record, err := operationRecordFromModel(rows[0])
	if err != nil {
		return OperationRecord{}, nil, err
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	return record, history, nil
}

// validateRetainedProjectRuntimeRecoveryOperation recognizes only the durable marker that quarantined this exact project.
func validateRetainedProjectRuntimeRecoveryOperation(project ProjectRecord, operation OperationRecord) error {
	if _, err := sequenceToModelInt("repair recovery operation revision", operation.Revision, false); err != nil {
		return err
	}
	marker := operation.Operation
	if err := marker.Validate(); err != nil {
		return err
	}
	if marker.ProjectID != project.Project.ID ||
		marker.Kind != domain.OperationKindProjectStart ||
		marker.State != domain.OperationFailed ||
		marker.Phase != domain.ProjectRecoveryRequiredPhase ||
		marker.Problem == nil ||
		marker.Problem.Code != domain.ProjectRecoveryAmbiguousLaunchProblemCode ||
		marker.Problem.Retryable {
		return fmt.Errorf("project %q does not own an exact retained-runtime recovery marker", project.Project.ID)
	}
	if marker.FinishedAt == nil || !marker.FinishedAt.Equal(project.Project.UpdatedAt) {
		return fmt.Errorf("project %q recovery marker does not match its quarantine projection", project.Project.ID)
	}
	if operation.Revision == domain.MaximumSequence || project.Revision != operation.Revision+1 {
		return fmt.Errorf("project %q recovery marker does not immediately precede its quarantine projection", project.Project.ID)
	}
	return nil
}

// validateRetainedProjectRuntimeRecoveryHistory rejects marker lookalikes that were not committed by the three-edge quarantine mutation.
func validateRetainedProjectRuntimeRecoveryHistory(
	project ProjectRecord,
	operation OperationRecord,
	history []OperationTransition,
) error {
	if len(history) != 3 ||
		history[0].State != domain.OperationQueued ||
		history[0].Phase != string(domain.OperationQueued) ||
		history[1].State != domain.OperationRunning ||
		history[1].Phase != domain.ProjectRecoveryIsolationPhase ||
		history[2].State != domain.OperationFailed ||
		history[0].Sequence == domain.MaximumSequence ||
		history[1].Sequence != history[0].Sequence+1 ||
		history[1].Sequence == domain.MaximumSequence ||
		history[2].Sequence != history[1].Sequence+1 ||
		history[2].Sequence == domain.MaximumSequence ||
		project.Revision != history[2].Sequence+1 ||
		!history[0].OccurredAt.Equal(project.Project.UpdatedAt) ||
		!history[1].OccurredAt.Equal(project.Project.UpdatedAt) ||
		!history[2].OccurredAt.Equal(project.Project.UpdatedAt) ||
		history[2].Phase != domain.ProjectRecoveryRequiredPhase ||
		!operationProblemsEqual(history[2].Problem, operation.Operation.Problem) {
		return fmt.Errorf("project %q recovery marker does not have the exact quarantine history", project.Project.ID)
	}
	return nil
}

// readProjectRuntimeNetworkBoundary validates the aggregate before returning its one raw-generation primary lease.
func readProjectRuntimeNetworkBoundary(
	tx *gorm.DB,
	projectID domain.ProjectID,
	purpose string,
) (NetworkRecord, identity.Lease, uint64, error) {
	present, err := inspectNetworkSchema(tx)
	if err != nil {
		return NetworkRecord{}, identity.Lease{}, 0, err
	}
	if !present {
		return NetworkRecord{}, identity.Lease{}, 0, fmt.Errorf("%s requires initialized network state", purpose)
	}
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return NetworkRecord{}, identity.Lease{}, 0, err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return NetworkRecord{}, identity.Lease{}, 0, err
	}
	if !initialized {
		return NetworkRecord{}, identity.Lease{}, 0, fmt.Errorf("%s requires initialized network state", purpose)
	}

	matches := make([]models.LoopbackAddressLease, 0, 1)
	for _, row := range rows.Leases {
		if row.State == "leased" &&
			row.ProjectId.Valid &&
			row.ProjectId.String == string(projectID) &&
			row.SourceProjectId == string(projectID) &&
			row.Kind == string(identity.LeaseKindPrimary) {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		return NetworkRecord{}, identity.Lease{}, 0, corruptStateError(
			"loopback address lease",
			string(projectID),
			fmt.Errorf("%s requires one active primary lease, found %d", purpose, len(matches)),
		)
	}
	lease, err := helperApprovalLeaseFromActiveRow(matches[0])
	if err != nil {
		return NetworkRecord{}, identity.Lease{}, 0, err
	}
	if lease.Key.Kind() != identity.LeaseKindPrimary {
		return NetworkRecord{}, identity.Lease{}, 0, corruptStateError(
			"loopback address lease",
			string(projectID),
			fmt.Errorf("active %s lease is not the project primary", purpose),
		)
	}
	generation, err := positiveNetworkGeneration(purpose+" primary lease generation", matches[0].LeaseGeneration)
	if err != nil {
		return NetworkRecord{}, identity.Lease{}, 0, corruptStateError("loopback address lease", string(projectID), err)
	}
	return network, lease, generation, nil
}

// validateCompleteRetainedProjectRuntimeRepairRequest rejects finalization without every server-derived inspection fence.
func validateCompleteRetainedProjectRuntimeRepairRequest(request CompleteRetainedProjectRuntimeRepairRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected repair project revision", request.ExpectedProjectRevision, false); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected repair session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	if err := validateStoredTime("expected repair session update time", request.ExpectedSessionUpdatedAt); err != nil {
		return err
	}
	if err := request.ExpectedRecoveryOperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected recovery operation revision", request.ExpectedRecoveryOperationRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected repair network revision", request.ExpectedNetworkRevision, false); err != nil {
		return err
	}
	if err := validateStoredTime("expected repair network update time", request.ExpectedNetworkUpdatedAt); err != nil {
		return err
	}
	if err := request.ExpectedPrimaryLease.Validate(); err != nil {
		return err
	}
	if request.ExpectedPrimaryLease.Key.ProjectID != request.ProjectID || request.ExpectedPrimaryLease.Key.Kind() != identity.LeaseKindPrimary {
		return fmt.Errorf("expected repair primary lease must belong to project %q", request.ProjectID)
	}
	if _, err := unsignedToModelInt("expected repair primary lease generation", request.ExpectedPrimaryLeaseGeneration, false); err != nil {
		return err
	}
	return validateStoredTime("retained runtime repair completion time", request.At)
}

// validateRetainedProjectRuntimeRepairFences proves finalization still targets the exact boundary inspected by the caller.
func validateRetainedProjectRuntimeRepairFences(
	boundary RetainedProjectRuntimeRepairBoundary,
	request CompleteRetainedProjectRuntimeRepairRequest,
) error {
	if boundary.Project.Revision != request.ExpectedProjectRevision {
		return &ProjectRevisionConflictError{
			ProjectID: request.ProjectID,
			Expected:  request.ExpectedProjectRevision,
			Actual:    boundary.Project.Revision,
		}
	}
	if boundary.SessionID != request.SessionID {
		return fmt.Errorf("project %q retained session is %q, not inspected session %q", request.ProjectID, boundary.SessionID, request.SessionID)
	}
	if boundary.SessionGeneration != request.ExpectedSessionGeneration {
		return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, boundary.SessionGeneration)
	}
	if !boundary.SessionUpdatedAt.Equal(request.ExpectedSessionUpdatedAt) {
		return fmt.Errorf("project %q retained session update time no longer matches the inspected boundary", request.ProjectID)
	}
	if boundary.RecoveryOperation.Operation.ID != request.ExpectedRecoveryOperationID {
		return fmt.Errorf(
			"project %q recovery operation is %q, not inspected operation %q",
			request.ProjectID,
			boundary.RecoveryOperation.Operation.ID,
			request.ExpectedRecoveryOperationID,
		)
	}
	if boundary.RecoveryOperation.Revision != request.ExpectedRecoveryOperationRevision {
		return &StaleRevisionError{
			OperationID: request.ExpectedRecoveryOperationID,
			Expected:    request.ExpectedRecoveryOperationRevision,
			Actual:      boundary.RecoveryOperation.Revision,
		}
	}
	if boundary.NetworkRevision != request.ExpectedNetworkRevision {
		return &NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: boundary.NetworkRevision}
	}
	if !boundary.NetworkUpdatedAt.Equal(request.ExpectedNetworkUpdatedAt) {
		return fmt.Errorf("project %q network update time no longer matches the inspected boundary", request.ProjectID)
	}
	if boundary.PrimaryLease != request.ExpectedPrimaryLease || boundary.PrimaryLeaseGeneration != request.ExpectedPrimaryLeaseGeneration {
		return fmt.Errorf("project %q primary lease no longer matches the inspected runtime boundary", request.ProjectID)
	}
	return nil
}

// deleteExactMissingEvidenceProjectSession removes no row if process evidence or any session authority changed after inspection.
func deleteExactMissingEvidenceProjectSession(
	tx *gorm.DB,
	request CompleteRetainedProjectRuntimeRepairRequest,
) error {
	deleted := tx.Where(
		`session_id = ? AND project_id = ? AND generation = ? AND owner = ? AND state = ?
		 AND pid IS NULL AND birth_token IS NULL AND executable_identity IS NULL AND argument_digest IS NULL`,
		string(request.SessionID),
		string(request.ProjectID),
		int(request.ExpectedSessionGeneration),
		string(domain.SessionOwnerHarbor),
		string(domain.SessionAwaitingAttach),
	).Delete(&models.ProjectSession{})
	if deleted.Error != nil {
		return fmt.Errorf("delete retained project session: %w", deleted.Error)
	}
	if deleted.RowsAffected != 1 {
		_, missing, err := readOnlyMissingEvidenceProjectSession(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if missing.SessionID != request.SessionID {
			return &ProjectSessionNotFoundError{ProjectID: request.ProjectID, SessionID: request.SessionID}
		}
		return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, missing.Generation)
	}
	return nil
}
