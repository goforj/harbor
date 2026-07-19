//go:build windows

package projectprocess

import (
	"errors"
	"fmt"
	"math"

	"golang.org/x/sys/windows"
)

// observeProcessBirthToken distinguishes a missing process from an observation failure.
func observeProcessBirthToken(pid int) (string, bool, error) {
	token, err := processBirthToken(pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// processBirthToken reads the same immutable creation timestamp captured when Harbor launched the process.
func processBirthToken(pid int) (string, error) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return "", fmt.Errorf("process PID %d is outside the Windows process identifier range", pid)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	var creation windows.Filetime
	var exit windows.Filetime
	var kernel windows.Filetime
	var user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("windows:%016x", uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime)), nil
}
