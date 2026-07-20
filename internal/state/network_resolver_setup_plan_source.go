package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// NetworkResolverSetupPlanSource resolves resolver approval authority from the named harbord database.
type NetworkResolverSetupPlanSource struct {
	plans *models.NetworkResolverSetupPlanRepo
}

var _ ticketissuer.ResolverPlanSource = (*NetworkResolverSetupPlanSource)(nil)

// NewNetworkResolverSetupPlanSource creates a strict read-only source over generated resolver-plan persistence.
func NewNetworkResolverSetupPlanSource(
	plans *models.NetworkResolverSetupPlanRepo,
) *NetworkResolverSetupPlanSource {
	return &NetworkResolverSetupPlanSource{plans: plans}
}

// Resolve returns one operation-bound resolver plan after rereading all dependent authority in one transaction.
func (source *NetworkResolverSetupPlanSource) Resolve(
	ctx context.Context,
	request ticketissuer.ResolverRequest,
) (ticketissuer.ResolverPlan, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve network resolver setup plan: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	connection, err := source.plans.WithContext(ctx).Builder()
	if err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("open network resolver setup plans: %w", err)
	}

	var result ticketissuer.ResolverPlan
	err = connection.Transaction(func(tx *gorm.DB) error {
		resolved, resolveErr := resolveNetworkResolverSetupPlan(tx, request.OperationID)
		if resolveErr != nil {
			return resolveErr
		}
		result = resolved
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf(
			"resolve network resolver setup plan %q: %w",
			request.OperationID,
			err,
		)
	}
	return result, nil
}

// resolveNetworkResolverSetupPlan validates operation, plan, network root, and machine projection in read order.
func resolveNetworkResolverSetupPlan(
	tx *gorm.DB,
	operationID domain.OperationID,
) (ticketissuer.ResolverPlan, error) {
	operation, err := readNetworkResolverSetupOperation(tx, operationID)
	if err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	if err := validateNetworkResolverSetupApprovalOperation(operation); err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	row, err := readNetworkResolverSetupPlanRow(tx, operationID)
	if err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	plan, networkRevision, err := networkResolverSetupPlanFromModel(row, operation)
	if err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	if err := requireResolvedNetworkResolverSetupAuthority(tx, operationID, networkRevision, plan); err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	if _, err := validateRetainedSequenceBounds(tx); err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	return plan, nil
}

// readNetworkResolverSetupOperation reads exactly one selected operation without trusting primary-key enforcement.
func readNetworkResolverSetupOperation(
	tx *gorm.DB,
	operationID domain.OperationID,
) (OperationRecord, error) {
	var rows []models.Operation
	if err := tx.
		Where("id = ?", string(operationID)).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("read network resolver setup operation: %w", err)
	}
	if len(rows) != 1 {
		return OperationRecord{}, corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("operation has %d rows, expected 1", len(rows)),
		)
	}
	record, err := operationRecordFromModel(rows[0])
	if err != nil {
		return OperationRecord{}, corruptNetworkResolverSetupPlan(operationID, err)
	}
	return record, nil
}

// validateNetworkResolverSetupApprovalOperation pins issuance to this boundary's exact active lifecycle state.
func validateNetworkResolverSetupApprovalOperation(operation OperationRecord) error {
	operationID := operation.Operation.ID
	if operation.Operation.Kind != domain.OperationKindNetworkResolverSetup {
		return corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("operation kind is %q, expected %q", operation.Operation.Kind, domain.OperationKindNetworkResolverSetup),
		)
	}
	if operation.Operation.ProjectID != "" {
		return corruptNetworkResolverSetupPlan(operationID, fmt.Errorf("operation is not global"))
	}
	if operation.Operation.State != domain.OperationRequiresApproval {
		return corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("operation state is %q, expected %q", operation.Operation.State, domain.OperationRequiresApproval),
		)
	}
	if operation.Operation.Phase != networkResolverSetupApprovalPhase {
		return corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("operation phase is %q, expected %q", operation.Operation.Phase, networkResolverSetupApprovalPhase),
		)
	}
	if operation.Revision == 0 || operation.Revision > domain.MaximumSequence {
		return corruptNetworkResolverSetupPlan(operationID, fmt.Errorf("operation revision is outside the durable sequence range"))
	}
	return nil
}

// readNetworkResolverSetupPlanRow reads the singleton without filtering away a mismatched operation owner.
func readNetworkResolverSetupPlanRow(
	tx *gorm.DB,
	operationID domain.OperationID,
) (models.NetworkResolverSetupPlan, error) {
	var rows []models.NetworkResolverSetupPlan
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return models.NetworkResolverSetupPlan{}, fmt.Errorf("read network resolver setup plan: %w", err)
	}
	if len(rows) != 1 {
		return models.NetworkResolverSetupPlan{}, corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("singleton plan has %d rows, expected 1", len(rows)),
		)
	}
	return rows[0], nil
}

// requireResolvedNetworkResolverSetupAuthority rejects plans whose identity-stage dependencies changed after staging.
func requireResolvedNetworkResolverSetupAuthority(
	tx *gorm.DB,
	operationID domain.OperationID,
	networkRevision domain.Sequence,
	plan ticketissuer.ResolverPlan,
) error {
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return err
	}
	if len(rows.States) == 1 && NetworkStage(rows.States[0].Stage) != NetworkStageIdentity {
		return corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("network stage is %q, expected %q", rows.States[0].Stage, NetworkStageIdentity),
		)
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return corruptNetworkResolverSetupPlan(operationID, err)
	}
	if !initialized {
		return corruptNetworkResolverSetupPlan(operationID, fmt.Errorf("network state is not initialized"))
	}
	if network.Stage != NetworkStageIdentity {
		return corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("network stage is %q, expected %q", network.Stage, NetworkStageIdentity),
		)
	}
	if network.Revision != networkRevision {
		return corruptNetworkResolverSetupPlan(
			operationID,
			fmt.Errorf("network revision is %d, plan expects %d", network.Revision, networkRevision),
		)
	}

	source, sourceFingerprint, err := resolverSetupSourceOwnership(plan.TargetOwnership)
	if err != nil {
		return corruptNetworkResolverSetupPlan(operationID, err)
	}
	if sourceFingerprint != plan.ExpectedSourceOwnershipFingerprint {
		return corruptNetworkResolverSetupPlan(operationID, fmt.Errorf("source ownership fingerprint differs from the target-derived record"))
	}
	if source.InstallationID != string(network.Ownership.InstallationID) ||
		source.Generation != network.Ownership.Generation ||
		source.LoopbackPoolPrefix != network.Pool.Prefix().String() {
		return corruptNetworkResolverSetupPlan(operationID, fmt.Errorf("target identity differs from the durable network root"))
	}
	projection, _, err := readMachineOwnershipProjectionInTransaction(tx)
	if err != nil {
		return corruptNetworkResolverSetupPlan(operationID, err)
	}
	if !projection.Exists || projection.Record != source || projection.Fingerprint != sourceFingerprint {
		return corruptNetworkResolverSetupPlan(operationID, fmt.Errorf("source ownership differs from the confirmed machine projection"))
	}
	return nil
}
