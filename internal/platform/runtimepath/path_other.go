//go:build !darwin && !linux && !windows

package runtimepath

import (
	"fmt"
	"path/filepath"
)

// platformDirectory gives unsupported targets the same user-owned fallback used when Linux has no session runtime root.
func platformDirectory(_ environmentLookup, dataDirectory dataDirectoryLookup, _ temporaryDirectoryLookup) (string, error) {
	root, err := dataDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve Harbor data root for runtime directory: %w", err)
	}

	return filepath.Join(root, "runtime"), nil
}
