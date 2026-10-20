//go:build darwin || linux

package projectprocess

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestSessionManifestRejectsSpecialFileWithoutWaiting excludes a FIFO before reading or waiting for another process.
func TestSessionManifestRejectsSpecialFileWithoutWaiting(t *testing.T) {
	path := filepath.Join(canonicalTestPath(t, t.TempDir()), "not-a-manifest")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOutputBrokerLaunchConfig(path); err == nil {
		t.Fatal("manifest reader accepted a FIFO")
	}
}
