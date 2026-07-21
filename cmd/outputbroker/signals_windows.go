//go:build windows

package main

import "os"

// terminationSignals includes the portable interrupt boundary for Windows broker workers.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
