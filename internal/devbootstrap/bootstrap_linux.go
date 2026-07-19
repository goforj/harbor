//go:build linux

package devbootstrap

import (
	"os"

	"golang.org/x/sys/unix"
)

// validatePlatformExtendedAccess relies on exact POSIX mode masks to bound effective Linux ACL grants.
func validatePlatformExtendedAccess(_ *os.File) error {
	return nil
}

// securePlatformCreatedAccess relies on the creation mode and exact final Linux mode mask.
func securePlatformCreatedAccess(_ *os.File) error {
	return nil
}

// publishPlatformNoReplace atomically installs a first helper without overwriting an unexpected destination.
func publishPlatformNoReplace(parent *os.File, source string, destination string) error {
	return classifyNoReplaceError(unix.Renameat2(
		int(parent.Fd()),
		source,
		int(parent.Fd()),
		destination,
		unix.RENAME_NOREPLACE,
	))
}
