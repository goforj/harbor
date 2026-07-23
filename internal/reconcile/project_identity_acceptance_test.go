//go:build projectidentityacceptance

package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforjruntime"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/testkit/goforjproject"
)

const (
	projectIdentityAcceptanceForjEnvironment             = "HARBOR_PROJECT_IDENTITY_FORJ_BINARY"
	projectIdentityAcceptanceAddressesEnvironment        = "HARBOR_PROOF_ADDRESSES"
	projectIdentityAcceptanceGoForjVersion               = "0.19.0"
	projectIdentityAcceptancePort                 uint16 = 3000
	projectIdentityAcceptanceTimeout                     = 8 * time.Minute
)

// projectIdentityAcceptanceProject joins generated metadata to its durable Harbor identity.
type projectIdentityAcceptanceProject struct {
	id      domain.ProjectID
	intent  domain.IntentID
	project goforjproject.Project
}

// TestGeneratedProjectsSharePortAcrossDistinctLoopbacks proves Harbor launches real generated Apps on one common port without host conflicts.
func TestGeneratedProjectsSharePortAcrossDistinctLoopbacks(t *testing.T) {
	configuration := loadProjectIdentityAcceptanceConfiguration(t)
	ctx, cancel := context.WithTimeout(t.Context(), projectIdentityAcceptanceTimeout)
	defer cancel()

	workspace, err := goforjproject.Render(ctx, goforjproject.Request{
		ForjExecutable: configuration.forj,
		GoForjVersion:  projectIdentityAcceptanceGoForjVersion,
		Projects: []goforjproject.Spec{
			{Name: "Harbor Orders", Module: "example.test/harbor/orders", Port: projectIdentityAcceptancePort},
			{Name: "Harbor Billing", Module: "example.test/harbor/billing", Port: projectIdentityAcceptancePort},
			{Name: "Harbor Inventory", Module: "example.test/harbor/inventory", Port: projectIdentityAcceptancePort},
		},
	})
	if err != nil {
		t.Fatalf("render generated projects: %v", err)
	}
	t.Cleanup(func() {
		if err := workspace.Close(); err != nil {
			t.Errorf("remove generated project workspace: %v", err)
		}
	})

	t.Setenv("PATH", filepath.Dir(configuration.forj)+string(os.PathListSeparator)+os.Getenv("PATH"))
	projectEnvironment := projectprocess.CaptureEnvironment()
	projects := []projectIdentityAcceptanceProject{
		{id: "project-orders", intent: "intent-start-orders", project: workspace.Projects[0]},
		{id: "project-billing", intent: "intent-start-billing", project: workspace.Projects[1]},
		{id: "project-inventory", intent: "intent-start-inventory", project: workspace.Projects[2]},
	}
	store, journal := newProjectLifecycleIntegrationState(t)
	registerProjectIdentityAcceptanceProjects(t, store, projects)
	initializeProjectIdentityAcceptanceNetwork(t, store, configuration.prefix, configuration.addresses, projects)

	supervisor := projectprocess.New(projectprocess.Options{
		GracePeriod:          3 * time.Second,
		OutputBufferLines:    256,
		OutputSpoolDirectory: filepath.Join(t.TempDir(), "project-output"),
		Environment:          projectEnvironment,
	})
	coordinator := NewProjectLifecycleCoordinator(store, journal, goforjruntime.New(supervisor), projectLifecycleTestRouteReconciler{})
	t.Cleanup(func() {
		closeContext, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer closeCancel()
		if err := coordinator.Close(closeContext); err != nil && closeContext.Err() == nil {
			t.Errorf("close generated project coordinator: %v", err)
		}
	})

	for index, project := range projects {
		operationID := domain.OperationID(fmt.Sprintf("operation-start-generated-%d", index+1))
		queued, err := coordinator.Start(ctx, ProjectStartRequest{
			ProjectID:   project.id,
			OperationID: operationID,
			IntentID:    project.intent,
		})
		if err != nil || queued.Operation.State != domain.OperationQueued {
			t.Fatalf("start generated project %q = %#v, %v", project.id, queued, err)
		}
	}

	ready := make([]state.ProjectRecord, 0, len(projects))
	for _, project := range projects {
		ready = append(ready, waitForProjectIdentityAcceptanceState(t, ctx, store, journal, supervisor, project.id, project.intent, domain.ProjectReady))
	}

	assertProjectIdentityAcceptanceEndpoints(t, ctx, store, projects, ready, configuration.addresses)

	for index := len(projects) - 1; index >= 0; index-- {
		project := projects[index]
		stopIntent := domain.IntentID(fmt.Sprintf("intent-stop-generated-%d", index+1))
		queued, err := coordinator.Stop(ctx, ProjectStopRequest{
			ProjectID:   project.id,
			OperationID: domain.OperationID(fmt.Sprintf("operation-stop-generated-%d", index+1)),
			IntentID:    stopIntent,
		})
		if err != nil || queued.Operation.State != domain.OperationQueued {
			t.Fatalf("stop generated project %q = %#v, %v", project.id, queued, err)
		}
		waitForProjectIdentityAcceptanceState(t, ctx, store, journal, supervisor, project.id, stopIntent, domain.ProjectStopped)
	}
}

// TestGeneratedMySQLFixturesRender proves the pinned GoForj generator accepts Harbor's Compose-capable fixture shape without starting Docker.
func TestGeneratedMySQLFixturesRender(t *testing.T) {
	forj := loadProjectIdentityAcceptanceForj(t)
	ctx, cancel := context.WithTimeout(t.Context(), projectIdentityAcceptanceTimeout)
	defer cancel()
	workspace, err := goforjproject.Render(ctx, goforjproject.Request{
		ForjExecutable: forj,
		GoForjVersion:  projectIdentityAcceptanceGoForjVersion,
		Projects: []goforjproject.Spec{
			{Name: "Harbor MySQL Orders", Module: "example.test/harbor/mysql-orders", Port: projectIdentityAcceptancePort, MySQL: true},
			{Name: "Harbor MySQL Billing", Module: "example.test/harbor/mysql-billing", Port: projectIdentityAcceptancePort, MySQL: true},
		},
	})
	if err != nil {
		t.Fatalf("render MySQL generated projects: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := workspace.Close(); closeErr != nil {
			t.Errorf("remove MySQL generated project workspace: %v", closeErr)
		}
	})
}

// projectIdentityAcceptanceConfiguration contains validated native test inputs supplied by the hosted worker.
type projectIdentityAcceptanceConfiguration struct {
	forj      string
	prefix    netip.Prefix
	addresses []netip.Addr
}

// loadProjectIdentityAcceptanceConfiguration rejects missing or ambiguous CI authority before rendering projects.
func loadProjectIdentityAcceptanceConfiguration(t *testing.T) projectIdentityAcceptanceConfiguration {
	t.Helper()
	forj := loadProjectIdentityAcceptanceForj(t)
	addresses := parseProjectIdentityAcceptanceAddresses(t, os.Getenv(projectIdentityAcceptanceAddressesEnvironment))
	if len(addresses) != 3 {
		t.Fatalf("%s contains %d addresses, want exactly three", projectIdentityAcceptanceAddressesEnvironment, len(addresses))
	}
	prefix := netip.PrefixFrom(addresses[0], 29).Masked()
	for _, address := range addresses {
		if !prefix.Contains(address) {
			t.Fatalf("proof address %s is outside shared /29 %s", address, prefix)
		}
		observation, err := loopback.New().Observe(t.Context(), address)
		if err != nil {
			t.Fatalf("observe pre-provisioned proof address %s: %v", address, err)
		}
		if observation.State != loopback.StateExact {
			t.Fatalf("proof address %s state = %q, want %q", address, observation.State, loopback.StateExact)
		}
	}
	return projectIdentityAcceptanceConfiguration{forj: forj, prefix: prefix, addresses: addresses}
}

// loadProjectIdentityAcceptanceForj validates the pinned direct CLI path shared by generated-project acceptance tests.
func loadProjectIdentityAcceptanceForj(t *testing.T) string {
	t.Helper()
	forj := strings.TrimSpace(os.Getenv(projectIdentityAcceptanceForjEnvironment))
	if forj == "" || !filepath.IsAbs(forj) || filepath.Clean(forj) != forj {
		t.Fatalf("%s must name an absolute clean forj binary", projectIdentityAcceptanceForjEnvironment)
	}
	wantExecutableName := "forj"
	if runtime.GOOS == "windows" {
		wantExecutableName += ".exe"
	}
	if filepath.Base(forj) != wantExecutableName {
		t.Fatalf("%s basename = %q, want %q for lifecycle PATH resolution", projectIdentityAcceptanceForjEnvironment, filepath.Base(forj), wantExecutableName)
	}
	return forj
}

// parseProjectIdentityAcceptanceAddresses requires a canonical ordered set of distinct IPv4 loopback identities.
func parseProjectIdentityAcceptanceAddresses(t *testing.T, raw string) []netip.Addr {
	t.Helper()
	parts := strings.Split(raw, ",")
	addresses := make([]netip.Addr, 0, len(parts))
	seen := make(map[netip.Addr]struct{}, len(parts))
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			t.Fatalf("%s contains an empty or padded address", projectIdentityAcceptanceAddressesEnvironment)
		}
		address, err := netip.ParseAddr(part)
		if err != nil || !address.Is4() || !address.IsLoopback() || address != address.Unmap() {
			t.Fatalf("%s address %q is not canonical IPv4 loopback", projectIdentityAcceptanceAddressesEnvironment, part)
		}
		if _, duplicate := seen[address]; duplicate {
			t.Fatalf("%s contains duplicate address %s", projectIdentityAcceptanceAddressesEnvironment, address)
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	if !slices.IsSortedFunc(addresses, func(left netip.Addr, right netip.Addr) int {
		return left.Compare(right)
	}) {
		t.Fatalf("%s addresses must be in canonical order", projectIdentityAcceptanceAddressesEnvironment)
	}
	return addresses
}

// registerProjectIdentityAcceptanceProjects persists the same inert snapshots used by production registration.
func registerProjectIdentityAcceptanceProjects(
	t *testing.T,
	store *state.Store,
	projects []projectIdentityAcceptanceProject,
) {
	t.Helper()
	for _, generated := range projects {
		discovery, err := projectdiscovery.NewDiscoverer().Discover(t.Context(), generated.project.Root)
		if err != nil {
			t.Fatalf("discover generated project %q: %v", generated.id, err)
		}
		project, err := discovery.ProjectSnapshot(generated.id, time.Now().UTC().Round(0))
		if err != nil {
			t.Fatalf("build generated project snapshot %q: %v", generated.id, err)
		}
		if _, err := store.RegisterProject(t.Context(), project); err != nil {
			t.Fatalf("register generated project %q: %v", generated.id, err)
		}
	}
}

// initializeProjectIdentityAcceptanceNetwork records only identities independently provisioned by the platform worker.
func initializeProjectIdentityAcceptanceNetwork(
	t *testing.T,
	store *state.Store,
	prefix netip.Prefix,
	addresses []netip.Addr,
	projects []projectIdentityAcceptanceProject,
) {
	t.Helper()
	pool, err := identity.NewPool(prefix, addresses)
	if err != nil {
		t.Fatalf("create project identity acceptance pool: %v", err)
	}
	ownership, err := identity.NewOwnership("project-identity-acceptance", 1)
	if err != nil {
		t.Fatalf("create project identity acceptance ownership: %v", err)
	}
	at := time.Now().UTC().Round(0)
	for _, project := range projects {
		record, err := store.Project(t.Context(), project.id)
		if err != nil {
			t.Fatalf("read registered project %q: %v", project.id, err)
		}
		if at.Before(record.Project.UpdatedAt) {
			at = record.Project.UpdatedAt
		}
	}
	result, err := store.InitializeNetworkIdentity(t.Context(), state.InitializeNetworkIdentityRequest{
		ExpectedNetworkRevision: 0,
		Ownership:               ownership,
		Pool:                    pool,
		PoolGeneration:          1,
		Setup: []state.NetworkSetupProof{
			{
				Component:  state.NetworkSetupComponentMachineOwnership,
				Evidence:   "hosted worker isolated Harbor ownership",
				Generation: 1,
				VerifiedAt: at,
			},
			{
				Component:  state.NetworkSetupComponentLoopbackPool,
				Evidence:   "hosted worker explicit loopback identities",
				Generation: 1,
				VerifiedAt: at,
			},
		},
		At: at,
	})
	if err != nil {
		t.Fatalf("initialize project identity acceptance network: %v", err)
	}
	if result.Record.Stage != state.NetworkStageIdentity || len(result.Record.Leases) != 0 {
		t.Fatalf("initialized project identity acceptance network = %#v", result.Record)
	}
}

// waitForProjectIdentityAcceptanceState waits for durable lifecycle completion and surfaces terminal operation diagnostics immediately.
func waitForProjectIdentityAcceptanceState(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	journal *state.OperationJournal,
	supervisor *projectprocess.Supervisor,
	projectID domain.ProjectID,
	intentID domain.IntentID,
	want domain.ProjectState,
) state.ProjectRecord {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var sessionID domain.SessionID
	for {
		record, projectErr := store.Project(ctx, projectID)
		if session, sessionErr := store.ActiveProjectSession(ctx, projectID); sessionErr == nil {
			sessionID = session.ID
		}
		if projectErr == nil && record.Project.State == want {
			return record
		}
		operation, operationErr := journal.OperationByIntent(ctx, intentID)
		if operationErr == nil && (operation.Operation.State == domain.OperationFailed || operation.Operation.State == domain.OperationCancelled) {
			diagnostics := projectIdentityAcceptanceOutputDiagnostics(supervisor, projectID, sessionID)
			if operation.Operation.Problem != nil {
				t.Fatalf(
					"project %q reached %q while waiting for %q: operation %q phase %q problem %q: %s%s",
					projectID,
					operation.Operation.State,
					want,
					operation.Operation.ID,
					operation.Operation.Phase,
					operation.Operation.Problem.Code,
					operation.Operation.Problem.Message,
					diagnostics,
				)
			}
			t.Fatalf(
				"project %q reached %q while waiting for %q: operation %q phase %q without a problem%s",
				projectID,
				operation.Operation.State,
				want,
				operation.Operation.ID,
				operation.Operation.Phase,
				diagnostics,
			)
		}
		select {
		case <-ctx.Done():
			t.Fatalf(
				"wait for project %q state %q: %v (last project error: %v, last operation error: %v)%s",
				projectID,
				want,
				ctx.Err(),
				projectErr,
				operationErr,
				projectIdentityAcceptanceOutputDiagnostics(supervisor, projectID, sessionID),
			)
		case <-ticker.C:
		}
	}
}

// projectIdentityAcceptanceOutputDiagnostics retains bounded historical child stdout and stderr when a lifecycle assertion fails.
func projectIdentityAcceptanceOutputDiagnostics(
	supervisor *projectprocess.Supervisor,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) string {
	if sessionID == "" {
		return ""
	}
	output, err := supervisor.ReadOutputHistory(projectID, sessionID, 0)
	if err != nil {
		return fmt.Sprintf(" (read child stdout/stderr history: %v)", err)
	}
	if output.Text == "" {
		return ""
	}
	omitted := ""
	if output.Truncated || output.HasMore {
		omitted = "\n[additional child output omitted]"
	}
	return fmt.Sprintf("\nchild stdout/stderr (bounded historical output):\n%s%s", output.Text, omitted)
}

// assertProjectIdentityAcceptanceEndpoints proves each durable resource and live readiness endpoint uses its exact lease.
func assertProjectIdentityAcceptanceEndpoints(
	t *testing.T,
	ctx context.Context,
	store *state.Store,
	projects []projectIdentityAcceptanceProject,
	ready []state.ProjectRecord,
	wantAddresses []netip.Addr,
) {
	t.Helper()
	network, initialized, err := store.Network(ctx)
	if err != nil || !initialized {
		t.Fatalf("read allocated project identity network = %#v, %t, %v", network, initialized, err)
	}
	if len(network.Leases) != len(projects) {
		t.Fatalf("network leases = %#v, want %d", network.Leases, len(projects))
	}
	leaseByProject := make(map[domain.ProjectID]netip.Addr, len(network.Leases))
	for _, lease := range network.Leases {
		leaseByProject[lease.Key.ProjectID] = lease.Address
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	t.Cleanup(client.CloseIdleConnections)
	observedAddresses := make([]netip.Addr, 0, len(projects))
	for index, project := range projects {
		address := leaseByProject[project.id]
		if !address.IsValid() {
			t.Fatalf("project %q has no durable primary lease", project.id)
		}
		observedAddresses = append(observedAddresses, address)
		record := ready[index]
		appResources := make([]domain.ResourceSnapshot, 0, 1)
		for _, candidate := range record.Project.Resources {
			if candidate.Name == "App" && candidate.Kind == "application" {
				appResources = append(appResources, candidate)
			}
		}
		if len(appResources) != 1 {
			t.Fatalf("project %q App application resources = %#v, want exactly one", project.id, appResources)
		}
		resource := appResources[0]
		parsed, err := url.Parse(resource.URL)
		if err != nil {
			t.Fatalf("parse project %q resource URL %q: %v", project.id, resource.URL, err)
		}
		if parsed.Scheme != "http" || parsed.Host != fmt.Sprintf("%s:%d", address, projectIdentityAcceptancePort) || parsed.Path != "" {
			t.Fatalf("project %q resource URL = %q, want http://%s:%d", project.id, resource.URL, address, projectIdentityAcceptancePort)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, resource.URL+"/-/ready", nil)
		if err != nil {
			t.Fatalf("build project %q readiness request: %v", project.id, err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("request project %q readiness: %v", project.id, err)
		}
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Fatalf("close project %q readiness response: %v", project.id, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("project %q readiness status = %d, want 200", project.id, response.StatusCode)
		}
	}
	slices.SortFunc(observedAddresses, func(left netip.Addr, right netip.Addr) int {
		return left.Compare(right)
	})
	if !slices.Equal(observedAddresses, wantAddresses) {
		t.Fatalf("allocated project addresses = %v, want %v", observedAddresses, wantAddresses)
	}
}
