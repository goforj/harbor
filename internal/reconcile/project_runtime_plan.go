package reconcile

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/managedsession"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

const managedRuntimeHTTPID = "http"

// PlanManagedRuntime derives one complete private runtime assignment from Harbor-owned lease and observation facts.
//
// App binds come from the static descriptor's declared ports and the project's durable loopback identity. Service
// binds are admitted only after the existing Compose observer proves the matching host publication, so this method
// never turns a default port or a stale reservation into a private upstream.
func (coordinator *ProjectLifecycleCoordinator) PlanManagedRuntime(
	ctx context.Context,
	request managedsession.RuntimePlanRequest,
) (managedsession.RuntimePlanResponse, error) {
	if coordinator == nil {
		panic("reconcile.ProjectLifecycleCoordinator.PlanManagedRuntime requires a non-nil receiver")
	}
	if err := request.Validate(); err != nil {
		return managedsession.RuntimePlanResponse{}, err
	}
	ctx = normalizeLifecycleContext(ctx)
	if err := ctx.Err(); err != nil {
		return managedsession.RuntimePlanResponse{}, err
	}
	project, err := coordinator.state.Project(ctx, request.Fence.ProjectID)
	if err != nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: read managed runtime project %q: %w", managedsession.ErrManagedSessionNotReady, request.Fence.ProjectID, err)
	}
	active, err := coordinator.state.ActiveProjectSession(ctx, request.Fence.ProjectID)
	if err != nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: read managed runtime session %q: %w", managedsession.ErrManagedSessionNotReady, request.Fence.SessionID, err)
	}
	if active.ID != request.Fence.SessionID || active.Generation != request.Fence.SessionGeneration || active.State != domain.SessionAttached || active.Process == nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: managed runtime fence is not the attached process", managedsession.ErrManagedSessionNotReady)
	}
	descriptorObserver, ok := coordinator.supervisor.(projectDescriptorObserver)
	if !ok {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: managed runtime descriptor observer is unavailable", managedsession.ErrManagedSessionNotReady)
	}
	descriptor, err := descriptorObserver.ObserveProjectDescriptor(ctx, project.Project.Path)
	if err != nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: observe managed runtime descriptor: %w", managedsession.ErrManagedSessionNotReady, err)
	}
	if active.DescriptorDigest == "" || descriptor.TopologyDigest != active.DescriptorDigest {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: managed runtime descriptor digest changed after attachment", managedsession.ErrManagedSessionNotReady)
	}
	network, initialized, err := coordinator.primaryLeases.state.Network(ctx)
	if err != nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: read managed runtime network: %w", managedsession.ErrManagedSessionNotReady, err)
	}
	if !initialized {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: managed runtime network is not initialized", managedsession.ErrManagedSessionNotReady)
	}
	if err := network.Validate(); err != nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: validate managed runtime network: %w", managedsession.ErrManagedSessionNotReady, err)
	}
	primary, found := primaryLeaseForKey(network.Leases, primaryLeaseKey(request.Fence.ProjectID))
	if !found {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: managed runtime primary lease is missing", managedsession.ErrManagedSessionNotReady)
	}
	apps, err := managedRuntimePlanApps(request, descriptor.Apps, primary.Address, network.Reservations.Endpoints)
	if err != nil {
		return managedsession.RuntimePlanResponse{}, err
	}
	serviceEndpoints, err := coordinator.managedRuntimeServiceEndpoints(ctx, request.Fence, descriptor, network.Reservations.Endpoints)
	if err != nil {
		return managedsession.RuntimePlanResponse{}, err
	}
	response := managedsession.RuntimePlanResponse{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         request.Fence,
		Plan: managedsession.RuntimePlan{
			Apps:             apps,
			ServiceEndpoints: serviceEndpoints,
		},
	}
	if err := managedsession.ValidateRuntimePlanCorrelation(request, response); err != nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("validate managed runtime plan: %w", err)
	}
	return response, nil
}

// managedRuntimeServiceEndpoints observes and joins every selected host service endpoint for one exact fence.
func (coordinator *ProjectLifecycleCoordinator) managedRuntimeServiceEndpoints(
	ctx context.Context,
	fence harbordruntime.ManagedPublicationFence,
	descriptor projectprocess.ProjectDescriptorObservation,
	reservations []state.EndpointReservation,
) ([]managedsession.RuntimePlanServiceEndpoint, error) {
	if !descriptor.ServiceRequirementsSupported {
		return []managedsession.RuntimePlanServiceEndpoint{}, nil
	}
	publications, err := coordinator.ObserveManagedPublicationsForPhase(ctx, fence.ProjectID, fence.SessionID, fence, true)
	if err != nil {
		return nil, fmt.Errorf("%w: observe managed runtime service publications: %w", managedsession.ErrManagedSessionNotReady, err)
	}
	publicationByID := make(map[string]harbordruntime.ManagedEndpointPublication, len(publications))
	for _, publication := range publications {
		if _, duplicate := publicationByID[publication.EndpointID]; duplicate {
			return nil, fmt.Errorf("managed runtime service publication %q is duplicated", publication.EndpointID)
		}
		publicationByID[publication.EndpointID] = publication
	}
	reservationByID := make(map[string]state.EndpointReservation, len(reservations))
	for _, reservation := range reservations {
		if reservation.Key.ProjectID != fence.ProjectID {
			continue
		}
		if _, duplicate := reservationByID[reservation.Key.EndpointID]; duplicate {
			return nil, fmt.Errorf("managed runtime service reservation %q is duplicated", reservation.Key.EndpointID)
		}
		reservationByID[reservation.Key.EndpointID] = reservation
	}
	endpoints := make([]managedsession.RuntimePlanServiceEndpoint, 0)
	for _, requirement := range descriptor.ServiceRequirements {
		if requirement.Owner != goforj.ServiceRequirementOwnerCompose || requirement.Lifecycle != goforj.ServiceRequirementLifecycleProject {
			continue
		}
		consumers := slices.Clone(requirement.Consumers)
		slices.Sort(consumers)
		for _, declared := range requirement.Endpoints {
			if declared.Protocol != goforj.ServiceEndpointProtocolTCP || declared.Visibility != goforj.ServiceEndpointVisibilityHost {
				continue
			}
			endpointID := primaryLeaseServiceEndpointIDPrefix + declared.ID
			publication, observed := publicationByID[endpointID]
			if !observed {
				return nil, fmt.Errorf("%w: managed runtime service endpoint %q has not been observed", managedsession.ErrManagedSessionNotReady, endpointID)
			}
			reservation, reserved := reservationByID[endpointID]
			if !reserved {
				return nil, fmt.Errorf("managed runtime service endpoint %q has no durable reservation", endpointID)
			}
			endpoint, err := managedRuntimeServiceEndpoint(fence, requirement, consumers, declared, publication, reservation)
			if err != nil {
				return nil, err
			}
			endpoints = append(endpoints, endpoint)
		}
	}
	slices.SortFunc(endpoints, func(left, right managedsession.RuntimePlanServiceEndpoint) int {
		return strings.Compare(left.ID, right.ID)
	})
	return endpoints, nil
}

// managedRuntimeServiceEndpoint joins one observed publication to its exact durable reservation generation.
func managedRuntimeServiceEndpoint(
	fence harbordruntime.ManagedPublicationFence,
	requirement goforj.ServiceRequirement,
	consumers []string,
	declared goforj.ServiceEndpoint,
	publication harbordruntime.ManagedEndpointPublication,
	reservation state.EndpointReservation,
) (managedsession.RuntimePlanServiceEndpoint, error) {
	if reservation.Protocol != state.EndpointProtocolTCP {
		return managedsession.RuntimePlanServiceEndpoint{}, fmt.Errorf("managed runtime service endpoint %q reservation protocol %q is not TCP", publication.EndpointID, reservation.Protocol)
	}
	if publication.Fence != fence {
		return managedsession.RuntimePlanServiceEndpoint{}, fmt.Errorf("managed runtime service publication %q does not match the requested fence", publication.EndpointID)
	}
	if publication.ReservationGeneration != reservation.Generation {
		return managedsession.RuntimePlanServiceEndpoint{}, fmt.Errorf("managed runtime service endpoint %q publication generation %d does not match durable generation %d", publication.EndpointID, publication.ReservationGeneration, reservation.Generation)
	}
	return managedsession.RuntimePlanServiceEndpoint{
		ID:            publication.EndpointID,
		RequirementID: requirement.ID,
		Consumers:     slices.Clone(consumers),
		PublishHost:   publication.Upstream.Addr().String(),
		PublishPort:   publication.Upstream.Port(),
		PublicHost:    reservation.Host,
		PublicPort:    reservation.Public.Port(),
	}, nil
}

// managedRuntimePlanApps translates descriptor runtime intent into exact loopback assignments for one request.
func managedRuntimePlanApps(
	request managedsession.RuntimePlanRequest,
	descriptorApps []goforj.App,
	primary netip.Addr,
	reservations []state.EndpointReservation,
) ([]managedsession.RuntimePlanApp, error) {
	if !primary.IsValid() || !primary.Is4() || !primary.IsLoopback() || primary != primary.Unmap() {
		return nil, fmt.Errorf("managed runtime primary lease address must be canonical IPv4 loopback")
	}
	appsByID := make(map[string]goforj.App, len(descriptorApps))
	for _, app := range descriptorApps {
		if _, duplicate := appsByID[app.ID]; duplicate {
			return nil, fmt.Errorf("managed runtime descriptor App %q is duplicated", app.ID)
		}
		appsByID[app.ID] = app
	}
	reservationByID := make(map[string]state.EndpointReservation, len(reservations))
	for _, reservation := range reservations {
		if reservation.Key.ProjectID != request.Fence.ProjectID {
			continue
		}
		if _, duplicate := reservationByID[reservation.Key.EndpointID]; duplicate {
			return nil, fmt.Errorf("managed runtime reservation %q is duplicated", reservation.Key.EndpointID)
		}
		reservationByID[reservation.Key.EndpointID] = reservation
	}
	usedBinds := make(map[string]string)
	apps := make([]managedsession.RuntimePlanApp, 0, len(request.ActiveApps))
	for _, active := range request.ActiveApps {
		descriptor, found := appsByID[string(active.ID)]
		if !found {
			return nil, fmt.Errorf("managed runtime request App %q is not in the descriptor", active.ID)
		}
		runtimesByID := make(map[string]goforj.Runtime, len(descriptor.Runtimes))
		for _, runtime := range descriptor.Runtimes {
			if _, duplicate := runtimesByID[runtime.ID]; duplicate {
				return nil, fmt.Errorf("managed runtime descriptor App %q runtime %q is duplicated", active.ID, runtime.ID)
			}
			runtimesByID[runtime.ID] = runtime
		}
		plannedRuntimes := make([]managedsession.RuntimePlanRuntime, 0, len(active.RuntimeIDs))
		for _, runtimeID := range active.RuntimeIDs {
			runtime, found := runtimesByID[runtimeID]
			if !found {
				return nil, fmt.Errorf("managed runtime request App %q runtime %q is not in the descriptor", active.ID, runtimeID)
			}
			if runtime.DefaultPort < 1024 || runtime.DefaultPort > 65535 {
				return nil, fmt.Errorf("managed runtime App %q runtime %q default port %d cannot be assigned without low-port support", active.ID, runtime.ID, runtime.DefaultPort)
			}
			bind := primary.String() + ":" + strconv.Itoa(runtime.DefaultPort)
			if previous, duplicate := usedBinds[bind]; duplicate {
				return nil, fmt.Errorf("managed runtime App %q runtime %q conflicts with %s at %s", active.ID, runtime.ID, previous, bind)
			}
			usedBinds[bind] = string(active.ID) + "/" + runtime.ID
			routes := []managedsession.RuntimePlanRoute{}
			if runtime.ReadinessPath != "" {
				routes = append(routes, managedsession.RuntimePlanRoute{Name: "readiness", Path: runtime.ReadinessPath})
			}
			plannedRuntimes = append(plannedRuntimes, managedsession.RuntimePlanRuntime{
				ID:        runtime.ID,
				BindHost:  primary.String(),
				BindPort:  uint16(runtime.DefaultPort),
				PublicURL: managedRuntimePublicURL(active.ID, runtime, reservationByID),
				Routes:    routes,
			})
		}
		apps = append(apps, managedsession.RuntimePlanApp{ID: string(active.ID), Active: true, Runtimes: plannedRuntimes})
	}
	return apps, nil
}

// managedRuntimePublicURL joins one public runtime intent to an existing Harbor HTTP reservation without inventing a route.
func managedRuntimePublicURL(appID domain.AppID, runtime goforj.Runtime, reservations map[string]state.EndpointReservation) string {
	if !runtime.PublicURL {
		return ""
	}
	endpointID := ""
	if appID == domain.AppID("app") && runtime.ID == managedRuntimeHTTPID {
		endpointID = primaryLeaseDefaultHTTPEndpointID
	}
	if endpointID == "" {
		return ""
	}
	reservation, found := reservations[endpointID]
	if !found || reservation.Protocol != state.EndpointProtocolHTTP || strings.TrimSpace(reservation.Host) == "" {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: reservation.Host}).String()
}

// primaryLeaseKey keeps the runtime planner's durable identity lookup explicit and project-scoped.
func primaryLeaseKey(projectID domain.ProjectID) identity.LeaseKey {
	return identity.LeaseKey{ProjectID: projectID}
}
