//go:build darwin || linux

package daemon

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// prepareRuntimeDirectory creates an owner-only leaf and rejects paths controlled by another operating-system user.
func prepareRuntimeDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime directory %q is a symbolic link", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime directory %q is not a directory", path)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("runtime directory %q has unsupported ownership metadata", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("runtime directory %q is owned by uid %d, want %d", path, stat.Uid, os.Geteuid())
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure %q: %w", path, err)
	}

	return nil
}

// openProcessLockFile prevents a lock path symlink from redirecting daemon state outside the owner-only runtime directory.
func openProcessLockFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(fd), path)
	if err := secureProcessLockFile(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return file, nil
}

// secureProcessLockFile rejects non-regular or foreign lock targets before granting daemon authority.
func secureProcessLockFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect lock file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("lock path %q is not a regular file", file.Name())
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("lock path %q has unsupported ownership metadata", file.Name())
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("lock path %q is owned by uid %d, want %d", file.Name(), stat.Uid, os.Geteuid())
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure lock path %q: %w", file.Name(), err)
	}

	return nil
}

// acquirePlatformLock requests a non-blocking whole-file lock so startup never waits behind another daemon.
func acquirePlatformLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// isPlatformLockContended separates a live daemon from filesystem and operating-system failures.
func isPlatformLockContended(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

// releasePlatformLock explicitly releases authority before closing the descriptor during orderly shutdown.
func releasePlatformLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
