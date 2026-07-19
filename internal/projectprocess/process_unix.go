//go:build !windows

package projectprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// platformProcess owns Unix process-group signaling for one launched command.
type platformProcess struct{}

// preparePlatformProcess puts the child in its own process group so descendants share Harbor's stop boundary.
func preparePlatformProcess(command *exec.Cmd) (*platformProcess, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &platformProcess{}, nil
}

// attach captures the kernel birth identity after the process has a stable PID.
func (process *platformProcess) attach(child *os.Process) (string, error) {
	return processBirthToken(child.Pid)
}

// resume is a no-op because Unix process-group ownership is established atomically by the child at launch.
func (process *platformProcess) resume(child *os.Process) error {
	return nil
}

// graceful asks the entire process group to terminate while preserving application cleanup time.
func (process *platformProcess) graceful(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// force kills the entire process group after the bounded graceful period expires.
func (process *platformProcess) force(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// treeAlive reports whether the dedicated process group still contains a root or descendant.
func (process *platformProcess) treeAlive(pid int) (bool, error) {
	err := syscall.Kill(-pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

// close is a no-op because Unix process-group identity does not allocate a Harbor-owned handle.
func (process *platformProcess) close() {}
