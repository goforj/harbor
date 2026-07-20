package projectprocess

import (
	"context"
	"fmt"
	"sort"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
)

// ServiceObservation is one complete replacement view of the active Compose services reported by the host runtime.
type ServiceObservation struct {
	Supported bool
	Services  []domain.ServiceSnapshot
}

// ServicePort is one non-secret port mapping observed for a selected Compose service.
type ServicePort struct {
	Address  string
	Private  uint16
	Public   uint16
	Protocol string
	Replica  int
}

// ServicePortObservation is the current, non-durable port view for one Compose service.
type ServicePortObservation struct {
	Supported bool
	Available bool
	Ports     []ServicePort
}

// ObserveServices asks Harbor's host-runtime adapter for Compose containers admitted to the exact supervised checkout.
func (supervisor *Supervisor) ObserveServices(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (ServiceObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ServiceObservation{}, err
	}
	if err := projectID.Validate(); err != nil {
		return ServiceObservation{}, fmt.Errorf("observe project services: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return ServiceObservation{}, fmt.Errorf("observe project services: %w", err)
	}
	checkoutRoot, found := supervisor.serviceObservationCheckout(projectID, sessionID)
	if !found {
		return ServiceObservation{}, ErrNotRunning
	}
	observed, err := supervisor.containerRuntime.ObserveProject(ctx, checkoutRoot)
	if err != nil {
		return ServiceObservation{}, fmt.Errorf("observe host container runtime: %w", err)
	}
	return projectRuntimeServices(observed)
}

// WaitServiceChange waits for one host-runtime event that may affect the supervised checkout's service topology.
//
// The event is only a wake hint. Callers must invoke ObserveServices again because the local Engine event stream is
// shared by neighboring Compose projects and does not establish Harbor ownership by itself.
func (supervisor *Supervisor) WaitServiceChange(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("wait for project service change: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return fmt.Errorf("wait for project service change: %w", err)
	}
	checkoutRoot, found := supervisor.serviceObservationCheckout(projectID, sessionID)
	if !found {
		return ErrNotRunning
	}
	source, ok := supervisor.containerRuntime.(containerruntime.ProjectChangeSource)
	if !ok {
		return containerruntime.ErrProjectChangeUnsupported
	}
	if err := source.WaitProjectChange(ctx, checkoutRoot); err != nil {
		return fmt.Errorf("wait for host container runtime change: %w", err)
	}
	return nil
}

// ObserveServicePorts returns only current runtime publication facts for one selected service.
func (supervisor *Supervisor) ObserveServicePorts(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	serviceID domain.ServiceID,
) (ServicePortObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ServicePortObservation{}, err
	}
	if err := projectID.Validate(); err != nil {
		return ServicePortObservation{}, fmt.Errorf("observe project service ports: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return ServicePortObservation{}, fmt.Errorf("observe project service ports: %w", err)
	}
	if err := serviceID.Validate(); err != nil {
		return ServicePortObservation{}, fmt.Errorf("observe project service ports: %w", err)
	}
	checkoutRoot, found := supervisor.serviceObservationCheckout(projectID, sessionID)
	if !found {
		return ServicePortObservation{}, ErrNotRunning
	}
	observed, err := supervisor.containerRuntime.ObserveProject(ctx, checkoutRoot)
	if err != nil {
		return ServicePortObservation{}, fmt.Errorf("observe host container runtime: %w", err)
	}
	return projectRuntimeServicePorts(observed, serviceID)
}

// serviceObservationCheckout snapshots immutable launch identity without retaining the supervisor lock across runtime I/O.
func (supervisor *Supervisor) serviceObservationCheckout(
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (string, bool) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	projectProcess, projectExists := supervisor.projects[projectID]
	sessionProcess, sessionExists := supervisor.sessions[sessionID]
	if !projectExists || !sessionExists || projectProcess != sessionProcess ||
		!projectProcess.acceptingStop || projectProcess.stopRequested.Load() {
		return "", false
	}
	return projectProcess.command.Dir, true
}

// projectRuntimeServices maps only logical service facts into durable project snapshots and discards container identities.
func projectRuntimeServices(observation containerruntime.ProjectObservation) (ServiceObservation, error) {
	if observation.Services == nil {
		return ServiceObservation{}, fmt.Errorf("host container runtime services must not be nil")
	}
	services := make([]domain.ServiceSnapshot, 0, len(observation.Services))
	identities := make(map[domain.ServiceID]struct{}, len(observation.Services))
	for _, observed := range observation.Services {
		if !observed.Active {
			continue
		}
		service := domain.ServiceSnapshot{
			ID:        domain.ServiceID(observed.ID),
			Name:      observed.Name,
			Kind:      "compose",
			State:     domain.EntityState(observed.State),
			Owner:     domain.ServiceOwnerCompose,
			Selection: domain.ServiceSelected,
			Required:  false,
		}
		if err := service.Validate(); err != nil {
			return ServiceObservation{}, fmt.Errorf("validate host runtime service %q: %w", observed.ID, err)
		}
		if service.State == domain.EntityStopped {
			return ServiceObservation{}, fmt.Errorf("active host runtime service %q cannot be stopped", service.ID)
		}
		if _, exists := identities[service.ID]; exists {
			return ServiceObservation{}, fmt.Errorf("host container runtime returned duplicate service %q", service.ID)
		}
		identities[service.ID] = struct{}{}
		services = append(services, service)
	}
	sort.Slice(services, func(left, right int) bool { return services[left].ID < services[right].ID })
	return ServiceObservation{Supported: true, Services: services}, nil
}

// projectRuntimeServicePorts discards container identity while retaining the exact port mappings it reported.
func projectRuntimeServicePorts(observation containerruntime.ProjectObservation, serviceID domain.ServiceID) (ServicePortObservation, error) {
	if observation.Services == nil {
		return ServicePortObservation{}, fmt.Errorf("host container runtime services must not be nil")
	}
	for _, service := range observation.Services {
		if service.ID != string(serviceID) || !service.Active {
			continue
		}
		ports := make([]ServicePort, 0)
		for _, container := range service.Containers {
			for _, port := range container.Ports {
				ports = append(ports, ServicePort{Address: port.Address, Private: port.Private, Public: port.Public, Protocol: port.Protocol, Replica: container.Replica})
			}
		}
		sort.Slice(ports, func(left, right int) bool {
			if ports[left].Private != ports[right].Private {
				return ports[left].Private < ports[right].Private
			}
			if ports[left].Public != ports[right].Public {
				return ports[left].Public < ports[right].Public
			}
			if ports[left].Protocol != ports[right].Protocol {
				return ports[left].Protocol < ports[right].Protocol
			}
			if ports[left].Address != ports[right].Address {
				return ports[left].Address < ports[right].Address
			}
			return ports[left].Replica < ports[right].Replica
		})
		return ServicePortObservation{Supported: true, Available: true, Ports: ports}, nil
	}
	return ServicePortObservation{Supported: true, Ports: []ServicePort{}}, nil
}
