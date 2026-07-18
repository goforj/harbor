//go:build windows

package userpaths

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestPlatformDataDirectoryUsesLocalAppData verifies Harbor follows the Windows per-user data convention.
func TestPlatformDataDirectoryUsesLocalAppData(t *testing.T) {
	root := `C:\Users\harbor\AppData\Local`
	got, err := platformDataDirectory(
		func(name string) (string, bool) {
			return root, name == localAppData
		},
		func() (string, error) {
			return "", errors.New("USERPROFILE should not be resolved")
		},
	)
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	if want := filepath.Join(root, "GoForj", "Harbor"); got != want {
		t.Fatalf("data directory = %q, want %q", got, want)
	}
}

// TestPlatformDataDirectoryFallsBackToUserProfile verifies stripped environments retain the standard Windows layout.
func TestPlatformDataDirectoryFallsBackToUserProfile(t *testing.T) {
	home := `C:\Users\harbor`
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
	if want := filepath.Join(home, "AppData", "Local", "GoForj", "Harbor"); got != want {
		t.Fatalf("data directory = %q, want %q", got, want)
	}
}

// TestPlatformDataDirectoryReturnsHomeError verifies Windows does not invent a process-relative state path.
func TestPlatformDataDirectoryReturnsHomeError(t *testing.T) {
	want := errors.New("LOCALAPPDATA and USERPROFILE are unavailable")
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
