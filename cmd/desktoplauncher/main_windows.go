//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/goforj/harbor/internal/platform/windowsinstallation"
)

// main admits the installed desktop before creating its restricted native process and private job.
func main() {
	code, err := runDesktopLauncher(os.Args[1:], launchInstalledDesktop)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Harbor desktop launcher: %v\n", err)
		os.Exit(1)
	}
	os.Exit(int(code))
}

// runDesktopLauncher rejects arguments before installation admission or GUI activation.
func runDesktopLauncher(arguments []string, activate func() (uint32, error)) (uint32, error) {
	if len(arguments) != 0 {
		return 0, errors.New("installed desktop launcher accepts no arguments")
	}
	return activate()
}

// launchInstalledDesktop retains the installation and child receipt through the complete GUI lifetime.
func launchInstalledDesktop() (code uint32, result error) {
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return 0, fmt.Errorf("admit Windows installation: %w", err)
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	child, err := installation.Start("desktop", nil)
	if err != nil {
		return 0, fmt.Errorf("start installed desktop: %w", err)
	}
	defer func() { result = errors.Join(result, child.Close()) }()
	code, err = child.Wait(context.Background())
	if err != nil {
		return code, fmt.Errorf("wait for installed desktop: %w", err)
	}
	return code, nil
}
