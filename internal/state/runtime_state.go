package state

import (
	"context"
	"fmt"

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
		return validateUninitializedRuntimeNetwork(state.Network)
	}
	if err := state.Network.Validate(); err != nil {
		return fmt.Errorf("runtime network: %w", err)
	}
	if state.Network.Revision > state.Snapshot.Sequence {
		return fmt.Errorf("runtime network revision %d exceeds snapshot sequence %d", state.Network.Revision, state.Snapshot.Sequence)
	}
	return validateRuntimeNetworkProjects(state.Snapshot.Projects, state.Network)
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

// validateRuntimeNetworkProjects proves every current project has durable routing or teardown state.
func validateRuntimeNetworkProjects(projects []domain.ProjectSnapshot, record NetworkRecord) error {
	known := make(map[domain.ProjectID]struct{}, len(projects))
	for _, project := range projects {
		known[project.ID] = struct{}{}
	}
	primary := make(map[domain.ProjectID]struct{}, len(record.Leases))
	for _, lease := range record.Leases {
		if _, exists := known[lease.Key.ProjectID]; !exists {
			return fmt.Errorf("runtime network lease references unknown project %q", lease.Key.ProjectID)
		}
		if lease.Key.Kind() == identity.LeaseKindPrimary {
			primary[lease.Key.ProjectID] = struct{}{}
		}
	}
	suppressed := make(map[domain.ProjectID]struct{}, len(record.Reservations.SuppressedProjectIDs))
	for _, projectID := range record.Reservations.SuppressedProjectIDs {
		suppressed[projectID] = struct{}{}
	}
	for _, project := range projects {
		if _, exists := primary[project.ID]; exists {
			continue
		}
		if _, exists := suppressed[project.ID]; !exists {
			return fmt.Errorf("runtime project %q has neither a primary network lease nor a staged release", project.ID)
		}
	}
	return nil
}
