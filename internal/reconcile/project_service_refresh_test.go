package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// projectServiceRefreshTestState captures the durable edges used by the watcher without opening a database.
type projectServiceRefreshTestState struct {
	*state.Store
	project     state.ProjectRecord
	session     domain.ProjectSession
	network     state.NetworkRecord
	initialized bool

	mutex             sync.Mutex
	refreshes         []state.RefreshProjectServicesRequest
	runtimeRefreshes  []state.RefreshProjectRuntimeRequest
	refresh           state.ProjectRecord
	runtimeRefresh    state.ProjectRecord
	refreshErr        error
	runtimeRefreshErr error
	refreshDone       chan struct{}
	runtimeDone       chan struct{}
	refreshOnce       sync.Once
	runtimeOnce       sync.Once
}

// Project returns the fixture's current durable project projection.
func (source *projectServiceRefreshTestState) Project(context.Context, domain.ProjectID) (state.ProjectRecord, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return source.project, nil
}

// ActiveProjectSession returns the fixture's exact active process session.
func (source *projectServiceRefreshTestState) ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return source.session, nil
}

// Network returns the fixture's current primary lease for endpoint-refresh fence checks.
func (source *projectServiceRefreshTestState) Network(context.Context) (state.NetworkRecord, bool, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	return source.network, source.initialized, nil
}

// ReplaceProjectNetwork records endpoint assignment without requiring a full network database fixture.
func (source *projectServiceRefreshTestState) ReplaceProjectNetwork(
	_ context.Context,
	request state.ReplaceProjectNetworkRequest,
) (state.NetworkMutationResult, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	source.network.Reservations.Endpoints = append([]state.EndpointReservation(nil), request.Endpoints...)
	return state.NetworkMutationResult{Record: source.network}, nil
}

// RefreshProjectServices records the fenced observation and returns the next durable projection.
func (source *projectServiceRefreshTestState) RefreshProjectServices(
	_ context.Context,
	request state.RefreshProjectServicesRequest,
) (state.ProjectRecord, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	source.refreshes = append(source.refreshes, request)
	if source.refreshDone != nil {
		source.refreshOnce.Do(func() { close(source.refreshDone) })
	}
	return source.refresh, source.refreshErr
}

// RefreshProjectRuntime records a fenced complete runtime replacement for watcher assertions.
func (source *projectServiceRefreshTestState) RefreshProjectRuntime(
	_ context.Context,
	request state.RefreshProjectRuntimeRequest,
) (state.ProjectRecord, error) {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	source.runtimeRefreshes = append(source.runtimeRefreshes, request)
	if source.runtimeDone != nil {
		source.runtimeOnce.Do(func() { close(source.runtimeDone) })
	}
	return source.runtimeRefresh, source.runtimeRefreshErr
}

// Refreshes returns a defensive copy of all observations accepted by the fixture.
func (source *projectServiceRefreshTestState) Refreshes() []state.RefreshProjectServicesRequest {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	refreshes := make([]state.RefreshProjectServicesRequest, len(source.refreshes))
	copy(refreshes, source.refreshes)
	for index := range refreshes {
		refreshes[index].Services = append([]domain.ServiceSnapshot(nil), refreshes[index].Services...)
	}
	return refreshes
}

// RuntimeRefreshes returns a defensive copy of complete runtime refresh requests.
func (source *projectServiceRefreshTestState) RuntimeRefreshes() []state.RefreshProjectRuntimeRequest {
	source.mutex.Lock()
	defer source.mutex.Unlock()
	refreshes := make([]state.RefreshProjectRuntimeRequest, len(source.runtimeRefreshes))
	copy(refreshes, source.runtimeRefreshes)
	for index := range refreshes {
		refreshes[index].Services = append([]domain.ServiceSnapshot(nil), refreshes[index].Services...)
		refreshes[index].Resources = append([]domain.ResourceSnapshot(nil), refreshes[index].Resources...)
	}
	return refreshes
}

// projectServiceRefreshTestSupervisor supplies one host event and then waits for watcher cancellation.
type projectServiceRefreshTestSupervisor struct {
	*projectprocess.Supervisor
	observation         projectprocess.ServiceObservation
	resourceObservation projectprocess.FrameworkResourceObservation
	resourceErr         error
	changeErr           error
	changeErrors        []error
	observationErrors   []error

	mutex         sync.Mutex
	waitCalls     int
	observeCalls  int
	resourceCalls int
}

// WaitServiceChange emits the configured wake event once and blocks subsequent waits until cancellation.
func (supervisor *projectServiceRefreshTestSupervisor) WaitServiceChange(ctx context.Context, _ domain.ProjectID, _ domain.SessionID) error {
	supervisor.mutex.Lock()
	supervisor.waitCalls++
	call := supervisor.waitCalls
	configuredErr := supervisor.changeErr
	if call <= len(supervisor.changeErrors) {
		configuredErr = supervisor.changeErrors[call-1]
	}
	supervisor.mutex.Unlock()
	if configuredErr != nil {
		return configuredErr
	}
	if call == 1 || (len(supervisor.changeErrors) > 0 && call == len(supervisor.changeErrors)+1) {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// ObserveServices returns the complete replacement topology after the wake hint.
func (supervisor *projectServiceRefreshTestSupervisor) ObserveServices(
	context.Context,
	domain.ProjectID,
	domain.SessionID,
) (projectprocess.ServiceObservation, error) {
	supervisor.mutex.Lock()
	defer supervisor.mutex.Unlock()
	supervisor.observeCalls++
	call := supervisor.observeCalls
	if call <= len(supervisor.observationErrors) {
		return projectprocess.ServiceObservation{}, supervisor.observationErrors[call-1]
	}
	return supervisor.observation, nil
}

// ObserveFrameworkResources returns the configured fresh framework catalog after a host wake edge.
func (supervisor *projectServiceRefreshTestSupervisor) ObserveFrameworkResources(
	context.Context,
	domain.ProjectID,
	domain.SessionID,
) (projectprocess.FrameworkResourceObservation, error) {
	supervisor.mutex.Lock()
	defer supervisor.mutex.Unlock()
	supervisor.resourceCalls++
	return supervisor.resourceObservation, supervisor.resourceErr
}

// ObserveCalls returns the number of fresh host observations requested by the watcher.
func (supervisor *projectServiceRefreshTestSupervisor) ObserveCalls() int {
	supervisor.mutex.Lock()
	defer supervisor.mutex.Unlock()
	return supervisor.observeCalls
}

// ResourceObserveCalls returns the number of fresh framework catalog observations requested by the watcher.
func (supervisor *projectServiceRefreshTestSupervisor) ResourceObserveCalls() int {
	supervisor.mutex.Lock()
	defer supervisor.mutex.Unlock()
	return supervisor.resourceCalls
}

// projectServiceRefreshTestRoutes records route publication after a durable refresh.
type projectServiceRefreshTestRoutes struct {
	mutex sync.Mutex
	calls int
}

// Reconcile records one route publication edge.
func (routes *projectServiceRefreshTestRoutes) Reconcile(context.Context) error {
	routes.mutex.Lock()
	routes.calls++
	routes.mutex.Unlock()
	return nil
}

// Calls returns the number of route publication edges observed by the fixture.
func (routes *projectServiceRefreshTestRoutes) Calls() int {
	routes.mutex.Lock()
	defer routes.mutex.Unlock()
	return routes.calls
}

// projectServiceRefreshTestLeaseCoordinator binds the watcher fixture to one retained private primary address.
func projectServiceRefreshTestLeaseCoordinator(t *testing.T, source *projectServiceRefreshTestState, address netip.Addr) *projectPrimaryLeaseCoordinator {
	t.Helper()
	key, err := identity.NewPrimaryKey(source.project.Project.ID)
	if err != nil {
		t.Fatalf("NewPrimaryKey() error = %v", err)
	}
	source.network = state.NetworkRecord{
		Stage:  state.NetworkStageIdentity,
		Leases: []identity.Lease{{Key: key, Address: address}},
	}
	source.initialized = true
	return &projectPrimaryLeaseCoordinator{state: source}
}

// projectServiceRefreshTestRuntimeContext creates the immutable descriptor and target carried by a ready watcher.
func projectServiceRefreshTestRuntimeContext(t *testing.T) (projectdiscovery.RuntimeTarget, projectprocess.ProjectDescriptorObservation) {
	t.Helper()
	address := netip.MustParseAddr("127.77.4.8")
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", address, 3000)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	descriptor := projectprocess.ProjectDescriptorObservation{
		ResourcesSupported: true,
		Resources: []goforj.Resource{{
			ID: "swagger", Name: "Swagger", Category: "docs", Protocol: goforj.ResourceProtocolHTTP,
			Owner: goforj.ResourceOwnerApp, App: "app", Runtime: "http", Path: "/swagger", Enabled: true,
		}},
	}
	return target, descriptor
}

// TestWatchReadyServicesRefreshesFromFreshObservation verifies host events wake a fenced observation rather than supplying topology directly.
func TestWatchReadyServicesRefreshesFromFreshObservation(t *testing.T) {
	at := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	project := projectActivityTestProject()
	project.Project.Name = "Orders"
	project.Project.Path = "/tmp/orders"
	project.Project.Slug = "orders"
	project.Project.State = domain.ProjectReady
	project.Project.UpdatedAt = at
	project.Project.Apps = []domain.AppSnapshot{}
	project.Project.Services = []domain.ServiceSnapshot{}
	project.Project.Resources = []domain.ResourceSnapshot{}
	session := projectActivityTestSession()
	session.State = domain.SessionAttached

	service := domain.ServiceSnapshot{
		ID:        "mysql",
		Name:      "MySQL",
		Kind:      "compose",
		State:     domain.EntityReady,
		Owner:     domain.ServiceOwnerCompose,
		Selection: domain.ServiceSelected,
	}
	refreshed := project
	refreshed.Revision++
	refreshed.Project.Services = []domain.ServiceSnapshot{service}
	source := &projectServiceRefreshTestState{
		project:     project,
		session:     session,
		refresh:     refreshed,
		refreshDone: make(chan struct{}),
	}
	supervisor := &projectServiceRefreshTestSupervisor{
		observation: projectprocess.ServiceObservation{
			Supported: true,
			Services:  []domain.ServiceSnapshot{service},
		},
	}
	routes := new(projectServiceRefreshTestRoutes)
	coordinator := &ProjectLifecycleCoordinator{
		state:      source,
		supervisor: supervisor,
		routes:     routes,
		now:        func() time.Time { return at },
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.watchReadyServices(ctx, project, session)
	}()

	select {
	case <-source.refreshDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("watchReadyServices() did not refresh after the host wake event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchReadyServices() did not stop after cancellation")
	}

	refreshes := source.Refreshes()
	if len(refreshes) != 1 {
		t.Fatalf("refreshes = %#v, want exactly one refresh", refreshes)
	}
	request := refreshes[0]
	if request.ProjectID != project.Project.ID || request.SessionID != session.ID ||
		request.ExpectedProjectRevision != project.Revision || request.ExpectedSessionGeneration != session.Generation {
		t.Fatalf("refresh request identity/fences = %#v", request)
	}
	if len(request.Services) != 1 || request.Services[0] != service {
		t.Fatalf("refresh request services = %#v, want %#v", request.Services, []domain.ServiceSnapshot{service})
	}
	if request.At != at {
		t.Fatalf("refresh request time = %s, want %s", request.At, at)
	}
	if supervisor.ObserveCalls() != 1 {
		t.Fatalf("ObserveServices() calls = %d, want one fresh observation", supervisor.ObserveCalls())
	}
	if routes.Calls() != 1 {
		t.Fatalf("route reconciliation calls = %d, want one publication", routes.Calls())
	}
	if coordinator.asyncErr != nil {
		t.Fatalf("watcher async error = %v", coordinator.asyncErr)
	}
}

// TestWatchReadyServicesRefreshesFrameworkResourcesAfterFreshObservation proves endpoint work follows durable resource replacement.
func TestWatchReadyServicesRefreshesFrameworkResourcesAfterFreshObservation(t *testing.T) {
	at := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	project := projectActivityTestProject()
	project.Project.Name = "Orders"
	project.Project.Path = "/tmp/orders"
	project.Project.Slug = "orders"
	project.Project.State = domain.ProjectReady
	project.Project.UpdatedAt = at
	project.Project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true}}
	project.Project.Services = []domain.ServiceSnapshot{}
	project.Project.Resources = []domain.ResourceSnapshot{{
		ID: "app-http", Name: "App", Kind: "application", URL: "http://127.77.4.8:3000",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
	}}
	session := projectActivityTestSession()
	session.State = domain.SessionAttached
	service := domain.ServiceSnapshot{ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected}
	refreshed := project
	refreshed.Revision++
	refreshed.Project.Services = []domain.ServiceSnapshot{service}
	refreshed.Project.Resources = append([]domain.ResourceSnapshot(nil), project.Project.Resources...)
	source := &projectServiceRefreshTestState{
		project: project, session: session, runtimeRefresh: refreshed, runtimeDone: make(chan struct{}),
	}
	supervisor := &projectServiceRefreshTestSupervisor{
		observation: projectprocess.ServiceObservation{Supported: true, Services: []domain.ServiceSnapshot{service}},
		resourceObservation: projectprocess.FrameworkResourceObservation{Supported: true, Resources: []projectprocess.FrameworkResource{{
			ID: "swagger", Name: "Swagger", Kind: "docs", URL: "http://127.77.4.8:3000/swagger", App: "app", Runtime: "http",
		}}},
	}
	routes := new(projectServiceRefreshTestRoutes)
	target, descriptor := projectServiceRefreshTestRuntimeContext(t)
	coordinator := &ProjectLifecycleCoordinator{
		state: source, supervisor: supervisor, routes: routes, now: func() time.Time { return at },
		primaryLeases: projectServiceRefreshTestLeaseCoordinator(t, source, target.Address),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.watchReadyServicesWithRuntime(ctx, project, session, target, descriptor)
	}()
	select {
	case <-source.runtimeDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("watchReadyServicesWithRuntime() did not refresh framework resources after wake")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchReadyServicesWithRuntime() did not stop after cancellation")
	}
	refreshes := source.RuntimeRefreshes()
	if len(refreshes) != 1 {
		t.Fatalf("runtime refreshes = %#v, want exactly one", refreshes)
	}
	if len(refreshes[0].Resources) != 2 || refreshes[0].Resources[1].ID != "swagger" {
		t.Fatalf("runtime refresh resources = %#v, want app-http and swagger", refreshes[0].Resources)
	}
	if len(source.Refreshes()) != 0 {
		t.Fatalf("framework refresh unexpectedly used service-only path: %#v", source.Refreshes())
	}
	if supervisor.ResourceObserveCalls() != 1 {
		t.Fatalf("ObserveFrameworkResources() calls = %d, want one", supervisor.ResourceObserveCalls())
	}
	if routes.Calls() != 1 {
		t.Fatalf("route reconciliation calls = %d, want one after durable resource refresh", routes.Calls())
	}
}

// TestWatchReadyServicesPreservesResourcesWhenFrameworkObservationUnsupported keeps last-known links on an unsupported wake path.
func TestWatchReadyServicesPreservesResourcesWhenFrameworkObservationUnsupported(t *testing.T) {
	project := projectActivityTestProject()
	project.Project.State = domain.ProjectReady
	project.Project.Resources = []domain.ResourceSnapshot{{ID: "app-http", Name: "App", Kind: "application", URL: "http://127.0.0.1:3000", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}}}
	session := projectActivityTestSession()
	source := &projectServiceRefreshTestState{project: project, session: session, refresh: project, refreshDone: make(chan struct{})}
	supervisor := &projectServiceRefreshTestSupervisor{
		observation:         projectprocess.ServiceObservation{Supported: true, Services: []domain.ServiceSnapshot{}},
		resourceObservation: projectprocess.FrameworkResourceObservation{Supported: false},
	}
	routes := new(projectServiceRefreshTestRoutes)
	target, descriptor := projectServiceRefreshTestRuntimeContext(t)
	coordinator := &ProjectLifecycleCoordinator{
		state: source, supervisor: supervisor, routes: routes, now: time.Now,
		primaryLeases: projectServiceRefreshTestLeaseCoordinator(t, source, target.Address),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.watchReadyServicesWithRuntime(ctx, project, session, target, descriptor)
	}()
	select {
	case <-source.refreshDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("unsupported framework observation did not use service refresh fallback")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unsupported framework observation watcher did not stop")
	}
	if len(source.RuntimeRefreshes()) != 0 {
		t.Fatalf("unsupported framework observation produced runtime refresh: %#v", source.RuntimeRefreshes())
	}
	if len(source.Refreshes()) != 1 {
		t.Fatalf("unsupported framework observation service refreshes = %#v, want one", source.Refreshes())
	}
	if supervisor.ResourceObserveCalls() != 1 {
		t.Fatalf("ObserveFrameworkResources() calls = %d, want one", supervisor.ResourceObserveCalls())
	}
}

// TestRefreshReadyProjectRuntimePreservesResourcesWhenFrameworkObservationErrors keeps the last durable links on query failure.
func TestRefreshReadyProjectRuntimePreservesResourcesWhenFrameworkObservationErrors(t *testing.T) {
	project := projectActivityTestProject()
	session := projectActivityTestSession()
	source := &projectServiceRefreshTestState{project: project, session: session, refresh: project}
	supervisor := &projectServiceRefreshTestSupervisor{resourceErr: errors.New("resource query failed")}
	target, descriptor := projectServiceRefreshTestRuntimeContext(t)
	primaryLeases := projectServiceRefreshTestLeaseCoordinator(t, source, target.Address)
	coordinator := &ProjectLifecycleCoordinator{state: source, supervisor: supervisor, primaryLeases: primaryLeases, now: time.Now}
	refreshed, err, resourceRefresh := coordinator.refreshReadyProjectRuntime(
		t.Context(), source, source, true, project, session, project.Revision, project.Project.UpdatedAt,
		target, descriptor, []domain.ServiceSnapshot{},
	)
	if err != nil {
		t.Fatalf("refreshReadyProjectRuntime() error = %v", err)
	}
	if resourceRefresh {
		t.Fatal("resource query failure unexpectedly selected complete runtime refresh")
	}
	if refreshed.Project.ID != project.Project.ID || len(source.RuntimeRefreshes()) != 0 || len(source.Refreshes()) != 1 {
		t.Fatalf("fallback refresh state = project %#v runtime %#v services %#v", refreshed.Project, source.RuntimeRefreshes(), source.Refreshes())
	}
}

// TestWatchReadyServicesWithdrawsRoutesOnResourceFenceFailure keeps a stale route from surviving primary-address drift.
func TestWatchReadyServicesWithdrawsRoutesOnResourceFenceFailure(t *testing.T) {
	project := projectActivityTestProject()
	project.Project.State = domain.ProjectReady
	session := projectActivityTestSession()
	source := &projectServiceRefreshTestState{project: project, session: session}
	target, descriptor := projectServiceRefreshTestRuntimeContext(t)
	primaryLeases := projectServiceRefreshTestLeaseCoordinator(t, source, target.Address)
	source.network.Leases[0].Address = netip.MustParseAddr("127.77.4.9")
	supervisor := &projectServiceRefreshTestSupervisor{
		observation:         projectprocess.ServiceObservation{Supported: true, Services: []domain.ServiceSnapshot{}},
		resourceObservation: projectprocess.FrameworkResourceObservation{Supported: true},
	}
	routes := new(projectServiceRefreshTestRoutes)
	coordinator := &ProjectLifecycleCoordinator{
		state: source, supervisor: supervisor, primaryLeases: primaryLeases, routes: routes, now: time.Now,
	}
	coordinator.watchReadyServicesWithRuntime(t.Context(), project, session, target, descriptor)
	if routes.Calls() != 1 {
		t.Fatalf("route reconciliation calls = %d, want one fail-closed withdrawal edge", routes.Calls())
	}
	if coordinator.asyncErr == nil {
		t.Fatal("resource fence failure did not remain visible to the daemon")
	}
}

// TestWatchReadyServicesStopsQuietlyWhenEventsAreUnsupported verifies unsupported host event streams do not become daemon errors.
func TestWatchReadyServicesStopsQuietlyWhenEventsAreUnsupported(t *testing.T) {
	project := projectActivityTestProject()
	session := projectActivityTestSession()
	source := &projectServiceRefreshTestState{project: project, session: session}
	supervisor := &projectServiceRefreshTestSupervisor{changeErr: containerruntime.ErrProjectChangeUnsupported}
	coordinator := &ProjectLifecycleCoordinator{
		state:      source,
		supervisor: supervisor,
		routes:     new(projectServiceRefreshTestRoutes),
		now:        time.Now,
	}

	coordinator.watchReadyServices(t.Context(), project, session)

	if len(source.Refreshes()) != 0 {
		t.Fatalf("unsupported event stream produced refreshes: %#v", source.Refreshes())
	}
	if supervisor.ObserveCalls() != 0 {
		t.Fatalf("unsupported event stream triggered observations: %d", supervisor.ObserveCalls())
	}
	if coordinator.asyncErr != nil {
		t.Fatalf("unsupported event stream recorded async error: %v", coordinator.asyncErr)
	}
}

// TestWatchReadyServicesRecordsUnexpectedEventWaitFailure keeps host-stream failures visible to daemon shutdown.
func TestWatchReadyServicesRecordsUnexpectedEventWaitFailure(t *testing.T) {
	testErr := errors.New("unexpected event wait failure")
	supervisor := &projectServiceRefreshTestSupervisor{changeErr: testErr}
	project := projectActivityTestProject()
	session := projectActivityTestSession()
	coordinator := &ProjectLifecycleCoordinator{
		state:      &projectServiceRefreshTestState{project: project, session: session},
		supervisor: supervisor,
		routes:     new(projectServiceRefreshTestRoutes),
		now:        time.Now,
	}

	coordinator.watchReadyServices(t.Context(), project, session)

	if coordinator.asyncErr == nil || !errors.Is(coordinator.asyncErr, testErr) {
		t.Fatalf("watcher error = %v, want event wait failure", coordinator.asyncErr)
	}
}

// TestWatchReadyServicesReconnectsTransientEventFailure keeps a Docker restart from permanently ending service refresh.
func TestWatchReadyServicesReconnectsTransientEventFailure(t *testing.T) {
	at := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	project := projectActivityTestProject()
	project.Project.State = domain.ProjectReady
	project.Project.UpdatedAt = at
	session := projectActivityTestSession()
	service := domain.ServiceSnapshot{
		ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityReady,
		Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
	}
	refreshed := project
	refreshed.Revision++
	refreshed.Project.Services = []domain.ServiceSnapshot{service}
	source := &projectServiceRefreshTestState{
		project:     project,
		session:     session,
		refresh:     refreshed,
		refreshDone: make(chan struct{}),
	}
	supervisor := &projectServiceRefreshTestSupervisor{
		changeErrors: []error{fmt.Errorf("%w: Docker is restarting", containerruntime.ErrProjectChangeTransient)},
		observation: projectprocess.ServiceObservation{
			Supported: true,
			Services:  []domain.ServiceSnapshot{service},
		},
	}
	coordinator := &ProjectLifecycleCoordinator{
		state: source, supervisor: supervisor, routes: new(projectServiceRefreshTestRoutes), now: func() time.Time { return at },
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.watchReadyServices(ctx, project, session)
	}()
	select {
	case <-source.refreshDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("watchReadyServices() did not reconnect after a transient event failure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchReadyServices() did not stop after cancellation")
	}
	if len(source.Refreshes()) != 1 || supervisor.waitCalls < 2 {
		t.Fatalf("reconnect calls/refreshes = %d/%#v, want at least two waits and one refresh", supervisor.waitCalls, source.Refreshes())
	}
	if coordinator.asyncErr != nil {
		t.Fatalf("transient event failure became an async error: %v", coordinator.asyncErr)
	}
}

// TestWatchReadyServicesSurfacesPersistentTransientEventFailure keeps reconnect attempts bounded and diagnosable.
func TestWatchReadyServicesSurfacesPersistentTransientEventFailure(t *testing.T) {
	testErr := fmt.Errorf("%w: Docker remains unavailable", containerruntime.ErrProjectChangeTransient)
	changeErrors := make([]error, maximumServiceChangeRetries+1)
	for index := range changeErrors {
		changeErrors[index] = testErr
	}
	supervisor := &projectServiceRefreshTestSupervisor{changeErrors: changeErrors}
	project := projectActivityTestProject()
	session := projectActivityTestSession()
	coordinator := &ProjectLifecycleCoordinator{
		state:      &projectServiceRefreshTestState{project: project, session: session},
		supervisor: supervisor,
		routes:     new(projectServiceRefreshTestRoutes),
		now:        time.Now,
	}

	coordinator.watchReadyServices(t.Context(), project, session)
	if coordinator.asyncErr == nil || !errors.Is(coordinator.asyncErr, testErr) {
		t.Fatalf("persistent transient event failure = %v, want the bounded terminal error", coordinator.asyncErr)
	}
	if supervisor.waitCalls != maximumServiceChangeRetries+1 {
		t.Fatalf("transient event waits = %d, want %d", supervisor.waitCalls, maximumServiceChangeRetries+1)
	}
}

// TestWatchReadyServicesRetriesTransientObservationFailure keeps a Docker restart from losing the ready service watcher.
func TestWatchReadyServicesRetriesTransientObservationFailure(t *testing.T) {
	at := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	project := projectActivityTestProject()
	project.Project.State = domain.ProjectReady
	project.Project.UpdatedAt = at
	session := projectActivityTestSession()
	service := domain.ServiceSnapshot{
		ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityReady,
		Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
	}
	refreshed := project
	refreshed.Revision++
	refreshed.Project.Services = []domain.ServiceSnapshot{service}
	source := &projectServiceRefreshTestState{
		project:     project,
		session:     session,
		refresh:     refreshed,
		refreshDone: make(chan struct{}),
	}
	supervisor := &projectServiceRefreshTestSupervisor{
		observationErrors: []error{fmt.Errorf("%w: Docker is restarting", containerruntime.ErrProjectObservationTransient)},
		observation: projectprocess.ServiceObservation{
			Supported: true,
			Services:  []domain.ServiceSnapshot{service},
		},
	}
	coordinator := &ProjectLifecycleCoordinator{
		state: source, supervisor: supervisor, routes: new(projectServiceRefreshTestRoutes), now: func() time.Time { return at },
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coordinator.watchReadyServices(ctx, project, session)
	}()
	select {
	case <-source.refreshDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("watchReadyServices() did not retry observation after a transient Docker failure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchReadyServices() did not stop after cancellation")
	}
	if len(source.Refreshes()) != 1 || supervisor.ObserveCalls() != 2 || supervisor.waitCalls < 1 {
		t.Fatalf("observation/wait calls and refreshes = %d/%d/%#v, want two observations and one refresh", supervisor.ObserveCalls(), supervisor.waitCalls, source.Refreshes())
	}
	if coordinator.asyncErr != nil {
		t.Fatalf("transient observation failure became an async error: %v", coordinator.asyncErr)
	}
}

// TestWatchReadyServicesSurfacesPersistentTransientObservationFailure keeps Docker observation retries bounded and diagnosable.
func TestWatchReadyServicesSurfacesPersistentTransientObservationFailure(t *testing.T) {
	testErr := fmt.Errorf("%w: Docker remains unavailable", containerruntime.ErrProjectObservationTransient)
	observationErrors := make([]error, maximumServiceObservationRetries+1)
	for index := range observationErrors {
		observationErrors[index] = testErr
	}
	supervisor := &projectServiceRefreshTestSupervisor{observationErrors: observationErrors}
	project := projectActivityTestProject()
	session := projectActivityTestSession()
	coordinator := &ProjectLifecycleCoordinator{
		state:      &projectServiceRefreshTestState{project: project, session: session},
		supervisor: supervisor,
		routes:     new(projectServiceRefreshTestRoutes),
		now:        time.Now,
	}

	coordinator.watchReadyServices(t.Context(), project, session)
	if coordinator.asyncErr == nil || !errors.Is(coordinator.asyncErr, testErr) {
		t.Fatalf("persistent transient observation failure = %v, want bounded terminal error", coordinator.asyncErr)
	}
	if supervisor.ObserveCalls() != maximumServiceObservationRetries+1 || supervisor.waitCalls != 1 {
		t.Fatalf("transient observation/wait calls = %d/%d, want %d/1", supervisor.ObserveCalls(), supervisor.waitCalls, maximumServiceObservationRetries+1)
	}
}
