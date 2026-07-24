package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectenvironment"
	"github.com/goforj/harbor/internal/projectruntime"
)

// ProjectEnvironmentRequest selects one registered project's provider-owned environment surface.
type ProjectEnvironmentRequest struct {
	ProjectID domain.ProjectID
}

// ProjectEnvironmentFileSaveRequest selects one repository or provider environment file through its displayed revision.
type ProjectEnvironmentFileSaveRequest struct {
	ProjectID domain.ProjectID
	Name      string
	Contents  string
	Revision  string
}

// ProjectEnvironment combines repository bindings with the runtime provider's effective inputs.
type ProjectEnvironment struct {
	Runtime          projectruntime.EnvironmentInspection
	Bindings         []projectenvironment.Binding
	BindingsRevision string
}

// ProjectEnvironment inspects a registered project without allocating network authority or launching it.
func (coordinator *ProjectLifecycleCoordinator) ProjectEnvironment(
	ctx context.Context,
	request ProjectEnvironmentRequest,
) (ProjectEnvironment, error) {
	if err := request.ProjectID.Validate(); err != nil {
		return ProjectEnvironment{}, err
	}
	ctx = normalizeLifecycleContext(ctx)
	project, err := coordinator.state.Project(ctx, request.ProjectID)
	if err != nil {
		return ProjectEnvironment{}, err
	}
	configuration, err := projectenvironment.Inspect(project.Project.Path)
	if err != nil {
		return ProjectEnvironment{}, fmt.Errorf("inspect repository environment bindings: %w", err)
	}
	manager, ok := coordinator.projectRuntimeCapabilities().(projectruntime.EnvironmentManager)
	if !ok {
		return ProjectEnvironment{}, errors.New("project runtime environment management is unavailable")
	}
	address, err := coordinator.projectEnvironmentAddress(ctx, request.ProjectID)
	if err != nil {
		return ProjectEnvironment{}, err
	}
	overrides, err := resolveProjectEnvironmentOverrides(project.Project.Path, address)
	if err != nil {
		return ProjectEnvironment{}, err
	}
	inspection, err := manager.InspectEnvironment(ctx, projectruntime.EnvironmentInspectionRequest{
		CheckoutRoot:         project.Project.Path,
		Address:              address,
		EnvironmentOverrides: overrides,
	})
	if err != nil {
		return ProjectEnvironment{}, err
	}
	return ProjectEnvironment{
		Runtime:          inspection,
		Bindings:         configuration.Bindings,
		BindingsRevision: configuration.Revision,
	}, nil
}

// SaveProjectEnvironmentFile writes one repository or provider environment file after resolving the checkout from durable state.
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
	if request.Name == projectenvironment.Filename {
		configuration, err := projectenvironment.Save(project.Project.Path, request.Contents, request.Revision)
		if err != nil {
			return projectruntime.EnvironmentFile{}, err
		}
		return projectruntime.EnvironmentFile{
			Name:     projectenvironment.Filename,
			Contents: request.Contents,
			Revision: configuration.Revision,
		}, nil
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

// resolveProjectEnvironmentOverrides translates the repository contract into the neutral runtime boundary.
func resolveProjectEnvironmentOverrides(
	checkoutRoot string,
	address netip.Addr,
) ([]projectruntime.EnvironmentVariable, error) {
	if !address.IsValid() {
		return []projectruntime.EnvironmentVariable{}, nil
	}
	resolved, err := projectenvironment.Resolve(checkoutRoot, projectenvironment.Facts{ProjectAddress: address})
	if err != nil {
		return nil, fmt.Errorf("resolve repository environment bindings: %w", err)
	}
	overrides := make([]projectruntime.EnvironmentVariable, 0, len(resolved))
	for _, override := range resolved {
		overrides = append(overrides, projectruntime.EnvironmentVariable{
			Name:   override.Name,
			Value:  override.Value,
			Source: override.Source,
		})
	}
	return overrides, nil
}
