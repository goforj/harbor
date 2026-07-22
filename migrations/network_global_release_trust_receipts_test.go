package migrations

import (
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseTrustReceiptsMigrationName = "2026_07_22_044000_create_network_global_release_trust_receipts"

// TestNetworkGlobalReleaseTrustReceiptsMigrationCreatesGuardsAndRollsBack verifies the trust receipt schema is additive only before loopback acknowledgement.
func TestNetworkGlobalReleaseTrustReceiptsMigrationCreatesGuardsAndRollsBack(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseTrustReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "trust")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply trust receipt migration: %v", err)
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"source_checkpoint_revision",
		"disposition",
		"confirmation_digest",
		"observation_fingerprint",
		"verified_at",
	} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_trust_receipts", column) {
			t.Fatalf("receipt migration did not create network_global_release_trust_receipts.%s", column)
		}
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		`INSERT INTO network_global_release_trust_receipts (
			id, operation_id, source_checkpoint_revision, disposition, confirmation_digest, observation_fingerprint, verified_at
		) VALUES (1, 'operation-release', 4, 'owned', ?, ?, '2026-07-22T04:40:00Z')`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)
	for _, statement := range []string{
		"UPDATE network_global_release_trust_receipts SET id = 2 WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET source_checkpoint_revision = 0 WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET disposition = 'other' WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET confirmation_digest = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET confirmation_digest = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET confirmation_digest = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET observation_fingerprint = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET observation_fingerprint = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB' WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET observation_fingerprint = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id = 1",
		"UPDATE network_global_release_trust_receipts SET verified_at = '' WHERE id = 1",
		"INSERT INTO network_global_release_trust_receipts (id, operation_id, source_checkpoint_revision, disposition, confirmation_digest, observation_fingerprint, verified_at) VALUES (2, 'operation-release', 4, 'owned', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '2026-07-22T04:40:00Z')",
		"UPDATE network_global_release_trust_receipts SET operation_id = 'operation-foreign' WHERE id = 1",
		"DELETE FROM network_global_release_plans WHERE id = 1",
	} {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("rollback discarded acknowledged trust release state")
	}
}

// TestNetworkGlobalReleaseTrustReceiptsMigrationRejectsAdvancedPlans rejects upgrades that cannot explain completed loopback release.
func TestNetworkGlobalReleaseTrustReceiptsMigrationRejectsAdvancedPlans(t *testing.T) {
	for _, phase := range []string{"loopbacks", "verify_effects", "ownership", "projection"} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseTrustReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, phase)
			if err := migration.Up(databaseConnection); err == nil {
				t.Fatalf("trust receipt migration accepted unexplained %s phase", phase)
			}
		})
	}
}

// TestNetworkGlobalReleaseTrustReceiptsMigrationRollsBackSafeTrustState verifies an empty trust-phase table remains recoverably reversible.
func TestNetworkGlobalReleaseTrustReceiptsMigrationRollsBackSafeTrustState(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseTrustReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "trust")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply trust receipt migration: %v", err)
	}
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback safe trust receipt migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_global_release_trust_receipts") {
		t.Fatal("rollback retained empty trust receipt table")
	}
}

// TestNetworkGlobalReleaseTrustReceiptsMigrationRejectsLaterPhaseRollback preserves an acknowledged phase even without a receipt row.
func TestNetworkGlobalReleaseTrustReceiptsMigrationRejectsLaterPhaseRollback(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseTrustReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "trust")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply trust receipt migration: %v", err)
	}
	mustExecNetworkSetupMigration(t, databaseConnection, "UPDATE network_global_release_plans SET phase = 'loopbacks' WHERE id = 1")
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("rollback discarded later trust release phase")
	}
}

// newNetworkGlobalReleaseTrustReceiptsPrerequisiteHarness applies the stream through the resolver receipt schema.
func newNetworkGlobalReleaseTrustReceiptsPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool { return migrationList[left].Name() < migrationList[right].Name() })
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseTrustReceiptsMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply trust receipt prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("trust receipt migration %q is not registered", networkGlobalReleaseTrustReceiptsMigrationName)
	return nil, nil
}
