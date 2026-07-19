//go:build windows

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const projectRemovalIntentLockRetryInterval = 10 * time.Millisecond

// acquireProjectRemovalIntentLock waits on a nonblocking byte-range lock so context cancellation stays responsive.
func acquireProjectRemovalIntentLock(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(projectRemovalIntentLockRetryInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		overlapped := new(windows.Overlapped)
		err := windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// releaseProjectRemovalIntentLock relinquishes the byte-range lock before its handle closes.
func releaseProjectRemovalIntentLock(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
}

// validateProjectRemovalIntentObject rejects reparse points and unexpected object or hard-link types.
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
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return fmt.Errorf("read project removal intent file information: %w", err)
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("project removal intent object is a Windows reparse point")
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read project removal intent owner: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("decode project removal intent owner: %w", err)
	}
	wantOwner, err := currentProjectRemovalIntentWindowsUserSID()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(wantOwner) {
		got := ""
		if owner != nil {
			got = owner.String()
		}
		return fmt.Errorf("project removal intent owner is %q, want %q", got, wantOwner.String())
	}
	if !directory && information.NumberOfLinks != 1 {
		return fmt.Errorf("project removal intent file has %d links, want 1", information.NumberOfLinks)
	}
	return nil
}

// currentProjectRemovalIntentWindowsUserSID resolves the interactive owner required for per-user journal state.
func currentProjectRemovalIntentWindowsUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user SID: %w", err)
	}
	return user.User.Sid, nil
}

// syncProjectRemovalIntentDirectory requests the strongest directory flush Windows exposes when supported.
func syncProjectRemovalIntentDirectory(directory *os.File) error {
	err := windows.FlushFileBuffers(windows.Handle(directory.Fd()))
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil
	}
	return err
}
