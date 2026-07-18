package tcprelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// testBackend owns a loopback listener and every accepted fixture connection.
type testBackend struct {
	listener net.Listener
	handler  func(net.Conn)
	accepted chan struct{}

	mutex       sync.Mutex
	connections map[net.Conn]struct{}
	wait        sync.WaitGroup
	closed      chan struct{}
}

// stubListener exposes controlled listener failures without platform-specific sockets.
type stubListener struct {
	address net.Addr
	accept  func() (net.Conn, error)
	closed  bool
}

// stubAddress supplies an arbitrary string to listener-address validation.
type stubAddress string

// temporaryAcceptError models one retryable listener failure.
type temporaryAcceptError struct{}

// TestNewRelayValidatesConfig covers every resource and endpoint boundary before a listener is owned.
func TestNewRelayValidatesConfig(t *testing.T) {
	valid := validTestConfig(netip.MustParseAddrPort("127.0.0.1:43101"))
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "missing upstream", mutate: func(config *Config) { config.Upstream = netip.AddrPort{} }, want: "IPv4 loopback"},
		{name: "public upstream", mutate: func(config *Config) { config.Upstream = netip.MustParseAddrPort("192.0.2.1:80") }, want: "IPv4 loopback"},
		{name: "IPv6 upstream", mutate: func(config *Config) { config.Upstream = netip.MustParseAddrPort("[::1]:80") }, want: "IPv4 loopback"},
		{name: "zero upstream port", mutate: func(config *Config) { config.Upstream = netip.MustParseAddrPort("127.0.0.1:0") }, want: "port"},
		{name: "negative maximum", mutate: func(config *Config) { config.MaxConnections = -1 }, want: "maximum connections"},
		{name: "excessive maximum", mutate: func(config *Config) { config.MaxConnections = hardMaximumConnections + 1 }, want: "maximum connections"},
		{name: "short connect timeout", mutate: func(config *Config) { config.ConnectTimeout = time.Nanosecond }, want: "connect timeout"},
		{name: "long connect timeout", mutate: func(config *Config) { config.ConnectTimeout = maximumConnectTimeout + time.Millisecond }, want: "connect timeout"},
		{name: "short shutdown timeout", mutate: func(config *Config) { config.ShutdownTimeout = time.Nanosecond }, want: "shutdown timeout"},
		{name: "long shutdown timeout", mutate: func(config *Config) { config.ShutdownTimeout = maximumShutdownTimeout + time.Millisecond }, want: "shutdown timeout"},
		{name: "short keepalive", mutate: func(config *Config) { config.KeepAlive = time.Millisecond }, want: "keepalive"},
		{name: "long keepalive", mutate: func(config *Config) { config.KeepAlive = maximumKeepAlive + time.Second }, want: "keepalive"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := New(config); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNewRelayAppliesDefaults verifies zero-valued tuning does not disable safety limits.
func TestNewRelayAppliesDefaults(t *testing.T) {
	relay, err := New(Config{Upstream: netip.MustParseAddrPort("127.0.0.1:43101")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if relay.config.MaxConnections != defaultMaximumConnections ||
		relay.config.ConnectTimeout != defaultConnectTimeout ||
		relay.config.ShutdownTimeout != defaultShutdownTimeout ||
		relay.config.KeepAlive != defaultKeepAlive {
		t.Fatalf("normalized config = %+v", relay.config)
	}
}

// TestRelayForwardsBytesAndPublishesCounters proves the relay remains payload-agnostic in both directions.
func TestRelayForwardsBytesAndPublishesCounters(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()
	relay := mustTestRelay(t, backend.Address(), nil)
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)

	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	payload := []byte("native protocol frame")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client Write() error = %v", err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(client, received); err != nil {
		t.Fatalf("client ReadFull() error = %v", err)
	}
	if string(received) != string(payload) {
		t.Fatalf("relay response = %q, want %q", received, payload)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client Close() error = %v", err)
	}
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool {
		return snapshot.CompletedConnections == 1
	})
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	snapshot := relay.Snapshot()
	if snapshot.Running || snapshot.ActiveConnections != 0 || snapshot.AcceptedConnections != 1 || snapshot.CompletedConnections != 1 {
		t.Fatalf("relay lifecycle snapshot = %+v", snapshot)
	}
	if snapshot.ClientBytes != uint64(len(payload)) || snapshot.UpstreamBytes != uint64(len(payload)) {
		t.Fatalf("relay byte counters = %+v", snapshot)
	}
}

// TestRelayPublishesBytesBeforeConnectionClose keeps long-lived service telemetry current.
func TestRelayPublishesBytesBeforeConnectionClose(t *testing.T) {
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	backend := newTestBackend(t, func(connection net.Conn) {
		request := make([]byte, len("request"))
		if _, err := io.ReadFull(connection, request); err != nil {
			return
		}
		if _, err := connection.Write([]byte("response")); err != nil {
			return
		}
		<-release
	})
	defer backend.Close()
	defer released()
	relay := mustTestRelay(t, backend.Address(), nil)
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)

	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	defer client.Close()
	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("client Write() error = %v", err)
	}
	if _, err := io.ReadFull(client, make([]byte, len("response"))); err != nil {
		t.Fatalf("client ReadFull() error = %v", err)
	}
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool {
		return snapshot.ActiveConnections == 1 &&
			snapshot.ClientBytes == uint64(len("request")) &&
			snapshot.UpstreamBytes == uint64(len("response"))
	})
	released()
}

// TestRelayPreservesHalfClose verifies protocols can send EOF before reading their response.
func TestRelayPreservesHalfClose(t *testing.T) {
	backend := newTestBackend(t, func(connection net.Conn) {
		request, err := io.ReadAll(connection)
		if err != nil {
			return
		}
		_, _ = connection.Write(append([]byte("reply:"), request...))
	})
	defer backend.Close()
	relay := mustTestRelay(t, backend.Address(), nil)
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)

	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("client Write() error = %v", err)
	}
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("client CloseWrite() error = %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("client ReadAll() error = %v", err)
	}
	if string(response) != "reply:request" {
		t.Fatalf("half-close response = %q", response)
	}
	_ = client.Close()
}

// TestRelayUpdateMovesOnlyNewConnections proves an upstream change never cuts an established stream.
func TestRelayUpdateMovesOnlyNewConnections(t *testing.T) {
	first := newTestBackend(t, prefixedConnection("first:"))
	defer first.Close()
	second := newTestBackend(t, prefixedConnection("second:"))
	defer second.Close()
	relay := mustTestRelay(t, first.Address(), nil)
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)

	firstClient := dialTestRelay(t, relay.Snapshot().ListenAddress)
	if got := exchangeFrame(t, firstClient, "before"); got != "first:before" {
		t.Fatalf("first response = %q", got)
	}
	if err := relay.UpdateUpstream(second.Address()); err != nil {
		t.Fatalf("UpdateUpstream() error = %v", err)
	}
	if got := exchangeFrame(t, firstClient, "existing"); got != "first:existing" {
		t.Fatalf("existing response = %q", got)
	}
	secondClient := dialTestRelay(t, relay.Snapshot().ListenAddress)
	if got := exchangeFrame(t, secondClient, "new"); got != "second:new" {
		t.Fatalf("new response = %q", got)
	}
	_ = firstClient.Close()
	_ = secondClient.Close()
	if relay.Snapshot().Upstream != second.Address() {
		t.Fatalf("snapshot upstream = %s, want %s", relay.Snapshot().Upstream, second.Address())
	}
}

// TestRelayBoundsAdmission verifies the listener does not admit a second user-space connection early.
func TestRelayBoundsAdmission(t *testing.T) {
	release := make(chan struct{})
	backend := newTestBackend(t, func(connection net.Conn) {
		<-release
	})
	defer backend.Close()
	config := validTestConfig(backend.Address())
	config.MaxConnections = 1
	relay, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)

	first := dialTestRelay(t, relay.Snapshot().ListenAddress)
	backend.WaitAccepted(t)
	second := dialTestRelay(t, relay.Snapshot().ListenAddress)
	time.Sleep(30 * time.Millisecond)
	if snapshot := relay.Snapshot(); snapshot.ActiveConnections != 1 || snapshot.AcceptedConnections != 1 {
		t.Fatalf("bounded snapshot = %+v", snapshot)
	}
	close(release)
	_ = first.Close()
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool {
		return snapshot.AcceptedConnections == 2
	})
	_ = second.Close()
}

// TestRelayShutdownAllowsDrain verifies cancellation stops admission before interrupting healthy work.
func TestRelayShutdownAllowsDrain(t *testing.T) {
	received := make(chan struct{})
	backend := newTestBackend(t, func(connection net.Conn) {
		request := make([]byte, len("work"))
		if _, err := io.ReadFull(connection, request); err != nil {
			return
		}
		close(received)
		remainder, _ := io.ReadAll(connection)
		request = append(request, remainder...)
		_, _ = connection.Write(append([]byte("drained:"), request...))
	})
	defer backend.Close()
	config := validTestConfig(backend.Address())
	config.ShutdownTimeout = time.Second
	relay, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer cancel()
	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	if _, err := client.Write([]byte("work")); err != nil {
		t.Fatalf("client Write() error = %v", err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("backend did not receive established work")
	}
	cancel()
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("client CloseWrite() error = %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil || string(response) != "drained:work" {
		t.Fatalf("drained response = %q, error = %v", response, err)
	}
	_ = client.Close()
	if err := <-result; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

// TestRelayShutdownForcesIdleConnections verifies the configured deadline bounds daemon teardown.
func TestRelayShutdownForcesIdleConnections(t *testing.T) {
	backend := newTestBackend(t, func(connection net.Conn) {
		_, _ = io.Copy(io.Discard, connection)
	})
	defer backend.Close()
	config := validTestConfig(backend.Address())
	config.ShutdownTimeout = 20 * time.Millisecond
	relay, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	backend.WaitAccepted(t)
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not enforce its shutdown deadline")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle client remained open after forced shutdown")
	}
	_ = client.Close()
}

// TestRelayContainsDialFailures verifies one unavailable project cannot relinquish relay authority.
func TestRelayContainsDialFailures(t *testing.T) {
	unavailable := reserveClosedAddress(t)
	errorsSeen := make(chan error, 1)
	relay := mustTestRelay(t, unavailable, func(err error) {
		errorsSeen <- err
	})
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)

	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = client.Read(make([]byte, 1))
	_ = client.Close()
	select {
	case observed := <-errorsSeen:
		if !strings.Contains(observed.Error(), unavailable.String()) {
			t.Fatalf("observed error = %v", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("dial failure was not observed")
	}
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool {
		return snapshot.DialFailures == 1 && snapshot.CompletedConnections == 1
	})
}

// TestRelayHandlesNilDialResult verifies an invalid network adapter result is isolated to its client.
func TestRelayHandlesNilDialResult(t *testing.T) {
	errorsSeen := make(chan error, 1)
	config := validTestConfig(netip.MustParseAddrPort("127.0.0.1:43101"))
	config.ObserveError = func(err error) { errorsSeen <- err }
	relay, err := newRelay(config, func(context.Context, string, string) (net.Conn, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("newRelay() error = %v", err)
	}
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)
	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	defer client.Close()
	select {
	case observed := <-errorsSeen:
		if !strings.Contains(observed.Error(), "no connection") {
			t.Fatalf("observed error = %v", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("nil dial result was not observed")
	}
}

// TestRelayClosesConnectionReturnedWithDialError contains invalid adapter results without leaks.
func TestRelayClosesConnectionReturnedWithDialError(t *testing.T) {
	upstream, peer := net.Pipe()
	defer peer.Close()
	errorsSeen := make(chan error, 1)
	config := validTestConfig(netip.MustParseAddrPort("127.0.0.1:43101"))
	config.ObserveError = func(err error) { errorsSeen <- err }
	relay, err := newRelay(config, func(context.Context, string, string) (net.Conn, error) {
		return upstream, errors.New("controlled dial failure")
	})
	if err != nil {
		t.Fatalf("newRelay() error = %v", err)
	}
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)
	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	defer client.Close()
	select {
	case <-errorsSeen:
	case <-time.After(time.Second):
		t.Fatal("dial error was not observed")
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err == nil {
		if _, err := peer.Read(make([]byte, 1)); err == nil {
			t.Fatal("connection returned with dial error remained open")
		}
	}
}

// TestRelayClosesLateDialResults prevents a cancellation race from leaking an upstream socket.
func TestRelayClosesLateDialResults(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	peerReady := make(chan net.Conn, 1)
	config := validTestConfig(netip.MustParseAddrPort("127.0.0.1:43101"))
	config.ShutdownTimeout = 20 * time.Millisecond
	config.ConnectTimeout = time.Second
	relay, err := newRelay(config, func(context.Context, string, string) (net.Conn, error) {
		close(entered)
		<-release
		upstream, peer := net.Pipe()
		peerReady <- peer
		return upstream, nil
	})
	if err != nil {
		t.Fatalf("newRelay() error = %v", err)
	}
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	defer client.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("relay did not enter controlled dial")
	}
	cancel()
	time.Sleep(2 * config.ShutdownTimeout)
	close(release)
	peer := <-peerReady
	defer peer.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not join the late dial")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("late upstream result remained open after shutdown")
	}
}

// TestConnectionPairCancelsWorkRegisteredAfterClosure covers the narrow pre-dial shutdown race.
func TestConnectionPairCancelsWorkRegisteredAfterClosure(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	pair := newConnectionPair(client)
	pair.Close()
	cancelled := make(chan struct{})
	pair.setCancel(func() {
		close(cancelled)
	})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("late cancellation hook was not invoked")
	}
}

// TestRelayLifecycleRejectsInvalidAndRepeatedServe covers listener ownership failure paths.
func TestRelayLifecycleRejectsInvalidAndRepeatedServe(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()

	t.Run("nil listener", func(t *testing.T) {
		relay := mustTestRelay(t, backend.Address(), nil)
		if err := relay.Serve(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("Serve() error = %v", err)
		}
	})

	t.Run("invalid listener address", func(t *testing.T) {
		relay := mustTestRelay(t, backend.Address(), nil)
		listener := &stubListener{address: stubAddress("not-an-address")}
		if err := relay.Serve(context.Background(), listener); err == nil || !strings.Contains(err.Error(), "not an IP socket") {
			t.Fatalf("Serve() error = %v", err)
		}
		if !listener.closed {
			t.Fatal("invalid listener was not closed")
		}
	})

	t.Run("non-loopback listener", func(t *testing.T) {
		relay := mustTestRelay(t, backend.Address(), nil)
		listener := &stubListener{address: stubAddress("192.0.2.1:43101")}
		if err := relay.Serve(context.Background(), listener); err == nil || !strings.Contains(err.Error(), "IPv4 loopback") {
			t.Fatalf("Serve() error = %v", err)
		}
	})

	t.Run("listener equals upstream", func(t *testing.T) {
		listener := listenLoopback(t)
		address, err := netip.ParseAddrPort(listener.Addr().String())
		if err != nil {
			t.Fatalf("ParseAddrPort() error = %v", err)
		}
		relay := mustTestRelay(t, address, nil)
		if err := relay.Serve(context.Background(), listener); err == nil || !strings.Contains(err.Error(), "cannot equal") {
			t.Fatalf("Serve() error = %v, want self-route rejection", err)
		}
		if _, err := net.DialTimeout("tcp4", address.String(), 20*time.Millisecond); err == nil {
			t.Fatal("self-routed listener remained open")
		}
	})

	t.Run("second Serve", func(t *testing.T) {
		relay := mustTestRelay(t, backend.Address(), nil)
		first := listenLoopback(t)
		cancel, result := startTestRelay(t, relay, first)
		second := listenLoopback(t)
		if err := relay.Serve(context.Background(), second); err == nil || !strings.Contains(err.Error(), "already started") {
			t.Fatalf("second Serve() error = %v", err)
		}
		finishTestRelay(t, cancel, result)
		third := listenLoopback(t)
		if err := relay.Serve(context.Background(), third); err == nil || !strings.Contains(err.Error(), "already started") {
			t.Fatalf("third Serve() error = %v", err)
		}
	})
}

// TestRelayReturnsUnexpectedListenerClosure prevents a vanished route from looking orderly.
func TestRelayReturnsUnexpectedListenerClosure(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()
	relay := mustTestRelay(t, backend.Address(), nil)
	listener := listenLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- relay.Serve(ctx, listener)
	}()
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool { return snapshot.Running })
	if err := listener.Close(); err != nil {
		t.Fatalf("listener Close() error = %v", err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "accept TCP relay connection") {
			t.Fatalf("Serve() error = %v, want terminal accept error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not report unexpected listener closure")
	}
}

// TestRelayReturnsEndpointAcceptFailures distinguishes a damaged listener from one failed client.
func TestRelayReturnsEndpointAcceptFailures(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()
	tests := []struct {
		name   string
		accept func() (net.Conn, error)
		want   string
	}{
		{name: "accept error", accept: func() (net.Conn, error) { return nil, errors.New("endpoint failed") }, want: "endpoint failed"},
		{name: "nil connection", accept: func() (net.Conn, error) { return nil, nil }, want: "no connection"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			relay := mustTestRelay(t, backend.Address(), nil)
			listener := &stubListener{address: stubAddress("127.0.0.1:43101"), accept: test.accept}
			if err := relay.Serve(context.Background(), listener); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Serve() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRelayRetriesTemporaryAcceptFailures verifies transient endpoint pressure does not drop the route.
func TestRelayRetriesTemporaryAcceptFailures(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()
	errorsSeen := make(chan error, 1)
	relay := mustTestRelay(t, backend.Address(), func(err error) {
		errorsSeen <- err
	})
	attempts := 0
	listener := &stubListener{
		address: stubAddress("127.0.0.1:43101"),
		accept: func() (net.Conn, error) {
			attempts++
			if attempts == 1 {
				return nil, temporaryAcceptError{}
			}
			return nil, nil
		},
	}
	if err := relay.Serve(context.Background(), listener); err == nil || !strings.Contains(err.Error(), "no connection") {
		t.Fatalf("Serve() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("Accept() attempts = %d, want 2", attempts)
	}
	select {
	case observed := <-errorsSeen:
		if !strings.Contains(observed.Error(), "retry TCP relay accept") {
			t.Fatalf("observed error = %v", observed)
		}
	default:
		t.Fatal("temporary accept failure was not observed")
	}
}

// TestRelayCancellationInterruptsAcceptBackoff keeps shutdown responsive under endpoint pressure.
func TestRelayCancellationInterruptsAcceptBackoff(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()
	observed := make(chan struct{}, 1)
	relay := mustTestRelay(t, backend.Address(), func(error) {
		select {
		case observed <- struct{}{}:
		default:
		}
	})
	listener := &stubListener{
		address: stubAddress("127.0.0.1:43101"),
		accept:  func() (net.Conn, error) { return nil, temporaryAcceptError{} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- relay.Serve(ctx, listener)
	}()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("relay did not enter accept backoff")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accept backoff ignored cancellation")
	}
}

// TestRelayConcurrentObservationsAndUpdates exercises the lock-free hot-path state under load.
func TestRelayConcurrentObservationsAndUpdates(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()
	relay := mustTestRelay(t, backend.Address(), nil)
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)

	var wait sync.WaitGroup
	for worker := 0; worker < 20; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 10; attempt++ {
				_ = relay.UpdateUpstream(backend.Address())
				_ = relay.Snapshot()
				client, err := net.DialTimeout("tcp4", relay.Snapshot().ListenAddress.String(), time.Second)
				if err != nil {
					return
				}
				_, _ = client.Write([]byte("x"))
				_, _ = io.ReadFull(client, make([]byte, 1))
				_ = client.Close()
			}
		}()
	}
	wait.Wait()
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool {
		return snapshot.ActiveConnections == 0
	})
}

// TestRelayUpdateRejectsUnsafeEndpoints keeps route replacement within Harbor-owned loopback.
func TestRelayUpdateRejectsUnsafeEndpoints(t *testing.T) {
	relay := mustTestRelay(t, netip.MustParseAddrPort("127.0.0.1:43101"), nil)
	for _, endpoint := range []netip.AddrPort{
		{},
		netip.MustParseAddrPort("127.0.0.1:0"),
		netip.MustParseAddrPort("192.0.2.1:43101"),
		netip.MustParseAddrPort("[::1]:43101"),
	} {
		if err := relay.UpdateUpstream(endpoint); err == nil {
			t.Fatalf("UpdateUpstream(%s) unexpectedly succeeded", endpoint)
		}
	}
}

// TestRelayUpdateRejectsActiveListener prevents route replacement from creating a relay cycle.
func TestRelayUpdateRejectsActiveListener(t *testing.T) {
	backend := newTestBackend(t, echoConnection)
	defer backend.Close()
	relay := mustTestRelay(t, backend.Address(), nil)
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer finishTestRelay(t, cancel, result)

	listenAddress := relay.Snapshot().ListenAddress
	if err := relay.UpdateUpstream(listenAddress); err == nil || !strings.Contains(err.Error(), "cannot equal") {
		t.Fatalf("UpdateUpstream(listener) error = %v, want self-route rejection", err)
	}
	if upstream := relay.Snapshot().Upstream; upstream != backend.Address() {
		t.Fatalf("upstream after rejected update = %s, want %s", upstream, backend.Address())
	}
}

// TestRelayContainsObserverPanics proves diagnostics cannot stop connection handling.
func TestRelayContainsObserverPanics(t *testing.T) {
	relay := mustTestRelay(t, reserveClosedAddress(t), func(error) {
		panic("observer failed")
	})
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	_ = client.Close()
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool {
		return snapshot.CompletedConnections == 1
	})
	finishTestRelay(t, cancel, result)
}

// TestRelayContainsBlockingObserver proves optional diagnostics cannot delay shutdown or admission.
func TestRelayContainsBlockingObserver(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	t.Cleanup(released)
	var enteredOnce sync.Once
	relay := mustTestRelay(t, reserveClosedAddress(t), func(error) {
		enteredOnce.Do(func() { close(entered) })
		<-release
	})
	listener := listenLoopback(t)
	cancel, result := startTestRelay(t, relay, listener)
	defer cancel()
	client := dialTestRelay(t, relay.Snapshot().ListenAddress)
	defer client.Close()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not receive the controlled dial failure")
	}
	for index := 0; index < maximumPendingDiagnostics+1; index++ {
		relay.observe(fmt.Errorf("queued diagnostic %d", index))
	}
	if dropped := relay.Snapshot().DroppedDiagnostics; dropped == 0 {
		t.Fatal("blocking observer did not produce bounded diagnostic drops")
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocking observer prevented relay shutdown")
	}
	beforeLateDrop := relay.Snapshot().DroppedDiagnostics
	relay.observe(errors.New("late diagnostic"))
	if dropped := relay.Snapshot().DroppedDiagnostics; dropped != beforeLateDrop+1 {
		t.Fatalf("late diagnostic drops = %d, want %d", dropped, beforeLateDrop+1)
	}
	released()
}

// TestConfigureKeepAliveAcceptsNonTCPSockets keeps deterministic adapters usable in unit tests.
func TestConfigureKeepAliveAcceptsNonTCPSockets(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := configureKeepAlive(left, time.Second); err != nil {
		t.Fatalf("configureKeepAlive() error = %v", err)
	}
}

// TestRelayErrorHelpersClassifyExpectedShutdown keeps teardown noise out of product diagnostics.
func TestRelayErrorHelpersClassifyExpectedShutdown(t *testing.T) {
	if err := expectedCopyError(nil); err != nil {
		t.Fatalf("expectedCopyError(nil) = %v", err)
	}
	if err := expectedCopyError(net.ErrClosed); err != nil {
		t.Fatalf("expectedCopyError(net.ErrClosed) = %v", err)
	}
	want := errors.New("payload failed")
	if err := expectedCopyError(want); !errors.Is(err, want) {
		t.Fatalf("expectedCopyError() = %v, want payload error", err)
	}
	relay := mustTestRelay(t, netip.MustParseAddrPort("127.0.0.1:43101"), nil)
	relay.observe(nil)
	relay.observe(want)
}

// TestAcceptBackoffCapsAndHonorsCancellation covers retry arithmetic without slowing the suite.
func TestAcceptBackoffCapsAndHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForAcceptRetry(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForAcceptRetry() error = %v, want context.Canceled", err)
	}
}

// Network identifies the controlled fixture address family.
func (address stubAddress) Network() string {
	return "stub"
}

// String returns the exact controlled fixture address.
func (address stubAddress) String() string {
	return string(address)
}

// Error describes the controlled temporary accept failure.
func (temporaryAcceptError) Error() string {
	return "temporary accept failure"
}

// Timeout reports that the fixture represents resource pressure rather than a deadline.
func (temporaryAcceptError) Timeout() bool {
	return false
}

// Temporary asks the relay to retry the controlled failure.
func (temporaryAcceptError) Temporary() bool {
	return true
}

// Accept delegates to the configured fixture behavior.
func (listener *stubListener) Accept() (net.Conn, error) {
	return listener.accept()
}

// Close records listener ownership release.
func (listener *stubListener) Close() error {
	listener.closed = true
	return nil
}

// Addr returns the controlled fixture address.
func (listener *stubListener) Addr() net.Addr {
	return listener.address
}

// newTestBackend starts a bounded loopback fixture for real relay protocol tests.
func newTestBackend(t *testing.T, handler func(net.Conn)) *testBackend {
	t.Helper()
	listener := listenLoopback(t)
	backend := &testBackend{
		listener:    listener,
		handler:     handler,
		accepted:    make(chan struct{}, 256),
		connections: make(map[net.Conn]struct{}),
		closed:      make(chan struct{}),
	}
	backend.wait.Add(1)
	go backend.acceptLoop()
	return backend
}

// acceptLoop owns fixture connections until Close releases the backend.
func (backend *testBackend) acceptLoop() {
	defer backend.wait.Done()
	for {
		connection, err := backend.listener.Accept()
		if err != nil {
			return
		}
		backend.mutex.Lock()
		backend.connections[connection] = struct{}{}
		backend.mutex.Unlock()
		select {
		case backend.accepted <- struct{}{}:
		default:
		}
		backend.wait.Add(1)
		go backend.serve(connection)
	}
}

// serve runs one fixture handler and guarantees connection ownership is released.
func (backend *testBackend) serve(connection net.Conn) {
	defer backend.wait.Done()
	defer connection.Close()
	defer func() {
		backend.mutex.Lock()
		delete(backend.connections, connection)
		backend.mutex.Unlock()
	}()
	backend.handler(connection)
}

// Address returns the backend's exact loopback endpoint.
func (backend *testBackend) Address() netip.AddrPort {
	address, _ := netip.ParseAddrPort(backend.listener.Addr().String())
	return canonicalEndpoint(address)
}

// WaitAccepted blocks until the backend observes one more connection.
func (backend *testBackend) WaitAccepted(t *testing.T) {
	t.Helper()
	select {
	case <-backend.accepted:
	case <-time.After(time.Second):
		t.Fatal("backend did not accept a connection")
	}
}

// Close releases all fixture sockets before joining their goroutines.
func (backend *testBackend) Close() {
	select {
	case <-backend.closed:
		return
	default:
		close(backend.closed)
	}
	_ = backend.listener.Close()
	backend.mutex.Lock()
	connections := make([]net.Conn, 0, len(backend.connections))
	for connection := range backend.connections {
		connections = append(connections, connection)
	}
	backend.mutex.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	backend.wait.Wait()
}

// echoConnection mirrors bytes without assigning meaning to their framing.
func echoConnection(connection net.Conn) {
	_, _ = io.Copy(connection, connection)
}

// prefixedConnection returns a one-byte-frame fixture that identifies its backend.
func prefixedConnection(prefix string) func(net.Conn) {
	return func(connection net.Conn) {
		buffer := make([]byte, 256)
		for {
			count, err := connection.Read(buffer)
			if count > 0 {
				_, _ = connection.Write(append([]byte(prefix), buffer[:count]...))
			}
			if err != nil {
				return
			}
		}
	}
}

// exchangeFrame writes one short frame and reads the prefixed response expected by update tests.
func exchangeFrame(t *testing.T, connection net.Conn, frame string) string {
	t.Helper()
	if _, err := connection.Write([]byte(frame)); err != nil {
		t.Fatalf("connection Write() error = %v", err)
	}
	response := make([]byte, len("second:")+len(frame))
	if strings.HasPrefix(frame, "before") || strings.HasPrefix(frame, "existing") {
		response = make([]byte, len("first:")+len(frame))
	}
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatalf("connection ReadFull() error = %v", err)
	}
	return string(response)
}

// validTestConfig keeps real-network tests fast without weakening production defaults.
func validTestConfig(upstream netip.AddrPort) Config {
	return Config{
		Upstream:        upstream,
		MaxConnections:  8,
		ConnectTimeout:  100 * time.Millisecond,
		ShutdownTimeout: 100 * time.Millisecond,
		KeepAlive:       time.Second,
	}
}

// mustTestRelay constructs one relay and fails at the test boundary.
func mustTestRelay(t *testing.T, upstream netip.AddrPort, observer ErrorObserver) *Relay {
	t.Helper()
	config := validTestConfig(upstream)
	config.ObserveError = observer
	relay, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return relay
}

// listenLoopback creates an ephemeral socket before the relay receives ownership.
func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	return listener
}

// reserveClosedAddress returns a currently unused loopback endpoint for deterministic dial refusal.
func reserveClosedAddress(t *testing.T) netip.AddrPort {
	t.Helper()
	listener := listenLoopback(t)
	address, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("ParseAddrPort() error = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener Close() error = %v", err)
	}
	return canonicalEndpoint(address)
}

// startTestRelay runs Serve and waits until the socket is visible in its snapshot.
func startTestRelay(t *testing.T, relay *Relay, listener net.Listener) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- relay.Serve(ctx, listener)
	}()
	waitForSnapshot(t, relay, func(snapshot Snapshot) bool {
		return snapshot.Running && snapshot.ListenAddress.IsValid()
	})
	return cancel, result
}

// finishTestRelay cancels and joins one fixture relay.
func finishTestRelay(t *testing.T, cancel context.CancelFunc, result <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop")
	}
}

// dialTestRelay opens a real client connection to the exact published listener.
func dialTestRelay(t *testing.T, address netip.AddrPort) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", address.String(), time.Second)
	if err != nil {
		t.Fatalf("net.DialTimeout() error = %v", err)
	}
	return connection
}

// waitForSnapshot polls only in tests where a network goroutine publishes state asynchronously.
func waitForSnapshot(t *testing.T, relay *Relay, condition func(Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition(relay.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("relay condition not reached; snapshot = %+v", relay.Snapshot())
}
