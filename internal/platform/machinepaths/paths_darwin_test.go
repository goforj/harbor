//go:build darwin

package machinepaths

import "testing"

// TestDarwinPlatformRootUsesInstallerLocation pins daemon and helper discovery to system Application Support.
func TestDarwinPlatformRootUsesInstallerLocation(t *testing.T) {
	root, err := platformRoot()
	if err != nil {
		t.Fatalf("platformRoot() error = %v", err)
	}
	if root != darwinPrivilegedRoot {
		t.Fatalf("platformRoot() = %q, want %q", root, darwinPrivilegedRoot)
	}
}
