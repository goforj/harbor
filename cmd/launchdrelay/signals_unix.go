//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

// terminationSignals includes the launchd stop signal and interactive development interruption.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
