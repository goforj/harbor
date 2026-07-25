//go:build windows

package wire

import (
	"context"
	"errors"

	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/platform/lowport"
)

var errWindowsDirectLowPortUnexpected = errors.New("Windows direct listeners must not invoke a privileged low-port adapter")

// windowsDirectLowPortBoundary supplies nonnil fail-closed dependencies for a policy that proves no low-port host mutation is required.
type windowsDirectLowPortBoundary struct{}

// Observe fails closed if direct-listener coordination drifts into native low-port observation.
func (windowsDirectLowPortBoundary) Observe(context.Context, lowport.Request) (lowport.Observation, error) {
	return lowport.Observation{}, errWindowsDirectLowPortUnexpected
}

// Issue fails closed if direct-listener coordination attempts to publish a low-port helper capability.
func (windowsDirectLowPortBoundary) Issue(context.Context, string, ticketissuer.LowPortRequest) (ticketissuer.LowPortResult, error) {
	return ticketissuer.LowPortResult{}, errWindowsDirectLowPortUnexpected
}

// Close has no resource to release because direct listeners never open a low-port service.
func (windowsDirectLowPortBoundary) Close() error {
	return nil
}
