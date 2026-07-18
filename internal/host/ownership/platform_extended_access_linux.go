//go:build linux

package ownership

import "os"

// validatePlatformExtendedAccess relies on the exact group-class mode bits as Linux's POSIX ACL mask.
func validatePlatformExtendedAccess(_ *os.File) error {
	// A zero group-class mask makes every named-user and named-group ACL grant ineffective.
	return nil
}
