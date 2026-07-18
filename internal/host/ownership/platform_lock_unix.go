//go:build darwin || linux

package ownership

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

const platformLockRetryInterval = 10 * time.Millisecond

// acquirePlatformLock retries a nonblocking whole-file lock until ownership or caller cancellation.
func acquirePlatformLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(platformLockRetryInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// releasePlatformLock relinquishes the whole-file transaction lock before closing its descriptor.
func releasePlatformLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
