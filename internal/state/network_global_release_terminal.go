package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// GlobalNetworkReleaseTerminalRecord retains only the successful release replay fences.
type GlobalNetworkReleaseTerminalRecord struct {
	// Operation is the succeeded global network-release operation retained for history.
	Operation OperationRecord
	// OwnerIdentity is the original machine-owner identity released by this operation.
	OwnerIdentity string
	// SourceCheckpointRevision is the ownership checkpoint that admitted the terminal record.
	SourceCheckpointRevision domain.Sequence
	// NetworkRevision is the released network aggregate revision.
	NetworkRevision domain.Sequence
}

// globalNetworkReleaseTerminalRow is the private persistence shape for a completed release.
type globalNetworkReleaseTerminalRow struct {
	OperationID              string `gorm:"column:operation_id"`
	OwnerIdentity            string `gorm:"column:owner_identity"`
	SourceCheckpointRevision int    `gorm:"column:source_checkpoint_revision"`
	NetworkRevision          int    `gorm:"column:network_revision"`
}

// TableName returns the durable global release terminal table name.
func (globalNetworkReleaseTerminalRow) TableName() string {
	return "network_global_release_terminals"
}

// ReadGlobalNetworkReleaseTerminal restores one completed global release without retaining authority payloads.
func (journal *OperationJournal) ReadGlobalNetworkReleaseTerminal(ctx context.Context, operationID domain.OperationID) (GlobalNetworkReleaseTerminalRecord, bool, error) {
	if err := operationID.Validate(); err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, err
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, err
	}
	builder, err := journal.operations.WithContext(ctx).Builder()
	if err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, fmt.Errorf("open global network release terminal: %w", err)
	}
	var result GlobalNetworkReleaseTerminalRecord
	var found bool
	err = builder.Transaction(func(tx *gorm.DB) error {
		var readErr error
		result, found, readErr = readValidatedGlobalNetworkReleaseTerminal(tx, operationID)
		return readErr
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, fmt.Errorf("read global network release terminal: %w", err)
	}
	return result, found, nil
}

// readValidatedGlobalNetworkReleaseTerminal checks the compact terminal boundary and its succeeded operation owner.
func readValidatedGlobalNetworkReleaseTerminal(tx *gorm.DB, operationID domain.OperationID) (GlobalNetworkReleaseTerminalRecord, bool, error) {
	var rows []globalNetworkReleaseTerminalRow
	if err := tx.Where("operation_id = ?", string(operationID)).Limit(2).Find(&rows).Error; err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, fmt.Errorf("read global network release terminal: %w", err)
	}
	if len(rows) > 1 {
		return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, fmt.Errorf("operation has %d terminal records, expected one", len(rows)))
	}
	operationRow, operationFound, err := findOperationByID(tx, operationID)
	if err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, err
	}
	if len(rows) == 0 {
		if operationFound {
			operation, conversionErr := operationRecordFromModel(operationRow)
			if conversionErr != nil {
				return GlobalNetworkReleaseTerminalRecord{}, false, conversionErr
			}
			if isSucceededGlobalNetworkRelease(operation) {
				return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, fmt.Errorf("succeeded global release operation has no terminal record"))
			}
		}
		return GlobalNetworkReleaseTerminalRecord{}, false, nil
	}
	if !operationFound {
		return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, fmt.Errorf("operation owner is missing"))
	}
	operation, err := operationRecordFromModel(operationRow)
	if err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, err
	}
	if !isSucceededGlobalNetworkRelease(operation) {
		return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, fmt.Errorf("terminal owner is not a succeeded global network release operation"))
	}
	if err := validateGlobalNetworkReleaseTerminalOwner(rows[0].OwnerIdentity); err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, err)
	}
	sourceCheckpointRevision, err := modelIntToSequence("global network release terminal source checkpoint revision", rows[0].SourceCheckpointRevision)
	if err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, err)
	}
	networkRevision, err := modelIntToSequence("global network release terminal network revision", rows[0].NetworkRevision)
	if err != nil {
		return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, err)
	}
	if sourceCheckpointRevision >= operation.Revision || networkRevision >= operation.Revision {
		return GlobalNetworkReleaseTerminalRecord{}, false, corruptGlobalNetworkReleaseTerminal(operationID, fmt.Errorf("terminal replay fences must precede succeeded operation revision"))
	}
	return GlobalNetworkReleaseTerminalRecord{
		Operation:                operation,
		OwnerIdentity:            rows[0].OwnerIdentity,
		SourceCheckpointRevision: sourceCheckpointRevision,
		NetworkRevision:          networkRevision,
	}, true, nil
}

// isSucceededGlobalNetworkRelease identifies the sole terminal operation shape that may own a release record.
func isSucceededGlobalNetworkRelease(operation OperationRecord) bool {
	return operation.Operation.Kind == domain.OperationKindNetworkRelease &&
		operation.Operation.ProjectID == "" &&
		operation.Operation.State == domain.OperationSucceeded &&
		operation.Operation.Phase == globalNetworkReleaseSucceededPhase
}

// sameGlobalNetworkReleaseTerminalOperation compares terminal replay operations by value because SQLite canonicalization may change time location identity.
func sameGlobalNetworkReleaseTerminalOperation(left, right OperationRecord) bool {
	leftOperation := left.Operation
	rightOperation := right.Operation
	return left.Revision == right.Revision &&
		leftOperation.ID == rightOperation.ID &&
		leftOperation.IntentID == rightOperation.IntentID &&
		leftOperation.Kind == rightOperation.Kind &&
		leftOperation.ProjectID == rightOperation.ProjectID &&
		leftOperation.State == rightOperation.State &&
		leftOperation.Phase == rightOperation.Phase &&
		operationProblemsEqual(leftOperation.Problem, rightOperation.Problem) &&
		leftOperation.RequestedAt.Equal(rightOperation.RequestedAt) &&
		operationTimesEqual(leftOperation.StartedAt, rightOperation.StartedAt) &&
		operationTimesEqual(leftOperation.FinishedAt, rightOperation.FinishedAt)
}

// validateGlobalNetworkReleaseTerminalOwner preserves the established durable owner-identity bounds without retaining authority.
func validateGlobalNetworkReleaseTerminalOwner(value string) error {
	if len(value) == 0 || len(value) > 256 || value != strings.TrimSpace(value) {
		return fmt.Errorf("global network release terminal owner identity is invalid")
	}
	for _, character := range value {
		if !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
			return fmt.Errorf("global network release terminal owner identity is invalid")
		}
	}
	return nil
}

// corruptGlobalNetworkReleaseTerminal identifies corruption in compact successful release history.
func corruptGlobalNetworkReleaseTerminal(operationID domain.OperationID, cause error) error {
	return corruptStateError("global network release terminal", string(operationID), cause)
}
