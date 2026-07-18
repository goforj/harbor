//go:build windows

package runtimepath

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestPlatformDirectoryUsesLocalAppData verifies Windows runtime state remains within Harbor's per-user root.
func TestPlatformDirectoryUsesLocalAppData(t *testing.T) {
	root := `C:\Users\harbor\AppData\Local\GoForj\Harbor`
	directory, err := platformDirectory(nil, func() (string, error) {
		return root, nil
	}, nil)
	if err != nil {
		t.Fatalf("resolve runtime directory: %v", err)
	}
	if want := filepath.Join(root, "runtime"); directory != want {
		t.Fatalf("runtime directory = %q, want %q", directory, want)
	}
}

// TestPlatformDirectoryPreservesDataError verifies Windows does not fall back to a machine-global temporary root.
func TestPlatformDirectoryPreservesDataError(t *testing.T) {
	want := errors.New("data directory unavailable")
	_, err := platformDirectory(nil, func() (string, error) {
		return "", want
	}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}
