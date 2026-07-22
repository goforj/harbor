package migrations

import (
	"sort"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

const networkGlobalReleasePlansMigrationName = "2026_07_22_040000_create_network_global_release_plans"

// TestNetworkGlobalReleasePlansMigrationUpgradesStream verifies the production stream reaches the durable release authority boundary.
func TestNetworkGlobalReleasePlansMigrationUpgradesStream(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleasePlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply global release plan migration: %v", err)
	}
	if !databaseConnection.Migrator().HasTable("network_global_release_plans") {
		t.Fatal("migration stream did not create network_global_release_plans")
	}
	for _, column := range []string{
		"id",
		"operation_id",
		"operation_revision",
		"network_state_id",
		"network_revision",
		"network_updated_at",
		"phase",
		"authority_payload",
		"authority_digest",
	} {
		if !databaseConnection.Migrator().HasColumn("network_global_release_plans", column) {
			t.Fatalf("migration stream did not create network_global_release_plans.%s", column)
		}
	}
	assertNetworkGlobalReleaseMigrationForeignKeys(t, databaseConnection)
	assertNetworkGlobalReleaseMigrationUniqueColumns(t, databaseConnection)
}

// TestNetworkGlobalReleasePlansMigrationEnforcesAuthority verifies the singleton and every durable authority constraint.
func TestNetworkGlobalReleasePlansMigrationEnforcesAuthority(t *testing.T) {
	for _, phase := range []string{
		"runtime_release",
		"low_ports",
		"resolver",
		"trust",
		"loopbacks",
		"verify_effects",
		"ownership",
		"projection",
	} {
		t.Run("phase "+phase, func(t *testing.T) {
			databaseConnection := newNetworkGlobalReleasePlansMigrationHarness(t)
			seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
			plan := defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5)
			plan.Phase = phase
			insertNetworkGlobalReleaseMigrationPlan(t, databaseConnection, plan)
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*networkGlobalReleaseMigrationPlan)
	}{
		{
			name: "non-singleton ID",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.ID = 2
			},
		},
		{
			name: "zero operation revision",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.OperationRevision = 0
			},
		},
		{
			name: "unsafe operation revision",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.OperationRevision = 9007199254740992
			},
		},
		{
			name: "non-singleton network state",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.NetworkStateID = 2
			},
		},
		{
			name: "zero network revision",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.NetworkRevision = 0
			},
		},
		{
			name: "unsafe network revision",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.NetworkRevision = 9007199254740992
			},
		},
		{
			name: "unknown phase",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.Phase = "complete"
			},
		},
		{
			name: "short authority payload",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.AuthorityPayload = "{"
			},
		},
		{
			name: "large authority payload",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.AuthorityPayload = strings.Repeat("a", 65537)
			},
		},
		{
			name: "short authority digest",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.AuthorityDigest = strings.Repeat("a", 63)
			},
		},
		{
			name: "uppercase authority digest",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.AuthorityDigest = strings.Repeat("A", 64)
			},
		},
		{
			name: "nonhex authority digest",
			mutate: func(plan *networkGlobalReleaseMigrationPlan) {
				plan.AuthorityDigest = strings.Repeat("g", 64)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection := newNetworkGlobalReleasePlansMigrationHarness(t)
			seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
			plan := defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5)
			test.mutate(&plan)
			if err := executeNetworkGlobalReleaseMigrationPlan(databaseConnection, plan); err == nil {
				t.Fatalf("invalid global release plan unexpectedly succeeded: %#v", plan)
			}
		})
	}

	for _, test := range []struct {
		name string
		plan func() networkGlobalReleaseMigrationPlan
	}{
		{
			name: "missing operation",
			plan: func() networkGlobalReleaseMigrationPlan {
				return defaultNetworkGlobalReleaseMigrationPlan("operation-missing", 3, 5)
			},
		},
		{
			name: "wrong operation revision",
			plan: func() networkGlobalReleaseMigrationPlan {
				return defaultNetworkGlobalReleaseMigrationPlan("operation-release", 2, 5)
			},
		},
		{
			name: "wrong network revision",
			plan: func() networkGlobalReleaseMigrationPlan {
				return defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 4)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			databaseConnection := newNetworkGlobalReleasePlansMigrationHarness(t)
			seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
			if err := executeNetworkGlobalReleaseMigrationPlan(databaseConnection, test.plan()); err == nil {
				t.Fatal("global release plan with an invalid owner unexpectedly succeeded")
			}
		})
	}
}

// TestNetworkGlobalReleasePlansMigrationRestrictsOwners verifies a retained authority prevents either referenced owner from drifting.
func TestNetworkGlobalReleasePlansMigrationRestrictsOwners(t *testing.T) {
	databaseConnection := newNetworkGlobalReleasePlansMigrationHarness(t)
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	insertNetworkGlobalReleaseMigrationPlan(t, databaseConnection, defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5))

	assertMigrationStatementFails(t, databaseConnection, "UPDATE operations SET revision = 4 WHERE id = 'operation-release'")
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM operations WHERE id = 'operation-release'")
	assertMigrationStatementFails(t, databaseConnection, "UPDATE network_state SET revision = 6 WHERE id = 1")
	assertMigrationStatementFails(t, databaseConnection, "DELETE FROM network_state WHERE id = 1")
}

// TestNetworkGlobalReleasePlansMigrationReleasesOwners verifies retiring the plan clears its restrictive owner boundary.
func TestNetworkGlobalReleasePlansMigrationReleasesOwners(t *testing.T) {
	databaseConnection := newNetworkGlobalReleasePlansMigrationHarness(t)
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	insertNetworkGlobalReleaseMigrationPlan(t, databaseConnection, defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5))

	mustExecNetworkSetupMigration(t, databaseConnection, "DELETE FROM network_global_release_plans WHERE id = 1")
	startedAt := networkGlobalReleaseMigrationTime().Add(time.Second)
	finishedAt := startedAt.Add(time.Second)
	mustExecNetworkSetupMigration(t, databaseConnection, `UPDATE operations
		SET state = 'succeeded', phase = 'complete', started_at = ?, finished_at = ?, revision = 4
		WHERE id = 'operation-release'`, startedAt, finishedAt)
	mustExecNetworkSetupMigration(t, databaseConnection,
		"UPDATE network_state SET revision = 6, updated_at = ? WHERE id = 1", finishedAt)
	mustExecNetworkSetupMigration(t, databaseConnection, "DELETE FROM operations WHERE id = 'operation-release'")
	mustExecNetworkSetupMigration(t, databaseConnection, "DELETE FROM network_state WHERE id = 1")
}

// TestNetworkGlobalReleasePlansMigrationRollsBackOnlyPlan verifies rollback leaves its prerequisite owners intact.
func TestNetworkGlobalReleasePlansMigrationRollsBackOnlyPlan(t *testing.T) {
	databaseConnection, migration := newNetworkGlobalReleasePlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply global release plan migration: %v", err)
	}
	seedNetworkGlobalReleaseMigrationOwners(t, databaseConnection, "operation-release", 3, 5)
	insertNetworkGlobalReleaseMigrationPlan(t, databaseConnection, defaultNetworkGlobalReleaseMigrationPlan("operation-release", 3, 5))

	if err := migration.Down(databaseConnection); err != nil {
		t.Fatalf("rollback global release plan migration: %v", err)
	}
	if databaseConnection.Migrator().HasTable("network_global_release_plans") {
		t.Fatal("rollback retained network_global_release_plans")
	}
	assertProjectionCount(t, databaseConnection, "operations", 1)
	assertProjectionCount(t, databaseConnection, "network_state", 1)
}

// newNetworkGlobalReleasePlansMigrationHarness applies the complete prerequisite stream and the release-plan boundary.
func newNetworkGlobalReleasePlansMigrationHarness(t *testing.T) *gorm.DB {
	t.Helper()
	databaseConnection, migration := newNetworkGlobalReleasePlansPrerequisiteHarness(t)
	if err := migration.Up(databaseConnection); err != nil {
		t.Fatalf("apply global release plan migration: %v", err)
	}
	return databaseConnection
}

// newNetworkGlobalReleasePlansPrerequisiteHarness applies production migrations through the release-plan predecessor.
func newNetworkGlobalReleasePlansPrerequisiteHarness(t *testing.T) (*gorm.DB, Migration) {
	t.Helper()
	connections, databaseConnection := openOperationMigrationDatabase(t)
	t.Cleanup(func() { closeOperationMigrationDatabase(t, connections) })
	migrations := selectMigrations("harbord", "default", "sqlite")
	sort.Slice(migrations, func(left, right int) bool { return migrations[left].Name() < migrations[right].Name() })
	for _, migration := range migrations {
		if migration.Name() == networkGlobalReleasePlansMigrationName {
			return databaseConnection, migration
		}
		if err := migration.Up(databaseConnection); err != nil {
			t.Fatalf("apply global release plan prerequisite %s: %v", migration.Name(), err)
		}
	}
	t.Fatalf("global release plan migration %q is not registered", networkGlobalReleasePlansMigrationName)
	return nil, nil
}

// networkGlobalReleaseMigrationPlan mirrors the complete SQL insert surface for schema probes.
type networkGlobalReleaseMigrationPlan struct {
	ID                int
	OperationID       string
	OperationRevision int64
	NetworkStateID    int
	NetworkRevision   int64
	NetworkUpdatedAt  time.Time
	Phase             string
	AuthorityPayload  string
	AuthorityDigest   string
}

// defaultNetworkGlobalReleaseMigrationPlan supplies one valid runtime-release authority snapshot.
func defaultNetworkGlobalReleaseMigrationPlan(operationID string, operationRevision, networkRevision int64) networkGlobalReleaseMigrationPlan {
	return networkGlobalReleaseMigrationPlan{
		ID:                1,
		OperationID:       operationID,
		OperationRevision: operationRevision,
		NetworkStateID:    1,
		NetworkRevision:   networkRevision,
		NetworkUpdatedAt:  networkGlobalReleaseMigrationTime(),
		Phase:             "runtime_release",
		AuthorityPayload:  "{}",
		AuthorityDigest:   strings.Repeat("a", 64),
	}
}

// seedNetworkGlobalReleaseMigrationOwners inserts the exact operation and network revisions referenced by a release plan.
func seedNetworkGlobalReleaseMigrationOwners(t *testing.T, databaseConnection *gorm.DB, operationID string, operationRevision, networkRevision int64) {
	t.Helper()
	requestedAt := networkGlobalReleaseMigrationTime()
	if err := databaseConnection.Exec(`INSERT INTO operations
		(id, intent_id, kind, project_id, state, phase, requested_at, revision)
		VALUES (?, ?, 'network.release', NULL, 'queued', 'queued', ?, ?)`,
		operationID,
		"intent-"+operationID,
		requestedAt,
		operationRevision,
	).Error; err != nil {
		t.Fatalf("insert global release operation %q: %v", operationID, err)
	}
	if err := databaseConnection.Exec(`INSERT INTO network_state
		(id, stage, installation_id, ownership_generation, pool_network, pool_prefix_length,
		 dns_suffix, created_at, updated_at, revision)
		VALUES (1, 'full', 'installation-a', 1, '127.77.0.0', 29, '.test', ?, ?, ?)`,
		requestedAt,
		requestedAt,
		networkRevision,
	).Error; err != nil {
		t.Fatalf("insert global release network state: %v", err)
	}
}

// insertNetworkGlobalReleaseMigrationPlan inserts a plan expected to satisfy every schema constraint.
func insertNetworkGlobalReleaseMigrationPlan(t *testing.T, databaseConnection *gorm.DB, plan networkGlobalReleaseMigrationPlan) {
	t.Helper()
	if err := executeNetworkGlobalReleaseMigrationPlan(databaseConnection, plan); err != nil {
		t.Fatalf("insert global release plan: %v", err)
	}
}

// executeNetworkGlobalReleaseMigrationPlan keeps valid and invalid records on the same SQL insert path.
func executeNetworkGlobalReleaseMigrationPlan(databaseConnection *gorm.DB, plan networkGlobalReleaseMigrationPlan) error {
	return databaseConnection.Exec(`INSERT INTO network_global_release_plans
		(id, operation_id, operation_revision, network_state_id, network_revision,
		 network_updated_at, phase, authority_payload, authority_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID,
		plan.OperationID,
		plan.OperationRevision,
		plan.NetworkStateID,
		plan.NetworkRevision,
		plan.NetworkUpdatedAt,
		plan.Phase,
		plan.AuthorityPayload,
		plan.AuthorityDigest,
	).Error
}

// networkGlobalReleaseMigrationTime returns the stable release authority timestamp used by migration fixtures.
func networkGlobalReleaseMigrationTime() time.Time {
	return time.Date(2026, time.July, 22, 4, 0, 0, 0, time.UTC)
}

// assertNetworkGlobalReleaseMigrationForeignKeys verifies both owner identities are revision-pinned and restrictive.
func assertNetworkGlobalReleaseMigrationForeignKeys(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var rows []struct {
		Table    string
		From     string
		To       string
		OnUpdate string `gorm:"column:on_update"`
		OnDelete string `gorm:"column:on_delete"`
	}
	if err := databaseConnection.Raw("PRAGMA foreign_key_list('network_global_release_plans')").Scan(&rows).Error; err != nil {
		t.Fatalf("read global release plan foreign keys: %v", err)
	}
	want := map[string]string{
		"operation_id":       "operations.id",
		"operation_revision": "operations.revision",
		"network_state_id":   "network_state.id",
		"network_revision":   "network_state.revision",
	}
	if len(rows) != len(want) {
		t.Fatalf("global release plan foreign key rows = %#v, want %d exact mappings", rows, len(want))
	}
	for _, row := range rows {
		key := row.Table + "." + row.To
		if want[row.From] != key || row.OnUpdate != "RESTRICT" || row.OnDelete != "RESTRICT" {
			t.Fatalf("global release plan foreign key row = %#v, want restrictive composite owner mapping", row)
		}
		delete(want, row.From)
	}
	if len(want) != 0 {
		t.Fatalf("global release plan foreign keys missing mappings %v", want)
	}
}

// assertNetworkGlobalReleaseMigrationUniqueColumns verifies both durable owners remain one-to-one with the singleton plan.
func assertNetworkGlobalReleaseMigrationUniqueColumns(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	var indexes []struct {
		Name   string
		Unique int
	}
	if err := databaseConnection.Raw("PRAGMA index_list('network_global_release_plans')").Scan(&indexes).Error; err != nil {
		t.Fatalf("read global release plan indexes: %v", err)
	}
	want := map[string]bool{"operation_id": false, "network_state_id": false}
	for _, index := range indexes {
		if index.Unique == 0 {
			continue
		}
		var columns []struct{ Name string }
		if err := databaseConnection.Raw("SELECT name FROM pragma_index_info(?)", index.Name).Scan(&columns).Error; err != nil {
			t.Fatalf("read global release plan index %s: %v", index.Name, err)
		}
		if len(columns) == 1 {
			if _, ok := want[columns[0].Name]; ok {
				want[columns[0].Name] = true
			}
		}
	}
	for column, found := range want {
		if !found {
			t.Fatalf("global release plan is missing unique %s owner", column)
		}
	}
}
