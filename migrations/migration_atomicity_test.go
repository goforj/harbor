package migrations

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// testMigration supplies transaction-aware schema callbacks to migration command tests.
type testMigration struct {
	name string
	up   func(*gorm.DB) error
	down func(*gorm.DB) error
}

// Name returns the deterministic ledger identity used by the test migration.
func (migration *testMigration) Name() string {
	return migration.name
}

// App identifies the test migration's application stream.
func (*testMigration) App() string {
	return "harbord"
}

// Connection identifies the test migration's application-local connection.
func (*testMigration) Connection() string {
	return "default"
}

// DatabaseConnection identifies the named database used by the test migration.
func (*testMigration) DatabaseConnection() string {
	return "harbord"
}

// SourcePath returns the deterministic embedded-style path used in the ledger.
func (migration *testMigration) SourcePath() string {
	return "harbord/default/" + migration.name
}

// Driver restricts the test migration to the SQLite harness.
func (*testMigration) Driver() string {
	return "sqlite"
}

// Up delegates the forward schema change to the test case.
func (migration *testMigration) Up(databaseConnection *gorm.DB) error {
	return migration.up(databaseConnection)
}

// Down delegates the reverse schema change to the test case.
func (migration *testMigration) Down(databaseConnection *gorm.DB) error {
	return migration.down(databaseConnection)
}

// TestApplyMigrationIsAtomic verifies neither failed DDL nor failed ledger writes leave partial schema.
func TestApplyMigrationIsAtomic(t *testing.T) {
	t.Run("schema failure", func(t *testing.T) {
		_, databaseConnection := newMigrationReadinessHarness(t)
		createMigrationReadinessLedger(t, databaseConnection)
		failure := errors.New("schema failed after creating a table")
		migration := &testMigration{
			name: "atomic_schema_failure",
			up: func(tx *gorm.DB) error {
				if err := tx.Exec("CREATE TABLE atomic_schema_failure (id INTEGER PRIMARY KEY)").Error; err != nil {
					return err
				}
				return failure
			},
			down: func(*gorm.DB) error { return nil },
		}

		err := applyMigration(databaseConnection, migration, testMigrationRecord(migration))
		if !errors.Is(err, failure) {
			t.Fatalf("applyMigration() error = %v, want %v", err, failure)
		}
		assertMigrationAbsent(t, databaseConnection, migration.Name(), "atomic_schema_failure")
	})

	t.Run("ledger failure", func(t *testing.T) {
		_, databaseConnection := newMigrationReadinessHarness(t)
		createMigrationReadinessLedger(t, databaseConnection)
		if err := databaseConnection.Exec(`CREATE TRIGGER reject_migration_insert
			BEFORE INSERT ON migrations
			BEGIN
				SELECT RAISE(ABORT, 'ledger insert rejected');
			END`).Error; err != nil {
			t.Fatalf("create rejecting insert trigger: %v", err)
		}
		migration := &testMigration{
			name: "atomic_ledger_failure",
			up: func(tx *gorm.DB) error {
				return tx.Exec("CREATE TABLE atomic_ledger_failure (id INTEGER PRIMARY KEY)").Error
			},
			down: func(*gorm.DB) error { return nil },
		}

		if err := applyMigration(databaseConnection, migration, testMigrationRecord(migration)); err == nil {
			t.Fatal("applyMigration() accepted a rejected ledger insert")
		}
		assertMigrationAbsent(t, databaseConnection, migration.Name(), "atomic_ledger_failure")
	})
}

// TestRollbackMigrationIsAtomic verifies failed reverse DDL and failed ledger removal restore applied state.
func TestRollbackMigrationIsAtomic(t *testing.T) {
	t.Run("schema failure", func(t *testing.T) {
		_, databaseConnection := newMigrationReadinessHarness(t)
		createMigrationReadinessLedger(t, databaseConnection)
		failure := errors.New("reverse schema failed after dropping a table")
		migration := &testMigration{
			name: "atomic_reverse_schema_failure",
			up:   func(*gorm.DB) error { return nil },
			down: func(tx *gorm.DB) error {
				if err := tx.Exec("DROP TABLE atomic_reverse_schema_failure").Error; err != nil {
					return err
				}
				return failure
			},
		}
		seedAppliedMigration(t, databaseConnection, migration)

		err := rollbackMigration(databaseConnection, migration)
		if !errors.Is(err, failure) {
			t.Fatalf("rollbackMigration() error = %v, want %v", err, failure)
		}
		assertMigrationPresent(t, databaseConnection, migration.Name(), "atomic_reverse_schema_failure")
	})

	t.Run("ledger failure", func(t *testing.T) {
		_, databaseConnection := newMigrationReadinessHarness(t)
		createMigrationReadinessLedger(t, databaseConnection)
		migration := &testMigration{
			name: "atomic_reverse_ledger_failure",
			up:   func(*gorm.DB) error { return nil },
			down: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE atomic_reverse_ledger_failure").Error
			},
		}
		seedAppliedMigration(t, databaseConnection, migration)
		if err := databaseConnection.Exec(`CREATE TRIGGER reject_migration_delete
			BEFORE DELETE ON migrations
			BEGIN
				SELECT RAISE(ABORT, 'ledger delete rejected');
			END`).Error; err != nil {
			t.Fatalf("create rejecting delete trigger: %v", err)
		}

		if err := rollbackMigration(databaseConnection, migration); err == nil {
			t.Fatal("rollbackMigration() accepted a rejected ledger deletion")
		}
		assertMigrationPresent(t, databaseConnection, migration.Name(), "atomic_reverse_ledger_failure")
	})
}

// TestMigrationTransactionCommitsSchemaAndLedger verifies the successful path changes both sides together.
func TestMigrationTransactionCommitsSchemaAndLedger(t *testing.T) {
	_, databaseConnection := newMigrationReadinessHarness(t)
	createMigrationReadinessLedger(t, databaseConnection)
	migration := &testMigration{
		name: "atomic_success",
		up: func(tx *gorm.DB) error {
			return tx.Exec("CREATE TABLE atomic_success (id INTEGER PRIMARY KEY)").Error
		},
		down: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE atomic_success").Error
		},
	}

	if err := applyMigration(databaseConnection, migration, testMigrationRecord(migration)); err != nil {
		t.Fatalf("applyMigration() error = %v", err)
	}
	assertMigrationPresent(t, databaseConnection, migration.Name(), "atomic_success")

	if err := rollbackMigration(databaseConnection, migration); err != nil {
		t.Fatalf("rollbackMigration() error = %v", err)
	}
	assertMigrationAbsent(t, databaseConnection, migration.Name(), "atomic_success")
}

// testMigrationRecord builds the exact ledger row committed beside a test schema.
func testMigrationRecord(migration Migration) migrationRecord {
	return migrationRecord{
		Name:       migration.Name(),
		App:        migration.App(),
		Connection: migration.Connection(),
		SourcePath: migration.SourcePath(),
		AppliedAt:  time.Now().UTC(),
	}
}

// seedAppliedMigration creates the schema and ledger state expected before rollback.
func seedAppliedMigration(t *testing.T, databaseConnection *gorm.DB, migration Migration) {
	t.Helper()
	if err := databaseConnection.Exec("CREATE TABLE " + migration.Name() + " (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create applied test table: %v", err)
	}
	record := testMigrationRecord(migration)
	if err := databaseConnection.Table("migrations").Create(&record).Error; err != nil {
		t.Fatalf("create applied migration record: %v", err)
	}
}

// assertMigrationAbsent verifies both sides of a failed forward migration rolled back.
func assertMigrationAbsent(t *testing.T, databaseConnection *gorm.DB, migrationName string, tableName string) {
	t.Helper()
	if databaseConnection.Migrator().HasTable(tableName) {
		t.Fatalf("failed migration retained table %q", tableName)
	}
	if countMigrationRecords(t, databaseConnection, migrationName) != 0 {
		t.Fatalf("failed migration retained ledger record %q", migrationName)
	}
}

// assertMigrationPresent verifies both sides of a failed rollback remain applied.
func assertMigrationPresent(t *testing.T, databaseConnection *gorm.DB, migrationName string, tableName string) {
	t.Helper()
	if !databaseConnection.Migrator().HasTable(tableName) {
		t.Fatalf("failed rollback removed table %q", tableName)
	}
	if countMigrationRecords(t, databaseConnection, migrationName) != 1 {
		t.Fatalf("failed rollback removed ledger record %q", migrationName)
	}
}

// countMigrationRecords returns the number of ledger rows for one migration identity.
func countMigrationRecords(t *testing.T, databaseConnection *gorm.DB, migrationName string) int64 {
	t.Helper()
	var count int64
	if err := databaseConnection.Table("migrations").Where("name = ?", migrationName).Count(&count).Error; err != nil {
		t.Fatalf("count migration records: %v", err)
	}

	return count
}
