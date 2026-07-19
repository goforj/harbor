//go:build darwin

package replaystore

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const darwinReplayExtendedSecurityAttribute = "com.apple.system.Security"

// validatePlatformExtendedAccess rejects named macOS ACLs that private mode bits cannot describe completely.
func validatePlatformExtendedAccess(file *os.File) error {
	_, err := unix.Fgetxattr(int(file.Fd()), darwinReplayExtendedSecurityAttribute, nil)
	if errors.Is(err, unix.ENOATTR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect replay object macOS extended ACL: %w", err)
	}
	return errors.New("replay object has a macOS extended ACL")
}
