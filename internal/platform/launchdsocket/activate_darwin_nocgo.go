//go:build darwin && !cgo

package launchdsocket

import "os"

// platformActivateSocket fails closed when a macOS build omits the public launchd C API bridge.
func platformActivateSocket(string) ([]*os.File, error) {
	return nil, ErrUnavailable
}
