//go:build windows

package main

import "os"

// terminationSignals keeps unsupported cross-builds free of Unix signal assumptions.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
