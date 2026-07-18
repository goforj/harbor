//go:build !darwin && !linux && !windows

package ownership

import (
	"fmt"
	"os"
	"runtime"
)

// validatePlatformDirectory rejects platforms without a reviewed elevated filesystem boundary.
func validatePlatformDirectory(_ string, _ os.FileInfo) error {
	return fmt.Errorf("machine ownership directory security is unsupported on %s", runtime.GOOS)
}

// securePlatformFile rejects platforms without a reviewed elevated filesystem boundary.
func securePlatformFile(_ *os.File, _ bool) error {
	return fmt.Errorf("machine ownership file security is unsupported on %s", runtime.GOOS)
}

// validatePlatformFile rejects platforms without a reviewed elevated filesystem boundary.
func validatePlatformFile(_ *os.File, _ os.FileInfo, _ bool) error {
	return fmt.Errorf("machine ownership file security is unsupported on %s", runtime.GOOS)
}
