package migrations

import (
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseEffectsReceiptsMigrationName = "2026_07_22_046000_create_network_global_release_effects_receipts"

// TestNetworkGlobalReleaseEffectsReceiptsMigrationCreatesGuards verifies the effects receipt schema retains only bounded durable evidence.
func TestNetworkGlobalReleaseEffectsReceiptsMigrationCreatesGuards(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseEffectsReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "verify_effects")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply effects receipt migration: %v", err)
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"source_checkpoint_revision",
		"runtime_observation_digest",
		"ownership_observation_fingerprint",
		"low_port_observation_fingerprint",
		"resolver_observation_fingerprint",
		"trust_observation_fingerprint",
		"loopback_observation_digest",
		"verified_at",
	} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_effects_receipts", column) {
			t.Fatalf("receipt migration did not create network_global_release_effects_receipts.%s", column)
		}
	}
	var foreignKeys []struct {
		Table    string `gorm:"column:table"`
		From     string `gorm:"column:from"`
		To       string `gorm:"column:to"`
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_list(network_global_release_effects_receipts)").Scan(&foreignKeys).Error; err != nil {
		t.Fatalf("inspect effects receipt foreign key: %v", err)
	}
	if len(foreignKeys) != 1 ||
		foreignKeys[0].Table != "network_global_release_plans" ||
		foreignKeys[0].From != "operation_id" ||
		foreignKeys[0].To != "operation_id" ||
		foreignKeys[0].OnUpdate != "RESTRICT" ||
		foreignKeys[0].OnDelete != "RESTRICT" {
		t.Fatalf("effects receipt foreign key = %#v", foreignKeys)
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		`INSERT INTO network_global_release_effects_receipts (
			id,
			operation_id,
			source_checkpoint_revision,
			runtime_observation_digest,
			ownership_observation_fingerprint,
			low_port_observation_fingerprint,
			resolver_observation_fingerprint,
			trust_observation_fingerprint,
			loopback_observation_digest,
			verified_at
		)
		VALUES (1, 'operation-release', 4, ?, ?, ?, ?, ?, ?, '2026-07-22T05:00:00Z')`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		strings.Repeat("c", 64),
		strings.Repeat("d", 64),
		strings.Repeat("e", 64),
		strings.Repeat("f", 64),
	)
	for _, statement := range []string{
		"UPDATE network_global_release_effects_receipts SET id = 2 WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET source_checkpoint_revision = 0 WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET runtime_observation_digest = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET runtime_observation_digest = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET ownership_observation_fingerprint = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET ownership_observation_fingerprint = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET low_port_observation_fingerprint = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET low_port_observation_fingerprint = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET resolver_observation_fingerprint = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET resolver_observation_fingerprint = 'CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET trust_observation_fingerprint = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET trust_observation_fingerprint = 'DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET loopback_observation_digest = 'invalid' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET loopback_observation_digest = 'EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE' WHERE id = 1",
		"UPDATE network_global_release_effects_receipts SET verified_at = '' WHERE id = 1",
		"INSERT INTO network_global_release_effects_receipts (id, operation_id, source_checkpoint_revision, runtime_observation_digest, ownership_observation_fingerprint, low_port_observation_fingerprint, resolver_observation_fingerprint, trust_observation_fingerprint, loopback_observation_digest, verified_at) VALUES (2, 'operation-release', 4, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd', 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff', '2026-07-22T05:00:00Z')",
		"UPDATE network_global_release_effects_receipts SET operation_id = 'operation-foreign' WHERE id = 1",
		"DELETE FROM network_global_release_plans WHERE id = 1",
	} {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("rollback discarded acknowledged effects release state")
	}
}

// TestNetworkGlobalReleaseEffectsReceiptsMigrationAcceptsPrerequisitePhases verifies the additive schema can be applied before or during effect verification.
func TestNetworkGlobalReleaseEffectsReceiptsMigrationAcceptsPrerequisitePhases(t *testing.T) {
	for _, phase := range []string{
		"runtime_release",
		"low_ports",
		"resolver",
		"trust",
		"loopbacks",
		"verify_effects",
	} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseEffectsReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, phase)
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply effects receipt migration in %s phase: %v", phase, err)
			}
		})
	}
}

// TestNetworkGlobalReleaseEffectsReceiptsMigrationRejectsAdvancedPlans rejects upgrades that cannot explain committed ownership or projection cleanup.
func TestNetworkGlobalReleaseEffectsReceiptsMigrationRejectsAdvancedPlans(t *testing.T) {
	for _, phase := range []string{"ownership", "projection"} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseEffectsReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, phase)
			if err := migration.Up(databaseConnection); err == nil {
				t.Fatalf("effects receipt migration accepted unexplained %s phase", phase)
			}
		})
	}
}

// TestNetworkGlobalReleaseEffectsReceiptsMigrationRollsBackSafeState verifies an empty pre-ownership receipt table remains reversible.
func TestNetworkGlobalReleaseEffectsReceiptsMigrationRollsBackSafeState(t *testing.T) {
	for _, phase := range []string{
		"runtime_release",
		"low_ports",
		"resolver",
		"trust",
		"loopbacks",
		"verify_effects",
	} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseEffectsReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, phase)
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply effects receipt migration: %v", err)
			}
			if err := migration.Down(databaseConnection); err != nil {
				t.Fatalf("rollback safe effects receipt migration: %v", err)
			}
			if databaseConnection.Migrator().HasTable("network_global_release_effects_receipts") {
				t.Fatal("rollback retained empty effects receipt table")
			}
		})
	}
}

// TestNetworkGlobalReleaseEffectsReceiptsMigrationRejectsLaterPhaseRollback preserves plans that have advanced past effect verification.
func TestNetworkGlobalReleaseEffectsReceiptsMigrationRejectsLaterPhaseRollback(t *testing.T) {
	for _, phase := range []string{"ownership", "projection"} {
		t.Run(phase, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseEffectsReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "verify_effects")
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply effects receipt migration: %v", err)
			}
			mustExecNetworkSetupMigration(t, databaseConnection, "UPDATE network_global_release_plans SET phase = ? WHERE id = 1", phase)
			if err := migration.Down(databaseConnection); err == nil {
				t.Fatalf("rollback discarded later %s release phase", phase)
			}
		})
	}
}

// newNetworkGlobalReleaseEffectsReceiptsPrerequisiteHarness applies the stream through the loopback receipt schema.
func newNetworkGlobalReleaseEffectsReceiptsPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool { return migrationList[left].Name() < migrationList[right].Name() })
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseEffectsReceiptsMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply effects receipt prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("effects receipt migration %q is not registered", networkGlobalReleaseEffectsReceiptsMigrationName)
	return nil, nil
}
