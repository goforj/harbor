//go:build windows

package projectprocess

import (
	"errors"
	"fmt"
	"os"
)

// replaceOutputSpool publishes a compacted spool while accommodating Windows' non-replacing rename semantics.
func replaceOutputSpool(temporaryPath, spoolPath string) error {
	if err := os.Rename(temporaryPath, spoolPath); err == nil {
		return nil
	} else {
		if removeErr := os.Remove(spoolPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace existing output spool: %w", errors.Join(err, removeErr))
		}
		if renameErr := os.Rename(temporaryPath, spoolPath); renameErr != nil {
			return fmt.Errorf("publish output spool after replacement: %w", errors.Join(err, renameErr))
		}
		return nil
	}
}
