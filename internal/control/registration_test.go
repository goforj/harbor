package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// controlTestRegistration returns one inert project result rooted in a valid host path.
func controlTestRegistration(t *testing.T) ProjectRegistration {
	t.Helper()
	return ProjectRegistration{
		Project: domain.ProjectSnapshot{
			ID:        "project-orders",
			Name:      "Orders API",
			Path:      filepath.Join(t.TempDir(), "orders"),
			Slug:      "orders-api",
			State:     domain.ProjectStopped,
			UpdatedAt: time.Date(2026, time.July, 18, 19, 30, 0, 0, time.UTC),
			Apps:      []domain.AppSnapshot{},
			Services:  []domain.ServiceSnapshot{},
			Resources: []domain.ResourceSnapshot{},
		},
		Revision: 43,
		Created:  true,
	}
}

// TestProjectRegistrationReplayAcceptsCurrentProjection verifies an idempotent retry can return a project that advanced after registration.
func TestProjectRegistrationReplayAcceptsCurrentProjection(t *testing.T) {
	registration := controlTestRegistration(t)
	registration.Created = false
	registration.Project.State = domain.ProjectReady
	registration.Project.Favorite = true
	registration.Project.Apps = []domain.AppSnapshot{{
		ID:       "app",
		Name:     "Orders API",
		State:    domain.EntityReady,
		Active:   true,
		Required: true,
	}}
	registration.Project.Resources = []domain.ResourceSnapshot{{
		ID:    "app",
		Name:  "Orders API",
		Kind:  "app",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
		URL:   "https://orders.test",
	}}

	if err := registration.Validate(); err != nil {
		t.Fatalf("validate replayed registration: %v", err)
	}
	client := newTypedResponseTestClient(t, methodProjectRegister, projectRegistrationResponse{Registration: registration})
	got, err := client.RegisterProject(t.Context(), RegisterProjectRequest{Path: registration.Project.Path})
	if err != nil {
		t.Fatalf("decode replayed registration: %v", err)
	}
	if !reflect.DeepEqual(got, registration) {
		t.Fatalf("registration = %#v, want %#v", got, registration)
	}
}

// TestControlClientRegistersProjectForHumanRoles verifies CLI and desktop share one daemon-authoritative mutation.
func TestControlClientRegistersProjectForHumanRoles(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			registration := controlTestRegistration(t)
			authority := &recordingAuthority{registration: registration}
			running := newRunningControlClient(t, role, authority, nil)
			request := RegisterProjectRequest{Path: registration.Project.Path}

			got, err := running.client.RegisterProject(context.Background(), request)
			if err != nil {
				t.Fatalf("register project: %v", err)
			}
			if !reflect.DeepEqual(got, registration) {
				t.Fatalf("registration = %#v, want %#v", got, registration)
			}
			authority.mu.Lock()
			requests := append([]RegisterProjectRequest(nil), authority.registrationRequests...)
			authority.mu.Unlock()
			if len(requests) != 1 || requests[0] != request {
				t.Fatalf("authority requests = %#v, want %#v", requests, request)
			}
			callers := authority.recordedCallers()
			if len(callers) != 1 || callers[0].Session.Role != role || callers[0].Transport != testClientPeer {
				t.Fatalf("authority callers = %#v, want authenticated %s caller", callers, role)
			}
		})
	}
}

// TestProjectRegistrationRequestValidationRejectsAmbiguousPaths verifies malformed paths stop before transport work.
func TestProjectRegistrationRequestValidationRejectsAmbiguousPaths(t *testing.T) {
	valid := RegisterProjectRequest{Path: t.TempDir()}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	for _, request := range []RegisterProjectRequest{
		{},
		{Path: "relative/project"},
		{Path: valid.Path + "\n"},
		{Path: strings.Repeat("a", maximumRegistrationPathBytes+1)},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("request %#v passed validation", request)
		}
	}
}

// TestDecodeProjectRegistrationRequestRejectsIgnoredOrDuplicateInput proves one path is the entire request contract.
func TestDecodeProjectRegistrationRequestRejectsIgnoredOrDuplicateInput(t *testing.T) {
	path := filepath.ToSlash(t.TempDir())
	valid := `{"path":` + quoteJSONForTest(t, path) + `}`
	request, err := decodeProjectRegistrationRequest([]byte(valid))
	if err != nil || filepath.ToSlash(request.Path) != path {
		t.Fatalf("decode valid request = %#v, %v", request, err)
	}
	for _, payload := range []string{
		`null`,
		`{}`,
		`{"path":"relative"}`,
		`{"path":` + quoteJSONForTest(t, path) + `,"force":true}`,
		`{"path":` + quoteJSONForTest(t, path) + `,"path":` + quoteJSONForTest(t, path) + `}`,
		valid + `{}`,
	} {
		if _, err := decodeProjectRegistrationRequest([]byte(payload)); err == nil {
			t.Fatalf("payload %s passed validation", payload)
		}
	}
}

// TestDecodeProjectRegistrationRequestAllowsJSONEscaping verifies a valid path cannot outgrow the server's encoded request budget.
func TestDecodeProjectRegistrationRequestAllowsJSONEscaping(t *testing.T) {
	prefix := filepath.Clean(t.TempDir()) + string(filepath.Separator)
	path := prefix + strings.Repeat("<", maximumRegistrationPathBytes-len(prefix))
	request := RegisterProjectRequest{Path: path}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate maximum path: %v", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal maximum path: %v", err)
	}
	if len(payload) <= maximumRegistrationPathBytes+256 {
		t.Fatalf("encoded request size = %d, want JSON expansion beyond the former budget", len(payload))
	}
	got, err := decodeProjectRegistrationRequest(payload)
	if err != nil {
		t.Fatalf("decode escaped maximum path: %v", err)
	}
	if got != request {
		t.Fatalf("decoded request = %#v, want %#v", got, request)
	}
}

// TestControlRegistrationRequiresNegotiatedCapability verifies old clients retain control.v1 without gaining the additive mutation.
func TestControlRegistrationRequiresNegotiatedCapability(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	authority := &recordingAuthority{registration: controlTestRegistration(t)}
	controlServer, err := newServer(ServerConfig{Authority: authority, RequestShutdown: func() {}}, testBuild)
	if err != nil {
		t.Fatalf("construct control server: %v", err)
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
		t.Fatalf("construct legacy control client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("control server did not stop")
		}
	})

	_, err = client.Call(t.Context(), methodProjectRegister, RegisterProjectRequest{Path: authority.registration.Project.Path})
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodePermissionDenied {
		t.Fatalf("registration error = %#v, want permission_denied", err)
	}
	if len(authority.registrationRequests) != 0 {
		t.Fatalf("authority registration requests = %d, want 0", len(authority.registrationRequests))
	}
}

// TestControlClientRejectsRegistrationAgainstOlderDaemon verifies additive capability absence fails before an unknown method call.
func TestControlClientRejectsRegistrationAgainstOlderDaemon(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	handlerCalled := make(chan struct{}, 1)
	server, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   []rpc.Capability{CapabilityV1},
		Handlers: map[string]session.Handler{
			methodProjectRegister: func(context.Context, session.Request) (any, error) {
				handlerCalled <- struct{}{}
				return nil, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("construct older daemon: %v", err)
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
		t.Fatalf("construct new client against older daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("older daemon did not stop")
		}
	})

	request := RegisterProjectRequest{Path: t.TempDir()}
	_, err = client.RegisterProject(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "does not support project registration") {
		t.Fatalf("registration error = %v, want upgrade guidance", err)
	}
	select {
	case <-handlerCalled:
		t.Fatal("client called project registration without negotiating its capability")
	default:
	}
}

// quoteJSONForTest serializes one path without reproducing platform escaping rules in fixtures.
func quoteJSONForTest(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal path: %v", err)
	}
	return string(encoded)
}

// TestControlClientRejectsMalformedProjectRegistrationResponses verifies peers cannot claim creation-time runtime state or invalid revisions.
func TestControlClientRejectsMalformedProjectRegistrationResponses(t *testing.T) {
	valid := controlTestRegistration(t)
	invalidRevision := valid
	invalidRevision.Revision = 0
	claimedRuntime := valid
	claimedRuntime.Project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityStopped, Required: true}}
	claimedRuntime.Project.Resources = []domain.ResourceSnapshot{{
		ID: "app", Name: "App", Kind: "app", URL: "https://orders.test",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
	}}
	for _, test := range []struct {
		name    string
		payload any
	}{
		{name: "wrong JSON shape", payload: "wrong"},
		{name: "zero revision", payload: projectRegistrationResponse{Registration: invalidRevision}},
		{name: "claimed runtime", payload: projectRegistrationResponse{Registration: claimedRuntime}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTypedResponseTestClient(t, methodProjectRegister, test.payload)
			_, err := client.RegisterProject(t.Context(), RegisterProjectRequest{Path: valid.Project.Path})
			if err == nil || (!strings.Contains(err.Error(), "decode project registration") && !strings.Contains(err.Error(), "validate project registration")) {
				t.Fatalf("registration response error = %v", err)
			}
		})
	}
}

// TestControlRegistrationConflictsUseReviewedWireCategory verifies identity details remain local while callers receive conflict.
func TestControlRegistrationConflictsUseReviewedWireCategory(t *testing.T) {
	registration := controlTestRegistration(t)
	privateCause := errors.New("conflict at private checkout path")
	authority := &recordingAuthority{registrationErr: NewProjectRegistrationConflictError(privateCause)}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)

	_, err := running.client.RegisterProject(t.Context(), RegisterProjectRequest{Path: registration.Project.Path})
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeConflict {
		t.Fatalf("registration error = %#v, want conflict", err)
	}
	if strings.Contains(err.Error(), "private checkout") {
		t.Fatalf("private conflict detail crossed control boundary: %v", err)
	}
}

// TestControlRegistrationInvalidProjectsUseReviewedWireCategory verifies selected-path diagnostics stay daemon-local.
func TestControlRegistrationInvalidProjectsUseReviewedWireCategory(t *testing.T) {
	registration := controlTestRegistration(t)
	privateCause := errors.New("invalid APP_NAME in /private/checkout/.env")
	authority := &recordingAuthority{registrationErr: NewProjectRegistrationInvalidError(privateCause)}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)

	_, err := running.client.RegisterProject(t.Context(), RegisterProjectRequest{Path: registration.Project.Path})
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
		t.Fatalf("registration error = %#v, want invalid_request", err)
	}
	if strings.Contains(err.Error(), "private/checkout") {
		t.Fatalf("private discovery detail crossed control boundary: %v", err)
	}
}
