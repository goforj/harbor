//go:build darwin

package devbootstrap

import (
	"errors"
	"fmt"
	"os"

	"github.com/goforj/harbor/internal/platform/darwinacl"
	"golang.org/x/sys/unix"
)

// validatePlatformExtendedAccess rejects named macOS ACLs that exact mode bits cannot describe.
func validatePlatformExtendedAccess(file *os.File) error {
	exists, err := darwinacl.Present(file)
	if err != nil {
		return fmt.Errorf("inspect macOS extended ACL: %w", err)
	}
	if !exists {
		return nil
	}
	return errors.New("object has a macOS extended ACL")
}

// securePlatformCreatedAccess clears inherited ACLs through the retained descriptor before enforcing the exact policy.
func securePlatformCreatedAccess(file *os.File) error {
	exists, err := darwinacl.Present(file)
	if err != nil {
		return fmt.Errorf("inspect created object macOS extended ACL: %w", err)
	}
	if !exists {
		return nil
	}

	if err := darwinacl.Remove(file); err != nil {
		return fmt.Errorf("clear created object macOS extended ACL: %w", err)
	}
	return validatePlatformExtendedAccess(file)
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
