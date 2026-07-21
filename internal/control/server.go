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
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/managedsession"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

const maximumEmptyRequestBytes = 64

// maximumProjectRegistrationRequestBytes allows encoding/json's six-byte escaping of each accepted path byte.
const maximumProjectRegistrationRequestBytes = maximumRegistrationPathBytes*6 + 16

// maximumProjectUnregisterRequestBytes covers two maximally escaped domain identifiers in one object.
const maximumProjectUnregisterRequestBytes = 4096

// maximumProjectLifecycleRequestBytes covers two maximally escaped domain identifiers in one object.
const maximumProjectLifecycleRequestBytes = 4096

// maximumProjectUnregisterApprovalRequestBytes covers a maximally escaped domain ID and exact integer revision.
const maximumProjectUnregisterApprovalRequestBytes = 2048

// ErrorObserver receives daemon-local method diagnostics together with the authenticated caller.
type ErrorObserver func(Caller, string, error)

// ServerConfig defines the immutable product authority and optional diagnostic sink.
type ServerConfig struct {
	// Authority owns the bounded daemon methods exposed by this server.
	Authority Authority
	// RequestShutdown publishes an idempotent, nonblocking, infallible request to the daemon lifecycle owner.
	RequestShutdown func()
	// ObserveError optionally records causes that are redacted from IPC responses.
	ObserveError ErrorObserver
	// ManagedAuthority optionally enables the isolated GoForj managed-session role on the same authenticated endpoint.
	ManagedAuthority managedsession.Authority
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
	if config.RequestShutdown == nil {
		return nil, errors.New("control server shutdown requester is required")
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

	shutdownAccepted := make(chan struct{})
	var acceptShutdown sync.Once
	var shutdownCaller Caller
	serverCapabilities := capabilities()
	serverAuthorize := authorizeControlHello
	var roleHandlers map[rpc.Role]map[string]session.Handler
	if server.config.ManagedAuthority != nil {
		serverCapabilities = append(serverCapabilities, managedsession.CapabilityV1)
		serverAuthorize = func(ctx context.Context, hello rpc.Hello) error {
			if hello.Role == rpc.RoleGoForjSession {
				if err := normalizeContext(ctx).Err(); err != nil {
					return err
				}
				if !containsCapability(hello.Capabilities, managedsession.CapabilityV1) {
					return errors.New("managed-session.v1 capability is required")
				}
				return nil
			}
			return authorizeControlHello(ctx, hello)
		}
		managedHandlers, handlerErr := managedsession.NewHandlerSet(transportPeer, server.config.ManagedAuthority)
		if handlerErr != nil {
			_ = connection.Close()
			return fmt.Errorf("configure managed session handlers: %w", handlerErr)
		}
		roleHandlers = map[rpc.Role]map[string]session.Handler{
			rpc.RoleGoForjSession: managedHandlers.Handlers(),
		}
	}
	controlSession, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  server.build.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   serverCapabilities,
		Authorize:      serverAuthorize,
		RoleHandlers:   roleHandlers,
		Handlers: map[string]session.Handler{
			methodDaemonStatus: server.statusHandler(transportPeer),
			methodDaemonStop: server.stopHandler(transportPeer, func(caller Caller) {
				acceptShutdown.Do(func() {
					shutdownCaller = caller
					close(shutdownAccepted)
				})
			}),
			methodNetworkSetupStart:                   server.networkSetupStartHandler(transportPeer),
			methodNetworkSetupApprovalPrepare:         server.networkSetupApprovalPrepareHandler(transportPeer),
			methodNetworkSetupApprovalConfirm:         server.networkSetupApprovalConfirmHandler(transportPeer),
			methodNetworkResolverSetupStart:           server.networkResolverSetupStartHandler(transportPeer),
			methodNetworkResolverSetupApprovalPrepare: server.networkResolverSetupApprovalPrepareHandler(transportPeer),
			methodNetworkResolverSetupApprovalConfirm: server.networkResolverSetupApprovalConfirmHandler(transportPeer),
			methodSnapshot:                            server.snapshotHandler(transportPeer),
			methodProjectActivity:                     server.projectActivityHandler(transportPeer),
			methodServiceLogs:                         server.serviceLogsHandler(transportPeer),
			methodProjectRuntimeRepairInspect:         server.projectRuntimeRepairInspectHandler(transportPeer),
			methodProjectRuntimeRepairConfirm:         server.projectRuntimeRepairConfirmHandler(transportPeer),
			methodProjectStart:                        server.projectStartHandler(transportPeer),
			methodProjectStop:                         server.projectStopHandler(transportPeer),
			methodProjectRestart:                      server.projectRestartHandler(transportPeer),
			methodProjectRegister:                     server.projectRegisterHandler(transportPeer),
			methodProjectUnregister:                   server.projectUnregisterHandler(transportPeer),
			methodProjectUnregisterApprovalPrepare:    server.projectUnregisterApprovalPrepareHandler(transportPeer),
			methodProjectUnregisterApprovalConfirm:    server.projectUnregisterApprovalConfirmHandler(transportPeer),
		},
		ObserveError: server.sessionErrorObserver(transportPeer),
	})
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("configure control session: %w", err)
	}

	serveErr := controlSession.Serve(ctx, connection)
	select {
	case <-shutdownAccepted:
		if err := publishShutdown(server.config.RequestShutdown); err != nil {
			server.observeControlError(shutdownCaller, methodDaemonStop, err)
			return errors.Join(serveErr, err)
		}
		return serveErr
	default:
		return serveErr
	}
}

// projectStartHandler admits one bounded start intent and validates daemon-selected operation identity before replying.
func (server *Server) projectStartHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectLifecycleV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project lifecycle capability was not negotiated"),
			)
		}
		startRequest, err := decodeStartProjectRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		lifecycle, err := server.config.Authority.StartProject(ctx, caller, startRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := lifecycle.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project start: %w", err))
		}
		if err := validateProjectLifecycleCorrelation(
			startRequest.ProjectID,
			startRequest.IntentID,
			domain.OperationKindProjectStart,
			lifecycle,
		); err != nil {
			return nil, authorityError(fmt.Errorf("validate project start: %w", err))
		}
		return projectLifecycleResponse{Lifecycle: lifecycle}, nil
	}
}

// projectStopHandler admits one bounded stop intent and validates daemon-selected operation identity before replying.
func (server *Server) projectStopHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectLifecycleV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project lifecycle capability was not negotiated"),
			)
		}
		stopRequest, err := decodeStopProjectRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		lifecycle, err := server.config.Authority.StopProject(ctx, caller, stopRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := lifecycle.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project stop: %w", err))
		}
		if err := validateProjectLifecycleCorrelation(
			stopRequest.ProjectID,
			stopRequest.IntentID,
			domain.OperationKindProjectStop,
			lifecycle,
		); err != nil {
			return nil, authorityError(fmt.Errorf("validate project stop: %w", err))
		}
		return projectLifecycleResponse{Lifecycle: lifecycle}, nil
	}
}

// projectRestartHandler admits one bounded restart intent and validates daemon-selected operation identity before replying.
func (server *Server) projectRestartHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectRestartV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project restart capability was not negotiated"),
			)
		}
		restartRequest, err := decodeRestartProjectRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		lifecycle, err := server.config.Authority.RestartProject(ctx, caller, restartRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := lifecycle.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project restart: %w", err))
		}
		if err := validateProjectLifecycleCorrelation(
			restartRequest.ProjectID,
			restartRequest.IntentID,
			domain.OperationKindProjectRestart,
			lifecycle,
		); err != nil {
			return nil, authorityError(fmt.Errorf("validate project restart: %w", err))
		}
		return projectLifecycleResponse{Lifecycle: lifecycle}, nil
	}
}

// observeControlError contains an optional diagnostic sink outside session dispatch.
func (server *Server) observeControlError(caller Caller, method string, err error) {
	if err == nil || server.config.ObserveError == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	server.config.ObserveError(caller, method, err)
}

// publishShutdown contains a configuration defect after an accepted connection has already ended.
func publishShutdown(request func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("publish daemon shutdown request: callback panicked: %v", recovered)
		}
	}()

	request()
	return nil
}

// stopHandler marks one authenticated request only after its acknowledgement is written to the serving connection.
func (server *Server) stopHandler(transportPeer local.PeerIdentity, acceptShutdown func(Caller)) session.Handler {
	return func(_ context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityDaemonControlV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("daemon control capability was not negotiated"),
			)
		}
		if err := decodeEmptyRequest(request.Payload); err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}

		return session.RespondAfterWrite(
			daemonStopResponse{Stopping: true},
			func() { acceptShutdown(caller) },
		), nil
	}
}

// projectUnregisterHandler admits only one client-owned intent while retaining operation identity inside daemon authority.
func (server *Server) projectUnregisterHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectUnregisterV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project unregister capability was not negotiated"),
			)
		}
		unregisterRequest, err := decodeProjectUnregisterRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		unregistration, err := server.config.Authority.UnregisterProject(ctx, caller, unregisterRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := unregistration.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project unregistration: %w", err))
		}
		if err := validateProjectUnregistrationCorrelation(unregisterRequest, unregistration); err != nil {
			return nil, authorityError(fmt.Errorf("validate project unregistration: %w", err))
		}
		return projectUnregistrationResponse{Unregistration: unregistration}, nil
	}
}

// projectUnregisterApprovalPrepareHandler derives caller identity before admitting one exact approval selection.
func (server *Server) projectUnregisterApprovalPrepareHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectUnregisterApprovalV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project unregister approval capability was not negotiated"),
			)
		}
		approvalRequest, err := decodePrepareProjectUnregisterApprovalRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		preparation, err := server.config.Authority.PrepareProjectUnregisterApproval(ctx, caller, approvalRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := preparation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project unregister approval preparation: %w", err))
		}
		if err := validateProjectUnregisterApprovalPreparationCorrelation(approvalRequest, preparation); err != nil {
			return nil, authorityError(fmt.Errorf("validate project unregister approval preparation: %w", err))
		}
		return projectUnregisterApprovalPreparationResponse{Preparation: preparation}, nil
	}
}

// projectUnregisterApprovalConfirmHandler derives caller identity before admitting one exact confirmation selection.
func (server *Server) projectUnregisterApprovalConfirmHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectUnregisterApprovalV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project unregister approval capability was not negotiated"),
			)
		}
		approvalRequest, err := decodeConfirmProjectUnregisterApprovalRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		confirmation, err := server.config.Authority.ConfirmProjectUnregisterApproval(ctx, caller, approvalRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := confirmation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project unregister approval confirmation: %w", err))
		}
		if err := validateProjectUnregisterApprovalConfirmationCorrelation(approvalRequest, confirmation); err != nil {
			return nil, authorityError(fmt.Errorf("validate project unregister approval confirmation: %w", err))
		}
		return projectUnregisterApprovalConfirmationResponse{Confirmation: confirmation}, nil
	}
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

// decodeProjectUnregisterRequest rejects hidden authority beyond one project and its client-stable intent.
func decodeProjectUnregisterRequest(payload []byte) (UnregisterProjectRequest, error) {
	if len(payload) == 0 || len(payload) > maximumProjectUnregisterRequestBytes {
		return UnregisterProjectRequest{}, errors.New("project unregister request exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return UnregisterProjectRequest{}, fmt.Errorf("decode project unregister request: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return UnregisterProjectRequest{}, errors.New("project unregister request must be an object")
	}

	var unregisterRequest UnregisterProjectRequest
	projectSeen := false
	intentSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return UnregisterProjectRequest{}, fmt.Errorf("decode project unregister field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return UnregisterProjectRequest{}, errors.New("project unregister field name must be a string")
		}
		switch field {
		case "project_id":
			if projectSeen {
				return UnregisterProjectRequest{}, errors.New("project unregister request contains duplicate field \"project_id\"")
			}
			if err := decoder.Decode(&unregisterRequest.ProjectID); err != nil {
				return UnregisterProjectRequest{}, fmt.Errorf("decode project unregister project ID: %w", err)
			}
			projectSeen = true
		case "intent_id":
			if intentSeen {
				return UnregisterProjectRequest{}, errors.New("project unregister request contains duplicate field \"intent_id\"")
			}
			if err := decoder.Decode(&unregisterRequest.IntentID); err != nil {
				return UnregisterProjectRequest{}, fmt.Errorf("decode project unregister intent ID: %w", err)
			}
			intentSeen = true
		default:
			return UnregisterProjectRequest{}, fmt.Errorf("project unregister request contains unknown field %q", field)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return UnregisterProjectRequest{}, fmt.Errorf("decode project unregister request end: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return UnregisterProjectRequest{}, errors.New("project unregister request object is not terminated")
	}
	if !projectSeen || !intentSeen {
		return UnregisterProjectRequest{}, errors.New("project unregister request requires project_id and intent_id")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return UnregisterProjectRequest{}, err
	}
	if err := unregisterRequest.Validate(); err != nil {
		return UnregisterProjectRequest{}, err
	}
	return unregisterRequest, nil
}

// decodeStartProjectRequest rejects hidden authority beyond one project and its client-stable start intent.
func decodeStartProjectRequest(payload []byte) (StartProjectRequest, error) {
	projectID, intentID, err := decodeProjectLifecycleSelection(payload, "start")
	if err != nil {
		return StartProjectRequest{}, err
	}
	request := StartProjectRequest{ProjectID: projectID, IntentID: intentID}
	if err := request.Validate(); err != nil {
		return StartProjectRequest{}, err
	}
	return request, nil
}

// decodeStopProjectRequest rejects hidden authority beyond one project and its client-stable stop intent.
func decodeStopProjectRequest(payload []byte) (StopProjectRequest, error) {
	projectID, intentID, err := decodeProjectLifecycleSelection(payload, "stop")
	if err != nil {
		return StopProjectRequest{}, err
	}
	request := StopProjectRequest{ProjectID: projectID, IntentID: intentID}
	if err := request.Validate(); err != nil {
		return StopProjectRequest{}, err
	}
	return request, nil
}

// decodeRestartProjectRequest rejects hidden authority beyond one project and its client-stable restart intent.
func decodeRestartProjectRequest(payload []byte) (RestartProjectRequest, error) {
	projectID, intentID, err := decodeProjectLifecycleSelection(payload, "restart")
	if err != nil {
		return RestartProjectRequest{}, err
	}
	request := RestartProjectRequest{ProjectID: projectID, IntentID: intentID}
	if err := request.Validate(); err != nil {
		return RestartProjectRequest{}, err
	}
	return request, nil
}

// decodeProjectLifecycleSelection parses both lifecycle methods through one bounded duplicate-aware object contract.
func decodeProjectLifecycleSelection(payload []byte, action string) (domain.ProjectID, domain.IntentID, error) {
	if len(payload) == 0 || len(payload) > maximumProjectLifecycleRequestBytes {
		return "", "", fmt.Errorf("project %s request exceeds its bounded object shape", action)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return "", "", fmt.Errorf("decode project %s request: %w", action, err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return "", "", fmt.Errorf("project %s request must be an object", action)
	}
	var projectID domain.ProjectID
	var intentID domain.IntentID
	projectSeen := false
	intentSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return "", "", fmt.Errorf("decode project %s field: %w", action, err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return "", "", fmt.Errorf("project %s field name must be a string", action)
		}
		switch field {
		case "project_id":
			if projectSeen {
				return "", "", fmt.Errorf("project %s request contains duplicate field %q", action, field)
			}
			if err := decoder.Decode(&projectID); err != nil {
				return "", "", fmt.Errorf("decode project %s project ID: %w", action, err)
			}
			projectSeen = true
		case "intent_id":
			if intentSeen {
				return "", "", fmt.Errorf("project %s request contains duplicate field %q", action, field)
			}
			if err := decoder.Decode(&intentID); err != nil {
				return "", "", fmt.Errorf("decode project %s intent ID: %w", action, err)
			}
			intentSeen = true
		default:
			return "", "", fmt.Errorf("project %s request contains unknown field %q", action, field)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return "", "", fmt.Errorf("decode project %s request end: %w", action, err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return "", "", fmt.Errorf("project %s request object is not terminated", action)
	}
	if !projectSeen || !intentSeen {
		return "", "", fmt.Errorf("project %s request requires project_id and intent_id", action)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return "", "", err
	}
	return projectID, intentID, nil
}

// decodePrepareProjectUnregisterApprovalRequest rejects any authority beyond one exact operation revision.
func decodePrepareProjectUnregisterApprovalRequest(payload []byte) (PrepareProjectUnregisterApprovalRequest, error) {
	operationID, revision, err := decodeProjectUnregisterApprovalSelection(payload)
	if err != nil {
		return PrepareProjectUnregisterApprovalRequest{}, err
	}
	request := PrepareProjectUnregisterApprovalRequest{
		OperationID:               operationID,
		ExpectedOperationRevision: revision,
	}
	if err := request.Validate(); err != nil {
		return PrepareProjectUnregisterApprovalRequest{}, err
	}
	return request, nil
}

// decodeConfirmProjectUnregisterApprovalRequest rejects any authority beyond one exact operation revision.
func decodeConfirmProjectUnregisterApprovalRequest(payload []byte) (ConfirmProjectUnregisterApprovalRequest, error) {
	operationID, revision, err := decodeProjectUnregisterApprovalSelection(payload)
	if err != nil {
		return ConfirmProjectUnregisterApprovalRequest{}, err
	}
	request := ConfirmProjectUnregisterApprovalRequest{
		OperationID:               operationID,
		ExpectedOperationRevision: revision,
	}
	if err := request.Validate(); err != nil {
		return ConfirmProjectUnregisterApprovalRequest{}, err
	}
	return request, nil
}

// decodeProjectUnregisterApprovalSelection parses both approval methods through one duplicate-aware object contract.
func decodeProjectUnregisterApprovalSelection(payload []byte) (domain.OperationID, domain.Sequence, error) {
	if len(payload) == 0 || len(payload) > maximumProjectUnregisterApprovalRequestBytes {
		return "", 0, errors.New("project unregister approval request exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return "", 0, fmt.Errorf("decode project unregister approval request: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return "", 0, errors.New("project unregister approval request must be an object")
	}
	var operationID domain.OperationID
	var revision domain.Sequence
	operationSeen := false
	revisionSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return "", 0, fmt.Errorf("decode project unregister approval field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return "", 0, errors.New("project unregister approval field name must be a string")
		}
		switch field {
		case "operation_id":
			if operationSeen {
				return "", 0, errors.New("project unregister approval request contains duplicate field \"operation_id\"")
			}
			if err := decoder.Decode(&operationID); err != nil {
				return "", 0, fmt.Errorf("decode project unregister approval operation ID: %w", err)
			}
			operationSeen = true
		case "expected_operation_revision":
			if revisionSeen {
				return "", 0, errors.New("project unregister approval request contains duplicate field \"expected_operation_revision\"")
			}
			if err := decoder.Decode(&revision); err != nil {
				return "", 0, fmt.Errorf("decode project unregister approval revision: %w", err)
			}
			revisionSeen = true
		default:
			return "", 0, fmt.Errorf("project unregister approval request contains unknown field %q", field)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return "", 0, fmt.Errorf("decode project unregister approval request end: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return "", 0, errors.New("project unregister approval request object is not terminated")
	}
	if !operationSeen || !revisionSeen {
		return "", 0, errors.New("project unregister approval request requires operation_id and expected_operation_revision")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return "", 0, err
	}
	return operationID, revision, nil
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
