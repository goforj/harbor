package migrations

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/logger"
	"github.com/goforj/harbor/internal/state"
	"gorm.io/gorm"
)

// TestCheckHarbordReadinessAcceptsMigrateCommandOutput verifies readiness recognizes the exact ledger and schema produced by GoForj's migration path.
func TestCheckHarbordReadinessAcceptsMigrateCommandOutput(t *testing.T) {
	connections, databaseConnection := newMigrationReadinessHarness(t)
	t.Setenv("FORJ_APP", "harbord")
	if err := NewMigrateCmd(logger.NewSilentLogger(), connections).Run(); err != nil {
		t.Fatalf("run harbord migrate: %v", err)
	}
	if !databaseConnection.Migrator().HasTable("harbor_state") {
		t.Fatal("harbord migrate did not create the final Harbor state schema")
	}

	if err := CheckHarbordReadiness(context.Background(), connections); err != nil {
		t.Fatalf("check migrated database: %v", err)
	}
}

// TestCheckHarbordReadinessAcceptsRecordedEmbeddedStream verifies startup trusts only matching embedded migration identities.
func TestCheckHarbordReadinessAcceptsRecordedEmbeddedStream(t *testing.T) {
	connections, databaseConnection := newMigrationReadinessHarness(t)
	createMigrationReadinessLedger(t, databaseConnection)
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		insertMigrationReadinessRecord(t, databaseConnection, migration.Name(), migration.App(), migration.Connection(), migration.SourcePath())
	}

	if err := CheckHarbordReadiness(nil, connections); err != nil {
		t.Fatalf("check ready database: %v", err)
	}
}

// TestCheckHarbordReadinessReportsPendingMigrations verifies a valid ledger cannot authorize startup before migrations run.
func TestCheckHarbordReadinessReportsPendingMigrations(t *testing.T) {
	connections, databaseConnection := newMigrationReadinessHarness(t)
	createMigrationReadinessLedger(t, databaseConnection)

	err := CheckHarbordReadiness(context.Background(), connections)
	var pending *PendingMigrationsError
	if !errors.As(err, &pending) {
		t.Fatalf("readiness error = %v, want PendingMigrationsError", err)
	}
	expected := selectMigrations("harbord", "default", "sqlite")
	expectedNames := make([]string, 0, len(expected))
	for _, migration := range expected {
		expectedNames = append(expectedNames, migration.Name())
	}
	if !reflect.DeepEqual(pending.Names, expectedNames) {
		t.Fatalf("pending migrations = %v, want %v", pending.Names, expectedNames)
	}
	if !strings.Contains(err.Error(), "harbord migrate") {
		t.Fatalf("pending error lacks migration guidance: %v", err)
	}
	if databaseConnection.Migrator().HasTable("harbor_state") {
		t.Fatal("readiness check applied a pending migration")
	}
}

// TestCheckHarbordReadinessRejectsMissingOrInvalidLedger verifies inspection fails closed without repairing schema.
func TestCheckHarbordReadinessRejectsMissingOrInvalidLedger(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{name: "missing table", want: "table is missing"},
		{
			name: "missing source path",
			schema: `CREATE TABLE migrations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				app TEXT NOT NULL,
				connection TEXT NOT NULL,
				applied_at DATETIME NOT NULL
			)`,
			want: `column "source_path" is missing`,
		},
		{
			name: "id is not primary key",
			schema: `CREATE TABLE migrations (
				id INTEGER NOT NULL,
				name TEXT NOT NULL UNIQUE,
				app TEXT NOT NULL,
				connection TEXT NOT NULL,
				source_path TEXT NOT NULL,
				applied_at DATETIME NOT NULL
			)`,
			want: `column "id" is not the primary key`,
		},
		{
			name: "nullable app",
			schema: `CREATE TABLE migrations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				app TEXT,
				connection TEXT NOT NULL,
				source_path TEXT NOT NULL,
				applied_at DATETIME NOT NULL
			)`,
			want: `column "app" must be NOT NULL`,
		},
		{
			name: "non-unique names",
			schema: `CREATE TABLE migrations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL,
				app TEXT NOT NULL,
				connection TEXT NOT NULL,
				source_path TEXT NOT NULL,
				applied_at DATETIME NOT NULL
			)`,
			want: "uniquely index migration names",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connections, databaseConnection := newMigrationReadinessHarness(t)
			if test.schema != "" {
				if err := databaseConnection.Exec(test.schema).Error; err != nil {
					t.Fatalf("create invalid ledger: %v", err)
				}
			}
			before := migrationReadinessSchema(t, databaseConnection)

			err := CheckHarbordReadiness(context.Background(), connections)
			var notReady *MigrationNotReadyError
			if !errors.As(err, &notReady) {
				t.Fatalf("readiness error = %v, want MigrationNotReadyError", err)
			}
			if !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "harbord migrate") {
				t.Fatalf("readiness error = %v, want %q and migration guidance", err, test.want)
			}
			after := migrationReadinessSchema(t, databaseConnection)
			if before != after {
				t.Fatalf("readiness changed schema\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

// TestCheckHarbordReadinessRejectsInvalidRecords verifies a matching name cannot hide corrupt migration identity data.
func TestCheckHarbordReadinessRejectsInvalidRecords(t *testing.T) {
	tests := []struct {
		name       string
		recordName string
		app        string
		connection string
		sourcePath string
		want       string
	}{
		{name: "empty name", app: "harbord", connection: "default", sourcePath: "harbord/default/example", want: "empty migration name"},
		{name: "wrong app", recordName: "expected", app: "other", connection: "default", sourcePath: "harbord/default/example", want: "ledger identity"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, databaseConnection := newMigrationReadinessHarness(t)
			createMigrationReadinessLedger(t, databaseConnection)
			expected := []Migration{&migration{
				app:        "harbord",
				name:       "expected",
				connection: "default",
				pathBase:   "harbord/default/example",
				driver:     "sqlite",
			}}
			insertMigrationReadinessRecord(t, databaseConnection, test.recordName, test.app, test.connection, test.sourcePath)

			err := checkHarbordMigrationReadiness(databaseConnection, expected)
			var notReady *MigrationNotReadyError
			if !errors.As(err, &notReady) {
				t.Fatalf("readiness error = %v, want MigrationNotReadyError", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readiness error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestCheckHarbordReadinessHonorsCancellation verifies database inspection preserves caller cancellation through the typed boundary.
func TestCheckHarbordReadinessHonorsCancellation(t *testing.T) {
	connections, databaseConnection := newMigrationReadinessHarness(t)
	createMigrationReadinessLedger(t, databaseConnection)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CheckHarbordReadiness(ctx, connections)
	var notReady *MigrationNotReadyError
	if !errors.As(err, &notReady) || !errors.Is(err, context.Canceled) {
		t.Fatalf("readiness error = %v, want wrapped context cancellation", err)
	}
}

// TestCheckHarbordReadinessRejectsConnectionFailure verifies daemon startup receives a typed error when the named database cannot open.
func TestCheckHarbordReadinessRejectsConnectionFailure(t *testing.T) {
	t.Setenv("DB_HARBORD_DRIVER", "not-compiled")
	t.Setenv("DB_HARBORD_DSN", filepath.Join(t.TempDir(), "harbor.db"))
	connections := database.NewConnections(inspects.NewManager())

	err := CheckHarbordReadiness(context.Background(), connections)
	var notReady *MigrationNotReadyError
	if !errors.As(err, &notReady) || !strings.Contains(err.Error(), "open harbord database") {
		t.Fatalf("readiness error = %v, want typed connection failure", err)
	}
}

// TestCheckHarbordReadinessRejectsMissingEmbeddedStream verifies a broken build cannot treat an empty registry as migrated.
func TestCheckHarbordReadinessRejectsMissingEmbeddedStream(t *testing.T) {
	connections, databaseConnection := newMigrationReadinessHarness(t)
	createMigrationReadinessLedger(t, databaseConnection)
	originalRegistry := registry
	registry = nil
	t.Cleanup(func() {
		registry = originalRegistry
	})

	err := CheckHarbordReadiness(context.Background(), connections)
	var notReady *MigrationNotReadyError
	if !errors.As(err, &notReady) || !strings.Contains(err.Error(), "no embedded harbord/default SQLite migrations") {
		t.Fatalf("readiness error = %v, want missing embedded stream failure", err)
	}
}

// TestMigrationReadinessErrorsHaveUsefulZeroValues verifies exported errors remain actionable when callers construct their zero values.
func TestMigrationReadinessErrorsHaveUsefulZeroValues(t *testing.T) {
	var pending PendingMigrationsError
	if message := pending.Error(); !strings.Contains(message, "migration names unavailable") || !strings.Contains(message, "harbord migrate") {
		t.Fatalf("zero pending error = %q", message)
	}
	var notReady MigrationNotReadyError
	if message := notReady.Error(); !strings.Contains(message, "readiness could not be established") || !strings.Contains(message, "harbord migrate") {
		t.Fatalf("zero not-ready error = %q", message)
	}
	if notReady.Unwrap() != nil {
		t.Fatalf("zero not-ready unwrap = %v, want nil", notReady.Unwrap())
	}
	var nilPending *PendingMigrationsError
	if message := nilPending.Error(); !strings.Contains(message, "migration names unavailable") {
		t.Fatalf("nil pending error = %q", message)
	}
	var nilNotReady *MigrationNotReadyError
	if message := nilNotReady.Error(); !strings.Contains(message, "readiness could not be established") {
		t.Fatalf("nil not-ready error = %q", message)
	}
	if nilNotReady.Unwrap() != nil {
		t.Fatalf("nil not-ready unwrap = %v, want nil", nilNotReady.Unwrap())
	}
}

// newMigrationReadinessHarness opens one isolated named SQLite database for read-only readiness checks.
func newMigrationReadinessHarness(t *testing.T) (*database.Connections, *gorm.DB) {
	t.Helper()
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", filepath.Join(t.TempDir(), "harbor.db"))
	if _, err := state.ConfigureDatabase(); err != nil {
		t.Fatalf("configure readiness database: %v", err)
	}
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close readiness database: %v", err)
		}
	})
	databaseConnection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open readiness database: %v", err)
	}
	return connections, databaseConnection
}

// createMigrationReadinessLedger creates the framework ledger shape without applying application migrations.
func createMigrationReadinessLedger(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	if err := databaseConnection.Exec(`CREATE TABLE migrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		app TEXT NOT NULL DEFAULT 'app',
		connection TEXT NOT NULL DEFAULT 'default',
		source_path TEXT NOT NULL DEFAULT '',
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
}

// insertMigrationReadinessRecord records one migration identity in the test ledger.
func insertMigrationReadinessRecord(t *testing.T, databaseConnection *gorm.DB, name, app, connection, sourcePath string) {
	t.Helper()
	if err := databaseConnection.Exec(
		"INSERT INTO migrations (name, app, connection, source_path) VALUES (?, ?, ?, ?)",
		name,
		app,
		connection,
		sourcePath,
	).Error; err != nil {
		t.Fatalf("insert migration ledger record: %v", err)
	}
}

// migrationReadinessSchema returns a stable schema fingerprint so tests catch accidental repair behavior.
func migrationReadinessSchema(t *testing.T, databaseConnection *gorm.DB) string {
	t.Helper()
	var definitions []string
	if err := databaseConnection.Raw("SELECT coalesce(sql, '') FROM sqlite_schema ORDER BY type, name").Scan(&definitions).Error; err != nil {
		t.Fatalf("read schema fingerprint: %v", err)
	}
	return strings.Join(definitions, "\n")
}
