//go:build darwin

package darwinacl

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestUnprivilegedOwnerInspectsAndRemovesACLThroughRetainedDescriptor proves the daemon-safe native path.
func TestUnprivilegedOwnerInspectsAndRemovesACLThroughRetainedDescriptor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("owner-level ACL access must run without root privileges")
	}
	parent := t.TempDir()
	path := filepath.Join(parent, "created")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("create Darwin ACL test directory: %v", err)
	}
	directory, err := os.Open(path)
	if err != nil {
		t.Fatalf("open Darwin ACL test directory: %v", err)
	}
	t.Cleanup(func() { _ = directory.Close() })

	present, err := Present(directory)
	if err != nil {
		t.Fatalf("inspect clean Darwin ACL test directory: %v", err)
	}
	if present {
		t.Fatal("clean Darwin ACL test directory has an ACL")
	}
	command := exec.Command("/bin/chmod", "+a", "everyone allow read", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install Darwin test ACL: %v: %s", err, output)
	}
	present, err = Present(directory)
	if err != nil {
		t.Fatalf("inspect installed Darwin test ACL as owner: %v", err)
	}
	if !present {
		t.Fatal("installed Darwin test ACL is absent")
	}

	movedPath := filepath.Join(parent, "moved")
	if err := os.Rename(path, movedPath); err != nil {
		t.Fatalf("move opened Darwin ACL test directory: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("replace opened Darwin ACL test directory path: %v", err)
	}
	replacement, err := os.Open(path)
	if err != nil {
		t.Fatalf("open replacement Darwin ACL test directory: %v", err)
	}
	t.Cleanup(func() { _ = replacement.Close() })

	if err := Remove(directory); err != nil {
		t.Fatalf("remove Darwin ACL as owner: %v", err)
	}
	present, err = Present(directory)
	if err != nil {
		t.Fatalf("inspect removed Darwin test ACL: %v", err)
	}
	if present {
		t.Fatal("removed Darwin test ACL remains present")
	}
	present, err = Present(replacement)
	if err != nil {
		t.Fatalf("inspect replacement Darwin ACL test directory: %v", err)
	}
	if present {
		t.Fatal("descriptor-scoped removal changed the replacement path")
	}
}

// TestPresentFailsClosedOnNativeInspectionErrors pins error propagation for callers enforcing private paths.
func TestPresentFailsClosedOnNativeInspectionErrors(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open Darwin ACL test directory: %v", err)
	}
	defer file.Close()
	original := inspectAccessControlList
	defer func() { inspectAccessControlList = original }()

	for _, test := range []struct {
		name      string
		probe     func(int) (bool, error)
		want      bool
		wantCause error
	}{
		{name: "absent", probe: func(int) (bool, error) { return false, nil }},
		{name: "present", probe: func(int) (bool, error) { return true, nil }, want: true},
		{name: "permission failure", probe: func(int) (bool, error) { return false, unix.EPERM }, wantCause: unix.EPERM},
		{name: "I/O failure", probe: func(int) (bool, error) { return false, unix.EIO }, wantCause: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspectAccessControlList = test.probe
			present, err := Present(file)
			if test.wantCause == nil && err != nil {
				t.Fatalf("Present() error = %v", err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("Present() error = %v, want cause %v", err, test.wantCause)
			}
			if present != test.want {
				t.Fatalf("Present() = %t, want %t", present, test.want)
			}
		})
	}
}

// TestFileSecurityHeaderClassification covers every native no-ACL representation and malformed metadata.
func TestFileSecurityHeaderClassification(t *testing.T) {
	if fileSecurityHeaderSize != 44 {
		t.Fatalf("fileSecurityHeader size = %d, want native kauth_filesec header size 44", fileSecurityHeaderSize)
	}
	noACL := fileSecurityHeader{entryCount: fileSecurityNoACL}
	var emptyACL fileSecurityHeader

	for _, test := range []struct {
		name        string
		header      fileSecurityHeader
		size        uintptr
		wantPresent bool
		wantErr     bool
	}{
		{name: "no security metadata", header: noACL, size: 0},
		{name: "explicit no ACL", header: noACL, size: fileSecurityHeaderSize},
		{name: "empty ACL", header: emptyACL, size: fileSecurityHeaderSize, wantPresent: true},
		{name: "ACL entries exceed header", header: emptyACL, size: fileSecurityHeaderSize + 1, wantPresent: true},
		{name: "truncated security metadata", header: emptyACL, size: fileSecurityHeaderSize - 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			present, err := fileSecurityHasACL(test.header, test.size)
			if (err != nil) != test.wantErr {
				t.Fatalf("fileSecurityHasACL() error = %v, wantErr %t", err, test.wantErr)
			}
			if present != test.wantPresent {
				t.Fatalf("fileSecurityHasACL() = %t, want %t", present, test.wantPresent)
			}
		})
	}
}
