//go:build !darwin

package launchdsocket

import "os"

// inspectPlatformSocket fails closed because launchd descriptors are meaningful only on macOS.
func inspectPlatformSocket(*os.File) (socketObservation, error) {
	return socketObservation{}, ErrUnavailable
}
