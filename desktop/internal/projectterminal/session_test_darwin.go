//go:build darwin

package projectterminal

import (
	"errors"
	"syscall"
)

// processExited reports whether macOS no longer exposes pid.
func processExited(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH), nil
}
