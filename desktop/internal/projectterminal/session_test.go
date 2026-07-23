//go:build darwin || linux

package projectterminal

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStartRejectsNonCanonicalProjectDirectories keeps terminal sessions inside
// the exact checkout boundary supplied by their caller.
func TestStartRejectsNonCanonicalProjectDirectories(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	file := filepath.Join(t.TempDir(), "project-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for _, projectDirectory := range []string{".", link, file} {
		_, err := Start(projectDirectory)
		if err == nil {
			t.Fatalf("Start(%q) error = nil, want canonical-directory error", projectDirectory)
		}
	}
}

// TestLoginShellRejectsUnsafeShellPaths avoids resolving shell names through PATH.
func TestLoginShellRejectsUnsafeShellPaths(t *testing.T) {
	for _, shell := range []string{"sh", t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		if _, err := executableShell(shell, "SHELL"); err == nil {
			t.Fatalf("executableShell(%q) error = nil, want error", shell)
		}
	}
}

// TestTerminalEnvironmentExcludesHarborState retains only ordinary terminal variables.
func TestTerminalEnvironmentExcludesHarborState(t *testing.T) {
	t.Setenv("HARBOR_SESSION_TICKET", "secret")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/injected.dylib")
	t.Setenv("MANAGED_SESSION_TOKEN", "secret")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_CTYPE", "UTF-8")
	t.Setenv("TERM", "unsafe")

	environment := environmentMap(terminalEnvironment("/bin/sh"))
	if environment["SHELL"] != "/bin/sh" {
		t.Fatalf("SHELL = %q, want /bin/sh", environment["SHELL"])
	}
	if environment["TERM"] != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", environment["TERM"])
	}
	if environment["PATH"] != "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" {
		t.Fatalf("PATH = %q, want fixed terminal path", environment["PATH"])
	}
	for _, name := range []string{"HARBOR_SESSION_TICKET", "DYLD_INSERT_LIBRARIES", "MANAGED_SESSION_TOKEN", "SSH_AUTH_SOCK"} {
		if _, found := environment[name]; found {
			t.Fatalf("terminal environment unexpectedly includes %s", name)
		}
	}
}

// TestSessionCarriesInputOutputSizeAndExit verifies the interactive terminal contract.
func TestSessionCarriesInputOutputSizeAndExit(t *testing.T) {
	directory := t.TempDir()
	session, err := startPlatform(directory, "/bin/sh")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	if err := session.Resize(31, 97); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	if _, err := io.WriteString(session, "stty size; printf 'cwd=%s\\n' \"$PWD\"; exit\n"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}

	output, err := readUntilTerminalClose(session)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Contains(output, []byte("31 97")) {
		t.Fatalf("terminal size output %q does not contain %q", output, "31 97")
	}
	if !bytes.Contains(output, []byte("cwd="+directory)) {
		t.Fatalf("terminal output %q does not contain project directory %q", output, directory)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}

// TestSessionUsesProjectDirectory verifies the login shell begins in the exact caller directory.
func TestSessionUsesProjectDirectory(t *testing.T) {
	directory := t.TempDir()
	session, err := startPlatform(directory, "/bin/sh")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	if _, err := io.WriteString(session, "printf 'cwd=%s\\n' \"$PWD\"; exit\n"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	output, err := readUntilTerminalClose(session)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Contains(output, []byte("cwd="+directory)) {
		t.Fatalf("terminal output %q does not contain project directory %q", output, directory)
	}
}

// TestSessionCloseIsIdempotent waits for the shell reaper on every Close call.
func TestSessionCloseIsIdempotent(t *testing.T) {
	session, err := startPlatform(t.TempDir(), "/bin/sh")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	const closerCount = 8
	closed := make(chan error, closerCount)
	var closers sync.WaitGroup
	closers.Add(closerCount)
	for range closerCount {
		go func() {
			defer closers.Done()
			closed <- session.Close()
		}()
	}
	go func() {
		closers.Wait()
		close(closed)
	}()

	select {
	case err, open := <-closed:
		if !open {
			t.Fatal("concurrent Close() calls produced no result")
		}
		if err != nil {
			t.Fatalf("concurrent Close() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Close() did not finish")
	}
	for err := range closed {
		if err != nil {
			t.Fatalf("concurrent Close() error = %v", err)
		}
	}

	if err := session.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() was not closed after Close()")
	}
}

// TestResizeRejectsEmptyDimensions protects the terminal ioctl from invalid sizes.
func TestResizeRejectsEmptyDimensions(t *testing.T) {
	session, err := startPlatform(t.TempDir(), "/bin/sh")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	if err := session.Resize(0, 80); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("Resize(0, 80) error = %v, want ErrInvalidSize", err)
	}
	if err := session.Resize(24, 0); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("Resize(24, 0) error = %v, want ErrInvalidSize", err)
	}
}

// TestSessionCloseTerminatesTheTerminalProcessGroup keeps background jobs bounded.
func TestSessionCloseTerminatesTheTerminalProcessGroup(t *testing.T) {
	session, err := startPlatform(t.TempDir(), "/bin/sh")
	if err != nil {
		t.Fatalf("startPlatform() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	if _, err := io.WriteString(session, "sleep 30 & printf 'child=%s\\n' \"$!\"\n"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}

	childPID := readChildPID(t, session)
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitForProcessExit(t, childPID)
}

// readUntilTerminalClose accepts the EIO that Unix PTYs report after their child exits.
func readUntilTerminalClose(session *Session) ([]byte, error) {
	output, err := io.ReadAll(session)
	if err != nil && !errors.Is(err, os.ErrClosed) && !strings.Contains(err.Error(), "input/output error") {
		return nil, err
	}

	return output, nil
}

// environmentMap turns command environment entries into values addressed by name.
func environmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}

	return values
}

// readChildPID observes the background job PID written by the test shell.
func readChildPID(t *testing.T, session *Session) int {
	t.Helper()

	lines := make(chan string, 4)
	go func() {
		reader := bufio.NewReader(session)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	var output strings.Builder
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatal("terminal closed before reporting the background child PID")
			}
			output.WriteString(line)
			line = strings.TrimSpace(line)
			if marker := strings.Index(line, "child="); marker >= 0 {
				pidText := strings.Fields(strings.TrimPrefix(line[marker:], "child="))
				if len(pidText) == 0 || pidText[0][0] < '0' || pidText[0][0] > '9' {
					continue
				}
				pid, err := strconv.Atoi(pidText[0])
				if err != nil {
					t.Fatalf("parse child PID from %q: %v", line, err)
				}
				return pid
			}
		case <-timeout.C:
			t.Fatalf("terminal did not report the background child PID; output = %q", output.String())
		}
	}
}

// waitForProcessExit allows init to reap a killed background child before asserting teardown.
func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		exited, err := processExited(pid)
		if err != nil {
			t.Fatalf("observe background child PID %d: %v", pid, err)
		}
		if exited {
			return
		}

		select {
		case <-timeout.C:
			t.Fatalf("background child PID %d remains live after Close()", pid)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
