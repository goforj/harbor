//go:build darwin || linux

package ticketredeemer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	unixGatewayMode = 0o711
	unixPrivateDir  = 0o700
	unixPrivateFile = 0o600
)

// validatePlatformProcessAdmission requires the production helper to hold the Unix superuser identity.
func validatePlatformProcessAdmission() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("privileged helper effective UID is %d, want 0", os.Geteuid())
	}
	return nil
}

// openPlatformRootDirectory opens the fixed root without following a final symbolic link.
func openPlatformRootDirectory(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

// openPlatformDirectory resolves one direct child through its retained parent descriptor.
func openPlatformDirectory(parent *os.File, parentPath string, name string) (*os.File, error) {
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(parentPath, name), Err: err}
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

// openPlatformFile resolves one direct non-directory child without permitting a blocking special-file open.
func openPlatformFile(parent *os.File, parentPath string, name string) (*os.File, error) {
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(parentPath, name), Err: err}
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

// platformEntryExists inspects a direct name without following a symbolic link or opening a special object.
func platformEntryExists(parent *os.File, parentPath string, name string) (bool, error) {
	var status unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &status, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, &os.PathError{Op: "stat", Path: filepath.Join(parentPath, name), Err: err}
	}
	return true, nil
}

// validatePlatformGatewayDirectory requires helper ownership while granting only traversal to unprivileged callers.
func validatePlatformGatewayDirectory(file *os.File, _ string) error {
	return validateUnixObject(file, true, unixGatewayMode, uint32(os.Geteuid()), false)
}

// platformPendingIdentity authenticates the requester through the exact owner-private pending directory.
func platformPendingIdentity(file *os.File) (string, error) {
	info, status, err := unixObjectInfo(file, true)
	if err != nil {
		return "", err
	}
	if err := validateUnixSecurity(info.Mode(), unixPrivateDir, "pending directory"); err != nil {
		return "", err
	}
	if err := validatePlatformExtendedAccess(file); err != nil {
		return "", err
	}
	if err := validateUnixPendingOwnerID(status.Uid); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(status.Uid), 10), nil
}

// validateUnixPendingOwnerID prevents the elevated machine identity from masquerading as an interactive requester.
func validateUnixPendingOwnerID(owner uint32) error {
	if owner == 0 {
		return errors.New("pending ticket owner must be a distinct non-root interactive UID")
	}
	return nil
}

// validatePlatformMachineDirectory requires an elevated-helper-owned private directory.
func validatePlatformMachineDirectory(file *os.File) error {
	return validateUnixObject(file, true, unixPrivateDir, uint32(os.Geteuid()), false)
}

// validatePlatformPendingFile binds a direct private file to the authenticated pending-directory owner.
func validatePlatformPendingFile(file *os.File, requesterIdentity string) error {
	owner, err := strconv.ParseUint(requesterIdentity, 10, 32)
	if err != nil || strconv.FormatUint(owner, 10) != requesterIdentity {
		return fmt.Errorf("requester identity %q is not a canonical Unix UID", requesterIdentity)
	}
	return validateUnixObject(file, false, unixPrivateFile, uint32(owner), true)
}

// validatePlatformMachineFile requires a protected claimed file owned only by the elevated helper identity.
func validatePlatformMachineFile(file *os.File) error {
	return validateUnixObject(file, false, unixPrivateFile, uint32(os.Geteuid()), true)
}

// securePlatformClaim transfers the claimed object out of future requester opens.
// Existing same-UID descriptors cannot be revoked on Unix, so later signature checks limit that race to failure or another daemon-signed ticket.
func securePlatformClaim(file *os.File) error {
	if err := file.Chown(os.Geteuid(), os.Getegid()); err != nil {
		return fmt.Errorf("transfer claimed ticket ownership: %w", err)
	}
	if err := file.Chmod(unixPrivateFile); err != nil {
		return fmt.Errorf("restrict claimed ticket mode: %w", err)
	}
	return nil
}

// syncPlatformDirectory persists both sides of the cross-directory claim transition.
func syncPlatformDirectory(directory *os.File) error {
	return directory.Sync()
}

// validatePlatformTopology requires pending and claims to share the filesystem that makes rename atomic.
func validatePlatformTopology(_ *os.File, pending *os.File, claims *os.File) error {
	_, pendingStatus, err := unixObjectInfo(pending, true)
	if err != nil {
		return err
	}
	_, claimsStatus, err := unixObjectInfo(claims, true)
	if err != nil {
		return err
	}
	if pendingStatus.Dev != claimsStatus.Dev {
		return errors.New("pending and claimed ticket directories are on different filesystems")
	}
	return nil
}

// validateUnixObject applies exact type, mode, ownership, link, and extended-access checks to one handle.
func validateUnixObject(file *os.File, directory bool, mode os.FileMode, owner uint32, singleLink bool) error {
	info, status, err := unixObjectInfo(file, directory)
	if err != nil {
		return err
	}
	kind := "file"
	if directory {
		kind = "directory"
	}
	if err := validateUnixSecurity(info.Mode(), mode, kind); err != nil {
		return err
	}
	if status.Uid != owner {
		return fmt.Errorf("%s owner is %d, want %d", kind, status.Uid, owner)
	}
	if singleLink && uint64(status.Nlink) != 1 {
		return fmt.Errorf("%s has %d hard links, want 1", kind, status.Nlink)
	}
	return validatePlatformExtendedAccess(file)
}

// unixObjectInfo rejects type ambiguity before returning native ownership and filesystem identity.
func unixObjectInfo(file *os.File, directory bool) (os.FileInfo, *syscall.Stat_t, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return nil, nil, errors.New("opened ticket spool object has the wrong type")
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, nil, errors.New("opened ticket spool object has unsupported native metadata")
	}
	return info, status, nil
}

// validateUnixSecurity rejects every permission or special bit outside the reviewed exact policy.
func validateUnixSecurity(mode os.FileMode, want os.FileMode, kind string) error {
	const securityBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if got := mode & securityBits; got != want {
		return fmt.Errorf("%s mode is %s, want exactly %04o with no special bits", kind, got, want)
	}
	return nil
}

// unixRenameError preserves ordinary no-source and destination-collision classifications.
func unixRenameError(err error) error {
	if errors.Is(err, unix.ENOENT) {
		return fs.ErrNotExist
	}
	if errors.Is(err, unix.EEXIST) {
		return fs.ErrExist
	}
	return err
}
