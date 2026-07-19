package ingressrelay

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

	"github.com/goforj/harbor/internal/network/tcprelay"
)

// reportedListener keeps a real test socket while exposing the launchd address being validated.
type reportedListener struct {
	net.Listener
	reported net.Addr
}

// fixedAddress supplies one controlled listener address.
type fixedAddress string

// failingListener exposes one terminal Accept failure at an exact public address.
type failingListener struct {
	address net.Addr
	err     error
	closed  bool
	mutex   sync.Mutex
}

// testBackend owns one private loopback target for paired forwarding tests.
type testBackend struct {
	listener net.Listener
	prefix   string
	wait     sync.WaitGroup
}

// TestNewRejectsInvalidPairedRoutes covers exact address, privilege, uniqueness, and timeout boundaries.
func TestNewRejectsInvalidPairedRoutes(t *testing.T) {
	valid := Config{
		HTTPUpstream:  netip.MustParseAddrPort("127.0.0.1:18080"),
		HTTPSUpstream: netip.MustParseAddrPort("127.0.0.1:18443"),
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "missing HTTP", mutate: func(config *Config) { config.HTTPUpstream = netip.AddrPort{} }, want: "HTTP upstream"},
		{name: "missing HTTPS", mutate: func(config *Config) { config.HTTPSUpstream = netip.AddrPort{} }, want: "HTTPS upstream"},
		{name: "other loopback", mutate: func(config *Config) { config.HTTPUpstream = netip.MustParseAddrPort("127.0.0.2:18080") }, want: "127.0.0.1"},
		{name: "public address", mutate: func(config *Config) { config.HTTPUpstream = netip.MustParseAddrPort("192.0.2.1:18080") }, want: "127.0.0.1"},
		{name: "IPv6", mutate: func(config *Config) { config.HTTPSUpstream = netip.MustParseAddrPort("[::1]:18443") }, want: "127.0.0.1"},
		{name: "mapped address", mutate: func(config *Config) { config.HTTPSUpstream = netip.MustParseAddrPort("[::ffff:127.0.0.1]:18443") }, want: "127.0.0.1"},
		{name: "privileged HTTP", mutate: func(config *Config) { config.HTTPUpstream = netip.MustParseAddrPort("127.0.0.1:80") }, want: "at least 1024"},
		{name: "same target", mutate: func(config *Config) { config.HTTPSUpstream = config.HTTPUpstream }, want: "distinct"},
		{name: "invalid shutdown", mutate: func(config *Config) { config.ShutdownTimeout = time.Nanosecond }, want: "shutdown timeout"},
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

// TestNewRuntimePreservesRawRelayConstructionFailures covers both defensive factory boundaries.
func TestNewRuntimePreservesRawRelayConstructionFailures(t *testing.T) {
	config := Config{
		HTTPUpstream:  netip.MustParseAddrPort("127.0.0.1:18080"),
		HTTPSUpstream: netip.MustParseAddrPort("127.0.0.1:18443"),
	}
	sentinel := errors.New("relay construction failed")
	for _, failAt := range []int{1, 2} {
		calls := 0
		_, err := newRuntime(config, func(config tcprelay.Config) (*tcprelay.Relay, error) {
			calls++
			if calls == failAt {
				return nil, sentinel
			}
			return tcprelay.New(config)
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("newRuntime() failure %d error = %v", failAt, err)
		}
	}
}

// TestRuntimeForwardsBothProtocolsAndStopsTogether proves the paired happy path and payload-free counters.
func TestRuntimeForwardsBothProtocolsAndStopsTogether(t *testing.T) {
	httpBackend := newTestBackend(t, "http:")
	defer httpBackend.Close()
	httpsBackend := newTestBackend(t, "https:")
	defer httpsBackend.Close()
	runtime := mustRuntime(t, httpBackend.Address(), httpsBackend.Address())
	httpPublic, httpAddress := newReportedListener(t, "127.0.0.1:80")
	httpsPublic, httpsAddress := newReportedListener(t, "127.0.0.1:443")
	cancel, result := startRuntime(t, runtime, Listeners{HTTP: httpPublic, HTTPS: httpsPublic})

	if got := exchange(t, httpAddress, "one"); got != "http:one" {
		t.Fatalf("HTTP response = %q", got)
	}
	if got := exchange(t, httpsAddress, "two"); got != "https:two" {
		t.Fatalf("HTTPS response = %q", got)
	}
	waitForSnapshot(t, runtime, func(snapshot Snapshot) bool {
		return snapshot.HTTP.CompletedConnections == 1 && snapshot.HTTPS.CompletedConnections == 1
	})
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.Running || snapshot.HTTP.Running || snapshot.HTTPS.Running {
		t.Fatalf("terminal snapshot = %+v", snapshot)
	}
	if snapshot.HTTP.ClientBytes != 3 || snapshot.HTTPS.ClientBytes != 3 {
		t.Fatalf("relay byte counters = %+v", snapshot)
	}
}

// TestRuntimeRejectsInvalidListenerPairs proves both capabilities close on every pre-start failure.
func TestRuntimeRejectsInvalidListenerPairs(t *testing.T) {
	tests := []struct {
		name      string
		listeners func(*testing.T) (Listeners, *failingListener, *failingListener)
		want      string
	}{
		{
			name: "missing HTTP",
			listeners: func(t *testing.T) (Listeners, *failingListener, *failingListener) {
				https := newFailingListener("127.0.0.1:443", errors.New("unused"))
				return Listeners{HTTPS: https}, nil, https
			},
			want: "HTTP listener is required",
		},
		{
			name: "missing HTTPS",
			listeners: func(t *testing.T) (Listeners, *failingListener, *failingListener) {
				http := newFailingListener("127.0.0.1:80", errors.New("unused"))
				return Listeners{HTTP: http}, http, nil
			},
			want: "HTTPS listener is required",
		},
		{
			name: "wrong HTTP",
			listeners: func(t *testing.T) (Listeners, *failingListener, *failingListener) {
				http := newFailingListener("127.0.0.1:8080", errors.New("unused"))
				https := newFailingListener("127.0.0.1:443", errors.New("unused"))
				return Listeners{HTTP: http, HTTPS: https}, http, https
			},
			want: "want exactly 127.0.0.1:80",
		},
		{
			name: "invalid HTTPS address",
			listeners: func(t *testing.T) (Listeners, *failingListener, *failingListener) {
				http := newFailingListener("127.0.0.1:80", errors.New("unused"))
				https := newFailingListener("not-an-address", errors.New("unused"))
				return Listeners{HTTP: http, HTTPS: https}, http, https
			},
			want: "not an IP socket",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := mustRuntime(
				t,
				netip.MustParseAddrPort("127.0.0.1:18080"),
				netip.MustParseAddrPort("127.0.0.1:18443"),
			)
			listeners, httpListener, httpsListener := test.listeners(t)
			if err := runtime.Serve(t.Context(), listeners); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Serve() error = %v, want containing %q", err, test.want)
			}
			for name, listener := range map[string]*failingListener{"HTTP": httpListener, "HTTPS": httpsListener} {
				if listener != nil && !listener.Closed() {
					t.Fatalf("%s listener was not closed", name)
				}
			}
		})
	}
}

// TestRuntimeTreatsOneSidedExitAsFatal proves a damaged socket cancels and joins its sibling.
func TestRuntimeTreatsOneSidedExitAsFatal(t *testing.T) {
	runtime := mustRuntime(
		t,
		netip.MustParseAddrPort("127.0.0.1:18080"),
		netip.MustParseAddrPort("127.0.0.1:18443"),
	)
	sentinel := errors.New("HTTP socket failed")
	http := newFailingListener("127.0.0.1:80", sentinel)
	https, _ := newReportedListener(t, "127.0.0.1:443")
	err := runtime.Serve(nil, Listeners{HTTP: http, HTTPS: https})
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "serve ingress HTTP relay") {
		t.Fatalf("Serve() error = %v, want HTTP terminal failure", err)
	}
	if runtime.Snapshot().Running {
		t.Fatal("one-sided failure retained a running pair")
	}
}

// TestRuntimeLifecycleIsSingleUse proves listener capability cannot cross generations.
func TestRuntimeLifecycleIsSingleUse(t *testing.T) {
	runtime := mustRuntime(
		t,
		netip.MustParseAddrPort("127.0.0.1:18080"),
		netip.MustParseAddrPort("127.0.0.1:18443"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	http, _ := newReportedListener(t, "127.0.0.1:80")
	https, _ := newReportedListener(t, "127.0.0.1:443")
	if err := runtime.Serve(ctx, Listeners{HTTP: http, HTTPS: https}); err != nil {
		t.Fatalf("first Serve() error = %v", err)
	}
	secondHTTP := newFailingListener("127.0.0.1:80", errors.New("unused"))
	secondHTTPS := newFailingListener("127.0.0.1:443", errors.New("unused"))
	err := runtime.Serve(context.Background(), Listeners{HTTP: secondHTTP, HTTPS: secondHTTPS})
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("second Serve() error = %v", err)
	}
	if !secondHTTP.Closed() || !secondHTTPS.Closed() {
		t.Fatal("rejected second Serve() retained supplied listeners")
	}
}

// TestGatedListenerBlocksAcceptUntilPairRelease pins the no-one-sided-admission primitive.
func TestGatedListenerBlocksAcceptUntilPairRelease(t *testing.T) {
	underlying, address := newReportedListener(t, "127.0.0.1:80")
	defer underlying.Close()
	gate := make(chan struct{})
	listener := &gatedListener{Listener: underlying, gate: gate}
	accepted := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if connection != nil {
			_ = connection.Close()
		}
		accepted <- err
	}()
	client, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("dial gated listener: %v", err)
	}
	defer client.Close()
	select {
	case err := <-accepted:
		t.Fatalf("Accept() crossed a closed gate: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(gate)
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("Accept() after gate error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept() remained blocked after pair release")
	}
}

// TestListenerAddressRejectsNoncanonicalSockets covers native adapter output outside the fixed contract.
func TestListenerAddressRejectsNoncanonicalSockets(t *testing.T) {
	tests := []struct {
		name    string
		address net.Addr
		want    string
	}{
		{name: "missing", address: nil, want: "no address"},
		{name: "public", address: fixedAddress("192.0.2.1:80"), want: "not canonical IPv4 loopback"},
		{name: "IPv6", address: fixedAddress("[::1]:80"), want: "not canonical IPv4 loopback"},
		{name: "mapped", address: fixedAddress("[::ffff:127.0.0.1]:80"), want: "not canonical IPv4 loopback"},
		{name: "zero port", address: fixedAddress("127.0.0.1:0"), want: "not canonical IPv4 loopback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listener := newFailingListener("127.0.0.1:80", errors.New("unused"))
			listener.address = test.address
			if _, err := listenerAddress(listener); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("listenerAddress() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestWaitForPairHonorsPreStartCancellation covers shutdown before both relay goroutines begin.
func TestWaitForPairHonorsPreStartCancellation(t *testing.T) {
	runtime := mustRuntime(
		t,
		netip.MustParseAddrPort("127.0.0.1:18080"),
		netip.MustParseAddrPort("127.0.0.1:18443"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, bothRunning, received := runtime.waitForPair(ctx, make(chan relayResult))
	if result != (relayResult{}) || bothRunning || received {
		t.Fatalf("waitForPair() = (%+v, %t, %t)", result, bothRunning, received)
	}
}

// Network identifies a controlled address as a test-only transport endpoint.
func (address fixedAddress) Network() string {
	return "test"
}

// String returns the exact controlled address text.
func (address fixedAddress) String() string {
	return string(address)
}

// Addr substitutes the launchd-owned socket identity without changing the real test socket.
func (listener *reportedListener) Addr() net.Addr {
	return listener.reported
}

// Accept returns the terminal controlled listener error.
func (listener *failingListener) Accept() (net.Conn, error) {
	return nil, listener.err
}

// Close records release of the controlled listener capability.
func (listener *failingListener) Close() error {
	listener.mutex.Lock()
	listener.closed = true
	listener.mutex.Unlock()
	return nil
}

// Addr returns the controlled public socket identity.
func (listener *failingListener) Addr() net.Addr {
	return listener.address
}

// Closed reports whether the runtime released this test capability.
func (listener *failingListener) Closed() bool {
	listener.mutex.Lock()
	defer listener.mutex.Unlock()
	return listener.closed
}

// newFailingListener constructs a terminal listener at one controlled address.
func newFailingListener(address string, err error) *failingListener {
	return &failingListener{address: fixedAddress(address), err: err}
}

// newTestBackend starts one prefixing private target.
func newTestBackend(t *testing.T, prefix string) *testBackend {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend: %v", err)
	}
	backend := &testBackend{listener: listener, prefix: prefix}
	backend.wait.Add(1)
	go backend.serve()
	return backend
}

// serve accepts short test frames until Close releases the backend.
func (backend *testBackend) serve() {
	defer backend.wait.Done()
	for {
		connection, err := backend.listener.Accept()
		if err != nil {
			return
		}
		backend.wait.Add(1)
		go func() {
			defer backend.wait.Done()
			defer connection.Close()
			payload, err := io.ReadAll(connection)
			if err == nil {
				_, _ = connection.Write(append([]byte(backend.prefix), payload...))
			}
		}()
	}
}

// Address returns the private backend endpoint.
func (backend *testBackend) Address() netip.AddrPort {
	address, _ := netip.ParseAddrPort(backend.listener.Addr().String())
	return address
}

// Close releases the listener and joins every backend worker.
func (backend *testBackend) Close() {
	_ = backend.listener.Close()
	backend.wait.Wait()
}

// newReportedListener creates a real ephemeral socket with one controlled public address report.
func newReportedListener(t *testing.T, reported string) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen public fixture: %v", err)
	}
	return &reportedListener{Listener: listener, reported: fixedAddress(reported)}, listener.Addr().String()
}

// mustRuntime constructs the paired runtime or fails its test.
func mustRuntime(t *testing.T, httpUpstream netip.AddrPort, httpsUpstream netip.AddrPort) *Runtime {
	t.Helper()
	runtime, err := New(Config{HTTPUpstream: httpUpstream, HTTPSUpstream: httpsUpstream, ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return runtime
}

// startRuntime runs a valid pair and waits until both relays publish running state.
func startRuntime(t *testing.T, runtime *Runtime, listeners Listeners) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runtime.Serve(ctx, listeners)
	}()
	waitForSnapshot(t, runtime, func(snapshot Snapshot) bool { return snapshot.Running })
	return cancel, result
}

// waitForSnapshot polls only bounded in-memory state until one assertion becomes true.
func waitForSnapshot(t *testing.T, runtime *Runtime, ready func(Snapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready(runtime.Snapshot()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime snapshot did not converge: %+v", runtime.Snapshot())
}

// exchange half-closes one short client frame before reading its response.
func exchange(t *testing.T, address string, payload string) string {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	tcp := connection.(*net.TCPConn)
	defer tcp.Close()
	if _, err := tcp.Write([]byte(payload)); err != nil {
		t.Fatalf("write relay: %v", err)
	}
	if err := tcp.CloseWrite(); err != nil {
		t.Fatalf("half-close relay request: %v", err)
	}
	response, err := io.ReadAll(tcp)
	if err != nil {
		t.Fatalf("read relay: %v", err)
	}
	return string(response)
}

// TestPairedResultClassifiesUnexpectedCleanExit covers the defensive terminal branch directly.
func TestPairedResultClassifiesUnexpectedCleanExit(t *testing.T) {
	err := pairedResult(false, relayResult{name: "HTTP"}, relayResult{name: "HTTPS"})
	if err == nil || !strings.Contains(err.Error(), "HTTP relay stopped unexpectedly") {
		t.Fatalf("pairedResult() error = %v", err)
	}
	if err := pairedResult(true, relayResult{name: "HTTP"}, relayResult{name: "HTTPS"}); err != nil {
		t.Fatalf("pairedResult() cancellation error = %v", err)
	}
	sentinel := errors.New("terminal")
	err = pairedResult(false, relayResult{name: "HTTP", err: sentinel}, relayResult{name: "HTTPS"})
	got := fmt.Sprint(err)
	if !errors.Is(err, sentinel) || !strings.Contains(got, "HTTP") {
		t.Fatalf("pairedResult() wrapped error = %v", err)
	}
	second := errors.New("second terminal")
	err = pairedResult(false, relayResult{name: "HTTP", err: sentinel}, relayResult{name: "HTTPS", err: second})
	if !errors.Is(err, sentinel) || !errors.Is(err, second) {
		t.Fatalf("pairedResult() joined error = %v", err)
	}
}
