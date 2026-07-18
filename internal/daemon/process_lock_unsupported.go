//go:build !darwin && !linux && !windows

package daemon

import (
	"errors"
	"fmt"
	"os"
)

// prepareRuntimeDirectory creates the owner-scoped fallback used on targets without a supported lock primitive.
func prepareRuntimeDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	return nil
}

// openProcessLockFile opens the lock target so unsupported targets fail at the capability boundary.
func openProcessLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

// acquirePlatformLock reports that singleton daemon authority is unavailable on this target.
func acquirePlatformLock(_ *os.File) error {
	return errors.New("daemon process locking is unsupported on this platform")
}

// isPlatformLockContended reports no false live-daemon result for an unsupported capability.
func isPlatformLockContended(_ error) bool {
	return false
}

// releasePlatformLock has no lock to release after unsupported acquisition fails.
func releasePlatformLock(_ *os.File) error {
	return nil
}
