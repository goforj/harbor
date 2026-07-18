//go:build linux

package runtimepath

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPlatformDirectoryUsesXDGRuntimeDirectory verifies the login session runtime root takes precedence on Linux.
func TestPlatformDirectoryUsesXDGRuntimeDirectory(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "run", "user", "1000")
	directory, err := platformDirectory(
		func(name string) (string, bool) {
			return root, name == xdgRuntimeDirectory
		},
		nil,
		func() string { return "" },
	)
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if want := filepath.Join(root, "goforj", "harbor"); directory != want {
		t.Fatalf("runtime directory = %q, want %q", directory, want)
	}
}

// TestPlatformDirectoryFallsBackToScopedTemporaryRoot verifies services without an XDG login session retain a short per-user path.
func TestPlatformDirectoryFallsBackToScopedTemporaryRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "var", "tmp")
	directory, err := platformDirectory(
		func(string) (string, bool) {
			return "", false
		},
		nil,
		func() string { return root },
	)
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if !strings.HasPrefix(directory, root+string(filepath.Separator)+"goforj-harbor-") {
		t.Fatalf("runtime directory = %q, want UID-scoped child of %q", directory, root)
	}
}

// TestPlatformDirectoryRejectsRelativeXDGRuntimeDirectory verifies malformed session state cannot redirect locks into a checkout.
func TestPlatformDirectoryRejectsRelativeXDGRuntimeDirectory(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "var", "tmp")
	directory, err := platformDirectory(
		func(name string) (string, bool) {
			return filepath.Join("run", "user", "1000"), name == xdgRuntimeDirectory
		},
		nil,
		func() string { return root },
	)
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if !strings.HasPrefix(directory, root+string(filepath.Separator)+"goforj-harbor-") {
		t.Fatalf("runtime directory = %q, want UID-scoped child of %q", directory, root)
	}
}

// TestPlatformDirectoryUsesShortFallbackForLongXDGRoot verifies endpoint discovery cannot produce an unusable Unix socket.
func TestPlatformDirectoryUsesShortFallbackForLongXDGRoot(t *testing.T) {
	longRoot := filepath.Join(string(filepath.Separator), strings.Repeat("long-runtime-root", 10))
	root := filepath.Join(string(filepath.Separator), "var", "tmp")
	directory, err := platformDirectory(
		func(name string) (string, bool) {
			return longRoot, name == xdgRuntimeDirectory
		},
		nil,
		func() string { return root },
	)
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if !strings.HasPrefix(directory, root+string(filepath.Separator)+"goforj-harbor-") {
		t.Fatalf("runtime directory = %q, want short UID-scoped fallback", directory)
	}
}

// TestPlatformDirectoryFallsBackFromInvalidTemporaryRoot verifies relative process environment cannot redirect daemon state.
func TestPlatformDirectoryFallsBackFromInvalidTemporaryRoot(t *testing.T) {
	directory, err := platformDirectory(
		func(string) (string, bool) {
			return "", false
		},
		nil,
		func() string { return filepath.Join("relative", "tmp") },
	)
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if !strings.HasPrefix(directory, unixTemporaryRoot+string(filepath.Separator)+"goforj-harbor-") {
		t.Fatalf("runtime directory = %q, want system temporary fallback", directory)
	}
}
