//go:build !darwin && !linux && !windows

package machinepaths

import (
	"fmt"
	"runtime"
)

// platformRoot fails closed until the target has a reviewed installer-owned privileged layout.
func platformRoot() (string, error) {
	return "", fmt.Errorf("%w on %s", ErrUnsupported, runtime.GOOS)
}
