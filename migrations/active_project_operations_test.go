package migrations

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

// TestActiveProjectOperationMigrationSerializesProjectLifecycleIntent verifies the database closes enqueue races per project.
func TestActiveProjectOperationMigrationSerializesProjectLifecycleIntent(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	migration := activeProjectOperationMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply active project operation migration: %v", err)
	}

	requestedAt := time.Date(2026, time.July, 19, 6, 30, 0, 0, time.UTC)
	insertActiveProjectOperation(t, databaseConnection, "operation-start", "intent-start", "project-orders", "queued", 1, requestedAt)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, revision)
		VALUES ('operation-stop-racing', 'intent-stop-racing', 'project.stop', 'project-orders', 'queued', 'queued', ?, 2)`, requestedAt)
	insertActiveProjectOperation(t, databaseConnection, "operation-other", "intent-other", "project-billing", "queued", 2, requestedAt)
	insertTerminalProjectOperation(t, databaseConnection, "operation-history", "intent-history", "project-orders", 3, requestedAt)

	startedAt := requestedAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	if err := databaseConnection.Exec(`UPDATE operations
		SET state = 'succeeded', phase = 'complete', started_at = ?, finished_at = ?, revision = 4
		WHERE id = 'operation-start'`, startedAt, finishedAt).Error; err != nil {
		t.Fatalf("complete first active operation: %v", err)
	}
	insertActiveProjectOperation(t, databaseConnection, "operation-stop", "intent-stop", "project-orders", "queued", 5, finishedAt)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback active project operation migration: %v", err)
	}
	insertActiveProjectOperation(t, databaseConnection, "operation-unrestricted", "intent-unrestricted", "project-orders", "queued", 6, finishedAt)
}

// activeProjectOperationMigration finds the production partial-index migration through the embedded registry.
func activeProjectOperationMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == "2026_07_19_063000_limit_active_project_operations" {
			return migration
		}
	}
	t.Fatal("active project operation migration is not registered")
	return nil
}

// insertActiveProjectOperation writes one queued row for partial-index behavior checks.
func insertActiveProjectOperation(
	t *testing.T,
	databaseConnection *gorm.DB,
	operationID string,
	intentID string,
	projectID string,
	state string,
	revision int,
	requestedAt time.Time,
) {
	t.Helper()
	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, revision)
		VALUES (?, ?, 'project.start', ?, ?, ?, ?, ?)`,
		operationID, intentID, projectID, state, state, requestedAt, revision,
	).Error; err != nil {
		t.Fatalf("insert active operation %q: %v", operationID, err)
	}
}

// insertTerminalProjectOperation proves retained history for a project does not block new lifecycle work.
func insertTerminalProjectOperation(
	t *testing.T,
	databaseConnection *gorm.DB,
	operationID string,
	intentID string,
	projectID string,
	revision int,
	requestedAt time.Time,
) {
	t.Helper()
	startedAt := requestedAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, finished_at, revision)
		VALUES (?, ?, 'project.start', ?, 'succeeded', 'complete', ?, ?, ?, ?)`,
		operationID, intentID, projectID, requestedAt, startedAt, finishedAt, revision,
	).Error; err != nil {
		t.Fatalf("insert terminal operation %q: %v", operationID, err)
	}
}
