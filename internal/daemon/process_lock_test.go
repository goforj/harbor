package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const processLockHelperPath = "HARBOR_PROCESS_LOCK_HELPER_PATH"

// TestProcessLockRejectsConcurrentOwner verifies a second acquisition is classified as a live daemon.
func TestProcessLockRejectsConcurrentOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), processLockFilename)
	first, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("acquire first process lock: %v", err)
	}
	t.Cleanup(func() {
		if err := first.Release(); err != nil {
			t.Errorf("release first process lock: %v", err)
		}
	})

	second, err := acquireProcessLock(path)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquisition error = %v, want %v", err, ErrAlreadyRunning)
	}
	if second != nil {
		t.Fatal("second acquisition returned a lock without daemon authority")
	}
}

// TestProcessLockRejectsAnotherProcess verifies the kernel lock, rather than process-local state, enforces authority.
func TestProcessLockRejectsAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), processLockFilename)
	lock, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("acquire parent process lock: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Errorf("release parent process lock: %v", err)
		}
	})

	command := exec.Command(os.Args[0], "-test.run=^TestProcessLockHelper$", "-test.count=1")
	command.Env = append(os.Environ(), processLockHelperPath+"="+path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run process-lock helper: %v\n%s", err, output)
	}
}

// TestProcessLockHelper attempts acquisition in a child test process used by TestProcessLockRejectsAnotherProcess.
func TestProcessLockHelper(t *testing.T) {
	path, ok := os.LookupEnv(processLockHelperPath)
	if !ok {
		t.Skip("process-lock helper is only run by its parent test")
	}

	lock, err := acquireProcessLock(path)
	if lock != nil {
		_ = lock.Release()
		t.Fatal("child process acquired the parent's daemon lock")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("child acquisition error = %v, want %v", err, ErrAlreadyRunning)
	}
}

// TestProcessLockCanBeReacquiredAfterRelease verifies orderly shutdown transfers authority to a later daemon.
func TestProcessLockCanBeReacquiredAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), processLockFilename)
	first, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("acquire first process lock: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first process lock: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first process lock again: %v", err)
	}

	second, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("reacquire process lock: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release second process lock: %v", err)
	}
}

// TestProcessLockCreatesRuntimeDirectory verifies first startup creates the owner-scoped daemon leaf.
func TestProcessLockCreatesRuntimeDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested", "runtime")
	path := filepath.Join(directory, processLockFilename)
	lock, err := acquireProcessLock(path)
	if err != nil {
		t.Fatalf("acquire process lock: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Release(); err != nil {
			t.Errorf("release process lock: %v", err)
		}
	})

	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("inspect runtime directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("runtime path mode = %v, want directory", info.Mode())
	}
	if lock.Path() != path {
		t.Fatalf("lock path = %q, want %q", lock.Path(), path)
	}
}

// TestProcessLockRejectsAmbiguousPaths verifies singleton authority never depends on the daemon working directory.
func TestProcessLockRejectsAmbiguousPaths(t *testing.T) {
	for _, path := range []string{"", processLockFilename, filepath.Join("relative", processLockFilename)} {
		lock, err := acquireProcessLock(path)
		if err == nil || !strings.Contains(err.Error(), "path") {
			t.Fatalf("acquireProcessLock(%q) error = %v, want path error", path, err)
		}
		if lock != nil {
			t.Fatalf("acquireProcessLock(%q) returned a lock", path)
		}
	}
}

// TestProcessLockPreservesInfrastructureFailure verifies filesystem errors are not mislabeled as another daemon.
func TestProcessLockPreservesInfrastructureFailure(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("create parent file: %v", err)
	}

	lock, err := acquireProcessLock(filepath.Join(parent, processLockFilename))
	if err == nil {
		t.Fatal("acquisition unexpectedly accepted a file as its runtime directory")
	}
	if errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("infrastructure failure = %v, must not identify a live daemon", err)
	}
	if lock != nil {
		t.Fatal("failed acquisition returned a lock")
	}
}
