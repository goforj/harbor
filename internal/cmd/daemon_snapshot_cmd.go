package cmd

import (
	"context"
	"io"
	"os"
)

// DaemonSnapshotCmd prints the daemon's complete authoritative state as JSON.
type DaemonSnapshotCmd struct {
	client *DaemonClient
	output io.Writer
}

// NewDaemonSnapshotCmd creates a daemon snapshot command.
func NewDaemonSnapshotCmd(client *DaemonClient) *DaemonSnapshotCmd {
	return &DaemonSnapshotCmd{client: client, output: os.Stdout}
}

// Signature defines CLI metadata for the daemon snapshot command.
func (*DaemonSnapshotCmd) Signature() string {
	return `name:"snapshot" help:"Print the authoritative Harbor daemon snapshot"`
}

// Run fetches and prints the current daemon snapshot as its canonical JSON object.
func (command *DaemonSnapshotCmd) Run(ctx context.Context) error {
	snapshot, err := command.client.Snapshot(ctx)
	if err != nil {
		return err
	}

	return writeDaemonJSON(command.output, snapshot)
}
