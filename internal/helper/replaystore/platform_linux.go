//go:build linux

package replaystore

import "os"

// validatePlatformExtendedAccess relies on exact zero group bits to zero every effective POSIX ACL mask grant.
func validatePlatformExtendedAccess(_ *os.File) error {
	return nil
}
