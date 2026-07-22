//go:build darwin

package runtimepath

import (
	"os"
	"path/filepath"
	"strconv"
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

// TestOutputBrokerDirectoryFallsBackWithoutRelocatingDaemon preserves daemon authority while shortening broker endpoints.
func TestOutputBrokerDirectoryFallsBackWithoutRelocatingDaemon(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), strings.Repeat("temporary-root", 4))
	preferred := filepath.Join(root, "goforj-harbor-"+strconv.Itoa(os.Geteuid()))
	if !unixSocketPathFits(preferred) {
		t.Fatalf("preferred daemon socket path does not fit the regression boundary: %q", preferred)
	}
	if outputBrokerUnixSocketPathFits(preferred) {
		t.Fatalf("preferred output-broker socket unexpectedly fits the regression boundary: %q", preferred)
	}
	directory, err := platformDirectory(nil, nil, func() string {
		return root
	})
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if directory != preferred {
		t.Fatalf("runtime directory = %q, want preferred daemon directory %q", directory, preferred)
	}
	brokerDirectory, err := darwinOutputBrokerDirectory(root, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("resolve output broker runtime directory: %v", err)
	}
	if !strings.HasPrefix(brokerDirectory, darwinTemporaryRoot+string(filepath.Separator)+"goforj-harbor-") {
		t.Fatalf("output broker runtime directory = %q, want short system temporary fallback", brokerDirectory)
	}
	if !outputBrokerUnixSocketPathFits(brokerDirectory) {
		t.Fatalf("fallback output-broker socket path exceeds the portable limit: %q", brokerDirectory)
	}
}

// TestDarwinOutputBrokerDirectoryKeepsLegacyFallbackWithinTheSocketLimit verifies every uint32 UID fits the established fallback shape.
func TestDarwinOutputBrokerDirectoryKeepsLegacyFallbackWithinTheSocketLimit(t *testing.T) {
	directory, err := darwinOutputBrokerDirectory(strings.Repeat("temporary-root", 10), ^uint32(0))
	if err != nil {
		t.Fatalf("resolve output broker runtime directory: %v", err)
	}
	want := filepath.Join(darwinTemporaryRoot, "goforj-harbor-4294967295")
	if directory != want {
		t.Fatalf("runtime directory = %q, want %q", directory, want)
	}
	if !outputBrokerUnixSocketPathFits(directory) {
		t.Fatalf("legacy fallback output-broker socket path exceeds the portable limit: %q", directory)
	}
}
