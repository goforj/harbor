package migrations

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

const networkReleaseOperationsMigrationName = "2026_07_22_030000_limit_active_network_release"

const activeNetworkReleaseIndexName = "operations_one_active_network_release_idx"

// TestNetworkReleaseOperationsMigrationRejectsActiveReleaseCollisions proves every active release phase shares one database owner.
func TestNetworkReleaseOperationsMigrationRejectsActiveReleaseCollisions(t *testing.T) {
	databaseConnection, _ := newNetworkReleaseOperationMigrationHarness(t)
	requestedAt := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)

	insertNetworkLifecycleMigrationOperation(t, databaseConnection, "operation-first", "network.release", "queued", nil, 1, requestedAt)
	for index, state := range []string{"queued", "running", "requires_approval"} {
		if err := executeNetworkLifecycleMigrationOperation(
			databaseConnection,
			"operation-racing-"+state,
			"network.release",
			state,
			nil,
			index+2,
			requestedAt,
		); err == nil {
			t.Fatalf("competing %s network release operation unexpectedly succeeded", state)
		}
	}
}

// TestNetworkReleaseOperationsMigrationAllowsTerminalSuccessor proves a completed release does not block its successor.
func TestNetworkReleaseOperationsMigrationAllowsTerminalSuccessor(t *testing.T) {
	databaseConnection, _ := newNetworkReleaseOperationMigrationHarness(t)
	requestedAt := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)
	insertNetworkLifecycleMigrationOperation(t, databaseConnection, "operation-first", "network.release", "running", nil, 1, requestedAt)

	startedAt := requestedAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	if err := databaseConnection.Exec(`UPDATE operations
		SET state = 'succeeded', phase = 'complete', started_at = ?, finished_at = ?, revision = 2
		WHERE id = 'operation-first'`, startedAt, finishedAt).Error; err != nil {
		t.Fatalf("complete first network release operation: %v", err)
	}
	insertNetworkLifecycleMigrationOperation(t, databaseConnection, "operation-next", "network.release", "requires_approval", nil, 3, finishedAt)
}

// TestNetworkReleaseOperationsMigrationDoesNotGloballySerializeProjects leaves cross-domain exclusion to release staging.
func TestNetworkReleaseOperationsMigrationDoesNotGloballySerializeProjects(t *testing.T) {
	databaseConnection, _ := newNetworkReleaseOperationMigrationHarness(t)
	requestedAt := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)
	insertNetworkLifecycleMigrationOperation(t, databaseConnection, "operation-release", "network.release", "running", nil, 1, requestedAt)

	projectID := "project-orders"
	insertNetworkLifecycleMigrationOperation(t, databaseConnection, "operation-project-lifecycle", "project.start", "running", &projectID, 2, requestedAt)
}

// TestNetworkReleaseOperationsMigrationRollbackRemovesGlobalGuard proves rollback drops the release guard.
func TestNetworkReleaseOperationsMigrationRollbackRemovesGlobalGuard(t *testing.T) {
	databaseConnection, migration := newNetworkReleaseOperationMigrationHarness(t)
	requestedAt := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)
	insertNetworkLifecycleMigrationOperation(t, databaseConnection, "operation-first", "network.release", "running", nil, 1, requestedAt)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback network release operation migration: %v", err)
	}
	var count int
	if err := databaseConnection.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, activeNetworkReleaseIndexName).Scan(&count).Error; err != nil {
		t.Fatalf("find network release index after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("network release index remains after rollback")
	}
	insertNetworkLifecycleMigrationOperation(t, databaseConnection, "operation-unrestricted", "network.release", "running", nil, 2, requestedAt)
}

// newNetworkReleaseOperationMigrationHarness applies only the operation owner and the new partial index.
func newNetworkReleaseOperationMigrationHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	migration := networkReleaseOperationMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply network release operation migration: %v", err)
	}
	return databaseConnection, migration
}

// networkReleaseOperationMigration finds the production partial-index migration through the embedded registry.
func networkReleaseOperationMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkReleaseOperationsMigrationName {
			return migration
		}
	}
	t.Fatalf("network release operation migration %q is not registered", networkReleaseOperationsMigrationName)
	return nil
}

// insertNetworkLifecycleMigrationOperation inserts one lifecycle operation that should satisfy the journal schema.
func insertNetworkLifecycleMigrationOperation(
	t *testing.T,
	databaseConnection *gorm.DB,
	operationID string,
	kind string,
	state string,
	projectID *string,
	revision int,
	requestedAt time.Time,
) {
	t.Helper()
	if err := executeNetworkLifecycleMigrationOperation(databaseConnection, operationID, kind, state, projectID, revision, requestedAt); err != nil {
		t.Fatalf("insert %s operation %q: %v", kind, operationID, err)
	}
}

// executeNetworkLifecycleMigrationOperation shares one canonical insert across successful and conflicting lifecycle cases.
func executeNetworkLifecycleMigrationOperation(
	databaseConnection *gorm.DB,
	operationID string,
	kind string,
	state string,
	projectID *string,
	revision int,
	requestedAt time.Time,
) error {
	var startedAt *time.Time
	if state == "running" || state == "requires_approval" {
		value := requestedAt
		startedAt = &value
	}
	return databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		operationID,
		"intent-"+operationID,
		kind,
		projectID,
		state,
		state,
		requestedAt,
		startedAt,
		revision,
	).Error
}
