package main

import (
	"fmt"
	"path/filepath"

	"github.com/goforj/harbor/desktop/internal/projectreadiness"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectcreation"
)

// ProjectCreationPrerequisites inspects only the selected compiled-in provider's tool requirements.
func (a *App) ProjectCreationPrerequisites(request projectcreation.Request) (projectreadiness.Report, error) {
	ctx, _, err := a.currentConnection()
	if err != nil {
		return projectreadiness.Report{}, err
	}
	adapter, ok := a.projectCreators[request.ProviderID]
	if !ok {
		return projectreadiness.Report{}, fmt.Errorf("unsupported project stack %q", request.ProviderID)
	}
	if len(request.Options) > 64 {
		return projectreadiness.Report{}, fmt.Errorf("too many creation options")
	}
	for key, value := range request.Options {
		if len(key) > 48 || len(value) > 4096 {
			return projectreadiness.Report{}, fmt.Errorf("creation option exceeds the supported size")
		}
	}
	return adapter.checkCreation(ctx, request.Options), nil
}

// ProjectPrerequisites derives checkout scope from a fresh authenticated snapshot instead of accepting a frontend path.
func (a *App) ProjectPrerequisites(projectID string) (projectreadiness.Report, error) {
	if err := domain.ProjectID(projectID).Validate(); err != nil {
		return projectreadiness.Report{}, err
	}
	ctx, client, err := a.currentConnection()
	if err != nil {
		return projectreadiness.Report{}, err
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return projectreadiness.Report{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return projectreadiness.Report{}, fmt.Errorf("validate project snapshot: %w", err)
	}
	for _, project := range snapshot.Projects {
		if string(project.ID) == projectID {
			if !filepath.IsAbs(project.Path) {
				return projectreadiness.Report{}, fmt.Errorf("registered checkout path must be absolute")
			}
			// Current registrations are GoForj-owned; future registration providers select their own adapter here.
			return a.projectCreators[projectcreation.GoForj.ID].checkProject(ctx, project), nil
		}
	}
	return projectreadiness.Report{}, fmt.Errorf("project was not found; refresh Harbor and try again")
}
