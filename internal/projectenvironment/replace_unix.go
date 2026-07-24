//go:build !windows

package projectenvironment

import "os"

// replaceFile atomically publishes a staged repository configuration on Unix hosts.
func replaceFile(stagedPath string, targetPath string) error {
	return os.Rename(stagedPath, targetPath)
}
