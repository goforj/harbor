package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/state"
)

const (
	// primaryLeaseServiceEndpointInitialGeneration starts a descriptor service endpoint's independent shape history.
	primaryLeaseServiceEndpointInitialGeneration uint64 = 1
	// primaryLeaseServiceEndpointIDPrefix keeps Harbor-owned service endpoint keys separate from resource IDs.
	primaryLeaseServiceEndpointIDPrefix = "service:"
)

// assignServiceEndpointReservations replaces Harbor-owned native service reservations from one validated descriptor.
//
// This boundary records only public endpoint authority. A native relay is intentionally not published here because
// its private upstream must come from a fresh managed-session or container observation, never from static intent.
func (coordinator *projectPrimaryLeaseCoordinator) assignServiceEndpointReservations(
	ctx context.Context,
	projectID domain.ProjectID,
	requirements []goforj.ServiceRequirement,
) error {
	if coordinator == nil {
		panic("reconcile.projectPrimaryLeaseCoordinator.assignServiceEndpointReservations requires a non-nil receiver")
	}
	if err := projectID.Validate(); err != nil {
		return err
	}
	if requirements == nil {
		return errors.New("descriptor service endpoint assignment requires initialized requirements")
	}
	ctx = normalizeLifecycleContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}

	var lastConflict error
	for attempt := 0; attempt < primaryLeasePersistenceAttempts; attempt++ {
		project, err := coordinator.state.Project(ctx, projectID)
		if err != nil {
			return fmt.Errorf("read project before service endpoint assignment: %w", err)
		}
		network, initialized, err := coordinator.state.Network(ctx)
		if err != nil {
			return fmt.Errorf("read network before service endpoint assignment: %w", err)
		}
		if !initialized {
			return fmt.Errorf("assign service endpoints for project %q: network identity is not initialized", projectID)
		}
		if err := network.Validate(); err != nil {
			return fmt.Errorf("assign service endpoints for project %q: invalid network authority: %w", projectID, err)
		}
		if network.Stage != state.NetworkStageResolver && network.Stage != state.NetworkStageFull {
			return nil
		}
		primary, found := primaryLeaseForKey(network.Leases, identity.LeaseKey{ProjectID: projectID})
		if !found {
			return fmt.Errorf("assign service endpoints for project %q: primary lease is missing", projectID)
		}
		desired, err := primaryLeaseServiceEndpoints(network, project, primary, requirements)
		if err != nil {
			return err
		}
		current := projectNetworkEndpoints(network, projectID)
		if endpointReservationsEqual(current, desired) {
			return nil
		}
		at := lifecycleTime(coordinator.now())
		if at.Before(project.Project.UpdatedAt) {
			at = project.Project.UpdatedAt
		}
		if at.Before(network.UpdatedAt) {
			at = network.UpdatedAt
		}
		result, err := coordinator.state.ReplaceProjectNetwork(ctx, state.ReplaceProjectNetworkRequest{
			ProjectID:               projectID,
			ExpectedNetworkRevision: network.Revision,
			ExpectedProjectRevision: project.Revision,
			Ensures:                 []state.NetworkLeaseEnsure{},
			Releases:                []state.NetworkLeaseRelease{},
			Endpoints:               desired,
			At:                      at,
		})
		if err != nil {
			if primaryLeaseRevisionConflict(err) {
				lastConflict = err
				continue
			}
			return fmt.Errorf("persist service endpoints for project %q: %w", projectID, err)
		}
		if err := result.Validate(); err != nil {
			return fmt.Errorf("validate persisted service endpoints for project %q: %w", projectID, err)
		}
		if got := projectNetworkEndpoints(result.Record, projectID); !endpointReservationsEqual(got, desired) {
			return fmt.Errorf("persisted service endpoints for project %q differ from requested authority", projectID)
		}
		return nil
	}
	return fmt.Errorf(
		"assign service endpoints for project %q did not converge after %d revisions: %w",
		projectID,
		primaryLeasePersistenceAttempts,
		lastConflict,
	)
}

// primaryLeaseServiceEndpoints derives native endpoint reservations while preserving HTTP and non-managed authority.
func primaryLeaseServiceEndpoints(
	network state.NetworkRecord,
	project state.ProjectRecord,
	primary identity.Lease,
	requirements []goforj.ServiceRequirement,
) ([]state.EndpointReservation, error) {
	if err := project.Project.Validate(); err != nil {
		return nil, fmt.Errorf("validate project before service endpoint assignment: %w", err)
	}
	if err := primary.Validate(); err != nil {
		return nil, fmt.Errorf("validate primary lease before service endpoint assignment: %w", err)
	}
	if primary.Key != (identity.LeaseKey{ProjectID: project.Project.ID}) {
		return nil, fmt.Errorf("service endpoint assignment requires the project's primary lease")
	}
	current := projectNetworkEndpoints(network, project.Project.ID)
	result := make([]state.EndpointReservation, 0, len(current)+len(requirements))
	existing := make(map[state.EndpointReservationKey]state.EndpointReservation, len(current))
	hosts := make(map[string]state.EndpointReservation, len(current))
	for _, endpoint := range current {
		existing[endpoint.Key] = endpoint
		if strings.HasPrefix(endpoint.Key.EndpointID, primaryLeaseServiceEndpointIDPrefix) {
			continue
		}
		result = append(result, endpoint)
		if prior, duplicate := hosts[endpoint.Host]; duplicate && prior.Key != endpoint.Key {
			return nil, fmt.Errorf("project %q has duplicate preserved endpoint host %q", project.Project.ID, endpoint.Host)
		}
		hosts[endpoint.Host] = endpoint
	}

	seenEndpoints := make(map[string]struct{})
	for _, requirement := range requirements {
		if requirement.Owner == goforj.ServiceRequirementOwnerAvailable {
			continue
		}
		if requirement.Owner != goforj.ServiceRequirementOwnerCompose && requirement.Owner != goforj.ServiceRequirementOwnerExternal {
			return nil, fmt.Errorf("service requirement %q owner %q is unsupported for native endpoint assignment", requirement.ID, requirement.Owner)
		}
		for _, endpoint := range requirement.Endpoints {
			if endpoint.Visibility != goforj.ServiceEndpointVisibilityHost {
				continue
			}
			if endpoint.Protocol != goforj.ServiceEndpointProtocolTCP {
				return nil, fmt.Errorf("service endpoint %q protocol %q cannot be host-published before managed-session observation", endpoint.ID, endpoint.Protocol)
			}
			if endpoint.NativePort < 1 || endpoint.NativePort > 65535 {
				return nil, fmt.Errorf("service endpoint %q native port %d is outside 1-65535", endpoint.ID, endpoint.NativePort)
			}
			endpointID := primaryLeaseServiceEndpointIDPrefix + endpoint.ID
			if _, duplicate := seenEndpoints[endpointID]; duplicate {
				return nil, fmt.Errorf("duplicate service endpoint reservation ID %q", endpointID)
			}
			seenEndpoints[endpointID] = struct{}{}
			host, err := projectServiceEndpointHost(project.Project.Slug, requirement.ServiceKey)
			if err != nil {
				return nil, fmt.Errorf("service endpoint %q: %w", endpoint.ID, err)
			}
			key := state.EndpointReservationKey{ProjectID: project.Project.ID, EndpointID: endpointID}
			if prior, conflict := existing[key]; conflict && prior.Protocol != state.EndpointProtocolTCP {
				return nil, fmt.Errorf("service endpoint %q conflicts with non-TCP endpoint", endpoint.ID)
			}
			if prior, duplicate := hosts[host]; duplicate && prior.Key != key {
				return nil, fmt.Errorf("service endpoint %q host %q collides with endpoint %q", endpoint.ID, host, prior.Key.EndpointID)
			}
			public := netip.AddrPortFrom(primary.Address.Unmap(), uint16(endpoint.NativePort))
			generation := primaryLeaseServiceEndpointInitialGeneration
			if prior, exists := existing[key]; exists {
				if prior.Protocol == state.EndpointProtocolTCP && prior.Host == host && prior.Public == public && prior.Identity != nil && *prior.Identity == primary.Key {
					generation = prior.Generation
				} else if prior.Generation == ^uint64(0) {
					return nil, fmt.Errorf("service endpoint %q generation cannot advance", endpoint.ID)
				} else {
					generation = prior.Generation + 1
				}
			}
			reservation := state.EndpointReservation{
				Key:        key,
				Protocol:   state.EndpointProtocolTCP,
				Host:       host,
				Public:     public,
				Identity:   &primary.Key,
				Generation: generation,
			}
			if prior, duplicate := hosts[host]; duplicate && prior.Key != reservation.Key {
				return nil, fmt.Errorf("service endpoint %q host %q collides with endpoint %q", endpoint.ID, host, prior.Key.EndpointID)
			}
			hosts[host] = reservation
			result = append(result, reservation)
		}
	}
	slices.SortFunc(result, projectEndpointReservationCompare)
	return result, nil
}

// projectServiceEndpointHost applies the stable native service naming policy inside one project zone.
func projectServiceEndpointHost(slug string, serviceKey string) (string, error) {
	if !validProjectResourceHostLabel(serviceKey) {
		return "", fmt.Errorf("service key %q must be a lowercase DNS label for native endpoint publication", serviceKey)
	}
	return serviceKey + "." + slug + ".test", nil
}
