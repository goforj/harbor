//go:build linux

package ticketissuer

import (
	"context"
	"fmt"
	"strconv"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// nativeConflictObserver binds Linux policy routing to the UID authenticated by the control transport and protected ownership record.
type nativeConflictObserver struct{}

// linuxConflictObserver is the native boundary used after the signed requester identity is parsed.
type linuxConflictObserver func(context.Context, hostconflict.Request, uint32) (hostconflict.Observation, error)

// Observe parses the canonical UID before delegating to the current network-namespace observer.
func (nativeConflictObserver) Observe(ctx context.Context, request hostconflict.Request, requesterIdentity string) (hostconflict.Observation, error) {
	return observeLinuxConflictsWith(ctx, request, requesterIdentity, hostconflict.ObserveLinux)
}

// observeLinuxConflictsWith rejects elevated or ambiguous identities before native policy routing sees them.
func observeLinuxConflictsWith(ctx context.Context, request hostconflict.Request, requesterIdentity string, observe linuxConflictObserver) (hostconflict.Observation, error) {
	requesterUID, err := strconv.ParseUint(requesterIdentity, 10, 32)
	if err != nil || requesterUID == 0 || strconv.FormatUint(requesterUID, 10) != requesterIdentity {
		return hostconflict.Observation{}, fmt.Errorf("observe helper ticket conflicts: requester identity is not a canonical non-root Linux UID")
	}
	return observe(ctx, request, uint32(requesterUID))
}
