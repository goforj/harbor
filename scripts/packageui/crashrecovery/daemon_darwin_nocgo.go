//go:build darwin && !cgo

package main

import (
	"context"
	"errors"

	"github.com/goforj/harbor/internal/control"
)

// installedCLI refuses native installed-app testing without executable observation support.
func installedCLI() (string, error) {
	return "", errors.New("native macOS installed-app crash recovery testing requires cgo")
}

// observeInstalledDaemon fails closed when native executable observation is unavailable.
func observeInstalledDaemon(context.Context, control.DaemonPeer) (daemonIdentity, error) {
	return daemonIdentity{}, errors.New("native macOS installed-app crash recovery testing requires cgo")
}

// crashInstalledDaemon sends no signal when native executable observation is unavailable.
func crashInstalledDaemon(context.Context, daemonIdentity) error {
	return errors.New("native macOS installed-app crash recovery testing requires cgo")
}

// ensureInstalledDaemon leaves launchd untouched when native observation is unavailable.
func ensureInstalledDaemon(context.Context) error {
	return errors.New("native macOS installed-app crash recovery testing requires cgo")
}
