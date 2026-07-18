//go:build linux

package runtimepath

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	xdgRuntimeDirectory = "XDG_RUNTIME_DIR"
	unixTemporaryRoot   = "/tmp"
)

// platformDirectory uses the login session's XDG runtime root and a short UID-scoped fallback for stripped environments.
func platformDirectory(environment environmentLookup, _ dataDirectoryLookup, temporaryDirectory temporaryDirectoryLookup) (string, error) {
	if root, ok := environment(xdgRuntimeDirectory); ok && root != "" && filepath.IsAbs(root) {
		directory := filepath.Join(filepath.Clean(root), "goforj", "harbor")
		if unixSocketPathFits(directory) {
			return directory, nil
		}
	}

	root := filepath.Clean(temporaryDirectory())
	directory := filepath.Join(root, fmt.Sprintf("goforj-harbor-%d", os.Geteuid()))
	if root == "." || !filepath.IsAbs(root) || !unixSocketPathFits(directory) {
		directory = filepath.Join(unixTemporaryRoot, fmt.Sprintf("goforj-harbor-%d", os.Geteuid()))
	}

	return directory, nil
}
