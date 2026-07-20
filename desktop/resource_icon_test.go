package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDiscoverResourceIconResolvesIconAgainstResourcePage proves routed resources retain their own asset base path.
func TestDiscoverResourceIconResolvesIconAgainstResourcePage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/lighthouse" {
			t.Fatalf("request path = %q, want /lighthouse", request.URL.Path)
		}
		_, _ = response.Write([]byte(`<!doctype html><link rel="icon" href="/lighthouse/assets/favicon.png">`))
	}))
	defer server.Close()

	iconURL, err := discoverResourceIcon(context.Background(), server.Client(), server.URL+"/lighthouse")
	if err != nil {
		t.Fatalf("discoverResourceIcon() error = %v", err)
	}
	if want := server.URL + "/lighthouse/assets/favicon.png"; iconURL != want {
		t.Fatalf("discoverResourceIcon() = %q, want %q", iconURL, want)
	}
}

// TestDiscoverResourceIconRejectsRemoteAssets keeps a resource metadata lookup inside its reviewed local origin.
func TestDiscoverResourceIconRejectsRemoteAssets(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<!doctype html><link rel="icon" href="https://example.test/favicon.png">`))
	}))
	defer server.Close()

	iconURL, err := discoverResourceIcon(context.Background(), server.Client(), server.URL+"/lighthouse")
	if err != nil {
		t.Fatalf("discoverResourceIcon() error = %v", err)
	}
	if iconURL != "" {
		t.Fatalf("discoverResourceIcon() = %q, want empty remote icon", iconURL)
	}
}
