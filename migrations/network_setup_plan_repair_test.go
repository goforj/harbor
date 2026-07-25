package migrations

import (
	"strings"
	"testing"
)

const networkSetupPlanRepairMigrationName = "2026_07_25_020000_expand_network_setup_plans_for_repair"

// TestNetworkSetupPlanRepairMigrationPersistsExactUpgradedOwnership proves repair authority survives restart without truncation.
func TestNetworkSetupPlanRepairMigrationPersistsExactUpgradedOwnership(t *testing.T) {
	databaseConnection := newNetworkSetupMigrationHarness(t)
	migration := networkSetupPlanRepairMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply network setup repair migration: %v", err)
	}
	insertNetworkSetupMigrationOperation(t, databaseConnection, "operation-repair", "requires_approval", 1)
	policyFingerprint := strings.Repeat("a", 64)
	if err := databaseConnection.Exec(`INSERT INTO network_setup_plans
		(id, operation_id, operation_revision, ownership_schema_version, installation_id,
		 owner_identity, ownership_generation, loopback_pool_prefix, network_policy_fingerprint,
		 ticket_verifier_key)
		VALUES (1, 'operation-repair', 1, 2, 'installation-a', '501', 2,
		        '127.77.0.8/29', ?, ?)`,
		policyFingerprint,
		networkSetupTestVerifierKey,
	).Error; err != nil {
		t.Fatalf("insert upgraded network repair plan: %v", err)
	}

	var read struct {
		SchemaVersion     int    `gorm:"column:ownership_schema_version"`
		Generation        int    `gorm:"column:ownership_generation"`
		PolicyFingerprint string `gorm:"column:network_policy_fingerprint"`
	}
	if err := databaseConnection.Raw(`SELECT ownership_schema_version, ownership_generation,
		network_policy_fingerprint FROM network_setup_plans WHERE id = 1`).Scan(&read).Error; err != nil {
		t.Fatalf("read upgraded network repair plan: %v", err)
	}
	if read.SchemaVersion != 2 || read.Generation != 2 || read.PolicyFingerprint != policyFingerprint {
		t.Fatalf("upgraded network repair plan = %#v", read)
	}

	mustExecNetworkSetupMigration(t, databaseConnection, "DELETE FROM network_setup_plans")
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback empty network setup repair migration: %v", err)
	}
}

// networkSetupPlanRepairMigration resolves the additive repair schema by its embedded stable identity.
func networkSetupPlanRepairMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkSetupPlanRepairMigrationName {
			return migration
		}
	}
	t.Fatalf("network setup plan repair migration %q is not registered", networkSetupPlanRepairMigrationName)
	return nil
}
