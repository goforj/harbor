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
	"github.com/goforj/harbor/internal/domain"
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
	events    *[]string
	startErr  error
	closeErr  error
	started   bool
	closed    bool
	done      chan struct{}
	closeOnce sync.Once
}

// Start returns the configured startup failure.
func (runtime *recordingLifecycleNetworkRuntime) Start(context.Context) error {
	if runtime.events != nil {
		*runtime.events = append(*runtime.events, "network.start")
	}
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
		if runtime.events != nil {
			*runtime.events = append(*runtime.events, "network.close")
		}
		runtime.closed = true
		close(runtime.done)
	})
	return runtime.closeErr
}

// recordingLifecycleCloser proves a recovered process coordinator is joined after network startup rejection.
type recordingLifecycleCloser struct {
	events    *[]string
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

// recordingNetworkReleaseRecovery records release arming before any project recovery boundary.
type recordingNetworkReleaseRecovery struct {
	events *[]string
	armed  bool
	err    error
}

// ArmNetworkRelease returns the configured durable-release recovery outcome.
func (recovery *recordingNetworkReleaseRecovery) ArmNetworkRelease(context.Context) (bool, error) {
	*recovery.events = append(*recovery.events, "release.arm")
	return recovery.armed, recovery.err
}

// recordingNetworkReleaseState reports whether the retained runtime is control-anchor-only.
type recordingNetworkReleaseState struct {
	armed bool
}

// NetworkReleaseArmed returns the configured retained runtime mode.
func (state recordingNetworkReleaseState) NetworkReleaseArmed() bool {
	return state.armed
}

// recordingNetworkReleaseCoordinator records release recovery after cold-anchor startup.
type recordingNetworkReleaseCoordinator struct {
	events *[]string
	err    error
}

// Recover records the release recovery boundary after the runtime control anchor starts.
func (coordinator *recordingNetworkReleaseCoordinator) Recover(context.Context) error {
	*coordinator.events = append(*coordinator.events, "release.recover")
	return coordinator.err
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
	if closer.events != nil {
		*closer.events = append(*closer.events, "lifecycle.resume")
	}
	closer.resumed = true
	if closer.onResume != nil {
		return errors.Join(closer.resumeErr, closer.onResume())
	}
	return closer.resumeErr
}

// Close records joined lifecycle cleanup and returns its configured result.
func (closer *recordingLifecycleCloser) Close(context.Context) error {
	closer.closeOnce.Do(func() {
		if closer.events != nil {
			*closer.events = append(*closer.events, "lifecycle.close")
		}
		closer.closed = true
		close(closer.done)
	})
	return closer.closeErr
}

// recordingActiveNetworkDataPlaneSetupOperationReader records startup selection of an interrupted setup operation.
type recordingActiveNetworkDataPlaneSetupOperationReader struct {
	events *[]string
	active state.NetworkDataPlaneSetupActiveOperation
	found  bool
	err    error
}

// ActiveNetworkDataPlaneSetupOperation returns the configured startup operation without durable state access.
func (reader *recordingActiveNetworkDataPlaneSetupOperationReader) ActiveNetworkDataPlaneSetupOperation(context.Context) (state.NetworkDataPlaneSetupActiveOperation, bool, error) {
	*reader.events = append(*reader.events, "operations.read")
	return reader.active, reader.found, reader.err
}

// recordingNetworkDataPlaneSetupRecovery records recovery only after an activation-phase operation was selected.
type recordingNetworkDataPlaneSetupRecovery struct {
	events *[]string
	err    error
	gotID  domain.OperationID
}

// Recover records the exact selected operation and returns its configured recovery outcome.
func (recovery *recordingNetworkDataPlaneSetupRecovery) Recover(_ context.Context, operationID domain.OperationID) (state.OperationRecord, error) {
	*recovery.events = append(*recovery.events, "setup.recover")
	recovery.gotID = operationID
	return state.OperationRecord{}, recovery.err
}

// recordingNetworkRuntimeActivator records full-stage runtime publication without opening listeners.
type recordingNetworkRuntimeActivator struct {
	events   *[]string
	err      error
	revision domain.Sequence
}

// ActivateNetwork records the requested durable full-network revision.
func (activator *recordingNetworkRuntimeActivator) ActivateNetwork(_ context.Context, revision domain.Sequence) error {
	*activator.events = append(*activator.events, "runtime.activate")
	activator.revision = revision
	return activator.err
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

// TestRecoverDaemonStateStopsBeforeRuntimeWork pins the pre-runtime recovery boundary.
func TestRecoverDaemonStateStopsBeforeRuntimeWork(t *testing.T) {
	events := []string{}
	release := &recordingNetworkReleaseRecovery{
		events: &events,
	}
	unregister := &recordingProjectUnregisterRecovery{
		events: &events,
	}
	lifecycle := &recordingProjectLifecycleRecovery{
		events: &events,
	}

	if err := recoverDaemonState(t.Context(), release, unregister, lifecycle); err != nil {
		t.Fatalf("recoverDaemonState() error = %v", err)
	}
	if got, want := strings.Join(events, ","), "release.arm,unregister.recover,lifecycle.recover"; got != want {
		t.Fatalf("daemon recovery order = %q, want %q", got, want)
	}
}

// TestRecoverDaemonStatePropagatesEachFailure prevents later recovery or runtime work from crossing a failed boundary.
func TestRecoverDaemonStatePropagatesEachFailure(t *testing.T) {
	releaseErr := errors.New("release recovery failed")
	unregisterErr := errors.New("unregister recovery failed")
	lifecycleErr := errors.New("lifecycle recovery failed")
	tests := []struct {
		name          string
		releaseErr    error
		unregisterErr error
		lifecycleErr  error
		want          error
		wantEvents    string
	}{
		{
			name:       "corrupt active release plan",
			releaseErr: releaseErr,
			want:       releaseErr,
			wantEvents: "release.arm",
		},
		{
			name:          "unregister",
			unregisterErr: unregisterErr,
			want:          unregisterErr,
			wantEvents:    "release.arm,unregister.recover",
		},
		{
			name:         "lifecycle",
			lifecycleErr: lifecycleErr,
			want:         lifecycleErr,
			wantEvents:   "release.arm,unregister.recover,lifecycle.recover",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			release := &recordingNetworkReleaseRecovery{
				events: &events,
				err:    test.releaseErr,
			}
			unregister := &recordingProjectUnregisterRecovery{
				events: &events,
				err:    test.unregisterErr,
			}
			lifecycle := &recordingProjectLifecycleRecovery{
				events:     &events,
				recoverErr: test.lifecycleErr,
			}

			err := recoverDaemonState(t.Context(), release, unregister, lifecycle)
			if !errors.Is(err, test.want) {
				t.Fatalf("recoverDaemonState() error = %v, want %v", err, test.want)
			}
			if got := strings.Join(events, ","); got != test.wantEvents {
				t.Fatalf("recovery events = %q, want %q", got, test.wantEvents)
			}
		})
	}
}

// TestRecoverDaemonStateSkipsProjectRecoveryForEveryActiveReleasePhase keeps global host cleanup isolated from project authority.
func TestRecoverDaemonStateSkipsProjectRecoveryForEveryActiveReleasePhase(t *testing.T) {
	phases := []state.GlobalNetworkReleasePlanPhase{
		state.GlobalNetworkReleasePlanPhaseRuntimeRelease,
		state.GlobalNetworkReleasePlanPhaseLowPorts,
		state.GlobalNetworkReleasePlanPhaseResolver,
		state.GlobalNetworkReleasePlanPhaseTrust,
		state.GlobalNetworkReleasePlanPhaseLoopbacks,
		state.GlobalNetworkReleasePlanPhaseVerifyEffects,
		state.GlobalNetworkReleasePlanPhaseOwnership,
		state.GlobalNetworkReleasePlanPhaseProjection,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			events := []string{}
			release := &recordingNetworkReleaseRecovery{
				events: &events,
				armed:  true,
			}
			unregister := &recordingProjectUnregisterRecovery{
				events: &events,
			}
			lifecycle := &recordingProjectLifecycleRecovery{
				events: &events,
			}

			if err := recoverDaemonState(t.Context(), release, unregister, lifecycle); err != nil {
				t.Fatalf("recoverDaemonState() error = %v", err)
			}
			if got, want := strings.Join(events, ","), "release.arm"; got != want {
				t.Fatalf("recovery events = %q, want %q", got, want)
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
	runtimeController, err := harbordruntime.NewController(store, new(state.OperationJournal))
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
	runtimeController, err := harbordruntime.NewController(store, new(state.OperationJournal))
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
	runtimeController, err := harbordruntime.NewController(store, new(state.OperationJournal))
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
	if _, err := provideControlServer(nil, networkDataPlaneSetupCapability{}, networkReleaseCapability{}, networkResolverPolicyMigrationCapability{}, shutdown, appLogger); err == nil {
		t.Fatal("provideControlServer(nil) error = nil, want required authority error")
	}
	if _, err := provideControlServer(new(authority.Authority), networkDataPlaneSetupCapability{}, networkReleaseCapability{}, networkResolverPolicyMigrationCapability{}, nil, appLogger); err == nil {
		t.Fatal("provideControlServer(nil shutdown) error = nil, want required shutdown coordinator error")
	}
	if _, err := provideControlServer(new(authority.Authority), networkDataPlaneSetupCapability{}, networkReleaseCapability{}, networkResolverPolicyMigrationCapability{}, shutdown, nil); err == nil {
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
	if _, err := provideDaemonRunner(
		nil,
		func(context.Context) error { return nil },
		runtimeController,
		coordinator,
		new(reconcile.ProjectLifecycleCoordinator),
		operations,
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		shutdown,
		nil,
	); err == nil {
		t.Fatal("provideDaemonRunner(nil server) error = nil, want required server error")
	}
	if _, err := provideDaemonRunner(
		new(control.Server),
		nil,
		runtimeController,
		coordinator,
		new(reconcile.ProjectLifecycleCoordinator),
		operations,
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		shutdown,
		nil,
	); err == nil {
		t.Fatal("provideDaemonRunner(nil readiness) error = nil, want required readiness error")
	}
	if _, err := provideDaemonRunner(
		new(control.Server),
		func(context.Context) error { return nil },
		nil,
		coordinator,
		new(reconcile.ProjectLifecycleCoordinator),
		operations,
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		shutdown,
		nil,
	); err == nil {
		t.Fatal("provideDaemonRunner(nil runtime) error = nil, want required runtime error")
	}
	if _, err := provideDaemonRunner(
		new(control.Server),
		func(context.Context) error { return nil },
		runtimeController,
		nil,
		new(reconcile.ProjectLifecycleCoordinator),
		operations,
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		shutdown,
		nil,
	); err == nil {
		t.Fatal("provideDaemonRunner(nil coordinator) error = nil, want required coordinator error")
	}
	if _, err := provideDaemonRunner(
		new(control.Server),
		func(context.Context) error { return nil },
		runtimeController,
		coordinator,
		new(reconcile.ProjectLifecycleCoordinator),
		operations,
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		nil,
		nil,
	); err == nil {
		t.Fatal("provideDaemonRunner(nil shutdown) error = nil, want required shutdown coordinator error")
	}
	if _, err := provideDaemonRunner(
		new(control.Server),
		func(context.Context) error { return nil },
		runtimeController,
		coordinator,
		nil,
		operations,
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		shutdown,
		nil,
	); err == nil {
		t.Fatal("provideDaemonRunner(nil lifecycle) error = nil, want required project lifecycle coordinator error")
	}
	if _, err := provideDaemonRunner(
		new(control.Server),
		func(context.Context) error { return nil },
		runtimeController,
		coordinator,
		new(reconcile.ProjectLifecycleCoordinator),
		nil,
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		shutdown,
		nil,
	); err == nil {
		t.Fatal("provideDaemonRunner(nil operations) error = nil, want required operation journal error")
	}
}

// TestProvideControlServerExposesResolverPolicyMigrationAuthority proves the optional capability reaches protocol negotiation.
func TestProvideControlServerExposesResolverPolicyMigrationAuthority(t *testing.T) {
	migration := &authority.NetworkResolverPolicyMigrationAuthority{}
	server, err := provideControlServer(
		new(authority.Authority),
		networkDataPlaneSetupCapability{},
		networkReleaseCapability{},
		networkResolverPolicyMigrationCapability{authority: migration},
		daemon.NewShutdown(),
		logger.NewSilentLogger(),
	)
	if err != nil {
		t.Fatalf("provideControlServer() error = %v", err)
	}
	if server == nil {
		t.Fatal("provideControlServer() = nil, want server")
	}
}

// TestSourceDevelopmentDaemonHandoffUsesCapturedEnvironment proves source-only authority retry cannot be enabled by Harbor's later dotenv load.
func TestSourceDevelopmentDaemonHandoffUsesCapturedEnvironment(t *testing.T) {
	t.Setenv(sourceDevelopmentHandoffEnvironment, "1")
	if handler := sourceDevelopmentDaemonHandoff(nil); handler != nil {
		t.Fatal("sourceDevelopmentDaemonHandoff() enabled from the current process environment")
	}
	if handler := sourceDevelopmentDaemonHandoff(projectprocess.Environment{"OTHER=value"}); handler != nil {
		t.Fatal("sourceDevelopmentDaemonHandoff() enabled without captured marker")
	}
	if handler := sourceDevelopmentDaemonHandoff(projectprocess.Environment{
		sourceDevelopmentHandoffEnvironment + "=1",
	}); handler != nil {
		t.Fatal("sourceDevelopmentDaemonHandoff() enabled without GoForj development provenance")
	}
	if handler := sourceDevelopmentDaemonHandoff(projectprocess.Environment{
		sourceDevelopmentHandoffEnvironment + "=1",
		sourceDevelopmentOriginEnvironment + "=" + sourceDevelopmentCommandOrigin,
		sourceDevelopmentSubprocessEnvironment + "=1",
	}); handler == nil {
		t.Fatal("sourceDevelopmentDaemonHandoff() did not enable from the captured GoForj development environment")
	}
}

// TestDaemonRuntimeCloseTimeoutExceedsControllerBudget keeps outer authority beyond nested cleanup.
func TestDaemonRuntimeCloseTimeoutExceedsControllerBudget(t *testing.T) {
	runtimeController, err := harbordruntime.NewController(new(state.Store), new(state.OperationJournal))
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
	closer := &recordingLifecycleCloser{
		closeErr: closeErr,
		done:     make(chan struct{}),
	}
	network := &recordingLifecycleNetworkRuntime{
		startErr: startErr,
		done:     make(chan struct{}),
	}
	runtime := newProjectLifecycleRuntime(
		network,
		closer,
		recordingNetworkReleaseState{},
		networkReleaseCapability{},
		func(context.Context) error { return nil },
	)

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
	runtime := newProjectLifecycleRuntime(
		network,
		lifecycle,
		recordingNetworkReleaseState{},
		networkReleaseCapability{},
		func(context.Context) error { return nil },
	)

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
	network := &recordingLifecycleNetworkRuntime{
		closeErr: networkCloseErr,
		done:     make(chan struct{}),
	}
	lifecycle := &recordingLifecycleCloser{
		resumeErr: resumeErr,
		closeErr:  lifecycleCloseErr,
		done:      make(chan struct{}),
	}
	runtime := newProjectLifecycleRuntime(
		network,
		lifecycle,
		recordingNetworkReleaseState{},
		networkReleaseCapability{},
		func(context.Context) error { return nil },
	)

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

// TestProjectLifecycleRuntimeRunsPostRuntimeRecoveryBeforeResume preserves the only safe startup sequence for retained launches.
func TestProjectLifecycleRuntimeRunsPostRuntimeRecoveryBeforeResume(t *testing.T) {
	events := []string{}
	network := &recordingLifecycleNetworkRuntime{
		events: &events,
		done:   make(chan struct{}),
	}
	lifecycle := &recordingLifecycleCloser{
		events: &events,
		done:   make(chan struct{}),
	}
	runtime := newProjectLifecycleRuntime(
		network,
		lifecycle,
		recordingNetworkReleaseState{},
		networkReleaseCapability{},
		func(context.Context) error {
			events = append(events, "post-runtime.recover")
			return nil
		},
	)

	if err := runtime.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got, want := strings.Join(events, ","), "network.start,post-runtime.recover,lifecycle.resume"; got != want {
		t.Fatalf("startup order = %q, want %q", got, want)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// TestProjectLifecycleRuntimeRetainsControlAnchorWithoutProjectRecovery keeps a recovered global release reachable for control-plane cleanup.
func TestProjectLifecycleRuntimeRetainsControlAnchorWithoutProjectRecovery(t *testing.T) {
	events := []string{}
	network := &recordingLifecycleNetworkRuntime{
		events: &events,
		done:   make(chan struct{}),
	}
	lifecycle := &recordingLifecycleCloser{
		events: &events,
		done:   make(chan struct{}),
	}
	release := recordingNetworkReleaseState{
		armed: true,
	}
	runtime := newProjectLifecycleRuntime(
		network,
		lifecycle,
		release,
		networkReleaseCapability{recovery: &recordingNetworkReleaseCoordinator{events: &events}},
		func(context.Context) error {
			events = append(events, "post-runtime.recover")
			return nil
		},
	)

	if err := runtime.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got, want := strings.Join(events, ","), "network.start,release.recover"; got != want {
		t.Fatalf("startup events = %q, want %q", got, want)
	}
	select {
	case <-runtime.Done():
		t.Fatal("Done() closed while the release control anchor remained live")
	default:
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got, want := strings.Join(events, ","), "network.start,release.recover,lifecycle.close,network.close"; got != want {
		t.Fatalf("close events = %q, want %q", got, want)
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("Done() remained open after release control anchor close")
	}
}

// TestProjectLifecycleRuntimeClosesBothAuthoritiesWhenNetworkReleaseRecoveryFails prevents an armed release from leaving its cold anchor live.
func TestProjectLifecycleRuntimeClosesBothAuthoritiesWhenNetworkReleaseRecoveryFails(t *testing.T) {
	events := []string{}
	recoveryErr := errors.New("network release recovery failed")
	lifecycleCloseErr := errors.New("lifecycle close failed")
	networkCloseErr := errors.New("network close failed")
	network := &recordingLifecycleNetworkRuntime{
		events:   &events,
		closeErr: networkCloseErr,
		done:     make(chan struct{}),
	}
	lifecycle := &recordingLifecycleCloser{
		events:   &events,
		closeErr: lifecycleCloseErr,
		done:     make(chan struct{}),
	}
	runtime := newProjectLifecycleRuntime(
		network,
		lifecycle,
		recordingNetworkReleaseState{armed: true},
		networkReleaseCapability{recovery: &recordingNetworkReleaseCoordinator{
			events: &events,
			err:    recoveryErr,
		}},
		func(context.Context) error {
			t.Fatal("post-runtime recovery ran while global release remained armed")
			return nil
		},
	)

	err := runtime.Start(t.Context())
	if !errors.Is(err, recoveryErr) || !errors.Is(err, lifecycleCloseErr) || !errors.Is(err, networkCloseErr) {
		t.Fatalf("Start() error = %v, want joined recovery and cleanup failures", err)
	}
	if lifecycle.resumed {
		t.Fatal("Start() resumed lifecycle work after network release recovery failed")
	}
	if got, want := strings.Join(events, ","), "network.start,release.recover,lifecycle.close,network.close"; got != want {
		t.Fatalf("failure cleanup order = %q, want %q", got, want)
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("Done() remained open after network release recovery failure")
	}
}

// TestProjectLifecycleRuntimeFailsClosedWithoutNetworkReleaseRecovery prevents an unsupported binary from retaining an armed release anchor.
func TestProjectLifecycleRuntimeFailsClosedWithoutNetworkReleaseRecovery(t *testing.T) {
	events := []string{}
	network := &recordingLifecycleNetworkRuntime{
		events: &events,
		done:   make(chan struct{}),
	}
	lifecycle := &recordingLifecycleCloser{
		events: &events,
		done:   make(chan struct{}),
	}
	runtime := newProjectLifecycleRuntime(
		network,
		lifecycle,
		recordingNetworkReleaseState{armed: true},
		networkReleaseCapability{},
		func(context.Context) error {
			t.Fatal("post-runtime recovery ran without network release recovery authority")
			return nil
		},
	)

	err := runtime.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "platform recovery authority is unavailable") {
		t.Fatalf("Start() error = %v, want unavailable release recovery authority", err)
	}
	if lifecycle.resumed {
		t.Fatal("Start() resumed lifecycle work without network release recovery authority")
	}
	if got, want := strings.Join(events, ","), "network.start,lifecycle.close,network.close"; got != want {
		t.Fatalf("failure cleanup order = %q, want %q", got, want)
	}
}

// TestProjectLifecycleRuntimeFailsFastWithoutPostRuntimeRecovery prevents retained launches from proceeding without the required boundary.
func TestProjectLifecycleRuntimeFailsFastWithoutPostRuntimeRecovery(t *testing.T) {
	network := &recordingLifecycleNetworkRuntime{done: make(chan struct{})}
	lifecycle := &recordingLifecycleCloser{done: make(chan struct{})}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("newProjectLifecycleRuntime() with nil post-runtime recovery did not fail fast")
		}
	}()

	_ = newProjectLifecycleRuntime(network, lifecycle, recordingNetworkReleaseState{}, networkReleaseCapability{}, nil)
}

// TestProjectLifecycleRuntimeFailsFastWithoutNetworkReleaseState prevents startup from silently bypassing release recovery.
func TestProjectLifecycleRuntimeFailsFastWithoutNetworkReleaseState(t *testing.T) {
	network := &recordingLifecycleNetworkRuntime{
		done: make(chan struct{}),
	}
	lifecycle := &recordingLifecycleCloser{
		done: make(chan struct{}),
	}
	defer func() {
		if recover() == nil {
			t.Fatal("newProjectLifecycleRuntime() with nil release state did not fail fast")
		}
	}()

	_ = newProjectLifecycleRuntime(
		network,
		lifecycle,
		nil,
		networkReleaseCapability{},
		func(context.Context) error { return nil },
	)
}

// TestProjectLifecycleRuntimeClosesBothAuthoritiesWhenPostRuntimeRecoveryFails preserves terminal ownership after partial startup.
func TestProjectLifecycleRuntimeClosesBothAuthoritiesWhenPostRuntimeRecoveryFails(t *testing.T) {
	events := []string{}
	recoveryErr := errors.New("post-runtime recovery failed")
	lifecycleCloseErr := errors.New("lifecycle close failed")
	networkCloseErr := errors.New("network close failed")
	network := &recordingLifecycleNetworkRuntime{
		events:   &events,
		closeErr: networkCloseErr,
		done:     make(chan struct{}),
	}
	lifecycle := &recordingLifecycleCloser{
		events:   &events,
		closeErr: lifecycleCloseErr,
		done:     make(chan struct{}),
	}
	runtime := newProjectLifecycleRuntime(
		network,
		lifecycle,
		recordingNetworkReleaseState{},
		networkReleaseCapability{},
		func(context.Context) error {
			events = append(events, "post-runtime.recover")
			return recoveryErr
		},
	)

	err := runtime.Start(t.Context())
	if !errors.Is(err, recoveryErr) || !errors.Is(err, lifecycleCloseErr) || !errors.Is(err, networkCloseErr) {
		t.Fatalf("Start() error = %v, want joined recovery and cleanup failures", err)
	}
	if lifecycle.resumed {
		t.Fatal("Start() resumed lifecycle work after post-runtime recovery failed")
	}
	if got, want := strings.Join(events, ","), "network.start,post-runtime.recover,lifecycle.close,network.close"; got != want {
		t.Fatalf("failure cleanup order = %q, want %q", got, want)
	}
	select {
	case <-runtime.Done():
	default:
		t.Fatal("Done() remained open after post-runtime recovery failure")
	}
	if terminal := runtime.Err(); !errors.Is(terminal, recoveryErr) || !errors.Is(terminal, lifecycleCloseErr) || !errors.Is(terminal, networkCloseErr) {
		t.Fatalf("Err() = %v, want joined recovery and cleanup failures", terminal)
	}
}

// TestRecoverDaemonRuntimeStateGuardsSetupRecovery pins every durable recovery boundary before endpoint and runtime work.
func TestRecoverDaemonRuntimeStateGuardsSetupRecovery(t *testing.T) {
	readErr := errors.New("read active setup failed")
	setupErr := errors.New("setup recovery failed")
	endpointErr := errors.New("endpoint reconciliation failed")
	activateErr := errors.New("runtime activation failed")
	operationID := domain.OperationID("operation-1")
	tests := []struct {
		name          string
		active        state.NetworkDataPlaneSetupActiveOperation
		found         bool
		readErr       error
		setupErr      error
		endpointState state.NetworkRecord
		endpointErr   error
		activateErr   error
		setup         bool
		want          error
		wantFailure   bool
		wantEvents    string
		wantRevision  domain.Sequence
	}{
		{name: "no active operation", endpointState: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 7}, wantEvents: "operations.read,endpoints.reconcile,runtime.activate", wantRevision: 7},
		{name: "trust approval", active: state.NetworkDataPlaneSetupActiveOperation{Phase: state.NetworkDataPlaneSetupPhaseTrustApproval}, found: true, endpointState: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 8}, wantEvents: "operations.read,endpoints.reconcile,runtime.activate", wantRevision: 8},
		{name: "low port approval", active: state.NetworkDataPlaneSetupActiveOperation{Phase: state.NetworkDataPlaneSetupPhaseLowPortApproval}, found: true, endpointState: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 9}, wantEvents: "operations.read,endpoints.reconcile,runtime.activate", wantRevision: 9},
		{name: "activation", active: state.NetworkDataPlaneSetupActiveOperation{Operation: state.OperationRecord{Operation: domain.Operation{ID: operationID}}, Phase: state.NetworkDataPlaneSetupPhaseActivation}, found: true, setup: true, endpointState: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 10}, wantEvents: "operations.read,setup.recover,endpoints.reconcile,runtime.activate", wantRevision: 10},
		{name: "activation without optional recovery", active: state.NetworkDataPlaneSetupActiveOperation{Phase: state.NetworkDataPlaneSetupPhaseActivation}, found: true, wantFailure: true, wantEvents: "operations.read"},
		{name: "resolver record", endpointState: state.NetworkRecord{Stage: state.NetworkStageResolver, Revision: 11}, wantEvents: "operations.read,endpoints.reconcile"},
		{name: "uninitialized record", endpointState: state.NetworkRecord{}, wantEvents: "operations.read,endpoints.reconcile"},
		{name: "operation read failure", readErr: readErr, want: readErr, wantEvents: "operations.read"},
		{name: "setup failure", active: state.NetworkDataPlaneSetupActiveOperation{Operation: state.OperationRecord{Operation: domain.Operation{ID: operationID}}, Phase: state.NetworkDataPlaneSetupPhaseActivation}, found: true, setup: true, setupErr: setupErr, want: setupErr, wantEvents: "operations.read,setup.recover"},
		{name: "endpoint failure", endpointErr: endpointErr, want: endpointErr, wantEvents: "operations.read,endpoints.reconcile"},
		{name: "runtime activation failure", endpointState: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 12}, activateErr: activateErr, want: activateErr, wantEvents: "operations.read,endpoints.reconcile,runtime.activate", wantRevision: 12},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			reader := &recordingActiveNetworkDataPlaneSetupOperationReader{events: &events, active: test.active, found: test.found, err: test.readErr}
			setup := &recordingNetworkDataPlaneSetupRecovery{events: &events, err: test.setupErr}
			lifecycle := &recordingProjectLifecycleRecovery{events: &events, endpointState: test.endpointState, endpointErr: test.endpointErr}
			activator := &recordingNetworkRuntimeActivator{events: &events, err: test.activateErr}
			var recovery networkDataPlaneSetupRecoveryAuthority
			if test.setup {
				recovery = setup
			}

			err := recoverDaemonRuntimeState(t.Context(), reader, recovery, lifecycle, activator)
			if test.wantFailure && err == nil {
				t.Fatal("recoverDaemonRuntimeState() error = nil, want failed-closed optional recovery error")
			}
			if !test.wantFailure && !errors.Is(err, test.want) {
				t.Fatalf("recoverDaemonRuntimeState() error = %v, want %v", err, test.want)
			}
			if got := strings.Join(events, ","); got != test.wantEvents {
				t.Fatalf("recovery events = %q, want %q", got, test.wantEvents)
			}
			if activator.revision != test.wantRevision {
				t.Fatalf("activated revision = %d, want %d", activator.revision, test.wantRevision)
			}
			if test.name == "activation" && setup.gotID != operationID {
				t.Fatalf("recovered operation = %q, want %q", setup.gotID, operationID)
			}
		})
	}
}

// TestRecoverDaemonRuntimeStateRejectsCanceledContextBeforeAnyCall prevents cancellation from mutating startup state.
func TestRecoverDaemonRuntimeStateRejectsCanceledContextBeforeAnyCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	events := []string{}
	reader := &recordingActiveNetworkDataPlaneSetupOperationReader{events: &events}
	setup := &recordingNetworkDataPlaneSetupRecovery{events: &events}
	lifecycle := &recordingProjectLifecycleRecovery{events: &events}
	activator := &recordingNetworkRuntimeActivator{events: &events}

	err := recoverDaemonRuntimeState(ctx, reader, setup, lifecycle, activator)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recoverDaemonRuntimeState() error = %v, want context cancellation", err)
	}
	if len(events) != 0 {
		t.Fatalf("canceled recovery calls = %q, want none", strings.Join(events, ","))
	}
}
