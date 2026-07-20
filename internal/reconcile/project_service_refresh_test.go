package reconcile

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// projectServiceRefreshTestState captures the durable edges used by the watcher without opening a database.
type projectServiceRefreshTestState struct {
	*state.Store
	project state.ProjectRecord
	session domain.ProjectSession

	mutex       sync.Mutex
	refreshes   []state.RefreshProjectServicesRequest
	refresh     state.ProjectRecord
	refreshErr  error
	refreshDone chan struct{}
	refreshOnce sync.Once
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

// projectServiceRefreshTestSupervisor supplies one host event and then waits for watcher cancellation.
type projectServiceRefreshTestSupervisor struct {
	*projectprocess.Supervisor
	observation projectprocess.ServiceObservation
	changeErr   error

	mutex        sync.Mutex
	waitCalls    int
	observeCalls int
}

// WaitServiceChange emits the configured wake event once and blocks subsequent waits until cancellation.
func (supervisor *projectServiceRefreshTestSupervisor) WaitServiceChange(ctx context.Context, _ domain.ProjectID, _ domain.SessionID) error {
	supervisor.mutex.Lock()
	supervisor.waitCalls++
	call := supervisor.waitCalls
	configuredErr := supervisor.changeErr
	supervisor.mutex.Unlock()
	if configuredErr != nil {
		return configuredErr
	}
	if call == 1 {
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
	return supervisor.observation, nil
}

// ObserveCalls returns the number of fresh host observations requested by the watcher.
func (supervisor *projectServiceRefreshTestSupervisor) ObserveCalls() int {
	supervisor.mutex.Lock()
	defer supervisor.mutex.Unlock()
	return supervisor.observeCalls
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
