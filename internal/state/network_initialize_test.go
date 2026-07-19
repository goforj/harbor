package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/migrations"
	"gorm.io/gorm"
)

const (
	// networkInitializeTestMigrationName identifies the base production network schema used by state tests.
	networkInitializeTestMigrationName = "2026_07_18_152632_create_network_persistence"
	// networkInitializeTestDigestMigrationName identifies the replay-proof upgrade applied after the base schema.
	networkInitializeTestDigestMigrationName = "2026_07_18_175743_add_network_release_set_digest"
	// networkInitializeTestStageMigrationName identifies the identity-stage compatibility upgrade.
	networkInitializeTestStageMigrationName = "2026_07_19_120000_add_network_stage"
	// networkInitializeTestOwnershipMigrationName identifies the daemon ownership projection schema.
	networkInitializeTestOwnershipMigrationName = "2026_07_19_140000_create_machine_ownership_projections"
	// networkInitializeTestOwnershipPolicyMigrationName identifies the policy-bound ownership projection upgrade.
	networkInitializeTestOwnershipPolicyMigrationName = "2026_07_19_150000_add_machine_ownership_network_policy_fingerprint"
)

// TestStoreInitializeNetworkCommitsCompleteAggregate verifies the first write owns one global revision and every hidden host fact.
func TestStoreInitializeNetworkCommitsCompleteAggregate(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	request := networkMutationTestInitializeRequest()

	result, err := store.InitializeNetwork(context.Background(), request)
	if err != nil {
		t.Fatalf("InitializeNetwork() error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 7 || result.Record.Stage != NetworkStageFull {
		t.Fatalf("InitializeNetwork() result = %#v, want applied revision 7", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("NetworkMutationResult.Validate() error = %v", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
		t.Fatalf("Harbor high-water = %d, want 7", highWater)
	}

	wantCounts := map[string]int64{
		"network_state":            1,
		"network_pool_candidates":  3,
		"network_setup_evidence":   4,
		"network_shared_listeners": 3,
		"loopback_address_leases":  3,
		"public_endpoint_leases":   3,
		"network_project_releases": 0,
	}
	for table, want := range wantCounts {
		if got := networkInitializeTestCount(t, connection, table); got != want {
			t.Fatalf("%s row count = %d, want %d", table, got, want)
		}
	}

	var tcp models.PublicEndpointLease
	if err := connection.Where("protocol = ?", "tcp").First(&tcp).Error; err != nil {
		t.Fatalf("read persisted TCP endpoint: %v", err)
	}
	if !tcp.LoopbackAddressLeaseId.Valid || tcp.LoopbackAddressLeaseId.Int64 <= 0 {
		t.Fatalf("TCP endpoint lease ID = %#v, want inserted lease identity", tcp.LoopbackAddressLeaseId)
	}
	var lease models.LoopbackAddressLease
	if err := connection.First(&lease, tcp.LoopbackAddressLeaseId.Int64).Error; err != nil {
		t.Fatalf("read referenced TCP lease: %v", err)
	}
	if lease.ProjectId.String != tcp.ProjectId || lease.Address != tcp.Address || lease.EnsureEvidence != " verified ensure " {
		t.Fatalf("TCP endpoint/lease join = endpoint %#v, lease %#v", tcp, lease)
	}

	read, initialized, err := store.Network(context.Background())
	if err != nil || !initialized {
		t.Fatalf("Network() = initialized %t, error %v", initialized, err)
	}
	if !reflect.DeepEqual(read, result.Record) {
		t.Fatalf("Network() record = %#v, want %#v", read, result.Record)
	}
}

// TestStoreInitializeNetworkReplaysExactHiddenState verifies retries ignore later project revisions but consume no sequence.
func TestStoreInitializeNetworkReplaysExactHiddenState(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	request := networkMutationTestInitializeRequest()
	first, err := store.InitializeNetwork(context.Background(), request)
	if err != nil {
		t.Fatalf("initial InitializeNetwork() error = %v", err)
	}

	replacement := projectStoreMutationTestProject("project-alpha")
	replacement.Name = "Advanced after network initialization"
	advanced, err := store.PutProject(context.Background(), replacement)
	if err != nil {
		t.Fatalf("advance project after initialization: %v", err)
	}
	if advanced.Revision != 8 {
		t.Fatalf("advanced project revision = %d, want 8", advanced.Revision)
	}

	replayed, err := store.InitializeNetwork(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed InitializeNetwork() error = %v", err)
	}
	if !replayed.Replayed || !reflect.DeepEqual(replayed.Record, first.Record) {
		t.Fatalf("replayed result = %#v, want original record marked replayed", replayed)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 8 {
		t.Fatalf("Harbor high-water after replay = %d, want 8", highWater)
	}
	for table, want := range map[string]int64{
		"network_state":            1,
		"network_pool_candidates":  3,
		"network_setup_evidence":   4,
		"network_shared_listeners": 3,
		"loopback_address_leases":  3,
		"public_endpoint_leases":   3,
	} {
		if got := networkInitializeTestCount(t, connection, table); got != want {
			t.Fatalf("%s count after replay = %d, want %d", table, got, want)
		}
	}
}

// TestStoreInitializeNetworkReplayRejectsProjectRewind verifies retry tolerance is directional because revisions never move backward.
func TestStoreInitializeNetworkReplayRejectsProjectRewind(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	request := networkMutationTestInitializeRequest()
	if _, err := store.InitializeNetwork(context.Background(), request); err != nil {
		t.Fatalf("initial InitializeNetwork() error = %v", err)
	}
	mustProjectStoreReadExec(t, connection, "UPDATE projects SET revision = 4 WHERE project_id = 'project-alpha'")

	_, err := store.InitializeNetwork(context.Background(), request)
	var conflict *ProjectRevisionConflictError
	if !errors.As(err, &conflict) || conflict.ProjectID != "project-alpha" || conflict.Expected != 5 || conflict.Actual != 4 {
		t.Fatalf("rewound replay error = %v, want typed alpha 5/4 conflict", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
		t.Fatalf("high-water after rewound replay = %d, want 7", highWater)
	}
}

// TestStoreInitializeNetworkRejectsDifferentSemanticRetries verifies every hidden fact group participates in replay identity.
func TestStoreInitializeNetworkRejectsDifferentSemanticRetries(t *testing.T) {
	tests := []struct {
		name       string
		difference string
		mutate     func(*InitializeNetworkRequest)
	}{
		{name: "root ownership", difference: "network ownership", mutate: func(request *InitializeNetworkRequest) {
			request.Ownership.Generation++
		}},
		{name: "pool generation", difference: "network pool candidates", mutate: func(request *InitializeNetworkRequest) {
			request.PoolGeneration++
		}},
		{name: "setup evidence", difference: "network setup proofs", mutate: func(request *InitializeNetworkRequest) {
			request.Setup[0].Evidence = "different machine ownership proof"
		}},
		{name: "setup time", difference: "network setup proofs", mutate: func(request *InitializeNetworkRequest) {
			request.Setup[1].VerifiedAt = request.Setup[1].VerifiedAt.Add(time.Second)
		}},
		{name: "listener facts", difference: "network listeners", mutate: func(request *InitializeNetworkRequest) {
			request.Listeners.DNS.Generation++
		}},
		{name: "lease ownership", difference: "network lease ensures", mutate: func(request *InitializeNetworkRequest) {
			request.Ensures[0].Lease.Ownership.Generation++
		}},
		{name: "lease evidence", difference: "network lease ensures", mutate: func(request *InitializeNetworkRequest) {
			request.Ensures[0].EnsureEvidence = "different ensure proof"
		}},
		{name: "lease time", difference: "network lease ensures", mutate: func(request *InitializeNetworkRequest) {
			request.Ensures[0].LeasedAt = request.Ensures[0].LeasedAt.Add(time.Second)
		}},
		{name: "endpoint generation", difference: "network endpoints", mutate: func(request *InitializeNetworkRequest) {
			request.Endpoints[0].Generation++
		}},
		{name: "root time", difference: "network root timestamps", mutate: func(request *InitializeNetworkRequest) {
			request.At = request.At.Add(time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newNetworkInitializeTestHarness(t, true)
			request := networkMutationTestInitializeRequest()
			if _, err := store.InitializeNetwork(context.Background(), request); err != nil {
				t.Fatalf("initial InitializeNetwork() error = %v", err)
			}
			test.mutate(&request)

			_, err := store.InitializeNetwork(context.Background(), request)
			var conflict *NetworkInitializationConflictError
			if !errors.As(err, &conflict) || conflict.ActualRevision != 7 || conflict.Difference != test.difference {
				t.Fatalf("semantic retry error = %v, want revision 7 %q conflict", err, test.difference)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
				t.Fatalf("Harbor high-water after conflict = %d, want 7", highWater)
			}
		})
	}
}

// TestStoreInitializeNetworkRequiresExactProjectSnapshot verifies set and revision conflicts are typed before allocation.
func TestStoreInitializeNetworkRequiresExactProjectSnapshot(t *testing.T) {
	t.Run("project revision", func(t *testing.T) {
		store, _ := newNetworkInitializeTestHarness(t, true)
		request := networkMutationTestInitializeRequest()
		request.ExpectedProjects[0].Revision = 4

		_, err := store.InitializeNetwork(context.Background(), request)
		var conflict *ProjectRevisionConflictError
		if !errors.As(err, &conflict) || conflict.ProjectID != "project-alpha" || conflict.Expected != 4 || conflict.Actual != 5 {
			t.Fatalf("project revision error = %v, want typed alpha 4/5 conflict", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
			t.Fatalf("high-water after project revision conflict = %d, want 6", highWater)
		}
	})

	t.Run("project set", func(t *testing.T) {
		store, _ := newNetworkInitializeTestHarness(t, true)
		request := networkMutationTestInitializeRequest()
		request.ExpectedProjects = request.ExpectedProjects[:1]
		request.Ensures = request.Ensures[:2]
		request.Endpoints = []EndpointReservation{request.Endpoints[0], request.Endpoints[2]}

		_, err := store.InitializeNetwork(context.Background(), request)
		var conflict *NetworkProjectSetConflictError
		if !errors.As(err, &conflict) ||
			!reflect.DeepEqual(conflict.Expected, []domain.ProjectID{"project-alpha"}) ||
			!reflect.DeepEqual(conflict.Actual, []domain.ProjectID{"project-alpha", "project-beta"}) {
			t.Fatalf("project set error = %v, want typed alpha versus alpha/beta conflict", err)
		}
	})

	t.Run("project sequence owner", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, true)
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
			 VALUES ('operation-collision', 'intent-collision', 'project.start', 'queued', 'queued', ?, 5)`,
			networkMutationTestTime(),
		)

		_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "reuses revision owned by operation") {
			t.Fatalf("project sequence ownership error = %v", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
			t.Fatalf("high-water after owner collision = %d, want 6", highWater)
		}
	})
}

// TestStoreInitializeNetworkAcceptsEmptyTopologyOnlyWithoutProjects verifies the deliberate project-free first-run shape.
func TestStoreInitializeNetworkAcceptsEmptyTopologyOnlyWithoutProjects(t *testing.T) {
	t.Run("zero projects", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		request := networkMutationTestInitializeRequest()
		request.ExpectedProjects = []NetworkProjectRevision{}
		request.Ensures = []NetworkLeaseEnsure{}
		request.Endpoints = []EndpointReservation{}

		result, err := store.InitializeNetwork(context.Background(), request)
		if err != nil {
			t.Fatalf("empty InitializeNetwork() error = %v", err)
		}
		if result.Replayed || result.Record.Revision != 1 || len(result.Record.Leases) != 0 || len(result.Record.Reservations.Endpoints) != 0 {
			t.Fatalf("empty InitializeNetwork() result = %#v", result)
		}
		if networkInitializeTestCount(t, connection, "loopback_address_leases") != 0 ||
			networkInitializeTestCount(t, connection, "public_endpoint_leases") != 0 {
			t.Fatal("empty initialization persisted project topology")
		}
	})

	t.Run("registered project", func(t *testing.T) {
		store, _ := newNetworkInitializeTestHarness(t, true)
		request := networkMutationTestInitializeRequest()
		request.ExpectedProjects = []NetworkProjectRevision{}
		request.Ensures = []NetworkLeaseEnsure{}
		request.Endpoints = []EndpointReservation{}

		_, err := store.InitializeNetwork(context.Background(), request)
		var conflict *NetworkProjectSetConflictError
		if !errors.As(err, &conflict) || len(conflict.Expected) != 0 || len(conflict.Actual) != 2 {
			t.Fatalf("empty topology with projects error = %v", err)
		}
	})
}

// TestStoreInitializeNetworkValidatesBeforeStorageAndHonorsContext verifies rejected calls never enter writer authority.
func TestStoreInitializeNetworkValidatesBeforeStorageAndHonorsContext(t *testing.T) {
	invalid := networkMutationTestInitializeRequest()
	invalid.ExpectedProjects = nil
	var absentStore *Store
	if _, err := absentStore.InitializeNetwork(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "must be initialized") {
		t.Fatalf("pre-storage validation error = %v", err)
	}

	store, _ := newNetworkInitializeTestHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.InitializeNetwork(ctx, networkMutationTestInitializeRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled InitializeNetwork() error = %v, want context.Canceled", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
		t.Fatalf("high-water after canceled initialization = %d, want 6", highWater)
	}
}

// TestStoreInitializeNetworkRequiresCompleteSchema verifies legacy and partially migrated databases cannot accept host facts.
func TestStoreInitializeNetworkRequiresCompleteSchema(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		_, err := store.InitializeNetwork(context.Background(), networkInitializeTestEmptyRequest())
		if err == nil || !strings.Contains(err.Error(), "schema is not installed") {
			t.Fatalf("legacy schema error = %v", err)
		}
	})

	t.Run("partial", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, networkStoreReadTestSchema[0])
		_, err := store.InitializeNetwork(context.Background(), networkInitializeTestEmptyRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "schema is incomplete") {
			t.Fatalf("partial schema error = %v", err)
		}
	})
}

// TestStoreInitializeNetworkRejectsCorruptPreflightAndExhaustedOrdering verifies no invalid retained state is advanced.
func TestStoreInitializeNetworkRejectsCorruptPreflightAndExhaustedOrdering(t *testing.T) {
	t.Run("orphaned network child", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		createNetworkStoreReadSchema(t, connection)
		mustProjectStoreReadExec(t, connection, `INSERT INTO network_pool_candidates
			(id, network_state_id, ordinal, address, generation) VALUES (1, 1, 1, '127.77.0.10', 1)`)

		_, err := store.InitializeNetwork(context.Background(), networkInitializeTestEmptyRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "child rows exist without the singleton root") {
			t.Fatalf("orphaned child error = %v", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 0 {
			t.Fatalf("high-water after orphaned child = %d, want 0", highWater)
		}
	})

	t.Run("future retained project", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, true)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 4 WHERE id = 1")

		_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "exceeds Harbor high-water 4") {
			t.Fatalf("future retained project error = %v", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != 4 {
			t.Fatalf("high-water after retained project failure = %d, want 4", highWater)
		}
	})

	t.Run("sequence exhausted", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, true)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = ? WHERE id = 1", domain.MaximumSequence)

		_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
		if err == nil || !strings.Contains(err.Error(), "cross-client ordering range") {
			t.Fatalf("exhausted sequence error = %v", err)
		}
		if highWater := projectStoreMutationSequence(t, store); highWater != domain.MaximumSequence {
			t.Fatalf("high-water after exhausted allocation = %d, want %d", highWater, domain.MaximumSequence)
		}
		if got := networkInitializeTestCount(t, connection, "network_state"); got != 0 {
			t.Fatalf("network root count after exhausted allocation = %d, want 0", got)
		}
	})
}

// TestStoreInitializeNetworkRollsBackEveryWritePhase verifies late persistence failures restore the sequence and all network rows.
func TestStoreInitializeNetworkRollsBackEveryWritePhase(t *testing.T) {
	tests := []struct {
		name  string
		table string
	}{
		{name: "root", table: "network_state"},
		{name: "candidate", table: "network_pool_candidates"},
		{name: "setup", table: "network_setup_evidence"},
		{name: "listener", table: "network_shared_listeners"},
		{name: "lease", table: "loopback_address_leases"},
		{name: "endpoint", table: "public_endpoint_leases"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newNetworkInitializeTestHarness(t, true)
			trigger := fmt.Sprintf(
				"CREATE TRIGGER fail_network_initialize BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'network initialize %s failure'); END",
				test.table,
				test.name,
			)
			mustProjectStoreReadExec(t, connection, trigger)

			_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
			if err == nil || !strings.Contains(err.Error(), "network initialize "+test.name+" failure") {
				t.Fatalf("%s failure error = %v", test.name, err)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
				t.Fatalf("high-water after %s failure = %d, want 6", test.name, highWater)
			}
			for _, table := range networkTableNames() {
				if got := networkInitializeTestCount(t, connection, table); got != 0 {
					t.Fatalf("%s count after %s failure = %d, want 0", table, test.name, got)
				}
			}
		})
	}
}

// TestStoreInitializeNetworkRollsBackReadbackCorruption verifies a successful insert is not enough to publish an altered aggregate.
func TestStoreInitializeNetworkRollsBackReadbackCorruption(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	mustProjectStoreReadExec(t, connection, `CREATE TRIGGER alter_network_initialize
		AFTER INSERT ON public_endpoint_leases
		BEGIN
			UPDATE network_pool_candidates SET generation = 999 WHERE ordinal = 1;
		END`)

	_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("readback corruption error = %v, want CorruptStateError", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
		t.Fatalf("high-water after readback corruption = %d, want 6", highWater)
	}
	for _, table := range networkTableNames() {
		if got := networkInitializeTestCount(t, connection, table); got != 0 {
			t.Fatalf("%s count after readback corruption = %d, want 0", table, got)
		}
	}
}

// TestStoreInitializeNetworkRejectsReadbackSequenceCollision verifies the new root remains the sole owner of its revision.
func TestStoreInitializeNetworkRejectsReadbackSequenceCollision(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	mustProjectStoreReadExec(t, connection, `CREATE TRIGGER collide_network_initialize
		AFTER INSERT ON public_endpoint_leases
		BEGIN
			INSERT OR IGNORE INTO operations
				(id, intent_id, kind, state, phase, requested_at, revision)
				VALUES ('operation-network-collision', 'intent-network-collision', 'project.start', 'queued', 'queued', '2026-07-18T12:00:00Z', 7);
		END`)

	_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "reuses revision owned by operation") {
		t.Fatalf("readback sequence collision error = %v", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
		t.Fatalf("high-water after readback sequence collision = %d, want 6", highWater)
	}
	if got := networkInitializeTestCount(t, connection, "operations"); got != 0 {
		t.Fatalf("operation count after collision rollback = %d, want 0", got)
	}
}

// TestStoreInitializeNetworkRejectsAdversarialReadbackFailures verifies every post-insert read boundary rolls back atomically.
func TestStoreInitializeNetworkRejectsAdversarialReadbackFailures(t *testing.T) {
	t.Run("query failure", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, true)
		want := errors.New("readback query sentinel")
		active := false
		registerNetworkInitializeReadbackCallbacks(t, connection, &active, func(tx *gorm.DB) {
			if tx.Statement.Table == "network_state" {
				tx.AddError(want)
			}
		})

		_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
		if !errors.Is(err, want) {
			t.Fatalf("readback query error = %v, want sentinel", err)
		}
		active = false
		assertNetworkInitializeTestRollback(t, store, connection)
	})

	t.Run("conversion failure", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, true)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER corrupt_network_initialize_readback
			AFTER INSERT ON public_endpoint_leases
			BEGIN
				UPDATE network_setup_evidence SET verified_at = '2099-01-01T00:00:00Z' WHERE component = 'resolver';
			END`)

		_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "must not be after the network state update time") {
			t.Fatalf("readback conversion error = %v", err)
		}
		assertNetworkInitializeTestRollback(t, store, connection)
	})

	t.Run("missing aggregate", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, true)
		active := false
		registerNetworkInitializeReadbackCallbacks(t, connection, &active, hideNetworkInitializeReadRows)

		_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "initialized aggregate is missing after insert") {
			t.Fatalf("missing readback aggregate error = %v", err)
		}
		active = false
		assertNetworkInitializeTestRollback(t, store, connection)
	})

	t.Run("revision rewrite", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, true)
		active := false
		registerNetworkInitializeReadbackCallbacks(t, connection, &active, func(tx *gorm.DB) {
			if tx.Statement.Table != "network_state" {
				return
			}
			rows, ok := tx.Statement.Dest.(*[]models.NetworkState)
			if ok && len(*rows) == 1 {
				(*rows)[0].Revision = 6
			}
		})

		_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "readback revision is 6, expected 7") {
			t.Fatalf("rewritten readback revision error = %v", err)
		}
		active = false
		assertNetworkInitializeTestRollback(t, store, connection)
	})
}

// TestStoreInitializeNetworkRejectsMissingInsertedLeaseIdentity verifies endpoint writes cannot guess a foreign surrogate ID.
func TestStoreInitializeNetworkRejectsMissingInsertedLeaseIdentity(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	request := cloneInitializeNetworkRequest(networkMutationTestInitializeRequest())
	request.Endpoints[2].Identity = recordTestLeaseKeyPointer("project-alpha", "unknown")
	err := connection.Transaction(func(tx *gorm.DB) error {
		return insertNetworkInitialization(tx, request, 7)
	})
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "has no inserted address lease") {
		t.Fatalf("missing inserted lease identity error = %v", err)
	}
	if got := networkInitializeTestCount(t, connection, "network_state"); got != 0 {
		t.Fatalf("network root after missing lease identity = %d, want rollback", got)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
		t.Fatalf("high-water after direct failed insert = %d, want 6", highWater)
	}
}

// TestStoreInitializeNetworkRejectsMissingReturnedLeaseID verifies TCP joins require a confirmed generated identity.
func TestStoreInitializeNetworkRejectsMissingReturnedLeaseID(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	callback := "harbor:test_network_initialize_lease_id"
	if err := connection.Callback().Create().After("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "loopback_address_leases" {
			return
		}
		row, ok := tx.Statement.Dest.(*models.LoopbackAddressLease)
		if ok {
			row.Id = 0
		}
	}); err != nil {
		t.Fatalf("register lease ID rewrite: %v", err)
	}
	t.Cleanup(func() { _ = connection.Callback().Create().Remove(callback) })

	_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "did not return a positive database ID") {
		t.Fatalf("missing returned lease ID error = %v", err)
	}
	assertNetworkInitializeTestRollback(t, store, connection)
}

// TestValidateNetworkInitializationProjectionRejectsMismatch verifies safe readback equality has its own typed corruption boundary.
func TestValidateNetworkInitializationProjectionRejectsMismatch(t *testing.T) {
	expected := networkInitializationProjection(cloneInitializeNetworkRequest(networkMutationTestInitializeRequest()), 7)
	if err := validateNetworkInitializationProjection(expected, expected); err != nil {
		t.Fatalf("matching projection error = %v", err)
	}
	persisted := expected
	persisted.Revision = 8
	var corrupt *CorruptStateError
	if err := validateNetworkInitializationProjection(persisted, expected); !errors.As(err, &corrupt) {
		t.Fatalf("mismatched projection error = %v, want CorruptStateError", err)
	}
}

// TestNetworkInitializationDifferenceClassifiesEveryFactGroup verifies replay diagnostics remain bounded and non-secret.
func TestNetworkInitializationDifferenceClassifiesEveryFactGroup(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	request := cloneInitializeNetworkRequest(networkMutationTestInitializeRequest())
	if _, err := store.InitializeNetwork(context.Background(), request); err != nil {
		t.Fatalf("InitializeNetwork() error = %v", err)
	}
	rows, err := readNetworkModelRows(connection)
	if err != nil {
		t.Fatalf("read initialized model rows: %v", err)
	}
	if difference := networkInitializationDifference(rows, request); difference != "" {
		t.Fatalf("matching initialization difference = %q", difference)
	}

	tests := []struct {
		name   string
		want   string
		mutate func(*networkModelRows)
	}{
		{name: "root count", want: "network root", mutate: func(rows *networkModelRows) { rows.States = nil }},
		{name: "pool root", want: "network pool", mutate: func(rows *networkModelRows) { rows.States[0].DnsSuffix = ".invalid" }},
		{name: "release", want: "project release state", mutate: func(rows *networkModelRows) {
			rows.Releases = []models.NetworkProjectRelease{{Id: 1}}
		}},
		{name: "candidate count", want: "network pool candidates", mutate: func(rows *networkModelRows) {
			rows.Candidates = rows.Candidates[:len(rows.Candidates)-1]
		}},
		{name: "setup count", want: "network setup proofs", mutate: func(rows *networkModelRows) {
			rows.SetupEvidence = rows.SetupEvidence[:len(rows.SetupEvidence)-1]
		}},
		{name: "listener count", want: "network listeners", mutate: func(rows *networkModelRows) {
			rows.Listeners = rows.Listeners[:len(rows.Listeners)-1]
		}},
		{name: "lease count", want: "network lease ensures", mutate: func(rows *networkModelRows) {
			rows.Leases = rows.Leases[:len(rows.Leases)-1]
		}},
		{name: "endpoint count", want: "network endpoints", mutate: func(rows *networkModelRows) {
			rows.Endpoints = rows.Endpoints[:len(rows.Endpoints)-1]
		}},
		{name: "HTTP identity", want: "network endpoint identities", mutate: func(rows *networkModelRows) {
			for index := range rows.Endpoints {
				if rows.Endpoints[index].Protocol == "http" {
					rows.Endpoints[index].LoopbackAddressLeaseId = rows.Endpoints[2].LoopbackAddressLeaseId
					return
				}
			}
		}},
		{name: "TCP missing identity", want: "network endpoint identities", mutate: func(rows *networkModelRows) {
			for index := range rows.Endpoints {
				if rows.Endpoints[index].Protocol == "tcp" {
					rows.Endpoints[index].LoopbackAddressLeaseId.Valid = false
					return
				}
			}
		}},
		{name: "TCP wrong identity", want: "network endpoint identities", mutate: func(rows *networkModelRows) {
			for index := range rows.Endpoints {
				if rows.Endpoints[index].Protocol == "tcp" {
					rows.Endpoints[index].LoopbackAddressLeaseId.Int64 = 999
					return
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneNetworkInitializeTestRows(rows)
			test.mutate(&candidate)
			if got := networkInitializationDifference(candidate, request); got != test.want {
				t.Fatalf("networkInitializationDifference() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestNetworkMutationConflictErrorsDescribeTheirScope verifies callers can log typed conflicts without inspecting fields manually.
func TestNetworkMutationConflictErrorsDescribeTheirScope(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: &NetworkRevisionConflictError{Expected: 3, Actual: 4}, want: "network revision is 4, not expected revision 3"},
		{err: &ProjectRevisionConflictError{ProjectID: "project-alpha", Expected: 5, Actual: 6}, want: "project \"project-alpha\" revision is 6"},
		{err: &NetworkProjectSetConflictError{Expected: []domain.ProjectID{"a"}, Actual: []domain.ProjectID{"a", "b"}}, want: "network project set is [a b]"},
		{err: &NetworkInitializationConflictError{ActualRevision: 7, Difference: "network listeners"}, want: "revision 7 with different network listeners"},
	}
	for _, test := range tests {
		if got := test.err.Error(); !strings.Contains(got, test.want) {
			t.Fatalf("conflict error = %q, want containing %q", got, test.want)
		}
	}
}

// TestStoreInitializeNetworkConcurrentRetriesAllocateOnce verifies serialized equivalent callers converge on one commit.
func TestStoreInitializeNetworkConcurrentRetriesAllocateOnce(t *testing.T) {
	store, _ := newNetworkInitializeTestHarnessWithConnections(t, true, 4)
	request := networkMutationTestInitializeRequest()
	start := make(chan struct{})
	results := make(chan struct {
		result NetworkMutationResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			result, err := store.InitializeNetwork(context.Background(), request)
			results <- struct {
				result NetworkMutationResult
				err    error
			}{result: result, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent InitializeNetwork() errors = %v and %v", first.err, second.err)
	}
	if first.result.Replayed == second.result.Replayed || !reflect.DeepEqual(first.result.Record, second.result.Record) {
		t.Fatalf("concurrent results = %#v and %#v, want one apply and one replay", first.result, second.result)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 7 {
		t.Fatalf("high-water after concurrent initialization = %d, want 7", highWater)
	}
}

// TestStoreInitializeNetworkClonesQueuedRequest verifies caller mutation cannot alter a plan after validation.
func TestStoreInitializeNetworkClonesQueuedRequest(t *testing.T) {
	store, _ := newNetworkInitializeTestHarness(t, true)
	request := networkMutationTestInitializeRequest()
	wantSetupEvidence := request.Setup[0].Evidence
	wantEnsureEvidence := request.Ensures[0].EnsureEvidence
	wantHost := request.Endpoints[0].Host
	wantTCPIdentity := *request.Endpoints[2].Identity

	<-store.mutations.permit
	released := false
	t.Cleanup(func() {
		if !released {
			store.mutations.permit <- struct{}{}
		}
	})
	ctx := &networkInitializeSignalContext{Context: context.Background(), reached: make(chan struct{})}
	result := make(chan struct {
		value NetworkMutationResult
		err   error
	}, 1)
	go func() {
		value, err := store.InitializeNetwork(ctx, request)
		result <- struct {
			value NetworkMutationResult
			err   error
		}{value: value, err: err}
	}()
	<-ctx.reached

	request.ExpectedProjects[0].ProjectID = "mutated-project"
	request.Setup[0].Evidence = "mutated setup"
	request.Ensures[0].EnsureEvidence = "mutated ensure"
	request.Endpoints[0].Host = "mutated.test"
	request.Endpoints[2].Identity.ProjectID = "mutated-project"
	store.mutations.permit <- struct{}{}
	released = true

	got := <-result
	if got.err != nil {
		t.Fatalf("queued InitializeNetwork() error = %v", got.err)
	}
	if got.value.Record.Reservations.Endpoints[0].Host != wantHost ||
		*got.value.Record.Reservations.Endpoints[2].Identity != wantTCPIdentity {
		t.Fatalf("queued endpoint projection = %#v, want cloned request", got.value.Record.Reservations.Endpoints)
	}
	connection, err := store.networkState.Builder()
	if err != nil {
		t.Fatalf("open network state after queued initialization: %v", err)
	}
	var proof models.NetworkSetupEvidence
	if err := connection.Where("component = ?", NetworkSetupComponentMachineOwnership).First(&proof).Error; err != nil {
		t.Fatalf("read queued setup proof: %v", err)
	}
	var lease models.LoopbackAddressLease
	if err := connection.Where("source_project_id = ? AND secondary_id = ''", "project-alpha").First(&lease).Error; err != nil {
		t.Fatalf("read queued lease: %v", err)
	}
	if proof.Evidence != wantSetupEvidence || lease.EnsureEvidence != wantEnsureEvidence {
		t.Fatalf("queued hidden facts = setup %q, ensure %q", proof.Evidence, lease.EnsureEvidence)
	}
}

// TestStoreInitializeNetworkPreservesStorageErrorIdentity verifies transaction wrappers retain the originating error.
func TestStoreInitializeNetworkPreservesStorageErrorIdentity(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, true)
	want := errors.New("network query sentinel")
	callback := "harbor:test_network_initialize_error"
	if err := connection.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "network_state" {
			tx.AddError(want)
		}
	}); err != nil {
		t.Fatalf("register query failure: %v", err)
	}
	t.Cleanup(func() { _ = connection.Callback().Query().Remove(callback) })

	_, err := store.InitializeNetwork(context.Background(), networkMutationTestInitializeRequest())
	if !errors.Is(err, want) {
		t.Fatalf("storage error = %v, want wrapping sentinel", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
		t.Fatalf("high-water after query failure = %d, want 6", highWater)
	}
}

// networkInitializeSignalContext exposes the post-clone cancellation check as a deterministic test barrier.
type networkInitializeSignalContext struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

// Err signals that request cloning has completed before reporting the embedded context state.
func (ctx *networkInitializeSignalContext) Err() error {
	ctx.once.Do(func() { close(ctx.reached) })
	return ctx.Context.Err()
}

// newNetworkInitializeTestHarness creates a fully migrated Store with the standard two-project optimistic snapshot.
func newNetworkInitializeTestHarness(t *testing.T, projects bool) (*Store, *gorm.DB) {
	t.Helper()
	return newNetworkInitializeTestHarnessWithConnections(t, projects, 1)
}

// newNetworkInitializeTestHarnessWithConnections allows concurrency tests to open independent SQLite connections.
func newNetworkInitializeTestHarnessWithConnections(t *testing.T, projects bool, maximumConnections int) (*Store, *gorm.DB) {
	t.Helper()
	store, connection := newProjectStoreReadTestHarness(t, maximumConnections, projectStoreMutationTestClock)
	applyNetworkInitializeTestMigration(t, connection)
	if projects {
		seedSingleProjectStoreReadState(t, connection, "project-alpha", 5)
		seedSingleProjectStoreReadState(t, connection, "project-beta", 6)
	}
	return store, connection
}

// applyNetworkInitializeTestMigration applies Harbor's embedded production network migrations in durable schema order.
func applyNetworkInitializeTestMigration(t *testing.T, connection *gorm.DB) {
	t.Helper()
	for _, name := range []string{
		networkInitializeTestMigrationName,
		networkInitializeTestDigestMigrationName,
		networkInitializeTestStageMigrationName,
		networkInitializeTestOwnershipMigrationName,
		networkInitializeTestOwnershipPolicyMigrationName,
	} {
		found := false
		for _, migration := range migrations.GetMigrations() {
			if migration.Name() != name ||
				migration.App() != "harbord" ||
				migration.Connection() != "default" ||
				(migration.Driver() != "" && migration.Driver() != "sqlite") {
				continue
			}
			if err := migration.Up(connection); err != nil {
				t.Fatalf("apply embedded network migration %q: %v", name, err)
			}
			found = true
			break
		}
		if !found {
			t.Fatalf("embedded network migration %q was not registered", name)
		}
	}
	if !connection.Migrator().HasColumn("network_project_releases", "release_set_digest") {
		t.Fatal("embedded network migrations did not install release_set_digest")
	}
	if !connection.Migrator().HasColumn("network_state", "stage") {
		t.Fatal("embedded network migrations did not install network stage")
	}
	if !connection.Migrator().HasColumn("machine_ownership_projections", "network_policy_fingerprint") {
		t.Fatal("embedded network migrations did not install policy-bound ownership projections")
	}
}

// networkInitializeTestEmptyRequest returns the valid host-only initialization shape used before projects exist.
func networkInitializeTestEmptyRequest() InitializeNetworkRequest {
	request := networkMutationTestInitializeRequest()
	request.ExpectedProjects = []NetworkProjectRevision{}
	request.Ensures = []NetworkLeaseEnsure{}
	request.Endpoints = []EndpointReservation{}
	return request
}

// networkInitializeTestCount returns one table's row count without hiding query failures.
func networkInitializeTestCount(t *testing.T, connection *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := connection.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// cloneNetworkInitializeTestRows isolates direct replay-difference mutations from the shared valid fixture.
func cloneNetworkInitializeTestRows(rows networkModelRows) networkModelRows {
	rows.States = append([]models.NetworkState(nil), rows.States...)
	rows.Candidates = append([]models.NetworkPoolCandidate(nil), rows.Candidates...)
	rows.SetupEvidence = append([]models.NetworkSetupEvidence(nil), rows.SetupEvidence...)
	rows.Listeners = append([]models.NetworkSharedListener(nil), rows.Listeners...)
	rows.Leases = append([]models.LoopbackAddressLease(nil), rows.Leases...)
	rows.Endpoints = append([]models.PublicEndpointLease(nil), rows.Endpoints...)
	rows.Releases = append([]models.NetworkProjectRelease(nil), rows.Releases...)
	rows.Projects = append([]models.Project(nil), rows.Projects...)
	rows.ReleaseOwners = append([]models.Operation(nil), rows.ReleaseOwners...)
	return rows
}

// registerNetworkInitializeReadbackCallbacks activates one query mutation only after endpoint insertion completes.
func registerNetworkInitializeReadbackCallbacks(
	t *testing.T,
	connection *gorm.DB,
	active *bool,
	readback func(*gorm.DB),
) {
	t.Helper()
	createCallback := "harbor:test_network_initialize_readback_active"
	queryCallback := "harbor:test_network_initialize_readback_query"
	if err := connection.Callback().Create().After("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if tx.Statement.Table == "public_endpoint_leases" {
			*active = true
		}
	}); err != nil {
		t.Fatalf("register readback activation: %v", err)
	}
	if err := connection.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if *active {
			readback(tx)
		}
	}); err != nil {
		t.Fatalf("register readback query mutation: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Callback().Create().Remove(createCallback)
		_ = connection.Callback().Query().Remove(queryCallback)
	})
}

// hideNetworkInitializeReadRows simulates a driver that loses the complete aggregate during late readback.
func hideNetworkInitializeReadRows(tx *gorm.DB) {
	switch rows := tx.Statement.Dest.(type) {
	case *[]models.NetworkState:
		*rows = nil
	case *[]models.NetworkPoolCandidate:
		*rows = nil
	case *[]models.NetworkSetupEvidence:
		*rows = nil
	case *[]models.NetworkSharedListener:
		*rows = nil
	case *[]models.LoopbackAddressLease:
		*rows = nil
	case *[]models.PublicEndpointLease:
		*rows = nil
	case *[]models.NetworkProjectRelease:
		*rows = nil
	}
}

// assertNetworkInitializeTestRollback proves late transaction failures restore both global and aggregate state.
func assertNetworkInitializeTestRollback(t *testing.T, store *Store, connection *gorm.DB) {
	t.Helper()
	if highWater := projectStoreMutationSequence(t, store); highWater != 6 {
		t.Fatalf("high-water after failed network initialization = %d, want 6", highWater)
	}
	for _, table := range networkTableNames() {
		if got := networkInitializeTestCount(t, connection, table); got != 0 {
			t.Fatalf("%s count after failed network initialization = %d, want 0", table, got)
		}
	}
}
