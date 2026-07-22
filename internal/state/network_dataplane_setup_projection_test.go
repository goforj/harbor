package state

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// networkDataPlaneSetupProjectionFixture retains one valid resolver projection and its source.
type networkDataPlaneSetupProjectionFixture struct {
	store    *Store
	database *gorm.DB
	source   *NetworkDataPlaneSetupProjectionSource
	request  ActivateNetworkResolverRequest
}

// TestNetworkDataPlaneSetupProjectionSourceResolvesCompletedResolver proves terminal resolver state needs no retained plan.
func TestNetworkDataPlaneSetupProjectionSourceResolvesCompletedResolver(t *testing.T) {
	fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
	completed, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverSetup() error = %v", err)
	}
	var retainedPlans int64
	if err := fixture.database.Model(&models.NetworkResolverSetupPlan{}).Count(&retainedPlans).Error; err != nil {
		t.Fatalf("count retired resolver plans: %v", err)
	}
	if retainedPlans != 0 {
		t.Fatalf("retained resolver setup plans = %d, want 0", retainedPlans)
	}

	beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
	beforeProjection := networkDataPlaneActivationTestProjection(t, fixture.database)
	beforeSequence := projectStoreMutationSequence(t, fixture.store)
	source := NewNetworkDataPlaneSetupProjectionSource(fixture.store.networkState)
	authorityTables := []string{
		"harbor_state",
		"machine_ownership_projections",
		"network_shared_listeners",
		"network_setup_evidence",
		"network_state",
	}
	seen := make(map[string]bool, len(authorityTables))
	outOfTransaction := make(map[string]bool, len(authorityTables))
	observing := true
	callback := "harbor:test_network_data_plane_setup_projection_transaction"
	if err := fixture.database.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if !observing || !slices.Contains(authorityTables, tx.Statement.Table) {
			return
		}
		seen[tx.Statement.Table] = true
		if _, ok := tx.Statement.ConnPool.(*sql.Tx); !ok {
			outOfTransaction[tx.Statement.Table] = true
		}
	}); err != nil {
		t.Fatalf("register projection transaction observer: %v", err)
	}
	t.Cleanup(func() { _ = fixture.database.Callback().Query().Remove(callback) })

	projected, err := source.Resolve(nil, fixture.request.Policy)
	observing = false
	if err != nil {
		t.Fatalf("Resolve(nil) error = %v", err)
	}
	expectedProof, err := networkResolverSetupCompletionProof(request, fixture.request.TargetOwnership.Generation)
	if err != nil {
		t.Fatalf("derive completed resolver proof: %v", err)
	}
	expectedOwnership := ownership.Observation{
		Exists:      true,
		Record:      fixture.request.TargetOwnership,
		Fingerprint: request.ResolverEvidence.OwnershipFingerprint,
	}
	want := NetworkDataPlaneSetupProjection{
		Stage:              NetworkStageResolver,
		NetworkRevision:    completed.NetworkRevision,
		NetworkUpdatedAt:   completed.Network.Record.UpdatedAt,
		ResolverProof:      expectedProof,
		ConfirmedOwnership: expectedOwnership,
	}
	if projected != want {
		t.Fatalf("Resolve(nil) = %#v, want %#v", projected, want)
	}
	if err := projected.Validate(); err != nil {
		t.Fatalf("Resolve(nil).Validate() error = %v", err)
	}
	for _, table := range authorityTables {
		if !seen[table] {
			t.Fatalf("Resolve(nil) did not read authority table %q", table)
		}
		if outOfTransaction[table] {
			t.Fatalf("Resolve(nil) read authority table %q outside its transaction", table)
		}
	}
	if afterRows := networkDataPlaneActivationTestRows(t, fixture.database); !reflect.DeepEqual(afterRows, beforeRows) {
		t.Fatal("Resolve(nil) mutated network rows")
	}
	if afterProjection := networkDataPlaneActivationTestProjection(t, fixture.database); !reflect.DeepEqual(afterProjection, beforeProjection) {
		t.Fatal("Resolve(nil) mutated confirmed ownership")
	}
	if afterSequence := projectStoreMutationSequence(t, fixture.store); afterSequence != beforeSequence {
		t.Fatalf("Resolve(nil) changed Harbor sequence from %d to %d", beforeSequence, afterSequence)
	}
}

// TestNetworkDataPlaneSetupProjectionSourceAcceptsExactFullReplay preserves resolver authority after full progression.
func TestNetworkDataPlaneSetupProjectionSourceAcceptsExactFullReplay(t *testing.T) {
	fixture, _, request := stagedNetworkResolverSetupCompletionFixture(t)
	completed, err := fixture.store.CompleteNetworkResolverSetup(context.Background(), request)
	if err != nil {
		t.Fatalf("CompleteNetworkResolverSetup() error = %v", err)
	}
	resolverProof, err := networkResolverSetupCompletionProof(request, fixture.request.TargetOwnership.Generation)
	if err != nil {
		t.Fatalf("derive completed resolver proof: %v", err)
	}
	fullRequest := networkDataPlaneActivationTestRequest(t, completed.NetworkRevision)
	fullRequest.ConfirmedOwnership = networkDataPlaneActivationTestObservation(t, fixture.request.TargetOwnership)
	fullRequest.Policy = fixture.request.Policy
	fullRequest.Setup[0] = resolverProof
	fullRequest.At = request.At.Add(time.Minute)
	fullRequest.Setup[1].VerifiedAt = fullRequest.At
	fullRequest.Listeners.DNS.VerifiedAt = fullRequest.At
	fullRequest.Listeners.HTTP.VerifiedAt = fullRequest.At
	fullRequest.Listeners.HTTPS.VerifiedAt = fullRequest.At
	full, err := fixture.store.ActivateNetworkDataPlane(context.Background(), fullRequest)
	if err != nil {
		t.Fatalf("ActivateNetworkDataPlane() error = %v", err)
	}

	source := NewNetworkDataPlaneSetupProjectionSource(fixture.store.networkState)
	projected, err := source.Resolve(context.Background(), fixture.request.Policy)
	if err != nil {
		t.Fatalf("Resolve() full error = %v", err)
	}
	if projected.Stage != NetworkStageFull || projected.NetworkRevision != full.Record.Revision ||
		!projected.NetworkUpdatedAt.Equal(full.Record.UpdatedAt) ||
		projected.ResolverProof != resolverProof ||
		projected.LowPortProof != fullRequest.Setup[1] ||
		projected.Listeners != fullRequest.Listeners ||
		projected.ConfirmedOwnership.Record != fixture.request.TargetOwnership {
		t.Fatalf("Resolve() full = %#v, want exact replayable full authority", projected)
	}
	if err := projected.Validate(); err != nil {
		t.Fatalf("Resolve() full Validate() error = %v", err)
	}

	want := projected
	projected.NetworkUpdatedAt = projected.NetworkUpdatedAt.Add(time.Second)
	projected.ResolverProof.Evidence = "caller mutation"
	projected.LowPortProof.Evidence = "caller mutation"
	projected.Listeners.HTTPS.Generation++
	projected.ConfirmedOwnership.Record.OwnerIdentity = "caller mutation"
	again, err := source.Resolve(context.Background(), fixture.request.Policy)
	if err != nil {
		t.Fatalf("second Resolve() full error = %v", err)
	}
	if again != want {
		t.Fatalf("second Resolve() = %#v, want value-isolated %#v", again, want)
	}
}

// TestNetworkDataPlaneSetupProjectionValidatePinsNarrowContract covers every independent result field.
func TestNetworkDataPlaneSetupProjectionValidatePinsNarrowContract(t *testing.T) {
	request := networkResolverActivationTestRequest(t, 1)
	valid := NetworkDataPlaneSetupProjection{
		Stage:              NetworkStageResolver,
		NetworkRevision:    2,
		NetworkUpdatedAt:   request.At,
		ResolverProof:      request.Resolver,
		ConfirmedOwnership: request.ConfirmedOwnership,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid resolver projection error = %v", err)
	}
	activation := networkDataPlaneActivationTestRequest(t, 1)
	full := NetworkDataPlaneSetupProjection{
		Stage:              NetworkStageFull,
		NetworkRevision:    3,
		NetworkUpdatedAt:   activation.At,
		ResolverProof:      activation.Setup[0],
		LowPortProof:       activation.Setup[1],
		Listeners:          activation.Listeners,
		ConfirmedOwnership: activation.ConfirmedOwnership,
	}
	if err := full.Validate(); err != nil {
		t.Fatalf("valid full projection error = %v", err)
	}

	tests := []struct {
		name   string
		want   string
		mutate func(*NetworkDataPlaneSetupProjection)
	}{
		{name: "stage", want: "expected \"resolver\" or \"full\"", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.Stage = NetworkStageIdentity
		}},
		{name: "revision", want: "revision must be positive", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.NetworkRevision = 0
		}},
		{name: "update time", want: "update time must not be zero", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.NetworkUpdatedAt = time.Time{}
		}},
		{name: "proof component", want: "expected \"resolver\"", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.ResolverProof.Component = NetworkSetupComponentLowPorts
		}},
		{name: "proof generation", want: "generation must be positive", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.ResolverProof.Generation = 0
		}},
		{name: "resolver proof after update", want: "resolver proof verification time", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.ResolverProof.VerifiedAt = value.NetworkUpdatedAt.Add(time.Second)
		}},
		{name: "resolver low-port authority", want: "must not contain low-port authority", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.LowPortProof = activation.Setup[1]
		}},
		{name: "resolver listener authority", want: "must not contain listener authority", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.Listeners = activation.Listeners
		}},
		{name: "ownership absent", want: "requires confirmed authority", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.ConfirmedOwnership = ownership.Observation{}
		}},
		{name: "ownership schema", want: "schema version is 1, want 2", mutate: func(value *NetworkDataPlaneSetupProjection) {
			source, err := networkDataPlaneActivationIdentityOwnership(value.ConfirmedOwnership)
			if err != nil {
				t.Fatalf("derive schema-one projection: %v", err)
			}
			value.ConfirmedOwnership = source
		}},
		{name: "ownership fingerprint", want: "fingerprint does not match", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.ConfirmedOwnership.Fingerprint = strings.Repeat("0", 64)
		}},
	}

	fullTests := []struct {
		name   string
		want   string
		mutate func(*NetworkDataPlaneSetupProjection)
	}{
		{name: "low-port component", want: "expected \"low_ports\"", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.LowPortProof.Component = NetworkSetupComponentResolver
		}},
		{name: "low-port generation", want: "generation must be positive", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.LowPortProof.Generation = 0
		}},
		{name: "low-port after update", want: "low-port proof verification time", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.LowPortProof.VerifiedAt = value.NetworkUpdatedAt.Add(time.Second)
		}},
		{name: "invalid listeners", want: "HTTP listener", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.Listeners.HTTP.Generation = 0
		}},
		{name: "DNS listener after update", want: "DNS listener verification time", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.Listeners.DNS.VerifiedAt = value.NetworkUpdatedAt.Add(time.Second)
		}},
		{name: "HTTP listener after update", want: "HTTP listener verification time", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.Listeners.HTTP.VerifiedAt = value.NetworkUpdatedAt.Add(time.Second)
		}},
		{name: "HTTPS listener after update", want: "HTTPS listener verification time", mutate: func(value *NetworkDataPlaneSetupProjection) {
			value.Listeners.HTTPS.VerifiedAt = value.NetworkUpdatedAt.Add(time.Second)
		}},
	}
	for _, test := range fullTests {
		t.Run("full "+test.name, func(t *testing.T) {
			candidate := full
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	projectionType := reflect.TypeOf(NetworkDataPlaneSetupProjection{})
	fields := make([]string, 0, projectionType.NumField())
	for index := 0; index < projectionType.NumField(); index++ {
		fields = append(fields, projectionType.Field(index).Name)
	}
	wantFields := []string{
		"Stage",
		"NetworkRevision",
		"NetworkUpdatedAt",
		"ResolverProof",
		"LowPortProof",
		"Listeners",
		"ConfirmedOwnership",
	}
	if !slices.Equal(fields, wantFields) {
		t.Fatalf("NetworkDataPlaneSetupProjection fields = %v, want narrow surface %v", fields, wantFields)
	}
}

// TestNetworkDataPlaneSetupProjectionSourceAdmission rejects invalid callers and pre-resolver lifecycle state.
func TestNetworkDataPlaneSetupProjectionSourceAdmission(t *testing.T) {
	fixture := newNetworkDataPlaneSetupProjectionFixture(t)
	beforeRows := networkDataPlaneActivationTestRows(t, fixture.database)
	beforeProjection := networkDataPlaneActivationTestProjection(t, fixture.database)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if projected, err := fixture.source.Resolve(ctx, fixture.request.Policy); !errors.Is(err, context.Canceled) ||
		projected != (NetworkDataPlaneSetupProjection{}) {
		t.Fatalf("Resolve(cancelled) = %#v, %v, want context.Canceled", projected, err)
	}
	invalidPolicy := fixture.request.Policy
	invalidPolicy.Suffix = ".invalid"
	if _, err := fixture.source.Resolve(context.Background(), invalidPolicy); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("Resolve(invalid policy) error = %v", err)
	}
	mismatchedPolicy := fixture.request.Policy
	mismatchedPolicy.AuthorityFingerprint = strings.Repeat("d", 64)
	if err := mismatchedPolicy.Validate(); err != nil {
		t.Fatalf("mismatched policy should remain canonical: %v", err)
	}
	if _, err := fixture.source.Resolve(context.Background(), mismatchedPolicy); err == nil ||
		!strings.Contains(err.Error(), "fingerprint does not match confirmed ownership") {
		t.Fatalf("Resolve(mismatched policy) error = %v", err)
	}
	if afterRows := networkDataPlaneActivationTestRows(t, fixture.database); !reflect.DeepEqual(afterRows, beforeRows) {
		t.Fatal("rejected Resolve() calls mutated network rows")
	}
	if afterProjection := networkDataPlaneActivationTestProjection(t, fixture.database); !reflect.DeepEqual(afterProjection, beforeProjection) {
		t.Fatal("rejected Resolve() calls mutated confirmed ownership")
	}

	store, _ := newNetworkInitializeTestHarness(t, false)
	uninitialized := NewNetworkDataPlaneSetupProjectionSource(store.networkState)
	if _, err := uninitialized.Resolve(context.Background(), fixture.request.Policy); err == nil ||
		!strings.Contains(err.Error(), "requires initialized network state") {
		t.Fatalf("Resolve(uninitialized) error = %v", err)
	}
	identityStore, identityDatabase := newNetworkInitializeTestHarness(t, false)
	initializeNetworkDataPlaneActivationIdentity(t, identityStore, identityDatabase)
	identity := NewNetworkDataPlaneSetupProjectionSource(identityStore.networkState)
	if _, err := identity.Resolve(context.Background(), fixture.request.Policy); err == nil ||
		!strings.Contains(err.Error(), `current stage is "identity"`) {
		t.Fatalf("Resolve(identity) error = %v", err)
	}
}

// TestNewNetworkDataPlaneSetupProjectionSourceRequiresRepository rejects missing persistence wiring.
func TestNewNetworkDataPlaneSetupProjectionSourceRequiresRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewNetworkDataPlaneSetupProjectionSource(nil) did not panic")
		}
	}()
	_ = NewNetworkDataPlaneSetupProjectionSource(nil)
}

// TestNetworkDataPlaneSetupProjectionSourceFailsClosedOnDurableMismatch covers every source-specific cross-link.
func TestNetworkDataPlaneSetupProjectionSourceFailsClosedOnDurableMismatch(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*testing.T, *networkDataPlaneSetupProjectionFixture)
	}{
		{name: "missing resolver proof", want: "found 2 rows, expected 3", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"DELETE FROM network_setup_evidence WHERE component = ?",
				NetworkSetupComponentResolver,
			)
		}},
		{name: "resolver proof wrong root", want: "network state ID is 2, expected 1", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(t, fixture.database, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE network_setup_evidence SET network_state_id = 2 WHERE component = ?",
				NetworkSetupComponentResolver,
			)
			mustProjectStoreReadExec(t, fixture.database, "PRAGMA foreign_keys = ON")
		}},
		{name: "resolver proof after root", want: "must not be after the network state update time", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE network_setup_evidence SET verified_at = ? WHERE component = ?",
				fixture.request.At.Add(time.Second),
				NetworkSetupComponentResolver,
			)
		}},
		{name: "resolver proof after ownership confirmation", want: "after policy-bound ownership confirmation", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE network_state SET updated_at = ? WHERE id = 1",
				fixture.request.At.Add(2*time.Second),
			)
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE network_setup_evidence SET verified_at = ? WHERE component = ?",
				fixture.request.At.Add(time.Second),
				NetworkSetupComponentResolver,
			)
		}},
		{name: "unexpected resolver-stage proof", want: "found 4 rows, expected 3", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			proof := models.NetworkSetupEvidence{
				NetworkStateId: networkStateSingletonID,
				Component:      string(NetworkSetupComponentLowPorts),
				Evidence:       "unexpected low-port proof",
				Generation:     1,
				VerifiedAt:     fixture.request.At,
			}
			if err := fixture.database.Create(&proof).Error; err != nil {
				t.Fatalf("insert unexpected low-port proof: %v", err)
			}
		}},
		{name: "missing ownership projection", want: "found 0 rows, expected 1", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(t, fixture.database, "DELETE FROM machine_ownership_projections")
		}},
		{name: "ownership fingerprint drift", want: "record fingerprint does not match", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE machine_ownership_projections SET record_fingerprint = ? WHERE id = 1",
				strings.Repeat("0", 64),
			)
		}},
		{name: "ownership root drift", want: "projected ownership differs from the durable network root", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			record := fixture.request.ConfirmedOwnership.Record
			record.InstallationID = "different-harbor-installation"
			fingerprint, err := record.Fingerprint()
			if err != nil {
				t.Fatalf("fingerprint drifted ownership record: %v", err)
			}
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE machine_ownership_projections SET installation_id = ?, record_fingerprint = ? WHERE id = 1",
				record.InstallationID,
				fingerprint,
			)
		}},
		{name: "ownership confirmation after root", want: "confirmation time is after", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE machine_ownership_projections SET confirmed_at = ? WHERE id = 1",
				fixture.request.At.Add(time.Second),
			)
		}},
		{name: "network revision beyond high-water", want: "revision exceeds captured sequence", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture) {
			mustProjectStoreReadExec(t, fixture.database, "UPDATE network_state SET revision = 3 WHERE id = 1")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkDataPlaneSetupProjectionFixture(t)
			test.mutate(t, fixture)
			projected, err := fixture.source.Resolve(context.Background(), fixture.request.Policy)
			if err == nil || projected != (NetworkDataPlaneSetupProjection{}) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() = %#v, %v, want corruption containing %q", projected, err, test.want)
			}
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) {
				t.Fatalf("Resolve() error = %v, want CorruptStateError", err)
			}
		})
	}
}

// TestNetworkDataPlaneSetupProjectionSourceFailsClosedOnFullReplayCorruption covers every full-only recovery fact.
func TestNetworkDataPlaneSetupProjectionSourceFailsClosedOnFullReplayCorruption(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*testing.T, *networkDataPlaneSetupProjectionFixture, ActivateNetworkDataPlaneRequest)
	}{
		{name: "missing low-port proof", want: "found 3 rows, expected 4", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture, _ ActivateNetworkDataPlaneRequest) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"DELETE FROM network_setup_evidence WHERE component = ?",
				NetworkSetupComponentLowPorts,
			)
		}},
		{name: "extra setup proof", want: "found 5 rows, expected 4", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture, request ActivateNetworkDataPlaneRequest) {
			mustProjectStoreReadExec(t, fixture.database, "PRAGMA foreign_keys = OFF")
			row := models.NetworkSetupEvidence{
				NetworkStateId: 2,
				Component:      string(NetworkSetupComponentLowPorts),
				Evidence:       "foreign low-port proof",
				Generation:     2,
				VerifiedAt:     request.At,
			}
			if err := fixture.database.Create(&row).Error; err != nil {
				t.Fatalf("insert extra setup proof: %v", err)
			}
			mustProjectStoreReadExec(t, fixture.database, "PRAGMA foreign_keys = ON")
		}},
		{name: "low-port proof after update", want: "must not be after the network state update time", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture, request ActivateNetworkDataPlaneRequest) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE network_setup_evidence SET verified_at = ? WHERE component = ?",
				request.At.Add(time.Second),
				NetworkSetupComponentLowPorts,
			)
		}},
		{name: "missing listener", want: "found 2 rows, expected 3", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture, _ ActivateNetworkDataPlaneRequest) {
			mustProjectStoreReadExec(t, fixture.database, "DELETE FROM network_shared_listeners WHERE kind = 'http'")
		}},
		{name: "extra listener", want: "found 4 rows, expected 3", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture, request ActivateNetworkDataPlaneRequest) {
			mustProjectStoreReadExec(t, fixture.database, "PRAGMA foreign_keys = OFF")
			row := models.NetworkSharedListener{
				NetworkStateId:    2,
				Kind:              "http",
				Mode:              string(ListenerModeDirect),
				AdvertisedAddress: "127.0.0.2",
				AdvertisedPort:    28080,
				BindAddress:       "127.0.0.2",
				BindPort:          28080,
				Generation:        2,
				VerifiedAt:        request.At,
			}
			if err := fixture.database.Create(&row).Error; err != nil {
				t.Fatalf("insert extra listener: %v", err)
			}
			mustProjectStoreReadExec(t, fixture.database, "PRAGMA foreign_keys = ON")
		}},
		{name: "listener after update", want: "must not be after the network state update time", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture, request ActivateNetworkDataPlaneRequest) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE network_shared_listeners SET verified_at = ? WHERE kind = 'https'",
				request.At.Add(time.Second),
			)
		}},
		{name: "listener policy drift", want: "does not match the authorized network data-plane policy", mutate: func(t *testing.T, fixture *networkDataPlaneSetupProjectionFixture, _ ActivateNetworkDataPlaneRequest) {
			mustProjectStoreReadExec(
				t,
				fixture.database,
				"UPDATE network_shared_listeners SET bind_port = 18081 WHERE kind = 'http'",
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := newFullNetworkDataPlaneSetupProjectionFixture(t)
			test.mutate(t, fixture, request)
			projected, err := fixture.source.Resolve(context.Background(), fixture.request.Policy)
			if err == nil || projected != (NetworkDataPlaneSetupProjection{}) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() = %#v, %v, want corruption containing %q", projected, err, test.want)
			}
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) {
				t.Fatalf("Resolve() error = %v, want CorruptStateError", err)
			}
		})
	}
}

// newNetworkDataPlaneSetupProjectionFixture creates a complete direct resolver-stage persistence fixture.
func newNetworkDataPlaneSetupProjectionFixture(t *testing.T) *networkDataPlaneSetupProjectionFixture {
	t.Helper()
	store, databaseConnection := newNetworkInitializeTestHarness(t, false)
	_, identity := initializeNetworkDataPlaneActivationIdentity(t, store, databaseConnection)
	request := networkResolverActivationTestRequest(t, identity.Record.Revision)
	_, err := store.ActivateNetworkResolver(context.Background(), request)
	if err != nil {
		t.Fatalf("ActivateNetworkResolver() fixture error = %v", err)
	}
	return &networkDataPlaneSetupProjectionFixture{
		store:    store,
		database: databaseConnection,
		source:   NewNetworkDataPlaneSetupProjectionSource(store.networkState),
		request:  request,
	}
}

// newFullNetworkDataPlaneSetupProjectionFixture advances one direct fixture to exact full-stage recovery state.
func newFullNetworkDataPlaneSetupProjectionFixture(
	t *testing.T,
) (*networkDataPlaneSetupProjectionFixture, ActivateNetworkDataPlaneRequest) {
	t.Helper()
	fixture := newNetworkDataPlaneSetupProjectionFixture(t)
	resolver, err := fixture.source.Resolve(context.Background(), fixture.request.Policy)
	if err != nil {
		t.Fatalf("resolve full fixture predecessor: %v", err)
	}
	request := networkDataPlaneActivationTestRequest(t, resolver.NetworkRevision)
	request.ConfirmedOwnership = resolver.ConfirmedOwnership
	request.Policy = fixture.request.Policy
	request.Setup[0] = resolver.ResolverProof
	request.At = resolver.NetworkUpdatedAt.Add(time.Minute)
	request.Setup[1].VerifiedAt = request.At
	request.Listeners.DNS.VerifiedAt = request.At
	request.Listeners.HTTP.VerifiedAt = request.At
	request.Listeners.HTTPS.VerifiedAt = request.At
	if _, err := fixture.store.ActivateNetworkDataPlane(context.Background(), request); err != nil {
		t.Fatalf("activate full projection fixture: %v", err)
	}
	return fixture, request
}
