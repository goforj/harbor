package state

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// networkStoreReadTestSchema mirrors the model columns while leaving fail-closed validation to the read boundary.
var networkStoreReadTestSchema = []string{
	`CREATE TABLE network_state (
		id INTEGER PRIMARY KEY,
		stage TEXT,
		installation_id TEXT,
		ownership_generation INTEGER,
		pool_network TEXT,
		pool_prefix_length INTEGER,
		dns_suffix TEXT,
		created_at DATETIME,
		updated_at DATETIME,
		revision INTEGER
	)`,
	`CREATE TABLE network_pool_candidates (
		id INTEGER PRIMARY KEY,
		network_state_id INTEGER,
		ordinal INTEGER,
		address TEXT,
		generation INTEGER
	)`,
	`CREATE TABLE network_setup_evidence (
		id INTEGER PRIMARY KEY,
		network_state_id INTEGER,
		component TEXT,
		evidence TEXT,
		generation INTEGER,
		verified_at DATETIME
	)`,
	`CREATE TABLE network_shared_listeners (
		id INTEGER PRIMARY KEY,
		network_state_id INTEGER,
		kind TEXT,
		mode TEXT,
		advertised_address TEXT,
		advertised_port INTEGER,
		bind_address TEXT,
		bind_port INTEGER,
		generation INTEGER,
		verified_at DATETIME
	)`,
	`CREATE TABLE loopback_address_leases (
		id INTEGER PRIMARY KEY,
		network_state_id INTEGER,
		project_id TEXT,
		source_project_id TEXT,
		kind TEXT,
		secondary_id TEXT,
		address TEXT,
		state TEXT,
		lease_generation INTEGER,
		ownership_installation_id TEXT,
		ownership_generation INTEGER,
		ensure_evidence TEXT,
		leased_at DATETIME,
		release_generation INTEGER,
		release_evidence TEXT,
		released_at DATETIME,
		quarantined_at DATETIME,
		reuse_after DATETIME,
		quarantine_reason TEXT
	)`,
	`CREATE TABLE public_endpoint_leases (
		id INTEGER PRIMARY KEY,
		network_state_id INTEGER,
		project_id TEXT,
		endpoint_id TEXT,
		protocol TEXT,
		hostname TEXT,
		address TEXT,
		port INTEGER,
		loopback_address_lease_id INTEGER,
		generation INTEGER,
		created_at DATETIME,
		updated_at DATETIME
	)`,
	`CREATE TABLE network_project_releases (
		id INTEGER PRIMARY KEY,
		network_state_id INTEGER,
		project_id TEXT,
		source_project_id TEXT,
		operation_id TEXT,
		state TEXT,
		begin_generation INTEGER,
		began_at DATETIME,
		completion_generation INTEGER,
		completed_at DATETIME,
		release_evidence TEXT,
		release_set_digest TEXT
	)`,
}

// TestStoreNetworkDistinguishesSchemaLifecycle verifies legacy absence, migrated emptiness, and partial migrations remain distinct.
func TestStoreNetworkDistinguishesSchemaLifecycle(t *testing.T) {
	t.Run("legacy schema", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)

		record, initialized, err := store.Network(nil)
		if err != nil {
			t.Fatalf("read legacy network state: %v", err)
		}
		if initialized || !reflect.DeepEqual(record, NetworkRecord{}) {
			t.Fatalf("legacy network state = (%#v, %t), want zero and absent", record, initialized)
		}
	})

	t.Run("migrated but uninitialized", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		createNetworkStoreReadSchema(t, connection)

		record, initialized, err := store.Network(context.Background())
		if err != nil {
			t.Fatalf("read uninitialized network state: %v", err)
		}
		if initialized || !reflect.DeepEqual(record, NetworkRecord{}) {
			t.Fatalf("uninitialized network state = (%#v, %t), want zero and absent", record, initialized)
		}
	})

	t.Run("partial migration", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		mustProjectStoreReadExec(t, connection, networkStoreReadTestSchema[0])

		_, _, err := store.Network(context.Background())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || corrupt.Entity != "network state" || corrupt.Key != "schema" {
			t.Fatalf("partial schema error = %v, want network schema CorruptStateError", err)
		}
		if !strings.Contains(err.Error(), "network persistence schema is incomplete") {
			t.Fatalf("partial schema error = %v, want incomplete schema detail", err)
		}
	})

	t.Run("pre-stage migration", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		for index, statement := range networkStoreReadTestSchema {
			if index == 0 {
				statement = strings.Replace(statement, "\n\t\tstage TEXT,", "", 1)
			}
			mustProjectStoreReadExec(t, connection, statement)
		}

		_, _, err := store.Network(context.Background())
		assertNetworkStoreReadCorruption(t, err, "network_state is missing stage column")
	})

	t.Run("wrong schema object", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		for _, statement := range networkStoreReadTestSchema[1:] {
			mustProjectStoreReadExec(t, connection, statement)
		}
		mustProjectStoreReadExec(t, connection, "CREATE VIEW network_state AS SELECT 1 AS id")

		_, _, err := store.Network(context.Background())
		assertNetworkStoreReadCorruption(t, err, "network_state must be one table")
	})
}

// TestStoreNetworkReadsEveryTableInsideOneTransaction verifies no durable child can be observed outside the aggregate instant.
func TestStoreNetworkReadsEveryTableInsideOneTransaction(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	createNetworkStoreReadSchema(t, connection)
	seedInitializedNetworkStoreReadState(t, connection)

	observed := make(map[string]int, len(networkTableNames()))
	nonTransactional := make(map[string]bool)
	callback := "harbor:test_network_transaction"
	if err := connection.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		table := tx.Statement.Table
		if table != "harbor_state" && !slicesContain(networkTableNames(), table) {
			return
		}
		observed[table]++
		if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
			nonTransactional[table] = true
		}
	}); err != nil {
		t.Fatalf("register network query observer: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Callback().Query().Remove(callback)
	})

	if _, initialized, err := store.Network(context.Background()); err != nil || !initialized {
		t.Fatalf("read initialized network aggregate = initialized %t, error %v", initialized, err)
	}
	for _, table := range networkTableNames() {
		if observed[table] != 1 {
			t.Fatalf("%s query count = %d, want 1", table, observed[table])
		}
		if nonTransactional[table] {
			t.Fatalf("%s was queried outside the aggregate transaction", table)
		}
	}
	if observed["harbor_state"] != 1 || nonTransactional["harbor_state"] {
		t.Fatalf("Harbor high-water query = count %d, outside transaction %t", observed["harbor_state"], nonTransactional["harbor_state"])
	}
}

// TestStoreNetworkReturnsInitializedDefensiveRecord verifies persisted rows become a canonical record with no shared mutable output.
func TestStoreNetworkReturnsInitializedDefensiveRecord(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	createNetworkStoreReadSchema(t, connection)
	seedInitializedNetworkStoreReadState(t, connection)

	first, initialized, err := store.Network(context.Background())
	if err != nil {
		t.Fatalf("read initialized network state: %v", err)
	}
	if !initialized || first.Revision != 7 {
		t.Fatalf("initialized network state = revision %d, initialized %t", first.Revision, initialized)
	}
	if got := first.Reservations.SuppressedProjectIDs; !reflect.DeepEqual(got, []domain.ProjectID{"project-alpha"}) {
		t.Fatalf("suppressed projects = %v, want project-alpha", got)
	}
	if len(first.Reservations.Endpoints) != 1 || first.Reservations.Endpoints[0].Key.ProjectID != "project-beta" {
		t.Fatalf("publishable endpoints = %#v, want only project-beta", first.Reservations.Endpoints)
	}

	baseline, _, err := store.Network(context.Background())
	if err != nil {
		t.Fatalf("read defensive baseline: %v", err)
	}
	first.Leases[0] = first.Leases[len(first.Leases)-1]
	first.Reservations.Endpoints[0].Host = "mutated.test"
	first.Reservations.SuppressedProjectIDs[0] = "mutated-project"
	afterMutation, _, err := store.Network(context.Background())
	if err != nil {
		t.Fatalf("read after caller mutation: %v", err)
	}
	if !reflect.DeepEqual(afterMutation, baseline) {
		t.Fatalf("caller mutation changed subsequent record: got %#v, want %#v", afterMutation, baseline)
	}
}

// TestStoreNetworkValidatesGlobalRevisionOwnership verifies the root is visible and uniquely owns its revision.
func TestStoreNetworkValidatesGlobalRevisionOwnership(t *testing.T) {
	t.Run("future revision", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		createNetworkStoreReadSchema(t, connection)
		seedInitializedNetworkStoreReadState(t, connection)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 6 WHERE id = 1")

		_, _, err := store.Network(context.Background())
		assertNetworkStoreReadCorruption(t, err, "exceeds captured sequence 6")
	})

	t.Run("project collision", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
		createNetworkStoreReadSchema(t, connection)
		seedInitializedNetworkStoreReadState(t, connection)
		mustProjectStoreReadExec(t, connection, "UPDATE projects SET revision = 7 WHERE project_id = 'project-alpha'")

		_, _, err := store.Network(context.Background())
		assertNetworkStoreReadCorruption(t, err, `network state reuses revision owned by project "project-alpha"`)
	})
}

// TestStoreNetworkPreservesQueryErrorIdentity verifies callers can distinguish storage failures from durable corruption.
func TestStoreNetworkPreservesQueryErrorIdentity(t *testing.T) {
	for _, table := range networkTableNames() {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			createNetworkStoreReadSchema(t, connection)
			queryErr := errors.New("network query failure for " + table)
			callback := "harbor:test_network_query_failure_" + table
			if err := connection.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(queryErr)
				}
			}); err != nil {
				t.Fatalf("register %s query failure: %v", table, err)
			}
			t.Cleanup(func() {
				_ = connection.Callback().Query().Remove(callback)
			})

			_, _, err := store.Network(context.Background())
			if !errors.Is(err, queryErr) {
				t.Fatalf("%s query error = %v, want sentinel identity", table, err)
			}
		})
	}

	for _, table := range []string{"projects", "operations", "harbor_state", "recent_resources", "operation_transitions"} {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
			createNetworkStoreReadSchema(t, connection)
			seedInitializedNetworkStoreReadState(t, connection)
			queryErr := errors.New("network referential query failure for " + table)
			callback := "harbor:test_network_referential_query_failure_" + table
			if err := connection.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(queryErr)
				}
			}); err != nil {
				t.Fatalf("register %s query failure: %v", table, err)
			}
			t.Cleanup(func() {
				_ = connection.Callback().Query().Remove(callback)
			})

			_, _, err := store.Network(context.Background())
			if !errors.Is(err, queryErr) {
				t.Fatalf("%s query error = %v, want sentinel identity", table, err)
			}
		})
	}
}

// TestStoreNetworkHonorsCancellation verifies a cancelled caller never starts schema inspection.
func TestStoreNetworkHonorsCancellation(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreReadTestClock)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := store.Network(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled network read error = %v, want context.Canceled", err)
	}
}

// createNetworkStoreReadSchema installs the complete optional table set for one Store test.
func createNetworkStoreReadSchema(t *testing.T, connection *gorm.DB) {
	t.Helper()
	for _, statement := range networkStoreReadTestSchema {
		mustProjectStoreReadExec(t, connection, statement)
	}
}

// seedInitializedNetworkStoreReadState inserts every network row class and its referenced project and operation owners.
func seedInitializedNetworkStoreReadState(t *testing.T, connection *gorm.DB) {
	t.Helper()
	insertEmptyProjectStoreReadProject(t, connection, "project-alpha", "/work/project-alpha", "project-alpha", 1)
	insertEmptyProjectStoreReadProject(t, connection, "project-beta", "/work/project-beta", "project-beta", 2)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operations (id, intent_id, kind, project_id, state, phase, requested_at, revision)
		 VALUES ('operation-release-alpha', 'intent-release-alpha', 'project.unregister', 'project-alpha', 'running', 'network.release', ?, 3)`,
		projectStoreReadTestTime(),
	)
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
		 VALUES ('operation-unrelated', 'intent-unrelated', 'maintenance.run', 'queued', 'queued', ?, 4)`,
		projectStoreReadTestTime(),
	)

	rows := validNetworkModelRows()
	rows.Releases = []models.NetworkProjectRelease{{
		Id:              71,
		NetworkStateId:  1,
		ProjectId:       null.StringFrom("project-alpha"),
		SourceProjectId: "project-alpha",
		OperationId:     "operation-release-alpha",
		State:           "releasing",
		BeginGeneration: 100,
		BeganAt:         projectStoreReadTestTime().Add(4 * time.Minute),
	}}
	fixtures := []struct {
		name  string
		value any
	}{
		{name: "network state", value: &rows.States},
		{name: "network pool candidates", value: &rows.Candidates},
		{name: "network setup evidence", value: &rows.SetupEvidence},
		{name: "network shared listeners", value: &rows.Listeners},
		{name: "loopback address leases", value: &rows.Leases},
		{name: "public endpoint leases", value: &rows.Endpoints},
		{name: "network project releases", value: &rows.Releases},
	}
	for _, fixture := range fixtures {
		if err := connection.Create(fixture.value).Error; err != nil {
			t.Fatalf("insert %s: %v", fixture.name, err)
		}
	}
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 7 WHERE id = 1")
}

// slicesContain keeps callback filtering independent from SQL rendering details.
func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// assertNetworkStoreReadCorruption requires a typed durable-corruption boundary and targeted diagnostic detail.
func assertNetworkStoreReadCorruption(t *testing.T, err error, want string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("network read error = %v, want CorruptStateError", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("network read error = %v, want containing %q", err, want)
	}
}
