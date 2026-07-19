//go:build !windows

package replaystore

import (
	"os"
	"testing"
)

// preparePlatformTestDirectory preserves the host permissions already created by the shared fixture.
func preparePlatformTestDirectory(_ *testing.T, _ string) {}

// replayTestOwnerID keeps portable store fixtures owned by the non-privileged test process.
func replayTestOwnerID() uint32 {
	return uint32(os.Geteuid())
}
