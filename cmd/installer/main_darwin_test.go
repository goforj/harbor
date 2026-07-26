//go:build darwin

package main

import (
	"bytes"
	"testing"
)

// TestRunRejectsIncompleteInstallIdentity keeps package-script omissions outside privileged bootstrap.
func TestRunRejectsIncompleteInstallIdentity(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"uninstall"},
		{"install"},
		{"install", "--user-id", "501"},
		{"install", "--user-id", "0", "--group-id", "20"},
		{"install", "--user-id", "0501", "--group-id", "20"},
	} {
		if err := run(arguments, &bytes.Buffer{}); err == nil {
			t.Fatalf("run(%q) error = nil", arguments)
		}
	}
}
