package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseResolverReceipt records the exact post-removal observation that permits trust release.
type GlobalNetworkReleaseResolverReceipt struct {
	// SourceCheckpointRevision identifies the resolver checkpoint that admitted this receipt.
	SourceCheckpointRevision domain.Sequence
	// ResolverEvidenceDigest retains the canonical helper proof without persisting helper authority.
	ResolverEvidenceDigest string
	// OwnedAbsentObservationFingerprint binds the post-removal host observation to this advance.
	OwnedAbsentObservationFingerprint string
	// VerifiedAt records when the daemon independently accepted the postcondition.
	VerifiedAt time.Time
}

// Validate rejects a receipt that cannot be replayed against one exact resolver checkpoint.
func (receipt GlobalNetworkReleaseResolverReceipt) Validate() error {
	if _, err := sequenceToModelInt("global network release resolver source checkpoint revision", receipt.SourceCheckpointRevision, false); err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.ResolverEvidenceDigest); err != nil {
		return fmt.Errorf("global network release resolver evidence digest: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.OwnedAbsentObservationFingerprint); err != nil {
		return fmt.Errorf("global network release resolver owned-absent observation fingerprint: %w", err)
	}
	return validateStoredTime("global network release resolver verification time", receipt.VerifiedAt)
}

// AdvanceGlobalNetworkReleaseResolverRequest binds one verified resolver release receipt to its active plan checkpoint.
type AdvanceGlobalNetworkReleaseResolverRequest struct {
	// OperationID identifies the active global release plan.
	OperationID domain.OperationID
	// CheckpointRevision fences the resolver phase that the receipt advances.
	CheckpointRevision domain.Sequence
	// NetworkRevision binds the receipt to the retained network authority snapshot.
	NetworkRevision domain.Sequence
	// Receipt contains the verified resolver-removal proof to retain durably.
	Receipt GlobalNetworkReleaseResolverReceipt
}

// Validate rejects an unfenced resolver release acknowledgement.
func (request AdvanceGlobalNetworkReleaseResolverRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release resolver checkpoint revision", request.CheckpointRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("global network release resolver network revision", request.NetworkRevision, false); err != nil {
		return err
	}
	if err := request.Receipt.Validate(); err != nil {
		return err
	}
	if request.Receipt.SourceCheckpointRevision != request.CheckpointRevision {
		return fmt.Errorf("global network release resolver receipt source checkpoint revision is %d, expected %d", request.Receipt.SourceCheckpointRevision, request.CheckpointRevision)
	}
	return nil
}

// globalNetworkReleaseResolverReceiptRow is the private persistence shape for the release receipt.
type globalNetworkReleaseResolverReceiptRow struct {
	ID                                int       `gorm:"column:id"`
	OperationID                       string    `gorm:"column:operation_id"`
	SourceCheckpointRevision          int       `gorm:"column:source_checkpoint_revision"`
	ResolverEvidenceDigest            string    `gorm:"column:resolver_evidence_digest"`
	OwnedAbsentObservationFingerprint string    `gorm:"column:owned_absent_observation_fingerprint"`
	VerifiedAt                        time.Time `gorm:"column:verified_at"`
}

// TableName returns the durable resolver release-receipt table name.
func (globalNetworkReleaseResolverReceiptRow) TableName() string {
	return "network_global_release_resolver_receipts"
}

// AdvanceGlobalNetworkReleaseResolver atomically persists one verified receipt and advances the release plan from resolver to trust.
func (journal *OperationJournal) AdvanceGlobalNetworkReleaseResolver(
	ctx context.Context,
	request AdvanceGlobalNetworkReleaseResolverRequest,
) (GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release resolver: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}

	var result GlobalNetworkReleasePlanRecord
	err := journal.mutations.mutateGlobalNetworkReleaseResolverAdvance(ctx, "global network release resolver advance", func(tx *gorm.DB) error {
		plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
		if err != nil {
			return err
		}
		if plan.NetworkRevision != request.NetworkRevision {
			return fmt.Errorf("global network release network revision is %d, expected %d", plan.NetworkRevision, request.NetworkRevision)
		}
		switch plan.Phase {
		case GlobalNetworkReleasePlanPhaseResolver:
			if plan.CheckpointRevision != request.CheckpointRevision {
				return fmt.Errorf("global network release resolver checkpoint revision is %d, expected %d", plan.CheckpointRevision, request.CheckpointRevision)
			}
			if plan.LowPortReceipt == nil {
				return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("resolver phase has no low-port receipt"))
			}
			if request.Receipt.VerifiedAt.Before(plan.NetworkUpdatedAt) {
				return fmt.Errorf("global network release resolver verification precedes network authority")
			}
			if request.Receipt.VerifiedAt.Before(plan.LowPortReceipt.VerifiedAt) {
				return fmt.Errorf("global network release resolver verification precedes low-port receipt")
			}
			checkpoint, err := allocateHarborSequence(tx)
			if err != nil {
				return err
			}
			receipt := globalNetworkReleaseResolverReceiptRow{
				ID:                                1,
				OperationID:                       string(request.OperationID),
				SourceCheckpointRevision:          int(request.CheckpointRevision),
				ResolverEvidenceDigest:            request.Receipt.ResolverEvidenceDigest,
				OwnedAbsentObservationFingerprint: request.Receipt.OwnedAbsentObservationFingerprint,
				VerifiedAt:                        request.Receipt.VerifiedAt,
			}
			if err := tx.Create(&receipt).Error; err != nil {
				return fmt.Errorf("create global network release resolver receipt: %w", err)
			}
			updated := tx.Model(&globalNetworkReleasePlanRow{}).
				Where("id = ? AND operation_id = ? AND phase = ? AND checkpoint_revision = ?", 1, string(request.OperationID), string(GlobalNetworkReleasePlanPhaseResolver), int(request.CheckpointRevision)).
				Updates(map[string]any{
					"phase":               string(GlobalNetworkReleasePlanPhaseTrust),
					"checkpoint_revision": int(checkpoint),
				})
			if updated.Error != nil {
				return fmt.Errorf("advance global network release resolver plan: %w", updated.Error)
			}
			if updated.RowsAffected != 1 {
				return fmt.Errorf("global network release resolver checkpoint compare-and-swap did not match")
			}
			plan.Phase = GlobalNetworkReleasePlanPhaseTrust
			plan.CheckpointRevision = checkpoint
			plan.ResolverReceipt = &request.Receipt
			result = plan
			return nil
		case GlobalNetworkReleasePlanPhaseTrust:
			if plan.ResolverReceipt == nil ||
				request.CheckpointRevision != plan.ResolverReceipt.SourceCheckpointRevision ||
				request.Receipt != *plan.ResolverReceipt {
				return fmt.Errorf("global network release resolver replay receipt differs from committed receipt")
			}
			result = plan
			return nil
		default:
			return fmt.Errorf("global network release resolver advance requires plan phase %q or %q, found %q", GlobalNetworkReleasePlanPhaseResolver, GlobalNetworkReleasePlanPhaseTrust, plan.Phase)
		}
	}, func(tx *gorm.DB) error {
		if err := validateGlobalNetworkReleaseMutationOwner(tx, request.OperationID, GlobalNetworkReleasePlanPhaseTrust); err != nil {
			return err
		}
		return validateCommittedGlobalNetworkReleaseResolverReceipt(tx, request)
	})
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release resolver: %w", err)
	}
	return result.Clone(), nil
}

// validateCommittedGlobalNetworkReleaseResolverReceipt proves post-write validation retained the exact caller-bound receipt.
func validateCommittedGlobalNetworkReleaseResolverReceipt(tx *gorm.DB, request AdvanceGlobalNetworkReleaseResolverRequest) error {
	plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
	if err != nil {
		return err
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseTrust || plan.ResolverReceipt == nil {
		return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("resolver advance did not retain a trust receipt"))
	}
	if *plan.ResolverReceipt != request.Receipt {
		return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("committed resolver receipt differs from request"))
	}
	return nil
}

// readOptionalGlobalNetworkReleaseResolverReceipt reads the singleton receipt without permitting malformed multiplicity to look absent.
func readOptionalGlobalNetworkReleaseResolverReceipt(tx *gorm.DB, operationID domain.OperationID) (globalNetworkReleaseResolverReceiptRow, bool, error) {
	var rows []globalNetworkReleaseResolverReceiptRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return globalNetworkReleaseResolverReceiptRow{}, false, fmt.Errorf("read global network release resolver receipt: %w", err)
	}
	if len(rows) > 1 {
		return globalNetworkReleaseResolverReceiptRow{}, false, corruptGlobalNetworkReleasePlan(operationID, fmt.Errorf("resolver receipt singleton contains %d rows, expected at most 1", len(rows)))
	}
	if len(rows) == 0 {
		return globalNetworkReleaseResolverReceiptRow{}, false, nil
	}
	return rows[0], true, nil
}

// globalNetworkReleaseResolverReceiptFromRow validates one persisted receipt against its release plan.
func globalNetworkReleaseResolverReceiptFromRow(row globalNetworkReleaseResolverReceiptRow, plan GlobalNetworkReleasePlanRecord) (GlobalNetworkReleaseResolverReceipt, error) {
	if row.ID != 1 {
		return GlobalNetworkReleaseResolverReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("resolver receipt singleton ID is %d, expected 1", row.ID))
	}
	if row.OperationID != string(plan.Operation.Operation.ID) {
		return GlobalNetworkReleaseResolverReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("resolver receipt belongs to operation %q", row.OperationID))
	}
	source, err := modelIntToSequence("global network release resolver receipt source checkpoint revision", row.SourceCheckpointRevision)
	if err != nil {
		return GlobalNetworkReleaseResolverReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	receipt := GlobalNetworkReleaseResolverReceipt{
		SourceCheckpointRevision:          source,
		ResolverEvidenceDigest:            row.ResolverEvidenceDigest,
		OwnedAbsentObservationFingerprint: row.OwnedAbsentObservationFingerprint,
		VerifiedAt:                        row.VerifiedAt,
	}
	if err := receipt.Validate(); err != nil {
		return GlobalNetworkReleaseResolverReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	if receipt.VerifiedAt.Before(plan.NetworkUpdatedAt) {
		return GlobalNetworkReleaseResolverReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("resolver receipt verification precedes network authority"))
	}
	if plan.LowPortReceipt == nil {
		return GlobalNetworkReleaseResolverReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("resolver receipt has no required low-port receipt"))
	}
	if receipt.VerifiedAt.Before(plan.LowPortReceipt.VerifiedAt) {
		return GlobalNetworkReleaseResolverReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("resolver receipt verification precedes low-port receipt"))
	}
	return receipt, nil
}

// validateGlobalNetworkReleaseResolverReceipt enforces the receipt's ordered relationship to the current release phase.
func validateGlobalNetworkReleaseResolverReceipt(tx *gorm.DB, plan GlobalNetworkReleasePlanRecord) (*GlobalNetworkReleaseResolverReceipt, error) {
	row, found, err := readOptionalGlobalNetworkReleaseResolverReceipt(tx, plan.Operation.Operation.ID)
	if err != nil {
		return nil, err
	}
	switch plan.Phase {
	case GlobalNetworkReleasePlanPhaseRuntimeRelease, GlobalNetworkReleasePlanPhaseLowPorts, GlobalNetworkReleasePlanPhaseResolver:
		if found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("plan phase %q retains a premature resolver receipt", plan.Phase))
		}
		return nil, nil
	case GlobalNetworkReleasePlanPhaseTrust:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q has no resolver receipt", plan.Phase))
		}
		receipt, err := globalNetworkReleaseResolverReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("resolver receipt source checkpoint revision %d does not precede trust checkpoint revision %d", receipt.SourceCheckpointRevision, plan.CheckpointRevision))
		}
		return &receipt, nil
	case GlobalNetworkReleasePlanPhaseLoopbacks,
		GlobalNetworkReleasePlanPhaseVerifyEffects,
		GlobalNetworkReleasePlanPhaseOwnership,
		GlobalNetworkReleasePlanPhaseProjection:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q has no resolver receipt", plan.Phase))
		}
		receipt, err := globalNetworkReleaseResolverReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 >= plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("resolver receipt source checkpoint revision %d does not precede the later %q checkpoint revision %d", receipt.SourceCheckpointRevision, plan.Phase, plan.CheckpointRevision))
		}
		return &receipt, nil
	}
	return nil, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, fmt.Errorf("release phase %q is unsupported", plan.Phase))
}
