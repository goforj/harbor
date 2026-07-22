package state

import (
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseActiveError reports a mutation blocked by the active global network release owner.
type GlobalNetworkReleaseActiveError struct {
	OperationID domain.OperationID
	State       domain.OperationState
	Action      string
}

// Error describes the blocked action and the global release operation that owns the boundary.
func (err *GlobalNetworkReleaseActiveError) Error() string {
	return fmt.Sprintf(
		"cannot %s while global network release operation %q is %q",
		err.Action,
		err.OperationID,
		err.State,
	)
}

// GlobalNetworkReleaseAuthorityRequiredError reports a direct release enqueue that lacks the durable authority receipt.
type GlobalNetworkReleaseAuthorityRequiredError struct {
	Action string
}

// Error describes the staging boundary required before a global release can be queued.
func (err *GlobalNetworkReleaseAuthorityRequiredError) Error() string {
	return fmt.Sprintf("cannot %s global network release without StageGlobalNetworkRelease authority", err.Action)
}

// findActiveGlobalNetworkReleaseOperation finds the sole nonterminal global network release operation.
func findActiveGlobalNetworkReleaseOperation(tx *gorm.DB) (OperationRecord, bool, error) {
	var rows []models.Operation
	if err := tx.
		Where("kind = ? AND project_id IS NULL AND state IN ?", domain.OperationKindNetworkRelease, []domain.OperationState{
			domain.OperationQueued,
			domain.OperationRunning,
			domain.OperationRequiresApproval,
		}).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, false, fmt.Errorf("read active global network release operation: %w", err)
	}
	if len(rows) > 1 {
		return OperationRecord{}, false, corruptStateError(
			"global network release operation",
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

// rejectGlobalNetworkReleaseEnqueue blocks operations that could alter global network authority during release.
func rejectGlobalNetworkReleaseEnqueue(tx *gorm.DB, operation domain.Operation) error {
	active, found, err := findActiveGlobalNetworkReleaseOperation(tx)
	if err != nil {
		return err
	}
	if found && globalNetworkReleaseBlockedKind(operation.Kind) {
		return &GlobalNetworkReleaseActiveError{
			OperationID: active.Operation.ID,
			State:       active.Operation.State,
			Action:      "enqueue operation",
		}
	}
	if !found && operation.Kind == domain.OperationKindNetworkRelease {
		return &GlobalNetworkReleaseAuthorityRequiredError{Action: "enqueue"}
	}
	return nil
}

// globalNetworkReleaseBlockedKind reports whether an operation could change network or project lifecycle authority.
func globalNetworkReleaseBlockedKind(kind domain.OperationKind) bool {
	switch kind {
	case domain.OperationKindNetworkSetup,
		domain.OperationKindNetworkResolverSetup,
		domain.OperationKindNetworkDataPlaneSetup,
		domain.OperationKindNetworkRelease,
		domain.OperationKindProjectStart,
		domain.OperationKindProjectStop,
		domain.OperationKindProjectRestart,
		domain.OperationKindProjectUnregister:
		return true
	default:
		return false
	}
}

// requireNoActiveGlobalNetworkReleaseMutation freezes ordinary writers behind the durable global release boundary.
func requireNoActiveGlobalNetworkReleaseMutation(tx *gorm.DB, action string) error {
	if !tx.Migrator().HasTable(&models.Operation{}) {
		return nil
	}
	if tx.Migrator().HasTable(globalNetworkReleasePlanRow{}) {
		if err := requireNoGlobalNetworkReleasePlan(tx, action); err != nil {
			return err
		}
	}
	active, found, err := findActiveGlobalNetworkReleaseOperation(tx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return &GlobalNetworkReleaseActiveError{
		OperationID: active.Operation.ID,
		State:       active.Operation.State,
		Action:      action,
	}
}

// requireNoGlobalNetworkReleasePlan rejects any retained plan after validating its exact active operation owner.
func requireNoGlobalNetworkReleasePlan(tx *gorm.DB, action string) error {
	var rows []globalNetworkReleasePlanRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return fmt.Errorf("read global network release plan guard: %w", err)
	}
	if len(rows) > 1 {
		return corruptGlobalNetworkReleasePlan("global", fmt.Errorf("singleton contains %d rows, expected at most 1", len(rows)))
	}
	if len(rows) == 0 {
		return nil
	}
	row := rows[0]
	operationID := domain.OperationID(row.OperationID)
	if row.ID != 1 {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton ID is %d, expected 1", row.ID))
	}
	operationRow, found, err := findOperationByID(tx, operationID)
	if err != nil {
		return err
	}
	if !found {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation owner is missing"))
	}
	operation, err := operationRecordFromModel(operationRow)
	if err != nil {
		return err
	}
	if operation.Operation.Kind != domain.OperationKindNetworkRelease || operation.Operation.ProjectID != "" ||
		operation.Operation.State != domain.OperationRunning || operation.Operation.Phase != globalNetworkReleaseRuntimeOperationPhase {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation owner is not the active global network release"))
	}
	plan, err := globalNetworkReleasePlanFromRow(row, operation)
	if err != nil {
		return err
	}
	highWater, err := validateRetainedSequenceBounds(tx)
	if err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseCheckpoint(tx, plan, highWater); err != nil {
		return err
	}
	lowPortReceipt, err := validateGlobalNetworkReleaseLowPortReceipt(tx, plan)
	if err != nil {
		return err
	}
	plan.LowPortReceipt = lowPortReceipt
	resolverReceipt, err := validateGlobalNetworkReleaseResolverReceipt(tx, plan)
	if err != nil {
		return err
	}
	plan.ResolverReceipt = resolverReceipt
	trustReceipt, err := validateGlobalNetworkReleaseTrustReceipt(tx, plan)
	if err != nil {
		return err
	}
	plan.TrustReceipt = trustReceipt
	if _, err := validateGlobalNetworkReleaseLoopbackReceipt(tx, plan); err != nil {
		return err
	}
	return &GlobalNetworkReleaseActiveError{
		OperationID: operation.Operation.ID,
		State:       operation.Operation.State,
		Action:      action,
	}
}

// validateGlobalNetworkReleaseMutationOwner proves the bypass callback left the exact requested active plan owner.
func validateGlobalNetworkReleaseMutationOwner(
	tx *gorm.DB,
	operationID domain.OperationID,
	phase GlobalNetworkReleasePlanPhase,
) error {
	var rows []globalNetworkReleasePlanRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return fmt.Errorf("read global network release mutation plan: %w", err)
	}
	if len(rows) != 1 {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton contains %d rows, expected 1", len(rows)))
	}
	if rows[0].OperationID != string(operationID) {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton belongs to operation %q", rows[0].OperationID))
	}
	operationRow, found, err := findOperationByID(tx, operationID)
	if err != nil {
		return err
	}
	if !found {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation owner is missing"))
	}
	operation, err := operationRecordFromModel(operationRow)
	if err != nil {
		return err
	}
	if operation.Operation.Kind != domain.OperationKindNetworkRelease ||
		operation.Operation.ProjectID != "" ||
		operation.Operation.State != domain.OperationRunning ||
		operation.Operation.Phase != globalNetworkReleaseRuntimeOperationPhase {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation owner is not a global network release"))
	}
	plan, err := globalNetworkReleasePlanFromRow(rows[0], operation)
	if err != nil {
		return err
	}
	highWater, err := validateRetainedSequenceBounds(tx)
	if err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseCheckpoint(tx, plan, highWater); err != nil {
		return err
	}
	lowPortReceipt, err := validateGlobalNetworkReleaseLowPortReceipt(tx, plan)
	if err != nil {
		return err
	}
	plan.LowPortReceipt = lowPortReceipt
	resolverReceipt, err := validateGlobalNetworkReleaseResolverReceipt(tx, plan)
	if err != nil {
		return err
	}
	plan.ResolverReceipt = resolverReceipt
	trustReceipt, err := validateGlobalNetworkReleaseTrustReceipt(tx, plan)
	if err != nil {
		return err
	}
	plan.TrustReceipt = trustReceipt
	if _, err := validateGlobalNetworkReleaseLoopbackReceipt(tx, plan); err != nil {
		return err
	}
	if plan.Phase != phase {
		return corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("plan phase is %q, expected %q", plan.Phase, phase))
	}
	return nil
}
