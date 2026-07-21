package reconcile

import (
	"context"
	"fmt"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/managedsession"
)

// managedPublicationObserver is the daemon-owned observation boundary used by the authenticated managed barrier.
//
// The process adapter must not be trusted to invent private upstreams. Harbor re-observes the exact supervised
// Compose session here, joins those facts to the descriptor and durable reservations, and only then allows the
// authority layer to replace native relays.
type managedPublicationObserver interface {
	ObserveManagedPublications(context.Context, domain.ProjectID, domain.SessionID, harbordruntime.ManagedPublicationFence) ([]harbordruntime.ManagedEndpointPublication, error)
}

// ObserveManagedPublications returns the complete Harbor-observed host publication set for one attached session.
func (coordinator *ProjectLifecycleCoordinator) ObserveManagedPublications(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	fence harbordruntime.ManagedPublicationFence,
) ([]harbordruntime.ManagedEndpointPublication, error) {
	return coordinator.observeManagedPublications(ctx, projectID, sessionID, fence, false)
}

// ObserveManagedPublicationsForPhase joins Compose service ports before App readiness when the authenticated barrier allows it.
func (coordinator *ProjectLifecycleCoordinator) ObserveManagedPublicationsForPhase(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	fence harbordruntime.ManagedPublicationFence,
	allowProjectStarting bool,
) ([]harbordruntime.ManagedEndpointPublication, error) {
	return coordinator.observeManagedPublications(ctx, projectID, sessionID, fence, allowProjectStarting)
}

// observeManagedPublications performs the fenced descriptor and service-port join for one lifecycle phase.
func (coordinator *ProjectLifecycleCoordinator) observeManagedPublications(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	fence harbordruntime.ManagedPublicationFence,
	allowProjectStarting bool,
) ([]harbordruntime.ManagedEndpointPublication, error) {
	if coordinator == nil {
		panic("reconcile.ProjectLifecycleCoordinator.ObserveManagedPublications requires a non-nil receiver")
	}
	ctx = normalizeLifecycleContext(ctx)
	if err := projectID.Validate(); err != nil {
		return nil, err
	}
	if err := sessionID.Validate(); err != nil {
		return nil, err
	}
	if fence.ProjectID != projectID || fence.SessionID != sessionID {
		return nil, fmt.Errorf("managed publication fence does not match project session")
	}

	project, err := coordinator.state.Project(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: read managed publication project %q: %w", managedsession.ErrManagedSessionNotReady, projectID, err)
	}
	active, err := coordinator.state.ActiveProjectSession(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("%w: read managed publication session %q: %w", managedsession.ErrManagedSessionNotReady, sessionID, err)
	}
	if active.ID != sessionID || active.Generation != fence.SessionGeneration || active.State != domain.SessionAttached || active.Process == nil {
		return nil, fmt.Errorf("%w: managed publication session %q is not the attached fence", managedsession.ErrManagedSessionNotReady, sessionID)
	}
	if project.Project.State != domain.ProjectReady && !(allowProjectStarting && project.Project.State == domain.ProjectStarting) {
		return nil, fmt.Errorf("%w: managed publication project %q is %q, not ready", managedsession.ErrManagedSessionNotReady, projectID, project.Project.State)
	}

	descriptorObserver, ok := coordinator.supervisor.(projectDescriptorObserver)
	if !ok {
		return nil, fmt.Errorf("managed publication descriptor observer is unavailable")
	}
	descriptor, err := descriptorObserver.ObserveProjectDescriptor(ctx, project.Project.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: observe managed publication descriptor for project %q: %w", managedsession.ErrManagedSessionNotReady, projectID, err)
	}
	if !descriptor.ServiceRequirementsSupported {
		return []harbordruntime.ManagedEndpointPublication{}, nil
	}

	network, initialized, err := coordinator.primaryLeases.state.Network(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: read managed publication network: %w", managedsession.ErrManagedSessionNotReady, err)
	}
	if !initialized {
		return nil, fmt.Errorf("%w: managed publication network is not initialized", managedsession.ErrManagedSessionNotReady)
	}
	if err := network.Validate(); err != nil {
		return nil, fmt.Errorf("validate managed publication network: %w", err)
	}

	portReader, ok := coordinator.supervisor.(projectServicePortReader)
	if !ok {
		return nil, fmt.Errorf("managed publication service-port observer is unavailable")
	}
	serviceIDs := managedPublicationServiceIDs(descriptor.ServiceRequirements)
	servicePorts := make([]harbordruntime.ManagedServicePortObservation, 0, len(serviceIDs))
	for _, serviceID := range serviceIDs {
		observation, observeErr := portReader.ObserveServicePorts(ctx, projectID, sessionID, serviceID)
		if observeErr != nil {
			return nil, fmt.Errorf("%w: observe managed publication service %q: %w", managedsession.ErrManagedSessionNotReady, serviceID, observeErr)
		}
		servicePorts = append(servicePorts, harbordruntime.ManagedServicePortObservation{
			ServiceID:   serviceID,
			Observation: observation,
		})
	}
	input := harbordruntime.ManagedPublicationObservationInput{
		Fence:        fence,
		Requirements: descriptor.ServiceRequirements,
		ServicePorts: servicePorts,
		Reservations: network.Reservations.Endpoints,
	}
	publications, err := harbordruntime.NormalizeManagedEndpointPublications(input)
	if err != nil {
		return nil, fmt.Errorf("%w: normalize managed publication observations: %w", managedsession.ErrManagedSessionNotReady, err)
	}
	if err := harbordruntime.ValidateManagedEndpointPublicationsComplete(input, publications); err != nil {
		return nil, fmt.Errorf("%w: %w", managedsession.ErrManagedSessionNotReady, err)
	}
	return publications, nil
}

// managedPublicationServiceIDs returns every service identity needed to match selected host TCP endpoints.
func managedPublicationServiceIDs(requirements []goforj.ServiceRequirement) []domain.ServiceID {
	seen := make(map[domain.ServiceID]struct{})
	for _, requirement := range requirements {
		if requirement.Owner != goforj.ServiceRequirementOwnerCompose || requirement.Lifecycle != goforj.ServiceRequirementLifecycleProject {
			continue
		}
		for _, endpoint := range requirement.Endpoints {
			if endpoint.Protocol != goforj.ServiceEndpointProtocolTCP || endpoint.Visibility != goforj.ServiceEndpointVisibilityHost {
				continue
			}
			seen[domain.ServiceID(requirement.ServiceKey)] = struct{}{}
		}
	}
	serviceIDs := make([]domain.ServiceID, 0, len(seen))
	for serviceID := range seen {
		serviceIDs = append(serviceIDs, serviceID)
	}
	slices.Sort(serviceIDs)
	return serviceIDs
}

var _ managedPublicationObserver = (*ProjectLifecycleCoordinator)(nil)
