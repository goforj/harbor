//go:build !windows

package main

import "context"

// withPlatformMachineTransaction preserves other platforms' existing reviewed helper authority lifecycle.
func withPlatformMachineTransaction(_ context.Context, operation func() error) error {
	return operation()
}
