package projectprocess

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/goforj/harbor/internal/containerruntime"
	"github.com/goforj/harbor/internal/domain"
)

// fakeContainerRuntime supplies deterministic host runtime observations.
type fakeContainerRuntime struct {
	observation containerruntime.ProjectObservation
	err         error
	checkout    string
	changeErr   error
	changeRoot  string
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

// WaitProjectChange records the canonical checkout selected by the supervisor's optional event boundary.
func (runtime *fakeContainerRuntime) WaitProjectChange(_ context.Context, checkout string) error {
	runtime.changeRoot = checkout
	return runtime.changeErr
}

// TestWaitServiceChangeUsesExactSupervisedCheckout keeps the optional event stream behind process identity fencing.
func TestWaitServiceChangeUsesExactSupervisedCheckout(t *testing.T) {
	projectID := domain.ProjectID("project-events")
	sessionID := domain.SessionID("session-events")
	runtime := &fakeContainerRuntime{}
	command := exec.Command("/opt/forj", "dev")
	command.Dir = "/work/checkouts/events"
	process := &managedProcess{command: command, acceptingStop: true}
	supervisor := &Supervisor{
		projects:         map[domain.ProjectID]*managedProcess{projectID: process},
		sessions:         map[domain.SessionID]*managedProcess{sessionID: process},
		containerRuntime: runtime,
	}

	if err := supervisor.WaitServiceChange(t.Context(), projectID, sessionID); err != nil {
		t.Fatalf("WaitServiceChange() error = %v", err)
	}
	if runtime.changeRoot != command.Dir {
		t.Fatalf("WaitProjectChange() checkout = %q, want %q", runtime.changeRoot, command.Dir)
	}
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

// TestProjectRuntimeServicePortsRetainsEveryObservedPort keeps non-HTTP Docker publications out of the browser-resource contract.
func TestProjectRuntimeServicePortsRetainsEveryObservedPort(t *testing.T) {
	observation, err := projectRuntimeServicePorts(containerruntime.ProjectObservation{Services: []containerruntime.Service{{
		ID: "mysql", Active: true, Containers: []containerruntime.Container{
			{Replica: 2, Ports: []containerruntime.Port{{Private: 3306, Public: 3306, Address: "127.0.0.1", Protocol: "tcp"}}},
			{Replica: 1, Ports: []containerruntime.Port{{Private: 33060, Protocol: "tcp"}}},
		},
	}}}, "mysql")
	if err != nil {
		t.Fatalf("projectRuntimeServicePorts() error = %v", err)
	}
	if !observation.Supported || !observation.Available || len(observation.Ports) != 2 {
		t.Fatalf("projectRuntimeServicePorts() = %#v", observation)
	}
	if got := observation.Ports[0]; got.Private != 3306 || got.Public != 3306 || got.Protocol != "tcp" || got.Replica != 2 {
		t.Fatalf("first port = %#v", got)
	}
	if got := observation.Ports[1]; got.Private != 33060 || got.Public != 0 || got.Replica != 1 {
		t.Fatalf("second port = %#v", got)
	}
}
