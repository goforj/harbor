//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/goforj/harbor/internal/platform/windowsinstallation"
)

// main forwards CLI arguments and the selected child's native exit status without shell interpretation.
func main() {
	code, err := runCLILauncher(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Harbor CLI launcher: %v\n", err)
		os.Exit(1)
	}
	os.Exit(int(code))
}

// runCLILauncher retains the installation and native child while Start inherits only the three admitted CLI handles.
func runCLILauncher(arguments []string) (code uint32, result error) {
	installation, err := windowsinstallation.Admit()
	if err != nil {
		return 0, fmt.Errorf("admit Windows installation: %w", err)
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	child, err := installation.Start("cli", arguments)
	if err != nil {
		return 0, fmt.Errorf("start installed CLI: %w", err)
	}
	defer func() { result = errors.Join(result, child.Close()) }()
	code, err = child.Wait(context.Background())
	if err != nil {
		return code, fmt.Errorf("wait for installed CLI: %w", err)
	}
	return code, nil
}
