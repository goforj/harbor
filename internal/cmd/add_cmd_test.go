package cmd

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// addTestRegistration returns one deterministic inert registration for CLI rendering.
func addTestRegistration(path string, created bool) control.ProjectRegistration {
	return control.ProjectRegistration{
		Project: domain.ProjectSnapshot{
			ID:        "project-orders",
			Name:      "Orders API",
			Path:      path,
			Slug:      "orders-api",
			State:     domain.ProjectStopped,
			UpdatedAt: time.Date(2026, time.July, 18, 20, 30, 0, 0, time.UTC),
			Apps:      []domain.AppSnapshot{},
			Services:  []domain.ServiceSnapshot{},
			Resources: []domain.ResourceSnapshot{},
		},
		Revision: 11,
		Created:  created,
	}
}

// newAddCommandFixture creates one command around an observable one-shot daemon connection.
func newAddCommandFixture(connection *fakeDaemonControlClient) (*AddCmd, *bytes.Buffer) {
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewAddCmd(client)
	output := &bytes.Buffer{}
	command.output = output
	return command, output
}

// TestAddCommandPrintsNewRegistration verifies the default output is honest about route activation.
func TestAddCommandPrintsNewRegistration(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "orders")
	registration := addTestRegistration(selected, true)
	connection := &fakeDaemonControlClient{registration: registration}
	command, output := newAddCommandFixture(connection)
	command.Path = selected

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run add: %v", err)
	}
	want := strings.Join([]string{
		"Added: Orders API",
		"Path: " + selected,
		"State: stopped",
		"Routing: not configured",
		"Revision: 11",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	wantRequest := control.RegisterProjectRequest{Path: filepath.Clean(selected)}
	if len(connection.registrationRequests) != 1 || connection.registrationRequests[0] != wantRequest {
		t.Fatalf("registration requests = %#v, want %#v", connection.registrationRequests, wantRequest)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", connection.closeCalls)
	}
}

// TestAddCommandExplainsIdempotentReplay verifies retries are not presented as another created project.
func TestAddCommandExplainsIdempotentReplay(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "orders")
	connection := &fakeDaemonControlClient{registration: addTestRegistration(selected, false)}
	command, output := newAddCommandFixture(connection)
	command.Path = selected

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run replayed add: %v", err)
	}
	if !strings.HasPrefix(output.String(), "Already registered: Orders API\n") {
		t.Fatalf("output = %q, want replay label", output.String())
	}
	if strings.Contains(output.String(), "Routing:") {
		t.Fatalf("output = %q, replay must not describe current routing state", output.String())
	}
}

// TestAddCommandPrintsTypedJSON verifies automation receives the control object without a CLI wrapper.
func TestAddCommandPrintsTypedJSON(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "orders")
	connection := &fakeDaemonControlClient{registration: addTestRegistration(selected, true)}
	command, output := newAddCommandFixture(connection)
	command.Path = selected
	command.JSON = true

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run JSON add: %v", err)
	}
	for _, expected := range []string{`"id": "project-orders"`, `"created": true`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("JSON output missing %s:\n%s", expected, output.String())
		}
	}
}

// TestAddCommandReturnsDaemonAndOutputFailures verifies no local fallback mutates project state.
func TestAddCommandReturnsDaemonAndOutputFailures(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "orders")
	requestErr := errors.New("daemon rejected project")
	connection := &fakeDaemonControlClient{registrationErr: requestErr}
	command, output := newAddCommandFixture(connection)
	command.Path = selected
	if err := command.Run(t.Context()); !errors.Is(err, requestErr) {
		t.Fatalf("daemon error = %v, want %v", err, requestErr)
	}
	if output.Len() != 0 || connection.closeCalls != 1 {
		t.Fatalf("failure output = %q, close calls %d", output.String(), connection.closeCalls)
	}

	writeErr := errors.New("output failed")
	connection = &fakeDaemonControlClient{registration: addTestRegistration(selected, true)}
	command, _ = newAddCommandFixture(connection)
	command.Path = selected
	command.output = failingDaemonWriter{err: writeErr}
	if err := command.Run(t.Context()); !errors.Is(err, writeErr) {
		t.Fatalf("output error = %v, want %v", err, writeErr)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("output failure close calls = %d, want 1", connection.closeCalls)
	}
}

// TestAddCommandKongSurfaceAcceptsDefaultAndExplicitPaths verifies the documented top-level CLI shape.
func TestAddCommandKongSurfaceAcceptsDefaultAndExplicitPaths(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "orders")
	for _, args := range [][]string{{"add"}, {"add", selected}, {"add", selected, "--json"}} {
		connection := &fakeDaemonControlClient{registration: addTestRegistration(selected, true)}
		command, _ := newAddCommandFixture(connection)
		root := struct {
			Add AddCmd `cmd:""`
		}{Add: *command}
		parser, err := kong.New(&root)
		if err != nil {
			t.Fatalf("create parser: %v", err)
		}
		parsed, err := parser.Parse(args)
		if err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		parsed.BindTo(t.Context(), (*context.Context)(nil))
		if err := parsed.Run(); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		if connection.registrationCalls != 1 {
			t.Fatalf("registration calls = %d, want 1", connection.registrationCalls)
		}
	}
}
