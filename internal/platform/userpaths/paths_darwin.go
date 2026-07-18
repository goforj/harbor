//go:build darwin

package userpaths

import "path/filepath"

// platformDataDirectory follows the Application Support convention used by macOS applications.
func platformDataDirectory(_ environmentLookup, home homeDirectoryLookup) (string, error) {
	directory, err := resolveHomeDirectory(home)
	if err != nil {
		return "", err
	}

	return filepath.Join(directory, "Library", "Application Support", "GoForj", "Harbor"), nil
}
