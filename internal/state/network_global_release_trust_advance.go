package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseTrustReceipt records the exact trust disposition and postcondition before loopback release.
type GlobalNetworkReleaseTrustReceipt struct {
	// SourceCheckpointRevision identifies the trust checkpoint that admitted this receipt.
	SourceCheckpointRevision domain.Sequence
	// Disposition records whether Harbor removed owned trust or preserved a preexisting entry.
	Disposition GlobalNetworkReleaseTrustDisposition
	// ConfirmationDigest retains the canonical confirmation proof without persisting helper authority.
	ConfirmationDigest string
	// ObservationFingerprint binds the independently observed trust state to this advance.
	ObservationFingerprint string
	// VerifiedAt records when the daemon independently accepted the trust postcondition.
	VerifiedAt time.Time
}

// Validate rejects a receipt that cannot be replayed against one exact trust checkpoint.
func (receipt GlobalNetworkReleaseTrustReceipt) Validate() error {
	if _, err := sequenceToModelInt("global network release trust source checkpoint revision", receipt.SourceCheckpointRevision, false); err != nil {
		return err
	}
	if err := receipt.Disposition.Validate(); err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.ConfirmationDigest); err != nil {
		return fmt.Errorf("global network release trust confirmation digest: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.ObservationFingerprint); err != nil {
		return fmt.Errorf("global network release trust observation fingerprint: %w", err)
	}
	return validateStoredTime("global network release trust verification time", receipt.VerifiedAt)
}

// AdvanceGlobalNetworkReleaseTrustRequest binds one verified trust release receipt to its active plan checkpoint.
type AdvanceGlobalNetworkReleaseTrustRequest struct {
	// OperationID identifies the active global release plan.
	OperationID domain.OperationID
	// CheckpointRevision fences the trust phase that the receipt advances.
	CheckpointRevision domain.Sequence
	// NetworkRevision binds the receipt to the retained network authority snapshot.
	NetworkRevision domain.Sequence
	// Receipt contains the verified trust proof to retain durably.
	Receipt GlobalNetworkReleaseTrustReceipt
}

// Validate rejects an unfenced trust release acknowledgement.
func (request AdvanceGlobalNetworkReleaseTrustRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release trust checkpoint revision", request.CheckpointRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release trust network revision", request.NetworkRevision, false); err != nil {
		return err
	}
	if err := request.Receipt.Validate(); err != nil {
		return err
	}
	if request.Receipt.SourceCheckpointRevision != request.CheckpointRevision {
		return fmt.Errorf("global network release trust receipt source checkpoint revision is %d, expected %d", request.Receipt.SourceCheckpointRevision, request.CheckpointRevision)
	}
	return nil
}

// globalNetworkReleaseTrustReceiptRow is the private persistence shape for the release receipt.
type globalNetworkReleaseTrustReceiptRow struct {
	ID                       int       `gorm:"column:id"`
	OperationID              string    `gorm:"column:operation_id"`
	SourceCheckpointRevision int       `gorm:"column:source_checkpoint_revision"`
	Disposition              string    `gorm:"column:disposition"`
	ConfirmationDigest       string    `gorm:"column:confirmation_digest"`
	ObservationFingerprint   string    `gorm:"column:observation_fingerprint"`
	VerifiedAt               time.Time `gorm:"column:verified_at"`
}

// TableName returns the durable trust release-receipt table name.
func (globalNetworkReleaseTrustReceiptRow) TableName() string {
	return "network_global_release_trust_receipts"
}

// AdvanceGlobalNetworkReleaseTrust atomically persists one verified receipt and advances the release plan from trust to loopbacks.
func (journal *OperationJournal) AdvanceGlobalNetworkReleaseTrust(ctx context.Context, request AdvanceGlobalNetworkReleaseTrustRequest) (GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release trust: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	var result GlobalNetworkReleasePlanRecord
	err := journal.mutations.mutateGlobalNetworkReleaseTrustAdvance(ctx, "global network release trust advance", func(tx *gorm.DB) error {
		plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
		if err != nil {
			return err
		}
		if plan.NetworkRevision != request.NetworkRevision {
			return fmt.Errorf("global network release network revision is %d, expected %d", plan.NetworkRevision, request.NetworkRevision)
		}
		switch plan.Phase {
		case GlobalNetworkReleasePlanPhaseTrust:
			if plan.CheckpointRevision != request.CheckpointRevision {
				return fmt.Errorf("global network release trust checkpoint revision is %d, expected %d", plan.CheckpointRevision, request.CheckpointRevision)
			}
			if plan.ResolverReceipt == nil {
				return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("trust phase has no resolver receipt"))
			}
			if request.Receipt.Disposition != plan.Authority.TrustDisposition {
				return fmt.Errorf("global network release trust receipt disposition is %q, expected %q", request.Receipt.Disposition, plan.Authority.TrustDisposition)
			}
			if request.Receipt.VerifiedAt.Before(plan.ResolverReceipt.VerifiedAt) {
				return fmt.Errorf("global network release trust verification precedes resolver receipt")
			}
			checkpoint, err := allocateHarborSequence(tx)
			if err != nil {
				return err
			}
			receipt := globalNetworkReleaseTrustReceiptRow{
				ID:                       1,
				OperationID:              string(request.OperationID),
				SourceCheckpointRevision: int(request.CheckpointRevision),
				Disposition:              string(request.Receipt.Disposition),
				ConfirmationDigest:       request.Receipt.ConfirmationDigest,
				ObservationFingerprint:   request.Receipt.ObservationFingerprint,
				VerifiedAt:               request.Receipt.VerifiedAt,
			}
			if err := tx.Create(&receipt).Error; err != nil {
				return fmt.Errorf("create global network release trust receipt: %w", err)
			}
			updated := tx.Model(&globalNetworkReleasePlanRow{}).
				Where("id = ? AND operation_id = ? AND phase = ? AND checkpoint_revision = ?", 1, string(request.OperationID), string(GlobalNetworkReleasePlanPhaseTrust), int(request.CheckpointRevision)).
				Updates(map[string]any{
					"phase":               string(GlobalNetworkReleasePlanPhaseLoopbacks),
					"checkpoint_revision": int(checkpoint),
				})
			if updated.Error != nil {
				return fmt.Errorf("advance global network release trust plan: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("global network release trust checkpoint compare-and-swap did not match")
			}
			plan.Phase = GlobalNetworkReleasePlanPhaseLoopbacks
			plan.CheckpointRevision = checkpoint
			plan.TrustReceipt = &request.Receipt
			result = plan
			return nil
		case GlobalNetworkReleasePlanPhaseLoopbacks:
			if plan.TrustReceipt == nil || request.CheckpointRevision != plan.TrustReceipt.SourceCheckpointRevision || request.Receipt != *plan.TrustReceipt {
				return fmt.Errorf("global network release trust replay receipt differs from committed receipt")
			}
			result = plan
			return nil
		default:
			return fmt.Errorf("global network release trust advance requires plan phase %q or %q, found %q", GlobalNetworkReleasePlanPhaseTrust, GlobalNetworkReleasePlanPhaseLoopbacks, plan.Phase)
		}
	}, func(tx *gorm.DB) error {
		if err := validateGlobalNetworkReleaseMutationOwner(tx, request.OperationID, GlobalNetworkReleasePlanPhaseLoopbacks); err != nil {
			return err
		}
		return validateCommittedGlobalNetworkReleaseTrustReceipt(tx, request)
	})
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release trust: %w", err)
	}
	return result.Clone(), nil
}

// validateCommittedGlobalNetworkReleaseTrustReceipt proves post-write validation retained the exact caller-bound receipt.
func validateCommittedGlobalNetworkReleaseTrustReceipt(tx *gorm.DB, request AdvanceGlobalNetworkReleaseTrustRequest) error {
	plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
	if err != nil {
		return err
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseLoopbacks || plan.TrustReceipt == nil {
		return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("trust advance did not retain a loopback receipt"))
	}
	if *plan.TrustReceipt != request.Receipt {
		return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("committed trust receipt differs from request"))
	}
	return nil
}

// readOptionalGlobalNetworkReleaseTrustReceipt reads the singleton receipt without permitting malformed multiplicity to look absent.
func readOptionalGlobalNetworkReleaseTrustReceipt(tx *gorm.DB, operationID domain.OperationID) (globalNetworkReleaseTrustReceiptRow, bool, error) {
	var rows []globalNetworkReleaseTrustReceiptRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return globalNetworkReleaseTrustReceiptRow{}, false, fmt.Errorf("read global network release trust receipt: %w", err)
	}
	if len(rows) > 1 {
		return globalNetworkReleaseTrustReceiptRow{}, false, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("trust receipt singleton contains %d rows, expected at most 1", len(rows)))
	}
	if len(rows) == 0 {
		return globalNetworkReleaseTrustReceiptRow{}, false, nil
	}
	return rows[0], true, nil
}

// globalNetworkReleaseTrustReceiptFromRow validates one persisted receipt against its release plan.
func globalNetworkReleaseTrustReceiptFromRow(row globalNetworkReleaseTrustReceiptRow, plan GlobalNetworkReleasePlanRecord) (GlobalNetworkReleaseTrustReceipt, error) {
	if row.ID != 1 {
		return GlobalNetworkReleaseTrustReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("trust receipt singleton ID is %d, expected 1", row.ID))
	}
	if row.OperationID != string(plan.Operation.Operation.ID) {
		return GlobalNetworkReleaseTrustReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("trust receipt belongs to operation %q", row.OperationID))
	}
	source, err := modelIntToSequence("global network release trust receipt source checkpoint revision", row.SourceCheckpointRevision)
	if err != nil {
		return GlobalNetworkReleaseTrustReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	receipt := GlobalNetworkReleaseTrustReceipt{
		SourceCheckpointRevision: source,
		Disposition:              GlobalNetworkReleaseTrustDisposition(row.Disposition),
		ConfirmationDigest:       row.ConfirmationDigest,
		ObservationFingerprint:   row.ObservationFingerprint,
		VerifiedAt:               row.VerifiedAt,
	}
	if err := receipt.Validate(); err != nil {
		return GlobalNetworkReleaseTrustReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	if receipt.Disposition != plan.Authority.TrustDisposition {
		return GlobalNetworkReleaseTrustReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("trust receipt disposition is %q, expected %q", receipt.Disposition, plan.Authority.TrustDisposition))
	}
	if plan.ResolverReceipt == nil {
		return GlobalNetworkReleaseTrustReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("trust receipt has no required resolver receipt"))
	}
	if receipt.VerifiedAt.Before(plan.ResolverReceipt.VerifiedAt) {
		return GlobalNetworkReleaseTrustReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("trust receipt verification precedes resolver receipt"))
	}
	return receipt, nil
}

// validateGlobalNetworkReleaseTrustReceipt enforces the receipt's ordered relationship to the current release phase.
func validateGlobalNetworkReleaseTrustReceipt(tx *gorm.DB, plan GlobalNetworkReleasePlanRecord) (*GlobalNetworkReleaseTrustReceipt, error) {
	row, found, err := readOptionalGlobalNetworkReleaseTrustReceipt(tx, plan.Operation.Operation.ID)
	if err != nil {
		return nil, err
	}
	switch plan.Phase {
	case GlobalNetworkReleasePlanPhaseRuntimeRelease, GlobalNetworkReleasePlanPhaseLowPorts, GlobalNetworkReleasePlanPhaseResolver, GlobalNetworkReleasePlanPhaseTrust:
		if found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("plan phase %q retains a premature trust receipt", plan.Phase))
		}
		return nil, nil
	case GlobalNetworkReleasePlanPhaseLoopbacks:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q has no trust receipt", plan.Phase))
		}
		receipt, err := globalNetworkReleaseTrustReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("trust receipt source checkpoint revision %d does not precede loopback checkpoint revision %d", receipt.SourceCheckpointRevision, plan.CheckpointRevision))
		}
		return &receipt, nil
	case GlobalNetworkReleasePlanPhaseVerifyEffects, GlobalNetworkReleasePlanPhaseOwnership, GlobalNetworkReleasePlanPhaseProjection:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q has no trust receipt", plan.Phase))
		}
		receipt, err := globalNetworkReleaseTrustReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 >= plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("trust receipt source checkpoint revision %d does not precede later %q checkpoint revision %d", receipt.SourceCheckpointRevision, plan.Phase, plan.CheckpointRevision))
		}
		return &receipt, nil
	}
	return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q is unsupported", plan.Phase))
}
