//go:build darwin

package runtimepath

import (
	"os"
	"path/filepath"
	"strconv"
)

const darwinTemporaryRoot = "/tmp"

// platformDirectory uses macOS's per-user temporary root while reserving enough path space for a Unix socket.
func platformDirectory(_ environmentLookup, _ dataDirectoryLookup, temporaryDirectory temporaryDirectoryLookup) (string, error) {
	leaf := "goforj-harbor-" + strconv.Itoa(os.Geteuid())
	root := filepath.Clean(temporaryDirectory())
	directory := filepath.Join(root, leaf)
	if root == "." || !filepath.IsAbs(root) || !unixSocketPathFits(directory) {
		directory = filepath.Join(darwinTemporaryRoot, leaf)
	}

	return directory, nil
}
