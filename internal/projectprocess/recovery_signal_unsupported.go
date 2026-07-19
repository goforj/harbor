//go:build !darwin && !linux && !windows

package projectprocess

import "fmt"

// newPriorProcessRecoveryControl fails closed where exact process-birth signaling is unsupported.
func newPriorProcessRecoveryControl() priorProcessRecoveryControl {
	unsupported := func(pid int, birthToken string) (PriorProcessState, error) {
		return "", fmt.Errorf("settle process %d: process recovery signaling is unsupported on this operating system", pid)
	}
	return priorProcessRecoveryControl{
		observe:  observeProcessBirthToken,
		graceful: unsupported,
		force:    unsupported,
	}
}
