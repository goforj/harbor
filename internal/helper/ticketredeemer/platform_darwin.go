//go:build darwin

package ticketredeemer

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const darwinExtendedSecurityAttribute = "com.apple.system.Security"

// validatePlatformExtendedAccess rejects named macOS ACLs that mode bits cannot describe completely.
func validatePlatformExtendedAccess(file *os.File) error {
	_, err := unix.Fgetxattr(int(file.Fd()), darwinExtendedSecurityAttribute, nil)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect macOS extended ACL: %w", err)
	}
	return errors.New("ticket spool object has a macOS extended ACL")
}

// renamePlatformNoReplace atomically crosses from pending to claims without replacing a consumed reference.
func renamePlatformNoReplace(pending *os.File, claims *os.File, _ *os.File, source string, destination string) (bool, error) {
	err := unix.RenameatxNp(
		int(pending.Fd()),
		source,
		int(claims.Fd()),
		destination,
		unix.RENAME_EXCL,
	)
	if err != nil {
		return false, unixRenameError(err)
	}
	return true, nil
}
