//go:build darwin || linux

package projectprocess

import "syscall"

// newPriorProcessRecoveryControl binds restart settlement to the birth-checked Unix session created at launch.
func newPriorProcessRecoveryControl() priorProcessRecoveryControl {
	observe := observeProcessBirthToken
	return priorProcessRecoveryControl{
		observe: observe,
		observeScope: func(pid int, birthToken string) (PriorProcessState, error) {
			state, _, err := observePersistedOwnedProcessSession(pid, birthToken)
			return state, err
		},
		graceful: func(pid int, birthToken string) (PriorProcessState, error) {
			rawBirthToken, err := recoverableOwnedUnixSessionBirth(pid, birthToken)
			if err != nil {
				return "", err
			}
			return signalOwnedProcessSession(pid, rawBirthToken, syscall.SIGTERM)
		},
		force: func(pid int, birthToken string) (PriorProcessState, error) {
			rawBirthToken, err := recoverableOwnedUnixSessionBirth(pid, birthToken)
			if err != nil {
				return "", err
			}
			return forceOwnedProcessSession(pid, rawBirthToken)
		},
	}
}
