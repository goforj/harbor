package harbordruntime

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
)

// TestPlanManagedNativeRoutesMapsObservedMySQLAndRedisPublications proves exact endpoint joins retain native ports and private upstreams.
func TestPlanManagedNativeRoutesMapsObservedMySQLAndRedisPublications(t *testing.T) {
	fence := managedPublicationTestFence()
	input := ManagedPublicationPlanInput{
		Fence: fence,
		Reservations: []state.EndpointReservation{
			managedPublicationReservation("service:redis", "redis.orders.test", "127.77.0.10:6379", 3),
			managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 2),
		},
		Publications: []ManagedEndpointPublication{
			managedEndpointPublication(fence, "service:redis", 3, netip.MustParseAddrPort("127.0.0.1:43107")),
			managedEndpointPublication(fence, "service:mysql", 2, netip.MustParseAddrPort("127.0.0.1:43106")),
		},
	}

	routes, err := PlanManagedNativeRoutes(input)
	if err != nil {
		t.Fatalf("PlanManagedNativeRoutes() error = %v", err)
	}
	want := []dataplane.NativeRoute{
		{ID: "orders:service:mysql", Host: "mysql.orders.test", Listen: netip.MustParseAddrPort("127.77.0.10:3306"), Upstream: netip.MustParseAddrPort("127.0.0.1:43106")},
		{ID: "orders:service:redis", Host: "redis.orders.test", Listen: netip.MustParseAddrPort("127.77.0.10:6379"), Upstream: netip.MustParseAddrPort("127.0.0.1:43107")},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}
}

// TestPlanManagedNativeRoutesWithholdsUnobservedReservations proves static public authority does not create a relay route.
func TestPlanManagedNativeRoutesWithholdsUnobservedReservations(t *testing.T) {
	fence := managedPublicationTestFence()
	routes, err := PlanManagedNativeRoutes(ManagedPublicationPlanInput{
		Fence: fence,
		Reservations: []state.EndpointReservation{
			managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 1),
		},
		Publications: []ManagedEndpointPublication{},
	})
	if err != nil {
		t.Fatalf("PlanManagedNativeRoutes() error = %v", err)
	}
	if routes == nil || len(routes) != 0 {
		t.Fatalf("routes = %#v, want an initialized empty route set", routes)
	}
}

// TestPlanManagedNativeRoutesRejectsSessionAndReservationFenceDrift keeps stale publications out of native authority.
func TestPlanManagedNativeRoutesRejectsSessionAndReservationFenceDrift(t *testing.T) {
	fence := managedPublicationTestFence()
	tests := []struct {
		name   string
		mutate func(*ManagedPublicationPlanInput)
		want   string
	}{
		{
			name: "project fence",
			mutate: func(input *ManagedPublicationPlanInput) {
				input.Publications[0].Fence.ProjectID = "other"
			},
			want: "project/session fence",
		},
		{
			name: "session fence",
			mutate: func(input *ManagedPublicationPlanInput) {
				input.Publications[0].Fence.SessionID = "session-other"
			},
			want: "project/session fence",
		},
		{
			name: "generation fence",
			mutate: func(input *ManagedPublicationPlanInput) {
				input.Publications[0].Fence.SessionGeneration++
			},
			want: "project/session fence",
		},
		{
			name: "reservation generation",
			mutate: func(input *ManagedPublicationPlanInput) {
				input.Publications[0].ReservationGeneration++
			},
			want: "reservation generation",
		},
		{
			name: "unknown endpoint",
			mutate: func(input *ManagedPublicationPlanInput) {
				input.Publications[0].EndpointID = "service:missing"
			},
			want: "no durable reservation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := ManagedPublicationPlanInput{
				Fence: fence,
				Reservations: []state.EndpointReservation{
					managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 4),
				},
				Publications: []ManagedEndpointPublication{
					managedEndpointPublication(fence, "service:mysql", 4, netip.MustParseAddrPort("127.0.0.1:43106")),
				},
			}
			test.mutate(&input)
			if _, err := PlanManagedNativeRoutes(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PlanManagedNativeRoutes() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestPlanManagedNativeRoutesRejectsInvalidPrivateUpstreams keeps relay destinations on canonical IPv4 loopback high ports.
func TestPlanManagedNativeRoutesRejectsInvalidPrivateUpstreams(t *testing.T) {
	fence := managedPublicationTestFence()
	tests := []struct {
		name     string
		upstream netip.AddrPort
		want     string
	}{
		{name: "foreign address", upstream: netip.MustParseAddrPort("192.0.2.10:43106"), want: "IPv4 loopback"},
		{name: "IPv6 loopback", upstream: netip.MustParseAddrPort("[::1]:43106"), want: "IPv4 loopback"},
		{name: "low port", upstream: netip.MustParseAddrPort("127.0.0.1:1023"), want: "high port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := ManagedPublicationPlanInput{
				Fence: fence,
				Reservations: []state.EndpointReservation{
					managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 1),
				},
				Publications: []ManagedEndpointPublication{
					managedEndpointPublication(fence, "service:mysql", 1, test.upstream),
				},
			}
			if _, err := PlanManagedNativeRoutes(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PlanManagedNativeRoutes() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestPlanManagedNativeRoutesRejectsAmbiguousEndpointCollisions keeps one host and private destination bound to one route.
func TestPlanManagedNativeRoutesRejectsAmbiguousEndpointCollisions(t *testing.T) {
	fence := managedPublicationTestFence()
	tests := []struct {
		name         string
		reservations []state.EndpointReservation
		publications []ManagedEndpointPublication
		want         string
	}{
		{
			name: "duplicate publication",
			reservations: []state.EndpointReservation{
				managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 1),
			},
			publications: []ManagedEndpointPublication{
				managedEndpointPublication(fence, "service:mysql", 1, netip.MustParseAddrPort("127.0.0.1:43106")),
				managedEndpointPublication(fence, "service:mysql", 1, netip.MustParseAddrPort("127.0.0.1:43107")),
			},
			want: "duplicated",
		},
		{
			name: "duplicate public host",
			reservations: []state.EndpointReservation{
				managedPublicationReservation("service:mysql", "shared.orders.test", "127.77.0.10:3306", 1),
				managedPublicationReservation("service:redis", "shared.orders.test", "127.77.0.10:6379", 1),
			},
			publications: []ManagedEndpointPublication{
				managedEndpointPublication(fence, "service:mysql", 1, netip.MustParseAddrPort("127.0.0.1:43106")),
				managedEndpointPublication(fence, "service:redis", 1, netip.MustParseAddrPort("127.0.0.1:43107")),
			},
			want: "host",
		},
		{
			name: "duplicate public listener",
			reservations: []state.EndpointReservation{
				managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 1),
				managedPublicationReservation("service:redis", "redis.orders.test", "127.77.0.10:3306", 1),
			},
			publications: []ManagedEndpointPublication{
				managedEndpointPublication(fence, "service:mysql", 1, netip.MustParseAddrPort("127.0.0.1:43106")),
				managedEndpointPublication(fence, "service:redis", 1, netip.MustParseAddrPort("127.0.0.1:43107")),
			},
			want: "listener",
		},
		{
			name: "duplicate private upstream",
			reservations: []state.EndpointReservation{
				managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 1),
				managedPublicationReservation("service:redis", "redis.orders.test", "127.77.0.10:6379", 1),
			},
			publications: []ManagedEndpointPublication{
				managedEndpointPublication(fence, "service:mysql", 1, netip.MustParseAddrPort("127.0.0.1:43106")),
				managedEndpointPublication(fence, "service:redis", 1, netip.MustParseAddrPort("127.0.0.1:43106")),
			},
			want: "upstream",
		},
		{
			name: "upstream is another public listener",
			reservations: []state.EndpointReservation{
				managedPublicationReservation("service:mysql", "mysql.orders.test", "127.77.0.10:3306", 1),
				managedPublicationReservation("service:redis", "redis.orders.test", "127.77.0.10:6379", 1),
			},
			publications: []ManagedEndpointPublication{
				managedEndpointPublication(fence, "service:mysql", 1, netip.MustParseAddrPort("127.77.0.10:6379")),
				managedEndpointPublication(fence, "service:redis", 1, netip.MustParseAddrPort("127.0.0.1:43107")),
			},
			want: "another public listener",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PlanManagedNativeRoutes(ManagedPublicationPlanInput{Fence: fence, Reservations: test.reservations, Publications: test.publications}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PlanManagedNativeRoutes() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestPlanManagedNativeRoutesRejectsNonTCPReservations keeps HTTP authority out of native relay planning.
func TestPlanManagedNativeRoutesRejectsNonTCPReservations(t *testing.T) {
	fence := managedPublicationTestFence()
	reservation := managedPublicationReservation("service:dashboard", "dashboard.orders.test", "127.77.0.10:8443", 1)
	reservation.Protocol = state.EndpointProtocolHTTP
	reservation.Identity = nil
	input := ManagedPublicationPlanInput{
		Fence:        fence,
		Reservations: []state.EndpointReservation{reservation},
		Publications: []ManagedEndpointPublication{managedEndpointPublication(fence, "service:dashboard", 1, netip.MustParseAddrPort("127.0.0.1:43106"))},
	}
	if _, err := PlanManagedNativeRoutes(input); err == nil || !strings.Contains(err.Error(), "not TCP") {
		t.Fatalf("PlanManagedNativeRoutes() error = %v, want non-TCP rejection", err)
	}
}

// managedPublicationTestFence creates one canonical project/session fence for pure planner tests.
func managedPublicationTestFence() ManagedPublicationFence {
	return ManagedPublicationFence{ProjectID: "orders", SessionID: "session-orders", SessionGeneration: 7}
}

// managedPublicationReservation creates one valid project-owned TCP reservation for planner tests.
func managedPublicationReservation(endpointID, host, public string, generation uint64) state.EndpointReservation {
	key := identity.LeaseKey{ProjectID: "orders"}
	return state.EndpointReservation{
		Key:        state.EndpointReservationKey{ProjectID: "orders", EndpointID: endpointID},
		Protocol:   state.EndpointProtocolTCP,
		Host:       host,
		Public:     netip.MustParseAddrPort(public),
		Identity:   &key,
		Generation: generation,
	}
}

// managedEndpointPublication creates one exact observed private publication for planner tests.
func managedEndpointPublication(fence ManagedPublicationFence, endpointID string, generation uint64, upstream netip.AddrPort) ManagedEndpointPublication {
	return ManagedEndpointPublication{Fence: fence, EndpointID: endpointID, ReservationGeneration: generation, Upstream: upstream}
}
