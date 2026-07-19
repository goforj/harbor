package migrations

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

const (
	// networkPersistenceMigrationName identifies the original production network schema migration.
	networkPersistenceMigrationName = "2026_07_18_152632_create_network_persistence"
	// networkReleaseDigestMigrationName identifies the replay-proof schema upgrade.
	networkReleaseDigestMigrationName = "2026_07_18_175743_add_network_release_set_digest"
	// networkStageMigrationName identifies the identity-stage compatibility upgrade.
	networkStageMigrationName = "2026_07_19_120000_add_network_stage"
	// networkMigrationReleaseSetDigest is the canonical digest fixture accepted by the upgraded schema.
	networkMigrationReleaseSetDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// TestNetworkPersistenceMigrationCreatesDurableSchema verifies the embedded migration owns the complete network aggregate without creating competing revision owners.
func TestNetworkPersistenceMigrationCreatesDurableSchema(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyNetworkPersistenceMigrations(t, databaseConnection)

	tables := []string{
		"network_state",
		"network_pool_candidates",
		"network_setup_evidence",
		"network_shared_listeners",
		"loopback_address_leases",
		"public_endpoint_leases",
		"network_project_releases",
	}
	for _, table := range tables {
		if !databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("migration did not create %s", table)
		}
	}

	indexes := map[string][]string{
		"network_pool_candidates":  {"network_pool_candidates_order_idx"},
		"network_setup_evidence":   {"network_setup_evidence_order_idx"},
		"network_shared_listeners": {"network_shared_listeners_order_idx"},
		"loopback_address_leases": {
			"loopback_address_leases_state_idx",
			"loopback_address_leases_project_idx",
			"loopback_address_leases_active_key_idx",
		},
		"public_endpoint_leases": {
			"public_endpoint_leases_project_idx",
			"public_endpoint_leases_socket_idx",
			"public_endpoint_leases_tcp_socket_idx",
		},
		"network_project_releases": {"network_project_releases_state_idx"},
	}
	for table, names := range indexes {
		for _, name := range names {
			if !databaseConnection.Migrator().HasIndex(table, name) {
				t.Fatalf("migration did not create %s on %s", name, table)
			}
		}
	}

	assertProjectionCount(t, databaseConnection, "network_state", 0)
	var sequence int
	if err := databaseConnection.Raw("SELECT sequence FROM harbor_state WHERE id = 1").Scan(&sequence).Error; err != nil {
		t.Fatalf("read Harbor sequence after network migration: %v", err)
	}
	if sequence != 0 {
		t.Fatalf("Harbor sequence after network migration = %d, want unchanged value 0", sequence)
	}

	insertNetworkMigrationState(t, databaseConnection, 1)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO network_state
		(id, installation_id, ownership_generation, pool_network, pool_prefix_length, dns_suffix, created_at, updated_at, revision)
		VALUES (2, 'installation-b', 1, '127.78.0.0', 24, '.test', '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z', 2)`)
	assertMigrationStatementFails(t, databaseConnection, `UPDATE network_state SET revision = 0 WHERE id = 1`)
	assertMigrationStatementFails(t, databaseConnection, `UPDATE network_state SET installation_id = '-unsafe' WHERE id = 1`)
	assertMigrationStatementFails(t, databaseConnection, `UPDATE network_state SET ownership_generation = 0 WHERE id = 1`)
	assertMigrationStatementFails(t, databaseConnection, `UPDATE network_state SET pool_network = '192.0.2.0' WHERE id = 1`)
	assertMigrationStatementFails(t, databaseConnection, `UPDATE network_state SET pool_prefix_length = 7 WHERE id = 1`)
	assertMigrationStatementFails(t, databaseConnection, `UPDATE network_state SET dns_suffix = '.local' WHERE id = 1`)
	assertMigrationStatementFails(t, databaseConnection, `UPDATE network_state SET updated_at = '2026-07-18T11:59:59Z' WHERE id = 1`)

	for _, table := range tables[1:] {
		for _, ownerColumn := range []string{"revision", "sequence"} {
			if migrationTableHasColumn(t, databaseConnection, table, ownerColumn) {
				t.Fatalf("%s unexpectedly owns independent %s ordering", table, ownerColumn)
			}
		}
	}
	for _, forbidden := range []string{"upstream", "upstream_address", "upstream_port", "pid", "container_id", "session_id", "private_port", "high_port"} {
		if migrationTableHasColumn(t, databaseConnection, "public_endpoint_leases", forbidden) {
			t.Fatalf("public_endpoint_leases persists runtime-only column %q", forbidden)
		}
	}
	if !migrationTableHasColumn(t, databaseConnection, "network_project_releases", "release_set_digest") {
		t.Fatal("network_project_releases is missing release_set_digest")
	}
	if !migrationTableHasColumn(t, databaseConnection, "network_state", "stage") {
		t.Fatal("network_state is missing stage")
	}
	var stage string
	if err := databaseConnection.Raw("SELECT stage FROM network_state WHERE id = 1").Scan(&stage).Error; err != nil {
		t.Fatalf("read migrated network stage: %v", err)
	}
	if stage != "full" {
		t.Fatalf("migrated network stage = %q, want full", stage)
	}
}

// TestNetworkStageMigrationPreservesFullRowsAndRefusesLossyRollback verifies upgrades stay compatible while identity authority cannot be silently reinterpreted.
func TestNetworkStageMigrationPreservesFullRowsAndRefusesLossyRollback(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := networkPersistenceMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply network persistence migration: %v", err)
	}
	if err := networkReleaseDigestMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply network release digest migration: %v", err)
	}
	insertNetworkMigrationState(t, databaseConnection, 1)

	migration := networkStageMigration(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply network stage migration: %v", err)
	}
	var stage string
	if err := databaseConnection.Raw("SELECT stage FROM network_state WHERE id = 1").Scan(&stage).Error; err != nil {
		t.Fatalf("read upgraded network stage: %v", err)
	}
	if stage != "full" {
		t.Fatalf("upgraded network stage = %q, want full", stage)
	}
	assertMigrationStatementFails(t, databaseConnection, "UPDATE network_state SET stage = 'partial' WHERE id = 1")
	mustExecNetworkMigration(t, databaseConnection, "UPDATE network_state SET stage = 'identity' WHERE id = 1")

	if err := databaseConnection.Transaction(func(tx *gorm.DB) error {
		return migration.Down(tx)
	}); err == nil {
		t.Fatal("network stage rollback accepted an identity-stage row")
	}
	if !migrationTableHasColumn(t, databaseConnection, "network_state", "stage") {
		t.Fatal("failed identity-stage rollback removed the stage column")
	}
	stage = ""
	if err := databaseConnection.Raw("SELECT stage FROM network_state WHERE id = 1").Scan(&stage).Error; err != nil {
		t.Fatalf("read network stage after rejected rollback: %v", err)
	}
	if stage != "identity" {
		t.Fatalf("network stage after rejected rollback = %q, want identity", stage)
	}

	mustExecNetworkMigration(t, databaseConnection, "UPDATE network_state SET stage = 'full' WHERE id = 1")
	if err := databaseConnection.Transaction(func(tx *gorm.DB) error {
		return migration.Down(tx)
	}); err != nil {
		t.Fatalf("rollback full-only network stage migration: %v", err)
	}
	if migrationTableHasColumn(t, databaseConnection, "network_state", "stage") {
		t.Fatal("full-only rollback retained the stage column")
	}
	assertProjectionCount(t, databaseConnection, "network_state", 1)
}

// TestNetworkPersistenceMigrationStagesProjectRelease verifies restrictive ownership, verified quarantine, replacement allocation, and replay evidence survive project deletion.
func TestNetworkPersistenceMigrationStagesProjectRelease(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyNetworkPersistenceMigrations(t, databaseConnection)
	seedNetworkMigrationProject(t, databaseConnection, "project-orders", "operation-release", 1, 2)
	insertNetworkMigrationState(t, databaseConnection, 3)
	insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
	insertNetworkMigrationCandidate(t, databaseConnection, 2, "127.77.0.11", 1)
	insertNetworkMigrationSetupEvidence(t, databaseConnection)
	insertNetworkMigrationListeners(t, databaseConnection)
	leaseID := insertNetworkMigrationLease(t, databaseConnection, "project-orders", "127.77.0.10")

	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO public_endpoint_leases
		(network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at)
		VALUES (1, 'project-orders', 'orders-http', 'http', 'orders.test', '127.0.0.1', 443, NULL, 1, '2026-07-18T12:01:00Z', '2026-07-18T12:01:00Z')`)
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO public_endpoint_leases
		(network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at)
		VALUES (1, 'project-orders', 'orders-mysql', 'tcp', 'mysql.orders.test', '127.77.0.10', 3306, ?, 1, '2026-07-18T12:01:00Z', '2026-07-18T12:01:00Z')`, leaseID)
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_project_releases
		(network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at)
		VALUES (1, 'project-orders', 'project-orders', 'operation-release', 'releasing', 1, '2026-07-18T12:02:00Z')`)

	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM projects WHERE project_id = 'project-orders'")
	assertMigrationStatementFails(t, databaseConnection, `UPDATE loopback_address_leases SET
		project_id = NULL,
		state = 'quarantined',
		release_generation = 2,
		release_evidence = 'verified host release',
		released_at = '2026-07-18T12:03:00Z',
		quarantined_at = '2026-07-18T12:03:00Z',
		reuse_after = '2026-07-18T12:08:00Z',
		quarantine_reason = 'stale client safety window'
		WHERE id = ?`, leaseID)
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM network_pool_candidates WHERE address = '127.77.0.10'")

	mustExecNetworkMigration(t, databaseConnection, "DELETE FROM public_endpoint_leases WHERE project_id = 'project-orders'")
	mustExecNetworkMigration(t, databaseConnection, `UPDATE loopback_address_leases SET
		project_id = NULL,
		state = 'quarantined',
		release_generation = 2,
		release_evidence = 'verified host release',
		released_at = '2026-07-18T12:03:00Z',
		quarantined_at = '2026-07-18T12:03:00Z',
		reuse_after = '2026-07-18T12:08:00Z',
		quarantine_reason = 'stale client safety window'
		WHERE id = ?`, leaseID)

	replacementID := insertNetworkMigrationLease(t, databaseConnection, "project-orders", "127.77.0.11")
	mustExecNetworkMigration(t, databaseConnection, "DELETE FROM loopback_address_leases WHERE id = ?", replacementID)
	mustExecNetworkMigration(t, databaseConnection, `UPDATE network_project_releases SET
		project_id = NULL,
		state = 'completed',
		completion_generation = 2,
		completed_at = '2026-07-18T12:04:00Z',
		release_evidence = 'all endpoints removed and host release verified',
		release_set_digest = ?
		WHERE operation_id = 'operation-release'`, networkMigrationReleaseSetDigest)
	mustExecNetworkMigration(t, databaseConnection, "DELETE FROM projects WHERE project_id = 'project-orders'")

	var lease struct {
		ProjectID       *string
		SourceProjectID string
		ReleaseEvidence string
		ReuseAfter      *time.Time
	}
	if err := databaseConnection.Raw(`SELECT project_id, source_project_id, release_evidence, reuse_after
		FROM loopback_address_leases WHERE id = ?`, leaseID).Scan(&lease).Error; err != nil {
		t.Fatalf("read quarantined lease: %v", err)
	}
	if lease.ProjectID != nil || lease.SourceProjectID != "project-orders" || lease.ReleaseEvidence == "" || lease.ReuseAfter == nil {
		t.Fatalf("quarantined lease = %#v, want retained source and verified release evidence", lease)
	}

	var release struct {
		ProjectID        *string
		SourceProjectID  string
		State            string
		ReleaseEvidence  string
		ReleaseSetDigest string
	}
	if err := databaseConnection.Raw(`SELECT project_id, source_project_id, state, release_evidence, release_set_digest
		FROM network_project_releases WHERE operation_id = 'operation-release'`).Scan(&release).Error; err != nil {
		t.Fatalf("read completed project release: %v", err)
	}
	if release.ProjectID != nil || release.SourceProjectID != "project-orders" || release.State != "completed" || release.ReleaseEvidence == "" || release.ReleaseSetDigest != networkMigrationReleaseSetDigest {
		t.Fatalf("completed release = %#v, want replay evidence independent of the deleted project", release)
	}
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM operations WHERE id = 'operation-release'")
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM network_state WHERE id = 1")
	assertNetworkMigrationForeignKeys(t, databaseConnection)
}

// TestNetworkPersistenceMigrationRejectsInvalidFacts verifies direct writers cannot bypass the durable network vocabulary and ownership boundaries.
func TestNetworkPersistenceMigrationRejectsInvalidFacts(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyNetworkPersistenceMigrations(t, databaseConnection)
	seedNetworkMigrationProject(t, databaseConnection, "project-one", "operation-one", 1, 3)
	insertProjectionProject(t, databaseConnection, "project-two", "/work/project-two", "project-two", 2)
	insertNetworkMigrationState(t, databaseConnection, 4)
	insertNetworkMigrationCandidate(t, databaseConnection, 1, "127.77.0.10", 1)
	insertNetworkMigrationCandidate(t, databaseConnection, 2, "127.77.0.11", 1)
	leaseID := insertNetworkMigrationLease(t, databaseConnection, "project-one", "127.77.0.10")
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_setup_evidence
		(network_state_id, component, evidence, generation, verified_at)
		VALUES (1, 'machine_ownership', 'verified owner', 1, '2026-07-18T12:00:00Z')`)

	invalidStatements := []string{
		`INSERT INTO network_pool_candidates (network_state_id, ordinal, address, generation) VALUES (1, 0, '127.77.0.12', 1)`,
		`INSERT INTO network_pool_candidates (network_state_id, ordinal, address, generation) VALUES (2, 3, '127.77.0.12', 1)`,
		`INSERT INTO network_pool_candidates (network_state_id, ordinal, address, generation) VALUES (1, 1, '127.77.0.12', 1)`,
		`INSERT INTO network_pool_candidates (network_state_id, ordinal, address, generation) VALUES (1, 3, '127.77.0.10', 1)`,
		`INSERT INTO network_pool_candidates (network_state_id, ordinal, address, generation) VALUES (1, 3, '192.0.2.10', 1)`,
		`INSERT INTO network_setup_evidence (network_state_id, component, evidence, generation, verified_at) VALUES (1, 'certificate_trust', 'trusted', 1, '2026-07-18T12:00:00Z')`,
		`INSERT INTO network_setup_evidence (network_state_id, component, evidence, generation, verified_at) VALUES (1, 'resolver', '', 1, '2026-07-18T12:00:00Z')`,
		`INSERT INTO network_setup_evidence (network_state_id, component, evidence, generation, verified_at) VALUES (1, 'machine_ownership', 'duplicate owner', 2, '2026-07-18T12:01:00Z')`,
		`INSERT INTO network_shared_listeners (network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at) VALUES (1, 'dns', 'direct', '127.0.0.1', 53, '127.0.0.1', 10053, 1, '2026-07-18T12:00:00Z')`,
		`INSERT INTO network_shared_listeners (network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at) VALUES (1, 'dns', 'redirect', '127.0.0.1', 53, '127.0.0.2', 10053, 1, '2026-07-18T12:00:00Z')`,
		`INSERT INTO network_shared_listeners (network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at) VALUES (1, 'dns', 'redirect', '127.0.0.1', 53, '127.0.0.1', 53, 1, '2026-07-18T12:00:00Z')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at) VALUES (1, 'missing-project', 'missing-project', 'primary', '', '127.77.0.11', 'leased', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at) VALUES (1, 'project-one', 'project-two', 'primary', '', '127.77.0.11', 'leased', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at) VALUES (1, 'project-two', 'project-two', 'primary', 'extra', '127.77.0.11', 'leased', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at) VALUES (1, 'project-two', 'project-two', 'primary', '', '127.77.0.12', 'leased', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at) VALUES (1, 'project-two', 'project-two', 'primary', '', '127.77.0.11', 'leased', 1, '-unsafe', 1, 'ensured', '2026-07-18T12:00:00Z')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at) VALUES (1, 'project-one', 'project-one', 'primary', '', '127.77.0.11', 'leased', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at, release_generation, release_evidence, released_at, quarantined_at, reuse_after, quarantine_reason) VALUES (1, NULL, 'project-two', 'primary', '', '127.77.0.11', 'quarantined', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z', 1, 'released', '2026-07-18T12:01:00Z', '2026-07-18T12:01:00Z', '2026-07-18T12:06:00Z', 'delay')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at, release_generation, release_evidence, released_at, quarantined_at, reuse_after, quarantine_reason) VALUES (1, NULL, 'project-two', 'primary', '', '127.77.0.11', 'quarantined', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z', 2, 'released', '2026-07-18T12:01:00Z', '2026-07-18T12:01:00Z', NULL, 'delay')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at, release_generation, release_evidence, released_at, quarantined_at, reuse_after, quarantine_reason) VALUES (1, NULL, 'project-two', 'primary', '', '127.77.0.11', 'quarantined', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z', 2, 'released', '2026-07-18T12:01:00Z', '2026-07-18T12:01:00Z', '2026-07-18T12:00:00Z', 'delay')`,
		`INSERT INTO loopback_address_leases (network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at, release_generation, release_evidence, released_at, quarantined_at, reuse_after, quarantine_reason) VALUES (1, NULL, 'project-two', 'primary', '', '127.77.0.11', 'quarantined', 1, 'installation-a', 1, 'ensured', '2026-07-18T12:00:00Z', 2, 'released', '2026-07-18T12:01:00Z', '2026-07-18T12:01:00Z', '2026-07-18T12:06:00Z', '')`,
		`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'bad http', 'http', 'orders.test', '127.0.0.1', 443, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`,
		`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'bad-host', 'http', 'Orders.test', '127.0.0.1', 443, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`,
		`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'bad-zone', 'http', 'orders.example', '127.0.0.1', 443, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`,
		`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'bad-label', 'http', '-orders.test', '127.0.0.1', 443, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`,
		fmt.Sprintf(`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', '%s', 'http', 'long-id.test', '127.0.0.1', 443, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`, strings.Repeat("a", 129)),
		`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'tcp-without-lease', 'tcp', 'mysql.project-one.test', '127.77.0.10', 3306, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`,
		fmt.Sprintf(`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-two', 'cross-project', 'tcp', 'mysql.project-two.test', '127.77.0.10', 3306, %d, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`, leaseID),
		fmt.Sprintf(`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'wrong-address', 'tcp', 'wrong.project-one.test', '127.77.0.11', 3306, %d, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`, leaseID),
		fmt.Sprintf(`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'http-with-lease', 'http', 'http-lease.project-one.test', '127.77.0.10', 443, %d, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`, leaseID),
		`INSERT INTO public_endpoint_leases (network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at) VALUES (1, 'project-one', 'bad-protocol', 'udp', 'udp.project-one.test', '127.0.0.1', 53, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`,
		`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at) VALUES (1, 'missing-project', 'missing-project', 'operation-one', 'releasing', 1, '2026-07-18T12:00:00Z')`,
		fmt.Sprintf(`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence, release_set_digest) VALUES (1, NULL, 'project-one', 'operation-one', 'completed', 1, '2026-07-18T12:00:00Z', 1, '2026-07-18T12:01:00Z', 'released', '%s')`, networkMigrationReleaseSetDigest),
		fmt.Sprintf(`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence, release_set_digest) VALUES (1, NULL, 'project-one', 'operation-one', 'completed', 1, '2026-07-18T12:00:00Z', 2, '2026-07-18T12:01:00Z', NULL, '%s')`, networkMigrationReleaseSetDigest),
		`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence) VALUES (1, 'project-one', 'project-one', 'operation-one', 'releasing', 1, '2026-07-18T12:00:00Z', 2, '2026-07-18T12:01:00Z', 'released')`,
		fmt.Sprintf(`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, release_set_digest) VALUES (1, 'project-one', 'project-one', 'operation-one', 'releasing', 1, '2026-07-18T12:00:00Z', '%s')`, networkMigrationReleaseSetDigest),
		`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence) VALUES (1, NULL, 'project-one', 'operation-one', 'completed', 1, '2026-07-18T12:00:00Z', 2, '2026-07-18T12:01:00Z', 'released')`,
		`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence, release_set_digest) VALUES (1, NULL, 'project-one', 'operation-one', 'completed', 1, '2026-07-18T12:00:00Z', 2, '2026-07-18T12:01:00Z', 'released', 'abcd')`,
		`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence, release_set_digest) VALUES (1, NULL, 'project-one', 'operation-one', 'completed', 1, '2026-07-18T12:00:00Z', 2, '2026-07-18T12:01:00Z', 'released', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeF')`,
		`INSERT INTO network_project_releases (network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence, release_set_digest) VALUES (1, NULL, 'project-one', 'operation-one', 'completed', 1, '2026-07-18T12:00:00Z', 2, '2026-07-18T12:01:00Z', 'released', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg')`,
	}
	for _, statement := range invalidStatements {
		assertMigrationStatementFails(t, databaseConnection, statement)
	}
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_shared_listeners
		(network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at)
		VALUES (1, 'dns', 'direct', '127.0.0.1', 53, '127.0.0.1', 53, 1, '2026-07-18T12:00:00Z')`)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO network_shared_listeners
		(network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at)
		VALUES (1, 'dns', 'direct', '127.0.0.2', 1053, '127.0.0.2', 1053, 2, '2026-07-18T12:01:00Z')`)

	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO public_endpoint_leases
		(network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at)
		VALUES (1, 'project-one', 'project-one-http', 'http', 'project-one.test', '127.0.0.1', 443, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO public_endpoint_leases
		(network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at)
		VALUES (1, 'project-two', 'same-host', 'http', 'project-one.test', '127.0.0.2', 8443, NULL, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`)
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO public_endpoint_leases
		(network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at)
		VALUES (1, 'project-one', 'project-one-mysql', 'tcp', 'mysql.project-one.test', '127.77.0.10', 3306, ?, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`, leaseID)
	assertMigrationStatementFails(t, databaseConnection, `INSERT INTO public_endpoint_leases
		(network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at)
		VALUES (1, 'project-one', 'same-socket', 'tcp', 'redis.project-one.test', '127.77.0.10', 3306, ?, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`, leaseID)
	secondLeaseID := insertNetworkMigrationLease(t, databaseConnection, "project-two", "127.77.0.11")
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO public_endpoint_leases
		(network_state_id, project_id, endpoint_id, protocol, hostname, address, port, loopback_address_lease_id, generation, created_at, updated_at)
		VALUES (1, 'project-two', 'project-two-mysql', 'tcp', 'mysql.project-two.test', '127.77.0.11', 3306, ?, 1, '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z')`, secondLeaseID)
	assertNetworkMigrationForeignKeys(t, databaseConnection)
}

// TestNetworkReleaseSetDigestMigrationUpgradesAndRollsBack proves releasing rows permit reversible upgrades while completed replay proof cannot be destroyed.
func TestNetworkReleaseSetDigestMigrationUpgradesAndRollsBack(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := networkPersistenceMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply original network persistence migration: %v", err)
	}
	seedNetworkMigrationProject(t, databaseConnection, "project-upgrade", "operation-upgrade", 1, 2)
	insertNetworkMigrationState(t, databaseConnection, 3)
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_project_releases
		(network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at)
		VALUES (1, 'project-upgrade', 'project-upgrade', 'operation-upgrade', 'releasing', 1, '2026-07-18T12:02:00Z')`)

	if err := ensureMigrationsTable(databaseConnection); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	migration := networkReleaseDigestMigration(t)
	record := migrationRecord{
		Name:       migration.Name(),
		App:        migration.App(),
		Connection: migration.Connection(),
		SourcePath: migration.SourcePath(),
		AppliedAt:  time.Date(2026, time.July, 18, 12, 3, 0, 0, time.UTC),
	}
	if err := applySQLiteMigration(databaseConnection, migration, record); err != nil {
		t.Fatalf("apply network release digest migration: %v", err)
	}
	if !migrationTableHasColumn(t, databaseConnection, "network_project_releases", "release_set_digest") {
		t.Fatal("upgraded network_project_releases is missing release_set_digest")
	}
	var releasingDigest *string
	if err := databaseConnection.Raw(`SELECT release_set_digest FROM network_project_releases WHERE operation_id = 'operation-upgrade'`).Scan(&releasingDigest).Error; err != nil {
		t.Fatalf("read upgraded releasing digest: %v", err)
	}
	if releasingDigest != nil {
		t.Fatalf("upgraded releasing digest = %q, want NULL", *releasingDigest)
	}

	if err := rollbackSQLiteMigration(databaseConnection, migration); err != nil {
		t.Fatalf("rollback network release digest migration: %v", err)
	}
	if migrationTableHasColumn(t, databaseConnection, "network_project_releases", "release_set_digest") {
		t.Fatal("rolled-back network_project_releases retained release_set_digest")
	}
	var release struct {
		State            string
		ReleaseEvidence  string
		ReleaseSetDigest string
	}
	if err := databaseConnection.Raw(`SELECT state, release_evidence FROM network_project_releases WHERE operation_id = 'operation-upgrade'`).Scan(&release).Error; err != nil {
		t.Fatalf("read rolled-back release: %v", err)
	}
	if release.State != "releasing" || release.ReleaseEvidence != "" {
		t.Fatalf("rolled-back release = %#v, want preserved releasing marker", release)
	}
	if countAtomicityMigrationRecords(t, databaseConnection, migration.Name()) != 0 {
		t.Fatal("rolled-back release digest migration retained its ledger record")
	}

	if err := applySQLiteMigration(databaseConnection, migration, record); err != nil {
		t.Fatalf("reapply network release digest migration: %v", err)
	}
	mustExecNetworkMigration(t, databaseConnection, `UPDATE network_project_releases SET
		project_id = NULL,
		state = 'completed',
		completion_generation = 2,
		completed_at = '2026-07-18T12:04:00Z',
		release_evidence = 'verified release',
		release_set_digest = ?
		WHERE operation_id = 'operation-upgrade'`, networkMigrationReleaseSetDigest)
	if err := rollbackSQLiteMigration(databaseConnection, migration); err == nil {
		t.Fatal("rollback discarded a completed release digest")
	}
	if !migrationTableHasColumn(t, databaseConnection, "network_project_releases", "release_set_digest") {
		t.Fatal("failed rollback removed release_set_digest")
	}
	if err := databaseConnection.Raw(`SELECT state, release_evidence, release_set_digest FROM network_project_releases WHERE operation_id = 'operation-upgrade'`).Scan(&release).Error; err != nil {
		t.Fatalf("read release after rejected rollback: %v", err)
	}
	if release.State != "completed" || release.ReleaseEvidence != "verified release" || release.ReleaseSetDigest != networkMigrationReleaseSetDigest {
		t.Fatalf("release after rejected rollback = %#v, want intact completion proof", release)
	}
	if countAtomicityMigrationRecords(t, databaseConnection, migration.Name()) != 1 {
		t.Fatal("failed rollback removed the release digest migration ledger record")
	}
}

// TestNetworkReleaseSetDigestMigrationRejectsUnverifiableCompletion proves an old completion cannot be silently upgraded without its exact release set.
func TestNetworkReleaseSetDigestMigrationRejectsUnverifiableCompletion(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := networkPersistenceMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply original network persistence migration: %v", err)
	}
	seedNetworkMigrationProject(t, databaseConnection, "project-completed", "operation-completed", 1, 2)
	insertNetworkMigrationState(t, databaseConnection, 3)
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_project_releases
		(network_state_id, project_id, source_project_id, operation_id, state, begin_generation, began_at, completion_generation, completed_at, release_evidence)
		VALUES (1, NULL, 'project-completed', 'operation-completed', 'completed', 1, '2026-07-18T12:02:00Z', 2, '2026-07-18T12:04:00Z', 'legacy release')`)
	if err := ensureMigrationsTable(databaseConnection); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	migration := networkReleaseDigestMigration(t)
	err := applySQLiteMigration(databaseConnection, migration, migrationRecord{
		Name:       migration.Name(),
		App:        migration.App(),
		Connection: migration.Connection(),
		SourcePath: migration.SourcePath(),
		AppliedAt:  time.Date(2026, time.July, 18, 12, 5, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("release digest migration accepted an unverifiable completed release")
	}
	if migrationTableHasColumn(t, databaseConnection, "network_project_releases", "release_set_digest") {
		t.Fatal("failed release digest migration retained its column")
	}
	if countAtomicityMigrationRecords(t, databaseConnection, migration.Name()) != 0 {
		t.Fatal("failed release digest migration retained a ledger record")
	}
}

// TestNetworkPersistenceMigrationRollbackTracksLedger verifies the production reverse migration removes only its schema and ledger identity.
func TestNetworkPersistenceMigrationRollbackTracksLedger(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := ensureMigrationsTable(databaseConnection); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	migration := networkPersistenceMigration(t)
	record := migrationRecord{
		Name:       migration.Name(),
		App:        migration.App(),
		Connection: migration.Connection(),
		SourcePath: migration.SourcePath(),
		AppliedAt:  time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	}
	if err := applySQLiteMigration(databaseConnection, migration, record); err != nil {
		t.Fatalf("apply network persistence migration: %v", err)
	}
	insertNetworkMigrationState(t, databaseConnection, 1)
	if countAtomicityMigrationRecords(t, databaseConnection, migration.Name()) != 1 {
		t.Fatal("network persistence migration ledger row was not committed")
	}

	if err := rollbackSQLiteMigration(databaseConnection, migration); err != nil {
		t.Fatalf("rollback network persistence migration: %v", err)
	}
	for _, table := range []string{
		"network_state",
		"network_pool_candidates",
		"network_setup_evidence",
		"network_shared_listeners",
		"loopback_address_leases",
		"public_endpoint_leases",
		"network_project_releases",
	} {
		if databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("rollback retained %s", table)
		}
	}
	if !databaseConnection.Migrator().HasTable("projects") || !databaseConnection.Migrator().HasTable("operations") {
		t.Fatal("rollback removed an earlier projection or operation table")
	}
	if countAtomicityMigrationRecords(t, databaseConnection, migration.Name()) != 0 {
		t.Fatal("rollback retained the network persistence migration ledger row")
	}
}

// TestNetworkPersistenceMigrationApplyIsAtomic verifies a late production DDL failure leaves neither earlier network tables nor a ledger row.
func TestNetworkPersistenceMigrationApplyIsAtomic(t *testing.T) {
	connections, databaseConnection := openOperationMigrationDatabase(t)
	defer closeOperationMigrationDatabase(t, connections)
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := ensureMigrationsTable(databaseConnection); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	if err := databaseConnection.Exec("CREATE TABLE network_shared_listeners (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create conflicting late migration table: %v", err)
	}
	migration := networkPersistenceMigration(t)
	record := migrationRecord{
		Name:       migration.Name(),
		App:        migration.App(),
		Connection: migration.Connection(),
		SourcePath: migration.SourcePath(),
		AppliedAt:  time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	}
	if err := applySQLiteMigration(databaseConnection, migration, record); err == nil {
		t.Fatal("network persistence migration accepted a late DDL conflict")
	}
	for _, table := range []string{"network_state", "network_pool_candidates", "network_setup_evidence"} {
		if databaseConnection.Migrator().HasTable(table) {
			t.Fatalf("failed migration retained partial table %s", table)
		}
	}
	if !databaseConnection.Migrator().HasTable("network_shared_listeners") {
		t.Fatal("failed migration removed the preexisting conflicting table")
	}
	if countAtomicityMigrationRecords(t, databaseConnection, migration.Name()) != 0 {
		t.Fatal("failed migration retained a ledger row")
	}
}

// applyNetworkPersistenceMigrations applies Harbor's production state migrations without creating the framework ledger.
func applyNetworkPersistenceMigrations(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	applyProjectProjectionMigrations(t, databaseConnection)
	if err := networkPersistenceMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply network persistence migration: %v", err)
	}
	if err := networkReleaseDigestMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply network release digest migration: %v", err)
	}
	if err := networkStageMigration(t).Up(databaseConnection); err != nil {
		t.Fatalf("apply network stage migration: %v", err)
	}
}

// networkPersistenceMigration finds the production network migration through Harbor's embedded registry.
func networkPersistenceMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkPersistenceMigrationName {
			return migration
		}
	}
	t.Fatalf("network persistence migration %q is not registered", networkPersistenceMigrationName)
	return nil
}

// networkReleaseDigestMigration finds the release digest upgrade through Harbor's embedded registry.
func networkReleaseDigestMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkReleaseDigestMigrationName {
			return migration
		}
	}
	t.Fatalf("network release digest migration %q is not registered", networkReleaseDigestMigrationName)
	return nil
}

// networkStageMigration finds the identity-stage upgrade through Harbor's embedded registry.
func networkStageMigration(t *testing.T) Migration {
	t.Helper()
	for _, migration := range selectMigrations("harbord", "default", "sqlite") {
		if migration.Name() == networkStageMigrationName {
			return migration
		}
	}
	t.Fatalf("network stage migration %q is not registered", networkStageMigrationName)
	return nil
}

// insertNetworkMigrationState creates the singleton owner for all durable network facts.
func insertNetworkMigrationState(t *testing.T, databaseConnection *gorm.DB, revision int) {
	t.Helper()
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_state
		(id, installation_id, ownership_generation, pool_network, pool_prefix_length, dns_suffix, created_at, updated_at, revision)
		VALUES (1, 'installation-a', 1, '127.77.0.0', 24, '.test', '2026-07-18T12:00:00Z', '2026-07-18T12:00:00Z', ?)`, revision)
}

// insertNetworkMigrationCandidate persists one canonical pool member at its deterministic ordinal.
func insertNetworkMigrationCandidate(t *testing.T, databaseConnection *gorm.DB, ordinal int, address string, generation int) {
	t.Helper()
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_pool_candidates
		(network_state_id, ordinal, address, generation) VALUES (1, ?, ?, ?)`, ordinal, address, generation)
}

// seedNetworkMigrationProject creates the project and running unregister operation needed by release ownership tests.
func seedNetworkMigrationProject(t *testing.T, databaseConnection *gorm.DB, projectID string, operationID string, projectRevision int, operationRevision int) {
	t.Helper()
	insertProjectionProject(t, databaseConnection, projectID, "/work/"+projectID, projectID, projectRevision)
	requestedAt := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Minute)
	if err := insertMigrationOperation(
		databaseConnection,
		operationID,
		"intent-"+operationID,
		"running",
		requestedAt,
		&startedAt,
		nil,
		nil,
		operationRevision,
	); err != nil {
		t.Fatalf("insert running release operation: %v", err)
	}
	if err := databaseConnection.Exec("UPDATE operations SET project_id = ?, kind = 'project.unregister' WHERE id = ?", projectID, operationID).Error; err != nil {
		t.Fatalf("bind release operation to project: %v", err)
	}
}

// insertNetworkMigrationSetupEvidence stores only the four elevated setup components owned by network persistence.
func insertNetworkMigrationSetupEvidence(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	for _, component := range []string{"machine_ownership", "loopback_pool", "resolver", "low_ports"} {
		mustExecNetworkMigration(t, databaseConnection, `INSERT INTO network_setup_evidence
			(network_state_id, component, evidence, generation, verified_at)
			VALUES (1, ?, ?, 1, '2026-07-18T12:00:00Z')`, component, "verified "+component)
	}
}

// insertNetworkMigrationListeners stores direct and redirected shared listener facts without runtime upstreams.
func insertNetworkMigrationListeners(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO network_shared_listeners (network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at) VALUES (1, 'dns', 'redirect', '127.0.0.1', 53, '127.0.0.1', 10053, 1, '2026-07-18T12:00:00Z')`,
		`INSERT INTO network_shared_listeners (network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at) VALUES (1, 'http', 'redirect', '127.0.0.1', 80, '127.0.0.1', 10080, 1, '2026-07-18T12:00:00Z')`,
		`INSERT INTO network_shared_listeners (network_state_id, kind, mode, advertised_address, advertised_port, bind_address, bind_port, generation, verified_at) VALUES (1, 'https', 'direct', '127.0.0.1', 443, '127.0.0.1', 443, 1, '2026-07-18T12:00:00Z')`,
	}
	for _, statement := range statements {
		mustExecNetworkMigration(t, databaseConnection, statement)
	}
}

// insertNetworkMigrationLease creates one active primary lease and returns its durable row identity.
func insertNetworkMigrationLease(t *testing.T, databaseConnection *gorm.DB, projectID string, address string) int {
	t.Helper()
	mustExecNetworkMigration(t, databaseConnection, `INSERT INTO loopback_address_leases
		(network_state_id, project_id, source_project_id, kind, secondary_id, address, state, lease_generation, ownership_installation_id, ownership_generation, ensure_evidence, leased_at)
		VALUES (1, ?, ?, 'primary', '', ?, 'leased', 1, 'installation-a', 1, 'verified ensure', '2026-07-18T12:00:00Z')`, projectID, projectID, address)
	var id int
	if err := databaseConnection.Raw("SELECT id FROM loopback_address_leases WHERE project_id = ? AND address = ?", projectID, address).Scan(&id).Error; err != nil {
		t.Fatalf("read active lease identity: %v", err)
	}
	if id == 0 {
		t.Fatal("active lease identity is zero")
	}
	return id
}

// migrationTableHasColumn reports whether one allowlisted migration table contains an exact column name.
func migrationTableHasColumn(t *testing.T, databaseConnection *gorm.DB, table string, column string) bool {
	t.Helper()
	allowed := map[string]struct{}{
		"network_state":            {},
		"network_pool_candidates":  {},
		"network_setup_evidence":   {},
		"network_shared_listeners": {},
		"loopback_address_leases":  {},
		"public_endpoint_leases":   {},
		"network_project_releases": {},
	}
	if _, found := allowed[table]; !found {
		t.Fatalf("table %q is not allowed for migration introspection", table)
	}
	var columns []struct {
		Name string
	}
	if err := databaseConnection.Raw("PRAGMA table_info(" + table + ")").Scan(&columns).Error; err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	for _, candidate := range columns {
		if candidate.Name == column {
			return true
		}
	}
	return false
}

// assertNetworkMigrationForeignKeys proves valid lifecycle fixtures leave no deferred ownership mismatch for a later connection to discover.
func assertNetworkMigrationForeignKeys(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var violations []struct {
		Table  string
		RowID  int
		Parent string
		FKID   int
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		t.Fatalf("check network migration foreign keys: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("network migration foreign key violations = %#v", violations)
	}
}

// mustExecNetworkMigration executes one setup statement and stops at the first unexpected schema rejection.
func mustExecNetworkMigration(t *testing.T, databaseConnection *gorm.DB, statement string, arguments ...any) {
	t.Helper()
	if err := databaseConnection.Exec(statement, arguments...).Error; err != nil {
		t.Fatalf("execute network migration statement: %v\n%s", err, statement)
	}
}
