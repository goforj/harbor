package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/desktop/internal/networkprerequisite"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/networksetupapproval"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	desktopReconnectDelay   = time.Second
	desktopPollInterval     = 2 * time.Second
	networkSetupIntentBytes = 16
	connectionEventName     = desktopwire.ConnectionEventName
	snapshotEventName       = desktopwire.SnapshotEventName
	networkSetupIntentID    = domain.IntentID("intent-network-setup")
)

var errDaemonDisconnected = errors.New("Harbor daemon is not connected")

// controlClient is the narrow daemon surface the desktop keeps alive between Wails calls.
type controlClient interface {
	Status(context.Context) (control.DaemonStatus, error)
	Snapshot(context.Context) (domain.Snapshot, error)
	RegisterProject(context.Context, control.RegisterProjectRequest) (control.ProjectRegistration, error)
	StartNetworkSetup(context.Context, control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error)
	PrepareNetworkSetupApproval(context.Context, control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error)
	ConfirmNetworkSetupApproval(context.Context, control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error)
	ProjectActivity(context.Context, control.ProjectActivityRequest) (control.ProjectActivity, error)
	StartProject(context.Context, control.StartProjectRequest) (control.ProjectLifecycleOperation, error)
	StopProject(context.Context, control.StopProjectRequest) (control.ProjectLifecycleOperation, error)
	UnregisterProject(context.Context, control.UnregisterProjectRequest) (control.ProjectUnregistration, error)
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

// waitFunc makes reconnect and polling boundaries cancellation-aware and deterministic in tests.
type waitFunc func(context.Context, time.Duration) bool

// networkSetupApprovalRunner performs one exact interactive helper attempt for a daemon-selected setup revision.
type networkSetupApprovalRunner interface {
	Execute(context.Context, networksetupapproval.Request) (networksetupapproval.Outcome, error)
}

// networkSetupApprovalFactory binds the current authenticated desktop session only when the user requests setup.
type networkSetupApprovalFactory func(networksetupapproval.Client) networkSetupApprovalRunner

// networkSetupIntentFactory creates a fresh retry identity without coupling desktop idempotency to process lifetime.
type networkSetupIntentFactory func() (domain.IntentID, error)

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
	mu                sync.RWMutex
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	client            controlClient
	clientLeases      int
	clientDrain       chan struct{}
	clientFactory     clientFactory
	events            desktopwire.Emitter
	open              resourceOpener
	choose            directoryChooser
	setupApproval     networkSetupApprovalFactory
	setupPrerequisite networkprerequisite.Ensurer
	setupIntent       networkSetupIntentFactory
	presentation      *presentationController
	wait              waitFunc
	reconnectDelay    time.Duration
	pollInterval      time.Duration
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
		clientFactory: factory,
		events:        desktopwire.NewEmitter(emit),
		open:          open,
		choose:        runtime.OpenDirectoryDialog,
		setupApproval: func(client networksetupapproval.Client) networkSetupApprovalRunner {
			return networksetupapproval.New(
				client,
				launcher.New(launcher.NewNativeTransport(), helper.SystemClock{}),
			)
		},
		setupPrerequisite: networkprerequisite.New(),
		setupIntent:       newNetworkSetupIntent,
		presentation:      newPresentationController(runtime.WindowUnminimise, runtime.Show, runtime.Quit),
		wait:              wait,
		reconnectDelay:    desktopReconnectDelay,
		pollInterval:      desktopPollInterval,
	}
}

// SetupNetwork starts or resumes Harbor's singleton network setup and opens native consent only when approval is required.
func (a *App) SetupNetwork() (control.NetworkSetupOperation, error) {
	ctx, client, release, err := a.leaseCurrentConnection()
	if err != nil {
		return control.NetworkSetupOperation{}, err
	}
	defer release()

	intentID := networkSetupIntentID
	setup, err := client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{IntentID: intentID})
	if networkSetupNeedsFreshIntent(setup, err) {
		intentID, err = a.setupIntent()
		if err != nil {
			return control.NetworkSetupOperation{}, fmt.Errorf("create Harbor network setup retry: %w", err)
		}
		setup, err = client.StartNetworkSetup(ctx, control.StartNetworkSetupRequest{IntentID: intentID})
	}
	if err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("start Harbor network setup: %w", err)
	}
	if err := setup.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup: %w", err)
	}
	if setup.Operation.IntentID != intentID {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup: daemon result belongs to another intent")
	}

	switch setup.Operation.State {
	case domain.OperationSucceeded:
		return setup, nil
	case domain.OperationRequiresApproval:
		return a.approveNetworkSetup(ctx, client, setup)
	default:
		return control.NetworkSetupOperation{}, fmt.Errorf("Harbor network setup is %s", setup.Operation.State)
	}
}

// networkSetupNeedsFreshIntent recovers only a poisoned singleton retry or an opaque fixed-intent replay failure.
func networkSetupNeedsFreshIntent(setup control.NetworkSetupOperation, err error) bool {
	if err != nil {
		var wireError rpc.WireError
		return errors.As(err, &wireError) && wireError.Code == rpc.ErrorCodeInternal
	}
	if setup.Operation.State == domain.OperationCancelled {
		return true
	}
	return setup.Operation.State == domain.OperationFailed &&
		setup.Operation.Problem != nil &&
		setup.Operation.Problem.Retryable
}

// newNetworkSetupIntent creates an independently retryable setup identity from operating-system entropy.
func newNetworkSetupIntent() (domain.IntentID, error) {
	return newNetworkSetupIntentFrom(rand.Reader)
}

// newNetworkSetupIntentFrom keeps entropy failure testable without weakening production identity generation.
func newNetworkSetupIntentFrom(reader io.Reader) (domain.IntentID, error) {
	random := make([]byte, networkSetupIntentBytes)
	if _, err := io.ReadFull(reader, random); err != nil {
		return "", fmt.Errorf("read network setup intent entropy: %w", err)
	}
	intentID := domain.IntentID("intent-network-setup-" + hex.EncodeToString(random))
	if err := intentID.Validate(); err != nil {
		return "", fmt.Errorf("validate network setup intent: %w", err)
	}
	return intentID, nil
}

// approveNetworkSetup delegates only the exact daemon-selected revision to the native approval boundary.
func (a *App) approveNetworkSetup(
	ctx context.Context,
	client controlClient,
	setup control.NetworkSetupOperation,
) (control.NetworkSetupOperation, error) {
	request := networksetupapproval.Request{
		OperationID:               setup.Operation.ID,
		ExpectedOperationRevision: setup.Revision,
	}
	runner := a.setupApproval(client)
	outcome, err := runner.Execute(ctx, request)
	if networkSetupNeedsPrerequisiteRepair(outcome, err) {
		repairErr := a.setupPrerequisite.Ensure(ctx)
		if repairErr != nil {
			return control.NetworkSetupOperation{}, fmt.Errorf("install Harbor privileged networking support: %w", repairErr)
		}
		outcome, err = runner.Execute(ctx, request)
		if networkSetupNeedsPrerequisiteRepair(outcome, err) {
			return control.NetworkSetupOperation{}, networkSetupPrerequisiteVerificationError(outcome, err)
		}
	}
	if err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("approve Harbor network setup: %w", err)
	}
	if outcome.State != networksetupapproval.Succeeded {
		return control.NetworkSetupOperation{}, networkSetupApprovalError(outcome)
	}
	if outcome.Confirmation == nil || outcome.HelperFailure != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("approve Harbor network setup: successful approval returned inconsistent evidence")
	}

	confirmation := *outcome.Confirmation
	if err := confirmation.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup confirmation: %w", err)
	}
	if confirmation.Operation.ID != setup.Operation.ID ||
		confirmation.Operation.IntentID != setup.Operation.IntentID ||
		confirmation.NetworkRevision != setup.Revision+2 ||
		confirmation.Revision != setup.Revision+3 {
		return control.NetworkSetupOperation{}, fmt.Errorf("validate Harbor network setup confirmation: result crossed the selected operation revision")
	}

	result := control.NetworkSetupOperation{
		Operation: confirmation.Operation,
		Revision:  confirmation.Revision,
	}
	return result, nil
}

// networkSetupNeedsPrerequisiteRepair recognizes only reviewed daemon and launcher evidence that the fixed helper boundary needs installation or repair.
func networkSetupNeedsPrerequisiteRepair(outcome networksetupapproval.Outcome, err error) bool {
	if err != nil {
		var wireError rpc.WireError
		return errors.As(err, &wireError) &&
			(wireError.Code == rpc.ErrorCodePrivilegedHelperRequired || wireError.Code == rpc.ErrorCodePrivilegedHelperUnsafe)
	}
	return outcome.State == networksetupapproval.Unavailable
}

// networkSetupPrerequisiteVerificationError reports only fixed peer-safe categories after native installation claimed success.
func networkSetupPrerequisiteVerificationError(outcome networksetupapproval.Outcome, err error) error {
	if err != nil {
		var wireError rpc.WireError
		if errors.As(err, &wireError) {
			switch wireError.Code {
			case rpc.ErrorCodePrivilegedHelperRequired:
				return errors.New("verify Harbor privileged networking support after installation: harbord still cannot find the ticket directory")
			case rpc.ErrorCodePrivilegedHelperUnsafe:
				return errors.New("verify Harbor privileged networking support after installation: harbord rejected the ticket directory's ownership, permissions, type, or ACLs")
			}
		}
	}
	if outcome.State == networksetupapproval.Unavailable {
		return errors.New("verify Harbor privileged networking support after installation: the native helper is unavailable")
	}
	return errors.New("verify Harbor privileged networking support after installation: the result was inconsistent")
}

// networkSetupApprovalError preserves retry guidance without exposing helper tickets or privileged command details.
func networkSetupApprovalError(outcome networksetupapproval.Outcome) error {
	switch outcome.State {
	case networksetupapproval.Declined:
		return errors.New("Harbor network setup approval was declined; setup is safe to retry")
	case networksetupapproval.Unavailable:
		return errors.New("Harbor network setup approval is unavailable on this installation")
	case networksetupapproval.HelperFailed:
		if outcome.HelperFailure == nil {
			return errors.New("Harbor network setup helper failed without a problem description")
		}
		return fmt.Errorf("Harbor network setup helper failed (%s): %s", outcome.HelperFailure.Code, outcome.HelperFailure.Message)
	case networksetupapproval.Indeterminate:
		return errors.New("Harbor network setup may have changed the host; refresh before retrying")
	default:
		return fmt.Errorf("Harbor network setup approval returned unsupported state %q", outcome.State)
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

// StartProject starts or resumes exactly one client-owned project start intent through the connected daemon.
func (a *App) StartProject(projectID string, intentID string) (control.ProjectLifecycleOperation, error) {
	request := control.StartProjectRequest{
		ProjectID: domain.ProjectID(projectID),
		IntentID:  domain.IntentID(intentID),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("project start request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectLifecycleOperation{}, err
	}
	result, err := client.StartProject(ctx, request)
	if err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("start GoForj project: %w", err)
	}
	if err := result.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project start: %w", err)
	}
	if result.Operation.Kind != domain.OperationKindProjectStart ||
		result.Operation.ProjectID != request.ProjectID ||
		result.Operation.IntentID != request.IntentID {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project start: daemon result does not match the requested action, project, and intent")
	}

	return result, nil
}

// ProjectActivity reads one bounded output chunk from the project's current durable session.
func (a *App) ProjectActivity(projectID string, sessionID string, cursor uint64) (control.ProjectActivity, error) {
	request := control.ProjectActivityRequest{
		ProjectID: domain.ProjectID(projectID),
		SessionID: domain.SessionID(sessionID),
		Cursor:    cursor,
	}
	if err := request.Validate(); err != nil {
		return control.ProjectActivity{}, fmt.Errorf("project activity request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectActivity{}, err
	}
	activity, err := client.ProjectActivity(ctx, request)
	if err != nil {
		return control.ProjectActivity{}, fmt.Errorf("read GoForj project activity: %w", err)
	}
	if err := activity.Validate(); err != nil {
		return control.ProjectActivity{}, fmt.Errorf("validate project activity: %w", err)
	}
	if activity.ProjectID != request.ProjectID {
		return control.ProjectActivity{}, errors.New("validate project activity: daemon result belongs to another project")
	}
	if request.SessionID != "" && activity.Session != nil && activity.Session.ID != request.SessionID && !activity.Session.Output.Reset {
		return control.ProjectActivity{}, errors.New("validate project activity: daemon changed sessions without resetting output")
	}

	return activity, nil
}

// StopProject starts or resumes exactly one client-owned project stop intent through the connected daemon.
func (a *App) StopProject(projectID string, intentID string) (control.ProjectLifecycleOperation, error) {
	request := control.StopProjectRequest{
		ProjectID: domain.ProjectID(projectID),
		IntentID:  domain.IntentID(intentID),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("project stop request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectLifecycleOperation{}, err
	}
	result, err := client.StopProject(ctx, request)
	if err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("stop GoForj project: %w", err)
	}
	if err := result.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project stop: %w", err)
	}
	if result.Operation.Kind != domain.OperationKindProjectStop ||
		result.Operation.ProjectID != request.ProjectID ||
		result.Operation.IntentID != request.IntentID {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("validate project stop: daemon result does not match the requested action, project, and intent")
	}

	return result, nil
}

// RemoveProject starts or resumes exactly one client-owned project removal intent through the connected daemon.
func (a *App) RemoveProject(projectID string, intentID string) (control.ProjectUnregistration, error) {
	request := control.UnregisterProjectRequest{
		ProjectID: domain.ProjectID(projectID),
		IntentID:  domain.IntentID(intentID),
	}
	if err := request.Validate(); err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("project removal request: %w", err)
	}

	ctx, client, err := a.currentConnection()
	if err != nil {
		return control.ProjectUnregistration{}, err
	}
	result, err := client.UnregisterProject(ctx, request)
	if err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("remove GoForj project: %w", err)
	}
	if err := result.Validate(); err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("validate project removal: %w", err)
	}
	if result.Operation.ProjectID != request.ProjectID || result.Operation.IntentID != request.IntentID {
		return control.ProjectUnregistration{}, fmt.Errorf("validate project removal: daemon result does not match the requested project and intent")
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
	a.presentation.startup(ctx)
	a.mu.Unlock()

	go a.run(runContext, done)
}

// shutdown cancels background calls, closes the live transport, and waits until the owner goroutine exits.
func (a *App) shutdown(_ context.Context) {
	a.presentation.shutdown()

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
	a.presentation.activate()
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
		a.retireClient(ctx, client)

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

// leaseCurrentConnection keeps an interactive approval on its selected session until its exact confirmation returns.
func (a *App) leaseCurrentConnection() (context.Context, controlClient, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.ctx == nil || a.client == nil {
		return nil, nil, nil, errDaemonDisconnected
	}
	if a.clientDrain != nil {
		panic("desktop connection is published while retirement is pending")
	}

	ctx := a.ctx
	client := a.client
	a.clientLeases++
	var once sync.Once
	release := func() {
		once.Do(func() {
			a.releaseClientLease(client)
		})
	}
	return ctx, client, release, nil
}

// releaseClientLease lets a retired connection close only after every selected approval has left its confirmation boundary.
func (a *App) releaseClientLease(client controlClient) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.clientLeases == 0 {
		panic("desktop connection lease released without ownership")
	}
	if a.client != nil && a.client != client {
		panic("desktop connection lease crossed a replacement session")
	}

	a.clientLeases--
	if a.clientLeases == 0 && a.clientDrain != nil {
		close(a.clientDrain)
		a.clientDrain = nil
	}
}

// installClient publishes a negotiated session only while the desktop lifecycle remains active.
func (a *App) installClient(ctx context.Context, client controlClient) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ctx.Err() != nil || a.ctx == nil {
		return false
	}
	if a.clientLeases != 0 || a.clientDrain != nil {
		panic("desktop connection installed before the retired session drained")
	}
	a.client = client
	return true
}

// removeClient clears only the connection owned by the exiting poll loop and reports when its approvals have drained.
func (a *App) removeClient(client controlClient) <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client != client {
		return nil
	}
	a.client = nil
	if a.clientLeases == 0 {
		return nil
	}
	if a.clientDrain != nil {
		panic("desktop connection retirement started more than once")
	}
	a.clientDrain = make(chan struct{})
	return a.clientDrain
}

// retireClient prevents polling from closing a session between privileged helper success and durable confirmation.
func (a *App) retireClient(ctx context.Context, client controlClient) {
	drained := a.removeClient(client)
	if drained != nil {
		select {
		case <-drained:
		case <-ctx.Done():
		}
	}
	_ = client.Close()
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
