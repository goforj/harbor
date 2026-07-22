package migrations

import (
	"sort"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleasePlanCheckpointRevisionMigrationName = "2026_07_22_041000_add_network_global_release_plan_checkpoint_revision"

// TestNetworkGlobalReleasePlanCheckpointRevisionMigrationBackfillsAndRollsBack verifies upgrades retain released authority rows.
func TestNetworkGlobalReleasePlanCheckpointRevisionMigrationBackfillsAndRollsBack(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleasePlanCheckpointRevisionPrerequisiteHarness(t)
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	insertNetworkGlobalReleaseMigrationPlan(t, databaseConnection, defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5))
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply checkpoint migration: %v", err)
	}
	if !databaseConnection.Migrator().HasColumn("network_global_release_plans", "checkpoint_revision") {
		t.Fatal("checkpoint migration did not create checkpoint_revision")
	}
	assertNetworkGlobalReleaseMigrationForeignKeys(t, databaseConnection)
	assertNetworkGlobalReleaseMigrationUniqueColumns(t, databaseConnection)
	var revision int64
	if err := databaseConnection.
		Raw("SELECT checkpoint_revision FROM network_global_release_plans WHERE id = 1").
		Scan(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if revision != 3 {
		t.Fatalf("checkpoint revision = %d, want 3", revision)
	}
	assertMigrationStatementFails(t, databaseConnection, "UPDATE network_global_release_plans SET checkpoint_revision = 0 WHERE id = 1")
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM operations WHERE id = 'operation-release'")
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM network_state WHERE id = 1")
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback checkpoint migration: %v", err)
	}
	if databaseConnection.Migrator().HasColumn("network_global_release_plans", "checkpoint_revision") {
		t.Fatal("rollback retained checkpoint_revision")
	}
	assertProjectionCount(t, databaseConnection, "network_global_release_plans", 1)
}

// TestNetworkGlobalReleasePlanCheckpointRevisionMigrationHandlesEmptyTable verifies a no-row upgrade and rollback.
func TestNetworkGlobalReleasePlanCheckpointRevisionMigrationHandlesEmptyTable(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleasePlanCheckpointRevisionPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatal(err)
	}
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatal(err)
	}
	assertProjectionCount(t, databaseConnection, "network_global_release_plans", 0)
}

// TestNetworkGlobalReleasePlanCheckpointRevisionMigrationRejectsAdvancedUpgrade prevents an old unexplained phase from receiving a false initial checkpoint.
func TestNetworkGlobalReleasePlanCheckpointRevisionMigrationRejectsAdvancedUpgrade(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleasePlanCheckpointRevisionPrerequisiteHarness(t)
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	plan := defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5)
	plan.Phase = "low_ports"
	insertNetworkGlobalReleaseMigrationPlan(t, databaseConnection, plan)
	if err := migration.Up(databaseConnection); err == nil {
		t.Fatal("checkpoint migration accepted an unexplained advanced plan")
	}
	if databaseConnection.Migrator().HasColumn("network_global_release_plans", "checkpoint_revision") {
		t.Fatal("rejected checkpoint migration changed the released schema")
	}
	assertProjectionCount(t, databaseConnection, "network_global_release_plans", 1)
}

// TestNetworkGlobalReleasePlanCheckpointRevisionMigrationRejectsAdvancedRollback prevents downgrade from discarding committed checkpoint ownership.
func TestNetworkGlobalReleasePlanCheckpointRevisionMigrationRejectsAdvancedRollback(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleasePlanCheckpointRevisionPrerequisiteHarness(t)
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	insertNetworkGlobalReleaseMigrationPlan(t, databaseConnection, defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5))
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply checkpoint migration: %v", err)
	}
	if err := databaseConnection.Exec(`UPDATE network_global_release_plans
		SET phase = 'low_ports', checkpoint_revision = 4
		WHERE id = 1`).Error; err != nil {
		t.Fatalf("advance checkpoint fixture: %v", err)
	}
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("checkpoint rollback discarded an advanced checkpoint")
	}
	if !databaseConnection.Migrator().HasColumn("network_global_release_plans", "checkpoint_revision") {
		t.Fatal("rejected checkpoint rollback changed the checkpointed schema")
	}
	assertProjectionCount(t, databaseConnection, "network_global_release_plans", 1)
}

// newNetworkGlobalReleasePlanCheckpointRevisionPrerequisiteHarness applies migrations through the released 040000 schema.
func newNetworkGlobalReleasePlanCheckpointRevisionPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() {
		closeOperationMigrationDatabase(t, connections)
	})
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool {
		return migrationList[left].Name() < migrationList[right].Name()
	})
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleasePlanCheckpointRevisionMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply checkpoint prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("checkpoint migration %q is not registered", networkGlobalReleasePlanCheckpointRevisionMigrationName)
	return nil, nil
}
