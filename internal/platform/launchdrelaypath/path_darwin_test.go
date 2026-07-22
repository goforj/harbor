//go:build darwin

package launchdrelaypath

import "testing"

// TestExecutableUsesFixedDarwinInstallerPath prevents callers from selecting the future relay destination.
func TestExecutableUsesFixedDarwinInstallerPath(t *testing.T) {
	if got := Executable(); got != "/Library/PrivilegedHelperTools/com.goforj.harbor.launchdrelay" {
		t.Fatalf("Executable() = %q", got)
	}
}
