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
	"github.com/goforj/harbor/internal/reconcile"
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

	application, err := InitializeApplication()
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

// TestProvideProjectUnregisterCoordinatorIsLazy proves production assembly retains machine-global stores behind the issuer factory.
func TestProvideProjectUnregisterCoordinatorIsLazy(t *testing.T) {
	store := new(state.Store)
	operations := new(state.OperationJournal)
	plans := new(state.HelperApprovalPlanSource)
	runtimeController, err := harbordruntime.NewController(store)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	issuerOpenCalls := 0
	coordinator, err := provideProjectUnregisterCoordinatorWithIssuerOpener(
		store,
		operations,
		plans,
		runtimeController,
		func(ticketissuer.PlanSource) (reconcile.TicketIssuer, error) {
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

// TestDaemonProvidersRejectIncompleteAssembly verifies constructor validation remains at the owning production boundaries.
func TestDaemonProvidersRejectIncompleteAssembly(t *testing.T) {
	store := new(state.Store)
	operations := new(state.OperationJournal)
	plans := new(state.HelperApprovalPlanSource)
	runtimeController, err := harbordruntime.NewController(store)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	openIssuer := func(ticketissuer.PlanSource) (reconcile.TicketIssuer, error) {
		return nil, errors.New("unused test issuer opener")
	}
	coordinator, err := provideProjectUnregisterCoordinatorWithIssuerOpener(
		store,
		operations,
		plans,
		runtimeController,
		openIssuer,
	)
	if err != nil {
		t.Fatalf("provideProjectUnregisterCoordinatorWithIssuerOpener() error = %v", err)
	}
	shutdown := daemon.NewShutdown()
	if _, err := provideControlServer(nil, shutdown); err == nil {
		t.Fatal("provideControlServer(nil) error = nil, want required authority error")
	}
	if _, err := provideControlServer(new(authority.Authority), nil); err == nil {
		t.Fatal("provideControlServer(nil shutdown) error = nil, want required shutdown coordinator error")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(nil, operations, plans, runtimeController, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil store) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, nil, plans, runtimeController, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil journal) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, operations, nil, runtimeController, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil plans) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, operations, plans, nil, openIssuer); err == nil {
		t.Fatal("provideProjectUnregisterCoordinatorWithIssuerOpener(nil runtime) error = nil")
	}
	if _, err := provideProjectUnregisterCoordinatorWithIssuerOpener(store, operations, plans, runtimeController, nil); err == nil {
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
