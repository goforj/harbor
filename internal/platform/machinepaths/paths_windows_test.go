//go:build windows

package machinepaths

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsPlatformRootUsesProgramDataKnownFolder verifies the production resolver invokes the native machine-global API.
func TestWindowsPlatformRootUsesProgramDataKnownFolder(t *testing.T) {
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatalf("windows.KnownFolderPath() error = %v", err)
	}
	root, err := platformRoot()
	if err != nil {
		t.Fatalf("platformRoot() error = %v", err)
	}
	if want := filepath.Join(filepath.Clean(programData), "GoForj", "Harbor", "Privileged"); root != want {
		t.Fatalf("platformRoot() = %q, want %q", root, want)
	}
}

// TestWindowsPlatformRootRequestsProgramData proves the resolver cannot drift to a user-scoped Known Folder.
func TestWindowsPlatformRootRequestsProgramData(t *testing.T) {
	programData := `C:\ProgramData`
	called := false
	root, err := platformRootFromKnownFolder(func(folderID *windows.KNOWNFOLDERID, flags uint32) (string, error) {
		called = true
		if folderID != windows.FOLDERID_ProgramData {
			t.Fatalf("folder ID = %v, want FOLDERID_ProgramData", folderID)
		}
		if flags != windows.KF_FLAG_DEFAULT {
			t.Fatalf("flags = %d, want KF_FLAG_DEFAULT", flags)
		}
		return programData, nil
	})
	if err != nil {
		t.Fatalf("platformRootFromKnownFolder() error = %v", err)
	}
	if !called {
		t.Fatal("Known Folder lookup was not called")
	}
	if want := filepath.Join(programData, "GoForj", "Harbor", "Privileged"); root != want {
		t.Fatalf("platformRootFromKnownFolder() = %q, want %q", root, want)
	}
}

// TestWindowsPlatformRootPreservesNativeFailure keeps installer diagnostics attached to API failures.
func TestWindowsPlatformRootPreservesNativeFailure(t *testing.T) {
	want := errors.New("known folder unavailable")
	_, err := platformRootFromKnownFolder(func(*windows.KNOWNFOLDERID, uint32) (string, error) {
		return "", want
	})
	if !errors.Is(err, want) {
		t.Fatalf("platformRootFromKnownFolder() error = %v, want wrapped %v", err, want)
	}
}

// TestWindowsPlatformRootRejectsUnsafeNativePaths prevents malformed API results from gaining working-directory semantics.
func TestWindowsPlatformRootRejectsUnsafeNativePaths(t *testing.T) {
	for _, path := range []string{"", `ProgramData`} {
		t.Run(path, func(t *testing.T) {
			_, err := platformRootFromKnownFolder(func(*windows.KNOWNFOLDERID, uint32) (string, error) {
				return path, nil
			})
			if err == nil || !strings.Contains(err.Error(), "ProgramData") {
				t.Fatalf("platformRootFromKnownFolder(%q) error = %v, want ProgramData path failure", path, err)
			}
		})
	}
}
