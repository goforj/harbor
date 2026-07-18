package domain

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// SnapshotSchemaVersion is the initial platform-neutral Harbor snapshot schema.
const SnapshotSchemaVersion uint16 = 1

// ServiceOwner identifies which system owns a service's lifecycle.
type ServiceOwner string

const (
	// ServiceOwnerCompose means GoForj owns the service through the project's Compose lifecycle.
	ServiceOwnerCompose ServiceOwner = "compose"
	// ServiceOwnerExternal means Harbor observes the service without controlling its lifecycle.
	ServiceOwnerExternal ServiceOwner = "external"
)

// Validate reports whether the service owner is recognized.
func (owner ServiceOwner) Validate() error {
	switch owner {
	case ServiceOwnerCompose, ServiceOwnerExternal:
		return nil
	default:
		return fmt.Errorf("unknown service owner %q", owner)
	}
}

// ServiceSelection distinguishes selected service intent from an available capability.
type ServiceSelection string

const (
	// ServiceSelected means the project selected the service requirement for its current topology.
	ServiceSelected ServiceSelection = "selected"
	// ServiceAvailable means the service is available to the project but was not selected.
	ServiceAvailable ServiceSelection = "available"
)

// Validate reports whether the service selection is recognized.
func (selection ServiceSelection) Validate() error {
	switch selection {
	case ServiceSelected, ServiceAvailable:
		return nil
	default:
		return fmt.Errorf("unknown service selection %q", selection)
	}
}

// ResourceOwnerKind identifies the type of entity that canonically owns a resource.
type ResourceOwnerKind string

const (
	// ResourceOwnedByApp means the resource belongs to one App.
	ResourceOwnedByApp ResourceOwnerKind = "app"
	// ResourceOwnedByService means the resource belongs to one service.
	ResourceOwnedByService ResourceOwnerKind = "service"
)

// Validate reports whether the resource owner kind is recognized.
func (kind ResourceOwnerKind) Validate() error {
	switch kind {
	case ResourceOwnedByApp, ResourceOwnedByService:
		return nil
	default:
		return fmt.Errorf("unknown resource owner kind %q", kind)
	}
}

// ResourceOwner points to exactly one App or service in the containing project.
type ResourceOwner struct {
	Kind      ResourceOwnerKind `json:"kind"`
	AppID     AppID             `json:"app_id,omitempty"`
	ServiceID ServiceID         `json:"service_id,omitempty"`
}

// Validate reports whether the resource owner has exactly one identity matching its kind.
func (owner ResourceOwner) Validate() error {
	if err := owner.Kind.Validate(); err != nil {
		return err
	}
	switch owner.Kind {
	case ResourceOwnedByApp:
		if err := owner.AppID.Validate(); err != nil {
			return err
		}
		if owner.ServiceID != "" {
			return fmt.Errorf("App-owned resource must not contain a service ID")
		}
	case ResourceOwnedByService:
		if err := owner.ServiceID.Validate(); err != nil {
			return err
		}
		if owner.AppID != "" {
			return fmt.Errorf("service-owned resource must not contain an App ID")
		}
	}
	return nil
}

// AppSnapshot is the stable summary of one App in a project snapshot.
type AppSnapshot struct {
	ID       AppID       `json:"id"`
	Name     string      `json:"name"`
	State    EntityState `json:"state"`
	Active   bool        `json:"active"`
	Required bool        `json:"required"`
}

// Validate reports whether the App summary has a stable identity, display name, and state.
func (app AppSnapshot) Validate() error {
	if err := app.ID.Validate(); err != nil {
		return err
	}
	if err := validateDisplayName("App name", app.Name); err != nil {
		return err
	}
	return app.State.Validate()
}

// ServiceSnapshot is the stable summary of one service in a project snapshot.
type ServiceSnapshot struct {
	ID        ServiceID        `json:"id"`
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	State     EntityState      `json:"state"`
	Owner     ServiceOwner     `json:"owner"`
	Selection ServiceSelection `json:"selection"`
	Required  bool             `json:"required"`
}

// Validate reports whether the service summary has valid identity and ownership facts.
func (service ServiceSnapshot) Validate() error {
	if err := service.ID.Validate(); err != nil {
		return err
	}
	if err := validateDisplayName("service name", service.Name); err != nil {
		return err
	}
	if err := validateIdentifier("service kind", service.Kind); err != nil {
		return err
	}
	if err := service.State.Validate(); err != nil {
		return err
	}
	if err := service.Owner.Validate(); err != nil {
		return err
	}
	return service.Selection.Validate()
}

// ResourceSnapshot is one launchable HTTP resource attached to its canonical owner.
type ResourceSnapshot struct {
	ID    ResourceID    `json:"id"`
	Name  string        `json:"name"`
	Kind  string        `json:"kind"`
	Owner ResourceOwner `json:"owner"`
	URL   string        `json:"url"`
}

// Validate reports whether the resource has valid identity, ownership, and an absolute HTTP URL.
func (resource ResourceSnapshot) Validate() error {
	if err := resource.ID.Validate(); err != nil {
		return err
	}
	if err := validateDisplayName("resource name", resource.Name); err != nil {
		return err
	}
	if err := validateIdentifier("resource kind", resource.Kind); err != nil {
		return err
	}
	if err := resource.Owner.Validate(); err != nil {
		return err
	}
	return validateResourceURL(resource.URL)
}

// ProjectSnapshot is the canonical owner of its Apps, services, and resources.
type ProjectSnapshot struct {
	ID        ProjectID          `json:"id"`
	Name      string             `json:"name"`
	Path      string             `json:"path"`
	Slug      string             `json:"slug"`
	State     ProjectState       `json:"state"`
	Favorite  bool               `json:"favorite"`
	UpdatedAt time.Time          `json:"updated_at"`
	Apps      []AppSnapshot      `json:"apps"`
	Services  []ServiceSnapshot  `json:"services"`
	Resources []ResourceSnapshot `json:"resources"`
}

// Validate reports whether the project contains a consistent, non-duplicated ownership tree.
func (project ProjectSnapshot) Validate() error {
	if err := project.ID.Validate(); err != nil {
		return err
	}
	if err := validateDisplayName("project name", project.Name); err != nil {
		return err
	}
	if err := validateProjectPath(project.Path); err != nil {
		return err
	}
	if err := validateIdentifier("project slug", project.Slug); err != nil {
		return err
	}
	if err := project.State.Validate(); err != nil {
		return err
	}
	if err := validateDomainTime("project updated time", project.UpdatedAt); err != nil {
		return err
	}

	apps := make(map[AppID]struct{}, len(project.Apps))
	for _, app := range project.Apps {
		if err := app.Validate(); err != nil {
			return fmt.Errorf("App %q: %w", app.ID, err)
		}
		if _, exists := apps[app.ID]; exists {
			return fmt.Errorf("duplicate App ID %q", app.ID)
		}
		apps[app.ID] = struct{}{}
	}

	services := make(map[ServiceID]struct{}, len(project.Services))
	for _, service := range project.Services {
		if err := service.Validate(); err != nil {
			return fmt.Errorf("service %q: %w", service.ID, err)
		}
		if _, exists := services[service.ID]; exists {
			return fmt.Errorf("duplicate service ID %q", service.ID)
		}
		services[service.ID] = struct{}{}
	}

	resources := make(map[ResourceID]struct{}, len(project.Resources))
	for _, resource := range project.Resources {
		if err := resource.Validate(); err != nil {
			return fmt.Errorf("resource %q: %w", resource.ID, err)
		}
		if _, exists := resources[resource.ID]; exists {
			return fmt.Errorf("duplicate resource ID %q", resource.ID)
		}
		resources[resource.ID] = struct{}{}
		if err := validateResourceOwnerReference(resource.Owner, apps, services); err != nil {
			return fmt.Errorf("resource %q: %w", resource.ID, err)
		}
	}
	return nil
}

// ResourceRef identifies a resource in the project scope where its ID is stable.
type ResourceRef struct {
	ProjectID  ProjectID  `json:"project_id"`
	ResourceID ResourceID `json:"resource_id"`
}

// Validate reports whether both parts of the project-scoped resource reference are valid.
func (reference ResourceRef) Validate() error {
	if err := reference.ProjectID.Validate(); err != nil {
		return err
	}
	return reference.ResourceID.Validate()
}

// Snapshot is a complete authoritative replacement of Harbor's client-visible state.
type Snapshot struct {
	SchemaVersion     uint16            `json:"schema_version"`
	Sequence          Sequence          `json:"sequence"`
	CapturedAt        time.Time         `json:"captured_at"`
	Projects          []ProjectSnapshot `json:"projects"`
	Operations        []Operation       `json:"operations"`
	RecentResourceIDs []ResourceRef     `json:"recent_resource_ids"`
}

// Validate reports whether the complete snapshot is internally consistent.
func (snapshot Snapshot) Validate() error {
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return fmt.Errorf("unsupported snapshot schema version %d", snapshot.SchemaVersion)
	}
	if err := validateDomainTime("snapshot capture time", snapshot.CapturedAt); err != nil {
		return err
	}

	projects := make(map[ProjectID]ProjectSnapshot, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		if err := project.Validate(); err != nil {
			return fmt.Errorf("project %q: %w", project.ID, err)
		}
		if _, exists := projects[project.ID]; exists {
			return fmt.Errorf("duplicate project ID %q", project.ID)
		}
		projects[project.ID] = project
	}

	operations := make(map[OperationID]struct{}, len(snapshot.Operations))
	intents := make(map[IntentID]struct{}, len(snapshot.Operations))
	for _, operation := range snapshot.Operations {
		if err := operation.Validate(); err != nil {
			return fmt.Errorf("operation %q: %w", operation.ID, err)
		}
		if operation.ProjectID != "" {
			if _, exists := projects[operation.ProjectID]; !exists {
				return fmt.Errorf("operation %q references unknown project %q", operation.ID, operation.ProjectID)
			}
		}
		if _, exists := operations[operation.ID]; exists {
			return fmt.Errorf("duplicate operation ID %q", operation.ID)
		}
		operations[operation.ID] = struct{}{}
		if _, exists := intents[operation.IntentID]; exists {
			return fmt.Errorf("duplicate intent ID %q", operation.IntentID)
		}
		intents[operation.IntentID] = struct{}{}
	}

	recentResources := make(map[ResourceRef]struct{}, len(snapshot.RecentResourceIDs))
	for _, reference := range snapshot.RecentResourceIDs {
		if err := reference.Validate(); err != nil {
			return err
		}
		if _, exists := recentResources[reference]; exists {
			return fmt.Errorf("duplicate recent resource reference %q/%q", reference.ProjectID, reference.ResourceID)
		}
		recentResources[reference] = struct{}{}
		project, exists := projects[reference.ProjectID]
		if !exists {
			return fmt.Errorf("recent resource references unknown project %q", reference.ProjectID)
		}
		if !projectHasResource(project, reference.ResourceID) {
			return fmt.Errorf("recent resource references unknown resource %q in project %q", reference.ResourceID, reference.ProjectID)
		}
	}
	return nil
}

// validateDisplayName permits human-readable Unicode while keeping serialized snapshots bounded and unambiguous.
func validateDisplayName(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", kind)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", kind)
	}
	if containsControlCharacter(value) {
		return fmt.Errorf("%s must not contain control characters", kind)
	}
	if len(value) > 512 {
		return fmt.Errorf("%s must not exceed 512 bytes", kind)
	}
	return nil
}

// validateProjectPath avoids imposing one operating system's canonical path syntax on shared domain data.
func validateProjectPath(path string) error {
	if path == "" {
		return fmt.Errorf("project path must not be empty")
	}
	if !utf8.ValidString(path) {
		return fmt.Errorf("project path must be valid UTF-8")
	}
	if strings.TrimSpace(path) != path {
		return fmt.Errorf("project path must not contain surrounding whitespace")
	}
	if containsControlCharacter(path) {
		return fmt.Errorf("project path must not contain control characters")
	}
	return nil
}

// validateResourceURL limits desktop-openable resources to absolute HTTP origins handled by the system browser.
func validateResourceURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("resource URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("resource URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil {
		return fmt.Errorf("resource URL must not contain user information")
	}
	return nil
}

// validateResourceOwnerReference proves that a resource points to an entity in its containing project.
func validateResourceOwnerReference(owner ResourceOwner, apps map[AppID]struct{}, services map[ServiceID]struct{}) error {
	switch owner.Kind {
	case ResourceOwnedByApp:
		if _, exists := apps[owner.AppID]; !exists {
			return fmt.Errorf("owner references unknown App %q", owner.AppID)
		}
	case ResourceOwnedByService:
		if _, exists := services[owner.ServiceID]; !exists {
			return fmt.Errorf("owner references unknown service %q", owner.ServiceID)
		}
	}
	return nil
}

// projectHasResource reports whether a project contains the referenced resource identity.
func projectHasResource(project ProjectSnapshot, resourceID ResourceID) bool {
	for _, resource := range project.Resources {
		if resource.ID == resourceID {
			return true
		}
	}
	return false
}
