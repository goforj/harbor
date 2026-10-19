package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"gorm.io/gorm"
)

// ErrNetworkSetupConfirmationUnavailable prevents resumption from inventing missing helper evidence.
var ErrNetworkSetupConfirmationUnavailable = errors.New("verified network setup confirmation is not retained")

// networkSetupConfirmationRow binds bounded helper postconditions to their existing immutable plan.
type networkSetupConfirmationRow struct {
	PlanID               int       `gorm:"column:plan_id"`
	OperationID          string    `gorm:"column:operation_id"`
	ApprovalRevision     int       `gorm:"column:approval_revision"`
	OwnershipFingerprint string    `gorm:"column:ownership_fingerprint"`
	EvidenceJSON         string    `gorm:"column:evidence_json"`
	VerifiedAt           time.Time `gorm:"column:verified_at"`
}

// TableName keeps recovery evidence separate from generated project and ownership models.
func (networkSetupConfirmationRow) TableName() string {
	return "network_setup_confirmation_receipts"
}

// RetainNetworkSetupConfirmation durably records independently verified effects before the completion transaction.
func (store *Store) RetainNetworkSetupConfirmation(ctx context.Context, request CompleteNetworkSetupRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	request = cloneCompleteNetworkSetupRequest(request)
	encoded, err := json.Marshal(request.HelperPoolEvidence)
	if err != nil || len(encoded) > helper.MaxResponseBytes {
		return fmt.Errorf("network setup confirmation evidence exceeds its protocol bound")
	}
	return store.mutations.mutate(normalizeContext(ctx), "retain network setup confirmation", func(tx *gorm.DB) error {
		plan, err := resolveNetworkSetupPoolPlan(tx, request.OperationID)
		if err != nil {
			return err
		}
		if plan.OperationRevision != request.ExpectedOperationRevision {
			return &StaleRevisionError{OperationID: request.OperationID, Expected: request.ExpectedOperationRevision, Actual: plan.OperationRevision}
		}
		operation, err := readNetworkSetupOperation(tx, request.OperationID)
		if err != nil {
			return err
		}
		if request.At.Before(operation.Operation.RequestedAt) {
			return fmt.Errorf("verified setup effects predate the pending approval")
		}
		if err := requireNetworkSetupCompletionMatchesPlan(request, plan.Ownership, plan.Pool); err != nil {
			return err
		}
		row := networkSetupConfirmationRow{PlanID: 1, OperationID: string(request.OperationID), ApprovalRevision: int(request.ExpectedOperationRevision), OwnershipFingerprint: request.ConfirmedOwnership.Fingerprint, EvidenceJSON: string(encoded), VerifiedAt: request.At}
		current, found, err := readNetworkSetupConfirmationRow(tx)
		if err != nil {
			return err
		}
		if found {
			if current.OperationID != row.OperationID || current.ApprovalRevision != row.ApprovalRevision || current.OwnershipFingerprint != row.OwnershipFingerprint {
				return fmt.Errorf("retained network setup confirmation belongs to different authority")
			}
			if current.EvidenceJSON == row.EvidenceJSON {
				return nil
			}
			if row.VerifiedAt.Before(current.VerifiedAt) {
				return fmt.Errorf("network setup readback predates retained verification")
			}
			if err := requireOneMutation(tx.Model(&networkSetupConfirmationRow{}).Where("plan_id = ? AND evidence_json = ?", 1, current.EvidenceJSON).Updates(map[string]any{"evidence_json": row.EvidenceJSON, "verified_at": row.VerifiedAt}), "refresh verified setup confirmation", row.OperationID); err != nil {
				return err
			}
		} else if err := requireOneCreate(tx.Create(&row), "retain verified setup confirmation", row.OperationID); err != nil {
			return err
		}
		_, err = allocateHarborSequence(tx)
		return err
	})
}

// NetworkSetupConfirmation reads only previously verified evidence for the exact pending approval.
func (store *Store) NetworkSetupConfirmation(ctx context.Context, operationID domain.OperationID, revision domain.Sequence) (helper.PoolMutationEvidence, error) {
	if err := operationID.Validate(); err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	if _, err := sequenceToModelInt("network setup confirmation revision", revision, false); err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	db, err := store.projects.WithContext(normalizeContext(ctx)).Builder()
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	var evidence helper.PoolMutationEvidence
	err = db.Transaction(func(tx *gorm.DB) error {
		plan, err := retainedNetworkSetupConfirmationPlan(tx, operationID, revision)
		if err != nil {
			return err
		}

		if plan.OperationRevision != revision {
			return &StaleRevisionError{OperationID: operationID, Expected: revision, Actual: plan.OperationRevision}
		}
		evidence, err = networkSetupConfirmationForPlan(tx, plan)
		return err
	})
	return evidence, err
}

// readNetworkSetupConfirmationRow distinguishes a verified receipt from a legacy schema or an uncompleted helper attempt.
func readNetworkSetupConfirmationRow(tx *gorm.DB) (networkSetupConfirmationRow, bool, error) {
	present, err := networkSetupConfirmationTableExists(tx)
	if err != nil {
		return networkSetupConfirmationRow{}, false, err
	}
	if !present {
		return networkSetupConfirmationRow{}, false, nil
	}
	var rows []networkSetupConfirmationRow
	if err := tx.Limit(2).Find(&rows).Error; err != nil {
		return networkSetupConfirmationRow{}, false, err
	}
	if len(rows) > 1 {
		return networkSetupConfirmationRow{}, false, fmt.Errorf("multiple network setup confirmation receipts")
	}
	if len(rows) == 0 {
		return networkSetupConfirmationRow{}, false, nil
	}
	return rows[0], true, nil
}

// networkSetupConfirmationForPlan rejects receipt substitution and malformed persisted evidence before resumption.
func networkSetupConfirmationForPlan(tx *gorm.DB, plan ticketissuer.PoolPlan) (helper.PoolMutationEvidence, error) {
	row, found, err := readNetworkSetupConfirmationRow(tx)
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	if !found {
		return helper.PoolMutationEvidence{}, ErrNetworkSetupConfirmationUnavailable
	}
	fingerprint, err := plan.Ownership.Fingerprint()
	if err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	if row.PlanID != 1 || row.OperationID != string(plan.OperationID) || row.ApprovalRevision != int(plan.OperationRevision) || row.OwnershipFingerprint != fingerprint {
		return helper.PoolMutationEvidence{}, fmt.Errorf("retained setup confirmation differs from the pending authority")
	}
	if err := validateStoredTime("network setup confirmation verification", row.VerifiedAt); err != nil {
		return helper.PoolMutationEvidence{}, err
	}
	if len(row.EvidenceJSON) > helper.MaxResponseBytes {
		return helper.PoolMutationEvidence{}, fmt.Errorf("retained setup confirmation exceeds its protocol bound")
	}
	var evidence helper.PoolMutationEvidence
	decoder := json.NewDecoder(bytes.NewBufferString(row.EvidenceJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return evidence, err
	}
	canonical, err := json.Marshal(evidence)
	if err != nil || string(canonical) != row.EvidenceJSON {
		return evidence, fmt.Errorf("retained setup confirmation is not canonical")
	}
	// The signed plan and native observations remain mandatory at final confirmation.
	if err := helper.WriteResponse(io.Discard, helper.Response{Version: helper.ProtocolVersion, OK: true, Result: &helper.OperationResult{Operation: helper.OperationEnsureLoopbackPool, PoolEvidence: &evidence}}); err != nil {
		return evidence, err
	}
	if evidence.Pool != plan.Pool.Prefix().String() {
		return evidence, fmt.Errorf("retained setup confirmation belongs to another pool")
	}
	return evidence, nil
}

// retainedNetworkSetupConfirmationPlan permits exact lost-response replay without opening fresh host authority.
func retainedNetworkSetupConfirmationPlan(tx *gorm.DB, id domain.OperationID, revision domain.Sequence) (ticketissuer.PoolPlan, error) {
	operation, err := readNetworkSetupOperation(tx, id)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	if operation.Operation.State != domain.OperationSucceeded {
		return resolveNetworkSetupPoolPlan(tx, id)
	}
	history, err := operationHistoryInTransaction(tx, operation)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	if len(history) != 5 || history[2].Sequence != revision {
		return ticketissuer.PoolPlan{}, fmt.Errorf("completed setup receipt crossed the approved revision")
	}
	projected, _, err := readMachineOwnershipProjectionInTransaction(tx)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	if !projected.Exists {
		return ticketissuer.PoolPlan{}, ErrNetworkSetupConfirmationUnavailable
	}
	pool, err := networkSetupIdentityPool(projected.Record.LoopbackPoolPrefix)
	if err != nil {
		return ticketissuer.PoolPlan{}, err
	}
	return ticketissuer.PoolPlan{OperationID: id, OperationRevision: revision, Ownership: projected.Record, Pool: pool}, nil
}

// retireNetworkSetupConfirmation removes only a terminal receipt when a new independently approved plan is staged.
func retireNetworkSetupConfirmation(tx *gorm.DB) error {
	receipt, found, err := readNetworkSetupConfirmationRow(tx)
	if err != nil || !found {
		return err
	}
	operation, err := readNetworkSetupOperation(tx, domain.OperationID(receipt.OperationID))
	if err != nil {
		return err
	}
	if operation.Operation.State != domain.OperationSucceeded {
		return fmt.Errorf("cannot retire a pending verified setup confirmation")
	}
	return requireOneMutation(tx.Where("plan_id = ? AND operation_id = ?", 1, receipt.OperationID).Delete(&networkSetupConfirmationRow{}), "retire completed setup confirmation", receipt.OperationID)
}

// networkSetupConfirmationTableExists keeps optional legacy projections distinct from a failed schema read.
func networkSetupConfirmationTableExists(tx *gorm.DB) (bool, error) {
	var count int64
	if err := tx.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", (networkSetupConfirmationRow{}).TableName()).Scan(&count).Error; err != nil {
		return false, err
	}
	return count == 1, nil
}

// DiscardNetworkSetupConfirmation retires only the exact pending proof after native readback establishes drift.
func (store *Store) DiscardNetworkSetupConfirmation(ctx context.Context, id domain.OperationID, revision domain.Sequence, evidence helper.PoolMutationEvidence) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("network setup confirmation revision", revision, false); err != nil {
		return err
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) > helper.MaxResponseBytes {
		return fmt.Errorf("invalid network setup confirmation evidence")
	}
	return store.mutations.mutate(normalizeContext(ctx), "discard changed setup confirmation", func(tx *gorm.DB) error {
		plan, err := resolveNetworkSetupPoolPlan(tx, id)
		if err != nil {
			return err
		}
		if plan.OperationRevision != revision {
			return &StaleRevisionError{OperationID: id, Expected: revision, Actual: plan.OperationRevision}
		}
		if _, err := networkSetupConfirmationForPlan(tx, plan); err != nil {
			return err
		}
		if err := requireOneMutation(tx.Where("plan_id = ? AND operation_id = ? AND approval_revision = ? AND evidence_json = ?", 1, string(id), int(revision), string(encoded)).Delete(&networkSetupConfirmationRow{}), "discard exact setup confirmation", string(id)); err != nil {
			return err
		}
		_, err = allocateHarborSequence(tx)
		return err
	})
}
