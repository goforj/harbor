// Package projectterminal starts and manages an interactive terminal for one project.
package projectterminal

import (
	"bufio"
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
	terminal io.ReadWriteCloser
	process  *os.Process
	resize   func(rows, columns uint16) error

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
	directory, err := canonicalProjectDirectory(projectDirectory)
	if err != nil {
		return nil, err
	}

	shell, err := loginShell()
	if err != nil {
		return nil, err
	}

	return startPlatform(directory, shell)
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
		session.closeTerminal()

		session.lifecycleMu.Lock()
		if !session.exited {
			terminate(session.process)
		}
		session.lifecycleMu.Unlock()

		<-session.done
	})

	return nil
}

// newSession begins reaping command after its pseudo-terminal has been created.
func newSession(terminal io.ReadWriteCloser, process *os.Process, wait func() error, resize func(rows, columns uint16) error) *Session {
	session := &Session{
		terminal: terminal,
		process:  process,
		resize:   resize,
		done:     make(chan struct{}),
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

// canonicalProjectDirectory validates a caller's project directory boundary.
func canonicalProjectDirectory(directory string) (string, error) {
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("project directory must be absolute: %q", directory)
	}

	cleaned := filepath.Clean(directory)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve project directory %q: %w", directory, err)
	}
	if resolved != cleaned {
		return "", fmt.Errorf("project directory must be canonical: %q", directory)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat project directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project directory is not a directory: %q", directory)
	}

	return resolved, nil
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

// accountLoginShell returns username's shell from the local account database when present.
func accountLoginShell(username string) (string, bool, error) {
	password, err := os.Open("/etc/passwd")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("open local account database: %w", err)
	}
	defer func() { _ = password.Close() }()

	scanner := bufio.NewScanner(password)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 7 || fields[0] != username {
			continue
		}

		return fields[6], true, nil
	}
	if err := scanner.Err(); err != nil {
		return "", false, fmt.Errorf("read local account database: %w", err)
	}

	return "", false, nil
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
	appendEnvironment("SSH_AUTH_SOCK", os.Getenv("SSH_AUTH_SOCK"))
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
