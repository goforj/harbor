package monitoring

import (
	"context"
	"github.com/goforj/harbor/internal/console"
)

// PollCmd manually enqueues one immediate poll cycle across monitors.
type PollCmd struct {
	MonitorID string `help:"Optional monitor id to poll instead of all enabled monitors"`
	Sync      bool   `help:"Run checks synchronously in-process and wait for completion"`
	JSON      bool   `help:"Print command result as JSON"`
}

// Signature defines CLI metadata for this command.
func (*PollCmd) Signature() string {
	return `name:"monitor:poll" help:"Run or enqueue one polling cycle (supports --monitor, --sync, --json)"`
}

// NewPollCmd creates a new PollCmd.
func NewPollCmd() *PollCmd {
	return &PollCmd{}
}

// Run executes the command.
func (c *PollCmd) Run(ctx context.Context) error {
	console.Warnf("monitor:poll is available only when Demo App, Database, and Jobs components are enabled")
	return nil
}
