package harbordruntime

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

const managedPublicationServiceEndpointPrefix = "service:"

var (
	// ErrManagedPublicationsIncomplete means Harbor has not observed every declared host endpoint yet.
	ErrManagedPublicationsIncomplete = errors.New("managed endpoint publications are incomplete")
)

// ManagedServicePortObservation pairs one exact Compose service identity with its current host port facts.
type ManagedServicePortObservation struct {
	ServiceID   domain.ServiceID
	Observation projectprocess.ServicePortObservation
}

// ManagedPublicationObservationInput combines admitted GoForj service intent with read-only host observations.
type ManagedPublicationObservationInput struct {
	Fence        ManagedPublicationFence
	Requirements []goforj.ServiceRequirement
	ServicePorts []ManagedServicePortObservation
	Reservations []state.EndpointReservation
}

// NormalizeManagedEndpointPublications converts exact observed host publications into planner inputs.
//
// Only selected Compose-owned host TCP endpoints become publications. Unknown or unselected facts are ignored;
// declared endpoints without a complete observation withdraw the complete replacement set. A malformed matching
// fact returns an error so callers can leave the registry's last good replacement untouched.
func NormalizeManagedEndpointPublications(input ManagedPublicationObservationInput) ([]ManagedEndpointPublication, error) {
	if err := input.Fence.Validate(); err != nil {
		return nil, err
	}
	targets, err := managedPublicationTargets(input.Requirements)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return []ManagedEndpointPublication{}, nil
	}
	observations, err := managedPublicationServiceObservations(input.ServicePorts)
	if err != nil {
		return nil, err
	}
	reservations, err := managedPublicationReservations(input.Fence, input.Reservations)
	if err != nil {
		return nil, err
	}

	publications := make([]ManagedEndpointPublication, 0, len(targets))
	incomplete := false
	for endpointID, target := range targets {
		observation, found := observations[target.ServiceID]
		if !found || !observation.Supported || !observation.Available {
			incomplete = true
			continue
		}
		if observation.Ports == nil {
			return nil, fmt.Errorf("managed publication service %q has a nil port observation", target.ServiceID)
		}
		port, found, err := matchingManagedServicePort(target, observation.Ports)
		if err != nil {
			return nil, err
		}
		if !found {
			incomplete = true
			continue
		}
		reservation, found := reservations[endpointID]
		if !found {
			return nil, fmt.Errorf("managed publication endpoint %q has no exact durable reservation", endpointID)
		}
		if reservation.Protocol != state.EndpointProtocolTCP {
			return nil, fmt.Errorf("managed publication endpoint %q reservation protocol %q is not TCP", endpointID, reservation.Protocol)
		}
		upstreamAddress, err := canonicalManagedObservedAddress(port.Address)
		if err != nil {
			return nil, fmt.Errorf("managed publication endpoint %q: %w", endpointID, err)
		}
		if port.Public == 0 {
			return nil, fmt.Errorf("managed publication endpoint %q has no public host port", endpointID)
		}
		publication := ManagedEndpointPublication{
			Fence:                 input.Fence,
			EndpointID:            endpointID,
			ReservationGeneration: reservation.Generation,
			Upstream:              netip.AddrPortFrom(upstreamAddress, port.Public),
		}
		if err := publication.Validate(); err != nil {
			return nil, fmt.Errorf("validate managed publication endpoint %q: %w", endpointID, err)
		}
		publications = append(publications, publication)
	}
	if incomplete {
		return []ManagedEndpointPublication{}, nil
	}
	slices.SortFunc(publications, compareManagedEndpointPublications)
	return publications, nil
}

// ValidateManagedEndpointPublicationsComplete proves that one normalized replacement covers every declared host endpoint.
//
// A partial observation is deliberately represented as an empty replacement by NormalizeManagedEndpointPublications
// so stale native routes can be withdrawn. A lifecycle barrier must distinguish that safe withdrawal from a project
// that has no host-visible service endpoints, and therefore calls this check before acknowledging Compose.
func ValidateManagedEndpointPublicationsComplete(
	input ManagedPublicationObservationInput,
	publications []ManagedEndpointPublication,
) error {
	if err := input.Fence.Validate(); err != nil {
		return err
	}
	targets, err := managedPublicationTargets(input.Requirements)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(publications))
	for index, publication := range publications {
		if err := publication.Validate(); err != nil {
			return fmt.Errorf("managed publication %d: %w", index+1, err)
		}
		if publication.Fence != input.Fence {
			return fmt.Errorf("managed publication %q does not match the requested fence", publication.EndpointID)
		}
		if _, duplicate := seen[publication.EndpointID]; duplicate {
			return fmt.Errorf("managed publication endpoint %q is duplicated", publication.EndpointID)
		}
		seen[publication.EndpointID] = struct{}{}
	}
	if len(seen) != len(targets) {
		return fmt.Errorf("%w: observed %d of %d declared host endpoints", ErrManagedPublicationsIncomplete, len(seen), len(targets))
	}
	for endpointID := range targets {
		if _, found := seen[endpointID]; !found {
			return fmt.Errorf("%w: endpoint %q has not been observed", ErrManagedPublicationsIncomplete, endpointID)
		}
	}
	return nil
}

// managedPublicationTarget records one declared host-visible endpoint that requires an observed publication.
type managedPublicationTarget struct {
	ServiceID domain.ServiceID
	Native    uint16
}

// managedPublicationTargets keeps only selected Compose-owned host TCP requirements in stable endpoint identity form.
func managedPublicationTargets(requirements []goforj.ServiceRequirement) (map[string]managedPublicationTarget, error) {
	targets := make(map[string]managedPublicationTarget)
	for _, requirement := range requirements {
		if requirement.Owner != goforj.ServiceRequirementOwnerCompose || requirement.Lifecycle != goforj.ServiceRequirementLifecycleProject {
			continue
		}
		serviceID := domain.ServiceID(requirement.ServiceKey)
		if err := serviceID.Validate(); err != nil {
			return nil, fmt.Errorf("managed publication service requirement %q: %w", requirement.ID, err)
		}
		for _, endpoint := range requirement.Endpoints {
			if endpoint.Protocol != goforj.ServiceEndpointProtocolTCP || endpoint.Visibility != goforj.ServiceEndpointVisibilityHost {
				continue
			}
			if endpoint.ID == "" || endpoint.NativePort <= 0 || endpoint.NativePort > 65535 {
				return nil, fmt.Errorf("managed publication service endpoint %q has an invalid identity or native port", endpoint.ID)
			}
			endpointID := managedPublicationServiceEndpointPrefix + endpoint.ID
			if _, duplicate := targets[endpointID]; duplicate {
				return nil, fmt.Errorf("managed publication endpoint %q is declared more than once", endpointID)
			}
			targets[endpointID] = managedPublicationTarget{ServiceID: serviceID, Native: uint16(endpoint.NativePort)}
		}
	}
	return targets, nil
}

// managedPublicationServiceObservations rejects duplicate service identities before matching any endpoint.
func managedPublicationServiceObservations(observations []ManagedServicePortObservation) (map[domain.ServiceID]projectprocess.ServicePortObservation, error) {
	byService := make(map[domain.ServiceID]projectprocess.ServicePortObservation, len(observations))
	for index, observed := range observations {
		if err := observed.ServiceID.Validate(); err != nil {
			return nil, fmt.Errorf("managed publication service observation %d: %w", index+1, err)
		}
		if _, duplicate := byService[observed.ServiceID]; duplicate {
			return nil, fmt.Errorf("managed publication service %q was observed more than once", observed.ServiceID)
		}
		byService[observed.ServiceID] = observed.Observation
	}
	return byService, nil
}

// managedPublicationReservations indexes exact project reservations before a candidate can become a publication.
func managedPublicationReservations(fence ManagedPublicationFence, reservations []state.EndpointReservation) (map[string]state.EndpointReservation, error) {
	byEndpoint := make(map[string]state.EndpointReservation, len(reservations))
	for index, reservation := range reservations {
		if err := reservation.Validate(); err != nil {
			return nil, fmt.Errorf("managed publication reservation %d: %w", index+1, err)
		}
		if reservation.Key.ProjectID != fence.ProjectID {
			continue
		}
		endpointID := reservation.Key.EndpointID
		if _, duplicate := byEndpoint[endpointID]; duplicate {
			return nil, fmt.Errorf("managed publication reservation %q is duplicated", endpointID)
		}
		byEndpoint[endpointID] = reservation
	}
	return byEndpoint, nil
}

// matchingManagedServicePort returns one exact native-port candidate and rejects ambiguous matching replicas.
func matchingManagedServicePort(target managedPublicationTarget, ports []projectprocess.ServicePort) (projectprocess.ServicePort, bool, error) {
	var match projectprocess.ServicePort
	matches := 0
	for _, port := range ports {
		if port.Private != target.Native {
			continue
		}
		if !strings.EqualFold(port.Protocol, "tcp") {
			return projectprocess.ServicePort{}, false, fmt.Errorf("managed publication service %q native port %d uses protocol %q, not TCP", target.ServiceID, target.Native, port.Protocol)
		}
		matches++
		match = port
	}
	if matches > 1 {
		return projectprocess.ServicePort{}, false, fmt.Errorf("managed publication service %q native port %d has %d matching replicas", target.ServiceID, target.Native, matches)
	}
	return match, matches == 1, nil
}

// canonicalManagedObservedAddress accepts only the literal IPv4 loopback address emitted by the local runtime adapter.
func canonicalManagedObservedAddress(raw string) (netip.Addr, error) {
	address, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("observed host address %q is invalid: %w", raw, err)
	}
	if !address.Is4() || !address.IsLoopback() || address != address.Unmap() || address.String() != raw {
		return netip.Addr{}, fmt.Errorf("observed host address %q is not canonical IPv4 loopback", raw)
	}
	return address, nil
}

// compareManagedEndpointPublications keeps complete replacements deterministic across service map iteration.
func compareManagedEndpointPublications(left, right ManagedEndpointPublication) int {
	return strings.Compare(left.EndpointID, right.EndpointID)
}
