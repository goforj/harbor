package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

const maximumEmptyRequestBytes = 64

// maximumProjectRegistrationRequestBytes allows encoding/json's six-byte escaping of each accepted path byte.
const maximumProjectRegistrationRequestBytes = maximumRegistrationPathBytes*6 + 16

// ErrorObserver receives daemon-local method diagnostics together with the authenticated caller.
type ErrorObserver func(Caller, string, error)

// ServerConfig defines the immutable product authority and optional diagnostic sink.
type ServerConfig struct {
	// Authority owns the bounded daemon methods exposed by this server.
	Authority Authority
	// ObserveError optionally records causes that are redacted from IPC responses.
	ObserveError ErrorObserver
}

// Server adapts authenticated local connections to the typed Harbor control API.
type Server struct {
	config ServerConfig
	build  buildinfo.Info
}

// NewServer validates and freezes daemon-side product control policy.
func NewServer(config ServerConfig) (*Server, error) {
	return newServer(config, buildinfo.Current())
}

// newServer keeps process build metadata deterministic in protocol tests.
func newServer(config ServerConfig, build buildinfo.Info) (*Server, error) {
	if authorityIsNil(config.Authority) {
		return nil, errors.New("control server authority is required")
	}
	if err := validateBuild(buildFromInfo(build)); err != nil {
		return nil, fmt.Errorf("control server build: %w", err)
	}

	return &Server{config: config, build: build}, nil
}

// authorityIsNil catches typed nil implementations before they become deferred request panics.
func authorityIsNil(authority Authority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Serve owns one operating-system-authenticated connection until either peer closes it.
func (server *Server) Serve(ctx context.Context, connection local.Conn) error {
	if connection == nil {
		return errors.New("control server connection is required")
	}
	ctx = normalizeContext(ctx)
	transportPeer := connection.Peer()
	if err := validateTransportPeer(transportPeer); err != nil {
		_ = connection.Close()
		return fmt.Errorf("control transport peer: %w", err)
	}

	controlSession, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  server.build.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   capabilities(),
		Authorize:      authorizeControlHello,
		Handlers: map[string]session.Handler{
			methodDaemonStatus:    server.statusHandler(transportPeer),
			methodSnapshot:        server.snapshotHandler(transportPeer),
			methodProjectRegister: server.projectRegisterHandler(transportPeer),
		},
		ObserveError: server.sessionErrorObserver(transportPeer),
	})
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("configure control session: %w", err)
	}

	return controlSession.Serve(ctx, connection)
}

// projectRegisterHandler admits only a validated canonical-path request before invoking daemon authority.
func (server *Server) projectRegisterHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectRegistrationV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project registration capability was not negotiated"),
			)
		}
		registrationRequest, err := decodeProjectRegistrationRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		registration, err := server.config.Authority.RegisterProject(ctx, caller, registrationRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := registration.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project registration: %w", err))
		}
		return projectRegistrationResponse{Registration: registration}, nil
	}
}

// statusHandler captures the immutable transport identity before session dispatch begins.
func (server *Server) statusHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if err := decodeEmptyRequest(request.Payload); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		status, err := server.config.Authority.Status(ctx, caller)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := validateServingStatus(status, server.build, caller.Session); err != nil {
			return nil, authorityError(fmt.Errorf("validate daemon status: %w", err))
		}

		return statusResponse{Status: status}, nil
	}
}

// snapshotHandler captures the immutable transport identity before session dispatch begins.
func (server *Server) snapshotHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if err := decodeEmptyRequest(request.Payload); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		snapshot, err := server.config.Authority.Snapshot(ctx, caller)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := validateControlSnapshot(snapshot); err != nil {
			return nil, authorityError(fmt.Errorf("validate daemon snapshot: %w", err))
		}

		return snapshotResponse{Snapshot: snapshot}, nil
	}
}

// sessionErrorObserver restores transport identity to diagnostics emitted below the product boundary.
func (server *Server) sessionErrorObserver(transportPeer local.PeerIdentity) session.ErrorObserver {
	if server.config.ObserveError == nil {
		return nil
	}

	return func(request session.Request, err error) {
		server.config.ObserveError(Caller{Transport: transportPeer, Session: request.Peer}, request.Method, err)
	}
}

// authorizeControlHello admits only human-facing clients that explicitly understand this product API.
func authorizeControlHello(ctx context.Context, hello rpc.Hello) error {
	if err := normalizeContext(ctx).Err(); err != nil {
		return err
	}
	if hello.Role != rpc.RoleCLI && hello.Role != rpc.RoleDesktop {
		return fmt.Errorf("role %q cannot use the product control API", hello.Role)
	}
	if !containsCapability(hello.Capabilities, CapabilityV1) {
		return errors.New("control.v1 capability is required")
	}

	return nil
}

// callerFromRequest enforces product-role and negotiated-feature policy at every method boundary.
func callerFromRequest(transportPeer local.PeerIdentity, request session.Request) (Caller, error) {
	if request.Peer.Role != rpc.RoleCLI && request.Peer.Role != rpc.RoleDesktop {
		return Caller{}, fmt.Errorf("role %q cannot use the product control API", request.Peer.Role)
	}
	if request.Peer.Protocol.Compare(protocolV1) != 0 {
		return Caller{}, fmt.Errorf("protocol %s cannot use control.v1", request.Peer.Protocol)
	}
	if !containsCapability(request.Peer.Capabilities, CapabilityV1) {
		return Caller{}, errors.New("control.v1 was not negotiated")
	}

	return Caller{Transport: transportPeer, Session: request.Peer}, nil
}

// containsCapability reports whether a canonical or caller-supplied feature list contains one capability.
func containsCapability(capabilities []rpc.Capability, wanted rpc.Capability) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}

	return false
}

// validateTransportPeer rejects impossible authenticated identities before reading application bytes.
func validateTransportPeer(peer local.PeerIdentity) error {
	if peer.UserID == "" || strings.TrimSpace(peer.UserID) != peer.UserID {
		return errors.New("operating-system user identity is invalid")
	}
	if len(peer.UserID) > 256 {
		return errors.New("operating-system user identity exceeds 256 bytes")
	}
	if !utf8.ValidString(peer.UserID) {
		return errors.New("operating-system user identity is not valid UTF-8")
	}
	for _, character := range peer.UserID {
		if unicode.IsControl(character) {
			return errors.New("operating-system user identity contains a control character")
		}
	}
	if peer.ProcessID == 0 {
		return errors.New("operating-system process identity is invalid")
	}

	return nil
}

// decodeEmptyRequest requires the reviewed empty-object shape without accepting ignored input.
func decodeEmptyRequest(payload []byte) error {
	if len(payload) == 0 || len(payload) > maximumEmptyRequestBytes {
		return errors.New("control request must be a bounded empty object")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var request map[string]json.RawMessage
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("decode empty control request: %w", err)
	}
	if request == nil || len(request) != 0 {
		return errors.New("control request must be an empty object")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return err
	}

	return nil
}

// decodeProjectRegistrationRequest rejects ignored fields, concatenated values, and oversized path documents.
func decodeProjectRegistrationRequest(payload []byte) (RegisterProjectRequest, error) {
	if len(payload) == 0 || len(payload) > maximumProjectRegistrationRequestBytes {
		return RegisterProjectRequest{}, errors.New("project registration request exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var registrationRequest RegisterProjectRequest
	opening, err := decoder.Token()
	if err != nil {
		return RegisterProjectRequest{}, fmt.Errorf("decode project registration request: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return RegisterProjectRequest{}, errors.New("project registration request must be an object")
	}
	pathSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return RegisterProjectRequest{}, fmt.Errorf("decode project registration field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return RegisterProjectRequest{}, errors.New("project registration field name must be a string")
		}
		if field != "path" {
			return RegisterProjectRequest{}, fmt.Errorf("project registration request contains unknown field %q", field)
		}
		if pathSeen {
			return RegisterProjectRequest{}, errors.New("project registration request contains duplicate field \"path\"")
		}
		if err := decoder.Decode(&registrationRequest.Path); err != nil {
			return RegisterProjectRequest{}, fmt.Errorf("decode project registration path: %w", err)
		}
		pathSeen = true
	}
	closing, err := decoder.Token()
	if err != nil {
		return RegisterProjectRequest{}, fmt.Errorf("decode project registration request end: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return RegisterProjectRequest{}, errors.New("project registration request object is not terminated")
	}
	if !pathSeen {
		return RegisterProjectRequest{}, errors.New("project registration request path is required")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return RegisterProjectRequest{}, err
	}
	if err := registrationRequest.Validate(); err != nil {
		return RegisterProjectRequest{}, err
	}
	return registrationRequest, nil
}

// requireJSONEnd rejects concatenated JSON values that could be interpreted differently by another client.
func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing control request data: %w", err)
	}

	return errors.New("control request contains more than one JSON value")
}
