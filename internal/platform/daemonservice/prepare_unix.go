//go:build darwin || linux

package daemonservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/goforj/harbor/internal/platform/runtimepath"
)

// ErrDaemonActive preserves a daemon that already owns this user's singleton authority.
var ErrDaemonActive = errors.New("an existing Harbor daemon owns the user runtime")

// PrepareDatabase serializes the selected release's migrations against a running source or installed daemon.
func PrepareDatabase(migrate func() error) error {
	directory, err := runtimepath.Directory()
	if err != nil {
		return err
	}
	return prepareDatabaseAt(directory, migrate)
}

// prepareDatabaseAt shares the daemon's exact native lock without retaining it across exec.
func prepareDatabaseAt(directory string, migrate func() error) (result error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	information, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok || !information.IsDir() || information.Mode()&os.ModeSymlink != 0 || status.Uid != uint32(os.Geteuid()) || information.Mode().Perm()&0o077 != 0 {
		return errors.New("installed daemon preparation requires an owner-private runtime directory")
	}
	path := filepath.Join(directory, runtimepath.DaemonLockFilename)
	descriptor, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer func() { result = errors.Join(result, file.Close()) }()
	information, err = file.Stat()
	if err != nil {
		return err
	}
	status, ok = information.Sys().(*syscall.Stat_t)
	if !ok || !information.Mode().IsRegular() || status.Uid != uint32(os.Geteuid()) || status.Nlink != 1 || information.Mode().Perm()&0o077 != 0 {
		return errors.New("installed daemon preparation found an unsafe singleton lock")
	}
	if err := syscall.Flock(descriptor, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrDaemonActive
		}
		return fmt.Errorf("lock installed daemon preparation: %w", err)
	}
	defer func() { result = errors.Join(result, syscall.Flock(descriptor, syscall.LOCK_UN)) }()
	return migrate()
}
