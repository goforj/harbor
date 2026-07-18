package harbordruntime

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
)

// TestDesiredStateFromRuntimeStateKeepsUninitializedHostsEmpty verifies absent host setup cannot acquire provisional listeners.
func TestDesiredStateFromRuntimeStateKeepsUninitializedHostsEmpty(t *testing.T) {
	runtimeState := state.RuntimeState{
		Snapshot: validControllerSnapshot(),
		Network:  validControllerUninitializedNetwork(),
	}

	desired, err := desiredStateFromRuntimeState(runtimeState)
	if err != nil {
		t.Fatalf("desiredStateFromRuntimeState() error = %v", err)
	}
	if !desired.Empty() || desired.ListenerPlan() != (dataplane.ListenerPlan{}) {
		t.Fatalf("uninitialized desired state = %#v, want truly empty", desired.ListenerPlan())
	}
	if len(desired.HTTPRoutes()) != 0 || len(desired.NativeRoutes()) != 0 || len(desired.DNSRecords()) != 0 {
		t.Fatalf("uninitialized routes = HTTP %v, native %v, DNS %v", desired.HTTPRoutes(), desired.NativeRoutes(), desired.DNSRecords())
	}
}

// TestDesiredStateFromRuntimeStateKeepsPendingProjectsEmpty verifies registration survives restart without claiming host networking.
func TestDesiredStateFromRuntimeStateKeepsPendingProjectsEmpty(t *testing.T) {
	tests := []struct {
		name    string
		project domain.ProjectSnapshot
	}{
		{
			name:    "new route-free registration",
			project: validControllerProject(),
		},
		{
			name: "stopped discovered topology",
			project: func() domain.ProjectSnapshot {
				project := validControllerProject()
				project.Apps = []domain.AppSnapshot{{
					ID: "app", Name: "App", State: domain.EntityStopped, Active: false, Required: true,
				}}
				project.Services = []domain.ServiceSnapshot{{
					ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityStopped,
					Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true,
				}}
				return project
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeState := state.RuntimeState{
				Snapshot: func() domain.Snapshot {
					snapshot := validControllerSnapshot()
					snapshot.Projects = []domain.ProjectSnapshot{test.project}
					return snapshot
				}(),
				Network: validControllerUninitializedNetwork(),
			}

			desired, err := desiredStateFromRuntimeState(runtimeState)
			if err != nil {
				t.Fatalf("desiredStateFromRuntimeState() error = %v", err)
			}
			if !desired.Empty() || desired.ListenerPlan() != (dataplane.ListenerPlan{}) {
				t.Fatalf("pending project desired state = %#v, want truly empty", desired.ListenerPlan())
			}
			if len(desired.HTTPRoutes()) != 0 || len(desired.NativeRoutes()) != 0 || len(desired.DNSRecords()) != 0 {
				t.Fatalf("pending project routes = HTTP %v, native %v, DNS %v", desired.HTTPRoutes(), desired.NativeRoutes(), desired.DNSRecords())
			}
		})
	}
}

// TestDesiredStateFromRuntimeStateProjectsOnlyVerifiedSharedBindings verifies reservations do not become live routes without session evidence.
func TestDesiredStateFromRuntimeStateProjectsOnlyVerifiedSharedBindings(t *testing.T) {
	runtimeState := initializedControllerRuntimeState()
	pending := validControllerProject()
	pending.ID = "billing"
	pending.Name = "Billing"
	pending.Path = "/workspace/billing"
	pending.Slug = "billing"
	runtimeState.Snapshot.Projects = append(runtimeState.Snapshot.Projects, pending)
	if len(runtimeState.Network.Reservations.Endpoints) == 0 {
		t.Fatal("initialized fixture must include a pending endpoint reservation")
	}

	desired, err := desiredStateFromRuntimeState(runtimeState)
	if err != nil {
		t.Fatalf("desiredStateFromRuntimeState() error = %v", err)
	}
	want := dataplane.ListenerPlan{
		DNS:   runtimeState.Network.Reservations.Listeners.DNS.Bind,
		HTTP:  runtimeState.Network.Reservations.Listeners.HTTP.Bind,
		HTTPS: runtimeState.Network.Reservations.Listeners.HTTPS.Bind,
	}
	if got := desired.ListenerPlan(); got != want {
		t.Fatalf("ListenerPlan() = %#v, want bind sockets %#v", got, want)
	}
	advertised := dataplane.ListenerPlan{
		DNS:   runtimeState.Network.Reservations.Listeners.DNS.Advertised,
		HTTP:  runtimeState.Network.Reservations.Listeners.HTTP.Advertised,
		HTTPS: runtimeState.Network.Reservations.Listeners.HTTPS.Advertised,
	}
	if desired.ListenerPlan() == advertised {
		t.Fatalf("ListenerPlan() used advertised sockets %#v instead of daemon bind sockets", advertised)
	}
	if desired.Empty() {
		t.Fatal("initialized desired state discarded durable shared listeners")
	}
	if len(desired.HTTPRoutes()) != 0 || len(desired.NativeRoutes()) != 0 || len(desired.DNSRecords()) != 0 {
		t.Fatalf("pending endpoint escaped as live routing: HTTP %v, native %v, DNS %v", desired.HTTPRoutes(), desired.NativeRoutes(), desired.DNSRecords())
	}
}

// TestDesiredStateFromRuntimeStateRejectsInvalidAndUnprojectableAggregates verifies the pure seam fails closed before projection.
func TestDesiredStateFromRuntimeStateRejectsInvalidAndUnprojectableAggregates(t *testing.T) {
	tests := []struct {
		name         string
		runtimeState state.RuntimeState
		want         error
	}{
		{
			name: "invalid aggregate",
			runtimeState: state.RuntimeState{
				Snapshot: validControllerSnapshot(),
				Network:  state.NetworkRecord{},
			},
			want: errors.New("leases must be initialized"),
		},
		{
			name:         "running project without initialized network",
			runtimeState: uninitializedControllerProjectState(func(project *domain.ProjectSnapshot) { project.State = domain.ProjectReady }),
			want:         errors.New("not pending"),
		},
		{
			name: "active App without initialized network",
			runtimeState: uninitializedControllerProjectState(func(project *domain.ProjectSnapshot) {
				project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityStopped, Active: true, Required: true}}
			}),
			want: errors.New("not pending"),
		},
		{
			name: "ready App without initialized network",
			runtimeState: uninitializedControllerProjectState(func(project *domain.ProjectSnapshot) {
				project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: false, Required: true}}
			}),
			want: errors.New("not pending"),
		},
		{
			name: "ready service without initialized network",
			runtimeState: uninitializedControllerProjectState(func(project *domain.ProjectSnapshot) {
				project.Services = []domain.ServiceSnapshot{{
					ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityReady,
					Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true,
				}}
			}),
			want: errors.New("not pending"),
		},
		{
			name: "published resource without initialized network",
			runtimeState: uninitializedControllerProjectState(func(project *domain.ProjectSnapshot) {
				project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityStopped, Active: false, Required: true}}
				project.Resources = []domain.ResourceSnapshot{{
					ID: "home", Name: "Home", Kind: "site",
					Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "https://orders.test",
				}}
			}),
			want: errors.New("not pending"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			desired, err := desiredStateFromRuntimeState(test.runtimeState)
			if err == nil || !errorMatches(err, test.want) {
				t.Fatalf("desiredStateFromRuntimeState() error = %v, want %v", err, test.want)
			}
			if !reflect.DeepEqual(desired, dataplane.DesiredState{}) {
				t.Fatalf("desiredStateFromRuntimeState() desired = %#v, want zero on failure", desired)
			}
		})
	}
}

// uninitializedControllerProjectState builds one valid aggregate whose project is altered to exercise projection authorization.
func uninitializedControllerProjectState(mutate func(*domain.ProjectSnapshot)) state.RuntimeState {
	project := validControllerProject()
	mutate(&project)
	snapshot := validControllerSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	return state.RuntimeState{
		Snapshot: snapshot,
		Network:  validControllerUninitializedNetwork(),
	}
}

// initializedControllerRuntimeState returns a valid aggregate with redirected shared sockets and one pending HTTP endpoint.
func initializedControllerRuntimeState() state.RuntimeState {
	verificationTime := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	pool, err := identity.NewPool(
		netip.MustParsePrefix("127.77.0.0/24"),
		[]netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.11")},
	)
	if err != nil {
		panic(err)
	}
	ownership := identity.Ownership{InstallationID: "harbor-installation", Generation: 9}
	snapshot := validControllerSnapshot()
	snapshot.Sequence = 21
	snapshot.Projects = []domain.ProjectSnapshot{validControllerProject()}
	return state.RuntimeState{
		Snapshot: snapshot,
		Network: state.NetworkRecord{
			Revision:    21,
			CreatedAt:   verificationTime,
			UpdatedAt:   verificationTime,
			Ownership:   ownership,
			Pool:        pool,
			Leases:      []identity.Lease{{Key: identity.LeaseKey{ProjectID: "orders"}, Address: netip.MustParseAddr("127.77.0.10"), Ownership: ownership}},
			Quarantines: []identity.Quarantine{},
			Reservations: state.DataPlaneReservations{
				Listeners: state.SharedListenerReservations{
					DNS:   controllerTestListener("127.0.0.1:53", "127.0.0.1:1053", verificationTime),
					HTTP:  controllerTestListener("127.0.0.1:80", "127.0.0.1:18080", verificationTime),
					HTTPS: controllerTestListener("127.0.0.1:443", "127.0.0.1:18443", verificationTime),
				},
				Endpoints: []state.EndpointReservation{{
					Key:        state.EndpointReservationKey{ProjectID: "orders", EndpointID: "web"},
					Protocol:   state.EndpointProtocolHTTP,
					Host:       "orders.test",
					Public:     netip.MustParseAddrPort("127.0.0.1:443"),
					Generation: 9,
				}},
				SuppressedProjectIDs: []domain.ProjectID{},
			},
		},
		NetworkInitialized: true,
	}
}

// controllerTestListener returns one redirected reservation whose advertised socket differs from the daemon bind socket.
func controllerTestListener(advertised string, bind string, verifiedAt time.Time) state.ListenerReservation {
	return state.ListenerReservation{
		Mode:       state.ListenerModeRedirect,
		Advertised: netip.MustParseAddrPort(advertised),
		Bind:       netip.MustParseAddrPort(bind),
		Generation: 9,
		VerifiedAt: verifiedAt,
	}
}
