package harbordruntime

import (
	"fmt"
	"net/netip"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

const minimumManagedUpstreamPort uint16 = 1024

// ManagedPublicationFence binds one observed publication to the project session that authorized it.
type ManagedPublicationFence struct {
	ProjectID         domain.ProjectID `json:"project_id"`
	SessionID         domain.SessionID `json:"session_id"`
	SessionGeneration uint64           `json:"session_generation"`
}

// Validate reports whether a managed publication fence contains complete session identity.
func (fence ManagedPublicationFence) Validate() error {
	if err := fence.ProjectID.Validate(); err != nil {
		return fmt.Errorf("managed publication project fence: %w", err)
	}
	if err := fence.SessionID.Validate(); err != nil {
		return fmt.Errorf("managed publication session fence: %w", err)
	}
	if fence.SessionGeneration == 0 {
		return fmt.Errorf("managed publication session generation must be positive")
	}
	return nil
}

// ManagedEndpointPublication is one private host publication observed for an authorized session endpoint.
type ManagedEndpointPublication struct {
	Fence                 ManagedPublicationFence `json:"fence"`
	EndpointID            string                  `json:"endpoint_id"`
	ReservationGeneration uint64                  `json:"reservation_generation"`
	Upstream              netip.AddrPort          `json:"upstream"`
}

// Validate reports whether a publication contains only a bounded loopback high-port upstream.
func (publication ManagedEndpointPublication) Validate() error {
	if err := publication.Fence.Validate(); err != nil {
		return err
	}
	key := state.EndpointReservationKey{ProjectID: publication.Fence.ProjectID, EndpointID: publication.EndpointID}
	if err := key.Validate(); err != nil {
		return fmt.Errorf("managed publication endpoint: %w", err)
	}
	if publication.ReservationGeneration == 0 {
		return fmt.Errorf("managed publication endpoint %q reservation generation must be positive", publication.EndpointID)
	}
	if !publication.Upstream.IsValid() || publication.Upstream.Port() < minimumManagedUpstreamPort {
		return fmt.Errorf("managed publication endpoint %q upstream %s must use a high port", publication.EndpointID, publication.Upstream)
	}
	upstreamAddress := publication.Upstream.Addr()
	if !upstreamAddress.Is4() || !upstreamAddress.IsLoopback() || upstreamAddress != upstreamAddress.Unmap() {
		return fmt.Errorf("managed publication endpoint %q upstream %s must use canonical IPv4 loopback", publication.EndpointID, publication.Upstream)
	}
	return nil
}

// ManagedPublicationPlanInput joins durable public endpoint reservations to fresh private publications.
type ManagedPublicationPlanInput struct {
	Fence        ManagedPublicationFence
	Reservations []state.EndpointReservation
	Publications []ManagedEndpointPublication
}

// ManagedDirectPublication records a native service that Compose already bound directly to its Harbor-owned public socket.
//
// It is an explicit barrier outcome rather than a relay route: creating a relay for this shape would connect the public
// listener back to itself. Its presence means the authenticated observation, session fence, and durable reservation joined
// on one exact TCP socket.
type ManagedDirectPublication struct {
	ID     string
	Host   string
	Listen netip.AddrPort
}

// ManagedNativePublicationPlan separates relay work from direct native publications that already satisfy the barrier.
type ManagedNativePublicationPlan struct {
	RelayRoutes        []dataplane.NativeRoute
	DirectPublications []ManagedDirectPublication
}

// Routes returns direct DNS publications and relay routes in one canonical data-plane generation.
func (plan ManagedNativePublicationPlan) Routes() []dataplane.NativeRoute {
	routes := append([]dataplane.NativeRoute(nil), plan.RelayRoutes...)
	for _, direct := range plan.DirectPublications {
		routes = append(routes, dataplane.NativeRoute{
			ID:       direct.ID,
			Host:     direct.Host,
			Listen:   direct.Listen,
			Upstream: direct.Listen,
			Direct:   true,
		})
	}
	slices.SortFunc(routes, compareManagedNativeRoutes)
	return routes
}

// Validate reports whether the plan input has one complete session fence and bounded endpoint facts.
func (input ManagedPublicationPlanInput) Validate() error {
	if err := input.Fence.Validate(); err != nil {
		return err
	}
	for index, reservation := range input.Reservations {
		if err := reservation.Validate(); err != nil {
			return fmt.Errorf("managed publication reservation %d: %w", index, err)
		}
		if reservation.Key.ProjectID != input.Fence.ProjectID {
			return fmt.Errorf("managed publication reservation %q belongs to project %q, not %q", reservation.Key.EndpointID, reservation.Key.ProjectID, input.Fence.ProjectID)
		}
	}
	for index, publication := range input.Publications {
		if err := publication.Validate(); err != nil {
			return fmt.Errorf("managed publication %d: %w", index, err)
		}
		if publication.Fence != input.Fence {
			return fmt.Errorf("managed publication %q does not match the requested project/session fence", publication.EndpointID)
		}
	}
	return nil
}

// PlanManagedNativeRoutes returns deterministic native relay routes for exact observed publications.
//
// Unobserved reservations remain withheld. The planner emits no route authority until a publication
// carries the exact project, session, generation, endpoint, and reservation-generation fences.
func PlanManagedNativePublications(input ManagedPublicationPlanInput) (ManagedNativePublicationPlan, error) {
	if err := input.Validate(); err != nil {
		return ManagedNativePublicationPlan{}, err
	}

	reservations := make(map[string]state.EndpointReservation, len(input.Reservations))
	for _, reservation := range input.Reservations {
		endpointID := reservation.Key.EndpointID
		if _, duplicate := reservations[endpointID]; duplicate {
			return ManagedNativePublicationPlan{}, fmt.Errorf("managed publication reservation %q is duplicated", endpointID)
		}
		reservations[endpointID] = reservation
	}

	publications := make(map[string]ManagedEndpointPublication, len(input.Publications))
	for _, publication := range input.Publications {
		if _, duplicate := publications[publication.EndpointID]; duplicate {
			return ManagedNativePublicationPlan{}, fmt.Errorf("managed publication endpoint %q is duplicated", publication.EndpointID)
		}
		reservation, found := reservations[publication.EndpointID]
		if !found {
			return ManagedNativePublicationPlan{}, fmt.Errorf("managed publication endpoint %q has no durable reservation", publication.EndpointID)
		}
		if reservation.Protocol != state.EndpointProtocolTCP {
			return ManagedNativePublicationPlan{}, fmt.Errorf("managed publication endpoint %q reservation protocol %q is not TCP", publication.EndpointID, reservation.Protocol)
		}
		if publication.ReservationGeneration != reservation.Generation {
			return ManagedNativePublicationPlan{}, fmt.Errorf("managed publication endpoint %q reservation generation %d does not match durable generation %d", publication.EndpointID, publication.ReservationGeneration, reservation.Generation)
		}
		publications[publication.EndpointID] = publication
	}

	plan := ManagedNativePublicationPlan{
		RelayRoutes:        make([]dataplane.NativeRoute, 0, len(publications)),
		DirectPublications: make([]ManagedDirectPublication, 0),
	}
	for endpointID, publication := range publications {
		reservation := reservations[endpointID]
		route := dataplane.NativeRoute{
			ID:       string(input.Fence.ProjectID) + ":" + endpointID,
			Host:     reservation.Host,
			Listen:   reservation.Public,
			Upstream: publication.Upstream,
		}
		if route.Listen == route.Upstream {
			plan.DirectPublications = append(plan.DirectPublications, ManagedDirectPublication{
				ID:     route.ID,
				Host:   route.Host,
				Listen: route.Listen,
			})
			continue
		}
		plan.RelayRoutes = append(plan.RelayRoutes, route)
	}
	slices.SortFunc(plan.RelayRoutes, compareManagedNativeRoutes)
	slices.SortFunc(plan.DirectPublications, compareManagedDirectPublications)
	if err := validateManagedNativePublicationCollisions(plan); err != nil {
		return ManagedNativePublicationPlan{}, err
	}
	return plan, nil
}

// PlanManagedNativeRoutes returns the relay portion of a managed publication plan.
//
// Callers that acknowledge managed-session barriers must use PlanManagedNativePublications so direct socket bindings
// remain explicit instead of being silently omitted from the result.
func PlanManagedNativeRoutes(input ManagedPublicationPlanInput) ([]dataplane.NativeRoute, error) {
	plan, err := PlanManagedNativePublications(input)
	if err != nil {
		return nil, err
	}
	return plan.RelayRoutes, nil
}

// MergeManagedNativePublicationPlans combines independently fenced project plans into one relay reconciliation set.
func MergeManagedNativePublicationPlans(plans []ManagedNativePublicationPlan) (ManagedNativePublicationPlan, error) {
	merged := ManagedNativePublicationPlan{
		RelayRoutes:        []dataplane.NativeRoute{},
		DirectPublications: []ManagedDirectPublication{},
	}
	for _, plan := range plans {
		merged.RelayRoutes = append(merged.RelayRoutes, plan.RelayRoutes...)
		merged.DirectPublications = append(merged.DirectPublications, plan.DirectPublications...)
	}
	slices.SortFunc(merged.RelayRoutes, compareManagedNativeRoutes)
	slices.SortFunc(merged.DirectPublications, compareManagedDirectPublications)
	if err := validateManagedNativePublicationCollisions(merged); err != nil {
		return ManagedNativePublicationPlan{}, err
	}
	return merged, nil
}

// compareManagedNativeRoutes keeps route publication order stable across map iteration and reconnects.
func compareManagedNativeRoutes(left dataplane.NativeRoute, right dataplane.NativeRoute) int {
	if left.Host != right.Host {
		if left.Host < right.Host {
			return -1
		}
		return 1
	}
	if left.ID < right.ID {
		return -1
	}
	if left.ID > right.ID {
		return 1
	}
	return 0
}

// compareManagedDirectPublications keeps direct barrier outcomes stable across map iteration and reconnects.
func compareManagedDirectPublications(left, right ManagedDirectPublication) int {
	if left.Host != right.Host {
		if left.Host < right.Host {
			return -1
		}
		return 1
	}
	if left.ID < right.ID {
		return -1
	}
	if left.ID > right.ID {
		return 1
	}
	return 0
}

// validateManagedNativeRouteCollisions rejects ambiguous public or private socket joins before relay creation.
func validateManagedNativeRouteCollisions(routes []dataplane.NativeRoute) error {
	hosts := make(map[string]struct{}, len(routes))
	listeners := make(map[netip.AddrPort]struct{}, len(routes))
	upstreams := make(map[netip.AddrPort]struct{}, len(routes))
	for _, route := range routes {
		if _, duplicate := hosts[route.Host]; duplicate {
			return fmt.Errorf("managed publication route host %q is duplicated", route.Host)
		}
		hosts[route.Host] = struct{}{}
		if _, duplicate := listeners[route.Listen]; duplicate {
			return fmt.Errorf("managed publication route listener %s is duplicated", route.Listen)
		}
		listeners[route.Listen] = struct{}{}
		if _, duplicate := upstreams[route.Upstream]; duplicate {
			return fmt.Errorf("managed publication route upstream %s is duplicated", route.Upstream)
		}
		upstreams[route.Upstream] = struct{}{}
		if route.Listen == route.Upstream {
			return fmt.Errorf("managed publication route %q upstream %s equals its public listener", route.ID, route.Upstream)
		}
	}
	for _, route := range routes {
		if _, public := listeners[route.Upstream]; public {
			return fmt.Errorf("managed publication route %q upstream %s is another public listener", route.ID, route.Upstream)
		}
	}
	return nil
}

// validateManagedNativePublicationCollisions keeps direct sockets and relay sockets in the same exclusive namespace.
func validateManagedNativePublicationCollisions(plan ManagedNativePublicationPlan) error {
	routes := append([]dataplane.NativeRoute(nil), plan.RelayRoutes...)
	for _, direct := range plan.DirectPublications {
		routes = append(routes, dataplane.NativeRoute{
			ID:     direct.ID,
			Host:   direct.Host,
			Listen: direct.Listen,
		})
	}
	hosts := make(map[string]struct{}, len(routes))
	listeners := make(map[netip.AddrPort]struct{}, len(routes))
	upstreams := make(map[netip.AddrPort]struct{}, len(plan.RelayRoutes))
	for _, route := range routes {
		if _, duplicate := hosts[route.Host]; duplicate {
			return fmt.Errorf("managed publication route host %q is duplicated", route.Host)
		}
		hosts[route.Host] = struct{}{}
		if _, duplicate := listeners[route.Listen]; duplicate {
			return fmt.Errorf("managed publication route listener %s is duplicated", route.Listen)
		}
		listeners[route.Listen] = struct{}{}
	}
	for _, route := range plan.RelayRoutes {
		if _, duplicate := upstreams[route.Upstream]; duplicate {
			return fmt.Errorf("managed publication route upstream %s is duplicated", route.Upstream)
		}
		upstreams[route.Upstream] = struct{}{}
		if _, public := listeners[route.Upstream]; public {
			return fmt.Errorf("managed publication route %q upstream %s is another public listener", route.ID, route.Upstream)
		}
	}
	return nil
}
