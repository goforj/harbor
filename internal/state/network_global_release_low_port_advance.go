package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseLowPortReceipt records the exact post-removal observation that permits resolver release.
type GlobalNetworkReleaseLowPortReceipt struct {
	SourceCheckpointRevision          domain.Sequence
	LowPortEvidenceDigest             string
	OwnedAbsentObservationFingerprint string
	VerifiedAt                        time.Time
}

// Validate rejects a receipt that cannot be replayed against one exact low-port checkpoint.
func (receipt GlobalNetworkReleaseLowPortReceipt) Validate() error {
	if _, err := sequenceToModelInt("global network release low-port source checkpoint revision", receipt.SourceCheckpointRevision, false); err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.LowPortEvidenceDigest); err != nil {
		return fmt.Errorf("global network release low-port evidence digest: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.OwnedAbsentObservationFingerprint); err != nil {
		return fmt.Errorf("global network release low-port owned-absent observation fingerprint: %w", err)
	}
	return validateStoredTime("global network release low-port verification time", receipt.VerifiedAt)
}

// AdvanceGlobalNetworkReleaseLowPortsRequest binds one verified low-port release receipt to its active plan checkpoint.
type AdvanceGlobalNetworkReleaseLowPortsRequest struct {
	OperationID        domain.OperationID
	CheckpointRevision domain.Sequence
	NetworkRevision    domain.Sequence
	Receipt            GlobalNetworkReleaseLowPortReceipt
}

// Validate rejects an unfenced low-port release acknowledgement.
func (request AdvanceGlobalNetworkReleaseLowPortsRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release low-port checkpoint revision", request.CheckpointRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release low-port network revision", request.NetworkRevision, false); err != nil {
		return err
	}
	if err := request.Receipt.Validate(); err != nil {
		return err
	}
	if request.Receipt.SourceCheckpointRevision != request.CheckpointRevision {
		return fmt.Errorf("global network release low-port receipt source checkpoint revision is %d, expected %d", request.Receipt.SourceCheckpointRevision, request.CheckpointRevision)
	}
	return nil
}

// globalNetworkReleaseLowPortReceiptRow is the private persistence shape for the release receipt.
type globalNetworkReleaseLowPortReceiptRow struct {
	ID                                int       `gorm:"column:id"`
	OperationID                       string    `gorm:"column:operation_id"`
	SourceCheckpointRevision          int       `gorm:"column:source_checkpoint_revision"`
	LowPortEvidenceDigest             string    `gorm:"column:low_port_evidence_digest"`
	OwnedAbsentObservationFingerprint string    `gorm:"column:owned_absent_observation_fingerprint"`
	VerifiedAt                        time.Time `gorm:"column:verified_at"`
}

// TableName returns the durable low-port release-receipt table name.
func (globalNetworkReleaseLowPortReceiptRow) TableName() string {
	return "network_global_release_low_port_receipts"
}

// AdvanceGlobalNetworkReleaseLowPorts atomically persists one verified receipt and advances the release plan from low_ports to resolver.
func (journal *OperationJournal) AdvanceGlobalNetworkReleaseLowPorts(
	ctx context.Context,
	request AdvanceGlobalNetworkReleaseLowPortsRequest,
) (GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release low ports: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}

	var result GlobalNetworkReleasePlanRecord
	err := journal.mutations.mutateGlobalNetworkReleaseLowPortAdvance(ctx, "global network release low-port advance", func(tx *gorm.DB) error {
		plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
		if err != nil {
			return err
		}
		if plan.NetworkRevision != request.NetworkRevision {
			return fmt.Errorf("global network release network revision is %d, expected %d", plan.NetworkRevision, request.NetworkRevision)
		}
		switch plan.Phase {
		case GlobalNetworkReleasePlanPhaseLowPorts:
			if plan.CheckpointRevision != request.CheckpointRevision {
				return fmt.Errorf("global network release low-port checkpoint revision is %d, expected %d", plan.CheckpointRevision, request.CheckpointRevision)
			}
			if request.Receipt.VerifiedAt.Before(plan.NetworkUpdatedAt) {
				return fmt.Errorf("global network release low-port verification precedes network authority")
			}
			checkpoint, err := allocateHarborSequence(tx)
			if err != nil {
				return err
			}
			receipt := globalNetworkReleaseLowPortReceiptRow{
				ID:                                1,
				OperationID:                       string(request.OperationID),
				SourceCheckpointRevision:          int(request.CheckpointRevision),
				LowPortEvidenceDigest:             request.Receipt.LowPortEvidenceDigest,
				OwnedAbsentObservationFingerprint: request.Receipt.OwnedAbsentObservationFingerprint,
				VerifiedAt:                        request.Receipt.VerifiedAt,
			}
			if err := tx.Create(&receipt).Error; err != nil {
				return fmt.Errorf("create global network release low-port receipt: %w", err)
			}
			updated := tx.Model(&globalNetworkReleasePlanRow{}).
				Where("id = ? AND operation_id = ? AND phase = ? AND checkpoint_revision = ?", 1, string(request.OperationID), string(GlobalNetworkReleasePlanPhaseLowPorts), int(request.CheckpointRevision)).
				Updates(map[string]any{
					"phase":               string(GlobalNetworkReleasePlanPhaseResolver),
					"checkpoint_revision": int(checkpoint),
				})
			if updated.Error != nil {
				return fmt.Errorf("advance global network release low-port plan: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("global network release low-port checkpoint compare-and-swap did not match")
			}
			plan.Phase = GlobalNetworkReleasePlanPhaseResolver
			plan.CheckpointRevision = checkpoint
			plan.LowPortReceipt = &request.Receipt
			result = plan
			return nil
		case GlobalNetworkReleasePlanPhaseResolver:
			if request.CheckpointRevision != plan.LowPortReceipt.SourceCheckpointRevision || request.Receipt != *plan.LowPortReceipt {
				return fmt.Errorf("global network release low-port replay receipt differs from committed receipt")
			}
			result = plan
			return nil
		default:
			return fmt.Errorf("global network release low-port advance requires plan phase %q or %q, found %q", GlobalNetworkReleasePlanPhaseLowPorts, GlobalNetworkReleasePlanPhaseResolver, plan.Phase)
		}
	}, func(tx *gorm.DB) error {
		if err := validateGlobalNetworkReleaseMutationOwner(tx, request.OperationID, GlobalNetworkReleasePlanPhaseResolver); err != nil {
			return err
		}
		return validateCommittedGlobalNetworkReleaseLowPortReceipt(tx, request)
	})
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release low ports: %w", err)
	}
	return result.Clone(), nil
}

// validateCommittedGlobalNetworkReleaseLowPortReceipt proves post-write validation retained the exact caller-bound receipt.
func validateCommittedGlobalNetworkReleaseLowPortReceipt(tx *gorm.DB, request AdvanceGlobalNetworkReleaseLowPortsRequest) error {
	plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
	if err != nil {
		return err
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseResolver || plan.LowPortReceipt == nil {
		return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("low-port advance did not retain a resolver receipt"))
	}
	if *plan.LowPortReceipt != request.Receipt {
		return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("committed low-port receipt differs from request"))
	}
	return nil
}

// readOptionalGlobalNetworkReleaseLowPortReceipt reads the singleton receipt without permitting malformed multiplicity to look absent.
func readOptionalGlobalNetworkReleaseLowPortReceipt(tx *gorm.DB, operationID domain.OperationID) (globalNetworkReleaseLowPortReceiptRow, bool, error) {
	var rows []globalNetworkReleaseLowPortReceiptRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return globalNetworkReleaseLowPortReceiptRow{}, false, fmt.Errorf("read global network release low-port receipt: %w", err)
	}
	if len(rows) > 1 {
		return globalNetworkReleaseLowPortReceiptRow{}, false, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("low-port receipt singleton contains %d rows, expected at most 1", len(rows)))
	}
	if len(rows) == 0 {
		return globalNetworkReleaseLowPortReceiptRow{}, false, nil
	}
	return rows[0], true, nil
}

// globalNetworkReleaseLowPortReceiptFromRow validates one persisted receipt against its release plan.
func globalNetworkReleaseLowPortReceiptFromRow(row globalNetworkReleaseLowPortReceiptRow, plan GlobalNetworkReleasePlanRecord) (GlobalNetworkReleaseLowPortReceipt, error) {
	if row.ID != 1 {
		return GlobalNetworkReleaseLowPortReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("low-port receipt singleton ID is %d, expected 1", row.ID))
	}
	if row.OperationID != string(plan.Operation.Operation.ID) {
		return GlobalNetworkReleaseLowPortReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("low-port receipt belongs to operation %q", row.OperationID))
	}
	source, err := modelIntToSequence("global network release low-port receipt source checkpoint revision", row.SourceCheckpointRevision)
	if err != nil {
		return GlobalNetworkReleaseLowPortReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	receipt := GlobalNetworkReleaseLowPortReceipt{
		SourceCheckpointRevision:          source,
		LowPortEvidenceDigest:             row.LowPortEvidenceDigest,
		OwnedAbsentObservationFingerprint: row.OwnedAbsentObservationFingerprint,
		VerifiedAt:                        row.VerifiedAt,
	}
	if err := receipt.Validate(); err != nil {
		return GlobalNetworkReleaseLowPortReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	if receipt.VerifiedAt.Before(plan.NetworkUpdatedAt) {
		return GlobalNetworkReleaseLowPortReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("low-port receipt verification precedes network authority"))
	}
	return receipt, nil
}

// validateGlobalNetworkReleaseLowPortReceipt enforces the receipt's ordered relationship to the current release phase.
func validateGlobalNetworkReleaseLowPortReceipt(tx *gorm.DB, plan GlobalNetworkReleasePlanRecord) (*GlobalNetworkReleaseLowPortReceipt, error) {
	row, found, err := readOptionalGlobalNetworkReleaseLowPortReceipt(tx, plan.Operation.Operation.ID)
	if err != nil {
		return nil, err
	}
	switch plan.Phase {
	case GlobalNetworkReleasePlanPhaseRuntimeRelease, GlobalNetworkReleasePlanPhaseLowPorts:
		if found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("plan phase %q retains a premature low-port receipt", plan.Phase))
		}
		return nil, nil
	case GlobalNetworkReleasePlanPhaseResolver:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q has no low-port receipt", plan.Phase))
		}
		receipt, err := globalNetworkReleaseLowPortReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("low-port receipt source checkpoint revision %d does not precede resolver checkpoint revision %d", receipt.SourceCheckpointRevision, plan.CheckpointRevision))
		}
		return &receipt, nil
	case GlobalNetworkReleasePlanPhaseTrust,
		GlobalNetworkReleasePlanPhaseLoopbacks,
		GlobalNetworkReleasePlanPhaseVerifyEffects,
		GlobalNetworkReleasePlanPhaseOwnership,
		GlobalNetworkReleasePlanPhaseProjection:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q has no low-port receipt", plan.Phase))
		}
		receipt, err := globalNetworkReleaseLowPortReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 >= plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("low-port receipt source checkpoint revision %d does not precede the later %q checkpoint revision %d", receipt.SourceCheckpointRevision, plan.Phase, plan.CheckpointRevision))
		}
		return &receipt, nil
	}
	return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q is unsupported", plan.Phase))
}
