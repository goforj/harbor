package control

import (
	"context"
	"errors"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// Caller carries both identities established before a product method reaches daemon authority.
type Caller struct {
	// Transport is the operating-system identity authenticated by the local socket or pipe.
	Transport local.PeerIdentity
	// Session is the role and feature identity established by protocol negotiation.
	Session session.Peer
}

// Authority owns the daemon-side implementation of bounded control methods.
type Authority interface {
	// Status returns the ready daemon's standalone product diagnostic.
	Status(context.Context, Caller) (DaemonStatus, error)
	// Snapshot returns a complete authoritative replacement of client-visible state.
	Snapshot(context.Context, Caller) (domain.Snapshot, error)
}

// normalizeContext lets public control calls accept a nil context without weakening dependency wiring.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

// authorityError preserves cancellation while classifying every other authority failure as daemon-internal.
func authorityError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return session.NewHandlerError(rpc.ErrorCodeInternal, err)
}
