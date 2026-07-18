//go:build !darwin && !linux && !windows

package replaystore

import (
	"fmt"
	"os"
	"runtime"
)

// securePlatformFile rejects targets without a reviewed protected-file creation contract.
func securePlatformFile(_ *os.File) error {
	return fmt.Errorf("helper replay storage is unsupported on %s", runtime.GOOS)
}

// validatePlatformDirectory rejects targets without a reviewed protected-directory contract.
func validatePlatformDirectory(_ string, _ os.FileInfo) error {
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
