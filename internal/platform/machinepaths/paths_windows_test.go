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
	flags := uint32(windows.KF_FLAG_DONT_VERIFY)
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, flags)
	if err != nil {
		t.Fatalf("windows.KnownFolderPath() error = %v", err)
	}
	programData, err = expandWindowsEnvironment(programData)
	if err != nil {
		t.Fatalf("expandWindowsEnvironment() error = %v", err)
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
		wantFlags := uint32(windows.KF_FLAG_DONT_VERIFY)
		if flags != wantFlags {
			t.Fatalf("flags = %d, want %d", flags, wantFlags)
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

// TestWindowsPlatformRootExpandsNativeProgramData proves expandable Known Folder results become canonical absolute paths.
func TestWindowsPlatformRootExpandsNativeProgramData(t *testing.T) {
	root, err := platformRootFromNative(
		func(*windows.KNOWNFOLDERID, uint32) (string, error) {
			return `%SystemDrive%\ProgramData`, nil
		},
		func(value string) (string, error) {
			if value != `%SystemDrive%\ProgramData` {
				t.Fatalf("expand value = %q", value)
			}
			return `C:\ProgramData`, nil
		},
	)
	if err != nil {
		t.Fatalf("platformRootFromNative() error = %v", err)
	}
	if want := `C:\ProgramData\GoForj\Harbor\Privileged`; root != want {
		t.Fatalf("platformRootFromNative() = %q, want %q", root, want)
	}
}

// TestResolveWindowsSystemDriveUsesNativeWindowsDirectory covers hosted processes without a SystemDrive variable.
func TestResolveWindowsSystemDriveUsesNativeWindowsDirectory(t *testing.T) {
	resolved, err := resolveWindowsSystemDrive(`%SystemDrive%\ProgramData`, func() (string, error) {
		return `C:\Windows`, nil
	})
	if err != nil {
		t.Fatalf("resolveWindowsSystemDrive() error = %v", err)
	}
	if resolved != `C:\ProgramData` {
		t.Fatalf("resolveWindowsSystemDrive() = %q, want C:\\ProgramData", resolved)
	}
}

// TestResolveWindowsSystemDriveRejectsInvalidNativeDirectory prevents relative fallback state from becoming authority.
func TestResolveWindowsSystemDriveRejectsInvalidNativeDirectory(t *testing.T) {
	if _, err := resolveWindowsSystemDrive(`%SystemDrive%\ProgramData`, func() (string, error) {
		return `Windows`, nil
	}); err == nil {
		t.Fatal("resolveWindowsSystemDrive() accepted a relative Windows directory")
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
