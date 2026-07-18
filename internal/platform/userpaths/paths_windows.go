//go:build windows

package userpaths

import "path/filepath"

const localAppData = "LOCALAPPDATA"

// platformDataDirectory follows the local application data convention used by Windows applications.
func platformDataDirectory(environment environmentLookup, home homeDirectoryLookup) (string, error) {
	if directory, ok := environment(localAppData); ok && directory != "" && filepath.IsAbs(directory) {
		return filepath.Join(filepath.Clean(directory), "GoForj", "Harbor"), nil
	}

	directory, err := resolveHomeDirectory(home)
	if err != nil {
		return "", err
	}

	return filepath.Join(directory, "AppData", "Local", "GoForj", "Harbor"), nil
}
