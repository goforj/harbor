//go:build windows

package runtimepath

import (
	"strings"
	"testing"
)

// TestPipePathIncludesCanonicalUserSID verifies endpoint discovery cannot overlap across Windows users.
func TestPipePathIncludesCanonicalUserSID(t *testing.T) {
	const userID = "S-1-5-21-100-200-300-400"
	path, err := pipePathForUserID(userID)
	if err != nil {
		t.Fatalf("build named pipe path: %v", err)
	}
	if path != windowsPipePrefix+userID {
		t.Fatalf("pipe path = %q, want prefix plus SID", path)
	}
}

// TestPipePathRejectsInvalidUserID verifies untrusted text cannot alter named-pipe endpoint selection.
func TestPipePathRejectsInvalidUserID(t *testing.T) {
	for _, userID := range []string{"", `S-1-5-21\\other`, `S-1-5-21/other`, "not-a-sid"} {
		path, err := pipePathForUserID(userID)
		if err == nil {
			t.Fatalf("pipePathForUserID(%q) unexpectedly returned %q", userID, path)
		}
		if path != "" {
			t.Fatalf("pipePathForUserID(%q) path = %q after failure", userID, path)
		}
	}
}

// TestPipePathUsesCurrentUser verifies the public endpoint belongs to a canonical Windows SID.
func TestPipePathUsesCurrentUser(t *testing.T) {
	path, err := PipePath()
	if err != nil {
		t.Fatalf("resolve current-user pipe path: %v", err)
	}
	if !strings.HasPrefix(path, windowsPipePrefix+"S-") {
		t.Fatalf("pipe path = %q, want current SID suffix", path)
	}
}
