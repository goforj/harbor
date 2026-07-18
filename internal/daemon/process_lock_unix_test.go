//go:build darwin || linux

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProcessLockAppliesOwnerOnlyModes verifies Unix clients cannot enter the runtime directory or alter its lock.
func TestProcessLockAppliesOwnerOnlyModes(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("create permissive runtime directory: %v", err)
	}
	path := filepath.Join(directory, processLockFilename)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("create permissive lock file: %v", err)
	}

	lock, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("acquire process lock: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Errorf("release process lock: %v", err)
		}
	})

	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("inspect runtime directory: %v", err)
	}
	if mode := directoryInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("runtime directory mode = %o, want 700", mode)
	}

	lockInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("inspect lock file: %v", err)
	}
	if mode := lockInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("lock file mode = %o, want 600", mode)
	}
}

// TestProcessLockPreservesExistingParentMode verifies runtime preparation never chmods an override root it did not create.
func TestProcessLockPreservesExistingParentMode(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "runtime-root")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("create runtime parent: %v", err)
	}
	directory := filepath.Join(parent, "goforj", "harbor")
	lock, err := acquireProcessLock(filepath.Join(directory, processLockFilename))
	if err != nil {
		t.Fatalf("acquire process lock: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Errorf("release process lock: %v", err)
		}
	})

	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("inspect runtime parent: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o755 {
		t.Fatalf("runtime parent mode = %o, want preserved 755", mode)
	}
}

// TestProcessLockRejectsSymlinkedRuntimeDirectory verifies a redirected leaf cannot split daemon authority.
func TestProcessLockRejectsSymlinkedRuntimeDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	directory := filepath.Join(root, "runtime")
	if err := os.Symlink(target, directory); err != nil {
		t.Fatalf("create runtime symlink: %v", err)
	}

	lock, err := acquireProcessLock(filepath.Join(directory, processLockFilename))
	if err == nil {
		t.Fatal("acquisition unexpectedly accepted a symlinked runtime directory")
	}
	if lock != nil {
		t.Fatal("failed symlink acquisition returned a lock")
	}
}

// TestProcessLockRejectsSymlinkedFile verifies lock acquisition cannot follow a redirected file target.
func TestProcessLockRejectsSymlinkedFile(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("create lock target: %v", err)
	}
	path := filepath.Join(directory, processLockFilename)
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create lock symlink: %v", err)
	}

	lock, err := acquireProcessLock(path)
	if err == nil {
		t.Fatal("acquisition unexpectedly accepted a symlinked lock file")
	}
	if lock != nil {
		t.Fatal("failed symlink acquisition returned a lock")
	}
}
