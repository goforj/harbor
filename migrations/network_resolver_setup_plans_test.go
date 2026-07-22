package migrations

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

const networkResolverSetupPlansMigrationName = "2026_07_20_020000_create_network_resolver_setup_plans"

const networkResolverSetupAdministratorTrustMigrationName = "2026_07_22_048000_add_network_resolver_setup_administrator_trust"

const networkResolverSetupVerifierKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// TestNetworkResolverSetupPlansMigrationCreatesExactAuthoritySchema verifies every persisted plan dimension and owner index.
func TestNetworkResolverSetupPlansMigrationCreatesExactAuthoritySchema(t *testing.T) {
	databaseConnection := newNetworkResolverSetupMigrationHarness(t)
	if !databaseConnection.Migrator().HasTable("network_resolver_setup_plans") {
		t.Fatal("migration did not create network_resolver_setup_plans")
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"operation_revision",
		"network_state_id",
		"network_revision",
		"source_ownership_fingerprint",
		"target_ownership_schema_version",
		"target_installation_id",
		"target_owner_identity",
		"target_ownership_generation",
		"target_loopback_pool_prefix",
		"target_network_policy_fingerprint",
		"target_ticket_verifier_key",
		"policy_suffix",
		"policy_authority_fingerprint",
		"policy_resolver_mechanism",
		"policy_low_ports_mechanism",
		"policy_trust_mechanism",
		"policy_dns_advertised_address",
		"policy_dns_advertised_port",
		"policy_dns_bind_address",
		"policy_dns_bind_port",
		"policy_http_advertised_address",
		"policy_http_advertised_port",
		"policy_http_bind_address",
		"policy_http_bind_port",
		"policy_https_advertised_address",
		"policy_https_advertised_port",
		"policy_https_bind_address",
		"policy_https_bind_port",
	} {
		if !databaseConnection.Migrator().HasColumn("network_resolver_setup_plans", column) {
			t.Fatalf("migration did not create network_resolver_setup_plans.%s", column)
		}
	}
	for _, forbidden := range []string{"project_id", "ticket_reference", "created_at", "updated_at"} {
		if databaseConnection.Migrator().HasColumn("network_resolver_setup_plans", forbidden) {
			t.Fatalf("resolver setup plan retained transient column %s", forbidden)
		}
	}
	for table, index := range map[string]string{
		"network_state": "network_state_resolver_setup_revision_idx",
		"operations":    "operations_one_active_network_resolver_setup_idx",
	} {
		if !databaseConnection.Migrator().HasIndex(table, index) {
			t.Fatalf("migration did not create %s on %s", index, table)
		}
	}
}

// TestNetworkResolverSetupPlansMigrationPersistsEverySupportedPolicy verifies platform profiles retain every field exactly.
func TestNetworkResolverSetupPlansMigrationPersistsEverySupportedPolicy(t *testing.T) {
	for _, profile := range []struct {
		name   string
		mutate func(*models.NetworkResolverSetupPlan)
	}{
		{name: "macOS", mutate: func(plan *models.NetworkResolverSetupPlan) {
			plan.PolicyResolverMechanism = "darwin-resolver-file-v1"
			plan.PolicyLowPortsMechanism = "darwin-launchd-relay-v1"
			plan.PolicyTrustMechanism = "darwin-current-user-trust-v1"
		}},
		{name: "Ubuntu", mutate: func(*models.NetworkResolverSetupPlan) {}},
		{name: "Windows", mutate: func(plan *models.NetworkResolverSetupPlan) {
			plan.PolicyResolverMechanism = "windows-nrpt-v1"
			plan.PolicyLowPortsMechanism = "windows-direct-low-ports-v1"
			plan.PolicyTrustMechanism = "windows-current-user-trust-v1"
			plan.PolicyDnsAdvertisedAddress = "127.0.0.2"
			plan.PolicyDnsAdvertisedPort = 53
			plan.PolicyDnsBindAddress = "127.0.0.2"
			plan.PolicyDnsBindPort = 53
			plan.PolicyHttpBindPort = 80
			plan.PolicyHttpsBindPort = 443
		}},
	} {
		t.Run(profile.name, func(t *testing.T) {
			databaseConnection := newNetworkResolverSetupMigrationHarness(t)
			seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)
			want := defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 1)
			profile.mutate(&want)
			insertNetworkResolverSetupMigrationPlan(t, databaseConnection, want)

			var read models.NetworkResolverSetupPlan
			if err := databaseConnection.First(&read, 1).Error; err != nil {
				t.Fatalf("read resolver setup plan: %v", err)
			}
			if read != want {
				t.Fatalf("resolver setup plan = %#v, want %#v", read, want)
			}
			assertNetworkResolverSetupForeignKeysClean(t, databaseConnection)
		})
	}
}

// TestNetworkResolverSetupAdministratorTrustMigrationAdmitsOnlyCompleteMacOSProfiles verifies the forward schema upgrade preserves legacy plans and permits only the new complete administrator profile.
func TestNetworkResolverSetupAdministratorTrustMigrationAdmitsOnlyCompleteMacOSProfiles(t *testing.T) {
	databaseConnection := newNetworkResolverSetupMigrationHarness(t)
	seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)

	legacy := defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 1)
	legacy.PolicyResolverMechanism = "darwin-resolver-file-v1"
	legacy.PolicyLowPortsMechanism = "darwin-launchd-relay-v1"
	legacy.PolicyTrustMechanism = "darwin-current-user-trust-v1"
	insertNetworkResolverSetupMigrationPlan(t, databaseConnection, legacy)

	migration := networkResolverSetupAdministratorTrustMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply administrator trust migration: %v", err)
	}

	var read models.NetworkResolverSetupPlan
	if err := databaseConnection.First(&read, 1).Error; err != nil {
		t.Fatalf("read preserved legacy plan: %v", err)
	}
	if read != legacy {
		t.Fatalf("preserved legacy plan = %#v, want %#v", read, legacy)
	}
	assertNetworkResolverSetupForeignKeysClean(t, databaseConnection)
	for table, index := range map[string]string{
		"network_state": "network_state_resolver_setup_revision_idx",
		"operations":    "operations_one_active_network_resolver_setup_idx",
	} {
		if !databaseConnection.Migrator().HasIndex(table, index) {
			t.Fatalf("administrator trust migration removed %s on %s", index, table)
		}
	}

	mustExecNetworkResolverSetupMigration(t, databaseConnection, "DELETE FROM network_resolver_setup_plans")
	administrator := legacy
	administrator.PolicyTrustMechanism = "darwin-administrator-trust-v1"
	insertNetworkResolverSetupMigrationPlan(t, databaseConnection, administrator)

	mustExecNetworkResolverSetupMigration(t, databaseConnection, "DELETE FROM network_resolver_setup_plans")
	mixed := administrator
	mixed.PolicyLowPortsMechanism = "ubuntu-nftables-v1"
	if err := executeNetworkResolverSetupMigrationPlan(databaseConnection, mixed); err == nil {
		t.Fatalf("mixed administrator plan unexpectedly succeeded: %#v", mixed)
	}
}

// TestNetworkResolverSetupAdministratorTrustMigrationRefusesUnsafeRollback proves downgrade cannot discard an active administrator-trust approval.
func TestNetworkResolverSetupAdministratorTrustMigrationRefusesUnsafeRollback(t *testing.T) {
	databaseConnection := newNetworkResolverSetupMigrationHarness(t)
	seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)
	migration := networkResolverSetupAdministratorTrustMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply administrator trust migration: %v", err)
	}

	administrator := defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 1)
	administrator.PolicyResolverMechanism = "darwin-resolver-file-v1"
	administrator.PolicyLowPortsMechanism = "darwin-launchd-relay-v1"
	administrator.PolicyTrustMechanism = "darwin-administrator-trust-v1"
	insertNetworkResolverSetupMigrationPlan(t, databaseConnection, administrator)

	if err := migration.Down(databaseConnection); err == nil {
		t.Fatal("administrator trust migration rollback unexpectedly succeeded")
	}
	var preserved models.NetworkResolverSetupPlan
	if err := databaseConnection.First(&preserved, 1).Error; err != nil {
		t.Fatalf("read administrator plan after refused rollback: %v", err)
	}
	if preserved != administrator {
		t.Fatalf("administrator plan after refused rollback = %#v, want %#v", preserved, administrator)
	}
	assertNetworkResolverSetupForeignKeysClean(t, databaseConnection)

	mustExecNetworkResolverSetupMigration(t, databaseConnection, "DELETE FROM network_resolver_setup_plans")
	legacy := administrator
	legacy.PolicyTrustMechanism = "darwin-current-user-trust-v1"
	insertNetworkResolverSetupMigrationPlan(t, databaseConnection, legacy)
	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback administrator trust migration after removing administrator plan: %v", err)
	}
	var downgraded models.NetworkResolverSetupPlan
	if err := databaseConnection.First(&downgraded, 1).Error; err != nil {
		t.Fatalf("read legacy plan after rollback: %v", err)
	}
	if downgraded != legacy {
		t.Fatalf("legacy plan after rollback = %#v, want %#v", downgraded, legacy)
	}
	assertNetworkResolverSetupForeignKeysClean(t, databaseConnection)
}

// TestNetworkResolverSetupPlansMigrationRejectsMalformedAuthority proves direct writers cannot broaden durable approval.
func TestNetworkResolverSetupPlansMigrationRejectsMalformedAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*models.NetworkResolverSetupPlan)
	}{
		{name: "non-singleton ID", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.Id = 2 }},
		{name: "zero operation revision", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.OperationRevision = 0 }},
		{name: "unsafe operation revision", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.OperationRevision = 9007199254740992 }},
		{name: "foreign network singleton", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.NetworkStateId = 2 }},
		{name: "zero network revision", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.NetworkRevision = 0 }},
		{name: "unsafe network revision", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.NetworkRevision = 9007199254740992 }},
		{name: "short source fingerprint", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.SourceOwnershipFingerprint = strings.Repeat("a", 63) }},
		{name: "uppercase source fingerprint", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.SourceOwnershipFingerprint = strings.Repeat("A", 64) }},
		{name: "target schema", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetOwnershipSchemaVersion = 1 }},
		{name: "empty installation", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetInstallationId = " " }},
		{name: "unsafe installation", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetInstallationId = "-installation" }},
		{name: "long installation", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetInstallationId = strings.Repeat("a", 129) }},
		{name: "empty owner", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetOwnerIdentity = "" }},
		{name: "unsafe owner", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetOwnerIdentity = "501/502" }},
		{name: "zero generation", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetOwnershipGeneration = 0 }},
		{name: "unsafe generation", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetOwnershipGeneration = 9007199254740992 }},
		{name: "public pool", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetLoopbackPoolPrefix = "192.0.2.0/29" }},
		{name: "wide pool", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetLoopbackPoolPrefix = "127.77.0.0/24" }},
		{name: "unaligned pool", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetLoopbackPoolPrefix = "127.77.0.9/29" }},
		{name: "target policy fingerprint", mutate: func(plan *models.NetworkResolverSetupPlan) {
			plan.TargetNetworkPolicyFingerprint = strings.Repeat("g", 64)
		}},
		{name: "short verifier", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetTicketVerifierKey = strings.Repeat("A", 43) }},
		{name: "unpadded verifier", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.TargetTicketVerifierKey = strings.Repeat("A", 44) }},
		{name: "suffix", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicySuffix = ".localhost" }},
		{name: "authority fingerprint", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyAuthorityFingerprint = strings.Repeat("A", 64) }},
		{name: "mixed mechanisms", mutate: func(plan *models.NetworkResolverSetupPlan) {
			plan.PolicyTrustMechanism = "darwin-current-user-trust-v1"
		}},
		{name: "unknown resolver", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyResolverMechanism = "unknown" }},
		{name: "public DNS address", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyDnsBindAddress = "192.0.2.1" }},
		{name: "zero DNS port", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyDnsBindPort = 0 }},
		{name: "advertised HTTP", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyHttpAdvertisedPort = 8080 }},
		{name: "advertised HTTPS", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyHttpsAdvertisedAddress = "127.0.0.2" }},
		{name: "redirected DNS low port", mutate: func(plan *models.NetworkResolverSetupPlan) {
			plan.PolicyDnsAdvertisedPort, plan.PolicyDnsBindPort = 53, 53
		}},
		{name: "redirected DNS mismatch", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyDnsBindPort++ }},
		{name: "direct redirected HTTP", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyHttpBindPort = 80 }},
		{name: "low redirected HTTPS", mutate: func(plan *models.NetworkResolverSetupPlan) { plan.PolicyHttpsBindPort = 844 }},
		{name: "cross-protocol collision", mutate: func(plan *models.NetworkResolverSetupPlan) {
			plan.PolicyDnsAdvertisedPort = plan.PolicyHttpBindPort
			plan.PolicyDnsBindPort = plan.PolicyHttpBindPort
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection := newNetworkResolverSetupMigrationHarness(t)
			seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)
			plan := defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 1)
			test.mutate(&plan)
			if err := executeNetworkResolverSetupMigrationPlan(databaseConnection, plan); err == nil {
				t.Fatalf("invalid resolver setup plan unexpectedly succeeded: %#v", plan)
			}
		})
	}

	for _, test := range []struct {
		name string
		plan func() models.NetworkResolverSetupPlan
	}{
		{name: "missing operation", plan: func() models.NetworkResolverSetupPlan {
			return defaultNetworkResolverSetupMigrationPlan("operation-missing", 3, 1)
		}},
		{name: "wrong operation revision", plan: func() models.NetworkResolverSetupPlan {
			return defaultNetworkResolverSetupMigrationPlan("operation-resolver", 2, 1)
		}},
		{name: "wrong network revision", plan: func() models.NetworkResolverSetupPlan {
			return defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 2)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection := newNetworkResolverSetupMigrationHarness(t)
			seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)
			if err := executeNetworkResolverSetupMigrationPlan(databaseConnection, test.plan()); err == nil {
				t.Fatalf("resolver setup plan with invalid foreign owner unexpectedly succeeded")
			}
		})
	}

	t.Run("second singleton", func(t *testing.T) {
		databaseConnection := newNetworkResolverSetupMigrationHarness(t)
		seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)
		plan := defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 1)
		insertNetworkResolverSetupMigrationPlan(t, databaseConnection, plan)
		if err := executeNetworkResolverSetupMigrationPlan(databaseConnection, plan); err == nil {
			t.Fatal("second resolver setup singleton unexpectedly succeeded")
		}
	})
}

// TestNetworkResolverSetupPlansMigrationSerializesGlobalApproval verifies only one active resolver operation can exist.
func TestNetworkResolverSetupPlansMigrationSerializesGlobalApproval(t *testing.T) {
	databaseConnection := newNetworkResolverSetupMigrationHarness(t)
	insertNetworkResolverSetupMigrationOperation(t, databaseConnection, "operation-first", "queued", 1)
	if err := executeNetworkResolverSetupMigrationOperation(databaseConnection, "operation-racing", "running", 2); err == nil {
		t.Fatal("competing active resolver operation unexpectedly succeeded")
	}
	started := networkResolverSetupMigrationTime().Add(time.Second)
	finished := started.Add(time.Second)
	mustExecNetworkResolverSetupMigration(t, databaseConnection, `UPDATE operations
		SET state = 'succeeded', phase = 'complete', started_at = ?, finished_at = ?
		WHERE id = 'operation-first'`, started, finished)
	insertNetworkResolverSetupMigrationOperation(t, databaseConnection, "operation-next", "requires_approval", 2)
}

// TestNetworkResolverSetupPlansMigrationLocksBothAuthorityRevisions verifies plans cannot drift from either owner.
func TestNetworkResolverSetupPlansMigrationLocksBothAuthorityRevisions(t *testing.T) {
	databaseConnection := newNetworkResolverSetupMigrationHarness(t)
	seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)
	insertNetworkResolverSetupMigrationPlan(
		t,
		databaseConnection,
		defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 1),
	)
	assertMigrationStatementFails(t, databaseConnection, "UPDATE operations SET revision = 4 WHERE id = 'operation-resolver'")
	assertMigrationStatementFails(t, databaseConnection, "UPDATE network_state SET revision = 2 WHERE id = 1")
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM network_state WHERE id = 1")

	mustExecNetworkResolverSetupMigration(t, databaseConnection, "DELETE FROM network_resolver_setup_plans WHERE id = 1")
	mustExecNetworkResolverSetupMigration(t, databaseConnection, "UPDATE operations SET revision = 4 WHERE id = 'operation-resolver'")
	mustExecNetworkResolverSetupMigration(t, databaseConnection, "UPDATE network_state SET revision = 2 WHERE id = 1")

	t.Run("operation delete cascades", func(t *testing.T) {
		databaseConnection := newNetworkResolverSetupMigrationHarness(t)
		seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-cascade", 3, 1)
		insertNetworkResolverSetupMigrationPlan(
			t,
			databaseConnection,
			defaultNetworkResolverSetupMigrationPlan("operation-cascade", 3, 1),
		)
		mustExecNetworkResolverSetupMigration(t, databaseConnection, "DELETE FROM operations WHERE id = 'operation-cascade'")
		assertProjectionCount(t, databaseConnection, "network_resolver_setup_plans", 0)
	})
}

// TestNetworkResolverSetupPlansMigrationRollbackPreservesOwners verifies reversal removes only resolver staging schema.
func TestNetworkResolverSetupPlansMigrationRollbackPreservesOwners(t *testing.T) {
	databaseConnection, migration := newNetworkResolverSetupMigrationPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver setup plan migration: %v", err)
	}
	seedNetworkResolverSetupMigrationOwners(t, databaseConnection, "operation-resolver", 3, 1)
	insertNetworkResolverSetupMigrationPlan(
		t,
		databaseConnection,
		defaultNetworkResolverSetupMigrationPlan("operation-resolver", 3, 1),
	)

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback resolver setup plan migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_resolver_setup_plans") {
		t.Fatal("rollback retained network_resolver_setup_plans")
	}
	for table, index := range map[string]string{
		"network_state": "network_state_resolver_setup_revision_idx",
		"operations":    "operations_one_active_network_resolver_setup_idx",
	} {
		if databaseConnection.Migrator().HasIndex(table, index) {
			t.Fatalf("rollback retained %s on %s", index, table)
		}
	}
	assertProjectionCount(t, databaseConnection, "operations", 1)
	assertProjectionCount(t, databaseConnection, "network_state", 1)
	if !databaseConnection.Migrator().HasIndex("operations", "operations_network_setup_revision_idx") {
		t.Fatal("rollback removed predecessor operation revision authority")
	}
}

// newNetworkResolverSetupMigrationHarness applies every production prerequisite and the resolver setup migration.
func newNetworkResolverSetupMigrationHarness(t *testing.T) *gorm.DB {
	t.Helper()
	databaseConnection, migration := newNetworkResolverSetupMigrationPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply resolver setup plan migration: %v", err)
	}
	return databaseConnection
}

// newNetworkResolverSetupMigrationPrerequisiteHarness applies production migrations up to the resolver-plan boundary.
func newNetworkResolverSetupMigrationPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrations := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrations, func(left, right int) bool { return migrations[left].Name() < migrations[right].Name() })
	for _, migration := range migrations {
		if migration.Name() == networkResolverSetupPlansMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply resolver setup prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("network resolver setup migration %q is not registered", networkResolverSetupPlansMigrationName)
	return nil, nil
}

// networkResolverSetupAdministratorTrustMigration returns the registered schema upgrade under test.
func networkResolverSetupAdministratorTrustMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range GetMigrations() {
		if migration.Name() == networkResolverSetupAdministratorTrustMigrationName &&
			migration.App() == "harbord" &&
			migration.Connection() == "default" {
			return migration
		}
	}
	t.Fatalf("administrator trust migration %q is not registered", networkResolverSetupAdministratorTrustMigrationName)
	return nil
}

// seedNetworkResolverSetupMigrationOwners inserts the exact operation and network revision referenced by a plan.
func seedNetworkResolverSetupMigrationOwners(
	t *testing.T,
	databaseConnection *gorm.DB,
	operationID string,
	operationRevision int,
	networkRevision int,
) {
	t.Helper()
	insertNetworkResolverSetupMigrationOperation(t, databaseConnection, operationID, "requires_approval", operationRevision)
	mustExecNetworkResolverSetupMigration(t, databaseConnection, `INSERT INTO network_state
		(id, stage, installation_id, ownership_generation, pool_network, pool_prefix_length,
		dns_suffix, created_at, updated_at, revision)
		VALUES (1, 'identity', 'installation-a', 1, '127.77.0.0', 29, '.test', ?, ?, ?)`,
		networkResolverSetupMigrationTime(),
		networkResolverSetupMigrationTime(),
		networkRevision,
	)
}

// insertNetworkResolverSetupMigrationOperation writes one valid global resolver operation in the requested active state.
func insertNetworkResolverSetupMigrationOperation(
	t *testing.T,
	databaseConnection *gorm.DB,
	operationID string,
	state string,
	revision int,
) {
	t.Helper()
	if err := executeNetworkResolverSetupMigrationOperation(databaseConnection, operationID, state, revision); err != nil {
		t.Fatalf("insert resolver setup operation %q: %v", operationID, err)
	}
}

// executeNetworkResolverSetupMigrationOperation shares one SQL path between valid and competing active operations.
func executeNetworkResolverSetupMigrationOperation(
	databaseConnection *gorm.DB,
	operationID string,
	state string,
	revision int,
) error {
	requested := networkResolverSetupMigrationTime()
	var started *time.Time
	if state == "running" || state == "requires_approval" {
		value := requested.Add(time.Second)
		started = &value
	}
	return databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, started_at, revision)
		VALUES (?, ?, 'network.resolver.setup', NULL, ?, ?, ?, ?, ?)`,
		operationID,
		"intent-"+operationID,
		state,
		state,
		requested,
		started,
		revision,
	).Error
}

// defaultNetworkResolverSetupMigrationPlan returns one complete canonical Ubuntu approval record.
func defaultNetworkResolverSetupMigrationPlan(
	operationID string,
	operationRevision int,
	networkRevision int,
) models.NetworkResolverSetupPlan {
	return models.NetworkResolverSetupPlan{
		Id:                             1,
		OperationId:                    operationID,
		OperationRevision:              operationRevision,
		NetworkStateId:                 1,
		NetworkRevision:                networkRevision,
		SourceOwnershipFingerprint:     strings.Repeat("a", 64),
		TargetOwnershipSchemaVersion:   2,
		TargetInstallationId:           "installation-a",
		TargetOwnerIdentity:            "501",
		TargetOwnershipGeneration:      1,
		TargetLoopbackPoolPrefix:       "127.77.0.0/29",
		TargetNetworkPolicyFingerprint: strings.Repeat("b", 64),
		TargetTicketVerifierKey:        networkResolverSetupVerifierKey,
		PolicySuffix:                   ".test",
		PolicyAuthorityFingerprint:     strings.Repeat("c", 64),
		PolicyResolverMechanism:        "ubuntu-systemd-resolved-v1",
		PolicyLowPortsMechanism:        "ubuntu-nftables-v1",
		PolicyTrustMechanism:           "ubuntu-system-trust-v1",
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

// insertNetworkResolverSetupMigrationPlan inserts one plan expected to satisfy every schema invariant.
func insertNetworkResolverSetupMigrationPlan(
	t *testing.T,
	databaseConnection *gorm.DB,
	plan models.NetworkResolverSetupPlan,
) {
	t.Helper()
	if err := executeNetworkResolverSetupMigrationPlan(databaseConnection, plan); err != nil {
		t.Fatalf("insert network resolver setup plan: %v", err)
	}
}

// executeNetworkResolverSetupMigrationPlan keeps valid and malformed records on the same generated-model path.
func executeNetworkResolverSetupMigrationPlan(
	databaseConnection *gorm.DB,
	plan models.NetworkResolverSetupPlan,
) error {
	return databaseConnection.Create(&plan).Error
}

// mustExecNetworkResolverSetupMigration executes one schema probe that must succeed.
func mustExecNetworkResolverSetupMigration(
	t *testing.T,
	databaseConnection *gorm.DB,
	statement string,
	arguments ...any,
) {
	t.Helper()
	if err := databaseConnection.Exec(statement, arguments...).Error; err != nil {
		t.Fatalf("execute network resolver setup migration statement: %v", err)
	}
}

// assertNetworkResolverSetupForeignKeysClean verifies both composite authority references are satisfied.
func assertNetworkResolverSetupForeignKeysClean(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var violations []struct {
		Table string
		RowID int
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		t.Fatalf("check resolver setup foreign keys: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("resolver setup foreign key violations = %#v", violations)
	}
}

// networkResolverSetupMigrationTime returns the stable UTC time shared by migration fixtures.
func networkResolverSetupMigrationTime() time.Time {
	return time.Date(2026, time.July, 20, 2, 0, 0, 0, time.UTC)
}
