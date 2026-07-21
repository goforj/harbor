package cmd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/goforj/harbor/internal/domain"
)

// urlOpener starts the operating system's default handler for one reviewed URL.
type urlOpener func(context.Context, string) error

// OpenCmd opens one resource selected from the daemon's current authoritative snapshot.
type OpenCmd struct {
	ProjectID  domain.ProjectID  `arg:"" name:"project" help:"Registered Harbor project ID"`
	ResourceID domain.ResourceID `arg:"" optional:"" default:"app-http" name:"resource" help:"Project resource ID (default: app-http)"`

	client *DaemonClient
	open   urlOpener
}

// NewOpenCmd creates the resource-opening command without contacting the daemon.
func NewOpenCmd(client *DaemonClient) *OpenCmd {
	return &OpenCmd{client: client, ResourceID: "app-http", open: openURL}
}

// Signature defines CLI metadata for opening one reviewed project resource.
func (*OpenCmd) Signature() string {
	return `name:"open" help:"Open a project resource in the system browser"`
}

// Run resolves the resource again through Harbor before handing its URL to the operating system.
func (command *OpenCmd) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := command.ProjectID.Validate(); err != nil {
		return fmt.Errorf("open resource: %w", err)
	}
	if err := command.ResourceID.Validate(); err != nil {
		return fmt.Errorf("open resource: %w", err)
	}
	if command.client == nil {
		return errors.New("open resource: daemon client is required")
	}
	if command.open == nil {
		return errors.New("open resource: URL opener is required")
	}
	snapshot, err := command.client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read Harbor snapshot: %w", err)
	}
	project, found := projectFromSnapshot(snapshot, command.ProjectID)
	if !found {
		return fmt.Errorf("open resource: project %q was not found", command.ProjectID)
	}
	resource, found := openResourceFromProject(project, command.ResourceID)
	if !found {
		return fmt.Errorf("open resource: resource %q was not found in project %q", command.ResourceID, command.ProjectID)
	}
	if err := resource.Validate(); err != nil {
		return fmt.Errorf("open resource: %w", err)
	}
	if err := command.open(ctx, resource.URL); err != nil {
		return fmt.Errorf("open resource %q: %w", resource.ID, err)
	}
	return nil
}

// openResourceFromProject selects one project-scoped resource without accepting a global resource identity.
func openResourceFromProject(project domain.ProjectSnapshot, resourceID domain.ResourceID) (domain.ResourceSnapshot, bool) {
	for _, resource := range project.Resources {
		if resource.ID == resourceID {
			return resource, true
		}
	}
	return domain.ResourceSnapshot{}, false
}

// openURL invokes only a fixed platform browser handler and never evaluates a shell command.
func openURL(ctx context.Context, rawURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	commandName, arguments, err := openURLCommand(rawURL)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, commandName, arguments...)
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", commandName, err)
	}
	return nil
}

// openURLCommand maps each supported OS to a fixed executable and argument shape.
func openURLCommand(rawURL string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{rawURL}, nil
	case "linux":
		return "xdg-open", []string{rawURL}, nil
	case "windows":
		return "rundll32.exe", []string{"url.dll,FileProtocolHandler", rawURL}, nil
	default:
		return "", nil, fmt.Errorf("opening resources is unsupported on %s", runtime.GOOS)
	}
}
