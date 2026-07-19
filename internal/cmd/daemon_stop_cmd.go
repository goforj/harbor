package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
)

// DaemonStopCmd requests graceful shutdown of the current user's Harbor daemon.
type DaemonStopCmd struct {
	client *DaemonClient
	output io.Writer
}

// NewDaemonStopCmd creates a daemon stop command without contacting the daemon during assembly.
func NewDaemonStopCmd(client *DaemonClient) *DaemonStopCmd {
	return &DaemonStopCmd{client: client, output: os.Stdout}
}

// Signature defines CLI metadata for daemon shutdown.
func (*DaemonStopCmd) Signature() string {
	return `name:"stop" help:"Stop the local Harbor daemon"`
}

// Run reports success only after the daemon has acknowledged graceful shutdown.
func (command *DaemonStopCmd) Run(ctx context.Context) error {
	if err := command.client.Stop(ctx); err != nil {
		return err
	}

	_, err := fmt.Fprintln(
		command.output,
		"Harbor daemon is stopping. Stable Harbor endpoints will be unavailable until it starts again.",
	)
	return err
}
