//go:build linux

package userpaths

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestPlatformDataDirectoryUsesXDGDataHome verifies an explicit XDG data root takes precedence over HOME.
func TestPlatformDataDirectoryUsesXDGDataHome(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "var", "data")
	got, err := platformDataDirectory(
		func(name string) (string, bool) {
			return root, name == xdgDataHome
		},
		func() (string, error) {
			return "", errors.New("HOME should not be resolved")
		},
	)
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	if want := filepath.Join(root, "goforj", "harbor"); got != want {
		t.Fatalf("data directory = %q, want %q", got, want)
	}
}

// TestPlatformDataDirectoryFallsBackToHome verifies the XDG default layout when no override exists.
func TestPlatformDataDirectoryFallsBackToHome(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "harbor")
	got, err := platformDataDirectory(
		func(string) (string, bool) {
			return "", false
		},
		func() (string, error) {
			return home, nil
		},
	)
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "goforj", "harbor"); got != want {
		t.Fatalf("data directory = %q, want %q", got, want)
	}
}

// TestPlatformDataDirectoryIgnoresRelativeXDGDataHome verifies invalid XDG paths cannot depend on the daemon working directory.
func TestPlatformDataDirectoryIgnoresRelativeXDGDataHome(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "harbor")
	got, err := platformDataDirectory(
		func(name string) (string, bool) {
			return filepath.Join("relative", "data"), name == xdgDataHome
		},
		func() (string, error) {
			return home, nil
		},
	)
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "goforj", "harbor"); got != want {
		t.Fatalf("data directory = %q, want %q", got, want)
	}
}

// TestPlatformDataDirectoryReturnsHomeError verifies a missing XDG root does not hide home lookup failures.
func TestPlatformDataDirectoryReturnsHomeError(t *testing.T) {
	want := errors.New("HOME is unavailable")
	_, err := platformDataDirectory(
		func(string) (string, bool) {
			return "", false
		},
		func() (string, error) {
			return "", want
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}
