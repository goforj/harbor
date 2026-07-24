//go:build windows

package projectenvironment

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// replaceFile publishes a staged repository configuration across Windows rename semantics.
func replaceFile(stagedPath string, targetPath string) error {
	if err := os.Rename(stagedPath, targetPath); err == nil {
		return nil
	}
	backup, err := os.CreateTemp(filepath.Dir(targetPath), ".harbor-config-backup-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(targetPath, backupPath); err != nil {
		return err
	}
	if err := os.Rename(stagedPath, targetPath); err != nil {
		if restoreErr := os.Rename(backupPath, targetPath); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
