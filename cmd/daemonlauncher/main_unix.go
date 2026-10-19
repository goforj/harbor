//go:build darwin || linux

// Package main starts Harbor's installed daemon independently of the desktop lifetime.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/goforj/harbor/internal/platform/daemonservice"
)

// main replaces the stable launcher with the daemon from the selected complete release.
func main() {
	daemon, err := resolveInstalledDaemon()
	if err == nil {
		var arguments []string
		arguments, err = prepareDaemonLaunch(daemon, os.Args[1:], runDaemonMigration)
		if err == nil {
			err = syscall.Exec(daemon, arguments, os.Environ())
		}
	}
	if errors.Is(err, daemonservice.ErrDaemonActive) {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start installed Harbor daemon: %v\n", err)
		os.Exit(1)
	}
}

// prepareDaemonLaunch applies embedded migrations only for the implicit installed-service launch.
func prepareDaemonLaunch(daemon string, arguments []string, migrate func(string) error) ([]string, error) {
	if len(arguments) == 0 {
		if err := migrate(daemon); err != nil {
			return nil, fmt.Errorf("prepare installed Harbor database: %w", err)
		}
	}
	return daemonArguments(daemon, arguments), nil
}

// runDaemonMigration uses the selected release as the same unprivileged user before it owns the daemon endpoint.
func runDaemonMigration(daemon string) error {
	return daemonservice.PrepareDatabase(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, daemon, "migrate")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	})
}

// daemonArguments starts the per-user service by default while preserving explicit maintenance operations.
func daemonArguments(daemon string, arguments []string) []string {
	if len(arguments) == 0 {
		arguments = []string{"--foreground"}
	}
	return append([]string{daemon}, arguments...)
}
