//go:build windows

package windowsfile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestSameObjectUsesStableFilesystemIdentity verifies aliases match while distinct open files do not.
func TestSameObjectUsesStableFilesystemIdentity(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first")
	secondPath := filepath.Join(directory, "second")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(first) error = %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(second) error = %v", err)
	}
	first, err := os.Open(firstPath)
	if err != nil {
		t.Fatalf("os.Open(first) error = %v", err)
	}
	defer first.Close()
	alias, err := os.Open(firstPath)
	if err != nil {
		t.Fatalf("os.Open(alias) error = %v", err)
	}
	defer alias.Close()
	second, err := os.Open(secondPath)
	if err != nil {
		t.Fatalf("os.Open(second) error = %v", err)
	}
	defer second.Close()

	same, err := SameObject(windows.Handle(first.Fd()), windows.Handle(alias.Fd()))
	if err != nil || !same {
		t.Fatalf("SameObject(alias) = %t, %v, want true", same, err)
	}
	same, err = SameObject(windows.Handle(first.Fd()), windows.Handle(second.Fd()))
	if err != nil || same {
		t.Fatalf("SameObject(distinct) = %t, %v, want false", same, err)
	}
}
