package migrations

import (
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseLoopbackReceiptsMigrationName = "2026_07_22_045000_create_network_global_release_loopback_receipts"

// TestNetworkGlobalReleaseLoopbackReceiptsMigrationCreatesGuardsAndRollsBack verifies the loopback receipt schema accepts only bounded durable evidence.
func TestNetworkGlobalReleaseLoopbackReceiptsMigrationCreatesGuardsAndRollsBack(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseLoopbackReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "loopbacks")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply loopback receipt migration: %v", err)
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"source_checkpoint_revision",
		"loopback_evidence_digest",
		"owned_absent_observation_digest",
		"verified_at",
	} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_loopback_receipts", column) {
			t.Fatalf("receipt migration did not create network_global_release_loopback_receipts.%s", column)
		}
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		`INSERT INTO network_global_release_loopback_receipts (
			id,
			operation_id,
			source_checkpoint_revision,
			loopback_evidence_digest,
			owned_absent_observation_digest,
			verified_at
		)
		VALUES (1, 'operation-release', 4, ?, ?, '2026-07-22T04:50:00Z')`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)
	for _, statement := range []string{
		"UPDATE network_global_release_loopback_receipts SET id = 2 WHERE id = 1",
		"UPDATE network_global_release_loopback_receipts SET source_checkpoint_revision = 0 WHERE id = 1",
		"UPDATE network_global_release_loopback_receipts SET loopback_evidence_digest = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_loopback_receipts SET loopback_evidence_digest = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE id = 1",
		"UPDATE network_global_release_loopback_receipts SET owned_absent_observation_digest = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_loopback_receipts SET owned_absent_observation_digest = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB' WHERE id = 1",
		"UPDATE network_global_release_loopback_receipts SET verified_at = '' WHERE id = 1",
		"INSERT INTO network_global_release_loopback_receipts (id, operation_id, source_checkpoint_revision, loopback_evidence_digest, owned_absent_observation_digest, verified_at) VALUES (2, 'operation-release', 4, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '2026-07-22T04:50:00Z')",
		"UPDATE network_global_release_loopback_receipts SET operation_id = 'operation-foreign' WHERE id = 1",
		"DELETE FROM network_global_release_plans WHERE id = 1",
	} {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("rollback discarded acknowledged loopback release state")
	}
}

// TestNetworkGlobalReleaseLoopbackReceiptsMigrationRejectsAdvancedPlans rejects upgrades after effect verification begins.
func TestNetworkGlobalReleaseLoopbackReceiptsMigrationRejectsAdvancedPlans(t *testing.T) {
	for _, phase := range []string{"verify_effects", "ownership", "projection"} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseLoopbackReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, phase)
			if err := migration.Up(databaseConnection); err == nil {
				t.Fatalf("loopback receipt migration accepted unexplained %s phase", phase)
			}
		})
	}
}

// TestNetworkGlobalReleaseLoopbackReceiptsMigrationRollsBackSafeLoopbackState verifies an empty loopback-phase table remains reversible.
func TestNetworkGlobalReleaseLoopbackReceiptsMigrationRollsBackSafeLoopbackState(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseLoopbackReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "loopbacks")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply loopback receipt migration: %v", err)
	}
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback safe loopback receipt migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_global_release_loopback_receipts") {
		t.Fatal("rollback retained empty loopback receipt table")
	}
}

// TestNetworkGlobalReleaseLoopbackReceiptsMigrationRejectsLaterPhaseRollback preserves plans that have advanced beyond loopbacks.
func TestNetworkGlobalReleaseLoopbackReceiptsMigrationRejectsLaterPhaseRollback(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseLoopbackReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "loopbacks")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply loopback receipt migration: %v", err)
	}
	mustExecNetworkSetupMigration(t, databaseConnection, "UPDATE network_global_release_plans SET phase = 'verify_effects' WHERE id = 1")
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("rollback discarded later loopback release phase")
	}
}

// newNetworkGlobalReleaseLoopbackReceiptsPrerequisiteHarness applies the stream through the trust receipt schema.
func newNetworkGlobalReleaseLoopbackReceiptsPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool { return migrationList[left].Name() < migrationList[right].Name() })
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseLoopbackReceiptsMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply loopback receipt prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("loopback receipt migration %q is not registered", networkGlobalReleaseLoopbackReceiptsMigrationName)
	return nil, nil
}
