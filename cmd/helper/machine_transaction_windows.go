//go:build windows

package main

import (
	"context"

	"github.com/goforj/harbor/internal/platform/windowsmachine"
)

// withPlatformMachineTransaction excludes helper storage and host mutations for the full protected uninstall transaction.
func withPlatformMachineTransaction(ctx context.Context, operation func() error) error {
	return windowsmachine.WithTransaction(ctx, operation)
}
