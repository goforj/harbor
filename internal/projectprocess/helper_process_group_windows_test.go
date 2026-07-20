//go:build windows

package projectprocess

import "os/exec"

// separateHelperProcessGroup is unnecessary because Windows Job Objects already cross console process groups.
func separateHelperProcessGroup(command *exec.Cmd) {}
