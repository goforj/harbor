package state

import (
	"context"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"gorm.io/gorm"
)

// UnattributedProjectRuntimeInspectionBoundary captures durable retry authority without claiming host-process ownership.
type UnattributedProjectRuntimeInspectionBoundary struct {
	Project                ProjectRecord
	NetworkRevision        domain.Sequence
	NetworkUpdatedAt       time.Time
	PrimaryLease           identity.Lease
	PrimaryLeaseGeneration uint64
}

// Validate reports whether the boundary identifies one route-free retryable project and its current primary lease.
func (boundary UnattributedProjectRuntimeInspectionBoundary) Validate() error {
	if err := boundary.Project.Validate(); err != nil {
		return err
	}
	if !projectCanStart(boundary.Project.Project.State) ||
		!projectMatchesInactiveState(boundary.Project.Project, boundary.Project.Project.State, boundary.Project.Project.UpdatedAt) {
		return fmt.Errorf("unattributed runtime inspection project must be a route-free retryable state")
	}
	if _, err := sequenceToModelInt("unattributed runtime inspection network revision", boundary.NetworkRevision, false); err != nil {
		return err
	}
	if err := validateStoredTime("unattributed runtime inspection network update time", boundary.NetworkUpdatedAt); err != nil {
		return err
	}
	if err := boundary.PrimaryLease.Validate(); err != nil {
		return err
	}
	if boundary.PrimaryLease.Key.ProjectID != boundary.Project.Project.ID || boundary.PrimaryLease.Key.Kind() != identity.LeaseKindPrimary {
		return fmt.Errorf("unattributed runtime inspection lease must belong to project %q", boundary.Project.Project.ID)
	}
	if _, err := unsignedToModelInt("unattributed runtime inspection primary lease generation", boundary.PrimaryLeaseGeneration, false); err != nil {
		return err
	}
	return nil
}

// UnattributedProjectRuntimeInspectionBoundary returns one consistent retry boundary only when no durable session remains.
func (store *Store) UnattributedProjectRuntimeInspectionBoundary(
	ctx context.Context,
	projectID domain.ProjectID,
) (UnattributedProjectRuntimeInspectionBoundary, error) {
	ctx = normalizeContext(ctx)
	if err := projectID.Validate(); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	if err := ctx.Err(); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	builder, err := store.projects.WithContext(ctx).Builder()
	if err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, fmt.Errorf("open unattributed runtime inspection boundary: %w", err)
	}

	var boundary UnattributedProjectRuntimeInspectionBoundary
	err = builder.Transaction(func(tx *gorm.DB) error {
		read, readErr := readUnattributedProjectRuntimeInspectionBoundary(tx, projectID)
		if readErr != nil {
			return readErr
		}
		boundary = read
		return nil
	})
	if err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, fmt.Errorf("read project %q unattributed runtime inspection boundary: %w", projectID, err)
	}
	return boundary, nil
}

// readUnattributedProjectRuntimeInspectionBoundary joins project, session, and network authority in one transaction.
func readUnattributedProjectRuntimeInspectionBoundary(
	tx *gorm.DB,
	projectID domain.ProjectID,
) (UnattributedProjectRuntimeInspectionBoundary, error) {
	if err := requireNoCompetingProjectOperation(tx, projectID, ""); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	highWater, err := readSnapshotSequence(tx)
	if err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	project, err := readLifecycleProject(tx, projectID)
	if err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	if !projectCanStart(project.Project.State) ||
		!projectMatchesInactiveState(project.Project, project.Project.State, project.Project.UpdatedAt) {
		return UnattributedProjectRuntimeInspectionBoundary{}, fmt.Errorf("project %q is not a route-free retryable state", projectID)
	}
	if err := validateVisibleSequence(highWater, project.Revision, "unattributed runtime inspection project", nil); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	if err := validateProjectSequenceOwner(tx, project); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	if err := requireNoActiveProjectSession(tx, projectID); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	network, lease, leaseGeneration, err := readProjectRuntimeNetworkBoundary(tx, projectID, "unattributed runtime inspection")
	if err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	if err := validateVisibleSequence(highWater, network.Revision, "unattributed runtime inspection network", nil); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}
	if err := validateNetworkSequenceExclusivity(tx, network.Revision); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, err
	}

	boundary := UnattributedProjectRuntimeInspectionBoundary{
		Project:                project,
		NetworkRevision:        network.Revision,
		NetworkUpdatedAt:       network.UpdatedAt,
		PrimaryLease:           lease,
		PrimaryLeaseGeneration: leaseGeneration,
	}
	if err := boundary.Validate(); err != nil {
		return UnattributedProjectRuntimeInspectionBoundary{}, corruptStateError("unattributed runtime inspection", string(projectID), err)
	}
	return boundary, nil
}
