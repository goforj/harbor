//go:build windows

package windowsinstallation

import (
	"context"
	"github.com/goforj/harbor/internal/platform/windowsmachine"
)

// WithMachineTransaction excludes helper and maintenance writers while a fixed installed lifecycle operation settles.
func WithMachineTransaction(ctx context.Context, operation func() error) error {
	return windowsmachine.WithTransaction(ctx, operation)
}
