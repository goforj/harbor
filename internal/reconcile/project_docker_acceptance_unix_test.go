//go:build (linux || darwin) && projectidentityacceptance && projectdockeracceptance

package reconcile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

// TestNativeGeneratedMySQLProjectsExposeComposeServices proves Harbor admits Compose services only from each exact generated checkout.
func TestNativeGeneratedMySQLProjectsExposeComposeServices(t *testing.T) {
	configuration := loadProjectIdentityAcceptanceConfiguration(t)
	ctx, cancel := context.WithTimeout(t.Context(), projectIdentityAcceptanceTimeout)
	defer cancel()

	runtime, err := containerruntime.NewDocker()
	if err != nil {
		t.Fatalf("configure local Docker runtime: %v", err)
	}
	workspace, err := goforjproject.Render(ctx, goforjproject.Request{
		ForjExecutable: configuration.forj,
		GoForjVersion:  projectIdentityAcceptanceGoForjVersion,
		Projects: []goforjproject.Spec{
			{Name: "Harbor Compose Orders", Module: "example.test/harbor/compose-orders", Port: projectIdentityAcceptancePort, MySQL: true},
			{Name: "Harbor Compose Billing", Module: "example.test/harbor/compose-billing", Port: projectIdentityAcceptancePort, MySQL: true},
			{Name: "Harbor Compose Inventory", Module: "example.test/harbor/compose-inventory", Port: projectIdentityAcceptancePort, MySQL: true},
		},
	})
	if err != nil {
		t.Fatalf("render generated Compose projects: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("remove generated Compose workspace: %v", closeErr)
		}
	})

	t.Setenv("PATH", filepath.Dir(configuration.forj)+string(os.PathListSeparator)+os.Getenv("PATH"))
	projectEnvironment := projectprocess.CaptureEnvironment()
	projects := []projectIdentityAcceptanceProject{
		{id: "project-compose-orders", intent: "intent-start-compose-orders", project: workspace.Projects[0]},
		{id: "project-compose-billing", intent: "intent-start-compose-billing", project: workspace.Projects[1]},
		{id: "project-compose-inventory", intent: "intent-start-compose-inventory", project: workspace.Projects[2]},
	}
	store, journal := newProjectLifecycleIntegrationState(t)
	registerProjectIdentityAcceptanceProjects(t, store, projects)
	initializeProjectIdentityAcceptanceNetwork(t, store, configuration.prefix, configuration.addresses, projects)

	supervisor := projectprocess.New(projectprocess.Options{
		GracePeriod:          3 * time.Second,
		Environment:          projectEnvironment,
		ContainerRuntime:     runtime,
		ServiceLogIdlePeriod: time.Second,
	})
	coordinator := NewProjectLifecycleCoordinator(store, journal, supervisor, projectLifecycleTestRouteReconciler{})
	t.Cleanup(func() {
		closeContext, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()
		if closeErr := coordinator.Close(closeContext); closeErr != nil && closeContext.Err() == nil {
			t.Errorf("close generated Compose coordinator: %v", closeErr)
		}
	})

	for index, project := range projects {
		queued, startErr := coordinator.Start(ctx, ProjectStartRequest{
			ProjectID:   project.id,
			OperationID: domain.OperationID(fmt.Sprintf("operation-start-compose-%d", index+1)),
			IntentID:    project.intent,
		})
		if startErr != nil || queued.Operation.State != domain.OperationQueued {
			t.Fatalf("start generated Compose project %q = %#v, %v", project.id, queued, startErr)
		}
	}

	ready := make([]state.ProjectRecord, 0, len(projects))
	for _, project := range projects {
		ready = append(ready, waitForProjectIdentityAcceptanceState(t, ctx, store, journal, project.id, project.intent, domain.ProjectReady))
	}
	before := observeGeneratedComposeContainerIDs(t, ctx, runtime, projects)
	observations := make(map[domain.ProjectID]projectprocess.ServiceObservation, len(projects))
	for _, project := range projects {
		observation := assertGeneratedComposeServices(t, ctx, store, supervisor, project)
		observations[project.id] = observation
		assertGeneratedComposeLogs(t, ctx, store, supervisor, project, observation)
	}
	after := observeGeneratedComposeContainerIDs(t, ctx, runtime, projects)
	if !sameGeneratedComposeContainerIDs(before, after) {
		t.Fatalf("generated Compose container IDs changed across Harbor observation: before=%v after=%v", before, after)
	}
	assertGeneratedComposeEventRefresh(t, ctx, store, runtime, supervisor, projects, projects[0], observations[projects[0].id])
	assertProjectIdentityAcceptanceEndpoints(t, ctx, store, projects, ready, configuration.addresses)

	stopped := projects[0]
	stopIntent := domain.IntentID("intent-stop-compose-orders")
	queued, stopErr := coordinator.Stop(ctx, ProjectStopRequest{
		ProjectID:   stopped.id,
		OperationID: "operation-stop-compose-orders",
		IntentID:    stopIntent,
	})
	if stopErr != nil || queued.Operation.State != domain.OperationQueued {
		t.Fatalf("stop generated Compose project %q = %#v, %v", stopped.id, queued, stopErr)
	}
	waitForProjectIdentityAcceptanceState(t, ctx, store, journal, stopped.id, stopIntent, domain.ProjectStopped)
	assertProjectIdentityAcceptanceEndpoints(t, ctx, store, projects[1:], ready[1:], configuration.addresses[1:])

	restartIntent := domain.IntentID("intent-restart-compose-orders")
	queued, restartErr := coordinator.Start(ctx, ProjectStartRequest{
		ProjectID:   stopped.id,
		OperationID: "operation-restart-compose-orders",
		IntentID:    restartIntent,
	})
	if restartErr != nil || queued.Operation.State != domain.OperationQueued {
		t.Fatalf("restart generated Compose project %q = %#v, %v", stopped.id, queued, restartErr)
	}
	ready[0] = waitForProjectIdentityAcceptanceState(t, ctx, store, journal, stopped.id, restartIntent, domain.ProjectReady)
	assertGeneratedComposeServices(t, ctx, store, supervisor, stopped)
	assertProjectIdentityAcceptanceEndpoints(t, ctx, store, projects, ready, configuration.addresses)
}

// assertGeneratedComposeEventRefresh proves a test-controlled Compose replacement reaches Harbor through the event wake path.
func assertGeneratedComposeEventRefresh(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	runtime *containerruntime.DockerRuntime,
	supervisor *projectprocess.Supervisor,
	projects []projectIdentityAcceptanceProject,
	target projectIdentityAcceptanceProject,
	initial projectprocess.ServiceObservation,
) {
	t.Helper()
	if !initial.Supported || len(initial.Services) == 0 {
		t.Fatalf("event-refresh target %q has no supported service observation: %#v", target.id, initial)
	}
	service := initial.Services[0]
	beforeProject, err := store.Project(ctx, target.id)
	if err != nil {
		t.Fatalf("read event-refresh target %q before replacement: %v", target.id, err)
	}
	beforeServiceIDs := generatedComposeServiceContainerIDs(t, ctx, runtime, target, service.ID)
	if len(beforeServiceIDs) == 0 {
		t.Fatalf("event-refresh target %q service %q has no admitted containers before replacement", target.id, service.ID)
	}
	composeProject := generatedComposeServiceProject(t, ctx, runtime, target, service.ID)
	beforeAll := observeGeneratedComposeContainerIDs(t, ctx, runtime, projects)

	runGeneratedComposeCommand(t, ctx, target.project.Root, composeProject, "stop", string(service.ID))
	stopped := waitForGeneratedComposeServiceProjection(t, ctx, store, target.id, service.ID, false)
	if stopped.Project.State != domain.ProjectReady {
		t.Fatalf("event-refresh target %q state after service stop = %q, want ready", target.id, stopped.Project.State)
	}
	duringAll := observeGeneratedComposeContainerIDs(t, ctx, runtime, projects)
	assertGeneratedComposeNeighborContainerIDs(t, beforeAll, duringAll, target.id)

	runGeneratedComposeCommand(t, ctx, target.project.Root, composeProject, "up", "--detach", "--force-recreate", string(service.ID))
	refreshed := waitForGeneratedComposeServiceProjection(t, ctx, store, target.id, service.ID, true)
	if refreshed.Revision <= beforeProject.Revision {
		t.Fatalf("event-refresh target %q revision = %d, want greater than %d", target.id, refreshed.Revision, beforeProject.Revision)
	}
	afterServiceIDs := generatedComposeServiceContainerIDs(t, ctx, runtime, target, service.ID)
	if len(afterServiceIDs) == 0 {
		t.Fatalf("event-refresh target %q service %q has no admitted containers after replacement", target.id, service.ID)
	}
	for id := range beforeServiceIDs {
		if _, stillPresent := afterServiceIDs[id]; stillPresent {
			t.Fatalf("event-refresh target %q service %q retained container %q across force recreation", target.id, service.ID, id)
		}
	}
	afterAll := observeGeneratedComposeContainerIDs(t, ctx, runtime, projects)
	assertGeneratedComposeNeighborContainerIDs(t, beforeAll, afterAll, target.id)
}

// generatedComposeServiceContainerIDs returns the current admitted identities for one exact generated service.
func generatedComposeServiceContainerIDs(
	t *testing.T,
	ctx context.Context,
	runtime *containerruntime.DockerRuntime,
	project projectIdentityAcceptanceProject,
	serviceID domain.ServiceID,
) map[string]struct{} {
	t.Helper()
	observation, err := runtime.ObserveProject(ctx, project.project.Root)
	if err != nil {
		t.Fatalf("observe event-refresh runtime for %q: %v", project.id, err)
	}
	ids := make(map[string]struct{})
	for _, service := range observation.Services {
		if service.ID != string(serviceID) || !service.Active {
			continue
		}
		for _, container := range service.Containers {
			if container.ID == "" {
				t.Fatalf("event-refresh service %q for %q contains an empty container ID", serviceID, project.id)
			}
			ids[container.ID] = struct{}{}
		}
	}
	return ids
}

// generatedComposeServiceProject returns the exact Compose project label needed by the test-owned replacement command.
func generatedComposeServiceProject(
	t *testing.T,
	ctx context.Context,
	runtime *containerruntime.DockerRuntime,
	project projectIdentityAcceptanceProject,
	serviceID domain.ServiceID,
) string {
	t.Helper()
	observation, err := runtime.ObserveProject(ctx, project.project.Root)
	if err != nil {
		t.Fatalf("observe event-refresh Compose project for %q: %v", project.id, err)
	}
	for _, service := range observation.Services {
		if service.ID == string(serviceID) && service.Project != "" {
			return service.Project
		}
	}
	t.Fatalf("event-refresh service %q for %q has no Compose project identity", serviceID, project.id)
	return ""
}

// waitForGeneratedComposeServiceProjection waits for one durable service identity to appear or disappear after a host event.
func waitForGeneratedComposeServiceProjection(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	projectID domain.ProjectID,
	serviceID domain.ServiceID,
	wantPresent bool,
) state.ProjectRecord {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		record, err := store.Project(ctx, projectID)
		if err == nil {
			present := false
			for _, service := range record.Project.Services {
				if service.ID == serviceID {
					present = true
					break
				}
			}
			if present == wantPresent {
				return record
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for generated Compose service %q on project %q to become present=%t: %v", serviceID, projectID, wantPresent, ctx.Err())
		case <-ticker.C:
		}
	}
}

// runGeneratedComposeCommand performs only the test-owned Compose mutation used to create a native replacement event.
func runGeneratedComposeCommand(
	t *testing.T,
	ctx context.Context,
	checkoutRoot string,
	composeProject string,
	arguments ...string,
) {
	t.Helper()
	base := []string{"compose", "--project-directory", checkoutRoot, "--project-name", composeProject}
	command := exec.CommandContext(ctx, "docker", append(base, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(append(base, arguments...), " "), err, output)
	}
}

// assertGeneratedComposeNeighborContainerIDs proves a test-controlled target mutation cannot alter peer projects.
func assertGeneratedComposeNeighborContainerIDs(
	t *testing.T,
	before map[string]domain.ProjectID,
	after map[string]domain.ProjectID,
	targetID domain.ProjectID,
) {
	t.Helper()
	for containerID, projectID := range before {
		if projectID == targetID {
			continue
		}
		if after[containerID] != projectID {
			t.Fatalf("neighbor container %q for project %q changed during target %q replacement", containerID, projectID, targetID)
		}
	}
	for containerID, projectID := range after {
		if projectID == targetID {
			continue
		}
		if before[containerID] != projectID {
			t.Fatalf("new neighbor container %q for project %q appeared during target %q replacement", containerID, projectID, targetID)
		}
	}
}

// assertGeneratedComposeServices confirms one running generated checkout cannot accidentally project an empty or foreign service set.
func assertGeneratedComposeServices(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	supervisor *projectprocess.Supervisor,
	project projectIdentityAcceptanceProject,
) projectprocess.ServiceObservation {
	t.Helper()
	session, err := store.ActiveProjectSession(ctx, project.id)
	if err != nil {
		t.Fatalf("read active generated Compose session %q: %v", project.id, err)
	}
	observation, err := supervisor.ObserveServices(ctx, project.id, session.ID)
	if err != nil {
		t.Fatalf("observe generated Compose services for %q: %v", project.id, err)
	}
	if !observation.Supported || len(observation.Services) == 0 {
		t.Fatalf("generated Compose services for %q = %#v, want one or more admitted services", project.id, observation)
	}
	return observation
}

// assertGeneratedComposeLogs opens one exact service follower so the generated project cannot satisfy service discovery while its log boundary is unusable.
func assertGeneratedComposeLogs(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	supervisor *projectprocess.Supervisor,
	project projectIdentityAcceptanceProject,
	observation projectprocess.ServiceObservation,
) {
	t.Helper()
	session, err := store.ActiveProjectSession(ctx, project.id)
	if err != nil {
		t.Fatalf("read generated Compose session %q for logs: %v", project.id, err)
	}
	selection, err := supervisor.ReadServiceLogs(ctx, project.id, session.ID, observation.Services[0].ID, 0)
	if err != nil {
		t.Fatalf("read generated Compose logs for %q: %v", project.id, err)
	}
	if !selection.Supported || !selection.Available || selection.Problem != nil {
		t.Fatalf("generated Compose logs for %q = %#v, want supported available follower without problem", project.id, selection)
	}
}

// observeGeneratedComposeContainerIDs records exact admitted container ownership for every generated checkout and rejects any shared runtime identity.
func observeGeneratedComposeContainerIDs(
	t *testing.T,
	ctx context.Context,
	runtime *containerruntime.DockerRuntime,
	projects []projectIdentityAcceptanceProject,
) map[string]domain.ProjectID {
	t.Helper()
	identities := make(map[string]domain.ProjectID)
	for _, project := range projects {
		observation, err := runtime.ObserveProject(ctx, project.project.Root)
		if err != nil {
			t.Fatalf("observe generated Compose runtime for %q: %v", project.id, err)
		}
		for _, service := range observation.Services {
			if !service.Active {
				continue
			}
			for _, container := range service.Containers {
				if container.ID == "" {
					t.Fatalf("generated Compose service %q for %q has an empty container ID", service.ID, project.id)
				}
				if owner, exists := identities[container.ID]; exists {
					t.Fatalf("generated Compose container %q is admitted to both %q and %q", container.ID, owner, project.id)
				}
				identities[container.ID] = project.id
			}
		}
	}
	if len(identities) == 0 {
		t.Fatal("generated Compose runtime has no admitted container IDs")
	}
	return identities
}

// sameGeneratedComposeContainerIDs compares exact admitted container identity ownership without depending on map iteration order.
func sameGeneratedComposeContainerIDs(left, right map[string]domain.ProjectID) bool {
	if len(left) != len(right) {
		return false
	}
	for identity, owner := range left {
		if right[identity] != owner {
			return false
		}
	}
	return true
}
