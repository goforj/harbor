//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/goforj/harbor/internal/platform/daemonservice"
	"github.com/goforj/harbor/internal/platform/windowsinstallation"
	"golang.org/x/sys/windows"
)

// launchedChild retains lifecycle authority over one admitted native product child.
type launchedChild interface {
	Wait(context.Context) (uint32, error)
	Close() error
}

// launcherOperations keeps orchestration testable without opening user data or starting native fixtures.
type launcherOperations struct {
	inspect func() error
	admit   func() error
	close   func() error
	start   func(string, []string) (launchedChild, error)
	prepare func(func() error) error
}

// main keeps the scheduler entrypoint outside user data until its actual restricted worker is admitted.
func main() {
	code, err := runDaemonLauncher(os.Args[1:], nativeLauncherOperations())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Harbor daemon launcher: %v\n", err)
		os.Exit(1)
	}
	os.Exit(int(code))
}

// nativeLauncherOperations binds every child to one retained fixed installation.
func nativeLauncherOperations() launcherOperations {
	var installation *windowsinstallation.Installation
	return launcherOperations{
		inspect: func() error {
			_, err := windowsinstallation.InspectCurrentToken()
			return err
		},
		admit: func() error {
			var err error
			installation, err = windowsinstallation.Admit()
			if err != nil {
				return err
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err == nil {
				err = admitInstalledUser(installation.Selection().UserSID, user.User.Sid.String())
			}
			if err != nil {
				return errors.Join(err, installation.Close())
			}
			return nil
		},
		close: func() error { return installation.Close() },
		start: func(role string, arguments []string) (launchedChild, error) {
			return installation.Start(role, arguments)
		},
		prepare: daemonservice.PrepareDatabase,
	}
}

// runDaemonLauncher migrates only under the singleton fence and starts a foreground daemon after releasing it.
func runDaemonLauncher(arguments []string, operations launcherOperations) (code uint32, result error) {
	if len(arguments) > 1 || (len(arguments) == 1 && arguments[0] != "--installed-worker" && arguments[0] != "--verify-stopped") {
		return 0, errors.New("expected no arguments, --installed-worker, or --verify-stopped")
	}
	worker := len(arguments) == 1 && arguments[0] == "--installed-worker"
	verify := len(arguments) == 1 && arguments[0] == "--verify-stopped"
	demote := !worker && !verify
	if worker || verify {
		if err := operations.inspect(); err != nil {
			if worker {
				return 0, fmt.Errorf("admit installed worker authority: %w", err)
			}
			demote = true
		}
	}
	if err := operations.admit(); err != nil {
		return 0, fmt.Errorf("admit Windows installation: %w", err)
	}
	defer func() { result = errors.Join(result, operations.close()) }()
	if demote {
		argument := "--installed-worker"
		if verify {
			argument = "--verify-stopped"
		}
		return waitForLaunchedChild(context.Background(), operations.start, "daemon-launcher", []string{argument})
	}
	if verify {
		if err := operations.prepare(func() error { return nil }); err != nil {
			return 0, fmt.Errorf("verify installed daemon stopped: %w", err)
		}
		return 0, nil
	}
	err := operations.prepare(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		code, err := waitForLaunchedChild(ctx, operations.start, "daemon", []string{"migrate"})
		if err != nil {
			return fmt.Errorf("migrate installed daemon: %w", err)
		}
		if code != 0 {
			return fmt.Errorf("installed daemon migration exited with code %d", code)
		}
		return nil
	})
	if errors.Is(err, daemonservice.ErrDaemonActive) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("prepare installed daemon database: %w", err)
	}
	return waitForLaunchedChild(context.Background(), operations.start, "daemon", []string{"--foreground"})
}

// waitForLaunchedChild keeps the native process receipt until that exact child settles, including cancellation.
func waitForLaunchedChild(ctx context.Context, start func(string, []string) (launchedChild, error), role string, arguments []string) (code uint32, result error) {
	child, err := start(role, arguments)
	if err != nil {
		return 0, fmt.Errorf("start installed %s: %w", role, err)
	}
	defer func() { result = errors.Join(result, child.Close()) }()
	code, err = child.Wait(ctx)
	if err != nil {
		return code, fmt.Errorf("wait for installed %s: %w", role, err)
	}
	return code, nil
}

// admitInstalledUser fences direct worker entry before PrepareDatabase can open another account's runtime.
func admitInstalledUser(selected, current string) error {
	if selected == "" || current != selected {
		return errors.New("installed daemon worker user differs from the selected installation")
	}
	return nil
}
