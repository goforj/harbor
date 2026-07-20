package projectprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
)

// fakeContainerRuntime supplies deterministic host runtime observations.
type fakeContainerRuntime struct {
	observation containerruntime.ProjectObservation
	err         error
	checkout    string
	closed      bool
}

// ObserveProject records the exact supervised checkout and returns configured services.
func (runtime *fakeContainerRuntime) ObserveProject(
	_ context.Context,
	checkout string,
) (containerruntime.ProjectObservation, error) {
	runtime.checkout = checkout
	return runtime.observation, runtime.err
}

// OpenServiceLogs returns no sources because observation tests do not request logs.
func (*fakeContainerRuntime) OpenServiceLogs(context.Context, string, string, int) (containerruntime.LogFollower, error) {
	return nil, errors.New("unexpected service log open")
}

// Close records runtime transport cleanup.
func (runtime *fakeContainerRuntime) Close() error {
	runtime.closed = true
	return nil
}

// TestProjectRuntimeServicesProjectsOnlyActiveLogicalServices verifies container identities cannot enter durable state.
func TestProjectRuntimeServicesProjectsOnlyActiveLogicalServices(t *testing.T) {
	observation, err := projectRuntimeServices(containerruntime.ProjectObservation{Services: []containerruntime.Service{
		{ID: "redis", Name: "redis", State: "working", Active: true, Containers: []containerruntime.Container{{ID: "ephemeral-redis"}}},
		{ID: "mysql", Name: "mysql", State: "ready", Active: true, Containers: []containerruntime.Container{{ID: "ephemeral-mysql"}}},
		{ID: "old", Name: "old", State: "stopped", Active: false, Containers: []containerruntime.Container{{ID: "ephemeral-old"}}},
	}})
	if err != nil {
		t.Fatalf("projectRuntimeServices() error = %v", err)
	}
	if !observation.Supported || len(observation.Services) != 2 || observation.Services[0].ID != "mysql" || observation.Services[1].ID != "redis" {
		t.Fatalf("projectRuntimeServices() = %#v", observation)
	}
	if observation.Services[0] != (domain.ServiceSnapshot{
		ID: "mysql", Name: "mysql", Kind: "compose", State: domain.EntityReady,
		Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
	}) {
		t.Fatalf("first service = %#v", observation.Services[0])
	}
}

// TestProjectRuntimeServicesRejectsInvalidOrDuplicateServices covers the direct runtime trust boundary.
func TestProjectRuntimeServicesRejectsInvalidOrDuplicateServices(t *testing.T) {
	for _, test := range []struct {
		name        string
		observation containerruntime.ProjectObservation
	}{
		{name: "nil", observation: containerruntime.ProjectObservation{}},
		{name: "invalid", observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{{ID: " bad", Name: "bad", State: "ready", Active: true}}}},
		{name: "active stopped", observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{{ID: "db", Name: "db", State: "stopped", Active: true}}}},
		{name: "duplicate", observation: containerruntime.ProjectObservation{Services: []containerruntime.Service{{ID: "db", Name: "db", State: "ready", Active: true}, {ID: "db", Name: "db", State: "ready", Active: true}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := projectRuntimeServices(test.observation); err == nil {
				t.Fatal("projectRuntimeServices() error = nil")
			}
		})
	}
}
