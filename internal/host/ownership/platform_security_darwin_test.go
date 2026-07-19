//go:build darwin

package ownership

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestDarwinRetainedHandleRejectsNativeACL proves mode bits cannot hide access attached to the opened inode.
func TestDarwinRetainedHandleRejectsNativeACL(t *testing.T) {
	for _, test := range []struct {
		name      string
		directory bool
	}{
		{name: "file"},
		{name: "directory", directory: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "protected")
			mode := os.FileMode(0o600)
			if test.directory {
				mode = 0o700
				if err := os.Mkdir(path, mode); err != nil {
					t.Fatalf("os.Mkdir() error = %v", err)
				}
			} else if err := os.WriteFile(path, nil, mode); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatalf("os.Open() error = %v", err)
			}
			t.Cleanup(func() { _ = file.Close() })
			info, err := file.Stat()
			if err != nil {
				t.Fatalf("File.Stat() error = %v", err)
			}
			if err := validatePlatformFile(file, info, test.directory); err != nil {
				t.Fatalf("validatePlatformFile() without ACL error = %v", err)
			}

			command := exec.Command("/bin/chmod", "+a", "everyone allow read", path)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("chmod +a error = %v: %s", err, output)
			}
			if err := validatePlatformFile(file, info, test.directory); err == nil || !strings.Contains(err.Error(), "access control list") {
				t.Fatalf("validatePlatformFile() ACL error = %v, want ACL rejection", err)
			}
		})
	}
}

// TestDarwinExtendedAccessProbeRejectsACLsAndFailures pins fail-closed native error handling.
func TestDarwinExtendedAccessProbeRejectsACLsAndFailures(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("os.Open() error = %v", err)
	}
	defer file.Close()
	original := inspectDarwinExtendedAccess
	defer func() { inspectDarwinExtendedAccess = original }()

	for _, test := range []struct {
		name      string
		probe     func(int) (bool, error)
		wantErr   bool
		wantCause error
	}{
		{name: "absent", probe: func(int) (bool, error) { return false, nil }},
		{name: "present", probe: func(int) (bool, error) { return true, nil }, wantErr: true},
		{name: "permission failure", probe: func(int) (bool, error) { return false, unix.EPERM }, wantErr: true, wantCause: unix.EPERM},
		{name: "I/O failure", probe: func(int) (bool, error) { return false, unix.EIO }, wantErr: true, wantCause: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspectDarwinExtendedAccess = test.probe
			err := validatePlatformExtendedAccess(file)
			if test.wantErr && err == nil {
				t.Fatal("validatePlatformExtendedAccess() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validatePlatformExtendedAccess() error = %v", err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("validatePlatformExtendedAccess() error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
}

// TestDarwinFileSecurityHeaderClassification covers every native no-ACL representation and malformed metadata.
func TestDarwinFileSecurityHeaderClassification(t *testing.T) {
	if darwinFileSecurityHeaderSize != 44 {
		t.Fatalf("darwinFileSecurityHeader size = %d, want native kauth_filesec header size 44", darwinFileSecurityHeaderSize)
	}
	noACL := darwinFileSecurityHeader{entryCount: darwinFileSecurityNoACL}
	var emptyACL darwinFileSecurityHeader

	for _, test := range []struct {
		name        string
		header      darwinFileSecurityHeader
		size        uintptr
		wantPresent bool
		wantErr     bool
	}{
		{name: "no security metadata", header: noACL, size: 0},
		{name: "explicit no ACL", header: noACL, size: darwinFileSecurityHeaderSize},
		{name: "empty ACL", header: emptyACL, size: darwinFileSecurityHeaderSize, wantPresent: true},
		{name: "ACL entries exceed header", header: emptyACL, size: darwinFileSecurityHeaderSize + 1, wantPresent: true},
		{name: "truncated security metadata", header: emptyACL, size: darwinFileSecurityHeaderSize - 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			present, err := darwinFileSecurityHasACL(test.header, test.size)
			if (err != nil) != test.wantErr {
				t.Fatalf("darwinFileSecurityHasACL() error = %v, wantErr %t", err, test.wantErr)
			}
			if present != test.wantPresent {
				t.Fatalf("darwinFileSecurityHasACL() present = %t, want %t", present, test.wantPresent)
			}
		})
	}
}
