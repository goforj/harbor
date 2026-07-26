//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveInstalledDaemonAtAdmitsOnlyOneSelectedRelease pins launcher execution beneath the release root.
func TestResolveInstalledDaemonAtAdmitsOnlyOneSelectedRelease(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "releases", "42")
	if err := os.MkdirAll(filepath.Join(release, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	daemon := filepath.Join(release, "bin", "harbord")
	if err := os.WriteFile(daemon, []byte("daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join("releases", "42"), current); err != nil {
		t.Fatal(err)
	}

	got, err := resolveInstalledDaemonAt(root, current)
	if err != nil {
		t.Fatalf("resolveInstalledDaemonAt() error = %v", err)
	}
	if got != daemon {
		t.Fatalf("resolveInstalledDaemonAt() = %q, want %q", got, daemon)
	}
}

// TestResolveInstalledDaemonAtRejectsASelectionOutsideTheReleaseRoot prevents path indirection from becoming execution authority.
func TestResolveInstalledDaemonAtRejectsASelectionOutsideTheReleaseRoot(t *testing.T) {
	root := t.TempDir()
	foreign := t.TempDir()
	current := filepath.Join(root, "current")
	if err := os.Symlink(foreign, current); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveInstalledDaemonAt(root, current); err == nil {
		t.Fatal("resolveInstalledDaemonAt() error = nil")
	}
}
