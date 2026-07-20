//go:build !windows

package reconcile

import (
	"os/exec"
	"os/signal"
	"syscall"
)

// separateProjectLifecycleHelperProcessGroup reproduces watcher children that move outside the launcher's process group.
func separateProjectLifecycleHelperProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// ignoreProjectLifecycleHelperGracefulStop keeps the descendant listener alive through Harbor's bounded graceful period.
func ignoreProjectLifecycleHelperGracefulStop() {
	signal.Ignore(syscall.SIGTERM)
}
