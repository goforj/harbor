package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goforj/harbor/internal/domain"
)

// ProjectStatusCmd prints one project from the daemon's authoritative snapshot.
type ProjectStatusCmd struct {
	ProjectID domain.ProjectID `arg:"" name:"project" help:"Registered Harbor project ID"`
	JSON      bool             `help:"Print the typed machine-readable project snapshot"`

	client *DaemonClient
	output io.Writer
}

// NewProjectStatusCmd creates a project status command without contacting the daemon.
func NewProjectStatusCmd(client *DaemonClient) *ProjectStatusCmd {
	return &ProjectStatusCmd{client: client, output: os.Stdout}
}

// Signature defines CLI metadata for project status.
func (*ProjectStatusCmd) Signature() string {
	return `name:"status" help:"Show a registered Harbor project"`
}

// Run reads one snapshot and selects exactly the requested project.
func (command *ProjectStatusCmd) Run(ctx context.Context) error {
	if err := command.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project status: %w", err)
	}
	snapshot, err := command.client.Snapshot(ctx)
	if err != nil {
		return err
	}
	project, found := projectFromSnapshot(snapshot, command.ProjectID)
	if !found {
		return fmt.Errorf("project status: project %q was not found", command.ProjectID)
	}
	if command.JSON {
		return writeDaemonJSON(command.output, project)
	}
	return writeProjectStatus(command.output, project)
}

// projectFromSnapshot selects one stable project identity from a validated snapshot.
func projectFromSnapshot(snapshot domain.Snapshot, projectID domain.ProjectID) (domain.ProjectSnapshot, bool) {
	for _, project := range snapshot.Projects {
		if project.ID == projectID {
			return project, true
		}
	}
	return domain.ProjectSnapshot{}, false
}

// writeProjectStatus presents the durable project state without inferring unreported runtime health.
func writeProjectStatus(output io.Writer, project domain.ProjectSnapshot) error {
	if _, err := fmt.Fprintf(output, "Project: %s\nPath: %s\nState: %s\nApps: %d\nServices: %d\nResources: %d\n", project.Name, project.Path, project.State, len(project.Apps), len(project.Services), len(project.Resources)); err != nil {
		return err
	}
	if len(project.Resources) == 0 {
		return nil
	}
	resources := make([]string, 0, len(project.Resources))
	for _, resource := range project.Resources {
		resources = append(resources, resource.Name+": "+resource.URL)
	}
	_, err := fmt.Fprintf(output, "Links: %s\n", strings.Join(resources, ", "))
	return err
}
