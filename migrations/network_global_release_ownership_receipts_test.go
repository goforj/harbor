package migrations

import (
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseOwnershipReceiptsMigrationName = "2026_07_22_047000_create_network_global_release_ownership_receipts"

// TestNetworkGlobalReleaseOwnershipReceiptsMigrationCreatesGuards verifies ownership release evidence remains bounded to one release plan.
func TestNetworkGlobalReleaseOwnershipReceiptsMigrationCreatesGuards(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseOwnershipReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "ownership")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply ownership receipt migration: %v", err)
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"source_checkpoint_revision",
		"released_ownership_fingerprint",
		"verified_at",
	} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_ownership_receipts", column) {
			t.Fatalf("receipt migration did not create network_global_release_ownership_receipts.%s", column)
		}
	}
	var foreignKeys []struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	foreignKeyQuery := databaseConnection.Raw(
		"PRAGMA foreign_key_list(network_global_release_ownership_receipts)",
	)
	if err := foreignKeyQuery.Scan(&foreignKeys).Error; err != nil {
		t.Fatalf("inspect ownership receipt foreign key: %v", err)
	}
	if len(foreignKeys) != 1 ||
		foreignKeys[0].Table != "network_global_release_plans" ||
		foreignKeys[0].From != "operation_id" ||
		foreignKeys[0].To != "operation_id" ||
		foreignKeys[0].OnUpdate != "RESTRICT" ||
		foreignKeys[0].OnDelete != "RESTRICT" {
		t.Fatalf("ownership receipt foreign key = %#v", foreignKeys)
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		`INSERT INTO network_global_release_ownership_receipts (
			id,
			operation_id,
			source_checkpoint_revision,
			released_ownership_fingerprint,
			verified_at
		)
		VALUES (1, 'operation-release', 4, ?, '2026-07-22T05:10:00Z')`,
		strings.Repeat("a", 64),
	)
	for _, statement := range []string{
		"UPDATE network_global_release_ownership_receipts SET id = 2 WHERE id = 1",
		"UPDATE network_global_release_ownership_receipts SET source_checkpoint_revision = 0 WHERE id = 1",
		"UPDATE network_global_release_ownership_receipts SET source_checkpoint_revision = 9007199254740992 WHERE id = 1",
		"UPDATE network_global_release_ownership_receipts SET released_ownership_fingerprint = 'invalid' WHERE id = 1",
		`UPDATE network_global_release_ownership_receipts
		 SET released_ownership_fingerprint = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
		 WHERE id = 1`,
		"UPDATE network_global_release_ownership_receipts SET verified_at = '' WHERE id = 1",
		"UPDATE network_global_release_ownership_receipts SET verified_at = 'not-a-time' WHERE id = 1",
		`INSERT INTO network_global_release_ownership_receipts (
			id,
			operation_id,
			source_checkpoint_revision,
			released_ownership_fingerprint,
			verified_at
		 ) VALUES (
			2,
			'operation-release',
			4,
			'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'2026-07-22T05:10:00Z'
		 )`,
		"UPDATE network_global_release_ownership_receipts SET operation_id = 'operation-foreign' WHERE id = 1",
		"DELETE FROM network_global_release_plans WHERE id = 1",
	} {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("ownership receipt migration rollback discarded acknowledged receipt state")
	}
	if !databaseConnection.Migrator().HasTable("network_global_release_ownership_receipts") {
		t.Fatal("rejected rollback removed the acknowledged ownership receipt table")
	}
}

// TestNetworkGlobalReleaseOwnershipReceiptsMigrationAcceptsPreProjectionPhases
// verifies the additive receipt table can be installed before projection begins.
func TestNetworkGlobalReleaseOwnershipReceiptsMigrationAcceptsPreProjectionPhases(t *testing.T) {
	for _, phase := range []string{
		"runtime_release",
		"low_ports",
		"resolver",
		"trust",
		"loopbacks",
		"verify_effects",
		"ownership",
	} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseOwnershipReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, phase)
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply ownership receipt migration in %s phase: %v", phase, err)
			}
		})
	}
}

// TestNetworkGlobalReleaseOwnershipReceiptsMigrationRejectsProjection prevents
// an upgrade from inventing ownership acknowledgement after projection begins.
func TestNetworkGlobalReleaseOwnershipReceiptsMigrationRejectsProjection(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseOwnershipReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "projection")
	if err := migration.Up(databaseConnection); err == nil {
		t.Fatal("ownership receipt migration accepted unexplained projection phase")
	}
	if databaseConnection.Migrator().HasTable("network_global_release_ownership_receipts") {
		t.Fatal("rejected ownership receipt migration created its table")
	}
}

// TestNetworkGlobalReleaseOwnershipReceiptsMigrationRollsBackEmptyPreProjectionState
// verifies every unacknowledged pre-projection phase remains reversible.
func TestNetworkGlobalReleaseOwnershipReceiptsMigrationRollsBackEmptyPreProjectionState(t *testing.T) {
	for _, phase := range []string{
		"runtime_release",
		"low_ports",
		"resolver",
		"trust",
		"loopbacks",
		"verify_effects",
		"ownership",
	} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseOwnershipReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, phase)
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply ownership receipt migration: %v", err)
			}
			if err := migration.Down(databaseConnection); err != nil {
				t.Fatalf("rollback safe ownership receipt migration: %v", err)
			}
			if databaseConnection.Migrator().HasTable("network_global_release_ownership_receipts") {
				t.Fatal("rollback retained empty ownership receipt table")
			}
		})
	}
}

// TestNetworkGlobalReleaseOwnershipReceiptsMigrationRejectsAcknowledgedRollback
// preserves a projected release boundary even without a persisted receipt row.
func TestNetworkGlobalReleaseOwnershipReceiptsMigrationRejectsAcknowledgedRollback(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseOwnershipReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "ownership")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply ownership receipt migration: %v", err)
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		"UPDATE network_global_release_plans SET phase = 'projection' WHERE id = 1",
	)
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("ownership receipt migration rollback discarded projected release state")
	}
	if !databaseConnection.Migrator().HasTable("network_global_release_ownership_receipts") {
		t.Fatal("rejected rollback removed the empty ownership receipt table")
	}
}

// newNetworkGlobalReleaseOwnershipReceiptsPrerequisiteHarness applies the stream through the effects receipt schema.
func newNetworkGlobalReleaseOwnershipReceiptsPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool {
		return migrationList[left].Name() < migrationList[right].Name()
	})
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseOwnershipReceiptsMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply ownership receipt prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("ownership receipt migration %q is not registered", networkGlobalReleaseOwnershipReceiptsMigrationName)
	return nil, nil
}
