package certificates

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/network/httpingress"
	"github.com/goforj/harbor/internal/trust/materialstore"
)

// TestManagerServesTrustedIngressAcrossRestartAndRotation proves the ready-only provider over real high ports.
func TestManagerServesTrustedIngressAcrossRestartAndRotation(t *testing.T) {
	clock := newTestClock(time.Now().UTC().Add(-time.Minute).Round(time.Second))
	config := testManagerConfig(clock)
	store := openTestMaterialStore(t)
	first, err := Bootstrap(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	created, err := first.EnsureLeaf(context.Background(), "portal.test")
	if err != nil {
		t.Fatalf("EnsureLeaf(created) error = %v", err)
	}
	firstRoot, err := first.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot(first) error = %v", err)
	}

	restarted, err := Open(context.Background(), store, config)
	if err != nil {
		t.Fatalf("Open(restart) error = %v", err)
	}
	reused, err := restarted.EnsureLeaf(context.Background(), "portal.test")
	if err != nil {
		t.Fatalf("EnsureLeaf(restart) error = %v", err)
	}
	if reused.Disposition != LeafReused || reused.Fingerprint != created.Fingerprint {
		t.Fatalf("EnsureLeaf(restart) = %#v, created = %#v", reused, created)
	}
	restartedRoot, err := restarted.PublicRoot()
	if err != nil {
		t.Fatalf("PublicRoot(restart) error = %v", err)
	}
	if restartedRoot.Fingerprint != firstRoot.Fingerprint {
		t.Fatalf("root identity changed across restart: %q != %q", restartedRoot.Fingerprint, firstRoot.Fingerprint)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Harbor-Upstream", "portal")
		_, _ = io.WriteString(writer, request.Host+" "+request.URL.RequestURI())
	}))
	defer upstream.Close()
	router := integrationRouter(t, upstream)
	server, err := httpingress.NewServer(httpingress.ServerConfig{}, router, restarted.Certificate)
	if err != nil {
		t.Fatalf("httpingress.NewServer() error = %v", err)
	}
	httpListener := integrationListener(t)
	httpsListener := integrationListener(t)
	httpsAddress := httpsListener.Addr().String()
	serveContext, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(serveContext, httpListener, httpsListener)
	}()
	waitForIntegrationServer(t, server)
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve() shutdown error = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve() did not stop")
		}
	}()

	client := trustedIntegrationClient(t, restartedRoot, clock, "portal.test")
	fingerprint, body := requestIntegrationRoute(t, client, httpsAddress, "portal.test", "/status?full=1")
	if fingerprint != created.Fingerprint || body != "portal.test /status?full=1" {
		t.Fatalf("trusted response = fingerprint %q, body %q", fingerprint, body)
	}
	unknown := trustedIntegrationClient(t, restartedRoot, clock, "unknown.test")
	if _, _, err := requestIntegrationRouteResult(unknown, httpsAddress, "unknown.test", "/"); err == nil {
		t.Fatal("unknown exact SNI completed a handshake")
	}

	clock.Set(clock.Now().Add(3*time.Hour + 30*time.Minute))
	const requests = 48
	fingerprints := make(chan string, requests+2)
	errorsChannel := make(chan error, requests)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < requests; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			fingerprint, _, err := requestIntegrationRouteResult(client, httpsAddress, "portal.test", "/rotation")
			if err != nil {
				errorsChannel <- err
				return
			}
			fingerprints <- fingerprint
		}()
	}
	close(start)
	renewed, renewalErr := restarted.EnsureLeaf(context.Background(), "portal.test")
	workers.Wait()
	close(errorsChannel)
	if renewalErr != nil {
		t.Fatalf("EnsureLeaf(rotation) error = %v", renewalErr)
	}
	if renewed.Disposition != LeafRenewed || renewed.Fingerprint == created.Fingerprint {
		t.Fatalf("EnsureLeaf(rotation) = %#v", renewed)
	}
	for err := range errorsChannel {
		t.Fatalf("request during rotation: %v", err)
	}
	finalFingerprint, _ := requestIntegrationRoute(t, client, httpsAddress, "portal.test", "/final")
	fingerprints <- finalFingerprint
	close(fingerprints)
	for observed := range fingerprints {
		if observed != created.Fingerprint && observed != renewed.Fingerprint {
			t.Fatalf("observed partial or foreign certificate fingerprint %q", observed)
		}
	}
	if finalFingerprint != renewed.Fingerprint {
		t.Fatalf("final fingerprint = %q, want renewed %q", finalFingerprint, renewed.Fingerprint)
	}
}

// integrationRouter builds one exact ingress route for a real private upstream.
func integrationRouter(t *testing.T, upstream *httptest.Server) *httpingress.Router {
	t.Helper()
	address, err := netip.ParseAddrPort(upstream.Listener.Addr().String())
	if err != nil {
		t.Fatalf("ParseAddrPort(upstream) error = %v", err)
	}
	snapshot, err := httpingress.NewSnapshot([]httpingress.Route{{Host: "portal.test", Upstream: address}})
	if err != nil {
		t.Fatalf("httpingress.NewSnapshot() error = %v", err)
	}
	router, err := httpingress.NewRouter(httpingress.Config{}, snapshot)
	if err != nil {
		t.Fatalf("httpingress.NewRouter() error = %v", err)
	}
	return router
}

// integrationListener binds an ephemeral high IPv4 loopback port.
func integrationListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	return listener
}

// waitForIntegrationServer waits until both listeners belong to the ingress lifecycle.
func waitForIntegrationServer(t *testing.T, server *httpingress.Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !server.Snapshot().Running {
		if time.Now().After(deadline) {
			t.Fatal("HTTP ingress did not enter running state")
		}
		time.Sleep(time.Millisecond)
	}
}

// trustedIntegrationClient constructs a fresh-connection client that verifies Harbor's public root and exact name.
func trustedIntegrationClient(t *testing.T, root Root, clock *testClock, host string) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root.CertificatePEM) {
		t.Fatal("AppendCertsFromPEM() rejected public root")
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: host,
			Time:       clock.Now,
		},
		ForceAttemptHTTP2: true,
		DisableKeepAlives: true,
	}}
}

// requestIntegrationRoute makes one trusted request and returns its leaf identity and body.
func requestIntegrationRoute(t *testing.T, client *http.Client, address, host, path string) (string, string) {
	t.Helper()
	fingerprint, body, err := requestIntegrationRouteResult(client, address, host, path)
	if err != nil {
		t.Fatalf("HTTPS request error = %v", err)
	}
	return fingerprint, body
}

// requestIntegrationRouteResult makes one fresh TLS request and returns its peer identity and body.
func requestIntegrationRouteResult(client *http.Client, address, host, path string) (string, string, error) {
	request, err := http.NewRequest(http.MethodGet, "https://"+address+path, nil)
	if err != nil {
		return "", "", err
	}
	request.Host = host
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return "", "", fmt.Errorf("read response: %v; close response: %v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Harbor-Upstream") != "portal" {
		return "", "", fmt.Errorf("HTTPS response = %d, upstream header %q", response.StatusCode, response.Header.Get("X-Harbor-Upstream"))
	}
	if response.TLS == nil {
		return "", "", fmt.Errorf("HTTPS response has no TLS state")
	}
	if len(response.TLS.PeerCertificates) != 1 {
		return "", "", fmt.Errorf("TLS response has %d peer certificates", len(response.TLS.PeerCertificates))
	}
	return fingerprintDER(response.TLS.PeerCertificates[0].Raw), string(body), nil
}

var _ MaterialStore = (*materialstore.Store)(nil)
