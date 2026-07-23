package migrations

import (
	"sort"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseTerminalsMigrationName = "2026_07_22_049000_create_network_global_release_terminals"

// TestNetworkGlobalReleaseTerminalsMigrationCreatesBoundedReplayFence verifies terminal history retains no release authority payload.
func TestNetworkGlobalReleaseTerminalsMigrationCreatesBoundedReplayFence(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseTerminalsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "ownership")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply global release terminal migration: %v", err)
	}
	for _, column := range []string{"operation_id", "owner_identity", "source_checkpoint_revision", "network_revision"} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_terminals", column) {
			t.Fatalf("terminal migration did not create network_global_release_terminals.%s", column)
		}
	}
	for _, forbidden := range []string{"authority_payload", "authority_digest", "verified_at"} {
		if databaseConnection.Migrator().HasColumn("network_global_release_terminals", forbidden) {
			t.Fatalf("terminal migration retained authority field %s", forbidden)
		}
	}
	var foreignKeys []struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_list(network_global_release_terminals)").Scan(&foreignKeys).Error; err != nil {
		t.Fatalf("inspect terminal foreign key: %v", err)
	}
	if len(foreignKeys) != 1 || foreignKeys[0].Table != "operations" || foreignKeys[0].From != "operation_id" || foreignKeys[0].To != "id" || foreignKeys[0].OnUpdate != "RESTRICT" || foreignKeys[0].OnDelete != "RESTRICT" {
		t.Fatalf("terminal foreign key = %#v", foreignKeys)
	}
	mustExecNetworkSetupMigration(t, databaseConnection, `INSERT INTO network_global_release_terminals (
		operation_id, owner_identity, source_checkpoint_revision, network_revision
	) VALUES ('operation-release', '502', 4, 3)`)
	for _, statement := range []string{
		"UPDATE network_global_release_terminals SET owner_identity = '' WHERE operation_id = 'operation-release'",
		"UPDATE network_global_release_terminals SET source_checkpoint_revision = 0 WHERE operation_id = 'operation-release'",
		"UPDATE network_global_release_terminals SET network_revision = 9007199254740992 WHERE operation_id = 'operation-release'",
		"UPDATE network_global_release_terminals SET operation_id = 'operation-foreign' WHERE operation_id = 'operation-release'",
		"DELETE FROM operations WHERE id = 'operation-release'",
	} {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("terminal migration rollback discarded successful release history")
	}
}

// TestNetworkGlobalReleaseTerminalsMigrationRollsBackEmptySchema verifies a terminal-free installation remains reversible.
func TestNetworkGlobalReleaseTerminalsMigrationRollsBackEmptySchema(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseTerminalsPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply global release terminal migration: %v", err)
	}
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback empty global release terminal migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_global_release_terminals") {
		t.Fatal("rollback retained empty terminal table")
	}
}

// newNetworkGlobalReleaseTerminalsPrerequisiteHarness applies the migration stream through the ownership-receipt schema.
func newNetworkGlobalReleaseTerminalsPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool {
		return migrationList[left].Name() < migrationList[right].Name()
	})
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseTerminalsMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply terminal prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("global release terminal migration %q is not registered", networkGlobalReleaseTerminalsMigrationName)
	return nil, nil
}
