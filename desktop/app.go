package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	desktopReconnectDelay = time.Second
	desktopPollInterval   = 2 * time.Second
	connectionEventName   = desktopwire.ConnectionEventName
	snapshotEventName     = desktopwire.SnapshotEventName
)

var errDaemonDisconnected = errors.New("Harbor daemon is not connected")

// controlClient is the narrow daemon surface the desktop keeps alive between Wails calls.
type controlClient interface {
	Status(context.Context) (control.DaemonStatus, error)
	Snapshot(context.Context) (domain.Snapshot, error)
	RegisterProject(context.Context, control.RegisterProjectRequest) (control.ProjectRegistration, error)
	Done() <-chan struct{}
	Close() error
}

// clientFactory opens one authenticated desktop-role control session.
type clientFactory func(context.Context, control.ClientConfig) (controlClient, error)

// eventEmitter keeps the replaceable raw Wails callback aligned with the typed desktop event boundary.
type eventEmitter = desktopwire.RawEmitter

// resourceOpener delegates a reviewed resource URL to the operating system browser.
type resourceOpener func(context.Context, string)

// directoryChooser keeps the native folder picker deterministic in adapter tests.
type directoryChooser func(context.Context, runtime.OpenDialogOptions) (string, error)

// windowRestorer raises the existing Wails window after the single-instance lock rejects a second process.
type windowRestorer func(context.Context)

// waitFunc makes reconnect and polling boundaries cancellation-aware and deterministic in tests.
type waitFunc func(context.Context, time.Duration) bool

// ConnectionState identifies the desktop backend's current relationship to harbord.
type ConnectionState = desktopwire.ConnectionState

const (
	// ConnectionConnecting means the desktop is opening or negotiating a daemon session.
	ConnectionConnecting ConnectionState = desktopwire.ConnectionConnecting
	// ConnectionConnected means the desktop owns a negotiated daemon session.
	ConnectionConnected ConnectionState = desktopwire.ConnectionConnected
	// ConnectionDisconnected means the last connection attempt or live session ended.
	ConnectionDisconnected ConnectionState = desktopwire.ConnectionDisconnected
)

// ConnectionEvent reports connection lifecycle independently from durable snapshot revisions.
type ConnectionEvent = desktopwire.ConnectionEvent

// snapshotCursor suppresses duplicate revisions only within one negotiated daemon connection.
type snapshotCursor struct {
	sequence    domain.Sequence
	initialized bool
}

// App owns the Wails lifecycle and the desktop's single persistent daemon connection.
type App struct {
	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	client         controlClient
	clientFactory  clientFactory
	events         desktopwire.Emitter
	open           resourceOpener
	choose         directoryChooser
	restore        windowRestorer
	wait           waitFunc
	reconnectDelay time.Duration
	pollInterval   time.Duration
}

// App must remain exactly compatible with the Go-owned Wails method contract.
var _ desktopwire.AppContract = (*App)(nil)

// NewApp creates a desktop client wired to Harbor's local control transport and the Wails runtime.
func NewApp() *App {
	return newApp(newDesktopClient, runtime.EventsEmit, runtime.BrowserOpenURL, waitForContext)
}

// newApp keeps operating-system and timing effects replaceable without making them optional in production.
func newApp(factory clientFactory, emit eventEmitter, open resourceOpener, wait waitFunc) *App {
	return &App{
		clientFactory:  factory,
		events:         desktopwire.NewEmitter(emit),
		open:           open,
		choose:         runtime.OpenDirectoryDialog,
		restore:        runtime.Show,
		wait:           wait,
		reconnectDelay: desktopReconnectDelay,
		pollInterval:   desktopPollInterval,
	}
}

// AddProject lets the operating system choose a directory before asking the connected daemon to register it.
func (a *App) AddProject() (desktopwire.AddProjectResult, error) {
	ctx, client, err := a.currentConnection()
	if err != nil {
		return desktopwire.AddProjectResult{}, err
	}

	path, err := a.choose(ctx, runtime.OpenDialogOptions{
		Title:                "Add a GoForj project",
		ResolvesAliases:      true,
		CanCreateDirectories: false,
	})
	if err != nil {
		return desktopwire.AddProjectResult{}, fmt.Errorf("choose GoForj project directory: %w", err)
	}
	if path == "" {
		return desktopwire.AddProjectResult{Canceled: true}, nil
	}

	registration, err := client.RegisterProject(ctx, control.RegisterProjectRequest{Path: path})
	if err != nil {
		return desktopwire.AddProjectResult{}, fmt.Errorf("register GoForj project: %w", err)
	}
	result := desktopwire.AddProjectResult{Registration: &registration}
	if err := result.Validate(); err != nil {
		return desktopwire.AddProjectResult{}, fmt.Errorf("validate project registration: %w", err)
	}
	return result, nil
}

// newDesktopClient adapts the concrete control client to the desktop's narrow lifecycle interface.
func newDesktopClient(ctx context.Context, config control.ClientConfig) (controlClient, error) {
	return control.NewClient(ctx, config)
}

// startup starts background connection ownership only after Wails supplies its runtime context.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		panic("desktop app started more than once")
	}
	runContext, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel
	a.done = make(chan struct{})
	done := a.done
	a.mu.Unlock()

	go a.run(runContext, done)
}

// shutdown cancels background calls, closes the live transport, and waits until the owner goroutine exits.
func (a *App) shutdown(_ context.Context) {
	a.mu.Lock()
	cancel := a.cancel
	client := a.client
	done := a.done
	a.ctx = nil
	a.cancel = nil
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if client != nil {
		_ = client.Close()
	}
	if done != nil {
		<-done
	}
}

// onSecondInstanceLaunch restores the existing client because a second desktop must not become another runtime authority.
func (a *App) onSecondInstanceLaunch(_ options.SecondInstanceData) {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		return
	}
	a.restore(ctx)
}

// Status returns the diagnostic reported by the currently authenticated daemon connection.
func (a *App) Status() (control.DaemonStatus, error) {
	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.DaemonStatus{}, err
	}

	status, err := client.Status(ctx)
	if err != nil {
		return control.DaemonStatus{}, fmt.Errorf("read Harbor daemon status: %w", err)
	}

	return status, nil
}

// Snapshot returns a fresh complete replacement of desktop-visible Harbor state.
func (a *App) Snapshot() (domain.Snapshot, error) {
	ctx, client, err := a.currentConnection()
	if err != nil {
		return domain.Snapshot{}, err
	}

	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("read Harbor snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return domain.Snapshot{}, fmt.Errorf("validate Harbor snapshot: %w", err)
	}

	return snapshot, nil
}

// OpenResource resolves a project-scoped resource from fresh daemon state before opening its reviewed URL.
func (a *App) OpenResource(projectID string, resourceID string) error {
	typedProjectID := domain.ProjectID(projectID)
	if err := typedProjectID.Validate(); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	typedResourceID := domain.ResourceID(resourceID)
	if err := typedResourceID.Validate(); err != nil {
		return fmt.Errorf("resource: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return err
	}
	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read Harbor snapshot: %w", err)
	}
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("validate Harbor snapshot: %w", err)
	}

	resource, err := findResource(snapshot, typedProjectID, typedResourceID)
	if err != nil {
		return err
	}

	a.open(ctx, resource.URL)
	return nil
}

// run owns reconnection so neither Wails calls nor the presentation layer can accidentally create competing sessions.
func (a *App) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	for {
		if ctx.Err() != nil {
			return
		}
		a.emitConnection(ctx, ConnectionConnecting)

		client, err := a.clientFactory(ctx, control.ClientConfig{Role: rpc.RoleDesktop})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.emitConnection(ctx, ConnectionDisconnected)
			if !a.wait(ctx, a.reconnectDelay) {
				return
			}
			continue
		}
		if client == nil {
			panic("desktop client factory returned a nil client")
		}

		if !a.installClient(ctx, client) {
			_ = client.Close()
			return
		}
		a.emitConnection(ctx, ConnectionConnected)
		a.poll(ctx, client)
		a.removeClient(client)
		_ = client.Close()

		if ctx.Err() != nil {
			return
		}
		a.emitConnection(ctx, ConnectionDisconnected)
		if !a.wait(ctx, a.reconnectDelay) {
			return
		}
	}
}

// poll emits only validated, monotonically ordered replacement snapshots while one connection remains usable.
func (a *App) poll(ctx context.Context, client controlClient) {
	cursor := snapshotCursor{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-client.Done():
			return
		default:
		}

		snapshot, err := client.Snapshot(ctx)
		if err != nil {
			select {
			case <-client.Done():
				return
			default:
				// A snapshot is the desktop's complete authority; reconnecting is safer than presenting an unrefreshable session as healthy.
				return
			}
		}
		a.emitSnapshot(ctx, snapshot, &cursor)

		if !a.wait(ctx, a.pollInterval) {
			return
		}
	}
}

// emitSnapshot rejects unchanged, stale, or invalid state within the current connection epoch.
func (a *App) emitSnapshot(ctx context.Context, snapshot domain.Snapshot, cursor *snapshotCursor) {
	if err := snapshot.Validate(); err != nil {
		return
	}
	if cursor.initialized && snapshot.Sequence <= cursor.sequence {
		return
	}
	cursor.sequence = snapshot.Sequence
	cursor.initialized = true

	a.events.Snapshot(ctx, snapshot)
}

// emitConnection keeps ephemeral transport state independent from durable snapshot ordering.
func (a *App) emitConnection(ctx context.Context, state ConnectionState) {
	a.events.Connection(ctx, ConnectionEvent{State: state})
}

// currentConnection snapshots lifecycle state so a Wails call cannot observe a partially installed client.
func (a *App) currentConnection() (context.Context, controlClient, error) {
	a.mu.RLock()
	ctx := a.ctx
	client := a.client
	a.mu.RUnlock()

	if ctx == nil || client == nil {
		return nil, nil, errDaemonDisconnected
	}

	return ctx, client, nil
}

// installClient publishes a negotiated session only while the desktop lifecycle remains active.
func (a *App) installClient(ctx context.Context, client controlClient) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ctx.Err() != nil || a.ctx == nil {
		return false
	}
	a.client = client
	return true
}

// removeClient clears only the connection owned by the exiting poll loop.
func (a *App) removeClient(client controlClient) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client == client {
		a.client = nil
	}
}

// findResource requires both identities because resource IDs are unique only inside a project snapshot.
func findResource(snapshot domain.Snapshot, projectID domain.ProjectID, resourceID domain.ResourceID) (domain.ResourceSnapshot, error) {
	for _, project := range snapshot.Projects {
		if project.ID != projectID {
			continue
		}
		for _, resource := range project.Resources {
			if resource.ID == resourceID {
				return resource, nil
			}
		}
		return domain.ResourceSnapshot{}, fmt.Errorf("resource %q was not found in project %q", resourceID, projectID)
	}

	return domain.ResourceSnapshot{}, fmt.Errorf("project %q was not found", projectID)
}

// waitForContext bounds background polling without delaying shutdown after cancellation.
func waitForContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
