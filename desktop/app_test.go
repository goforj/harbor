package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/desktop/internal/networkprerequisite"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/networksetupapproval"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// fakeControlClient keeps lifecycle and response behavior explicit in desktop adapter tests.
type fakeControlClient struct {
	mu                sync.Mutex
	status            control.DaemonStatus
	statusErr         error
	snapshot          domain.Snapshot
	snapshotErr       error
	snapshotHook      func()
	registration      control.ProjectRegistration
	registerErr       error
	registerPath      string
	networkSetup      control.NetworkSetupOperation
	networkSetupErr   error
	networkSetupReq   control.StartNetworkSetupRequest
	networkSetupHook  func(control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error)
	setupPreparation  control.NetworkSetupApprovalPreparation
	setupPrepareErr   error
	setupPrepareReq   control.PrepareNetworkSetupApprovalRequest
	setupConfirmation control.NetworkSetupApprovalConfirmation
	setupConfirmErr   error
	setupConfirmReq   control.ConfirmNetworkSetupApprovalRequest
	startLifecycle    control.ProjectLifecycleOperation
	startErr          error
	startRequest      control.StartProjectRequest
	stopLifecycle     control.ProjectLifecycleOperation
	stopErr           error
	stopRequest       control.StopProjectRequest
	unregistration    control.ProjectUnregistration
	unregisterErr     error
	unregisterRequest control.UnregisterProjectRequest
	done              chan struct{}
	closeOnce         sync.Once
	closeCount        atomic.Int32
}

// fakeNetworkSetupApprovalRunner records one exact setup selection and returns its configured bounded outcome.
type fakeNetworkSetupApprovalRunner struct {
	requests []networksetupapproval.Request
	outcome  networksetupapproval.Outcome
	err      error
	execute  func(int, networksetupapproval.Request) (networksetupapproval.Outcome, error)
}

// Execute records the selected setup revision without opening native consent.
func (runner *fakeNetworkSetupApprovalRunner) Execute(_ context.Context, request networksetupapproval.Request) (networksetupapproval.Outcome, error) {
	runner.requests = append(runner.requests, request)
	if runner.execute != nil {
		return runner.execute(len(runner.requests), request)
	}
	return runner.outcome, runner.err
}

// fakeNetworkPrerequisiteEnsurer records the bounded source-development repair handoff.
type fakeNetworkPrerequisiteEnsurer struct {
	calls int
	err   error
}

// Ensure records one attempted repair and returns its configured native result.
func (ensurer *fakeNetworkPrerequisiteEnsurer) Ensure(context.Context) error {
	ensurer.calls++
	return ensurer.err
}

// newFakeControlClient creates a connected test client with a valid replacement snapshot.
func newFakeControlClient() *fakeControlClient {
	return &fakeControlClient{
		status: control.DaemonStatus{
			State:                 control.DaemonStateReady,
			Build:                 control.Build{Version: "test"},
			Protocol:              rpc.Version{Major: 1},
			Capabilities:          []rpc.Capability{control.CapabilityV1},
			SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
			Sequence:              8,
		},
		snapshot:       testSnapshot(),
		registration:   testRegistration(),
		networkSetup:   testNetworkSetupOperation(domain.OperationSucceeded, 10),
		startLifecycle: testProjectLifecycle(domain.OperationKindProjectStart, "desktop-start-orders"),
		stopLifecycle:  testProjectLifecycle(domain.OperationKindProjectStop, "desktop-stop-orders"),
		unregistration: testUnregistration(),
		done:           make(chan struct{}),
	}
}

// StartNetworkSetup records the singleton setup identity and returns the configured durable operation.
func (client *fakeControlClient) StartNetworkSetup(_ context.Context, request control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.networkSetupReq = request
	if client.networkSetupHook != nil {
		return client.networkSetupHook(request)
	}
	return client.networkSetup, client.networkSetupErr
}

// PrepareNetworkSetupApproval records the selected operation revision for executor-backed tests.
func (client *fakeControlClient) PrepareNetworkSetupApproval(_ context.Context, request control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.setupPrepareReq = request
	return client.setupPreparation, client.setupPrepareErr
}

// ConfirmNetworkSetupApproval records the helper evidence selected for durable confirmation.
func (client *fakeControlClient) ConfirmNetworkSetupApproval(_ context.Context, request control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.setupConfirmReq = request
	return client.setupConfirmation, client.setupConfirmErr
}

// Status returns the configured daemon status or test failure.
func (client *fakeControlClient) Status(_ context.Context) (control.DaemonStatus, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.status, client.statusErr
}

// Snapshot returns a defensive copy of the configured replacement snapshot.
func (client *fakeControlClient) Snapshot(_ context.Context) (domain.Snapshot, error) {
	client.mu.Lock()
	snapshot := cloneSnapshot(client.snapshot)
	err := client.snapshotErr
	hook := client.snapshotHook
	client.mu.Unlock()
	if hook != nil {
		hook()
	}
	return snapshot, err
}

// RegisterProject records the selected path and returns the configured authoritative mutation result.
func (client *fakeControlClient) RegisterProject(_ context.Context, request control.RegisterProjectRequest) (control.ProjectRegistration, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.registerPath = request.Path
	return client.registration, client.registerErr
}

// StartProject records the stable lifecycle identity and returns the configured start operation.
func (client *fakeControlClient) StartProject(_ context.Context, request control.StartProjectRequest) (control.ProjectLifecycleOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.startRequest = request
	return client.startLifecycle, client.startErr
}

// StopProject records the stable lifecycle identity and returns the configured stop operation.
func (client *fakeControlClient) StopProject(_ context.Context, request control.StopProjectRequest) (control.ProjectLifecycleOperation, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.stopRequest = request
	return client.stopLifecycle, client.stopErr
}

// UnregisterProject records the stable removal identity and returns the configured authoritative operation.
func (client *fakeControlClient) UnregisterProject(_ context.Context, request control.UnregisterProjectRequest) (control.ProjectUnregistration, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.unregisterRequest = request
	return client.unregistration, client.unregisterErr
}

// Done exposes terminal connection state to the desktop owner loop.
func (client *fakeControlClient) Done() <-chan struct{} {
	return client.done
}

// Close terminates the fake connection exactly once.
func (client *fakeControlClient) Close() error {
	client.closeOnce.Do(func() {
		client.closeCount.Add(1)
		close(client.done)
	})
	return nil
}

// TestNewAppWiresProductionDependencies covers the zero-configuration Wails composition without starting a daemon connection.
func TestNewAppWiresProductionDependencies(t *testing.T) {
	t.Parallel()

	app := NewApp()
	if app.clientFactory == nil || app.open == nil || app.choose == nil || app.setupApproval == nil || app.setupPrerequisite == nil || app.setupIntent == nil || app.presentation == nil || app.wait == nil {
		t.Fatal("NewApp() left a production dependency unwired")
	}
}

// TestNewDesktopClientHonorsCancellation verifies the concrete adapter forwards the desktop request context.
func TestNewDesktopClientHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newDesktopClient(ctx, control.ClientConfig{Role: rpc.RoleDesktop}); err == nil {
		t.Fatal("newDesktopClient() error = nil for cancelled dial")
	}
}

// TestAppLifecycleConnectsAsDesktopAndPublishesSnapshots covers startup, polling, and joined shutdown ownership.
func TestAppLifecycleConnectsAsDesktopAndPublishesSnapshots(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	roles := make(chan rpc.Role, 1)
	emitted := make(chan domain.Snapshot, 1)
	connections := make(chan ConnectionState, 2)
	app := newApp(
		func(_ context.Context, config control.ClientConfig) (controlClient, error) {
			roles <- config.Role
			return client, nil
		},
		func(_ context.Context, event string, values ...interface{}) {
			switch event {
			case snapshotEventName:
				emitted <- values[0].(domain.Snapshot)
			case connectionEventName:
				connections <- values[0].(ConnectionEvent).State
			default:
				t.Errorf("unexpected event = %q", event)
			}
		},
		func(context.Context, string) {},
		func(ctx context.Context, _ time.Duration) bool {
			<-ctx.Done()
			return false
		},
	)

	app.startup(context.Background())
	select {
	case role := <-roles:
		if role != rpc.RoleDesktop {
			t.Fatalf("role = %q, want %q", role, rpc.RoleDesktop)
		}
	case <-time.After(time.Second):
		t.Fatal("desktop client was not created")
	}
	select {
	case snapshot := <-emitted:
		if snapshot.Sequence != client.snapshot.Sequence {
			t.Fatalf("emitted sequence = %d, want %d", snapshot.Sequence, client.snapshot.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("initial snapshot was not emitted")
	}
	for _, want := range []ConnectionState{ConnectionConnecting, ConnectionConnected} {
		select {
		case got := <-connections:
			if got != want {
				t.Fatalf("connection state = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("connection state %q was not emitted", want)
		}
	}

	app.shutdown(context.Background())
	if client.closeCount.Load() != 1 {
		t.Fatalf("Close() count = %d, want 1", client.closeCount.Load())
	}
}

// TestStartupRejectsCompetingLifecycle prevents two owner goroutines from sharing one desktop instance.
func TestStartupRejectsCompetingLifecycle(t *testing.T) {
	t.Parallel()

	app := testApp()
	app.startup(context.Background())
	defer app.shutdown(context.Background())
	defer func() {
		if recover() == nil {
			t.Fatal("startup() panic = nil for second active lifecycle")
		}
	}()
	app.startup(context.Background())
}

// TestSecondInstanceBeforeStartupIsInert keeps an early platform callback from using a missing Wails context.
func TestSecondInstanceBeforeStartupIsInert(t *testing.T) {
	t.Parallel()

	testApp().onSecondInstanceLaunch(options.SecondInstanceData{})
}

// TestSecondInstanceRestoresTheOwnedWindow verifies relaunch remains presentation-only.
func TestSecondInstanceRestoresTheOwnedWindow(t *testing.T) {
	t.Parallel()

	app := testApp()
	restored := false
	app.presentation = newPresentationController(
		func(context.Context) {},
		func(context.Context) { restored = true },
		func(context.Context) {},
	)
	app.presentation.startup(context.Background())
	app.onSecondInstanceLaunch(options.SecondInstanceData{})
	if !restored {
		t.Fatal("onSecondInstanceLaunch() did not restore the Wails window")
	}
}

// TestAppReconnectsAfterDialFailure proves daemon startup order does not strand the desktop in a fixture state.
func TestAppReconnectsAfterDialFailure(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	var attempts atomic.Int32
	emitted := make(chan struct{}, 1)
	connections := make(chan ConnectionState, 4)
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("daemon is starting")
			}
			return client, nil
		},
		func(_ context.Context, event string, values ...interface{}) {
			if event == snapshotEventName {
				emitted <- struct{}{}
				return
			}
			connections <- values[0].(ConnectionEvent).State
		},
		func(context.Context, string) {},
		func(ctx context.Context, _ time.Duration) bool {
			if attempts.Load() == 1 {
				return true
			}
			<-ctx.Done()
			return false
		},
	)

	app.startup(context.Background())
	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("snapshot was not emitted after reconnect")
	}
	app.shutdown(context.Background())
	if attempts.Load() != 2 {
		t.Fatalf("connection attempts = %d, want 2", attempts.Load())
	}
	for index, want := range []ConnectionState{
		ConnectionConnecting,
		ConnectionDisconnected,
		ConnectionConnecting,
		ConnectionConnected,
	} {
		select {
		case got := <-connections:
			if got != want {
				t.Fatalf("connection state %d = %q, want %q", index, got, want)
			}
		default:
			t.Fatalf("connection state %d = missing, want %q", index, want)
		}
	}
}

// TestRunStopsWhenReconnectWaitIsCancelled covers a daemon that remains unavailable through shutdown.
func TestRunStopsWhenReconnectWaitIsCancelled(t *testing.T) {
	t.Parallel()

	var waits atomic.Int32
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) {
			return nil, errors.New("unavailable")
		},
		func(context.Context, string, ...interface{}) {},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { waits.Add(1); return false },
	)
	done := make(chan struct{})
	app.run(context.Background(), done)
	select {
	case <-done:
	default:
		t.Fatal("run() did not report completion")
	}
	if waits.Load() != 1 {
		t.Fatalf("wait count = %d, want 1", waits.Load())
	}
}

// TestRunClosesClientWhenLifecycleEndsDuringDial covers a factory that returns after cancellation.
func TestRunClosesClientWhenLifecycleEndsDuringDial(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	ctx, cancel := context.WithCancel(context.Background())
	app := testApp()
	app.ctx = context.Background()
	app.clientFactory = func(context.Context, control.ClientConfig) (controlClient, error) {
		cancel()
		return client, nil
	}
	done := make(chan struct{})
	app.run(ctx, done)
	if client.closeCount.Load() != 1 {
		t.Fatalf("Close() count = %d, want 1", client.closeCount.Load())
	}
}

// TestPollStopsForContextConnectionAndRetryBoundaries covers every terminal polling decision.
func TestPollStopsForContextConnectionAndRetryBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("context", func(t *testing.T) {
		client := newFakeControlClient()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		testApp().poll(ctx, client)
	})

	t.Run("connection", func(t *testing.T) {
		client := newFakeControlClient()
		_ = client.Close()
		testApp().poll(context.Background(), client)
	})

	t.Run("snapshot failure", func(t *testing.T) {
		client := newFakeControlClient()
		client.snapshotErr = errors.New("read failed")
		var waits atomic.Int32
		app := newApp(
			func(context.Context, control.ClientConfig) (controlClient, error) { return client, nil },
			func(context.Context, string, ...interface{}) { t.Fatal("invalid snapshot was emitted") },
			func(context.Context, string) {},
			func(context.Context, time.Duration) bool { waits.Add(1); return false },
		)
		app.poll(context.Background(), client)
		if waits.Load() != 0 {
			t.Fatalf("wait count = %d, want 0 before reconnect", waits.Load())
		}
	})

	t.Run("snapshot failure then connection close", func(t *testing.T) {
		client := newFakeControlClient()
		client.snapshotErr = errors.New("read failed")
		client.snapshotHook = func() { _ = client.Close() }
		var waits atomic.Int32
		app := newApp(
			func(context.Context, control.ClientConfig) (controlClient, error) { return client, nil },
			func(context.Context, string, ...interface{}) { t.Fatal("invalid snapshot was emitted") },
			func(context.Context, string) {},
			func(context.Context, time.Duration) bool { waits.Add(1); return false },
		)
		app.poll(context.Background(), client)
		if waits.Load() != 0 {
			t.Fatalf("wait count = %d, want 0 after connection closed", waits.Load())
		}
	})
}

// TestAppReconnectsAfterSnapshotFailureAndPublishesNewBaseline proves one unusable authority read starts a fresh ordering epoch.
func TestAppReconnectsAfterSnapshotFailureAndPublishesNewBaseline(t *testing.T) {
	t.Parallel()

	first := newFakeControlClient()
	second := newFakeControlClient()
	second.snapshot.Sequence = 3
	first.snapshotHook = func() {
		first.mu.Lock()
		first.snapshotErr = errors.New("snapshot authority failed")
		first.snapshotHook = nil
		first.mu.Unlock()
	}

	var attempts atomic.Int32
	var pollWaits atomic.Int32
	snapshots := make(chan domain.Sequence, 2)
	connections := make(chan ConnectionState, 5)
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) {
			if attempts.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
		func(_ context.Context, event string, values ...interface{}) {
			switch event {
			case snapshotEventName:
				snapshots <- values[0].(domain.Snapshot).Sequence
			case connectionEventName:
				connections <- values[0].(ConnectionEvent).State
			}
		},
		func(context.Context, string) {},
		func(ctx context.Context, duration time.Duration) bool {
			if duration == time.Millisecond {
				if pollWaits.Add(1) == 1 {
					return true
				}
				<-ctx.Done()
				return false
			}
			return true
		},
	)
	app.pollInterval = time.Millisecond
	app.reconnectDelay = 2 * time.Millisecond

	app.startup(context.Background())
	gotSnapshots := make([]domain.Sequence, 0, 2)
	for range 2 {
		select {
		case sequence := <-snapshots:
			gotSnapshots = append(gotSnapshots, sequence)
		case <-time.After(time.Second):
			app.shutdown(context.Background())
			t.Fatal("timed out waiting for snapshots across reconnect")
		}
	}
	app.shutdown(context.Background())

	if attempts.Load() != 2 {
		t.Fatalf("connection attempts = %d, want 2", attempts.Load())
	}
	if gotSnapshots[0] != 8 || gotSnapshots[1] != 3 {
		t.Fatalf("snapshot sequences = %v, want [8 3] across reconnect", gotSnapshots)
	}
	for index, want := range []ConnectionState{
		ConnectionConnecting,
		ConnectionConnected,
		ConnectionDisconnected,
		ConnectionConnecting,
		ConnectionConnected,
	} {
		select {
		case got := <-connections:
			if got != want {
				t.Fatalf("connection state %d = %q, want %q", index, got, want)
			}
		default:
			t.Fatalf("connection state %d = missing, want %q", index, want)
		}
	}
}

// TestAppExportedReadsRequireAndUseCurrentConnection verifies the Wails surface never dials ad hoc.
func TestAppExportedReadsRequireAndUseCurrentConnection(t *testing.T) {
	t.Parallel()

	app := testApp()
	if _, err := app.Status(); !errors.Is(err, errDaemonDisconnected) {
		t.Fatalf("Status() error = %v, want disconnected", err)
	}
	if _, err := app.Snapshot(); !errors.Is(err, errDaemonDisconnected) {
		t.Fatalf("Snapshot() error = %v, want disconnected", err)
	}

	client := newFakeControlClient()
	app.ctx = context.Background()
	app.client = client
	status, err := app.Status()
	if err != nil || status.Sequence != 8 {
		t.Fatalf("Status() = (%+v, %v), want sequence 8", status, err)
	}
	snapshot, err := app.Snapshot()
	if err != nil || snapshot.Sequence != 8 {
		t.Fatalf("Snapshot() = (sequence %d, %v), want sequence 8", snapshot.Sequence, err)
	}

	client.statusErr = errors.New("status failed")
	client.snapshotErr = errors.New("snapshot failed")
	if _, err := app.Status(); err == nil || !strings.Contains(err.Error(), "status failed") {
		t.Fatalf("Status() error = %v, want wrapped failure", err)
	}
	if _, err := app.Snapshot(); err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Fatalf("Snapshot() error = %v, want wrapped failure", err)
	}
}

// TestSnapshotRejectsInvalidDaemonState keeps even injected control clients behind domain validation.
func TestSnapshotRejectsInvalidDaemonState(t *testing.T) {
	t.Parallel()

	app := testApp()
	client := newFakeControlClient()
	client.snapshot.Projects = nil
	app.ctx = context.Background()
	app.client = client

	if _, err := app.Snapshot(); err == nil || !strings.Contains(err.Error(), "validate Harbor snapshot") {
		t.Fatalf("Snapshot() error = %v, want validation failure", err)
	}
}

// TestSetupNetworkReplaysCompletedOperationWithoutConsent keeps an idempotent desktop retry entirely unprivileged.
func TestSetupNetworkReplaysCompletedOperationWithoutConsent(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner {
		t.Fatal("setup approval was constructed for a completed operation")
		return nil
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.State != domain.OperationSucceeded || result.Revision != client.networkSetup.Revision {
		t.Fatalf("SetupNetwork() = %#v", result)
	}
	if client.networkSetupReq.IntentID != networkSetupIntentID {
		t.Fatalf("setup intent = %q, want %q", client.networkSetupReq.IntentID, networkSetupIntentID)
	}
}

// TestSetupNetworkRetriesAnOpaqueFixedIntentFailure lets an older poisoned singleton record stop blocking the app forever.
func TestSetupNetworkRetriesAnOpaqueFixedIntentFailure(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	const retryIntent domain.IntentID = "intent-network-setup-retry"
	app.setupIntent = func() (domain.IntentID, error) { return retryIntent, nil }
	requests := make([]control.StartNetworkSetupRequest, 0, 2)
	client.networkSetupHook = func(request control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return control.NetworkSetupOperation{}, rpc.NewWireError(rpc.ErrorCodeInternal)
		}
		result := testNetworkSetupOperation(domain.OperationSucceeded, 10)
		result.Operation.IntentID = request.IntentID
		return result, nil
	}
	app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner {
		t.Fatal("setup approval was constructed for a completed retry")
		return nil
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.IntentID != retryIntent || len(requests) != 2 ||
		requests[0].IntentID != networkSetupIntentID || requests[1].IntentID != retryIntent {
		t.Fatalf("setup retry result = %#v, requests = %#v", result, requests)
	}
}

// TestSetupNetworkRetriesARecoverableTerminalOperation gives each safe retry a new durable idempotency boundary.
func TestSetupNetworkRetriesARecoverableTerminalOperation(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	const retryIntent domain.IntentID = "intent-network-setup-retry"
	app.setupIntent = func() (domain.IntentID, error) { return retryIntent, nil }
	failed := testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	finishedAt := *failed.Operation.StartedAt
	failed.Operation.State = domain.OperationFailed
	failed.Operation.Phase = "setup preflight failed"
	failed.Operation.FinishedAt = &finishedAt
	failed.Operation.Problem = &domain.Problem{
		Code:      "network.setup.preflight_failed",
		Message:   "Harbor could not inspect local networking.",
		Retryable: true,
	}
	requests := make([]control.StartNetworkSetupRequest, 0, 2)
	client.networkSetupHook = func(request control.StartNetworkSetupRequest) (control.NetworkSetupOperation, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return failed, nil
		}
		result := testNetworkSetupOperation(domain.OperationSucceeded, 10)
		result.Operation.IntentID = request.IntentID
		return result, nil
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if result.Operation.IntentID != retryIntent || len(requests) != 2 {
		t.Fatalf("setup retry result = %#v, requests = %#v", result, requests)
	}
}

// TestSetupNetworkApprovesOnlyTheSelectedRevision verifies the Wails action delegates the exact daemon operation boundary.
func TestSetupNetworkApprovesOnlyTheSelectedRevision(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
	runner := &fakeNetworkSetupApprovalRunner{outcome: networksetupapproval.Outcome{
		State:        networksetupapproval.Succeeded,
		Confirmation: &confirmation,
	}}
	app.setupApproval = func(got networksetupapproval.Client) networkSetupApprovalRunner {
		if got != client {
			t.Fatalf("approval client = %T, want installed client", got)
		}
		return runner
	}

	result, err := app.SetupNetwork()
	if err != nil {
		t.Fatalf("SetupNetwork() error = %v", err)
	}
	if len(runner.requests) != 1 ||
		runner.requests[0].OperationID != client.networkSetup.Operation.ID ||
		runner.requests[0].ExpectedOperationRevision != client.networkSetup.Revision {
		t.Fatalf("approval requests = %#v", runner.requests)
	}
	if result.Operation.ID != confirmation.Operation.ID ||
		result.Operation.IntentID != confirmation.Operation.IntentID ||
		result.Operation.State != confirmation.Operation.State ||
		result.Revision != confirmation.Revision {
		t.Fatalf("SetupNetwork() = %#v, want confirmed operation %#v", result, confirmation)
	}
}

// TestSetupNetworkRepairsOnlyReviewedMissingHelperEvidence retries one exact approval after native source setup.
func TestSetupNetworkRepairsOnlyReviewedMissingHelperEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		firstOutcome networksetupapproval.Outcome
		firstErr     error
	}{
		{
			name:     "daemon prerequisite",
			firstErr: rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired),
		},
		{
			name:         "launcher unavailable",
			firstOutcome: networksetupapproval.Outcome{State: networksetupapproval.Unavailable},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			runner := &fakeNetworkSetupApprovalRunner{
				execute: func(call int, _ networksetupapproval.Request) (networksetupapproval.Outcome, error) {
					if call == 1 {
						return test.firstOutcome, test.firstErr
					}
					return networksetupapproval.Outcome{
						State:        networksetupapproval.Succeeded,
						Confirmation: &confirmation,
					}, nil
				},
			}
			ensurer := &fakeNetworkPrerequisiteEnsurer{}
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
			app.setupPrerequisite = ensurer

			result, err := app.SetupNetwork()
			if err != nil {
				t.Fatalf("SetupNetwork() error = %v", err)
			}
			if result.Operation.State != domain.OperationSucceeded || len(runner.requests) != 2 || ensurer.calls != 1 {
				t.Fatalf("setup result/calls = %#v/%d/%d, want success/2/1", result, len(runner.requests), ensurer.calls)
			}
			if runner.requests[0] != runner.requests[1] {
				t.Fatalf("approval retry crossed request boundary: %#v", runner.requests)
			}
		})
	}
}

// TestSetupNetworkBoundsPrerequisiteRepairAndPreservesNativeFailures prevents repair loops and production mutation.
func TestSetupNetworkBoundsPrerequisiteRepairAndPreservesNativeFailures(t *testing.T) {
	t.Parallel()

	privilegedRequired := rpc.NewWireError(rpc.ErrorCodePrivilegedHelperRequired)
	tests := []struct {
		name          string
		repairErr     error
		wantCalls     int
		wantApprovals int
		want          string
	}{
		{name: "repair failure", repairErr: networkprerequisite.ErrDeclined, wantCalls: 1, wantApprovals: 1, want: "declined"},
		{name: "packaged build", repairErr: networkprerequisite.ErrUnavailable, wantCalls: 1, wantApprovals: 1, want: privilegedRequired.Message},
		{name: "retry remains missing", wantCalls: 1, wantApprovals: 2, want: privilegedRequired.Message},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			runner := &fakeNetworkSetupApprovalRunner{err: privilegedRequired}
			ensurer := &fakeNetworkPrerequisiteEnsurer{err: test.repairErr}
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
			app.setupPrerequisite = ensurer

			_, err := app.SetupNetwork()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
			if ensurer.calls != test.wantCalls || len(runner.requests) != test.wantApprovals {
				t.Fatalf("repair/approval calls = %d/%d, want %d/%d", ensurer.calls, len(runner.requests), test.wantCalls, test.wantApprovals)
			}
		})
	}
}

// TestSetupNetworkDoesNotRepairUnreviewedApprovalFailures keeps arbitrary daemon and client errors away from native elevation.
func TestSetupNetworkDoesNotRepairUnreviewedApprovalFailures(t *testing.T) {
	t.Parallel()

	app, client := connectedTestApp()
	client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
	runner := &fakeNetworkSetupApprovalRunner{err: rpc.NewWireError(rpc.ErrorCodeInternal)}
	ensurer := &fakeNetworkPrerequisiteEnsurer{}
	app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
	app.setupPrerequisite = ensurer

	_, err := app.SetupNetwork()
	if err == nil || !strings.Contains(err.Error(), rpc.NewWireError(rpc.ErrorCodeInternal).Message) {
		t.Fatalf("SetupNetwork() error = %v, want reviewed internal failure", err)
	}
	if ensurer.calls != 0 || len(runner.requests) != 1 {
		t.Fatalf("repair/approval calls = %d/%d, want 0/1", ensurer.calls, len(runner.requests))
	}
}

// TestSetupNetworkPreservesSafeApprovalOutcomes verifies native consent failures remain actionable without claiming completion.
func TestSetupNetworkPreservesSafeApprovalOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome networksetupapproval.Outcome
		want    string
	}{
		{name: "declined", outcome: networksetupapproval.Outcome{State: networksetupapproval.Declined}, want: "safe to retry"},
		{name: "unavailable", outcome: networksetupapproval.Outcome{State: networksetupapproval.Unavailable}, want: "unavailable"},
		{
			name: "helper failed",
			outcome: networksetupapproval.Outcome{
				State: networksetupapproval.HelperFailed,
				HelperFailure: &networksetupapproval.HelperFailure{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "loopback setup failed",
				},
			},
			want: "loopback setup failed",
		},
		{name: "helper failed without description", outcome: networksetupapproval.Outcome{State: networksetupapproval.HelperFailed}, want: "without a problem description"},
		{name: "indeterminate", outcome: networksetupapproval.Outcome{State: networksetupapproval.Indeterminate}, want: "refresh before retrying"},
		{name: "unsupported", outcome: networksetupapproval.Outcome{}, want: "unsupported state"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner {
				return &fakeNetworkSetupApprovalRunner{outcome: test.outcome}
			}
			_, err := app.SetupNetwork()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestSetupNetworkRejectsInconsistentDaemonAndApprovalResults covers every response boundary before the UI can claim readiness.
func TestSetupNetworkRejectsInconsistentDaemonAndApprovalResults(t *testing.T) {
	t.Parallel()

	approvalErr := errors.New("approval failed")
	tests := []struct {
		name   string
		mutate func(*fakeControlClient, *fakeNetworkSetupApprovalRunner)
		want   string
	}{
		{name: "start failure", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetupErr = errors.New("start failed")
		}, want: "start failed"},
		{name: "invalid start", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetup = control.NetworkSetupOperation{}
		}, want: "validate Harbor network setup"},
		{name: "wrong intent", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetup.Operation.IntentID = "intent-other"
		}, want: "another intent"},
		{name: "unsupported operation state", mutate: func(client *fakeControlClient, _ *fakeNetworkSetupApprovalRunner) {
			client.networkSetup = testNetworkSetupOperation(domain.OperationRunning, 7)
		}, want: "is running"},
		{name: "approval failure", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			runner.err = approvalErr
		}, want: "approval failed"},
		{name: "missing confirmation", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded}
		}, want: "inconsistent evidence"},
		{name: "unexpected helper failure", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(testNetworkSetupOperation(domain.OperationRequiresApproval, 7), 9, 10)
			runner.outcome = networksetupapproval.Outcome{
				State:         networksetupapproval.Succeeded,
				Confirmation:  &confirmation,
				HelperFailure: &networksetupapproval.HelperFailure{},
			}
		}, want: "inconsistent evidence"},
		{name: "invalid confirmation", mutate: func(_ *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := control.NetworkSetupApprovalConfirmation{}
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "validate Harbor network setup confirmation"},
		{name: "crossed operation", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			confirmation.Operation.ID = "operation-other"
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
		{name: "crossed intent", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			confirmation.Operation.IntentID = "intent-other"
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
		{name: "crossed network revision", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 8, 9)
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
		{name: "crossed operation revision", mutate: func(client *fakeControlClient, runner *fakeNetworkSetupApprovalRunner) {
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 10, 11)
			runner.outcome = networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
		}, want: "crossed the selected operation"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			app, client := connectedTestApp()
			client.networkSetup = testNetworkSetupOperation(domain.OperationRequiresApproval, 7)
			confirmation := testNetworkSetupConfirmation(client.networkSetup, 9, 10)
			runner := &fakeNetworkSetupApprovalRunner{outcome: networksetupapproval.Outcome{
				State:        networksetupapproval.Succeeded,
				Confirmation: &confirmation,
			}}
			test.mutate(client, runner)
			app.setupApproval = func(networksetupapproval.Client) networkSetupApprovalRunner { return runner }
			_, err := app.SetupNetwork()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SetupNetwork() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNewNetworkSetupIntentUsesBoundedEntropy verifies retry IDs are canonical and entropy failures remain visible.
func TestNewNetworkSetupIntentUsesBoundedEntropy(t *testing.T) {
	t.Parallel()

	first, err := newNetworkSetupIntent()
	if err != nil {
		t.Fatalf("newNetworkSetupIntent() error = %v", err)
	}
	second, err := newNetworkSetupIntent()
	if err != nil {
		t.Fatalf("newNetworkSetupIntent() second error = %v", err)
	}
	if first == second || !strings.HasPrefix(string(first), "intent-network-setup-") {
		t.Fatalf("network setup intents = %q and %q", first, second)
	}
	if _, err := newNetworkSetupIntentFrom(strings.NewReader("short")); err == nil {
		t.Fatal("newNetworkSetupIntentFrom(short entropy) error = nil")
	}
}

// TestAddProjectPreservesPickerAndRegistrationOutcomes covers every user-visible boundary in the native selection flow.
func TestAddProjectPreservesPickerAndRegistrationOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("disconnected", func(t *testing.T) {
		app := testApp()
		chosen := false
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			chosen = true
			return "/workspace/orders", nil
		}

		if _, err := app.AddProject(); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("AddProject() error = %v, want disconnected", err)
		}
		if chosen {
			t.Fatal("AddProject() opened a picker without a daemon connection")
		}
	})

	t.Run("canceled", func(t *testing.T) {
		app, client := connectedTestApp()
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) { return "", nil }

		result, err := app.AddProject()
		if err != nil {
			t.Fatalf("AddProject() error = %v", err)
		}
		if !result.Canceled || result.Registration != nil {
			t.Fatalf("AddProject() = %+v, want canceled result", result)
		}
		if client.registerPath != "" {
			t.Fatalf("RegisterProject() path = %q after cancel", client.registerPath)
		}
	})

	t.Run("picker failure", func(t *testing.T) {
		app, _ := connectedTestApp()
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			return "", errors.New("dialog unavailable")
		}

		if _, err := app.AddProject(); err == nil || !strings.Contains(err.Error(), "dialog unavailable") {
			t.Fatalf("AddProject() error = %v, want picker failure", err)
		}
	})

	t.Run("registration failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.registerErr = errors.New("not a GoForj project")
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			return "/workspace/orders", nil
		}

		if _, err := app.AddProject(); err == nil || !strings.Contains(err.Error(), "not a GoForj project") {
			t.Fatalf("AddProject() error = %v, want registration failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.registration = control.ProjectRegistration{}
		app.choose = func(context.Context, runtime.OpenDialogOptions) (string, error) {
			return "/workspace/orders", nil
		}

		if _, err := app.AddProject(); err == nil || !strings.Contains(err.Error(), "validate project registration") {
			t.Fatalf("AddProject() error = %v, want validation failure", err)
		}
	})

	t.Run("registered", func(t *testing.T) {
		app, client := connectedTestApp()
		var options runtime.OpenDialogOptions
		app.choose = func(_ context.Context, selectedOptions runtime.OpenDialogOptions) (string, error) {
			options = selectedOptions
			return "/workspace/orders", nil
		}

		result, err := app.AddProject()
		if err != nil {
			t.Fatalf("AddProject() error = %v", err)
		}
		if result.Canceled || result.Registration == nil || result.Registration.Project.Name != "Orders" {
			t.Fatalf("AddProject() = %+v, want Orders registration", result)
		}
		if client.registerPath != "/workspace/orders" {
			t.Fatalf("RegisterProject() path = %q, want selected path", client.registerPath)
		}
		if options.Title != "Add a GoForj project" || !options.ResolvesAliases || options.CanCreateDirectories {
			t.Fatalf("picker options = %+v, want reviewed project-directory settings", options)
		}
	})
}

// TestRemoveProjectPreservesStableIdentityAndOperationState covers the complete native removal boundary.
func TestRemoveProjectPreservesStableIdentityAndOperationState(t *testing.T) {
	t.Parallel()

	t.Run("invalid request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.RemoveProject("", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "project ID") {
			t.Fatalf("RemoveProject() error = %v, want invalid project", err)
		}
		if client.unregisterRequest != (control.UnregisterProjectRequest{}) {
			t.Fatalf("UnregisterProject() request = %+v after local validation", client.unregisterRequest)
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		if _, err := testApp().RemoveProject("orders", "desktop-remove-orders"); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("RemoveProject() error = %v, want disconnected", err)
		}
	})

	t.Run("daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregisterErr = errors.New("project is busy")
		if _, err := app.RemoveProject("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "project is busy") {
			t.Fatalf("RemoveProject() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.unregistration = control.ProjectUnregistration{}
		if _, err := app.RemoveProject("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "validate project removal") {
			t.Fatalf("RemoveProject() error = %v, want invalid operation", err)
		}
	})

	t.Run("mismatched daemon result", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*control.ProjectUnregistration)
		}{
			{name: "project", mutate: func(result *control.ProjectUnregistration) { result.Operation.ProjectID = "other" }},
			{name: "intent", mutate: func(result *control.ProjectUnregistration) { result.Operation.IntentID = "desktop-remove-other" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				app, client := connectedTestApp()
				test.mutate(&client.unregistration)
				if _, err := app.RemoveProject("orders", "desktop-remove-orders"); err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("RemoveProject() error = %v, want correlation failure", err)
				}
			})
		}
	})

	t.Run("started", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.RemoveProject("orders", "desktop-remove-orders")
		if err != nil {
			t.Fatalf("RemoveProject() error = %v", err)
		}
		wantRequest := control.UnregisterProjectRequest{ProjectID: "orders", IntentID: "desktop-remove-orders"}
		if client.unregisterRequest != wantRequest {
			t.Fatalf("UnregisterProject() request = %+v, want %+v", client.unregisterRequest, wantRequest)
		}
		if result.Operation.State != domain.OperationQueued || result.Revision != 9 {
			t.Fatalf("RemoveProject() = %+v, want queued operation at revision 9", result)
		}
	})
}

// TestProjectLifecyclePreservesActionIdentityAndOperationState covers both native lifecycle boundaries.
func TestProjectLifecyclePreservesActionIdentityAndOperationState(t *testing.T) {
	t.Parallel()

	t.Run("invalid start request", func(t *testing.T) {
		app, client := connectedTestApp()
		if _, err := app.StartProject("", "desktop-start-orders"); err == nil || !strings.Contains(err.Error(), "project ID") {
			t.Fatalf("StartProject() error = %v, want invalid project", err)
		}
		if client.startRequest != (control.StartProjectRequest{}) {
			t.Fatalf("StartProject() request = %+v after local validation", client.startRequest)
		}
	})

	t.Run("disconnected stop", func(t *testing.T) {
		if _, err := testApp().StopProject("orders", "desktop-stop-orders"); !errors.Is(err, errDaemonDisconnected) {
			t.Fatalf("StopProject() error = %v, want disconnected", err)
		}
	})

	t.Run("daemon failure", func(t *testing.T) {
		app, client := connectedTestApp()
		client.startErr = errors.New("project is busy")
		if _, err := app.StartProject("orders", "desktop-start-orders"); err == nil || !strings.Contains(err.Error(), "project is busy") {
			t.Fatalf("StartProject() error = %v, want daemon failure", err)
		}
	})

	t.Run("invalid daemon result", func(t *testing.T) {
		app, client := connectedTestApp()
		client.stopLifecycle = control.ProjectLifecycleOperation{}
		if _, err := app.StopProject("orders", "desktop-stop-orders"); err == nil || !strings.Contains(err.Error(), "validate project stop") {
			t.Fatalf("StopProject() error = %v, want invalid operation", err)
		}
	})

	t.Run("mismatched daemon result", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(*control.ProjectLifecycleOperation)
		}{
			{name: "kind", mutate: func(result *control.ProjectLifecycleOperation) {
				result.Operation.Kind = domain.OperationKindProjectStop
			}},
			{name: "project", mutate: func(result *control.ProjectLifecycleOperation) { result.Operation.ProjectID = "other" }},
			{name: "intent", mutate: func(result *control.ProjectLifecycleOperation) { result.Operation.IntentID = "desktop-start-other" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				app, client := connectedTestApp()
				test.mutate(&client.startLifecycle)
				if _, err := app.StartProject("orders", "desktop-start-orders"); err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("StartProject() error = %v, want correlation failure", err)
				}
			})
		}
	})

	t.Run("started", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.StartProject("orders", "desktop-start-orders")
		if err != nil {
			t.Fatalf("StartProject() error = %v", err)
		}
		wantRequest := control.StartProjectRequest{ProjectID: "orders", IntentID: "desktop-start-orders"}
		if client.startRequest != wantRequest {
			t.Fatalf("StartProject() request = %+v, want %+v", client.startRequest, wantRequest)
		}
		if result.Operation.Kind != domain.OperationKindProjectStart || result.Revision != 9 {
			t.Fatalf("StartProject() = %+v, want queued start at revision 9", result)
		}
	})

	t.Run("stopped", func(t *testing.T) {
		app, client := connectedTestApp()
		result, err := app.StopProject("orders", "desktop-stop-orders")
		if err != nil {
			t.Fatalf("StopProject() error = %v", err)
		}
		wantRequest := control.StopProjectRequest{ProjectID: "orders", IntentID: "desktop-stop-orders"}
		if client.stopRequest != wantRequest {
			t.Fatalf("StopProject() request = %+v, want %+v", client.stopRequest, wantRequest)
		}
		if result.Operation.Kind != domain.OperationKindProjectStop || result.Revision != 9 {
			t.Fatalf("StopProject() = %+v, want queued stop at revision 9", result)
		}
	})
}

// TestOpenResourceUsesFreshProjectScopedState proves JavaScript cannot supply a URL or rely on a globally unique resource ID.
func TestOpenResourceUsesFreshProjectScopedState(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	secondProject := client.snapshot.Projects[0]
	secondProject.Apps = append([]domain.AppSnapshot(nil), secondProject.Apps...)
	secondProject.Services = append([]domain.ServiceSnapshot(nil), secondProject.Services...)
	secondProject.Resources = append([]domain.ResourceSnapshot(nil), secondProject.Resources...)
	secondProject.ID = "billing"
	secondProject.Name = "Billing"
	secondProject.Path = "/workspace/billing"
	secondProject.Slug = "billing"
	secondProject.Resources[0].URL = "https://billing.example.test"
	client.snapshot.Projects = append(client.snapshot.Projects, secondProject)
	var opened string
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return client, nil },
		func(context.Context, string, ...interface{}) {},
		func(_ context.Context, target string) { opened = target },
		func(context.Context, time.Duration) bool { return false },
	)
	app.ctx = context.Background()
	app.client = client

	if err := app.OpenResource("billing", "web"); err != nil {
		t.Fatalf("OpenResource() error = %v", err)
	}
	if opened != "https://billing.example.test" {
		t.Fatalf("opened URL = %q, want billing resource", opened)
	}
}

// TestOpenResourceRejectsInvalidAndMissingIdentities covers every client-controlled lookup boundary.
func TestOpenResourceRejectsInvalidAndMissingIdentities(t *testing.T) {
	t.Parallel()

	client := newFakeControlClient()
	app := testApp()
	if err := app.OpenResource("orders", "web"); !errors.Is(err, errDaemonDisconnected) {
		t.Fatalf("OpenResource() disconnected error = %v, want disconnected", err)
	}
	app.ctx = context.Background()
	app.client = client

	tests := []struct {
		name       string
		projectID  string
		resourceID string
		want       string
	}{
		{name: "invalid project", resourceID: "web", want: "project ID"},
		{name: "invalid resource", projectID: "orders", want: "resource ID"},
		{name: "missing project", projectID: "billing", resourceID: "web", want: "project \"billing\" was not found"},
		{name: "missing resource", projectID: "orders", resourceID: "mail", want: "resource \"mail\" was not found"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := app.OpenResource(test.projectID, test.resourceID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenResource() error = %v, want containing %q", err, test.want)
			}
		})
	}

	client.snapshotErr = errors.New("read failed")
	if err := app.OpenResource("orders", "web"); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("OpenResource() read error = %v, want wrapped failure", err)
	}
	client.snapshotErr = nil
	client.snapshot.Projects = nil
	if err := app.OpenResource("orders", "web"); err == nil || !strings.Contains(err.Error(), "validate Harbor snapshot") {
		t.Fatalf("OpenResource() validation error = %v, want invalid snapshot", err)
	}
}

// TestEmitSnapshotRejectsInvalidUnchangedAndStaleState prevents polling from repeatedly publishing one durable revision.
func TestEmitSnapshotRejectsInvalidUnchangedAndStaleState(t *testing.T) {
	t.Parallel()

	var sequences []domain.Sequence
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return newFakeControlClient(), nil },
		func(_ context.Context, _ string, values ...interface{}) {
			sequences = append(sequences, values[0].(domain.Snapshot).Sequence)
		},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)

	snapshot := testSnapshot()
	cursor := snapshotCursor{}
	app.emitSnapshot(context.Background(), snapshot, &cursor)
	app.emitSnapshot(context.Background(), snapshot, &cursor)
	stale := snapshot
	stale.Sequence--
	app.emitSnapshot(context.Background(), stale, &cursor)
	invalid := snapshot
	invalid.SchemaVersion = 0
	app.emitSnapshot(context.Background(), invalid, &cursor)

	newer := snapshot
	newer.Sequence++
	app.emitSnapshot(context.Background(), newer, &cursor)

	if len(sequences) != 2 || sequences[0] != 8 || sequences[1] != 9 {
		t.Fatalf("emitted sequences = %v, want [8 9]", sequences)
	}
}

// TestEmitSnapshotResetsOrderingForEachNegotiatedConnection prevents a restarted daemon from being hidden by an old cursor.
func TestEmitSnapshotResetsOrderingForEachNegotiatedConnection(t *testing.T) {
	t.Parallel()

	var sequences []domain.Sequence
	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return newFakeControlClient(), nil },
		func(_ context.Context, event string, values ...interface{}) {
			if event == snapshotEventName {
				sequences = append(sequences, values[0].(domain.Snapshot).Sequence)
			}
		},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)

	snapshot := testSnapshot()
	firstConnection := snapshotCursor{}
	secondConnection := snapshotCursor{}
	app.emitSnapshot(context.Background(), snapshot, &firstConnection)
	app.emitSnapshot(context.Background(), snapshot, &secondConnection)

	if len(sequences) != 2 || sequences[0] != snapshot.Sequence || sequences[1] != snapshot.Sequence {
		t.Fatalf("emitted sequences = %v, want the first revision from both connections", sequences)
	}
}

// TestRunRejectsNilFactoryResult keeps a broken composition from becoming a silent reconnect loop.
func TestRunRejectsNilFactoryResult(t *testing.T) {
	t.Parallel()

	app := newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return nil, nil },
		func(context.Context, string, ...interface{}) {},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)
	app.ctx = context.Background()
	done := make(chan struct{})
	defer func() {
		if recover() == nil {
			t.Fatal("run() panic = nil, want nil client failure")
		}
		select {
		case <-done:
		default:
			t.Fatal("run() did not close its completion channel")
		}
	}()
	app.run(context.Background(), done)
}

// TestInstallAndRemoveClientHonorLifecycleOwnership covers cancellation and replacement-safe cleanup.
func TestInstallAndRemoveClientHonorLifecycleOwnership(t *testing.T) {
	t.Parallel()

	app := testApp()
	client := newFakeControlClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.ctx = context.Background()
	if app.installClient(ctx, client) {
		t.Fatal("installClient() = true for cancelled lifecycle")
	}
	if !app.installClient(context.Background(), client) {
		t.Fatal("installClient() = false for active lifecycle")
	}
	other := newFakeControlClient()
	app.removeClient(other)
	if app.client != client {
		t.Fatal("removeClient() cleared a different connection")
	}
	app.removeClient(client)
	if app.client != nil {
		t.Fatal("removeClient() retained the owned connection")
	}
}

// TestWaitForContextCoversTimerAndCancellation proves production waits cannot delay shutdown.
func TestWaitForContextCoversTimerAndCancellation(t *testing.T) {
	t.Parallel()

	if !waitForContext(context.Background(), time.Millisecond) {
		t.Fatal("waitForContext() = false after timer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForContext(ctx, time.Hour) {
		t.Fatal("waitForContext() = true after cancellation")
	}
}

// testApp creates an adapter whose side effects are inert until a test installs a fake connection.
func testApp() *App {
	return newApp(
		func(context.Context, control.ClientConfig) (controlClient, error) { return newFakeControlClient(), nil },
		func(context.Context, string, ...interface{}) {},
		func(context.Context, string) {},
		func(context.Context, time.Duration) bool { return false },
	)
}

// connectedTestApp creates a desktop adapter with one installed fake daemon connection.
func connectedTestApp() (*App, *fakeControlClient) {
	app := testApp()
	client := newFakeControlClient()
	app.ctx = context.Background()
	app.client = client
	return app, client
}

// testSnapshot returns the smallest valid snapshot with one project-scoped HTTP resource.
func testSnapshot() domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:      8,
		CapturedAt:    time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		Projects: []domain.ProjectSnapshot{
			{
				ID:        "orders",
				Name:      "Orders",
				Path:      "/workspace/orders",
				Slug:      "orders",
				State:     domain.ProjectReady,
				UpdatedAt: time.Date(2026, time.July, 18, 11, 0, 0, 0, time.UTC),
				Apps: []domain.AppSnapshot{
					{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true},
				},
				Services: []domain.ServiceSnapshot{},
				Resources: []domain.ResourceSnapshot{
					{
						ID:    "web",
						Name:  "Web",
						Kind:  "application",
						Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
						URL:   "https://orders.example.test",
					},
				},
			},
		},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{{ProjectID: "orders", ResourceID: "web"}},
	}
}

// testNetworkSetupOperation returns one valid singleton setup operation at the requested lifecycle state and revision.
func testNetworkSetupOperation(state domain.OperationState, revision domain.Sequence) control.NetworkSetupOperation {
	requestedAt := time.Date(2026, time.July, 18, 12, 10, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Second)
	finishedAt := requestedAt.Add(2 * time.Second)
	operation := domain.Operation{
		ID:          "operation-network-setup",
		IntentID:    networkSetupIntentID,
		Kind:        domain.OperationKindNetworkSetup,
		State:       state,
		Phase:       string(state),
		RequestedAt: requestedAt,
	}
	switch state {
	case domain.OperationRunning, domain.OperationRequiresApproval:
		operation.StartedAt = &startedAt
	case domain.OperationSucceeded:
		operation.StartedAt = &startedAt
		operation.FinishedAt = &finishedAt
	}
	return control.NetworkSetupOperation{Operation: operation, Revision: revision}
}

// testNetworkSetupConfirmation advances one approval operation to a valid completed confirmation.
func testNetworkSetupConfirmation(
	setup control.NetworkSetupOperation,
	networkRevision domain.Sequence,
	revision domain.Sequence,
) control.NetworkSetupApprovalConfirmation {
	operation := setup.Operation
	finishedAt := operation.RequestedAt.Add(2 * time.Second)
	operation.State = domain.OperationSucceeded
	operation.Phase = string(domain.OperationSucceeded)
	operation.FinishedAt = &finishedAt
	return control.NetworkSetupApprovalConfirmation{
		Operation:       operation,
		Revision:        revision,
		NetworkRevision: networkRevision,
		Pool:            "127.42.0.0/29",
	}
}

// testRegistration returns a valid inert registration without claiming network-backed resources.
func testRegistration() control.ProjectRegistration {
	project := testSnapshot().Projects[0]
	project.State = domain.ProjectStopped
	project.Apps = []domain.AppSnapshot{}
	project.Services = []domain.ServiceSnapshot{}
	project.Resources = []domain.ResourceSnapshot{}
	return control.ProjectRegistration{
		Project:  project,
		Revision: 9,
		Created:  true,
	}
}

// testUnregistration returns a valid queued operation before project-specific cleanup has been observed.
func testUnregistration() control.ProjectUnregistration {
	return control.ProjectUnregistration{
		Operation: domain.Operation{
			ID:          "operation-remove-orders",
			IntentID:    "desktop-remove-orders",
			Kind:        domain.OperationKindProjectUnregister,
			ProjectID:   "orders",
			State:       domain.OperationQueued,
			Phase:       string(domain.OperationQueued),
			RequestedAt: time.Date(2026, time.July, 18, 12, 5, 0, 0, time.UTC),
		},
		Revision: 9,
	}
}

// testProjectLifecycle returns a valid queued operation for one exact desktop lifecycle intent.
func testProjectLifecycle(kind domain.OperationKind, intentID domain.IntentID) control.ProjectLifecycleOperation {
	return control.ProjectLifecycleOperation{
		Operation: domain.Operation{
			ID:          domain.OperationID("operation-" + string(kind)),
			IntentID:    intentID,
			Kind:        kind,
			ProjectID:   "orders",
			State:       domain.OperationQueued,
			Phase:       string(domain.OperationQueued),
			RequestedAt: time.Date(2026, time.July, 18, 12, 5, 0, 0, time.UTC),
		},
		Revision: 9,
	}
}

// cloneSnapshot copies the nested collections a test may mutate after a fake response.
func cloneSnapshot(snapshot domain.Snapshot) domain.Snapshot {
	copySnapshot := snapshot
	copySnapshot.Projects = make([]domain.ProjectSnapshot, len(snapshot.Projects))
	copy(copySnapshot.Projects, snapshot.Projects)
	for index := range copySnapshot.Projects {
		copySnapshot.Projects[index].Apps = make([]domain.AppSnapshot, len(snapshot.Projects[index].Apps))
		copy(copySnapshot.Projects[index].Apps, snapshot.Projects[index].Apps)
		copySnapshot.Projects[index].Services = make([]domain.ServiceSnapshot, len(snapshot.Projects[index].Services))
		copy(copySnapshot.Projects[index].Services, snapshot.Projects[index].Services)
		copySnapshot.Projects[index].Resources = make([]domain.ResourceSnapshot, len(snapshot.Projects[index].Resources))
		copy(copySnapshot.Projects[index].Resources, snapshot.Projects[index].Resources)
	}
	copySnapshot.Operations = make([]domain.Operation, len(snapshot.Operations))
	copy(copySnapshot.Operations, snapshot.Operations)
	copySnapshot.RecentResourceIDs = make([]domain.ResourceRef, len(snapshot.RecentResourceIDs))
	copy(copySnapshot.RecentResourceIDs, snapshot.RecentResourceIDs)
	return copySnapshot
}
