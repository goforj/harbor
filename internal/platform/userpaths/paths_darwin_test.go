//go:build darwin

package userpaths

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestPlatformDataDirectoryUsesApplicationSupport verifies Harbor follows the macOS per-user data convention.
func TestPlatformDataDirectoryUsesApplicationSupport(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "harbor")
	got, err := platformDataDirectory(nil, func() (string, error) {
		return home, nil
	})
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	if want := filepath.Join(home, "Library", "Application Support", "GoForj", "Harbor"); got != want {
		t.Fatalf("data directory = %q, want %q", got, want)
	}
}

// TestPlatformDataDirectoryReturnsHomeError verifies macOS does not invent a process-relative state path.
func TestPlatformDataDirectoryReturnsHomeError(t *testing.T) {
	want := errors.New("HOME is unavailable")
	_, err := platformDataDirectory(nil, func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}
