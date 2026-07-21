//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

// terminationSignals includes the normal process termination boundary for Unix broker workers.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
