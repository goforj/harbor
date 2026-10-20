//go:build windows && release

package daemonprerequisite

import (
	"context"
	"errors"
	"time"

	"github.com/goforj/harbor/internal/platform/windowsinstallation"
)

// windowsEnsurer activates only the immutable installer-owned task after effective token admission.
type windowsEnsurer struct {
	inspect func() error
	start   func(context.Context) error
}

// newPlatformEnsurer leaves registration and repair exclusively with the elevated admitted installer.
func newPlatformEnsurer() Ensurer {
	return &windowsEnsurer{inspect: func() error { _, err := windowsinstallation.InspectCurrentToken(); return err }, start: startWindowsInstalledTask}
}

// Ensure refuses elevated desktop execution before observing or activating machine-owned installation state.
func (ensurer *windowsEnsurer) Ensure(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensurer.inspect(); err != nil {
		return err
	}
	return ensurer.start(ctx)
}

// startWindowsInstalledTask retains every selected file while the signed task policy admits and starts its fixed action.
func startWindowsInstalledTask(ctx context.Context) (result error) {
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	return installation.Task(ctx, "start")
}
