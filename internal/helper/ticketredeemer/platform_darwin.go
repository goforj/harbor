//go:build darwin

package ticketredeemer

import (
	"errors"
	"fmt"
	"os"

	"github.com/goforj/harbor/internal/platform/darwinacl"
	"golang.org/x/sys/unix"
)

// validatePlatformExtendedAccess rejects named macOS ACLs that mode bits cannot describe completely.
func validatePlatformExtendedAccess(file *os.File) error {
	present, err := darwinacl.Present(file)
	if err != nil {
		return fmt.Errorf("inspect macOS extended ACL: %w", err)
	}
	if present {
		return errors.New("ticket spool object has a macOS extended ACL")
	}
	return nil
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
