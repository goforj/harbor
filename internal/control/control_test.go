package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

var (
	testClientPeer = local.PeerIdentity{UserID: "501", ProcessID: 1201}
	testDaemonPeer = local.PeerIdentity{UserID: "501", ProcessID: 1202}
	testBuild      = buildinfo.Info{Version: "v1.2.3+ipc", Revision: "abc123", Modified: true}
)

// testLocalConn attaches a deterministic operating-system identity to an in-memory stream.
type testLocalConn struct {
	net.Conn
	peer local.PeerIdentity
}

// Peer returns the identity authenticated for this test endpoint.
func (connection *testLocalConn) Peer() local.PeerIdentity {
	return connection.peer
}

// recordingAuthority records application-boundary caller identities and returns configured results.
type recordingAuthority struct {
	mu                   sync.Mutex
	status               DaemonStatus
	snapshot             domain.Snapshot
	registration         ProjectRegistration
	statusErr            error
	snapshotErr          error
	registrationErr      error
	callers              []Caller
	registrationRequests []RegisterProjectRequest
}

// RegisterProject records the authenticated caller and request before returning the configured registration.
func (authority *recordingAuthority) RegisterProject(
	ctx context.Context,
	caller Caller,
	request RegisterProjectRequest,
) (ProjectRegistration, error) {
	if err := normalizeContext(ctx).Err(); err != nil {
		return ProjectRegistration{}, err
	}
	authority.mu.Lock()
	authority.callers = append(authority.callers, caller)
	authority.registrationRequests = append(authority.registrationRequests, request)
	authority.mu.Unlock()
	return authority.registration, authority.registrationErr
}

// Status records the authenticated caller before returning the configured diagnostic.
func (authority *recordingAuthority) Status(ctx context.Context, caller Caller) (DaemonStatus, error) {
	if err := normalizeContext(ctx).Err(); err != nil {
		return DaemonStatus{}, err
	}
	authority.mu.Lock()
	authority.callers = append(authority.callers, caller)
	authority.mu.Unlock()

	return authority.status, authority.statusErr
}

// Snapshot records the authenticated caller before returning the configured replacement state.
func (authority *recordingAuthority) Snapshot(ctx context.Context, caller Caller) (domain.Snapshot, error) {
	if err := normalizeContext(ctx).Err(); err != nil {
		return domain.Snapshot{}, err
	}
	authority.mu.Lock()
	authority.callers = append(authority.callers, caller)
	authority.mu.Unlock()

	return authority.snapshot, authority.snapshotErr
}

// recordedCallers returns a copy that can be inspected without racing request handlers.
func (authority *recordingAuthority) recordedCallers() []Caller {
	authority.mu.Lock()
	defer authority.mu.Unlock()

	return append([]Caller(nil), authority.callers...)
}

// runningControlClient owns one in-memory daemon session and its deterministic shutdown.
type runningControlClient struct {
	client     *Client
	cancel     context.CancelFunc
	serverDone <-chan error
}

// close terminates both endpoints and waits for daemon dispatch to drain.
func (running runningControlClient) close(t *testing.T) {
	t.Helper()
	if err := running.client.Close(); err != nil {
		t.Errorf("close control client: %v", err)
	}
	running.cancel()
	select {
	case err := <-running.serverDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("control server stopped with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("control server did not stop")
	}
}

// newRunningControlClient negotiates a real framed session over an authenticated in-memory transport.
func newRunningControlClient(
	t *testing.T,
	role rpc.Role,
	authority Authority,
	observer ErrorObserver,
) runningControlClient {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	controlServer, err := newServer(ServerConfig{Authority: authority, ObserveError: observer}, testBuild)
	if err != nil {
		t.Fatalf("construct control server: %v", err)
	}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(serverContext, serverConnection)
	}()

	controlClient, err := newClient(context.Background(), ClientConfig{
		Role: role,
		Dial: func(context.Context) (local.Conn, error) {
			return clientConnection, nil
		},
	}, testBuild)
	if err != nil {
		cancelServer()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatalf("construct control client: %v", err)
	}
	running := runningControlClient{client: controlClient, cancel: cancelServer, serverDone: serverDone}
	t.Cleanup(func() { running.close(t) })

	return running
}

// testStatus returns the one reviewed status shape used by end-to-end tests.
func testStatus() DaemonStatus {
	return DaemonStatus{
		State:                 DaemonStateReady,
		Build:                 buildFromInfo(testBuild),
		Protocol:              protocolV1,
		Capabilities:          capabilities(),
		SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:              42,
	}
}

// testSnapshot returns a canonical empty-project snapshot for end-to-end tests.
func testSnapshot() domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion:     domain.SnapshotSchemaVersion,
		Sequence:          42,
		CapturedAt:        time.Date(2026, time.July, 18, 8, 30, 0, 0, time.UTC),
		Projects:          []domain.ProjectSnapshot{},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{},
	}
}

// TestControlClientReadsStatusAndSnapshot verifies both human-facing roles use the same typed API.
func TestControlClientReadsStatusAndSnapshot(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			authority := &recordingAuthority{status: testStatus(), snapshot: testSnapshot()}
			running := newRunningControlClient(t, role, authority, nil)

			status, err := running.client.Status(context.Background())
			if err != nil {
				t.Fatalf("read status: %v", err)
			}
			if !reflect.DeepEqual(status, testStatus()) {
				t.Fatalf("status = %#v, want %#v", status, testStatus())
			}
			snapshot, err := running.client.Snapshot(nil)
			if err != nil {
				t.Fatalf("read snapshot: %v", err)
			}
			if !reflect.DeepEqual(snapshot, testSnapshot()) {
				t.Fatalf("snapshot = %#v, want %#v", snapshot, testSnapshot())
			}

			callers := authority.recordedCallers()
			if len(callers) != 2 {
				t.Fatalf("authority callers = %d, want 2", len(callers))
			}
			for _, caller := range callers {
				if caller.Transport != testClientPeer || caller.Session.Role != role || caller.Session.BuildVersion != testBuild.Version {
					t.Fatalf("authority caller = %#v, want client OS identity and %s role", caller, role)
				}
				if !containsCapability(caller.Session.Capabilities, CapabilityV1) {
					t.Fatalf("authority caller capabilities = %v, want control.v1", caller.Session.Capabilities)
				}
			}

			peer := running.client.Peer()
			if peer.Transport != testDaemonPeer || peer.Session.Role != rpc.RoleDaemon || peer.Session.BuildVersion != testBuild.Version {
				t.Fatalf("daemon peer = %#v, want authenticated test daemon", peer)
			}
			peer.Session.Capabilities[0] = "mutated"
			if !containsCapability(running.client.Peer().Session.Capabilities, CapabilityV1) {
				t.Fatal("mutating returned daemon peer changed client state")
			}
		})
	}
}

// TestControlResponseJSONShapes verifies standalone status and snapshot objects retain reviewed field names.
func TestControlResponseJSONShapes(t *testing.T) {
	statusJSON, err := json.Marshal(statusResponse{Status: testStatus()})
	if err != nil {
		t.Fatalf("marshal status response: %v", err)
	}
	wantStatus := `{"status":{"state":"ready","build":{"version":"v1.2.3+ipc","revision":"abc123","modified":true},"protocol":{"major":1,"minor":0},"capabilities":["control.project-registration.v1","control.v1"],"snapshot_schema_version":1,"sequence":42}}`
	if string(statusJSON) != wantStatus {
		t.Fatalf("status JSON = %s, want %s", statusJSON, wantStatus)
	}

	snapshotJSON, err := json.Marshal(snapshotResponse{Snapshot: testSnapshot()})
	if err != nil {
		t.Fatalf("marshal snapshot response: %v", err)
	}
	wantSnapshot := `{"snapshot":{"schema_version":1,"sequence":42,"captured_at":"2026-07-18T08:30:00Z","projects":[],"operations":[],"recent_resource_ids":[]}}`
	if string(snapshotJSON) != wantSnapshot {
		t.Fatalf("snapshot JSON = %s, want %s", snapshotJSON, wantSnapshot)
	}
}

// TestControlRejectsUnreviewedRequestFields verifies ignored input cannot hide inside no-argument methods.
func TestControlRejectsUnreviewedRequestFields(t *testing.T) {
	authority := &recordingAuthority{status: testStatus(), snapshot: testSnapshot()}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)

	for _, payload := range []any{map[string]bool{"force": true}, nilRequestPayload{}} {
		_, err := running.client.session.Call(context.Background(), methodDaemonStatus, payload)
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
			t.Fatalf("unreviewed request error = %#v, want invalid_request", err)
		}
	}
	if _, err := running.client.Status(context.Background()); err != nil {
		t.Fatalf("status after rejected request: %v", err)
	}
	if callers := authority.recordedCallers(); len(callers) != 1 {
		t.Fatalf("authority calls = %d, want only the valid request", len(callers))
	}
}

// nilRequestPayload serializes to null so tests can distinguish it from an empty request object.
type nilRequestPayload struct{}

// MarshalJSON emits the JSON null value rejected by the no-argument control schema.
func (nilRequestPayload) MarshalJSON() ([]byte, error) {
	return []byte("null"), nil
}

// TestControlClientExposesTerminalConnectionState verifies desktop reconnect loops can observe daemon loss.
func TestControlClientExposesTerminalConnectionState(t *testing.T) {
	running := newRunningControlClient(t, rpc.RoleDesktop, &recordingAuthority{status: testStatus()}, nil)
	if err := running.client.Err(); err != nil {
		t.Fatalf("connected client error = %v, want nil", err)
	}
	if err := running.client.Close(); err != nil {
		t.Fatalf("close control client: %v", err)
	}
	select {
	case <-running.client.Done():
	case <-time.After(time.Second):
		t.Fatal("client Done did not close")
	}
	if !errors.Is(running.client.Err(), session.ErrClosed) {
		t.Fatalf("terminal client error = %v, want session closed", running.client.Err())
	}
}

// TestControlRedactsAuthorityFailures verifies durable diagnostics remain daemon-local and the session stays usable.
func TestControlRedactsAuthorityFailures(t *testing.T) {
	secretFailure := errors.New("database failed near secret-path")
	authority := &recordingAuthority{statusErr: secretFailure, snapshot: testSnapshot()}
	observed := make(chan error, 1)
	running := newRunningControlClient(t, rpc.RoleDesktop, authority, func(caller Caller, method string, err error) {
		if caller.Transport != testClientPeer || method != methodDaemonStatus {
			return
		}
		observed <- err
	})

	_, err := running.client.Status(context.Background())
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("authority wire error = %#v, want internal", err)
	}
	if strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("authority cause crossed the wire: %v", err)
	}
	select {
	case diagnostic := <-observed:
		if !errors.Is(diagnostic, secretFailure) {
			t.Fatalf("observed error = %v, want local authority cause", diagnostic)
		}
	case <-time.After(time.Second):
		t.Fatal("authority failure was not observed locally")
	}
	if _, err := running.client.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot after authority failure: %v", err)
	}
}

// TestControlServerRejectsRolesAndMissingCapability verifies negotiation fails before product dispatch.
func TestControlServerRejectsRolesAndMissingCapability(t *testing.T) {
	tests := []struct {
		name         string
		role         rpc.Role
		capabilities []rpc.Capability
	}{
		{name: "GoForj session", role: rpc.RoleGoForjSession, capabilities: capabilities()},
		{name: "missing capability", role: rpc.RoleCLI},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientStream, serverStream := net.Pipe()
			serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
			controlServer, err := newServer(ServerConfig{Authority: &recordingAuthority{}}, testBuild)
			if err != nil {
				t.Fatalf("construct control server: %v", err)
			}
			serverDone := make(chan error, 1)
			go func() { serverDone <- controlServer.Serve(context.Background(), serverConnection) }()

			_, err = session.NewClient(context.Background(), clientStream, session.ClientConfig{
				Role:           test.role,
				ClientVersion:  testBuild.Version,
				ProtocolRanges: protocolRanges(),
				Capabilities:   test.capabilities,
			})
			var handshakeError *session.HandshakeError
			if !errors.As(err, &handshakeError) || handshakeError.Failure.Code != rpc.ErrorCodePermissionDenied {
				t.Fatalf("handshake error = %#v, want permission_denied", err)
			}
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("rejected control server did not stop")
			}
		})
	}
}

// TestControlClientRejectsInvalidConfigurationBeforeDial verifies product roles fail closed without transport work.
func TestControlClientRejectsInvalidConfigurationBeforeDial(t *testing.T) {
	dialed := false
	_, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleGoForjSession,
		Dial: func(context.Context) (local.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		},
	}, testBuild)
	if err == nil || dialed {
		t.Fatalf("invalid role error = %v, dialed = %t", err, dialed)
	}

	_, err = newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) { return nil, nil },
	}, testBuild)
	if err == nil || !strings.Contains(err.Error(), "nil connection") {
		t.Fatalf("nil dial result error = %v, want explicit failure", err)
	}
}

// TestControlServerRejectsInvalidTransportIdentity verifies application bytes are not read from unauthenticated shapes.
func TestControlServerRejectsInvalidTransportIdentity(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	controlServer, err := newServer(ServerConfig{Authority: &recordingAuthority{}}, testBuild)
	if err != nil {
		t.Fatalf("construct control server: %v", err)
	}

	err = controlServer.Serve(context.Background(), &testLocalConn{Conn: serverStream})
	if err == nil || !strings.Contains(err.Error(), "transport peer") {
		t.Fatalf("transport identity error = %v, want rejection", err)
	}
}

// TestCurrentBuildNegotiatesWithControlPolicy verifies development build metadata is valid in Hello and Welcome.
func TestCurrentBuildNegotiatesWithControlPolicy(t *testing.T) {
	current := buildinfo.Current()
	if err := validateBuild(buildFromInfo(current)); err != nil {
		t.Fatalf("current build status metadata: %v", err)
	}
	hello := rpc.Hello{
		ProtocolRanges: protocolRanges(),
		Role:           rpc.RoleCLI,
		ClientVersion:  current.Version,
		Capabilities:   capabilities(),
	}
	if _, err := rpc.NewHelloEnvelope(hello); err != nil {
		t.Fatalf("current build Hello: %v", err)
	}
	welcome, rejection := rpc.NegotiateHello(hello, current.Version, protocolRanges(), capabilities())
	if rejection != nil {
		t.Fatalf("current build negotiation rejected: %#v", rejection)
	}
	if _, err := rpc.NewWelcomeEnvelope(welcome); err != nil {
		t.Fatalf("current build Welcome: %v", err)
	}
}

// TestNewServerRejectsMissingAuthority verifies required daemon wiring fails before accepting a connection.
func TestNewServerRejectsMissingAuthority(t *testing.T) {
	if _, err := NewServer(ServerConfig{}); err == nil {
		t.Fatal("NewServer accepted a missing authority")
	}
	var typedNil *recordingAuthority
	if _, err := NewServer(ServerConfig{Authority: typedNil}); err == nil {
		t.Fatal("NewServer accepted a typed nil authority")
	}
}
