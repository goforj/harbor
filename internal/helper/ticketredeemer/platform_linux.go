//go:build linux

package ticketredeemer

import (
	"os"

	"golang.org/x/sys/unix"
)

// validatePlatformExtendedAccess relies on exact mode class bits to bound every effective POSIX ACL grant.
func validatePlatformExtendedAccess(_ *os.File) error {
	return nil
}

// renamePlatformNoReplace atomically crosses from pending to claims without replacing a consumed reference.
func renamePlatformNoReplace(pending *os.File, claims *os.File, _ *os.File, source string, destination string) (bool, error) {
	err := unix.Renameat2(
		int(pending.Fd()),
		source,
		int(claims.Fd()),
		destination,
		unix.RENAME_NOREPLACE,
	)
	if err != nil {
		return false, unixRenameError(err)
	}
	return true, nil
}
