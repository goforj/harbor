//go:build darwin

package loopbackhandler

import (
	"context"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// darwinHostConflictObserver is the process-global native macOS observation boundary.
type darwinHostConflictObserver func(context.Context, hostconflict.Request) (hostconflict.Observation, error)

// observePlatformPreAssignment observes the signed request in the process-global macOS network stack.
func observePlatformPreAssignment(ctx context.Context, request hostconflict.Request, _ string) (hostconflict.Observation, error) {
	return observeDarwinPreAssignmentWith(ctx, request, hostconflict.ObserveDarwin)
}

// observeDarwinPreAssignmentWith keeps the native observer injectable without expanding production authority.
func observeDarwinPreAssignmentWith(ctx context.Context, request hostconflict.Request, observe darwinHostConflictObserver) (hostconflict.Observation, error) {
	return observe(ctx, request)
}
