//go:build !darwin && !linux && !windows

package main

import "os"

// terminationSignals keeps unsupported broker workers free from invented native signal semantics.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
