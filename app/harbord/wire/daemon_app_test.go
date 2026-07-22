package wire

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/logger"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingLifecycleNetworkRuntime captures composite startup and cleanup without opening network listeners.
type recordingLifecycleNetworkRuntime struct {
	startErr  error
	closeErr  error
	started   bool
	closed    bool
	done      chan struct{}
	closeOnce sync.Once
}

// Start returns the configured startup failure.
func (runtime *recordingLifecycleNetworkRuntime) Start(context.Context) error {
	runtime.started = true
	return runtime.startErr
}

// Done returns a stable terminal channel for the daemon runtime contract.
func (runtime *recordingLifecycleNetworkRuntime) Done() <-chan struct{} {
	return runtime.done
}

// Err returns no independent terminal failure for this startup fixture.
func (runtime *recordingLifecycleNetworkRuntime) Err() error {
	return nil
}

// Close records joined network cleanup after a post-start lifecycle failure.
func (runtime *recordingLifecycleNetworkRuntime) Close(context.Context) error {
	runtime.closeOnce.Do(func() {
		runtime.closed = true
		close(runtime.done)
	})
	return runtime.closeErr
}

// recordingLifecycleCloser proves a recovered process coordinator is joined after network startup rejection.
type recordingLifecycleCloser struct {
	resumed   bool
	resumeErr error
	onResume  func() error
	closed    bool
	closeErr  error
	done      chan struct{}
	closeOnce sync.Once
}

// recordingProjectUnregisterRecovery records the first daemon recovery boundary and returns its configured failure.
type recordingProjectUnregisterRecovery struct {
	events *[]string
	err    error
}

// Recover records project-removal recovery before any lifecycle or endpoint work.
func (recovery *recordingProjectUnregisterRecovery) Recover(context.Context) error {
	*recovery.events = append(*recovery.events, "unregister.recover")
	return recovery.err
}

// recordingProjectLifecycleRecovery records process recovery and the subsequent full-stage endpoint backfill.
type recordingProjectLifecycleRecovery struct {
	events        *[]string
	recoverErr    error
	endpointErr   error
	endpointState state.NetworkRecord
}

// Recover records durable process-lifecycle recovery before endpoint authority can advance.
func (recovery *recordingProjectLifecycleRecovery) Recover(context.Context) error {
	*recovery.events = append(*recovery.events, "lifecycle.recover")
	return recovery.recoverErr
}

// ReconcileFullStageDefaultHTTPEndpoints records the last durable recovery step before runtime reconciliation.
func (recovery *recordingProjectLifecycleRecovery) ReconcileFullStageDefaultHTTPEndpoints(
	context.Context,
) (state.NetworkRecord, error) {
	*recovery.events = append(*recovery.events, "endpoints.reconcile")
	return recovery.endpointState, recovery.endpointErr
}

// recordingNetworkResolverObserver fails any native read because coordinator assembly must remain side-effect free.
type recordingNetworkResolverObserver struct {
	calls int
}

// Observe records an unexpected native resolver read during dependency assembly.
func (observer *recordingNetworkResolverObserver) Observe(
	context.Context,
	resolver.Request,
) (resolver.Observation, error) {
	observer.calls++
	return resolver.Observation{}, errors.New("resolver observer must remain lazy")
}

// Resume records recovered-operation dispatch after network startup and returns its configured result.
func (closer *recordingLifecycleCloser) Resume(context.Context) error {
	closer.resumed = true
	if closer.onResume != nil {
		return errors.Join(closer.resumeErr, closer.onResume())
	}
	return closer.resumeErr
}

// Close records joined lifecycle cleanup and returns its configured result.
func (closer *recordingLifecycleCloser) Close(context.Context) error {
	closer.closeOnce.Do(func() {
		closer.closed = true
		close(closer.done)
	})
	return closer.closeErr
}

// Done closes after the recording lifecycle has joined cleanup.
func (closer *recordingLifecycleCloser) Done() <-chan struct{} {
	return closer.done
}

// Err returns the configured lifecycle cleanup failure.
func (closer *recordingLifecycleCloser) Err() error {
	return closer.closeErr
}

// TestProvideHarbordReadinessIsLazy verifies assembly does not touch durable state before daemon authority is requested.
func TestProvideHarbordReadinessIsLazy(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath)
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close database connections: %v", err)
		}
	})

	readiness, err := provideHarbordReadiness(connections)
	if err != nil {
		t.Fatalf("provideHarbordReadiness() error = %v", err)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database exists after readiness assembly: %v", err)
	}

	err = readiness(t.Context())
	if err == nil || !strings.Contains(err.Error(), "migrations are not ready") {
		t.Fatalf("readiness error = %v, want missing migration ledger", err)
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("database was not opened by readiness invocation: %v", err)
	}
}

// TestProvideHarbordReadinessRejectsMissingConnections verifies invalid assembly fails before foreground authority can be acquired.
func TestProvideHarbordReadinessRejectsMissingConnections(t *testing.T) {
	readiness, err := provideHarbordReadiness(nil)
	if err == nil || readiness != nil {
		t.Fatalf("provideHarbordReadiness(nil) = (%v, %v), want nil readiness and construction error", readiness, err)
	}
}

// TestRecoverDaemonStateBackfillsFullEndpointsBeforeRuntimeReconciliation pins the complete startup recovery order.
func TestRecoverDaemonStateBackfillsFullEndpointsBeforeRuntimeReconciliation(t *testing.T) {
	events := []string{}
	unregister := &recordingProjectUnregisterRecovery{events: &events}
	lifecycle := &recordingProjectLifecycleRecovery{
		events:        &events,
		endpointState: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 42},
	}

	if err := recoverDaemonState(t.Context(), unregister, lifecycle); err != nil {
		t.Fatalf("recoverDaemonState() error = %v", err)
	}
	// daemon.Runner invokes Runtime.Start only after this recovery callback returns.
	events = append(events, "runtime.reconcile")
	if got, want := strings.Join(events, ","), "unregister.recover,lifecycle.recover,endpoints.reconcile,runtime.reconcile"; got != want {
		t.Fatalf("daemon recovery order = %q, want %q", got, want)
	}
}

// TestRecoverDaemonStatePropagatesEachFailure prevents later recovery or runtime work from crossing a failed boundary.
func TestRecoverDaemonStatePropagatesEachFailure(t *testing.T) {
	unregisterErr := errors.New("unregister recovery failed")
	lifecycleErr := errors.New("lifecycle recovery failed")
	endpointErr := errors.New("endpoint backfill failed")
	tests := []struct {
		name          string
		unregisterErr error
		lifecycleErr  error
		endpointErr   error
		want          error
		wantEvents    string
	}{
		{
			name:          "unregister",
			unregisterErr: unregisterErr,
			want:          unregisterErr,
			wantEvents:    "unregister.recover",
		},
		{
			name:         "lifecycle",
			lifecycleErr: lifecycleErr,
			want:         lifecycleErr,
			wantEvents:   "unregister.recover,lifecycle.recover",
		},
		{
			name:        "endpoint",
			endpointErr: endpointErr,
			want:        endpointErr,
			wantEvents:  "unregister.recover,lifecycle.recover,endpoints.reconcile",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			unregister := &recordingProjectUnregisterRecovery{events: &events, err: test.unregisterErr}
			lifecycle := &recordingProjectLifecycleRecovery{
				events:      &events,
				recoverErr:  test.lifecycleErr,
				endpointErr: test.endpointErr,
			}

			err := recoverDaemonState(t.Context(), unregister, lifecycle)
			if !errors.Is(err, test.want) {
				t.Fatalf("recoverDaemonState() error = %v, want %v", err, test.want)
			}
			if got := strings.Join(events, ","); got != test.wantEvents {
				t.Fatalf("recovery events = %q, want %q", got, test.wantEvents)
			}
		})
	}
}

// TestInitializeApplicationWiresForegroundServices verifies Wire constructs the complete production daemon dependency graph lazily.
func TestInitializeApplicationWiresForegroundServices(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath)

	application, err := InitializeApplication(projectprocess.CaptureEnvironment())
	if err != nil {
		t.Fatalf("InitializeApplication() error = %v", err)
	}
	if application.RootCmd() == nil {
		t.Fatal("InitializeApplication() returned no root command")
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database exists after application assembly: %v", err)
	}

	parser, err := kong.New(application.RootCmd(), kong.Name("harbord"))
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}
	if _, err := parser.Parse([]string{"--foreground", "about"}); err == nil || !strings.Contains(err.Error(), "--foreground cannot be combined") {
		t.Fatalf("production foreground conflict error = %v", err)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("foreground parsing touched the database before daemon execution: %v", err)
	}
}

// TestProvideProjectUnregisterCoordinatorIsLazy proves production assembly retains default issuer stores behind the issuer factory.
func TestProvideProjectUnregisterCoordinatorIsLazy(t *testing.T) {
	store := new(state.Store)
	operations := new(state.OperationJournal)
	plans := new(state.HelperApprovalPlanSource)
	ownership := new(state.MachineOwnershipProjectionSource)
	runtimeController, err := harbordruntime.NewController(store)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	issuerOpenCalls := 0
	coordinator, err := provideProjectUnregisterCoordinatorWithIssuerOpener(
		store,
		operations,
		plans,
		ownership,
		runtimeController,
		func(ticketissuer.PlanSource, *state.MachineOwnershipProjectionSource) (reconcile.TicketIssuer, error) {
			issuerOpenCalls++
			return nil, errors.New("issuer opener must remain lazy")
		},
	)
	if err != nil {
		t.Fatalf("provideProjectUnregisterCoordinatorWithIssuerOpener() error = %v", err)
	}
	if coordinator == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener() returned nil coordinator")
	}
	if issuerOpenCalls != 0 {
		t.Fatalf("issuer opener calls after coordinator assembly = %d, want 0", issuerOpenCalls)
	}
}

// TestProvideNetworkSetupCoordinatorIsLazy proves assembly does not create keys, open the spool, or scan host pools.
func TestProvideNetworkSetupCoordinatorIsLazy(t *testing.T) {
	keyOpenCalls := 0
	issuerOpenCalls := 0
	coordinator, err := provideNetworkSetupCoordinatorWithOpeners(
		new(state.Store),
		new(state.OperationJournal),
		new(state.NetworkSetupPlanSource),
		new(state.MachineOwnershipProjectionSource),
		func() (reconcile.SigningKeyStore, error) {
			keyOpenCalls++
			return nil, errors.New("signing-key opener must remain lazy")
		},
		func(ticketissuer.PoolPlanSource) (reconcile.PoolIssuer, error) {
			issuerOpenCalls++
			return nil, errors.New("pool issuer opener must remain lazy")
		},
	)
	if err != nil {
		t.Fatalf("provideNetworkSetupCoordinatorWithOpeners() error = %v", err)
	}
	if coordinator == nil {
		t.Fatal("provideNetworkSetupCoordinatorWithOpeners() returned nil coordinator")
	}
	if keyOpenCalls != 0 || issuerOpenCalls != 0 {
		t.Fatalf("network setup opener calls after assembly = keys %d, issuer %d; want zero", keyOpenCalls, issuerOpenCalls)
	}
}

// TestProvideNetworkResolverSetupCoordinatorIsLazy proves assembly does not open capability stores or observe native policy.
func TestProvideNetworkResolverSetupCoordinatorIsLazy(t *testing.T) {
	store := new(state.Store)
	runtimeController, err := harbordruntime.NewController(store)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	observer := new(recordingNetworkResolverObserver)
	issuerOpenCalls := 0
	coordinator, err := provideNetworkResolverSetupCoordinatorWithIssuerOpener(
		store,
		new(state.OperationJournal),
		new(state.NetworkResolverSetupPlanSource),
		new(state.MachineOwnershipProjectionSource),
		runtimeController,
		observer,
		networkplan.PlatformUbuntu2404,
		func(
			*state.NetworkResolverSetupPlanSource,
			*state.MachineOwnershipProjectionSource,
			reconcile.NetworkResolverSetupResolverObserver,
		) (reconcile.NetworkResolverSetupIssuer, error) {
			issuerOpenCalls++
			return nil, errors.New("resolver issuer opener must remain lazy")
		},
	)
	if err != nil {
		t.Fatalf("provideNetworkResolverSetupCoordinatorWithIssuerOpener() error = %v", err)
	}
	if coordinator == nil {
		t.Fatal("provideNetworkResolverSetupCoordinatorWithIssuerOpener() returned nil coordinator")
	}
	if issuerOpenCalls != 0 || observer.calls != 0 {
		t.Fatalf("resolver setup effects after assembly = issuer %d, observer %d; want zero", issuerOpenCalls, observer.calls)
	}
}

// TestProvideNetworkSetupCoordinatorRejectsIncompleteAssembly covers every required production dependency.
func TestProvideNetworkSetupCoordinatorRejectsIncompleteAssembly(t *testing.T) {
	store := new(state.Store)
	operations := new(state.OperationJournal)
	plans := new(state.NetworkSetupPlanSource)
	ownership := new(state.MachineOwnershipProjectionSource)
	openKeys := func() (reconcile.SigningKeyStore, error) {
		return nil, errors.New("unused signing-key opener")
	}
	openIssuer := func(ticketissuer.PoolPlanSource) (reconcile.PoolIssuer, error) {
		return nil, errors.New("unused pool issuer opener")
	}
	tests := []struct {
		name string
		call func() error
	}{
		{name: "store", call: func() error {
			_, err := provideNetworkSetupCoordinatorWithOpeners(nil, operations, plans, ownership, openKeys, openIssuer)
			return err
		}},
		{name: "operations", call: func() error {
			_, err := provideNetworkSetupCoordinatorWithOpeners(store, nil, plans, ownership, openKeys, openIssuer)
			return err
		}},
		{name: "plans", call: func() error {
			_, err := provideNetworkSetupCoordinatorWithOpeners(store, operations, nil, ownership, openKeys, openIssuer)
			return err
		}},
		{name: "ownership", call: func() error {
			_, err := provideNetworkSetupCoordinatorWithOpeners(store, operations, plans, nil, openKeys, openIssuer)
			return err
		}},
		{name: "keys", call: func() error {
			_, err := provideNetworkSetupCoordinatorWithOpeners(store, operations, plans, ownership, nil, openIssuer)
			return err
		}},
		{name: "issuer", call: func() error {
			_, err := provideNetworkSetupCoordinatorWithOpeners(store, operations, plans, ownership, openKeys, nil)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("provideNetworkSetupCoordinatorWithOpeners() error = nil")
			}
		})
	}
}

// TestControlErrorObserverRetainsRedactedCauseContext verifies daemon diagnostics keep the failure omitted from IPC responses.
func TestControlErrorObserverRetainsRedactedCauseContext(t *testing.T) {
	t.Setenv("APP_LOG_FORMAT", "json")
	appLogger := logger.NewAppLogger()
	entries := make([]logger.LogEntry, 0, 1)
	appLogger.AddSink(func(entry logger.LogEntry) {
		entries = append(entries, entry)
	})
	observer := newControlErrorObserver(appLogger)
	cause := errors.New("select loopback pool: native route inspection failed")
	observer(control.Caller{
		Transport: local.PeerIdentity{UserID: "501", ProcessID: 1201},
		Session:   session.Peer{Role: rpc.RoleDesktop},
	}, "network.setup.start", cause)

	if len(entries) != 1 {
		t.Fatalf("control diagnostic entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Level != "error" || entry.Message != "Harbor control request failed" {
		t.Fatalf("control diagnostic = %#v", entry)
	}
	wantFields := map[string]any{
		"error":           cause.Error(),
		"control_method":  "network.setup.start",
		"peer_role":       string(rpc.RoleDesktop),
		"peer_user_id":    "501",
		"peer_process_id": uint64(1201),
	}
	for name, want := range wantFields {
		if got := entry.Fields[name]; got != want {
			t.Errorf("control diagnostic field %q = %#v, want %#v", name, got, want)
		}
	}
}

// TestControlErrorObserverReportsMissingHelperAsSetupSignal keeps handled first-run onboarding out of daemon error telemetry.
func TestControlErrorObserverReportsMissingHelperAsSetupSignal(t *testing.T) {
	t.Setenv("APP_LOG_FORMAT", "json")
	appLogger := logger.NewAppLogger()
	entries := make([]logger.LogEntry, 0, 1)
	appLogger.AddSink(func(entry logger.LogEntry) {
		entries = append(entries, entry)
	})
	observer := newControlErrorObserver(appLogger)
	observer(control.Caller{
		Transport: local.PeerIdentity{UserID: "501", ProcessID: 1201},
		Session:   session.Peer{Role: rpc.RoleDesktop},
	}, "control.v1.network.setup.approval.prepare", &ticketissuer.PoolPrerequisiteMissingError{})

	if len(entries) != 1 {
		t.Fatalf("control diagnostic entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Level != "info" || entry.Message != "Harbor control request requires privileged setup" {
		t.Fatalf("control setup diagnostic = %#v", entry)
	}
	if got := entry.Fields["error"]; got != "helper pool prerequisite is missing" {
		t.Fatalf("control setup diagnostic error = %#v", got)
	}
}

// TestControlErrorObserverSuppressesOnlyCancellationFanOut keeps connection retirement quiet without hiding joined failures.
func TestControlErrorObserverSuppressesOnlyCancellationFanOut(t *testing.T) {
	t.Setenv("APP_LOG_FORMAT", "json")
	appLogger := logger.NewAppLogger()
	entries := make([]logger.LogEntry, 0, 1)
	appLogger.AddSink(func(entry logger.LogEntry) {
		entries = append(entries, entry)
	})
	observer := newControlErrorObserver(appLogger)
	caller := control.Caller{
		Transport: local.PeerIdentity{UserID: "501", ProcessID: 1201},
		Session:   session.Peer{Role: rpc.RoleDesktop},
	}

	observer(caller, "control.v1.daemon.status", fmt.Errorf("read Harbor sequence: %w", context.Canceled))
	observer(caller, "control.v1.project.activity", errors.Join(
		fmt.Errorf("read project Apps: %w", context.Canceled),
		fmt.Errorf("release activity follower: %w", context.Canceled),
	))
	if len(entries) != 0 {
		t.Fatalf("cancellation-only diagnostic entries = %d, want 0", len(entries))
	}

	cleanupFailure := errors.New("release activity follower failed")
	observer(caller, "control.v1.project.activity", errors.Join(context.Canceled, cleanupFailure))
	if len(entries) != 1 {
		t.Fatalf("joined-failure diagnostic entries = %d, want 1", len(entries))
	}
	errorField, ok := entries[0].Fields["error"].(string)
	if entries[0].Level != "error" || !ok || !strings.Contains(errorField, cleanupFailure.Error()) {
		t.Fatalf("joined-failure diagnostic = %#v", entries[0])
	}
}

// TestDaemonProvidersRejectIncompleteAssembly verifies constructor validation remains at the owning production boundaries.
func TestDaemonProvidersRejectIncompleteAssembly(t *testing.T) {
	store := new(state.Store)
	operations := new(state.OperationJournal)
	plans := new(state.HelperApprovalPlanSource)
	ownership := new(state.MachineOwnershipProjectionSource)
	runtimeController, err := harbordruntime.NewController(store)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	openIssuer := func(ticketissuer.PlanSource, *state.MachineOwnershipProjectionSource) (reconcile.TicketIssuer, error) {
		return nil, errors.New("unused test issuer opener")
	}
	coordinator, err := provideProjectUnregisterCoordinatorWithIssuerOpener(
		store,
		operations,
		plans,
		ownership,
		runtimeController,
		openIssuer,
	)
	if err != nil {
		t.Fatalf("provideProjectUnregisterCoordinatorWithIssuerOpener() error = %v", err)
	}
	shutdown := daemon.NewShutdown()
	appLogger := logger.NewSilentLogger()
	if _, err := provideControlServer(nil, shutdown, appLogger); err == nil {
		t.Fatal("provideControlServer(nil) error = nil, want required authority error")
	}
	if _, err := provideControlServer(new(authority.Authority), nil, appLogger); err == nil {
		t.Fatal("provideControlServer(nil shutdown) error = nil, want required shutdown coordinator error")
	}
	if _, err := provideControlServer(new(authority.Authority), shutdown, nil); err == nil {
		t.Fatal("provideControlServer(nil logger) error = nil, want required application logger error")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(nil, operations, plans, ownership, runtimeController, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil store) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, nil, plans, ownership, runtimeController, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil journal) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, operations, nil, ownership, runtimeController, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil plans) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, operations, plans, nil, runtimeController, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil ownership) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, operations, plans, ownership, nil, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil runtime) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, operations, plans, ownership, runtimeController, nil); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil opener) error = nil")
	}
	if _, err := provideDaemonRunner(nil, func(context.Context) error { return nil }, runtimeController, coordinator, new(reconcile.ProjectLifecycleCoordinator), shutdown); err == nil {
		t.Fatal("provideDaemonRunner(nil server) error = nil, want required server error")
	}
	if _, err := provideDaemonRunner(new(control.Server), nil, runtimeController, coordinator, new(reconcile.ProjectLifecycleCoordinator), shutdown); err == nil {
		t.Fatal("provideDaemonRunner(nil readiness) error = nil, want required readiness error")
	}
	if _, err := provideDaemonRunner(new(control.Server), func(context.Context) error { return nil }, nil, coordinator, new(reconcile.ProjectLifecycleCoordinator), shutdown); err == nil {
		t.Fatal("provideDaemonRunner(nil runtime) error = nil, want required runtime error")
	}
	if _, err := provideDaemonRunner(new(control.Server), func(context.Context) error { return nil }, runtimeController, nil, new(reconcile.ProjectLifecycleCoordinator), shutdown); err == nil {
		t.Fatal("provideDaemonRunner(nil coordinator) error = nil, want required coordinator error")
	}
	if _, err := provideDaemonRunner(new(control.Server), func(context.Context) error { return nil }, runtimeController, coordinator, new(reconcile.ProjectLifecycleCoordinator), nil); err == nil {
		t.Fatal("provideDaemonRunner(nil shutdown) error = nil, want required shutdown coordinator error")
	}
	if _, err := provideDaemonRunner(new(control.Server), func(context.Context) error { return nil }, runtimeController, coordinator, nil, shutdown); err == nil {
		t.Fatal("provideDaemonRunner(nil lifecycle) error = nil, want required project lifecycle coordinator error")
	}
}

// TestDaemonRuntimeCloseTimeoutExceedsControllerBudget keeps outer authority beyond nested cleanup.
func TestDaemonRuntimeCloseTimeoutExceedsControllerBudget(t *testing.T) {
	runtimeController, err := harbordruntime.NewController(new(state.Store))
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}

	timeout := daemonRuntimeCloseTimeout(runtimeController)
	if timeout != runtimeController.ShutdownTimeout()+runtimeCloseCoordinationMargin {
		t.Fatalf("daemon runtime close timeout = %s, want controller budget plus %s", timeout, runtimeCloseCoordinationMargin)
	}
	if timeout <= runtimeController.ShutdownTimeout() {
		t.Fatalf("daemon runtime close timeout = %s, must exceed controller budget %s", timeout, runtimeController.ShutdownTimeout())
	}
}

// TestProjectLifecycleRuntimeClosesRecoveredProcessesWhenNetworkStartFails prevents orphaned forj descendants during daemon startup.
func TestProjectLifecycleRuntimeClosesRecoveredProcessesWhenNetworkStartFails(t *testing.T) {
	startErr := errors.New("network runtime rejected startup")
	closeErr := errors.New("project process cleanup failed")
	closer := &recordingLifecycleCloser{closeErr: closeErr, done: make(chan struct{})}
	runtime := newProjectLifecycleRuntime(&recordingLifecycleNetworkRuntime{startErr: startErr, done: make(chan struct{})}, closer)

	err := runtime.Start(t.Context())
	if !closer.closed || !errors.Is(err, startErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Start() = %v, closed = %t, want joined startup and cleanup failures", err, closer.closed)
	}
	if closer.resumed {
		t.Fatal("Start() resumed recovered lifecycle work after network startup failed")
	}
}

// TestProjectLifecycleRuntimeResumesRecoveredStartsAfterNetworkStartup proves routes exist before queued work can launch.
func TestProjectLifecycleRuntimeResumesRecoveredStartsAfterNetworkStartup(t *testing.T) {
	network := &recordingLifecycleNetworkRuntime{done: make(chan struct{})}
	lifecycle := &recordingLifecycleCloser{
		done: make(chan struct{}),
		onResume: func() error {
			if !network.started {
				return errors.New("lifecycle resumed before network startup")
			}
			return nil
		},
	}
	runtime := newProjectLifecycleRuntime(network, lifecycle)

	if err := runtime.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !network.started || !lifecycle.resumed {
		t.Fatalf("startup state = network started %t, lifecycle resumed %t", network.started, lifecycle.resumed)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestProjectLifecycleRuntimeJoinsCleanupWhenResumeFails proves partial startup releases both owned authorities.
func TestProjectLifecycleRuntimeJoinsCleanupWhenResumeFails(t *testing.T) {
	resumeErr := errors.New("resume recovered starts failed")
	lifecycleCloseErr := errors.New("project process cleanup failed")
	networkCloseErr := errors.New("network cleanup failed")
	network := &recordingLifecycleNetworkRuntime{closeErr: networkCloseErr, done: make(chan struct{})}
	lifecycle := &recordingLifecycleCloser{
		resumeErr: resumeErr,
		closeErr:  lifecycleCloseErr,
		done:      make(chan struct{}),
	}
	runtime := newProjectLifecycleRuntime(network, lifecycle)

	err := runtime.Start(t.Context())
	if !errors.Is(err, resumeErr) || !errors.Is(err, lifecycleCloseErr) || !errors.Is(err, networkCloseErr) {
		t.Fatalf("Start() error = %v, want joined resume and cleanup failures", err)
	}
	if !network.started || !network.closed || !lifecycle.resumed || !lifecycle.closed {
		t.Fatalf(
			"startup cleanup = network started %t closed %t, lifecycle resumed %t closed %t",
			network.started,
			network.closed,
			lifecycle.resumed,
			lifecycle.closed,
		)
	}
}
