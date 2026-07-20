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
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestDecodeServiceLogsRequestRequiresAnExactBoundedObject protects service selection from hidden authority.
func TestDecodeServiceLogsRequestRequiresAnExactBoundedObject(t *testing.T) {
	want := ServiceLogsRequest{
		ProjectID:        "project-orders",
		SessionID:        "session-current",
		ServiceID:        "mysql",
		Cursor:           42,
		WaitMilliseconds: 25_000,
	}
	got, err := decodeServiceLogsRequest([]byte(
		`{"project_id":"project-orders","session_id":"session-current","service_id":"mysql","cursor":42,"wait_milliseconds":25000}`,
	))
	if err != nil || got != want {
		t.Fatalf("decodeServiceLogsRequest() = %#v, %v, want %#v", got, err, want)
	}
	for _, payload := range []string{
		`{"project_id":"project-orders","service_id":"mysql","cursor":0,"cursor":1}`,
		`{"project_id":"project-orders","service_id":"mysql","service_\u0069d":"redis","cursor":0}`,
		`{"project_id":"project-orders","service_id":"mysql","cursor":0,"history":true}`,
		`{"project_id":"project-orders","cursor":0}`,
		`{"project_id":"project-orders","service_id":"mysql"}`,
		`{"project_id":"project-orders","service_id":"mysql","cursor":1}`,
		`{"project_id":"project-orders","service_id":"mysql","cursor":0,"wait_milliseconds":25001}`,
		"{" + strings.Repeat(" ", maximumServiceLogsRequestBytes) + "}",
	} {
		if _, err := decodeServiceLogsRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeServiceLogsRequest(%q) error = nil", payload)
		}
	}
}

// rawServiceLogsPayload emits an adversarial request without encoding normalization.
type rawServiceLogsPayload string

// MarshalJSON returns the exact service selection document used by strict-decoding tests.
func (payload rawServiceLogsPayload) MarshalJSON() ([]byte, error) {
	return []byte(payload), nil
}

// TestServiceLogsHandlerRejectsDuplicateFields verifies strict decoding runs before daemon authority.
func TestServiceLogsHandlerRejectsDuplicateFields(t *testing.T) {
	authority := &recordingAuthority{serviceLogs: serviceLogsTestValue("mysql ready\n")}
	running := newRunningControlClient(t, rpc.RoleDesktop, authority, nil)
	_, err := running.client.session.Call(t.Context(), methodServiceLogs, rawServiceLogsPayload(
		`{"project_id":"project-orders","service_id":"mysql","cursor":0,"cursor":1}`,
	))
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
		t.Fatalf("service logs error = %#v, want invalid_request", err)
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid service logs reached authority %d times", len(callers))
	}
}

// TestBoundServiceLogsResponseAccountsForJSONEscaping preserves an exact continuation cursor within the wire budget.
func TestBoundServiceLogsResponseAccountsForJSONEscaping(t *testing.T) {
	text := strings.Repeat("\"\\\n\t", maximumServiceLogOutputBytes/4)
	logs := serviceLogsTestValue(text)
	bounded, err := BoundServiceLogsResponse(logs)
	if err != nil {
		t.Fatalf("BoundServiceLogsResponse() error = %v", err)
	}
	payload, err := json.Marshal(serviceLogsResponse{Logs: bounded})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(payload) > MaximumServiceLogsResponseBytes {
		t.Fatalf("response bytes = %d, want <= %d", len(payload), MaximumServiceLogsResponseBytes)
	}
	if bounded.Output.Text == text || !strings.HasPrefix(text, bounded.Output.Text) || !bounded.Output.HasMore {
		t.Fatalf("bounded service logs = %#v", bounded)
	}
	if bounded.Output.NextCursor != uint64(len(bounded.Output.Text)) {
		t.Fatalf("next cursor = %d, want %d", bounded.Output.NextCursor, len(bounded.Output.Text))
	}
}

// TestControlClientRoundTripsServiceLogsForHumanRoles verifies both clients receive only the selected service stream.
func TestControlClientRoundTripsServiceLogsForHumanRoles(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			want := serviceLogsTestValue("mysql ready\n")
			authority := &recordingAuthority{serviceLogs: want}
			running := newRunningControlClient(t, role, authority, nil)
			request := ServiceLogsRequest{
				ProjectID: "project-orders",
				SessionID: "session-current",
				ServiceID: "mysql",
			}
			got, err := running.client.ServiceLogs(t.Context(), request)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("ServiceLogs() = %#v, %v, want %#v", got, err, want)
			}
			authority.mu.Lock()
			requests := append([]ServiceLogsRequest(nil), authority.serviceLogsRequests...)
			authority.mu.Unlock()
			if !reflect.DeepEqual(requests, []ServiceLogsRequest{request}) {
				t.Fatalf("authority requests = %#v", requests)
			}
			callers := authority.recordedCallers()
			if len(callers) != 1 || callers[0].Session.Role != role ||
				!containsCapability(callers[0].Session.Capabilities, CapabilityServiceLogsV1) {
				t.Fatalf("authority callers = %#v", callers)
			}
		})
	}
}

// TestServiceLogsWaitRequiresNegotiatedCapability keeps additive waits away from older strict decoders.
func TestServiceLogsWaitRequiresNegotiatedCapability(t *testing.T) {
	client := newServiceLogsCapabilityTestClient(t, []rpc.Capability{CapabilityServiceLogsV1, CapabilityV1})
	immediate := ServiceLogsRequest{ProjectID: "project-orders", ServiceID: "mysql"}
	if _, err := client.ServiceLogs(t.Context(), immediate); err != nil {
		t.Fatalf("immediate ServiceLogs() error = %v", err)
	}
	wait := ServiceLogsRequest{
		ProjectID:        "project-orders",
		SessionID:        "session-current",
		ServiceID:        "mysql",
		WaitMilliseconds: 1,
	}
	if _, err := client.ServiceLogs(t.Context(), wait); err == nil || !strings.Contains(err.Error(), "live service logs") {
		t.Fatalf("ServiceLogs() error = %v, want unsupported wait capability", err)
	}
	_, err := client.session.Call(t.Context(), methodServiceLogs, wait)
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodePermissionDenied {
		t.Fatalf("raw service logs wait error = %#v, want permission_denied", err)
	}
}

// TestUnavailableServiceLogsMayResetOnlyTheirSessionCursor distinguishes reselection from transcript data.
func TestUnavailableServiceLogsMayResetOnlyTheirSessionCursor(t *testing.T) {
	logs := ServiceLogs{
		ProjectID: "project-orders",
		ServiceID: "mysql",
		SessionID: "session-current",
		Supported: true,
		Output:    ServiceLogOutputChunk{Reset: true},
	}
	if err := logs.Validate(); err != nil {
		t.Fatalf("reset-only unavailable ServiceLogs.Validate() error = %v", err)
	}
	logs.Output.Text = "hidden"
	if err := logs.Validate(); err == nil {
		t.Fatal("unavailable service logs accepted transcript data")
	}
}

// newServiceLogsCapabilityTestClient negotiates only the supplied log capabilities against a real control server.
func newServiceLogsCapabilityTestClient(t *testing.T, clientCapabilities []rpc.Capability) *Client {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	controlServer, err := newServer(ServerConfig{
		Authority:       &recordingAuthority{serviceLogs: serviceLogsTestValue("ready\n")},
		RequestShutdown: func() {},
	}, testBuild)
	if err != nil {
		t.Fatalf("newServer() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(context.Background(), &testLocalConn{Conn: serverStream, peer: testClientPeer})
	}()
	clientSession, err := session.NewClient(context.Background(), &testLocalConn{Conn: clientStream, peer: testDaemonPeer}, session.ClientConfig{
		Role: rpc.RoleDesktop, ClientVersion: testBuild.Version, ProtocolRanges: protocolRanges(), Capabilities: clientCapabilities,
	})
	if err != nil {
		t.Fatalf("session.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("service logs server did not stop")
		}
	})
	return &Client{session: clientSession, peer: DaemonPeer{Session: clientSession.Peer()}}
}

// serviceLogsTestValue returns one complete current-session service output response.
func serviceLogsTestValue(text string) ServiceLogs {
	return ServiceLogs{
		ProjectID: "project-orders",
		ServiceID: "mysql",
		SessionID: "session-current",
		Supported: true,
		Available: true,
		Output: ServiceLogOutputChunk{
			Available:  true,
			NextCursor: uint64(len(text)),
			Text:       text,
		},
	}
}
