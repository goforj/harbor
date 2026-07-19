package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const (
	networkSetupQueuedPhase   = "queued"
	networkSetupRunningPhase  = "preparing"
	networkSetupApprovalPhase = "awaiting approval"
)

// StageNetworkSetupRequest contains the complete immutable intent needed to stage first-run network approval.
type StageNetworkSetupRequest struct {
	// Operation is the queued global journal identity that will own setup approval.
	Operation domain.Operation
	// Ownership is the complete generation-one authority that the helper must establish.
	Ownership ownership.Record
}

// Validate rejects setup requests that cannot become the singleton generation-one loopback authority.
func (request StageNetworkSetupRequest) Validate() error {
	if err := request.Operation.Validate(); err != nil {
		return fmt.Errorf("network setup operation: %w", err)
	}
	if request.Operation.Kind != domain.OperationKindNetworkSetup {
		return fmt.Errorf("network setup operation kind must be %q", domain.OperationKindNetworkSetup)
	}
	if request.Operation.ProjectID != "" {
		return fmt.Errorf("network setup operation must be global")
	}
	if request.Operation.State != domain.OperationQueued {
		return fmt.Errorf("network setup operation must be queued")
	}
	if request.Operation.Phase != networkSetupQueuedPhase {
		return fmt.Errorf("network setup queued phase must be %q", networkSetupQueuedPhase)
	}
	if request.Ownership.SchemaVersion != ownership.CurrentSchemaVersion {
		return fmt.Errorf(
			"network setup ownership schema version is %d, want %d",
			request.Ownership.SchemaVersion,
			ownership.CurrentSchemaVersion,
		)
	}
	if request.Ownership.Generation != networkSetupOwnershipGeneration {
		return fmt.Errorf(
			"network setup ownership generation is %d, want %d",
			request.Ownership.Generation,
			networkSetupOwnershipGeneration,
		)
	}
	if err := request.Ownership.Validate(); err != nil {
		return fmt.Errorf("network setup ownership: %w", err)
	}
	if _, err := networkSetupIdentityPool(request.Ownership.LoopbackPoolPrefix); err != nil {
		return fmt.Errorf("network setup ownership: %w", err)
	}
	return nil
}

// StageNetworkSetup atomically stages a new singleton setup plan or replays its exact durable result.
func (journal *OperationJournal) StageNetworkSetup(
	ctx context.Context,
	request StageNetworkSetupRequest,
) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("stage network setup: %w", err)
	}
	ctx = normalizeContext(ctx)

	var result OperationRecord
	err := journal.mutations.mutate(ctx, "network setup staging", func(tx *gorm.DB) error {
		staged, err := stageNetworkSetupInTransaction(tx, request)
		if err != nil {
			return err
		}
		result = staged
		return nil
	})
	if err != nil {
		return OperationRecord{}, fmt.Errorf("stage network setup: %w", err)
	}
	return result, nil
}

// stageNetworkSetupInTransaction keeps conflict detection, lifecycle writes, and plan binding in one writer instant.
func stageNetworkSetupInTransaction(tx *gorm.DB, request StageNetworkSetupRequest) (OperationRecord, error) {
	if err := requireNetworkStateAbsentForStaging(tx); err != nil {
		return OperationRecord{}, err
	}
	planRow, planFound, err := readOptionalNetworkSetupPlanForStaging(tx, request.Operation.ID)
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
		if record.Operation.Kind != request.Operation.Kind ||
			record.Operation.ProjectID != request.Operation.ProjectID {
			return OperationRecord{}, networkSetupIntentConflict(request.Operation, record.Operation)
		}
		if !planFound {
			return OperationRecord{}, fmt.Errorf(
				"network setup operation %q exists without its singleton plan",
				record.Operation.ID,
			)
		}
		return replayNetworkSetupInTransaction(tx, record, planRow)
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
		return OperationRecord{}, fmt.Errorf(
			"network setup plan already belongs to operation %q, not %q",
			planRow.OperationId,
			request.Operation.ID,
		)
	}
	if active, found, err := findActiveNetworkSetupOperation(tx); err != nil {
		return OperationRecord{}, err
	} else if found {
		return OperationRecord{}, fmt.Errorf(
			"network setup operation %q is already active for intent %q",
			active.Operation.ID,
			active.Operation.IntentID,
		)
	}

	queued, err := insertQueuedNetworkSetupOperation(tx, request.Operation)
	if err != nil {
		return OperationRecord{}, err
	}
	running, err := transitionOperationInTransaction(
		tx,
		request.Operation.ID,
		queued.Revision,
		domain.OperationRunning,
		networkSetupRunningPhase,
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
		networkSetupApprovalPhase,
		request.Operation.RequestedAt,
		nil,
	)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := insertNetworkSetupPlan(tx, approval, request.Ownership); err != nil {
		return OperationRecord{}, err
	}
	planRow, planFound, err = readOptionalNetworkSetupPlanForStaging(tx, request.Operation.ID)
	if err != nil {
		return OperationRecord{}, err
	}
	if !planFound {
		return OperationRecord{}, corruptNetworkSetupPlan(request.Operation.ID, fmt.Errorf("singleton plan is missing after insert"))
	}
	if err := requireInsertedNetworkSetupPlan(planRow, approval, request.Ownership); err != nil {
		return OperationRecord{}, err
	}
	return approval, nil
}

// requireNetworkStateAbsentForStaging prevents first-run authority from being introduced after network initialization.
func requireNetworkStateAbsentForStaging(tx *gorm.DB) error {
	var rows []struct {
		ID int `gorm:"column:id"`
	}
	if err := tx.Table((&models.NetworkState{}).TableName()).Select("id").Order("id ASC").Limit(1).Find(&rows).Error; err != nil {
		return fmt.Errorf("read network state for setup staging: %w", err)
	}
	if len(rows) != 0 {
		return fmt.Errorf("network state already exists")
	}
	return nil
}

// readOptionalNetworkSetupPlanForStaging distinguishes an empty singleton from duplicate or malformed authority.
func readOptionalNetworkSetupPlanForStaging(
	tx *gorm.DB,
	operationID domain.OperationID,
) (models.NetworkSetupPlan, bool, error) {
	var rows []models.NetworkSetupPlan
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return models.NetworkSetupPlan{}, false, fmt.Errorf("read network setup plan for staging: %w", err)
	}
	if len(rows) > 1 {
		return models.NetworkSetupPlan{}, false, corruptNetworkSetupPlan(
			operationID,
			fmt.Errorf("singleton plan has %d rows, expected at most 1", len(rows)),
		)
	}
	if len(rows) == 0 {
		return models.NetworkSetupPlan{}, false, nil
	}
	if rows[0].Id != 1 {
		return models.NetworkSetupPlan{}, false, corruptNetworkSetupPlan(
			operationID,
			fmt.Errorf("singleton ID is %d, expected 1", rows[0].Id),
		)
	}
	return rows[0], true, nil
}

// networkSetupIntentConflict preserves the journal's typed idempotency boundary for non-exact setup retries.
func networkSetupIntentConflict(requested, existing domain.Operation) error {
	return &IntentConflictError{
		IntentID:            requested.IntentID,
		ExistingOperationID: existing.ID,
		ExistingKind:        existing.Kind,
		ExistingProjectID:   existing.ProjectID,
		RequestedKind:       requested.Kind,
		RequestedProjectID:  requested.ProjectID,
	}
}

// replayNetworkSetupInTransaction returns only the exact lifecycle and ownership plan produced by this staging boundary.
func replayNetworkSetupInTransaction(
	tx *gorm.DB,
	record OperationRecord,
	planRow models.NetworkSetupPlan,
) (OperationRecord, error) {
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return OperationRecord{}, err
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := requireExactNetworkSetupHistory(record, history); err != nil {
		return OperationRecord{}, err
	}
	if err := validateStoredNetworkSetupPlan(planRow, record); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// requireExactNetworkSetupHistory proves a replay has the three fixed staging edges produced for its durable operation.
func requireExactNetworkSetupHistory(
	record OperationRecord,
	history []OperationTransition,
) error {
	operationID := record.Operation.ID
	if len(history) != 3 {
		return fmt.Errorf("network setup operation %q has %d transitions, expected 3", operationID, len(history))
	}
	expectedStates := [...]domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationRequiresApproval,
	}
	expectedPhases := [...]string{
		networkSetupQueuedPhase,
		networkSetupRunningPhase,
		networkSetupApprovalPhase,
	}
	for index := range history {
		transition := history[index]
		if transition.State != expectedStates[index] || transition.Phase != expectedPhases[index] ||
			!transition.OccurredAt.Equal(record.Operation.RequestedAt) {
			return fmt.Errorf("network setup operation %q transition %d differs from the staged lifecycle", operationID, index+1)
		}
		if index > 0 && transition.Sequence != history[index-1].Sequence+1 {
			return fmt.Errorf("network setup operation %q revisions are not contiguous", operationID)
		}
	}
	return nil
}

// findActiveNetworkSetupOperation detects a foreign global setup owner before relying on a storage constraint error.
func findActiveNetworkSetupOperation(tx *gorm.DB) (OperationRecord, bool, error) {
	var rows []models.Operation
	if err := tx.
		Where("kind = ? AND project_id IS NULL AND state IN ?", domain.OperationKindNetworkSetup, []domain.OperationState{
			domain.OperationQueued,
			domain.OperationRunning,
			domain.OperationRequiresApproval,
		}).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, false, fmt.Errorf("read active network setup operation: %w", err)
	}
	if len(rows) > 1 {
		return OperationRecord{}, false, corruptStateError(
			"network setup operation",
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

// insertQueuedNetworkSetupOperation appends the initial operation and history edge without opening a nested transaction.
func insertQueuedNetworkSetupOperation(tx *gorm.DB, operation domain.Operation) (OperationRecord, error) {
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return OperationRecord{}, err
	}
	row, err := operationModelFromDomain(operation, sequence)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := tx.Create(&row).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("create network setup operation: %w", err)
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
		return OperationRecord{}, fmt.Errorf("append queued network setup transition: %w", err)
	}
	return OperationRecord{Operation: operation, Revision: sequence}, nil
}

// insertNetworkSetupPlan binds the complete ownership request to the approval revision before commit.
func insertNetworkSetupPlan(tx *gorm.DB, approval OperationRecord, record ownership.Record) error {
	revision, err := sequenceToModelInt("network setup plan operation revision", approval.Revision, false)
	if err != nil {
		return err
	}
	row := models.NetworkSetupPlan{
		Id:                     1,
		OperationId:            string(approval.Operation.ID),
		OperationRevision:      revision,
		OwnershipSchemaVersion: int(record.SchemaVersion),
		InstallationId:         record.InstallationID,
		OwnerIdentity:          record.OwnerIdentity,
		OwnershipGeneration:    int(record.Generation),
		LoopbackPoolPrefix:     record.LoopbackPoolPrefix,
		TicketVerifierKey:      record.TicketVerifierKey,
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("create network setup plan: %w", err)
	}
	return nil
}

// validateStoredNetworkSetupPlan proves the singleton contains complete ownership bound to the durable approval operation.
func validateStoredNetworkSetupPlan(row models.NetworkSetupPlan, operation OperationRecord) error {
	_, err := networkSetupPoolPlanFromModel(row, operation)
	return err
}

// requireInsertedNetworkSetupPlan validates the singleton binding and compares every newly requested ownership field.
func requireInsertedNetworkSetupPlan(
	row models.NetworkSetupPlan,
	operation OperationRecord,
	record ownership.Record,
) error {
	plan, err := networkSetupPoolPlanFromModel(row, operation)
	if err != nil {
		return err
	}
	if plan.Ownership != record {
		return fmt.Errorf("network setup plan for operation %q differs from the exact ownership request", operation.Operation.ID)
	}
	return nil
}
