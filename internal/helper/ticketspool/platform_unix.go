//go:build darwin || linux

package ticketspool

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// createPlatformFile creates a direct owner-private child beneath the retained directory descriptor.
func createPlatformFile(_ *os.Root, directory *os.File, directoryPath string, name string) (*os.File, error) {
	fileDescriptor, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: directoryPath + string(os.PathSeparator) + name, Err: err}
	}
	return os.NewFile(uintptr(fileDescriptor), name), nil
}

// reopenPlatformFile opens the staged child without following a substituted symbolic link.
func reopenPlatformFile(_ *os.Root, directory *os.File, directoryPath string, name string) (*os.File, error) {
	fileDescriptor, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: directoryPath + string(os.PathSeparator) + name, Err: err}
	}
	return os.NewFile(uintptr(fileDescriptor), name), nil
}

// validatePlatformDirectory requires the daemon profile to exclusively own its pre-provisioned pending directory.
func validatePlatformDirectory(_ string, info os.FileInfo) error {
	return validateUnixObject(info, true)
}

// validatePlatformObject requires direct private files and rejects hard-linked or ACL-broadened staging objects.
func validatePlatformObject(file *os.File, info os.FileInfo, directory bool) error {
	if err := validateUnixObject(info, directory); err != nil {
		return err
	}
	return validatePlatformExtendedACL(file)
}

// validateUnixObject enforces the same owner and modes before and after retaining descriptors.
func validateUnixObject(info os.FileInfo, directory bool) error {
	if special := info.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky); special != 0 {
		return fmt.Errorf("path has special mode bits %v", special)
	}
	if directory {
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory")
		}
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf("directory mode is %04o, want 0700", info.Mode().Perm())
		}
	} else {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path is not a regular file")
		}
		if info.Mode().Perm() != privateFileMode {
			return fmt.Errorf("file mode is %04o, want 0600", info.Mode().Perm())
		}
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(status.Uid) != os.Geteuid() {
		return fmt.Errorf("path is not owned by the current effective user")
	}
	if !directory && uint64(status.Nlink) != 1 {
		return fmt.Errorf("file has %d hard links, want 1", status.Nlink)
	}
	return nil
}

// syncPlatformDirectory commits the final reference name after the file content has been synced.
func syncPlatformDirectory(directory *os.File) error {
	return directory.Sync()
}
