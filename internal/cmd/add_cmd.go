package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/goforj/harbor/internal/control"
)

// AddCmd registers one selected GoForj project with the local Harbor daemon.
type AddCmd struct {
	Path string `arg:"" optional:"" default:"." help:"GoForj project directory"`
	JSON bool   `help:"Print the typed machine-readable registration"`

	client *DaemonClient
	output io.Writer
}

// NewAddCmd creates the project registration command.
func NewAddCmd(client *DaemonClient) *AddCmd {
	return &AddCmd{Path: ".", client: client, output: os.Stdout}
}

// Signature defines CLI metadata for project registration.
func (*AddCmd) Signature() string {
	return `name:"add" help:"Register a GoForj project"`
}

// Run canonicalizes client input and asks harbord to own the registration mutation.
func (command *AddCmd) Run(ctx context.Context) error {
	absolute, err := filepath.Abs(command.Path)
	if err != nil {
		return fmt.Errorf("resolve project path: %w", err)
	}
	registration, err := command.client.RegisterProject(ctx, control.RegisterProjectRequest{Path: filepath.Clean(absolute)})
	if err != nil {
		return err
	}
	if command.JSON {
		return writeDaemonJSON(command.output, registration)
	}
	return writeProjectRegistration(command.output, registration)
}

// writeProjectRegistration distinguishes a new registration from the current state returned by a replay.
func writeProjectRegistration(output io.Writer, registration control.ProjectRegistration) error {
	if registration.Created {
		_, err := fmt.Fprintf(
			output,
			"Added: %s\nPath: %s\nState: %s\nRouting: not configured\nRevision: %d\n",
			registration.Project.Name,
			registration.Project.Path,
			registration.Project.State,
			registration.Revision,
		)
		return err
	}
	_, err := fmt.Fprintf(
		output,
		"Already registered: %s\nPath: %s\nState: %s\nRevision: %d\n",
		registration.Project.Name,
		registration.Project.Path,
		registration.Project.State,
		registration.Revision,
	)
	return err
}
