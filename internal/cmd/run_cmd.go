package cmd

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/logger"
	"github.com/goforj/harbor/internal/runtime"
)

// RunCmd starts enabled app runtimes together in a single host process.
type RunCmd struct {
	logger *logger.AppLogger
}

// NewRunCmd creates a new run command.
func NewRunCmd(
	logger *logger.AppLogger,
) *RunCmd {
	return &RunCmd{
		logger: logger,
	}
}

// Signature defines CLI metadata for this command.
func (*RunCmd) Signature() string {
	return `name:"run" aliases:"app" help:"Run enabled app runtimes together"`
}

func (c *RunCmd) Run(ctx context.Context) error {
	runtimes := make([]runtime.Runtime, 0, 3)
	if len(runtimes) == 0 {
		return fmt.Errorf("run: no runtimes are enabled for this app")
	}

	return runtime.NewRuntimeHost(runtimes...).Run(ctx)
}
