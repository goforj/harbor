package state

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// HelperApprovalPlanSource resolves revision-bound helper authority from the named harbord database.
type HelperApprovalPlanSource struct {
	plans *models.HelperApprovalPlanRepo
}

var _ ticketissuer.PlanSource = (*HelperApprovalPlanSource)(nil)

// NewHelperApprovalPlanSource creates a read-only source over generated approval-plan persistence.
func NewHelperApprovalPlanSource(plans *models.HelperApprovalPlanRepo) *HelperApprovalPlanSource {
	if plans == nil {
		panic("state.NewHelperApprovalPlanSource requires a non-nil approval plan repository")
	}
	return &HelperApprovalPlanSource{plans: plans}
}

// Resolve returns one exact approval plan from a transactionally consistent durable instant.
func (source *HelperApprovalPlanSource) Resolve(
	ctx context.Context,
	request ticketissuer.Request,
) (ticketissuer.Plan, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return ticketissuer.Plan{}, fmt.Errorf("resolve helper approval plan: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ticketissuer.Plan{}, err
	}

	connection, err := source.plans.WithContext(ctx).Builder()
	if err != nil {
		return ticketissuer.Plan{}, fmt.Errorf("open helper approval plans: %w", err)
	}

	var result ticketissuer.Plan
	err = connection.Transaction(func(tx *gorm.DB) error {
		plans, resolveErr := resolveHelperApprovalPlanSet(tx, request.OperationID)
		if resolveErr != nil {
			return resolveErr
		}
		for _, plan := range plans {
			if plan.Request.LeaseKey == request.LeaseKey {
				result = plan.Plan
				return nil
			}
		}
		return fmt.Errorf(
			"helper approval plan for operation %q and lease %q/%q was not found",
			request.OperationID,
			request.LeaseKey.ProjectID,
			request.LeaseKey.SecondaryID,
		)
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ticketissuer.Plan{}, fmt.Errorf("resolve helper approval plan %q: %w", request.OperationID, err)
	}
	return result, nil
}

// RequestsForOperation returns the complete canonical helper request set after validating its release authority.
func (source *HelperApprovalPlanSource) RequestsForOperation(
	ctx context.Context,
	operationID domain.OperationID,
) ([]ticketissuer.Request, error) {
	ctx = normalizeContext(ctx)
	if err := operationID.Validate(); err != nil {
		return nil, fmt.Errorf("enumerate helper approval plans: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	connection, err := source.plans.WithContext(ctx).Builder()
	if err != nil {
		return nil, fmt.Errorf("open helper approval plans: %w", err)
	}

	requests := []ticketissuer.Request{}
	err = connection.Transaction(func(tx *gorm.DB) error {
		plans, resolveErr := resolveHelperApprovalPlanSet(tx, operationID)
		if resolveErr != nil {
			return resolveErr
		}
		requests = make([]ticketissuer.Request, 0, len(plans))
		for _, plan := range plans {
			requests = append(requests, plan.Request)
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("enumerate helper approval plans %q: %w", operationID, err)
	}
	return requests, nil
}

// resolvedHelperApprovalPlan couples one canonical recovery request to its fully validated ticket authority.
type resolvedHelperApprovalPlan struct {
	Request ticketissuer.Request
	Plan    ticketissuer.Plan
}

// resolveHelperApprovalPlanSet validates the complete unregister release set before exposing any individual plan.
func resolveHelperApprovalPlanSet(
	tx *gorm.DB,
	operationID domain.OperationID,
) ([]resolvedHelperApprovalPlan, error) {
	operation, err := readHelperApprovalOperation(tx, operationID)
	if err != nil {
		return nil, err
	}
	if operation.Operation.State != domain.OperationRequiresApproval {
		return nil, corruptStateError(
			"helper approval plan",
			string(operationID),
			fmt.Errorf("operation state is %q, expected %q", operation.Operation.State, domain.OperationRequiresApproval),
		)
	}
	rows, err := readHelperApprovalPlanRows(tx, operationID)
	if err != nil {
		return nil, err
	}
	intents, err := projectNetworkReleaseApprovalIntentsInTransaction(tx, operation)
	if err != nil {
		return nil, err
	}
	records, err := readHelperApprovalPlanRecordsInTransaction(tx, operationID, operation.Revision)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(intents) || len(records) != len(intents) {
		return nil, corruptStateError(
			"helper approval plan",
			string(operationID),
			fmt.Errorf("durable plans differ from the exact project release set"),
		)
	}

	resolved := make([]resolvedHelperApprovalPlan, 0, len(rows))
	for index, row := range rows {
		key := helperApprovalPlanKey(row)
		record := records[index]
		if record.OperationID != operationID ||
			record.OperationRevision != operation.Revision ||
			!sameHelperApprovalIntent(record.Intent, intents[index]) {
			return nil, corruptStateError(
				"helper approval plan",
				key,
				fmt.Errorf("durable plan differs from the exact project release effect"),
			)
		}
		if err := validateHelperApprovalPlanCollisions(tx, row); err != nil {
			return nil, err
		}
		if err := validateHelperApprovalActiveLease(tx, row); err != nil {
			return nil, err
		}
		plan := ticketissuer.Plan{
			OperationID:       operation.Operation.ID,
			OperationRevision: operation.Revision,
			OperationState:    operation.Operation.State,
			Mutation:          record.Intent.Mutation,
			Lease:             record.Intent.Lease,
			LeaseState:        record.Intent.LeaseState,
			Requirements:      slices.Clone(record.Intent.Requirements),
		}
		if err := plan.Validate(); err != nil {
			return nil, corruptStateError("helper approval plan", key, err)
		}
		request := ticketissuer.Request{OperationID: operationID, LeaseKey: plan.Lease.Key}
		resolved = append(resolved, resolvedHelperApprovalPlan{Request: request, Plan: plan})
	}
	return resolved, nil
}

// readHelperApprovalPlanRows reads the operation's bounded durable plan set without trusting schema uniqueness.
func readHelperApprovalPlanRows(tx *gorm.DB, operationID domain.OperationID) ([]models.HelperApprovalPlan, error) {
	var rows []models.HelperApprovalPlan
	if err := tx.
		Where("operation_id = ?", string(operationID)).
		Order("project_id ASC").
		Order("kind ASC").
		Order("secondary_id ASC").
		Order("address ASC").
		Order("id ASC").
		Limit(maximumNetworkPoolCandidateCount + 1).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read helper approval plan: %w", err)
	}
	if len(rows) == 0 {
		return nil, corruptStateError(
			"helper approval plan",
			string(operationID),
			fmt.Errorf("approval-state operation has no durable plans"),
		)
	}
	if len(rows) > maximumNetworkPoolCandidateCount {
		return nil, corruptStateError(
			"helper approval plan",
			string(operationID),
			fmt.Errorf("plan count exceeds %d", maximumNetworkPoolCandidateCount),
		)
	}
	return rows, nil
}

// validateHelperApprovalPlanCollisions rejects another plan claiming the selected logical key or address.
func validateHelperApprovalPlanCollisions(tx *gorm.DB, plan models.HelperApprovalPlan) error {
	var rows []models.HelperApprovalPlan
	if err := tx.
		Where(
			"id = ? OR address = ? OR (project_id = ? AND secondary_id = ?)",
			plan.Id,
			plan.Address,
			plan.ProjectId,
			plan.SecondaryId,
		).
		Order("id ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("read helper approval plan collisions: %w", err)
	}
	if len(rows) != 1 {
		return corruptStateError(
			"helper approval plan",
			helperApprovalPlanKey(plan),
			fmt.Errorf("another plan claims the same lease key or address"),
		)
	}
	return nil
}

// readHelperApprovalOperation validates the unique operation owner before any approval plan can grant authority.
func readHelperApprovalOperation(tx *gorm.DB, operationID domain.OperationID) (OperationRecord, error) {
	var rows []models.Operation
	if err := tx.
		Where("id = ?", string(operationID)).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("read helper approval operation: %w", err)
	}
	if len(rows) == 0 {
		return OperationRecord{}, &OperationNotFoundError{OperationID: operationID}
	}
	if len(rows) > 1 {
		return OperationRecord{}, corruptStateError(
			"helper approval plan",
			string(operationID),
			fmt.Errorf("operation has %d rows, expected 1", len(rows)),
		)
	}
	record, err := operationRecordFromModel(rows[0])
	if err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// validateHelperApprovalActiveLease requires active plans to match every durable lease identity field exactly.
func validateHelperApprovalActiveLease(
	tx *gorm.DB,
	plan models.HelperApprovalPlan,
) error {
	key := helperApprovalPlanKey(plan)
	if !plan.LoopbackAddressLeaseId.Valid || plan.LoopbackAddressLeaseId.Int64 <= 0 {
		return corruptStateError("helper approval plan", key, fmt.Errorf("active plan requires a positive lease ID"))
	}
	leaseID := int(plan.LoopbackAddressLeaseId.Int64)
	if int64(leaseID) != plan.LoopbackAddressLeaseId.Int64 {
		return corruptStateError("helper approval plan", key, fmt.Errorf("active lease ID exceeds the platform integer range"))
	}

	var rows []models.LoopbackAddressLease
	if err := tx.
		Where(
			"id = ? OR address = ? OR (project_id = ? AND kind = ? AND secondary_id = ?)",
			leaseID,
			plan.Address,
			plan.ProjectId,
			plan.Kind,
			plan.SecondaryId,
		).
		Order("id ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("read helper approval active lease: %w", err)
	}
	if len(rows) != 1 {
		return corruptStateError(
			"helper approval plan",
			key,
			fmt.Errorf("active lease has %d rows, expected 1", len(rows)),
		)
	}
	row := rows[0]
	if row.Id != leaseID ||
		row.NetworkStateId != plan.NetworkStateId ||
		!row.ProjectId.Valid ||
		row.ProjectId.String != plan.ProjectId ||
		row.SourceProjectId != plan.ProjectId ||
		row.Kind != plan.Kind ||
		row.SecondaryId != plan.SecondaryId ||
		row.Address != plan.Address ||
		row.State != "leased" ||
		row.OwnershipInstallationId != plan.OwnershipInstallationId ||
		row.OwnershipGeneration != plan.OwnershipGeneration {
		return corruptStateError("helper approval plan", key, fmt.Errorf("active lease differs from the exact approval plan"))
	}
	if _, err := positiveNetworkGeneration("active lease generation", row.LeaseGeneration); err != nil {
		return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), err)
	}
	if err := validateNetworkEvidence("active lease ensure evidence", row.EnsureEvidence); err != nil {
		return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), err)
	}
	if err := validateStoredTime("active lease time", row.LeasedAt); err != nil {
		return corruptStateError("loopback address lease", durableKey(row.Address, row.Id), err)
	}
	if networkLeaseHasReleaseFields(row) {
		return corruptStateError(
			"loopback address lease",
			durableKey(row.Address, row.Id),
			fmt.Errorf("active lease contains release or quarantine fields"),
		)
	}
	return nil
}

// helperApprovalPlanKey renders one stable non-secret durable identity for corruption reports.
func helperApprovalPlanKey(row models.HelperApprovalPlan) string {
	return durableKey(row.OperationId+"/"+row.ProjectId+"/"+row.SecondaryId, row.Id)
}
