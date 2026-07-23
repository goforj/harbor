//go:build linux

package projectterminal

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// terminate kills every process in the private session created for the terminal.
func terminate(process *os.Process) {
	if process == nil {
		return
	}

	for range 3 {
		terminateLinuxSession(process.Pid)
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = process.Kill()
	}
}

// terminateLinuxSession signals each process whose kernel session ID matches leaderPID.
func terminateLinuxSession(leaderPID int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		sessionID, err := linuxSessionID(pid)
		if err != nil || sessionID != leaderPID {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// linuxSessionID reads the session field from Linux's proc stat record.
func linuxSessionID(pid int) (int, error) {
	contents, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}

	closingName := strings.LastIndex(string(contents), ")")
	if closingName < 0 {
		return 0, errors.New("malformed proc stat")
	}
	fields := strings.Fields(string(contents)[closingName+1:])
	if len(fields) < 4 {
		return 0, errors.New("malformed proc stat")
	}

	return strconv.Atoi(fields[3])
}
