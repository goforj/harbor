//go:build darwin

package ticketspool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

const darwinExtendedSecurityAttribute = "com.apple.system.Security"

// validatePlatformExtendedACL rejects macOS ACL entries that can grant access beyond the private mode bits.
func validatePlatformExtendedACL(file *os.File) error {
	_, err := unix.Fgetxattr(int(file.Fd()), darwinExtendedSecurityAttribute, nil)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect macOS extended ACL: %w", err)
	}
	return fmt.Errorf("path has a macOS extended ACL")
}

// renamePlatformNoReplace commits one direct child without replacing an existing immutable reference.
func renamePlatformNoReplace(_ *os.Root, directory *os.File, _ *os.File, source string, destination string) (bool, error) {
	err := unix.RenameatxNp(
		int(directory.Fd()),
		source,
		int(directory.Fd()),
		destination,
		unix.RENAME_EXCL,
	)
	if err == unix.EEXIST {
		return false, fs.ErrExist
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
