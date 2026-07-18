//go:build darwin

package ownership

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const darwinAccessControlAttribute = "com.apple.system.Security"

var inspectDarwinExtendedAccess = unix.Fgetxattr

// validatePlatformExtendedAccess rejects macOS ACLs because they can grant access beyond Unix mode bits.
func validatePlatformExtendedAccess(file *os.File) error {
	_, err := inspectDarwinExtendedAccess(int(file.Fd()), darwinAccessControlAttribute, nil)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect machine ownership macOS access control list: %w", err)
	}
	return fmt.Errorf("machine ownership path has a macOS access control list")
}
