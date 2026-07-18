//go:build linux

package ticketspool

import (
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// validatePlatformExtendedACL relies on exact 0700/0600 modes because their zero group class also zeros the POSIX ACL mask.
func validatePlatformExtendedACL(_ *os.File) error {
	return nil
}

// renamePlatformNoReplace commits one direct child without replacing an existing immutable reference.
func renamePlatformNoReplace(_ *os.Root, directory *os.File, _ *os.File, source string, destination string) (bool, error) {
	err := unix.Renameat2(
		int(directory.Fd()),
		source,
		int(directory.Fd()),
		destination,
		unix.RENAME_NOREPLACE,
	)
	if err == unix.EEXIST {
		return false, fs.ErrExist
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
