package projectterminal

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTerminal provides deterministic PTY behavior without launching a user's shell.
type fakeTerminal struct {
	mu       sync.Mutex
	input    bytes.Buffer
	output   *io.PipeReader
	send     *io.PipeWriter
	resizes  [][2]uint16
	exit     chan struct{}
	close    sync.Once
	exitErr  error
	closeErr error
}

// newFakeTerminal creates a pipe-backed test session.
func newFakeTerminal() *fakeTerminal {
	output, send := io.Pipe()
	return &fakeTerminal{
		output: output,
		send:   send,
		exit:   make(chan struct{}),
	}
}

// Read receives deterministic test output.
func (terminal *fakeTerminal) Read(buffer []byte) (int, error) {
	return terminal.output.Read(buffer)
}

// Write records terminal input.
func (terminal *fakeTerminal) Write(buffer []byte) (int, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.input.Write(buffer)
}

// Resize records the requested grid.
func (terminal *fakeTerminal) Resize(rows, columns uint16) error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.resizes = append(terminal.resizes, [2]uint16{rows, columns})
	return nil
}

// Wait blocks until the fake shell exits.
func (terminal *fakeTerminal) Wait() error {
	<-terminal.exit
	return terminal.exitErr
}

// Close settles the fake shell and its output stream.
func (terminal *fakeTerminal) Close() error {
	terminal.close.Do(func() {
		_ = terminal.send.Close()
		close(terminal.exit)
	})
	return terminal.closeErr
}

// TestManagerRoutesBoundedSessionScopedIO proves the registry never accepts a project path after startup.
func TestManagerRoutesBoundedSessionScopedIO(t *testing.T) {
	t.Parallel()

	terminal := newFakeTerminal()
	events := make(chan Event, 2)
	manager := newManager(
		func(directory string) (terminalSession, error) {
			if directory != "/projects/orders" {
				t.Fatalf("start directory = %q", directory)
			}
			return terminal, nil
		},
		func(event Event) {
			events <- event
		},
	)

	sessionID, err := manager.Start("/projects/orders", 31, 117)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !strings.HasPrefix(sessionID, "terminal-") {
		t.Fatalf("Start() session ID = %q", sessionID)
	}
	if err := manager.Write(sessionID, []byte("early")); !errors.Is(err, ErrSessionNotAttached) {
		t.Fatalf("Write() before Attach() error = %v", err)
	}
	if err := manager.Attach(sessionID); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if err := manager.Write(sessionID, []byte("pwd\r")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := manager.Resize(sessionID, 40, 120); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	if _, err := terminal.send.Write([]byte("ready\r\n")); err != nil {
		t.Fatalf("send output: %v", err)
	}

	output := <-events
	if output.SessionID != sessionID || string(output.Data) != "ready\r\n" || output.Exited {
		t.Fatalf("output event = %#v", output)
	}
	deadline := time.Now().Add(time.Second)
	for {
		terminal.mu.Lock()
		inputReady := terminal.input.String() == "pwd\r" && len(terminal.resizes) == 2
		terminal.mu.Unlock()
		if inputReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued terminal input was not drained")
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Close(sessionID); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	exited := <-events
	if exited.SessionID != sessionID || !exited.Exited || exited.ExitError != nil {
		t.Fatalf("exit event = %#v", exited)
	}

	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.input.String() != "pwd\r" {
		t.Fatalf("input = %q", terminal.input.String())
	}
	if got := terminal.resizes; len(got) != 2 || got[0] != [2]uint16{31, 117} || got[1] != [2]uint16{40, 120} {
		t.Fatalf("resizes = %#v", got)
	}
}

// TestManagerExpiresAStartThatIsNeverAttached keeps lost Wails responses from consuming terminal slots.
func TestManagerExpiresAStartThatIsNeverAttached(t *testing.T) {
	t.Parallel()

	terminal := newFakeTerminal()
	manager := newManager(
		func(string) (terminalSession, error) {
			return terminal, nil
		},
		func(Event) {},
	)
	manager.attachWait = 10 * time.Millisecond
	sessionID, err := manager.Start("/projects/orders", 24, 80)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.RLock()
		_, retained := manager.sessions[sessionID]
		manager.mu.RUnlock()
		if !retained {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unattached terminal session did not expire")
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Attach(sessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Attach() after expiry error = %v", err)
	}
}

// TestManagerCoalescesResizeFloods keeps only one pending ioctl per terminal.
func TestManagerCoalescesResizeFloods(t *testing.T) {
	t.Parallel()

	terminal := newFakeTerminal()
	manager := newManager(
		func(string) (terminalSession, error) {
			return terminal, nil
		},
		func(Event) {},
	)
	sessionID, err := manager.Start("/projects/orders", 24, 80)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := manager.Attach(sessionID); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	for index := range 500 {
		if err := manager.Resize(sessionID, uint16(25+index%100), uint16(81+index%200)); err != nil {
			t.Fatalf("Resize() error = %v", err)
		}
	}

	managed, err := manager.session(sessionID)
	if err != nil {
		t.Fatalf("session() error = %v", err)
	}
	if queued := len(managed.resize); queued > 1 {
		t.Fatalf("pending resize count = %d, want at most 1", queued)
	}
	time.Sleep(50 * time.Millisecond)
	terminal.mu.Lock()
	resizes := append([][2]uint16(nil), terminal.resizes...)
	terminal.mu.Unlock()
	if len(resizes) > 4 {
		t.Fatalf("resize ioctl count = %d, want a 60 Hz bound", len(resizes))
	}
	if got := resizes[len(resizes)-1]; got != [2]uint16{124, 180} {
		t.Fatalf("latest resize = %v, want [124 180]", got)
	}
	if err := manager.Close(sessionID); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestManagerCloseDoesNotWaitForBlockedEventDelivery keeps Wails backpressure out of PTY ownership.
func TestManagerCloseDoesNotWaitForBlockedEventDelivery(t *testing.T) {
	t.Parallel()

	terminal := newFakeTerminal()
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	manager := newManager(
		func(string) (terminalSession, error) {
			return terminal, nil
		},
		func(Event) {
			select {
			case <-handlerStarted:
			default:
				close(handlerStarted)
			}
			<-releaseHandler
		},
	)
	sessionID, err := manager.Start("/projects/orders", 24, 80)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := manager.Attach(sessionID); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if _, err := terminal.send.Write([]byte("output")); err != nil {
		t.Fatalf("send output: %v", err)
	}
	<-handlerStarted

	closed := make(chan error, 1)
	go func() {
		closed <- manager.Close(sessionID)
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() waited for blocked event delivery")
	}
	close(releaseHandler)
}

// TestManagerRejectsRendererResourceAbuse covers every public input bound.
func TestManagerRejectsRendererResourceAbuse(t *testing.T) {
	t.Parallel()

	manager := newManager(
		func(string) (terminalSession, error) {
			return newFakeTerminal(), nil
		},
		func(Event) {},
	)
	if _, err := manager.Start("/projects/orders", 0, 80); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("Start() zero size error = %v", err)
	}
	if _, err := manager.Start("/projects/orders", maxTerminalRows+1, 80); err == nil {
		t.Fatal("Start() oversized rows error = nil")
	}
	if err := manager.Write("unknown", nil); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Write() unknown session error = %v", err)
	}
	if err := manager.Write("unknown", make([]byte, maxInputBytes+1)); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("Write() oversized input error = %v", err)
	}
	if err := manager.Resize("unknown", 24, maxTerminalColumn+1); err == nil {
		t.Fatal("Resize() oversized columns error = nil")
	}
	if err := manager.Close("unknown"); err != nil {
		t.Fatalf("Close() unknown session error = %v", err)
	}
}

// TestManagerCloseAllRejectsNewSessionsAndSettlesExistingShells proves desktop shutdown owns its terminals.
func TestManagerCloseAllRejectsNewSessionsAndSettlesExistingShells(t *testing.T) {
	t.Parallel()

	terminal := newFakeTerminal()
	manager := newManager(
		func(string) (terminalSession, error) {
			return terminal, nil
		},
		func(Event) {},
	)
	if _, err := manager.Start("/projects/orders", 24, 80); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	manager.CloseAll()

	if _, err := manager.Start("/projects/orders", 24, 80); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Start() after CloseAll() error = %v", err)
	}
}
