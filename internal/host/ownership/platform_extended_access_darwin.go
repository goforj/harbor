//go:build darwin

package ownership

import (
	"fmt"
	"os"

	"github.com/goforj/harbor/internal/platform/darwinacl"
)

// validatePlatformExtendedAccess rejects macOS ACLs because they can grant access beyond Unix mode bits.
func validatePlatformExtendedAccess(file *os.File) error {
	present, err := darwinacl.Present(file)
	if err != nil {
		return fmt.Errorf("inspect machine ownership macOS access control list: %w", err)
	}
	if present {
		return fmt.Errorf("machine ownership path has a macOS access control list")
	}
	return nil
}
