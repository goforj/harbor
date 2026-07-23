// Package projectterminal starts and manages an interactive terminal for one project.
package projectterminal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
)

// ErrInvalidSize reports an attempt to resize a terminal to an unusable size.
var ErrInvalidSize = errors.New("terminal size must have at least one row and column")

// Session owns the pseudo-terminal and login shell started for a project.
type Session struct {
	terminal  io.ReadWriteCloser
	resize    func(rows, columns uint16) error
	terminate func()

	terminalCloseOnce sync.Once
	closeOnce         sync.Once
	lifecycleMu       sync.Mutex
	exited            bool
	exitErr           error
	done              chan struct{}
}

// Start opens a login shell in projectDirectory.
//
// projectDirectory must be an existing canonical absolute directory. Requiring
// the canonical spelling prevents a terminal from silently operating in a
// different checkout through a relative path or symlink.
func Start(projectDirectory string) (*Session, error) {
	shell, err := loginShell()
	if err != nil {
		return nil, err
	}

	return startPlatform(projectDirectory, shell)
}

// Read reads terminal output.
func (session *Session) Read(buffer []byte) (int, error) {
	return session.terminal.Read(buffer)
}

// Write sends input to the login shell.
func (session *Session) Write(buffer []byte) (int, error) {
	return session.terminal.Write(buffer)
}

// Resize changes the terminal dimensions measured in character cells.
func (session *Session) Resize(rows, columns uint16) error {
	if rows == 0 || columns == 0 {
		return ErrInvalidSize
	}

	return session.resize(rows, columns)
}

// Done is closed after the login shell exits and terminal resources are closed.
func (session *Session) Done() <-chan struct{} {
	return session.done
}

// Wait waits for the login shell to exit and returns its exit error.
func (session *Session) Wait() error {
	<-session.done
	return session.exitErr
}

// Close closes the terminal and terminates the login shell if it is still running.
//
// Close is idempotent and waits for the shell reaper so a closed session cannot
// retain a child process or file descriptor.
func (session *Session) Close() error {
	session.closeOnce.Do(func() {
		session.lifecycleMu.Lock()
		if !session.exited {
			session.terminate()
		}
		session.lifecycleMu.Unlock()

		session.closeTerminal()
		<-session.done
	})

	return nil
}

// newSession begins reaping command after its pseudo-terminal has been created.
func newSession(
	terminal io.ReadWriteCloser,
	wait func() error,
	resize func(rows, columns uint16) error,
	terminate func(),
) *Session {
	session := &Session{
		terminal:  terminal,
		resize:    resize,
		terminate: terminate,
		done:      make(chan struct{}),
	}

	go func() {
		err := wait()
		session.closeTerminal()

		session.lifecycleMu.Lock()
		session.exitErr = err
		session.exited = true
		close(session.done)
		session.lifecycleMu.Unlock()
	}()

	return session
}

// closeTerminal closes the parent terminal descriptor at most once.
func (session *Session) closeTerminal() {
	session.terminalCloseOnce.Do(func() {
		_ = session.terminal.Close()
	})
}

// openProjectDirectory pins the validated checkout to a directory handle for spawn.
func openProjectDirectory(directory string) (*os.File, error) {
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("project directory must be absolute: %q", directory)
	}

	cleaned := filepath.Clean(directory)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return nil, fmt.Errorf("resolve project directory %q: %w", directory, err)
	}
	if resolved != cleaned {
		return nil, fmt.Errorf("project directory must be canonical: %q", directory)
	}

	before, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat project directory %q: %w", directory, err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("project directory is not a directory: %q", directory)
	}

	handle, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("open project directory %q: %w", directory, err)
	}
	handleInfo, err := handle.Stat()
	if err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("inspect project directory handle %q: %w", directory, err)
	}
	after, err := os.Stat(resolved)
	if err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("recheck project directory %q: %w", directory, err)
	}
	if !os.SameFile(before, handleInfo) || !os.SameFile(handleInfo, after) {
		_ = handle.Close()
		return nil, fmt.Errorf("project directory changed while opening: %q", directory)
	}

	return handle, nil
}

// loginShell resolves the user's account shell without searching PATH.
func loginShell() (string, error) {
	account, err := user.Current()
	if err == nil {
		if shell, found, shellErr := accountLoginShell(account.Username); shellErr != nil {
			return "", shellErr
		} else if found {
			return executableShell(shell, "account login shell")
		}
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		return defaultLoginShell()
	}

	return executableShell(shell, "SHELL")
}

// terminalEnvironment returns only the environment required by an interactive user shell.
func terminalEnvironment(shell string) []string {
	environment := make([]string, 0, 12)
	appendEnvironment := func(name string, value string) {
		if value != "" {
			environment = append(environment, name+"="+value)
		}
	}

	if account, err := user.Current(); err == nil {
		appendEnvironment("HOME", account.HomeDir)
		appendEnvironment("USER", account.Username)
		appendEnvironment("LOGNAME", account.Username)
	}
	if !environmentContains(environment, "HOME") {
		appendEnvironment("HOME", os.Getenv("HOME"))
	}
	appendEnvironment("SHELL", shell)
	appendEnvironment("TERM", "xterm-256color")
	appendEnvironment("COLORTERM", os.Getenv("COLORTERM"))
	appendEnvironment("LANG", os.Getenv("LANG"))
	appendEnvironment("TMPDIR", os.Getenv("TMPDIR"))
	appendEnvironment("PATH", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")

	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(name, "LC_") {
			appendEnvironment(name, value)
		}
	}

	return environment
}

// environmentContains reports whether environment already has name assigned.
func environmentContains(environment []string, name string) bool {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}

	return false
}

// executableShell validates a shell path before it is executed directly.
func executableShell(shell string, source string) (string, error) {
	if !filepath.IsAbs(shell) {
		return "", fmt.Errorf("%s must be an absolute path: %q", source, shell)
	}

	info, err := os.Stat(shell)
	if err != nil {
		return "", fmt.Errorf("stat %s %q: %w", source, shell, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file: %q", source, shell)
	}

	return shell, nil
}
