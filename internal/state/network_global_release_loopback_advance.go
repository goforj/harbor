package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseLoopbackReceipt records the verified absence of every owned loopback target before effect verification.
type GlobalNetworkReleaseLoopbackReceipt struct {
	// SourceCheckpointRevision identifies the loopback checkpoint that admitted this receipt.
	SourceCheckpointRevision domain.Sequence
	// LoopbackEvidenceDigest retains the canonical helper evidence without persisting helper authority.
	LoopbackEvidenceDigest string
	// OwnedAbsentObservationDigest binds the independently observed owned-absent state to this advance.
	OwnedAbsentObservationDigest string
	// VerifiedAt records when the daemon independently accepted the loopback postcondition.
	VerifiedAt time.Time
}

// Validate rejects a receipt that cannot be replayed against one exact loopback checkpoint.
func (receipt GlobalNetworkReleaseLoopbackReceipt) Validate() error {
	if _, err := sequenceToModelInt(
		"global network release loopback source checkpoint revision",
		receipt.SourceCheckpointRevision,
		false,
	); err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.LoopbackEvidenceDigest); err != nil {
		return fmt.Errorf("global network release loopback evidence digest: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.OwnedAbsentObservationDigest); err != nil {
		return fmt.Errorf("global network release loopback owned-absent observation digest: %w", err)
	}
	return validateStoredTime("global network release loopback verification time", receipt.VerifiedAt)
}

// AdvanceGlobalNetworkReleaseLoopbacksRequest binds one verified loopback release receipt to its active plan checkpoint.
type AdvanceGlobalNetworkReleaseLoopbacksRequest struct {
	// OperationID identifies the active global release plan.
	OperationID domain.OperationID
	// CheckpointRevision fences the loopback phase that the receipt advances.
	CheckpointRevision domain.Sequence
	// NetworkRevision binds the receipt to the retained network authority snapshot.
	NetworkRevision domain.Sequence
	// Receipt contains the verified loopback proof to retain durably.
	Receipt GlobalNetworkReleaseLoopbackReceipt
}

// Validate rejects an unfenced loopback release acknowledgement.
func (request AdvanceGlobalNetworkReleaseLoopbacksRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt(
		"global network release loopback checkpoint revision",
		request.CheckpointRevision,
		false,
	); err != nil {
		return err
	}
	if _, err := sequenceToModelInt(
		"global network release loopback network revision",
		request.NetworkRevision,
		false,
	); err != nil {
		return err
	}
	if err := request.Receipt.Validate(); err != nil {
		return err
	}
	if request.Receipt.SourceCheckpointRevision != request.CheckpointRevision {
		return fmt.Errorf(
			"global network release loopback receipt source checkpoint revision is %d, expected %d",
			request.Receipt.SourceCheckpointRevision,
			request.CheckpointRevision,
		)
	}
	return nil
}

// globalNetworkReleaseLoopbackReceiptRow is the private persistence shape for the release receipt.
type globalNetworkReleaseLoopbackReceiptRow struct {
	ID                           int       `gorm:"column:id"`
	OperationID                  string    `gorm:"column:operation_id"`
	SourceCheckpointRevision     int       `gorm:"column:source_checkpoint_revision"`
	LoopbackEvidenceDigest       string    `gorm:"column:loopback_evidence_digest"`
	OwnedAbsentObservationDigest string    `gorm:"column:owned_absent_observation_digest"`
	VerifiedAt                   time.Time `gorm:"column:verified_at"`
}

// TableName returns the durable loopback release-receipt table name.
func (globalNetworkReleaseLoopbackReceiptRow) TableName() string {
	return "network_global_release_loopback_receipts"
}

// AdvanceGlobalNetworkReleaseLoopbacks atomically persists one verified receipt and advances the release plan from loopbacks to effect verification.
func (journal *OperationJournal) AdvanceGlobalNetworkReleaseLoopbacks(
	ctx context.Context,
	request AdvanceGlobalNetworkReleaseLoopbacksRequest,
) (GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release loopbacks: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	var result GlobalNetworkReleasePlanRecord
	err := journal.mutations.mutateGlobalNetworkReleaseLoopbackAdvance(
		ctx,
		"global network release loopback advance",
		func(tx *gorm.DB) error {
			plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
			if err != nil {
				return err
			}
			if plan.NetworkRevision != request.NetworkRevision {
				return fmt.Errorf(
					"global network release network revision is %d, expected %d",
					plan.NetworkRevision,
					request.NetworkRevision,
				)
			}
			switch plan.Phase {
			case GlobalNetworkReleasePlanPhaseLoopbacks:
				if plan.CheckpointRevision != request.CheckpointRevision {
					return fmt.Errorf(
						"global network release loopback checkpoint revision is %d, expected %d",
						plan.CheckpointRevision,
						request.CheckpointRevision,
					)
				}
				if plan.TrustReceipt == nil {
					return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("loopback phase has no trust receipt"))
				}
				if request.Receipt.VerifiedAt.Before(plan.TrustReceipt.VerifiedAt) {
					return fmt.Errorf("global network release loopback verification precedes trust receipt")
				}
				checkpoint, err := allocateHarborSequence(tx)
				if err != nil {
					return err
				}
				receipt := globalNetworkReleaseLoopbackReceiptRow{
					ID:                           1,
					OperationID:                  string(request.OperationID),
					SourceCheckpointRevision:     int(request.CheckpointRevision),
					LoopbackEvidenceDigest:       request.Receipt.LoopbackEvidenceDigest,
					OwnedAbsentObservationDigest: request.Receipt.OwnedAbsentObservationDigest,
					VerifiedAt:                   request.Receipt.VerifiedAt,
				}
				if err := tx.Create(&receipt).Error; err != nil {
					return fmt.Errorf("create global network release loopback receipt: %w", err)
				}
				updated := tx.Model(&globalNetworkReleasePlanRow{}).
					Where(
						"id = ? AND operation_id = ? AND phase = ? AND checkpoint_revision = ?",
						1,
						string(request.OperationID),
						string(GlobalNetworkReleasePlanPhaseLoopbacks),
						int(request.CheckpointRevision),
					).
					Updates(map[string]any{
						"phase":               string(GlobalNetworkReleasePlanPhaseVerifyEffects),
						"checkpoint_revision": int(checkpoint),
					})
				if updated.Error != nil {
					return fmt.Errorf("advance global network release loopback plan: %w", updated.Error)
				}
				if updated.RowsAffected != 1 {
					return fmt.Errorf("global network release loopback checkpoint compare-and-swap did not match")
				}
				plan.Phase = GlobalNetworkReleasePlanPhaseVerifyEffects
				plan.CheckpointRevision = checkpoint
				plan.LoopbackReceipt = &request.Receipt
				result = plan
				return nil
			case GlobalNetworkReleasePlanPhaseVerifyEffects:
				if plan.LoopbackReceipt == nil ||
					request.CheckpointRevision != plan.LoopbackReceipt.SourceCheckpointRevision ||
					request.Receipt != *plan.LoopbackReceipt {
					return fmt.Errorf("global network release loopback replay receipt differs from committed receipt")
				}
				result = plan
				return nil
			default:
				return fmt.Errorf(
					"global network release loopback advance requires plan phase %q or %q, found %q",
					GlobalNetworkReleasePlanPhaseLoopbacks,
					GlobalNetworkReleasePlanPhaseVerifyEffects,
					plan.Phase,
				)
			}
		},
		func(tx *gorm.DB) error {
			if err := validateGlobalNetworkReleaseMutationOwner(
				tx,
				request.OperationID,
				GlobalNetworkReleasePlanPhaseVerifyEffects,
			); err != nil {
				return err
			}
			return validateCommittedGlobalNetworkReleaseLoopbackReceipt(tx, request)
		},
	)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release loopbacks: %w", err)
	}
	return result.Clone(), nil
}

// validateCommittedGlobalNetworkReleaseLoopbackReceipt proves post-write validation retained the exact caller-bound receipt.
func validateCommittedGlobalNetworkReleaseLoopbackReceipt(
	tx *gorm.DB,
	request AdvanceGlobalNetworkReleaseLoopbacksRequest,
) error {
	plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
	if err != nil {
		return err
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseVerifyEffects || plan.LoopbackReceipt == nil {
		return corruptGlobalNetworkReleasePlan(
			request.OperationID,
			fmt.Errorf("loopback advance did not retain an effect-verification receipt"),
		)
	}
	if *plan.LoopbackReceipt != request.Receipt {
		return corruptGlobalNetworkReleasePlan(
			request.OperationID,
			fmt.Errorf("committed loopback receipt differs from request"),
		)
	}
	return nil
}

// readOptionalGlobalNetworkReleaseLoopbackReceipt reads the singleton receipt without permitting malformed multiplicity to look absent.
func readOptionalGlobalNetworkReleaseLoopbackReceipt(
	tx *gorm.DB,
	operationID domain.OperationID,
) (globalNetworkReleaseLoopbackReceiptRow, bool, error) {
	var rows []globalNetworkReleaseLoopbackReceiptRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return globalNetworkReleaseLoopbackReceiptRow{}, false, fmt.Errorf("read global network release loopback receipt: %w", err)
	}
	if len(rows) > 1 {
		return globalNetworkReleaseLoopbackReceiptRow{}, false, corruptGlobalNetworkReleasePlan(
			operationID,
			fmt.Errorf("loopback receipt singleton contains %d rows, expected at most 1", len(rows)),
		)
	}
	if len(rows) == 0 {
		return globalNetworkReleaseLoopbackReceiptRow{}, false, nil
	}
	return rows[0], true, nil
}

// globalNetworkReleaseLoopbackReceiptFromRow validates one persisted receipt against its release plan.
func globalNetworkReleaseLoopbackReceiptFromRow(
	row globalNetworkReleaseLoopbackReceiptRow,
	plan GlobalNetworkReleasePlanRecord,
) (GlobalNetworkReleaseLoopbackReceipt, error) {
	if row.ID != 1 {
		return GlobalNetworkReleaseLoopbackReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("loopback receipt singleton ID is %d, expected 1", row.ID),
		)
	}
	if row.OperationID != string(plan.Operation.Operation.ID) {
		return GlobalNetworkReleaseLoopbackReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("loopback receipt belongs to operation %q", row.OperationID),
		)
	}
	source, err := modelIntToSequence(
		"global network release loopback receipt source checkpoint revision",
		row.SourceCheckpointRevision,
	)
	if err != nil {
		return GlobalNetworkReleaseLoopbackReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	receipt := GlobalNetworkReleaseLoopbackReceipt{
		SourceCheckpointRevision:     source,
		LoopbackEvidenceDigest:       row.LoopbackEvidenceDigest,
		OwnedAbsentObservationDigest: row.OwnedAbsentObservationDigest,
		VerifiedAt:                   row.VerifiedAt,
	}
	if err := receipt.Validate(); err != nil {
		return GlobalNetworkReleaseLoopbackReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	if plan.TrustReceipt == nil {
		return GlobalNetworkReleaseLoopbackReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("loopback receipt has no required trust receipt"),
		)
	}
	if receipt.VerifiedAt.Before(plan.TrustReceipt.VerifiedAt) {
		return GlobalNetworkReleaseLoopbackReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("loopback receipt verification precedes trust receipt"),
		)
	}
	return receipt, nil
}

// validateGlobalNetworkReleaseLoopbackReceipt enforces the receipt's ordered relationship to the current release phase.
func validateGlobalNetworkReleaseLoopbackReceipt(
	tx *gorm.DB,
	plan GlobalNetworkReleasePlanRecord,
) (*GlobalNetworkReleaseLoopbackReceipt, error) {
	row, found, err := readOptionalGlobalNetworkReleaseLoopbackReceipt(tx, plan.Operation.Operation.ID)
	if err != nil {
		return nil, err
	}
	switch plan.Phase {
	case GlobalNetworkReleasePlanPhaseRuntimeRelease,
		GlobalNetworkReleasePlanPhaseLowPorts,
		GlobalNetworkReleasePlanPhaseResolver,
		GlobalNetworkReleasePlanPhaseTrust,
		GlobalNetworkReleasePlanPhaseLoopbacks:
		if found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("plan phase %q retains a premature loopback receipt", plan.Phase),
			)
		}
		return nil, nil
	case GlobalNetworkReleasePlanPhaseVerifyEffects:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("release phase %q has no loopback receipt", plan.Phase),
			)
		}
		receipt, err := globalNetworkReleaseLoopbackReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf(
					"loopback receipt source checkpoint revision %d does not precede effect-verification checkpoint revision %d",
					receipt.SourceCheckpointRevision,
					plan.CheckpointRevision,
				),
			)
		}
		return &receipt, nil
	case GlobalNetworkReleasePlanPhaseOwnership, GlobalNetworkReleasePlanPhaseProjection:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("release phase %q has no loopback receipt", plan.Phase),
			)
		}
		receipt, err := globalNetworkReleaseLoopbackReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 >= plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf(
					"loopback receipt source checkpoint revision %d does not precede later %q checkpoint revision %d",
					receipt.SourceCheckpointRevision,
					plan.Phase,
					plan.CheckpointRevision,
				),
			)
		}
		return &receipt, nil
	}
	return nil, corruptGlobalNetworkReleasePlan(
		plan.Operation.Operation.ID,
		fmt.Errorf("release phase %q is unsupported", plan.Phase),
	)
}
