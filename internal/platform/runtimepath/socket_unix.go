//go:build darwin || linux

package runtimepath

import (
	"fmt"
	"path/filepath"
)

const (
	socketFilename                 = "harbord.sock"
	maximumUnixSocketFilenameBytes = len("output-") + 32 + len(".sock")
	maxPortableUnixSocketPathBytes = 103
)

// SocketPath returns Harbor's local Unix socket path with the portable sockaddr length enforced.
func SocketPath() (string, error) {
	directory, err := Directory()
	if err != nil {
		return "", err
	}

	path := filepath.Join(directory, socketFilename)
	if !unixSocketPathFits(directory) {
		return "", fmt.Errorf("daemon socket path %q exceeds %d bytes", path, maxPortableUnixSocketPathBytes)
	}

	return path, nil
}

// unixSocketPathFits reserves the terminating byte required by macOS's smallest supported sockaddr layout.
func unixSocketPathFits(directory string) bool {
	return len([]byte(filepath.Join(directory, socketFilename))) <= maxPortableUnixSocketPathBytes
}

// outputBrokerUnixSocketPathFits reserves space for the broker's per-session endpoint name.
func outputBrokerUnixSocketPathFits(directory string) bool {
	return len([]byte(directory))+1+maximumUnixSocketFilenameBytes <= maxPortableUnixSocketPathBytes
}
