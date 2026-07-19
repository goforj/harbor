//go:build windows

package projectprocess

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// TestObserveProcessBirthTokenTreatsSignaledProcessAsAbsent proves an exited process object cannot retain lifecycle authority.
func TestObserveProcessBirthTokenTreatsSignaledProcessAsAbsent(t *testing.T) {
	const handle windows.Handle = 42
	var access uint32
	closed := false
	timesCalled := false
	observer := windowsProcessObserver{
		open: func(requestedAccess uint32, inheritHandle bool, pid uint32) (windows.Handle, error) {
			access = requestedAccess
			return handle, nil
		},
		close: func(closedHandle windows.Handle) error {
			if closedHandle != handle {
				t.Fatalf("CloseHandle handle = %d, want %d", closedHandle, handle)
			}
			closed = true
			return nil
		},
		wait: func(waitedHandle windows.Handle, milliseconds uint32) (uint32, error) {
			if waitedHandle != handle {
				t.Fatalf("WaitForSingleObject handle = %d, want %d", waitedHandle, handle)
			}
			if milliseconds != 0 {
				t.Fatalf("WaitForSingleObject timeout = %d, want 0", milliseconds)
			}
			return windows.WAIT_OBJECT_0, nil
		},
		times: func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
			timesCalled = true
			return nil
		},
	}

	token, present, err := observeProcessBirthTokenWith(100, observer)
	if err != nil {
		t.Fatalf("observeProcessBirthTokenWith() error = %v", err)
	}
	if present || token != "" {
		t.Fatalf("observeProcessBirthTokenWith() = (%q, %t), want absent", token, present)
	}
	wantAccess := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE)
	if access != wantAccess {
		t.Fatalf("OpenProcess access = %#x, want %#x", access, wantAccess)
	}
	if !closed {
		t.Fatal("CloseHandle was not called")
	}
	if timesCalled {
		t.Fatal("GetProcessTimes was called for a signaled process")
	}
}

// TestObserveProcessBirthTokenReadsRunningProcessBirth proves an unsignaled process still yields its immutable birth token.
func TestObserveProcessBirthTokenReadsRunningProcessBirth(t *testing.T) {
	const handle windows.Handle = 84
	observer := windowsProcessObserver{
		open: func(uint32, bool, uint32) (windows.Handle, error) {
			return handle, nil
		},
		close: func(windows.Handle) error {
			return nil
		},
		wait: func(windows.Handle, uint32) (uint32, error) {
			return uint32(windows.WAIT_TIMEOUT), nil
		},
		times: func(_ windows.Handle, creation, _ *windows.Filetime, _ *windows.Filetime, _ *windows.Filetime) error {
			creation.HighDateTime = 0x01234567
			creation.LowDateTime = 0x89abcdef
			return nil
		},
	}

	token, present, err := observeProcessBirthTokenWith(100, observer)
	if err != nil {
		t.Fatalf("observeProcessBirthTokenWith() error = %v", err)
	}
	if !present || token != "windows:0123456789abcdef" {
		t.Fatalf("observeProcessBirthTokenWith() = (%q, %t), want running process token", token, present)
	}
}

// TestObserveProcessBirthTokenRejectsWaitFailure keeps restart recovery fail-closed when Windows cannot observe liveness.
func TestObserveProcessBirthTokenRejectsWaitFailure(t *testing.T) {
	wantErr := windows.ERROR_INVALID_HANDLE
	observer := windowsProcessObserver{
		open: func(uint32, bool, uint32) (windows.Handle, error) {
			return windows.Handle(126), nil
		},
		close: func(windows.Handle) error {
			return nil
		},
		wait: func(windows.Handle, uint32) (uint32, error) {
			return windows.WAIT_FAILED, wantErr
		},
		times: func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
			t.Fatal("GetProcessTimes was called after WAIT_FAILED")
			return nil
		},
	}

	_, _, err := observeProcessBirthTokenWith(100, observer)
	if !errors.Is(err, wantErr) {
		t.Fatalf("observeProcessBirthTokenWith() error = %v, want %v", err, wantErr)
	}
}
