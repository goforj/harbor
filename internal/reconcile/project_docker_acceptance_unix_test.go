//go:build (linux || darwin) && projectidentityacceptance && projectdockeracceptance

package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/productproof"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

const (
	projectIdentityAcceptanceProductEvidenceDirectoryEnvironment = "HARBOR_DOCKER_PRODUCT_EVIDENCE_DIRECTORY"
	projectIdentityAcceptanceProductCommitEnvironment            = "HARBOR_DOCKER_PRODUCT_COMMIT"
	projectIdentityAcceptanceProductRunnerNameEnvironment        = "HARBOR_DOCKER_PRODUCT_RUNNER_NAME"
	projectIdentityAcceptanceProductRunnerImageEnvironment       = "HARBOR_DOCKER_PRODUCT_RUNNER_IMAGE"
	projectIdentityAcceptanceProductRunnerVersionEnvironment     = "HARBOR_DOCKER_PRODUCT_RUNNER_IMAGE_VERSION"
	projectIdentityAcceptanceProductEngineKindEnvironment        = "HARBOR_DOCKER_PRODUCT_ENGINE_KIND"
)

// TestNativeGeneratedMySQLProjectsExposeComposeServices proves Harbor admits Compose services only from each exact generated checkout.
func TestNativeGeneratedMySQLProjectsExposeComposeServices(t *testing.T) {
	configuration := loadProjectIdentityAcceptanceConfiguration(t)
	ctx, cancel := context.WithTimeout(t.Context(), projectIdentityAcceptanceTimeout)
	defer cancel()
	productEvidenceDirectory := strings.TrimSpace(os.Getenv(projectIdentityAcceptanceProductEvidenceDirectoryEnvironment))
	var lifecycleEvidence *productproof.DockerProjectEvidence
	var cleanupEvidence *productproof.DockerCleanupEvidence
	composeProjects := make(map[domain.ProjectID]string)
	if productEvidenceDirectory != "" {
		t.Cleanup(func() {
			if t.Failed() {
				return
			}
			if lifecycleEvidence == nil || cleanupEvidence == nil {
				t.Errorf("Docker product evidence was not finalized")
				return
			}
			cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			if err := assertGeneratedComposeProjectsClean(cleanupContext, composeProjects); err != nil {
				t.Errorf("verify generated Compose cleanup: %v", err)
				return
			}
			if err := productproof.WriteDockerProjectEvidence(productEvidenceDirectory, *lifecycleEvidence, *cleanupEvidence); err != nil {
				t.Errorf("write Docker product evidence: %v", err)
				return
			}
			if err := productproof.VerifyDockerProjectEvidenceDirectory(
				filepath.Dir(productEvidenceDirectory),
				productproof.DockerProjectRequirement{
					Commit:      lifecycleEvidence.Runtime.Commit,
					Platforms:   []string{lifecycleEvidence.Runtime.GOOS},
					AppPort:     projectIdentityAcceptancePort,
					ServicePort: 3306,
				},
			); err != nil {
				t.Errorf("verify Docker product evidence: %v", err)
			}
		})
	}

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
		composeProjects[project.id] = generatedComposeServiceProject(t, ctx, runtime, project, observation.Services[0].ID)
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
	assertGeneratedComposePeerSurvival(t, ctx, store, projects[1:])
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
	if productEvidenceDirectory != "" {
		lifecycle, cleanup, evidenceErr := buildGeneratedDockerProductEvidence(
			ctx,
			configuration.forj,
			projects,
			configuration.addresses,
			runtime,
			composeProjects,
		)
		if evidenceErr != nil {
			t.Fatalf("build Docker product evidence: %v", evidenceErr)
		}
		lifecycleEvidence = &lifecycle
		cleanupEvidence = &cleanup
	}
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

// assertGeneratedComposePeerSurvival proves stopping one project leaves every other ready project online.
func assertGeneratedComposePeerSurvival(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	projects []projectIdentityAcceptanceProject,
) {
	t.Helper()
	for _, project := range projects {
		record, err := store.Project(ctx, project.id)
		if err != nil {
			t.Fatalf("read peer project %q after target stop: %v", project.id, err)
		}
		if record.Project.State != domain.ProjectReady || len(record.Project.Services) == 0 {
			t.Fatalf("peer project %q after target stop = state %q services %#v, want ready with services", project.id, record.Project.State, record.Project.Services)
		}
	}
}

// buildGeneratedDockerProductEvidence converts only completed native observations into the fixed product-proof schema.
func buildGeneratedDockerProductEvidence(
	ctx context.Context,
	forj string,
	projects []projectIdentityAcceptanceProject,
	addresses []netip.Addr,
	dockerRuntime *containerruntime.DockerRuntime,
	composeProjects map[domain.ProjectID]string,
) (productproof.DockerProjectEvidence, productproof.DockerCleanupEvidence, error) {
	if len(projects) != 3 || len(addresses) != len(projects) {
		return productproof.DockerProjectEvidence{}, productproof.DockerCleanupEvidence{}, fmt.Errorf("Docker product evidence requires three projects and matching addresses")
	}
	runtimeEvidence, dependencies, err := generatedDockerProductRuntimeEvidence(ctx, forj)
	if err != nil {
		return productproof.DockerProjectEvidence{}, productproof.DockerCleanupEvidence{}, err
	}
	digestPaths := append([]string{forj}, projectConfigurationPaths(projects)...)
	digests := make([]string, 0, len(digestPaths))
	seenDigests := make(map[string]struct{}, len(digestPaths))
	for _, path := range digestPaths {
		digest, digestErr := sha256File(path)
		if digestErr != nil {
			return productproof.DockerProjectEvidence{}, productproof.DockerCleanupEvidence{}, digestErr
		}
		if _, exists := seenDigests[digest]; exists {
			continue
		}
		seenDigests[digest] = struct{}{}
		digests = append(digests, digest)
	}
	projectEvidence := make([]productproof.ProjectEvidence, 0, len(projects))
	projectIDs := make([]string, 0, len(projects))
	for index, project := range projects {
		observation, observeErr := dockerRuntime.ObserveProject(ctx, project.project.Root)
		if observeErr != nil {
			return productproof.DockerProjectEvidence{}, productproof.DockerCleanupEvidence{}, fmt.Errorf("observe final Docker project %q: %w", project.id, observeErr)
		}
		containerIDs, servicePort, serviceErr := generatedDockerProjectFacts(observation)
		if serviceErr != nil {
			return productproof.DockerProjectEvidence{}, productproof.DockerCleanupEvidence{}, fmt.Errorf("project %q product facts: %w", project.id, serviceErr)
		}
		if composeProjects[project.id] == "" {
			return productproof.DockerProjectEvidence{}, productproof.DockerCleanupEvidence{}, fmt.Errorf("project %q has no exact Compose identity", project.id)
		}
		projectEvidence = append(projectEvidence, productproof.ProjectEvidence{
			ID:           string(project.id),
			Address:      addresses[index].String(),
			AppPort:      projectIdentityAcceptancePort,
			ServicePort:  servicePort,
			ContainerIDs: containerIDs,
		})
		projectIDs = append(projectIDs, string(project.id))
	}
	assertions := []productproof.AssertionEvidence{
		{ID: "docker.projects.generated", Passed: true, Detail: "three generated GoForj projects reached ready with Compose services"},
		{ID: "docker.projects.isolated", Passed: true, Detail: "admitted container identities were disjoint across registered checkouts"},
		{ID: "docker.adapter.read_only", Passed: true, Detail: "Harbor observations and log selection did not mutate initial container identities"},
		{ID: "docker.logs.available", Passed: true, Detail: "one exact service log follower was opened for every generated project"},
		{ID: "docker.events.refresh", Passed: true, Detail: "a test-controlled service replacement woke a fenced fresh Harbor observation"},
		{ID: "docker.projects.stop_peer_survival", Passed: true, Detail: "two peer projects remained ready while one project stopped"},
		{ID: "docker.projects.restart", Passed: true, Detail: "the stopped project restarted on its original assigned identity"},
	}
	lifecycle := productproof.DockerProjectEvidence{
		SchemaVersion:   productproof.DockerProjectEvidenceSchemaVersion,
		Capability:      "docker_project_lifecycle",
		Scope:           "product_end_to_end",
		Runtime:         runtimeEvidence,
		Dependencies:    dependencies,
		Projects:        projectEvidence,
		Assertions:      assertions,
		ArtifactDigests: append([]string(nil), digests...),
	}
	cleanup := productproof.DockerCleanupEvidence{
		SchemaVersion:   productproof.DockerProjectEvidenceSchemaVersion,
		Capability:      "docker_project_lifecycle_cleanup",
		Scope:           "product_end_to_end",
		Runtime:         runtimeEvidence,
		ProjectIDs:      projectIDs,
		Assertions:      []productproof.AssertionEvidence{{ID: "docker.cleanup.exact", Passed: true, Detail: "only the exact generated Compose project namespace was removed"}},
		ArtifactDigests: append([]string(nil), digests...),
	}
	return lifecycle, cleanup, nil
}

// generatedDockerProductRuntimeEvidence reads only explicit protected-worker identity and pinned dependency inputs.
func generatedDockerProductRuntimeEvidence(ctx context.Context, forj string) (productproof.RuntimeEvidence, productproof.DependencyEvidence, error) {
	read := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", fmt.Errorf("Docker product evidence environment %s is required", name)
		}
		return value, nil
	}
	commit, err := read(projectIdentityAcceptanceProductCommitEnvironment)
	if err != nil {
		return productproof.RuntimeEvidence{}, productproof.DependencyEvidence{}, err
	}
	runnerName, err := read(projectIdentityAcceptanceProductRunnerNameEnvironment)
	if err != nil {
		return productproof.RuntimeEvidence{}, productproof.DependencyEvidence{}, err
	}
	runnerImage, err := read(projectIdentityAcceptanceProductRunnerImageEnvironment)
	if err != nil {
		return productproof.RuntimeEvidence{}, productproof.DependencyEvidence{}, err
	}
	runnerVersion, err := read(projectIdentityAcceptanceProductRunnerVersionEnvironment)
	if err != nil {
		return productproof.RuntimeEvidence{}, productproof.DependencyEvidence{}, err
	}
	engineKind, err := read(projectIdentityAcceptanceProductEngineKindEnvironment)
	if err != nil {
		return productproof.RuntimeEvidence{}, productproof.DependencyEvidence{}, err
	}
	engineVersion, err := generatedDockerEngineVersion(ctx)
	if err != nil {
		return productproof.RuntimeEvidence{}, productproof.DependencyEvidence{}, err
	}
	forjDigest, err := sha256File(forj)
	if err != nil {
		return productproof.RuntimeEvidence{}, productproof.DependencyEvidence{}, err
	}
	return productproof.RuntimeEvidence{
			GOOS:               runtime.GOOS,
			GOARCH:             runtime.GOARCH,
			Commit:             commit,
			RunnerName:         runnerName,
			RunnerImage:        runnerImage,
			RunnerImageVersion: runnerVersion,
		}, productproof.DependencyEvidence{
			GoForjVersion: projectIdentityAcceptanceGoForjVersion,
			GoForjDigest:  forjDigest,
			EngineKind:    engineKind,
			EngineVersion: engineVersion,
		}, nil
}

// generatedDockerEngineVersion reads the server version through Docker's read-only CLI metadata endpoint.
func generatedDockerEngineVersion(ctx context.Context) (string, error) {
	command := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read Docker Engine version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	version := strings.TrimSpace(string(output))
	if version == "" || len(version) > 128 {
		return "", fmt.Errorf("Docker Engine version is empty or oversized")
	}
	return version, nil
}

// projectConfigurationPaths returns only generated non-secret descriptor files included in proof digests.
func projectConfigurationPaths(projects []projectIdentityAcceptanceProject) []string {
	paths := make([]string, 0, len(projects))
	for _, project := range projects {
		paths = append(paths, project.project.ConfigurationPath)
	}
	return paths
}

// generatedDockerProjectFacts extracts exact active container IDs and proves every generated project exposes MySQL port 3306.
func generatedDockerProjectFacts(observation containerruntime.ProjectObservation) ([]string, uint16, error) {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	portFound := false
	for _, service := range observation.Services {
		if !service.Active {
			continue
		}
		for _, container := range service.Containers {
			if container.ID == "" {
				return nil, 0, fmt.Errorf("active service %q contains an empty container identity", service.ID)
			}
			if _, exists := seen[container.ID]; !exists {
				seen[container.ID] = struct{}{}
				ids = append(ids, container.ID)
			}
			for _, port := range container.Ports {
				if port.Private == 3306 || port.Public == 3306 {
					portFound = true
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil, 0, fmt.Errorf("no active admitted containers")
	}
	if !portFound {
		return nil, 0, fmt.Errorf("no admitted MySQL service port 3306")
	}
	sort.Strings(ids)
	return ids, 3306, nil
}

// sha256File computes one bounded artifact identity without retaining its contents in the manifest process.
func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open artifact %q: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	const maximumDigestBytes = 256 << 20
	count, err := io.Copy(hash, io.LimitReader(file, maximumDigestBytes+1))
	if err != nil {
		return "", fmt.Errorf("hash artifact %q: %w", path, err)
	}
	if count > maximumDigestBytes {
		return "", fmt.Errorf("artifact %q exceeds %d bytes", path, maximumDigestBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// assertGeneratedComposeProjectsClean verifies exact Compose labels are gone without deleting or inspecting foreign namespaces.
func assertGeneratedComposeProjectsClean(ctx context.Context, composeProjects map[domain.ProjectID]string) error {
	if len(composeProjects) != 3 {
		return fmt.Errorf("expected cleanup identities for three generated projects, got %d", len(composeProjects))
	}
	for projectID, composeProject := range composeProjects {
		if strings.TrimSpace(composeProject) == "" {
			return fmt.Errorf("project %q has an empty Compose cleanup identity", projectID)
		}
		command := exec.CommandContext(ctx, "docker", "ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project="+composeProject)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("inspect Compose cleanup for project %q: %w: %s", projectID, err, strings.TrimSpace(string(output)))
		}
		if strings.TrimSpace(string(output)) != "" {
			return fmt.Errorf("Compose project %q still has containers after Harbor cleanup", projectID)
		}
	}
	return nil
}

// TestGeneratedDockerProductRuntimeEvidenceRequiresProtectedIdentity keeps local acceptance runs from inventing worker metadata.
func TestGeneratedDockerProductRuntimeEvidenceRequiresProtectedIdentity(t *testing.T) {
	for _, name := range []string{
		projectIdentityAcceptanceProductCommitEnvironment,
		projectIdentityAcceptanceProductRunnerNameEnvironment,
		projectIdentityAcceptanceProductRunnerImageEnvironment,
		projectIdentityAcceptanceProductRunnerVersionEnvironment,
		projectIdentityAcceptanceProductEngineKindEnvironment,
	} {
		t.Setenv(name, "")
	}
	_, _, err := generatedDockerProductRuntimeEvidence(t.Context(), filepath.Join(t.TempDir(), "forj"))
	if err == nil || !strings.Contains(err.Error(), projectIdentityAcceptanceProductCommitEnvironment) {
		t.Fatalf("generatedDockerProductRuntimeEvidence() error = %v, want explicit commit identity failure", err)
	}
}

// TestGeneratedDockerProjectFactsRequiresActiveMySQLPort covers the bounded native facts admitted into product evidence.
func TestGeneratedDockerProjectFactsRequiresActiveMySQLPort(t *testing.T) {
	tests := []struct {
		name        string
		observation containerruntime.ProjectObservation
		wantError   string
		wantIDs     string
	}{
		{name: "empty", observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{}}, wantError: "no active admitted containers"},
		{name: "missing port", observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{{ID: "mysql", Active: true, Containers: []containerruntime.Container{{ID: "mysql-1"}}}}}, wantError: "no admitted MySQL service port"},
		{name: "valid", observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{{ID: "mysql", Active: true, Containers: []containerruntime.Container{{ID: "mysql-2", Ports: []containerruntime.Port{{Private: 3306}}}, {ID: "mysql-1", Ports: []containerruntime.Port{{Private: 3306}}}}}}}, wantIDs: "mysql-1,mysql-2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ids, port, err := generatedDockerProjectFacts(test.observation)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("generatedDockerProjectFacts() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || port != 3306 || strings.Join(ids, ",") != test.wantIDs {
				t.Fatalf("generatedDockerProjectFacts() = ids %v port %d err %v, want ids %q port 3306", ids, port, err, test.wantIDs)
			}
		})
	}
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
