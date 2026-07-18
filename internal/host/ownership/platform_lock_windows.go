//go:build windows

package ownership

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	lockFileFailImmediately   = 0x00000001
	lockFileExclusiveLock     = 0x00000002
	errorLockViolation        = syscall.Errno(33)
	platformLockRetryInterval = 10 * time.Millisecond
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// acquirePlatformLock retries a nonblocking byte-range lock until ownership or caller cancellation.
func acquirePlatformLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(platformLockRetryInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		acquired, err := tryAcquirePlatformLock(file)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// tryAcquirePlatformLock distinguishes ordinary contention from a broken Windows lock handle.
func tryAcquirePlatformLock(file *os.File) (bool, error) {
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
		return true, nil
	}
	if errors.Is(callErr, errorLockViolation) {
		return false, nil
	}
	if callErr == syscall.Errno(0) {
		return false, errors.New("LockFileEx failed without an operating-system error")
	}
	return false, callErr
}

// releasePlatformLock relinquishes the byte-range transaction lock before closing its handle.
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
