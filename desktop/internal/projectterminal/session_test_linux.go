//go:build linux

package projectterminal

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// processExited reports whether the kernel has ended pid, including unreaped zombies.
func processExited(pid int) (bool, error) {
	contents, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	fields := strings.Fields(string(contents))
	if len(fields) < 3 {
		return false, errors.New("malformed proc stat")
	}

	return fields[2] == "Z", nil
}
