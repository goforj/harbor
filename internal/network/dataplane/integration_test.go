package dataplane

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// TestRuntimeServesExactSyntheticGeneration exercises the complete local data plane without host mutation or trust-store access.
func TestRuntimeServesExactSyntheticGeneration(t *testing.T) {
	alphaHTTP := newHTTPMarkerServer(t, "alpha")
	defer alphaHTTP.Close()
	betaHTTP := newHTTPMarkerServer(t, "beta")
	defer betaHTTP.Close()
	alphaNative := newNativeMarkerServer(t, "alpha")
	defer alphaNative.close()
	betaNative := newNativeMarkerServer(t, "beta")
	defer betaNative.close()

	ports := reserveTCPPorts(t, 4)
	listeners := ListenerPlan{
		DNS:   reserveDNSPort(t),
		HTTP:  ports[0],
		HTTPS: ports[1],
	}
	desired := mustDesiredState(
		t,
		listeners,
		[]HTTPRoute{
			{ID: "http:beta", Host: "beta.test", Upstream: serverEndpoint(t, betaHTTP.Listener)},
			{ID: "http:alpha", Host: "alpha.test", Upstream: serverEndpoint(t, alphaHTTP.Listener)},
		},
		[]NativeRoute{
			{ID: "tcp:beta", Host: "db.beta.test", Listen: ports[3], Upstream: betaNative.endpoint},
			{ID: "tcp:alpha", Host: "db.alpha.test", Listen: ports[2], Upstream: alphaNative.endpoint},
		},
	)
	certificate := testTLSCertificate(t)
	var certificateMutex sync.Mutex
	certificateHosts := make([]string, 0, 2)
	config := Config{
		Desired: desired,
		CertificateProvider: func(_ context.Context, host string) (*tls.Certificate, error) {
			certificateMutex.Lock()
			certificateHosts = append(certificateHosts, host)
			certificateMutex.Unlock()
			return certificate, nil
		},
		StartupTimeout:  5 * time.Second,
		ShutdownTimeout: 2 * time.Second,
	}
	runtime := mustRuntime(t, config)
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	if err := runtime.Start(parent); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runtime.Close(ctx)
	})

	ready := runtime.Snapshot()
	if err := ready.Validate(); err != nil {
		t.Fatalf("ready Snapshot().Validate() error = %v", err)
	}
	if ready.State != StateReady || ready.DNS.Records != 4 || ready.Ingress.Routes != 2 || len(ready.Relays) != 2 {
		t.Fatalf("ready Snapshot() = %#v", ready)
	}
	if ready.Relays[0].Host != "db.alpha.test" || ready.Relays[1].Host != "db.beta.test" {
		t.Fatalf("ready relay order = %#v", ready.Relays)
	}

	for _, network := range []string{"udp", "tcp"} {
		assertDNSAnswer(t, listeners.DNS, network, "alpha.test", listeners.HTTPS.Addr())
		assertDNSAnswer(t, listeners.DNS, network, "beta.test", listeners.HTTPS.Addr())
		assertDNSAnswer(t, listeners.DNS, network, "db.alpha.test", ports[2].Addr())
		assertDNSAnswer(t, listeners.DNS, network, "db.beta.test", ports[3].Addr())
		assertDNSRcode(t, listeners.DNS, network, "missing.test", dns.RcodeNameError)
	}

	redirectClient := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       5 * time.Second,
	}
	redirect := performHTTPRoute(t, redirectClient, "http://"+listeners.HTTP.String()+"/docs?q=harbor", "alpha.test")
	if redirect.StatusCode != http.StatusPermanentRedirect || redirect.Header.Get("Location") != "https://alpha.test/docs?q=harbor" {
		body := readResponseBody(t, redirect)
		t.Fatalf("HTTP redirect = status %d, location %q, body %q", redirect.StatusCode, redirect.Header.Get("Location"), body)
	}
	_ = readResponseBody(t, redirect)
	unknownHTTP := performHTTPRoute(t, redirectClient, "http://"+listeners.HTTP.String()+"/", "unknown.test")
	if unknownHTTP.StatusCode != http.StatusMisdirectedRequest {
		body := readResponseBody(t, unknownHTTP)
		t.Fatalf("unknown HTTP route = status %d, body %q", unknownHTTP.StatusCode, body)
	}
	_ = readResponseBody(t, unknownHTTP)

	for _, route := range []struct {
		host string
		want string
	}{
		{host: "alpha.test", want: "alpha|alpha.test|/health?from=integration"},
		{host: "beta.test", want: "beta|beta.test|/health?from=integration"},
	} {
		client, transport := newHTTPSClient(route.host)
		response := performHTTPRoute(t, client, "https://"+listeners.HTTPS.String()+"/health?from=integration", route.host)
		body := readResponseBody(t, response)
		transport.CloseIdleConnections()
		if response.StatusCode != http.StatusOK || body != route.want {
			t.Fatalf("HTTPS route %q = status %d, body %q", route.host, response.StatusCode, body)
		}
		if response.ProtoMajor != 2 {
			t.Fatalf("HTTPS route %q protocol = %q, want HTTP/2", route.host, response.Proto)
		}
	}
	assertUnknownSNIRejected(t, listeners.HTTPS)

	assertNativeExchange(t, ports[2], "ping-alpha\n", "alpha|ping-alpha\n")
	assertNativeExchange(t, ports[3], "ping-beta\n", "beta|ping-beta\n")
	waitForRelayCounters(t, runtime)
	certificateMutex.Lock()
	gotCertificateHosts := append([]string{}, certificateHosts...)
	certificateMutex.Unlock()
	if strings.Join(gotCertificateHosts, ",") != "alpha.test,beta.test" {
		t.Fatalf("certificate provider hosts = %v", gotCertificateHosts)
	}

	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	stopped := runtime.Snapshot()
	if stopped.State != StateStopped || runtime.Err() != nil {
		t.Fatalf("stopped runtime = state %s, error %v", stopped.State, runtime.Err())
	}
	if err := stopped.Validate(); err != nil {
		t.Fatalf("stopped Snapshot().Validate() error = %v", err)
	}
	for _, endpoint := range ports {
		assertTCPRebindable(t, endpoint)
	}
	assertDNSRebindable(t, listeners.DNS)

	replacement := mustRuntime(t, config)
	if err := replacement.Start(context.Background()); err != nil {
		t.Fatalf("replacement Start() error = %v", err)
	}
	if snapshot := replacement.Snapshot(); snapshot.State != StateReady {
		t.Fatalf("replacement Snapshot() = %#v", snapshot)
	} else if err := snapshot.Validate(); err != nil {
		t.Fatalf("replacement Snapshot().Validate() error = %v", err)
	}
	if err := replacement.Close(context.Background()); err != nil {
		t.Fatalf("replacement Close() error = %v", err)
	}
	for _, endpoint := range ports {
		assertTCPRebindable(t, endpoint)
	}
	assertDNSRebindable(t, listeners.DNS)
}

// newHTTPMarkerServer returns one private upstream that exposes exact proxy routing in its body.
func newHTTPMarkerServer(t *testing.T, marker string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprintf(writer, "%s|%s|%s", marker, request.Host, request.URL.RequestURI())
	}))
}

// testTLSCertificate returns ephemeral key material without involving Harbor's trust implementation.
func testTLSCertificate(t *testing.T) *tls.Certificate {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := server.TLS.Certificates[0]
	server.Close()
	return &certificate
}

// serverEndpoint extracts one canonical explicit loopback endpoint from a synthetic server.
func serverEndpoint(t *testing.T, listener net.Listener) netip.AddrPort {
	t.Helper()
	endpoint, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("parse synthetic server address: %v", err)
	}
	return endpoint
}

// performHTTPRoute sends one direct-socket request while retaining the public Harbor authority.
func performHTTPRoute(t *testing.T, client *http.Client, target string, host string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("construct request for %q: %v", host, err)
	}
	request.Host = host
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request %q through data plane: %v", host, err)
	}
	return response
}

// readResponseBody closes every response promptly so ingress shutdown does not retain test clients.
func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

// newHTTPSClient reaches the explicit listener while authenticating routing with one exact SNI value.
func newHTTPSClient(host string) (*http.Client, *http.Transport) {
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // The integration boundary is routing, not the separately tested trust store.
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
		},
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, transport
}

// assertUnknownSNIRejected proves the certificate provider cannot turn an unregistered name into a route.
func assertUnknownSNIRejected(t *testing.T, endpoint netip.AddrPort) {
	t.Helper()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	connection, err := tls.DialWithDialer(dialer, "tcp4", endpoint.String(), &tls.Config{
		InsecureSkipVerify: true, // The handshake must reach SNI authorization to exercise the rejection.
		ServerName:         "unknown.test",
		MinVersion:         tls.VersionTLS12,
	})
	if err == nil {
		_ = connection.Close()
		t.Fatal("unknown TLS SNI completed a handshake")
	}
}

// assertDNSAnswer checks one exact A response over the selected authoritative transport.
func assertDNSAnswer(t *testing.T, endpoint netip.AddrPort, network string, name string, want netip.Addr) {
	t.Helper()
	response := exchangeDNS(t, endpoint, network, name)
	if response.Rcode != dns.RcodeSuccess || len(response.Answer) != 1 {
		t.Fatalf("DNS %s answer for %q = rcode %s, answers %#v", network, name, dns.RcodeToString[response.Rcode], response.Answer)
	}
	record, ok := response.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("DNS %s answer for %q has type %T", network, name, response.Answer[0])
	}
	address, ok := netip.AddrFromSlice(record.A)
	if !ok || address.Unmap() != want.Unmap() {
		t.Fatalf("DNS %s answer for %q = %s, want %s", network, name, record.A, want)
	}
}

// assertDNSRcode checks authoritative negative behavior without relying on the operating-system resolver.
func assertDNSRcode(t *testing.T, endpoint netip.AddrPort, network string, name string, want int) {
	t.Helper()
	response := exchangeDNS(t, endpoint, network, name)
	if response.Rcode != want {
		t.Fatalf("DNS %s rcode for %q = %s, want %s", network, name, dns.RcodeToString[response.Rcode], dns.RcodeToString[want])
	}
}

// exchangeDNS performs one direct exact-name query against the configured high-port listener.
func exchangeDNS(t *testing.T, endpoint netip.AddrPort, network string, name string) *dns.Msg {
	t.Helper()
	message := new(dns.Msg)
	message.SetQuestion(dns.Fqdn(name), dns.TypeA)
	response, _, err := (&dns.Client{Net: network, Timeout: 5 * time.Second}).Exchange(message, endpoint.String())
	if err != nil {
		t.Fatalf("exchange DNS %s query for %q: %v", network, name, err)
	}
	return response
}

// nativeMarkerServer is one bounded private TCP upstream used to prove byte-transparent selection.
type nativeMarkerServer struct {
	listener net.Listener
	endpoint netip.AddrPort
	marker   string
	done     chan struct{}
}

// newNativeMarkerServer starts one loopback server that prefixes a single newline-delimited payload.
func newNativeMarkerServer(t *testing.T, marker string) *nativeMarkerServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start native marker server: %v", err)
	}
	server := &nativeMarkerServer{
		listener: listener,
		endpoint: serverEndpoint(t, listener),
		marker:   marker,
		done:     make(chan struct{}),
	}
	go server.serve()
	return server
}

// serve accepts independent synthetic clients until test cleanup closes the private listener.
func (server *nativeMarkerServer) serve() {
	defer close(server.done)
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			return
		}
		go server.respond(connection)
	}
}

// respond returns the selected upstream marker and original bytes before closing its connection.
func (server *nativeMarkerServer) respond(connection net.Conn) {
	defer connection.Close()
	payload, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		return
	}
	_, _ = io.WriteString(connection, server.marker+"|"+payload)
}

// close stops admission and joins the server's accept loop.
func (server *nativeMarkerServer) close() {
	_ = server.listener.Close()
	<-server.done
}

// assertNativeExchange verifies a public relay selects the right private upstream without altering bytes.
func assertNativeExchange(t *testing.T, endpoint netip.AddrPort, request string, want string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp4", endpoint.String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial native route %s: %v", endpoint, err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set native route deadline: %v", err)
	}
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatalf("write native route %s: %v", endpoint, err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatalf("read native route %s: %v", endpoint, err)
	}
	if string(response) != want {
		t.Fatalf("native route %s response = %q, want %q", endpoint, response, want)
	}
}

// waitForRelayCounters waits for asynchronous copy completion before inspecting payload-free status.
func waitForRelayCounters(t *testing.T, runtime *Runtime) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := runtime.Snapshot()
		complete := len(snapshot.Relays) == 2
		for _, relay := range snapshot.Relays {
			complete = complete && relay.AcceptedConnections == 1 && relay.CompletedConnections == 1
			complete = complete && relay.ClientBytes > 0 && relay.UpstreamBytes > 0 && relay.DialFailures == 0
		}
		if complete {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("relay counters did not settle: %#v", runtime.Snapshot().Relays)
}
