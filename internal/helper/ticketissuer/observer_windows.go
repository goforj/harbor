//go:build windows

package ticketissuer

import (
	"context"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// nativeConflictObserver selects Windows' current-compartment native observer after ownership has authenticated the caller.
type nativeConflictObserver struct{}

// Observe delegates the exact request because IP Helper selection is compartment-scoped rather than SID-scoped.
func (nativeConflictObserver) Observe(ctx context.Context, request hostconflict.Request, _ string) (hostconflict.Observation, error) {
	return hostconflict.ObserveWindows(ctx, request)
}
