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

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestControlClientRoundTripsProjectUnregisterApproval verifies both human roles preserve exact caller and revision authority.
func TestControlClientRoundTripsProjectUnregisterApproval(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			preparation := validControlApprovalPreparation()
			confirmation := validControlApprovalConfirmation(t)
			authority := &recordingAuthority{
				approvalPreparation:  preparation,
				approvalConfirmation: confirmation,
			}
			running := newRunningControlClient(t, role, authority, nil)
			prepareRequest := PrepareProjectUnregisterApprovalRequest{
				OperationID:               preparation.OperationID,
				ExpectedOperationRevision: preparation.OperationRevision,
			}
			confirmRequest := ConfirmProjectUnregisterApprovalRequest{
				OperationID:               preparation.OperationID,
				ExpectedOperationRevision: preparation.OperationRevision,
			}

			gotPreparation, err := running.client.PrepareProjectUnregisterApproval(t.Context(), prepareRequest)
			if err != nil {
				t.Fatalf("PrepareProjectUnregisterApproval() error = %v", err)
			}
			if !reflect.DeepEqual(gotPreparation, preparation) {
				t.Fatalf("preparation = %#v, want %#v", gotPreparation, preparation)
			}
			gotConfirmation, err := running.client.ConfirmProjectUnregisterApproval(t.Context(), confirmRequest)
			if err != nil {
				t.Fatalf("ConfirmProjectUnregisterApproval() error = %v", err)
			}
			if !reflect.DeepEqual(gotConfirmation, confirmation) {
				t.Fatalf("confirmation = %#v, want %#v", gotConfirmation, confirmation)
			}

			authority.mu.Lock()
			prepareRequests := append([]PrepareProjectUnregisterApprovalRequest(nil), authority.approvalPrepareRequests...)
			confirmRequests := append([]ConfirmProjectUnregisterApprovalRequest(nil), authority.approvalConfirmRequests...)
			authority.mu.Unlock()
			if !reflect.DeepEqual(prepareRequests, []PrepareProjectUnregisterApprovalRequest{prepareRequest}) ||
				!reflect.DeepEqual(confirmRequests, []ConfirmProjectUnregisterApprovalRequest{confirmRequest}) {
				t.Fatalf("authority approval requests = %#v / %#v", prepareRequests, confirmRequests)
			}
			callers := authority.recordedCallers()
			if len(callers) != 2 {
				t.Fatalf("authority callers = %d, want 2", len(callers))
			}
			for _, caller := range callers {
				if caller.Transport != testClientPeer || caller.Transport.UserID != "501" || caller.Session.Role != role {
					t.Fatalf("approval caller = %#v, want authenticated %s caller", caller, role)
				}
				if !containsCapability(caller.Session.Capabilities, CapabilityProjectUnregisterApprovalV1) {
					t.Fatalf("approval caller capabilities = %v", caller.Session.Capabilities)
				}
			}
		})
	}
}

// TestDecodeProjectUnregisterApprovalRequestsRequiresExactObject proves duplicate, unknown, missing, and trailing authority is rejected.
func TestDecodeProjectUnregisterApprovalRequestsRequiresExactObject(t *testing.T) {
	valid := `{"operation_id":"operation-remove","expected_operation_revision":41}`
	prepare, err := decodePrepareProjectUnregisterApprovalRequest([]byte(valid))
	if err != nil {
		t.Fatalf("decodePrepareProjectUnregisterApprovalRequest() error = %v", err)
	}
	confirm, err := decodeConfirmProjectUnregisterApprovalRequest([]byte(valid))
	if err != nil {
		t.Fatalf("decodeConfirmProjectUnregisterApprovalRequest() error = %v", err)
	}
	if prepare.OperationID != confirm.OperationID || prepare.ExpectedOperationRevision != confirm.ExpectedOperationRevision {
		t.Fatalf("decoded selections differ = %#v / %#v", prepare, confirm)
	}

	tooLarge := "{" + strings.Repeat(" ", maximumProjectUnregisterApprovalRequestBytes) + "}"
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "missing"},
		{name: "null", payload: `null`},
		{name: "array", payload: `[]`},
		{name: "empty object", payload: `{}`},
		{name: "missing operation", payload: `{"expected_operation_revision":41}`},
		{name: "missing revision", payload: `{"operation_id":"operation-remove"}`},
		{name: "unknown", payload: `{"operation_id":"operation-remove","expected_operation_revision":41,"force":true}`},
		{name: "duplicate operation", payload: `{"operation_id":"operation-remove","operation_id":"operation-other","expected_operation_revision":41}`},
		{name: "duplicate revision", payload: `{"operation_id":"operation-remove","expected_operation_revision":41,"expected_operation_revision":42}`},
		{name: "operation type", payload: `{"operation_id":7,"expected_operation_revision":41}`},
		{name: "revision type", payload: `{"operation_id":"operation-remove","expected_operation_revision":"41"}`},
		{name: "invalid operation", payload: `{"operation_id":" bad ","expected_operation_revision":41}`},
		{name: "zero revision", payload: `{"operation_id":"operation-remove","expected_operation_revision":0}`},
		{name: "negative revision", payload: `{"operation_id":"operation-remove","expected_operation_revision":-1}`},
		{name: "unterminated", payload: `{"operation_id":"operation-remove","expected_operation_revision":41`},
		{name: "concatenated", payload: valid + `{}`},
		{name: "trailing malformed", payload: valid + ` x`},
		{name: "too large", payload: tooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodePrepareProjectUnregisterApprovalRequest([]byte(test.payload)); err == nil {
				t.Fatal("prepare decoder accepted invalid payload")
			}
			if _, err := decodeConfirmProjectUnregisterApprovalRequest([]byte(test.payload)); err == nil {
				t.Fatal("confirm decoder accepted invalid payload")
			}
		})
	}
}

// rawApprovalRequestPayload emits deliberate duplicate or unknown JSON fields through a real session client.
type rawApprovalRequestPayload string

// MarshalJSON returns the exact test document without normalization by encoding/json.
func (payload rawApprovalRequestPayload) MarshalJSON() ([]byte, error) {
	return []byte(payload), nil
}

// TestProjectUnregisterApprovalHandlersRejectUnreviewedJSON proves strict decoding runs before daemon authority.
func TestProjectUnregisterApprovalHandlersRejectUnreviewedJSON(t *testing.T) {
	authority := &recordingAuthority{
		approvalPreparation:  validControlApprovalPreparation(),
		approvalConfirmation: validControlApprovalConfirmation(t),
	}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	payloads := []rawApprovalRequestPayload{
		`{"operation_id":"operation-remove","operation_id":"operation-other","expected_operation_revision":41}`,
		`{"operation_id":"operation-remove","expected_operation_revision":41,"force":true}`,
	}
	for _, method := range []string{methodProjectUnregisterApprovalPrepare, methodProjectUnregisterApprovalConfirm} {
		for _, payload := range payloads {
			_, err := running.client.session.Call(t.Context(), method, payload)
			var wireError rpc.WireError
			if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
				t.Fatalf("%s payload %s error = %#v, want invalid_request", method, payload, err)
			}
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid approval JSON reached authority %d times", len(callers))
	}
}

// TestProjectUnregisterApprovalRequiresNegotiatedCapability proves base control clients cannot reach either privileged workflow method.
func TestProjectUnregisterApprovalRequiresNegotiatedCapability(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	authority := &recordingAuthority{
		approvalPreparation:  validControlApprovalPreparation(),
		approvalConfirmation: validControlApprovalConfirmation(t),
	}
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
			t.Error("legacy approval server did not stop")
		}
	})

	for _, call := range []struct {
		method  string
		request any
	}{
		{method: methodProjectUnregisterApprovalPrepare, request: PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41}},
		{method: methodProjectUnregisterApprovalConfirm, request: ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41}},
	} {
		_, err := client.Call(t.Context(), call.method, call.request)
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodePermissionDenied {
			t.Fatalf("%s error = %#v, want permission_denied", call.method, err)
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("unnegotiated approval reached authority %d times", len(callers))
	}
}

// TestControlClientRejectsApprovalAgainstOlderDaemon proves capability checks precede unknown method calls.
func TestControlClientRejectsApprovalAgainstOlderDaemon(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	handlerCalled := make(chan string, 2)
	server, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   []rpc.Capability{CapabilityV1},
		Handlers: map[string]session.Handler{
			methodProjectUnregisterApprovalPrepare: func(context.Context, session.Request) (any, error) {
				handlerCalled <- methodProjectUnregisterApprovalPrepare
				return nil, nil
			},
			methodProjectUnregisterApprovalConfirm: func(context.Context, session.Request) (any, error) {
				handlerCalled <- methodProjectUnregisterApprovalConfirm
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
			t.Error("older approval daemon did not stop")
		}
	})

	_, prepareErr := client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{
		OperationID:               "operation-remove",
		ExpectedOperationRevision: 41,
	})
	_, confirmErr := client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{
		OperationID:               "operation-remove",
		ExpectedOperationRevision: 41,
	})
	for _, err := range []error{prepareErr, confirmErr} {
		if err == nil || !strings.Contains(err.Error(), "does not support project unregister approval") {
			t.Fatalf("older daemon error = %v", err)
		}
	}
	select {
	case method := <-handlerCalled:
		t.Fatalf("client called %s without negotiated approval capability", method)
	default:
	}
}

// TestControlClientRejectsMalformedApprovalResponses proves response JSON and domain invariants are checked for both methods.
func TestControlClientRejectsMalformedApprovalResponses(t *testing.T) {
	invalidPreparation := validControlApprovalPreparation()
	invalidPreparation.Ticket = nil
	invalidConfirmation := validControlApprovalConfirmation(t)
	invalidConfirmation.Revision = 0
	otherPreparation := validControlApprovalPreparation()
	otherPreparation.OperationID = "operation-other"
	otherPreparation.Ticket.OperationID = "operation-other"
	otherConfirmation := validControlApprovalConfirmation(t)
	otherConfirmation.Operation.ID = "operation-other"
	for _, test := range []struct {
		name    string
		method  string
		payload any
		call    func(*Client) error
		want    string
	}{
		{
			name: "prepare JSON", method: methodProjectUnregisterApprovalPrepare, payload: "wrong",
			call: func(client *Client) error {
				_, err := client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
				return err
			}, want: "decode project unregister approval preparation",
		},
		{
			name: "prepare value", method: methodProjectUnregisterApprovalPrepare,
			payload: projectUnregisterApprovalPreparationResponse{Preparation: invalidPreparation},
			call: func(client *Client) error {
				_, err := client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
				return err
			}, want: "validate project unregister approval preparation",
		},
		{
			name: "prepare correlation", method: methodProjectUnregisterApprovalPrepare,
			payload: projectUnregisterApprovalPreparationResponse{Preparation: otherPreparation},
			call: func(client *Client) error {
				_, err := client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
				return err
			}, want: "requested operation revision",
		},
		{
			name: "confirm JSON", method: methodProjectUnregisterApprovalConfirm, payload: "wrong",
			call: func(client *Client) error {
				_, err := client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
				return err
			}, want: "decode project unregister approval confirmation",
		},
		{
			name: "confirm value", method: methodProjectUnregisterApprovalConfirm,
			payload: projectUnregisterApprovalConfirmationResponse{Confirmation: invalidConfirmation},
			call: func(client *Client) error {
				_, err := client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
				return err
			}, want: "validate project unregister approval confirmation",
		},
		{
			name: "confirm correlation", method: methodProjectUnregisterApprovalConfirm,
			payload: projectUnregisterApprovalConfirmationResponse{Confirmation: otherConfirmation},
			call: func(client *Client) error {
				_, err := client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
				return err
			}, want: "requested operation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTypedResponseTestClient(t, test.method, test.payload)
			if err := test.call(client); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("typed approval response error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectUnregisterApprovalHandlersRejectInvalidOutputAndRedactFailures proves daemon defects remain internal.
func TestProjectUnregisterApprovalHandlersRejectInvalidOutputAndRedactFailures(t *testing.T) {
	privatePrepare := errors.New("private signing store path failed")
	privateConfirm := errors.New("private network release evidence failed")
	authority := &recordingAuthority{
		approvalPrepareErr: privatePrepare,
		approvalConfirmErr: privateConfirm,
	}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	for _, call := range []func() error{
		func() error {
			_, err := running.client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
			return err
		},
		func() error {
			_, err := running.client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
			return err
		},
	} {
		err := call()
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
			t.Fatalf("approval authority error = %#v, want internal", err)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatalf("private approval failure crossed control boundary: %v", err)
		}
	}

	authority.approvalPrepareErr = nil
	authority.approvalConfirmErr = nil
	for _, call := range []func() error{
		func() error {
			_, err := running.client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
			return err
		},
		func() error {
			_, err := running.client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
			return err
		},
	} {
		err := call()
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
			t.Fatalf("invalid approval output error = %#v, want internal", err)
		}
	}

	otherPreparation := validControlApprovalPreparation()
	otherPreparation.OperationID = "operation-other"
	otherPreparation.Ticket.OperationID = "operation-other"
	otherConfirmation := validControlApprovalConfirmation(t)
	otherConfirmation.Operation.ID = "operation-other"
	authority.approvalPreparation = otherPreparation
	authority.approvalConfirmation = otherConfirmation
	for _, call := range []func() error{
		func() error {
			_, err := running.client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
			return err
		},
		func() error {
			_, err := running.client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-remove", ExpectedOperationRevision: 41})
			return err
		},
	} {
		err := call()
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
			t.Fatalf("uncorrelated approval output error = %#v, want internal", err)
		}
	}
}

// TestControlClientRejectsInvalidApprovalRequestsLocally proves malformed selections never reach daemon authority.
func TestControlClientRejectsInvalidApprovalRequestsLocally(t *testing.T) {
	authority := &recordingAuthority{
		approvalPreparation:  validControlApprovalPreparation(),
		approvalConfirmation: validControlApprovalConfirmation(t),
	}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	if _, err := running.client.PrepareProjectUnregisterApproval(t.Context(), PrepareProjectUnregisterApprovalRequest{}); err == nil {
		t.Fatal("PrepareProjectUnregisterApproval() accepted invalid request")
	}
	if _, err := running.client.ConfirmProjectUnregisterApproval(t.Context(), ConfirmProjectUnregisterApprovalRequest{}); err == nil {
		t.Fatal("ConfirmProjectUnregisterApproval() accepted invalid request")
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid client approval reached authority %d times", len(callers))
	}
}

// TestProjectUnregisterApprovalResponseJSONShapes fixes the reviewed result envelope field names.
func TestProjectUnregisterApprovalResponseJSONShapes(t *testing.T) {
	preparationJSON, err := json.Marshal(projectUnregisterApprovalPreparationResponse{Preparation: validControlApprovalPreparation()})
	if err != nil {
		t.Fatalf("marshal preparation response: %v", err)
	}
	if !strings.HasPrefix(string(preparationJSON), `{"preparation":`) {
		t.Fatalf("preparation JSON = %s", preparationJSON)
	}
	confirmationJSON, err := json.Marshal(projectUnregisterApprovalConfirmationResponse{Confirmation: validControlApprovalConfirmation(t)})
	if err != nil {
		t.Fatalf("marshal confirmation response: %v", err)
	}
	if !strings.HasPrefix(string(confirmationJSON), `{"confirmation":`) {
		t.Fatalf("confirmation JSON = %s", confirmationJSON)
	}
}
