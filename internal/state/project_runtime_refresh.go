package state

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// RefreshProjectServicesRequest binds one observed Compose topology to the exact ready process session that owns it.
type RefreshProjectServicesRequest struct {
	ProjectID                 domain.ProjectID
	ExpectedProjectRevision   domain.Sequence
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Services                  []domain.ServiceSnapshot
	At                        time.Time
}

// RefreshProjectServices replaces only the observed Compose service projection while preserving process-owned App facts.
//
// The project revision and session generation fences prevent a late Engine event from publishing topology after a
// stop, restart, or replacement session has taken ownership of the same project.
func (store *Store) RefreshProjectServices(
	ctx context.Context,
	request RefreshProjectServicesRequest,
) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := validateRefreshProjectServicesRequest(request); err != nil {
		return ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}

	request.Services = append([]domain.ServiceSnapshot(nil), request.Services...)
	var result ProjectRecord
	err := store.mutations.mutate(ctx, "refresh project services", func(tx *gorm.DB) error {
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Revision != request.ExpectedProjectRevision {
			return &ProjectRevisionConflictError{
				ProjectID: request.ProjectID,
				Expected:  request.ExpectedProjectRevision,
				Actual:    project.Revision,
			}
		}
		session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if session.Generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, session.Generation)
		}
		if !sessionCanPublishReadiness(session) {
			return fmt.Errorf("session %q must remain process-backed while refreshing services", request.SessionID)
		}
		if project.Project.State != domain.ProjectReady && project.Project.State != domain.ProjectDegraded {
			return fmt.Errorf("project %q cannot refresh services from state %q", request.ProjectID, project.Project.State)
		}
		if request.At.Before(project.Project.UpdatedAt) {
			return fmt.Errorf("service refresh time precedes project projection")
		}
		observedResources := resourcesForObservedServices(project.Project.Resources, request.Services)
		if reflect.DeepEqual(project.Project.Services, request.Services) && reflect.DeepEqual(project.Project.Resources, observedResources) {
			result = project
			return nil
		}

		next := project.Project
		next.UpdatedAt = request.At
		next.Services = append([]domain.ServiceSnapshot(nil), request.Services...)
		next.Resources = observedResources
		persisted, err := persistLifecycleProject(tx, next)
		if err != nil {
			return err
		}
		runtimeState, err := store.runtimeStateCandidate(tx)
		if err != nil {
			return fmt.Errorf("read refreshed runtime candidate: %w", err)
		}
		if err := runtimeState.Validate(); err != nil {
			return fmt.Errorf("validate refreshed runtime candidate: %w", err)
		}
		result = persisted
		return nil
	})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("refresh project %q services: %w", request.ProjectID, err)
	}
	return result, nil
}

// validateRefreshProjectServicesRequest rejects an observation that cannot become a canonical project projection.
func validateRefreshProjectServicesRequest(request RefreshProjectServicesRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected project revision", request.ExpectedProjectRevision, false); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	if request.Services == nil {
		return fmt.Errorf("refreshed project services must not be nil")
	}
	seen := make(map[domain.ServiceID]struct{}, len(request.Services))
	var previous domain.ServiceID
	for index, service := range request.Services {
		if err := service.Validate(); err != nil {
			return fmt.Errorf("refreshed service %q: %w", service.ID, err)
		}
		if service.Kind != "compose" || service.Owner != domain.ServiceOwnerCompose || service.Selection != domain.ServiceSelected || service.Required {
			return fmt.Errorf("refreshed service %q is not a Harbor-observed Compose service", service.ID)
		}
		if service.State == domain.EntityStopped {
			return fmt.Errorf("refreshed active service %q must not be stopped", service.ID)
		}
		if _, exists := seen[service.ID]; exists {
			return fmt.Errorf("duplicate refreshed service ID %q", service.ID)
		}
		if index > 0 && previous > service.ID {
			return fmt.Errorf("refreshed project services must use canonical service ID order")
		}
		seen[service.ID] = struct{}{}
		previous = service.ID
	}
	return validateStoredTime("service refresh time", request.At)
}

// resourcesForObservedServices removes stale service-owned links while retaining App links that remain valid.
func resourcesForObservedServices(resources []domain.ResourceSnapshot, services []domain.ServiceSnapshot) []domain.ResourceSnapshot {
	serviceIDs := make(map[domain.ServiceID]struct{}, len(services))
	for _, service := range services {
		serviceIDs[service.ID] = struct{}{}
	}
	filtered := make([]domain.ResourceSnapshot, 0, len(resources))
	for _, resource := range resources {
		if resource.Owner.Kind == domain.ResourceOwnedByApp {
			filtered = append(filtered, resource)
			continue
		}
		if _, exists := serviceIDs[resource.Owner.ServiceID]; exists {
			filtered = append(filtered, resource)
		}
	}
	sort.Slice(filtered, func(left, right int) bool { return filtered[left].ID < filtered[right].ID })
	return filtered
}
