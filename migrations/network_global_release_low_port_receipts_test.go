package migrations

import (
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const networkGlobalReleaseLowPortReceiptsMigrationName = "2026_07_22_042000_create_network_global_release_low_port_receipts"

// TestNetworkGlobalReleaseLowPortReceiptsMigrationCreatesAndRollsBack verifies the new receipt schema is additive while no acknowledgement exists.
func TestNetworkGlobalReleaseLowPortReceiptsMigrationCreatesAndRollsBack(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseLowPortReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseLowPortReceiptPlan(t, databaseConnection, "runtime_release")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply low-port receipt migration: %v", err)
	}
	for _, column := range []string{"id", "operation_id", "source_checkpoint_revision", "low_port_evidence_digest", "owned_absent_observation_fingerprint", "verified_at"} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_low_port_receipts", column) {
			t.Fatalf("receipt migration did not create network_global_release_low_port_receipts.%s", column)
		}
	}
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback low-port receipt migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_global_release_low_port_receipts") {
		t.Fatal("rollback retained network_global_release_low_port_receipts")
	}
}

// TestNetworkGlobalReleaseLowPortReceiptsMigrationRejectsUnexplainedAdvancedPlans prevents upgrades from inventing a receipt for completed low-port release work.
func TestNetworkGlobalReleaseLowPortReceiptsMigrationRejectsUnexplainedAdvancedPlans(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseLowPortReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseLowPortReceiptPlan(t, databaseConnection, "resolver")
	if err := migration.Up(databaseConnection); err == nil {
		t.Fatal("receipt migration accepted unexplained resolver phase")
	}
	if databaseConnection.Migrator().HasTable("network_global_release_low_port_receipts") {
		t.Fatal("rejected receipt migration created its table")
	}
}

// TestNetworkGlobalReleaseLowPortReceiptsMigrationEnforcesReceiptAuthority verifies the singleton, fingerprints, source, and plan owner constraints.
func TestNetworkGlobalReleaseLowPortReceiptsMigrationEnforcesReceiptAuthority(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleaseLowPortReceiptsPrerequisiteHarness(t)
	seedNetworkGlobalReleaseLowPortReceiptPlan(t, databaseConnection, "low_ports")
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply low-port receipt migration: %v", err)
	}
	mustExecNetworkSetupMigration(
		t,
		databaseConnection,
		`INSERT INTO network_global_release_low_port_receipts
			(id, operation_id, source_checkpoint_revision, low_port_evidence_digest, owned_absent_observation_fingerprint, verified_at)
			VALUES (1, 'operation-release', 3, ?, ?, ?)`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		networkGlobalReleaseMigrationTime(),
	)

	for _, test := range []struct {
		name      string
		statement string
	}{
		{
			name:      "singleton ID",
			statement: "UPDATE network_global_release_low_port_receipts SET id = 2 WHERE id = 1",
		},
		{
			name:      "source checkpoint",
			statement: "UPDATE network_global_release_low_port_receipts SET source_checkpoint_revision = 0 WHERE id = 1",
		},
		{
			name:      "evidence digest",
			statement: "UPDATE network_global_release_low_port_receipts SET low_port_evidence_digest = 'invalid' WHERE id = 1",
		},
		{
			name:      "observation fingerprint",
			statement: "UPDATE network_global_release_low_port_receipts SET owned_absent_observation_fingerprint = 'invalid' WHERE id = 1",
		},
		{
			name:      "foreign plan",
			statement: "UPDATE network_global_release_low_port_receipts SET operation_id = 'operation-foreign' WHERE id = 1",
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

// TestNetworkGlobalReleaseLowPortReceiptsMigrationRejectsAcknowledgedRollback preserves both receipt evidence and advanced phase boundaries.
func TestNetworkGlobalReleaseLowPortReceiptsMigrationRejectsAcknowledgedRollback(t *testing.T) {
	for _, test := range []struct {
		name    string
		phase   string
		receipt bool
	}{
		{
			name:    "receipt",
			phase:   "low_ports",
			receipt: true,
		},
		{
			name:  "advanced phase",
			phase: "resolver",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection, migration := newNetworkGlobalReleaseLowPortReceiptsPrerequisiteHarness(t)
			seedNetworkGlobalReleaseLowPortReceiptPlan(t, databaseConnection, "low_ports")
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply low-port receipt migration: %v", err)
			}
			if test.phase != "low_ports" {
				mustExecNetworkSetupMigration(t, databaseConnection, "UPDATE network_global_release_plans SET phase = ? WHERE id = 1", test.phase)
			}
			if test.receipt {
				mustExecNetworkSetupMigration(t, databaseConnection, `INSERT INTO network_global_release_low_port_receipts
					(id, operation_id, source_checkpoint_revision, low_port_evidence_digest, owned_absent_observation_fingerprint, verified_at)
					VALUES (1, 'operation-release', 3, ?, ?, ?)`, strings.Repeat("a", 64), strings.Repeat("b", 64), networkGlobalReleaseMigrationTime())
			}
			if err := migration.Down(databaseConnection); err == nil {
				t.Fatal("receipt migration rollback discarded acknowledged release state")
			}
			if !databaseConnection.Migrator().HasTable("network_global_release_low_port_receipts") {
				t.Fatal("rejected rollback removed the receipt table")
			}
		})
	}
}

// newNetworkGlobalReleaseLowPortReceiptsPrerequisiteHarness applies the stream through the checkpointed release-plan schema.
func newNetworkGlobalReleaseLowPortReceiptsPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrationList := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrationList, func(left, right int) bool { return migrationList[left].Name() < migrationList[right].Name() })
	for _, migration := range migrationList {
		if migration.Name() == networkGlobalReleaseLowPortReceiptsMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply low-port receipt prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("low-port receipt migration %q is not registered", networkGlobalReleaseLowPortReceiptsMigrationName)
	return nil, nil
}

// seedNetworkGlobalReleaseLowPortReceiptPlan installs one checkpointed pre-receipt plan for migration probes.
func seedNetworkGlobalReleaseLowPortReceiptPlan(t *testing.T, databaseConnection *gorm.DB, phase string) {
	t.Helper()
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	mustExecNetworkSetupMigration(t, databaseConnection, `INSERT INTO network_global_release_plans
		(id, operation_id, operation_revision, checkpoint_revision, network_state_id, network_revision, network_updated_at, phase, authority_payload, authority_digest)
		VALUES (1, 'operation-release', 3, 3, 1, 5, ?, ?, '{}', ?)`, networkGlobalReleaseMigrationTime(), phase, strings.Repeat("a", 64))
}
