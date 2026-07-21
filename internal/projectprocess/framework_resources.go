package projectprocess

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/frameworkresources"
)

// FrameworkResource is one launchable GoForj resource with an explicit App or service owner.
type FrameworkResource struct {
	ID      string
	Name    string
	Kind    string
	URL     string
	App     string
	Service string
	Runtime string
}

// FrameworkResourceObservation is optional framework-owned resource metadata for one supervised project.
type FrameworkResourceObservation struct {
	Supported bool
	Resources []FrameworkResource
}

// ObserveFrameworkResources asks the exact running GoForj executable for its host-resolved resource catalog.
func (supervisor *Supervisor) ObserveFrameworkResources(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (FrameworkResourceObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return FrameworkResourceObservation{}, err
	}
	if err := projectID.Validate(); err != nil {
		return FrameworkResourceObservation{}, fmt.Errorf("observe project framework resources: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return FrameworkResourceObservation{}, fmt.Errorf("observe project framework resources: %w", err)
	}
	query, found := supervisor.frameworkResourceQuery(projectID, sessionID)
	if !found {
		return FrameworkResourceObservation{}, ErrNotRunning
	}
	observation, err := frameworkresources.Observe(ctx, query)
	if err != nil {
		return FrameworkResourceObservation{}, fmt.Errorf("observe GoForj framework resources: %w", err)
	}
	resources := make([]FrameworkResource, 0, len(observation.Resources))
	for _, resource := range observation.Resources {
		resources = append(resources, FrameworkResource{
			ID:      resource.ID,
			Name:    resource.Name,
			Kind:    resource.Kind,
			URL:     resource.URL,
			App:     resource.App,
			Service: resource.Service,
			Runtime: resource.Runtime,
		})
	}
	return FrameworkResourceObservation{Supported: observation.Supported, Resources: resources}, nil
}

// frameworkResourceQuery snapshots the accepted command boundary without holding the supervisor lock during subprocess I/O.
func (supervisor *Supervisor) frameworkResourceQuery(
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (frameworkresources.Query, bool) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	projectProcess, projectExists := supervisor.projects[projectID]
	sessionProcess, sessionExists := supervisor.sessions[sessionID]
	if !projectExists || !sessionExists || projectProcess != sessionProcess ||
		!projectProcess.acceptingStop || projectProcess.stopRequested.Load() {
		return frameworkresources.Query{}, false
	}
	return frameworkresources.Query{
		Executable:  projectProcess.command.Path,
		Checkout:    projectProcess.command.Dir,
		Environment: append([]string(nil), projectProcess.command.Env...),
	}, true
}
