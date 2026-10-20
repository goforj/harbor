//go:build windows

// Package main installs Harbor's sealed development package at its fixed Windows machine location.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/platform/windowsinstallation"
	"golang.org/x/sys/windows"
)

// payload contains only build-sealed signed components, with no downloaded or caller-selected executable inputs.
//
//go:embed all:payload
var payload embed.FS

// main runs only same-user admitted setup after Windows requests explicit administrator consent for this signed executable.
func main() {
	silent := len(os.Args) == 2 && os.Args[1] == "--silent"
	err := runSetup(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Harbor setup:", err)
	}
	if !silent {
		message, flags := "Harbor is installed. Open GoForj Harbor from the Start menu.", uintptr(0x40)
		if err != nil {
			message, flags = "Harbor setup could not complete: "+err.Error(), 0x10
		}
		text, textErr := windows.UTF16PtrFromString(message)
		if textErr == nil {
			_, _, _ = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("GoForj Harbor setup"))), flags)
		}
	}
	if err != nil {
		os.Exit(1)
	}
}

// runSetup accepts no installation path, principal, command or trust-store overrides.
func runSetup(arguments []string) (result error) {
	if err := admitSetupArguments(arguments); err != nil {
		return err
	}
	sealed, err := fs.Sub(payload, "payload")
	if err != nil {
		return err
	}
	installation, err := windowsinstallation.Install(sealed)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, installation.Close()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	agent, err := installation.Path("installer")
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, agent, "--complete-install")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("complete admitted machine installation: %w: %.4096s", err, output)
	}
	return nil
}

// admitSetupArguments keeps privilege requests bound to a fixed installation without caller-selected paths or identities.
func admitSetupArguments(arguments []string) error {
	if len(arguments) != 0 && !(len(arguments) == 1 && arguments[0] == "--silent") {
		return errors.New("setup accepts only the optional --silent argument")
	}
	return nil
}
