package authority

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingStore provides immutable durable results with race-safe call accounting.
type recordingStore struct {
	sequence             domain.Sequence
	runtimeState         state.RuntimeState
	sequenceErr          error
	runtimeStateErr      error
	sequenceCalls        atomic.Int64
	runtimeStateCalls    atomic.Int64
	nilContexts          atomic.Int64
	registration         state.ProjectRegistration
	registrationErr      error
	registrationCalls    atomic.Int64
	registrationMu       sync.Mutex
	registrationProjects []domain.ProjectSnapshot
}

// httpRouteObserverFunc adapts one exact route predicate to the authority's observation boundary.
type httpRouteObserverFunc func(string, netip.AddrPort) bool

// HTTPRouteLive applies the configured exact route predicate.
func (observe httpRouteObserverFunc) HTTPRouteLive(host string, upstream netip.AddrPort) bool {
	return observe(host, upstream)
}

// recordingProjectLifecycle supplies explicit managed-process coordination to authority boundary tests.
type recordingProjectLifecycle struct {
	mutex              sync.Mutex
	startRecord        state.OperationRecord
	stopRecord         state.OperationRecord
	startErr           error
	stopErr            error
	activity           reconcile.ProjectActivity
	activityErr        error
	serviceLogs        reconcile.ProjectServiceLogs
	serviceLogsErr     error
	starts             []reconcile.ProjectStartRequest
	stops              []reconcile.ProjectStopRequest
	activities         []reconcile.ProjectActivityRequest
	serviceLogRequests []reconcile.ProjectServiceLogsRequest
}

// Start returns the configured durable start progress.

func (lifecycle *recordingProjectLifecycle) Start(_ context.Context, request reconcile.ProjectStartRequest) (state.OperationRecord, error) {
	lifecycle.mutex.Lock()
	lifecycle.starts = append(lifecycle.starts, request)
	lifecycle.mutex.Unlock()
	return lifecycle.startRecord, lifecycle.startErr
}

// Stop returns the configured durable stop progress.

func (lifecycle *recordingProjectLifecycle) Stop(_ context.Context, request reconcile.ProjectStopRequest) (state.OperationRecord, error) {
	lifecycle.mutex.Lock()
	lifecycle.stops = append(lifecycle.stops, request)
	lifecycle.mutex.Unlock()
	return lifecycle.stopRecord, lifecycle.stopErr
}

// ProjectActivity returns configured current-session activity while retaining the exact selection.
func (lifecycle *recordingProjectLifecycle) ProjectActivity(
	_ context.Context,
	request reconcile.ProjectActivityRequest,
) (reconcile.ProjectActivity, error) {
	lifecycle.mutex.Lock()
	lifecycle.activities = append(lifecycle.activities, request)
	lifecycle.mutex.Unlock()
	return lifecycle.activity, lifecycle.activityErr
}

// ServiceLogs returns configured service output while retaining the exact selection.
func (lifecycle *recordingProjectLifecycle) ServiceLogs(
	_ context.Context,
	request reconcile.ProjectServiceLogsRequest,
) (reconcile.ProjectServiceLogs, error) {
	lifecycle.mutex.Lock()
	lifecycle.serviceLogRequests = append(lifecycle.serviceLogRequests, request)
	lifecycle.mutex.Unlock()
	return lifecycle.serviceLogs, lifecycle.serviceLogsErr
}

// testProjectLifecycles provides a non-nil explicit collaborator to tests outside the lifecycle boundary.
func testProjectLifecycles() projectLifecycleCoordinator {
	return new(recordingProjectLifecycle)
}

// testHTTPRoutes supplies an initialized observer with no live public routes.
func testHTTPRoutes() httpRouteObserver {
	return httpRouteObserverFunc(func(string, netip.AddrPort) bool { return false })
}

// CurrentSequence returns the configured global sequence while preserving caller cancellation.
func (store *recordingStore) CurrentSequence(ctx context.Context) (domain.Sequence, error) {
	store.sequenceCalls.Add(1)
	if ctx == nil {
		store.nilContexts.Add(1)
		return 0, errors.New("current sequence received a nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if store.sequenceErr != nil {
		return 0, store.sequenceErr
	}

	return store.sequence, nil
}

// RuntimeState returns the configured atomic replacement while preserving caller cancellation.
func (store *recordingStore) RuntimeState(ctx context.Context) (state.RuntimeState, error) {
	store.runtimeStateCalls.Add(1)
	if ctx == nil {
		store.nilContexts.Add(1)
		return state.RuntimeState{}, errors.New("runtime state received a nil context")
	}
	if err := ctx.Err(); err != nil {
		return state.RuntimeState{}, err
	}
	if store.runtimeStateErr != nil {
		return state.RuntimeState{}, store.runtimeStateErr
	}

	return store.runtimeState, nil
}

// RegisterProject returns the configured atomic registration while preserving caller cancellation.
func (store *recordingStore) RegisterProject(ctx context.Context, project domain.ProjectSnapshot) (state.ProjectRegistration, error) {
	store.registrationCalls.Add(1)
	if ctx == nil {
		store.nilContexts.Add(1)
		return state.ProjectRegistration{}, errors.New("registration received a nil context")
	}
	if err := ctx.Err(); err != nil {
		return state.ProjectRegistration{}, err
	}
	if store.registrationErr != nil {
		return state.ProjectRegistration{}, store.registrationErr
	}
	store.registrationMu.Lock()
	store.registrationProjects = append(store.registrationProjects, project)
	store.registrationMu.Unlock()
	return store.registration, nil
}

// emptySnapshot returns a valid complete replacement with canonical initialized collections.
func emptySnapshot(sequence domain.Sequence) domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion:     domain.SnapshotSchemaVersion,
		Sequence:          sequence,
		CapturedAt:        time.Date(2026, time.July, 18, 12, 30, 0, 0, time.UTC),
		Projects:          []domain.ProjectSnapshot{},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{},
	}
}

// authorityRouteRuntimeState constructs the durable private resource and matching full-stage reservation used by projection tests.
func authorityRouteRuntimeState() state.RuntimeState {
	snapshot := emptySnapshot(17)
	snapshot.Projects = []domain.ProjectSnapshot{{
		ID:        "project-orders",
		Name:      "Orders",
		Path:      "/workspace/orders",
		Slug:      "orders",
		State:     domain.ProjectReady,
		UpdatedAt: snapshot.CapturedAt,
		Apps: []domain.AppSnapshot{{
			ID:       "app",
			Name:     "App",
			State:    domain.EntityReady,
			Active:   true,
			Required: true,
		}},
		Services: []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{{
			ID:   "app-http",
			Name: "App",
			Kind: "application",
			Owner: domain.ResourceOwner{
				Kind:  domain.ResourceOwnedByApp,
				AppID: "app",
			},
			URL: "http://127.77.0.10:3000",
		}},
	}}
	return state.RuntimeState{
		Snapshot:           snapshot,
		NetworkInitialized: true,
		Network: state.NetworkRecord{
			Stage: state.NetworkStageFull,
			Reservations: state.DataPlaneReservations{Endpoints: []state.EndpointReservation{{
				Key: state.EndpointReservationKey{
					ProjectID:  "project-orders",
					EndpointID: "app-http",
				},
				Protocol:   state.EndpointProtocolHTTP,
				Host:       "orders.test",
				Public:     netip.MustParseAddrPort("127.0.0.1:443"),
				Generation: 1,
			}}},
		},
	}
}

// controlCaller returns a negotiated control peer suitable for direct authority tests.
func controlCaller(capabilities []rpc.Capability) control.Caller {
	return control.Caller{Session: session.Peer{
		Role:         rpc.RoleCLI,
		BuildVersion: "v2.4.0",
		Protocol:     rpc.Version{Major: 1, Minor: 0},
		Capabilities: capabilities,
	}}
}

// TestNewAuthorityUsesCurrentBuild verifies production construction captures build identity with its complete required Store graph.
func TestNewAuthorityUsesCurrentBuild(t *testing.T) {
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close database connections: %v", err)
		}
	})
	store := state.NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		state.NewMutationCoordinator(connections),
	)
	want := buildinfo.Current()

	authority := NewAuthority(store, new(reconcile.ProjectUnregisterCoordinator), new(reconcile.ProjectLifecycleCoordinator), new(reconcile.NetworkSetupCoordinator), new(reconcile.NetworkResolverSetupCoordinator), new(harbordruntime.Controller))
	if authority == nil {
		t.Fatal("NewAuthority() returned nil")
	}
	if authority.build != want {
		t.Fatalf("NewAuthority() build = %#v, want %#v", authority.build, want)
	}
}

// TestAuthorityStatusMapsServingState verifies status comes only from the serving build, caller negotiation, and durable sequence.
func TestAuthorityStatusMapsServingState(t *testing.T) {
	store := &recordingStore{sequence: 42}
	build := buildinfo.Info{Version: "v3.2.1", Revision: "abc123", Modified: true}
	authority := newAuthority(store, testProjectUnregisterApprovals(), build, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	caller := controlCaller([]rpc.Capability{control.CapabilityV1, "events.v1"})

	status, err := authority.Status(context.Background(), caller)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	want := control.DaemonStatus{
		State: control.DaemonStateReady,
		Build: control.Build{
			Version:  build.Version,
			Revision: build.Revision,
			Modified: build.Modified,
		},
		Protocol:              caller.Session.Protocol,
		Capabilities:          []rpc.Capability{control.CapabilityV1, "events.v1"},
		SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:              store.sequence,
	}
	if !reflect.DeepEqual(status, want) {
		t.Fatalf("Status() = %#v, want %#v", status, want)
	}
	if got := store.sequenceCalls.Load(); got != 1 {
		t.Fatalf("CurrentSequence() calls = %d, want 1", got)
	}
	if got := store.runtimeStateCalls.Load(); got != 0 {
		t.Fatalf("RuntimeState() calls = %d, want 0", got)
	}
}

// TestAuthorityStatusReturnsFreshCanonicalCapabilities verifies caller-owned slices cannot alter status results across calls.
func TestAuthorityStatusReturnsFreshCanonicalCapabilities(t *testing.T) {
	store := &recordingStore{sequence: 9}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	capabilities := []rpc.Capability{"events.v1", control.CapabilityV1, "events.v1"}
	caller := controlCaller(capabilities)

	first, err := authority.Status(context.Background(), caller)
	if err != nil {
		t.Fatalf("first Status() error = %v", err)
	}
	want := []rpc.Capability{control.CapabilityV1, "events.v1"}
	if !reflect.DeepEqual(first.Capabilities, want) {
		t.Fatalf("first capabilities = %v, want %v", first.Capabilities, want)
	}
	capabilities[0] = "changed.v1"
	if !reflect.DeepEqual(first.Capabilities, want) {
		t.Fatalf("caller mutation changed first capabilities to %v", first.Capabilities)
	}

	first.Capabilities[0] = "response.changed.v1"
	caller.Session.Capabilities = []rpc.Capability{control.CapabilityV1, "events.v1"}
	second, err := authority.Status(context.Background(), caller)
	if err != nil {
		t.Fatalf("second Status() error = %v", err)
	}
	if !reflect.DeepEqual(second.Capabilities, want) {
		t.Fatalf("second capabilities = %v, want fresh %v", second.Capabilities, want)
	}
}

// TestAuthorityNormalizesNilContexts verifies both public reads give the Store a usable context.
func TestAuthorityNormalizesNilContexts(t *testing.T) {
	snapshot := emptySnapshot(7)
	store := &recordingStore{sequence: snapshot.Sequence, runtimeState: state.RuntimeState{Snapshot: snapshot}}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	caller := controlCaller([]rpc.Capability{control.CapabilityV1})

	if _, err := authority.Status(nil, caller); err != nil {
		t.Fatalf("Status(nil) error = %v", err)
	}
	if _, err := authority.Snapshot(nil, caller); err != nil {
		t.Fatalf("Snapshot(nil) error = %v", err)
	}
	if got := store.nilContexts.Load(); got != 0 {
		t.Fatalf("Store nil contexts = %d, want 0", got)
	}
}

// TestAuthorityPreservesStoreErrorsAndCancellation verifies control transport classification can inspect original causes.
func TestAuthorityPreservesStoreErrorsAndCancellation(t *testing.T) {
	statusFailure := errors.New("sequence unavailable")
	snapshotFailure := errors.New("snapshot unavailable")
	caller := controlCaller([]rpc.Capability{control.CapabilityV1})

	statusAuthority := newAuthority(&recordingStore{sequenceErr: statusFailure}, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	if _, err := statusAuthority.Status(context.Background(), caller); !errors.Is(err, statusFailure) {
		t.Fatalf("Status() error = %v, want %v", err, statusFailure)
	}

	snapshotAuthority := newAuthority(&recordingStore{runtimeStateErr: snapshotFailure}, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	if _, err := snapshotAuthority.Snapshot(context.Background(), caller); !errors.Is(err, snapshotFailure) {
		t.Fatalf("Snapshot() error = %v, want %v", err, snapshotFailure)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancelDeadline()
	for _, test := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{name: "cancelled", ctx: cancelled, want: context.Canceled},
		{name: "deadline", ctx: expired, want: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := newAuthority(&recordingStore{}, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
			if _, err := authority.Status(test.ctx, caller); !errors.Is(err, test.want) {
				t.Fatalf("Status() error = %v, want %v", err, test.want)
			}
			if _, err := authority.Snapshot(test.ctx, caller); !errors.Is(err, test.want) {
				t.Fatalf("Snapshot() error = %v, want %v", err, test.want)
			}
		})
	}
}

// TestAuthorityRejectsInvalidNegotiatedCapabilities verifies malformed direct calls do not emit invalid status data.
func TestAuthorityRejectsInvalidNegotiatedCapabilities(t *testing.T) {
	store := &recordingStore{sequence: 5}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	caller := controlCaller([]rpc.Capability{"bad capability"})

	if _, err := authority.Status(context.Background(), caller); err == nil {
		t.Fatal("Status() error = nil, want invalid capability error")
	}
	if got := store.sequenceCalls.Load(); got != 0 {
		t.Fatalf("CurrentSequence() calls = %d, want 0 after invalid negotiation", got)
	}
}

// TestAuthoritySnapshotPassesThroughStoreState verifies authority neither invents nor filters durable project state.
func TestAuthoritySnapshotPassesThroughStoreState(t *testing.T) {
	snapshot := emptySnapshot(17)
	store := &recordingStore{runtimeState: state.RuntimeState{Snapshot: snapshot}}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())

	got, err := authority.Snapshot(context.Background(), control.Caller{})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("Snapshot() = %#v, want exact Store snapshot %#v", got, snapshot)
	}
	if calls := store.runtimeStateCalls.Load(); calls != 1 {
		t.Fatalf("RuntimeState() calls = %d, want 1", calls)
	}
}

// TestAuthoritySnapshotProjectsOnlyExactLiveFullStageRoutes verifies public launch URLs fail closed at every durable and live join.
func TestAuthoritySnapshotProjectsOnlyExactLiveFullStageRoutes(t *testing.T) {
	privateUpstream := netip.MustParseAddrPort("127.77.0.10:3000")
	tests := []struct {
		name      string
		mutate    func(*state.RuntimeState)
		observe   httpRouteObserverFunc
		wantURL   string
		wantCalls int
	}{
		{
			name: "live projection",
			observe: func(host string, upstream netip.AddrPort) bool {
				return host == "orders.test" && upstream == privateUpstream
			},
			wantURL:   "https://orders.test",
			wantCalls: 1,
		},
		{
			name:    "route absent",
			observe: func(string, netip.AddrPort) bool { return false },
			wantURL: "http://127.77.0.10:3000", wantCalls: 1,
		},
		{
			name: "route inexact",
			observe: func(host string, upstream netip.AddrPort) bool {
				return host == "orders.test" && upstream == netip.MustParseAddrPort("127.77.0.10:3001")
			},
			wantURL: "http://127.77.0.10:3000", wantCalls: 1,
		},
		{
			name: "identity stage",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Network.Stage = state.NetworkStageIdentity
				runtimeState.Network.Reservations.Endpoints = []state.EndpointReservation{}
			},
			observe: func(string, netip.AddrPort) bool { return true },
			wantURL: "http://127.77.0.10:3000",
		},
		{
			name: "malformed reservation join",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Network.Reservations.Endpoints[0].Host = "legacy.orders.test"
			},
			observe: func(string, netip.AddrPort) bool { return true },
			wantURL: "http://127.77.0.10:3000",
		},
		{
			name: "noncanonical private origin",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Snapshot.Projects[0].Resources[0].URL = "http://127.77.0.10:3000/"
			},
			observe: func(string, netip.AddrPort) bool { return true },
			wantURL: "http://127.77.0.10:3000/",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeState := authorityRouteRuntimeState()
			if test.mutate != nil {
				test.mutate(&runtimeState)
			}
			original := cloneControlSnapshot(runtimeState.Snapshot)
			calls := 0
			observer := httpRouteObserverFunc(func(host string, upstream netip.AddrPort) bool {
				calls++
				return test.observe(host, upstream)
			})
			store := &recordingStore{runtimeState: runtimeState}
			authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), observer)

			got, err := authority.Snapshot(t.Context(), control.Caller{})
			if err != nil {
				t.Fatalf("Snapshot() error = %v", err)
			}
			if gotURL := got.Projects[0].Resources[0].URL; gotURL != test.wantURL {
				t.Fatalf("Snapshot() resource URL = %q, want %q", gotURL, test.wantURL)
			}
			if calls != test.wantCalls {
				t.Fatalf("HTTPRouteLive() calls = %d, want %d", calls, test.wantCalls)
			}
			if !reflect.DeepEqual(store.runtimeState.Snapshot, original) {
				t.Fatal("Snapshot() mutated the Store runtime snapshot")
			}
		})
	}
}

// TestAuthoritySupportsConcurrentReads verifies status and snapshot calls share no mutable response state.
func TestAuthoritySupportsConcurrentReads(t *testing.T) {
	const readers = 64
	snapshot := emptySnapshot(71)
	store := &recordingStore{sequence: snapshot.Sequence, runtimeState: state.RuntimeState{Snapshot: snapshot}}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "v1.0.0", Revision: "race-safe"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	caller := controlCaller([]rpc.Capability{control.CapabilityV1, "events.v1"})
	errorsFound := make(chan error, readers*2)
	start := make(chan struct{})
	var wait sync.WaitGroup

	for range readers {
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			status, err := authority.Status(context.Background(), caller)
			if err == nil && status.Sequence != snapshot.Sequence {
				err = errors.New("Status() returned the wrong sequence")
			}
			if err != nil {
				errorsFound <- err
			}
		}()
		go func() {
			defer wait.Done()
			<-start
			got, err := authority.Snapshot(context.Background(), caller)
			if err == nil && !reflect.DeepEqual(got, snapshot) {
				err = errors.New("Snapshot() returned different state")
			}
			if err != nil {
				errorsFound <- err
			}
		}()
	}

	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent authority read: %v", err)
	}
	if got := store.sequenceCalls.Load(); got != readers {
		t.Fatalf("CurrentSequence() calls = %d, want %d", got, readers)
	}
	if got := store.runtimeStateCalls.Load(); got != readers {
		t.Fatalf("RuntimeState() calls = %d, want %d", got, readers)
	}
}
