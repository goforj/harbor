package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/goforj/harbor/internal/platform/runtimepath"
)

const processLockFilename = "harbord.lock"

// ErrAlreadyRunning identifies a live Harbor daemon that already owns the per-user process lock.
var ErrAlreadyRunning = errors.New("Harbor daemon is already running")

// ProcessLock holds the operating-system lock that grants one process daemon authority for the current user.
type ProcessLock struct {
	mutex sync.Mutex
	path  string
	file  *os.File
}

// AcquireProcessLock acquires Harbor's singleton daemon lock in the current user's runtime directory.
func AcquireProcessLock() (*ProcessLock, error) {
	directory, err := runtimepath.Directory()
	if err != nil {
		return nil, fmt.Errorf("resolve daemon runtime directory: %w", err)
	}

	return acquireProcessLock(filepath.Join(directory, processLockFilename))
}

// acquireProcessLock acquires a specific lock path so platform behavior can be proven without changing user state.
func acquireProcessLock(path string) (*ProcessLock, error) {
	if path == "" {
		return nil, errors.New("acquire daemon process lock: path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("acquire daemon process lock: path %q is not absolute", path)
	}

	path = filepath.Clean(path)
	if err := prepareRuntimeDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("prepare daemon runtime directory: %w", err)
	}

	file, err := openProcessLockFile(path)
	if err != nil {
		return nil, fmt.Errorf("open daemon process lock %q: %w", path, err)
	}

	if err := acquirePlatformLock(file); err != nil {
		closeErr := file.Close()
		if isPlatformLockContended(err) {
			alreadyRunning := fmt.Errorf("%w: %s", ErrAlreadyRunning, path)
			if closeErr != nil {
				return nil, errors.Join(alreadyRunning, fmt.Errorf("close contended daemon process lock: %w", closeErr))
			}
			return nil, alreadyRunning
		}

		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close failed daemon process lock: %w", closeErr))
		}
		return nil, fmt.Errorf("acquire daemon process lock %q: %w", path, err)
	}

	return &ProcessLock{path: path, file: file}, nil
}

// Path returns the filesystem path whose operating-system lock this process owns.
func (lock *ProcessLock) Path() string {
	return lock.path
}

// Release relinquishes daemon authority and safely tolerates repeated shutdown paths.
func (lock *ProcessLock) Release() error {
	lock.mutex.Lock()
	defer lock.mutex.Unlock()

	if lock.file == nil {
		return nil
	}

	file := lock.file
	lock.file = nil
	unlockErr := releasePlatformLock(file)
	closeErr := file.Close()

	if unlockErr != nil {
		unlockErr = fmt.Errorf("release daemon process lock %q: %w", lock.path, unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close daemon process lock %q: %w", lock.path, closeErr)
	}

	return errors.Join(unlockErr, closeErr)
}
