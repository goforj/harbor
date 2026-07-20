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
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestDecodeProjectActivityRequestRequiresAnExactBoundedObject protects the pull API from hidden selection authority.
func TestDecodeProjectActivityRequestRequiresAnExactBoundedObject(t *testing.T) {
	want := ProjectActivityRequest{ProjectID: "project-orders", SessionID: "session-current", Cursor: 42}
	got, err := decodeProjectActivityRequest([]byte(`{"project_id":"project-orders","session_id":"session-current","cursor":42}`))
	if err != nil || got != want {
		t.Fatalf("decodeProjectActivityRequest() = %#v, %v, want %#v", got, err, want)
	}
	for _, payload := range []string{
		`{"project_id":"project-orders","cursor":0,"cursor":1}`,
		`{"project_id":"project-orders","cursor":0,"history":true}`,
		`{"project_id":"project-orders"}`,
		`{"project_id":"project-orders","cursor":1}`,
		"{" + strings.Repeat(" ", maximumProjectActivityRequestBytes) + "}",
	} {
		if _, err := decodeProjectActivityRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeProjectActivityRequest(%q) error = nil", payload)
		}
	}
}

// rawProjectActivityPayload emits an adversarial request without encoding normalization.
type rawProjectActivityPayload string

// MarshalJSON returns the exact document needed to exercise duplicate-field handling.
func (payload rawProjectActivityPayload) MarshalJSON() ([]byte, error) {
	return []byte(payload), nil
}

// TestProjectActivityHandlerRejectsUnreviewedJSON verifies strict decoding runs before daemon authority.
func TestProjectActivityHandlerRejectsUnreviewedJSON(t *testing.T) {
	authority := &recordingAuthority{projectActivity: projectActivityTestValue("ready")}
	running := newRunningControlClient(t, rpc.RoleDesktop, authority, nil)
	_, err := running.client.session.Call(t.Context(), methodProjectActivity, rawProjectActivityPayload(
		`{"project_id":"project-orders","cursor":0,"cursor":1}`,
	))
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
		t.Fatalf("project activity error = %#v, want invalid_request", err)
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid project activity reached authority %d times", len(callers))
	}
}

// TestBoundProjectActivityResponseAccountsForJSONEscaping verifies the complete response remains bounded with an exact continuation cursor.
func TestBoundProjectActivityResponseAccountsForJSONEscaping(t *testing.T) {
	text := strings.Repeat("\"\\\n\t", maximumProjectOutputChunkBytes/4)
	activity := projectActivityTestValue(text)
	bounded, err := BoundProjectActivityResponse(activity)
	if err != nil {
		t.Fatalf("BoundProjectActivityResponse() error = %v", err)
	}
	payload, err := json.Marshal(projectActivityResponse{Activity: bounded})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if len(payload) > MaximumProjectActivityResponseBytes {
		t.Fatalf("response bytes = %d, want <= %d", len(payload), MaximumProjectActivityResponseBytes)
	}
	if bounded.Session == nil || bounded.Session.Output.Text == text ||
		!strings.HasPrefix(text, bounded.Session.Output.Text) || !bounded.Session.Output.HasMore {
		t.Fatalf("bounded activity = %#v", bounded)
	}
	if bounded.Session.Output.NextCursor != uint64(len(bounded.Session.Output.Text)) {
		t.Fatalf("next cursor = %d, want %d", bounded.Session.Output.NextCursor, len(bounded.Session.Output.Text))
	}
}

// TestControlClientRoundTripsProjectActivityForHumanRoles verifies negotiated callers receive only current bounded activity.
func TestControlClientRoundTripsProjectActivityForHumanRoles(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			want := projectActivityTestValue("server ready\n")
			authority := &recordingAuthority{projectActivity: want}
			running := newRunningControlClient(t, role, authority, nil)
			request := ProjectActivityRequest{ProjectID: "project-orders", SessionID: "session-current", Cursor: 0}
			got, err := running.client.ProjectActivity(t.Context(), request)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("ProjectActivity() = %#v, %v, want %#v", got, err, want)
			}
			authority.mu.Lock()
			requests := append([]ProjectActivityRequest(nil), authority.projectActivityRequests...)
			authority.mu.Unlock()
			if !reflect.DeepEqual(requests, []ProjectActivityRequest{request}) {
				t.Fatalf("authority requests = %#v", requests)
			}
			callers := authority.recordedCallers()
			if len(callers) != 1 || callers[0].Session.Role != role ||
				!containsCapability(callers[0].Session.Capabilities, CapabilityProjectActivityV1) {
				t.Fatalf("authority callers = %#v", callers)
			}
		})
	}
}

// TestProjectActivityRequiresNegotiatedCapability proves a base control session cannot reach current process output.
func TestProjectActivityRequiresNegotiatedCapability(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	authority := &recordingAuthority{}
	controlServer, err := newServer(ServerConfig{Authority: authority, RequestShutdown: func() {}}, testBuild)
	if err != nil {
		t.Fatalf("newServer() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(context.Background(), &testLocalConn{Conn: serverStream, peer: testClientPeer})
	}()
	client, err := session.NewClient(context.Background(), &testLocalConn{Conn: clientStream, peer: testDaemonPeer}, session.ClientConfig{
		Role: rpc.RoleCLI, ClientVersion: testBuild.Version, ProtocolRanges: protocolRanges(), Capabilities: []rpc.Capability{CapabilityV1},
	})
	if err != nil {
		t.Fatalf("session.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("project activity server did not stop")
		}
	})
	_, err = client.Call(t.Context(), methodProjectActivity, ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0})
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodePermissionDenied {
		t.Fatalf("project activity error = %#v, want permission_denied", err)
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("unnegotiated project activity reached authority %d times", len(callers))
	}
}

// TestControlClientRejectsContradictoryOrOversizedProjectActivity verifies daemon responses retain project and size correlation.
func TestControlClientRejectsContradictoryOrOversizedProjectActivity(t *testing.T) {
	contradictory := projectActivityTestValue("ready")
	contradictory.ProjectID = "project-other"
	changedWithoutReset := projectActivityTestValue("ready")
	changedWithoutReset.Session.ID = "session-other"
	oversized := projectActivityTestValue(strings.Repeat("\n", maximumProjectOutputChunkBytes))
	for _, test := range []struct {
		name     string
		activity ProjectActivity
		request  ProjectActivityRequest
		want     string
	}{
		{
			name: "project correlation", activity: contradictory,
			request: ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0}, want: "requested project",
		},
		{
			name: "session correlation", activity: changedWithoutReset,
			request: ProjectActivityRequest{ProjectID: "project-orders", SessionID: "session-current", Cursor: 0}, want: "without resetting",
		},
		{
			name: "encoded response size", activity: oversized,
			request: ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0}, want: "exceeds",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTypedResponseTestClient(t, methodProjectActivity, projectActivityResponse{Activity: test.activity})
			_, err := client.ProjectActivity(t.Context(), test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ProjectActivity() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProjectActivityHandlerRejectsUnboundedOrUncorrelatedAuthorityOutput verifies server defenses run before serialization.
func TestProjectActivityHandlerRejectsUnboundedOrUncorrelatedAuthorityOutput(t *testing.T) {
	changedWithoutReset := projectActivityTestValue("ready")
	changedWithoutReset.Session.ID = "session-other"
	for _, test := range []struct {
		name     string
		activity ProjectActivity
		request  ProjectActivityRequest
	}{
		{
			name: "session correlation", activity: changedWithoutReset,
			request: ProjectActivityRequest{ProjectID: "project-orders", SessionID: "session-current", Cursor: 0},
		},
		{
			name: "encoded response size", activity: projectActivityTestValue(strings.Repeat("\n", maximumProjectOutputChunkBytes)),
			request: ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			running := newRunningControlClient(t, rpc.RoleDesktop, &recordingAuthority{projectActivity: test.activity}, nil)
			_, err := running.client.ProjectActivity(t.Context(), test.request)
			var wireError rpc.WireError
			if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
				t.Fatalf("ProjectActivity() error = %#v, want internal", err)
			}
		})
	}
}

// TestProjectActivityJSONOmitsProcessOwnership verifies the wire shape cannot carry paths or process identifiers.
func TestProjectActivityJSONOmitsProcessOwnership(t *testing.T) {
	payload, err := json.Marshal(projectActivityResponse{Activity: projectActivityTestValue("ready")})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, forbidden := range []string{`"pid"`, `"path"`, `"process"`, `"history"`} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("project activity JSON contains %s: %s", forbidden, payload)
		}
	}
}

// projectActivityTestValue returns one complete current-session activity response.
func projectActivityTestValue(text string) ProjectActivity {
	return ProjectActivity{
		ProjectID: "project-orders",
		Session: &ProjectSessionActivity{
			ID:         "session-current",
			State:      domain.SessionAttached,
			Generation: 3,
			Output: ProjectOutputChunk{
				Available:  true,
				NextCursor: uint64(len(text)),
				Text:       text,
			},
		},
	}
}
