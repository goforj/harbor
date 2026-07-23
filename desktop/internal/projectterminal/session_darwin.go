//go:build darwin

package projectterminal

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// terminate kills every process in the private session created for the terminal.
func terminate(process *os.Process) {
	if process == nil {
		return
	}

	for range 3 {
		terminateDarwinSession(process.Pid)
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = process.Kill()
	}
}

// terminateDarwinSession signals all members of the login shell's native session.
func terminateDarwinSession(leaderPID int) {
	leader, err := unix.SysctlKinfoProc("kern.proc.pid", leaderPID)
	if err != nil {
		return
	}

	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return
	}
	for _, candidate := range processes {
		if candidate.Eproc.Sess != leader.Eproc.Sess || candidate.Proc.P_pid <= 0 {
			continue
		}
		_ = syscall.Kill(int(candidate.Proc.P_pid), syscall.SIGKILL)
	}
}
