package migrations

import (
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseResolverReceiptsMigrationName = "2026_07_22_043000_create_network_global_release_resolver_receipts"

// TestNetworkGlobalReleaseResolverReceiptsMigrationCreatesAndRollsBack verifies the new receipt schema is additive while no acknowledgement exists.
func TestNetworkGlobalReleaseResolverReceiptsMigrationCreatesAndRollsBack(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseResolverReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "resolver")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver receipt migration: %v", err)
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"source_checkpoint_revision",
		"resolver_evidence_digest",
		"owned_absent_observation_fingerprint",
		"verified_at",
	} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_resolver_receipts", column) {
			t.Fatalf("receipt migration did not create network_global_release_resolver_receipts.%s", column)
		}
	}
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback resolver receipt migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_global_release_resolver_receipts") {
		t.Fatal("rollback retained network_global_release_resolver_receipts")
	}
}

// TestNetworkGlobalReleaseResolverReceiptsMigrationRejectsUnexplainedAdvancedPlans prevents upgrades from inventing a receipt for trust release work.
func TestNetworkGlobalReleaseResolverReceiptsMigrationRejectsUnexplainedAdvancedPlans(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseResolverReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "trust")
	if err := migration.Up(databaseConnection); err == nil {
		t.Fatal("receipt migration accepted unexplained trust phase")
	}
	if databaseConnection.Migrator().HasTable("network_global_release_resolver_receipts") {
		t.Fatal("rejected receipt migration created its table")
	}
}

// TestNetworkGlobalReleaseResolverReceiptsMigrationEnforcesReceiptAuthority verifies the singleton, source, evidence, timestamp, and plan owner constraints.
func TestNetworkGlobalReleaseResolverReceiptsMigrationEnforcesReceiptAuthority(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseResolverReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "resolver")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver receipt migration: %v", err)
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		`INSERT INTO network_global_release_resolver_receipts (
			id,
			operation_id,
			source_checkpoint_revision,
			resolver_evidence_digest,
			owned_absent_observation_fingerprint,
			verified_at
		)
		VALUES (1, 'operation-release', 3, ?, ?, '2026-07-22T04:30:00Z')`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)

	for _, test := range []struct {
		name      string
		statement string
	}{
		{
			name:      "singleton ID",
			statement: "UPDATE network_global_release_resolver_receipts SET id = 2 WHERE id = 1",
		},
		{
			name:      "source checkpoint",
			statement: "UPDATE network_global_release_resolver_receipts SET source_checkpoint_revision = 0 WHERE id = 1",
		},
		{
			name:      "evidence digest",
			statement: "UPDATE network_global_release_resolver_receipts SET resolver_evidence_digest = 'invalid' WHERE id = 1",
		},
		{
			name:      "uppercase evidence digest",
			statement: "UPDATE network_global_release_resolver_receipts SET resolver_evidence_digest = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE id = 1",
		},
		{
			name:      "observation fingerprint",
			statement: "UPDATE network_global_release_resolver_receipts SET owned_absent_observation_fingerprint = 'invalid' WHERE id = 1",
		},
		{
			name:      "uppercase observation fingerprint",
			statement: "UPDATE network_global_release_resolver_receipts SET owned_absent_observation_fingerprint = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB' WHERE id = 1",
		},
		{
			name:      "stored time",
			statement: "UPDATE network_global_release_resolver_receipts SET verified_at = '' WHERE id = 1",
		},
		{
			name:      "foreign plan",
			statement: "UPDATE network_global_release_resolver_receipts SET operation_id = 'operation-foreign' WHERE id = 1",
		},
		{
			name:      "delete plan owner",
			statement: "DELETE FROM network_global_release_plans WHERE id = 1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertMigrationStatementFails(t, databaseConnection, test.statement)
		})
	}
}

// TestNetworkGlobalReleaseResolverReceiptsMigrationRejectsAcknowledgedRollback preserves both receipt evidence and trust-phase boundaries.
func TestNetworkGlobalReleaseResolverReceiptsMigrationRejectsAcknowledgedRollback(t *testing.T) {
	for _, test := range []struct {
		name    string
		phase   string
		receipt bool
	}{
		{
			name:    "receipt",
			phase:   "resolver",
			receipt: true,
		},
		{
			name:  "advanced phase",
			phase: "trust",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseResolverReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseResolverReceiptPlan(t, databaseConnection, "resolver")
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply resolver receipt migration: %v", err)
			}
			if test.phase != "resolver" {
				mustExecNetworkSetupMigration(t, databaseConnection, "UPDATE network_global_release_plans SET phase = ? WHERE id = 1", test.phase)
			}
			if test.receipt {
				mustExecNetworkSetupMigration(
					t,
					databaseConnection,
					`INSERT INTO network_global_release_resolver_receipts (
						id,
						operation_id,
						source_checkpoint_revision,
						resolver_evidence_digest,
						owned_absent_observation_fingerprint,
						verified_at
					)
					VALUES (1, 'operation-release', 3, ?, ?, '2026-07-22T04:30:00Z')`,
					strings.Repeat("a", 64),
					strings.Repeat("b", 64),
				)
			}
			if err := migration.Down(databaseConnection); err == nil {
				t.Fatal("receipt migration rollback discarded acknowledged release state")
			}
			if !databaseConnection.Migrator().HasTable("network_global_release_resolver_receipts") {
				t.Fatal("rejected rollback removed the receipt table")
			}
		})
	}
}

// newNetworkGlobalReleaseResolverReceiptsPrerequisiteHarness applies the stream through the low-port receipt schema.
func newNetworkGlobalReleaseResolverReceiptsPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool { return migrationList[left].Name() < migrationList[right].Name() })
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseResolverReceiptsMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply resolver receipt prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("resolver receipt migration %q is not registered", networkGlobalReleaseResolverReceiptsMigrationName)
	return nil, nil
}

// seedNetworkGlobalReleaseResolverReceiptPlan installs one checkpointed pre-receipt plan for migration probes.
func seedNetworkGlobalReleaseResolverReceiptPlan(t *testing.T, databaseConnection *gorm.DB, phase string) {
	t.Helper()
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	mustExecNetworkSetupMigration(t, databaseConnection, `INSERT INTO network_global_release_plans (
		id,
		operation_id,
		operation_revision,
		checkpoint_revision,
		network_state_id,
		network_revision,
		network_updated_at,
		phase,
		authority_payload,
		authority_digest
	)
	VALUES (1, 'operation-release', 3, 3, 1, 5, ?, ?, '{}', ?)`, networkGlobalReleaseMigrationTime(), phase, strings.Repeat("a", 64))
}
