package runtimepath

import (
	"path/filepath"
	"testing"
)

// TestDirectoryReturnsAnAbsolutePath verifies runtime state cannot inherit daemon working-directory semantics.
func TestDirectoryReturnsAnAbsolutePath(t *testing.T) {
	directory, err := Directory()
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if !filepath.IsAbs(directory) {
		t.Fatalf("runtime directory = %q, want an absolute path", directory)
	}
}
