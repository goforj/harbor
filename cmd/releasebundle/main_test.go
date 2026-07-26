package main

import (
	"bytes"
	"testing"
)

// TestRunRejectsIncompleteReleaseIdentity keeps workflow omissions visible.
func TestRunRejectsIncompleteReleaseIdentity(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"--version", "0.1.0-dev.1"}, &output); err == nil {
		t.Fatal("run() error = nil")
	}
}
