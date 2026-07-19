package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// TestActivateNetworkDataPlaneRequestRejectsIncompleteAuthority covers the exact two-proof activation contract.
func TestActivateNetworkDataPlaneRequestRejectsIncompleteAuthority(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*ActivateNetworkDataPlaneRequest)
	}{
		{name: "zero revision", want: "network revision must be positive", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.ExpectedNetworkRevision = 0
		}},
		{name: "nil proofs", want: "setup proofs must be initialized", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Setup = nil
		}},
		{name: "missing proof", want: "expected 2", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Setup = request.Setup[:1]
		}},
		{name: "identity proof", want: "expected \"resolver\"", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Setup[0] = networkMutationTestSetupProof(NetworkSetupComponentMachineOwnership)
		}},
		{name: "reversed proofs", want: "expected \"resolver\"", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Setup[0], request.Setup[1] = request.Setup[1], request.Setup[0]
		}},
		{name: "invalid proof", want: "generation must be positive", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Setup[0].Generation = 0
		}},
		{name: "future proof", want: "must not be after the network mutation time", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Setup[0].VerifiedAt = request.At.Add(time.Second)
		}},
		{name: "invalid listener", want: "generation must be positive", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Listeners.DNS.Generation = 0
		}},
		{name: "future listener", want: "must not be after the network mutation time", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Listeners.HTTPS.VerifiedAt = request.At.Add(time.Second)
		}},
		{name: "zero time", want: "activation time", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.At = time.Time{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := networkDataPlaneActivationTestRequest(1)
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ActivateNetworkDataPlaneRequest.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestStoreActivateNetworkDataPlaneCommitsOnlyAdditionalAuthority verifies identity becomes full in one global revision.
func TestStoreActivateNetworkDataPlaneCommitsOnlyAdditionalAuthority(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	identityRequest := networkIdentityInitializeTestRequest()
	identityResult, err := store.InitializeNetworkIdentity(context.Background(), identityRequest)
	if err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	before := networkDataPlaneActivationTestRows(t, connection)
	request := networkDataPlaneActivationTestRequest(identityResult.Record.Revision)

	result, err := store.ActivateNetworkDataPlane(context.Background(), request)
	if err != nil {
		t.Fatalf("ActivateNetworkDataPlane() error = %v", err)
	}
	if result.Replayed || result.Record.Stage != NetworkStageFull || result.Record.Revision != 2 {
		t.Fatalf("ActivateNetworkDataPlane() result = %#v, want applied full revision 2", result)
	}
	if !result.Record.CreatedAt.Equal(identityRequest.At) || !result.Record.UpdatedAt.Equal(request.At) ||
		result.Record.Ownership != identityResult.Record.Ownership ||
		!reflect.DeepEqual(result.Record.Pool, identityResult.Record.Pool) {
		t.Fatalf("activated root identity = %#v, want preserved %#v", result.Record, identityResult.Record)
	}
	if result.Record.Reservations.Listeners != request.Listeners ||
		len(result.Record.Reservations.Endpoints) != 0 ||
		len(result.Record.Leases) != 0 || len(result.Record.Quarantines) != 0 {
		t.Fatalf("activated projection = %#v, want listeners without fabricated project authority", result.Record)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("NetworkMutationResult.Validate() error = %v", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 2 {
		t.Fatalf("Harbor high-water after activation = %d, want 2", highWater)
	}
	for table, want := range map[string]int64{
		"network_state":            1,
		"network_pool_candidates":  3,
		"network_setup_evidence":   4,
		"network_shared_listeners": 3,
		"loopback_address_leases":  0,
		"public_endpoint_leases":   0,
		"network_project_releases": 0,
	} {
		if got := networkInitializeTestCount(t, connection, table); got != want {
			t.Fatalf("%s count after activation = %d, want %d", table, got, want)
		}
	}
	after := networkDataPlaneActivationTestRows(t, connection)
	if !reflect.DeepEqual(after.Candidates, before.Candidates) {
		t.Fatal("activation changed pool candidate rows")
	}
	for _, component := range []NetworkSetupComponent{
		NetworkSetupComponentMachineOwnership,
		NetworkSetupComponentLoopbackPool,
	} {
		if !reflect.DeepEqual(
			networkDataPlaneActivationTestProof(before.SetupEvidence, component),
			networkDataPlaneActivationTestProof(after.SetupEvidence, component),
		) {
			t.Fatalf("activation changed %q identity proof", component)
		}
	}
	read, initialized, err := store.Network(context.Background())
	if err != nil || !initialized || !reflect.DeepEqual(read, result.Record) {
		t.Fatalf("Network() = %#v, %t, %v; want activation result", read, initialized, err)
	}
}

// TestStoreActivateNetworkDataPlaneRejectsUninitializedState verifies activation cannot create the identity foundation implicitly.
func TestStoreActivateNetworkDataPlaneRejectsUninitializedState(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)

	_, err := store.ActivateNetworkDataPlane(context.Background(), networkDataPlaneActivationTestRequest(1))
	var missing *NetworkNotInitializedError
	if !errors.As(err, &missing) {
		t.Fatalf("uninitialized activation error = %v, want NetworkNotInitializedError", err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 0 {
		t.Fatalf("Harbor high-water after uninitialized activation = %d, want 0", highWater)
	}
	for _, table := range networkTableNames() {
		if got := networkInitializeTestCount(t, connection, table); got != 0 {
			t.Fatalf("%s count after uninitialized activation = %d, want 0", table, got)
		}
	}
}

// TestStoreActivateNetworkDataPlanePreservesLifecycleState verifies activation retains leases, quarantine, releases, and suppression.
func TestStoreActivateNetworkDataPlanePreservesLifecycleState(t *testing.T) {
	store, connection, _, _, begin, initialization := newNetworkReleaseTestHarness(t, 1)
	staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
	if err != nil {
		t.Fatalf("BeginProjectNetworkRelease() error = %v", err)
	}
	completion := networkReleaseTestCompleteRequest(begin, staged.Release)
	completed, err := store.CompleteProjectNetworkRelease(context.Background(), completion)
	if err != nil {
		t.Fatalf("CompleteProjectNetworkRelease() error = %v", err)
	}

	// The fixture models a valid identity-stage restart after project lifecycle facts already exist.
	mustProjectStoreReadExec(t, connection, "DELETE FROM public_endpoint_leases")
	mustProjectStoreReadExec(t, connection, "DELETE FROM network_shared_listeners")
	mustProjectStoreReadExec(
		t,
		connection,
		"DELETE FROM network_setup_evidence WHERE component IN ('resolver', 'low_ports')",
	)
	mustProjectStoreReadExec(t, connection, "UPDATE network_state SET stage = 'identity' WHERE id = 1")
	identityRecord, initialized, err := store.Network(context.Background())
	if err != nil || !initialized || identityRecord.Stage != NetworkStageIdentity {
		t.Fatalf("identity lifecycle fixture = %#v, %t, %v", identityRecord, initialized, err)
	}
	if len(identityRecord.Leases) == 0 || len(identityRecord.Quarantines) == 0 ||
		len(identityRecord.Reservations.SuppressedProjectIDs) == 0 {
		t.Fatalf("identity lifecycle fixture lacks preservation facts: %#v", identityRecord)
	}
	before := networkDataPlaneActivationTestRows(t, connection)
	request := ActivateNetworkDataPlaneRequest{
		ExpectedNetworkRevision: completed.Record.Revision,
		Setup: []NetworkSetupProof{
			initialization.Setup[2],
			initialization.Setup[3],
		},
		Listeners: initialization.Listeners,
		At:        completion.At.Add(time.Minute),
	}

	result, err := store.ActivateNetworkDataPlane(context.Background(), request)
	if err != nil {
		t.Fatalf("ActivateNetworkDataPlane() lifecycle error = %v", err)
	}
	if result.Record.Revision != completed.Record.Revision+1 || result.Record.Stage != NetworkStageFull {
		t.Fatalf("lifecycle activation result = %#v", result)
	}
	if !reflect.DeepEqual(result.Record.Leases, identityRecord.Leases) ||
		!reflect.DeepEqual(result.Record.Quarantines, identityRecord.Quarantines) ||
		!reflect.DeepEqual(
			result.Record.Reservations.SuppressedProjectIDs,
			identityRecord.Reservations.SuppressedProjectIDs,
		) {
		t.Fatalf("activation changed lifecycle projection: before %#v after %#v", identityRecord, result.Record)
	}
	after := networkDataPlaneActivationTestRows(t, connection)
	for _, rows := range []struct {
		name   string
		before any
		after  any
	}{
		{name: "candidates", before: before.Candidates, after: after.Candidates},
		{name: "leases and quarantines", before: before.Leases, after: after.Leases},
		{name: "release and suppression facts", before: before.Releases, after: after.Releases},
		{name: "projects", before: before.Projects, after: after.Projects},
		{name: "release owners", before: before.ReleaseOwners, after: after.ReleaseOwners},
	} {
		if !reflect.DeepEqual(rows.before, rows.after) {
			t.Fatalf("activation changed %s", rows.name)
		}
	}
}

// TestStoreActivateNetworkDataPlaneReplaysExactDurableFacts verifies replay ignores non-durable call time and consumes no sequence.
func TestStoreActivateNetworkDataPlaneReplaysExactDurableFacts(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	request := networkDataPlaneActivationTestRequest(1)
	first, err := store.ActivateNetworkDataPlane(context.Background(), request)
	if err != nil {
		t.Fatalf("first ActivateNetworkDataPlane() error = %v", err)
	}
	before := networkDataPlaneActivationTestRows(t, connection)
	request.At = request.At.Add(time.Hour)

	replayed, err := store.ActivateNetworkDataPlane(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed ActivateNetworkDataPlane() error = %v", err)
	}
	if !replayed.Replayed || !reflect.DeepEqual(replayed.Record, first.Record) {
		t.Fatalf("replayed result = %#v, want unchanged first result", replayed)
	}
	if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatal("exact activation replay changed durable rows")
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 2 {
		t.Fatalf("Harbor high-water after replay = %d, want 2", highWater)
	}

	request.ExpectedNetworkRevision = 3
	_, err = store.ActivateNetworkDataPlane(context.Background(), request)
	var conflict *NetworkRevisionConflictError
	if !errors.As(err, &conflict) || conflict.Expected != 3 || conflict.Actual != 2 {
		t.Fatalf("future replay error = %v, want typed revision 3/2 conflict", err)
	}
}

// TestStoreActivateNetworkDataPlaneReturnsTypedConflicts verifies stale identity and divergent full-stage facts are distinguishable.
func TestStoreActivateNetworkDataPlaneReturnsTypedConflicts(t *testing.T) {
	t.Run("stale identity revision", func(t *testing.T) {
		store, _ := newNetworkInitializeTestHarness(t, false)
		if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
			t.Fatalf("InitializeNetworkIdentity() error = %v", err)
		}
		_, err := store.ActivateNetworkDataPlane(context.Background(), networkDataPlaneActivationTestRequest(2))
		var conflict *NetworkRevisionConflictError
		if !errors.As(err, &conflict) || conflict.Expected != 2 || conflict.Actual != 1 {
			t.Fatalf("stale identity error = %v, want typed revision 2/1 conflict", err)
		}
	})

	t.Run("activation time", func(t *testing.T) {
		store, _ := newNetworkInitializeTestHarness(t, false)
		identityRequest := networkIdentityInitializeTestRequest()
		if _, err := store.InitializeNetworkIdentity(context.Background(), identityRequest); err != nil {
			t.Fatalf("InitializeNetworkIdentity() error = %v", err)
		}
		request := networkDataPlaneActivationTestRequest(1)
		request.At = identityRequest.At.Add(-time.Second)
		for index := range request.Setup {
			request.Setup[index].VerifiedAt = request.At.Add(-time.Second)
		}
		request.Listeners.DNS.VerifiedAt = request.At.Add(-time.Second)
		request.Listeners.HTTP.VerifiedAt = request.At.Add(-time.Second)
		request.Listeners.HTTPS.VerifiedAt = request.At.Add(-time.Second)
		_, err := store.ActivateNetworkDataPlane(context.Background(), request)
		var conflict *NetworkDataPlaneActivationConflictError
		if !errors.As(err, &conflict) || conflict.ActualRevision != 1 || conflict.Difference != "activation time" {
			t.Fatalf("activation time error = %v, want typed activation conflict", err)
		}
	})

	for _, test := range []struct {
		name       string
		difference string
		mutate     func(*ActivateNetworkDataPlaneRequest)
	}{
		{name: "proof", difference: "network setup proofs", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Setup[0].Evidence = "different resolver proof"
		}},
		{name: "listener", difference: "network listeners", mutate: func(request *ActivateNetworkDataPlaneRequest) {
			request.Listeners.DNS.Generation++
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newNetworkInitializeTestHarness(t, false)
			if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
				t.Fatalf("InitializeNetworkIdentity() error = %v", err)
			}
			request := networkDataPlaneActivationTestRequest(1)
			if _, err := store.ActivateNetworkDataPlane(context.Background(), request); err != nil {
				t.Fatalf("seed ActivateNetworkDataPlane() error = %v", err)
			}
			test.mutate(&request)
			_, err := store.ActivateNetworkDataPlane(context.Background(), request)
			var conflict *NetworkDataPlaneActivationConflictError
			if !errors.As(err, &conflict) || conflict.ActualRevision != 2 || conflict.Difference != test.difference {
				t.Fatalf("divergent replay error = %v, want revision 2 %q conflict", err, test.difference)
			}
		})
	}
}

// TestStoreActivateNetworkDataPlaneRejectsIdentityEndpoints verifies no endpoint can cross the lifecycle boundary implicitly.
func TestStoreActivateNetworkDataPlaneRejectsIdentityEndpoints(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	identityRequest := networkIdentityInitializeTestRequest()
	if _, err := store.InitializeNetworkIdentity(context.Background(), identityRequest); err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	project := emptyProjectStoreMutationProject("project-alpha")
	if _, err := store.PutProject(context.Background(), project); err != nil {
		t.Fatalf("PutProject() error = %v", err)
	}
	endpoint := models.PublicEndpointLease{
		NetworkStateId: networkStateSingletonID,
		ProjectId:      "project-alpha",
		EndpointId:     "web",
		Protocol:       string(EndpointProtocolHTTP),
		Hostname:       "alpha.test",
		Address:        "127.0.0.1",
		Port:           443,
		Generation:     1,
		CreatedAt:      identityRequest.At,
		UpdatedAt:      identityRequest.At,
	}
	if err := connection.Create(&endpoint).Error; err != nil {
		t.Fatalf("seed forbidden identity endpoint: %v", err)
	}

	_, err := store.ActivateNetworkDataPlane(context.Background(), networkDataPlaneActivationTestRequest(1))
	if err == nil || !strings.Contains(err.Error(), "identity-stage network must not contain endpoint reservations") {
		t.Fatalf("identity endpoint error = %v", err)
	}
	if networkInitializeTestCount(t, connection, "network_setup_evidence") != 2 ||
		networkInitializeTestCount(t, connection, "network_shared_listeners") != 0 {
		t.Fatal("rejected identity endpoint changed activation rows")
	}
}

// TestStoreActivateNetworkDataPlaneClonesQueuedProofs verifies caller mutation cannot alter validated hidden facts.
func TestStoreActivateNetworkDataPlaneClonesQueuedProofs(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	request := networkDataPlaneActivationTestRequest(1)
	wantEvidence := request.Setup[0].Evidence

	<-store.mutations.permit
	released := false
	t.Cleanup(func() {
		if !released {
			store.mutations.permit <- struct{}{}
		}
	})
	ctx := &networkInitializeSignalContext{Context: context.Background(), reached: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := store.ActivateNetworkDataPlane(ctx, request)
		result <- err
	}()
	<-ctx.reached
	request.Setup[0].Evidence = "caller-mutated resolver proof"
	store.mutations.permit <- struct{}{}
	released = true
	if err := <-result; err != nil {
		t.Fatalf("queued ActivateNetworkDataPlane() error = %v", err)
	}
	var proof models.NetworkSetupEvidence
	if err := connection.Where("component = ?", NetworkSetupComponentResolver).First(&proof).Error; err != nil {
		t.Fatalf("read cloned resolver proof: %v", err)
	}
	if proof.Evidence != wantEvidence {
		t.Fatalf("persisted resolver evidence = %q, want cloned %q", proof.Evidence, wantEvidence)
	}
}

// TestStoreActivateNetworkDataPlaneHonorsCancellation verifies canceled calls cannot enter or survive the queued writer boundary.
func TestStoreActivateNetworkDataPlaneHonorsCancellation(t *testing.T) {
	t.Run("before mutation", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
			t.Fatalf("InitializeNetworkIdentity() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := store.ActivateNetworkDataPlane(ctx, networkDataPlaneActivationTestRequest(1))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-canceled activation error = %v", err)
		}
		if networkInitializeTestCount(t, connection, "network_setup_evidence") != 2 {
			t.Fatal("pre-canceled activation changed setup rows")
		}
	})

	t.Run("while queued", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
			t.Fatalf("InitializeNetworkIdentity() error = %v", err)
		}
		before := networkDataPlaneActivationTestRows(t, connection)
		<-store.mutations.permit
		released := false
		t.Cleanup(func() {
			if !released {
				store.mutations.permit <- struct{}{}
			}
		})
		base, cancel := context.WithCancel(context.Background())
		ctx := &networkInitializeSignalContext{Context: base, reached: make(chan struct{})}
		result := make(chan error, 1)
		go func() {
			_, err := store.ActivateNetworkDataPlane(ctx, networkDataPlaneActivationTestRequest(1))
			result <- err
		}()
		<-ctx.reached
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation error = %v", err)
		}
		store.mutations.permit <- struct{}{}
		released = true
		if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("queued cancellation changed network rows")
		}
	})
}

// TestStoreActivateNetworkDataPlaneRollsBackWriteFaults verifies each write phase is atomic with the global sequence.
func TestStoreActivateNetworkDataPlaneRollsBackWriteFaults(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{
			name:    "setup proof",
			trigger: "CREATE TRIGGER fail_network_activation BEFORE INSERT ON network_setup_evidence WHEN NEW.component = 'resolver' BEGIN SELECT RAISE(ABORT, 'activation setup failure'); END",
		},
		{
			name:    "listener",
			trigger: "CREATE TRIGGER fail_network_activation BEFORE INSERT ON network_shared_listeners WHEN NEW.kind = 'https' BEGIN SELECT RAISE(ABORT, 'activation listener failure'); END",
		},
		{
			name:    "root update",
			trigger: "CREATE TRIGGER fail_network_activation BEFORE UPDATE ON network_state WHEN NEW.stage = 'full' BEGIN SELECT RAISE(ABORT, 'activation root failure'); END",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newNetworkInitializeTestHarness(t, false)
			if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
				t.Fatalf("InitializeNetworkIdentity() error = %v", err)
			}
			before := networkDataPlaneActivationTestRows(t, connection)
			mustProjectStoreReadExec(t, connection, test.trigger)

			_, err := store.ActivateNetworkDataPlane(context.Background(), networkDataPlaneActivationTestRequest(1))
			if err == nil || !strings.Contains(err.Error(), "activation ") {
				t.Fatalf("%s fault error = %v", test.name, err)
			}
			if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s fault left partial network rows", test.name)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 1 {
				t.Fatalf("Harbor high-water after %s fault = %d, want 1", test.name, highWater)
			}
		})
	}
}

// TestStoreActivateNetworkDataPlaneRollsBackReadbackCorruption verifies late verification failures restore identity state.
func TestStoreActivateNetworkDataPlaneRollsBackReadbackCorruption(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	before := networkDataPlaneActivationTestRows(t, connection)
	active := false
	updateCallback := "harbor:test_network_activation_readback_active"
	queryCallback := "harbor:test_network_activation_readback_corruption"
	if err := connection.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Table == "network_state" {
			active = true
		}
	}); err != nil {
		t.Fatalf("register activation update callback: %v", err)
	}
	if err := connection.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if !active {
			return
		}
		if rows, ok := tx.Statement.Dest.(*[]models.NetworkState); ok && len(*rows) != 0 {
			(*rows)[0].Stage = string(NetworkStageIdentity)
		}
	}); err != nil {
		t.Fatalf("register activation query callback: %v", err)
	}

	_, err := store.ActivateNetworkDataPlane(context.Background(), networkDataPlaneActivationTestRequest(1))
	_ = connection.Callback().Update().Remove(updateCallback)
	_ = connection.Callback().Query().Remove(queryCallback)
	if err == nil || !strings.Contains(err.Error(), "component \"low_ports\" is unsupported") {
		t.Fatalf("readback corruption error = %v", err)
	}
	if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatal("readback corruption left partial activation rows")
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 1 {
		t.Fatalf("Harbor high-water after readback corruption = %d, want 1", highWater)
	}
}

// TestStoreActivateNetworkDataPlaneRejectsAllocatedSequenceCollision verifies the new revision remains globally exclusive.
func TestStoreActivateNetworkDataPlaneRejectsAllocatedSequenceCollision(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	project := emptyProjectStoreMutationProject("project-alpha")
	projectResult, err := store.PutProject(context.Background(), project)
	if err != nil || projectResult.Revision != 2 {
		t.Fatalf("PutProject() = %#v, %v; want revision 2", projectResult, err)
	}
	before := networkDataPlaneActivationTestRows(t, connection)
	callback := "harbor:test_network_activation_sequence_collision"
	if err := connection.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "network_state" {
			return
		}
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec(
			"UPDATE projects SET revision = (SELECT revision FROM network_state WHERE id = 1) WHERE project_id = 'project-alpha'",
		).Error)
	}); err != nil {
		t.Fatalf("register sequence collision callback: %v", err)
	}

	_, err = store.ActivateNetworkDataPlane(context.Background(), networkDataPlaneActivationTestRequest(1))
	_ = connection.Callback().Update().Remove(callback)
	if err == nil || !strings.Contains(err.Error(), "sequence") || !strings.Contains(err.Error(), "network state") {
		t.Fatalf("sequence collision error = %v", err)
	}
	if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatal("sequence collision left partial activation rows")
	}
	readProject, err := store.Project(context.Background(), "project-alpha")
	if err != nil || readProject.Revision != 2 {
		t.Fatalf("project after collision = %#v, %v; want revision 2", readProject, err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 2 {
		t.Fatalf("Harbor high-water after collision = %d, want 2", highWater)
	}
}

// TestStoreActivateNetworkDataPlaneConcurrentRetriesAllocateOnce verifies serialized equal callers converge on one activation.
func TestStoreActivateNetworkDataPlaneConcurrentRetriesAllocateOnce(t *testing.T) {
	store, _ := newNetworkInitializeTestHarnessWithConnections(t, false, 4)
	if _, err := store.InitializeNetworkIdentity(context.Background(), networkIdentityInitializeTestRequest()); err != nil {
		t.Fatalf("InitializeNetworkIdentity() error = %v", err)
	}
	request := networkDataPlaneActivationTestRequest(1)
	start := make(chan struct{})
	results := make(chan struct {
		result NetworkMutationResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			result, err := store.ActivateNetworkDataPlane(context.Background(), request)
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
		t.Fatalf("concurrent activation errors = %v and %v", first.err, second.err)
	}
	if first.result.Replayed == second.result.Replayed || !reflect.DeepEqual(first.result.Record, second.result.Record) {
		t.Fatalf("concurrent activation results = %#v and %#v", first.result, second.result)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 2 {
		t.Fatalf("Harbor high-water after concurrent activation = %d, want 2", highWater)
	}
}

// TestNetworkDataPlaneActivationConflictErrorDescribesScope verifies typed conflict diagnostics omit proof contents.
func TestNetworkDataPlaneActivationConflictErrorDescribesScope(t *testing.T) {
	err := &NetworkDataPlaneActivationConflictError{ActualRevision: 9, Difference: "network listeners"}
	if got := err.Error(); got != "network data plane is already active at revision 9 with different network listeners" {
		t.Fatalf("NetworkDataPlaneActivationConflictError.Error() = %q", got)
	}
}

// networkDataPlaneActivationTestRequest returns the exact resolver, low-port, and listener facts used by activation tests.
func networkDataPlaneActivationTestRequest(expected domain.Sequence) ActivateNetworkDataPlaneRequest {
	full := networkMutationTestInitializeRequest()
	return ActivateNetworkDataPlaneRequest{
		ExpectedNetworkRevision: expected,
		Setup: []NetworkSetupProof{
			full.Setup[2],
			full.Setup[3],
		},
		Listeners: full.Listeners,
		At:        full.At.Add(time.Minute),
	}
}

// networkDataPlaneActivationTestRows reads the complete hidden state used for preservation and rollback assertions.
func networkDataPlaneActivationTestRows(t *testing.T, connection *gorm.DB) networkModelRows {
	t.Helper()
	rows, err := readNetworkModelRows(connection)
	if err != nil {
		t.Fatalf("read activation network rows: %v", err)
	}
	return rows
}

// networkDataPlaneActivationTestProof finds one unique setup proof for preservation assertions.
func networkDataPlaneActivationTestProof(
	rows []models.NetworkSetupEvidence,
	component NetworkSetupComponent,
) models.NetworkSetupEvidence {
	for _, row := range rows {
		if row.Component == string(component) {
			return row
		}
	}
	return models.NetworkSetupEvidence{Component: fmt.Sprintf("missing:%s", component)}
}
