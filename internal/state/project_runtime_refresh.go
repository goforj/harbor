package state

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
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

// RefreshProjectRuntimeRequest binds one complete observed runtime topology to the exact ready process session that owns it.
type RefreshProjectRuntimeRequest struct {
	ProjectID                 domain.ProjectID
	ExpectedProjectRevision   domain.Sequence
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	PrimaryAddress            netip.Addr
	Services                  []domain.ServiceSnapshot
	Resources                 []domain.ResourceSnapshot
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

// RefreshProjectRuntime atomically replaces observed services and framework resources behind one lifecycle fence.
//
// The primary address and resource URL checks keep a fresh framework report private even when a host event races
// with process replacement. Endpoint reservations are intentionally reconciled by the caller after this durable
// project mutation succeeds, so a route can never be published from an observation that was not persisted.
func (store *Store) RefreshProjectRuntime(
	ctx context.Context,
	request RefreshProjectRuntimeRequest,
) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := validateRefreshProjectRuntimeRequest(request); err != nil {
		return ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}

	request.Services = append([]domain.ServiceSnapshot(nil), request.Services...)
	request.Resources = append([]domain.ResourceSnapshot(nil), request.Resources...)
	var result ProjectRecord
	err := store.mutations.mutate(ctx, "refresh project runtime", func(tx *gorm.DB) error {
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
			return fmt.Errorf("session %q must remain process-backed while refreshing runtime", request.SessionID)
		}
		if project.Project.State != domain.ProjectReady && project.Project.State != domain.ProjectDegraded {
			return fmt.Errorf("project %q cannot refresh runtime from state %q", request.ProjectID, project.Project.State)
		}
		if request.At.Before(project.Project.UpdatedAt) {
			return fmt.Errorf("runtime refresh time precedes project projection")
		}
		if len(project.Project.Apps) != 1 {
			return fmt.Errorf("project %q must contain exactly one App while refreshing runtime", request.ProjectID)
		}
		runtime := DefaultProjectRuntime{
			App:       project.Project.Apps[0],
			Services:  request.Services,
			Resources: request.Resources,
		}
		if err := runtime.Validate(); err != nil {
			return fmt.Errorf("validate refreshed project runtime: %w", err)
		}
		if err := validateRefreshProjectRuntimeResources(request.PrimaryAddress, request.Resources); err != nil {
			return err
		}
		if reflect.DeepEqual(project.Project.Services, request.Services) && reflect.DeepEqual(project.Project.Resources, request.Resources) {
			result = project
			return nil
		}

		next := project.Project
		next.UpdatedAt = request.At
		next.Services = append([]domain.ServiceSnapshot(nil), request.Services...)
		next.Resources = append([]domain.ResourceSnapshot(nil), request.Resources...)
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
		return ProjectRecord{}, fmt.Errorf("refresh project %q runtime: %w", request.ProjectID, err)
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

// validateRefreshProjectRuntimeRequest rejects a complete runtime observation that cannot be joined to one process session.
func validateRefreshProjectRuntimeRequest(request RefreshProjectRuntimeRequest) error {
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
	if !request.PrimaryAddress.IsValid() || !request.PrimaryAddress.Is4() || !request.PrimaryAddress.IsLoopback() {
		return fmt.Errorf("refreshed runtime primary address must be an IPv4 loopback")
	}
	if request.Services == nil {
		return fmt.Errorf("refreshed runtime services must not be nil")
	}
	if request.Resources == nil {
		return fmt.Errorf("refreshed runtime resources must not be nil")
	}
	if err := validateRefreshProjectServices(request.Services); err != nil {
		return err
	}
	if err := validateRefreshProjectRuntimeResourceShape(request.Resources); err != nil {
		return err
	}
	if err := validateRefreshProjectRuntimeResources(request.PrimaryAddress, request.Resources); err != nil {
		return err
	}
	return validateStoredTime("runtime refresh time", request.At)
}

// validateRefreshProjectServices keeps service admission identical between topology-only and complete runtime refreshes.
func validateRefreshProjectServices(services []domain.ServiceSnapshot) error {
	seen := make(map[domain.ServiceID]struct{}, len(services))
	var previous domain.ServiceID
	for index, service := range services {
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
	return nil
}

// validateRefreshProjectRuntimeResourceShape rejects non-HTTP or ambiguous links before ownership checks run.
func validateRefreshProjectRuntimeResourceShape(resources []domain.ResourceSnapshot) error {
	seen := make(map[domain.ResourceID]struct{}, len(resources))
	var previous domain.ResourceID
	for index, resource := range resources {
		if err := resource.Validate(); err != nil {
			return fmt.Errorf("refreshed resource %q: %w", resource.ID, err)
		}
		if _, exists := seen[resource.ID]; exists {
			return fmt.Errorf("duplicate refreshed resource ID %q", resource.ID)
		}
		if index > 0 && previous > resource.ID {
			return fmt.Errorf("refreshed project resources must use canonical resource ID order")
		}
		parsed, err := url.Parse(resource.URL)
		if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			return fmt.Errorf("refreshed resource %q must be a query-free HTTP URL", resource.ID)
		}
		seen[resource.ID] = struct{}{}
		previous = resource.ID
	}
	return nil
}

// validateRefreshProjectRuntimeResources confines every fresh framework URL to the exact assigned loopback identity.
func validateRefreshProjectRuntimeResources(primary netip.Addr, resources []domain.ResourceSnapshot) error {
	assigned := primary.Unmap()
	appHTTP := false
	for _, resource := range resources {
		parsed, err := url.Parse(resource.URL)
		if err != nil {
			return fmt.Errorf("refreshed resource %q URL: %w", resource.ID, err)
		}
		addressPort, err := netip.ParseAddrPort(parsed.Host)
		if err != nil {
			return fmt.Errorf("refreshed resource %q URL host %q must be a literal address with an explicit port", resource.ID, parsed.Host)
		}
		addressPort = netip.AddrPortFrom(addressPort.Addr().Unmap(), addressPort.Port())
		if !addressPort.Addr().Is4() || !addressPort.Addr().IsLoopback() || addressPort.Addr() != assigned || addressPort.Port() == 0 || parsed.Host != addressPort.String() {
			return fmt.Errorf("refreshed resource %q URL must use assigned IPv4 loopback address %s", resource.ID, primary)
		}
		if resource.ID == "app-http" {
			appHTTP = true
			if resource.Owner.Kind != domain.ResourceOwnedByApp || resource.Kind != "application" {
				return fmt.Errorf("refreshed app-http resource must be an App-owned application")
			}
		}
	}
	if !appHTTP {
		return fmt.Errorf("refreshed runtime must contain the required app-http resource")
	}
	return nil
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
