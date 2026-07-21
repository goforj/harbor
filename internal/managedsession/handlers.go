package managedsession

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

var managedSessionProtocolV1 = rpc.Version{Major: 1, Minor: 0}

// Authority owns the durable and process-local effects behind managed-session requests.
//
// Implementations must authenticate the supplied transport peer and bind each method to the exact
// project/session fence before changing state. The handler layer does not persist credentials or infer
// process ownership from a port, PID, or request payload.
type Authority interface {
	RegisterManagedSession(context.Context, local.PeerIdentity, RegisterRequest) (RegisterResponse, error)
	ReplaceManagedPublications(context.Context, local.PeerIdentity, ReplacePublicationsRequest) (ReplacePublicationsResponse, error)
	AcknowledgeManagedBarrier(context.Context, local.PeerIdentity, BarrierRequest) (BarrierResponse, error)
}

// HandlerSet exposes the three bounded managed-session methods to the generic RPC session server.
type HandlerSet struct {
	authority Authority
	peer      local.PeerIdentity
}

// NewHandlerSet binds one authenticated local peer to an authority without broadening the human control API.
func NewHandlerSet(peer local.PeerIdentity, authority Authority) (*HandlerSet, error) {
	if err := validateManagedSessionPeer(peer); err != nil {
		return nil, err
	}
	if authority == nil {
		return nil, errors.New("managed session authority is required")
	}
	return &HandlerSet{authority: authority, peer: peer}, nil
}

// Handlers returns a fresh method map suitable for session.ServerConfig.
func (set *HandlerSet) Handlers() map[string]session.Handler {
	if set == nil {
		return nil
	}
	return map[string]session.Handler{
		MethodRegister:            set.registerHandler(),
		MethodReplacePublications: set.replacePublicationsHandler(),
		MethodBarrier:             set.barrierHandler(),
	}
}

// registerHandler admits only a negotiated GoForj session and validates the authority response before writing it.
func (set *HandlerSet) registerHandler() session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		if err := validateManagedSessionRequestPeer(request); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		registration, err := DecodeRegisterRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		response, err := set.authority.RegisterManagedSession(ctx, set.peer, registration)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInternal, err)
		}
		if err := ValidateRegisterCorrelation(registration, response); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInternal, fmt.Errorf("validate managed session registration response: %w", err))
		}
		return response, nil
	}
}

// replacePublicationsHandler admits only complete exact-fence publication replacements.
func (set *HandlerSet) replacePublicationsHandler() session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		if err := validateManagedSessionRequestPeer(request); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		publicationRequest, err := DecodeReplacePublicationsRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		response, err := set.authority.ReplaceManagedPublications(ctx, set.peer, publicationRequest)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInternal, err)
		}
		if err := ValidateReplacePublicationsCorrelation(publicationRequest, response); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInternal, fmt.Errorf("validate managed session publication response: %w", err))
		}
		return response, nil
	}
}

// barrierHandler admits one typed Compose barrier and validates the correlated acknowledgement.
func (set *HandlerSet) barrierHandler() session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		if err := validateManagedSessionRequestPeer(request); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		barrierRequest, err := DecodeBarrierRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		response, err := set.authority.AcknowledgeManagedBarrier(ctx, set.peer, barrierRequest)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInternal, err)
		}
		if err := ValidateBarrierCorrelation(barrierRequest, response); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInternal, fmt.Errorf("validate managed session barrier response: %w", err))
		}
		return response, nil
	}
}

// validateManagedSessionRequestPeer keeps method authorization independent from the generic human control server.
func validateManagedSessionRequestPeer(request session.Request) error {
	if request.Peer.Role != rpc.RoleGoForjSession {
		return fmt.Errorf("role %q cannot use the managed-session API", request.Peer.Role)
	}
	if request.Peer.Protocol.Compare(managedSessionProtocolV1) != 0 {
		return fmt.Errorf("protocol %s cannot use managed-session.v1", request.Peer.Protocol)
	}
	for _, capability := range request.Peer.Capabilities {
		if capability == CapabilityV1 {
			return nil
		}
	}
	return errors.New("managed-session.v1 was not negotiated")
}

// validateManagedSessionPeer rejects impossible transport identities before a handler set can be reused.
func validateManagedSessionPeer(peer local.PeerIdentity) error {
	if peer.UserID == "" || strings.TrimSpace(peer.UserID) != peer.UserID {
		return errors.New("managed session operating-system user identity is invalid")
	}
	if len(peer.UserID) > 256 {
		return errors.New("managed session operating-system user identity exceeds 256 bytes")
	}
	if !utf8.ValidString(peer.UserID) {
		return errors.New("managed session operating-system user identity is not valid UTF-8")
	}
	for _, character := range peer.UserID {
		if unicode.IsControl(character) {
			return errors.New("managed session operating-system user identity contains a control character")
		}
	}
	if peer.ProcessID == 0 {
		return errors.New("managed session operating-system process identity is invalid")
	}
	return nil
}
