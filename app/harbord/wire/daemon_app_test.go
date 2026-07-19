package wire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/authority"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/daemon"
	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/logger"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// failingLifecycleRuntime is the minimal network runtime needed to prove composite startup cleanup.
type failingLifecycleRuntime struct {
	startErr error
	done     chan struct{}
}

// Start returns the configured startup failure.
func (runtime *failingLifecycleRuntime) Start(context.Context) error {
	return runtime.startErr
}

// Done returns a stable terminal channel for the daemon runtime contract.
func (runtime *failingLifecycleRuntime) Done() <-chan struct{} {
	return runtime.done
}

// Err returns no independent terminal failure for this startup fixture.
func (runtime *failingLifecycleRuntime) Err() error {
	return nil
}

// Close is inert because a failed runtime Start owns its own network cleanup.
func (runtime *failingLifecycleRuntime) Close(context.Context) error {
	return nil
}

// recordingLifecycleCloser proves a recovered process coordinator is joined after network startup rejection.
type recordingLifecycleCloser struct {
	closed   bool
	closeErr error
	done     chan struct{}
}

// Close records joined lifecycle cleanup and returns its configured result.
func (closer *recordingLifecycleCloser) Close(context.Context) error {
	if !closer.closed {
		closer.closed = true
		close(closer.done)
	}
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
	runtime := newProjectLifecycleRuntime(&failingLifecycleRuntime{startErr: startErr, done: make(chan struct{})}, closer)

	err := runtime.Start(t.Context())
	if !closer.closed || !errors.Is(err, startErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Start() = %v, closed = %t, want joined startup and cleanup failures", err, closer.closed)
	}
}
