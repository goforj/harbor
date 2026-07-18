package migrations

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/state"
	"gorm.io/gorm"
)

// TestOperationJournalMigrationCreatesConstrainedSchema verifies the generated migration stream is the durable schema authority.
func TestOperationJournalMigrationCreatesConstrainedSchema(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	migration := operationJournalMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}

	for _, table := range []string{"operation_journal_state", "operations", "operation_transitions"} {
		if !databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("migration did not create %s", table)
		}
	}
	var sequence int
	if err := databaseConnection.Raw("SELECT sequence FROM operation_journal_state WHERE id = 1").Scan(&sequence).Error; err != nil {
		t.Fatalf("read journal sequence: %v", err)
	}
	if sequence != 0 {
		t.Fatalf("initial sequence = %d, want 0", sequence)
	}

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 123456789, time.UTC)
	if err := insertMigrationOperation(databaseConnection, "operation-01", "intent-01", "queued", requestedAt, nil, nil, nil, 1); err != nil {
		t.Fatalf("insert valid queued operation: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO operation_transitions
        (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
        VALUES (?, 1, NULL, 'queued', 'queued', ?, 1)`, "operation-01", requestedAt).Error; err != nil {
		t.Fatalf("insert valid queued transition: %v", err)
	}

	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operations
        (id, intent_id, kind, state, phase, requested_at, revision)
        VALUES ('operation-bad-state', 'intent-bad-state', 'project.start', 'unknown', 'queued', ?, 2)`, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operations
        (id, intent_id, kind, state, phase, requested_at, started_at, revision)
        VALUES ('operation-bad-shape', 'intent-bad-shape', 'project.start', 'queued', 'queued', ?, ?, 2)`, requestedAt, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operations
        (id, intent_id, kind, state, phase, problem_code, requested_at, revision)
        VALUES ('operation-partial-problem', 'intent-partial-problem', 'project.start', 'queued', 'queued', 'partial', ?, 2)`, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operations
        (id, intent_id, kind, state, phase, requested_at, revision)
        VALUES ('operation-duplicate-intent', 'intent-01', 'project.start', 'queued', 'queued', ?, 2)`, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operation_transitions
        (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
        VALUES ('operation-01', 2, 'queued', 'succeeded', 'complete', ?, 2)`, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operation_transitions
        (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
        VALUES ('missing-operation', 1, NULL, 'queued', 'queued', ?, 2)`, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operations
        (id, intent_id, kind, state, phase, requested_at, revision)
        VALUES ('operation-duplicate-revision', 'intent-duplicate-revision', 'project.start', 'queued', 'queued', ?, 1)`, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operation_transitions
        (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence)
        VALUES ('operation-01', 2, 'queued', 'running', 'running', ?, 1)`, requestedAt)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback operation journal migration: %v", err)
	}
	for _, table := range []string{"operation_journal_state", "operations", "operation_transitions"} {
		if databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("rollback retained %s", table)
		}
	}
}

// TestOperationJournalMigrationAcceptsEveryDomainStateShape verifies SQL constraints remain aligned with valid lifecycle records.
func TestOperationJournalMigrationAcceptsEveryDomainStateShape(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}

	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	problemCode := "apply_failed"
	states := []struct {
		name       string
		startedAt  *time.Time
		finishedAt *time.Time
		problem    *string
	}{
		{name: "queued"},
		{name: "running", startedAt: &startedAt},
		{name: "requires_approval", startedAt: &startedAt},
		{name: "succeeded", startedAt: &startedAt, finishedAt: &finishedAt},
		{name: "failed", startedAt: &startedAt, finishedAt: &finishedAt, problem: &problemCode},
		{name: "cancelled", finishedAt: &finishedAt},
	}
	for index, lifecycle := range states {
		if err := insertMigrationOperation(
			databaseConnection,
			"operation-"+lifecycle.name,
			"intent-"+lifecycle.name,
			lifecycle.name,
			requestedAt,
			lifecycle.startedAt,
			lifecycle.finishedAt,
			lifecycle.problem,
			index+1,
		); err != nil {
			t.Fatalf("insert %s operation at index %d: %v", lifecycle.name, index, err)
		}
	}
}

// openOperationMigrationDatabase opens an isolated named GoForj connection with Harbor's runtime SQLite policy.
func openOperationMigrationDatabase(t *testing.T) (*database.Connections, *gorm.DB) {
	t.Helper()
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", filepath.Join(t.TempDir(), "harbor.db"))
	if _, err := state.ConfigureDatabase(); err != nil {
		t.Fatalf("configure database: %v", err)
	}
	connections := database.NewConnections(inspects.NewManager())
	databaseConnection, err := connections.GetHarbord()
	if err != nil {
		closeOperationMigrationDatabase(t, connections)
		t.Fatalf("open named database: %v", err)
	}
	return connections, databaseConnection
}

// closeOperationMigrationDatabase closes all handles so Windows can remove the temporary database directory.
func closeOperationMigrationDatabase(t *testing.T, connections *database.Connections) {
	t.Helper()
	if err := connections.Close(context.Background()); err != nil {
		t.Errorf("close migration database: %v", err)
	}
}

// operationJournalMigration finds the production migration through the same embedded registry used by the CLI.
func operationJournalMigration(t *testing.T) Migration {
	t.Helper()
	migrations := selectMigrations("harbord", "default", "sqlite")
	for _, migration := range migrations {
		if migration.Name() == "2026_07_18_073856_create_operation_journal" {
			return migration
		}
	}
	available := make([]string, 0, len(migrations))
	for _, migration := range migrations {
		available = append(available, migration.Name())
	}
	t.Fatalf("operation journal migration is not registered; available: %v", available)
	return nil
}

// insertMigrationOperation inserts one lifecycle row while keeping problem columns atomically present or absent.
func insertMigrationOperation(databaseConnection *gorm.DB, id string, intentID string, lifecycleState string, requestedAt time.Time, startedAt *time.Time, finishedAt *time.Time, problemCode *string, revision int) error {
	var problemMessage any
	var problemRetryable any
	if problemCode != nil {
		problemMessage = "Harbor could not apply the operation."
		problemRetryable = true
	}
	return databaseConnection.Exec(`INSERT INTO operations
        (id, intent_id, kind, state, phase, problem_code, problem_message, problem_retryable, requested_at, started_at, finished_at, revision)
        VALUES (?, ?, 'project.start', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		intentID,
		lifecycleState,
		lifecycleState,
		problemCode,
		problemMessage,
		problemRetryable,
		requestedAt,
		startedAt,
		finishedAt,
		revision,
	).Error
}

// assertMigrationStatementFails verifies database invariants reject rows that bypass Harbor's domain constructors.
func assertMigrationStatementFails(t *testing.T, databaseConnection *gorm.DB, statement string, arguments ...any) {
	t.Helper()
	if err := databaseConnection.Exec(statement, arguments...).Error; err == nil {
		t.Fatalf("statement unexpectedly succeeded: %s", statement)
	}
}
