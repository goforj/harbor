//go:build linux

package machinepaths

import "testing"

// TestLinuxPlatformRootUsesInstallerLocation pins daemon and helper discovery to the package-owned directory.
func TestLinuxPlatformRootUsesInstallerLocation(t *testing.T) {
	root, err := platformRoot()
	if err != nil {
		t.Fatalf("platformRoot() error = %v", err)
	}
	if root != linuxPrivilegedRoot {
		t.Fatalf("platformRoot() = %q, want %q", root, linuxPrivilegedRoot)
	}
}
