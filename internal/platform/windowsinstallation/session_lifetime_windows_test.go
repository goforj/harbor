//go:build windows

package windowsinstallation

import (
	"golang.org/x/sys/windows"
	"testing"
)

// TestInstalledRoleSessionLifetimeLimits leaves non-daemon process trees contained in their original launcher jobs.
func TestInstalledRoleSessionLifetimeLimits(t *testing.T) {
	for _, role := range []string{"daemon", "daemon-launcher", "desktop", "cli", "uninstall-guard", "unknown"} {
		flags := installedRoleJobLimits(role)
		if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 || flags&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0 {
			t.Fatalf("unsafe %s lifetime flags %#x", role, flags)
		}
		if (flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0) != (role == "daemon" || role == "daemon-launcher") {
			t.Fatalf("wrong %s breakaway policy %#x", role, flags)
		}
	}
}
