package state

import (
	"database/sql"
	"fmt"
	"strconv"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const networkStateSingletonID = 1

// sqliteSchemaObject identifies the exact SQLite object occupying an optional table name.
type sqliteSchemaObject struct {
	Type string `gorm:"column:type"`
}

// networkSequenceRow preserves NULLs so weakened schemas cannot turn malformed ownership into zero values.
type networkSequenceRow struct {
	ID       sql.NullInt64 `gorm:"column:id"`
	Revision sql.NullInt64 `gorm:"column:revision"`
}

// readOptionalNetworkSequenceOwner reads the one network revision without requiring legacy databases to have the table.
func readOptionalNetworkSequenceOwner(tx *gorm.DB) (domain.Sequence, bool, error) {
	table := (&models.NetworkState{}).TableName()
	var objects []sqliteSchemaObject
	if err := tx.Raw(
		"SELECT type FROM sqlite_master WHERE name = ? ORDER BY type ASC",
		table,
	).Scan(&objects).Error; err != nil {
		return 0, false, fmt.Errorf("inspect optional network state schema: %w", err)
	}
	if len(objects) == 0 {
		return 0, false, nil
	}
	if len(objects) != 1 || objects[0].Type != "table" {
		return 0, false, corruptStateError(
			"network state",
			"schema",
			fmt.Errorf("network_state must be one table, found object types %v", networkSchemaObjectTypes(objects)),
		)
	}

	var rows []networkSequenceRow
	if err := tx.Table(table).Select("id", "revision").Order("id ASC").Find(&rows).Error; err != nil {
		return 0, false, corruptStateError("network state", "schema", fmt.Errorf("read singleton revision: %w", err))
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	if len(rows) != 1 {
		return 0, false, corruptStateError("network state", "1", fmt.Errorf("singleton contains %d rows, expected 1", len(rows)))
	}
	row := rows[0]
	if !row.ID.Valid {
		return 0, false, corruptStateError("network state", "NULL", fmt.Errorf("singleton ID must not be NULL"))
	}
	if row.ID.Int64 != networkStateSingletonID {
		return 0, false, corruptStateError("network state", strconv.FormatInt(row.ID.Int64, 10), fmt.Errorf("singleton ID must be 1"))
	}
	if !row.Revision.Valid {
		return 0, false, corruptStateError("network state", "1", fmt.Errorf("revision must not be NULL"))
	}
	if row.Revision.Int64 <= 0 {
		return 0, false, corruptStateError("network state", "1", fmt.Errorf("revision must be positive"))
	}
	revision := domain.Sequence(row.Revision.Int64)
	if revision > domain.MaximumSequence {
		return 0, false, corruptStateError("network state", "1", fmt.Errorf("revision exceeds the cross-client ordering range"))
	}
	return revision, true, nil
}

// networkSchemaObjectTypes keeps malformed-schema diagnostics deterministic.
func networkSchemaObjectTypes(objects []sqliteSchemaObject) []string {
	types := make([]string, 0, len(objects))
	for _, object := range objects {
		types = append(types, object.Type)
	}
	return types
}

// validateOptionalNetworkSequenceOwner proves the root owns one committed global revision and no other durable row reuses it.
func validateOptionalNetworkSequenceOwner(tx *gorm.DB, highWater domain.Sequence) error {
	revision, exists, err := readOptionalNetworkSequenceOwner(tx)
	if err != nil || !exists {
		return err
	}
	if err := validateVisibleSequence(highWater, revision, "network state", nil); err != nil {
		return err
	}
	return validateNetworkSequenceExclusivity(tx, revision)
}

// validateNetworkSequenceExclusivity checks every materialized global owner, including terminal operation headers.
func validateNetworkSequenceExclusivity(tx *gorm.DB, revision domain.Sequence) error {
	sequence := int(revision)
	owner := "network state"

	var projects []models.Project
	if err := tx.Select("id", "project_id").Where("revision = ?", sequence).Order("id ASC").Find(&projects).Error; err != nil {
		return fmt.Errorf("verify network project sequence owners: %w", err)
	}
	if len(projects) != 0 {
		return sequenceOwnerCollision(sequence, owner, "project "+strconv.Quote(projects[0].ProjectId))
	}

	var recents []models.RecentResource
	if err := tx.Select("id", "project_id", "resource_id").Where("sequence = ?", sequence).Order("id ASC").Find(&recents).Error; err != nil {
		return fmt.Errorf("verify network recent sequence owners: %w", err)
	}
	if len(recents) != 0 {
		recent := recents[0]
		return sequenceOwnerCollision(sequence, owner, fmt.Sprintf("recent resource %q/%q", recent.ProjectId, recent.ResourceId))
	}

	var operations []models.Operation
	if err := tx.Select("id").Where("revision = ?", sequence).Order("id ASC").Find(&operations).Error; err != nil {
		return fmt.Errorf("verify network operation sequence owners: %w", err)
	}
	if len(operations) != 0 {
		return sequenceOwnerCollision(sequence, owner, "operation "+strconv.Quote(operations[0].Id))
	}

	var transitions []models.OperationTransition
	if err := tx.Select("id", "operation_id", "ordinal").Where("sequence = ?", sequence).Order("id ASC").Find(&transitions).Error; err != nil {
		return fmt.Errorf("verify network transition sequence owners: %w", err)
	}
	if len(transitions) != 0 {
		transition := transitions[0]
		return sequenceOwnerCollision(
			sequence,
			owner,
			fmt.Sprintf("operation transition %q ordinal %d", transition.OperationId, transition.Ordinal),
		)
	}
	return nil
}

// validateNetworkSequenceCollision rejects a targeted owner whose existing revision belongs to the network root.
func validateNetworkSequenceCollision(tx *gorm.DB, sequence int, owner string) error {
	revision, exists, err := readOptionalNetworkSequenceOwner(tx)
	if err != nil || !exists {
		return err
	}
	if revision == domain.Sequence(sequence) {
		return sequenceOwnerCollision(sequence, owner, "network state")
	}
	return nil
}
