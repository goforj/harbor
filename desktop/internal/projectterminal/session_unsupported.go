//go:build !darwin && !linux

package projectterminal

import (
	"errors"
	"os"
)

// ErrUnsupported reports a platform without this Mac-first terminal backend.
var ErrUnsupported = errors.New("project terminal is supported only on macOS and Linux")

// startPlatform returns a clear error without starting a shell on unsupported platforms.
func startPlatform(string, string) (*Session, error) {
	return nil, ErrUnsupported
}

// defaultLoginShell reports that no platform shell is available.
func defaultLoginShell() (string, error) {
	return "", ErrUnsupported
}

// terminate is unnecessary because unsupported platforms cannot create sessions.
func terminate(*os.Process) {}
