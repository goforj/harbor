//go:build !windows

package projectprocess

import (
	"os"
	"os/exec"
	"syscall"
)

const ownedUnixSessionBirthTokenPrefix = "harbor-unix-session-v1:"

// platformProcess owns the immutable Unix session boundary for one launched command.
type platformProcess struct {
	birthToken string
}

// preparePlatformProcess creates a session boundary that watcher-created process groups cannot escape.
func preparePlatformProcess(command *exec.Cmd) (*platformProcess, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return &platformProcess{}, nil
}

// attach captures the kernel birth identity after the process has a stable PID.
func (process *platformProcess) attach(child *os.Process) (string, error) {
	birthToken, err := processBirthToken(child.Pid)
	if err != nil {
		return "", err
	}
	process.birthToken = birthToken
	return ownedUnixSessionBirthTokenPrefix + birthToken, nil
}

// resume is a no-op because Unix process-group ownership is established atomically by the child at launch.
func (process *platformProcess) resume(child *os.Process) error {
	return nil
}

// graceful asks every exact member of the owned session to terminate while preserving application cleanup time.
func (process *platformProcess) graceful(pid int) error {
	_, err := signalOwnedProcessSession(pid, process.birthToken, syscall.SIGTERM)
	return err
}

// force kills every exact member that remains in the owned session after the bounded graceful period expires.
func (process *platformProcess) force(pid int) error {
	if process.birthToken == "" {
		return forceUnattachedProcessSession(pid)
	}
	_, err := forceOwnedProcessSession(pid, process.birthToken)
	return err
}

// treeAlive reports whether the dedicated session still contains a live root or descendant.
func (process *platformProcess) treeAlive(pid int) (bool, error) {
	state, _, err := observeOwnedProcessSession(pid, process.birthToken)
	return state == PriorProcessPresent, err
}

// close is a no-op because Unix process-group identity does not allocate a Harbor-owned handle.
func (process *platformProcess) close() {}
