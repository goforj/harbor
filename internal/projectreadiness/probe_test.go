package projectreadiness

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/projectdiscovery"
)

// readinessClientFunc adapts one deterministic response function to HTTPClient.
type readinessClientFunc func(*http.Request) (*http.Response, error)

// Do delegates the request to the deterministic fixture.
func (client readinessClientFunc) Do(request *http.Request) (*http.Response, error) {
	return client(request)
}

// TestProberRequiresExactGeneratedReadyResponse proves process survival cannot substitute for App readiness.
func TestProberRequiresExactGeneratedReadyResponse(t *testing.T) {
	client := readinessClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != readinessTestTarget().ReadyURL || request.Header.Get("Accept") != "application/json" {
			t.Fatalf("readiness request = %#v", request)
		}
		return readinessTestResponse(http.StatusOK, `{"status":"ready","app":"app"}`), nil
	})
	state, err := NewProber(client).Probe(t.Context(), readinessTestTarget())
	if err != nil || state != StateReady {
		t.Fatalf("Probe() = %q, %v", state, err)
	}
}

// TestProberTreatsStartupAbsenceAndExplicitUnreadyAsPending keeps normal launch convergence non-terminal.
func TestProberTreatsStartupAbsenceAndExplicitUnreadyAsPending(t *testing.T) {
	tests := []struct {
		name   string
		client HTTPClient
	}{
		{name: "listener absent", client: readinessClientFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})},
		{name: "not ready", client: readinessClientFunc(func(*http.Request) (*http.Response, error) {
			return readinessTestResponse(http.StatusServiceUnavailable, `{"status":"not_ready","app":"app"}`), nil
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := NewProber(test.client).Probe(t.Context(), readinessTestTarget())
			if err != nil || state != StatePending {
				t.Fatalf("Probe() = %q, %v", state, err)
			}
		})
	}
}

// TestProberRejectsFalseSuccessfulResponses covers every body and identity boundary behind online publication.
func TestProberRejectsFalseSuccessfulResponses(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{name: "unexpected HTTP", code: http.StatusAccepted, body: `{}`, want: "HTTP 202"},
		{name: "malformed", code: http.StatusOK, body: `{`, want: "decode"},
		{name: "multiple", code: http.StatusOK, body: `{"status":"ready","app":"app"} {}`, want: "multiple JSON"},
		{name: "missing status", code: http.StatusOK, body: `{"app":"app"}`, want: "canonical"},
		{name: "wrong status", code: http.StatusOK, body: `{"status":"not_ready","app":"app"}`, want: "not \"ready\""},
		{name: "wrong App", code: http.StatusOK, body: `{"status":"ready","app":"other"}`, want: "not \"app\""},
		{name: "spaced identity", code: http.StatusOK, body: `{"status":"ready","app":" app "}`, want: "canonical"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := readinessClientFunc(func(*http.Request) (*http.Response, error) {
				return readinessTestResponse(test.code, test.body), nil
			})
			_, err := NewProber(client).Probe(t.Context(), readinessTestTarget())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Probe() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestProberBoundsEveryResponseBody keeps an untrusted local listener from consuming unbounded memory or work.
func TestProberBoundsEveryResponseBody(t *testing.T) {
	large := strings.Repeat("x", int(maximumReadinessBodyBytes)+1)
	for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusBadGateway} {
		client := readinessClientFunc(func(*http.Request) (*http.Response, error) {
			return readinessTestResponse(code, large), nil
		})
		_, err := NewProber(client).Probe(t.Context(), readinessTestTarget())
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("Probe(HTTP %d) error = %v", code, err)
		}
	}
}

// TestProberPreservesCancellationAndRejectsIncompleteClients keeps transport lifecycle failures distinct from pending startup.
func TestProberPreservesCancellationAndRejectsIncompleteClients(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := readinessClientFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled Probe reached HTTP client")
		return nil, nil
	})
	if _, err := NewProber(client).Probe(ctx, readinessTestTarget()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe(cancelled) error = %v", err)
	}

	incomplete := readinessClientFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	if _, err := NewProber(incomplete).Probe(t.Context(), readinessTestTarget()); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Probe(incomplete) error = %v", err)
	}
}

// readinessTestTarget returns the generated default App target used by probe fixtures.
func readinessTestTarget() projectdiscovery.RuntimeTarget {
	return projectdiscovery.RuntimeTarget{
		AppID:       "app",
		Name:        "App",
		Port:        3000,
		ResourceURL: "http://127.0.0.1:3000",
		ReadyURL:    "http://127.0.0.1:3000/-/ready",
	}
}

// readinessTestResponse creates one closeable response body for the HTTP fixture.
func readinessTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}
