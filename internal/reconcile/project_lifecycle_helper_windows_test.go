//go:build windows

package reconcile

import (
	"os"
	"os/exec"
	"os/signal"
)

// separateProjectLifecycleHelperProcessGroup is unnecessary because the supervising Job Object crosses process groups.
func separateProjectLifecycleHelperProcessGroup(command *exec.Cmd) {}

// ignoreProjectLifecycleHelperGracefulStop keeps the portable helper compatible with the Unix regression mode.
func ignoreProjectLifecycleHelperGracefulStop() {
	signal.Ignore(os.Interrupt)
}
