package monitoring

import (
	"context"

	"github.com/goforj/harbor/internal/console"
)

// TestPollLoopCmd repeatedly triggers monitor polling to validate queue drain behavior.
type TestPollLoopCmd struct {
	Count     int    `help:"Number of poll cycles to trigger" default:"10"`
	SleepMs   int    `help:"Pause in milliseconds between cycles" default:"250"`
	MonitorID string `help:"Optional monitor id to target instead of all enabled monitors"`
	Sync      bool   `help:"Run each cycle synchronously in-process"`
	JSON      bool   `help:"Print command result as JSON"`
}

// Signature defines CLI metadata for this command.
func (*TestPollLoopCmd) Signature() string {
	return `name:"test:monitor-poll-loop" help:"Stress polling by repeatedly triggering monitor poll cycles"`
}

// NewTestPollLoopCmd creates a new TestPollLoopCmd.
func NewTestPollLoopCmd() *TestPollLoopCmd {
	return &TestPollLoopCmd{}
}

// Run executes the command.
func (c *TestPollLoopCmd) Run(ctx context.Context) error {
	console.Warnf("test:monitor-poll-loop is available only when Demo App, Database, and Jobs components are enabled")
	return nil
}
