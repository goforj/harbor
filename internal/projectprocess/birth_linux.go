//go:build linux

package projectprocess

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processBirthToken combines the boot identity and kernel start tick so PID reuse cannot match another process.
func processBirthToken(pid int) (string, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", fmt.Errorf("read process stat: %w", err)
	}
	closingParenthesis := strings.LastIndexByte(string(stat), ')')
	if closingParenthesis < 0 || closingParenthesis+2 >= len(stat) {
		return "", fmt.Errorf("read process stat: malformed process record")
	}
	fields := strings.Fields(string(stat[closingParenthesis+2:]))
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return "", fmt.Errorf("read process stat: start time is missing")
	}
	if _, err := strconv.ParseUint(fields[startTimeIndex], 10, 64); err != nil {
		return "", fmt.Errorf("read process stat start time: %w", err)
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read kernel boot identity: %w", err)
	}
	boot := strings.TrimSpace(string(bootID))
	if boot == "" {
		return "", fmt.Errorf("read kernel boot identity: identity is empty")
	}
	return fmt.Sprintf("linux:%s:%s", boot, fields[startTimeIndex]), nil
}
