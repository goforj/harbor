package state

import (
	"errors"
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
