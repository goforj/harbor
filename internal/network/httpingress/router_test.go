package httpingress

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewRouterValidatesConfiguration covers dependency and resource-bound failures.
func TestNewRouterValidatesConfiguration(t *testing.T) {
	t.Parallel()
	snapshot := mustSnapshot(t, nil)
	if _, err := NewRouter(Config{}, nil); err == nil || !strings.Contains(err.Error(), "initial ingress snapshot") {
		t.Fatalf("NewRouter() nil snapshot error = %v", err)
	}
	tests := []struct {
		name    string
		config  Config
		message string
	}{
		{name: "negative connect", config: Config{ConnectTimeout: -time.Second}, message: "connect timeout"},
		{name: "long connect", config: Config{ConnectTimeout: maximumConnectTimeout + time.Nanosecond}, message: "connect timeout"},
		{name: "negative response", config: Config{ResponseHeaderTimeout: -time.Second}, message: "response header timeout"},
		{name: "long response", config: Config{ResponseHeaderTimeout: maximumResponseHeaderTimeout + time.Nanosecond}, message: "response header timeout"},
		{name: "negative idle", config: Config{IdleConnectionTimeout: -time.Second}, message: "idle connection timeout"},
		{name: "long idle", config: Config{IdleConnectionTimeout: maximumIdleConnectionTimeout + time.Nanosecond}, message: "idle connection timeout"},
		{name: "negative connections", config: Config{MaxConnectionsPerHost: -1}, message: "maximum connections"},
		{name: "too many connections", config: Config{MaxConnectionsPerHost: hardMaximumConnections + 1}, message: "maximum connections"},
		{name: "negative body", config: Config{MaxRequestBodyBytes: -1}, message: "maximum request body"},
		{name: "large body", config: Config{MaxRequestBodyBytes: hardMaximumRequestBody + 1}, message: "maximum request body"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewRouter(test.config, snapshot); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("NewRouter() error = %v, want containing %q", err, test.message)
			}
		})
	}

	router, err := NewRouter(Config{}, snapshot)
	if err != nil {
		t.Fatalf("NewRouter() defaults error = %v", err)
	}
	if router.config.ConnectTimeout != defaultConnectTimeout ||
		router.config.ResponseHeaderTimeout != defaultResponseHeaderTimeout ||
		router.config.IdleConnectionTimeout != defaultIdleConnectionTimeout ||
		router.config.MaxConnectionsPerHost != defaultMaximumConnections ||
		router.config.MaxRequestBodyBytes != defaultMaximumRequestBody {
		t.Fatalf("NewRouter() normalized config = %#v", router.config)
	}
	if err := router.Replace(nil); err == nil || !strings.Contains(err.Error(), "replacement ingress snapshot") {
		t.Fatalf("Replace(nil) error = %v", err)
	}
	router.CloseIdleConnections()
	if router.DroppedErrors() != 0 {
		t.Fatalf("DroppedErrors() = %d, want 0", router.DroppedErrors())
	}
}

// TestHandlersRejectForwardProxyTargets verifies absolute-form and CONNECT requests never select an upstream.
func TestHandlersRejectForwardProxyTargets(t *testing.T) {
	t.Parallel()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{
		Host:     "orders.test",
		Upstream: netip.MustParseAddrPort("127.0.0.1:43101"),
	}}))
	tests := []struct {
		name    string
		handler http.Handler
		request *http.Request
	}{
		{
			name:    "HTTP absolute form",
			handler: router.HTTPHandler(),
			request: &http.Request{Method: http.MethodGet, Host: "orders.test", RequestURI: "http://foreign.example/", URL: &url.URL{Scheme: "http", Host: "foreign.example", Path: "/"}},
		},
		{
			name:    "HTTPS absolute form",
			handler: router.HTTPSHandler(),
			request: &http.Request{Method: http.MethodGet, Host: "orders.test", RequestURI: "https://foreign.example/", URL: &url.URL{Scheme: "https", Host: "foreign.example", Path: "/"}, TLS: &tls.ConnectionState{ServerName: "orders.test"}},
		},
		{
			name:    "CONNECT",
			handler: router.HTTPSHandler(),
			request: &http.Request{Method: http.MethodConnect, Host: "orders.test", RequestURI: "orders.test:443", URL: &url.URL{Path: "orders.test:443"}, TLS: &tls.ConnectionState{ServerName: "orders.test"}},
		},
		{
			name:    "asterisk",
			handler: router.HTTPHandler(),
			request: &http.Request{Method: http.MethodOptions, Host: "orders.test", RequestURI: "*", URL: &url.URL{Path: "*"}},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.request.Header = make(http.Header)
			test.request.Body = http.NoBody
			test.request = test.request.WithContext(context.Background())
			response := httptest.NewRecorder()
			test.handler.ServeHTTP(response, test.request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}

// TestHTTPHandlerRedirectsOnlyRegisteredHosts verifies unknown names never inherit a default project.
func TestHTTPHandlerRedirectsOnlyRegisteredHosts(t *testing.T) {
	t.Parallel()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{
		Host:     "orders.test",
		Upstream: netip.MustParseAddrPort("127.0.0.1:43101"),
	}}))

	request := httptest.NewRequest(http.MethodGet, "/reports/daily?format=csv", nil)
	request.Host = "ORDERS.TEST:80"
	response := httptest.NewRecorder()
	router.HTTPHandler().ServeHTTP(response, request)
	if response.Code != http.StatusPermanentRedirect {
		t.Fatalf("registered status = %d, want %d", response.Code, http.StatusPermanentRedirect)
	}
	if location := response.Header().Get("Location"); location != "https://orders.test/reports/daily?format=csv" {
		t.Fatalf("Location = %q", location)
	}

	for _, authority := range []string{"unknown.test", "orders.test:bad", "outside.example"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Host = authority
		response := httptest.NewRecorder()
		router.HTTPHandler().ServeHTTP(response, request)
		if response.Code != http.StatusMisdirectedRequest {
			t.Fatalf("authority %q status = %d, want %d", authority, response.Code, http.StatusMisdirectedRequest)
		}
	}
}

// TestHTTPSHandlerProxiesWithTrustedForwardingHeaders verifies request metadata cannot spoof Harbor's boundary.
func TestHTTPSHandlerProxiesWithTrustedForwardingHeaders(t *testing.T) {
	t.Parallel()
	type observation struct {
		method          string
		path            string
		rawQuery        string
		host            string
		body            string
		forwarded       string
		forwardedFor    string
		forwardedHost   string
		forwardedProto  string
		realIP          string
		forwardedPort   string
		forwardedServer string
		forwardedSSL    string
		urlScheme       string
		frontEndHTTPS   string
	}
	observed := make(chan observation, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
		}
		observed <- observation{
			method:          request.Method,
			path:            request.URL.Path,
			rawQuery:        request.URL.RawQuery,
			host:            request.Host,
			body:            string(body),
			forwarded:       request.Header.Get("Forwarded"),
			forwardedFor:    request.Header.Get("X-Forwarded-For"),
			forwardedHost:   request.Header.Get("X-Forwarded-Host"),
			forwardedProto:  request.Header.Get("X-Forwarded-Proto"),
			realIP:          request.Header.Get("X-Real-IP"),
			forwardedPort:   request.Header.Get("X-Forwarded-Port"),
			forwardedServer: request.Header.Get("X-Forwarded-Server"),
			forwardedSSL:    request.Header.Get("X-Forwarded-Ssl"),
			urlScheme:       request.Header.Get("X-Url-Scheme"),
			frontEndHTTPS:   request.Header.Get("Front-End-Https"),
		}
		writer.Header().Set("X-Upstream", "orders")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("created"))
	}))
	defer upstream.Close()

	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{
		Host:     "orders.test",
		Upstream: upstreamAddress(t, upstream.URL),
	}}))
	request := httptest.NewRequest(http.MethodPost, "/api/orders?dry_run=true", strings.NewReader("payload"))
	request.Host = "ORDERS.TEST:443"
	request.RemoteAddr = "192.0.2.10:4242"
	request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
	request.Header.Set("Forwarded", "for=attacker")
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	request.Header.Set("X-Forwarded-Host", "attacker.test")
	request.Header.Set("X-Forwarded-Proto", "http")
	request.Header.Set("X-Real-IP", "203.0.113.100")
	request.Header.Set("X-Forwarded-Port", "8443")
	request.Header.Set("X-Forwarded-Server", "attacker.test")
	request.Header.Set("X-Forwarded-Ssl", "on")
	request.Header.Set("X-Url-Scheme", "http")
	request.Header.Set("Front-End-Https", "off")
	response := httptest.NewRecorder()
	router.HTTPSHandler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated || response.Body.String() != "created" || response.Header().Get("X-Upstream") != "orders" {
		t.Fatalf("response = status %d, body %q, headers %#v", response.Code, response.Body.String(), response.Header())
	}
	got := <-observed
	if got.method != http.MethodPost || got.path != "/api/orders" || got.rawQuery != "dry_run=true" || got.host != "orders.test" || got.body != "payload" {
		t.Fatalf("upstream request = %#v", got)
	}
	if got.forwarded != "" || got.forwardedFor != "192.0.2.10" || got.forwardedHost != "ORDERS.TEST:443" || got.forwardedProto != "https" ||
		got.realIP != "192.0.2.10" || got.forwardedPort != "" || got.forwardedServer != "" || got.forwardedSSL != "" ||
		got.urlScheme != "" || got.frontEndHTTPS != "" {
		t.Fatalf("forwarding headers = %#v", got)
	}
}

// TestHTTPSHandlerBoundsStreamingRequestBodies verifies declared and chunked bodies share one limit.
func TestHTTPSHandlerBoundsStreamingRequestBodies(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	router := mustRouter(t, Config{MaxRequestBodyBytes: 4}, mustSnapshot(t, []Route{{
		Host:     "orders.test",
		Upstream: upstreamAddress(t, upstream.URL),
	}}))

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("12345"))
	request.Host = "orders.test"
	request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
	response := httptest.NewRecorder()
	router.HTTPSHandler().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared body status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}

	request = httptest.NewRequest(http.MethodPost, "/upload", io.NopCloser(strings.NewReader("12345")))
	request.Host = "orders.test"
	request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	response = httptest.NewRecorder()
	router.HTTPSHandler().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked body status = %d, want %d; body = %q", response.Code, http.StatusRequestEntityTooLarge, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("1234"))
	request.Host = "orders.test"
	request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
	response = httptest.NewRecorder()
	router.HTTPSHandler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("exact body status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

// TestHTTPSHandlerRejectsUntrustedRouteSelections covers both layers of the Host/SNI boundary.
func TestHTTPSHandlerRejectsUntrustedRouteSelections(t *testing.T) {
	t.Parallel()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{
		Host:     "orders.test",
		Upstream: netip.MustParseAddrPort("127.0.0.1:43101"),
	}}))
	tests := []struct {
		name string
		host string
		tls  *tls.ConnectionState
	}{
		{name: "no TLS", host: "orders.test"},
		{name: "unknown host", host: "unknown.test", tls: &tls.ConnectionState{ServerName: "unknown.test"}},
		{name: "outside host", host: "outside.example", tls: &tls.ConnectionState{ServerName: "outside.example"}},
		{name: "missing SNI", host: "orders.test", tls: &tls.ConnectionState{}},
		{name: "different SNI", host: "orders.test", tls: &tls.ConnectionState{ServerName: "admin.orders.test"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Host = test.host
			request.TLS = test.tls
			response := httptest.NewRecorder()
			router.HTTPSHandler().ServeHTTP(response, request)
			if response.Code != http.StatusMisdirectedRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusMisdirectedRequest)
			}
		})
	}
}

// TestTLSConfigAuthorizesBeforeCertificateLookup verifies unknown SNI never reaches key selection.
func TestTLSConfigAuthorizesBeforeCertificateLookup(t *testing.T) {
	t.Parallel()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{
		Host:     "orders.test",
		Upstream: netip.MustParseAddrPort("127.0.0.1:43101"),
	}}))
	if _, err := router.TLSConfig(nil); err == nil || !strings.Contains(err.Error(), "certificate provider") {
		t.Fatalf("TLSConfig(nil) error = %v", err)
	}

	var calls atomic.Int64
	certificate := &tls.Certificate{}
	config, err := router.TLSConfig(func(ctx context.Context, host string) (*tls.Certificate, error) {
		calls.Add(1)
		if ctx == nil || host != "orders.test" {
			t.Fatalf("provider context/host = %v/%q", ctx, host)
		}
		return certificate, nil
	})
	if err != nil {
		t.Fatalf("TLSConfig() error = %v", err)
	}
	if config.MinVersion != tls.VersionTLS12 || len(config.NextProtos) != 2 {
		t.Fatalf("TLSConfig() policy = %#v", config)
	}
	got, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: "ORDERS.TEST."})
	if err != nil || got != certificate {
		t.Fatalf("GetCertificate(known) = %p, %v", got, err)
	}
	for _, host := range []string{"", "unknown.test", "outside.example"} {
		if _, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: host}); err == nil {
			t.Fatalf("GetCertificate(%q) succeeded", host)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}

	providerError := errors.New("certificate unavailable")
	config, err = router.TLSConfig(func(context.Context, string) (*tls.Certificate, error) {
		return nil, providerError
	})
	if err != nil {
		t.Fatalf("TLSConfig(error provider) error = %v", err)
	}
	if _, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: "orders.test"}); !errors.Is(err, providerError) {
		t.Fatalf("GetCertificate() error = %v, want %v", err, providerError)
	}
	config, err = router.TLSConfig(func(context.Context, string) (*tls.Certificate, error) { return nil, nil })
	if err != nil {
		t.Fatalf("TLSConfig(nil certificate provider) error = %v", err)
	}
	if _, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: "orders.test"}); err == nil || !strings.Contains(err.Error(), "returned no certificate") {
		t.Fatalf("GetCertificate() nil certificate error = %v", err)
	}
}

// TestRouterReplaceIsAtomic verifies readers observe complete candidates during concurrent publication.
func TestRouterReplaceIsAtomic(t *testing.T) {
	t.Parallel()
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("first"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("second"))
	}))
	defer second.Close()
	firstSnapshot := mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstreamAddress(t, first.URL)}})
	secondSnapshot := mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstreamAddress(t, second.URL)}})
	router := mustRouter(t, Config{}, firstSnapshot)

	var workers sync.WaitGroup
	var failures atomic.Int64
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				request := httptest.NewRequest(http.MethodGet, "/", nil)
				request.Host = "orders.test"
				request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
				response := httptest.NewRecorder()
				router.HTTPSHandler().ServeHTTP(response, request)
				if body := response.Body.String(); response.Code != http.StatusOK || body != "first" && body != "second" {
					failures.Add(1)
				}
			}
		}()
	}
	for iteration := 0; iteration < 200; iteration++ {
		if iteration%2 == 0 {
			_ = router.Replace(secondSnapshot)
		} else {
			_ = router.Replace(firstSnapshot)
		}
	}
	workers.Wait()
	if failures.Load() != 0 {
		t.Fatalf("concurrent proxy failures = %d", failures.Load())
	}
}

// TestProxyFailureIsContained verifies upstream and observer failures cannot relinquish ingress.
func TestProxyFailureIsContained(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	upstream, err := netip.ParseAddrPort(address)
	if err != nil {
		t.Fatalf("ParseAddrPort() error = %v", err)
	}
	var observed atomic.Int64
	router := mustRouter(t, Config{
		ConnectTimeout: 100 * time.Millisecond,
		ObserveError: func(error) {
			observed.Add(1)
			panic("observer failure")
		},
	}, mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstream}}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "orders.test"
	request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
	response := httptest.NewRecorder()
	router.HTTPSHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	eventually(t, func() bool { return observed.Load() == 1 }, "observer did not receive proxy failure")
}

// TestBlockingObserverCannotDelayRequests verifies diagnostic callbacks have bounded concurrency and storage.
func TestBlockingObserverCannotDelayRequests(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	upstream, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("ParseAddrPort() error = %v", err)
	}
	_ = listener.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	router := mustRouter(t, Config{
		ConnectTimeout: 50 * time.Millisecond,
		ObserveError: func(error) {
			close(started)
			<-release
		},
	}, mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstream}}))
	proxyOnce := func() {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Host = "orders.test"
		request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
		response := httptest.NewRecorder()
		router.HTTPSHandler().ServeHTTP(response, request)
		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
		}
	}
	proxyOnce()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("observer did not start")
	}
	startedAt := time.Now()
	for count := 0; count < 20; count++ {
		proxyOnce()
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		close(release)
		t.Fatalf("blocking observer delayed requests for %s", elapsed)
	}
	if router.DroppedErrors() == 0 {
		close(release)
		t.Fatal("blocking observer did not produce bounded diagnostic drops")
	}
	close(release)
}

// TestHTTPSStreamingFlushesImmediately verifies SSE-style responses are not buffered to completion.
func TestHTTPSStreamingFlushesImmediately(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: first\n\n")
		writer.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(writer, "data: second\n\n")
	}))
	defer upstream.Close()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstreamAddress(t, upstream.URL)}}))
	downstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Host = "orders.test"
		request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
		router.HTTPSHandler().ServeHTTP(writer, request)
	}))
	defer downstream.Close()

	response, err := http.Get(downstream.URL)
	if err != nil {
		close(release)
		t.Fatalf("Get() error = %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		close(release)
		t.Fatalf("ReadString() first error = %v", err)
	}
	if line != "data: first\n" {
		close(release)
		t.Fatalf("first line = %q", line)
	}
	close(release)
	remainder, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(remainder) != "\ndata: second\n\n" {
		t.Fatalf("remainder = %q", remainder)
	}
}

// TestHTTPSWebSocketUpgradeTunnelsBidirectionally verifies upgraded connections are not buffered as HTTP bodies.
func TestHTTPSWebSocketUpgradeTunnelsBidirectionally(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.EqualFold(request.Header.Get("Connection"), "upgrade") || request.Header.Get("Upgrade") != "harbor-test" {
			http.Error(writer, "missing upgrade", http.StatusBadRequest)
			return
		}
		connection, buffer, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack() error = %v", err)
			return
		}
		defer connection.Close()
		_, _ = io.WriteString(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: harbor-test\r\n\r\n")
		_ = buffer.Flush()
		_, _ = io.Copy(connection, connection)
	}))
	defer upstream.Close()
	router := mustRouter(t, Config{}, mustSnapshot(t, []Route{{Host: "orders.test", Upstream: upstreamAddress(t, upstream.URL)}}))
	downstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Host = "orders.test"
		request.TLS = &tls.ConnectionState{ServerName: "orders.test"}
		router.HTTPSHandler().ServeHTTP(writer, request)
	}))
	defer downstream.Close()
	parsed, err := url.Parse(downstream.URL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	connection, err := net.DialTimeout("tcp4", parsed.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("DialTimeout() error = %v", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	_, _ = io.WriteString(connection, "GET /live HTTP/1.1\r\nHost: orders.test\r\nConnection: Upgrade\r\nUpgrade: harbor-test\r\n\r\n")
	reader := bufio.NewReader(connection)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString(status) error = %v", err)
	}
	if status != "HTTP/1.1 101 Switching Protocols\r\n" {
		t.Fatalf("upgrade status = %q", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("ReadString(header) error = %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	_, _ = io.WriteString(connection, "ping\n")
	echo, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString(echo) error = %v", err)
	}
	if echo != "ping\n" {
		t.Fatalf("upgrade echo = %q", echo)
	}
}

// mustSnapshot fails a test at the authoritative candidate construction boundary.
func mustSnapshot(t *testing.T, routes []Route) *Snapshot {
	t.Helper()
	snapshot, err := NewSnapshot(routes)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	return snapshot
}

// mustRouter fails a test before requests can exercise an invalid router.
func mustRouter(t *testing.T, config Config, snapshot *Snapshot) *Router {
	t.Helper()
	router, err := NewRouter(config, snapshot)
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	return router
}

// upstreamAddress extracts the numeric listener endpoint from an httptest server URL.
func upstreamAddress(t *testing.T, rawURL string) netip.AddrPort {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	address, err := netip.ParseAddrPort(parsed.Host)
	if err != nil {
		t.Fatalf("ParseAddrPort(%q) error = %v", parsed.Host, err)
	}
	return address
}

// eventually waits for asynchronous diagnostics without relying on scheduler timing.
func eventually(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

// ExampleRouter_HTTPHandler documents Harbor's exact-name redirect response.
func ExampleRouter_HTTPHandler() {
	snapshot, _ := NewSnapshot([]Route{{
		Host:     "orders.test",
		Upstream: netip.MustParseAddrPort("127.0.0.1:43101"),
	}})
	router, _ := NewRouter(Config{}, snapshot)
	request := httptest.NewRequest(http.MethodGet, "/reports", nil)
	request.Host = "orders.test"
	response := httptest.NewRecorder()
	router.HTTPHandler().ServeHTTP(response, request)
	fmt.Println(response.Code, response.Header().Get("Location"))
	// Output:
	// 308 https://orders.test/reports
}
