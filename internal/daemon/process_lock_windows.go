//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusiveLock   = 0x00000002
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// prepareRuntimeDirectory creates the daemon leaf beneath the current user's Local AppData security boundary.
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

	return nil
}

// openProcessLockFile opens a non-inheritable Go file handle inside the per-user Local AppData tree.
func openProcessLockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect lock file: %w", err), file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("lock path %q is not a regular file", path), file.Close())
	}

	return file, nil
}

// acquirePlatformLock requests a non-blocking exclusive byte-range lock from the Windows kernel.
func acquirePlatformLock(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := lockFileEx.Call(
		file.Fd(),
		lockFileFailImmediately|lockFileExclusiveLock,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	runtime.KeepAlive(overlapped)
	if result != 0 {
		return nil
	}
	if callErr == syscall.Errno(0) {
		return errors.New("LockFileEx failed without an operating-system error")
	}

	return callErr
}

// isPlatformLockContended separates another live process from unrelated Windows handle failures.
func isPlatformLockContended(err error) bool {
	return errors.Is(err, errorLockViolation)
}

// releasePlatformLock relinquishes the byte range before the process closes its Windows handle.
func releasePlatformLock(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := unlockFileEx.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	runtime.KeepAlive(overlapped)
	if result != 0 {
		return nil
	}
	if callErr == syscall.Errno(0) {
		return errors.New("UnlockFileEx failed without an operating-system error")
	}

	return callErr
}
