package harbordruntime

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
)

// TestDesiredHTTPStateFromRuntimeStateProjectsReadyAppRoute verifies the strict durable join and stable route identity.
func TestDesiredHTTPStateFromRuntimeStateProjectsReadyAppRoute(t *testing.T) {
	runtimeState := readyHTTPRuntimeState()

	desired, err := desiredHTTPStateFromRuntimeState(runtimeState)
	if err != nil {
		t.Fatalf("desiredHTTPStateFromRuntimeState() error = %v", err)
	}
	routes := desired.HTTPRoutes()
	want := dataplane.HTTPRoute{
		ID:       "orders:app-http",
		Host:     "orders.test",
		Upstream: netip.MustParseAddrPort("127.77.0.10:3000"),
	}
	if len(routes) != 1 || routes[0] != want {
		t.Fatalf("HTTPRoutes() = %#v, want %#v", routes, []dataplane.HTTPRoute{want})
	}
}

// TestDesiredHTTPStateFromRuntimeStateRejectsMalformedReadyJoins verifies readiness alone cannot authorize an ambiguous route.
func TestDesiredHTTPStateFromRuntimeStateRejectsMalformedReadyJoins(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*state.RuntimeState)
		want   string
	}{
		{
			name: "missing reservation",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Network.Reservations.Endpoints = []state.EndpointReservation{}
			},
			want: "app-http reservation is missing",
		},
		{
			name: "noncanonical host",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Network.Reservations.Endpoints[0].Host = "other.test"
			},
			want: "must equal \"orders.test\"",
		},
		{
			name: "noncanonical upstream",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Snapshot.Projects[0].Resources[0].URL = "http://127.77.0.10:3000/"
			},
			want: "canonical IPv4 loopback HTTP origin",
		},
		{
			name: "wrong primary address",
			mutate: func(runtimeState *state.RuntimeState) {
				runtimeState.Snapshot.Projects[0].Resources[0].URL = "http://127.77.0.11:3000"
			},
			want: "does not match primary lease",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeState := readyHTTPRuntimeState()
			test.mutate(&runtimeState)
			if err := runtimeState.Validate(); err != nil {
				t.Fatalf("malformed join fixture must remain structurally valid: %v", err)
			}
			if _, err := desiredHTTPStateFromRuntimeState(runtimeState); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("desiredHTTPStateFromRuntimeState() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestControllerStartProjectsReadyHTTPRoute verifies startup publishes routes before controller readiness.
func TestControllerStartProjectsReadyHTTPRoute(t *testing.T) {
	runtimeState := readyHTTPRuntimeState()
	source := runtimeStateTestSource(runtimeState)
	authority := &testCertificateAuthority{
		root: validTestRoot(),
		ensureLeaf: func(context.Context, string) (certificates.LeafResult, error) {
			return certificates.LeafResult{}, nil
		},
	}
	runtime := newTestDataPlane(nil)
	controller := newHTTPTestController(t, source, authority, runtime)

	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	replacements := replacementHTTPRoutes(runtime)
	if len(replacements) < 2 || len(replacements[len(replacements)-1]) != 1 {
		t.Fatalf("startup replacements = %#v, want withdrawal followed by one route", replacements)
	}
	if got := replacements[len(replacements)-1][0]; got.Host != "orders.test" || got.Upstream.String() != "127.77.0.10:3000" {
		t.Fatalf("startup route = %#v", got)
	}
	if got := ensuredLeafHosts(authority); len(got) != 1 || got[0] != "orders.test" {
		t.Fatalf("EnsureLeaf hosts = %v, want [orders.test]", got)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerHTTPRouteLiveRequiresReadyExactPublication verifies observation is lifecycle and route exact.
func TestControllerHTTPRouteLiveRequiresReadyExactPublication(t *testing.T) {
	runtimeState := readyHTTPRuntimeState()
	source := runtimeStateTestSource(runtimeState)
	authority := &testCertificateAuthority{
		root: validTestRoot(),
		ensureLeaf: func(context.Context, string) (certificates.LeafResult, error) {
			return certificates.LeafResult{}, nil
		},
	}
	runtime := newTestDataPlane(nil)
	controller := newHTTPTestController(t, source, authority, runtime)
	upstream := netip.MustParseAddrPort("127.77.0.10:3000")
	if controller.HTTPRouteLive("orders.test", upstream) {
		t.Fatal("HTTPRouteLive() before Start = true, want false")
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !controller.HTTPRouteLive("orders.test", upstream) {
		t.Fatal("HTTPRouteLive() for exact ready route = false, want true")
	}
	if controller.HTTPRouteLive("other.test", upstream) ||
		controller.HTTPRouteLive("orders.test", netip.MustParseAddrPort("127.77.0.10:3001")) {
		t.Fatal("HTTPRouteLive() accepted an inexact route")
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if controller.HTTPRouteLive("orders.test", upstream) {
		t.Fatal("HTTPRouteLive() after Close = true, want false")
	}
}

// TestControllerReconcileAddsReadyHTTPRoute verifies a stopped reservation becomes live only after durable readiness.
func TestControllerReconcileAddsReadyHTTPRoute(t *testing.T) {
	ready := readyHTTPRuntimeState()
	stopped := stoppedHTTPRuntimeState(ready)
	source := runtimeStateTestSource(stopped)
	authority := &testCertificateAuthority{
		root: validTestRoot(),
		ensureLeaf: func(context.Context, string) (certificates.LeafResult, error) {
			return certificates.LeafResult{}, nil
		},
	}
	runtime := newTestDataPlane(nil)
	controller := newHTTPTestController(t, source, authority, runtime)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	source.snapshot = ready.Snapshot
	source.network = ready.Network

	if err := controller.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	replacements := replacementHTTPRoutes(runtime)
	last := replacements[len(replacements)-1]
	if len(last) != 1 || last[0].Host != "orders.test" {
		t.Fatalf("last replacement = %#v, want orders.test", last)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerReconcileWithdrawsStoppedRouteBeforeNewLeafFailure verifies removal does not depend on unrelated certificate issuance.
func TestControllerReconcileWithdrawsStoppedRouteBeforeNewLeafFailure(t *testing.T) {
	initial := readyHTTPRuntimeState()
	next := stoppedHTTPRuntimeState(initial)
	next = addReadyHTTPProject(next, "inventory", "inventory", "127.77.0.11", 4000)
	source := runtimeStateTestSource(initial)
	leavesErr := errors.New("leaf issuance failed")
	authority := &testCertificateAuthority{
		root: validTestRoot(),
		ensureLeaf: func(_ context.Context, host string) (certificates.LeafResult, error) {
			if host == "inventory.test" {
				return certificates.LeafResult{}, leavesErr
			}
			return certificates.LeafResult{}, nil
		},
	}
	runtime := newTestDataPlane(nil)
	controller := newHTTPTestController(t, source, authority, runtime)
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	source.snapshot = next.Snapshot
	source.network = next.Network

	err := controller.Reconcile(context.Background())
	if !errors.Is(err, leavesErr) {
		t.Fatalf("Reconcile() error = %v, want %v", err, leavesErr)
	}
	replacements := replacementHTTPRoutes(runtime)
	last := replacements[len(replacements)-1]
	if len(last) != 0 {
		t.Fatalf("last replacement after leaf failure = %#v, want stopped orders route withdrawn", last)
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestControllerReconcileRejectsNonReadyLifecycle verifies reconciliation cannot acquire state or routes outside readiness.
func TestControllerReconcileRejectsNonReadyLifecycle(t *testing.T) {
	source := &testRuntimeStateSource{snapshot: validControllerSnapshot()}
	controller := newFakeController(
		t,
		source,
		testControllerDependencies(&testMaterialStore{}, &testCertificateAuthority{root: validTestRoot()}, newTestDataPlane(nil)),
	)

	if err := controller.Reconcile(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Reconcile() before Start error = %v, want %v", err, ErrNotReady)
	}
	if source.calls.Load() != 0 {
		t.Fatalf("Reconcile() before Start durable reads = %d, want zero", source.calls.Load())
	}
	if err := controller.Close(context.Background()); err != nil {
		t.Fatalf("Close() before Start error = %v", err)
	}
	if err := controller.Reconcile(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Reconcile() after Close error = %v, want %v", err, ErrNotReady)
	}
}

// newHTTPTestController constructs a controller whose initial topology comes from durable network state.
func newHTTPTestController(
	t *testing.T,
	source *testRuntimeStateSource,
	authority certificateAuthority,
	runtime *testDataPlane,
) *Controller {
	t.Helper()
	dependencies := testControllerDependencies(&testMaterialStore{}, authority, runtime)
	dependencies.newDesiredState = desiredStateFromRuntimeState
	return newFakeController(t, source, dependencies)
}

// runtimeStateTestSource exposes one mutable state for sequential reconciliation tests.
func runtimeStateTestSource(runtimeState state.RuntimeState) *testRuntimeStateSource {
	return &testRuntimeStateSource{
		snapshot:           runtimeState.Snapshot,
		network:            runtimeState.Network,
		networkInitialized: runtimeState.NetworkInitialized,
	}
}

// replacementHTTPRoutes returns defensive route snapshots from every replacement call.
func replacementHTTPRoutes(runtime *testDataPlane) [][]dataplane.HTTPRoute {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	routes := make([][]dataplane.HTTPRoute, 0, len(runtime.replacements))
	for _, replacement := range runtime.replacements {
		routes = append(routes, replacement.HTTPRoutes())
	}
	return routes
}

// ensuredLeafHosts returns a defensive record of certificate authorization calls.
func ensuredLeafHosts(authority *testCertificateAuthority) []string {
	authority.mutex.Lock()
	defer authority.mutex.Unlock()
	return append([]string(nil), authority.ensureLeafHosts...)
}

// readyHTTPRuntimeState creates one ready App route joined to a primary lease and reservation.
func readyHTTPRuntimeState() state.RuntimeState {
	runtimeState := initializedControllerRuntimeState()
	project := validControllerProject()
	project.State = domain.ProjectReady
	project.Apps = []domain.AppSnapshot{{
		ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true,
	}}
	project.Resources = []domain.ResourceSnapshot{{
		ID: appHTTPResourceID, Name: "App", Kind: "application",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
		URL:   "http://127.77.0.10:3000",
	}}
	runtimeState.Snapshot.Projects = []domain.ProjectSnapshot{project}
	runtimeState.Network.Reservations.Endpoints[0].Key.EndpointID = string(appHTTPResourceID)
	if err := runtimeState.Validate(); err != nil {
		panic(err)
	}
	return runtimeState
}

// stoppedHTTPRuntimeState retains network ownership while removing all live project resource evidence.
func stoppedHTTPRuntimeState(runtimeState state.RuntimeState) state.RuntimeState {
	project := runtimeState.Snapshot.Projects[0]
	project.Apps = append([]domain.AppSnapshot(nil), project.Apps...)
	project.State = domain.ProjectStopped
	project.Apps[0].State = domain.EntityStopped
	project.Apps[0].Active = false
	project.Resources = []domain.ResourceSnapshot{}
	runtimeState.Snapshot.Projects = append([]domain.ProjectSnapshot(nil), runtimeState.Snapshot.Projects...)
	runtimeState.Snapshot.Projects[0] = project
	if err := runtimeState.Validate(); err != nil {
		panic(err)
	}
	return runtimeState
}

// addReadyHTTPProject adds one canonical second route to a stopped-project aggregate.
func addReadyHTTPProject(
	runtimeState state.RuntimeState,
	projectID domain.ProjectID,
	slug string,
	address string,
	port uint16,
) state.RuntimeState {
	project := validControllerProject()
	project.ID = projectID
	project.Name = "Inventory"
	project.Path = "/workspace/" + slug
	project.Slug = slug
	project.State = domain.ProjectReady
	project.Apps = []domain.AppSnapshot{{
		ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true,
	}}
	project.Resources = []domain.ResourceSnapshot{{
		ID: appHTTPResourceID, Name: "App", Kind: "application",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
		URL:   "http://" + netip.AddrPortFrom(netip.MustParseAddr(address), port).String(),
	}}
	runtimeState.Snapshot.Projects = append(runtimeState.Snapshot.Projects, project)
	ownership := runtimeState.Network.Ownership
	lease := identity.Lease{
		Key: identity.LeaseKey{ProjectID: projectID}, Address: netip.MustParseAddr(address), Ownership: ownership,
	}
	runtimeState.Network.Leases = append([]identity.Lease{lease}, runtimeState.Network.Leases...)
	reservation := state.EndpointReservation{
		Key:      state.EndpointReservationKey{ProjectID: projectID, EndpointID: string(appHTTPResourceID)},
		Protocol: state.EndpointProtocolHTTP, Host: slug + ".test",
		Public: runtimeState.Network.Reservations.Listeners.HTTPS.Advertised, Generation: ownership.Generation,
	}
	runtimeState.Network.Reservations.Endpoints = append(
		[]state.EndpointReservation{reservation},
		runtimeState.Network.Reservations.Endpoints...,
	)
	if err := runtimeState.Validate(); err != nil {
		panic(err)
	}
	return runtimeState
}
