//go:build !linux && !darwin && !windows

package projectprocess

import "fmt"

// observeProcessBirthToken rejects unsupported kernels because absence cannot be distinguished safely.
func observeProcessBirthToken(pid int) (string, bool, error) {
	return "", false, fmt.Errorf("observe process %d: process birth evidence is unsupported on this operating system", pid)
}

// processBirthToken rejects unsupported kernels because a PID alone cannot safely authorize later stop actions.
func processBirthToken(pid int) (string, error) {
	return "", fmt.Errorf("process birth evidence is unsupported on this operating system")
}
