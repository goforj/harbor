//go:build !linux && !darwin && !windows

package projectprocess

import "fmt"

// processBirthToken rejects unsupported kernels because a PID alone cannot safely authorize later stop actions.
func processBirthToken(pid int) (string, error) {
	return "", fmt.Errorf("process birth evidence is unsupported on this operating system")
}
