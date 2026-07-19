//go:build darwin

package projectprocess

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// observeProcessBirthToken distinguishes a missing process from an observation failure.
func observeProcessBirthToken(pid int) (string, bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return "", false, err
	}
	if len(processes) == 0 {
		return "", false, nil
	}
	if len(processes) != 1 {
		return "", false, fmt.Errorf("read process creation time: kernel returned %d records", len(processes))
	}
	started := processes[0].Proc.P_starttime
	if started.Sec == 0 && started.Usec == 0 {
		return "", false, fmt.Errorf("read process creation time: timestamp is empty")
	}
	return fmt.Sprintf("darwin:%d:%d", started.Sec, started.Usec), true, nil
}

// processBirthToken uses the kernel's process creation timestamp to distinguish reused PIDs.
func processBirthToken(pid int) (string, error) {
	token, present, err := observeProcessBirthToken(pid)
	if err != nil {
		return "", fmt.Errorf("read process creation time: %w", err)
	}
	if !present {
		return "", fmt.Errorf("read process creation time: %w", unix.ESRCH)
	}
	return token, nil
}
