package migrations

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

const networkDataPlaneSetupOperationsMigrationName = "2026_07_22_010000_limit_active_network_dataplane_setup"

// TestNetworkDataPlaneSetupOperationsMigrationSerializesGlobalApproval proves every active phase shares one database owner.
func TestNetworkDataPlaneSetupOperationsMigrationSerializesGlobalApproval(t *testing.T) {
	databaseConnection, migration := newNetworkDataPlaneSetupOperationMigrationHarness(t)
	requestedAt := time.Date(2026, time.July, 22, 1, 0, 0, 0, time.UTC)

	insertNetworkDataPlaneSetupMigrationOperation(t, databaseConnection, "operation-first", "queued", 1, requestedAt)
	for index, state := range []string{"queued", "running", "requires_approval"} {
		if err := executeNetworkDataPlaneSetupMigrationOperation(
			databaseConnection,
			"operation-racing-"+state,
			state,
			index+2,
			requestedAt,
		); err == nil {
			t.Fatalf("competing %s network data-plane setup operation unexpectedly succeeded", state)
		}
	}

	startedAt := requestedAt.Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	if err := databaseConnection.Exec(`UPDATE operations
		SET state = 'succeeded', phase = 'complete', started_at = ?, finished_at = ?, revision = 5
		WHERE id = 'operation-first'`, startedAt, finishedAt).Error; err != nil {
		t.Fatalf("complete first network data-plane setup operation: %v", err)
	}
	insertNetworkDataPlaneSetupMigrationOperation(t, databaseConnection, "operation-next", "requires_approval", 6, finishedAt)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback network data-plane setup operation migration: %v", err)
	}
	insertNetworkDataPlaneSetupMigrationOperation(t, databaseConnection, "operation-unrestricted", "running", 7, finishedAt)
}

// TestNetworkDataPlaneSetupOperationsMigrationDoesNotConflateOtherOperationDomains keeps the guard scoped to trusted ingress.
func TestNetworkDataPlaneSetupOperationsMigrationDoesNotConflateOtherOperationDomains(t *testing.T) {
	databaseConnection, _ := newNetworkDataPlaneSetupOperationMigrationHarness(t)
	requestedAt := time.Date(2026, time.July, 22, 1, 0, 0, 0, time.UTC)
	insertNetworkDataPlaneSetupMigrationOperation(t, databaseConnection, "operation-dataplane", "running", 1, requestedAt)

	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES ('operation-resolver', 'intent-resolver', 'network.resolver.setup', NULL, 'running', 'running', ?, ?, 2)`,
		requestedAt,
		requestedAt,
	).Error; err != nil {
		t.Fatalf("insert unrelated resolver operation: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES ('operation-project-scoped', 'intent-project-scoped', 'network.data-plane.setup', 'project-orders', 'running', 'running', ?, ?, 3)`,
		requestedAt,
		requestedAt,
	).Error; err != nil {
		t.Fatalf("insert invalid-but-index-independent project-scoped row: %v", err)
	}
}

// newNetworkDataPlaneSetupOperationMigrationHarness applies only the operation owner and the new partial index.
func newNetworkDataPlaneSetupOperationMigrationHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	if err := operationJournalMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply operation journal migration: %v", err)
	}
	migration := networkDataPlaneSetupOperationMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply network data-plane setup operation migration: %v", err)
	}
	return databaseConnection, migration
}

// networkDataPlaneSetupOperationMigration finds the production partial-index migration through the embedded registry.
func networkDataPlaneSetupOperationMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkDataPlaneSetupOperationsMigrationName {
			return migration
		}
	}
	t.Fatalf("network data-plane setup operation migration %q is not registered", networkDataPlaneSetupOperationsMigrationName)
	return nil
}

// insertNetworkDataPlaneSetupMigrationOperation inserts one valid active global setup owner.
func insertNetworkDataPlaneSetupMigrationOperation(
	t *testing.T,
	databaseConnection *gorm.DB,
	operationID string,
	state string,
	revision int,
	requestedAt time.Time,
) {
	t.Helper()
	if err := executeNetworkDataPlaneSetupMigrationOperation(
		databaseConnection,
		operationID,
		state,
		revision,
		requestedAt,
	); err != nil {
		t.Fatalf("insert network data-plane setup operation %q: %v", operationID, err)
	}
}

// executeNetworkDataPlaneSetupMigrationOperation shares one canonical insert across success and collision cases.
func executeNetworkDataPlaneSetupMigrationOperation(
	databaseConnection *gorm.DB,
	operationID string,
	state string,
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
		VALUES (?, ?, 'network.data-plane.setup', NULL, ?, ?, ?, ?, ?)`,
		operationID,
		"intent-"+operationID,
		state,
		state,
		requestedAt,
		startedAt,
		revision,
	).Error
}
