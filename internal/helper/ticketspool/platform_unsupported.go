//go:build !darwin && !linux && !windows

package ticketspool

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("helper ticket spool is unsupported on this platform")

// createPlatformFile rejects platforms without a reviewed secure-creation policy.
func createPlatformFile(*os.Root, *os.File, string, string) (*os.File, error) {
	return nil, errUnsupported
}

// reopenPlatformFile rejects platforms without a reviewed no-follow open policy.
func reopenPlatformFile(*os.Root, *os.File, string, string) (*os.File, error) {
	return nil, errUnsupported
}

// validatePlatformDirectory rejects platforms without a reviewed private-directory policy.
func validatePlatformDirectory(string, os.FileInfo) error {
	return errUnsupported
}

// validatePlatformObject rejects platforms without a reviewed opened-object policy.
func validatePlatformObject(*os.File, os.FileInfo, bool) error {
	return errUnsupported
}

// renamePlatformNoReplace rejects platforms without a reviewed atomic publication primitive.
func renamePlatformNoReplace(*os.Root, *os.File, *os.File, string, string) (bool, error) {
	return false, errUnsupported
}

// syncPlatformDirectory rejects platforms without a reviewed directory durability primitive.
func syncPlatformDirectory(*os.File) error {
	return errUnsupported
}
