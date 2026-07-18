//go:build !integration

package migrations

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// atomicityTestMigration supplies transaction-aware schema callbacks to the SQLite migration tests.
type atomicityTestMigration struct {
	name string
	up   func(*gorm.DB) error
	down func(*gorm.DB) error
}

// Name returns the deterministic ledger identity used by the test migration.
func (migration *atomicityTestMigration) Name() string {
	return migration.name
}

// App identifies the test migration's application stream.
func (*atomicityTestMigration) App() string {
	return "app"
}

// Connection identifies the test migration's application-local connection.
func (*atomicityTestMigration) Connection() string {
	return "default"
}

// DatabaseConnection identifies the generated database registry connection.
func (*atomicityTestMigration) DatabaseConnection() string {
	return "default"
}

// SourcePath returns the deterministic embedded-style path used in the ledger.
func (migration *atomicityTestMigration) SourcePath() string {
	return "app/default/" + migration.name
}

// Driver restricts the test migration to the SQLite transaction path.
func (*atomicityTestMigration) Driver() string {
	return "sqlite"
}

// Up delegates the forward schema change to the test case.
func (migration *atomicityTestMigration) Up(databaseConnection *gorm.DB) error {
	return migration.up(databaseConnection)
}

// Down delegates the reverse schema change to the test case.
func (migration *atomicityTestMigration) Down(databaseConnection *gorm.DB) error {
	return migration.down(databaseConnection)
}

// TestSQLiteApplyMigrationIsAtomic verifies neither failed DDL nor failed ledger writes leave partial schema.
func TestSQLiteApplyMigrationIsAtomic(t *testing.T) {
	t.Run("schema failure", func(t *testing.T) {
		databaseConnection := newMigrationAtomicityDatabase(t)
		failure := errors.New("schema failed after creating a table")
		migration := &atomicityTestMigration{
			name: "atomic_schema_failure",
			up: func(tx *gorm.DB) error {
				if err := tx.Exec("CREATE TABLE atomic_schema_failure (id INTEGER PRIMARY KEY)").Error; err != nil {
					return err
				}
				return failure
			},
			down: func(*gorm.DB) error { return nil },
		}

		err := applySQLiteMigration(databaseConnection, migration, migrationAtomicityRecord(migration))
		if !errors.Is(err, failure) {
			t.Fatalf("applySQLiteMigration() error = %v, want %v", err, failure)
		}
		assertAtomicityMigrationAbsent(t, databaseConnection, migration.Name(), "atomic_schema_failure")
	})

	t.Run("ledger failure", func(t *testing.T) {
		databaseConnection := newMigrationAtomicityDatabase(t)
		if err := databaseConnection.Exec(`CREATE TRIGGER reject_migration_insert
			BEFORE INSERT ON migrations
			BEGIN
				SELECT RAISE(ABORT, 'ledger insert rejected');
			END`).Error; err != nil {
			t.Fatalf("create rejecting insert trigger: %v", err)
		}
		migration := &atomicityTestMigration{
			name: "atomic_ledger_failure",
			up: func(tx *gorm.DB) error {
				return tx.Exec("CREATE TABLE atomic_ledger_failure (id INTEGER PRIMARY KEY)").Error
			},
			down: func(*gorm.DB) error { return nil },
		}

		if err := applySQLiteMigration(databaseConnection, migration, migrationAtomicityRecord(migration)); err == nil {
			t.Fatal("applySQLiteMigration() accepted a rejected ledger insert")
		}
		assertAtomicityMigrationAbsent(t, databaseConnection, migration.Name(), "atomic_ledger_failure")
	})
}

// TestSQLiteRollbackMigrationIsAtomic verifies failed reverse DDL and failed ledger removal restore applied state.
func TestSQLiteRollbackMigrationIsAtomic(t *testing.T) {
	t.Run("schema failure", func(t *testing.T) {
		databaseConnection := newMigrationAtomicityDatabase(t)
		failure := errors.New("reverse schema failed after dropping a table")
		migration := &atomicityTestMigration{
			name: "atomic_reverse_schema_failure",
			up:   func(*gorm.DB) error { return nil },
			down: func(tx *gorm.DB) error {
				if err := tx.Exec("DROP TABLE atomic_reverse_schema_failure").Error; err != nil {
					return err
				}
				return failure
			},
		}
		seedAtomicityMigration(t, databaseConnection, migration)

		err := rollbackSQLiteMigration(databaseConnection, migration)
		if !errors.Is(err, failure) {
			t.Fatalf("rollbackSQLiteMigration() error = %v, want %v", err, failure)
		}
		assertAtomicityMigrationPresent(t, databaseConnection, migration.Name(), "atomic_reverse_schema_failure")
	})

	t.Run("ledger failure", func(t *testing.T) {
		databaseConnection := newMigrationAtomicityDatabase(t)
		migration := &atomicityTestMigration{
			name: "atomic_reverse_ledger_failure",
			up:   func(*gorm.DB) error { return nil },
			down: func(tx *gorm.DB) error {
				return tx.Exec("DROP TABLE atomic_reverse_ledger_failure").Error
			},
		}
		seedAtomicityMigration(t, databaseConnection, migration)
		if err := databaseConnection.Exec(`CREATE TRIGGER reject_migration_delete
			BEFORE DELETE ON migrations
			BEGIN
				SELECT RAISE(ABORT, 'ledger delete rejected');
			END`).Error; err != nil {
			t.Fatalf("create rejecting delete trigger: %v", err)
		}

		if err := rollbackSQLiteMigration(databaseConnection, migration); err == nil {
			t.Fatal("rollbackSQLiteMigration() accepted a rejected ledger deletion")
		}
		assertAtomicityMigrationPresent(t, databaseConnection, migration.Name(), "atomic_reverse_ledger_failure")
	})
}

// TestSQLiteMigrationTransactionCommitsSchemaAndLedger verifies the successful path changes both sides together.
func TestSQLiteMigrationTransactionCommitsSchemaAndLedger(t *testing.T) {
	databaseConnection := newMigrationAtomicityDatabase(t)
	migration := &atomicityTestMigration{
		name: "atomic_success",
		up: func(tx *gorm.DB) error {
			return tx.Exec("CREATE TABLE atomic_success (id INTEGER PRIMARY KEY)").Error
		},
		down: func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE atomic_success").Error
		},
	}

	if err := applySQLiteMigration(databaseConnection, migration, migrationAtomicityRecord(migration)); err != nil {
		t.Fatalf("applySQLiteMigration() error = %v", err)
	}
	assertAtomicityMigrationPresent(t, databaseConnection, migration.Name(), "atomic_success")

	if err := rollbackSQLiteMigration(databaseConnection, migration); err != nil {
		t.Fatalf("rollbackSQLiteMigration() error = %v", err)
	}
	assertAtomicityMigrationAbsent(t, databaseConnection, migration.Name(), "atomic_success")
}

// newMigrationAtomicityDatabase opens an isolated file-backed database so transaction tests use one durable SQLite schema.
func newMigrationAtomicityDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	databaseConnection, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open SQLite migration database: %v", err)
	}
	if err := ensureMigrationsTable(databaseConnection); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	sqlDatabase, err := databaseConnection.DB()
	if err != nil {
		t.Fatalf("open SQLite connection pool: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDatabase.Close(); err != nil {
			t.Errorf("close SQLite migration database: %v", err)
		}
	})

	return databaseConnection
}

// migrationAtomicityRecord builds the exact ledger row committed beside a test schema.
func migrationAtomicityRecord(migration Migration) migrationRecord {
	return migrationRecord{
		Name:       migration.Name(),
		App:        migration.App(),
		Connection: migration.Connection(),
		SourcePath: migration.SourcePath(),
		AppliedAt:  time.Now().UTC(),
	}
}

// seedAtomicityMigration creates the schema and ledger state expected before rollback.
func seedAtomicityMigration(t *testing.T, databaseConnection *gorm.DB, migration Migration) {
	t.Helper()
	if err := databaseConnection.Exec("CREATE TABLE " + migration.Name() + " (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create applied test table: %v", err)
	}
	record := migrationAtomicityRecord(migration)
	if err := databaseConnection.Table("migrations").Create(&record).Error; err != nil {
		t.Fatalf("create applied migration record: %v", err)
	}
}

// assertAtomicityMigrationAbsent verifies a transaction left neither schema nor ledger state behind.
func assertAtomicityMigrationAbsent(t *testing.T, databaseConnection *gorm.DB, migrationName string, tableName string) {
	t.Helper()
	if databaseConnection.Migrator().HasTable(tableName) {
		t.Fatalf("migration retained table %q", tableName)
	}
	if countAtomicityMigrationRecords(t, databaseConnection, migrationName) != 0 {
		t.Fatalf("migration retained ledger record %q", migrationName)
	}
}

// assertAtomicityMigrationPresent verifies a transaction retained both schema and ledger state.
func assertAtomicityMigrationPresent(t *testing.T, databaseConnection *gorm.DB, migrationName string, tableName string) {
	t.Helper()
	if !databaseConnection.Migrator().HasTable(tableName) {
		t.Fatalf("migration removed table %q", tableName)
	}
	if countAtomicityMigrationRecords(t, databaseConnection, migrationName) != 1 {
		t.Fatalf("migration changed ledger record %q", migrationName)
	}
}

// countAtomicityMigrationRecords returns the number of ledger rows for one migration identity.
func countAtomicityMigrationRecords(t *testing.T, databaseConnection *gorm.DB, migrationName string) int64 {
	t.Helper()
	var count int64
	if err := databaseConnection.Table("migrations").Where("name = ?", migrationName).Count(&count).Error; err != nil {
		t.Fatalf("count migration records: %v", err)
	}

	return count
}
