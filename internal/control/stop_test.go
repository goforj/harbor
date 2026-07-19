package control

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// rawStopSession owns one deliberately low-level client so tests can hold an acknowledged connection open.
type rawStopSession struct {
	client     *session.Client
	cancel     context.CancelFunc
	serverDone <-chan error
	finishOnce sync.Once
	serveErr   error
}

// finish closes the client and returns the control server's retained connection result once.
func (running *rawStopSession) finish(t *testing.T) error {
	t.Helper()
	running.finishOnce.Do(func() {
		if err := running.client.Close(); err != nil {
			running.serveErr = errors.Join(running.serveErr, err)
		}
		select {
		case err := <-running.serverDone:
			running.serveErr = errors.Join(running.serveErr, err)
		case <-time.After(2 * time.Second):
			running.cancel()
			running.serveErr = errors.Join(running.serveErr, errors.New("control server did not stop"))
		}
		running.cancel()
	})

	return running.serveErr
}

// configuredStopClient owns a typed client connected to a server with a test-selected acknowledgement payload.
type configuredStopClient struct {
	client     *Client
	cancel     context.CancelFunc
	serverDone <-chan error
	finishOnce sync.Once
}

// finish closes a configured typed fixture without treating its deliberately invalid response as a server failure.
func (running *configuredStopClient) finish(t *testing.T) {
	t.Helper()
	running.finishOnce.Do(func() {
		_ = running.client.Close()
		select {
		case <-running.serverDone:
		case <-time.After(2 * time.Second):
			running.cancel()
			t.Error("configured stop server did not stop")
		}
		running.cancel()
	})
}

// armedStopWriteConnection fails response writes only after protocol negotiation has completed.
type armedStopWriteConnection struct {
	net.Conn
	armed     atomic.Bool
	attempted chan struct{}
	failure   error
	once      sync.Once
}

// arm moves the connection past its successful handshake writes and onto the tested response boundary.
func (connection *armedStopWriteConnection) arm() {
	connection.armed.Store(true)
}

// Write rejects the first post-handshake response so acceptance cannot be published for undelivered output.
func (connection *armedStopWriteConnection) Write(payload []byte) (int, error) {
	if !connection.armed.Load() {
		return connection.Conn.Write(payload)
	}
	connection.once.Do(func() { close(connection.attempted) })
	return 0, connection.failure
}

// terminalStopReadConnection replaces peer closure with one observable server-side transport failure.
type terminalStopReadConnection struct {
	net.Conn
	failure error
}

// Read retains a selected connection failure after the acknowledged client disconnects.
func (connection *terminalStopReadConnection) Read(payload []byte) (int, error) {
	read, err := connection.Conn.Read(payload)
	if err != nil {
		return read, connection.failure
	}
	return read, nil
}

// newRawStopSession starts one real control server around a client-selected capability set and transport wrapper.
func newRawStopSession(
	t *testing.T,
	role rpc.Role,
	clientCapabilities []rpc.Capability,
	requestShutdown func(),
	observer ErrorObserver,
	wrap func(net.Conn) net.Conn,
) *rawStopSession {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	if wrap != nil {
		serverStream = wrap(serverStream)
	}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	controlServer, err := newServer(ServerConfig{
		Authority:       &recordingAuthority{},
		RequestShutdown: requestShutdown,
		ObserveError:    observer,
	}, testBuild)
	if err != nil {
		t.Fatalf("construct raw stop server: %v", err)
	}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(serverContext, serverConnection)
	}()

	rawClient, err := session.NewClient(context.Background(), clientStream, session.ClientConfig{
		Role:           role,
		ClientVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   clientCapabilities,
	})
	if err != nil {
		cancelServer()
		_ = clientStream.Close()
		_ = serverStream.Close()
		t.Fatalf("construct raw stop client: %v", err)
	}
	running := &rawStopSession{client: rawClient, cancel: cancelServer, serverDone: serverDone}
	t.Cleanup(func() { _ = running.finish(t) })
	return running
}

// newConfiguredStopClient starts a typed client against one deliberately malformed synthetic daemon acknowledgement.
func newConfiguredStopClient(t *testing.T, response any) *configuredStopClient {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	controlServer, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   capabilities(),
		Authorize:      authorizeControlHello,
		Handlers: map[string]session.Handler{
			methodDaemonStop: func(context.Context, session.Request) (any, error) {
				return response, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("construct synthetic stop server: %v", err)
	}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(serverContext, serverConnection)
	}()

	client, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) {
			return clientConnection, nil
		},
	}, testBuild)
	if err != nil {
		cancelServer()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatalf("construct configured stop client: %v", err)
	}
	running := &configuredStopClient{client: client, cancel: cancelServer, serverDone: serverDone}
	t.Cleanup(func() { running.finish(t) })
	return running
}

// requireStopPublished waits for lifecycle publication without using timing to establish ordering.
func requireStopPublished(t *testing.T, requested <-chan struct{}) {
	t.Helper()
	select {
	case <-requested:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for daemon shutdown publication")
	}
}

// requireStopPending verifies no rejected or still-held connection has published lifecycle shutdown.
func requireStopPending(t *testing.T, requested <-chan struct{}) {
	t.Helper()
	select {
	case <-requested:
		t.Fatal("daemon shutdown was published before the accepted connection ended")
	default:
	}
}

// requireStopWireCode verifies a rejected product request retains its reviewed wire category.
func requireStopWireCode(t *testing.T, err error, code rpc.ErrorCode) {
	t.Helper()
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != code {
		t.Fatalf("stop error = %#v, want %s", err, code)
	}
}

// TestControlClientStopClosesBeforePublishing verifies every supported human client consumes the acknowledgement first.
func TestControlClientStopClosesBeforePublishing(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			shutdown := daemon.NewShutdown()
			published := make(chan error, 1)
			var client *Client
			running := newRunningControlClientWithShutdown(
				t,
				role,
				&recordingAuthority{},
				nil,
				func() {
					select {
					case <-client.Done():
						published <- nil
					default:
						published <- errors.New("shutdown published before the client session closed")
					}
					shutdown.Request()
				},
			)
			client = running.client

			if err := client.Stop(t.Context()); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			select {
			case <-client.Done():
			default:
				t.Fatal("Stop() returned before closing the acknowledged client session")
			}
			requireStopPublished(t, shutdown.Requested())
			if err := <-published; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestControlStopWaitsForAcceptedConnectionEnd verifies a raw caller cannot trigger teardown before consuming and closing.
func TestControlStopWaitsForAcceptedConnectionEnd(t *testing.T) {
	shutdown := daemon.NewShutdown()
	running := newRawStopSession(t, rpc.RoleCLI, capabilities(), shutdown.Request, nil, nil)

	payload, err := running.client.Call(t.Context(), methodDaemonStop, struct{}{})
	if err != nil {
		t.Fatalf("raw stop call: %v", err)
	}
	if string(payload) != `{"stopping":true}` {
		t.Fatalf("raw stop response = %s, want exact acknowledgement", payload)
	}
	requireStopPending(t, shutdown.Requested())
	if err := running.finish(t); err != nil {
		t.Fatalf("finish accepted raw stop: %v", err)
	}
	requireStopPublished(t, shutdown.Requested())
}

// TestHeldAcceptedStopDoesNotBlockAnotherClient verifies acceptance state belongs to one serving connection.
func TestHeldAcceptedStopDoesNotBlockAnotherClient(t *testing.T) {
	shutdown := daemon.NewShutdown()
	controlServer, err := newServer(ServerConfig{
		Authority:       &recordingAuthority{},
		RequestShutdown: shutdown.Request,
	}, testBuild)
	if err != nil {
		t.Fatalf("construct shared stop server: %v", err)
	}

	heldClientStream, heldServerStream := net.Pipe()
	heldContext, cancelHeld := context.WithCancel(context.Background())
	heldDone := make(chan error, 1)
	go func() {
		heldDone <- controlServer.Serve(
			heldContext,
			&testLocalConn{Conn: heldServerStream, peer: testClientPeer},
		)
	}()
	heldClient, err := session.NewClient(context.Background(), heldClientStream, session.ClientConfig{
		Role:           rpc.RoleCLI,
		ClientVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   capabilities(),
	})
	if err != nil {
		cancelHeld()
		_ = heldClientStream.Close()
		_ = heldServerStream.Close()
		t.Fatalf("construct held stop client: %v", err)
	}
	held := &rawStopSession{client: heldClient, cancel: cancelHeld, serverDone: heldDone}
	t.Cleanup(func() { _ = held.finish(t) })
	if _, err := held.client.Call(t.Context(), methodDaemonStop, struct{}{}); err != nil {
		t.Fatalf("held raw stop call: %v", err)
	}
	requireStopPending(t, shutdown.Requested())

	officialClientStream, officialServerStream := net.Pipe()
	officialContext, cancelOfficial := context.WithCancel(context.Background())
	officialDone := make(chan error, 1)
	go func() {
		officialDone <- controlServer.Serve(
			officialContext,
			&testLocalConn{Conn: officialServerStream, peer: testClientPeer},
		)
	}()
	officialClient, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) {
			return &testLocalConn{Conn: officialClientStream, peer: testDaemonPeer}, nil
		},
	}, testBuild)
	if err != nil {
		cancelOfficial()
		_ = officialClientStream.Close()
		_ = officialServerStream.Close()
		t.Fatalf("construct official stop client: %v", err)
	}
	official := runningControlClient{
		client:     officialClient,
		cancel:     cancelOfficial,
		serverDone: officialDone,
	}
	t.Cleanup(func() { official.close(t) })
	if err := official.client.Stop(t.Context()); err != nil {
		t.Fatalf("official Stop() error = %v", err)
	}
	requireStopPublished(t, shutdown.Requested())
	select {
	case <-held.client.Done():
		t.Fatal("a second accepted connection closed the held raw session")
	default:
	}
}

// TestControlStopRejectsMissingCapabilityAndInvalidPayloads verifies rejected calls never acquire shutdown authority.
func TestControlStopRejectsMissingCapabilityAndInvalidPayloads(t *testing.T) {
	t.Run("missing capability", func(t *testing.T) {
		shutdown := daemon.NewShutdown()
		running := newRawStopSession(
			t,
			rpc.RoleCLI,
			[]rpc.Capability{CapabilityV1},
			shutdown.Request,
			nil,
			nil,
		)
		_, err := running.client.Call(t.Context(), methodDaemonStop, struct{}{})
		requireStopWireCode(t, err, rpc.ErrorCodePermissionDenied)
		if err := running.finish(t); err != nil {
			t.Fatalf("finish rejected raw stop: %v", err)
		}
		requireStopPending(t, shutdown.Requested())
	})

	t.Run("invalid payloads", func(t *testing.T) {
		accepted := make(chan struct{})
		server := &Server{}
		handler := server.stopHandler(testClientPeer, func(Caller) { close(accepted) })
		peer := session.Peer{
			Role:         rpc.RoleCLI,
			BuildVersion: testBuild.Version,
			Protocol:     protocolV1,
			Capabilities: capabilities(),
		}
		for _, payload := range [][]byte{nil, []byte("null"), []byte(`{"force":true}`), []byte("{")} {
			_, err := handler(t.Context(), session.Request{Peer: peer, Payload: payload})
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeInvalidRequest {
				t.Fatalf("payload %q error = %#v, want invalid_request", payload, err)
			}
		}
		requireStopPending(t, accepted)
	})
}

// TestControlStopSkipsPublicationWhenResponseCannotBeDelivered verifies a write failure never accepts shutdown.
func TestControlStopSkipsPublicationWhenResponseCannotBeDelivered(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		shutdown := daemon.NewShutdown()
		writeFailure := errors.New("stop response write failed")
		failedWrite := &armedStopWriteConnection{
			attempted: make(chan struct{}),
			failure:   writeFailure,
		}
		running := newRawStopSession(
			t,
			rpc.RoleCLI,
			capabilities(),
			shutdown.Request,
			nil,
			func(connection net.Conn) net.Conn {
				failedWrite.Conn = connection
				return failedWrite
			},
		)
		failedWrite.arm()
		_, err := running.client.Call(t.Context(), methodDaemonStop, struct{}{})
		if err == nil {
			t.Fatal("raw stop succeeded after its response write failed")
		}
		select {
		case <-failedWrite.attempted:
		case <-time.After(2 * time.Second):
			t.Fatal("stop response write was not attempted")
		}
		_ = running.finish(t)
		requireStopPending(t, shutdown.Requested())
	})
}

// TestControlClientRejectsInvalidStopAcknowledgements verifies a client never closes itself on untrusted response shapes.
func TestControlClientRejectsInvalidStopAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		name     string
		response any
		message  string
	}{
		{name: "not stopping", response: daemonStopResponse{}, message: "did not confirm shutdown"},
		{name: "wrong field type", response: map[string]any{"stopping": "yes"}, message: "decode daemon stop response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			running := newConfiguredStopClient(t, test.response)
			err := running.client.Stop(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Stop() error = %v, want %q", err, test.message)
			}
			select {
			case <-running.client.Done():
				t.Fatal("invalid acknowledgement closed the client session")
			default:
			}
		})
	}
}

// TestControlStopPublishesAndRetainsAcceptedServeFailure verifies shutdown cannot erase a terminal connection cause.
func TestControlStopPublishesAndRetainsAcceptedServeFailure(t *testing.T) {
	shutdown := daemon.NewShutdown()
	terminalFailure := errors.New("accepted stop connection failed")
	running := newRawStopSession(
		t,
		rpc.RoleCLI,
		capabilities(),
		shutdown.Request,
		nil,
		func(connection net.Conn) net.Conn {
			return &terminalStopReadConnection{Conn: connection, failure: terminalFailure}
		},
	)
	if _, err := running.client.Call(t.Context(), methodDaemonStop, struct{}{}); err != nil {
		t.Fatalf("raw stop call: %v", err)
	}
	err := running.finish(t)
	if !errors.Is(err, terminalFailure) {
		t.Fatalf("accepted Serve() error = %v, want %v", err, terminalFailure)
	}
	requireStopPublished(t, shutdown.Requested())
}

// TestControlStopContainsObservesAndReturnsShutdownPanic verifies bad wiring cannot crash the daemon or disappear.
func TestControlStopContainsObservesAndReturnsShutdownPanic(t *testing.T) {
	panicValue := "shutdown callback defect"
	observed := make(chan error, 1)
	running := newRawStopSession(
		t,
		rpc.RoleCLI,
		capabilities(),
		func() { panic(panicValue) },
		func(caller Caller, method string, err error) {
			if caller.Transport == testClientPeer && method == methodDaemonStop {
				observed <- err
			}
		},
		nil,
	)
	if _, err := running.client.Call(t.Context(), methodDaemonStop, struct{}{}); err != nil {
		t.Fatalf("raw stop call: %v", err)
	}
	err := running.finish(t)
	if err == nil || !strings.Contains(err.Error(), panicValue) {
		t.Fatalf("accepted Serve() error = %v, want contained callback panic", err)
	}
	select {
	case diagnostic := <-observed:
		if !strings.Contains(diagnostic.Error(), panicValue) {
			t.Fatalf("observed error = %v, want callback panic", diagnostic)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown callback panic was not observed")
	}
}
