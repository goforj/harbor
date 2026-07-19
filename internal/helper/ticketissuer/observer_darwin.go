//go:build darwin

package ticketissuer

import (
	"context"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// nativeConflictObserver selects macOS's process-global native observer after ownership has authenticated the caller.
type nativeConflictObserver struct{}

// Observe delegates the exact request because macOS route selection is process-global rather than UID-scoped.
func (nativeConflictObserver) Observe(ctx context.Context, request hostconflict.Request, _ string) (hostconflict.Observation, error) {
	return hostconflict.ObserveDarwin(ctx, request)
}
