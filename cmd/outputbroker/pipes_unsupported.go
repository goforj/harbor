//go:build !darwin && !linux && !windows

package main

import (
	"errors"
	"os"
)

// openInheritedOutputPipe keeps unsupported broker process adoption explicit.
func openInheritedOutputPipe(int, string) (*os.File, error) {
	return nil, errors.New("output broker inherited pipes are unsupported on this platform")
}
