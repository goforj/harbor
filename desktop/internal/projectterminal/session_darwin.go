//go:build darwin

package projectterminal

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// darwinProcessIdentity fences a process tree member against PID reuse.
type darwinProcessIdentity struct {
	PID       int
	ParentPID int
	SessionID int
	Birth     string
}

// terminate kills the exact shell and every descendant visible before signaling.
func terminate(process *os.Process, expectedBirth string) {
	if process == nil {
		return
	}

	for range 3 {
		terminateDarwinTree(process.Pid, expectedBirth)
	}
	if current, err := processBirthToken(process.Pid); err == nil && current == expectedBirth {
		if err := syscall.Kill(process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = process.Kill()
		}
	}
}

// processBirthToken returns one Darwin process's kernel start timestamp.
func processBirthToken(pid int) (string, error) {
	identity, err := darwinProcessIdentityForPID(pid)
	if err != nil {
		return "", err
	}
	return identity.Birth, nil
}

// terminateDarwinTree signals birth-checked descendants even when they created another session.
func terminateDarwinTree(leaderPID int, expectedBirth string) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return
	}

	observed := make(map[int]darwinProcessIdentity, len(processes))
	for _, process := range processes {
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		observed[pid] = darwinProcessIdentityFromKinfo(process)
	}
	leader, found := observed[leaderPID]
	if !found || leader.Birth != expectedBirth {
		return
	}

	owned := map[int]bool{leaderPID: true}
	for changed := true; changed; {
		changed = false
		for pid, candidate := range observed {
			if owned[pid] {
				continue
			}
			if owned[candidate.ParentPID] || candidate.SessionID == leaderPID {
				owned[pid] = true
				changed = true
			}
		}
	}
	for pid := range owned {
		candidate := observed[pid]
		current, err := darwinProcessIdentityForPID(pid)
		if err != nil || current.Birth != candidate.Birth {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// darwinProcessIdentityForPID reads one exact Darwin process identity.
func darwinProcessIdentityForPID(pid int) (darwinProcessIdentity, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return darwinProcessIdentity{}, err
	}
	if process.Proc.P_pid <= 0 {
		return darwinProcessIdentity{}, fmt.Errorf("Darwin process %d is unavailable", pid)
	}
	return darwinProcessIdentityFromKinfo(*process), nil
}

// darwinProcessIdentityFromKinfo translates one kernel observation without losing its birth timestamp.
func darwinProcessIdentityFromKinfo(process unix.KinfoProc) darwinProcessIdentity {
	started := process.Proc.P_starttime
	return darwinProcessIdentity{
		PID:       int(process.Proc.P_pid),
		ParentPID: int(process.Eproc.Ppid),
		SessionID: int(process.Eproc.Sess),
		Birth:     fmt.Sprintf("%d:%d", started.Sec, started.Usec),
	}
}
