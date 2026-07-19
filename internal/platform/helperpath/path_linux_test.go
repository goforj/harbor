//go:build linux

package helperpath

import "testing"

// TestExecutableUsesFixedLinuxInstallerPath prevents PATH or environment configuration from redirecting elevation.
func TestExecutableUsesFixedLinuxInstallerPath(t *testing.T) {
	if got := Executable(); got != "/usr/libexec/harbor-helper" {
		t.Fatalf("Executable() = %q", got)
	}
}
