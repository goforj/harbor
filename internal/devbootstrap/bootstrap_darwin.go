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
	exists, err := darwinExtendedAccessExists(file)
	if err != nil {
		return fmt.Errorf("inspect macOS extended ACL: %w", err)
	}
	if !exists {
		return nil
	}
	return errors.New("object has a macOS extended ACL")
}

// securePlatformCreatedAccess avoids macOS's protected-attribute removal error when a new object inherited no ACL.
func securePlatformCreatedAccess(file *os.File) error {
	exists, err := darwinExtendedAccessExists(file)
	if err != nil {
		return fmt.Errorf("inspect created object macOS extended ACL: %w", err)
	}
	if !exists {
		return nil
	}

	err = unix.Fremovexattr(int(file.Fd()), darwinExtendedSecurityAttribute)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	if err != nil {
		return err
	}
	return validatePlatformExtendedAccess(file)
}

// darwinExtendedAccessExists distinguishes an absent ACL from a protected mutation without changing the object.
func darwinExtendedAccessExists(file *os.File) (bool, error) {
	_, err := unix.Fgetxattr(int(file.Fd()), darwinExtendedSecurityAttribute, nil)
	if errors.Is(err, unix.ENOATTR) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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
