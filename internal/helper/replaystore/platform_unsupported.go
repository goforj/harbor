//go:build !darwin && !linux && !windows

package replaystore

import (
	"fmt"
	"os"
	"runtime"
)

// createPlatformFile rejects targets without a reviewed protected-file creation contract.
func createPlatformFile(_ *os.Root, _ string, _ string) (*os.File, error) {
	return nil, fmt.Errorf("helper replay storage is unsupported on %s", runtime.GOOS)
}

// securePlatformFile rejects targets without a reviewed protected-file creation contract.
func securePlatformFile(_ *os.File) error {
	return fmt.Errorf("helper replay storage is unsupported on %s", runtime.GOOS)
}

// validatePlatformDirectory rejects targets without a reviewed protected-directory contract.
func validatePlatformDirectory(_ string, _ os.FileInfo) error {
	return fmt.Errorf("helper replay storage is unsupported on %s", runtime.GOOS)
}

// validatePlatformRoot rejects targets without a reviewed retained-directory contract.
func validatePlatformRoot(_ *os.Root) error {
	return fmt.Errorf("helper replay storage is unsupported on %s", runtime.GOOS)
}

// validatePlatformFile rejects targets without a reviewed protected-file contract.
func validatePlatformFile(_ *os.File, _ os.FileInfo) error {
	return fmt.Errorf("helper replay storage is unsupported on %s", runtime.GOOS)
}

// platformSyncDirectory rejects targets without a reviewed durability contract.
func platformSyncDirectory(_ *os.File) error {
	return fmt.Errorf("helper replay storage is unsupported on %s", runtime.GOOS)
}
