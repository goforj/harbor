package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectruntime"
)

// ProjectEnvironmentRequest selects one registered project's provider-owned environment surface.
type ProjectEnvironmentRequest struct {
	ProjectID domain.ProjectID
}

// ProjectEnvironmentFileSaveRequest selects one provider-recognized file through its displayed revision.
type ProjectEnvironmentFileSaveRequest struct {
	ProjectID domain.ProjectID
	Name      string
	Contents  string
	Revision  string
}

// ProjectEnvironment inspects a registered project without allocating network authority or launching it.
func (coordinator *ProjectLifecycleCoordinator) ProjectEnvironment(
	ctx context.Context,
	request ProjectEnvironmentRequest,
) (projectruntime.EnvironmentInspection, error) {
	if err := request.ProjectID.Validate(); err != nil {
		return projectruntime.EnvironmentInspection{}, err
	}
	ctx = normalizeLifecycleContext(ctx)
	project, err := coordinator.state.Project(ctx, request.ProjectID)
	if err != nil {
		return projectruntime.EnvironmentInspection{}, err
	}
	manager, ok := coordinator.projectRuntimeCapabilities().(projectruntime.EnvironmentManager)
	if !ok {
		return projectruntime.EnvironmentInspection{}, errors.New("project runtime environment management is unavailable")
	}
	address, err := coordinator.projectEnvironmentAddress(ctx, request.ProjectID)
	if err != nil {
		return projectruntime.EnvironmentInspection{}, err
	}
	return manager.InspectEnvironment(ctx, projectruntime.EnvironmentInspectionRequest{
		CheckoutRoot: project.Project.Path,
		Address:      address,
	})
}

// SaveProjectEnvironmentFile writes one provider-owned environment file after resolving the checkout from durable state.
func (coordinator *ProjectLifecycleCoordinator) SaveProjectEnvironmentFile(
	ctx context.Context,
	request ProjectEnvironmentFileSaveRequest,
) (projectruntime.EnvironmentFile, error) {
	if err := request.ProjectID.Validate(); err != nil {
		return projectruntime.EnvironmentFile{}, err
	}
	ctx = normalizeLifecycleContext(ctx)
	project, err := coordinator.state.Project(ctx, request.ProjectID)
	if err != nil {
		return projectruntime.EnvironmentFile{}, err
	}
	manager, ok := coordinator.projectRuntimeCapabilities().(projectruntime.EnvironmentManager)
	if !ok {
		return projectruntime.EnvironmentFile{}, errors.New("project runtime environment management is unavailable")
	}
	return manager.SaveEnvironmentFile(ctx, projectruntime.EnvironmentFileSaveRequest{
		CheckoutRoot: project.Project.Path,
		Name:         request.Name,
		Contents:     request.Contents,
		Revision:     request.Revision,
	})
}

// projectEnvironmentAddress returns retained assignment authority without creating a lease merely for inspection.
func (coordinator *ProjectLifecycleCoordinator) projectEnvironmentAddress(
	ctx context.Context,
	projectID domain.ProjectID,
) (netip.Addr, error) {
	network, initialized, err := coordinator.primaryLeases.state.Network(ctx)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read project network environment: %w", err)
	}
	if !initialized {
		return netip.Addr{}, nil
	}
	lease, found := primaryLeaseForKey(network.Leases, identity.LeaseKey{ProjectID: projectID})
	if !found {
		return netip.Addr{}, nil
	}
	return lease.Address, nil
}
