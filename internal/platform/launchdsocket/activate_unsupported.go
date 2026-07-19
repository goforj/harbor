//go:build !darwin

package launchdsocket

import "os"

// platformActivateSocket fails closed on operating systems without launchd socket activation.
func platformActivateSocket(string) ([]*os.File, error) {
	return nil, ErrUnavailable
}
