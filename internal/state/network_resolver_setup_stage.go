package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const (
	networkResolverSetupQueuedPhase   = "queued"
	networkResolverSetupRunningPhase  = "preparing resolver"
	networkResolverSetupApprovalPhase = "awaiting resolver approval"
)

// StageNetworkResolverSetupRequest contains the complete immutable authority needed to stage resolver approval.
type StageNetworkResolverSetupRequest struct {
	// Operation is the queued global journal identity that owns this resolver transition.
	Operation domain.Operation
	// ExpectedNetworkRevision binds approval to one exact identity-stage network root.
	ExpectedNetworkRevision domain.Sequence
	// ExpectedSourceOwnershipFingerprint binds approval to the exact schema-one host record being upgraded.
	ExpectedSourceOwnershipFingerprint string
	// TargetOwnership is the complete schema-two host record the helper must establish.
	TargetOwnership ownership.Record
	// Policy is the complete canonical host resolver, low-port, and trust policy.
	Policy networkpolicy.Policy
}

// Validate rejects resolver setup requests that do not describe one exact schema-one to schema-two transition.
func (request StageNetworkResolverSetupRequest) Validate() error {
	if err := request.Operation.Validate(); err != nil {
		return fmt.Errorf("network resolver setup operation: %w", err)
	}
	if request.Operation.Kind != domain.OperationKindNetworkResolverSetup {
		return fmt.Errorf("network resolver setup operation kind must be %q", domain.OperationKindNetworkResolverSetup)
	}
	if request.Operation.ProjectID != "" {
		return fmt.Errorf("network resolver setup operation must be global")
	}
	if request.Operation.State != domain.OperationQueued {
		return fmt.Errorf("network resolver setup operation must be queued")
	}
	if request.Operation.Phase != networkResolverSetupQueuedPhase {
		return fmt.Errorf("network resolver setup queued phase must be %q", networkResolverSetupQueuedPhase)
	}
	if _, err := sequenceToModelInt("network resolver setup expected network revision", request.ExpectedNetworkRevision, false); err != nil {
		return err
	}
	if request.TargetOwnership.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return fmt.Errorf(
			"network resolver setup target ownership schema is %d, want %d",
			request.TargetOwnership.SchemaVersion,
			ownership.NetworkPolicySchemaVersion,
		)
	}
	if err := request.TargetOwnership.Validate(); err != nil {
		return fmt.Errorf("network resolver setup target ownership: %w", err)
	}
	if err := request.Policy.Validate(); err != nil {
		return fmt.Errorf("network resolver setup policy: %w", err)
	}
	policyFingerprint, err := request.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("network resolver setup policy fingerprint: %w", err)
	}
	if policyFingerprint != request.TargetOwnership.NetworkPolicyFingerprint {
		return fmt.Errorf("network resolver setup policy does not match target ownership")
	}
	_, sourceFingerprint, err := resolverSetupSourceOwnership(request.TargetOwnership)
	if err != nil {
		return err
	}
	if request.ExpectedSourceOwnershipFingerprint != sourceFingerprint {
		return fmt.Errorf("network resolver setup source ownership fingerprint does not match its target-derived schema-one record")
	}
	return nil
}

// StageNetworkResolverSetup atomically stages a resolver approval plan or exactly replays its durable intent.
func (journal *OperationJournal) StageNetworkResolverSetup(
	ctx context.Context,
	request StageNetworkResolverSetupRequest,
) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("stage network resolver setup: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return OperationRecord{}, err
	}

	var result OperationRecord
	err := journal.mutations.mutate(ctx, "network resolver setup staging", func(tx *gorm.DB) error {
		staged, err := stageNetworkResolverSetupInTransaction(tx, request)
		if err != nil {
			return err
		}
		result = staged
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("stage network resolver setup: %w", err)
	}
	return result, nil
}

// stageNetworkResolverSetupInTransaction binds lifecycle, identity authority, and policy in one writer instant.
func stageNetworkResolverSetupInTransaction(
	tx *gorm.DB,
	request StageNetworkResolverSetupRequest,
) (OperationRecord, error) {
	if err := requireNetworkResolverSetupAuthority(tx, request); err != nil {
		return OperationRecord{}, err
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return OperationRecord{}, err
	}

	planRow, planFound, err := readOptionalNetworkResolverSetupPlanForStaging(tx, request.Operation.ID)
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
			return OperationRecord{}, networkResolverSetupIntentConflict(request.Operation, record.Operation)
		}
		if !planFound {
			return OperationRecord{}, corruptNetworkResolverSetupPlan(
				record.Operation.ID,
				fmt.Errorf("operation exists without its singleton plan"),
			)
		}
		return replayNetworkResolverSetupInTransaction(tx, record, planRow, request)
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
	if planFound {
		return OperationRecord{}, corruptNetworkResolverSetupPlan(
			request.Operation.ID,
			fmt.Errorf("singleton plan already belongs to operation %q", planRow.OperationId),
		)
	}
	if active, activeFound, err := findActiveNetworkResolverSetupOperation(tx); err != nil {
		return OperationRecord{}, err
	} else if activeFound {
		return OperationRecord{}, fmt.Errorf(
			"network resolver setup operation %q is already active for intent %q",
			active.Operation.ID,
			active.Operation.IntentID,
		)
	}

	queued, err := insertQueuedNetworkResolverSetupOperation(tx, request.Operation)
	if err != nil {
		return OperationRecord{}, err
	}
	running, err := transitionOperationInTransaction(
		tx,
		request.Operation.ID,
		queued.Revision,
		domain.OperationRunning,
		networkResolverSetupRunningPhase,
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
		networkResolverSetupApprovalPhase,
		request.Operation.RequestedAt,
		nil,
	)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := insertNetworkResolverSetupPlan(tx, approval, request); err != nil {
		return OperationRecord{}, err
	}
	planRow, planFound, err = readOptionalNetworkResolverSetupPlanForStaging(tx, request.Operation.ID)
	if err != nil {
		return OperationRecord{}, err
	}
	if !planFound {
		return OperationRecord{}, corruptNetworkResolverSetupPlan(
			request.Operation.ID,
			fmt.Errorf("singleton plan is missing after insert"),
		)
	}
	if err := requireExactNetworkResolverSetupPlan(planRow, approval, request); err != nil {
		return OperationRecord{}, err
	}
	return approval, nil
}

// requireNetworkResolverSetupAuthority proves the requested source is the current identity-stage machine authority.
func requireNetworkResolverSetupAuthority(tx *gorm.DB, request StageNetworkResolverSetupRequest) error {
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return err
	}
	if !initialized {
		return &NetworkNotInitializedError{}
	}
	if network.Stage != NetworkStageIdentity {
		return fmt.Errorf("network resolver setup requires identity stage, found %q", network.Stage)
	}
	if network.Revision != request.ExpectedNetworkRevision {
		return &NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: network.Revision}
	}

	source, expectedFingerprint, err := resolverSetupSourceOwnership(request.TargetOwnership)
	if err != nil {
		return err
	}
	if expectedFingerprint != request.ExpectedSourceOwnershipFingerprint {
		return fmt.Errorf("network resolver setup source ownership fingerprint differs from its target-derived record")
	}
	if source.InstallationID != string(network.Ownership.InstallationID) ||
		source.Generation != network.Ownership.Generation ||
		source.LoopbackPoolPrefix != network.Pool.Prefix().String() {
		return fmt.Errorf("network resolver setup target identity differs from the durable network root")
	}
	projected, _, err := readMachineOwnershipProjectionInTransaction(tx)
	if err != nil {
		return err
	}
	if !projected.Exists || projected.Record != source || projected.Fingerprint != expectedFingerprint {
		return fmt.Errorf("network resolver setup source ownership differs from the confirmed machine projection")
	}
	return nil
}

// readOptionalNetworkResolverSetupPlanForStaging distinguishes an empty singleton from malformed authority.
func readOptionalNetworkResolverSetupPlanForStaging(
	tx *gorm.DB,
	operationID domain.OperationID,
) (models.NetworkResolverSetupPlan, bool, error) {
	var rows []models.NetworkResolverSetupPlan
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return models.NetworkResolverSetupPlan{}, false, fmt.Errorf("read network resolver setup plan for staging: %w", err)
	}
	if len(rows) > 1 {
		return models.NetworkResolverSetupPlan{}, false, corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("singleton plan has %d rows, expected at most 1", len(rows)),
		)
	}
	if len(rows) == 0 {
		return models.NetworkResolverSetupPlan{}, false, nil
	}
	if rows[0].Id != networkResolverSetupPlanSingletonID {
		return models.NetworkResolverSetupPlan{}, false, corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("singleton ID is %d, expected %d", rows[0].Id, networkResolverSetupPlanSingletonID),
		)
	}
	return rows[0], true, nil
}

// networkResolverSetupIntentConflict preserves typed idempotency when one intent crosses operation boundaries.
func networkResolverSetupIntentConflict(requested, existing domain.Operation) error {
	return &IntentConflictError{
		IntentID:            requested.IntentID,
		ExistingOperationID: existing.ID,
		ExistingKind:        existing.Kind,
		ExistingProjectID:   existing.ProjectID,
		RequestedKind:       requested.Kind,
		RequestedProjectID:  requested.ProjectID,
	}
}

// replayNetworkResolverSetupInTransaction accepts only the exact request that produced the fixed staged lifecycle.
func replayNetworkResolverSetupInTransaction(
	tx *gorm.DB,
	record OperationRecord,
	planRow models.NetworkResolverSetupPlan,
	request StageNetworkResolverSetupRequest,
) (OperationRecord, error) {
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := requireExactNetworkResolverSetupHistory(record, history); err != nil {
		return OperationRecord{}, err
	}
	if err := requireExactNetworkResolverSetupOperation(record, request.Operation); err != nil {
		return OperationRecord{}, err
	}
	if err := requireExactNetworkResolverSetupPlan(planRow, record, request); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// requireExactNetworkResolverSetupOperation rejects retries that reuse an intent for a different operation request.
func requireExactNetworkResolverSetupOperation(record OperationRecord, requested domain.Operation) error {
	stored := record.Operation
	if stored.ID != requested.ID ||
		stored.IntentID != requested.IntentID ||
		stored.Kind != requested.Kind ||
		stored.ProjectID != requested.ProjectID ||
		!stored.RequestedAt.Equal(requested.RequestedAt) {
		return fmt.Errorf("network resolver setup intent %q differs from its exact staged operation", requested.IntentID)
	}
	return nil
}

// requireExactNetworkResolverSetupHistory proves a replay has exactly the three staging edges produced here.
func requireExactNetworkResolverSetupHistory(record OperationRecord, history []OperationTransition) error {
	if len(history) != 3 {
		return corruptNetworkResolverSetupPlan(
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
		networkResolverSetupQueuedPhase,
		networkResolverSetupRunningPhase,
		networkResolverSetupApprovalPhase,
	}
	for index := range history {
		transition := history[index]
		if transition.State != expectedStates[index] ||
			transition.Phase != expectedPhases[index] ||
			!transition.OccurredAt.Equal(record.Operation.RequestedAt) {
			return corruptNetworkResolverSetupPlan(
				record.Operation.ID,
				fmt.Errorf("transition %d differs from the staged lifecycle", index+1),
			)
		}
		if index > 0 && transition.Sequence != history[index-1].Sequence+1 {
			return corruptNetworkResolverSetupPlan(
				record.Operation.ID,
				fmt.Errorf("operation revisions are not contiguous"),
			)
		}
	}
	if record.Operation.State != domain.OperationRequiresApproval ||
		record.Operation.Phase != networkResolverSetupApprovalPhase ||
		record.Revision != history[len(history)-1].Sequence {
		return corruptNetworkResolverSetupPlan(record.Operation.ID, fmt.Errorf("operation does not match its approval transition"))
	}
	return nil
}

// findActiveNetworkResolverSetupOperation detects a foreign global resolver owner before the storage constraint does.
func findActiveNetworkResolverSetupOperation(tx *gorm.DB) (OperationRecord, bool, error) {
	var rows []models.Operation
	if err := tx.
		Where("kind = ? AND project_id IS NULL AND state IN ?", domain.OperationKindNetworkResolverSetup, []domain.OperationState{
			domain.OperationQueued,
			domain.OperationRunning,
			domain.OperationRequiresApproval,
		}).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, false, fmt.Errorf("read active network resolver setup operation: %w", err)
	}
	if len(rows) > 1 {
		return OperationRecord{}, false, corruptStateError(
			"network resolver setup operation",
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

// insertQueuedNetworkResolverSetupOperation appends the first fixed lifecycle edge in the caller's transaction.
func insertQueuedNetworkResolverSetupOperation(tx *gorm.DB, operation domain.Operation) (OperationRecord, error) {
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return OperationRecord{}, err
	}
	row, err := operationModelFromDomain(operation, sequence)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&row).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("create network resolver setup operation: %w", err)
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
		return OperationRecord{}, fmt.Errorf("append queued network resolver setup transition: %w", err)
	}
	return OperationRecord{Operation: operation, Revision: sequence}, nil
}

// insertNetworkResolverSetupPlan binds every requested authority field to the approval revision before commit.
func insertNetworkResolverSetupPlan(
	tx *gorm.DB,
	approval OperationRecord,
	request StageNetworkResolverSetupRequest,
) error {
	row, err := networkResolverSetupPlanModel(approval, request)
	if err != nil {
		return err
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("create network resolver setup plan: %w", err)
	}
	return nil
}

// requireExactNetworkResolverSetupPlan compares every durable authority field with the exact staging request.
func requireExactNetworkResolverSetupPlan(
	row models.NetworkResolverSetupPlan,
	operation OperationRecord,
	request StageNetworkResolverSetupRequest,
) error {
	plan, networkRevision, err := networkResolverSetupPlanFromModel(row, operation)
	if err != nil {
		return err
	}
	if networkRevision != request.ExpectedNetworkRevision ||
		plan.ExpectedSourceOwnershipFingerprint != request.ExpectedSourceOwnershipFingerprint ||
		plan.TargetOwnership != request.TargetOwnership ||
		plan.Policy != request.Policy {
		return fmt.Errorf("network resolver setup plan for operation %q differs from the exact authority request", operation.Operation.ID)
	}
	return nil
}
