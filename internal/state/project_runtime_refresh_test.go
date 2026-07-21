package state

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestRefreshProjectServicesReplacesTopologyAndPrunesStaleResources proves a live Compose replacement cannot leave
// a resource owned by a service that the fresh runtime observation no longer admits.
func TestRefreshProjectServicesReplacesTopologyAndPrunesStaleResources(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project, session, _ := projectLifecycleTestReadyProject(t, store, "project-refresh")
	project.Resources = append(project.Resources, domain.ResourceSnapshot{
		ID: "redis-admin", Name: "Redis Admin", Kind: "cache", URL: "http://127.0.0.1:8081",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "redis"},
	})
	projectRecord, err := store.PutProject(t.Context(), project)
	if err != nil {
		t.Fatalf("PutProject() with a service-owned resource error = %v", err)
	}
	beforeRecord := projectRecord
	services := []domain.ServiceSnapshot{
		{ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityDegraded, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected},
	}
	refreshed, err := store.RefreshProjectServices(t.Context(), RefreshProjectServicesRequest{
		ProjectID:                 project.ID,
		ExpectedProjectRevision:   beforeRecord.Revision,
		SessionID:                 session.ID,
		ExpectedSessionGeneration: session.Generation,
		Services:                  services,
		At:                        project.UpdatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("RefreshProjectServices() error = %v", err)
	}
	if !reflect.DeepEqual(refreshed.Project.Services, services) {
		t.Fatalf("refreshed services = %#v, want %#v", refreshed.Project.Services, services)
	}
	if len(refreshed.Project.Resources) != 2 {
		t.Fatalf("refreshed resources = %#v, want App and mysql resources", refreshed.Project.Resources)
	}
	for _, resource := range refreshed.Project.Resources {
		if resource.Owner.Kind == domain.ResourceOwnedByService && resource.Owner.ServiceID == "redis" {
			t.Fatalf("stale redis resource survived refresh: %#v", resource)
		}
	}
	if refreshed.Revision <= beforeRecord.Revision {
		t.Fatalf("refreshed revision = %d, want a new project revision", refreshed.Revision)
	}

	stale := RefreshProjectServicesRequest{
		ProjectID:                 project.ID,
		ExpectedProjectRevision:   beforeRecord.Revision,
		SessionID:                 session.ID,
		ExpectedSessionGeneration: session.Generation,
		Services:                  services,
		At:                        project.UpdatedAt.Add(2 * time.Second),
	}
	var conflict *ProjectRevisionConflictError
	if _, err := store.RefreshProjectServices(t.Context(), stale); !errors.As(err, &conflict) {
		t.Fatalf("RefreshProjectServices(stale revision) error = %v, want project revision conflict", err)
	}
}

// TestRefreshProjectServicesRejectsStoppedService keeps inactive observations out of the ready projection boundary.
func TestRefreshProjectServicesRejectsStoppedService(t *testing.T) {
	request := RefreshProjectServicesRequest{
		ProjectID:                 "project-refresh",
		ExpectedProjectRevision:   1,
		SessionID:                 "session-refresh",
		ExpectedSessionGeneration: 1,
		Services: []domain.ServiceSnapshot{{
			ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityStopped,
			Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
		}},
		At: projectStoreMutationTestTime(),
	}
	if err := validateRefreshProjectServicesRequest(request); err == nil {
		t.Fatal("validateRefreshProjectServicesRequest() error = nil, want stopped-service rejection")
	}
}

// TestRefreshProjectRuntimeReplacesResourcesBehindSessionFence proves a fresh framework report cannot leave stale links.
func TestRefreshProjectRuntimeReplacesResourcesBehindSessionFence(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project, session, _ := projectLifecycleTestReadyProject(t, store, "project-runtime-refresh")
	project.Resources = []domain.ResourceSnapshot{{
		ID: "app-http", Name: "App", Kind: "application", URL: "http://127.0.0.1:3000",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
	}}
	projectRecord, err := store.PutProject(t.Context(), project)
	if err != nil {
		t.Fatalf("PutProject() error = %v", err)
	}
	services := []domain.ServiceSnapshot{{
		ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityReady,
		Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
	}}
	resources := []domain.ResourceSnapshot{
		{ID: "app-http", Name: "App", Kind: "application", URL: "http://127.0.0.1:3000", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}},
		{ID: "mysql-admin", Name: "MySQL Admin", Kind: "admin", URL: "http://127.0.0.1:8080", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "mysql"}},
		{ID: "swagger", Name: "Swagger", Kind: "docs", URL: "http://127.0.0.1:3000/swagger", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}},
	}
	refreshed, err := store.RefreshProjectRuntime(t.Context(), RefreshProjectRuntimeRequest{
		ProjectID: project.ID, ExpectedProjectRevision: projectRecord.Revision,
		SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
		PrimaryAddress: netip.MustParseAddr("127.0.0.1"), Services: services, Resources: resources,
		At: project.UpdatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("RefreshProjectRuntime() error = %v", err)
	}
	if !reflect.DeepEqual(refreshed.Project.Services, services) || !reflect.DeepEqual(refreshed.Project.Resources, resources) {
		t.Fatalf("refreshed runtime = services %#v resources %#v", refreshed.Project.Services, refreshed.Project.Resources)
	}
	if refreshed.Revision <= projectRecord.Revision {
		t.Fatalf("refreshed revision = %d, want a new project revision", refreshed.Revision)
	}
	stale := RefreshProjectRuntimeRequest{
		ProjectID: project.ID, ExpectedProjectRevision: projectRecord.Revision,
		SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
		PrimaryAddress: netip.MustParseAddr("127.0.0.1"), Services: services, Resources: resources,
		At: project.UpdatedAt.Add(2 * time.Second),
	}
	var conflict *ProjectRevisionConflictError
	if _, err := store.RefreshProjectRuntime(t.Context(), stale); !errors.As(err, &conflict) {
		t.Fatalf("RefreshProjectRuntime(stale revision) error = %v, want project revision conflict", err)
	}
	generationDrift := stale
	generationDrift.ExpectedProjectRevision = refreshed.Revision
	generationDrift.ExpectedSessionGeneration++
	var sessionDrift *StaleSessionGenerationError
	if _, err := store.RefreshProjectRuntime(t.Context(), generationDrift); !errors.As(err, &sessionDrift) {
		t.Fatalf("RefreshProjectRuntime(session drift) error = %v, want stale session generation", err)
	}
}

// TestValidateRefreshProjectRuntimeRejectsForeignResource verifies no non-loopback framework URL reaches durable state.
func TestValidateRefreshProjectRuntimeRejectsForeignResource(t *testing.T) {
	request := RefreshProjectRuntimeRequest{
		ProjectID: "project-runtime-refresh", ExpectedProjectRevision: 1,
		SessionID: "session-refresh", ExpectedSessionGeneration: 1,
		PrimaryAddress: netip.MustParseAddr("127.0.0.1"),
		Services:       []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{{
			ID: "app-http", Name: "App", Kind: "application", URL: "http://dev.diclan.app:3000",
			Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
		}},
		At: projectStoreMutationTestTime(),
	}
	if err := validateRefreshProjectRuntimeRequest(request); err == nil {
		t.Fatal("validateRefreshProjectRuntimeRequest() error = nil, want foreign-resource rejection")
	}
	request.Resources = []domain.ResourceSnapshot{}
	if err := validateRefreshProjectRuntimeRequest(request); err == nil {
		t.Fatal("validateRefreshProjectRuntimeRequest() error = nil, want missing app-http rejection")
	}
	request.Resources = []domain.ResourceSnapshot{{
		ID: "app-http", Name: "App", Kind: "application", URL: "http://127.0.0.1:3000?unsafe=1",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
	}}
	if err := validateRefreshProjectRuntimeRequest(request); err == nil {
		t.Fatal("validateRefreshProjectRuntimeRequest() error = nil, want query-bearing URL rejection")
	}
	request.Resources = []domain.ResourceSnapshot{{
		ID: "app-http", Name: "App", Kind: "application", URL: "http://127.0.0.1:3000",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "mysql"},
	}}
	if err := validateRefreshProjectRuntimeRequest(request); err == nil {
		t.Fatal("validateRefreshProjectRuntimeRequest() error = nil, want non-App app-http rejection")
	}
}

// TestValidateRefreshProjectRuntimeAcceptsOnlyValidPrimaryAddress keeps a late framework report on a private identity.
func TestValidateRefreshProjectRuntimeAcceptsOnlyValidPrimaryAddress(t *testing.T) {
	request := RefreshProjectRuntimeRequest{
		ProjectID: "project-runtime-refresh", ExpectedProjectRevision: 1,
		SessionID: "session-refresh", ExpectedSessionGeneration: 1,
		PrimaryAddress: netip.MustParseAddr("192.0.2.10"),
		Services:       []domain.ServiceSnapshot{}, Resources: []domain.ResourceSnapshot{},
		At: projectStoreMutationTestTime(),
	}
	if err := validateRefreshProjectRuntimeRequest(request); err == nil {
		t.Fatal("validateRefreshProjectRuntimeRequest() error = nil, want non-loopback primary rejection")
	}
	request.PrimaryAddress = netip.MustParseAddr("::ffff:127.0.0.1")
	if err := validateRefreshProjectRuntimeRequest(request); err == nil {
		t.Fatal("validateRefreshProjectRuntimeRequest() error = nil, want mapped IPv4-in-IPv6 primary rejection")
	}
}
