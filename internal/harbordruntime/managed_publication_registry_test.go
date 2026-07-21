package harbordruntime

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// TestManagedPublicationRegistryRequiresAnAttachedSession keeps unauthenticated or processless lifecycle rows out of the observation sink.
func TestManagedPublicationRegistryRequiresAnAttachedSession(t *testing.T) {
	registry := NewManagedPublicationRegistry()
	for _, test := range []struct {
		name  string
		state domain.SessionState
		clear bool
		want  string
	}{
		{name: "planned", state: domain.SessionPlanned, clear: true, want: "not attached"},
		{name: "awaiting attach", state: domain.SessionAwaitingAttach, want: "not attached"},
		{name: "stopping", state: domain.SessionStopping, want: "not attached"},
		{name: "disconnected", state: domain.SessionDisconnected, want: "not attached"},
		{name: "attached without process", state: domain.SessionAttached, clear: true, want: "must contain process"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := managedPublicationRegistrySession()
			session.State = test.state
			if test.clear {
				session.Process = nil
			}
			if _, err := registry.Open(session); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestManagedPublicationRegistryReplacesAndCopiesOneCompleteObservation proves replacement and reads cannot mutate registry state.
func TestManagedPublicationRegistryReplacesAndCopiesOneCompleteObservation(t *testing.T) {
	registry := NewManagedPublicationRegistry()
	fence, err := registry.Open(managedPublicationRegistrySession())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	first := []ManagedEndpointPublication{
		managedEndpointPublication(fence, "service:redis", 3, netip.MustParseAddrPort("127.0.0.1:43107")),
		managedEndpointPublication(fence, "service:mysql", 2, netip.MustParseAddrPort("127.0.0.1:43106")),
	}
	if err := registry.Replace(fence, first); err != nil {
		t.Fatalf("Replace(first) error = %v", err)
	}
	first[0].EndpointID = "service:mutated"
	first[0].Upstream = netip.MustParseAddrPort("127.0.0.1:43108")
	observed, err := registry.Snapshot(fence)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	want := []ManagedEndpointPublication{
		managedEndpointPublication(fence, "service:mysql", 2, netip.MustParseAddrPort("127.0.0.1:43106")),
		managedEndpointPublication(fence, "service:redis", 3, netip.MustParseAddrPort("127.0.0.1:43107")),
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("Snapshot() = %#v, want %#v", observed, want)
	}
	observed[0].EndpointID = "service:changed"
	again, err := registry.Snapshot(fence)
	if err != nil {
		t.Fatalf("Snapshot(second) error = %v", err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("Snapshot(second) = %#v, want %#v", again, want)
	}

	replacement := []ManagedEndpointPublication{
		managedEndpointPublication(fence, "service:mysql", 4, netip.MustParseAddrPort("127.0.0.1:43109")),
	}
	if err := registry.Replace(fence, replacement); err != nil {
		t.Fatalf("Replace(second) error = %v", err)
	}
	final, err := registry.Snapshot(fence)
	if err != nil {
		t.Fatalf("Snapshot(final) error = %v", err)
	}
	if !reflect.DeepEqual(final, replacement) {
		t.Fatalf("Snapshot(final) = %#v, want %#v", final, replacement)
	}
}

// TestManagedPublicationRegistryRejectsInvalidReplacementWithoutPartialState proves malformed or duplicate input cannot erase a valid observation.
func TestManagedPublicationRegistryRejectsInvalidReplacementWithoutPartialState(t *testing.T) {
	registry := NewManagedPublicationRegistry()
	fence, err := registry.Open(managedPublicationRegistrySession())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	valid := managedEndpointPublication(fence, "service:mysql", 2, netip.MustParseAddrPort("127.0.0.1:43106"))
	if err := registry.Replace(fence, []ManagedEndpointPublication{valid}); err != nil {
		t.Fatalf("Replace(valid) error = %v", err)
	}
	invalid := valid
	invalid.Fence.SessionGeneration++
	if err := registry.Replace(fence, []ManagedEndpointPublication{invalid}); err == nil || !strings.Contains(err.Error(), "fence") {
		t.Fatalf("Replace(invalid fence) error = %v", err)
	}
	duplicate := []ManagedEndpointPublication{valid, valid}
	if err := registry.Replace(fence, duplicate); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("Replace(duplicate) error = %v", err)
	}
	observed, err := registry.Snapshot(fence)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !reflect.DeepEqual(observed, []ManagedEndpointPublication{valid}) {
		t.Fatalf("Snapshot() = %#v, want prior valid observation", observed)
	}
}

// TestManagedPublicationRegistryFencesCloseAndReplacementSessions proves old sessions cannot write after an explicit close or replacement.
func TestManagedPublicationRegistryFencesCloseAndReplacementSessions(t *testing.T) {
	registry := NewManagedPublicationRegistry()
	firstSession := managedPublicationRegistrySession()
	firstFence, err := registry.Open(firstSession)
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	if _, err := registry.Open(firstSession); !errors.Is(err, ErrManagedPublicationSessionOpen) {
		t.Fatalf("Open(duplicate) error = %v, want already-open error", err)
	}
	if err := registry.Close(firstFence); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
	if _, err := registry.Snapshot(firstFence); !errors.Is(err, ErrManagedPublicationFenceNotFound) {
		t.Fatalf("Snapshot(closed) error = %v, want not-found error", err)
	}
	if err := registry.Replace(firstFence, nil); !errors.Is(err, ErrManagedPublicationFenceNotFound) {
		t.Fatalf("Replace(closed) error = %v, want not-found error", err)
	}

	secondSession := firstSession
	secondSession.ID = "session-orders-next"
	secondSession.Generation++
	secondSession.UpdatedAt = secondSession.UpdatedAt.Add(time.Second)
	secondFence, err := registry.Open(secondSession)
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}
	if err := registry.Replace(firstFence, nil); !errors.Is(err, ErrManagedPublicationFenceMismatch) && !errors.Is(err, ErrManagedPublicationFenceNotFound) {
		t.Fatalf("Replace(old after replacement) error = %v, want stale-fence error", err)
	}
	if err := registry.Close(firstFence); !errors.Is(err, ErrManagedPublicationFenceMismatch) && !errors.Is(err, ErrManagedPublicationFenceNotFound) {
		t.Fatalf("Close(old after replacement) error = %v, want stale-fence error", err)
	}
	if err := registry.Close(secondFence); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
}

// TestManagedPublicationRegistryFeedsThePurePlanner proves registry observations remain a separate input to route planning.
func TestManagedPublicationRegistryFeedsThePurePlanner(t *testing.T) {
	registry := NewManagedPublicationRegistry()
	fence, err := registry.Open(managedPublicationRegistrySession())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	publication := managedEndpointPublication(fence, "service:mysql", 2, netip.MustParseAddrPort("127.0.0.1:43106"))
	if err := registry.Replace(fence, []ManagedEndpointPublication{publication}); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	observed, err := registry.Snapshot(fence)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	routes, err := PlanManagedNativeRoutes(ManagedPublicationPlanInput{
		Fence:        fence,
		Reservations: []state.EndpointReservation{managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 2)},
		Publications: observed,
	})
	if err != nil {
		t.Fatalf("PlanManagedNativeRoutes() error = %v", err)
	}
	want := []dataplane.NativeRoute{{
		ID: "orders:service:mysql", Host: "mysql.orders.test",
		Listen: netip.MustParseAddrPort("127.77.0.10:3306"), Upstream: netip.MustParseAddrPort("127.0.0.1:43106"),
	}}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}
}

// managedPublicationRegistrySession creates one canonical attached process session for registry tests.
func managedPublicationRegistrySession() domain.ProjectSession {
	at := time.Date(2026, 7, 21, 7, 0, 0, 0, time.UTC)
	return domain.ProjectSession{
		ID: "session-orders", ProjectID: "orders", Owner: domain.SessionOwnerHarbor, State: domain.SessionAttached,
		DescriptorDigest: strings.Repeat("a", 64), CredentialDigest: strings.Repeat("b", 64), Generation: 7,
		Process: &domain.ProcessEvidence{
			PID: 4201, BirthToken: "birth-4201", ExecutableIdentity: "/usr/local/bin/forj", ArgumentDigest: strings.Repeat("c", 64),
		},
		CreatedAt: at, UpdatedAt: at,
	}
}
