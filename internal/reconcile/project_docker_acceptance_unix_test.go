//go:build (linux || darwin) && projectidentityacceptance && projectdockeracceptance

package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	for _, project := range projects {
		observation := assertGeneratedComposeServices(t, ctx, store, supervisor, project)
		assertGeneratedComposeLogs(t, ctx, store, supervisor, project, observation)
	}
	after := observeGeneratedComposeContainerIDs(t, ctx, runtime, projects)
	if !sameGeneratedComposeContainerIDs(before, after) {
		t.Fatalf("generated Compose container IDs changed across Harbor observation: before=%v after=%v", before, after)
	}
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
