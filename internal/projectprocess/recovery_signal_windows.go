//go:build windows

package projectprocess

import (
	"errors"

	"golang.org/x/sys/windows"
)

// newPriorProcessRecoveryControl binds restart settlement to birth-checked Windows process actions.
func newPriorProcessRecoveryControl() priorProcessRecoveryControl {
	observe := observeProcessBirthToken
	return priorProcessRecoveryControl{
		observe: observe,
		graceful: func(pid int, birthToken string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, birthToken, observe, signalPriorWindowsProcessGroup)
		},
		force: forceExactPriorWindowsProcess,
	}
}

// signalPriorWindowsProcessGroup requests cleanup from the independent console process group created by Harbor.
func signalPriorWindowsProcessGroup(pid int) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}

// forceExactPriorWindowsProcess terminates through the same handle used to revalidate the immutable creation time.
func forceExactPriorWindowsProcess(pid int, expectedBirth string) (PriorProcessState, error) {
	if err := validateWindowsPID(pid); err != nil {
		return "", err
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return PriorProcessAbsent, nil
	}
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return "", err
	}
	if event == windows.WAIT_OBJECT_0 {
		return PriorProcessAbsent, nil
	}
	if event != uint32(windows.WAIT_TIMEOUT) {
		return "", errors.New("observe prior Windows process before force: unexpected wait result")
	}
	birthToken, err := processBirthTokenFromHandle(handle, windows.GetProcessTimes)
	if err != nil {
		return "", err
	}
	if birthToken != expectedBirth {
		return PriorProcessReplaced, nil
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return PriorProcessPresent, err
	}
	return PriorProcessPresent, nil
}
