//go:build windows

package loopbackhandler

import (
	"context"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// windowsHostConflictObserver is the current-compartment native Windows observation boundary.
type windowsHostConflictObserver func(context.Context, hostconflict.Request) (hostconflict.Observation, error)

// observePlatformPreAssignment observes the signed request in the current Windows network compartment.
func observePlatformPreAssignment(ctx context.Context, request hostconflict.Request, _ string) (hostconflict.Observation, error) {
	return observeWindowsPreAssignmentWith(ctx, request, hostconflict.ObserveWindows)
}

// observeWindowsPreAssignmentWith keeps the native observer injectable without expanding production authority.
func observeWindowsPreAssignmentWith(ctx context.Context, request hostconflict.Request, observe windowsHostConflictObserver) (hostconflict.Observation, error) {
	return observe(ctx, request)
}
