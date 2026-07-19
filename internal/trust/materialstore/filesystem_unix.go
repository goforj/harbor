//go:build darwin || linux

package materialstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// openPlatformFileNoFollow retains the final Unix directory entry without following a symbolic link.
func openPlatformFileNoFollow(path string, directory bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directory {
		flags |= unix.O_DIRECTORY
	}
	descriptor, err := unix.Open(path, flags, 0)
	if err != nil {
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("path is a symbolic link")
		}
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		return nil, errors.Join(fmt.Errorf("retain private Unix object handle"), unix.Close(descriptor))
	}
	return file, nil
}

// preparePlatformRoot creates the private leaf without broadening any existing ancestor permissions.
func preparePlatformRoot(path string) error {
	return preparePlatformRootWithSync(path, func(_ string, directory *os.File) error {
		return platformSyncDirectory(directory)
	})
}

// preparePlatformRootWithSync creates missing ancestors one at a time so every new directory entry is durably linked.
func preparePlatformRootWithSync(path string, syncDirectory func(string, *os.File) error) error {
	if syncDirectory == nil {
		return fmt.Errorf("prepare certificate material root: directory sync is required")
	}
	if err := prepareUnixParentHierarchy(filepath.Dir(path), syncDirectory); err != nil {
		return err
	}
	err := os.Mkdir(path, privateDirectoryMode)
	created := err == nil
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create certificate material root: %w", err)
	}
	if created {
		if err := secureAndSyncNewUnixDirectory(path, syncDirectory); err != nil {
			return fmt.Errorf("secure and sync new certificate material root: %w", err)
		}
	} else if err := syncUnixDirectoryAndParent(path, syncDirectory); err != nil {
		return fmt.Errorf("sync existing certificate material root: %w", err)
	}
	return nil
}

// prepareUnixParentHierarchy records and creates only missing ancestors, preserving permissions on every existing directory.
func prepareUnixParentHierarchy(parent string, syncDirectory func(string, *os.File) error) error {
	missing := make([]string, 0, 4)
	existing := ""
	for candidate := filepath.Clean(parent); ; candidate = filepath.Dir(candidate) {
		info, err := os.Stat(candidate)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("certificate material ancestor %q is not a directory", candidate)
			}
			existing = candidate
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect certificate material ancestor %q: %w", candidate, err)
		}
		missing = append(missing, candidate)
		next := filepath.Dir(candidate)
		if next == candidate {
			return fmt.Errorf("certificate material hierarchy has no existing ancestor")
		}
	}
	if filepath.Dir(existing) != existing {
		if err := syncUnixDirectoryAndParent(existing, syncDirectory); err != nil {
			return fmt.Errorf("sync existing certificate material ancestor %q: %w", existing, err)
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		path := missing[index]
		err := os.Mkdir(path, privateDirectoryMode)
		if errors.Is(err, fs.ErrExist) {
			info, statErr := os.Stat(path)
			if statErr != nil || !info.IsDir() {
				return errors.Join(fmt.Errorf("concurrently created certificate material ancestor %q is not a directory", path), statErr)
			}
			if err := syncUnixDirectoryAndParent(path, syncDirectory); err != nil {
				return fmt.Errorf("sync concurrently created certificate material ancestor %q: %w", path, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("create certificate material ancestor %q: %w", path, err)
		}
		if err := secureAndSyncNewUnixDirectory(path, syncDirectory); err != nil {
			return fmt.Errorf("secure and sync certificate material ancestor %q: %w", path, err)
		}
	}
	return nil
}

// secureAndSyncNewUnixDirectory flushes the new directory before the parent entry that makes it reachable.
func secureAndSyncNewUnixDirectory(path string, syncDirectory func(string, *os.File) error) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open new directory: %w", err)
	}
	secureErr := platformSecureCreatedFile(directory, true)
	validateErr := validatePlatformFile(directory, true)
	syncErr := syncDirectory(path, directory)
	closeErr := directory.Close()
	if err := errors.Join(secureErr, validateErr, syncErr, closeErr); err != nil {
		return err
	}
	parentPath := filepath.Dir(path)
	parent, err := os.Open(parentPath)
	if err != nil {
		return fmt.Errorf("open parent directory %q for sync: %w", parentPath, err)
	}
	syncErr = syncDirectory(parentPath, parent)
	closeErr = parent.Close()
	return errors.Join(syncErr, closeErr)
}

// syncUnixDirectoryAndParent retries durability for a hierarchy that may remain after an earlier sync failure.
func syncUnixDirectoryAndParent(path string, syncDirectory func(string, *os.File) error) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	syncErr := syncDirectory(path, directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	parentPath := filepath.Dir(path)
	parent, err := os.Open(parentPath)
	if err != nil {
		return fmt.Errorf("open parent directory %q for sync: %w", parentPath, err)
	}
	syncErr = syncDirectory(parentPath, parent)
	closeErr = parent.Close()
	return errors.Join(syncErr, closeErr)
}

// platformSecureCreatedFile applies exact owner-only mode to the already opened object.
func platformSecureCreatedFile(file *os.File, directory bool) error {
	mode := os.FileMode(privateFileMode)
	if directory {
		mode = privateDirectoryMode
	}
	return file.Chmod(mode)
}

// validatePlatformPath requires a direct owner-controlled Unix object with no effective group or world access.
func validatePlatformPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path is a symbolic link")
	}
	if directory && !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	validateErr := validatePlatformFile(file, directory)
	closeErr := file.Close()
	return errors.Join(validateErr, closeErr)
}

// validatePlatformFile requires a direct owner-controlled Unix handle with no effective group or world access.
func validatePlatformFile(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	wantMode := os.FileMode(privateFileMode)
	if directory {
		wantMode = privateDirectoryMode
	}
	if info.Mode().Perm() != wantMode {
		return fmt.Errorf("permissions are %04o, want %04o", info.Mode().Perm(), wantMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("path has unsupported ownership metadata")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("path is owned by uid %d, want %d", stat.Uid, os.Geteuid())
	}
	if !directory && stat.Nlink != 1 {
		return fmt.Errorf("private file has %d hard links, want 1", stat.Nlink)
	}
	return nil
}

// platformSameFile compares identities captured from retained Unix handles.
func platformSameFile(first *os.File, second *os.File) (bool, error) {
	firstInfo, err := first.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect first private Unix object: %w", err)
	}
	secondInfo, err := second.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect second private Unix object: %w", err)
	}
	return os.SameFile(firstInfo, secondInfo), nil
}

// platformSyncDirectory commits metadata through the already verified directory handle.
func platformSyncDirectory(directory *os.File) error {
	return directory.Sync()
}
