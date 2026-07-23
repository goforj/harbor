package projectterminal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	maxSessions       = 8
	maxInputBytes     = 64 * 1024
	maxTerminalRows   = 512
	maxTerminalColumn = 1024
	outputFrameBytes  = 32 * 1024
	sessionIDBytes    = 16
	inputQueueFrames  = 32
	eventQueueFrames  = 256
	defaultAttachWait = 10 * time.Second
	resizeInterval    = time.Second / 60
)

var (
	// ErrManagerClosed reports an operation attempted after terminal ownership shut down.
	ErrManagerClosed = errors.New("project terminal manager is closed")
	// ErrSessionLimit reports that the desktop already owns its maximum number of terminals.
	ErrSessionLimit = errors.New("project terminal session limit reached")
	// ErrSessionNotFound reports an unknown or expired opaque terminal identity.
	ErrSessionNotFound = errors.New("project terminal session was not found")
	// ErrInputTooLarge reports a terminal input frame larger than the bounded bridge contract.
	ErrInputTooLarge = errors.New("project terminal input exceeds 64 KiB")
	// ErrInputQueueFull reports that a terminal is not consuming input fast enough.
	ErrInputQueueFull = errors.New("project terminal input queue is full")
	// ErrSessionNotAttached reports input or resize sent before the renderer attached to output.
	ErrSessionNotAttached = errors.New("project terminal session is not attached")
)

// Event reports output or exit from one desktop-owned terminal session.
type Event struct {
	SessionID string
	Data      []byte
	Exited    bool
	ExitError error
	Dropped   bool
}

// EventHandler consumes ordered output and exit events from terminal sessions.
type EventHandler func(Event)

// terminalSession is the process and PTY surface owned by Manager.
type terminalSession interface {
	io.Reader
	io.Writer
	Resize(rows, columns uint16) error
	Wait() error
	Close() error
}

// sessionStarter opens one terminal in an already-authorized project directory.
type sessionStarter func(string) (terminalSession, error)

// managedSession serializes input and records whether its exit was user-requested.
type managedSession struct {
	session     terminalSession
	mu          sync.Mutex
	resizeMu    sync.Mutex
	closing     bool
	attached    bool
	input       chan []byte
	resize      chan [2]uint16
	stop        chan struct{}
	stopOnce    sync.Once
	done        chan struct{}
	attachTimer *time.Timer
}

// Manager owns the bounded set of interactive terminals created by the desktop.
type Manager struct {
	mu         sync.RWMutex
	sessions   map[string]*managedSession
	starting   int
	closed     bool
	startWG    sync.WaitGroup
	start      sessionStarter
	emit       EventHandler
	eventMu    sync.Mutex
	events     chan Event
	attachWait time.Duration
}

// NewManager creates a desktop-local terminal owner.
func NewManager(emit EventHandler) *Manager {
	return newManager(func(directory string) (terminalSession, error) {
		return Start(directory)
	}, emit)
}

// newManager creates a terminal owner with a replaceable process boundary for tests.
func newManager(start sessionStarter, emit EventHandler) *Manager {
	manager := &Manager{
		sessions:   make(map[string]*managedSession),
		start:      start,
		emit:       emit,
		events:     make(chan Event, eventQueueFrames),
		attachWait: defaultAttachWait,
	}
	go manager.dispatch()
	return manager
}

// Start opens one terminal in projectDirectory with an initial character grid.
func (manager *Manager) Start(projectDirectory string, rows, columns uint16) (string, error) {
	if err := validateSize(rows, columns); err != nil {
		return "", err
	}

	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return "", ErrManagerClosed
	}
	if len(manager.sessions)+manager.starting >= maxSessions {
		manager.mu.Unlock()
		return "", ErrSessionLimit
	}
	manager.starting++
	manager.startWG.Add(1)
	manager.mu.Unlock()
	defer func() {
		manager.mu.Lock()
		manager.starting--
		manager.mu.Unlock()
		manager.startWG.Done()
	}()

	session, err := manager.start(projectDirectory)
	if err != nil {
		return "", err
	}
	if err := session.Resize(rows, columns); err != nil {
		_ = session.Close()
		return "", fmt.Errorf("set initial project terminal size: %w", err)
	}

	sessionID, err := newSessionID()
	if err != nil {
		_ = session.Close()
		return "", err
	}
	managed := &managedSession{
		session: session,
		input:   make(chan []byte, inputQueueFrames),
		resize:  make(chan [2]uint16, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		_ = session.Close()
		return "", ErrManagerClosed
	}
	manager.sessions[sessionID] = managed
	managed.attachTimer = time.AfterFunc(manager.attachWait, func() {
		manager.expireUnattached(sessionID, managed)
	})
	return sessionID, nil
}

// Attach starts output delivery only after the renderer knows the opaque session identity.
func (manager *Manager) Attach(sessionID string) error {
	managed, err := manager.session(sessionID)
	if err != nil {
		return err
	}
	manager.attach(sessionID, managed)
	return nil
}

// Write sends one bounded input frame to a terminal.
func (manager *Manager) Write(sessionID string, data []byte) error {
	if len(data) > maxInputBytes {
		return ErrInputTooLarge
	}
	managed, err := manager.session(sessionID)
	if err != nil {
		return err
	}

	managed.mu.Lock()
	attached := managed.attached
	closing := managed.closing
	managed.mu.Unlock()
	if !attached {
		return ErrSessionNotAttached
	}
	if closing || len(data) == 0 {
		return nil
	}

	input := append([]byte(nil), data...)
	select {
	case managed.input <- input:
		return nil
	default:
		return ErrInputQueueFull
	}
}

// Resize changes one terminal's character grid.
func (manager *Manager) Resize(sessionID string, rows, columns uint16) error {
	if err := validateSize(rows, columns); err != nil {
		return err
	}
	managed, err := manager.session(sessionID)
	if err != nil {
		return err
	}
	managed.mu.Lock()
	attached := managed.attached
	closing := managed.closing
	managed.mu.Unlock()
	if !attached {
		return ErrSessionNotAttached
	}
	if closing {
		return nil
	}
	managed.resizeMu.Lock()
	defer managed.resizeMu.Unlock()
	size := [2]uint16{rows, columns}
	select {
	case managed.resize <- size:
	default:
		select {
		case <-managed.resize:
		default:
		}
		managed.resize <- size
	}
	return nil
}

// Close terminates one exact terminal session and waits for its output pump to settle.
func (manager *Manager) Close(sessionID string) error {
	managed, err := manager.session(sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrManagerClosed) {
			return nil
		}
		return err
	}
	managed.mu.Lock()
	managed.closing = true
	if managed.attachTimer != nil {
		managed.attachTimer.Stop()
	}
	managed.mu.Unlock()
	manager.attach(sessionID, managed)
	if err := managed.session.Close(); err != nil {
		return fmt.Errorf("close project terminal: %w", err)
	}
	<-managed.done
	return nil
}

// CloseAll rejects new terminals and settles every session currently owned by the desktop.
func (manager *Manager) CloseAll() {
	manager.mu.Lock()
	manager.closed = true
	sessions := make(map[string]*managedSession, len(manager.sessions))
	for sessionID, managed := range manager.sessions {
		sessions[sessionID] = managed
		managed.mu.Lock()
		managed.closing = true
		if managed.attachTimer != nil {
			managed.attachTimer.Stop()
		}
		managed.mu.Unlock()
	}
	manager.mu.Unlock()

	for sessionID, managed := range sessions {
		manager.attach(sessionID, managed)
		_ = managed.session.Close()
		<-managed.done
	}
	manager.startWG.Wait()
}

// session resolves an opaque ID without exposing process identities to callers.
func (manager *Manager) session(sessionID string) (*managedSession, error) {
	if sessionID == "" {
		return nil, ErrSessionNotFound
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return nil, ErrManagerClosed
	}
	managed := manager.sessions[sessionID]
	if managed == nil {
		return nil, ErrSessionNotFound
	}
	return managed, nil
}

// pump preserves PTY byte order and removes the session only after its shell exits.
func (manager *Manager) attach(sessionID string, managed *managedSession) {
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.attached {
		return
	}
	managed.attached = true
	if managed.attachTimer != nil {
		managed.attachTimer.Stop()
	}
	go manager.pump(sessionID, managed)
}

// expireUnattached reaps a shell whose renderer never claimed its start response.
func (manager *Manager) expireUnattached(sessionID string, managed *managedSession) {
	manager.mu.RLock()
	current := manager.sessions[sessionID]
	manager.mu.RUnlock()
	if current != managed {
		return
	}

	managed.mu.Lock()
	if managed.attached {
		managed.mu.Unlock()
		return
	}
	managed.closing = true
	managed.attached = true
	managed.mu.Unlock()
	go manager.pump(sessionID, managed)
	_ = managed.session.Close()
}

// pump preserves PTY byte order and removes the session only after its shell exits.
func (manager *Manager) pump(sessionID string, managed *managedSession) {
	defer close(managed.done)

	inputDone := make(chan struct{})
	resizeDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		manager.pumpInput(managed)
	}()
	go func() {
		defer close(resizeDone)
		manager.pumpResize(managed)
	}()

	buffer := make([]byte, outputFrameBytes)
	dropped := false
	for {
		count, err := managed.session.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			if manager.publish(Event{SessionID: sessionID, Data: data, Dropped: dropped}) {
				dropped = false
			} else {
				dropped = true
			}
		}
		if err != nil {
			break
		}
	}
	exitErr := managed.session.Wait()
	managed.stopOnce.Do(func() {
		close(managed.stop)
	})
	<-inputDone
	<-resizeDone

	manager.mu.Lock()
	if manager.sessions[sessionID] == managed {
		delete(manager.sessions, sessionID)
	}
	manager.mu.Unlock()

	managed.mu.Lock()
	closing := managed.closing
	managed.mu.Unlock()
	if closing {
		exitErr = nil
	}
	manager.publish(Event{
		SessionID: sessionID,
		Exited:    true,
		ExitError: exitErr,
		Dropped:   dropped,
	})
}

// pumpResize coalesces renderer floods to one pending terminal ioctl.
func (manager *Manager) pumpResize(managed *managedSession) {
	var lastResize time.Time
	for {
		var size [2]uint16
		select {
		case <-managed.stop:
			return
		case size = <-managed.resize:
		}
		wait := time.Until(lastResize.Add(resizeInterval))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-managed.stop:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		select {
		case size = <-managed.resize:
		default:
		}
		if err := managed.session.Resize(size[0], size[1]); err != nil {
			_ = managed.session.Close()
			return
		}
		lastResize = time.Now()
	}
}

// pumpInput drains a bounded queue so renderer calls cannot block on PTY backpressure.
func (manager *Manager) pumpInput(managed *managedSession) {
	for {
		select {
		case <-managed.stop:
			return
		case data := <-managed.input:
			for len(data) > 0 {
				written, err := managed.session.Write(data)
				if err != nil || written == 0 {
					_ = managed.session.Close()
					return
				}
				data = data[written:]
			}
		}
	}
}

// publish reserves event capacity for terminal exits and drops excess output explicitly.
func (manager *Manager) publish(event Event) bool {
	manager.eventMu.Lock()
	defer manager.eventMu.Unlock()
	if !event.Exited && len(manager.events) >= cap(manager.events)-maxSessions {
		return false
	}
	select {
	case manager.events <- event:
		return true
	default:
		return false
	}
}

// dispatch isolates PTY draining and shutdown from a slow Wails event sink.
func (manager *Manager) dispatch() {
	for event := range manager.events {
		manager.emit(event)
	}
}

// validateSize bounds PTY allocation independently from renderer-controlled dimensions.
func validateSize(rows, columns uint16) error {
	if rows == 0 || columns == 0 {
		return ErrInvalidSize
	}
	if rows > maxTerminalRows || columns > maxTerminalColumn {
		return fmt.Errorf(
			"terminal size exceeds %d rows by %d columns",
			maxTerminalRows,
			maxTerminalColumn,
		)
	}
	return nil
}

// newSessionID creates an unguessable desktop-process-local terminal identity.
func newSessionID() (string, error) {
	var random [sessionIDBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create project terminal session ID: %w", err)
	}
	return "terminal-" + hex.EncodeToString(random[:]), nil
}
