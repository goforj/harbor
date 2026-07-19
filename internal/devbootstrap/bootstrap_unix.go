//go:build darwin || linux

package devbootstrap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const helperStagingRandomBytes = 16

// createdDirectory retains the exact parent capability needed for bounded rollback.
type createdDirectory struct {
	parent *os.File
	name   string
	path   string
}

// unixTransaction owns every descriptor and direct name created during one bootstrap attempt.
type unixTransaction struct {
	files       []*os.File
	created     []createdDirectory
	preserveNew bool
}

// helperIdentity records the direct destination object admitted before atomic replacement.
type helperIdentity struct {
	exists bool
	dev    uint64
	ino    uint64
}

// sourceSnapshot binds copied bytes to one direct source object for the complete staging interval.
type sourceSnapshot struct {
	information os.FileInfo
	dev         uint64
	ino         uint64
}

// retainedDirectory binds one exact policy to the handle opened before helper publication.
type retainedDirectory struct {
	file        *os.File
	requirement directoryPlan
}

// platformEffectiveUID returns the native effective Unix identity used for privileged admission.
func platformEffectiveUID() int {
	return os.Geteuid()
}

// applyPlatformPlan validates the complete existing graph before creating directories and publishing the helper last.
func applyPlatformPlan(prepared plan) (applyErr error) {
	source, snapshot, err := openHelperSource(prepared.helperSource)
	if err != nil {
		return err
	}
	defer func() {
		applyErr = errors.Join(applyErr, source.Close())
	}()

	if err := preflightPlatformPlan(prepared); err != nil {
		return err
	}

	transaction := &unixTransaction{}
	defer func() {
		if !transaction.preserveNew {
			applyErr = errors.Join(applyErr, transaction.rollback())
		}
		applyErr = errors.Join(applyErr, transaction.close())
	}()

	retainedDirectories := make([]retainedDirectory, 0, len(prepared.directories))
	for _, directory := range prepared.directories {
		handle, _, err := transaction.walkDirectory(directory.path, &directory, true)
		if err != nil {
			return err
		}
		retainedDirectories = append(retainedDirectories, retainedDirectory{file: handle, requirement: directory})
	}

	helperParentPath := filepath.Dir(prepared.helperDestination)
	helperParent, _, err := transaction.walkDirectory(helperParentPath, nil, true)
	if err != nil {
		return err
	}
	initial, err := inspectHelperDestination(helperParent, prepared)
	if err != nil {
		return err
	}
	published, err := installHelper(source, snapshot, helperParent, initial, prepared)
	if published {
		// Once a name transition has applied, removing any surrounding topology could strand a valid helper or erase concurrent runtime data.
		transaction.preserveNew = true
	}
	if err != nil {
		if published {
			return errors.Join(ErrDurabilityUncertain, err)
		}
		return err
	}
	if err := transaction.revalidateInstalledPlan(prepared, retainedDirectories, helperParent); err != nil {
		return errors.Join(ErrDurabilityUncertain, err)
	}
	transaction.preserveNew = true
	return nil
}

// revalidateInstalledPlan proves retained metadata and every fixed direct name still describe the complete installed graph.
func (transaction *unixTransaction) revalidateInstalledPlan(
	prepared plan,
	retained []retainedDirectory,
	helperParent *os.File,
) error {
	for _, directory := range retained {
		if err := validateExactDirectory(directory.file, directory.requirement.path, directory.requirement); err != nil {
			return fmt.Errorf("revalidate retained development bootstrap topology: %w", err)
		}
		reopened, exists, err := transaction.walkDirectory(directory.requirement.path, &directory.requirement, false)
		if err != nil {
			return fmt.Errorf("revalidate direct development bootstrap topology: %w", err)
		}
		if !exists {
			return fmt.Errorf("development bootstrap directory %q disappeared after helper installation", directory.requirement.path)
		}
		if err := requireSameFileIdentity(directory.file, reopened, directory.requirement.path); err != nil {
			return err
		}
	}
	reopenedParent, exists, err := transaction.walkDirectory(filepath.Dir(prepared.helperDestination), nil, false)
	if err != nil {
		return fmt.Errorf("revalidate development helper parent: %w", err)
	}
	if !exists {
		return fmt.Errorf("development helper parent %q disappeared after installation", filepath.Dir(prepared.helperDestination))
	}
	if err := requireSameFileIdentity(helperParent, reopenedParent, filepath.Dir(prepared.helperDestination)); err != nil {
		return err
	}
	installed, err := inspectHelperDestination(reopenedParent, prepared)
	if err != nil {
		return fmt.Errorf("revalidate installed development helper: %w", err)
	}
	if !installed.exists {
		return fmt.Errorf("installed development helper %q disappeared during final validation", prepared.helperDestination)
	}
	return nil
}

// preflightPlatformPlan rejects every observable policy violation before any transaction object is created.
func preflightPlatformPlan(prepared plan) (preflightErr error) {
	transaction := &unixTransaction{preserveNew: true}
	defer func() {
		preflightErr = errors.Join(preflightErr, transaction.close())
	}()

	for _, directory := range prepared.directories {
		_, _, err := transaction.walkDirectory(directory.path, &directory, false)
		if err != nil {
			return err
		}
	}

	helperParent, exists, err := transaction.walkDirectory(filepath.Dir(prepared.helperDestination), nil, false)
	if err != nil {
		return err
	}
	if exists {
		if _, err := inspectHelperDestination(helperParent, prepared); err != nil {
			return err
		}
	}
	return nil
}

// walkDirectory opens an absolute path component-by-component and creates only absent fixed components when requested.
func (transaction *unixTransaction) walkDirectory(path string, exact *directoryPlan, create bool) (*os.File, bool, error) {
	root, err := openRootDirectory()
	if err != nil {
		return nil, false, err
	}
	transaction.retain(root)
	if err := validateAncestorDirectory(root, "/"); err != nil {
		return nil, false, err
	}

	parent := root
	parentPath := "/"
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, name := range components {
		final := index == len(components)-1
		status, exists, err := statDirectEntry(parent, parentPath, name)
		if err != nil {
			return nil, false, err
		}
		if !exists && !create {
			return nil, false, nil
		}

		childPath := filepath.Join(parentPath, name)
		created := false
		if !exists {
			if err := unix.Mkdirat(int(parent.Fd()), name, privateDirectoryMode); err != nil {
				if !errors.Is(err, unix.EEXIST) {
					return nil, false, &os.PathError{Op: "mkdir", Path: childPath, Err: err}
				}
				status, exists, err = statDirectEntry(parent, parentPath, name)
				if err != nil || !exists {
					return nil, false, errors.Join(fmt.Errorf("development bootstrap directory %q raced with creation", childPath), err)
				}
			} else {
				created = true
				transaction.created = append(transaction.created, createdDirectory{parent: parent, name: name, path: childPath})
			}
		}

		child, err := openDirectDirectory(parent, childPath, name)
		if err != nil {
			return nil, false, err
		}
		transaction.retain(child)
		if created {
			requirement := directoryPlan{path: childPath, mode: ancestorDirectoryMode, uid: 0, gid: 0}
			if final && exact != nil {
				requirement = *exact
			}
			if err := secureCreatedDirectory(child, requirement); err != nil {
				return nil, false, err
			}
			if err := child.Sync(); err != nil {
				return nil, false, fmt.Errorf("sync created development bootstrap directory %q: %w", childPath, err)
			}
			if err := parent.Sync(); err != nil {
				return nil, false, fmt.Errorf("sync development bootstrap parent %q: %w", parentPath, err)
			}
		} else if final && exact != nil {
			if err := validateExactDirectory(child, childPath, *exact); err != nil {
				return nil, false, err
			}
		} else if err := validateAncestorDirectory(child, childPath); err != nil {
			return nil, false, err
		}
		if !created {
			if err := requireSameDirectObject(parent, parentPath, name, child); err != nil {
				return nil, false, err
			}
		}
		_ = status
		parent = child
		parentPath = childPath
	}
	return parent, true, nil
}

// retain keeps a descriptor live until rollback and final validation no longer need its directory capability.
func (transaction *unixTransaction) retain(file *os.File) {
	transaction.files = append(transaction.files, file)
}

// rollback removes only direct directory names created by this transaction and never descends into their contents.
func (transaction *unixTransaction) rollback() error {
	var rollbackErr error
	for index := len(transaction.created) - 1; index >= 0; index-- {
		created := transaction.created[index]
		err := unix.Unlinkat(int(created.parent.Fd()), created.name, unix.AT_REMOVEDIR)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, &os.PathError{Op: "remove", Path: created.path, Err: err})
			continue
		}
		if err := created.parent.Sync(); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("sync rollback parent for %q: %w", created.path, err))
		}
	}
	return rollbackErr
}

// close releases retained descriptors in reverse order so children do not outlive their required parent capabilities.
func (transaction *unixTransaction) close() error {
	var closeErr error
	for index := len(transaction.files) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, transaction.files[index].Close())
	}
	transaction.files = nil
	return closeErr
}

// openRootDirectory starts every fixed-path traversal from an unfollowed native root descriptor.
func openRootDirectory() (*os.File, error) {
	descriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: "/", Err: err}
	}
	return os.NewFile(uintptr(descriptor), "/"), nil
}

// openDirectDirectory resolves one child only through its already validated parent descriptor.
func openDirectDirectory(parent *os.File, path string, name string) (*os.File, error) {
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

// statDirectEntry inspects one child without following a symbolic link or opening a special object.
func statDirectEntry(parent *os.File, parentPath string, name string) (unix.Stat_t, bool, error) {
	var status unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &status, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return unix.Stat_t{}, false, nil
	}
	if err != nil {
		return unix.Stat_t{}, false, &os.PathError{Op: "stat", Path: filepath.Join(parentPath, name), Err: err}
	}
	return status, true, nil
}

// secureCreatedDirectory establishes exact ownership and mode only on an object created by this transaction.
func secureCreatedDirectory(directory *os.File, requirement directoryPlan) error {
	if err := unix.Fchown(int(directory.Fd()), int(requirement.uid), int(requirement.gid)); err != nil {
		return fmt.Errorf("assign development bootstrap directory %q ownership: %w", requirement.path, err)
	}
	if err := unix.Fchmod(int(directory.Fd()), requirement.mode); err != nil {
		return fmt.Errorf("set development bootstrap directory %q mode: %w", requirement.path, err)
	}
	if err := securePlatformCreatedAccess(directory); err != nil {
		return fmt.Errorf("secure development bootstrap directory %q extended access: %w", requirement.path, err)
	}
	return validateExactDirectory(directory, requirement.path, requirement)
}

// validateExactDirectory enforces the runtime's precise direct-directory ownership, mode, and extended-access policy.
func validateExactDirectory(directory *os.File, path string, requirement directoryPlan) error {
	status, err := fileStatus(directory)
	if err != nil {
		return fmt.Errorf("inspect development bootstrap directory %q: %w", path, err)
	}
	if statusModeType(status) != unix.S_IFDIR {
		return fmt.Errorf("%w: %q is not a direct directory", ErrUnsafeObject, path)
	}
	if statusModeSecurity(status) != requirement.mode {
		return fmt.Errorf("%w: directory %q mode is %04o, want exactly %04o", ErrUnsafeObject, path, statusModeSecurity(status), requirement.mode)
	}
	if uint32(status.Uid) != requirement.uid || uint32(status.Gid) != requirement.gid {
		return fmt.Errorf(
			"%w: directory %q owner is %d:%d, want %d:%d",
			ErrUnsafeObject,
			path,
			status.Uid,
			status.Gid,
			requirement.uid,
			requirement.gid,
		)
	}
	if err := validatePlatformExtendedAccess(directory); err != nil {
		return fmt.Errorf("%w: validate directory %q extended access: %v", ErrUnsafeObject, path, err)
	}
	return nil
}

// validateAncestorDirectory permits native system-directory modes only when no non-root identity can replace descendants.
func validateAncestorDirectory(directory *os.File, path string) error {
	status, err := fileStatus(directory)
	if err != nil {
		return fmt.Errorf("inspect development bootstrap ancestor %q: %w", path, err)
	}
	if statusModeType(status) != unix.S_IFDIR {
		return fmt.Errorf("%w: ancestor %q is not a direct directory", ErrUnsafeObject, path)
	}
	mode := statusModeSecurity(status)
	if !validAncestorDirectoryPolicy(uint32(status.Uid), mode) {
		return fmt.Errorf("%w: ancestor %q owner/mode is %d/%04o, want root-owned with no set-ID or non-root write bits", ErrUnsafeObject, path, status.Uid, mode)
	}
	if err := validatePlatformExtendedAccess(directory); err != nil {
		return fmt.Errorf("%w: validate ancestor %q extended access: %v", ErrUnsafeObject, path, err)
	}
	return nil
}

// validAncestorDirectoryPolicy allows sticky system directories because sticky cannot grant replacement authority without a write bit.
func validAncestorDirectoryPolicy(ownerUID uint32, mode uint32) bool {
	return ownerUID == 0 && mode&(0o6000|0o022) == 0
}

// requireSameDirectObject detects replacement between no-follow inspection and descriptor retention.
func requireSameDirectObject(parent *os.File, parentPath string, name string, opened *os.File) error {
	current, exists, err := statDirectEntry(parent, parentPath, name)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: directory %q disappeared while opening", ErrUnsafeObject, filepath.Join(parentPath, name))
	}
	retained, err := fileStatus(opened)
	if err != nil {
		return err
	}
	if !sameStatusIdentity(current, retained) {
		return fmt.Errorf("%w: directory %q changed while opening", ErrUnsafeObject, filepath.Join(parentPath, name))
	}
	return nil
}

// requireSameFileIdentity proves two retained handles still refer to one native filesystem object.
func requireSameFileIdentity(expected *os.File, actual *os.File, path string) error {
	expectedStatus, err := fileStatus(expected)
	if err != nil {
		return fmt.Errorf("inspect retained development bootstrap object %q: %w", path, err)
	}
	actualStatus, err := fileStatus(actual)
	if err != nil {
		return fmt.Errorf("inspect reopened development bootstrap object %q: %w", path, err)
	}
	if !sameStatusIdentity(expectedStatus, actualStatus) {
		return fmt.Errorf("%w: development bootstrap object %q changed during installation", ErrUnsafeObject, path)
	}
	return nil
}

// openHelperSource retains one direct, single-link executable source before privileged destinations are inspected.
func openHelperSource(path string) (*os.File, sourceSnapshot, error) {
	direct, err := os.Lstat(path)
	if err != nil {
		return nil, sourceSnapshot{}, fmt.Errorf("inspect development helper source %q: %w", path, err)
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, sourceSnapshot{}, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(descriptor), path)
	information, err := file.Stat()
	if err != nil {
		return nil, sourceSnapshot{}, errors.Join(fmt.Errorf("inspect opened development helper source %q: %w", path, err), file.Close())
	}
	status, err := fileStatus(file)
	if err != nil {
		return nil, sourceSnapshot{}, errors.Join(fmt.Errorf("inspect development helper source %q native metadata: %w", path, err), file.Close())
	}
	if !direct.Mode().IsRegular() || !information.Mode().IsRegular() || !os.SameFile(direct, information) {
		return nil, sourceSnapshot{}, errors.Join(fmt.Errorf("development helper source %q is not one direct regular file", path), file.Close())
	}
	if uint64(status.Nlink) != 1 {
		return nil, sourceSnapshot{}, errors.Join(fmt.Errorf("development helper source %q has %d hard links, want 1", path, status.Nlink), file.Close())
	}
	if information.Size() == 0 {
		return nil, sourceSnapshot{}, errors.Join(fmt.Errorf("development helper source %q is empty", path), file.Close())
	}
	if information.Mode().Perm()&0o111 == 0 {
		return nil, sourceSnapshot{}, errors.Join(fmt.Errorf("development helper source %q is not executable", path), file.Close())
	}
	return file, sourceSnapshot{
		information: information,
		dev:         uint64(status.Dev),
		ino:         uint64(status.Ino),
	}, nil
}

// validateHelperSourceStable rejects source replacement or mutation while its privileged copy was being staged.
func validateHelperSourceStable(source *os.File, snapshot sourceSnapshot, path string) error {
	opened, err := source.Stat()
	if err != nil {
		return fmt.Errorf("reinspect development helper source %q: %w", path, err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("reinspect direct development helper source %q: %w", path, err)
	}
	status, err := fileStatus(source)
	if err != nil || uint64(status.Dev) != snapshot.dev || uint64(status.Ino) != snapshot.ino ||
		!os.SameFile(opened, current) || opened.Size() != snapshot.information.Size() ||
		!opened.ModTime().Equal(snapshot.information.ModTime()) {
		return fmt.Errorf("development helper source %q changed while staging", path)
	}
	return nil
}

// inspectHelperDestination accepts only the platform's exact existing Harbor helper shape before replacement.
func inspectHelperDestination(parent *os.File, prepared plan) (helperIdentity, error) {
	name := filepath.Base(prepared.helperDestination)
	status, exists, err := statDirectEntry(parent, filepath.Dir(prepared.helperDestination), name)
	if err != nil {
		return helperIdentity{}, err
	}
	if !exists {
		return helperIdentity{}, nil
	}
	if err := validateHelperStatus(status, prepared); err != nil {
		return helperIdentity{}, fmt.Errorf("%w: existing helper %q: %v", ErrUnsafeObject, prepared.helperDestination, err)
	}
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return helperIdentity{}, &os.PathError{Op: "open", Path: prepared.helperDestination, Err: err}
	}
	helper := os.NewFile(uintptr(descriptor), prepared.helperDestination)
	opened, inspectErr := fileStatus(helper)
	if inspectErr == nil && !sameStatusIdentity(status, opened) {
		inspectErr = fmt.Errorf("%w: existing helper %q changed while opening", ErrUnsafeObject, prepared.helperDestination)
	}
	if inspectErr == nil {
		inspectErr = validatePlatformExtendedAccess(helper)
	}
	closeErr := helper.Close()
	if inspectErr != nil || closeErr != nil {
		return helperIdentity{}, errors.Join(
			fmt.Errorf("%w: validate existing helper %q extended access: %v", ErrUnsafeObject, prepared.helperDestination, inspectErr),
			closeErr,
		)
	}
	return helperIdentity{exists: true, dev: uint64(status.Dev), ino: uint64(status.Ino)}, nil
}

// validateHelperStatus enforces the installed helper policy shared with the native launcher.
func validateHelperStatus(status unix.Stat_t, prepared plan) error {
	if statusModeType(status) != unix.S_IFREG {
		return errors.New("object is not a direct regular file")
	}
	if statusModeSecurity(status) != prepared.helperMode {
		return fmt.Errorf("mode is %04o, want exactly %04o", statusModeSecurity(status), prepared.helperMode)
	}
	if uint32(status.Uid) != prepared.helperUID {
		return fmt.Errorf("owner UID is %d, want %d", status.Uid, prepared.helperUID)
	}
	if uint32(status.Gid) != prepared.helperGID {
		return fmt.Errorf("owner GID is %d, want %d", status.Gid, prepared.helperGID)
	}
	if uint64(status.Nlink) != 1 {
		return fmt.Errorf("link count is %d, want 1", status.Nlink)
	}
	return nil
}

// installHelper stages copied bytes durably beside the fixed destination before one atomic name transition.
func installHelper(
	source *os.File,
	snapshot sourceSnapshot,
	parent *os.File,
	initial helperIdentity,
	prepared plan,
) (published bool, installErr error) {
	stagingName, staging, err := createHelperStagingFile(parent, filepath.Dir(prepared.helperDestination))
	if err != nil {
		return false, err
	}
	stagingOpen := true
	defer func() {
		if stagingOpen {
			installErr = errors.Join(installErr, staging.Close())
		}
		removeErr := unix.Unlinkat(int(parent.Fd()), stagingName, 0)
		if removeErr == nil {
			installErr = errors.Join(installErr, parent.Sync())
		} else if !errors.Is(removeErr, unix.ENOENT) {
			installErr = errors.Join(installErr, &os.PathError{Op: "remove", Path: filepath.Join(filepath.Dir(prepared.helperDestination), stagingName), Err: removeErr})
		}
	}()

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("rewind development helper source: %w", err)
	}
	written, err := io.Copy(staging, source)
	if err != nil {
		return false, fmt.Errorf("copy development helper source: %w", err)
	}
	if written != snapshot.information.Size() {
		return false, fmt.Errorf("copy development helper source wrote %d bytes, want %d", written, snapshot.information.Size())
	}
	if err := validateHelperSourceStable(source, snapshot, prepared.helperSource); err != nil {
		return false, err
	}
	if err := unix.Fchown(int(staging.Fd()), int(prepared.helperUID), int(prepared.helperGID)); err != nil {
		return false, fmt.Errorf("assign staged development helper ownership: %w", err)
	}
	if err := unix.Fchmod(int(staging.Fd()), prepared.helperMode); err != nil {
		return false, fmt.Errorf("set staged development helper mode: %w", err)
	}
	if err := securePlatformCreatedAccess(staging); err != nil {
		return false, fmt.Errorf("secure staged development helper extended access: %w", err)
	}
	if err := staging.Sync(); err != nil {
		return false, fmt.Errorf("sync staged development helper: %w", err)
	}
	stagedStatus, err := fileStatus(staging)
	if err != nil {
		return false, fmt.Errorf("inspect staged development helper: %w", err)
	}
	if err := validateHelperStatus(stagedStatus, prepared); err != nil {
		return false, fmt.Errorf("validate staged development helper: %w", err)
	}
	if err := staging.Close(); err != nil {
		stagingOpen = false
		return false, fmt.Errorf("close staged development helper: %w", err)
	}
	stagingOpen = false

	current, err := inspectHelperDestination(parent, prepared)
	if err != nil {
		return false, err
	}
	if current != initial {
		return false, fmt.Errorf("%w: helper destination %q changed during bootstrap", ErrUnsafeObject, prepared.helperDestination)
	}
	destinationName := filepath.Base(prepared.helperDestination)
	if initial.exists {
		if err := unix.Renameat(int(parent.Fd()), stagingName, int(parent.Fd()), destinationName); err != nil {
			return false, &os.PathError{Op: "rename", Path: prepared.helperDestination, Err: err}
		}
	} else if err := publishPlatformNoReplace(parent, stagingName, destinationName); err != nil {
		return false, &os.PathError{Op: "rename", Path: prepared.helperDestination, Err: err}
	}
	published = true

	installed, exists, err := statDirectEntry(parent, filepath.Dir(prepared.helperDestination), destinationName)
	if err != nil {
		return true, err
	}
	if !exists || uint64(installed.Dev) != uint64(stagedStatus.Dev) || uint64(installed.Ino) != uint64(stagedStatus.Ino) {
		return true, fmt.Errorf("installed development helper %q changed before durability confirmation", prepared.helperDestination)
	}
	if err := validateHelperStatus(installed, prepared); err != nil {
		return true, fmt.Errorf("validate installed development helper: %w", err)
	}
	if err := parent.Sync(); err != nil {
		return true, fmt.Errorf("sync development helper directory: %w", err)
	}
	return true, nil
}

// createHelperStagingFile reserves a private random direct name in the destination directory.
func createHelperStagingFile(parent *os.File, parentPath string) (string, *os.File, error) {
	for attempt := 0; attempt < 128; attempt++ {
		randomBytes := make([]byte, helperStagingRandomBytes)
		if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
			return "", nil, fmt.Errorf("generate development helper staging name: %w", err)
		}
		name := ".harbor-helper.devbootstrap-" + hex.EncodeToString(randomBytes)
		descriptor, err := unix.Openat(
			int(parent.Fd()),
			name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, &os.PathError{Op: "open", Path: filepath.Join(parentPath, name), Err: err}
		}
		return name, os.NewFile(uintptr(descriptor), filepath.Join(parentPath, name)), nil
	}
	return "", nil, errors.New("development helper staging names were exhausted")
}

// fileStatus returns native metadata for one already retained object.
func fileStatus(file *os.File) (unix.Stat_t, error) {
	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil {
		return unix.Stat_t{}, err
	}
	return status, nil
}

// statusModeType normalizes platform-specific stat mode widths before object-type comparison.
func statusModeType(status unix.Stat_t) uint32 {
	return uint32(status.Mode) & uint32(unix.S_IFMT)
}

// statusModeSecurity returns every permission, set-ID, and sticky bit enforced by exact policy.
func statusModeSecurity(status unix.Stat_t) uint32 {
	return uint32(status.Mode) & 0o7777
}

// sameStatusIdentity compares the stable filesystem identity fields available on both supported Unix platforms.
func sameStatusIdentity(left unix.Stat_t, right unix.Stat_t) bool {
	return uint64(left.Dev) == uint64(right.Dev) && uint64(left.Ino) == uint64(right.Ino)
}

// classifyNoReplaceError preserves an ordinary destination collision for callers and tests.
func classifyNoReplaceError(err error) error {
	if errors.Is(err, unix.EEXIST) {
		return fs.ErrExist
	}
	return err
}
