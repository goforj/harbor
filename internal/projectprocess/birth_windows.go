//go:build windows

package projectprocess

import (
	"errors"
	"fmt"
	"math"

	"golang.org/x/sys/windows"
)

type windowsProcessObserver struct {
	open  func(uint32, bool, uint32) (windows.Handle, error)
	close func(windows.Handle) error
	wait  func(windows.Handle, uint32) (uint32, error)
	times func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error
}

// observeProcessBirthToken distinguishes a missing process from an observation failure.
func observeProcessBirthToken(pid int) (string, bool, error) {
	return observeProcessBirthTokenWith(pid, windowsProcessObserver{
		open:  windows.OpenProcess,
		close: windows.CloseHandle,
		wait:  windows.WaitForSingleObject,
		times: windows.GetProcessTimes,
	})
}

// observeProcessBirthTokenWith verifies liveness before trusting a Windows process object's creation timestamp.
func observeProcessBirthTokenWith(pid int, observer windowsProcessObserver) (string, bool, error) {
	if err := validateWindowsPID(pid); err != nil {
		return "", false, err
	}
	handle, err := observer.open(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer observer.close(handle)

	event, err := observer.wait(handle, 0)
	switch event {
	case windows.WAIT_OBJECT_0:
		if err != nil {
			return "", false, fmt.Errorf("wait for prior process: %w", err)
		}
		return "", false, nil
	case uint32(windows.WAIT_TIMEOUT):
		if err != nil {
			return "", false, fmt.Errorf("wait for prior process: %w", err)
		}
	case windows.WAIT_FAILED:
		if err == nil {
			return "", false, fmt.Errorf("wait for prior process: Windows returned WAIT_FAILED without an error")
		}
		return "", false, fmt.Errorf("wait for prior process: %w", err)
	default:
		if err != nil {
			return "", false, fmt.Errorf("wait for prior process: %w", err)
		}
		return "", false, fmt.Errorf("wait for prior process: unexpected result %#x", event)
	}

	token, err := processBirthTokenFromHandle(handle, observer.times)
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// processBirthToken reads the same immutable creation timestamp captured when Harbor launched the process.
func processBirthToken(pid int) (string, error) {
	if err := validateWindowsPID(pid); err != nil {
		return "", err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	return processBirthTokenFromHandle(handle, windows.GetProcessTimes)
}

// processBirthTokenFromHandle reads the immutable creation timestamp from an already-authorized process handle.
func processBirthTokenFromHandle(
	handle windows.Handle,
	getProcessTimes func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error,
) (string, error) {
	var creation windows.Filetime
	var exit windows.Filetime
	var kernel windows.Filetime
	var user windows.Filetime
	if err := getProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("windows:%016x", uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime)), nil
}

// validateWindowsPID keeps process identifiers within the range accepted by OpenProcess.
func validateWindowsPID(pid int) error {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return fmt.Errorf("process PID %d is outside the Windows process identifier range", pid)
	}
	return nil
}
