//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/goforj/harbor/internal/platform/installpaths"
)

// main replaces the stable launcher with the daemon from the selected complete release.
func main() {
	daemon, err := resolveInstalledDaemon()
	if err == nil {
		err = syscall.Exec(daemon, daemonArguments(daemon, os.Args[1:]), os.Environ())
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start installed Harbor daemon: %v\n", err)
		os.Exit(1)
	}
}

// daemonArguments preserves daemon subcommands while replacing only the launcher's process identity.
func daemonArguments(daemon string, arguments []string) []string {
	return append([]string{daemon}, arguments...)
}

// resolveInstalledDaemon admits only one regular executable beneath the selected release directory.
func resolveInstalledDaemon() (string, error) {
	root, err := installpaths.MachineRoot()
	if err != nil {
		return "", err
	}
	current, err := installpaths.CurrentRelease()
	if err != nil {
		return "", err
	}
	return resolveInstalledDaemonAt(root, current)
}

// resolveInstalledDaemonAt keeps the fixed-path admission rules independently testable.
func resolveInstalledDaemonAt(root string, current string) (string, error) {
	currentInformation, err := os.Lstat(current)
	if err != nil {
		return "", fmt.Errorf("inspect current Harbor release: %w", err)
	}
	if currentInformation.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("current Harbor release is not a symbolic link")
	}
	selected, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("resolve current Harbor release: %w", err)
	}
	selected = filepath.Clean(selected)
	releases := filepath.Join(root, "releases")
	relative, err := filepath.Rel(releases, selected)
	if err != nil {
		return "", fmt.Errorf("relate selected Harbor release: %w", err)
	}
	if relative == "." || relative == "" || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) || strings.Contains(relative, string(filepath.Separator)) {
		return "", fmt.Errorf("selected Harbor release %q is outside the versioned release root", selected)
	}
	daemon := filepath.Join(selected, "bin", "harbord")
	information, err := os.Lstat(daemon)
	if err != nil {
		return "", fmt.Errorf("inspect selected Harbor daemon: %w", err)
	}
	if !information.Mode().IsRegular() || information.Mode().Perm()&0o111 == 0 {
		return "", errors.New("selected Harbor daemon is not a regular executable")
	}
	return daemon, nil
}
