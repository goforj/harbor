//go:build !windows

package projectprocess

import (
	"os/exec"
	"syscall"
)

// separateHelperProcessGroup reproduces watcher behavior that creates a process group outside the launcher's group.
func separateHelperProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
