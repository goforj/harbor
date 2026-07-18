//go:build darwin

package ticketredeemer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDarwinRetainedHandleRejectsNamedACL proves exact mode bits cannot conceal a named macOS grant.
func TestDarwinRetainedHandleRejectsNamedACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claimed-ticket")
	if err := os.WriteFile(path, []byte("ticket"), unixPrivateFile); err != nil {
		t.Fatalf("write ACL fixture: %v", err)
	}
	command := exec.Command("chmod", "+a", "everyone allow read", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply macOS ACL fixture: %v: %s", err, output)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open ACL fixture: %v", err)
	}
	if err := validatePlatformExtendedAccess(file); err == nil {
		t.Fatal("validatePlatformExtendedAccess() accepted a named ACL")
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close ACL fixture: %v", err)
	}
}
