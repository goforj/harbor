package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// controlTestUnregistration returns a valid approval-bound operation suitable for initiation responses.
func controlTestUnregistration(t *testing.T) ProjectUnregistration {
	t.Helper()
	requestedAt := time.Date(2026, time.July, 19, 13, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation(
		"operation-remove",
		"intent-remove",
		domain.OperationKindProjectUnregister,
		"project-orders",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation, err = operation.Transition(
		domain.OperationRunning,
		"releasing project network",
		requestedAt.Add(time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	operation, err = operation.Transition(
		domain.OperationRequiresApproval,
		"awaiting host network release approval",
		requestedAt.Add(2*time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("Transition(requires_approval) error = %v", err)
	}
	return ProjectUnregistration{Operation: operation, Revision: 43}
}

// TestProjectUnregisterContractValidationAndCorrelation covers the complete initial request and result invariants.
func TestProjectUnregisterContractValidationAndCorrelation(t *testing.T) {
	request := UnregisterProjectRequest{ProjectID: "project-orders", IntentID: "intent-remove"}
	unregistration := controlTestUnregistration(t)
	if err := request.Validate(); err != nil {
		t.Fatalf("UnregisterProjectRequest.Validate() error = %v", err)
	}
	if err := unregistration.Validate(); err != nil {
		t.Fatalf("ProjectUnregistration.Validate() error = %v", err)
	}
	if err := validateProjectUnregistrationCorrelation(request, unregistration); err != nil {
		t.Fatalf("validateProjectUnregistrationCorrelation() error = %v", err)
	}

	for _, invalid := range []UnregisterProjectRequest{
		{},
		{ProjectID: request.ProjectID},
		{IntentID: request.IntentID},
		{ProjectID: " bad ", IntentID: request.IntentID},
		{ProjectID: request.ProjectID, IntentID: "bad\nintent"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("request %#v passed validation", invalid)
		}
	}

	for _, test := range []struct {
		name   string
		mutate func(*ProjectUnregistration)
	}{
		{name: "operation", mutate: func(result *ProjectUnregistration) { result.Operation.ID = "" }},
		{name: "kind", mutate: func(result *ProjectUnregistration) { result.Operation.Kind = "project.refresh" }},
		{name: "zero revision", mutate: func(result *ProjectUnregistration) { result.Revision = 0 }},
		{name: "large revision", mutate: func(result *ProjectUnregistration) { result.Revision = domain.MaximumSequence + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := unregistration
			test.mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("ProjectUnregistration.Validate() error = nil")
			}
		})
	}

	otherOperation := unregistration
	otherOperation.Operation.ID = "operation-other"
	if err := validateProjectUnregistrationCorrelation(request, otherOperation); err != nil {
		t.Fatalf("daemon-selected operation ID affected correlation: %v", err)
	}
	for _, mutate := range []func(*ProjectUnregistration){
		func(result *ProjectUnregistration) { result.Operation.ProjectID = "project-other" },
		func(result *ProjectUnregistration) { result.Operation.IntentID = "intent-other" },
	} {
		invalid := unregistration
		mutate(&invalid)
		if err := invalid.Validate(); err != nil {
			t.Fatalf("correlation fixture became structurally invalid: %v", err)
		}
		if err := validateProjectUnregistrationCorrelation(request, invalid); err == nil {
			t.Fatal("uncorrelated project unregistration passed validation")
		}
	}
}

// TestDecodeProjectUnregisterRequestRequiresExactObject proves only project and intent authority is accepted.
func TestDecodeProjectUnregisterRequestRequiresExactObject(t *testing.T) {
	valid := `{"project_id":"project-orders","intent_id":"intent-remove"}`
	request, err := decodeProjectUnregisterRequest([]byte(valid))
	if err != nil {
		t.Fatalf("decodeProjectUnregisterRequest() error = %v", err)
	}
	if request != (UnregisterProjectRequest{ProjectID: "project-orders", IntentID: "intent-remove"}) {
		t.Fatalf("decoded request = %#v", request)
	}

	tooLarge := "{" + strings.Repeat(" ", maximumProjectUnregisterRequestBytes) + "}"
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "missing"},
		{name: "malformed", payload: `x`},
		{name: "null", payload: `null`},
		{name: "array", payload: `[]`},
		{name: "empty", payload: `{}`},
		{name: "missing project", payload: `{"intent_id":"intent-remove"}`},
		{name: "missing intent", payload: `{"project_id":"project-orders"}`},
		{name: "unknown", payload: `{"project_id":"project-orders","intent_id":"intent-remove","force":true}`},
		{name: "operation authority", payload: `{"project_id":"project-orders","intent_id":"intent-remove","operation_id":"operation-client"}`},
		{name: "duplicate project", payload: `{"project_id":"project-orders","project_id":"project-other","intent_id":"intent-remove"}`},
		{name: "duplicate intent", payload: `{"project_id":"project-orders","intent_id":"intent-remove","intent_id":"intent-other"}`},
		{name: "project type", payload: `{"project_id":7,"intent_id":"intent-remove"}`},
		{name: "intent type", payload: `{"project_id":"project-orders","intent_id":7}`},
		{name: "invalid project", payload: `{"project_id":" bad ","intent_id":"intent-remove"}`},
		{name: "invalid intent", payload: `{"project_id":"project-orders","intent_id":" bad "}`},
		{name: "unterminated", payload: `{"project_id":"project-orders","intent_id":"intent-remove"`},
		{name: "concatenated", payload: valid + `{}`},
		{name: "trailing malformed", payload: valid + ` x`},
		{name: "too large", payload: tooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeProjectUnregisterRequest([]byte(test.payload)); err == nil {
				t.Fatal("decoder accepted invalid payload")
			}
		})
	}
}

// TestProjectUnregisterHandlerRejectsInvalidSessionIdentity proves method admission does not trust request payload alone.
func TestProjectUnregisterHandlerRejectsInvalidSessionIdentity(t *testing.T) {
	server := &Server{config: ServerConfig{Authority: &recordingAuthority{}}}
	_, err := server.projectUnregisterHandler(testClientPeer)(t.Context(), session.Request{})
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodePermissionDenied {
		t.Fatalf("handler error = %#v, want permission_denied", err)
	}
}

// TestDecodeProjectUnregisterRequestAllowsMaximumEscapedIDs proves the fixed budget covers valid JSON expansion.
func TestDecodeProjectUnregisterRequestAllowsMaximumEscapedIDs(t *testing.T) {
	request := UnregisterProjectRequest{
		ProjectID: domain.ProjectID(strings.Repeat("<", 256)),
		IntentID:  domain.IntentID(strings.Repeat(">", 256)),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("maximum request validation error = %v", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(payload) <= 512 {
		t.Fatalf("escaped payload size = %d, want expansion", len(payload))
	}
	got, err := decodeProjectUnregisterRequest(payload)
	if err != nil {
		t.Fatalf("decodeProjectUnregisterRequest() error = %v", err)
	}
	if got != request {
		t.Fatalf("decoded request = %#v, want %#v", got, request)
	}
}

// rawProjectUnregisterPayload emits deliberate duplicate or unknown fields through a real session.
type rawProjectUnregisterPayload string

// MarshalJSON returns the exact test document without normalization by encoding/json.
func (payload rawProjectUnregisterPayload) MarshalJSON() ([]byte, error) {
	return []byte(payload), nil
}

// TestControlClientRoundTripsProjectUnregisterForHumanRoles verifies CLI and desktop share one authenticated method.
func TestControlClientRoundTripsProjectUnregisterForHumanRoles(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			unregistration := controlTestUnregistration(t)
			request := UnregisterProjectRequest{
				ProjectID: unregistration.Operation.ProjectID,
				IntentID:  unregistration.Operation.IntentID,
			}
			authority := &recordingAuthority{unregistration: unregistration}
			running := newRunningControlClient(t, role, authority, nil)

			got, err := running.client.UnregisterProject(t.Context(), request)
			if err != nil {
				t.Fatalf("UnregisterProject() error = %v", err)
			}
			if !reflect.DeepEqual(got, unregistration) {
				t.Fatalf("unregistration = %#v, want %#v", got, unregistration)
			}
			authority.mu.Lock()
			requests := append([]UnregisterProjectRequest(nil), authority.unregistrationRequests...)
			authority.mu.Unlock()
			if !reflect.DeepEqual(requests, []UnregisterProjectRequest{request}) {
				t.Fatalf("authority requests = %#v, want %#v", requests, request)
			}
			callers := authority.recordedCallers()
			if len(callers) != 1 || callers[0].Transport != testClientPeer || callers[0].Session.Role != role {
				t.Fatalf("authority callers = %#v, want authenticated %s caller", callers, role)
			}
			if !containsCapability(callers[0].Session.Capabilities, CapabilityProjectUnregisterV1) {
				t.Fatalf("caller capabilities = %v", callers[0].Session.Capabilities)
			}
		})
	}
}

// TestProjectUnregisterHandlerRejectsUnreviewedJSON proves strict decoding precedes daemon authority.
func TestProjectUnregisterHandlerRejectsUnreviewedJSON(t *testing.T) {
	authority := &recordingAuthority{unregistration: controlTestUnregistration(t)}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	for _, payload := range []rawProjectUnregisterPayload{
		`{"project_id":"project-orders","project_id":"project-other","intent_id":"intent-remove"}`,
		`{"project_id":"project-orders","intent_id":"intent-remove","force":true}`,
	} {
		_, err := running.client.session.Call(t.Context(), methodProjectUnregister, payload)
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
			t.Fatalf("payload %s error = %#v, want invalid_request", payload, err)
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid project unregister JSON reached authority %d times", len(callers))
	}
}

// TestProjectUnregisterRequiresNegotiatedCapability proves base control clients cannot initiate removal.
func TestProjectUnregisterRequiresNegotiatedCapability(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	authority := &recordingAuthority{unregistration: controlTestUnregistration(t)}
	controlServer, err := newServer(ServerConfig{Authority: authority, RequestShutdown: func() {}}, testBuild)
	if err != nil {
		t.Fatalf("newServer() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- controlServer.Serve(context.Background(), serverConnection) }()
	client, err := session.NewClient(context.Background(), clientConnection, session.ClientConfig{
		Role:           rpc.RoleCLI,
		ClientVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   []rpc.Capability{CapabilityProjectUnregisterApprovalV1, CapabilityV1},
	})
	if err != nil {
		t.Fatalf("session.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("legacy project unregister server did not stop")
		}
	})

	_, err = client.Call(t.Context(), methodProjectUnregister, UnregisterProjectRequest{
		ProjectID: "project-orders",
		IntentID:  "intent-remove",
	})
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodePermissionDenied {
		t.Fatalf("unregister error = %#v, want permission_denied", err)
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("unnegotiated unregister reached authority %d times", len(callers))
	}
}

// TestControlClientRejectsProjectUnregisterAgainstOlderDaemon proves capability checks precede method dispatch.
func TestControlClientRejectsProjectUnregisterAgainstOlderDaemon(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	handlerCalled := make(chan struct{}, 1)
	server, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   []rpc.Capability{CapabilityProjectUnregisterApprovalV1, CapabilityV1},
		Handlers: map[string]session.Handler{
			methodProjectUnregister: func(context.Context, session.Request) (any, error) {
				handlerCalled <- struct{}{}
				return nil, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("session.NewServer() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(context.Background(), serverStream) }()
	client, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) {
			return &testLocalConn{Conn: clientStream, peer: testDaemonPeer}, nil
		},
	}, testBuild)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("older project unregister daemon did not stop")
		}
	})

	_, err = client.UnregisterProject(t.Context(), UnregisterProjectRequest{
		ProjectID: "project-orders",
		IntentID:  "intent-remove",
	})
	if err == nil || !strings.Contains(err.Error(), "does not support project unregister") {
		t.Fatalf("older daemon error = %v", err)
	}
	select {
	case <-handlerCalled:
		t.Fatal("client called unregister without its negotiated capability")
	default:
	}
}

// TestControlClientRejectsMalformedProjectUnregisterResponses proves response structure and correlation fail closed.
func TestControlClientRejectsMalformedProjectUnregisterResponses(t *testing.T) {
	valid := controlTestUnregistration(t)
	invalid := valid
	invalid.Revision = 0
	otherProject := valid
	otherProject.Operation.ProjectID = "project-other"
	otherIntent := valid
	otherIntent.Operation.IntentID = "intent-other"
	request := UnregisterProjectRequest{ProjectID: "project-orders", IntentID: "intent-remove"}
	for _, test := range []struct {
		name    string
		payload any
		want    string
	}{
		{name: "JSON", payload: "wrong", want: "decode project unregistration"},
		{name: "value", payload: projectUnregistrationResponse{Unregistration: invalid}, want: "validate project unregistration"},
		{name: "project correlation", payload: projectUnregistrationResponse{Unregistration: otherProject}, want: "requested project and intent"},
		{name: "intent correlation", payload: projectUnregistrationResponse{Unregistration: otherIntent}, want: "requested project and intent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTypedResponseTestClient(t, methodProjectUnregister, test.payload)
			if _, err := client.UnregisterProject(t.Context(), request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("UnregisterProject() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectUnregisterHandlerRejectsInvalidOutputAndRedactsFailures proves daemon defects remain local.
func TestProjectUnregisterHandlerRejectsInvalidOutputAndRedactsFailures(t *testing.T) {
	request := UnregisterProjectRequest{ProjectID: "project-orders", IntentID: "intent-remove"}
	privateFailure := errors.New("private unregister database path failed")
	authority := &recordingAuthority{unregistrationErr: privateFailure}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)

	_, err := running.client.UnregisterProject(t.Context(), request)
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("authority error = %#v, want internal", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("private failure crossed control boundary: %v", err)
	}

	authority.unregistrationErr = nil
	authority.unregistration = ProjectUnregistration{}
	_, err = running.client.UnregisterProject(t.Context(), request)
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("invalid output error = %#v, want internal", err)
	}

	authority.unregistration = controlTestUnregistration(t)
	authority.unregistration.Operation.IntentID = "intent-other"
	_, err = running.client.UnregisterProject(t.Context(), request)
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("uncorrelated output error = %#v, want internal", err)
	}
}

// TestControlClientRejectsInvalidProjectUnregisterLocally proves malformed IDs never reach daemon authority.
func TestControlClientRejectsInvalidProjectUnregisterLocally(t *testing.T) {
	authority := &recordingAuthority{unregistration: controlTestUnregistration(t)}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	if _, err := running.client.UnregisterProject(t.Context(), UnregisterProjectRequest{}); err == nil {
		t.Fatal("UnregisterProject() accepted invalid request")
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid local request reached authority %d times", len(callers))
	}
}

// TestProjectUnregisterErrorConstructorsKeepReviewedWireCodes verifies initiation classification remains in control.
func TestProjectUnregisterErrorConstructorsKeepReviewedWireCodes(t *testing.T) {
	cause := errors.New("reviewed project unregister failure")
	for _, test := range []struct {
		name      string
		construct func(error) error
		want      rpc.ErrorCode
	}{
		{name: "conflict", construct: NewProjectUnregisterConflictError, want: rpc.ErrorCodeConflict},
		{name: "not found", construct: NewProjectUnregisterNotFoundError, want: rpc.ErrorCodeNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.construct(cause)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.want || !errors.Is(err, cause) {
				t.Fatalf("constructor error = %#v, want %q wrapping cause", err, test.want)
			}
		})
	}
}
