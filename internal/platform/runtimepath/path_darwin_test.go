//go:build darwin

package runtimepath

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPlatformDirectoryUsesTemporaryRoot verifies macOS runtime state remains short and UID-scoped.
func TestPlatformDirectoryUsesTemporaryRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "var", "folders", "harbor", "T")
	directory, err := platformDirectory(nil, nil, func() string {
		return root
	})
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if !strings.HasPrefix(directory, root+string(filepath.Separator)+"goforj-harbor-") {
		t.Fatalf("runtime directory = %q, want UID-scoped child of %q", directory, root)
	}
}

// TestPlatformDirectoryUsesShortFallback verifies long macOS temporary roots cannot exceed sockaddr limits.
func TestPlatformDirectoryUsesShortFallback(t *testing.T) {
	longRoot := filepath.Join(string(filepath.Separator), strings.Repeat("temporary-root", 10))
	directory, err := platformDirectory(nil, nil, func() string {
		return longRoot
	})
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if !strings.HasPrefix(directory, darwinTemporaryRoot+string(filepath.Separator)+"goforj-harbor-") {
		t.Fatalf("runtime directory = %q, want short system temporary fallback", directory)
	}
}
