//go:build linux

package projectterminal

import (
	"io"
	"os"
	"testing"
)

// TestSessionCloseTerminatesAChildThatEscapesTheShellSession covers common daemonizing tools.
func TestSessionCloseTerminatesAChildThatEscapesTheShellSession(t *testing.T) {
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}

	session, err := startPlatform(t.TempDir(), "/bin/sh")
	if err != nil {
		t.Fatalf("startPlatform() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	if _, err := io.WriteString(session, "setsid sleep 30 & printf 'child=%s\\n' \"$!\"\n"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	childPID := readChildPID(t, session)
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitForProcessExit(t, childPID)
}
