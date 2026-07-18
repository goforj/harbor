//go:build linux

package userpaths

import "path/filepath"

const xdgDataHome = "XDG_DATA_HOME"

// platformDataDirectory follows the XDG data directory convention used by Linux desktop applications.
func platformDataDirectory(environment environmentLookup, home homeDirectoryLookup) (string, error) {
	if directory, ok := environment(xdgDataHome); ok && directory != "" && filepath.IsAbs(directory) {
		return filepath.Join(filepath.Clean(directory), "goforj", "harbor"), nil
	}

	directory, err := resolveHomeDirectory(home)
	if err != nil {
		return "", err
	}

	return filepath.Join(directory, ".local", "share", "goforj", "harbor"), nil
}
