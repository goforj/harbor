package state

import (
	"context"
	"fmt"
	"slices"

	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// networkSchemaObject identifies the SQLite schema object occupying one required network table name.
type networkSchemaObject struct {
	Name string `gorm:"column:name"`
	Type string `gorm:"column:type"`
}

// Network returns the complete durable network aggregate from one database instant.
func (store *Store) Network(ctx context.Context) (NetworkRecord, bool, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return NetworkRecord{}, false, err
	}
	builder, err := store.networkState.WithContext(ctx).Builder()
	if err != nil {
		return NetworkRecord{}, false, fmt.Errorf("open network state: %w", err)
	}

	var record NetworkRecord
	var initialized bool
	err = builder.Transaction(func(tx *gorm.DB) error {
		present, err := inspectNetworkSchema(tx)
		if err != nil || !present {
			return err
		}
		rows, err := readNetworkModelRows(tx)
		if err != nil {
			return err
		}
		read, exists, err := networkRecordFromModels(rows)
		if err != nil || !exists {
			return err
		}
		highWater, err := readSnapshotSequence(tx)
		if err != nil {
			return err
		}
		if err := validateVisibleSequence(highWater, read.Revision, "network state", nil); err != nil {
			return err
		}
		if err := validateNetworkSequenceExclusivity(tx, read.Revision); err != nil {
			return err
		}
		record = read
		initialized = true
		return nil
	})
	if err != nil {
		return NetworkRecord{}, false, fmt.Errorf("read network state: %w", err)
	}
	return record, initialized, nil
}

// inspectNetworkSchema distinguishes a legacy database from an incomplete migration without querying missing tables.
func inspectNetworkSchema(tx *gorm.DB) (bool, error) {
	tables := networkTableNames()
	var objects []networkSchemaObject
	if err := tx.Raw(
		"SELECT name, type FROM sqlite_master WHERE name IN ? ORDER BY name ASC, type ASC",
		tables,
	).Scan(&objects).Error; err != nil {
		return false, fmt.Errorf("inspect network schema: %w", err)
	}
	if len(objects) == 0 {
		return false, nil
	}

	objectTypes := make(map[string][]string, len(objects))
	for _, object := range objects {
		objectTypes[object.Name] = append(objectTypes[object.Name], object.Type)
	}
	missing := make([]string, 0, len(tables))
	for _, table := range tables {
		types := objectTypes[table]
		if len(types) == 0 {
			missing = append(missing, table)
			continue
		}
		if len(types) != 1 || types[0] != "table" {
			return false, corruptStateError(
				"network state",
				"schema",
				fmt.Errorf("%s must be one table, found object types %v", table, types),
			)
		}
	}
	if len(missing) != 0 {
		return false, corruptStateError(
			"network state",
			"schema",
			fmt.Errorf("network persistence schema is incomplete; missing tables %v", missing),
		)
	}
	return true, nil
}

// networkTableNames returns every table that must appear atomically in the durable network migration.
func networkTableNames() []string {
	tables := []string{
		(&models.NetworkState{}).TableName(),
		(&models.NetworkPoolCandidate{}).TableName(),
		(&models.NetworkSetupEvidence{}).TableName(),
		(&models.NetworkSharedListener{}).TableName(),
		(&models.LoopbackAddressLease{}).TableName(),
		(&models.PublicEndpointLease{}).TableName(),
		(&models.NetworkProjectRelease{}).TableName(),
	}
	slices.Sort(tables)
	return tables
}

// readNetworkModelRows loads every network-owned row and its referential owners through the caller's transaction.
func readNetworkModelRows(tx *gorm.DB) (networkModelRows, error) {
	var rows networkModelRows
	if err := tx.Order("id ASC").Find(&rows.States).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read network singleton: %w", err)
	}
	if err := tx.Order("ordinal ASC").Order("address ASC").Order("id ASC").Find(&rows.Candidates).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read network pool candidates: %w", err)
	}
	if err := tx.Order("component ASC").Order("id ASC").Find(&rows.SetupEvidence).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read network setup evidence: %w", err)
	}
	if err := tx.Order("kind ASC").Order("id ASC").Find(&rows.Listeners).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read network shared listeners: %w", err)
	}
	if err := tx.Order("address ASC").Order("id ASC").Find(&rows.Leases).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read loopback address leases: %w", err)
	}
	if err := tx.Order("hostname ASC").Order("project_id ASC").Order("endpoint_id ASC").Order("id ASC").Find(&rows.Endpoints).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read public endpoint leases: %w", err)
	}
	if err := tx.Order("source_project_id ASC").Order("id ASC").Find(&rows.Releases).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read network project releases: %w", err)
	}
	if len(rows.States) == 0 {
		return rows, nil
	}
	if err := tx.Select("id", "project_id", "state").Order("project_id ASC").Order("id ASC").Find(&rows.Projects).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read network projects: %w", err)
	}

	operationIDs := make([]string, 0, len(rows.Releases))
	knownOperationIDs := make(map[string]struct{}, len(rows.Releases))
	for _, release := range rows.Releases {
		if _, exists := knownOperationIDs[release.OperationId]; exists {
			continue
		}
		knownOperationIDs[release.OperationId] = struct{}{}
		operationIDs = append(operationIDs, release.OperationId)
	}
	if len(operationIDs) == 0 {
		return rows, nil
	}
	slices.Sort(operationIDs)
	if err := tx.
		Select("id", "kind", "project_id").
		Where("id IN ?", operationIDs).
		Order("id ASC").
		Find(&rows.ReleaseOwners).Error; err != nil {
		return networkModelRows{}, fmt.Errorf("read network release owners: %w", err)
	}
	return rows, nil
}
