//go:build linux

package projectterminal

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// linuxProcessIdentity fences a process tree member against PID reuse.
type linuxProcessIdentity struct {
	PID        int
	ParentPID  int
	SessionID  int
	StartTicks string
}

// terminate kills the exact shell and every descendant visible before signaling.
func terminate(process *os.Process, expectedBirth string) {
	if process == nil {
		return
	}

	for range 3 {
		terminateLinuxTree(process.Pid, expectedBirth)
	}
	if current, err := processBirthToken(process.Pid); err == nil && current == expectedBirth {
		if err := syscall.Kill(process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = process.Kill()
		}
	}
}

// processBirthToken returns the kernel start tick for one live Linux PID.
func processBirthToken(pid int) (string, error) {
	identity, err := linuxProcessIdentityForPID(pid)
	if err != nil {
		return "", err
	}
	return identity.StartTicks, nil
}

// terminateLinuxTree signals birth-checked descendants even when they created another session.
func terminateLinuxTree(leaderPID int, expectedBirth string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}

	processes := make(map[int]linuxProcessIdentity)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		identity, err := linuxProcessIdentityForPID(pid)
		if err != nil {
			continue
		}
		processes[pid] = identity
	}
	leader, found := processes[leaderPID]
	if !found || leader.StartTicks != expectedBirth {
		return
	}

	owned := map[int]bool{leaderPID: true}
	for changed := true; changed; {
		changed = false
		for pid, candidate := range processes {
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
		candidate := processes[pid]
		current, err := linuxProcessIdentityForPID(pid)
		if err != nil || current.StartTicks != candidate.StartTicks {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// linuxProcessIdentityForPID reads ownership fields from Linux's proc stat record.
func linuxProcessIdentityForPID(pid int) (linuxProcessIdentity, error) {
	contents, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return linuxProcessIdentity{}, err
	}

	closingName := strings.LastIndex(string(contents), ")")
	if closingName < 0 {
		return linuxProcessIdentity{}, errors.New("malformed proc stat")
	}
	fields := strings.Fields(string(contents)[closingName+1:])
	if len(fields) <= 19 {
		return linuxProcessIdentity{}, errors.New("malformed proc stat")
	}

	parentPID, err := strconv.Atoi(fields[1])
	if err != nil {
		return linuxProcessIdentity{}, fmt.Errorf("parse proc parent PID: %w", err)
	}
	sessionID, err := strconv.Atoi(fields[3])
	if err != nil {
		return linuxProcessIdentity{}, fmt.Errorf("parse proc session ID: %w", err)
	}
	return linuxProcessIdentity{
		PID:        pid,
		ParentPID:  parentPID,
		SessionID:  sessionID,
		StartTicks: fields[19],
	}, nil
}
