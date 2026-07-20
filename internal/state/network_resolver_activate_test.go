package state

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// TestActivateNetworkResolverRequestRejectsIncompleteAuthority covers every independent resolver authority branch.
func TestActivateNetworkResolverRequestRejectsIncompleteAuthority(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*ActivateNetworkResolverRequest)
	}{
		{name: "zero revision", want: "network revision must be positive", mutate: func(request *ActivateNetworkResolverRequest) {
			request.ExpectedNetworkRevision = 0
		}},
		{name: "missing confirmed ownership", want: "requires confirmed authority", mutate: func(request *ActivateNetworkResolverRequest) {
			request.ConfirmedOwnership = ownership.Observation{}
		}},
		{name: "identity ownership", want: "schema version is 1, want 2", mutate: func(request *ActivateNetworkResolverRequest) {
			source, err := networkDataPlaneActivationIdentityOwnership(request.ConfirmedOwnership)
			if err != nil {
				t.Fatalf("derive request validation source: %v", err)
			}
			request.ConfirmedOwnership = source
		}},
		{name: "ownership fingerprint", want: "fingerprint does not match", mutate: func(request *ActivateNetworkResolverRequest) {
			request.ConfirmedOwnership.Fingerprint = strings.Repeat("0", 64)
		}},
		{name: "invalid policy", want: "network resolver policy", mutate: func(request *ActivateNetworkResolverRequest) {
			request.Policy.Suffix = ".invalid"
		}},
		{name: "policy fingerprint", want: "policy fingerprint does not match", mutate: func(request *ActivateNetworkResolverRequest) {
			request.Policy.AuthorityFingerprint = strings.Repeat("d", 64)
		}},
		{name: "wrong proof", want: "expected \"resolver\"", mutate: func(request *ActivateNetworkResolverRequest) {
			request.Resolver.Component = NetworkSetupComponentLowPorts
		}},
		{name: "invalid proof", want: "generation must be positive", mutate: func(request *ActivateNetworkResolverRequest) {
			request.Resolver.Generation = 0
		}},
		{name: "future proof", want: "must not be after the network mutation time", mutate: func(request *ActivateNetworkResolverRequest) {
			request.Resolver.VerifiedAt = request.At.Add(time.Second)
		}},
		{name: "zero time", want: "resolver activation time", mutate: func(request *ActivateNetworkResolverRequest) {
			request.At = time.Time{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := networkResolverActivationTestRequest(t, 1)
			test.mutate(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ActivateNetworkResolverRequest.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestStoreActivateNetworkResolverCommitsAndReplaysExactAuthority verifies one atomic identity-to-resolver transition.
func TestStoreActivateNetworkResolverCommitsAndReplaysExactAuthority(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	identityRequest, identityResult := initializeNetworkDataPlaneActivationIdentity(t, store, connection)
	before := networkDataPlaneActivationTestRows(t, connection)
	beforeProjection := networkDataPlaneActivationTestProjection(t, connection)
	request := networkResolverActivationTestRequest(t, identityResult.Record.Revision)

	result, err := store.ActivateNetworkResolver(context.Background(), request)
	if err != nil {
		t.Fatalf("ActivateNetworkResolver() error = %v", err)
	}
	if result.Replayed || result.Record.Stage != NetworkStageResolver || result.Record.Revision != 2 {
		t.Fatalf("ActivateNetworkResolver() result = %#v, want applied resolver revision 2", result)
	}
	if !result.Record.CreatedAt.Equal(identityRequest.At) || !result.Record.UpdatedAt.Equal(request.At) ||
		result.Record.Ownership != identityResult.Record.Ownership ||
		!reflect.DeepEqual(result.Record.Pool, identityResult.Record.Pool) ||
		result.Record.Reservations.Listeners != (SharedListenerReservations{}) ||
		len(result.Record.Reservations.Endpoints) != 0 {
		t.Fatalf("resolver projection = %#v, want preserved non-publishable identity", result.Record)
	}
	after := networkDataPlaneActivationTestRows(t, connection)
	afterProjection := networkDataPlaneActivationTestProjection(t, connection)
	if len(after.SetupEvidence) != 3 || len(after.Listeners) != 0 || len(after.Endpoints) != 0 {
		t.Fatalf("resolver rows = proofs %d, listeners %d, endpoints %d", len(after.SetupEvidence), len(after.Listeners), len(after.Endpoints))
	}
	expectedProjection := beforeProjection.row
	expectedProjection.OwnershipSchemaVersion = int(ownership.NetworkPolicySchemaVersion)
	expectedProjection.NetworkPolicyFingerprint = machineOwnershipNetworkPolicyModelValue(request.ConfirmedOwnership.Record)
	expectedProjection.RecordFingerprint = request.ConfirmedOwnership.Fingerprint
	expectedProjection.ConfirmedAt = request.At
	if afterProjection.observation != request.ConfirmedOwnership || !reflect.DeepEqual(afterProjection.row, expectedProjection) {
		t.Fatalf("resolver ownership projection = %#v, want %#v", afterProjection, expectedProjection)
	}
	if !reflect.DeepEqual(after.Candidates, before.Candidates) ||
		!reflect.DeepEqual(after.Leases, before.Leases) ||
		!reflect.DeepEqual(after.Releases, before.Releases) {
		t.Fatal("resolver activation changed retained lifecycle state")
	}

	retry := request
	retry.At = request.At.Add(time.Hour)
	replayed, err := store.ActivateNetworkResolver(context.Background(), retry)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Record, result.Record) {
		t.Fatalf("resolver replay = %#v, %v; want exact durable record", replayed, err)
	}
	if highWater := projectStoreMutationSequence(t, store); highWater != 2 {
		t.Fatalf("Harbor high-water after resolver replay = %d, want 2", highWater)
	}

	stale := request
	stale.ExpectedNetworkRevision = 3
	_, err = store.ActivateNetworkResolver(context.Background(), stale)
	var revisionConflict *NetworkRevisionConflictError
	if !errors.As(err, &revisionConflict) {
		t.Fatalf("future resolver retry error = %v, want NetworkRevisionConflictError", err)
	}

	divergent := request
	divergent.Resolver.Evidence = "different verified resolver"
	_, err = store.ActivateNetworkResolver(context.Background(), divergent)
	var activationConflict *NetworkResolverActivationConflictError
	if !errors.As(err, &activationConflict) || activationConflict.Difference != "network setup proofs" {
		t.Fatalf("divergent resolver retry error = %v, want typed proof conflict", err)
	}
}

// TestStoreActivateNetworkResolverRejectsMissingFoundation verifies resolver authority cannot create persistence or identity implicitly.
func TestStoreActivateNetworkResolverRejectsMissingFoundation(t *testing.T) {
	t.Run("schema", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		_, err := store.ActivateNetworkResolver(context.Background(), networkResolverActivationTestRequest(t, 1))
		if err == nil || !strings.Contains(err.Error(), "network persistence schema is not installed") {
			t.Fatalf("missing schema error = %v", err)
		}
	})

	t.Run("identity", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		_, err := store.ActivateNetworkResolver(context.Background(), networkResolverActivationTestRequest(t, 1))
		var missing *NetworkNotInitializedError
		if !errors.As(err, &missing) {
			t.Fatalf("uninitialized resolver error = %v, want NetworkNotInitializedError", err)
		}
		for _, table := range networkTableNames() {
			if got := networkInitializeTestCount(t, connection, table); got != 0 {
				t.Fatalf("%s count after uninitialized resolver = %d, want 0", table, got)
			}
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		initializeNetworkDataPlaneActivationIdentity(t, store, connection)
		before := networkDataPlaneActivationTestRows(t, connection)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := store.ActivateNetworkResolver(ctx, networkResolverActivationTestRequest(t, 1))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled resolver error = %v, want context.Canceled", err)
		}
		if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
			t.Fatal("cancelled resolver activation changed network rows")
		}
	})
}

// TestStoreActivateNetworkResolverClassifiesAuthorityConflicts distinguishes optimistic staleness from durable divergence.
func TestStoreActivateNetworkResolverClassifiesAuthorityConflicts(t *testing.T) {
	t.Run("stale identity revision", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		initializeNetworkDataPlaneActivationIdentity(t, store, connection)
		_, err := store.ActivateNetworkResolver(context.Background(), networkResolverActivationTestRequest(t, 2))
		var conflict *NetworkRevisionConflictError
		if !errors.As(err, &conflict) || conflict.Expected != 2 || conflict.Actual != 1 {
			t.Fatalf("stale resolver activation error = %v, want revision 2/1", err)
		}
	})

	t.Run("different identity source", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		initializeNetworkDataPlaneActivationIdentity(t, store, connection)
		projected := networkDataPlaneActivationTestProjection(t, connection)
		different := projected.observation.Record
		different.OwnerIdentity = "502"
		differentObservation := networkDataPlaneActivationTestObservation(t, different)
		if err := connection.Model(&models.MachineOwnershipProjection{}).
			Where("id = ?", machineOwnershipProjectionSingletonID).
			Updates(map[string]any{
				"owner_identity":     different.OwnerIdentity,
				"record_fingerprint": differentObservation.Fingerprint,
			}).Error; err != nil {
			t.Fatalf("seed different identity projection: %v", err)
		}

		_, err := store.ActivateNetworkResolver(context.Background(), networkResolverActivationTestRequest(t, 1))
		var conflict *NetworkResolverActivationConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "machine ownership projection" {
			t.Fatalf("different identity projection error = %v, want typed ownership conflict", err)
		}
	})

	t.Run("identity with schema two", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		initializeNetworkDataPlaneActivationIdentity(t, store, connection)
		request := networkResolverActivationTestRequest(t, 1)
		if err := connection.Model(&models.MachineOwnershipProjection{}).
			Where("id = ?", machineOwnershipProjectionSingletonID).
			Updates(map[string]any{
				"ownership_schema_version":   int(ownership.NetworkPolicySchemaVersion),
				"network_policy_fingerprint": request.ConfirmedOwnership.Record.NetworkPolicyFingerprint,
				"record_fingerprint":         request.ConfirmedOwnership.Fingerprint,
				"confirmed_at":               request.At,
			}).Error; err != nil {
			t.Fatalf("seed premature schema-two projection: %v", err)
		}

		_, err := store.ActivateNetworkResolver(context.Background(), request)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "identity-stage network retains schema-2 ownership") {
			t.Fatalf("identity schema-two error = %v, want corrupt lifecycle state", err)
		}
	})

	t.Run("projection confirmation time", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		identityRequest, _ := initializeNetworkDataPlaneActivationIdentity(t, store, connection)
		confirmedAt := identityRequest.At.Add(30 * time.Second)
		if err := connection.Model(&models.MachineOwnershipProjection{}).
			Where("id = ?", machineOwnershipProjectionSingletonID).
			Update("confirmed_at", confirmedAt).Error; err != nil {
			t.Fatalf("seed later identity confirmation: %v", err)
		}
		request := networkResolverActivationTestRequest(t, 1)
		request.At = identityRequest.At.Add(15 * time.Second)

		_, err := store.ActivateNetworkResolver(context.Background(), request)
		var conflict *NetworkResolverActivationConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "activation time" {
			t.Fatalf("projection confirmation time error = %v, want activation-time conflict", err)
		}
	})

	t.Run("full stage", func(t *testing.T) {
		store, connection := newNetworkInitializeTestHarness(t, false)
		initializeNetworkDataPlaneActivationIdentity(t, store, connection)
		full := networkDataPlaneActivationTestRequest(t, 1)
		result, err := store.ActivateNetworkDataPlane(context.Background(), full)
		if err != nil {
			t.Fatalf("seed full activation: %v", err)
		}
		request := networkResolverActivationTestRequest(t, result.Record.Revision)
		_, err = store.ActivateNetworkResolver(context.Background(), request)
		var conflict *NetworkResolverActivationConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "network stage" {
			t.Fatalf("full-stage resolver error = %v, want typed stage conflict", err)
		}
	})
}

// TestStoreActivateNetworkDataPlaneBuildsOnExactResolverAuthority verifies full activation preserves the staged policy and proof.
func TestStoreActivateNetworkDataPlaneBuildsOnExactResolverAuthority(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	initializeNetworkDataPlaneActivationIdentity(t, store, connection)
	resolverRequest := networkResolverActivationTestRequest(t, 1)
	resolverResult, err := store.ActivateNetworkResolver(context.Background(), resolverRequest)
	if err != nil {
		t.Fatalf("ActivateNetworkResolver() error = %v", err)
	}
	resolverRows := networkDataPlaneActivationTestRows(t, connection)
	resolverProjection := networkDataPlaneActivationTestProjection(t, connection)

	request := networkDataPlaneActivationTestRequest(t, resolverResult.Record.Revision)
	request.At = resolverRequest.At.Add(time.Minute)
	result, err := store.ActivateNetworkDataPlane(context.Background(), request)
	if err != nil {
		t.Fatalf("ActivateNetworkDataPlane() from resolver error = %v", err)
	}
	if result.Replayed || result.Record.Stage != NetworkStageFull || result.Record.Revision != 3 {
		t.Fatalf("full activation result = %#v, want full revision 3", result)
	}
	fullRows := networkDataPlaneActivationTestRows(t, connection)
	fullProjection := networkDataPlaneActivationTestProjection(t, connection)
	if len(fullRows.SetupEvidence) != 4 || len(fullRows.Listeners) != 3 {
		t.Fatalf("full rows = proofs %d, listeners %d", len(fullRows.SetupEvidence), len(fullRows.Listeners))
	}
	if !reflect.DeepEqual(
		networkDataPlaneActivationTestProof(resolverRows.SetupEvidence, NetworkSetupComponentResolver),
		networkDataPlaneActivationTestProof(fullRows.SetupEvidence, NetworkSetupComponentResolver),
	) {
		t.Fatal("full activation changed the persisted resolver proof")
	}
	if !reflect.DeepEqual(fullProjection, resolverProjection) {
		t.Fatal("full activation changed already-confirmed policy ownership")
	}
}

// TestStoreActivateNetworkDataPlaneRejectsResolverAuthorityDrift verifies full activation cannot replace staged resolver facts.
func TestStoreActivateNetworkDataPlaneRejectsResolverAuthorityDrift(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	initializeNetworkDataPlaneActivationIdentity(t, store, connection)
	resolverResult, err := store.ActivateNetworkResolver(context.Background(), networkResolverActivationTestRequest(t, 1))
	if err != nil {
		t.Fatalf("ActivateNetworkResolver() error = %v", err)
	}
	before := networkDataPlaneActivationTestRows(t, connection)
	beforeProjection := networkDataPlaneActivationTestProjection(t, connection)
	request := networkDataPlaneActivationTestRequest(t, resolverResult.Record.Revision)
	request.Setup[0].Evidence = "different resolver authority"

	_, err = store.ActivateNetworkDataPlane(context.Background(), request)
	var conflict *NetworkDataPlaneActivationConflictError
	if !errors.As(err, &conflict) || conflict.Difference != "network resolver proof" {
		t.Fatalf("resolver drift error = %v, want typed resolver proof conflict", err)
	}
	if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatal("resolver drift changed durable network rows")
	}
	if after := networkDataPlaneActivationTestProjection(t, connection); !reflect.DeepEqual(after, beforeProjection) {
		t.Fatal("resolver drift changed ownership projection")
	}
}

// TestStoreActivateNetworkResolverRollsBackWriteFaults verifies each durable write phase is atomic.
func TestStoreActivateNetworkResolverRollsBackWriteFaults(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{name: "proof", trigger: "CREATE TRIGGER fail_resolver_activation BEFORE INSERT ON network_setup_evidence WHEN NEW.component = 'resolver' BEGIN SELECT RAISE(ABORT, 'resolver proof failure'); END"},
		{name: "root", trigger: "CREATE TRIGGER fail_resolver_activation BEFORE UPDATE ON network_state WHEN NEW.stage = 'resolver' BEGIN SELECT RAISE(ABORT, 'resolver root failure'); END"},
		{name: "ownership", trigger: "CREATE TRIGGER fail_resolver_activation BEFORE UPDATE ON machine_ownership_projections BEGIN SELECT RAISE(ABORT, 'resolver ownership failure'); END"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newNetworkInitializeTestHarness(t, false)
			initializeNetworkDataPlaneActivationIdentity(t, store, connection)
			before := networkDataPlaneActivationTestRows(t, connection)
			beforeProjection := networkDataPlaneActivationTestProjection(t, connection)
			mustProjectStoreReadExec(t, connection, test.trigger)

			_, err := store.ActivateNetworkResolver(context.Background(), networkResolverActivationTestRequest(t, 1))
			if err == nil || !strings.Contains(err.Error(), "resolver ") {
				t.Fatalf("%s fault error = %v", test.name, err)
			}
			if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s fault left partial network rows", test.name)
			}
			if after := networkDataPlaneActivationTestProjection(t, connection); !reflect.DeepEqual(after, beforeProjection) {
				t.Fatalf("%s fault changed ownership projection", test.name)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 1 {
				t.Fatalf("Harbor high-water after %s fault = %d, want 1", test.name, highWater)
			}
		})
	}
}

// TestStoreActivateNetworkResolverRollsBackReadbackCorruption verifies late conversion failures restore identity state.
func TestStoreActivateNetworkResolverRollsBackReadbackCorruption(t *testing.T) {
	store, connection := newNetworkInitializeTestHarness(t, false)
	initializeNetworkDataPlaneActivationIdentity(t, store, connection)
	before := networkDataPlaneActivationTestRows(t, connection)
	beforeProjection := networkDataPlaneActivationTestProjection(t, connection)
	active := false
	updateCallback := "harbor:test_network_resolver_readback_active"
	queryCallback := "harbor:test_network_resolver_readback_corruption"
	if err := connection.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Table == "network_state" {
			active = true
		}
	}); err != nil {
		t.Fatalf("register resolver update callback: %v", err)
	}
	if err := connection.Callback().Query().After("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
		if !active {
			return
		}
		if rows, ok := tx.Statement.Dest.(*[]models.NetworkState); ok && len(*rows) != 0 {
			(*rows)[0].Stage = string(NetworkStageIdentity)
		}
	}); err != nil {
		t.Fatalf("register resolver query callback: %v", err)
	}

	_, err := store.ActivateNetworkResolver(context.Background(), networkResolverActivationTestRequest(t, 1))
	_ = connection.Callback().Update().Remove(updateCallback)
	_ = connection.Callback().Query().Remove(queryCallback)
	if err == nil || !strings.Contains(err.Error(), "identity-stage network retains schema-2 ownership") {
		t.Fatalf("resolver readback corruption error = %v", err)
	}
	if after := networkDataPlaneActivationTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatal("resolver readback corruption left partial network rows")
	}
	if after := networkDataPlaneActivationTestProjection(t, connection); !reflect.DeepEqual(after, beforeProjection) {
		t.Fatal("resolver readback corruption changed ownership projection")
	}
}

// TestNetworkResolverActivationConflictErrorDescribesScope verifies diagnostics omit proof contents.
func TestNetworkResolverActivationConflictErrorDescribesScope(t *testing.T) {
	err := &NetworkResolverActivationConflictError{ActualRevision: 9, Difference: "network setup proofs"}
	if got := err.Error(); got != "network resolver is already active at revision 9 with different network setup proofs" {
		t.Fatalf("NetworkResolverActivationConflictError.Error() = %q", got)
	}
}

// networkResolverActivationTestRequest returns one exact schema-two policy and resolver proof.
func networkResolverActivationTestRequest(t *testing.T, expected domain.Sequence) ActivateNetworkResolverRequest {
	t.Helper()
	full := networkDataPlaneActivationTestRequest(t, expected)
	return ActivateNetworkResolverRequest{
		ExpectedNetworkRevision: expected,
		ConfirmedOwnership:      full.ConfirmedOwnership,
		Policy:                  full.Policy,
		Resolver:                full.Setup[0],
		At:                      full.At,
	}
}
