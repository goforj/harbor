//go:build darwin

package devbootstrap

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const darwinExtendedSecurityAttribute = "com.apple.system.Security"

// validatePlatformExtendedAccess rejects named macOS ACLs that exact mode bits cannot describe.
func validatePlatformExtendedAccess(file *os.File) error {
	_, err := unix.Fgetxattr(int(file.Fd()), darwinExtendedSecurityAttribute, nil)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect macOS extended ACL: %w", err)
	}
	return errors.New("object has a macOS extended ACL")
}

// securePlatformCreatedAccess removes only an ACL inherited by a transaction-created object.
func securePlatformCreatedAccess(file *os.File) error {
	err := unix.Fremovexattr(int(file.Fd()), darwinExtendedSecurityAttribute)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	return err
}

// publishPlatformNoReplace atomically installs a first helper without overwriting an unexpected destination.
func publishPlatformNoReplace(parent *os.File, source string, destination string) error {
	return classifyNoReplaceError(unix.RenameatxNp(
		int(parent.Fd()),
		source,
		int(parent.Fd()),
		destination,
		unix.RENAME_EXCL,
	))
}
