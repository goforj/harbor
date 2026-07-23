package migrations

import (
	"database/sql"
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseTerminalOwnershipFingerprintMigrationName = "2026_07_22_051000_add_network_global_release_terminal_ownership_fingerprint"

// TestNetworkGlobalReleaseTerminalOwnershipFingerprintMigrationPreservesLegacyTerminals verifies the additive evidence field leaves existing replay fences readable.
func TestNetworkGlobalReleaseTerminalOwnershipFingerprintMigrationPreservesLegacyTerminals(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseTerminalOwnershipFingerprintPrerequisiteHarness(t)
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 4, 3)
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		`INSERT INTO network_global_release_terminals (
			operation_id, owner_identity, source_checkpoint_revision, network_revision
		) VALUES ('operation-release', '502', 4, 3)`,
	)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply terminal ownership fingerprint migration: %v", err)
	}
	if !databaseConnection.Migrator().HasColumn("network_global_release_terminals", "released_ownership_fingerprint") {
		t.Fatal("migration did not create network_global_release_terminals.released_ownership_fingerprint")
	}
	var legacy sql.NullString
	if err := databaseConnection.Raw(
		"SELECT released_ownership_fingerprint FROM network_global_release_terminals WHERE operation_id = 'operation-release'",
	).Scan(&legacy).Error; err != nil {
		t.Fatalf("read legacy terminal ownership fingerprint: %v", err)
	}
	if legacy.Valid {
		t.Fatalf("legacy terminal ownership fingerprint = %#v, want NULL", legacy)
	}
	for _, statement := range []string{
		"UPDATE network_global_release_terminals SET released_ownership_fingerprint = 'invalid' WHERE operation_id = 'operation-release'",
		"UPDATE network_global_release_terminals SET released_ownership_fingerprint = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE operation_id = 'operation-release'",
	} {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		"UPDATE network_global_release_terminals SET released_ownership_fingerprint = ? WHERE operation_id = 'operation-release'",
		strings.Repeat("a", 64),
	)
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("terminal ownership fingerprint rollback discarded exact replay evidence")
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		"UPDATE network_global_release_terminals SET released_ownership_fingerprint = NULL WHERE operation_id = 'operation-release'",
	)
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback legacy terminal ownership fingerprint migration: %v", err)
	}
	if databaseConnection.Migrator().HasColumn("network_global_release_terminals", "released_ownership_fingerprint") {
		t.Fatal("rollback retained terminal ownership fingerprint column")
	}
}

// newNetworkGlobalReleaseTerminalOwnershipFingerprintPrerequisiteHarness applies the stream through the terminal schema that lacks ownership receipt evidence.
func newNetworkGlobalReleaseTerminalOwnershipFingerprintPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool {
		return migrationList[left].Name() < migrationList[right].Name()
	})
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseTerminalOwnershipFingerprintMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply terminal ownership fingerprint prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("terminal ownership fingerprint migration %q is not registered", networkGlobalReleaseTerminalOwnershipFingerprintMigrationName)
	return nil, nil
}
