//go:build darwin

package projectprocess

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const darwinProcessStateZombie int8 = 5

// validateUnixProcessBirthToken rejects fabricated session markers before an absent leader can authorize enumeration.
func validateUnixProcessBirthToken(birthToken string) error {
	const prefix = "darwin:"
	if !strings.HasPrefix(birthToken, prefix) {
		return fmt.Errorf("Darwin process birth token has an unsupported format")
	}
	remainder := strings.TrimPrefix(birthToken, prefix)
	seconds, microseconds, found := strings.Cut(remainder, ":")
	if !found || seconds == "" || microseconds == "" {
		return fmt.Errorf("Darwin process birth token is incomplete")
	}
	if _, err := strconv.ParseInt(seconds, 10, 64); err != nil {
		return fmt.Errorf("Darwin process birth token seconds: %w", err)
	}
	if _, err := strconv.ParseInt(microseconds, 10, 64); err != nil {
		return fmt.Errorf("Darwin process birth token microseconds: %w", err)
	}
	return nil
}

// unixProcessZombie reports whether one currently present Darwin process has reached the inert zombie state.
func unixProcessZombie(pid int) (bool, bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return false, false, fmt.Errorf("read Darwin process %d state: %w", pid, err)
	}
	if len(processes) == 0 {
		return false, false, nil
	}
	if len(processes) != 1 {
		return false, false, fmt.Errorf("read Darwin process %d state: kernel returned %d records", pid, len(processes))
	}
	return processes[0].Proc.P_stat == darwinProcessStateZombie, true, nil
}

// unixSessionMembers enumerates live Darwin processes whose kernel session ID matches Harbor's launch PID.
func unixSessionMembers(sessionID int) ([]unixProcessMember, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("enumerate Darwin processes: %w", err)
	}
	members := make([]unixProcessMember, 0)
	for _, process := range processes {
		if process.Proc.P_stat == darwinProcessStateZombie {
			continue
		}
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		observedSessionID, err := unix.Getsid(pid)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read Darwin process %d session: %w", pid, err)
		}
		if observedSessionID != sessionID {
			continue
		}
		started := process.Proc.P_starttime
		if started.Sec == 0 && started.Usec == 0 {
			return nil, fmt.Errorf("read Darwin session member %d birth: timestamp is empty", pid)
		}
		members = append(members, unixProcessMember{
			PID:        pid,
			BirthToken: fmt.Sprintf("darwin:%d:%d", started.Sec, started.Usec),
		})
	}
	slices.SortFunc(members, func(left, right unixProcessMember) int {
		return cmp.Compare(left.PID, right.PID)
	})
	return members, nil
}
