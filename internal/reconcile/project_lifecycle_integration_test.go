package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/projectreadiness"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/migrations"
)

const projectLifecycleHelperEnvironment = "HARBOR_PROJECT_LIFECYCLE_HELPER"

// projectLifecycleTestRouteReconciler keeps identity-stage lifecycle tests independent from a live data plane.
type projectLifecycleTestRouteReconciler struct{}

// Reconcile accepts one state-derived lifecycle edge without external publication.
func (projectLifecycleTestRouteReconciler) Reconcile(context.Context) error {
	return nil
}

// projectLifecycleRouteReconcilerFunc adapts one deterministic test function to route reconciliation.
type projectLifecycleRouteReconcilerFunc func(context.Context) error

// Reconcile invokes the deterministic route fixture.
func (reconciler projectLifecycleRouteReconcilerFunc) Reconcile(ctx context.Context) error {
	return reconciler(ctx)
}

// projectLifecycleRecordingRouteReconciler captures the durable project state visible at each publication edge.
type projectLifecycleRecordingRouteReconciler struct {
	store     *state.Store
	projectID domain.ProjectID
	mutex     sync.Mutex
	states    []domain.ProjectState
}

// Reconcile records the already-committed state that authorizes one route replacement.
func (reconciler *projectLifecycleRecordingRouteReconciler) Reconcile(ctx context.Context) error {
	record, err := reconciler.store.Project(ctx, reconciler.projectID)
	if err != nil {
		return err
	}
	reconciler.mutex.Lock()
	reconciler.states = append(reconciler.states, record.Project.State)
	reconciler.mutex.Unlock()
	return nil
}

// States returns a defensive copy of the observed lifecycle edges.
func (reconciler *projectLifecycleRecordingRouteReconciler) States() []domain.ProjectState {
	reconciler.mutex.Lock()
	defer reconciler.mutex.Unlock()
	return slices.Clone(reconciler.states)
}

// projectLifecycleRevisionRaceState changes the registered checkout immediately before the fenced start mutation.
type projectLifecycleRevisionRaceState struct {
	*state.Store
	replacementPath string
	mutex           sync.Mutex
	mutated         bool
}

// BeginProjectStart injects one project replacement after admission and then delegates to the real durable mutation.
func (fixture *projectLifecycleRevisionRaceState) BeginProjectStart(
	ctx context.Context,
	request state.BeginProjectStartRequest,
) (state.ProjectLifecycleMutation, error) {
	fixture.mutex.Lock()
	if !fixture.mutated {
		current, err := fixture.Store.Project(ctx, request.ProjectID)
		if err != nil {
			fixture.mutex.Unlock()
			return state.ProjectLifecycleMutation{}, err
		}
		drifted := current.Project
		drifted.Path = fixture.replacementPath
		drifted.UpdatedAt = request.At
		if _, err := fixture.Store.PutProject(ctx, drifted); err != nil {
			fixture.mutex.Unlock()
			return state.ProjectLifecycleMutation{}, err
		}
		fixture.mutated = true
	}
	fixture.mutex.Unlock()
	return fixture.Store.BeginProjectStart(ctx, request)
}

// projectLifecycleRevisionRaceSupervisor records any launch that escapes the project revision fence.
type projectLifecycleRevisionRaceSupervisor struct {
	mutex  sync.Mutex
	starts []projectprocess.StartRequest
}

// projectLifecycleBlockingLeaseState holds admission until the daemon lifecycle context is cancelled.
type projectLifecycleBlockingLeaseState struct {
	entered chan struct{}
	once    sync.Once
}

// Project proves the admission worker reached its cancellable state read before waiting for shutdown.
func (fixture *projectLifecycleBlockingLeaseState) Project(
	ctx context.Context,
	_ domain.ProjectID,
) (state.ProjectRecord, error) {
	fixture.once.Do(func() { close(fixture.entered) })
	<-ctx.Done()
	return state.ProjectRecord{}, ctx.Err()
}

// Network is unreachable because the blocking project read ends admission first.
func (*projectLifecycleBlockingLeaseState) Network(context.Context) (state.NetworkRecord, bool, error) {
	return state.NetworkRecord{}, false, errors.New("unexpected network read after blocked project admission")
}

// ReplaceProjectNetwork is unreachable because cancelled admission cannot persist a lease.
func (*projectLifecycleBlockingLeaseState) ReplaceProjectNetwork(
	context.Context,
	state.ReplaceProjectNetworkRequest,
) (state.NetworkMutationResult, error) {
	return state.NetworkMutationResult{}, errors.New("unexpected network write after blocked project admission")
}

// Start records an unexpected process launch after the injected project revision changed.
func (fixture *projectLifecycleRevisionRaceSupervisor) Start(
	_ context.Context,
	request projectprocess.StartRequest,
) (*projectprocess.Handle, error) {
	fixture.mutex.Lock()
	fixture.starts = append(fixture.starts, request)
	fixture.mutex.Unlock()
	return nil, errors.New("unexpected process launch after project revision drift")
}

// Stop is inert because the revision-race fixture never accepts a process.
func (*projectLifecycleRevisionRaceSupervisor) Stop(context.Context, domain.ProjectID, domain.SessionID) error {
	return nil
}

// ObservePriorProcess is unreachable because the revision-race fixture never persists process evidence.
func (*projectLifecycleRevisionRaceSupervisor) ObservePriorProcess(
	context.Context,
	domain.ProcessEvidence,
) (projectprocess.PriorProcessObservation, error) {
	return projectprocess.PriorProcessObservation{}, errors.New("unexpected prior process observation after project revision drift")
}

// Close is inert because the revision-race fixture never accepts a process.
func (*projectLifecycleRevisionRaceSupervisor) Close(context.Context) error {
	return nil
}

// StartCount returns the number of launches that escaped the revision fence.
func (fixture *projectLifecycleRevisionRaceSupervisor) StartCount() int {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	return len(fixture.starts)
}

// newProjectLifecycleAdmissionTestCoordinator wires real lifecycle persistence to an injected lease-state boundary.
func newProjectLifecycleAdmissionTestCoordinator(
	lifecycleState projectLifecycleState,
	journal *state.OperationJournal,
	leaseState projectPrimaryLeaseState,
	supervisor projectProcessSupervisor,
	address netip.Addr,
) *ProjectLifecycleCoordinator {
	discoverer := &primaryLeaseTestDiscoverer{port: 3000}
	observer := &primaryLeaseTestLoopbackObserver{
		facts: map[netip.Addr]loopback.Observation{address: primaryLeaseTestExactObservation(address)},
		errs:  make(map[netip.Addr]error),
	}
	prober := &primaryLeaseTestPortProber{
		results: make(map[netip.Addr]identity.ProbeResult),
		errs:    make(map[netip.Addr]error),
	}
	return newProjectLifecycleCoordinator(
		lifecycleState,
		journal,
		newProjectPrimaryLeaseCoordinator(leaseState, discoverer, observer, prober, time.Now),
		projectreadiness.NewProber(&http.Client{Timeout: time.Second}),
		supervisor,
		projectLifecycleTestRouteReconciler{},
		time.Now,
		newLifecycleOperationID,
		newLifecycleIntentID,
		newHarborProjectSession,
		defaultProjectStartupTimeout,
		defaultReadinessInterval,
	)
}

// TestMain turns a copy of this portable test binary into the fake forj executable used by the integration test.
func TestMain(m *testing.M) {
	if os.Getenv(projectLifecycleHelperEnvironment) == "1" {
		runProjectLifecycleHelper()
		return
	}
	os.Exit(m.Run())
}

// runProjectLifecycleHelper exposes the generated readiness shape until Harbor stops the owned process.
func runProjectLifecycleHelper() {
	if err := godotenv.Overload(".env.host"); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	address := os.Getenv("IP_ADDRESS")
	port := os.Getenv("HARBOR_PROJECT_LIFECYCLE_PORT")
	if got := os.Getenv("DEV_SERVICE_IP_ADDRESS"); got != address {
		_, _ = fmt.Fprintf(os.Stderr, "DEV_SERVICE_IP_ADDRESS=%q, want %q\n", got, address)
		os.Exit(2)
	}
	wantLighthouseURL := fmt.Sprintf("ws://%s:%s/lighthouse/ws/agent", address, port)
	if got := os.Getenv("LIGHTHOUSE_URL"); got != wantLighthouseURL {
		_, _ = fmt.Fprintf(os.Stderr, "LIGHTHOUSE_URL=%q, want %q\n", got, wantLighthouseURL)
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(address, port))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/-/ready" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"ready","app":"app"}`))
	})}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt)
	go func() {
		_ = server.Serve(listener)
	}()
	<-stopped
	_ = server.Close()
}

// TestProjectRuntimeEnvironmentOverridesPinsInternalEndpointsToAssignedIdentity prevents split bind and dial targets.
func TestProjectRuntimeEnvironmentOverridesPinsInternalEndpointsToAssignedIdentity(t *testing.T) {
	target, err := projectdiscovery.NewRuntimeTarget(
		"app",
		"App",
		netip.MustParseAddr("127.77.0.11"),
		3000,
	)
	if err != nil {
		t.Fatalf("create runtime target: %v", err)
	}

	overrides := projectRuntimeEnvironmentOverrides(target)
	if len(overrides) != 4 || overrides["API_HTTP_HOST"] != "127.77.0.11" ||
		overrides["DEV_SERVICE_IP_ADDRESS"] != "127.77.0.11" ||
		overrides["IP_ADDRESS"] != "127.77.0.11" ||
		overrides["LIGHTHOUSE_URL"] != "ws://127.77.0.11:3000/lighthouse/ws/agent" {
		t.Fatalf("runtime environment overrides = %#v", overrides)
	}
}

// TestProjectLifecycleCoordinatorRetriesRouteReconciliation covers transient recovery and bounded failure reporting.
func TestProjectLifecycleCoordinatorRetriesRouteReconciliation(t *testing.T) {
	sentinel := errors.New("synthetic route publication failure")
	for _, test := range []struct {
		name      string
		failures  int
		wantCalls int
		wantError bool
	}{
		{name: "recovers", failures: 2, wantCalls: 3},
		{name: "exhausts", failures: lifecyclePersistenceAttempts, wantCalls: lifecyclePersistenceAttempts, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			coordinator := &ProjectLifecycleCoordinator{
				routes: projectLifecycleRouteReconcilerFunc(func(context.Context) error {
					calls++
					if calls <= test.failures {
						return sentinel
					}
					return nil
				}),
			}
			err := coordinator.reconcileProjectRoutes(t.Context(), "test route edge")
			if calls != test.wantCalls {
				t.Fatalf("Reconcile() calls = %d, want %d", calls, test.wantCalls)
			}
			if test.wantError && !errors.Is(err, sentinel) {
				t.Fatalf("reconcileProjectRoutes() error = %v, want %v", err, sentinel)
			}
			if !test.wantError && err != nil {
				t.Fatalf("reconcileProjectRoutes() error = %v", err)
			}
		})
	}
}

// TestProjectLifecycleCoordinatorBringsForjDevOnlineAndStopsIt proves the complete durable process and readiness vertical.
func TestProjectLifecycleCoordinatorBringsForjDevOnlineAndStopsIt(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))
	installProjectLifecycleIntegrationForj(t, port)

	supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 500 * time.Millisecond})
	coordinator, discoverer := newProjectLifecycleIntegrationCoordinator(t, store, journal, supervisor, netip.MustParseAddr("127.0.0.1"), uint16(port))
	routes := &projectLifecycleRecordingRouteReconciler{store: store, projectID: project.ID}
	coordinator.routes = routes
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("close lifecycle coordinator: %v", err)
		}
	})

	queued, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start", IntentID: "intent-start",
	})
	if err != nil || queued.Operation.State != domain.OperationQueued {
		t.Fatalf("Start() = %#v, %v", queued, err)
	}
	ready := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)
	if len(ready.Project.Apps) != 1 || ready.Project.Apps[0].ID != "app" || len(ready.Project.Resources) != 1 || ready.Project.Resources[0].Kind != "application" || ready.Project.Resources[0].URL != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("ready project = %#v", ready.Project)
	}
	if !slices.Equal(discoverer.calls, []netip.Addr{netip.MustParseAddr("127.0.0.1")}) {
		t.Fatalf("admission target discoveries = %v, want one exact assigned target", discoverer.calls)
	}
	network, initialized, err := store.Network(t.Context())
	if err != nil || !initialized || len(network.Leases) != 1 || network.Leases[0].Address != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("allocated lifecycle network = %#v, %t, %v", network, initialized, err)
	}
	startOperation, err := journal.OperationByIntent(t.Context(), "intent-start")
	if err != nil || startOperation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("start operation = %#v, %v", startOperation, err)
	}

	stopping, err := coordinator.Stop(t.Context(), ProjectStopRequest{
		ProjectID: project.ID, OperationID: "operation-stop", IntentID: "intent-stop",
	})
	if err != nil || stopping.Operation.State != domain.OperationQueued {
		t.Fatalf("Stop() = %#v, %v", stopping, err)
	}
	stopped := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectStopped)
	if len(stopped.Project.Apps) != 1 || stopped.Project.Apps[0].State != domain.EntityStopped || len(stopped.Project.Resources) != 0 {
		t.Fatalf("stopped project = %#v", stopped.Project)
	}
	if _, err := store.ActiveProjectSession(t.Context(), project.ID); err == nil {
		t.Fatal("stopped project retained an active session")
	}
	stopOperation, err := journal.OperationByIntent(t.Context(), "intent-stop")
	if err != nil || stopOperation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("stop operation = %#v, %v", stopOperation, err)
	}
	waitForProjectLifecycleRouteStates(
		t,
		routes,
		[]domain.ProjectState{domain.ProjectReady, domain.ProjectStopping, domain.ProjectStopped},
	)
}

// TestProjectLifecycleCoordinatorCloseRetiresReadyProcessAuthority proves a daemon restart does not preserve a phantom online session.
func TestProjectLifecycleCoordinatorCloseRetiresReadyProcessAuthority(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))
	installProjectLifecycleIntegrationForj(t, port)
	coordinator, _ := newProjectLifecycleIntegrationCoordinator(
		t,
		store,
		journal,
		newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 500 * time.Millisecond}),
		netip.MustParseAddr("127.0.0.1"),
		uint16(port),
	)

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-close", IntentID: "intent-start-close",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := coordinator.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectStopped)
	if _, err := store.ActiveProjectSession(t.Context(), project.ID); err == nil {
		t.Fatal("daemon close retained an active project session")
	}
}

// TestProjectLifecycleCoordinatorRejectsCheckoutDriftAfterLeaseAdmission proves process path and admitted target share one project revision.
func TestProjectLifecycleCoordinatorRejectsCheckoutDriftAfterLeaseAdmission(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, _ := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	address := netip.MustParseAddr("127.0.0.1")
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, address)

	discoverer := &primaryLeaseTestDiscoverer{port: 3000}
	observer := &primaryLeaseTestLoopbackObserver{
		facts: map[netip.Addr]loopback.Observation{address: primaryLeaseTestExactObservation(address)},
		errs:  make(map[netip.Addr]error),
	}
	prober := &primaryLeaseTestPortProber{
		results: make(map[netip.Addr]identity.ProbeResult),
		errs:    make(map[netip.Addr]error),
	}
	racingState := &projectLifecycleRevisionRaceState{
		Store:           store,
		replacementPath: t.TempDir(),
	}
	supervisor := &projectLifecycleRevisionRaceSupervisor{}
	coordinator := newProjectLifecycleCoordinator(
		racingState,
		journal,
		newProjectPrimaryLeaseCoordinator(store, discoverer, observer, prober, time.Now),
		projectreadiness.NewProber(&http.Client{Timeout: time.Second}),
		supervisor,
		projectLifecycleTestRouteReconciler{},
		time.Now,
		newLifecycleOperationID,
		newLifecycleIntentID,
		newHarborProjectSession,
		defaultProjectStartupTimeout,
		defaultReadinessInterval,
	)

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-revision-race", IntentID: "intent-start-revision-race",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancelled := waitForProjectLifecycleOperationState(t, journal, "intent-start-revision-race", domain.OperationCancelled)
	if cancelled.Operation.Problem != nil || cancelled.Operation.Phase != "lifecycle prerequisites unavailable" {
		t.Fatalf("cancelled operation = %#v", cancelled.Operation)
	}
	if supervisor.StartCount() != 0 {
		t.Fatalf("process launches after project revision drift = %d, want 0", supervisor.StartCount())
	}
	current, err := store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("read drifted project: %v", err)
	}
	if current.Project.Path != racingState.replacementPath || current.Project.State != domain.ProjectStopped {
		t.Fatalf("project after revision race = %#v", current.Project)
	}
	if _, err := store.ActiveProjectSession(t.Context(), project.ID); err == nil {
		t.Fatal("revision race created an active session")
	}
	network, initialized, err := store.Network(t.Context())
	if err != nil || !initialized || len(network.Leases) != 1 || network.Leases[0].Address != address {
		t.Fatalf("network after revision race = %#v, %t, %v", network, initialized, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	closeErr := coordinator.Close(ctx)
	var conflict *state.ProjectRevisionConflictError
	if !errors.As(closeErr, &conflict) || conflict.ProjectID != project.ID {
		t.Fatalf("Close() error = %v, want project revision conflict", closeErr)
	}
}

// TestProjectLifecycleCoordinatorPersistsAdmissionRejectionWithoutHealthFailure keeps correctable setup work client-visible.
func TestProjectLifecycleCoordinatorPersistsAdmissionRejectionWithoutHealthFailure(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, _ := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	address := netip.MustParseAddr("127.0.0.1")
	supervisor := &projectLifecycleRevisionRaceSupervisor{}
	coordinator := newProjectLifecycleAdmissionTestCoordinator(store, journal, store, supervisor, address)

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-setup-required", IntentID: "intent-start-setup-required",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	failed := waitForProjectLifecycleOperationState(t, journal, "intent-start-setup-required", domain.OperationFailed)
	if failed.Operation.Problem == nil || failed.Operation.Problem.Code != "project.network.setup_required" ||
		!failed.Operation.Problem.Retryable || failed.Operation.StartedAt == nil || failed.Operation.FinishedAt == nil {
		t.Fatalf("failed admission operation = %#v", failed.Operation)
	}
	if supervisor.StartCount() != 0 {
		t.Fatalf("process launches after rejected admission = %d, want 0", supervisor.StartCount())
	}
	current, err := store.Project(t.Context(), project.ID)
	if err != nil || current.Project.State != domain.ProjectStopped {
		t.Fatalf("project after rejected admission = %#v, %v", current.Project, err)
	}
	if err := coordinator.Err(); err != nil {
		t.Fatalf("correctable admission rejection changed daemon health: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := coordinator.Close(ctx); err != nil {
		t.Fatalf("Close() after admission rejection = %v", err)
	}
}

// TestProjectLifecycleCoordinatorCancelsContextEndedAdmission keeps cancellation distinct from actionable rejection.
func TestProjectLifecycleCoordinatorCancelsContextEndedAdmission(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "cancelled", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			root, _ := newProjectLifecycleIntegrationCheckout(t)
			project := registerProjectLifecycleIntegrationProject(t, store, root)
			address := netip.MustParseAddr("127.77.0.11")
			leaseFixture := newPrimaryLeaseTestFixture(t, address)
			leaseFixture.state.projectErr = test.cause
			supervisor := &projectLifecycleRevisionRaceSupervisor{}
			coordinator := newProjectLifecycleAdmissionTestCoordinator(store, journal, leaseFixture.state, supervisor, address)

			if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
				ProjectID: project.ID, OperationID: "operation-start-context-ended", IntentID: "intent-start-context-ended",
			}); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			cancelled := waitForProjectLifecycleOperationState(t, journal, "intent-start-context-ended", domain.OperationCancelled)
			if cancelled.Operation.Problem != nil || cancelled.Operation.StartedAt != nil || cancelled.Operation.FinishedAt == nil {
				t.Fatalf("cancelled admission operation = %#v", cancelled.Operation)
			}
			if supervisor.StartCount() != 0 {
				t.Fatalf("process launches after context-ended admission = %d, want 0", supervisor.StartCount())
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := coordinator.Close(ctx); err != nil {
				t.Fatalf("Close() after context-ended admission = %v", err)
			}
		})
	}
}

// TestProjectLifecycleCoordinatorCancelsAdmissionDuringDaemonShutdown proves shutdown wins over pending admission work.
func TestProjectLifecycleCoordinatorCancelsAdmissionDuringDaemonShutdown(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, _ := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	address := netip.MustParseAddr("127.0.0.1")
	leaseState := &projectLifecycleBlockingLeaseState{entered: make(chan struct{})}
	supervisor := &projectLifecycleRevisionRaceSupervisor{}
	coordinator := newProjectLifecycleAdmissionTestCoordinator(store, journal, leaseState, supervisor, address)

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-daemon-cancel", IntentID: "intent-start-daemon-cancel",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-leaseState.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("lease admission did not reach cancellable project read")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := coordinator.Close(ctx); err != nil {
		t.Fatalf("Close() during admission = %v", err)
	}
	cancelled := waitForProjectLifecycleOperationState(t, journal, "intent-start-daemon-cancel", domain.OperationCancelled)
	if cancelled.Operation.Problem != nil || cancelled.Operation.StartedAt != nil || cancelled.Operation.FinishedAt == nil {
		t.Fatalf("daemon-cancelled admission operation = %#v", cancelled.Operation)
	}
	if supervisor.StartCount() != 0 {
		t.Fatalf("process launches during daemon-cancelled admission = %d, want 0", supervisor.StartCount())
	}
}

// newProjectLifecycleIntegrationState creates one fully migrated named harbord database.
func newProjectLifecycleIntegrationState(t *testing.T) (*state.Store, *state.OperationJournal) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbord.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate")
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close lifecycle database: %v", err)
		}
	})
	connection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open lifecycle database: %v", err)
	}
	registered := append([]migrations.Migration(nil), migrations.GetMigrations()...)
	sort.Slice(registered, func(left int, right int) bool { return registered[left].Name() < registered[right].Name() })
	for _, migration := range registered {
		if migration.App() != "harbord" || migration.Connection() != "default" || (migration.Driver() != "" && migration.Driver() != "sqlite") {
			continue
		}
		if err := migration.Up(connection); err != nil {
			t.Fatalf("apply lifecycle migration %s: %v", migration.Name(), err)
		}
	}
	mutations := state.NewMutationCoordinator(connections)
	store := state.NewStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		mutations,
	)
	journal := state.NewOperationJournal(
		connections,
		models.NewOperationRepo(connections),
		models.NewOperationTransitionRepo(connections),
		models.NewHarborStateRepo(connections),
		mutations,
	)
	return store, journal
}

// newProjectLifecycleIntegrationCheckout creates the minimum real checkout metadata used by discovery and readiness.
func newProjectLifecycleIntegrationCheckout(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve lifecycle port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release lifecycle port: %v", err)
	}
	root := filepath.Join(t.TempDir(), "orders")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create lifecycle checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".goforj.yml"), []byte("project_name: Orders\n"), 0o600); err != nil {
		t.Fatalf("write lifecycle marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(fmt.Sprintf("APP_NAME=Orders\nAPI_HTTP_PORT=%d\n", port)), 0o600); err != nil {
		t.Fatalf("write lifecycle environment: %v", err)
	}
	return root, port
}

// registerProjectLifecycleIntegrationProject commits the same inert project shape used by control registration.
func registerProjectLifecycleIntegrationProject(t *testing.T, store *state.Store, root string) domain.ProjectSnapshot {
	t.Helper()
	discovery, err := projectdiscovery.NewDiscoverer().Discover(t.Context(), root)
	if err != nil {
		t.Fatalf("discover lifecycle checkout: %v", err)
	}
	project, err := discovery.ProjectSnapshot("project-orders", time.Now().UTC())
	if err != nil {
		t.Fatalf("create lifecycle project: %v", err)
	}
	if _, err := store.RegisterProject(t.Context(), project); err != nil {
		t.Fatalf("register lifecycle project: %v", err)
	}
	return project
}

// initializeProjectLifecycleIntegrationIdentity commits only the verified pool so lifecycle must allocate its project lease.
func initializeProjectLifecycleIntegrationIdentity(
	t *testing.T,
	store *state.Store,
	projectID domain.ProjectID,
	address netip.Addr,
) {
	t.Helper()
	project, err := store.Project(t.Context(), projectID)
	if err != nil {
		t.Fatalf("read lifecycle project: %v", err)
	}
	pool, err := identity.NewPool(netip.MustParsePrefix("127.0.0.0/8"), []netip.Addr{address})
	if err != nil {
		t.Fatalf("create lifecycle identity pool: %v", err)
	}
	ownership, err := identity.NewOwnership("lifecycle-test-installation", 1)
	if err != nil {
		t.Fatalf("create lifecycle identity ownership: %v", err)
	}
	initializedAt := time.Now().UTC().Round(0)
	if initializedAt.Before(project.Project.UpdatedAt) {
		initializedAt = project.Project.UpdatedAt
	}
	initialized, err := store.InitializeNetworkIdentity(t.Context(), state.InitializeNetworkIdentityRequest{
		ExpectedNetworkRevision: 0,
		Ownership:               ownership,
		Pool:                    pool,
		PoolGeneration:          1,
		Setup: []state.NetworkSetupProof{
			{
				Component:  state.NetworkSetupComponentMachineOwnership,
				Evidence:   "lifecycle test machine ownership",
				Generation: 1,
				VerifiedAt: initializedAt,
			},
			{
				Component:  state.NetworkSetupComponentLoopbackPool,
				Evidence:   "lifecycle test loopback pool",
				Generation: 1,
				VerifiedAt: initializedAt,
			},
		},
		At: initializedAt,
	})
	if err != nil {
		t.Fatalf("initialize lifecycle identity: %v", err)
	}
	if initialized.Record.Stage != state.NetworkStageIdentity || len(initialized.Record.Leases) != 0 {
		t.Fatalf("initialized lifecycle network = %#v", initialized.Record)
	}
}

// newProjectLifecycleIntegrationCoordinator injects portable host facts while retaining production discovery, persistence, and process lifecycle.
func newProjectLifecycleIntegrationCoordinator(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
	supervisor *projectprocess.Supervisor,
	address netip.Addr,
	port uint16,
) (*ProjectLifecycleCoordinator, *primaryLeaseTestDiscoverer) {
	t.Helper()
	coordinator := NewProjectLifecycleCoordinator(store, journal, supervisor, projectLifecycleTestRouteReconciler{})
	discoverer := &primaryLeaseTestDiscoverer{port: port}
	observer := &primaryLeaseTestLoopbackObserver{
		facts: map[netip.Addr]loopback.Observation{address: primaryLeaseTestExactObservation(address)},
		errs:  make(map[netip.Addr]error),
	}
	prober := &primaryLeaseTestPortProber{
		results: make(map[netip.Addr]identity.ProbeResult),
		errs:    make(map[netip.Addr]error),
	}
	coordinator.primaryLeases = newProjectPrimaryLeaseCoordinator(store, discoverer, observer, prober, time.Now)
	return coordinator, discoverer
}

// installProjectLifecycleIntegrationForj places a portable test-binary copy where exec.LookPath resolves forj.
func installProjectLifecycleIntegrationForj(t *testing.T, port int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve lifecycle test executable: %v", err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("read lifecycle test executable: %v", err)
	}
	name := "forj"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := t.TempDir()
	forj := filepath.Join(bin, name)
	if err := os.WriteFile(forj, data, 0o700); err != nil {
		t.Fatalf("install lifecycle forj: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(projectLifecycleHelperEnvironment, "1")
	t.Setenv("HARBOR_PROJECT_LIFECYCLE_PORT", strconv.Itoa(port))
}

// newProjectLifecycleIntegrationSupervisor admits the portable helper executable while production supervisors retain real metadata verification.
func newProjectLifecycleIntegrationSupervisor(options projectprocess.Options) *projectprocess.Supervisor {
	return projectprocess.NewWithExecutableVerifier(options, func(string) error { return nil })
}

// waitForProjectLifecycleOperationState polls the journal until background lifecycle work reaches the requested edge.
func waitForProjectLifecycleOperationState(
	t *testing.T,
	journal *state.OperationJournal,
	intentID domain.IntentID,
	want domain.OperationState,
) state.OperationRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		record, err := journal.OperationByIntent(t.Context(), intentID)
		if err == nil && record.Operation.State == want {
			return record
		}
		time.Sleep(20 * time.Millisecond)
	}
	record, err := journal.OperationByIntent(t.Context(), intentID)
	t.Fatalf("operation state = %#v, %v, want %q", record.Operation, err, want)
	return state.OperationRecord{}
}

// waitForProjectLifecycleState polls durable projection because control intentionally returns after journaling.
func waitForProjectLifecycleState(t *testing.T, store *state.Store, projectID domain.ProjectID, want domain.ProjectState) state.ProjectRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		record, err := store.Project(t.Context(), projectID)
		if err == nil && record.Project.State == want {
			return record
		}
		time.Sleep(20 * time.Millisecond)
	}
	record, err := store.Project(t.Context(), projectID)
	t.Fatalf("project state = %#v, %v, want %q", record.Project, err, want)
	return state.ProjectRecord{}
}

// waitForProjectLifecycleRouteStates waits for asynchronous lifecycle work to finish its final route publication edge.
func waitForProjectLifecycleRouteStates(
	t *testing.T,
	reconciler *projectLifecycleRecordingRouteReconciler,
	want []domain.ProjectState,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := reconciler.States(); slices.Equal(got, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("route reconciliation states = %v, want %v", reconciler.States(), want)
}
