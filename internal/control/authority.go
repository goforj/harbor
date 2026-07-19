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
	// RegisterProject discovers and durably registers one canonical GoForj checkout.
	RegisterProject(context.Context, Caller, RegisterProjectRequest) (ProjectRegistration, error)
	// UnregisterProject starts or resumes one idempotent project removal intent.
	UnregisterProject(context.Context, Caller, UnregisterProjectRequest) (ProjectUnregistration, error)
	// PrepareProjectUnregisterApproval returns release progress and at most one caller-bound helper capability.
	PrepareProjectUnregisterApproval(context.Context, Caller, PrepareProjectUnregisterApprovalRequest) (ProjectUnregisterApprovalPreparation, error)
	// ConfirmProjectUnregisterApproval verifies host release before completing the durable unregister operation.
	ConfirmProjectUnregisterApproval(context.Context, Caller, ConfirmProjectUnregisterApprovalRequest) (ProjectUnregisterApprovalConfirmation, error)
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
	var classified *session.HandlerError
	if errors.As(err, &classified) {
		return classified
	}

	return session.NewHandlerError(rpc.ErrorCodeInternal, err)
}

// NewProjectRegistrationConflictError classifies a safe daemon-side registration conflict for control clients.
func NewProjectRegistrationConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewProjectRegistrationInvalidError classifies a selected checkout that cannot form a valid registration.
func NewProjectRegistrationInvalidError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeInvalidRequest, cause)
}

// NewProjectUnregisterConflictError classifies current state that prevents unregister initiation or progress.
func NewProjectUnregisterConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewProjectUnregisterNotFoundError classifies a requested project that is not durably registered.
func NewProjectUnregisterNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}

// NewProjectUnregisterApprovalConflictError classifies a reviewed unregister lifecycle conflict for control clients.
func NewProjectUnregisterApprovalConflictError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeConflict, cause)
}

// NewProjectUnregisterApprovalNotFoundError classifies missing unregister authority for control clients.
func NewProjectUnregisterApprovalNotFoundError(cause error) error {
	return session.NewHandlerError(rpc.ErrorCodeNotFound, cause)
}
