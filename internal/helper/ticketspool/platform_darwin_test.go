//go:build darwin

package ticketspool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDarwinExtendedACLIsRejected proves mode 0700 cannot conceal a named macOS access grant.
func TestDarwinExtendedACLIsRejected(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending")
	prepareTestDirectory(t, directory)
	command := exec.Command("/bin/chmod", "+a", "everyone allow read", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply macOS ACL fixture: %v: %s", err, output)
	}
	file, err := os.Open(directory)
	if err != nil {
		t.Fatalf("open ACL fixture: %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatalf("stat ACL fixture: %v", err)
	}
	validationErr := validatePlatformObject(file, info, true)
	closeErr := file.Close()
	if validationErr == nil {
		t.Fatal("validatePlatformObject() accepted a macOS extended ACL")
	}
	if closeErr != nil {
		t.Fatalf("close ACL fixture: %v", closeErr)
	}
}
