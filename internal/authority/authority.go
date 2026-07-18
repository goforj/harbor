package authority

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/state"
)

// controlState limits daemon authority to the complete durable reads needed by control clients.
type controlState interface {
	// CurrentSequence establishes the diagnostic revision without loading the larger replacement snapshot.
	CurrentSequence(context.Context) (domain.Sequence, error)
	// Snapshot supplies one transactionally consistent replacement for every client projection.
	Snapshot(context.Context) (domain.Snapshot, error)
}

// Authority projects the daemon's durable state through the bounded control protocol.
type Authority struct {
	store controlState
	build buildinfo.Info
}

var _ control.Authority = (*Authority)(nil)

// NewAuthority creates the production control authority for the daemon's shared state store.
func NewAuthority(store *state.Store) *Authority {
	return newAuthority(store, buildinfo.Current())
}

// newAuthority keeps process build metadata deterministic without broadening production injection.
func newAuthority(store controlState, build buildinfo.Info) *Authority {
	return &Authority{store: store, build: build}
}

// Status joins session negotiation with one durable sequence so diagnostics identify the exact authority serving the caller.
func (authority *Authority) Status(ctx context.Context, caller control.Caller) (control.DaemonStatus, error) {
	ctx = normalizeContext(ctx)
	capabilities, err := rpc.CanonicalCapabilities(caller.Session.Capabilities)
	if err != nil {
		return control.DaemonStatus{}, fmt.Errorf("canonicalize negotiated capabilities: %w", err)
	}
	sequence, err := authority.store.CurrentSequence(ctx)
	if err != nil {
		return control.DaemonStatus{}, err
	}

	return control.DaemonStatus{
		State: control.DaemonStateReady,
		Build: control.Build{
			Version:  authority.build.Version,
			Revision: authority.build.Revision,
			Modified: authority.build.Modified,
		},
		Protocol:              caller.Session.Protocol,
		Capabilities:          capabilities,
		SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:              sequence,
	}, nil
}

// Snapshot delegates the complete durable replacement so the control layer cannot drift from the Store's transaction boundary.
func (authority *Authority) Snapshot(ctx context.Context, _ control.Caller) (domain.Snapshot, error) {
	return authority.store.Snapshot(normalizeContext(ctx))
}

// normalizeContext keeps nil control calls usable while preserving explicit cancellation.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}
