//go:build darwin

package replaystore

import (
	"errors"
	"fmt"
	"os"

	"github.com/goforj/harbor/internal/platform/darwinacl"
)

// validatePlatformExtendedAccess rejects named macOS ACLs that private mode bits cannot describe completely.
func validatePlatformExtendedAccess(file *os.File) error {
	present, err := darwinacl.Present(file)
	if err != nil {
		return fmt.Errorf("inspect replay object macOS extended ACL: %w", err)
	}
	if present {
		return errors.New("replay object has a macOS extended ACL")
	}
	return nil
}
