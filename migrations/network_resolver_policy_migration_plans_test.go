package migrations

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const networkResolverPolicyMigrationPlansMigrationName = "2026_07_22_050000_create_network_resolver_policy_migration_plans"
const networkResolverPolicyMigrationPlansRepairMigrationName = "2026_07_23_010000_repair_network_resolver_policy_migration_plans"

// TestNetworkResolverPolicyMigrationPlansMigrationCreatesBoundSingleton verifies the schema retains its complete authority dimensions.
func TestNetworkResolverPolicyMigrationPlansMigrationCreatesBoundSingleton(t *testing.T) {
	databaseConnection, migration := newNetworkResolverPolicyMigrationPlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver policy migration plan migration: %v", err)
	}
	if !databaseConnection.Migrator().HasTable("network_resolver_policy_migration_plans") {
		t.Fatal("migration did not create network_resolver_policy_migration_plans")
	}
	if !databaseConnection.Migrator().HasIndex("operations", "operations_one_active_network_resolver_policy_migration_idx") {
		t.Fatal("migration did not create active migration owner index")
	}
	for _, column := range []string{
		"operation_id", "operation_kind", "operation_state", "operation_phase", "operation_revision", "network_state_id", "network_revision",
		"source_ownership_schema_version", "source_ownership_fingerprint", "post_ownership_fingerprint", "replacement_policy_fingerprint",
	} {
		if !databaseConnection.Migrator().HasColumn("network_resolver_policy_migration_plans", column) {
			t.Fatalf("migration did not create network_resolver_policy_migration_plans.%s", column)
		}
	}
	seedNetworkResolverPolicyMigrationOwners(t, databaseConnection, "policy-migration", 4, 2)
	plan := defaultNetworkResolverPolicyMigrationPlan("policy-migration", 4, 2)
	if err := databaseConnection.Create(&plan).Error; err != nil {
		t.Fatalf("insert valid resolver policy migration plan: %v", err)
	}
	if err := databaseConnection.Exec("DELETE FROM network_resolver_policy_migration_plans").Error; err != nil {
		t.Fatalf("clear valid plan: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*models.NetworkResolverPolicyMigrationPlan)
	}{
		{
			name:   "singleton",
			mutate: func(plan *models.NetworkResolverPolicyMigrationPlan) { plan.Id = 2 },
		},
		{
			name:   "operation kind",
			mutate: func(plan *models.NetworkResolverPolicyMigrationPlan) { plan.OperationKind = "network.resolver.setup" },
		},
		{
			name:   "operation state",
			mutate: func(plan *models.NetworkResolverPolicyMigrationPlan) { plan.OperationState = "running" },
		},
		{
			name:   "operation phase",
			mutate: func(plan *models.NetworkResolverPolicyMigrationPlan) { plan.OperationPhase = "resolver" },
		},
		{
			name:   "network root",
			mutate: func(plan *models.NetworkResolverPolicyMigrationPlan) { plan.NetworkStateId = 2 },
		},
		{
			name:   "operation foreign key",
			mutate: func(plan *models.NetworkResolverPolicyMigrationPlan) { plan.OperationId = "missing" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := plan
			test.mutate(&candidate)
			if err := databaseConnection.Create(&candidate).Error; err == nil {
				t.Fatalf("malformed plan unexpectedly succeeded: %#v", candidate)
			}
		})
	}
}

// TestNetworkResolverPolicyMigrationPlansMigrationRollbackPreservesPlanAuthority verifies rollback never discards staged retirement authority.
func TestNetworkResolverPolicyMigrationPlansMigrationRollbackPreservesPlanAuthority(t *testing.T) {
	databaseConnection, migration := newNetworkResolverPolicyMigrationPlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver policy migration plan migration: %v", err)
	}
	seedNetworkResolverPolicyMigrationOwners(t, databaseConnection, "policy-migration", 4, 2)
	plan := defaultNetworkResolverPolicyMigrationPlan("policy-migration", 4, 2)
	if err := databaseConnection.Create(&plan).Error; err != nil {
		t.Fatalf("insert resolver policy migration plan: %v", err)
	}
	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("rollback discarded resolver policy migration authority")
	}
	if !databaseConnection.Migrator().HasTable("network_resolver_policy_migration_plans") {
		t.Fatal("failed rollback removed resolver policy migration plans")
	}
	if !databaseConnection.Migrator().HasIndex("operations", "operations_one_active_network_resolver_policy_migration_idx") {
		t.Fatal("failed rollback removed active migration owner index")
	}
	var persisted models.NetworkResolverPolicyMigrationPlan
	if err := databaseConnection.First(&persisted, 1).Error; err != nil {
		t.Fatalf("read resolver policy migration plan after failed rollback: %v", err)
	}
	if persisted != plan {
		t.Fatalf("failed rollback plan = %#v, want %#v", persisted, plan)
	}
	if err := databaseConnection.Exec("DELETE FROM network_resolver_policy_migration_plans").Error; err != nil {
		t.Fatalf("clear resolver policy migration plan: %v", err)
	}
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback empty resolver policy migration plan migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_resolver_policy_migration_plans") {
		t.Fatal("rollback retained resolver policy migration plans")
	}
	if databaseConnection.Migrator().HasIndex("operations", "operations_one_active_network_resolver_policy_migration_idx") {
		t.Fatal("rollback retained active migration owner index")
	}
	if !databaseConnection.Migrator().HasTable("operations") || !databaseConnection.Migrator().HasTable("network_state") {
		t.Fatal("rollback removed predecessor authority tables")
	}
}

// TestNetworkResolverPolicyMigrationPlansRepairUpgradesTransientSchema verifies existing development databases gain the missing operation authority.
func TestNetworkResolverPolicyMigrationPlansRepairUpgradesTransientSchema(t *testing.T) {
	databaseConnection, migration := newNetworkResolverPolicyMigrationPlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver policy migration plan migration: %v", err)
	}
	seedNetworkResolverPolicyMigrationOwners(t, databaseConnection, "policy-migration", 4, 2)
	plan := defaultNetworkResolverPolicyMigrationPlan("policy-migration", 4, 2)
	if err := databaseConnection.Create(&plan).Error; err != nil {
		t.Fatalf("insert resolver policy migration plan: %v", err)
	}
	removeTransientNetworkResolverPolicyMigrationPlanColumns(t, databaseConnection)

	repair := networkResolverPolicyMigrationPlansRepairMigration(t)
	if err := databaseConnection.Transaction(repair.Up); err != nil {
		t.Fatalf("repair resolver policy migration plan schema: %v", err)
	}

	for _, column := range []string{"operation_kind", "operation_state", "operation_phase"} {
		if !databaseConnection.Migrator().HasColumn("network_resolver_policy_migration_plans", column) {
			t.Fatalf("repair did not create network_resolver_policy_migration_plans.%s", column)
		}
	}
	var persisted models.NetworkResolverPolicyMigrationPlan
	if err := databaseConnection.First(&persisted, 1).Error; err != nil {
		t.Fatalf("read repaired resolver policy migration plan: %v", err)
	}
	if persisted != plan {
		t.Fatalf("repaired resolver policy migration plan = %#v, want %#v", persisted, plan)
	}
}

// TestNetworkResolverPolicyMigrationPlansRepairAcceptsCanonicalSchema verifies new databases can apply the repair after the complete creation migration.
func TestNetworkResolverPolicyMigrationPlansRepairAcceptsCanonicalSchema(t *testing.T) {
	databaseConnection, migration := newNetworkResolverPolicyMigrationPlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver policy migration plan migration: %v", err)
	}

	repair := networkResolverPolicyMigrationPlansRepairMigration(t)
	if err := databaseConnection.Transaction(repair.Up); err != nil {
		t.Fatalf("repair canonical resolver policy migration plan schema: %v", err)
	}
	for _, column := range []string{"operation_kind", "operation_state", "operation_phase"} {
		if !databaseConnection.Migrator().HasColumn("network_resolver_policy_migration_plans", column) {
			t.Fatalf("repair removed network_resolver_policy_migration_plans.%s", column)
		}
	}
}

// TestNetworkResolverPolicyMigrationPlansRepairRejectsMismatchedOperation verifies repair never invents authority for a staged row.
func TestNetworkResolverPolicyMigrationPlansRepairRejectsMismatchedOperation(t *testing.T) {
	databaseConnection, migration := newNetworkResolverPolicyMigrationPlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver policy migration plan migration: %v", err)
	}
	seedNetworkResolverPolicyMigrationOwners(t, databaseConnection, "policy-migration", 4, 2)
	plan := defaultNetworkResolverPolicyMigrationPlan("policy-migration", 4, 2)
	if err := databaseConnection.Create(&plan).Error; err != nil {
		t.Fatalf("insert resolver policy migration plan: %v", err)
	}
	removeTransientNetworkResolverPolicyMigrationPlanColumns(t, databaseConnection)
	if err := databaseConnection.Exec("UPDATE operations SET state = 'running' WHERE id = ?", plan.OperationId).Error; err != nil {
		t.Fatalf("drift operation state: %v", err)
	}

	repair := networkResolverPolicyMigrationPlansRepairMigration(t)
	if err := databaseConnection.Transaction(repair.Up); err == nil {
		t.Fatal("repair accepted a plan whose operation authority is no longer confirmable")
	}
	if !databaseConnection.Migrator().HasTable("network_resolver_policy_migration_plans") {
		t.Fatal("failed repair removed the original resolver policy migration plan table")
	}
	for _, column := range []string{"operation_kind", "operation_state", "operation_phase"} {
		if databaseConnection.Migrator().HasColumn("network_resolver_policy_migration_plans", column) {
			t.Fatalf("failed repair partially added network_resolver_policy_migration_plans.%s", column)
		}
	}
	var count int64
	if err := databaseConnection.Table("network_resolver_policy_migration_plans").Count(&count).Error; err != nil {
		t.Fatalf("count resolver policy migration plans after rejected repair: %v", err)
	}
	if count != 1 {
		t.Fatalf("resolver policy migration plan count after rejected repair = %d, want 1", count)
	}
}

// newNetworkResolverPolicyMigrationPlansPrerequisiteHarness applies production migrations through the immediate predecessor.
func newNetworkResolverPolicyMigrationPlansPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrations := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrations, func(left, right int) bool { return migrations[left].Name() < migrations[right].Name() })
	for _, migration := range migrations {
		if migration.Name() == networkResolverPolicyMigrationPlansMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply resolver policy migration prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("resolver policy migration %q is not registered", networkResolverPolicyMigrationPlansMigrationName)
	return nil, nil
}

// networkResolverPolicyMigrationPlansRepairMigration returns the production repair migration by stable identity.
func networkResolverPolicyMigrationPlansRepairMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkResolverPolicyMigrationPlansRepairMigrationName {
			return migration
		}
	}
	t.Fatalf("resolver policy migration plan repair %q is not registered", networkResolverPolicyMigrationPlansRepairMigrationName)
	return nil
}

// removeTransientNetworkResolverPolicyMigrationPlanColumns recreates the exact schema observed in pre-commit development databases.
func removeTransientNetworkResolverPolicyMigrationPlanColumns(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	for _, column := range []string{"operation_kind", "operation_state", "operation_phase"} {
		if err := databaseConnection.Exec("ALTER TABLE network_resolver_policy_migration_plans DROP COLUMN " + column).Error; err != nil {
			t.Fatalf("remove transient resolver policy migration plan column %s: %v", column, err)
		}
	}
}

// seedNetworkResolverPolicyMigrationOwners writes the exact foreign-key owners for one approval plan.
func seedNetworkResolverPolicyMigrationOwners(t *testing.T, databaseConnection *gorm.DB, operationID string, operationRevision, networkRevision int) {
	t.Helper()
	requested := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)
	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES (?, ?, 'network.resolver.policy-migration', NULL, 'requires_approval', 'awaiting resolver policy migration approval', ?, ?, ?)`, operationID, "intent-"+operationID, requested, requested, operationRevision).Error; err != nil {
		t.Fatalf("insert migration operation owner: %v", err)
	}
	if err := databaseConnection.Exec(`INSERT INTO network_state
		(id, stage, installation_id, ownership_generation, pool_network, pool_prefix_length, dns_suffix, created_at, updated_at, revision)
		VALUES (1, 'resolver', 'installation-a', 1, '127.77.0.0', 29, '.test', ?, ?, ?)`, requested, requested, networkRevision).Error; err != nil {
		t.Fatalf("insert migration network owner: %v", err)
	}
}

// defaultNetworkResolverPolicyMigrationPlan returns one complete legacy macOS authority row.
func defaultNetworkResolverPolicyMigrationPlan(operationID string, operationRevision, networkRevision int) models.NetworkResolverPolicyMigrationPlan {
	return models.NetworkResolverPolicyMigrationPlan{
		Id:                             1,
		OperationId:                    operationID,
		OperationKind:                  "network.resolver.policy-migration",
		OperationState:                 "requires_approval",
		OperationPhase:                 "awaiting resolver policy migration approval",
		OperationRevision:              operationRevision,
		NetworkStateId:                 1,
		NetworkRevision:                networkRevision,
		SourceOwnershipSchemaVersion:   2,
		SourceOwnershipFingerprint:     strings.Repeat("a", 64),
		SourceInstallationId:           "installation-a",
		SourceOwnerIdentity:            "501",
		SourceOwnershipGeneration:      1,
		SourceLoopbackPoolPrefix:       "127.77.0.0/29",
		SourceNetworkPolicyFingerprint: strings.Repeat("b", 64),
		SourceTicketVerifierKey:        networkResolverSetupVerifierKey,
		PostOwnershipFingerprint:       strings.Repeat("c", 64),
		ReplacementPolicyFingerprint:   strings.Repeat("d", 64),
		PolicySuffix:                   ".test",
		PolicyAuthorityFingerprint:     strings.Repeat("e", 64),
		PolicyResolverMechanism:        "darwin-resolver-file-v1",
		PolicyLowPortsMechanism:        "darwin-launchd-relay-v1",
		PolicyTrustMechanism:           "darwin-current-user-trust-v1",
		PolicyDnsAdvertisedAddress:     "127.0.0.1",
		PolicyDnsAdvertisedPort:        1053,
		PolicyDnsBindAddress:           "127.0.0.1",
		PolicyDnsBindPort:              1053,
		PolicyHttpAdvertisedAddress:    "127.0.0.1",
		PolicyHttpAdvertisedPort:       80,
		PolicyHttpBindAddress:          "127.0.0.1",
		PolicyHttpBindPort:             18080,
		PolicyHttpsAdvertisedAddress:   "127.0.0.1",
		PolicyHttpsAdvertisedPort:      443,
		PolicyHttpsBindAddress:         "127.0.0.1",
		PolicyHttpsBindPort:            18443,
	}
}
