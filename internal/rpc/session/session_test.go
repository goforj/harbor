package session

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/rpc"
)

var testProtocolRanges = []rpc.VersionRange{{
	Min: rpc.Version{Major: 1, Minor: 0},
	Max: rpc.Version{Major: 1, Minor: 2},
}}

// testPair owns one in-memory client/server connection and its terminal result.
type testPair struct {
	client           *Client
	clientConnection net.Conn
	cancelServer     context.CancelFunc
	serverDone       chan error
	serverStopped    chan struct{}
}

// notifyingConnection reports completed frame writes so capacity tests can
// prove the server accepted a request without timing assumptions.
type notifyingConnection struct {
	net.Conn
	writes chan struct{}
}

// failingWriteConnection injects one terminal write failure after negotiation.
type failingWriteConnection struct {
	net.Conn
	failAt atomic.Int64
	writes atomic.Int64
	cause  error
}

// Write fails at the configured write ordinal without forwarding partial bytes.
func (c *failingWriteConnection) Write(payload []byte) (int, error) {
	if c.writes.Add(1) == c.failAt.Load() {
		return 0, c.cause
	}

	return c.Conn.Write(payload)
}

// rawServerPeer exposes framed protocol control without starting the high-level client reader.
type rawServerPeer struct {
	connection net.Conn
	reader     *rpc.FrameReader
	writer     *rpc.FrameWriter
	protocol   rpc.Version
	cancel     context.CancelFunc
	done       chan error
	stopped    chan struct{}
}

// Write forwards one transport write and reports only completed writes.
func (c *notifyingConnection) Write(payload []byte) (int, error) {
	written, err := c.Conn.Write(payload)
	if err == nil {
		c.writes <- struct{}{}
	}

	return written, err
}

// newTestPair negotiates a real framed session and registers bounded cleanup.
func newTestPair(t *testing.T, serverConfig ServerConfig, clientConfig ClientConfig) *testPair {
	t.Helper()
	server, err := NewServer(serverConfig)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	serverStopped := make(chan struct{})
	go func() {
		serverDone <- server.Serve(serverContext, serverConnection)
		close(serverStopped)
	}()

	client, err := NewClient(t.Context(), clientConnection, clientConfig)
	if err != nil {
		cancelServer()
		_ = clientConnection.Close()
		select {
		case <-serverStopped:
		case <-time.After(time.Second):
		}
		t.Fatalf("new client: %v", err)
	}
	pair := &testPair{
		client:           client,
		clientConnection: clientConnection,
		cancelServer:     cancelServer,
		serverDone:       serverDone,
		serverStopped:    serverStopped,
	}
	t.Cleanup(func() {
		_ = pair.client.Close()
		pair.cancelServer()
		select {
		case <-pair.serverStopped:
		case <-time.After(time.Second):
			t.Error("server did not stop during cleanup")
		}
	})

	return pair
}

// testServerConfig returns the smallest valid daemon policy for a handler map.
func testServerConfig(handlers map[string]Handler) ServerConfig {
	return ServerConfig{
		DaemonVersion:  "test-daemon",
		ProtocolRanges: testProtocolRanges,
		Capabilities:   []rpc.Capability{"control.v1", "events.v1"},
		Handlers:       handlers,
	}
}

// testClientConfig returns a CLI policy sharing one capability with the daemon.
func testClientConfig() ClientConfig {
	return ClientConfig{
		Role:           rpc.RoleCLI,
		ClientVersion:  "test-client",
		ProtocolRanges: testProtocolRanges,
		Capabilities:   []rpc.Capability{"control.v1"},
	}
}

// newRawServerPeer completes a valid CLI handshake and leaves subsequent envelopes test-controlled.
func newRawServerPeer(t *testing.T, config ServerConfig) *rawServerPeer {
	t.Helper()
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("new raw server: %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- server.Serve(ctx, serverConnection)
		close(stopped)
	}()
	reader := rpc.NewDefaultFrameReader(clientConnection)
	writer := rpc.NewDefaultFrameWriter(clientConnection)
	clientConfig := testClientConfig()
	hello, err := rpc.NewHelloEnvelope(rpc.Hello{
		ProtocolRanges: clientConfig.ProtocolRanges,
		Role:           clientConfig.Role,
		ClientVersion:  clientConfig.ClientVersion,
		Capabilities:   clientConfig.Capabilities,
	})
	if err != nil {
		t.Fatalf("create raw hello: %v", err)
	}
	if err := writer.WriteEnvelope(hello); err != nil {
		t.Fatalf("write raw hello: %v", err)
	}
	welcomeEnvelope, err := reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read raw welcome: %v", err)
	}
	welcome, err := rpc.DecodePayload[rpc.Welcome](welcomeEnvelope)
	if err != nil {
		t.Fatalf("decode raw welcome: %v", err)
	}
	peer := &rawServerPeer{
		connection: clientConnection,
		reader:     reader,
		writer:     writer,
		protocol:   welcome.Protocol,
		cancel:     cancel,
		done:       done,
		stopped:    stopped,
	}
	t.Cleanup(func() {
		_ = peer.connection.Close()
		peer.cancel()
		select {
		case <-peer.stopped:
		case <-time.After(time.Second):
			t.Error("raw server peer did not stop")
		}
	})

	return peer
}

// newScriptedClient performs a valid handshake before the fake daemon emits
// caller-controlled response traffic.
func newScriptedClient(
	t *testing.T,
	script func(net.Conn, *rpc.FrameWriter, rpc.Version),
) (*Client, <-chan struct{}) {
	t.Helper()
	serverConnection, clientConnection := net.Pipe()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer serverConnection.Close()
		reader := rpc.NewDefaultFrameReader(serverConnection)
		writer := rpc.NewDefaultFrameWriter(serverConnection)
		helloEnvelope, err := reader.ReadEnvelope()
		if err != nil {
			return
		}
		hello, err := rpc.DecodePayload[rpc.Hello](helloEnvelope)
		if err != nil {
			return
		}
		welcome, rejection := rpc.NegotiateHello(
			hello,
			"scripted-daemon",
			testProtocolRanges,
			[]rpc.Capability{"control.v1"},
		)
		if rejection != nil {
			return
		}
		welcomeEnvelope, err := rpc.NewWelcomeEnvelope(welcome)
		if err != nil || writer.WriteEnvelope(welcomeEnvelope) != nil {
			return
		}
		script(serverConnection, writer, welcome.Protocol)
	}()
	client, err := NewClient(t.Context(), clientConnection, testClientConfig())
	if err != nil {
		t.Fatalf("new scripted client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("scripted daemon did not stop")
		}
	})

	return client, stopped
}

// TestConcurrentCallsCorrelateResponses proves interleaved completion cannot cross request IDs.
func TestConcurrentCallsCorrelateResponses(t *testing.T) {
	const callCount = 24
	const concurrency = 4

	started := make(chan struct{}, callCount)
	release := make(chan struct{})
	var active atomic.Int64
	var maximum atomic.Int64
	handler := func(ctx context.Context, request Request) (any, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		var input struct {
			Value int `json:"value"`
		}
		if err := json.Unmarshal(request.Payload, &input); err != nil {
			return nil, NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}

		return map[string]int{"value": input.Value}, nil
	}
	serverConfig := testServerConfig(map[string]Handler{"echo": handler})
	serverConfig.MaxConcurrentRequests = concurrency
	serverConfig.MaxQueuedRequests = callCount
	pair := newTestPair(t, serverConfig, testClientConfig())

	results := make(chan error, callCount)
	for value := range callCount {
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			payload, err := pair.client.Call(ctx, "echo", map[string]int{"value": value})
			if err != nil {
				results <- err
				return
			}
			var response struct {
				Value int `json:"value"`
			}
			if err := json.Unmarshal(payload, &response); err != nil {
				results <- err
				return
			}
			if response.Value != value {
				results <- fmt.Errorf("response value = %d, want %d", response.Value, value)
				return
			}
			results <- nil
		}()
	}
	for range concurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("concurrent handlers did not start")
		}
	}
	close(release)
	for range callCount {
		if err := <-results; err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}
	if maximum.Load() != concurrency {
		t.Fatalf("maximum handler concurrency = %d, want %d", maximum.Load(), concurrency)
	}
	if pair.client.Peer().Role != rpc.RoleDaemon {
		t.Fatalf("negotiated peer role = %q, want daemon", pair.client.Peer().Role)
	}
	capabilities := pair.client.Peer().Capabilities
	if len(capabilities) != 1 || capabilities[0] != "control.v1" {
		t.Fatalf("negotiated capabilities = %v, want [control.v1]", capabilities)
	}
	if err := pair.client.Err(); err != nil {
		t.Fatalf("healthy client error = %v, want nil", err)
	}
}

// TestServerQueueRejectsBeyondCapacity verifies accepted work remains bounded
// without stalling the reader needed for cancellation and other responses.
func TestServerQueueRejectsBeyondCapacity(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	block := func(ctx context.Context, _ Request) (any, error) {
		started <- struct{}{}
		select {
		case <-release:
			return struct{}{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	serverConfig := testServerConfig(map[string]Handler{"block": block})
	serverConfig.MaxConcurrentRequests = 1
	serverConfig.MaxQueuedRequests = 1
	server, err := NewServer(serverConfig)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverConnection, rawClientConnection := net.Pipe()
	clientConnection := &notifyingConnection{
		Conn:   rawClientConnection,
		writes: make(chan struct{}, 4),
	}
	serverContext, cancelServer := context.WithCancel(t.Context())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(serverContext, serverConnection)
	}()
	client, err := NewClient(t.Context(), clientConnection, testClientConfig())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var releaseOnce sync.Once
	releaseAll := func() {
		releaseOnce.Do(func() { close(release) })
	}
	defer func() {
		releaseAll()
		_ = client.Close()
		cancelServer()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("bounded server did not stop")
		}
	}()
	<-clientConnection.writes

	first := make(chan error, 1)
	go func() {
		_, err := client.Call(t.Context(), "block", struct{}{})
		first <- err
	}()
	<-clientConnection.writes
	<-started

	second := make(chan error, 1)
	go func() {
		_, err := client.Call(t.Context(), "block", struct{}{})
		second <- err
	}()
	<-clientConnection.writes

	_, err = client.Call(t.Context(), "block", struct{}{})
	assertWireErrorCode(t, err, rpc.ErrorCodeUnavailable)
	releaseAll()
	for _, result := range []<-chan error{first, second} {
		if err := <-result; err != nil {
			t.Fatalf("accepted bounded call: %v", err)
		}
	}
}

// TestClientPendingLimitFailsFast verifies local callers cannot create an unbounded wait set.
func TestClientPendingLimitFailsFast(t *testing.T) {
	started := make(chan struct{})
	block := func(ctx context.Context, _ Request) (any, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	}
	clientConfig := testClientConfig()
	clientConfig.MaxPendingRequests = 1
	pair := newTestPair(t, testServerConfig(map[string]Handler{"block": block}), clientConfig)
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := pair.client.Call(ctx, "block", struct{}{})
		first <- err
	}()
	<-started
	if _, err := pair.client.Call(t.Context(), "block", struct{}{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second call error = %v, want ErrBusy", err)
	}
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first call error = %v, want context cancellation", err)
	}
}

// TestCancellationAndDeadlineKeepConnectionUsable verifies request contexts do
// not become connection-level cancellation.
func TestCancellationAndDeadlineKeepConnectionUsable(t *testing.T) {
	started := make(chan struct{}, 2)
	observed := make(chan error, 2)
	block := func(ctx context.Context, _ Request) (any, error) {
		started <- struct{}{}
		<-ctx.Done()
		observed <- ctx.Err()

		return nil, ctx.Err()
	}
	ping := func(context.Context, Request) (any, error) {
		return map[string]bool{"ok": true}, nil
	}
	pair := newTestPair(t, testServerConfig(map[string]Handler{
		"block": block,
		"ping":  ping,
	}), testClientConfig())

	cancelContext, cancel := context.WithCancel(t.Context())
	cancelResult := make(chan error, 1)
	go func() {
		_, err := pair.client.Call(cancelContext, "block", struct{}{})
		cancelResult <- err
	}()
	<-started
	cancel()
	if err := <-cancelResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call error = %v, want context cancellation", err)
	}
	if err := <-observed; !errors.Is(err, context.Canceled) {
		t.Fatalf("handler cancellation = %v, want context cancellation", err)
	}

	deadlineContext, deadlineCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer deadlineCancel()
	deadlineResult := make(chan error, 1)
	go func() {
		_, err := pair.client.Call(deadlineContext, "block", struct{}{})
		deadlineResult <- err
	}()
	<-started
	if err := <-deadlineResult; !errors.Is(err, context.DeadlineExceeded) {
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeDeadlineExceeded {
			t.Fatalf("deadline call error = %v, want deadline exceeded", err)
		}
	}
	if err := <-observed; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handler deadline = %v, want deadline exceeded", err)
	}

	ctx, callCancel := context.WithTimeout(t.Context(), time.Second)
	defer callCancel()
	payload, err := pair.client.Call(ctx, "ping", struct{}{})
	if err != nil {
		t.Fatalf("ping after cancellation: %v", err)
	}
	if string(payload) != `{"ok":true}` {
		t.Fatalf("ping payload = %s", payload)
	}
}

// TestExpiredWireRequestReturnsDeadlineError verifies the daemon rejects work
// that expired before dispatch while keeping the connection usable.
func TestExpiredWireRequestReturnsDeadlineError(t *testing.T) {
	peer := newRawServerPeer(t, testServerConfig(nil))
	expired, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"expired-request",
		"missing",
		time.Now().UTC().Add(-time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create expired request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(expired); err != nil {
		t.Fatalf("write expired request: %v", err)
	}
	response, err := peer.reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read expired response: %v", err)
	}
	if response.Error == nil || response.Error.Code != rpc.ErrorCodeDeadlineExceeded {
		t.Fatalf("expired response error = %+v, want deadline exceeded", response.Error)
	}

	request, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"next-request",
		"missing",
		time.Now().UTC().Add(time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create next request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(request); err != nil {
		t.Fatalf("write next request: %v", err)
	}
	response, err = peer.reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read next response: %v", err)
	}
	if response.Error == nil || response.Error.Code != rpc.ErrorCodeNotFound {
		t.Fatalf("next response error = %+v, want not found", response.Error)
	}
}

// TestDuplicateRequestIDTerminatesConnection prevents two responses from sharing one correlation key.
func TestDuplicateRequestIDTerminatesConnection(t *testing.T) {
	started := make(chan struct{})
	block := func(ctx context.Context, _ Request) (any, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	}
	peer := newRawServerPeer(t, testServerConfig(map[string]Handler{"block": block}))
	request, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"duplicate-request",
		"block",
		time.Now().UTC().Add(time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create duplicate request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(request); err != nil {
		t.Fatalf("write first duplicate request: %v", err)
	}
	<-started
	if err := peer.writer.WriteEnvelope(request); err != nil {
		t.Fatalf("write second duplicate request: %v", err)
	}
	select {
	case err := <-peer.done:
		if !errors.Is(err, ErrProtocolViolation) {
			t.Fatalf("duplicate request error = %v, want protocol violation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("duplicate request did not terminate connection")
	}
}

// TestUnknownMethodAndHandlerFailureUseSafeWireErrors verifies causes remain daemon-local.
func TestUnknownMethodAndHandlerFailureUseSafeWireErrors(t *testing.T) {
	secretCause := errors.New("database password=hunter2")
	fail := func(context.Context, Request) (any, error) {
		return nil, NewHandlerError(rpc.ErrorCodeConflict, secretCause)
	}
	pair := newTestPair(t, testServerConfig(map[string]Handler{"fail": fail}), testClientConfig())

	_, err := pair.client.Call(t.Context(), "missing", struct{}{})
	assertWireErrorCode(t, err, rpc.ErrorCodeNotFound)
	_, err = pair.client.Call(t.Context(), "fail", struct{}{})
	assertWireErrorCode(t, err, rpc.ErrorCodeConflict)
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("wire error leaked handler cause: %v", err)
	}
}

// TestInvalidHandlerPayloadIsObservedAndRedacted verifies response encoding
// failures retain diagnostics without exposing Go type details to the peer.
func TestInvalidHandlerPayloadIsObservedAndRedacted(t *testing.T) {
	observed := make(chan error, 1)
	serverConfig := testServerConfig(map[string]Handler{
		"invalid-payload": func(context.Context, Request) (any, error) {
			return func() {}, nil
		},
	})
	serverConfig.ObserveError = func(_ Request, err error) {
		observed <- err
	}
	pair := newTestPair(t, serverConfig, testClientConfig())
	_, err := pair.client.Call(t.Context(), "invalid-payload", struct{}{})
	assertWireErrorCode(t, err, rpc.ErrorCodeInternal)
	if strings.Contains(err.Error(), "func") {
		t.Fatalf("wire response leaked encoding cause: %v", err)
	}
	var typeError *json.UnsupportedTypeError
	if localError := <-observed; !errors.As(localError, &typeError) {
		t.Fatalf("observed error = %T %v, want unsupported type", localError, localError)
	}
}

// TestHandlerPanicIsObservedAndRedacted proves a faulty method cannot crash or poison the connection.
func TestHandlerPanicIsObservedAndRedacted(t *testing.T) {
	observed := make(chan error, 1)
	serverConfig := testServerConfig(map[string]Handler{
		"panic": func(context.Context, Request) (any, error) {
			panic("secret panic detail")
		},
		"ping": func(context.Context, Request) (any, error) {
			return struct{}{}, nil
		},
	})
	serverConfig.ObserveError = func(_ Request, err error) {
		observed <- err
		panic("observer failure")
	}
	pair := newTestPair(t, serverConfig, testClientConfig())

	_, err := pair.client.Call(t.Context(), "panic", struct{}{})
	assertWireErrorCode(t, err, rpc.ErrorCodeInternal)
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("wire panic error leaked local detail: %v", err)
	}
	localError := <-observed
	var panicError *HandlerPanicError
	if !errors.As(localError, &panicError) {
		t.Fatalf("observed error type = %T, want HandlerPanicError", localError)
	}
	if len(panicError.Stack()) == 0 {
		t.Fatal("observed panic has no stack")
	}
	if _, err := pair.client.Call(t.Context(), "ping", struct{}{}); err != nil {
		t.Fatalf("ping after handler panic: %v", err)
	}
}

// TestHandshakeRejectsIncompatibleAndUnauthorizedPeers verifies rejection categories remain explicit.
func TestHandshakeRejectsIncompatibleAndUnauthorizedPeers(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ClientConfig)
		wantCode  rpc.ErrorCode
	}{
		{
			name: "incompatible protocol",
			configure: func(config *ClientConfig) {
				config.ProtocolRanges = []rpc.VersionRange{{
					Min: rpc.Version{Major: 2},
					Max: rpc.Version{Major: 2},
				}}
			},
			wantCode: rpc.ErrorCodeUnsupportedProtocol,
		},
		{
			name: "session role without authorization",
			configure: func(config *ClientConfig) {
				config.Role = rpc.RoleGoForjSession
			},
			wantCode: rpc.ErrorCodePermissionDenied,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, err := NewServer(testServerConfig(nil))
			if err != nil {
				t.Fatalf("new server: %v", err)
			}
			serverConnection, clientConnection := net.Pipe()
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- server.Serve(t.Context(), serverConnection)
			}()
			config := testClientConfig()
			test.configure(&config)
			_, err = NewClient(t.Context(), clientConnection, config)
			var handshakeError *HandshakeError
			if !errors.As(err, &handshakeError) {
				t.Fatalf("handshake error = %T %v, want HandshakeError", err, err)
			}
			if handshakeError.Failure.Code != test.wantCode {
				t.Fatalf("rejection code = %q, want %q", handshakeError.Failure.Code, test.wantCode)
			}
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("rejected server session did not stop")
			}
		})
	}
}

// TestAuthorizerRejectionReturnsPermissionDenied verifies custom authorization
// causes stay local even for otherwise valid CLI peers.
func TestAuthorizerRejectionReturnsPermissionDenied(t *testing.T) {
	serverConfig := testServerConfig(nil)
	serverConfig.Authorize = func(context.Context, rpc.Hello) error {
		return errors.New("private transport identity detail")
	}
	server, err := NewServer(serverConfig)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(t.Context(), serverConnection)
	}()
	_, err = NewClient(t.Context(), clientConnection, testClientConfig())
	var handshakeError *HandshakeError
	if !errors.As(err, &handshakeError) {
		t.Fatalf("client error = %T %v, want HandshakeError", err, err)
	}
	if handshakeError.Failure.Code != rpc.ErrorCodePermissionDenied {
		t.Fatalf("authorization code = %q, want permission denied", handshakeError.Failure.Code)
	}
	if strings.Contains(err.Error(), "identity") {
		t.Fatalf("authorization rejection leaked local cause: %v", err)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("authorization rejection left server blocked")
	}
}

// TestAuthorizedSessionRoleNegotiates proves session admission must be an explicit application choice.
func TestAuthorizedSessionRoleNegotiates(t *testing.T) {
	serverConfig := testServerConfig(map[string]Handler{
		"ping": func(context.Context, Request) (any, error) { return struct{}{}, nil },
	})
	serverConfig.Authorize = func(_ context.Context, hello rpc.Hello) error {
		if hello.Role != rpc.RoleGoForjSession {
			return errors.New("unexpected role")
		}

		return nil
	}
	clientConfig := testClientConfig()
	clientConfig.Role = rpc.RoleGoForjSession
	pair := newTestPair(t, serverConfig, clientConfig)
	if _, err := pair.client.Call(t.Context(), "ping", struct{}{}); err != nil {
		t.Fatalf("authorized session ping: %v", err)
	}
}

// TestOversizedFrameTerminatesConnection verifies a hostile length is not drained or retried.
func TestOversizedFrameTerminatesConnection(t *testing.T) {
	pair := newTestPair(t, testServerConfig(nil), testClientConfig())
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], rpc.MaximumFrameSize+1)
	if _, err := pair.clientConnection.Write(header[:]); err != nil {
		t.Fatalf("write oversized header: %v", err)
	}
	select {
	case err := <-pair.serverDone:
		var sizeError rpc.FrameSizeError
		if !errors.As(err, &sizeError) {
			t.Fatalf("server error = %T %v, want FrameSizeError", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not terminate oversized frame")
	}
	select {
	case <-pair.client.Done():
	case <-time.After(time.Second):
		t.Fatal("client did not observe oversized-frame shutdown")
	}
}

// TestMalformedFrameTerminatesConnection verifies invalid JSON makes the session terminal.
func TestMalformedFrameTerminatesConnection(t *testing.T) {
	pair := newTestPair(t, testServerConfig(nil), testClientConfig())
	frame := []byte{0, 0, 0, 1, '{'}
	if _, err := pair.clientConnection.Write(frame); err != nil {
		t.Fatalf("write malformed frame: %v", err)
	}
	select {
	case err := <-pair.serverDone:
		if !errors.Is(err, rpc.ErrInvalidFrameJSON) {
			t.Fatalf("server error = %v, want invalid JSON", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not terminate malformed frame")
	}
}

// TestClientTerminatesInvalidDaemonTraffic verifies response protocol,
// correlation, kind, and frame bounds are terminal on the client side.
func TestClientTerminatesInvalidDaemonTraffic(t *testing.T) {
	tests := []struct {
		name   string
		script func(net.Conn, *rpc.FrameWriter, rpc.Version)
	}{
		{
			name: "wrong protocol",
			script: func(_ net.Conn, writer *rpc.FrameWriter, _ rpc.Version) {
				response, _ := rpc.NewResponseEnvelope(rpc.Version{Major: 1}, "unknown-request", struct{}{})
				_ = writer.WriteEnvelope(response)
			},
		},
		{
			name: "unknown request ID",
			script: func(_ net.Conn, writer *rpc.FrameWriter, protocol rpc.Version) {
				response, _ := rpc.NewResponseEnvelope(protocol, "unknown-request", struct{}{})
				_ = writer.WriteEnvelope(response)
			},
		},
		{
			name: "unexpected event",
			script: func(_ net.Conn, writer *rpc.FrameWriter, protocol rpc.Version) {
				event, _ := rpc.NewEventEnvelope(protocol, "state", 1, struct{}{})
				_ = writer.WriteEnvelope(event)
			},
		},
		{
			name: "oversized frame",
			script: func(connection net.Conn, _ *rpc.FrameWriter, _ rpc.Version) {
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], rpc.MaximumFrameSize+1)
				_, _ = connection.Write(header[:])
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, stopped := newScriptedClient(t, test.script)
			select {
			case <-client.Done():
			case <-time.After(time.Second):
				t.Fatal("invalid daemon traffic did not terminate client")
			}
			if !errors.Is(client.Err(), ErrClosed) {
				t.Fatalf("client error = %v, want closed session", client.Err())
			}
			<-stopped
		})
	}
}

// TestClientRejectsInvalidWelcomeSelection verifies a daemon cannot grant an
// unadvertised protocol or capability during negotiation.
func TestClientRejectsInvalidWelcomeSelection(t *testing.T) {
	tests := []struct {
		name    string
		welcome rpc.Welcome
	}{
		{
			name: "unadvertised protocol",
			welcome: rpc.Welcome{
				Protocol: rpc.Version{Major: 2},
				ProtocolRanges: []rpc.VersionRange{{
					Min: rpc.Version{Major: 2},
					Max: rpc.Version{Major: 2},
				}},
				Role:          rpc.RoleDaemon,
				DaemonVersion: "fake-daemon",
			},
		},
		{
			name: "unadvertised capability",
			welcome: rpc.Welcome{
				Protocol:       rpc.Version{Major: 1, Minor: 2},
				ProtocolRanges: testProtocolRanges,
				Role:           rpc.RoleDaemon,
				DaemonVersion:  "fake-daemon",
				Capabilities:   []rpc.Capability{"future.v1"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverConnection, clientConnection := net.Pipe()
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				defer serverConnection.Close()
				reader := rpc.NewDefaultFrameReader(serverConnection)
				writer := rpc.NewDefaultFrameWriter(serverConnection)
				if _, err := reader.ReadEnvelope(); err != nil {
					return
				}
				envelope, err := rpc.NewWelcomeEnvelope(test.welcome)
				if err == nil {
					_ = writer.WriteEnvelope(envelope)
				}
			}()
			_, err := NewClient(t.Context(), clientConnection, testClientConfig())
			if !errors.Is(err, ErrProtocolViolation) {
				t.Fatalf("client handshake error = %v, want protocol violation", err)
			}
			<-serverDone
		})
	}
}

// TestClientWriteFailureClearsPendingAndTerminates verifies a failed frame does
// not retain request correlation state or leave future callers blocked.
func TestClientWriteFailureClearsPendingAndTerminates(t *testing.T) {
	server, err := NewServer(testServerConfig(nil))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverConnection, rawClientConnection := net.Pipe()
	cause := errors.New("injected client write failure")
	clientConnection := &failingWriteConnection{Conn: rawClientConnection, cause: cause}
	clientConnection.failAt.Store(2)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Serve(t.Context(), serverConnection)
	}()
	client, err := NewClient(t.Context(), clientConnection, testClientConfig())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Call(t.Context(), "missing", struct{}{})
	if !errors.Is(err, ErrClosed) || !errors.Is(err, cause) {
		t.Fatalf("call error = %v, want closed session wrapping write cause", err)
	}
	if len(client.pending) != 0 {
		t.Fatalf("pending calls = %d, want zero", len(client.pending))
	}
	if !errors.Is(client.Err(), cause) {
		t.Fatalf("client terminal error = %v, want write cause", client.Err())
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("client write failure left server blocked")
	}
}

// TestClientHandshakeCancellationClosesBlockedTransport verifies cancellation
// wakes a Hello write even when the peer never reads it.
func TestClientHandshakeCancellationClosesBlockedTransport(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	defer serverConnection.Close()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := NewClient(ctx, clientConnection, testClientConfig())
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("client handshake error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("client handshake cancellation left NewClient blocked")
	}
}

// TestServerHandshakeCancellationClosesBlockedTransport verifies cancellation
// wakes Serve while a peer holds an idle unauthenticated connection.
func TestServerHandshakeCancellationClosesBlockedTransport(t *testing.T) {
	server, err := NewServer(testServerConfig(nil))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(ctx, serverConnection)
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("server handshake error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server handshake cancellation left Serve blocked")
	}
}

// TestConfiguredHandshakeTimeoutsReturnDeadlineExceeded verifies stream-specific
// timeout text does not leak into caller control flow.
func TestConfiguredHandshakeTimeoutsReturnDeadlineExceeded(t *testing.T) {
	t.Run("client", func(t *testing.T) {
		serverConnection, clientConnection := net.Pipe()
		defer serverConnection.Close()
		config := testClientConfig()
		config.HandshakeTimeout = 10 * time.Millisecond
		_, err := NewClient(t.Context(), clientConnection, config)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("client timeout error = %v, want deadline exceeded", err)
		}
	})

	t.Run("server", func(t *testing.T) {
		config := testServerConfig(nil)
		config.HandshakeTimeout = 10 * time.Millisecond
		server, err := NewServer(config)
		if err != nil {
			t.Fatalf("new server: %v", err)
		}
		serverConnection, clientConnection := net.Pipe()
		defer clientConnection.Close()
		err = server.Serve(t.Context(), serverConnection)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("server timeout error = %v, want deadline exceeded", err)
		}
	})
}

// TestServerIdleTimeoutClosesNegotiatedConnection verifies the daemon reports
// one stable semantic cause instead of a transport-specific timeout.
func TestServerIdleTimeoutClosesNegotiatedConnection(t *testing.T) {
	config := testServerConfig(nil)
	config.IdleTimeout = 20 * time.Millisecond
	peer := newRawServerPeer(t, config)

	select {
	case err := <-peer.done:
		if !errors.Is(err, ErrIdleTimeout) {
			t.Fatalf("server error = %v, want idle timeout", err)
		}
		if err.Error() != ErrIdleTimeout.Error() {
			t.Fatalf("server error text = %q, want %q", err, ErrIdleTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("idle negotiated connection did not close")
	}
}

// TestServerIdleTimeoutWaitsForActiveAndQueuedWork verifies request deadlines,
// not connection idleness, remain responsible for bounding accepted handlers.
func TestServerIdleTimeoutWaitsForActiveAndQueuedWork(t *testing.T) {
	const idleTimeout = 100 * time.Millisecond

	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	handler := func(ctx context.Context, request Request) (any, error) {
		started <- request.ID
		release := releaseFirst
		if request.ID == "request-2" {
			release = releaseSecond
		}
		select {
		case <-release:
			return struct{}{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	config := testServerConfig(map[string]Handler{"block": handler})
	config.IdleTimeout = idleTimeout
	config.MaxConcurrentRequests = 1
	config.MaxQueuedRequests = 1
	peer := newRawServerPeer(t, config)
	first, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"request-1",
		"block",
		time.Now().Add(5*time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create first request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(first); err != nil {
		t.Fatalf("write first request: %v", err)
	}
	if requestID := <-started; requestID != "request-1" {
		t.Fatalf("first started request = %q, want request-1", requestID)
	}
	second, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"request-2",
		"block",
		time.Now().Add(5*time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create second request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(second); err != nil {
		t.Fatalf("write queued request: %v", err)
	}
	third, err := rpc.NewRequestEnvelope(
		peer.protocol,
		"request-3",
		"block",
		time.Now().Add(5*time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create capacity request: %v", err)
	}
	if err := peer.writer.WriteEnvelope(third); err != nil {
		t.Fatalf("write capacity request: %v", err)
	}
	if err := peer.connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set capacity response deadline: %v", err)
	}
	capacityResponse, err := peer.reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read capacity response: %v", err)
	}
	if capacityResponse.RequestID != "request-3" || capacityResponse.Error == nil ||
		capacityResponse.Error.Code != rpc.ErrorCodeUnavailable {
		t.Fatalf("capacity response = %+v, want unavailable request-3", capacityResponse)
	}

	select {
	case err := <-peer.done:
		t.Fatalf("server stopped during active and queued work: %v", err)
	case <-time.After(3 * idleTimeout):
	}
	close(releaseFirst)
	firstResponse, err := peer.reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read first response: %v", err)
	}
	if firstResponse.RequestID != "request-1" {
		t.Fatalf("first response ID = %q, want request-1", firstResponse.RequestID)
	}
	if requestID := <-started; requestID != "request-2" {
		t.Fatalf("second started request = %q, want request-2", requestID)
	}
	select {
	case err := <-peer.done:
		t.Fatalf("server stopped during second accepted request: %v", err)
	case <-time.After(3 * idleTimeout):
	}
	close(releaseSecond)
	secondResponse, err := peer.reader.ReadEnvelope()
	if err != nil {
		t.Fatalf("read second response: %v", err)
	}
	if secondResponse.RequestID != "request-2" {
		t.Fatalf("second response ID = %q, want request-2", secondResponse.RequestID)
	}
	if err := peer.connection.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear response deadline: %v", err)
	}

	select {
	case err := <-peer.done:
		if !errors.Is(err, ErrIdleTimeout) {
			t.Fatalf("server error after work = %v, want idle timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not re-arm idle timeout after work")
	}
}

// TestRequestAcceptanceAtIdleExpiryHasOneWinner exercises the timer and
// request-accounting boundary without depending on scheduler timing.
func TestRequestAcceptanceAtIdleExpiryHasOneWinner(t *testing.T) {
	config := testServerConfig(map[string]Handler{
		"block": func(ctx context.Context, _ Request) (any, error) {
			<-ctx.Done()

			return nil, ctx.Err()
		},
	})
	config.IdleTimeout = time.Hour
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	for iteration := range 100 {
		serverConnection, clientConnection := net.Pipe()
		active := newServerConnection(
			server,
			serverConnection,
			rpc.NewDefaultFrameReader(serverConnection),
			rpc.NewDefaultFrameWriter(io.Discard),
			Peer{Protocol: rpc.Version{Major: 1}},
		)
		active.startIdleTimeout()
		active.requestsMu.Lock()
		cycle := active.idleCycle
		active.requestsMu.Unlock()
		envelope, createErr := rpc.NewRequestEnvelope(
			rpc.Version{Major: 1},
			fmt.Sprintf("request-%d", iteration),
			"block",
			time.Now().Add(time.Second),
			struct{}{},
		)
		if createErr != nil {
			t.Fatalf("create boundary request: %v", createErr)
		}
		start := make(chan struct{})
		accepted := make(chan error, 1)
		expired := make(chan struct{})
		go func() {
			<-start
			accepted <- active.acceptRequest(envelope)
		}()
		go func() {
			<-start
			active.expireIdleTimeout(cycle)
			close(expired)
		}()
		close(start)
		acceptErr := <-accepted
		<-expired

		if acceptErr == nil {
			active.cancelRequests()
			active.work.Wait()
			if terminalErr := active.terminalError(); terminalErr != nil {
				t.Fatalf("iteration %d accepted request but terminated: %v", iteration, terminalErr)
			}
		} else {
			if !errors.Is(acceptErr, ErrIdleTimeout) {
				t.Fatalf("iteration %d acceptance error = %v, want idle timeout", iteration, acceptErr)
			}
			if !errors.Is(active.terminalError(), ErrIdleTimeout) {
				t.Fatalf("iteration %d terminal error = %v, want idle timeout", iteration, active.terminalError())
			}
		}
		active.stopIdleTimeout()
		active.terminate(nil)
		_ = clientConnection.Close()
	}
}

// TestTerminalConnectionRejectsRequestsAndIdleCallbacks verifies timer and
// dispatch paths cannot revive or overwrite an already-terminal connection.
func TestTerminalConnectionRejectsRequestsAndIdleCallbacks(t *testing.T) {
	config := testServerConfig(nil)
	config.IdleTimeout = time.Hour
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	active := newServerConnection(
		server,
		serverConnection,
		rpc.NewDefaultFrameReader(serverConnection),
		rpc.NewDefaultFrameWriter(io.Discard),
		Peer{Protocol: rpc.Version{Major: 1}},
	)
	active.startIdleTimeout()
	active.requestsMu.Lock()
	cycle := active.idleCycle
	active.requestsMu.Unlock()
	active.terminate(context.Canceled)

	active.expireIdleTimeout(cycle)
	if !errors.Is(active.terminalError(), context.Canceled) {
		t.Fatalf("idle callback replaced terminal error: %v", active.terminalError())
	}
	active.stopIdleTimeout()
	active.requestsMu.Lock()
	active.armIdleTimeoutLocked()
	timer := active.idleTimer
	active.requestsMu.Unlock()
	if timer != nil {
		t.Fatal("terminal connection re-armed its idle timer")
	}

	envelope, err := rpc.NewRequestEnvelope(
		rpc.Version{Major: 1},
		"request-after-close",
		"missing",
		time.Now().Add(time.Second),
		struct{}{},
	)
	if err != nil {
		t.Fatalf("create request after close: %v", err)
	}
	if err := active.acceptRequest(envelope); !errors.Is(err, ErrClosed) {
		t.Fatalf("request after close error = %v, want ErrClosed", err)
	}
}

// TestIdleTimerStopAndConnectionCloseAreRaceSafe repeatedly overlaps timer
// invalidation, expiry, and transport termination to expose lifecycle races.
func TestIdleTimerStopAndConnectionCloseAreRaceSafe(t *testing.T) {
	config := testServerConfig(nil)
	config.IdleTimeout = time.Hour
	server, err := NewServer(config)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	for range 200 {
		serverConnection, clientConnection := net.Pipe()
		active := newServerConnection(
			server,
			serverConnection,
			rpc.NewDefaultFrameReader(serverConnection),
			rpc.NewDefaultFrameWriter(io.Discard),
			Peer{Protocol: rpc.Version{Major: 1}},
		)
		active.startIdleTimeout()
		active.requestsMu.Lock()
		cycle := active.idleCycle
		active.requestsMu.Unlock()
		start := make(chan struct{})
		var raced sync.WaitGroup
		for _, operation := range []func(){
			active.stopIdleTimeout,
			func() { active.expireIdleTimeout(cycle) },
			func() { active.terminate(context.Canceled) },
		} {
			raced.Add(1)
			go func() {
				defer raced.Done()
				<-start
				operation()
			}()
		}
		close(start)
		raced.Wait()
		terminalErr := active.terminalError()
		if !errors.Is(terminalErr, context.Canceled) && !errors.Is(terminalErr, ErrIdleTimeout) {
			t.Fatalf("terminal error = %v, want cancellation or idle timeout", terminalErr)
		}
		_ = clientConnection.Close()
	}
}

// TestUnknownCancelsDoNotRefreshIdleTimeout verifies a peer cannot retain an
// otherwise unused daemon connection by sending correlation-free traffic.
func TestUnknownCancelsDoNotRefreshIdleTimeout(t *testing.T) {
	config := testServerConfig(nil)
	config.IdleTimeout = 40 * time.Millisecond
	peer := newRawServerPeer(t, config)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()

	for {
		select {
		case err := <-peer.done:
			if !errors.Is(err, ErrIdleTimeout) {
				t.Fatalf("server error = %v, want idle timeout", err)
			}
			return
		case <-ticker.C:
			envelope, err := rpc.NewCancelEnvelope(peer.protocol, "unknown-request")
			if err != nil {
				t.Fatalf("create unknown cancel: %v", err)
			}
			if err := peer.writer.WriteEnvelope(envelope); err != nil {
				select {
				case terminalErr := <-peer.done:
					if !errors.Is(terminalErr, ErrIdleTimeout) {
						t.Fatalf("server error = %v, want idle timeout", terminalErr)
					}
					return
				case <-time.After(time.Second):
					t.Fatalf("write unknown cancel: %v", err)
				}
			}
		case <-deadline.C:
			t.Fatal("unknown cancels kept an idle connection alive")
		}
	}
}

// TestServerCancellationJoinsActiveHandler verifies shutdown wakes the reader and request work.
func TestServerCancellationJoinsActiveHandler(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	block := func(ctx context.Context, _ Request) (any, error) {
		close(started)
		<-ctx.Done()
		close(stopped)

		return nil, ctx.Err()
	}
	pair := newTestPair(t, testServerConfig(map[string]Handler{"block": block}), testClientConfig())
	callDone := make(chan error, 1)
	go func() {
		_, err := pair.client.Call(t.Context(), "block", struct{}{})
		callDone <- err
	}()
	<-started
	pair.cancelServer()
	select {
	case err := <-pair.serverDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("server error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server cancellation left Serve blocked")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("server cancellation left handler blocked")
	}
	select {
	case err := <-callDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("client call error = %v, want closed session", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server cancellation left client call blocked")
	}
}

// TestConcurrentCloseWakesCalls verifies terminal shutdown is idempotent under caller races.
func TestConcurrentCloseWakesCalls(t *testing.T) {
	started := make(chan struct{}, 8)
	block := func(ctx context.Context, _ Request) (any, error) {
		started <- struct{}{}
		<-ctx.Done()

		return nil, ctx.Err()
	}
	pair := newTestPair(t, testServerConfig(map[string]Handler{"block": block}), testClientConfig())
	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := pair.client.Call(t.Context(), "block", struct{}{})
			results <- err
		}()
	}
	for range 8 {
		<-started
	}

	var closes sync.WaitGroup
	for range 8 {
		closes.Add(1)
		go func() {
			defer closes.Done()
			_ = pair.client.Close()
		}()
	}
	closes.Wait()
	for range 8 {
		select {
		case err := <-results:
			if !errors.Is(err, ErrClosed) {
				t.Errorf("call error = %v, want closed session", err)
			}
		case <-time.After(time.Second):
			t.Fatal("client close left call blocked")
		}
	}
}

// TestConfigurationRejectsNilHandlersAndUnsafeLimits verifies required dependencies fail fast.
func TestConfigurationRejectsNilHandlersAndUnsafeLimits(t *testing.T) {
	config := testServerConfig(map[string]Handler{"ping": nil})
	if _, err := NewServer(config); err == nil {
		t.Fatal("NewServer accepted a nil handler")
	}
	config = testServerConfig(nil)
	config.MaxConcurrentRequests = maximumRequestLimit + 1
	if _, err := NewServer(config); err == nil {
		t.Fatal("NewServer accepted an unsafe concurrency limit")
	}
	clientConfig := testClientConfig()
	clientConfig.MaxPendingRequests = maximumRequestLimit + 1
	serverConnection, clientConnection := net.Pipe()
	defer serverConnection.Close()
	defer clientConnection.Close()
	if _, err := NewClient(t.Context(), clientConnection, clientConfig); err == nil {
		t.Fatal("NewClient accepted an unsafe pending limit")
	}
	var zero HandlerError
	if zero.Error() == "" || zero.Unwrap() != nil || zero.Code() != rpc.ErrorCodeInternal {
		t.Fatal("zero HandlerError is not safe")
	}
}

// TestServerConfigurationValidationCoversEachBoundary verifies invalid daemon
// policy is rejected before any transport starts.
func TestServerConfigurationValidationCoversEachBoundary(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ServerConfig)
	}{
		{
			name: "missing protocol ranges",
			configure: func(config *ServerConfig) {
				config.ProtocolRanges = nil
			},
		},
		{
			name: "invalid capability",
			configure: func(config *ServerConfig) {
				config.Capabilities = []rpc.Capability{""}
			},
		},
		{
			name: "negative handshake timeout",
			configure: func(config *ServerConfig) {
				config.HandshakeTimeout = -time.Second
			},
		},
		{
			name: "negative idle timeout",
			configure: func(config *ServerConfig) {
				config.IdleTimeout = -time.Second
			},
		},
		{
			name: "negative concurrent limit",
			configure: func(config *ServerConfig) {
				config.MaxConcurrentRequests = -1
			},
		},
		{
			name: "negative queue limit",
			configure: func(config *ServerConfig) {
				config.MaxQueuedRequests = -1
			},
		},
		{
			name: "excessive queue limit",
			configure: func(config *ServerConfig) {
				config.MaxQueuedRequests = maximumRequestLimit + 1
			},
		},
		{
			name: "invalid method",
			configure: func(config *ServerConfig) {
				config.Handlers = map[string]Handler{"": func(context.Context, Request) (any, error) {
					return struct{}{}, nil
				}}
			},
		},
		{
			name: "invalid daemon version",
			configure: func(config *ServerConfig) {
				config.DaemonVersion = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testServerConfig(nil)
			test.configure(&config)
			if _, err := NewServer(config); err == nil {
				t.Fatal("NewServer accepted invalid configuration")
			}
		})
	}

	server, err := NewServer(testServerConfig(nil))
	if err != nil {
		t.Fatalf("new valid server: %v", err)
	}
	if server.config.IdleTimeout != defaultIdleTimeout {
		t.Fatalf("default idle timeout = %s, want %s", server.config.IdleTimeout, defaultIdleTimeout)
	}
	if err := server.Serve(t.Context(), nil); err == nil {
		t.Fatal("Serve accepted a nil connection")
	}
}

// TestClientConfigurationValidationCoversEachBoundary verifies invalid client
// policy is rejected before a Hello is written.
func TestClientConfigurationValidationCoversEachBoundary(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ClientConfig)
	}{
		{
			name: "daemon role",
			configure: func(config *ClientConfig) {
				config.Role = rpc.RoleDaemon
			},
		},
		{
			name: "missing protocol ranges",
			configure: func(config *ClientConfig) {
				config.ProtocolRanges = nil
			},
		},
		{
			name: "invalid capability",
			configure: func(config *ClientConfig) {
				config.Capabilities = []rpc.Capability{""}
			},
		},
		{
			name: "negative handshake timeout",
			configure: func(config *ClientConfig) {
				config.HandshakeTimeout = -time.Second
			},
		},
		{
			name: "negative request timeout",
			configure: func(config *ClientConfig) {
				config.RequestTimeout = -time.Second
			},
		},
		{
			name: "negative pending limit",
			configure: func(config *ClientConfig) {
				config.MaxPendingRequests = -1
			},
		},
		{
			name: "invalid client version",
			configure: func(config *ClientConfig) {
				config.ClientVersion = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serverConnection, clientConnection := net.Pipe()
			defer serverConnection.Close()
			defer clientConnection.Close()
			config := testClientConfig()
			test.configure(&config)
			if _, err := NewClient(t.Context(), clientConnection, config); err == nil {
				t.Fatal("NewClient accepted invalid configuration")
			}
		})
	}
	if _, err := NewClient(t.Context(), nil, testClientConfig()); err == nil {
		t.Fatal("NewClient accepted a nil connection")
	}
}

// TestExportedErrorsHaveSafeZeroValues verifies ordinary error inspection cannot panic.
func TestExportedErrorsHaveSafeZeroValues(t *testing.T) {
	var handlerError *HandlerError
	if handlerError.Error() == "" || handlerError.Unwrap() != nil || handlerError.Code() != rpc.ErrorCodeInternal {
		t.Fatal("nil HandlerError is not safe")
	}
	withoutCause := NewHandlerError(rpc.ErrorCodeConflict, nil)
	if withoutCause.Error() == "" || withoutCause.Unwrap() == nil {
		t.Fatal("HandlerError without a cause is not safe")
	}
	var panicError *HandlerPanicError
	if panicError.Error() == "" || panicError.Stack() != nil {
		t.Fatal("nil HandlerPanicError is not safe")
	}
	var handshakeError *HandshakeError
	if handshakeError.Error() == "" {
		t.Fatal("nil HandshakeError is not safe")
	}
	rejection := &HandshakeError{Failure: rpc.NewWireError(rpc.ErrorCodePermissionDenied)}
	if rejection.Error() != rejection.Failure.Message {
		t.Fatal("HandshakeError did not return its reviewed message")
	}
}

// assertWireErrorCode verifies a call received one reviewed peer error category.
func assertWireErrorCode(t *testing.T, err error, expected rpc.ErrorCode) {
	t.Helper()
	var wireError rpc.WireError
	if !errors.As(err, &wireError) {
		t.Fatalf("call error = %T %v, want rpc.WireError", err, err)
	}
	if wireError.Code != expected {
		t.Fatalf("wire error code = %q, want %q", wireError.Code, expected)
	}
}
