//go:build !darwin && !linux && !windows

package ownership

import (
	"context"
	"fmt"
	"os"
	"runtime"
)

// acquirePlatformLock rejects platforms without a reviewed machine-global locking primitive.
func acquirePlatformLock(_ context.Context, _ *os.File) error {
	return fmt.Errorf("machine ownership locking is unsupported on %s", runtime.GOOS)
}

// releasePlatformLock has no effect when acquisition cannot succeed on an unsupported platform.
func releasePlatformLock(_ *os.File) error {
	return nil
}
