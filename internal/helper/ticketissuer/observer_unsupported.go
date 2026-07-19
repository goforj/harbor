//go:build !darwin && !linux && !windows

package ticketissuer

import (
	"context"
	"fmt"
	"runtime"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// nativeConflictObserver fails closed on operating systems without a reviewed Harbor network profile.
type nativeConflictObserver struct{}

// Observe rejects unsupported hosts before any ticket can authorize a mutation.
func (nativeConflictObserver) Observe(context.Context, hostconflict.Request, string) (hostconflict.Observation, error) {
	return hostconflict.Observation{}, fmt.Errorf("helper ticket conflict observation is unsupported on %s", runtime.GOOS)
}
