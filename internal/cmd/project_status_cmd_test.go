package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// newProjectStatusCommandFixture creates a snapshot-backed status command with captured output.
func newProjectStatusCommandFixture(connection *fakeDaemonControlClient) (*ProjectStatusCmd, *bytes.Buffer) {
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewProjectStatusCmd(client)
	command.ProjectID = "project-orders"
	output := &bytes.Buffer{}
	command.output = output
	return command, output
}

// TestProjectStatusCommandSelectsOneAuthoritativeProject verifies the CLI never manufactures project state outside the daemon snapshot.
func TestProjectStatusCommandSelectsOneAuthoritativeProject(t *testing.T) {
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{addTestRegistration(t.TempDir(), true).Project}
	connection := &fakeDaemonControlClient{snapshot: snapshot}
	command, output := newProjectStatusCommandFixture(connection)
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run status: %v", err)
	}
	for _, line := range []string{"Project: ", "State: stopped\n", "Apps: 0\n", "Services: 0\n", "Resources: 0\n"} {
		if !strings.Contains(output.String(), line) {
			t.Fatalf("output missing %q: %q", line, output.String())
		}
	}
	if connection.snapshotCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("calls = snapshot:%d close:%d", connection.snapshotCalls, connection.closeCalls)
	}
}

// TestProjectStatusCommandReturnsNotFoundAfterOneSnapshot verifies a stale CLI selection cannot be mistaken for an empty project.
func TestProjectStatusCommandReturnsNotFoundAfterOneSnapshot(t *testing.T) {
	connection := &fakeDaemonControlClient{snapshot: daemonTestSnapshot()}
	command, output := newProjectStatusCommandFixture(connection)
	err := command.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("not found error = %v", err)
	}
	if output.Len() != 0 || connection.snapshotCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("output = %q, calls = snapshot:%d close:%d", output.String(), connection.snapshotCalls, connection.closeCalls)
	}
}

// TestProjectStatusCommandRejectsInvalidProjectBeforeConnecting verifies malformed selectors cannot contact the daemon.
func TestProjectStatusCommandRejectsInvalidProjectBeforeConnecting(t *testing.T) {
	connectCalls := 0
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connectCalls++
		return &fakeDaemonControlClient{}, nil
	})
	command := NewProjectStatusCmd(client)
	command.ProjectID = " bad "
	if err := command.Run(t.Context()); err == nil {
		t.Fatal("invalid project error = nil")
	}
	if connectCalls != 0 {
		t.Fatalf("connect calls = %d, want 0", connectCalls)
	}
}

// TestProjectStatusCommandJSONWritesOnlyTheSelectedProject verifies scripts receive one typed project rather than a presentation wrapper.
func TestProjectStatusCommandJSONWritesOnlyTheSelectedProject(t *testing.T) {
	project := addTestRegistration(t.TempDir(), true).Project
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	command, output := newProjectStatusCommandFixture(&fakeDaemonControlClient{snapshot: snapshot})
	command.JSON = true
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run JSON status: %v", err)
	}
	if !strings.Contains(output.String(), `"id": "project-orders"`) || strings.Contains(output.String(), `"projects"`) {
		t.Fatalf("JSON output = %q", output.String())
	}
}
