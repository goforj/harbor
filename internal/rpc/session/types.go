package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/rpc"
)

const (
	defaultHandshakeTimeout      = 5 * time.Second
	defaultWriteTimeout          = 5 * time.Second
	defaultIdleTimeout           = 5 * time.Minute
	defaultRequestTimeout        = 30 * time.Second
	defaultMaxConcurrentRequests = 16
	defaultMaxQueuedRequests     = 32
	defaultMaxPendingRequests    = 64
	maximumRequestLimit          = 4096
)

var (
	// ErrClosed reports an operation attempted after a session became terminal.
	ErrClosed = errors.New("RPC session is closed")
	// ErrBusy reports that a local bounded request queue has reached capacity.
	ErrBusy = errors.New("RPC session request capacity is exhausted")
	// ErrProtocolViolation reports peer input that cannot safely continue on the connection.
	ErrProtocolViolation = errors.New("RPC protocol violation")
	// ErrIdleTimeout reports a negotiated connection that remained without accepted work.
	ErrIdleTimeout = errors.New("RPC session idle timeout")
	// ErrWriteTimeout reports a post-handshake frame that could not be written in time.
	ErrWriteTimeout = errors.New("RPC session write timeout")
)

// Peer describes the identity and features established by protocol negotiation.
type Peer struct {
	// Role is the peer's declared role after transport authentication.
	Role rpc.Role
	// BuildVersion is the peer's application build version.
	BuildVersion string
	// Protocol is the exact protocol version selected for this connection.
	Protocol rpc.Version
	// Capabilities is the canonical capability intersection for this connection.
	Capabilities []rpc.Capability
}

// Request is the bounded input passed to a registered handler.
type Request struct {
	// ID correlates one response or cancellation with its request.
	ID string
	// Method identifies the registered handler selected for this request.
	Method string
	// Payload contains one complete JSON value owned by the method schema.
	Payload json.RawMessage
	// Peer carries the negotiated client identity used for method authorization.
	Peer Peer
}

// Handler processes one request and must stop promptly when its context is cancelled.
type Handler func(context.Context, Request) (any, error)

// ErrorObserver receives daemon-local handler diagnostics before the peer sees
// only their redaction-safe wire category.
type ErrorObserver func(Request, error)

// AuthorizeFunc applies application authorization after the transport has
// authenticated the local peer and the Hello payload has been validated.
type AuthorizeFunc func(context.Context, rpc.Hello) error

// ServerConfig defines one immutable daemon-side RPC policy.
type ServerConfig struct {
	// DaemonVersion is the daemon build version advertised during negotiation.
	DaemonVersion string
	// ProtocolRanges are the protocol revisions understood by the daemon.
	ProtocolRanges []rpc.VersionRange
	// Capabilities are independently negotiated daemon features.
	Capabilities []rpc.Capability
	// Handlers maps bounded method names to their implementation.
	Handlers map[string]Handler
	// Authorize optionally tightens access for authenticated CLI and desktop
	// peers and is required before a GoForj session role can connect.
	Authorize AuthorizeFunc
	// ObserveError optionally records handler causes that are intentionally absent from wire errors.
	ObserveError ErrorObserver
	// HandshakeTimeout bounds unauthenticated negotiation work; zero uses five seconds.
	HandshakeTimeout time.Duration
	// WriteTimeout bounds each post-handshake frame write; zero uses five seconds.
	WriteTimeout time.Duration
	// IdleTimeout bounds negotiated connections only while they have no accepted
	// requests; zero uses five minutes.
	IdleTimeout time.Duration
	// MaxConcurrentRequests bounds handlers executing on one connection; zero uses 16.
	MaxConcurrentRequests int
	// MaxQueuedRequests bounds accepted requests waiting for a handler slot; zero uses 32.
	MaxQueuedRequests int
}

// ClientConfig defines one immutable daemon client policy.
type ClientConfig struct {
	// Role identifies the client authorization boundary.
	Role rpc.Role
	// ClientVersion is the client build version advertised during negotiation.
	ClientVersion string
	// ProtocolRanges are the protocol revisions understood by the client.
	ProtocolRanges []rpc.VersionRange
	// Capabilities are independently negotiated client features.
	Capabilities []rpc.Capability
	// HandshakeTimeout bounds initial negotiation; zero uses five seconds.
	HandshakeTimeout time.Duration
	// WriteTimeout bounds each post-handshake frame write; zero uses five seconds.
	WriteTimeout time.Duration
	// RequestTimeout supplies a deadline when Call receives a context without one; zero uses 30 seconds.
	RequestTimeout time.Duration
	// MaxPendingRequests bounds concurrent calls and cancelled calls awaiting a response; zero uses 64.
	MaxPendingRequests int
}

// HandlerError classifies a handler failure without exposing its diagnostic cause on the wire.
type HandlerError struct {
	code  rpc.ErrorCode
	cause error
}

// NewHandlerError creates a typed handler failure for a reviewed wire error category.
func NewHandlerError(code rpc.ErrorCode, cause error) *HandlerError {
	if cause == nil {
		cause = errors.New("handler failed without a diagnostic cause")
	}

	return &HandlerError{code: code, cause: cause}
}

// Error returns the daemon-local diagnostic cause.
func (e *HandlerError) Error() string {
	if e == nil || e.cause == nil {
		return "RPC handler failed"
	}

	return e.cause.Error()
}

// Unwrap exposes the daemon-local cause to logging and error inspection only.
func (e *HandlerError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}

// Code returns the reviewed category serialized instead of the diagnostic cause.
func (e *HandlerError) Code() rpc.ErrorCode {
	if e == nil || e.code == "" {
		return rpc.ErrorCodeInternal
	}

	return e.code
}

// HandlerPanicError preserves a recovered panic and stack for daemon-local observation only.
type HandlerPanicError struct {
	value any
	stack []byte
}

// Error returns a local diagnostic without participating in wire serialization.
func (e *HandlerPanicError) Error() string {
	if e == nil {
		return "RPC handler panicked"
	}

	return fmt.Sprintf("RPC handler panicked: %v", e.value)
}

// Stack returns a copy of the stack captured at the handler boundary.
func (e *HandlerPanicError) Stack() []byte {
	if e == nil {
		return nil
	}

	return append([]byte(nil), e.stack...)
}

// HandshakeError reports a redaction-safe daemon rejection to a client.
type HandshakeError struct {
	// Failure is the reviewed rejection safe to display to the local user.
	Failure rpc.WireError
	// ProtocolRanges let callers distinguish upgrade guidance without parsing text.
	ProtocolRanges []rpc.VersionRange
}

// Error returns the daemon's reviewed rejection message.
func (e *HandshakeError) Error() string {
	if e == nil || e.Failure.Message == "" {
		return "RPC handshake was rejected"
	}

	return e.Failure.Message
}

// normalizedServerConfig validates and copies mutable server configuration.
func normalizedServerConfig(config ServerConfig) (ServerConfig, error) {
	ranges, err := rpc.CanonicalVersionRanges(config.ProtocolRanges)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("server protocol ranges: %w", err)
	}
	capabilities, err := rpc.CanonicalCapabilities(config.Capabilities)
	if err != nil {
		return ServerConfig{}, fmt.Errorf("server capabilities: %w", err)
	}
	if config.HandshakeTimeout < 0 {
		return ServerConfig{}, errors.New("server handshake timeout cannot be negative")
	}
	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = defaultHandshakeTimeout
	}
	if config.WriteTimeout < 0 {
		return ServerConfig{}, errors.New("server write timeout cannot be negative")
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.IdleTimeout < 0 {
		return ServerConfig{}, errors.New("server idle timeout cannot be negative")
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultIdleTimeout
	}
	if config.MaxConcurrentRequests < 0 || config.MaxConcurrentRequests > maximumRequestLimit {
		return ServerConfig{}, fmt.Errorf("maximum concurrent requests must be between 0 and %d", maximumRequestLimit)
	}
	if config.MaxConcurrentRequests == 0 {
		config.MaxConcurrentRequests = defaultMaxConcurrentRequests
	}
	if config.MaxQueuedRequests < 0 || config.MaxQueuedRequests > maximumRequestLimit {
		return ServerConfig{}, fmt.Errorf("maximum queued requests must be between 0 and %d", maximumRequestLimit)
	}
	if config.MaxQueuedRequests == 0 {
		config.MaxQueuedRequests = defaultMaxQueuedRequests
	}

	handlers := make(map[string]Handler, len(config.Handlers))
	for method, handler := range config.Handlers {
		if handler == nil {
			return ServerConfig{}, fmt.Errorf("handler %q is nil", method)
		}
		if _, err := rpc.NewRequestEnvelope(ranges[0].Min, "config-check", method, time.Now().UTC().Add(time.Second), struct{}{}); err != nil {
			return ServerConfig{}, fmt.Errorf("handler method %q: %w", method, err)
		}
		handlers[method] = handler
	}

	// Negotiating a known-good synthetic client reuses the protocol's daemon
	// token validation instead of maintaining a second validation policy here.
	validationHello := rpc.Hello{
		ProtocolRanges: ranges,
		Role:           rpc.RoleCLI,
		ClientVersion:  "config-check",
	}
	if _, rejection := rpc.NegotiateHello(validationHello, config.DaemonVersion, ranges, capabilities); rejection != nil {
		return ServerConfig{}, errors.New("server daemon version or negotiation configuration is invalid")
	}

	config.ProtocolRanges = ranges
	config.Capabilities = capabilities
	config.Handlers = handlers

	return config, nil
}

// normalizedClientConfig validates and copies mutable client configuration.
func normalizedClientConfig(config ClientConfig) (ClientConfig, error) {
	if err := config.Role.ValidateClient(); err != nil {
		return ClientConfig{}, fmt.Errorf("client role: %w", err)
	}
	ranges, err := rpc.CanonicalVersionRanges(config.ProtocolRanges)
	if err != nil {
		return ClientConfig{}, fmt.Errorf("client protocol ranges: %w", err)
	}
	capabilities, err := rpc.CanonicalCapabilities(config.Capabilities)
	if err != nil {
		return ClientConfig{}, fmt.Errorf("client capabilities: %w", err)
	}
	if config.HandshakeTimeout < 0 {
		return ClientConfig{}, errors.New("client handshake timeout cannot be negative")
	}
	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = defaultHandshakeTimeout
	}
	if config.WriteTimeout < 0 {
		return ClientConfig{}, errors.New("client write timeout cannot be negative")
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = defaultWriteTimeout
	}
	if config.RequestTimeout < 0 {
		return ClientConfig{}, errors.New("client request timeout cannot be negative")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	if config.MaxPendingRequests < 0 || config.MaxPendingRequests > maximumRequestLimit {
		return ClientConfig{}, fmt.Errorf("maximum pending requests must be between 0 and %d", maximumRequestLimit)
	}
	if config.MaxPendingRequests == 0 {
		config.MaxPendingRequests = defaultMaxPendingRequests
	}

	hello := rpc.Hello{
		ProtocolRanges: ranges,
		Role:           config.Role,
		ClientVersion:  config.ClientVersion,
		Capabilities:   capabilities,
	}
	if _, err := rpc.NewHelloEnvelope(hello); err != nil {
		return ClientConfig{}, fmt.Errorf("client hello: %w", err)
	}

	config.ProtocolRanges = ranges
	config.Capabilities = capabilities

	return config, nil
}
