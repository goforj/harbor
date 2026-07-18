package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goforj/harbor/internal/control"
)

// DaemonStatusCmd prints the ready daemon's negotiated status.
type DaemonStatusCmd struct {
	JSON bool `help:"Print the typed machine-readable status"`

	client *DaemonClient
	output io.Writer
}

// NewDaemonStatusCmd creates a daemon status command.
func NewDaemonStatusCmd(client *DaemonClient) *DaemonStatusCmd {
	return &DaemonStatusCmd{client: client, output: os.Stdout}
}

// Signature defines CLI metadata for the daemon status command.
func (*DaemonStatusCmd) Signature() string {
	return `name:"status" help:"Show Harbor daemon status"`
}

// Run fetches and prints the current daemon status.
func (command *DaemonStatusCmd) Run(ctx context.Context) error {
	status, err := command.client.Status(ctx)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeDaemonJSON(command.output, status)
	}

	return writeDaemonStatus(command.output, status)
}

// writeDaemonStatus keeps the default diagnostic stable and easy to scan in a terminal.
func writeDaemonStatus(output io.Writer, status control.DaemonStatus) error {
	revision := status.Build.Revision
	if revision == "" {
		revision = "unavailable"
	}
	modified := "no"
	if status.Build.Modified {
		modified = "yes"
	}
	capabilities := make([]string, len(status.Capabilities))
	for index, capability := range status.Capabilities {
		capabilities[index] = string(capability)
	}

	_, err := fmt.Fprintf(
		output,
		"State: %s\nVersion: %s\nRevision: %s\nModified: %s\nProtocol: %s\nSnapshot schema: %d\nSequence: %d\nCapabilities: %s\n",
		status.State,
		status.Build.Version,
		revision,
		modified,
		status.Protocol,
		status.SnapshotSchemaVersion,
		status.Sequence,
		strings.Join(capabilities, ", "),
	)
	return err
}

// writeDaemonJSON serializes typed control values without introducing a CLI wrapper shape.
func writeDaemonJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
