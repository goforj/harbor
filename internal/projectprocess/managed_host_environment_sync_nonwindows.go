//go:build !windows

package projectprocess

import (
	"errors"
	"os"
)

// syncManagedHostEnvironmentDirectory persists a completed rename or deletion in its parent directory.
func syncManagedHostEnvironmentDirectory(path string) (syncErr error) {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		syncErr = errors.Join(syncErr, directory.Close())
	}()
	return directory.Sync()
}
