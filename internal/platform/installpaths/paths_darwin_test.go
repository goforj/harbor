//go:build darwin

package installpaths

import "testing"

// TestDarwinProductPathsMatchThePackageContract pins launchers to the root-owned release layout.
func TestDarwinProductPathsMatchThePackageContract(t *testing.T) {
	root, err := MachineRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root != "/Library/Application Support/GoForj/Harbor" {
		t.Fatalf("MachineRoot() = %q", root)
	}
	launcher, err := DaemonLauncher()
	if err != nil {
		t.Fatal(err)
	}
	if launcher != "/Library/Application Support/GoForj/Harbor/daemon-launcher" {
		t.Fatalf("DaemonLauncher() = %q", launcher)
	}
	current, err := CurrentRelease()
	if err != nil {
		t.Fatal(err)
	}
	if current != "/Library/Application Support/GoForj/Harbor/current" {
		t.Fatalf("CurrentRelease() = %q", current)
	}
}
