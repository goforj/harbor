//go:build !darwin && !linux && !windows

package userpaths

import "path/filepath"

// platformDataDirectory gives unsupported desktop targets a deterministic user-scoped fallback.
func platformDataDirectory(_ environmentLookup, home homeDirectoryLookup) (string, error) {
	directory, err := resolveHomeDirectory(home)
	if err != nil {
		return "", err
	}

	return filepath.Join(directory, ".goforj", "harbor"), nil
}
