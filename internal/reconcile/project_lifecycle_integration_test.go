package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/goforj/harbor/internal/containerruntime"
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
	"gorm.io/gorm"
)

const (
	projectLifecycleHelperEnvironment          = "HARBOR_PROJECT_LIFECYCLE_HELPER"
	projectLifecycleHelperModeEnvironment      = "HARBOR_PROJECT_LIFECYCLE_HELPER_MODE"
	projectLifecycleDescriptorModeEnvironment  = "HARBOR_PROJECT_LIFECYCLE_DESCRIPTOR_MODE"
	projectLifecycleServiceReportEnvironment   = "HARBOR_PROJECT_LIFECYCLE_SERVICE_REPORT"
	projectLifecycleHelperWatcherMode          = "watcher"
	projectLifecycleHelperIgnoringListenerMode = "ignoring-listener"
)

// projectLifecycleTestRouteReconciler keeps identity-stage lifecycle tests independent from a live data plane.
type projectLifecycleTestRouteReconciler struct{}

// projectLifecycleContainerRuntime gives lifecycle tests deterministic direct host-runtime observations.
type projectLifecycleContainerRuntime struct {
	observation containerruntime.ProjectObservation
	err         error
}

// ObserveProject returns the configured direct container-runtime view.
func (runtime *projectLifecycleContainerRuntime) ObserveProject(context.Context, string) (containerruntime.ProjectObservation, error) {
	return runtime.observation, runtime.err
}

// OpenServiceLogs rejects log access because lifecycle integration tests exercise only startup observation.
func (*projectLifecycleContainerRuntime) OpenServiceLogs(
	context.Context,
	string,
	string,
	int,
) (containerruntime.LogFollower, error) {
	return nil, errors.New("unexpected service log access in project lifecycle integration test")
}

// Close is inert because the deterministic runtime owns no host transport.
func (*projectLifecycleContainerRuntime) Close() error {
	return nil
}

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

// Down rejects reset work because the revision-race fixture must fail before runtime effects begin.
func (*projectLifecycleRevisionRaceSupervisor) Down(context.Context, projectprocess.DownRequest) error {
	return errors.New("unexpected project reset after project revision drift")
}

// ReadOutput returns no transcript because the revision-race fixture never accepts a process launch.
func (*projectLifecycleRevisionRaceSupervisor) ReadOutput(
	domain.ProjectID,
	domain.SessionID,
	uint64,
) projectprocess.OutputChunk {
	return projectprocess.OutputChunk{}
}

// WaitOutput returns no transcript because the revision-race fixture never accepts a process launch.
func (*projectLifecycleRevisionRaceSupervisor) WaitOutput(
	context.Context,
	domain.ProjectID,
	domain.SessionID,
	uint64,
) (projectprocess.OutputChunk, error) {
	return projectprocess.OutputChunk{}, nil
}

// ReadServiceLogs rejects runtime access because the revision-race fixture never accepts a process launch.
func (*projectLifecycleRevisionRaceSupervisor) ReadServiceLogs(
	context.Context,
	domain.ProjectID,
	domain.SessionID,
	domain.ServiceID,
	uint64,
) (projectprocess.ServiceLogSelection, error) {
	return projectprocess.ServiceLogSelection{}, errors.New("unexpected service log read after project revision drift")
}

// WaitServiceLogs rejects runtime access because the revision-race fixture never accepts a process launch.
func (*projectLifecycleRevisionRaceSupervisor) WaitServiceLogs(
	context.Context,
	domain.ProjectID,
	domain.SessionID,
	domain.ServiceID,
	uint64,
) (projectprocess.ServiceLogSelection, error) {
	return projectprocess.ServiceLogSelection{}, errors.New("unexpected service log wait after project revision drift")
}

// Stop is inert because the revision-race fixture never accepts a process.
func (*projectLifecycleRevisionRaceSupervisor) Stop(context.Context, domain.ProjectID, domain.SessionID) error {
	return nil
}

// ObserveServices is unreachable because the revision-race fixture never accepts a process.
func (*projectLifecycleRevisionRaceSupervisor) ObserveServices(
	context.Context,
	domain.ProjectID,
	domain.SessionID,
) (projectprocess.ServiceObservation, error) {
	return projectprocess.ServiceObservation{}, errors.New("unexpected service observation after project revision drift")
}

// ObserveFrameworkResources is unreachable because the revision-race fixture never accepts a process.
func (*projectLifecycleRevisionRaceSupervisor) ObserveFrameworkResources(
	context.Context,
	domain.ProjectID,
	domain.SessionID,
) (projectprocess.FrameworkResourceObservation, error) {
	return projectprocess.FrameworkResourceObservation{}, errors.New("unexpected framework resource observation after project revision drift")
}

// ObservePriorProcess is unreachable because the revision-race fixture never persists process evidence.
func (*projectLifecycleRevisionRaceSupervisor) ObservePriorProcess(
	context.Context,
	domain.ProcessEvidence,
) (projectprocess.PriorProcessObservation, error) {
	return projectprocess.PriorProcessObservation{}, errors.New("unexpected prior process observation after project revision drift")
}

// SettlePriorProcess is unreachable because the revision-race fixture never persists process evidence.
func (*projectLifecycleRevisionRaceSupervisor) SettlePriorProcess(
	context.Context,
	domain.ProcessEvidence,
) (projectprocess.PriorProcessSettlement, error) {
	return projectprocess.PriorProcessSettlement{}, errors.New("unexpected prior process settlement after project revision drift")
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
	if len(os.Args) == 2 && os.Args[1] == "down" {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "project:describe" {
		if len(os.Args) != 3 || os.Args[2] != "--json" {
			_, _ = fmt.Fprintln(os.Stderr, "unexpected project descriptor arguments")
			os.Exit(90)
		}
		if os.Getenv(projectLifecycleDescriptorModeEnvironment) == "invalid" {
			_, _ = fmt.Fprintln(os.Stdout, `{"schema_version":1,"project":{"name":"lifecycle","module":"example.com/lifecycle","config_digest":"not-a-digest"},"goforj":{"version":"v0.20.1","cli_capabilities":["project-descriptor.v1"],"generated_project":{"generation":"v0.20.1","capabilities":[]}},"apps":[]}`)
			return
		}
		_, _ = fmt.Fprintln(os.Stdout, `{"schema_version":1,"project":{"name":"lifecycle","module":"example.com/lifecycle","config_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"goforj":{"version":"v0.20.1","cli_capabilities":["project-descriptor.v1"],"generated_project":{"generation":"v0.20.1","capabilities":[]}},"apps":[{"id":"app","name":"app","entrypoint":"cmd/app/main.go","runtimes":[{"id":"http","kind":"http","default_port":3000,"public_url":true,"readiness_path":"/-/ready"}]}]}`)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "dev:status" {
		if os.Getenv(projectLifecycleServiceReportEnvironment) == "problem" {
			_, _ = fmt.Fprintln(os.Stdout, `{"schema_version":1,"supported":true,"problem":"Compose status unavailable","services":[]}`)
			return
		}
		_, _ = fmt.Fprintln(os.Stdout, `{"schema_version":1,"supported":true,"services":[{"id":"mysql","name":"MySQL","kind":"compose","state":"ready","active":true,"required":true,"containers":[]},{"id":"old","name":"Old","kind":"compose","state":"stopped","active":false,"required":false,"containers":[]}]}`)
		return
	}
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
	if os.Getenv(projectLifecycleHelperModeEnvironment) == projectLifecycleHelperWatcherMode {
		command := exec.Command(os.Args[0], "dev")
		command.Env = projectLifecycleRestartReplaceEnvironment(
			os.Environ(),
			projectLifecycleHelperModeEnvironment,
			projectLifecycleHelperIgnoringListenerMode,
		)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		separateProjectLifecycleHelperProcessGroup(command)
		if err := command.Start(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		waitForProjectLifecycleHelperTermination()
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
	if os.Getenv(projectLifecycleHelperModeEnvironment) == projectLifecycleHelperIgnoringListenerMode {
		ignoreProjectLifecycleHelperGracefulStop()
		waitForProjectLifecycleHelperTermination()
	}
	<-stopped
	_ = server.Close()
}

// waitForProjectLifecycleHelperTermination keeps an intentionally signal-ignoring test process alive without triggering the Go deadlock detector.
func waitForProjectLifecycleHelperTermination() {
	for {
		time.Sleep(time.Hour)
	}
}

// TestProjectLifecycleCoordinatorRejectsDescriptorBeforeNetworkMutation proves invalid static intent cannot allocate a durable project identity.
func TestProjectLifecycleCoordinatorRejectsDescriptorBeforeNetworkMutation(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))
	installProjectLifecycleIntegrationForj(t, port)
	t.Setenv(projectLifecycleDescriptorModeEnvironment, "invalid")

	coordinator, _ := newProjectLifecycleIntegrationCoordinator(
		t,
		store,
		journal,
		newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 500 * time.Millisecond}),
		netip.MustParseAddr("127.0.0.1"),
		uint16(port),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("close lifecycle coordinator: %v", err)
		}
	})

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-invalid-descriptor", IntentID: "intent-start-invalid-descriptor",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	failed := waitForProjectLifecycleOperationState(t, journal, "intent-start-invalid-descriptor", domain.OperationFailed)
	if failed.Operation.Problem == nil || failed.Operation.Problem.Code != "project.descriptor.invalid" {
		t.Fatalf("failed descriptor operation = %#v", failed.Operation)
	}
	current, err := store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("read project after invalid descriptor: %v", err)
	}
	if current.Project.State != domain.ProjectStopped {
		t.Fatalf("project after invalid descriptor = %q, want stopped", current.Project.State)
	}
	network, initialized, err := store.Network(t.Context())
	if err != nil || !initialized {
		t.Fatalf("read network after invalid descriptor = %#v, %t, %v", network, initialized, err)
	}
	if len(network.Leases) != 0 || len(network.Reservations.Endpoints) != 0 {
		t.Fatalf("network mutated before descriptor admission: leases=%#v endpoints=%#v", network.Leases, network.Reservations.Endpoints)
	}
}

// TestProjectLifecycleCoordinatorKeepsReadyAppWhenServiceObservationFails proves auxiliary container context cannot tear down a healthy project.
func TestProjectLifecycleCoordinatorKeepsReadyAppWhenServiceObservationFails(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))
	installProjectLifecycleIntegrationForj(t, port)
	t.Setenv(projectLifecycleServiceReportEnvironment, "problem")

	coordinator, _ := newProjectLifecycleIntegrationCoordinator(
		t,
		store,
		journal,
		newProjectLifecycleIntegrationSupervisor(projectprocess.Options{
			GracePeriod:      500 * time.Millisecond,
			ContainerRuntime: &projectLifecycleContainerRuntime{err: errors.New("container observation unavailable")},
		}),
		netip.MustParseAddr("127.0.0.1"),
		uint16(port),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("close lifecycle coordinator: %v", err)
		}
	})

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-observation-problem", IntentID: "intent-start-observation-problem",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ready := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)
	if ready.Project.Services == nil || len(ready.Project.Services) != 0 {
		t.Fatalf("ready project services = %#v, want non-nil empty observation", ready.Project.Services)
	}
	operation := waitForProjectLifecycleOperationState(t, journal, "intent-start-observation-problem", domain.OperationSucceeded)
	if operation.Operation.Phase != "ready; service observation unavailable" {
		t.Fatalf("start operation phase = %q", operation.Operation.Phase)
	}
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

	supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{
		GracePeriod: 500 * time.Millisecond,
		ContainerRuntime: &projectLifecycleContainerRuntime{observation: containerruntime.ProjectObservation{
			Services: []containerruntime.Service{{
				ID: "mysql", Name: "mysql", State: "ready", Active: true,
			}},
		}},
	})
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
	if len(ready.Project.Services) != 1 || ready.Project.Services[0].ID != "mysql" || ready.Project.Services[0].State != domain.EntityReady || ready.Project.Services[0].Owner != domain.ServiceOwnerCompose {
		t.Fatalf("ready project services = %#v", ready.Project.Services)
	}
	session, err := store.ActiveProjectSession(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("read active session: %v", err)
	}
	firstSessionID := session.ID
	if session.DescriptorDigest != strings.Repeat("a", 64) {
		t.Fatalf("active session descriptor digest = %q, want descriptor topology digest", session.DescriptorDigest)
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
	waitForProjectLifecycleRouteStates(t, routes, []domain.ProjectState{domain.ProjectStarting, domain.ProjectReady})

	restarting, err := coordinator.Restart(t.Context(), ProjectRestartRequest{
		ProjectID: project.ID, OperationID: "operation-restart", IntentID: "intent-restart",
	})
	if err != nil || restarting.Operation.State != domain.OperationQueued {
		t.Fatalf("Restart() = %#v, %v", restarting, err)
	}
	readyAgain := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)
	restartOperation := waitForProjectLifecycleOperationState(t, journal, "intent-restart", domain.OperationSucceeded)
	if readyAgain.Project.State != domain.ProjectReady || readyAgain.Project.Resources[0].URL != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("restarted project = %#v", readyAgain.Project)
	}
	replacementSession, err := store.ActiveProjectSession(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("read replacement active session: %v", err)
	}
	if replacementSession.ID == firstSessionID || restartOperation.Operation.Kind != domain.OperationKindProjectRestart {
		t.Fatalf("replacement session = %#v, restart operation = %#v", replacementSession, restartOperation.Operation)
	}
	waitForProjectLifecycleRouteStates(
		t,
		routes,
		[]domain.ProjectState{domain.ProjectStarting, domain.ProjectReady, domain.ProjectStopping, domain.ProjectStarting, domain.ProjectReady},
	)

	stopping, err := coordinator.Stop(t.Context(), ProjectStopRequest{
		ProjectID: project.ID, OperationID: "operation-stop", IntentID: "intent-stop",
	})
	if err != nil || stopping.Operation.State != domain.OperationQueued {
		t.Fatalf("Stop() = %#v, %v", stopping, err)
	}
	stopped := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectStopped)
	if len(stopped.Project.Apps) != 1 || stopped.Project.Apps[0].State != domain.EntityStopped || len(stopped.Project.Resources) != 0 || len(stopped.Project.Services) != 1 || stopped.Project.Services[0].State != domain.EntityStopped {
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
		[]domain.ProjectState{domain.ProjectStarting, domain.ProjectReady, domain.ProjectStopping, domain.ProjectStarting, domain.ProjectReady, domain.ProjectStopping, domain.ProjectStopped},
	)
}

// TestProjectLifecycleCoordinatorLeavesNoStartingProjectAfterReadinessTimeout proves a stalled child converges to a terminal failure.
func TestProjectLifecycleCoordinatorLeavesNoStartingProjectAfterReadinessTimeout(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))
	installProjectLifecycleIntegrationForj(t, port)
	t.Setenv(projectLifecycleHelperModeEnvironment, projectLifecycleHelperIgnoringListenerMode)
	supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 100 * time.Millisecond})
	coordinator, _ := newProjectLifecycleIntegrationCoordinator(t, store, journal, supervisor, netip.MustParseAddr("127.0.0.1"), uint16(port))
	coordinator.startupTimeout = 100 * time.Millisecond
	coordinator.processJoinTimeout = 2 * time.Second
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("close lifecycle coordinator: %v", err)
		}
	})

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-readiness-timeout", IntentID: "intent-start-readiness-timeout",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	failed := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectFailed)
	if failed.Project.State != domain.ProjectFailed {
		t.Fatalf("project after readiness timeout = %q, want failed", failed.Project.State)
	}
	operation := waitForProjectLifecycleOperationState(t, journal, "intent-start-readiness-timeout", domain.OperationFailed)
	if operation.Operation.Problem == nil || operation.Operation.Problem.Code != "project.readiness.timeout" {
		t.Fatalf("readiness timeout operation = %#v", operation.Operation)
	}
	if _, err := store.ActiveProjectSession(t.Context(), project.ID); err == nil {
		t.Fatal("readiness timeout retained an active session")
	}
}

// TestProjectLifecycleCoordinatorStopsAfterRouteWithdrawalFailure proves a publication failure cannot strand a project in stopping.
func TestProjectLifecycleCoordinatorStopsAfterRouteWithdrawalFailure(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, port := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))
	installProjectLifecycleIntegrationForj(t, port)
	supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 500 * time.Millisecond})
	coordinator, _ := newProjectLifecycleIntegrationCoordinator(t, store, journal, supervisor, netip.MustParseAddr("127.0.0.1"), uint16(port))
	routeErr := errors.New("route withdrawal unavailable")
	var failRoutes atomic.Bool
	coordinator.routes = projectLifecycleRouteReconcilerFunc(func(context.Context) error {
		if failRoutes.Load() {
			return routeErr
		}
		return nil
	})
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = coordinator.Close(ctx)
	})

	if _, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID: project.ID, OperationID: "operation-start-route-failure", IntentID: "intent-start-route-failure",
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)

	failRoutes.Store(true)
	if _, err := coordinator.Stop(t.Context(), ProjectStopRequest{
		ProjectID: project.ID, OperationID: "operation-stop-route-failure", IntentID: "intent-stop-route-failure",
	}); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	stopped := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectStopped)
	if stopped.Project.State != domain.ProjectStopped {
		t.Fatalf("project after route withdrawal failure = %q, want stopped", stopped.Project.State)
	}
	operation := waitForProjectLifecycleOperationState(t, journal, "intent-stop-route-failure", domain.OperationSucceeded)
	if operation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("stop operation after route withdrawal failure = %q, want succeeded", operation.Operation.State)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closeErr := coordinator.Close(ctx)
	closed = true
	if !errors.Is(closeErr, routeErr) {
		t.Fatalf("Close() error = %v, want route withdrawal failure retained for diagnostics", closeErr)
	}
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
	return openProjectLifecycleIntegrationState(t, filepath.Join(t.TempDir(), "harbord.db"), true)
}

// openProjectLifecycleIntegrationState opens one database generation and optionally applies the production migrations.
func openProjectLifecycleIntegrationState(
	t *testing.T,
	databasePath string,
	applyMigrations bool,
) (*state.Store, *state.OperationJournal) {
	t.Helper()
	store, journal, _ := openProjectLifecycleIntegrationStateWithConnection(t, databasePath, applyMigrations)
	return store, journal
}

// openProjectLifecycleIntegrationStateWithConnection also exposes the test-only database handle for durable crash-boundary injection.
func openProjectLifecycleIntegrationStateWithConnection(
	t *testing.T,
	databasePath string,
	applyMigrations bool,
) (*state.Store, *state.OperationJournal, *gorm.DB) {
	t.Helper()
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate")
	t.Setenv("DB_HARBORD_MAX_OPEN_CONNECTIONS", "1")
	t.Setenv("DB_HARBORD_MAX_IDLE_CONNECTIONS", "1")
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close lifecycle database: %v", err)
		}
	})
	if applyMigrations {
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
	connection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open lifecycle database for test injection: %v", err)
	}
	return store, journal, connection
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
	if options.ContainerRuntime == nil {
		options.ContainerRuntime = &projectLifecycleContainerRuntime{
			observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{}},
		}
	}
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
