//go:build darwin

package ownership

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
