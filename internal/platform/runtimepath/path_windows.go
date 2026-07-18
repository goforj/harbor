//go:build windows

package runtimepath

import (
	"fmt"
	"path/filepath"
)

// platformDirectory keeps transient daemon artifacts beneath Harbor's per-user Local AppData root on Windows.
func platformDirectory(_ environmentLookup, dataDirectory dataDirectoryLookup, _ temporaryDirectoryLookup) (string, error) {
	root, err := dataDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve Harbor data root for runtime directory: %w", err)
	}

	return filepath.Join(root, "runtime"), nil
}
