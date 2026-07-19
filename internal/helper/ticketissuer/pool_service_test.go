package ticketissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/internal/platform/loopback"
)

// poolPlanSource scripts both durable reads and optionally blocks the first one for lifecycle tests.
type poolPlanSource struct {
	plans     []PoolPlan
	errors    []error
	requests  []PoolRequest
	started   chan struct{}
	release   chan struct{}
	blockOnce sync.Once
}

// Resolve returns the next durable pool plan without retaining caller-mutable authority.
func (source *poolPlanSource) Resolve(_ context.Context, request PoolRequest) (PoolPlan, error) {
	source.requests = append(source.requests, request)
	index := len(source.requests) - 1
	if source.started != nil {
		source.blockOnce.Do(func() {
			close(source.started)
			<-source.release
		})
	}
	if index < len(source.errors) && source.errors[index] != nil {
		return PoolPlan{}, source.errors[index]
	}
	if len(source.plans) == 0 {
		return PoolPlan{}, errors.New("pool plan script is empty")
	}
	if index >= len(source.plans) {
		index = len(source.plans) - 1
	}
	return source.plans[index], nil
}

// poolKeyStore records whether issuance loaded existing authority or provisioned first-claim authority.
type poolKeyStore struct {
	key         ed25519.PrivateKey
	loadErr     error
	createErr   error
	loadCalls   int
	createCalls int
}

// closingPoolKeyStore adapts a scripted key store to the default-opening lifecycle contract.
type closingPoolKeyStore struct {
	PoolKeyStore
	closeCalls int
}

// Close records release of one default-opened signing-key store.
func (store *closingPoolKeyStore) Close() error {
	store.closeCalls++
	return nil
}

// closingPoolPublisher adapts a scripted publisher to the default-opening lifecycle contract.
type closingPoolPublisher struct {
	Publisher
	closeCalls int
}

// Close records release of one default-opened pending-ticket publisher.
func (publisher *closingPoolPublisher) Close() error {
	publisher.closeCalls++
	return nil
}

// Load returns the scripted established repair key.
func (store *poolKeyStore) Load(context.Context) (ed25519.PrivateKey, error) {
	store.loadCalls++
	return append(ed25519.PrivateKey(nil), store.key...), store.loadErr
}

// LoadOrCreate returns the scripted first-claim bootstrap key.
func (store *poolKeyStore) LoadOrCreate(context.Context) (ed25519.PrivateKey, error) {
	store.createCalls++
	return append(ed25519.PrivateKey(nil), store.key...), store.createErr
}

// poolLoopbackObserver returns independently scripted native facts for every exact pool address.
type poolLoopbackObserver struct {
	observations map[netip.Addr]loopback.Observation
	errors       map[netip.Addr]error
	calls        []netip.Addr
}

// Observe records canonical address order before returning its exact scripted facts.
func (observer *poolLoopbackObserver) Observe(_ context.Context, address netip.Addr) (loopback.Observation, error) {
	observer.calls = append(observer.calls, address)
	if err := observer.errors[address]; err != nil {
		return loopback.Observation{}, err
	}
	return observer.observations[address], nil
}

// poolConflictCall captures the route-only candidate and authenticated requester.
type poolConflictCall struct {
	address      netip.Addr
	requester    string
	requirements []hostconflict.SocketRequirement
}

// poolConflictObserver returns independently scripted pre-assignment facts for absent identities.
type poolConflictObserver struct {
	observations map[netip.Addr]hostconflict.Observation
	errors       map[netip.Addr]error
	calls        []poolConflictCall
}

// Observe records the immutable request before returning its exact scripted facts.
func (observer *poolConflictObserver) Observe(_ context.Context, request hostconflict.Request, requester string) (hostconflict.Observation, error) {
	address := request.Candidate()
	observer.calls = append(observer.calls, poolConflictCall{
		address:      address,
		requester:    requester,
		requirements: request.Requirements(),
	})
	if err := observer.errors[address]; err != nil {
		return hostconflict.Observation{}, err
	}
	return observer.observations[address], nil
}

// poolIssuerFixture contains one complete durable plan and every independently replaceable authority boundary.
type poolIssuerFixture struct {
	now       time.Time
	request   PoolRequest
	plan      PoolPlan
	private   ed25519.PrivateKey
	plans     *poolPlanSource
	keys      *poolKeyStore
	publisher *capturingPublisher
	loopback  *poolLoopbackObserver
	conflicts *poolConflictObserver
	service   *PoolService
}

// TestPoolServiceIssueBootstrapBindsExactPoolAuthority proves a fresh bootstrap emits only eight absent route-only identities.
func TestPoolServiceIssueBootstrapBindsExactPoolAuthority(t *testing.T) {
	fixture := newPoolIssuerFixture(t, PoolModeBootstrap)
	result, err := fixture.service.Issue(t.Context(), fixture.plan.Ownership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := result.Validate(fixture.now); err != nil {
		t.Fatalf("PoolResult.Validate() error = %v", err)
	}
	if result.OperationID != fixture.plan.OperationID || result.Operation != helper.OperationEnsureLoopbackPool || result.Pool != fixture.plan.Pool.Prefix() {
		t.Fatalf("Issue() result = %#v", result)
	}
	if len(fixture.plans.requests) != 2 || fixture.keys.createCalls != 1 || fixture.keys.loadCalls != 0 || len(fixture.loopback.calls) != 8 || len(fixture.conflicts.calls) != 8 || fixture.publisher.calls != 1 {
		t.Fatalf("bootstrap calls = plans %d create/load %d/%d loopback %d conflicts %d publisher %d", len(fixture.plans.requests), fixture.keys.createCalls, fixture.keys.loadCalls, len(fixture.loopback.calls), len(fixture.conflicts.calls), fixture.publisher.calls)
	}

	ticket := fixture.publisher.ticket
	if ticket.Operation != helper.OperationEnsureLoopbackPool || ticket.InstallationID != fixture.plan.Ownership.InstallationID || ticket.RequesterIdentity != fixture.plan.Ownership.OwnerIdentity || ticket.OwnershipGeneration != 1 || ticket.OwnershipSchemaVersion != ownership.IdentitySchemaVersion || ticket.NetworkPolicyFingerprint != "" || ticket.ApprovedPool != fixture.plan.Ownership.LoopbackPoolPrefix {
		t.Fatalf("published ownership authority = %#v", ticket)
	}
	if ticket.ApprovedAddress != "" || ticket.ExpectedObservation != (helper.ExpectedObservation{}) || ticket.ExpectedPreAssignment != nil || ticket.ExpectedLoopbackPool == nil {
		t.Fatalf("published ticket mixed scalar and pool authority: %#v", ticket)
	}
	addresses := fixture.plan.Pool.Candidates()
	for index, expected := range ticket.ExpectedLoopbackPool.Identities {
		if expected.Address != addresses[index].String() || expected.ExpectedObservation.State != helper.ObservationAbsent || expected.ExpectedPreAssignment == nil || expected.ExpectedPreAssignment.Requirements == nil || len(expected.ExpectedPreAssignment.Requirements) != 0 {
			t.Fatalf("identity %d = %#v", index, expected)
		}
		call := fixture.conflicts.calls[index]
		if call.address != addresses[index] || call.requester != fixture.plan.Ownership.OwnerIdentity || len(call.requirements) != 0 {
			t.Fatalf("conflict call %d = %#v", index, call)
		}
	}
	if ticket.Nonce != strings.Repeat("5a", ticketNonceBytes) || ticket.ExpiresAt != fixture.now.Add(helper.MaxTicketLifetime) {
		t.Fatalf("published nonce/expiry = %q / %s", ticket.Nonce, ticket.ExpiresAt)
	}
	if !bytes.Equal(fixture.publisher.key, fixture.private) {
		t.Fatal("publisher received a different bootstrap key")
	}
}

// TestPoolServiceIssueBootstrapRecoveryBindsMixedPostconditions proves a generation-one retry can describe already repaired identities without inferring protected ownership.
func TestPoolServiceIssueBootstrapRecoveryBindsMixedPostconditions(t *testing.T) {
	fixture := newPoolIssuerFixture(t, PoolModeBootstrap)
	ownedIndexes := map[int]struct{}{1: {}, 4: {}, 7: {}}
	for index, address := range fixture.plan.Pool.Candidates() {
		if _, owned := ownedIndexes[index]; owned {
			fixture.loopback.observations[address] = poolLoopbackObservation(address, loopback.StateExact)
		}
	}

	result, err := fixture.service.Issue(t.Context(), fixture.plan.Ownership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if result.Operation != helper.OperationEnsureLoopbackPool || fixture.keys.createCalls != 1 || fixture.keys.loadCalls != 0 || len(fixture.conflicts.calls) != 5 || fixture.publisher.calls != 1 {
		t.Fatalf("bootstrap recovery result/calls = %#v create/load %d/%d conflicts %d publisher %d", result, fixture.keys.createCalls, fixture.keys.loadCalls, len(fixture.conflicts.calls), fixture.publisher.calls)
	}
	for index, expected := range fixture.publisher.ticket.ExpectedLoopbackPool.Identities {
		_, owned := ownedIndexes[index]
		if owned {
			if expected.ExpectedObservation.State != helper.ObservationOwned || expected.ExpectedPreAssignment != nil {
				t.Fatalf("owned identity %d = %#v", index, expected)
			}
			continue
		}
		if expected.ExpectedObservation.State != helper.ObservationAbsent || expected.ExpectedPreAssignment == nil || len(expected.ExpectedPreAssignment.Requirements) != 0 {
			t.Fatalf("absent identity %d = %#v", index, expected)
		}
	}
}

// TestPoolServiceIssueRepairBindsMixedPostconditions proves repair observes conflicts only for missing identities.
func TestPoolServiceIssueRepairBindsMixedPostconditions(t *testing.T) {
	fixture := newPoolIssuerFixture(t, PoolModeRepair)
	fixture.plan.Ownership.SchemaVersion = ownership.NetworkPolicySchemaVersion
	fixture.plan.Ownership.NetworkPolicyFingerprint = strings.Repeat("c", 64)
	fixture.plans.plans = []PoolPlan{fixture.plan}
	ownedIndexes := map[int]struct{}{1: {}, 4: {}, 7: {}}
	for index, address := range fixture.plan.Pool.Candidates() {
		if _, owned := ownedIndexes[index]; owned {
			fixture.loopback.observations[address] = poolLoopbackObservation(address, loopback.StateExact)
		}
	}

	result, err := fixture.service.Issue(t.Context(), fixture.plan.Ownership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if result.Operation != helper.OperationEnsureLoopbackPool || fixture.keys.loadCalls != 1 || fixture.keys.createCalls != 0 || len(fixture.conflicts.calls) != 5 || fixture.publisher.calls != 1 {
		t.Fatalf("repair result/calls = %#v load/create %d/%d conflicts %d publisher %d", result, fixture.keys.loadCalls, fixture.keys.createCalls, len(fixture.conflicts.calls), fixture.publisher.calls)
	}
	if fixture.publisher.ticket.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion || fixture.publisher.ticket.NetworkPolicyFingerprint != fixture.plan.Ownership.NetworkPolicyFingerprint {
		t.Fatalf("repair ticket policy ownership = %#v", fixture.publisher.ticket)
	}
	for index, expected := range fixture.publisher.ticket.ExpectedLoopbackPool.Identities {
		_, owned := ownedIndexes[index]
		if owned {
			if expected.ExpectedObservation.State != helper.ObservationOwned || expected.ExpectedPreAssignment != nil {
				t.Fatalf("owned identity %d = %#v", index, expected)
			}
			continue
		}
		if expected.ExpectedObservation.State != helper.ObservationAbsent || expected.ExpectedPreAssignment == nil || len(expected.ExpectedPreAssignment.Requirements) != 0 {
			t.Fatalf("absent identity %d = %#v", index, expected)
		}
	}
}

// TestPoolServiceIssueFailsClosed covers the independently trusted plan, observation, key, and publication boundaries.
func TestPoolServiceIssueFailsClosed(t *testing.T) {
	sentinel := errors.New("pool issuer sentinel")
	tests := []struct {
		name      string
		mode      PoolMode
		mutate    func(*poolIssuerFixture)
		contains  string
		publishes bool
	}{
		{name: "plan read", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) { f.plans.errors = []error{sentinel} }, contains: "resolve approval plan"},
		{name: "changed plan", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			changed := f.plan
			changed.OperationRevision++
			f.plans.plans = []PoolPlan{f.plan, changed}
		}, contains: "plan changed"},
		{name: "wrong requester", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			changed := f.plan
			changed.Ownership.OwnerIdentity = "2000"
			f.plans.plans = []PoolPlan{changed}
		}, contains: "authenticated requester"},
		{name: "unsupported mode", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			f.plan.Mode = "replacement"
			f.plans.plans = []PoolPlan{f.plan}
		}, contains: "mode"},
		{name: "bootstrap generation", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			f.plan.Ownership.Generation = 2
			f.plans.plans = []PoolPlan{f.plan}
		}, contains: "generation 1"},
		{name: "bootstrap ownership schema", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			f.plan.Ownership.SchemaVersion = ownership.NetworkPolicySchemaVersion
			f.plan.Ownership.NetworkPolicyFingerprint = strings.Repeat("c", 64)
			f.plans.plans = []PoolPlan{f.plan}
		}, contains: "ownership schema version"},
		{name: "bootstrap key", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) { f.keys.createErr = sentinel }, contains: "load or create bootstrap signing key"},
		{name: "repair key", mode: PoolModeRepair, mutate: func(f *poolIssuerFixture) { f.keys.loadErr = sentinel }, contains: "load established signing key"},
		{name: "key mismatch", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) { f.keys.key = deterministicPrivateKey(9) }, contains: "does not match machine ownership"},
		{name: "loopback read", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) { f.loopback.errors[f.plan.Pool.Candidates()[2]] = sentinel }, contains: "observe loopback assignment"},
		{name: "loopback address", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			address := f.plan.Pool.Candidates()[2]
			f.loopback.observations[address] = poolLoopbackObservation(f.plan.Pool.Candidates()[3], loopback.StateAbsent)
		}, contains: "does not match"},
		{name: "conflict read", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) { f.conflicts.errors[f.plan.Pool.Candidates()[4]] = sentinel }, contains: "observe pre-assignment conflicts"},
		{name: "conflict request", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			addresses := f.plan.Pool.Candidates()
			f.conflicts.observations[addresses[4]] = poolSafeHostObservation(t, addresses[5])
		}, contains: "does not match route-only request"},
		{name: "conflict indeterminate", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			address := f.plan.Pool.Candidates()[4]
			observation := f.conflicts.observations[address]
			observation.Routes.Complete = false
			f.conflicts.observations[address] = observation
		}, contains: "pre-assignment state"},
		{name: "entropy", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) { f.service.entropy = errorReader{err: sentinel} }, contains: "generate nonce"},
		{name: "publication", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) {
			f.publisher.reference = helper.TicketReference(strings.Repeat("b", 64))
			f.publisher.err = sentinel
		}, contains: "publish capability", publishes: true},
		{name: "invalid published reference", mode: PoolModeBootstrap, mutate: func(f *poolIssuerFixture) { f.publisher.reference = "bad" }, contains: "invalid result", publishes: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPoolIssuerFixture(t, test.mode)
			test.mutate(fixture)
			result, err := fixture.service.Issue(t.Context(), fixture.plan.Ownership.OwnerIdentity, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Issue() = %#v, %v, want %q", result, err, test.contains)
			}
			if result != (PoolResult{}) || fixture.publisher.calls != boolInt(test.publishes) {
				t.Fatalf("failed Issue() result/publisher = %#v / %d", result, fixture.publisher.calls)
			}
		})
	}
}

// TestPoolContractsAndPlanComparison covers public validation and every durable comparison dimension without host effects.
func TestPoolContractsAndPlanComparison(t *testing.T) {
	fixture := newPoolIssuerFixture(t, PoolModeBootstrap)
	if err := fixture.request.Validate(); err != nil {
		t.Fatalf("PoolRequest.Validate() error = %v", err)
	}
	if err := fixture.plan.Validate(); err != nil {
		t.Fatalf("PoolPlan.Validate() error = %v", err)
	}
	validResult := PoolResult{
		OperationID: fixture.plan.OperationID,
		Reference:   fixture.publisher.reference,
		Operation:   helper.OperationEnsureLoopbackPool,
		Pool:        fixture.plan.Pool.Prefix(),
		ExpiresAt:   fixture.now.Add(helper.MaxTicketLifetime),
	}
	if err := validResult.Validate(fixture.now); err != nil {
		t.Fatalf("PoolResult.Validate() error = %v", err)
	}

	invalidPlans := []func(*PoolPlan){
		func(plan *PoolPlan) { plan.OperationRevision = 0 },
		func(plan *PoolPlan) { plan.OperationState = domain.OperationRunning },
		func(plan *PoolPlan) { plan.Mode = "replacement" },
		func(plan *PoolPlan) { plan.Ownership.Generation = 2 },
		func(plan *PoolPlan) { plan.Ownership.TicketVerifierKey = "bad" },
		func(plan *PoolPlan) { plan.Ownership.LoopbackPoolPrefix = "127.77.0.16/29" },
		func(plan *PoolPlan) {
			plan.Pool = mustIdentityPool(t, "127.77.0.8/29", 7)
		},
		func(plan *PoolPlan) {
			plan.Pool = mustIdentityPool(t, "127.77.0.0/24", 8)
			plan.Ownership.LoopbackPoolPrefix = "127.77.0.0/24"
		},
	}
	for index, mutate := range invalidPlans {
		plan := fixture.plan
		mutate(&plan)
		if err := plan.Validate(); err == nil {
			t.Fatalf("invalid plan %d passed validation", index)
		}
	}

	comparisonMutations := []func(*PoolPlan){
		func(plan *PoolPlan) { plan.OperationID = "operation-other" },
		func(plan *PoolPlan) { plan.OperationRevision++ },
		func(plan *PoolPlan) { plan.OperationState = domain.OperationRunning },
		func(plan *PoolPlan) { plan.Mode = PoolModeRepair },
		func(plan *PoolPlan) { plan.Ownership.Generation++ },
		func(plan *PoolPlan) {
			plan.Pool = mustIdentityPool(t, "127.77.0.16/29", 8)
			plan.Ownership.LoopbackPoolPrefix = "127.77.0.16/29"
		},
	}
	for index, mutate := range comparisonMutations {
		changed := fixture.plan
		mutate(&changed)
		if samePoolPlan(fixture.plan, changed) {
			t.Fatalf("comparison mutation %d was ignored", index)
		}
	}
}

// TestPoolServiceLifecycleAndDependencies verifies cancellation, serialized close, and fail-fast construction.
func TestPoolServiceLifecycleAndDependencies(t *testing.T) {
	fixture := newPoolIssuerFixture(t, PoolModeBootstrap)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := fixture.service.Issue(cancelled, fixture.plan.Ownership.OwnerIdentity, fixture.request); !errors.Is(err, context.Canceled) || result != (PoolResult{}) {
		t.Fatalf("Issue(cancelled) = %#v, %v", result, err)
	}
	if len(fixture.plans.requests) != 0 {
		t.Fatalf("cancelled issue resolved %d plans", len(fixture.plans.requests))
	}
	closed := 0
	fixture.service.closeStore = func() error { closed++; return nil }
	if err := fixture.service.Close(); err != nil || closed != 1 {
		t.Fatalf("Close() = %v, count %d", err, closed)
	}
	if err := fixture.service.Close(); err != nil || closed != 1 {
		t.Fatalf("Close(replay) = %v, count %d", err, closed)
	}
	if result, err := fixture.service.Issue(t.Context(), fixture.plan.Ownership.OwnerIdentity, fixture.request); err == nil || result != (PoolResult{}) {
		t.Fatalf("Issue(closed) = %#v, %v", result, err)
	}

	if _, err := OpenDefaultPoolService(nil); err == nil {
		t.Fatal("OpenDefaultPoolService(nil) error = nil")
	}
	constructors := []func(){
		func() {
			NewPoolService(nil, fixture.keys, fixture.publisher, fixture.loopback, fixture.conflicts, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewPoolService(fixture.plans, nil, fixture.publisher, fixture.loopback, fixture.conflicts, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewPoolService(fixture.plans, fixture.keys, nil, fixture.loopback, fixture.conflicts, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewPoolService(fixture.plans, fixture.keys, fixture.publisher, nil, fixture.conflicts, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewPoolService(fixture.plans, fixture.keys, fixture.publisher, fixture.loopback, nil, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewPoolService(fixture.plans, fixture.keys, fixture.publisher, fixture.loopback, fixture.conflicts, nil, bytes.NewReader(nil))
		},
		func() {
			NewPoolService(fixture.plans, fixture.keys, fixture.publisher, fixture.loopback, fixture.conflicts, fixedClock{now: fixture.now}, nil)
		},
	}
	for index, construct := range constructors {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("constructor %d did not panic", index)
				}
			}()
			construct()
		}()
	}
}

// TestOpenDefaultPoolServiceOpensOnlyDaemonStores proves default pool issuance has no protected ownership-store dependency.
func TestOpenDefaultPoolServiceOpensOnlyDaemonStores(t *testing.T) {
	fixture := newPoolIssuerFixture(t, PoolModeBootstrap)
	if _, err := openDefaultPoolService(fixture.plans, poolDefaultOpeners{}); err == nil || !strings.Contains(err.Error(), "openers are incomplete") {
		t.Fatalf("openDefaultPoolService(incomplete) error = %v", err)
	}

	sentinel := errors.New("default pool store sentinel")
	publisherOpens := 0
	if _, err := openDefaultPoolService(fixture.plans, poolDefaultOpeners{
		openKeys: func() (poolKeyStoreCloser, error) { return nil, sentinel },
		openPublisher: func() (poolPublisherCloser, error) {
			publisherOpens++
			return nil, nil
		},
	}); !errors.Is(err, sentinel) || publisherOpens != 0 {
		t.Fatalf("openDefaultPoolService(key failure) error/publisher opens = %v / %d", err, publisherOpens)
	}

	failedKeys := &closingPoolKeyStore{PoolKeyStore: fixture.keys}
	if _, err := openDefaultPoolService(fixture.plans, poolDefaultOpeners{
		openKeys: func() (poolKeyStoreCloser, error) { return failedKeys, nil },
		openPublisher: func() (poolPublisherCloser, error) {
			return nil, sentinel
		},
	}); !errors.Is(err, sentinel) || failedKeys.closeCalls != 1 {
		t.Fatalf("openDefaultPoolService(publisher failure) error/key closes = %v / %d", err, failedKeys.closeCalls)
	}

	keys := &closingPoolKeyStore{PoolKeyStore: fixture.keys}
	publisher := &closingPoolPublisher{Publisher: fixture.publisher}
	keyOpens := 0
	publisherOpens = 0
	service, err := openDefaultPoolService(fixture.plans, poolDefaultOpeners{
		openKeys: func() (poolKeyStoreCloser, error) {
			keyOpens++
			return keys, nil
		},
		openPublisher: func() (poolPublisherCloser, error) {
			publisherOpens++
			return publisher, nil
		},
	})
	if err != nil {
		t.Fatalf("openDefaultPoolService() error = %v", err)
	}
	if keyOpens != 1 || publisherOpens != 1 {
		t.Fatalf("openDefaultPoolService() opens = keys %d publisher %d", keyOpens, publisherOpens)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := service.Close(); err != nil || keys.closeCalls != 1 || publisher.closeCalls != 1 {
		t.Fatalf("Close(replay) = %v, key/publisher closes %d/%d", err, keys.closeCalls, publisher.closeCalls)
	}
}

// TestPoolServiceCloseWaitsForIssuance verifies stores cannot close inside one serialized publication.
func TestPoolServiceCloseWaitsForIssuance(t *testing.T) {
	fixture := newPoolIssuerFixture(t, PoolModeBootstrap)
	fixture.plans.started = make(chan struct{})
	fixture.plans.release = make(chan struct{})
	issueDone := make(chan error, 1)
	go func() {
		_, err := fixture.service.Issue(t.Context(), fixture.plan.Ownership.OwnerIdentity, fixture.request)
		issueDone <- err
	}()
	<-fixture.plans.started

	closed := make(chan struct{})
	fixture.service.closeStore = func() error { close(closed); return nil }
	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.service.Close() }()
	select {
	case <-closed:
		t.Fatal("Close() entered stores while issuance was blocked")
	default:
	}
	close(fixture.plans.release)
	if err := <-issueDone; err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// newPoolIssuerFixture builds one valid all-absent bootstrap or repair issuance graph.
func newPoolIssuerFixture(t *testing.T, mode PoolMode) *poolIssuerFixture {
	t.Helper()
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	privateKey := deterministicPrivateKey(7)
	generation := uint64(7)
	if mode == PoolModeBootstrap {
		generation = 1
	}
	pool := mustIdentityPool(t, "127.77.0.8/29", 8)
	record := ownership.Record{
		SchemaVersion:      ownership.CurrentSchemaVersion,
		InstallationID:     "harbor-pool-test",
		OwnerIdentity:      "1000",
		Generation:         generation,
		LoopbackPoolPrefix: pool.Prefix().String(),
		TicketVerifierKey:  base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
	}
	plan := PoolPlan{
		OperationID:       "operation-pool-test",
		OperationRevision: 11,
		OperationState:    domain.OperationRequiresApproval,
		Mode:              mode,
		Ownership:         record,
		Pool:              pool,
	}
	plans := &poolPlanSource{plans: []PoolPlan{plan}}
	keys := &poolKeyStore{key: privateKey}
	publisher := &capturingPublisher{reference: helper.TicketReference(strings.Repeat("a", 64))}
	loopbackObserver := &poolLoopbackObserver{
		observations: make(map[netip.Addr]loopback.Observation),
		errors:       make(map[netip.Addr]error),
	}
	conflictObserver := &poolConflictObserver{
		observations: make(map[netip.Addr]hostconflict.Observation),
		errors:       make(map[netip.Addr]error),
	}
	for _, address := range pool.Candidates() {
		loopbackObserver.observations[address] = poolLoopbackObservation(address, loopback.StateAbsent)
		conflictObserver.observations[address] = poolSafeHostObservation(t, address)
	}
	service := NewPoolService(
		plans,
		keys,
		publisher,
		loopbackObserver,
		conflictObserver,
		fixedClock{now: now},
		bytes.NewReader(bytes.Repeat([]byte{0x5a}, ticketNonceBytes*2)),
	)
	return &poolIssuerFixture{
		now: now, request: PoolRequest{OperationID: plan.OperationID}, plan: plan, private: privateKey,
		plans: plans, keys: keys, publisher: publisher,
		loopback: loopbackObserver, conflicts: conflictObserver, service: service,
	}
}

// mustIdentityPool constructs the requested leading subset of one canonical prefix.
func mustIdentityPool(t *testing.T, prefixText string, count int) identity.Pool {
	t.Helper()
	prefix := netip.MustParsePrefix(prefixText)
	addresses := make([]netip.Addr, 0, count)
	address := prefix.Addr()
	for range count {
		addresses = append(addresses, address)
		address = address.Next()
	}
	pool, err := identity.NewPool(prefix, addresses)
	if err != nil {
		t.Fatalf("identity.NewPool() error = %v", err)
	}
	return pool
}

// poolLoopbackObservation returns valid Linux assignment facts for one absent or exact address.
func poolLoopbackObservation(address netip.Addr, state loopback.State) loopback.Observation {
	observation := loopback.Observation{
		Address: address,
		Loopback: loopback.InterfaceFact{
			Name: "lo", Index: 1, Kind: loopback.InterfaceKindLinuxNative, NativeLoopback: true,
		},
		State:       loopback.StateAbsent,
		Assignments: []loopback.AssignmentFact{},
	}
	if state == loopback.StateExact {
		observation.State = loopback.StateExact
		observation.Assignments = []loopback.AssignmentFact{{
			Address: address, PrefixLength: 32, InterfaceName: "lo", InterfaceIndex: 1,
			NativeLoopback: true, InterfaceKind: loopback.InterfaceKindLinuxNative,
			Linux: &loopback.LinuxAssignmentFact{
				Scope: loopback.LinuxAddressScopeHost, Flags: 1 << 7, Label: "lo", AddressMatchesLocal: true,
				CacheInfoPresent: true, ValidLifetimeSeconds: ^uint32(0), PreferredLifetimeSeconds: ^uint32(0),
			},
		}}
	}
	return observation
}

// poolSafeHostObservation returns one complete route-only Linux baseline for an exact address.
func poolSafeHostObservation(t *testing.T, address netip.Addr) hostconflict.Observation {
	t.Helper()
	request, err := hostconflict.NewPreAssignmentRequest(address, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := hostconflict.NewLinuxScope(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	interfaceIdentity := hostconflict.InterfaceIdentity{Name: "lo", Index: 1}
	baseline := hostconflict.RouteFact{
		Destination: netip.MustParsePrefix("127.0.0.0/8"), Interface: interfaceIdentity,
		NativeLoopback: true, Normalization: hostconflict.RouteNormalizationDirect,
	}
	return hostconflict.Observation{
		Request: request,
		Scope:   scope,
		Loopback: hostconflict.LoopbackIdentity{
			Interface: interfaceIdentity,
			Kind:      hostconflict.LoopbackKindLinuxNative,
		},
		Routes:  hostconflict.RouteSnapshot{Complete: true, Selected: &baseline, Matching: []hostconflict.RouteFact{baseline}},
		Sockets: hostconflict.SocketSnapshot{Complete: true, Endpoints: []hostconflict.SocketFact{}},
		Policy: hostconflict.PolicyFacts{Linux: &hostconflict.LinuxPolicyFacts{
			Complete: true,
			RouteLocalnet: []hostconflict.RouteLocalnetFact{{
				Interface: interfaceIdentity,
				Enabled:   false,
			}},
		}},
	}
}

var _ PoolPlanSource = (*poolPlanSource)(nil)
var _ PoolKeyStore = (*poolKeyStore)(nil)
var _ LoopbackObserver = (*poolLoopbackObserver)(nil)
var _ ConflictObserver = (*poolConflictObserver)(nil)
