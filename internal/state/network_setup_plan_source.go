package state

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"gorm.io/gorm"
)

const (
	networkSetupOwnershipGeneration = 1
	networkSetupPoolIdentityCount   = 8
)

// NetworkSetupPlanSource resolves singleton machine setup authority from the named harbord database.
type NetworkSetupPlanSource struct {
	plans *models.NetworkSetupPlanRepo
}

var _ ticketissuer.PoolPlanSource = (*NetworkSetupPlanSource)(nil)

// NewNetworkSetupPlanSource creates a read-only source over generated network setup persistence.
func NewNetworkSetupPlanSource(plans *models.NetworkSetupPlanRepo) *NetworkSetupPlanSource {
	if plans == nil {
		panic("state.NewNetworkSetupPlanSource requires a non-nil network setup plan repository")
	}
	return &NetworkSetupPlanSource{plans: plans}
}

// Resolve returns the exact global pool plan from one transactionally consistent durable instant.
func (source *NetworkSetupPlanSource) Resolve(
	ctx context.Context,
	request ticketissuer.PoolRequest,
) (ticketissuer.PoolPlan, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return ticketissuer.PoolPlan{}, fmt.Errorf("resolve network setup plan: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ticketissuer.PoolPlan{}, err
	}

	connection, err := source.plans.WithContext(ctx).Builder()
	if err != nil {
		return ticketissuer.PoolPlan{}, fmt.Errorf("open network setup plans: %w", err)
	}

	var result ticketissuer.PoolPlan
	err = connection.Transaction(func(tx *gorm.DB) error {
		resolved, resolveErr := resolveNetworkSetupPoolPlan(tx, request.OperationID)
		if resolveErr != nil {
			return resolveErr
		}
		result = resolved
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ticketissuer.PoolPlan{}, fmt.Errorf("resolve network setup plan %q: %w", request.OperationID, err)
	}
	return result, nil
}

// resolveNetworkSetupPoolPlan validates bootstrap or exact ownership-preserving repair authority in read order.
func resolveNetworkSetupPoolPlan(tx *gorm.DB, operationID domain.OperationID) (ticketissuer.PoolPlan, error) {
	operation, err := readNetworkSetupOperation(tx, operationID)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	if operation.Operation.Kind != domain.OperationKindNetworkSetup {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(
			operationID,
			fmt.Errorf("operation kind is %q, expected %q", operation.Operation.Kind, domain.OperationKindNetworkSetup),
		)
	}
	if operation.Operation.ProjectID != "" {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(operationID, fmt.Errorf("operation is not global"))
	}
	if operation.Operation.State != domain.OperationRequiresApproval {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(
			operationID,
			fmt.Errorf("operation state is %q, expected %q", operation.Operation.State, domain.OperationRequiresApproval),
		)
	}
	if operation.Revision == 0 || operation.Revision > domain.MaximumSequence {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(operationID, fmt.Errorf("operation revision is outside the durable sequence range"))
	}

	row, err := readNetworkSetupPlanRow(tx, operationID)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	plan, err := networkSetupPoolPlanFromModel(row, operation)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	plan, err = resolveNetworkSetupPoolMode(tx, operationID, plan)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(operationID, err)
	}
	return plan, nil
}

// resolveNetworkSetupPoolMode treats an initialized network as repair authority only when every durable owner agrees.
func resolveNetworkSetupPoolMode(
	tx *gorm.DB,
	operationID domain.OperationID,
	plan ticketissuer.PoolPlan,
) (ticketissuer.PoolPlan, error) {
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	network, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	if !initialized {
		if plan.Ownership.SchemaVersion != ownership.CurrentSchemaVersion ||
			plan.Ownership.Generation != networkSetupOwnershipGeneration {
			return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(operationID, fmt.Errorf("bootstrap ownership is not generation-one identity authority"))
		}
		return plan, nil
	}
	projected, _, err := readMachineOwnershipProjectionInTransaction(tx)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	if !projected.Exists || projected.Record != plan.Ownership {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(operationID, fmt.Errorf("repair ownership differs from machine projection"))
	}
	if network.Pool.Prefix() != plan.Pool.Prefix() ||
		string(network.Ownership.InstallationID) != plan.Ownership.InstallationID ||
		network.Ownership.Generation != plan.Ownership.Generation {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(operationID, fmt.Errorf("repair ownership differs from initialized network"))
	}
	plan.Mode = ticketissuer.PoolModeRepair
	plan.Ownership = projected.Record
	return plan, nil
}

// readNetworkSetupOperation reads exactly one requested operation without trusting primary-key enforcement.
func readNetworkSetupOperation(tx *gorm.DB, operationID domain.OperationID) (OperationRecord, error) {
	var rows []models.Operation
	if err := tx.
		Where("id = ?", string(operationID)).
		Order("revision ASC").
		Limit(2).
		Find(&rows).Error; err != nil {
		return OperationRecord{}, fmt.Errorf("read network setup operation: %w", err)
	}
	if len(rows) != 1 {
		return OperationRecord{}, corruptNetworkSetupPlan(
			operationID,
			fmt.Errorf("operation has %d rows, expected 1", len(rows)),
		)
	}
	record, err := operationRecordFromModel(rows[0])
	if err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// readNetworkSetupPlanRow reads the global singleton without filtering away a mismatched operation owner.
func readNetworkSetupPlanRow(tx *gorm.DB, operationID domain.OperationID) (models.NetworkSetupPlan, error) {
	var rows []models.NetworkSetupPlan
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return models.NetworkSetupPlan{}, fmt.Errorf("read network setup plan: %w", err)
	}
	if len(rows) != 1 {
		return models.NetworkSetupPlan{}, corruptNetworkSetupPlan(
			operationID,
			fmt.Errorf("singleton plan has %d rows, expected 1", len(rows)),
		)
	}
	return rows[0], nil
}

// networkSetupPoolPlanFromModel reconstructs complete ownership and all eight canonical pool identities.
func networkSetupPoolPlanFromModel(
	row models.NetworkSetupPlan,
	operation OperationRecord,
) (ticketissuer.PoolPlan, error) {
	key := operation.Operation.ID
	if row.Id != 1 {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(key, fmt.Errorf("singleton ID is %d, expected 1", row.Id))
	}
	if row.OperationId != string(operation.Operation.ID) {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(key, fmt.Errorf("operation ID does not match its owner"))
	}
	if row.OperationRevision <= 0 || uint64(row.OperationRevision) > uint64(domain.MaximumSequence) {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(key, fmt.Errorf("operation revision is outside the durable sequence range"))
	}
	if domain.Sequence(row.OperationRevision) != operation.Revision {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(
			key,
			fmt.Errorf("plan revision is %d, operation revision is %d", row.OperationRevision, operation.Revision),
		)
	}
	if row.OwnershipSchemaVersion != int(ownership.IdentitySchemaVersion) &&
		row.OwnershipSchemaVersion != int(ownership.NetworkPolicySchemaVersion) {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(
			key,
			fmt.Errorf("ownership schema version is %d, expected %d or %d", row.OwnershipSchemaVersion, ownership.IdentitySchemaVersion, ownership.NetworkPolicySchemaVersion),
		)
	}
	if row.OwnershipGeneration <= 0 || uint64(row.OwnershipGeneration) > uint64(domain.MaximumSequence) {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(
			key,
			fmt.Errorf("ownership generation is outside the durable sequence range"),
		)
	}
	networkPolicyFingerprint := ""
	switch uint32(row.OwnershipSchemaVersion) {
	case ownership.IdentitySchemaVersion:
		if row.NetworkPolicyFingerprint != nil {
			return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(key, fmt.Errorf("identity ownership retains a network policy fingerprint"))
		}
	case ownership.NetworkPolicySchemaVersion:
		if row.NetworkPolicyFingerprint == nil {
			return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(key, fmt.Errorf("policy-bound ownership is missing its network policy fingerprint"))
		}
		networkPolicyFingerprint = *row.NetworkPolicyFingerprint
	}

	plannedOwnership := ownership.Record{
		SchemaVersion:            uint32(row.OwnershipSchemaVersion),
		InstallationID:           row.InstallationId,
		OwnerIdentity:            row.OwnerIdentity,
		Generation:               uint64(row.OwnershipGeneration),
		LoopbackPoolPrefix:       row.LoopbackPoolPrefix,
		NetworkPolicyFingerprint: networkPolicyFingerprint,
		TicketVerifierKey:        row.TicketVerifierKey,
	}
	if err := plannedOwnership.Validate(); err != nil {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(key, err)
	}

	pool, err := networkSetupIdentityPool(plannedOwnership.LoopbackPoolPrefix)
	if err != nil {
		return ticketissuer.PoolPlan{}, corruptNetworkSetupPlan(key, err)
	}
	plan := ticketissuer.PoolPlan{
		OperationID:       operation.Operation.ID,
		OperationRevision: operation.Revision,
		OperationState:    operation.Operation.State,
		Mode:              ticketissuer.PoolModeBootstrap,
		Ownership:         plannedOwnership,
		Pool:              pool,
	}
	return plan, nil
}

// networkSetupIdentityPool constructs the complete canonical address set implied by one exact /29.
func networkSetupIdentityPool(value string) (identity.Pool, error) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() || prefix.Addr() != prefix.Addr().Unmap() ||
		!prefix.Addr().IsLoopback() || prefix.Bits() != 29 || prefix != prefix.Masked() || prefix.String() != value {
		return identity.Pool{}, fmt.Errorf("loopback pool prefix %q is not a canonical IPv4-loopback /29", value)
	}

	addresses := make([]netip.Addr, networkSetupPoolIdentityCount)
	address := prefix.Addr()
	for index := range addresses {
		if !prefix.Contains(address) {
			return identity.Pool{}, fmt.Errorf("loopback pool prefix %q does not contain eight identities", value)
		}
		addresses[index] = address
		address = address.Next()
	}
	pool, err := identity.NewPool(prefix, addresses)
	if err != nil {
		return identity.Pool{}, fmt.Errorf("construct network setup pool: %w", err)
	}
	return pool, nil
}

// corruptNetworkSetupPlan applies one typed corruption identity to every durable authority mismatch.
func corruptNetworkSetupPlan(operationID domain.OperationID, cause error) error {
	return corruptStateError("network setup plan", string(operationID), cause)
}
