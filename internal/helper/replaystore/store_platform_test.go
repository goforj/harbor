//go:build !windows

package replaystore

import "testing"

// preparePlatformTestDirectory preserves the host permissions already created by the shared fixture.
func preparePlatformTestDirectory(_ *testing.T, _ string) {}
