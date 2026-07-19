//go:build darwin

package projectprocess

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processBirthToken uses the kernel's process creation timestamp to distinguish reused PIDs.
func processBirthToken(pid int) (string, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", fmt.Errorf("read process creation time: %w", err)
	}
	started := process.Proc.P_starttime
	if started.Sec == 0 && started.Usec == 0 {
		return "", fmt.Errorf("read process creation time: timestamp is empty")
	}
	return fmt.Sprintf("darwin:%d:%d", started.Sec, started.Usec), nil
}
