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
