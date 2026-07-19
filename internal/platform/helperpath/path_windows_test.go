//go:build windows

package helperpath

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsExecutableUsesProgramFilesContract verifies no environment-selected path can redirect elevation.
func TestWindowsExecutableUsesProgramFilesContract(t *testing.T) {
	programFiles := `C:\Program Files`
	path := windowsExecutableFromKnownFolder(func(folderID *windows.KNOWNFOLDERID, flags uint32) (string, error) {
		if folderID != windows.FOLDERID_ProgramFiles || flags != windows.KF_FLAG_DEFAULT {
			t.Fatalf("KnownFolderPath() = (%v, %#x)", folderID, flags)
		}
		return programFiles, nil
	})
	want := filepath.Join(programFiles, "GoForj", "Harbor", "harbor-helper.exe")
	if path != want {
		t.Fatalf("Windows helper path = %q, want %q", path, want)
	}
}

// TestWindowsExecutableFailsClosedOnInvalidKnownFolder verifies native lookup anomalies select no executable.
func TestWindowsExecutableFailsClosedOnInvalidKnownFolder(t *testing.T) {
	tests := []struct {
		path string
		err  error
	}{
		{err: errors.New("known folder unavailable")},
		{path: ""},
		{path: `Program Files`},
	}
	for _, test := range tests {
		path := windowsExecutableFromKnownFolder(func(*windows.KNOWNFOLDERID, uint32) (string, error) {
			return test.path, test.err
		})
		if path != "" {
			t.Fatalf("windowsExecutableFromKnownFolder(%q, %v) = %q", test.path, test.err, path)
		}
	}
}
