//go:build darwin || linux

package ownership

import (
	"fmt"
	"os"
	"syscall"
)

// validatePlatformDirectory requires the elevated helper to own a directory that other identities cannot modify.
func validatePlatformDirectory(_ string, info os.FileInfo) error {
	if err := validateUnixMode(info.Mode(), 0o700, "directory"); err != nil {
		return err
	}
	return validateUnixOwner(info, "directory")
}

// securePlatformFile removes inherited write access before a created handle can participate in validation or publication.
func securePlatformFile(file *os.File, directory bool) error {
	mode := os.FileMode(privateFileMode)
	if directory {
		mode = 0o700
	}
	return file.Chmod(mode)
}

// validatePlatformFile requires helper ownership, protected mode bits, and one direct filesystem name.
func validatePlatformFile(file *os.File, info os.FileInfo, directory bool) error {
	kind := "file"
	wantMode := os.FileMode(privateFileMode)
	if directory {
		kind = "directory"
		wantMode = 0o700
	}
	if err := validateUnixMode(info.Mode(), wantMode, kind); err != nil {
		return err
	}
	if err := validateUnixOwner(info, kind); err != nil {
		return err
	}
	if !directory {
		status, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("file has unsupported link metadata")
		}
		if status.Nlink != 1 {
			return fmt.Errorf("file has %d hard links, want 1", status.Nlink)
		}
	}
	return validatePlatformExtendedAccess(file)
}

// validateUnixMode excludes every permission and special bit outside the reviewed owner-only boundary.
func validateUnixMode(mode os.FileMode, want os.FileMode, kind string) error {
	const securityBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if got := mode & securityBits; got != want {
		return fmt.Errorf("%s mode is %s, want exactly %04o with no special bits", kind, got, want)
	}
	return nil
}

// validateUnixOwner compares opened filesystem ownership to the elevated helper process identity.
func validateUnixOwner(info os.FileInfo, kind string) error {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has unsupported ownership metadata", kind)
	}
	return validateUnixOwnerID(status.Uid, kind)
}

// validateUnixOwnerID keeps the equality rule directly testable without requiring privileged chown fixtures.
func validateUnixOwnerID(owner uint32, kind string) error {
	if int(owner) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the elevated helper identity", kind)
	}
	return nil
}
