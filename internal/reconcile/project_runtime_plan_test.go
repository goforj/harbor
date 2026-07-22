package reconcile

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/managedsession"
	"github.com/goforj/harbor/internal/state"
)

// TestManagedRuntimeServiceEndpointRequiresExactPublicationGeneration keeps stale Compose facts out of the plan.
func TestManagedRuntimeServiceEndpointRequiresExactPublicationGeneration(t *testing.T) {
	fence := harbordruntime.ManagedPublicationFence{ProjectID: "project-orders", SessionID: "session-orders", SessionGeneration: 2}
	requirement := goforj.ServiceRequirement{ID: "database", Consumers: []string{"app"}}
	publication := harbordruntime.ManagedEndpointPublication{
		Fence:                 fence,
		EndpointID:            "service:database.tcp",
		ReservationGeneration: 3,
		Upstream:              netip.MustParseAddrPort("127.0.0.1:43106"),
	}
	reservation := state.EndpointReservation{
		Key:        state.EndpointReservationKey{ProjectID: fence.ProjectID, EndpointID: publication.EndpointID},
		Protocol:   state.EndpointProtocolTCP,
		Host:       "database.orders.test",
		Public:     netip.MustParseAddrPort("127.77.1.8:3306"),
		Generation: 4,
	}
	declared := goforj.ServiceEndpoint{ID: "endpoint.database.primary.tcp", Protocol: goforj.ServiceEndpointProtocolTCP, NativePort: 3306, Visibility: goforj.ServiceEndpointVisibilityHost}
	if _, err := managedRuntimeServiceEndpoint(fence, requirement, []string{"app"}, declared, publication, reservation); err == nil || !strings.Contains(err.Error(), "does not match durable generation") {
		t.Fatalf("managedRuntimeServiceEndpoint() error = %v, want generation mismatch", err)
	}
	publication.ReservationGeneration = reservation.Generation
	endpoint, err := managedRuntimeServiceEndpoint(fence, requirement, []string{"app"}, declared, publication, reservation)
	if err != nil {
		t.Fatalf("managedRuntimeServiceEndpoint() error = %v", err)
	}
	if endpoint.ID != publication.EndpointID || endpoint.RequirementID != requirement.ID || endpoint.PublishPort != publication.Upstream.Port() || endpoint.PublicPort != reservation.Public.Port() {
		t.Fatalf("managed service endpoint = %#v", endpoint)
	}
	reservation.Protocol = state.EndpointProtocolHTTP
	if _, err := managedRuntimeServiceEndpoint(fence, requirement, []string{"app"}, declared, publication, reservation); err == nil || !strings.Contains(err.Error(), "is not TCP") {
		t.Fatalf("managedRuntimeServiceEndpoint() protocol error = %v, want TCP rejection", err)
	}
	reservation.Protocol = state.EndpointProtocolTCP
	publication.Fence.SessionGeneration++
	if _, err := managedRuntimeServiceEndpoint(fence, requirement, []string{"app"}, declared, publication, reservation); err == nil || !strings.Contains(err.Error(), "does not match the requested fence") {
		t.Fatalf("managedRuntimeServiceEndpoint() fence error = %v, want fence rejection", err)
	}
}

// TestManagedRuntimePlanAppsUsesDurableLoopbackAndDescriptorIntent proves static defaults become private assignments only at a Harbor lease.
func TestManagedRuntimePlanAppsUsesDurableLoopbackAndDescriptorIntent(t *testing.T) {
	request := managedRuntimePlanTestRequest()
	apps, err := managedRuntimePlanApps(
		request,
		[]goforj.App{{ID: "app", Runtimes: []goforj.Runtime{{ID: "http", DefaultPort: 3000, PublicURL: true, ReadinessPath: "/-/ready"}}}},
		netip.MustParseAddr("127.77.1.8"),
		[]state.EndpointReservation{{
			Key:        state.EndpointReservationKey{ProjectID: request.Fence.ProjectID, EndpointID: primaryLeaseDefaultHTTPEndpointID},
			Protocol:   state.EndpointProtocolHTTP,
			Host:       "orders.test",
			Public:     netip.MustParseAddrPort("127.77.1.8:443"),
			Generation: 1,
		}},
	)
	if err != nil {
		t.Fatalf("managedRuntimePlanApps() error = %v", err)
	}
	if len(apps) != 1 || len(apps[0].Runtimes) != 1 {
		t.Fatalf("managed runtime Apps = %#v, want one App/runtime", apps)
	}
	runtime := apps[0].Runtimes[0]
	if runtime.BindHost != "127.77.1.8" || runtime.BindPort != 3000 || runtime.PublicURL != "https://orders.test" {
		t.Fatalf("managed runtime assignment = %#v", runtime)
	}
	if len(runtime.Routes) != 1 || runtime.Routes[0].Name != "readiness" || runtime.Routes[0].Path != "/-/ready" {
		t.Fatalf("managed runtime routes = %#v", runtime.Routes)
	}
}

// TestManagedRuntimePlanAppsRejectsUnsupportedOrAmbiguousAssignments keeps low ports, missing Apps, and duplicate binds fail-closed.
func TestManagedRuntimePlanAppsRejectsUnsupportedOrAmbiguousAssignments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*managedsession.RuntimePlanRequest, *[]goforj.App)
		want   string
	}{
		{
			name: "missing App",
			mutate: func(request *managedsession.RuntimePlanRequest, apps *[]goforj.App) {
				request.ActiveApps[0].ID = "worker"
				*apps = []goforj.App{{ID: "app"}}
			},
			want: "not in the descriptor",
		},
		{
			name: "low port",
			mutate: func(_ *managedsession.RuntimePlanRequest, apps *[]goforj.App) {
				*apps = []goforj.App{{ID: "app", Runtimes: []goforj.Runtime{{ID: "http", DefaultPort: 80}}}}
			},
			want: "low-port",
		},
		{
			name: "duplicate bind",
			mutate: func(request *managedsession.RuntimePlanRequest, apps *[]goforj.App) {
				request.ActiveApps = []managedsession.ActiveApp{
					{ID: "app", RuntimeIDs: []string{"http"}},
					{ID: "worker", RuntimeIDs: []string{"http"}},
				}
				*apps = []goforj.App{
					{ID: "app", Runtimes: []goforj.Runtime{{ID: "http", DefaultPort: 3000}}},
					{ID: "worker", Runtimes: []goforj.Runtime{{ID: "http", DefaultPort: 3000}}},
				}
			},
			want: "conflicts",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := managedRuntimePlanTestRequest()
			apps := []goforj.App{{ID: "app", Runtimes: []goforj.Runtime{{ID: "http", DefaultPort: 3000}}}}
			test.mutate(&request, &apps)
			_, err := managedRuntimePlanApps(request, apps, netip.MustParseAddr("127.77.1.8"), nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("managedRuntimePlanApps() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// managedRuntimePlanTestRequest returns one valid active App fence for planner tests.
func managedRuntimePlanTestRequest() managedsession.RuntimePlanRequest {
	return managedsession.RuntimePlanRequest{
		SchemaVersion: managedsession.SchemaVersion,
		Fence: harbordruntime.ManagedPublicationFence{
			ProjectID:         "project-orders",
			SessionID:         "session-orders",
			SessionGeneration: 2,
		},
		ActiveApps: []managedsession.ActiveApp{{ID: "app", RuntimeIDs: []string{"http"}}},
	}
}
