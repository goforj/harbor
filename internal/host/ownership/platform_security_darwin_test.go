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

// TestDarwinExtendedAccessProbeRejectsEveryResultExceptENOATTR pins fail-closed native error handling.
func TestDarwinExtendedAccessProbeRejectsEveryResultExceptENOATTR(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("os.Open() error = %v", err)
	}
	defer file.Close()
	original := inspectDarwinExtendedAccess
	defer func() { inspectDarwinExtendedAccess = original }()

	for _, test := range []struct {
		name    string
		probe   func(int, string, []byte) (int, error)
		wantErr bool
	}{
		{name: "absent", probe: func(int, string, []byte) (int, error) { return 0, unix.ENOATTR }},
		{name: "present", probe: func(int, string, []byte) (int, error) { return 1, nil }, wantErr: true},
		{name: "zero-length present", probe: func(int, string, []byte) (int, error) { return 0, nil }, wantErr: true},
		{name: "probe failure", probe: func(int, string, []byte) (int, error) { return 0, unix.EIO }, wantErr: true},
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
			if errors.Is(err, unix.ENOATTR) {
				t.Fatalf("validatePlatformExtendedAccess() leaked ENOATTR = %v", err)
			}
		})
	}
}
