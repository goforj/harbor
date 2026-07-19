//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

const projectRemovalIntentLockRetryInterval = 10 * time.Millisecond

// acquireProjectRemovalIntentLock waits on a nonblocking file lock so context cancellation stays responsive.
func acquireProjectRemovalIntentLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(projectRemovalIntentLockRetryInterval)
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

// releaseProjectRemovalIntentLock relinquishes the whole-file lock before its descriptor closes.
func releaseProjectRemovalIntentLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

// validateProjectRemovalIntentObject requires the current user to exclusively own a direct journal object.
func validateProjectRemovalIntentObject(file *os.File, directory bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if directory && !info.IsDir() {
		return errors.New("project removal intent object is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return errors.New("project removal intent object is not a regular file")
	}
	wantMode := os.FileMode(projectRemovalIntentFileMode)
	if directory {
		wantMode = projectRemovalIntentDirectoryMode
	}
	if info.Mode().Perm() != wantMode {
		return fmt.Errorf("project removal intent permissions are %04o, want %04o", info.Mode().Perm(), wantMode)
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("project removal intent object has unsupported ownership metadata")
	}
	if status.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("project removal intent object is owned by uid %d, want %d", status.Uid, os.Geteuid())
	}
	if !directory && status.Nlink != 1 {
		return fmt.Errorf("project removal intent file has %d links, want 1", status.Nlink)
	}
	return nil
}

// syncProjectRemovalIntentDirectory makes a preceding record rename or removal durable on Unix filesystems.
func syncProjectRemovalIntentDirectory(directory *os.File) error {
	return directory.Sync()
}
