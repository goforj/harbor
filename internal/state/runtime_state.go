package state

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"gorm.io/gorm"
)

// RuntimeState is the complete client and network input captured from one durable database instant.
type RuntimeState struct {
	Snapshot           domain.Snapshot
	Network            NetworkRecord
	NetworkInitialized bool
}

// Validate rejects runtime inputs whose network lifecycle or project ownership is ambiguous.
func (state RuntimeState) Validate() error {
	if err := state.Snapshot.Validate(); err != nil {
		return fmt.Errorf("runtime snapshot: %w", err)
	}
	if !state.NetworkInitialized {
		if err := validateUninitializedRuntimeNetwork(state.Network); err != nil {
			return err
		}
		return validateUnleasedRuntimeProjects(state.Snapshot.Projects)
	}
	if err := state.Network.Validate(); err != nil {
		return fmt.Errorf("runtime network: %w", err)
	}
	if state.Network.Revision > state.Snapshot.Sequence {
		return fmt.Errorf("runtime network revision %d exceeds snapshot sequence %d", state.Network.Revision, state.Snapshot.Sequence)
	}
	return validateRuntimeNetworkProjects(state.Snapshot.Projects, state.Network)
}

// validateUnleasedRuntimeProjects permits direct loopback development without granting Harbor-owned routing authority.
func validateUnleasedRuntimeProjects(projects []domain.ProjectSnapshot) error {
	for _, project := range projects {
		if err := validateUnleasedRuntimeProject(project); err != nil {
			return fmt.Errorf("runtime project %q is not route-safe without network ownership: %w", project.ID, err)
		}
	}
	return nil
}

// validateUnleasedRuntimeProject separates current-port loopback development from routes that require a Harbor lease.
func validateUnleasedRuntimeProject(project domain.ProjectSnapshot) error {
	switch project.State {
	case domain.ProjectStopped:
		return validateStoppedRuntimeProject(project)
	case domain.ProjectStarting, domain.ProjectFailed, domain.ProjectUnavailable:
		return validateRouteFreeRuntimeProject(project)
	case domain.ProjectReady, domain.ProjectRebuilding, domain.ProjectDegraded, domain.ProjectStopping:
		return validateActiveDirectRuntimeProject(project)
	default:
		return fmt.Errorf("project state %q requires a primary network lease or staged release", project.State)
	}
}

// validateStoppedRuntimeProject defines the fully inactive shape retained after a joined process has stopped.
func validateStoppedRuntimeProject(project domain.ProjectSnapshot) error {
	for _, app := range project.Apps {
		if app.Active {
			return fmt.Errorf("App %q must be inactive", app.ID)
		}
		if app.State != domain.EntityStopped {
			return fmt.Errorf("App %q state %q must be stopped", app.ID, app.State)
		}
	}
	for _, service := range project.Services {
		if service.State != domain.EntityStopped {
			return fmt.Errorf("service %q state %q must be stopped", service.ID, service.State)
		}
	}
	if len(project.Resources) != 0 {
		return fmt.Errorf("project publishes %d resources", len(project.Resources))
	}
	return nil
}

// validateRouteFreeRuntimeProject prevents lifecycle transitions from retaining launchable URLs before or after readiness.
func validateRouteFreeRuntimeProject(project domain.ProjectSnapshot) error {
	if len(project.Resources) != 0 {
		return fmt.Errorf("project publishes %d resources", len(project.Resources))
	}
	for _, app := range project.Apps {
		if app.Active {
			return fmt.Errorf("App %q must be inactive while project state is %q", app.ID, project.State)
		}
		if !routeFreeEntityState(app.State) {
			return fmt.Errorf("App %q state %q is not route-free while project state is %q", app.ID, app.State, project.State)
		}
	}
	for _, service := range project.Services {
		if !routeFreeEntityState(service.State) {
			return fmt.Errorf("service %q state %q is not route-free while project state is %q", service.ID, service.State, project.State)
		}
	}
	return nil
}

// routeFreeEntityState reports terminal entity states that do not imply a reachable runtime.
func routeFreeEntityState(state domain.EntityState) bool {
	switch state {
	case domain.EntityStopped, domain.EntityFailed, domain.EntityUnavailable:
		return true
	default:
		return false
	}
}

// validateActiveDirectRuntimeProject requires coherent process-backed entities before accepting unleased loopback routes.
func validateActiveDirectRuntimeProject(project domain.ProjectSnapshot) error {
	if len(project.Resources) == 0 {
		return fmt.Errorf("project state %q does not publish a direct loopback resource", project.State)
	}
	requiredApp := false
	for _, app := range project.Apps {
		if app.Required {
			requiredApp = true
			if !app.Active {
				return fmt.Errorf("required App %q must be active while project state is %q", app.ID, project.State)
			}
		}
		if app.Active && !activeDirectEntityState(project.State, app.State) {
			return fmt.Errorf("active App %q state %q is inconsistent with project state %q", app.ID, app.State, project.State)
		}
	}
	if !requiredApp {
		return fmt.Errorf("project state %q does not contain a required App", project.State)
	}
	for _, service := range project.Services {
		if service.Required && !activeDirectEntityState(project.State, service.State) {
			return fmt.Errorf("required service %q state %q is inconsistent with project state %q", service.ID, service.State, project.State)
		}
	}
	return validateDirectLoopbackResources(project.Resources)
}

// activeDirectEntityState keeps each active aggregate state aligned with the entity states it may truthfully expose.
func activeDirectEntityState(projectState domain.ProjectState, entityState domain.EntityState) bool {
	switch projectState {
	case domain.ProjectReady, domain.ProjectStopping:
		return entityState == domain.EntityReady
	case domain.ProjectRebuilding:
		return entityState == domain.EntityReady || entityState == domain.EntityWorking || entityState == domain.EntityDegraded
	case domain.ProjectDegraded:
		return entityState == domain.EntityReady || entityState == domain.EntityDegraded
	default:
		return false
	}
}

// validateDirectLoopbackResources confines an unleased runtime to literal local addresses that cannot claim a public route.
func validateDirectLoopbackResources(resources []domain.ResourceSnapshot) error {
	for _, resource := range resources {
		parsed, err := url.Parse(resource.URL)
		if err != nil {
			return fmt.Errorf("resource %q URL: %w", resource.ID, err)
		}
		address, err := netip.ParseAddr(parsed.Hostname())
		if err != nil || !address.Unmap().IsLoopback() {
			return fmt.Errorf("resource %q URL host %q is not a literal loopback address", resource.ID, parsed.Hostname())
		}
	}
	return nil
}

// RuntimeState returns the client projection and complete optional network aggregate from one database instant.
func (store *Store) RuntimeState(ctx context.Context) (RuntimeState, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return RuntimeState{}, err
	}
	builder, err := store.projects.WithContext(ctx).Builder()
	if err != nil {
		return RuntimeState{}, fmt.Errorf("open Harbor runtime state: %w", err)
	}

	var result RuntimeState
	err = builder.Transaction(func(tx *gorm.DB) error {
		snapshot, err := store.readSnapshot(tx)
		if err != nil {
			return err
		}
		network, initialized, err := readRuntimeNetwork(tx, snapshot.Sequence)
		if err != nil {
			return err
		}
		candidate := RuntimeState{
			Snapshot:           snapshot,
			Network:            network,
			NetworkInitialized: initialized,
		}
		if err := candidate.Validate(); err != nil {
			return corruptStateError("runtime state", "aggregate", err)
		}
		result = candidate
		return nil
	})
	if err != nil {
		return RuntimeState{}, fmt.Errorf("read Harbor runtime state: %w", err)
	}
	return result, nil
}

// readRuntimeNetwork distinguishes legacy and first-run databases while requiring a complete migrated schema.
func readRuntimeNetwork(tx *gorm.DB, highWater domain.Sequence) (NetworkRecord, bool, error) {
	present, err := inspectNetworkSchema(tx)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	if !present {
		return uninitializedRuntimeNetwork(), false, nil
	}
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	record, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return NetworkRecord{}, false, err
	}
	if !initialized {
		return uninitializedRuntimeNetwork(), false, nil
	}
	if err := validateVisibleSequence(highWater, record.Revision, "network state", nil); err != nil {
		return NetworkRecord{}, false, err
	}
	if err := validateNetworkSequenceExclusivity(tx, record.Revision); err != nil {
		return NetworkRecord{}, false, err
	}
	return record, true, nil
}

// uninitializedRuntimeNetwork gives consumers stable empty arrays without implying that host setup exists.
func uninitializedRuntimeNetwork() NetworkRecord {
	return NetworkRecord{
		Leases:      []identity.Lease{},
		Quarantines: []identity.Quarantine{},
		Reservations: DataPlaneReservations{
			Endpoints:            []EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
}

// validateUninitializedRuntimeNetwork requires the explicit lifecycle flag to agree with every exposed network fact.
func validateUninitializedRuntimeNetwork(record NetworkRecord) error {
	if record.Leases == nil {
		return fmt.Errorf("uninitialized runtime network leases must be initialized")
	}
	if record.Quarantines == nil {
		return fmt.Errorf("uninitialized runtime network quarantines must be initialized")
	}
	if record.Reservations.Endpoints == nil {
		return fmt.Errorf("uninitialized runtime network endpoints must be initialized")
	}
	if record.Reservations.SuppressedProjectIDs == nil {
		return fmt.Errorf("uninitialized runtime network suppressed projects must be initialized")
	}
	if record.Revision != 0 || !record.CreatedAt.IsZero() || !record.UpdatedAt.IsZero() {
		return fmt.Errorf("uninitialized runtime network must not contain durable root facts")
	}
	if record.Stage != "" {
		return fmt.Errorf("uninitialized runtime network must not contain a lifecycle stage")
	}
	if record.Ownership != (identity.Ownership{}) || record.Pool.Prefix().IsValid() || record.Pool.Capacity() != 0 {
		return fmt.Errorf("uninitialized runtime network must not contain identity ownership or pool facts")
	}
	if record.Reservations.Listeners != (SharedListenerReservations{}) {
		return fmt.Errorf("uninitialized runtime network must not contain listener reservations")
	}
	if len(record.Leases) != 0 || len(record.Quarantines) != 0 || len(record.Reservations.Endpoints) != 0 || len(record.Reservations.SuppressedProjectIDs) != 0 {
		return fmt.Errorf("uninitialized runtime network collections must be empty")
	}
	return nil
}

// validateRuntimeNetworkProjects permits newly registered pending projects while requiring authority for every runtime-bearing project.
func validateRuntimeNetworkProjects(projects []domain.ProjectSnapshot, record NetworkRecord) error {
	known := make(map[domain.ProjectID]struct{}, len(projects))
	for _, project := range projects {
		known[project.ID] = struct{}{}
	}
	primary := make(map[domain.ProjectID]netip.Addr, len(record.Leases))
	for _, lease := range record.Leases {
		if _, exists := known[lease.Key.ProjectID]; !exists {
			return fmt.Errorf("runtime network lease references unknown project %q", lease.Key.ProjectID)
		}
		if lease.Key.Kind() == identity.LeaseKindPrimary {
			primary[lease.Key.ProjectID] = lease.Address
		}
	}
	suppressed := make(map[domain.ProjectID]struct{}, len(record.Reservations.SuppressedProjectIDs))
	for _, projectID := range record.Reservations.SuppressedProjectIDs {
		suppressed[projectID] = struct{}{}
	}
	for _, project := range projects {
		if address, exists := primary[project.ID]; exists {
			if record.Stage != NetworkStageFull {
				if err := validateNonPublishableRuntimeProject(project, address); err != nil {
					return fmt.Errorf("runtime project %q is unsafe for %s-stage networking: %w", project.ID, record.Stage, err)
				}
			}
			continue
		}
		if _, exists := suppressed[project.ID]; exists {
			continue
		}
		if err := validateUnleasedRuntimeProject(project); err != nil {
			return fmt.Errorf(
				"runtime project %q has neither a primary network lease nor a staged release and is not route-safe without network ownership: %w",
				project.ID,
				err,
			)
		}
	}
	return nil
}

// validateNonPublishableRuntimeProject confines a pre-full project to its exact literal loopback address.
func validateNonPublishableRuntimeProject(project domain.ProjectSnapshot, address netip.Addr) error {
	if err := validateUnleasedRuntimeProject(project); err != nil {
		return err
	}
	for _, resource := range project.Resources {
		parsed, err := url.Parse(resource.URL)
		if err != nil {
			return fmt.Errorf("resource %q URL: %w", resource.ID, err)
		}
		resourceAddress, err := netip.ParseAddr(parsed.Hostname())
		if err != nil || resourceAddress.Unmap() != address {
			return fmt.Errorf("resource %q URL host %q is not assigned identity %s", resource.ID, parsed.Hostname(), address)
		}
	}
	return nil
}
