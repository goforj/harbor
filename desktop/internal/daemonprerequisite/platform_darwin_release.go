//go:build darwin && release

package daemonprerequisite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/goforj/harbor/internal/platform/installpaths"
)

const daemonLaunchAgentMode = fs.FileMode(0o644)

// darwinEnsurerDependencies isolates the current user, filesystem, and launchctl boundaries.
type darwinEnsurerDependencies struct {
	home       func() (string, error)
	userID     func() int
	run        func(context.Context, ...string) error
	readFile   func(string) ([]byte, error)
	writeAgent func(string, []byte) error
	inspect    func(string) error
}

// darwinEnsurer owns one exact per-user LaunchAgent definition.
type darwinEnsurer struct {
	dependencies darwinEnsurerDependencies
}

// newPlatformEnsurer activates the root-owned daemon launcher only in a release build.
func newPlatformEnsurer() Ensurer {
	return newDarwinEnsurer(darwinEnsurerDependencies{
		home:       os.UserHomeDir,
		userID:     os.Geteuid,
		run:        runLaunchctl,
		readFile:   os.ReadFile,
		writeAgent: writeLaunchAgent,
		inspect:    inspectDaemonLauncher,
	})
}

// newDarwinEnsurer requires complete dependencies because partial launch activation is not recoverable.
func newDarwinEnsurer(dependencies darwinEnsurerDependencies) Ensurer {
	if dependencies.home == nil || dependencies.userID == nil || dependencies.run == nil ||
		dependencies.readFile == nil || dependencies.writeAgent == nil || dependencies.inspect == nil {
		panic("macOS daemon prerequisite requires every dependency")
	}
	return &darwinEnsurer{dependencies: dependencies}
}

// Ensure installs the exact user-owned definition and asks launchd to start its fixed launcher.
func (ensurer *darwinEnsurer) Ensure(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	userID := ensurer.dependencies.userID()
	if userID <= 0 {
		return errors.New("Harbor desktop must run as a non-root macOS user")
	}
	launcher, err := installpaths.DaemonLauncher()
	if err != nil {
		return err
	}
	if err := ensurer.dependencies.inspect(launcher); err != nil {
		return err
	}
	home, err := ensurer.dependencies.home()
	if err != nil {
		return fmt.Errorf("resolve Harbor user home: %w", err)
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return fmt.Errorf("Harbor user home %q is not absolute and canonical", home)
	}
	agentPath := filepath.Join(home, "Library", "LaunchAgents", installpaths.DaemonLaunchAgentLabel+".plist")
	agent := daemonLaunchAgent(launcher)
	current, readErr := ensurer.dependencies.readFile(agentPath)
	switch {
	case readErr == nil && string(current) != string(agent):
		return fmt.Errorf("preserve non-matching Harbor daemon LaunchAgent %q", agentPath)
	case readErr == nil:
	case errors.Is(readErr, os.ErrNotExist):
		if err := ensurer.dependencies.writeAgent(agentPath, agent); err != nil {
			return err
		}
	default:
		return fmt.Errorf("read Harbor daemon LaunchAgent: %w", readErr)
	}

	service := "gui/" + strconv.Itoa(userID) + "/" + installpaths.DaemonLaunchAgentLabel
	if err := ensurer.dependencies.run(ctx, "print", service); err != nil {
		domain := "gui/" + strconv.Itoa(userID)
		if err := ensurer.dependencies.run(ctx, "bootstrap", domain, agentPath); err != nil {
			return fmt.Errorf("bootstrap Harbor daemon LaunchAgent: %w", err)
		}
	}
	if err := ensurer.dependencies.run(ctx, "kickstart", service); err != nil {
		return fmt.Errorf("start Harbor daemon LaunchAgent: %w", err)
	}
	return nil
}

// daemonLaunchAgent renders the fixed user-service contract without caller-selected XML.
func daemonLaunchAgent(launcher string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + installpaths.DaemonLaunchAgentLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + launcher + `</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/Library/Application Support/GoForj/Harbor/current</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Background</string>
  <key>ThrottleInterval</key>
  <integer>5</integer>
</dict>
</plist>
`)
}

// inspectDaemonLauncher admits only the package-owned fixed regular executable.
func inspectDaemonLauncher(path string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect installed Harbor daemon launcher: %w", err)
	}
	if !information.Mode().IsRegular() || information.Mode().Perm()&0o111 == 0 {
		return errors.New("installed Harbor daemon launcher is not a regular executable")
	}
	return nil
}

// writeLaunchAgent creates only the fixed user LaunchAgents parent and atomically publishes one definition.
func writeLaunchAgent(path string, content []byte) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create Harbor LaunchAgents directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".com.goforj.harbor.daemon-")
	if err != nil {
		return fmt.Errorf("stage Harbor daemon LaunchAgent: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(daemonLaunchAgentMode); err != nil {
		return fmt.Errorf("set Harbor daemon LaunchAgent mode: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write Harbor daemon LaunchAgent: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Harbor daemon LaunchAgent: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Harbor daemon LaunchAgent: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("publish Harbor daemon LaunchAgent: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("retire staged Harbor daemon LaunchAgent: %w", err)
	}
	committed = true
	return nil
}

// runLaunchctl executes only fixed launchctl verbs with a stripped environment.
func runLaunchctl(ctx context.Context, arguments ...string) error {
	command := exec.CommandContext(ctx, "/bin/launchctl", arguments...)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(output))
	}
	return nil
}
