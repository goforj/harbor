//go:build darwin

package runtimepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// outputBrokerDirectory keeps broker endpoints short without relocating daemon-owned runtime authority.
func outputBrokerDirectory(temporaryDirectory temporaryDirectoryLookup) (string, error) {
	return darwinOutputBrokerDirectory(temporaryDirectory(), uint32(os.Geteuid()))
}

// darwinOutputBrokerDirectory selects the established fallback only for a new broker endpoint that cannot fit the preferred directory.
func darwinOutputBrokerDirectory(temporaryRoot string, userID uint32) (string, error) {
	leaf := "goforj-harbor-" + strconv.FormatUint(uint64(userID), 10)
	root := filepath.Clean(temporaryRoot)
	directory := filepath.Join(root, leaf)
	if root != "." && filepath.IsAbs(root) && unixSocketPathFits(directory) && outputBrokerUnixSocketPathFits(directory) {
		return directory, nil
	}
	directory = filepath.Join(darwinTemporaryRoot, leaf)
	if !outputBrokerUnixSocketPathFits(directory) {
		return "", fmt.Errorf("macOS output broker runtime directory %q exceeds the Unix socket path limit", directory)
	}
	return directory, nil
}
