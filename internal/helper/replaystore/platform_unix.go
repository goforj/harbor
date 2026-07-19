//go:build darwin || linux

package replaystore

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const (
	unixReplayDirectoryMode os.FileMode = 0o700
	unixReplayFileMode      os.FileMode = 0o600
)

// createPlatformFile creates one owner-private tombstone beneath the already verified directory handle.
func createPlatformFile(root *os.Root, _ string, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, unixReplayFileMode)
}

// securePlatformFile relies on exclusive creation before handle-level metadata validation rejects umask or owner drift.
func securePlatformFile(_ *os.File) error {
	return nil
}

// validatePlatformDirectory rejects unsafe path metadata before retaining the replay root.
func validatePlatformDirectory(_ string, info os.FileInfo, owner uint32) error {
	return validateUnixReplayMetadata(info, true, owner)
}

// validatePlatformRoot rechecks exact metadata and extended access through the retained directory handle.
func validatePlatformRoot(root *os.Root, owner uint32) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	validationErr := validateUnixReplayObject(directory, true, owner)
	closeErr := directory.Close()
	return errors.Join(validationErr, closeErr)
}

// validatePlatformFile requires every durable nonce tombstone to retain the exact machine-private policy.
func validatePlatformFile(file *os.File, _ os.FileInfo, owner uint32) error {
	return validateUnixReplayObject(file, false, owner)
}

// validateUnixReplayObject combines native metadata with access controls not represented by Unix mode bits.
func validateUnixReplayObject(file *os.File, directory bool, owner uint32) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := validateUnixReplayMetadata(info, directory, owner); err != nil {
		return err
	}
	return validatePlatformExtendedAccess(file)
}

// validateUnixReplayMetadata applies the exact type, mode, owner, and link policy to one stat result.
func validateUnixReplayMetadata(info os.FileInfo, directory bool, owner uint32) error {
	kind := "file"
	wantMode := unixReplayFileMode
	if directory {
		kind = "directory"
		wantMode = unixReplayDirectoryMode
	}
	if directory && info.Mode().Type() != os.ModeDir || !directory && info.Mode().Type() != 0 {
		return fmt.Errorf("replay %s has the wrong object type", kind)
	}
	const securityBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if got := info.Mode() & securityBits; got != wantMode {
		return fmt.Errorf("replay %s mode is %s, want exactly %04o with no special bits", kind, got, wantMode)
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("replay %s has unsupported native metadata", kind)
	}
	if status.Uid != owner {
		return fmt.Errorf("replay %s owner is UID %d, want UID %d", kind, status.Uid, owner)
	}
	if !directory && uint64(status.Nlink) != 1 {
		return fmt.Errorf("replay file has %d hard links, want 1", status.Nlink)
	}
	return nil
}

// platformSyncDirectory commits the tombstone link before privileged mutation can begin.
func platformSyncDirectory(directory *os.File) error {
	return directory.Sync()
}
