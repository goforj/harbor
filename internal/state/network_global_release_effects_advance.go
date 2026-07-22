package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseEffectsReceipt records the verified absence of every released network effect before ownership release.
type GlobalNetworkReleaseEffectsReceipt struct {
	// SourceCheckpointRevision identifies the effects checkpoint that admitted this receipt.
	SourceCheckpointRevision domain.Sequence
	// RuntimeObservationDigest binds the independently observed runtime state to this advance.
	RuntimeObservationDigest string
	// OwnershipObservationFingerprint binds the independently observed ownership state to this advance.
	OwnershipObservationFingerprint string
	// LowPortObservationFingerprint binds the independently observed low-port state to this advance.
	LowPortObservationFingerprint string
	// ResolverObservationFingerprint binds the independently observed resolver state to this advance.
	ResolverObservationFingerprint string
	// TrustObservationFingerprint binds the independently observed trust state to this advance.
	TrustObservationFingerprint string
	// LoopbackObservationDigest binds the independently observed loopback state to this advance.
	LoopbackObservationDigest string
	// VerifiedAt records when the daemon independently accepted the effects postcondition.
	VerifiedAt time.Time
}

// Validate rejects a receipt that cannot be replayed against one exact effects checkpoint.
func (receipt GlobalNetworkReleaseEffectsReceipt) Validate() error {
	if _, err := sequenceToModelInt(
		"global network release effects source checkpoint revision",
		receipt.SourceCheckpointRevision,
		false,
	); err != nil {
		return err
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.RuntimeObservationDigest); err != nil {
		return fmt.Errorf("global network release effects runtime observation digest: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.OwnershipObservationFingerprint); err != nil {
		return fmt.Errorf("global network release effects ownership observation fingerprint: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.LowPortObservationFingerprint); err != nil {
		return fmt.Errorf("global network release effects low-port observation fingerprint: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.ResolverObservationFingerprint); err != nil {
		return fmt.Errorf("global network release effects resolver observation fingerprint: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.TrustObservationFingerprint); err != nil {
		return fmt.Errorf("global network release effects trust observation fingerprint: %w", err)
	}
	if err := validateGlobalNetworkReleaseDigest(receipt.LoopbackObservationDigest); err != nil {
		return fmt.Errorf("global network release effects loopback observation digest: %w", err)
	}
	return validateStoredTime("global network release effects verification time", receipt.VerifiedAt)
}

// AdvanceGlobalNetworkReleaseEffectsRequest binds one verified effects receipt to its active plan checkpoint.
type AdvanceGlobalNetworkReleaseEffectsRequest struct {
	// OperationID identifies the active global release plan.
	OperationID domain.OperationID
	// CheckpointRevision fences the effects phase that the receipt advances.
	CheckpointRevision domain.Sequence
	// NetworkRevision binds the receipt to the retained network authority snapshot.
	NetworkRevision domain.Sequence
	// Receipt contains the verified effects proof to retain durably.
	Receipt GlobalNetworkReleaseEffectsReceipt
}

// Validate rejects an unfenced effects release acknowledgement.
func (request AdvanceGlobalNetworkReleaseEffectsRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt(
		"global network release effects checkpoint revision",
		request.CheckpointRevision,
		false,
	); err != nil {
		return err
	}
	if _, err := sequenceToModelInt(
		"global network release effects network revision",
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
			"global network release effects receipt source checkpoint revision is %d, expected %d",
			request.Receipt.SourceCheckpointRevision,
			request.CheckpointRevision,
		)
	}
	return nil
}

// globalNetworkReleaseEffectsReceiptRow is the private persistence shape for the release receipt.
type globalNetworkReleaseEffectsReceiptRow struct {
	ID                              int       `gorm:"column:id"`
	OperationID                     string    `gorm:"column:operation_id"`
	SourceCheckpointRevision        int       `gorm:"column:source_checkpoint_revision"`
	RuntimeObservationDigest        string    `gorm:"column:runtime_observation_digest"`
	OwnershipObservationFingerprint string    `gorm:"column:ownership_observation_fingerprint"`
	LowPortObservationFingerprint   string    `gorm:"column:low_port_observation_fingerprint"`
	ResolverObservationFingerprint  string    `gorm:"column:resolver_observation_fingerprint"`
	TrustObservationFingerprint     string    `gorm:"column:trust_observation_fingerprint"`
	LoopbackObservationDigest       string    `gorm:"column:loopback_observation_digest"`
	VerifiedAt                      time.Time `gorm:"column:verified_at"`
}

// TableName returns the durable effects release-receipt table name.
func (globalNetworkReleaseEffectsReceiptRow) TableName() string {
	return "network_global_release_effects_receipts"
}

// AdvanceGlobalNetworkReleaseEffects atomically persists one verified receipt and advances the release plan from effect verification to ownership.
func (journal *OperationJournal) AdvanceGlobalNetworkReleaseEffects(
	ctx context.Context,
	request AdvanceGlobalNetworkReleaseEffectsRequest,
) (GlobalNetworkReleasePlanRecord, error) {
	if err := request.Validate(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release effects: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleasePlanRecord{}, err
	}
	var result GlobalNetworkReleasePlanRecord
	err := journal.mutations.mutateGlobalNetworkReleaseEffectsAdvance(
		ctx,
		"global network release effects advance",
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
			case GlobalNetworkReleasePlanPhaseVerifyEffects:
				if plan.CheckpointRevision != request.CheckpointRevision {
					return fmt.Errorf(
						"global network release effects checkpoint revision is %d, expected %d",
						plan.CheckpointRevision,
						request.CheckpointRevision,
					)
				}
				if plan.LoopbackReceipt == nil {
					return corruptGlobalNetworkReleasePlan(request.OperationID, fmt.Errorf("effects phase has no loopback receipt"))
				}
				if request.Receipt.VerifiedAt.Before(plan.LoopbackReceipt.VerifiedAt) {
					return fmt.Errorf("global network release effects verification precedes loopback receipt")
				}
				if request.Receipt.OwnershipObservationFingerprint != plan.Authority.ExpectedOwnershipFingerprint {
					return fmt.Errorf(
						"global network release effects ownership observation fingerprint does not match retained authority",
					)
				}
				checkpoint, err := allocateHarborSequence(tx)
				if err != nil {
					return err
				}
				receipt := globalNetworkReleaseEffectsReceiptRow{
					ID:                              1,
					OperationID:                     string(request.OperationID),
					SourceCheckpointRevision:        int(request.CheckpointRevision),
					RuntimeObservationDigest:        request.Receipt.RuntimeObservationDigest,
					OwnershipObservationFingerprint: request.Receipt.OwnershipObservationFingerprint,
					LowPortObservationFingerprint:   request.Receipt.LowPortObservationFingerprint,
					ResolverObservationFingerprint:  request.Receipt.ResolverObservationFingerprint,
					TrustObservationFingerprint:     request.Receipt.TrustObservationFingerprint,
					LoopbackObservationDigest:       request.Receipt.LoopbackObservationDigest,
					VerifiedAt:                      request.Receipt.VerifiedAt,
				}
				if err := tx.Create(&receipt).Error; err != nil {
					return fmt.Errorf("create global network release effects receipt: %w", err)
				}
				updated := tx.Model(&globalNetworkReleasePlanRow{}).
					Where(
						"id = ? AND operation_id = ? AND phase = ? AND checkpoint_revision = ?",
						1,
						string(request.OperationID),
						string(GlobalNetworkReleasePlanPhaseVerifyEffects),
						int(request.CheckpointRevision),
					).
					Updates(map[string]any{
						"phase":               string(GlobalNetworkReleasePlanPhaseOwnership),
						"checkpoint_revision": int(checkpoint),
					})
				if updated.Error != nil {
					return fmt.Errorf("advance global network release effects plan: %w", updated.Error)
				}
				if updated.RowsAffected != 1 {
					return fmt.Errorf("global network release effects checkpoint compare-and-swap did not match")
				}
				plan.Phase = GlobalNetworkReleasePlanPhaseOwnership
				plan.CheckpointRevision = checkpoint
				plan.EffectsReceipt = &request.Receipt
				result = plan
				return nil
			case GlobalNetworkReleasePlanPhaseOwnership:
				if plan.EffectsReceipt == nil ||
					request.CheckpointRevision != plan.EffectsReceipt.SourceCheckpointRevision ||
					request.Receipt != *plan.EffectsReceipt {
					return fmt.Errorf("global network release effects replay receipt differs from committed receipt")
				}
				result = plan
				return nil
			default:
				return fmt.Errorf(
					"global network release effects advance requires plan phase %q or %q, found %q",
					GlobalNetworkReleasePlanPhaseVerifyEffects,
					GlobalNetworkReleasePlanPhaseOwnership,
					plan.Phase,
				)
			}
		},
		func(tx *gorm.DB) error {
			if err := validateGlobalNetworkReleaseMutationOwner(
				tx,
				request.OperationID,
				GlobalNetworkReleasePlanPhaseOwnership,
			); err != nil {
				return err
			}
			return validateCommittedGlobalNetworkReleaseEffectsReceipt(tx, request)
		},
	)
	if err != nil {
		return GlobalNetworkReleasePlanRecord{}, fmt.Errorf("advance global network release effects: %w", err)
	}
	return result.Clone(), nil
}

// validateCommittedGlobalNetworkReleaseEffectsReceipt proves post-write validation retained the exact caller-bound receipt.
func validateCommittedGlobalNetworkReleaseEffectsReceipt(
	tx *gorm.DB,
	request AdvanceGlobalNetworkReleaseEffectsRequest,
) error {
	plan, err := readValidatedGlobalNetworkReleasePlanForRuntimeAdvance(tx, request.OperationID)
	if err != nil {
		return err
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseOwnership || plan.EffectsReceipt == nil {
		return corruptGlobalNetworkReleasePlan(
			request.OperationID,
			fmt.Errorf("effects advance did not retain an effect-verification receipt"),
		)
	}
	if *plan.EffectsReceipt != request.Receipt {
		return corruptGlobalNetworkReleasePlan(
			request.OperationID,
			fmt.Errorf("committed effects receipt differs from request"),
		)
	}
	return nil
}

// readOptionalGlobalNetworkReleaseEffectsReceipt reads the singleton receipt without permitting malformed multiplicity to look absent.
func readOptionalGlobalNetworkReleaseEffectsReceipt(
	tx *gorm.DB,
	operationID domain.OperationID,
) (globalNetworkReleaseEffectsReceiptRow, bool, error) {
	var rows []globalNetworkReleaseEffectsReceiptRow
	if err := tx.Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return globalNetworkReleaseEffectsReceiptRow{}, false, fmt.Errorf("read global network release effects receipt: %w", err)
	}
	if len(rows) > 1 {
		return globalNetworkReleaseEffectsReceiptRow{}, false, corruptGlobalNetworkReleasePlan(
			operationID,
			fmt.Errorf("effects receipt singleton contains %d rows, expected at most 1", len(rows)),
		)
	}
	if len(rows) == 0 {
		return globalNetworkReleaseEffectsReceiptRow{}, false, nil
	}
	return rows[0], true, nil
}

// globalNetworkReleaseEffectsReceiptFromRow validates one persisted receipt against its release plan.
func globalNetworkReleaseEffectsReceiptFromRow(
	row globalNetworkReleaseEffectsReceiptRow,
	plan GlobalNetworkReleasePlanRecord,
) (GlobalNetworkReleaseEffectsReceipt, error) {
	if row.ID != 1 {
		return GlobalNetworkReleaseEffectsReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("effects receipt singleton ID is %d, expected 1", row.ID),
		)
	}
	if row.OperationID != string(plan.Operation.Operation.ID) {
		return GlobalNetworkReleaseEffectsReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("effects receipt belongs to operation %q", row.OperationID),
		)
	}
	source, err := modelIntToSequence(
		"global network release effects receipt source checkpoint revision",
		row.SourceCheckpointRevision,
	)
	if err != nil {
		return GlobalNetworkReleaseEffectsReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	receipt := GlobalNetworkReleaseEffectsReceipt{
		SourceCheckpointRevision:        source,
		RuntimeObservationDigest:        row.RuntimeObservationDigest,
		OwnershipObservationFingerprint: row.OwnershipObservationFingerprint,
		LowPortObservationFingerprint:   row.LowPortObservationFingerprint,
		ResolverObservationFingerprint:  row.ResolverObservationFingerprint,
		TrustObservationFingerprint:     row.TrustObservationFingerprint,
		LoopbackObservationDigest:       row.LoopbackObservationDigest,
		VerifiedAt:                      row.VerifiedAt,
	}
	if err := receipt.Validate(); err != nil {
		return GlobalNetworkReleaseEffectsReceipt{}, corruptGlobalNetworkReleasePlan(plan.Operation.Operation.ID, err)
	}
	if receipt.OwnershipObservationFingerprint != plan.Authority.ExpectedOwnershipFingerprint {
		return GlobalNetworkReleaseEffectsReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("effects receipt ownership observation fingerprint does not match retained authority"),
		)
	}
	if plan.LoopbackReceipt == nil {
		return GlobalNetworkReleaseEffectsReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("effects receipt has no required loopback receipt"),
		)
	}
	if receipt.VerifiedAt.Before(plan.LoopbackReceipt.VerifiedAt) {
		return GlobalNetworkReleaseEffectsReceipt{}, corruptGlobalNetworkReleasePlan(
			plan.Operation.Operation.ID,
			fmt.Errorf("effects receipt verification precedes loopback receipt"),
		)
	}
	return receipt, nil
}

// validateGlobalNetworkReleaseEffectsReceipt enforces the receipt's ordered relationship to the current release phase.
func validateGlobalNetworkReleaseEffectsReceipt(
	tx *gorm.DB,
	plan GlobalNetworkReleasePlanRecord,
) (*GlobalNetworkReleaseEffectsReceipt, error) {
	row, found, err := readOptionalGlobalNetworkReleaseEffectsReceipt(tx, plan.Operation.Operation.ID)
	if err != nil {
		return nil, err
	}
	switch plan.Phase {
	case GlobalNetworkReleasePlanPhaseRuntimeRelease,
		GlobalNetworkReleasePlanPhaseLowPorts,
		GlobalNetworkReleasePlanPhaseResolver,
		GlobalNetworkReleasePlanPhaseTrust,
		GlobalNetworkReleasePlanPhaseLoopbacks,
		GlobalNetworkReleasePlanPhaseVerifyEffects:
		if found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("plan phase %q retains a premature effects receipt", plan.Phase),
			)
		}
		return nil, nil
	case GlobalNetworkReleasePlanPhaseOwnership:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("release phase %q has no effects receipt", plan.Phase),
			)
		}
		receipt, err := globalNetworkReleaseEffectsReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf(
					"effects receipt source checkpoint revision %d does not precede ownership checkpoint revision %d",
					receipt.SourceCheckpointRevision,
					plan.CheckpointRevision,
				),
			)
		}
		return &receipt, nil
	case GlobalNetworkReleasePlanPhaseProjection:
		if !found {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf("release phase %q has no effects receipt", plan.Phase),
			)
		}
		receipt, err := globalNetworkReleaseEffectsReceiptFromRow(row, plan)
		if err != nil {
			return nil, err
		}
		if receipt.SourceCheckpointRevision+1 >= plan.CheckpointRevision {
			return nil, corruptGlobalNetworkReleasePlan(
				plan.Operation.Operation.ID,
				fmt.Errorf(
					"effects receipt source checkpoint revision %d does not precede later %q checkpoint revision %d",
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
