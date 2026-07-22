package state

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// AdvanceGlobalNetworkReleaseRuntimeRequest binds one runtime retirement checkpoint to its immutable release plan.
type AdvanceGlobalNetworkReleaseRuntimeRequest struct {
	// OperationID identifies the staged global release operation.
	OperationID domain.OperationID
	// CheckpointRevision is the runtime-release checkpoint revision accepted during staging.
	CheckpointRevision domain.Sequence
	// NetworkRevision binds retirement to the exact durable full-network authority.
	NetworkRevision domain.Sequence
}

// Validate rejects an unfenced runtime-release checkpoint request.
func (request AdvanceGlobalNetworkReleaseRuntimeRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release runtime checkpoint revision", request.CheckpointRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release runtime network revision", request.NetworkRevision, false); err != nil {
		return err
	}
	return nil
}

// AdvanceGlobalNetworkReleaseRuntime atomically persists the runtime owner's transition from runtime_release to low_ports.
// Process-local listener retirement is intentionally proved by the runtime controller before this persistence-only boundary is called.
func (journal *OperationJournal) AdvanceGlobalNetworkReleaseRuntime(
	ctx context.Context,
	request AdvanceGlobalNetworkReleaseRuntimeRequest,
) (GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release runtime: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}

	var result GlobalNetworkReleasePlanRecord
	err := journal.mutations.mutateGlobalNetworkReleaseRuntimeAdvance(ctx, "global network release runtime advance", func(tx *gorm.DB) error {
		plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
		if err != nil {
			return err
		}
		if plan.NetworkRevision != request.NetworkRevision {
			return fmt.Errorf("global network release network revision is %d, expected %d", plan.NetworkRevision, request.NetworkRevision)
		}
		switch plan.Phase {
		case GlobalNetworkReleasePlanPhaseRuntimeRelease:
			if plan.CheckpointRevision != request.CheckpointRevision {
				return fmt.Errorf("global network release runtime checkpoint revision is %d, expected %d", plan.CheckpointRevision, request.CheckpointRevision)
			}
			checkpoint, err := allocateHarborSequence(tx)
			if err != nil {
				return err
			}
			updated := tx.Model(&globalNetworkReleasePlanRow{}).
				Where("id = ? AND operation_id = ? AND phase = ? AND checkpoint_revision = ?", 1, string(request.OperationID), string(GlobalNetworkReleasePlanPhaseRuntimeRelease), int(request.CheckpointRevision)).
				Updates(map[string]any{
					"phase":               string(GlobalNetworkReleasePlanPhaseLowPorts),
					"checkpoint_revision": int(checkpoint),
				})
			if updated.Error != nil {
				return fmt.Errorf("advance global network release runtime plan: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("global network release runtime checkpoint compare-and-swap did not match")
			}
			plan.Phase = GlobalNetworkReleasePlanPhaseLowPorts
			plan.CheckpointRevision = checkpoint
			result = plan
			return nil
		case GlobalNetworkReleasePlanPhaseLowPorts:
			if request.CheckpointRevision != plan.Operation.Revision {
				return fmt.Errorf("global network release runtime replay checkpoint revision is %d, expected %d", request.CheckpointRevision, plan.Operation.Revision)
			}
			result = plan
			return nil
		default:
			return fmt.Errorf("global network release runtime advance requires plan phase %q or %q, found %q", GlobalNetworkReleasePlanPhaseRuntimeRelease, GlobalNetworkReleasePlanPhaseLowPorts, plan.Phase)
		}
	}, func(tx *gorm.DB) error {
		return validateGlobalNetworkReleaseMutationOwner(tx, request.OperationID, GlobalNetworkReleasePlanPhaseLowPorts)
	})
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release runtime: %w", err)
	}
	return result.Clone(), nil
}

// readValidatedGlobalNetworkReleasePlanForRuntimeAdvance restores the exact active plan before its phase-specific mutation.
func readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx *gorm.DB, operationID domain.OperationID) (GlobalNetworkReleasePlanRecord, error) {
	row, planFound, err := readOptionalGlobalNetworkReleasePlanForStaging(tx, operationID)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	if !planFound {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("active global network release operation has no durable plan"))
	}
	if row.OperationID != string(operationID) {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("singleton belongs to operation %q", row.OperationID))
	}
	operationRow, operationFound, err := findOperationByID(tx, operationID)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	if !operationFound {
		return GlobalNetworkReleasePlanRecord{}, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("operation owner is missing"))
	}
	operation, err := operationRecordFromModel(operationRow)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	return validateActiveGlobalNetworkReleasePlan(tx, row, operation)
}
