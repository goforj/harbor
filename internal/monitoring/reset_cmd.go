package monitoring

import (
	"github.com/goforj/harbor/internal/console"
)

// ResetCmd clears monitoring tables.
type ResetCmd struct {
	Confirm bool `help:"Confirm destructive reset"`
}

// Signature defines CLI metadata for this command.
func (*ResetCmd) Signature() string {
	return `name:"monitor:reset" help:"Delete all monitor, check, and incident records"`
}

// NewResetCmd creates a new ResetCmd.
func NewResetCmd() *ResetCmd {
	return &ResetCmd{}
}

// Run executes the command.
func (c *ResetCmd) Run() error {
	console.Warnf("monitor:reset is available only when Demo App and Database components are enabled")
	return nil
}
