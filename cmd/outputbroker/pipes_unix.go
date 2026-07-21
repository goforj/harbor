//go:build darwin || linux

package main

import (
	"fmt"
	"os"
)

// openInheritedOutputPipe adopts one descriptor passed by the Harbor supervisor.
func openInheritedOutputPipe(descriptor int, name string) (*os.File, error) {
	file := os.NewFile(uintptr(descriptor), "harbor-output-broker-"+name)
	if file == nil {
		return nil, fmt.Errorf("open inherited output %s descriptor %d: descriptor is invalid", name, descriptor)
	}
	return file, nil
}
