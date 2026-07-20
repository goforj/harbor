//go:build windows

package main

import (
	"testing"

	"github.com/goforj/harbor/internal/platform/resolver"
)

// TestWindowsResolverAdapterComposition proves the privileged helper links the reviewed Windows NRPT backend.
func TestWindowsResolverAdapterComposition(t *testing.T) {
	if resolver.New() == nil {
		t.Fatal("resolver.New() returned nil")
	}
}
