//go:build linux

package loopbackhandler

import (
	"context"
	"fmt"
	"strconv"

	"github.com/goforj/harbor/internal/platform/hostconflict"
)

// linuxHostConflictObserver is the requester-aware native Linux observation boundary.
type linuxHostConflictObserver func(context.Context, hostconflict.Request, uint32) (hostconflict.Observation, error)

// observePlatformPreAssignment observes the signed request in the current Linux network namespace.
func observePlatformPreAssignment(ctx context.Context, request hostconflict.Request, requesterIdentity string) (hostconflict.Observation, error) {
	return observeLinuxPreAssignmentWith(ctx, request, requesterIdentity, hostconflict.ObserveLinux)
}

// observeLinuxPreAssignmentWith binds Linux policy routing to the canonical signed interactive UID.
func observeLinuxPreAssignmentWith(ctx context.Context, request hostconflict.Request, requesterIdentity string, observe linuxHostConflictObserver) (hostconflict.Observation, error) {
	requesterUID, err := strconv.ParseUint(requesterIdentity, 10, 32)
	if err != nil || requesterUID == 0 || strconv.FormatUint(requesterUID, 10) != requesterIdentity {
		return hostconflict.Observation{}, fmt.Errorf("helper requester identity %q is not a canonical non-root Linux UID", requesterIdentity)
	}
	return observe(ctx, request, uint32(requesterUID))
}
