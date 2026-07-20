package control

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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

// TestProjectLifecycleRequestsRequireStableProjectAndIntent validates both method-specific request shapes.
func TestProjectLifecycleRequestsRequireStableProjectAndIntent(t *testing.T) {
	validStart := StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"}
	validStop := StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"}
	if err := validStart.Validate(); err != nil {
		t.Fatalf("StartProjectRequest.Validate() error = %v", err)
	}
	if err := validStop.Validate(); err != nil {
		t.Fatalf("StopProjectRequest.Validate() error = %v", err)
	}

	for _, test := range []struct {
		name     string
		validate func() error
		want     string
	}{
		{name: "start project", validate: func() error { request := validStart; request.ProjectID = " bad "; return request.Validate() }, want: "project ID"},
		{name: "start intent", validate: func() error { request := validStart; request.IntentID = ""; return request.Validate() }, want: "intent ID"},
		{name: "stop project", validate: func() error { request := validStop; request.ProjectID = ""; return request.Validate() }, want: "project ID"},
		{name: "stop intent", validate: func() error { request := validStop; request.IntentID = " bad "; return request.Validate() }, want: "intent ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectLifecycleOperationAcceptsOnlyStartAndStopProgress keeps unrelated operations off the action response.
func TestProjectLifecycleOperationAcceptsOnlyStartAndStopProgress(t *testing.T) {
	for _, kind := range []domain.OperationKind{domain.OperationKindProjectStart, domain.OperationKindProjectStop} {
		result := projectLifecycleTestResult(t, kind)
		if err := result.Validate(); err != nil {
			t.Fatalf("ProjectLifecycleOperation{%s}.Validate() error = %v", kind, err)
		}
	}

	invalid := projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	invalid.Operation.Kind = domain.OperationKindProjectUnregister
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "start or stop") {
		t.Fatalf("ProjectLifecycleOperation(unregister).Validate() error = %v", err)
	}
	invalid = projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	invalid.Revision = 0
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("ProjectLifecycleOperation(zero revision).Validate() error = %v", err)
	}
	invalid.Revision = domain.Sequence(math.MaxUint64)
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("ProjectLifecycleOperation(overflow revision).Validate() error = %v", err)
	}
}

// TestProjectLifecycleCorrelationRequiresTheExactMethodIdentity prevents cross-action replay confusion.
func TestProjectLifecycleCorrelationRequiresTheExactMethodIdentity(t *testing.T) {
	result := projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	if err := validateProjectLifecycleCorrelation("project-orders", "intent-start-orders", domain.OperationKindProjectStart, result); err != nil {
		t.Fatalf("validateProjectLifecycleCorrelation() error = %v", err)
	}
	for _, mutate := range []func(*ProjectLifecycleOperation){
		func(candidate *ProjectLifecycleOperation) { candidate.Operation.ProjectID = "project-other" },
		func(candidate *ProjectLifecycleOperation) { candidate.Operation.IntentID = "intent-other" },
		func(candidate *ProjectLifecycleOperation) { candidate.Operation.Kind = domain.OperationKindProjectStop },
	} {
		candidate := result
		mutate(&candidate)
		if err := validateProjectLifecycleCorrelation("project-orders", "intent-start-orders", domain.OperationKindProjectStart, candidate); err == nil {
			t.Fatalf("validateProjectLifecycleCorrelation(%#v) error = nil", candidate)
		}
	}
}

// TestProjectLifecycleProtocolNamesAndCapabilityRemainStable protects the additive wire surface from accidental renaming.
func TestProjectLifecycleProtocolNamesAndCapabilityRemainStable(t *testing.T) {
	if CapabilityProjectLifecycleV1 != "control.project-lifecycle.v1" {
		t.Fatalf("CapabilityProjectLifecycleV1 = %q", CapabilityProjectLifecycleV1)
	}
	if methodProjectStart != "control.v1.project.start" || methodProjectStop != "control.v1.project.stop" {
		t.Fatalf("lifecycle methods = %q / %q", methodProjectStart, methodProjectStop)
	}
	if !reflect.DeepEqual(capabilities(), []rpc.Capability{
		CapabilityDaemonControlV1,
		CapabilityNetworkResolverSetupV1,
		CapabilityNetworkSetupV1,
		CapabilityProjectActivityWaitV1,
		CapabilityProjectActivityV1,
		CapabilityProjectLifecycleV1,
		CapabilityProjectRegistrationV1,
		CapabilityProjectRuntimeRepairV1,
		CapabilityProjectUnregisterApprovalV1,
		CapabilityProjectUnregisterV1,
		CapabilityServiceLogsWaitV1,
		CapabilityServiceLogsV1,
		CapabilityV1,
	}) {
		t.Fatalf("capabilities() = %v, want canonical lifecycle advertisement", capabilities())
	}
}

// TestDecodeProjectLifecycleRequestsRequiresExactObjects proves only project and intent authority crosses either method.
func TestDecodeProjectLifecycleRequestsRequiresExactObjects(t *testing.T) {
	valid := `{"project_id":"project-orders","intent_id":"intent-orders"}`
	start, err := decodeStartProjectRequest([]byte(valid))
	if err != nil || start != (StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-orders"}) {
		t.Fatalf("decodeStartProjectRequest() = %#v, %v", start, err)
	}
	stop, err := decodeStopProjectRequest([]byte(valid))
	if err != nil || stop != (StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-orders"}) {
		t.Fatalf("decodeStopProjectRequest() = %#v, %v", stop, err)
	}

	tooLarge := "{" + strings.Repeat(" ", maximumProjectLifecycleRequestBytes) + "}"
	invalid := []string{
		"",
		`x`,
		`null`,
		`[]`,
		`{}`,
		`{"project_id":"project-orders"}`,
		`{"intent_id":"intent-orders"}`,
		`{"project_id":"project-orders","intent_id":"intent-orders","force":true}`,
		`{"project_id":"project-orders","project_id":"project-other","intent_id":"intent-orders"}`,
		`{"project_id":"project-orders","intent_id":"intent-orders","intent_id":"intent-other"}`,
		`{"project_id":7,"intent_id":"intent-orders"}`,
		`{"project_id":"project-orders","intent_id":7}`,
		`{"project_id":" bad ","intent_id":"intent-orders"}`,
		`{"project_id":"project-orders","intent_id":" bad "}`,
		`{"project_id":"project-orders","intent_id":"intent-orders"`,
		valid + `{}`,
		valid + ` x`,
		tooLarge,
	}
	for _, decoder := range []struct {
		name string
		call func([]byte) error
	}{
		{name: "start", call: func(payload []byte) error { _, err := decodeStartProjectRequest(payload); return err }},
		{name: "stop", call: func(payload []byte) error { _, err := decodeStopProjectRequest(payload); return err }},
	} {
		decoder := decoder
		t.Run(decoder.name, func(t *testing.T) {
			for _, payload := range invalid {
				if err := decoder.call([]byte(payload)); err == nil {
					t.Fatalf("decoder accepted %q", payload)
				}
			}
		})
	}
}

// TestDecodeProjectLifecycleRequestsAllowsMaximumEscapedIDs proves valid identifiers fit the reviewed request budget.
func TestDecodeProjectLifecycleRequestsAllowsMaximumEscapedIDs(t *testing.T) {
	request := StartProjectRequest{
		ProjectID: domain.ProjectID(strings.Repeat("<", 256)),
		IntentID:  domain.IntentID(strings.Repeat(">", 256)),
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(payload) <= 512 {
		t.Fatalf("escaped payload length = %d, want expansion", len(payload))
	}
	got, err := decodeStartProjectRequest(payload)
	if err != nil || got != request {
		t.Fatalf("decodeStartProjectRequest() = %#v, %v, want %#v", got, err, request)
	}
}

// TestControlClientRoundTripsProjectLifecycleForHumanRoles verifies both actions retain caller and intent correlation.
func TestControlClientRoundTripsProjectLifecycleForHumanRoles(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		role := role
		t.Run(string(role), func(t *testing.T) {
			start := projectLifecycleTestResult(t, domain.OperationKindProjectStart)
			stop := projectLifecycleTestResult(t, domain.OperationKindProjectStop)
			authority := &recordingAuthority{startLifecycle: start, stopLifecycle: stop}
			running := newRunningControlClient(t, role, authority, nil)
			startRequest := StartProjectRequest{ProjectID: start.Operation.ProjectID, IntentID: start.Operation.IntentID}
			stopRequest := StopProjectRequest{ProjectID: stop.Operation.ProjectID, IntentID: stop.Operation.IntentID}

			gotStart, err := running.client.StartProject(t.Context(), startRequest)
			if err != nil || !reflect.DeepEqual(gotStart, start) {
				t.Fatalf("StartProject() = %#v, %v, want %#v", gotStart, err, start)
			}
			gotStop, err := running.client.StopProject(t.Context(), stopRequest)
			if err != nil || !reflect.DeepEqual(gotStop, stop) {
				t.Fatalf("StopProject() = %#v, %v, want %#v", gotStop, err, stop)
			}

			authority.mu.Lock()
			startRequests := append([]StartProjectRequest(nil), authority.startRequests...)
			stopRequests := append([]StopProjectRequest(nil), authority.stopRequests...)
			authority.mu.Unlock()
			if !reflect.DeepEqual(startRequests, []StartProjectRequest{startRequest}) ||
				!reflect.DeepEqual(stopRequests, []StopProjectRequest{stopRequest}) {
				t.Fatalf("authority requests = %#v / %#v", startRequests, stopRequests)
			}
			callers := authority.recordedCallers()
			if len(callers) != 2 {
				t.Fatalf("authority callers = %d, want 2", len(callers))
			}
			for _, caller := range callers {
				if caller.Transport != testClientPeer || caller.Session.Role != role ||
					!containsCapability(caller.Session.Capabilities, CapabilityProjectLifecycleV1) {
					t.Fatalf("authority caller = %#v", caller)
				}
			}
		})
	}
}

// rawProjectLifecyclePayload emits duplicate and unknown fields through a real framed session.
type rawProjectLifecyclePayload string

// MarshalJSON returns the exact adversarial document without encoding/json normalization.
func (payload rawProjectLifecyclePayload) MarshalJSON() ([]byte, error) {
	return []byte(payload), nil
}

// TestProjectLifecycleHandlersRejectUnreviewedJSON proves strict decoding precedes daemon authority.
func TestProjectLifecycleHandlersRejectUnreviewedJSON(t *testing.T) {
	authority := &recordingAuthority{
		startLifecycle: projectLifecycleTestResult(t, domain.OperationKindProjectStart),
		stopLifecycle:  projectLifecycleTestResult(t, domain.OperationKindProjectStop),
	}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	for _, method := range []string{methodProjectStart, methodProjectStop} {
		for _, payload := range []rawProjectLifecyclePayload{
			`{"project_id":"project-orders","project_id":"project-other","intent_id":"intent-orders"}`,
			`{"project_id":"project-orders","intent_id":"intent-orders","force":true}`,
		} {
			_, err := running.client.session.Call(t.Context(), method, payload)
			var wireError rpc.WireError
			if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
				t.Fatalf("%s payload %s error = %#v", method, payload, err)
			}
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid lifecycle JSON reached authority %d times", len(callers))
	}
}

// TestProjectLifecycleHandlersRejectInvalidSessionIdentity proves payloads cannot bypass authenticated control admission.
func TestProjectLifecycleHandlersRejectInvalidSessionIdentity(t *testing.T) {
	server := &Server{config: ServerConfig{Authority: &recordingAuthority{}}}
	for _, handler := range []session.Handler{
		server.projectStartHandler(testClientPeer),
		server.projectStopHandler(testClientPeer),
	} {
		_, err := handler(t.Context(), session.Request{})
		var handlerError *session.HandlerError
		if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodePermissionDenied {
			t.Fatalf("handler error = %#v, want permission_denied", err)
		}
	}
}

// TestProjectLifecycleRequiresNegotiatedCapability proves base control clients cannot start or stop projects.
func TestProjectLifecycleRequiresNegotiatedCapability(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	authority := &recordingAuthority{}
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
		Capabilities:   []rpc.Capability{CapabilityV1},
	})
	if err != nil {
		t.Fatalf("session.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("legacy lifecycle server did not stop")
		}
	})
	for _, call := range []struct {
		method  string
		request any
	}{
		{method: methodProjectStart, request: StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"}},
		{method: methodProjectStop, request: StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"}},
	} {
		_, err := client.Call(t.Context(), call.method, call.request)
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodePermissionDenied {
			t.Fatalf("%s error = %#v, want permission_denied", call.method, err)
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("unnegotiated lifecycle reached authority %d times", len(callers))
	}
}

// TestControlClientRejectsProjectLifecycleAgainstOlderDaemon proves capability checks precede method dispatch.
func TestControlClientRejectsProjectLifecycleAgainstOlderDaemon(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	handlerCalled := make(chan struct{}, 2)
	server, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   []rpc.Capability{CapabilityV1},
		Handlers: map[string]session.Handler{
			methodProjectStart: func(context.Context, session.Request) (any, error) { handlerCalled <- struct{}{}; return nil, nil },
			methodProjectStop:  func(context.Context, session.Request) (any, error) { handlerCalled <- struct{}{}; return nil, nil },
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
			t.Error("older lifecycle daemon did not stop")
		}
	})
	if _, err := client.StartProject(t.Context(), StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"}); err == nil || !strings.Contains(err.Error(), "does not support project lifecycle") {
		t.Fatalf("StartProject() error = %v", err)
	}
	if _, err := client.StopProject(t.Context(), StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"}); err == nil || !strings.Contains(err.Error(), "does not support project lifecycle") {
		t.Fatalf("StopProject() error = %v", err)
	}
	select {
	case <-handlerCalled:
		t.Fatal("client called lifecycle method without its negotiated capability")
	default:
	}
}

// TestControlClientRejectsMalformedProjectLifecycleResponses proves response structure and correlation fail closed.
func TestControlClientRejectsMalformedProjectLifecycleResponses(t *testing.T) {
	start := projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	stop := projectLifecycleTestResult(t, domain.OperationKindProjectStop)
	invalid := start
	invalid.Revision = 0
	wrongProject := start
	wrongProject.Operation.ProjectID = "project-other"
	wrongIntent := start
	wrongIntent.Operation.IntentID = "intent-other"
	invalidStop := stop
	invalidStop.Revision = 0
	for _, test := range []struct {
		name    string
		method  string
		payload any
		call    func(*Client) error
		want    string
	}{
		{name: "start JSON", method: methodProjectStart, payload: "wrong", call: func(client *Client) error {
			_, err := client.StartProject(t.Context(), StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"})
			return err
		}, want: "decode project start"},
		{name: "start value", method: methodProjectStart, payload: projectLifecycleResponse{Lifecycle: invalid}, call: func(client *Client) error {
			_, err := client.StartProject(t.Context(), StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"})
			return err
		}, want: "validate project start"},
		{name: "start project", method: methodProjectStart, payload: projectLifecycleResponse{Lifecycle: wrongProject}, call: func(client *Client) error {
			_, err := client.StartProject(t.Context(), StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"})
			return err
		}, want: "does not match"},
		{name: "start intent", method: methodProjectStart, payload: projectLifecycleResponse{Lifecycle: wrongIntent}, call: func(client *Client) error {
			_, err := client.StartProject(t.Context(), StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"})
			return err
		}, want: "does not match"},
		{name: "start kind on stop", method: methodProjectStop, payload: projectLifecycleResponse{Lifecycle: start}, call: func(client *Client) error {
			_, err := client.StopProject(t.Context(), StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"})
			return err
		}, want: "does not match"},
		{name: "stop JSON", method: methodProjectStop, payload: "wrong", call: func(client *Client) error {
			_, err := client.StopProject(t.Context(), StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"})
			return err
		}, want: "decode project stop"},
		{name: "stop value", method: methodProjectStop, payload: projectLifecycleResponse{Lifecycle: invalidStop}, call: func(client *Client) error {
			_, err := client.StopProject(t.Context(), StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"})
			return err
		}, want: "validate project stop"},
		{name: "stop valid", method: methodProjectStop, payload: projectLifecycleResponse{Lifecycle: stop}, call: func(client *Client) error {
			_, err := client.StopProject(t.Context(), StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTypedResponseTestClient(t, test.method, test.payload)
			err := test.call(client)
			if test.want == "" {
				if err != nil {
					t.Fatalf("call error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("call error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectLifecycleHandlersRejectInvalidOutputAndRedactFailures keeps daemon defects and private paths off the wire.
func TestProjectLifecycleHandlersRejectInvalidOutputAndRedactFailures(t *testing.T) {
	privateFailure := errors.New("private lifecycle state path failed")
	authority := &recordingAuthority{startErr: privateFailure}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	request := StartProjectRequest{ProjectID: "project-orders", IntentID: "intent-start-orders"}
	_, err := running.client.StartProject(t.Context(), request)
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal || strings.Contains(err.Error(), "private") {
		t.Fatalf("private authority error = %#v", err)
	}
	authority.startErr = nil
	authority.startLifecycle = ProjectLifecycleOperation{}
	_, err = running.client.StartProject(t.Context(), request)
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("invalid authority output error = %#v", err)
	}
	authority.startLifecycle = projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	authority.startLifecycle.Operation.IntentID = "intent-other"
	_, err = running.client.StartProject(t.Context(), request)
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("uncorrelated authority output error = %#v", err)
	}

	stopRequest := StopProjectRequest{ProjectID: "project-orders", IntentID: "intent-stop-orders"}
	authority.stopErr = privateFailure
	_, err = running.client.StopProject(t.Context(), stopRequest)
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal || strings.Contains(err.Error(), "private") {
		t.Fatalf("private stop authority error = %#v", err)
	}
	authority.stopErr = nil
	authority.stopLifecycle = ProjectLifecycleOperation{}
	_, err = running.client.StopProject(t.Context(), stopRequest)
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("invalid stop authority output error = %#v", err)
	}
	authority.stopLifecycle = projectLifecycleTestResult(t, domain.OperationKindProjectStart)
	_, err = running.client.StopProject(t.Context(), stopRequest)
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("uncorrelated stop authority output error = %#v", err)
	}
}

// TestControlClientRejectsInvalidProjectLifecycleLocally keeps malformed identities away from daemon authority.
func TestControlClientRejectsInvalidProjectLifecycleLocally(t *testing.T) {
	authority := &recordingAuthority{}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	if _, err := running.client.StartProject(t.Context(), StartProjectRequest{}); err == nil {
		t.Fatal("StartProject() accepted invalid request")
	}
	if _, err := running.client.StopProject(t.Context(), StopProjectRequest{}); err == nil {
		t.Fatal("StopProject() accepted invalid request")
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid local lifecycle requests reached authority %d times", len(callers))
	}
}

// TestProjectLifecycleErrorConstructorsKeepReviewedWireCodes verifies classification remains at the control boundary.
func TestProjectLifecycleErrorConstructorsKeepReviewedWireCodes(t *testing.T) {
	cause := errors.New("reviewed lifecycle failure")
	for _, test := range []struct {
		name      string
		construct func(error) error
		want      rpc.ErrorCode
	}{
		{name: "invalid", construct: NewProjectLifecycleInvalidError, want: rpc.ErrorCodeInvalidRequest},
		{name: "not found", construct: NewProjectLifecycleNotFoundError, want: rpc.ErrorCodeNotFound},
		{name: "conflict", construct: NewProjectLifecycleConflictError, want: rpc.ErrorCodeConflict},
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

// projectLifecycleTestResult creates one valid queued lifecycle operation response.
func projectLifecycleTestResult(t *testing.T, kind domain.OperationKind) ProjectLifecycleOperation {
	t.Helper()
	intent := domain.IntentID("intent-start-orders")
	if kind == domain.OperationKindProjectStop {
		intent = "intent-stop-orders"
	}
	operation, err := domain.NewOperation("operation-orders", intent, kind, "project-orders", time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return ProjectLifecycleOperation{Operation: operation, Revision: 41}
}
