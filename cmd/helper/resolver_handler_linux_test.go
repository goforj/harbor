//go:build linux

package main

import (
	"testing"

	"github.com/goforj/harbor/internal/platform/resolver"
)

// TestLinuxResolverAdapterComposition proves the production helper links the implemented Linux resolver backend.
func TestLinuxResolverAdapterComposition(t *testing.T) {
	if resolver.New() == nil {
		t.Fatal("resolver.New() returned nil")
	}
}
