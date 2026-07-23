package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const globalNetworkReleaseSucceededPhase = "network released"

// FinalizeGlobalNetworkReleaseProjectionRequest identifies the exact projection checkpoint to retire.
type FinalizeGlobalNetworkReleaseProjectionRequest struct {
	// OperationID identifies the active global release plan.
	OperationID domain.OperationID
	// CheckpointRevision fences terminal deletion to the retained projection checkpoint.
	CheckpointRevision domain.Sequence
	// NetworkRevision binds terminal deletion to the staged network aggregate.
	NetworkRevision domain.Sequence
	// At records the terminal operation transition time.
	At time.Time
}

// Validate rejects an unfenced terminal release acknowledgement.
func (request FinalizeGlobalNetworkReleaseProjectionRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release projection checkpoint revision", request.CheckpointRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release projection network revision", request.NetworkRevision, false); err != nil {
		return err
	}
	return validateStoredTime("global network release projection completion time", request.At)
}

// FinalizeGlobalNetworkReleaseProjection atomically removes the durable network aggregate, release artifacts, and active operation owner.
func (journal *OperationJournal) FinalizeGlobalNetworkReleaseProjection(
	ctx context.Context,
	request FinalizeGlobalNetworkReleaseProjectionRequest,
) (OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("finalize global network release projection: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return OperationRecord{}, err
	}

	var result OperationRecord
	var expectedTerminal globalNetworkReleaseTerminalRow
	err := journal.mutations.mutateGlobalNetworkReleaseProjectionFinalize(
		ctx,
		"global network release projection finalization",
		func(tx *gorm.DB) error {
			plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
			if err != nil {
				return err
			}
			if plan.Phase != GlobalNetworkReleasePlanPhaseProjection || plan.OwnershipReceipt == nil {
				return fmt.Errorf("global network release projection finalization requires a projection plan with ownership receipt")
			}
			if plan.CheckpointRevision != request.CheckpointRevision || plan.NetworkRevision != request.NetworkRevision {
				return fmt.Errorf("global network release projection finalization fence differs from durable plan")
			}
			if request.At.Before(plan.OwnershipReceipt.VerifiedAt) {
				return fmt.Errorf("global network release projection completion precedes ownership receipt")
			}
			if err := requireGlobalNetworkReleaseProjection(tx, plan); err != nil {
				return err
			}
			terminal := globalNetworkReleaseTerminalRow{
				OperationID:              string(request.OperationID),
				OwnerIdentity:            plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
				SourceCheckpointRevision: int(plan.OwnershipReceipt.SourceCheckpointRevision),
				NetworkRevision:          int(plan.NetworkRevision),
			}
			expectedTerminal = terminal
			if err := tx.Create(&terminal).Error; err != nil {
				return fmt.Errorf("create global network release terminal: %w", err)
			}
			if err := deleteGlobalNetworkReleaseProjection(tx, request.OperationID); err != nil {
				return err
			}
			completed, err := transitionOperationInTransaction(tx, request.OperationID, plan.Operation.Revision, domain.OperationSucceeded, globalNetworkReleaseSucceededPhase, request.At, nil)
			if err != nil {
				return err
			}
			result = completed
			return nil
		},
		func(tx *gorm.DB) error {
			if err := requireNoGlobalNetworkReleasePlan(tx, "validate global network release projection finalization"); err != nil {
				return err
			}
			terminal, found, err := readValidatedGlobalNetworkReleaseTerminal(tx, request.OperationID)
			if err != nil {
				return err
			}
			if !found ||
				!sameGlobalNetworkReleaseTerminalOperation(terminal.Operation, result) ||
				terminal.OwnerIdentity != expectedTerminal.OwnerIdentity ||
				terminal.SourceCheckpointRevision != domain.Sequence(expectedTerminal.SourceCheckpointRevision) ||
				terminal.NetworkRevision != domain.Sequence(expectedTerminal.NetworkRevision) {
				return fmt.Errorf("global network release projection finalization did not retain the exact terminal record")
			}
			return nil
		},
	)
	if err != nil {
		return OperationRecord{}, fmt.Errorf("finalize global network release projection: %w", err)
	}
	return result, nil
}

// requireGlobalNetworkReleaseProjection proves the retained plan still names the exact full aggregate being deleted.
func requireGlobalNetworkReleaseProjection(tx *gorm.DB, plan GlobalNetworkReleasePlanRecord) error {
	policyFingerprint, err := plan.Authority.Policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("fingerprint global network release policy: %w", err)
	}
	projection, err := resolveNetworkDataPlaneSetupProjection(tx, plan.Authority.Policy, policyFingerprint)
	if err != nil {
		return fmt.Errorf("read global network release projection: %w", err)
	}
	if !sameNetworkDataPlaneSetupProjection(projection, plan.Authority.Projection) {
		return fmt.Errorf("global network release projection differs from retained authority")
	}
	return nil
}

// deleteGlobalNetworkReleaseProjection removes only the active projection before its RESTRICT-referenced plan and receipts.
func deleteGlobalNetworkReleaseProjection(tx *gorm.DB, operationID domain.OperationID) error {
	deletions := []struct {
		name      string
		value     any
		predicate string
		argument  any
	}{
		{
			name:      "network project releases",
			value:     &models.NetworkProjectRelease{},
			predicate: "network_state_id = ?",
			argument:  networkStateSingletonID,
		},
		{
			name:      "public endpoint leases",
			value:     &models.PublicEndpointLease{},
			predicate: "network_state_id = ?",
			argument:  networkStateSingletonID,
		},
		{
			name:      "loopback address leases",
			value:     &models.LoopbackAddressLease{},
			predicate: "network_state_id = ?",
			argument:  networkStateSingletonID,
		},
		{
			name:      "network shared listeners",
			value:     &models.NetworkSharedListener{},
			predicate: "network_state_id = ?",
			argument:  networkStateSingletonID,
		},
		{
			name:      "network setup evidence",
			value:     &models.NetworkSetupEvidence{},
			predicate: "network_state_id = ?",
			argument:  networkStateSingletonID,
		},
		{
			name:      "network pool candidates",
			value:     &models.NetworkPoolCandidate{},
			predicate: "network_state_id = ?",
			argument:  networkStateSingletonID,
		},
		{
			name:      "machine ownership projection",
			value:     &models.MachineOwnershipProjection{},
			predicate: "network_state_id = ?",
			argument:  networkStateSingletonID,
		},
		{
			name:      "global network release ownership receipt",
			value:     &globalNetworkReleaseOwnershipReceiptRow{},
			predicate: "operation_id = ?",
			argument:  string(operationID),
		},
		{
			name:      "global network release effects receipt",
			value:     &globalNetworkReleaseEffectsReceiptRow{},
			predicate: "operation_id = ?",
			argument:  string(operationID),
		},
		{
			name:      "global network release loopback receipt",
			value:     &globalNetworkReleaseLoopbackReceiptRow{},
			predicate: "operation_id = ?",
			argument:  string(operationID),
		},
		{
			name:      "global network release trust receipt",
			value:     &globalNetworkReleaseTrustReceiptRow{},
			predicate: "operation_id = ?",
			argument:  string(operationID),
		},
		{
			name:      "global network release resolver receipt",
			value:     &globalNetworkReleaseResolverReceiptRow{},
			predicate: "operation_id = ?",
			argument:  string(operationID),
		},
		{
			name:      "global network release low-port receipt",
			value:     &globalNetworkReleaseLowPortReceiptRow{},
			predicate: "operation_id = ?",
			argument:  string(operationID),
		},
		{
			name:      "global network release plan",
			value:     &globalNetworkReleasePlanRow{},
			predicate: "operation_id = ?",
			argument:  string(operationID),
		},
	}
	for _, deletion := range deletions {
		deleted := tx.Where(deletion.predicate, deletion.argument).Delete(deletion.value)
		if deleted.Error != nil {
			return fmt.Errorf("delete %s: %w", deletion.name, deleted.Error)
		}
	}
	for _, table := range []string{
		"helper_approval_plans",
		"network_data_plane_setup_plans",
		"network_resolver_setup_plans",
	} {
		var count int64
		if err := tx.Table(table).Where("network_state_id = ?", networkStateSingletonID).Count(&count).Error; err != nil {
			return fmt.Errorf("count unrelated network state holder %s: %w", table, err)
		}
		if count != 0 {
			return fmt.Errorf("unrelated network state holder %s retains %d rows", table, count)
		}
	}
	if err := tx.Where("id = ?", networkStateSingletonID).Delete(&models.NetworkState{}).Error; err != nil {
		return fmt.Errorf("delete network state: %w", err)
	}
	return nil
}
