//go:build darwin || linux

package projectterminal

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/creack/pty"
)

// startPlatform starts the configured shell with a controlling pseudo-terminal.
func startPlatform(directory string, shell string) (*Session, error) {
	directoryHandle, err := openProjectDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = directoryHandle.Close()
	}()

	command := exec.Command(shell, "-l")
	command.Dir = directory
	command.Env = terminalEnvironment(shell)

	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		return nil, fmt.Errorf("start login shell %q: %w", shell, err)
	}
	if err := verifyProcessDirectory(command.Process.Pid, directoryHandle); err != nil {
		_ = terminal.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("verify login shell project directory: %w", err)
	}
	birthToken, err := processBirthToken(command.Process.Pid)
	if err != nil {
		_ = terminal.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("capture login shell identity: %w", err)
	}

	return newSession(
		terminal,
		command.Wait,
		func(rows, columns uint16) error {
			return pty.Setsize(terminal, &pty.Winsize{Rows: rows, Cols: columns})
		},
		func() {
			terminate(command.Process, birthToken)
		},
	), nil
}

// defaultLoginShell chooses the standard shell only when SHELL is unavailable.
func defaultLoginShell() (string, error) {
	if runtime.GOOS == "darwin" {
		return executableShell("/bin/zsh", "default login shell")
	}

	return executableShell("/bin/sh", "default login shell")
}
