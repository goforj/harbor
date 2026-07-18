package httpingress

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewServerValidatesDependenciesAndBounds covers every constructor failure branch.
func TestNewServerValidatesDependenciesAndBounds(t *testing.T) {
	t.Parallel()
	router := mustRouter(t, Config{}, mustSnapshot(t, nil))
	certificate := testCertificateProvider(t, nil)
	if _, err := NewServer(ServerConfig{}, nil, certificate); err == nil || !strings.Contains(err.Error(), "router is required") {
		t.Fatalf("NewServer(nil router) error = %v", err)
	}
	if _, err := NewServer(ServerConfig{}, router, nil); err == nil || !strings.Contains(err.Error(), "certificate provider") {
		t.Fatalf("NewServer(nil certificate) error = %v", err)
	}
	tests := []struct {
		name    string
		config  ServerConfig
		message string
	}{
		{name: "negative read header", config: ServerConfig{ReadHeaderTimeout: -time.Second}, message: "read header timeout"},
		{name: "long read header", config: ServerConfig{ReadHeaderTimeout: maximumReadHeaderTimeout + time.Nanosecond}, message: "read header timeout"},
		{name: "negative idle", config: ServerConfig{IdleTimeout: -time.Second}, message: "client idle timeout"},
		{name: "long idle", config: ServerConfig{IdleTimeout: maximumServerIdleTimeout + time.Nanosecond}, message: "client idle timeout"},
		{name: "negative shutdown", config: ServerConfig{ShutdownTimeout: -time.Second}, message: "shutdown timeout"},
		{name: "long shutdown", config: ServerConfig{ShutdownTimeout: maximumShutdownTimeout + time.Nanosecond}, message: "shutdown timeout"},
		{name: "negative headers", config: ServerConfig{MaxHeaderBytes: -1}, message: "maximum header bytes"},
		{name: "large headers", config: ServerConfig{MaxHeaderBytes: maximumMaxHeaderBytes + 1}, message: "maximum header bytes"},
		{name: "negative connections", config: ServerConfig{MaxClientConnections: -1}, message: "maximum client connections"},
		{name: "many connections", config: ServerConfig{MaxClientConnections: hardClientConnections + 1}, message: "maximum client connections"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewServer(test.config, router, certificate); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("NewServer() error = %v, want containing %q", err, test.message)
			}
		})
	}
	server, err := NewServer(ServerConfig{}, router, certificate)
	if err != nil {
		t.Fatalf("NewServer() defaults error = %v", err)
	}
	if server.config.ReadHeaderTimeout != defaultReadHeaderTimeout ||
		server.config.IdleTimeout != defaultServerIdleTimeout ||
		server.config.ShutdownTimeout != defaultShutdownTimeout ||
		server.config.MaxHeaderBytes != defaultMaxHeaderBytes ||
		server.config.MaxClientConnections != defaultClientConnections {
		t.Fatalf("NewServer() normalized config = %#v", server.config)
	}
}

// TestServerServesExactHTTPAndTLSRoutes verifies the paired lifecycle over real sockets.
func TestServerServesExactHTTPAndTLSRoutes(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Project", "orders")
		_, _ = writer.Write([]byte(request.Host + " " + request.URL.RequestURI()))
	}))
	defer upstream.Close()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstreamAddress(t, upstream.URL)}}))
	var certificateCalls atomic.Int64
	server := mustServer(t, ServerConfig{}, router, testCertificateProvider(t, &certificateCalls))
	httpListener := listenLoopback(t)
	httpsListener := listenLoopback(t)
	httpAddress := httpListener.Addr().String()
	httpsAddress := httpsListener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, httpListener, httpsListener)
	}()
	waitForServerRunning(t, server)

	redirectClient := &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	request, err := http.NewRequest(http.MethodGet, "http://"+httpAddress+"/reports?day=today", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Host = "orders.test"
	response, err := redirectClient.Do(request)
	if err != nil {
		t.Fatalf("HTTP Do() error = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusPermanentRedirect || response.Header.Get("Location") != "https://orders.test/reports?day=today" {
		t.Fatalf("HTTP response = %d, Location %q", response.StatusCode, response.Header.Get("Location"))
	}
	optionsConnection := dialAddress(t, httpAddress)
	if _, err := io.WriteString(optionsConnection, "OPTIONS * HTTP/1.1\r\nHost: orders.test\r\nConnection: close\r\n\r\n"); err != nil {
		_ = optionsConnection.Close()
		t.Fatalf("write asterisk request: %v", err)
	}
	optionsResponse, err := http.ReadResponse(bufio.NewReader(optionsConnection), &http.Request{Method: http.MethodOptions})
	if err != nil {
		_ = optionsConnection.Close()
		t.Fatalf("read asterisk response: %v", err)
	}
	_ = optionsResponse.Body.Close()
	_ = optionsConnection.Close()
	if optionsResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("OPTIONS * status = %d, want %d", optionsResponse.StatusCode, http.StatusBadRequest)
	}

	tlsClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "orders.test",
		},
		ForceAttemptHTTP2: true,
	}}
	request, err = http.NewRequest(http.MethodGet, "https://"+httpsAddress+"/api/orders?limit=5", nil)
	if err != nil {
		t.Fatalf("NewRequest() TLS error = %v", err)
	}
	request.Host = "orders.test"
	response, err = tlsClient.Do(request)
	if err != nil {
		t.Fatalf("HTTPS Do() error = %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 2 || response.Header.Get("X-Project") != "orders" || string(body) != "orders.test /api/orders?limit=5" {
		t.Fatalf("HTTPS response = %d, headers %#v, body %q", response.StatusCode, response.Header, body)
	}
	if certificateCalls.Load() == 0 {
		t.Fatal("certificate provider was not called")
	}

	unknownClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "unknown.test",
	}}}
	if _, err := unknownClient.Get("https://" + httpsAddress + "/"); err == nil {
		t.Fatal("unknown TLS SNI completed a handshake")
	}

	cancel()
	if err := waitForServe(t, done); err != nil {
		t.Fatalf("Serve() shutdown error = %v", err)
	}
	if snapshot := server.Snapshot(); snapshot.Running || snapshot.HTTPAddress.String() != httpAddress || snapshot.HTTPSAddress.String() != httpsAddress {
		t.Fatalf("Snapshot() after shutdown = %#v", snapshot)
	}
}

// TestServeRejectsInvalidListenersAndOwnsCleanup verifies failed admission does not leak sockets.
func TestServeRejectsInvalidListenersAndOwnsCleanup(t *testing.T) {
	t.Parallel()
	newServer := func(t *testing.T) *Server {
		t.Helper()
		return mustServer(t, ServerConfig{}, mustRouter(t, Config{}, mustSnapshot(t, nil)), testCertificateProvider(t, nil))
	}

	listener := listenLoopback(t)
	address := listener.Addr().String()
	if err := newServer(t).Serve(context.Background(), listener, nil); err == nil || !strings.Contains(err.Error(), "listeners are required") {
		t.Fatalf("Serve(nil) error = %v", err)
	}
	assertListenerClosed(t, address)

	nonLoopback, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("Listen(non-loopback) error = %v", err)
	}
	nonLoopbackAddress := nonLoopback.Addr().String()
	httpsListener := listenLoopback(t)
	httpsAddress := httpsListener.Addr().String()
	if err := newServer(t).Serve(context.Background(), nonLoopback, httpsListener); err == nil || !strings.Contains(err.Error(), "IPv4 loopback") {
		t.Fatalf("Serve(non-loopback) error = %v", err)
	}
	assertListenerClosed(t, nonLoopbackAddress)
	assertListenerClosed(t, httpsAddress)

	shared := listenLoopback(t)
	sharedAddress := shared.Addr().String()
	if err := newServer(t).Serve(context.Background(), shared, shared); err == nil || !strings.Contains(err.Error(), "must be distinct") {
		t.Fatalf("Serve(shared) error = %v", err)
	}
	assertListenerClosed(t, sharedAddress)

	fake := &invalidAddressListener{}
	httpsListener = listenLoopback(t)
	if err := newServer(t).Serve(context.Background(), fake, httpsListener); err == nil || !strings.Contains(err.Error(), "must be TCP") {
		t.Fatalf("Serve(fake) error = %v", err)
	}
	if !fake.closed.Load() {
		t.Fatal("invalid listener was not closed")
	}
}

// TestServerRejectsRepeatedLifecycle verifies one instance cannot own a second listener pair.
func TestServerRejectsRepeatedLifecycle(t *testing.T) {
	t.Parallel()
	server := mustServer(t, ServerConfig{}, mustRouter(t, Config{}, mustSnapshot(t, nil)), testCertificateProvider(t, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	httpListener := listenLoopback(t)
	httpsListener := listenLoopback(t)
	go func() {
		done <- server.Serve(ctx, httpListener, httpsListener)
	}()
	waitForServerRunning(t, server)
	cancel()
	if err := waitForServe(t, done); err != nil {
		t.Fatalf("first Serve() error = %v", err)
	}
	secondHTTPListener := listenLoopback(t)
	secondHTTPSListener := listenLoopback(t)
	httpAddress := secondHTTPListener.Addr().String()
	httpsAddress := secondHTTPSListener.Addr().String()
	if err := server.Serve(context.Background(), secondHTTPListener, secondHTTPSListener); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("second Serve() error = %v", err)
	}
	assertListenerClosed(t, httpAddress)
	assertListenerClosed(t, httpsAddress)
}

// TestServerReportsUnexpectedListenerFailure verifies one failed listener shuts down its pair.
func TestServerReportsUnexpectedListenerFailure(t *testing.T) {
	t.Parallel()
	server := mustServer(t, ServerConfig{}, mustRouter(t, Config{}, mustSnapshot(t, nil)), testCertificateProvider(t, nil))
	httpListener := listenLoopback(t)
	httpsListener := listenLoopback(t)
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background(), httpListener, httpsListener)
	}()
	waitForServerRunning(t, server)
	if err := httpListener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := waitForServe(t, done); err == nil || !strings.Contains(err.Error(), "serve HTTP ingress") {
		t.Fatalf("Serve() error = %v", err)
	}
}

// TestServerForcesBoundedShutdown verifies a stuck request cannot outlive the configured deadline.
func TestServerForcesBoundedShutdown(t *testing.T) {
	t.Parallel()
	requestStarted := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-release
		_, _ = writer.Write([]byte("late"))
	}))
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstreamAddress(t, upstream.URL)}}))
	server := mustServer(t, ServerConfig{ShutdownTimeout: 50 * time.Millisecond}, router, testCertificateProvider(t, nil))
	httpListener := listenLoopback(t)
	httpsListener := listenLoopback(t)
	httpsAddress := httpsListener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, httpListener, httpsListener)
	}()
	waitForServerRunning(t, server)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "orders.test"}}}
	requestDone := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodGet, "https://"+httpsAddress+"/stuck", nil)
		if err == nil {
			request.Host = "orders.test"
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_ = response.Body.Close()
			}
			err = requestErr
		}
		requestDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		close(release)
		upstream.Close()
		t.Fatal("upstream request did not start")
	}
	started := time.Now()
	cancel()
	serveErr := waitForServe(t, done)
	if serveErr == nil || !errors.Is(serveErr, context.DeadlineExceeded) {
		close(release)
		upstream.Close()
		t.Fatalf("Serve() error = %v, want shutdown deadline", serveErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		close(release)
		upstream.Close()
		t.Fatalf("bounded shutdown took %s", elapsed)
	}
	close(release)
	upstream.Close()
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client request did not finish after forced shutdown")
	}
}

// TestBoundedListenerSharesAndRejectsOverload verifies excess sockets close until one admission slot is free.
func TestBoundedListenerSharesAndRejectsOverload(t *testing.T) {
	t.Parallel()
	base := listenLoopback(t)
	limiter := newConnectionLimiter(1)
	listener := newBoundedListener(base, limiter)
	firstAccepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		firstAccepted <- connection
	}()
	firstClient := dialAddress(t, base.Addr().String())
	defer firstClient.Close()
	firstServer := <-firstAccepted
	secondAccepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		secondAccepted <- connection
	}()
	secondClient := dialAddress(t, base.Addr().String())
	select {
	case <-secondAccepted:
		t.Fatal("second connection bypassed the shared admission limit")
	case <-time.After(50 * time.Millisecond):
	}
	if err := secondClient.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	if _, err := secondClient.Read(make([]byte, 1)); err == nil {
		t.Fatal("overloaded client socket remained open")
	}
	_ = secondClient.Close()
	if err := firstServer.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := firstServer.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("second Close() error = %v", err)
	}
	thirdClient := dialAddress(t, base.Addr().String())
	defer thirdClient.Close()
	var secondServer net.Conn
	select {
	case secondServer = <-secondAccepted:
	case <-time.After(5 * time.Second):
		t.Fatal("second connection was not admitted after release")
	}
	_ = secondServer.Close()
	if err := listener.Close(); err != nil {
		t.Fatalf("listener Close() error = %v", err)
	}
}

// TestBoundedListenerReportsFailureWhileSaturated proves a full peer cannot hide listener loss.
func TestBoundedListenerReportsFailureWhileSaturated(t *testing.T) {
	t.Parallel()
	base := listenLoopback(t)
	limiter := newConnectionLimiter(1)
	listener := newBoundedListener(base, limiter)
	firstAccepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		firstAccepted <- connection
	}()
	firstClient := dialAddress(t, base.Addr().String())
	defer firstClient.Close()
	firstServer := <-firstAccepted
	defer firstServer.Close()

	failure := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		failure <- err
	}()
	if err := base.Close(); err != nil {
		t.Fatalf("close underlying listener: %v", err)
	}
	select {
	case err := <-failure:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept() failure = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("saturation concealed the listener failure")
	}
}

// TestIdleBoundedListenerDoesNotReserveAdmission keeps one quiet protocol from starving its peer.
func TestIdleBoundedListenerDoesNotReserveAdmission(t *testing.T) {
	t.Parallel()
	limiter := newConnectionLimiter(1)
	idleBase := listenLoopback(t)
	idle := newBoundedListener(idleBase, limiter)
	activeBase := listenLoopback(t)
	active := newBoundedListener(activeBase, limiter)
	defer idle.Close()
	defer active.Close()

	idleDone := make(chan error, 1)
	go func() {
		_, err := idle.Accept()
		idleDone <- err
	}()
	activeAccepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := active.Accept()
		activeAccepted <- connection
	}()
	client := dialAddress(t, activeBase.Addr().String())
	defer client.Close()
	select {
	case connection := <-activeAccepted:
		_ = connection.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("idle listener reserved the shared admission slot")
	}
	_ = idle.Close()
	select {
	case err := <-idleDone:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("idle Accept() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle Accept() did not stop")
	}
}

// TestServerClosesHijackedConnectionsOnShutdown proves upgrades remain owned after net/http releases them.
func TestServerClosesHijackedConnectionsOnShutdown(t *testing.T) {
	t.Parallel()
	upgraded := make(chan struct{})
	upstreamClosed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, buffer, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: harbor-test\r\n\r\n")
		_ = buffer.Flush()
		close(upgraded)
		_, _ = io.Copy(io.Discard, connection)
		close(upstreamClosed)
	}))
	defer upstream.Close()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstreamAddress(t, upstream.URL)}}))
	server := mustServer(t, ServerConfig{}, router, testCertificateProvider(t, nil))
	httpListener := listenLoopback(t)
	httpsListener := listenLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, httpListener, httpsListener)
	}()
	waitForServerRunning(t, server)

	connection, err := tls.Dial("tcp4", httpsListener.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "orders.test",
	})
	if err != nil {
		cancel()
		t.Fatalf("Dial() error = %v", err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "GET /live HTTP/1.1\r\nHost: orders.test\r\nConnection: Upgrade\r\nUpgrade: harbor-test\r\n\r\n"); err != nil {
		cancel()
		t.Fatalf("WriteString() error = %v", err)
	}
	reader := bufio.NewReader(connection)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			cancel()
			t.Fatalf("read upgrade response: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	select {
	case <-upgraded:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("upstream was not upgraded")
	}
	cancel()
	if err := waitForServe(t, done); err != nil {
		t.Fatalf("Serve() shutdown error = %v", err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("hijacked client connection survived ingress shutdown")
	}
	select {
	case <-upstreamClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("hijacked upstream connection survived ingress shutdown")
	}
}

// invalidAddressListener exercises validation without binding a host socket.
type invalidAddressListener struct {
	closed atomic.Bool
}

// Accept cannot succeed because validation closes this listener before serving.
func (listener *invalidAddressListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

// Close records ownership transfer for the validation test.
func (listener *invalidAddressListener) Close() error {
	listener.closed.Store(true)
	return nil
}

// Addr reports a non-TCP address so ingress rejects the listener.
func (listener *invalidAddressListener) Addr() net.Addr {
	return invalidNetworkAddress("invalid")
}

// invalidNetworkAddress is a minimal non-TCP net.Addr.
type invalidNetworkAddress string

// Network identifies the deliberately unsupported transport.
func (address invalidNetworkAddress) Network() string {
	return "invalid"
}

// String returns the deliberately invalid listener address.
func (address invalidNetworkAddress) String() string {
	return string(address)
}

// mustServer fails before a lifecycle can retain invalid dependencies.
func mustServer(t *testing.T, config ServerConfig, router *Router, certificate CertificateProvider) *Server {
	t.Helper()
	server, err := NewServer(config, router, certificate)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

// testCertificateProvider borrows httptest's ephemeral certificate for local handshake behavior.
func testCertificateProvider(t *testing.T, calls *atomic.Int64) CertificateProvider {
	t.Helper()
	source := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := source.TLS.Certificates[0]
	source.Close()
	return func(ctx context.Context, host string) (*tls.Certificate, error) {
		if ctx == nil || host != "orders.test" && host != "unused.test" {
			return nil, errors.New("unexpected certificate request")
		}
		if calls != nil {
			calls.Add(1)
		}
		return &certificate, nil
	}
}

// listenLoopback binds one ephemeral IPv4 loopback listener for lifecycle tests.
func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	return listener
}

// dialAddress connects to one already-bound test listener.
func dialAddress(t *testing.T, address string) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", address, 5*time.Second)
	if err != nil {
		t.Fatalf("DialTimeout(%q) error = %v", address, err)
	}
	return connection
}

// waitForServerRunning observes lifecycle publication without assuming goroutine scheduling.
func waitForServerRunning(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if server.Snapshot().Running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("server did not publish running state")
}

// waitForServe bounds every lifecycle join so a regression fails instead of hanging the suite.
func waitForServe(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not return")
		return nil
	}
}

// assertListenerClosed proves validation transferred and released listener ownership.
func assertListenerClosed(t *testing.T, address string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", address, 50*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("listener %q remained reachable", address)
	}
}

// TestIngressListenerAddressRejectsMalformedAddresses covers nil and unparsable address metadata.
func TestIngressListenerAddressRejectsMalformedAddresses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		listener net.Listener
		message  string
	}{
		{name: "nil address", listener: &customAddressListener{}, message: "must be TCP"},
		{name: "unparsable", listener: &customAddressListener{address: staticAddress{network: "tcp", value: "bad"}}, message: "is invalid"},
		{name: "zero port", listener: &customAddressListener{address: staticAddress{network: "tcp", value: "127.0.0.1:0"}}, message: "must not be zero"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ingressListenerAddress("test", test.listener); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("ingressListenerAddress() error = %v, want containing %q", err, test.message)
			}
		})
	}
}

// customAddressListener exposes controlled address metadata without accepting connections.
type customAddressListener struct {
	address net.Addr
}

// Accept reports closure because these listeners are validation-only.
func (listener *customAddressListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

// Close releases no operating-system resources for a validation-only listener.
func (listener *customAddressListener) Close() error {
	return nil
}

// Addr returns the controlled address metadata.
func (listener *customAddressListener) Addr() net.Addr {
	return listener.address
}

// staticAddress supplies controlled network and string forms.
type staticAddress struct {
	network string
	value   string
}

// Network returns the configured transport name.
func (address staticAddress) Network() string {
	return address.network
}

// String returns the configured endpoint representation.
func (address staticAddress) String() string {
	return address.value
}

// TestServerSnapshotStartsEmpty verifies construction does not imply socket ownership.
func TestServerSnapshotStartsEmpty(t *testing.T) {
	t.Parallel()
	server := mustServer(t, ServerConfig{}, mustRouter(t, Config{}, mustSnapshot(t, nil)), testCertificateProvider(t, nil))
	if got := server.Snapshot(); got.Running || got.HTTPAddress.IsValid() || got.HTTPSAddress.IsValid() {
		t.Fatalf("Snapshot() = %#v, want empty", got)
	}
}

// TestUnexpectedServeErrorClassification documents the sole normal http.Server terminal value.
func TestUnexpectedServeErrorClassification(t *testing.T) {
	t.Parallel()
	if err := unexpectedServeError(serveResult{name: "HTTP"}); err != nil {
		t.Fatalf("unexpectedServeError(nil) = %v", err)
	}
	if err := unexpectedServeError(serveResult{name: "HTTP", err: http.ErrServerClosed}); err != nil {
		t.Fatalf("unexpectedServeError(ErrServerClosed) = %v", err)
	}
	sentinel := errors.New("failed")
	if err := unexpectedServeError(serveResult{name: "HTTP", err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("unexpectedServeError(sentinel) = %v", err)
	}
}

// TestConnectionLimiterClosedWait verifies closing a listener unblocks admission without a free slot.
func TestConnectionLimiterClosedWait(t *testing.T) {
	t.Parallel()
	base := listenLoopback(t)
	limiter := newConnectionLimiter(1)
	limiter.slots <- struct{}{}
	listener := newBoundedListener(base, limiter)
	done := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		done <- err
	}()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept() did not unblock")
	}
	<-limiter.slots
}

// TestIngressAddressRoundTrip verifies the bound address shape matches netip parsing.
func TestIngressAddressRoundTrip(t *testing.T) {
	t.Parallel()
	listener := listenLoopback(t)
	defer listener.Close()
	got, err := ingressListenerAddress("HTTP", listener)
	if err != nil {
		t.Fatalf("ingressListenerAddress() error = %v", err)
	}
	want := netip.MustParseAddrPort(listener.Addr().String())
	if got != want {
		t.Fatalf("ingressListenerAddress() = %v, want %v", got, want)
	}
}
