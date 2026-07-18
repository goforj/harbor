package monitoring

import (
	"context"

	"github.com/goforj/harbor/internal/console"
)

// RetentionCmd runs retention downsampling + truncation for monitoring data.
type RetentionCmd struct {
	DryRun bool   `help:"Preview work without writing/deleting data"`
	Now    string `help:"Optional RFC3339 time override for deterministic runs"`
	JSON   bool   `help:"Print command result as JSON"`
}

// Signature defines CLI metadata for this command.
func (*RetentionCmd) Signature() string {
	return `name:"monitor:retention" help:"Run monitoring retention downsample + truncation"`
}

// NewRetentionCmd creates a new RetentionCmd.
func NewRetentionCmd() *RetentionCmd {
	return &RetentionCmd{}
}

// Run executes the command.
func (c *RetentionCmd) Run(ctx context.Context) error {
	console.Warnf("monitor:retention is available only when Demo App and Database components are enabled")
	return nil
}
