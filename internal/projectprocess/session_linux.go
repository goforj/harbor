//go:build linux

package projectprocess

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// validateUnixProcessBirthToken rejects fabricated session markers before an absent leader can authorize enumeration.
func validateUnixProcessBirthToken(birthToken string) error {
	const prefix = "linux:"
	if !strings.HasPrefix(birthToken, prefix) {
		return fmt.Errorf("Linux process birth token has an unsupported format")
	}
	remainder := strings.TrimPrefix(birthToken, prefix)
	bootID, startTime, found := strings.Cut(remainder, ":")
	if !found || bootID == "" || startTime == "" {
		return fmt.Errorf("Linux process birth token is incomplete")
	}
	if _, err := strconv.ParseUint(startTime, 10, 64); err != nil {
		return fmt.Errorf("Linux process birth token start time: %w", err)
	}
	return nil
}

// unixProcessZombie reports whether one currently present Linux process has reached the inert zombie state.
func unixProcessZombie(pid int) (bool, bool, error) {
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read Linux process %d state: %w", pid, err)
	}
	state, _, err := linuxProcessStatStateAndSession(pid, contents)
	if err != nil {
		return false, false, err
	}
	return state == "Z", true, nil
}

// unixSessionMembers enumerates live Linux processes whose kernel session ID matches Harbor's launch PID.
func unixSessionMembers(sessionID int) ([]unixProcessMember, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("enumerate Linux processes: %w", err)
	}
	members := make([]unixProcessMember, 0)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read Linux process %d session: %w", pid, err)
		}
		state, observedSessionID, err := linuxProcessStatStateAndSession(pid, contents)
		if err != nil {
			return nil, err
		}
		if observedSessionID != sessionID || state == "Z" {
			continue
		}
		birthToken, err := processBirthToken(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read Linux session member %d birth: %w", pid, err)
		}
		members = append(members, unixProcessMember{PID: pid, BirthToken: birthToken})
	}
	slices.SortFunc(members, func(left, right unixProcessMember) int {
		return cmp.Compare(left.PID, right.PID)
	})
	return members, nil
}

// linuxProcessStatStateAndSession extracts the process state and session identity from one procfs record.
func linuxProcessStatStateAndSession(pid int, contents []byte) (string, int, error) {
	closingParenthesis := strings.LastIndexByte(string(contents), ')')
	if closingParenthesis < 0 || closingParenthesis+2 >= len(contents) {
		return "", 0, fmt.Errorf("read Linux process %d session: malformed process record", pid)
	}
	fields := strings.Fields(string(contents[closingParenthesis+2:]))
	const sessionIndex = 3
	if len(fields) <= sessionIndex {
		return "", 0, fmt.Errorf("read Linux process %d session: session ID is missing", pid)
	}
	observedSessionID, err := strconv.Atoi(fields[sessionIndex])
	if err != nil {
		return "", 0, fmt.Errorf("read Linux process %d session ID: %w", pid, err)
	}
	return fields[0], observedSessionID, nil
}
