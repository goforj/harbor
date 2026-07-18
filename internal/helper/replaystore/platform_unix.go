//go:build darwin || linux

package replaystore

import (
	"fmt"
	"os"
	"syscall"
)

// createPlatformFile creates one owner-private tombstone beneath the already verified directory handle.
func createPlatformFile(root *os.Root, _ string, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// securePlatformFile relies on exclusive creation with mode 0600 inside the verified owner-private root.
func securePlatformFile(_ *os.File) error {
	return nil
}

// validatePlatformDirectory requires the elevated helper to own a private replay root.
func validatePlatformDirectory(_ string, info os.FileInfo) error {
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("directory mode is %04o, want 0700", info.Mode().Perm())
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(status.Uid) != os.Geteuid() {
		return fmt.Errorf("directory is not owned by the helper identity")
	}
	return nil
}

// validatePlatformRoot relies on SameFile matching the retained handle to the already validated Unix metadata.
func validatePlatformRoot(_ *os.Root) error {
	return nil
}

// validatePlatformFile requires every durable nonce tombstone to remain owner-private.
func validatePlatformFile(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("file mode is %04o, want 0600", info.Mode().Perm())
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(status.Uid) != os.Geteuid() {
		return fmt.Errorf("file is not owned by the helper identity")
	}
	return nil
}

// platformSyncDirectory commits the tombstone link before privileged mutation can begin.
func platformSyncDirectory(directory *os.File) error {
	return directory.Sync()
}
