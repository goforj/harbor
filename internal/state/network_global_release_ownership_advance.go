package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseOwnershipReceipt records the verified release of the
// machine ownership boundary before projection teardown.
type GlobalNetworkReleaseOwnershipReceipt struct {
	// SourceCheckpointRevision identifies the ownership checkpoint that admitted this receipt.
	SourceCheckpointRevision domain.Sequence
	// ReleasedOwnershipFingerprint identifies the exact ownership authority released from the machine.
	ReleasedOwnershipFingerprint string
	// VerifiedAt records when the daemon independently accepted the ownership release postcondition.
	VerifiedAt time.Time
}

// Validate rejects a receipt that cannot be replayed against one exact ownership checkpoint.
func (receipt GlobalNetworkReleaseOwnershipReceipt) Validate() error {
	if _, err := sequenceToModelInt(
		"global network release ownership source checkpoint revision",
		receipt.SourceCheckpointRevision,
		false,
	); err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.ReleasedOwnershipFingerprint); err != nil {
		return fmt.Errorf("global network release released ownership fingerprint: %w", err)
	}
	return validateStoredTime("global network release ownership verification time", receipt.VerifiedAt)
}

// AdvanceGlobalNetworkReleaseOwnershipRequest binds one verified
// machine-ownership release receipt to its active plan checkpoint.
type AdvanceGlobalNetworkReleaseOwnershipRequest struct {
	// OperationID identifies the active global release plan.
	OperationID domain.OperationID
	// CheckpointRevision fences the ownership phase that the receipt advances.
	CheckpointRevision domain.Sequence
	// NetworkRevision binds the receipt to the retained network authority snapshot.
	NetworkRevision domain.Sequence
	// Receipt contains the verified ownership release proof to retain durably.
	Receipt GlobalNetworkReleaseOwnershipReceipt
}

// Validate rejects an unfenced ownership release acknowledgement.
func (request AdvanceGlobalNetworkReleaseOwnershipRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt(
		"global network release ownership checkpoint revision",
		request.CheckpointRevision,
		false,
	); err != nil {
		return err
	}
	if _, err := sequenceToModelInt(
		"global network release ownership network revision",
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
			"global network release ownership receipt source checkpoint revision is %d, expected %d",
			request.Receipt.SourceCheckpointRevision,
			request.CheckpointRevision,
		)
	}
	return nil
}

// globalNetworkReleaseOwnershipReceiptRow is the private persistence shape for the ownership release receipt.
type globalNetworkReleaseOwnershipReceiptRow struct {
	ID                           int       `gorm:"column:id"`
	OperationID                  string    `gorm:"column:operation_id"`
	SourceCheckpointRevision     int       `gorm:"column:source_checkpoint_revision"`
	ReleasedOwnershipFingerprint string    `gorm:"column:released_ownership_fingerprint"`
	VerifiedAt                   time.Time `gorm:"column:verified_at"`
}

// TableName returns the durable ownership release-receipt table name.
func (globalNetworkReleaseOwnershipReceiptRow) TableName() string {
	return "network_global_release_ownership_receipts"
}

// AdvanceGlobalNetworkReleaseOwnership atomically persists one verified receipt
// and advances the release plan from ownership to projection.
func (journal *OperationJournal) AdvanceGlobalNetworkReleaseOwnership(
	ctx context.Context,
	request AdvanceGlobalNetworkReleaseOwnershipRequest,
) (GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release ownership: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}

	var result GlobalNetworkReleasePlanRecord
	err := journal.mutations.mutateGlobalNetworkReleaseOwnershipAdvance(
		ctx,
		"global network release ownership advance",
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
			case GlobalNetworkReleasePlanPhaseOwnership:
				if plan.CheckpointRevision != request.CheckpointRevision {
					return fmt.Errorf(
						"global network release ownership checkpoint revision is %d, expected %d",
						plan.CheckpointRevision,
						request.CheckpointRevision,
					)
				}
				if plan.EffectsReceipt == nil {
					return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("ownership phase has no effects receipt"))
				}
				if request.Receipt.ReleasedOwnershipFingerprint != plan.Authority.ExpectedOwnershipFingerprint ||
					request.Receipt.ReleasedOwnershipFingerprint != plan.EffectsReceipt.OwnershipObservationFingerprint {
					return fmt.Errorf("global network release released ownership fingerprint does not match retained authority")
				}
				if request.Receipt.VerifiedAt.Before(plan.EffectsReceipt.VerifiedAt) {
					return fmt.Errorf("global network release ownership verification precedes effects receipt")
				}
				checkpoint, err := allocateHarborSequence(tx)
				if err != nil {
					return err
				}
				receipt := globalNetworkReleaseOwnershipReceiptRow{
					ID:                           1,
					OperationID:                  string(request.OperationID),
					SourceCheckpointRevision:     int(request.CheckpointRevision),
					ReleasedOwnershipFingerprint: request.Receipt.ReleasedOwnershipFingerprint,
					VerifiedAt:                   request.Receipt.VerifiedAt,
				}
				if err := tx.Create(&receipt).Error; err != nil {
					return fmt.Errorf("create global network release ownership receipt: %w", err)
				}
				updated := tx.Model(&globalNetworkReleasePlanRow{}).
					Where(
						"id = ? AND operation_id = ? AND phase = ? AND checkpoint_revision = ?",
						1,
						string(request.OperationID),
						string(GlobalNetworkReleasePlanPhaseOwnership),
						int(request.CheckpointRevision),
					).
					Updates(map[string]any{
						"phase":               string(GlobalNetworkReleasePlanPhaseProjection),
						"checkpoint_revision": int(checkpoint),
					})
				if updated.Error != nil {
					return fmt.Errorf("advance global network release ownership plan: %w", updated.Error)
				}
				if updated.RowsAffected != 1 {
					return fmt.Errorf("global network release ownership checkpoint compare-and-swap did not match")
				}
				plan.Phase = GlobalNetworkReleasePlanPhaseProjection
				plan.CheckpointRevision = checkpoint
				plan.OwnershipReceipt = &request.Receipt
				result = plan
				return nil
			case GlobalNetworkReleasePlanPhaseProjection:
				if plan.OwnershipReceipt == nil ||
					request.CheckpointRevision != plan.OwnershipReceipt.SourceCheckpointRevision ||
					request.Receipt != *plan.OwnershipReceipt {
					return fmt.Errorf("global network release ownership replay receipt differs from committed receipt")
				}
				result = plan
				return nil
			default:
				return fmt.Errorf(
					"global network release ownership advance requires plan phase %q or %q, found %q",
					GlobalNetworkReleasePlanPhaseOwnership,
					GlobalNetworkReleasePlanPhaseProjection,
					plan.Phase,
				)
			}
		},
		func(tx *gorm.DB) error {
			if err := validateGlobalNetworkReleaseMutationOwner(
				tx,
				request.OperationID,
				GlobalNetworkReleasePlanPhaseProjection,
			); err != nil {
				return err
			}
			return validateCommittedGlobalNetworkReleaseOwnershipReceipt(tx, request)
		},
	)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release ownership: %w", err)
	}
	return result.Clone(), nil
}

// validateCommittedGlobalNetworkReleaseOwnershipReceipt proves post-write
// validation retained the exact caller-bound receipt and projection owner.
func validateCommittedGlobalNetworkReleaseOwnershipReceipt(
	tx *gorm.DB,
	request AdvanceGlobalNetworkReleaseOwnershipRequest,
) error {
	plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
	if err != nil {
		return err
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseProjection || plan.OwnershipReceipt == nil {
		return corruptGlobalNetworkReleasePlan(
			request.OperationID,
			fmt.Errorf("ownership advance did not retain a projection receipt"),
		)
	}
	if *plan.OwnershipReceipt != request.Receipt {
		return corruptGlobalNetworkReleasePlan(
			request.OperationID,
			fmt.Errorf("committed ownership receipt differs from request"),
		)
	}
	return nil
}

// readOptionalGlobalNetworkReleaseOwnershipReceipt reads the singleton receipt
// without permitting malformed multiplicity to look absent.
func readOptionalGlobalNetworkReleaseOwnershipReceipt(
	tx *gorm.DB,
	operationID domain.OperationID,
) (globalNetworkReleaseOwnershipReceiptRow, bool, error) {
	var rows []globalNetworkReleaseOwnershipReceiptRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return globalNetworkReleaseOwnershipReceiptRow{}, false, fmt.Errorf(
			"read global network release ownership receipt: %w",
			err,
		)
	}
	if len(rows) > 1 {
		return globalNetworkReleaseOwnershipReceiptRow{}, false, corruptGlobalNetworkReleasePlan(
			operationID,
			fmt.Errorf("ownership receipt singleton contains %d rows, expected at most 1", len(rows)),
		)
	}
	if len(rows) == 0 {
		return globalNetworkReleaseOwnershipReceiptRow{}, false, nil
	}
	return rows[0], true, nil
}

// globalNetworkReleaseOwnershipReceiptFromRow validates one persisted receipt against its release plan.
func globalNetworkReleaseOwnershipReceiptFromRow(
	row globalNetworkReleaseOwnershipReceiptRow,
	plan GlobalNetworkReleasePlanRecord,
) (GlobalNetworkReleaseOwnershipReceipt, error) {
	if row.ID != 1 {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("ownership receipt singleton ID is %d, expected 1", row.ID),
		)
	}
	if row.OperationID != string(plan.Operation.Operation.ID) {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("ownership receipt belongs to operation %q", row.OperationID),
		)
	}
	source, err := modelIntToSequence(
		"global network release ownership receipt source checkpoint revision",
		row.SourceCheckpointRevision,
	)
	if err != nil {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	receipt := GlobalNetworkReleaseOwnershipReceipt{
		SourceCheckpointRevision:     source,
		ReleasedOwnershipFingerprint: row.ReleasedOwnershipFingerprint,
		VerifiedAt:                   row.VerifiedAt,
	}
	if err := receipt.Validate(); err != nil {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	if receipt.ReleasedOwnershipFingerprint != plan.Authority.ExpectedOwnershipFingerprint {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("ownership receipt fingerprint does not match retained authority"),
		)
	}
	if plan.EffectsReceipt == nil {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("ownership receipt has no required effects receipt"),
		)
	}
	if receipt.ReleasedOwnershipFingerprint != plan.EffectsReceipt.OwnershipObservationFingerprint {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("ownership receipt fingerprint does not match effects receipt"),
		)
	}
	if receipt.SourceCheckpointRevision != plan.EffectsReceipt.SourceCheckpointRevision+1 {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf(
				"ownership receipt source checkpoint revision %d does not immediately follow effects source checkpoint revision %d",
				receipt.SourceCheckpointRevision,
				plan.EffectsReceipt.SourceCheckpointRevision,
			),
		)
	}
	if receipt.VerifiedAt.Before(plan.EffectsReceipt.VerifiedAt) {
		return GlobalNetworkReleaseOwnershipReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("ownership receipt verification precedes effects receipt"),
		)
	}
	return receipt, nil
}

// validateGlobalNetworkReleaseOwnershipReceipt enforces the receipt's ordered
// relationship to the current release phase.
func validateGlobalNetworkReleaseOwnershipReceipt(
	tx *gorm.DB,
	plan GlobalNetworkReleasePlanRecord,
) (*GlobalNetworkReleaseOwnershipReceipt, error) {
	row, found, err := readOptionalGlobalNetworkReleaseOwnershipReceipt(tx, plan.Operation.Operation.ID)
	if err != nil {
		return nil, err
	}
	switch plan.Phase {
	case GlobalNetworkReleasePlanPhaseRuntimeRelease,
		GlobalNetworkReleasePlanPhaseLowPorts,
		GlobalNetworkReleasePlanPhaseResolver,
		GlobalNetworkReleasePlanPhaseTrust,
		GlobalNetworkReleasePlanPhaseLoopbacks,
		GlobalNetworkReleasePlanPhaseVerifyEffects,
		GlobalNetworkReleasePlanPhaseOwnership:
		if found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("plan phase %q retains a premature ownership receipt", plan.Phase),
			)
		}
		return nil, nil
	case GlobalNetworkReleasePlanPhaseProjection:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("release phase %q has no ownership receipt", plan.Phase),
			)
		}
		receipt, err := globalNetworkReleaseOwnershipReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf(
					"ownership receipt source checkpoint revision %d does not precede projection checkpoint revision %d",
					receipt.SourceCheckpointRevision,
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
