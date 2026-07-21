//go:build darwin || linux

package local

import (
	"path/filepath"
	"testing"
)

// TestEndpointReferenceUsesCurrentUserUnixSocket verifies launch context discovery does not require a live daemon connection.
func TestEndpointReferenceUsesCurrentUserUnixSocket(t *testing.T) {
	reference, err := EndpointReference()
	if err != nil {
		t.Fatalf("EndpointReference() error = %v", err)
	}
	if !filepath.IsAbs(reference) {
		t.Fatalf("EndpointReference() = %q, want absolute Unix path", reference)
	}
}
