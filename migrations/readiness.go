package migrations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/goforj/harbor/internal/database"
	"gorm.io/gorm"
)

const harbordMigrationGuidance = "run `harbord migrate` before starting harbord"

// PendingMigrationsError reports registered Harbor migrations that have not been recorded as applied.
type PendingMigrationsError struct {
	// Names lists pending migrations in application order.
	Names []string
}

// Error describes the pending migrations and the framework command that applies them.
func (err *PendingMigrationsError) Error() string {
	names := "migration names unavailable"
	if err != nil && len(err.Names) > 0 {
		names = strings.Join(err.Names, ", ")
	}
	return fmt.Sprintf("harbord database has pending migrations: %s; %s", names, harbordMigrationGuidance)
}

// MigrationNotReadyError reports that Harbor could not prove the migration ledger is valid.
type MigrationNotReadyError struct {
	// Cause preserves the database or ledger validation failure.
	Cause error
}

// Error describes why the migration ledger cannot authorize daemon startup.
func (err *MigrationNotReadyError) Error() string {
	reason := "readiness could not be established"
	if err != nil && err.Cause != nil {
		reason = err.Cause.Error()
	}
	return fmt.Sprintf("harbord database migrations are not ready: %s; %s", reason, harbordMigrationGuidance)
}

// Unwrap exposes the database or validation failure for callers that need its classification.
func (err *MigrationNotReadyError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// CheckHarbordReadiness proves every embedded harbord/default SQLite migration is recorded as applied without changing the database.
func CheckHarbordReadiness(ctx context.Context, connections *database.Connections) error {
	if ctx == nil {
		ctx = context.Background()
	}
	databaseConnection, err := connections.GetHarbord()
	if err != nil {
		return migrationNotReady(fmt.Errorf("open harbord database: %w", err))
	}
	databaseConnection = databaseConnection.WithContext(ctx)

	expected := selectMigrations("harbord", "default", "sqlite")
	if len(expected) == 0 {
		return migrationNotReady(errors.New("no embedded harbord/default SQLite migrations are registered"))
	}
	return checkHarbordMigrationReadiness(databaseConnection, expected)
}

// checkHarbordMigrationReadiness compares one validated migration ledger with the selected embedded stream.
func checkHarbordMigrationReadiness(databaseConnection *gorm.DB, expected []Migration) error {
	if err := validateMigrationLedgerSchema(databaseConnection); err != nil {
		return migrationNotReady(err)
	}

	var records []migrationReadinessRecord
	if err := databaseConnection.
		Raw("SELECT name, app, connection, source_path FROM migrations ORDER BY name ASC").
		Scan(&records).Error; err != nil {
		return migrationNotReady(fmt.Errorf("read migration ledger: %w", err))
	}

	recorded := make(map[string]migrationReadinessRecord, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.Name) == "" {
			return migrationNotReady(errors.New("migration ledger contains an empty migration name"))
		}
		recorded[record.Name] = record
	}

	pending := make([]string, 0)
	for _, migration := range expected {
		record, exists := recorded[migration.Name()]
		if !exists {
			pending = append(pending, migration.Name())
			continue
		}
		if record.App != migration.App() || record.Connection != migration.Connection() || record.SourcePath != migration.SourcePath() {
			return migrationNotReady(fmt.Errorf(
				"migration %q ledger identity is %s/%s at %q, expected %s/%s at %q",
				migration.Name(),
				record.App,
				record.Connection,
				record.SourcePath,
				migration.App(),
				migration.Connection(),
				migration.SourcePath(),
			))
		}
	}
	if len(pending) > 0 {
		sort.Strings(pending)
		return &PendingMigrationsError{Names: pending}
	}
	return nil
}

// migrationReadinessRecord contains only the immutable identity needed to match an applied migration.
type migrationReadinessRecord struct {
	Name       string
	App        string
	Connection string
	SourcePath string
}

// sqliteMigrationColumn captures the schema guarantees Harbor relies on before trusting migration rows.
type sqliteMigrationColumn struct {
	Name       string
	NotNull    int `gorm:"column:notnull"`
	PrimaryKey int `gorm:"column:pk"`
}

// validateMigrationLedgerSchema rejects missing or weakened migration ledgers without invoking the mutating migration setup path.
func validateMigrationLedgerSchema(databaseConnection *gorm.DB) error {
	var columns []sqliteMigrationColumn
	if err := databaseConnection.Raw("PRAGMA table_info('migrations')").Scan(&columns).Error; err != nil {
		return fmt.Errorf("inspect migration ledger schema: %w", err)
	}
	if len(columns) == 0 {
		return errors.New("migration ledger table is missing")
	}

	byName := make(map[string]sqliteMigrationColumn, len(columns))
	for _, column := range columns {
		byName[column.Name] = column
	}
	for _, name := range []string{"id", "name", "app", "connection", "source_path", "applied_at"} {
		column, exists := byName[name]
		if !exists {
			return fmt.Errorf("migration ledger column %q is missing", name)
		}
		if name == "id" {
			if column.PrimaryKey != 1 {
				return errors.New("migration ledger column \"id\" is not the primary key")
			}
			continue
		}
		if column.NotNull != 1 {
			return fmt.Errorf("migration ledger column %q must be NOT NULL", name)
		}
	}

	var uniqueNameIndexes int
	if err := databaseConnection.Raw(`
		SELECT COUNT(*)
		FROM (
			SELECT indexes.name
			FROM pragma_index_list('migrations') AS indexes
			JOIN pragma_index_info(indexes.name) AS columns
			WHERE indexes."unique" = 1 AND indexes.partial = 0
			GROUP BY indexes.name
			HAVING COUNT(*) = 1 AND MIN(columns.name) = 'name'
		) AS unique_name_indexes
	`).Scan(&uniqueNameIndexes).Error; err != nil {
		return fmt.Errorf("inspect migration ledger indexes: %w", err)
	}
	if uniqueNameIndexes == 0 {
		return errors.New("migration ledger must uniquely index migration names")
	}
	return nil
}

// migrationNotReady gives every ledger integrity failure the same typed startup boundary.
func migrationNotReady(cause error) error {
	return &MigrationNotReadyError{Cause: cause}
}
