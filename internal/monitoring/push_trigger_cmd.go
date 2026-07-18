package monitoring

import (
	"context"
	"github.com/goforj/harbor/internal/console"
)

// PushTriggerCmd writes a synthetic heartbeat for the push monitor.
type PushTriggerCmd struct {
}

// Signature defines CLI metadata for this command.
func (*PushTriggerCmd) Signature() string {
	return `name:"monitor:push-test-trigger" help:"Emit a synthetic heartbeat for the push monitor"`
}

// NewPushTriggerCmd creates a new PushTriggerCmd.
func NewPushTriggerCmd() *PushTriggerCmd {
	return &PushTriggerCmd{}
}

// Run executes the command.
func (c *PushTriggerCmd) Run(ctx context.Context) error {
	console.Warnf("monitor:push-test-trigger is available only when App, Database, and Jobs components are enabled")
	return nil
}
