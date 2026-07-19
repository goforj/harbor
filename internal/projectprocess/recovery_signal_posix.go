//go:build darwin || linux

package projectprocess

import (
	"errors"
	"syscall"
)

// newPriorProcessRecoveryControl binds restart settlement to birth-checked Unix process-group signals.
func newPriorProcessRecoveryControl() priorProcessRecoveryControl {
	observe := observeProcessBirthToken
	return priorProcessRecoveryControl{
		observe: observe,
		graceful: func(pid int, birthToken string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, birthToken, observe, func(pid int) error {
				return signalPriorProcessGroup(pid, syscall.SIGTERM)
			})
		},
		force: func(pid int, birthToken string) (PriorProcessState, error) {
			return signalPriorProcessIfExact(pid, birthToken, observe, func(pid int) error {
				return signalPriorProcessGroup(pid, syscall.SIGKILL)
			})
		},
	}
}

// signalPriorProcessGroup targets the dedicated process group created for every Harbor-owned development process.
func signalPriorProcessGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
