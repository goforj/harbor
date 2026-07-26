//go:build !darwin

package installpaths

import (
	"errors"
	"runtime"
)

// platformMachineRoot keeps unimplemented package layouts explicit.
func platformMachineRoot() (string, error) {
	return "", errors.New("Harbor product installation paths are not implemented on " + runtime.GOOS)
}
