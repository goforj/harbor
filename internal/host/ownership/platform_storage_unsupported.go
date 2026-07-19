//go:build !darwin && !linux && !windows

package ownership

import (
	"fmt"
	"os"
	"runtime"
)

// createPlatformFile rejects platforms without a reviewed secure creation primitive.
func createPlatformFile(_ *os.Root, _ string, _ string) (*os.File, error) {
	return nil, fmt.Errorf("machine ownership file creation is unsupported on %s", runtime.GOOS)
}

// platformRenameNoReplace rejects platforms without a reviewed durable no-replace rename primitive.
func platformRenameNoReplace(_ *os.Root, _ string, _ string, _ string) (bool, error) {
	return false, fmt.Errorf("machine ownership rename is unsupported on %s", runtime.GOOS)
}

// platformRenameReplace rejects platforms without a reviewed durable overwrite primitive.
func platformRenameReplace(_ *os.Root, _ string, _ string, _ string) (bool, error) {
	return false, fmt.Errorf("machine ownership replacement is unsupported on %s", runtime.GOOS)
}

// platformConfirmEntry rejects platforms without a reviewed entry durability primitive.
func platformConfirmEntry(_ *os.Root, _ string, _ string) error {
	return fmt.Errorf("machine ownership entry confirmation is unsupported on %s", runtime.GOOS)
}

// platformConfirmCleanup has no effect when protected mutation cannot succeed on the platform.
func platformConfirmCleanup(_ *os.Root) error {
	return nil
}
