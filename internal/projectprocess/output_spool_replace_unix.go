//go:build !windows

package projectprocess

import "os"

// replaceOutputSpool atomically publishes a compacted spool on Unix filesystems.
func replaceOutputSpool(temporaryPath, spoolPath string) error {
	return os.Rename(temporaryPath, spoolPath)
}
