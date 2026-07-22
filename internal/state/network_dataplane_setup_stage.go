package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const (
	networkDataPlaneSetupQueuedPhase   = "queued"
	networkDataPlaneSetupRunningPhase  = "preparing trusted ingress"
	networkDataPlaneSetupApprovalPhase = "awaiting trust approval"
)

// StageNetworkDataPlaneSetupRequest contains the exact resolver authority that may stage trusted-ingress approval.
type StageNetworkDataPlaneSetupRequest struct {
	// Operation is the queued global journal identity that owns this full-network transition.
	Operation domain.Operation
	// Projection is the exact current resolver predecessor reread before any journal edge is appended.
	Projection NetworkDataPlaneSetupProjection
	// Policy is the freshly reconstructed canonical host-network policy bound by Projection.
	Policy networkpolicy.Policy
}

// Validate rejects requests that do not describe one exact resolver-to-full approval boundary.
func (request StageNetworkDataPlaneSetupRequest) Validate() error {
	if err := request.Operation.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup operation: %w", err)
	}
	if request.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup {
		return fmt.Errorf("network data-plane setup operation kind must be %q", domain.OperationKindNetworkDataPlaneSetup)
	}
	if request.Operation.ProjectID != "" {
		return fmt.Errorf("network data-plane setup operation must be global")
	}
	if request.Operation.State != domain.OperationQueued {
		return fmt.Errorf("network data-plane setup operation must be queued")
	}
	if request.Operation.Phase != networkDataPlaneSetupQueuedPhase {
		return fmt.Errorf("network data-plane setup queued phase must be %q", networkDataPlaneSetupQueuedPhase)
	}
	if request.Projection.Stage != NetworkStageResolver {
		return fmt.Errorf(
			"network data-plane setup requires %q predecessor, found %q",
			NetworkStageResolver,
			request.Projection.Stage,
		)
	}
	if err := request.Projection.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup projection: %w", err)
	}
	if err := request.Policy.Validate(); err != nil {
		return fmt.Errorf("network data-plane setup policy: %w", err)
	}
	policyFingerprint, err := request.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("network data-plane setup policy fingerprint: %w", err)
	}
	if policyFingerprint != request.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint {
		return fmt.Errorf("network data-plane setup policy does not match confirmed ownership")
	}
	return nil
}

// StageNetworkDataPlaneSetup atomically stages trusted-ingress approval or exactly replays its fixed lifecycle.
func (journal *OperationJournal) StageNetworkDataPlaneSetup(
	ctx context.Context,
	request StageNetworkDataPlaneSetupRequest,
) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("stage network data-plane setup: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return OperationRecord{}, err
	}

	var result OperationRecord
	err := journal.mutations.mutate(ctx, "network data-plane setup staging", func(tx *gorm.DB) error {
		staged, stageErr := stageNetworkDataPlaneSetupInTransaction(tx, request)
		if stageErr != nil {
			return stageErr
		}
		result = staged
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("stage network data-plane setup: %w", err)
	}
	return result, nil
}

// stageNetworkDataPlaneSetupInTransaction binds the current resolver projection to one fixed approval lifecycle.
func stageNetworkDataPlaneSetupInTransaction(
	tx *gorm.DB,
	request StageNetworkDataPlaneSetupRequest,
) (OperationRecord, error) {
	if err := requireCurrentNetworkDataPlaneSetupProjection(tx, request); err != nil {
		return OperationRecord{}, err
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return OperationRecord{}, err
	}
	active, activeFound, err := findActiveNetworkDataPlaneSetupOperation(tx)
	if err != nil {
		return OperationRecord{}, err
	}

	existing, found, err := findOperationByIntent(tx, request.Operation.IntentID)
	if err != nil {
		return OperationRecord{}, err
	}
	if found {
		record, err := operationRecordFromModel(existing)
		if err != nil {
			return OperationRecord{}, err
		}
		if record.Operation.Kind != request.Operation.Kind || record.Operation.ProjectID != request.Operation.ProjectID {
			return OperationRecord{}, networkDataPlaneSetupIntentConflict(request.Operation, record.Operation)
		}
		return replayNetworkDataPlaneSetupInTransaction(tx, record, request)
	}

	existing, found, err = findOperationByID(tx, request.Operation.ID)
	if err != nil {
		return OperationRecord{}, err
	}
	if found {
		record, err := operationRecordFromModel(existing)
		if err != nil {
			return OperationRecord{}, err
		}
		return OperationRecord{}, &OperationIDConflictError{
			OperationID:       request.Operation.ID,
			ExistingIntentID:  record.Operation.IntentID,
			RequestedIntentID: request.Operation.IntentID,
		}
	}
	if activeFound {
		return OperationRecord{}, fmt.Errorf(
			"network data-plane setup operation %q is already active for intent %q",
			active.Operation.ID,
			active.Operation.IntentID,
		)
	}

	queued, err := insertQueuedNetworkDataPlaneSetupOperation(tx, request.Operation)
	if err != nil {
		return OperationRecord{}, err
	}
	running, err := transitionOperationInTransaction(
		tx,
		request.Operation.ID,
		queued.Revision,
		domain.OperationRunning,
		networkDataPlaneSetupRunningPhase,
		request.Operation.RequestedAt,
		nil,
	)
	if err != nil {
		return OperationRecord{}, err
	}
	approval, err := transitionOperationInTransaction(
		tx,
		request.Operation.ID,
		running.Revision,
		domain.OperationRequiresApproval,
		networkDataPlaneSetupApprovalPhase,
		request.Operation.RequestedAt,
		nil,
	)
	if err != nil {
		return OperationRecord{}, err
	}
	return replayNetworkDataPlaneSetupInTransaction(tx, approval, request)
}

// requireCurrentNetworkDataPlaneSetupProjection rejects any authority change since the caller's read.
func requireCurrentNetworkDataPlaneSetupProjection(
	tx *gorm.DB,
	request StageNetworkDataPlaneSetupRequest,
) error {
	policyFingerprint, err := request.Policy.Fingerprint()
	if err != nil {
		return err
	}
	current, err := resolveNetworkDataPlaneSetupProjection(tx, request.Policy, policyFingerprint)
	if err != nil {
		return err
	}
	if current.Stage != NetworkStageResolver {
		return fmt.Errorf(
			"network data-plane setup requires %q stage, found %q",
			NetworkStageResolver,
			current.Stage,
		)
	}
	if current.Stage != request.Projection.Stage {
		return fmt.Errorf(
			"network data-plane setup stage changed from %q to %q",
			request.Projection.Stage,
			current.Stage,
		)
	}
	if current.NetworkRevision != request.Projection.NetworkRevision {
		return &NetworkRevisionConflictError{
			Expected: request.Projection.NetworkRevision,
			Actual:   current.NetworkRevision,
		}
	}
	if !current.NetworkUpdatedAt.Equal(request.Projection.NetworkUpdatedAt) {
		return fmt.Errorf("network data-plane setup update time differs from the current projection")
	}
	if !sameNetworkDataPlaneSetupResolverProof(current.ResolverProof, request.Projection.ResolverProof) {
		return fmt.Errorf("network data-plane setup resolver proof differs from the current projection")
	}
	if current.ConfirmedOwnership != request.Projection.ConfirmedOwnership {
		return fmt.Errorf("network data-plane setup confirmed ownership differs from the current projection")
	}
	return nil
}

// sameNetworkDataPlaneSetupResolverProof compares every persisted proof field without relying on time representation identity.
func sameNetworkDataPlaneSetupResolverProof(left NetworkSetupProof, right NetworkSetupProof) bool {
	return left.Component == right.Component &&
		left.Evidence == right.Evidence &&
		left.Generation == right.Generation &&
		left.VerifiedAt.Equal(right.VerifiedAt)
}

// networkDataPlaneSetupIntentConflict preserves typed idempotency when one intent crosses operation boundaries.
func networkDataPlaneSetupIntentConflict(requested domain.Operation, existing domain.Operation) error {
	return &IntentConflictError{
		IntentID:            requested.IntentID,
		ExistingOperationID: existing.ID,
		ExistingKind:        existing.Kind,
		ExistingProjectID:   existing.ProjectID,
		RequestedKind:       requested.Kind,
		RequestedProjectID:  requested.ProjectID,
	}
}

// replayNetworkDataPlaneSetupInTransaction accepts only the request that produced the exact fixed staging history.
func replayNetworkDataPlaneSetupInTransaction(
	tx *gorm.DB,
	record OperationRecord,
	request StageNetworkDataPlaneSetupRequest,
) (OperationRecord, error) {
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := requireExactNetworkDataPlaneSetupHistory(record, history); err != nil {
		return OperationRecord{}, err
	}
	if err := requireExactNetworkDataPlaneSetupOperation(record, request.Operation); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// requireExactNetworkDataPlaneSetupOperation rejects retries that reuse an intent for a different operation request.
func requireExactNetworkDataPlaneSetupOperation(record OperationRecord, requested domain.Operation) error {
	stored := record.Operation
	if stored.ID != requested.ID ||
		stored.IntentID != requested.IntentID ||
		stored.Kind != requested.Kind ||
		stored.ProjectID != requested.ProjectID ||
		!stored.RequestedAt.Equal(requested.RequestedAt) {
		return fmt.Errorf("network data-plane setup intent %q differs from its exact staged operation", requested.IntentID)
	}
	return nil
}

// requireExactNetworkDataPlaneSetupHistory proves a replay has exactly the three staging edges produced here.
func requireExactNetworkDataPlaneSetupHistory(record OperationRecord, history []OperationTransition) error {
	if len(history) != 3 {
		return corruptNetworkDataPlaneSetupOperation(
			record.Operation.ID,
			fmt.Errorf("operation has %d transitions, expected 3", len(history)),
		)
	}
	expectedStates := [...]domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationRequiresApproval,
	}
	expectedPhases := [...]string{
		networkDataPlaneSetupQueuedPhase,
		networkDataPlaneSetupRunningPhase,
		networkDataPlaneSetupApprovalPhase,
	}
	for index := range history {
		transition := history[index]
		if transition.State != expectedStates[index] ||
			transition.Phase != expectedPhases[index] ||
			!transition.OccurredAt.Equal(record.Operation.RequestedAt) {
			return corruptNetworkDataPlaneSetupOperation(
				record.Operation.ID,
				fmt.Errorf("transition %d differs from the staged lifecycle", index+1),
			)
		}
		if index > 0 && transition.Sequence != history[index-1].Sequence+1 {
			return corruptNetworkDataPlaneSetupOperation(
				record.Operation.ID,
				fmt.Errorf("operation revisions are not contiguous"),
			)
		}
	}
	if record.Operation.State != domain.OperationRequiresApproval ||
		record.Operation.Phase != networkDataPlaneSetupApprovalPhase ||
		record.Revision != history[len(history)-1].Sequence {
		return corruptNetworkDataPlaneSetupOperation(
			record.Operation.ID,
			fmt.Errorf("operation does not match its approval transition"),
		)
	}
	return nil
}

// findActiveNetworkDataPlaneSetupOperation detects a foreign global trusted-ingress owner before the storage guard does.
func findActiveNetworkDataPlaneSetupOperation(tx *gorm.DB) (OperationRecord, bool, error) {
	var rows []models.Operation
	if err := tx.
		Where("kind = ? AND project_id IS NULL AND state IN ?", domain.OperationKindNetworkDataPlaneSetup, []domain.OperationState{
			domain.OperationQueued,
			domain.OperationRunning,
			domain.OperationRequiresApproval,
		}).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, false, fmt.Errorf("read active network data-plane setup operation: %w", err)
	}
	if len(rows) > 1 {
		return OperationRecord{}, false, corruptStateError(
			"network data-plane setup operation",
			"global",
			fmt.Errorf("found %d active operations, expected at most 1", len(rows)),
		)
	}
	if len(rows) == 0 {
		return OperationRecord{}, false, nil
	}
	record, err := operationRecordFromModel(rows[0])
	if err != nil {
		return OperationRecord{}, false, err
	}
	return record, true, nil
}

// insertQueuedNetworkDataPlaneSetupOperation appends the first fixed lifecycle edge in the caller's transaction.
func insertQueuedNetworkDataPlaneSetupOperation(
	tx *gorm.DB,
	operation domain.Operation,
) (OperationRecord, error) {
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return OperationRecord{}, err
	}
	row, err := operationModelFromDomain(operation, sequence)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&row).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("create network data-plane setup operation: %w", err)
	}
	transition, err := operationTransitionModelFromDomain(OperationTransition{
		OperationID: operation.ID,
		Ordinal:     1,
		State:       domain.OperationQueued,
		Phase:       operation.Phase,
		OccurredAt:  operation.RequestedAt,
		Sequence:    sequence,
	})
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&transition).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("append queued network data-plane setup transition: %w", err)
	}
	return OperationRecord{Operation: operation, Revision: sequence}, nil
}

// corruptNetworkDataPlaneSetupOperation identifies fixed-lifecycle corruption without implying a retained plan exists.
func corruptNetworkDataPlaneSetupOperation(operationID domain.OperationID, cause error) error {
	return corruptStateError("network data-plane setup operation", string(operationID), cause)
}
