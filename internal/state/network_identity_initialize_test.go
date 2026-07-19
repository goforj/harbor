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
)

// TestStoreInitializeNetworkIdentityCommitsOnlyFoundationAuthority verifies identity setup cannot fabricate leases, endpoints, or shared listeners.
func TestStoreInitializeNetworkIdentityCommitsOnlyFoundationAuthority(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	request := networkIdentityInitializeTestRequest()

	result, err := store.InitializeNetworkIdentity(context.Background(), request)
	if err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	if result.Replayed || result.Record.Revision != 1 || result.Record.Stage != NetworkStageIdentity {
		t.Fatalf("InitializeNetworkIdentity() result = %#v, want applied identity revision 1", result)
	}
	if len(result.Record.Leases) != 0 || len(result.Record.Quarantines) != 0 ||
		len(result.Record.Reservations.Endpoints) != 0 ||
		result.Record.Reservations.Listeners != (SharedListenerReservations{}) {
		t.Fatalf("identity initialization projected data-plane authority: %#v", result.Record)
	}
	for table, want := range map[string]int64{
		"network_state":            1,
		"network_pool_candidates":  3,
		"network_setup_evidence":   2,
		"network_shared_listeners": 0,
		"loopback_address_leases":  0,
		"public_endpoint_leases":   0,
		"network_project_releases": 0,
	} {
		if got := networkInitializeTestCount(t, connection, table); got != want {
			t.Fatalf("%s row count = %d, want %d", table, got, want)
		}
	}
	var stage string
	if err := connection.Raw("SELECT stage FROM network_state WHERE id = 1").Scan(&stage).Error; err != nil {
		t.Fatalf("read initialized network stage: %v", err)
	}
	if stage != string(NetworkStageIdentity) {
		t.Fatalf("persisted network stage = %q, want identity", stage)
	}

	runtimeState, err := store.RuntimeState(context.Background())
	if err != nil {
		t.Fatalf("RuntimeState() identity foundation error = %v", err)
	}
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != NetworkStageIdentity {
		t.Fatalf("RuntimeState() network = initialized %t, stage %q", runtimeState.NetworkInitialized, runtimeState.Network.Stage)
	}
}

// TestStoreInitializeNetworkIdentityReplaysExactFoundationWithoutProjectCoupling verifies retries do not consume ordering or depend on later project revisions.
func TestStoreInitializeNetworkIdentityReplaysExactFoundationWithoutProjectCoupling(t *testing.T) {
	store, _ := newNetworkInitializeTestHarness(t, false)
	request := networkIdentityInitializeTestRequest()
	first, err := store.InitializeNetworkIdentity(context.Background(), request)
	if err != nil {
		t.Fatalf("initial InitializeNetworkIdentity() error = %v", err)
	}

	project := emptyProjectStoreMutationProject("project-alpha")
	project.Name = "Registered after identity setup"
	advanced, err := store.PutProject(context.Background(), project)
	if err != nil {
		t.Fatalf("advance project after identity setup: %v", err)
	}
	if advanced.Revision != 2 {
		t.Fatalf("registered project revision = %d, want 2", advanced.Revision)
	}

	replayed, err := store.InitializeNetworkIdentity(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed InitializeNetworkIdentity() error = %v", err)
	}
	if !replayed.Replayed || !reflect.DeepEqual(replayed.Record, first.Record) {
		t.Fatalf("replayed identity result = %#v, want original record marked replayed", replayed)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 2 {
		t.Fatalf("Harbor high-water after replay = %d, want 2", highWater)
	}
}

// TestStoreInitializeNetworkIdentityRejectsSemanticAndStageConflicts verifies retries cannot narrow or alter existing authority.
func TestStoreInitializeNetworkIdentityRejectsSemanticAndStageConflicts(t *testing.T) {
	tests := []struct {
		name       string
		difference string
		mutate     func(*InitializeNetworkIdentityRequest)
	}{
		{name: "ownership", difference: "network ownership", mutate: func(request *InitializeNetworkIdentityRequest) {
			request.Ownership.Generation++
		}},
		{name: "pool generation", difference: "network pool candidates", mutate: func(request *InitializeNetworkIdentityRequest) {
			request.PoolGeneration++
		}},
		{name: "setup proof", difference: "network setup proofs", mutate: func(request *InitializeNetworkIdentityRequest) {
			request.Setup[0].Evidence = "different owner proof"
		}},
		{name: "root time", difference: "network root timestamps", mutate: func(request *InitializeNetworkIdentityRequest) {
			request.At = request.At.Add(time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newNetworkInitializeTestHarness(t, false)
			request := networkIdentityInitializeTestRequest()
			if _, err := store.InitializeNetworkIdentity(context.Background(), request); err != nil {
				t.Fatalf("initial InitializeNetworkIdentity() error = %v", err)
			}
			test.mutate(&request)

			_, err := store.InitializeNetworkIdentity(context.Background(), request)
			var conflict *NetworkInitializationConflictError
			if !errors.As(err, &conflict) || conflict.ActualRevision != 1 || conflict.Difference != test.difference {
				t.Fatalf("semantic retry error = %v, want revision 1 %q conflict", err, test.difference)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 1 {
				t.Fatalf("Harbor high-water after conflict = %d, want 1", highWater)
			}
		})
	}

	t.Run("full aggregate", func(t *testing.T) {
		store, _ := newNetworkInitializeTestHarness(t, false)
		full := networkInitializeTestEmptyRequest()
		if _, err := store.InitializeNetwork(context.Background(), full); err != nil {
			t.Fatalf("InitializeNetwork() error = %v", err)
		}
		request := networkIdentityInitializeTestRequest()
		_, err := store.InitializeNetworkIdentity(context.Background(), request)
		var conflict *NetworkInitializationConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "network stage" {
			t.Fatalf("full-stage identity retry error = %v, want network stage conflict", err)
		}
	})

	t.Run("identity aggregate", func(t *testing.T) {
		store, _ := newNetworkInitializeTestHarness(t, false)
		identityRequest := networkIdentityInitializeTestRequest()
		if _, err := store.InitializeNetworkIdentity(context.Background(), identityRequest); err != nil {
			t.Fatalf("InitializeNetworkIdentity() error = %v", err)
		}
		_, err := store.InitializeNetwork(context.Background(), networkInitializeTestEmptyRequest())
		var conflict *NetworkInitializationConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "network stage" {
			t.Fatalf("identity-stage full retry error = %v, want network stage conflict", err)
		}
	})
}

// TestStoreInitializeNetworkIdentityConcurrentRetriesAllocateOnce verifies equivalent first-run callers converge on one durable revision.
func TestStoreInitializeNetworkIdentityConcurrentRetriesAllocateOnce(t *testing.T) {
	store, _ := newNetworkInitializeTestHarnessWithConnections(t, false, 4)
	request := networkIdentityInitializeTestRequest()
	type result struct {
		value NetworkMutationResult
		err   error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			start.Done()
			start.Wait()
			value, err := store.InitializeNetworkIdentity(context.Background(), request)
			results <- result{value: value, err: err}
		}()
	}
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent InitializeNetworkIdentity() errors = %v and %v", first.err, second.err)
	}
	if first.value.Record.Revision != 1 || second.value.Record.Revision != 1 || first.value.Replayed == second.value.Replayed {
		t.Fatalf("concurrent results = %#v and %#v, want one write and one replay at revision 1", first.value, second.value)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 1 {
		t.Fatalf("Harbor high-water after concurrent retries = %d, want 1", highWater)
	}
}

// TestStoreInitializeNetworkIdentityRollsBackEveryFoundationWrite verifies partial ownership cannot survive a failed transaction.
func TestStoreInitializeNetworkIdentityRollsBackEveryFoundationWrite(t *testing.T) {
	for _, test := range []struct {
		name  string
		table string
	}{
		{name: "root", table: "network_state"},
		{name: "candidate", table: "network_pool_candidates"},
		{name: "setup", table: "network_setup_evidence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newNetworkInitializeTestHarness(t, false)
			trigger := fmt.Sprintf(
				"CREATE TRIGGER fail_network_identity_initialize BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'network identity %s failure'); END",
				test.table,
				test.name,
			)
			mustProjectStoreReadExec(t, connection, trigger)

			_, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest())
			if err == nil || !strings.Contains(err.Error(), "network identity "+test.name+" failure") {
				t.Fatalf("%s failure error = %v", test.name, err)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 0 {
				t.Fatalf("Harbor high-water after %s failure = %d, want 0", test.name, highWater)
			}
			for _, table := range networkTableNames() {
				if got := networkInitializeTestCount(t, connection, table); got != 0 {
					t.Fatalf("%s count after %s failure = %d, want 0", table, test.name, got)
				}
			}
		})
	}
}

// TestInitializeNetworkIdentityRequestRejectsDataPlaneProofs verifies identity setup cannot smuggle later-stage authority into persistence.
func TestInitializeNetworkIdentityRequestRejectsDataPlaneProofs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*InitializeNetworkIdentityRequest)
	}{
		{name: "nonzero revision", mutate: func(request *InitializeNetworkIdentityRequest) { request.ExpectedNetworkRevision = 1 }},
		{name: "nil proofs", mutate: func(request *InitializeNetworkIdentityRequest) { request.Setup = nil }},
		{name: "resolver proof", mutate: func(request *InitializeNetworkIdentityRequest) {
			request.Setup = append(request.Setup, networkMutationTestSetupProof(NetworkSetupComponentResolver))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := networkIdentityInitializeTestRequest()
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("InitializeNetworkIdentityRequest.Validate() accepted invalid identity plan")
			}
		})
	}
}

// networkIdentityInitializeTestRequest returns the valid two-proof identity foundation used by Store tests.
func networkIdentityInitializeTestRequest() InitializeNetworkIdentityRequest {
	full := networkMutationTestInitializeRequest()
	return InitializeNetworkIdentityRequest{
		ExpectedNetworkRevision: 0,
		Ownership:               full.Ownership,
		Pool:                    full.Pool,
		PoolGeneration:          full.PoolGeneration,
		Setup: []NetworkSetupProof{
			full.Setup[0],
			full.Setup[1],
		},
		At: full.At,
	}
}
