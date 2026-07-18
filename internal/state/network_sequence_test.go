package state

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// TestOptionalNetworkSequenceSupportsLegacyAndUninitializedDatabases verifies rollout does not require a root before setup commits one.
func TestOptionalNetworkSequenceSupportsLegacyAndUninitializedDatabases(t *testing.T) {
	t.Run("legacy missing table", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		if _, exists, err := readOptionalNetworkSequenceOwner(connection); err != nil || exists {
			t.Fatalf("legacy network owner = exists %t, error %v", exists, err)
		}
		if _, err := store.Snapshot(context.Background()); err != nil {
			t.Fatalf("read legacy snapshot: %v", err)
		}
		project := emptyProjectStoreMutationProject("project-legacy-network")
		if record, err := store.PutProject(context.Background(), project); err != nil || record.Revision != 1 {
			t.Fatalf("legacy project put = %#v, error %v", record, err)
		}
	})

	t.Run("migrated empty schema", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		applyNetworkInitializeTestMigration(t, connection)
		if _, exists, err := readOptionalNetworkSequenceOwner(connection); err != nil || exists {
			t.Fatalf("empty network owner = exists %t, error %v", exists, err)
		}
		if _, err := store.Snapshot(context.Background()); err != nil {
			t.Fatalf("read uninitialized network snapshot: %v", err)
		}
		project := emptyProjectStoreMutationProject("project-empty-network")
		if record, err := store.PutProject(context.Background(), project); err != nil || record.Revision != 1 {
			t.Fatalf("empty-network project put = %#v, error %v", record, err)
		}
	})

	t.Run("partial schema", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		createNetworkSequenceTestTable(t, connection)
		project := emptyProjectStoreMutationProject("project-partial-network")
		_, err := store.PutProject(context.Background(), project)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "network persistence schema is incomplete") {
			t.Fatalf("partial-schema project put error = %v", err)
		}
		if highWater := networkSequenceTestHighWater(t, connection); highWater != 0 {
			t.Fatalf("partial-schema high-water = %d, want 0", highWater)
		}
	})
}

// TestOptionalNetworkSequenceAllowsDistinctCommittedOwnership verifies a valid root participates in allocation without blocking later owners.
func TestOptionalNetworkSequenceAllowsDistinctCommittedOwnership(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	initialized, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
	if err != nil || initialized.Record.Revision != 7 {
		t.Fatalf("initialize committed network owner = %#v, error %v", initialized, err)
	}

	revision, exists, err := readOptionalNetworkSequenceOwner(connection)
	if err != nil || !exists || revision != 7 {
		t.Fatalf("committed network owner = revision %d, exists %t, error %v", revision, exists, err)
	}
	if _, err := store.Snapshot(context.Background()); err != nil {
		t.Fatalf("read snapshot with committed network owner: %v", err)
	}
	project, err := store.Project(context.Background(), "project-alpha")
	if err != nil {
		t.Fatalf("read project after network owner: %v", err)
	}
	project.Project.Favorite = !project.Project.Favorite
	record, err := store.PutProject(context.Background(), project.Project)
	if err != nil || record.Revision != 8 {
		t.Fatalf("project after network owner = %#v, error %v, want revision 8", record, err)
	}
	if highWater := networkSequenceTestHighWater(t, connection); highWater != 8 {
		t.Fatalf("high-water after distinct project owner = %d, want 8", highWater)
	}
}

// TestOptionalNetworkSequenceRejectsMalformedSingletons verifies weakened schemas cannot disguise malformed root ownership.
func TestOptionalNetworkSequenceRejectsMalformedSingletons(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *gorm.DB)
		want  string
	}{
		{
			name: "view instead of table",
			setup: func(t *testing.T, connection *gorm.DB) {
				mustProjectStoreReadExec(t, connection, "CREATE VIEW network_state AS SELECT 1 AS id, 1 AS revision")
			},
			want: "must be one table",
		},
		{
			name: "missing revision column",
			setup: func(t *testing.T, connection *gorm.DB) {
				mustProjectStoreReadExec(t, connection, "CREATE TABLE network_state (id INTEGER)")
			},
			want: "read singleton revision",
		},
		{
			name: "multiple rows",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkSequenceTestTable(t, connection)
				mustProjectStoreReadExec(t, connection, "INSERT INTO network_state (id, revision) VALUES (1, 1), (2, 2)")
			},
			want: "singleton contains 2 rows",
		},
		{
			name: "null ID",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkSequenceTestTable(t, connection)
				mustProjectStoreReadExec(t, connection, "INSERT INTO network_state (id, revision) VALUES (NULL, 1)")
			},
			want: "singleton ID must not be NULL",
		},
		{
			name: "wrong ID",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkSequenceTestTable(t, connection)
				mustProjectStoreReadExec(t, connection, "INSERT INTO network_state (id, revision) VALUES (2, 1)")
			},
			want: "singleton ID must be 1",
		},
		{
			name: "null revision",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkSequenceTestTable(t, connection)
				mustProjectStoreReadExec(t, connection, "INSERT INTO network_state (id, revision) VALUES (1, NULL)")
			},
			want: "revision must not be NULL",
		},
		{
			name: "zero revision",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkSequenceTestTable(t, connection)
				insertNetworkSequenceTestRoot(t, connection, 0)
			},
			want: "revision must be positive",
		},
		{
			name: "negative revision",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkSequenceTestTable(t, connection)
				insertNetworkSequenceTestRoot(t, connection, -1)
			},
			want: "revision must be positive",
		},
		{
			name: "cross-client overflow",
			setup: func(t *testing.T, connection *gorm.DB) {
				createNetworkSequenceTestTable(t, connection)
				insertNetworkSequenceTestRoot(t, connection, int64(domain.MaximumSequence)+1)
			},
			want: "exceeds the cross-client ordering range",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			test.setup(t, connection)

			_, _, err := readOptionalNetworkSequenceOwner(connection)
			assertNetworkSequenceCorruption(t, err, test.want)
			_, err = store.Snapshot(context.Background())
			assertNetworkSequenceCorruption(t, err, test.want)
		})
	}
}

// TestOptionalNetworkSequenceReportsQueryFailures verifies storage failures remain distinguishable from optional absence.
func TestOptionalNetworkSequenceReportsQueryFailures(t *testing.T) {
	t.Run("schema inspection", func(t *testing.T) {
		_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := readOptionalNetworkSequenceOwner(connection.WithContext(cancelled))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled schema inspection error = %v, want context.Canceled", err)
		}
	})

	for _, test := range []struct {
		table string
		want  string
	}{
		{table: "projects", want: "verify network project sequence owners"},
		{table: "recent_resources", want: "verify network recent sequence owners"},
		{table: "operations", want: "verify network operation sequence owners"},
		{table: "operation_transitions", want: "verify network transition sequence owners"},
	} {
		t.Run(test.table, func(t *testing.T) {
			_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			createNetworkSequenceTestTable(t, connection)
			insertNetworkSequenceTestRoot(t, connection, 1)
			mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, "DROP TABLE "+test.table)

			err := validateOptionalNetworkSequenceOwner(connection, 1)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("missing %s error = %v, want %q", test.table, err, test.want)
			}
		})
	}
}

// TestStoreSnapshotRejectsNetworkSequenceCollisions verifies the root cannot reuse any materialized global owner.
func TestStoreSnapshotRejectsNetworkSequenceCollisions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Store, *gorm.DB)
		want  string
	}{
		{
			name: "project",
			setup: func(t *testing.T, _ *Store, connection *gorm.DB) {
				seedSingleProjectStoreReadState(t, connection, "project-network-owner", 1)
				insertNetworkSequenceTestRoot(t, connection, 1)
			},
			want: "project \"project-network-owner\"",
		},
		{
			name: "recent resource",
			setup: func(t *testing.T, _ *Store, connection *gorm.DB) {
				seedSingleProjectStoreReadState(t, connection, "project-network-recent", 1)
				mustProjectStoreReadExec(t, connection,
					"INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES (?, 'docs', ?, 2)",
					"project-network-recent", projectStoreReadTestTime(),
				)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")
				insertNetworkSequenceTestRoot(t, connection, 2)
			},
			want: "recent resource",
		},
		{
			name: "active operation",
			setup: func(t *testing.T, _ *Store, connection *gorm.DB) {
				insertNetworkSequenceTestOperation(t, connection, "operation-network-active", "queued", 1)
				mustProjectStoreReadExec(t, connection,
					"INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence) VALUES (?, 1, 'queued', 'queued', ?, 1)",
					"operation-network-active", projectStoreReadTestTime(),
				)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
				insertNetworkSequenceTestRoot(t, connection, 1)
			},
			want: "operation \"operation-network-active\"",
		},
		{
			name: "terminal operation header",
			setup: func(t *testing.T, _ *Store, connection *gorm.DB) {
				seedSucceededOperationHistory(t, connection, "network-terminal", 1, 2, 3)
				mustProjectStoreReadExec(t, connection, "UPDATE operations SET revision = 4 WHERE id = 'operation-network-terminal'")
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 4 WHERE id = 1")
				insertNetworkSequenceTestRoot(t, connection, 4)
			},
			want: "operation \"operation-network-terminal\"",
		},
		{
			name: "retained transition",
			setup: func(t *testing.T, _ *Store, connection *gorm.DB) {
				seedSucceededOperationHistory(t, connection, "network-transition", 1, 2, 3)
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 3 WHERE id = 1")
				insertNetworkSequenceTestRoot(t, connection, 2)
			},
			want: "operation transition \"operation-network-transition\" ordinal 2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			createNetworkSequenceTestTable(t, connection)
			test.setup(t, store, connection)

			_, err := store.Snapshot(context.Background())
			assertHarborSequenceCollision(t, err, test.want)
			if test.name == "project" {
				_, err = store.Project(context.Background(), "project-network-owner")
				assertHarborSequenceCollision(t, err, test.want)
			}
		})
	}
}

// TestNetworkSequencePreflightRejectsFutureRootWithoutConsumption verifies every writer sees the optional root before advancing authority.
func TestNetworkSequencePreflightRejectsFutureRootWithoutConsumption(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	initialized, err := store.InitializeNetwork(context.Background(), networkInitializeTestEmptyRequest())
	if err != nil || initialized.Record.Revision != 1 {
		t.Fatalf("initialize future network root = %#v, error %v", initialized, err)
	}
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 0 WHERE id = 1")

	project := emptyProjectStoreMutationProject("project-network-future")
	if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "exceeds captured sequence 0") {
		t.Fatalf("future network put error = %v", err)
	}
	if highWater := networkSequenceTestHighWater(t, connection); highWater != 0 {
		t.Fatalf("high-water after future root rejection = %d, want 0", highWater)
	}
	if count := projectStoreMutationCount(t, connection, "projects"); count != 0 {
		t.Fatalf("project count after future root rejection = %d, want 0", count)
	}
}

// TestNetworkSequenceTargetMutationsRejectReuseWithoutConsumption verifies overwritten owners cannot erase an existing collision.
func TestNetworkSequenceTargetMutationsRejectReuseWithoutConsumption(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := emptyProjectStoreMutationProject("project-network-target")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put target project: %v", err)
		}
		createNetworkSequenceTestTable(t, connection)
		insertNetworkSequenceTestRoot(t, connection, 1)

		changed := project
		changed.Name = "Changed Project"
		if _, err := store.PutProject(context.Background(), changed); err == nil || !strings.Contains(err.Error(), "network state") {
			t.Fatalf("project/network reuse error = %v", err)
		}
		if highWater := networkSequenceTestHighWater(t, connection); highWater != 1 {
			t.Fatalf("project collision high-water = %d, want 1", highWater)
		}
		assertProjectStoreMutationRoot(t, connection, project.ID, project.Name, 1)
	})

	t.Run("recent resource", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-network-recent-target")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put recency project: %v", err)
		}
		reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err != nil {
			t.Fatalf("record initial recency: %v", err)
		}
		createNetworkSequenceTestTable(t, connection)
		insertNetworkSequenceTestRoot(t, connection, 2)

		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "network state") {
			t.Fatalf("recency/network reuse error = %v", err)
		}
		if highWater := networkSequenceTestHighWater(t, connection); highWater != 2 {
			t.Fatalf("recency collision high-water = %d, want 2", highWater)
		}
		assertProjectStoreMutationRecent(t, connection, reference, 2)
	})
}

// TestNetworkSequenceUnregisterRejectsPartialSchemaBeforeLiveAndReplay verifies optional persistence fails closed once migration starts.
func TestNetworkSequenceUnregisterRejectsPartialSchemaBeforeLiveAndReplay(t *testing.T) {
	t.Run("live history", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-network-unregister-live")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-network-unregister-live")
		createNetworkSequenceTestTable(t, connection)
		insertNetworkSequenceTestRoot(t, connection, 2)

		_, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
		)
		assertNetworkSequencePartialSchema(t, err)
		if highWater := networkSequenceTestHighWater(t, connection); highWater != 3 {
			t.Fatalf("live unregister collision high-water = %d, want 3", highWater)
		}
		if count := projectStoreMutationCount(t, connection, "projects"); count != 1 {
			t.Fatalf("project count after live collision = %d, want 1", count)
		}
	})

	t.Run("completed replay", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-network-unregister-replay")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-network-unregister-replay")
		completed, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
		)
		if err != nil || completed.Revision != 4 {
			t.Fatalf("complete unregister before collision = %#v, error %v", completed, err)
		}
		createNetworkSequenceTestTable(t, connection)
		insertNetworkSequenceTestRoot(t, connection, 4)

		_, err = store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
		)
		assertNetworkSequencePartialSchema(t, err)
		if highWater := networkSequenceTestHighWater(t, connection); highWater != 4 {
			t.Fatalf("replay collision high-water = %d, want 4", highWater)
		}
		if count := projectStoreMutationCount(t, connection, "projects"); count != 0 {
			t.Fatalf("project count after replay collision = %d, want 0", count)
		}
	})

	t.Run("future replay root", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-network-unregister-future")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-network-unregister-future")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
		); err != nil {
			t.Fatalf("complete unregister before future root: %v", err)
		}
		createNetworkSequenceTestTable(t, connection)
		insertNetworkSequenceTestRoot(t, connection, 5)

		_, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
		)
		assertNetworkSequencePartialSchema(t, err)
		if highWater := networkSequenceTestHighWater(t, connection); highWater != 4 {
			t.Fatalf("future replay high-water = %d, want 4", highWater)
		}
	})
}

// assertNetworkSequencePartialSchema requires the stable corruption boundary for a partially installed optional schema.
func assertNetworkSequencePartialSchema(t *testing.T, err error) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || corrupt.Entity != "network state" || corrupt.Key != "schema" ||
		!strings.Contains(err.Error(), "network persistence schema is incomplete") {
		t.Fatalf("partial network schema error = %v", err)
	}
}

// createNetworkSequenceTestTable creates the intentionally permissive optional root used by corruption tests.
func createNetworkSequenceTestTable(t *testing.T, connection *gorm.DB) {
	t.Helper()
	mustProjectStoreReadExec(t, connection, "CREATE TABLE network_state (id INTEGER, revision INTEGER)")
}

// insertNetworkSequenceTestRoot inserts one optional root revision without changing the independent Harbor high-water.
func insertNetworkSequenceTestRoot(t *testing.T, connection *gorm.DB, revision any) {
	t.Helper()
	mustProjectStoreReadExec(t, connection, "INSERT INTO network_state (id, revision) VALUES (1, ?)", revision)
}

// insertNetworkSequenceTestOperation inserts one permissive operation header for root-collision tests.
func insertNetworkSequenceTestOperation(t *testing.T, connection *gorm.DB, operationID string, state string, revision int) {
	t.Helper()
	mustProjectStoreReadExec(t, connection,
		`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
		 VALUES (?, ?, 'maintenance.run', ?, ?, ?, ?)`,
		operationID, "intent-"+operationID, state, state, projectStoreReadTestTime(), revision,
	)
}

// networkSequenceTestHighWater reads the authority directly because corrupt roots intentionally make complete Store reads fail.
func networkSequenceTestHighWater(t *testing.T, connection *gorm.DB) int {
	t.Helper()
	var highWater int
	if err := connection.Raw("SELECT sequence FROM harbor_state WHERE id = 1").Scan(&highWater).Error; err != nil {
		t.Fatalf("read Harbor high-water: %v", err)
	}
	return highWater
}

// assertNetworkSequenceCorruption verifies malformed optional roots retain the typed network-state boundary.
func assertNetworkSequenceCorruption(t *testing.T, err error, cause string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("network state error = %v, want CorruptStateError", err)
	}
	if corrupt.Entity != "network state" || !strings.Contains(err.Error(), cause) {
		t.Fatalf("network corruption = %#v / %v, want cause %q", corrupt, err, cause)
	}
}

// assertHarborSequenceCollision verifies optional-root reuse reaches the shared ordering corruption boundary.
func assertHarborSequenceCollision(t *testing.T, err error, existing string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("sequence collision error = %v, want CorruptStateError", err)
	}
	if corrupt.Entity != "Harbor sequence" || !strings.Contains(err.Error(), "reuses revision owned by "+existing) {
		t.Fatalf("sequence collision = %#v / %v, want existing owner %q", corrupt, err, existing)
	}
}
