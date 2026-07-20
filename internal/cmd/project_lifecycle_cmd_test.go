package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// projectLifecycleTestOperation creates valid authoritative progress for one requested lifecycle action.
func projectLifecycleTestOperation(
	t *testing.T,
	kind domain.OperationKind,
	state domain.OperationState,
) control.ProjectLifecycleOperation {
	t.Helper()
	action := projectLifecycleAction(kind)
	requestedAt := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation(
		domain.OperationID("operation-"+action),
		domain.IntentID("intent-"+action),
		kind,
		"project-orders",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("create %s operation: %v", action, err)
	}
	if state != domain.OperationQueued {
		operation, err = operation.Transition(domain.OperationRunning, action+"ing project", requestedAt.Add(time.Second), nil)
		if err != nil {
			t.Fatalf("start %s operation: %v", action, err)
		}
	}
	if state != domain.OperationQueued && state != domain.OperationRunning {
		var problem *domain.Problem
		if state == domain.OperationFailed {
			problem = &domain.Problem{Code: "project_lifecycle_failed", Message: "Harbor could not complete the project action", Retryable: true}
		}
		operation, err = operation.Transition(state, action+" operation finished", requestedAt.Add(2*time.Second), problem)
		if err != nil {
			t.Fatalf("finish %s operation: %v", action, err)
		}
	}
	return control.ProjectLifecycleOperation{Operation: operation, Revision: 44}
}

// newStartCommandFixture creates a start command with deterministic intent generation and captured output.
func newStartCommandFixture(t *testing.T, connection *fakeDaemonControlClient) (*StartCmd, *bytes.Buffer) {
	t.Helper()
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewStartCmd(client)
	command.ProjectID = "project-orders"
	command.newIntentID = func(domain.OperationKind) (domain.IntentID, error) { return "intent-start", nil }
	output := &bytes.Buffer{}
	command.output = output
	return command, output
}

// newStopCommandFixture creates a stop command with deterministic intent generation and captured output.
func newStopCommandFixture(t *testing.T, connection *fakeDaemonControlClient) (*StopCmd, *bytes.Buffer) {
	t.Helper()
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewStopCmd(client)
	command.ProjectID = "project-orders"
	command.newIntentID = func(domain.OperationKind) (domain.IntentID, error) { return "intent-stop", nil }
	output := &bytes.Buffer{}
	command.output = output
	return command, output
}

// TestProjectLifecycleCommandsForwardStableActionIntents verifies Start and Stop share the daemon contract without sharing an action identity.
func TestProjectLifecycleCommandsForwardStableActionIntents(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    domain.OperationKind
		command func(*fakeDaemonControlClient) (error, *bytes.Buffer)
		assert  func(*testing.T, *fakeDaemonControlClient)
		want    string
	}{
		{
			name: "start", kind: domain.OperationKindProjectStart,
			command: func(connection *fakeDaemonControlClient) (error, *bytes.Buffer) {
				command, output := newStartCommandFixture(t, connection)
				return command.Run(t.Context()), output
			},
			assert: func(t *testing.T, connection *fakeDaemonControlClient) {
				t.Helper()
				want := []control.StartProjectRequest{{ProjectID: "project-orders", IntentID: "intent-start"}}
				if connection.startLifecycleCalls != 1 || !reflect.DeepEqual(connection.startLifecycleRequests, want) || connection.stopLifecycleCalls != 0 {
					t.Fatalf("start requests = %#v, calls = start:%d stop:%d", connection.startLifecycleRequests, connection.startLifecycleCalls, connection.stopLifecycleCalls)
				}
			}, want: "Started: project-orders\n",
		},
		{
			name: "stop", kind: domain.OperationKindProjectStop,
			command: func(connection *fakeDaemonControlClient) (error, *bytes.Buffer) {
				command, output := newStopCommandFixture(t, connection)
				return command.Run(t.Context()), output
			},
			assert: func(t *testing.T, connection *fakeDaemonControlClient) {
				t.Helper()
				want := []control.StopProjectRequest{{ProjectID: "project-orders", IntentID: "intent-stop"}}
				if connection.stopLifecycleCalls != 1 || !reflect.DeepEqual(connection.stopLifecycleRequests, want) || connection.startLifecycleCalls != 0 {
					t.Fatalf("stop requests = %#v, calls = start:%d stop:%d", connection.stopLifecycleRequests, connection.startLifecycleCalls, connection.stopLifecycleCalls)
				}
			}, want: "Stopped: project-orders\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := &fakeDaemonControlClient{}
			if test.kind == domain.OperationKindProjectStart {
				connection.startLifecycle = projectLifecycleTestOperation(t, test.kind, domain.OperationSucceeded)
			} else {
				connection.stopLifecycle = projectLifecycleTestOperation(t, test.kind, domain.OperationSucceeded)
			}
			err, output := test.command(connection)
			if err != nil {
				t.Fatalf("run %s: %v", test.name, err)
			}
			test.assert(t, connection)
			if !strings.Contains(output.String(), test.want) || !strings.Contains(output.String(), "Revision: 44\n") {
				t.Fatalf("output = %q", output.String())
			}
			if connection.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", connection.closeCalls)
			}
		})
	}
}

// TestProjectLifecycleCommandsReportTerminalFailures verifies scripts receive nonzero status only after durable failure output is rendered.
func TestProjectLifecycleCommandsReportTerminalFailures(t *testing.T) {
	connection := &fakeDaemonControlClient{startLifecycle: projectLifecycleTestOperation(t, domain.OperationKindProjectStart, domain.OperationFailed)}
	command, output := newStartCommandFixture(t, connection)
	err := command.Run(t.Context())
	var terminal projectLifecycleTerminalError
	if !errors.As(err, &terminal) || terminal.state != domain.OperationFailed || terminal.kind != domain.OperationKindProjectStart {
		t.Fatalf("terminal error = %v", err)
	}
	if !strings.Contains(output.String(), "Starting failed: project-orders\n") || !strings.Contains(output.String(), "Problem: Harbor could not complete the project action\n") {
		t.Fatalf("output = %q", output.String())
	}
}

// TestProjectLifecycleCommandsReturnTerminalFailureAfterJSON verifies machine-readable output preserves shell failure semantics.
func TestProjectLifecycleCommandsReturnTerminalFailureAfterJSON(t *testing.T) {
	connection := &fakeDaemonControlClient{stopLifecycle: projectLifecycleTestOperation(t, domain.OperationKindProjectStop, domain.OperationCancelled)}
	command, output := newStopCommandFixture(t, connection)
	command.JSON = true
	err := command.Run(t.Context())
	var terminal projectLifecycleTerminalError
	if !errors.As(err, &terminal) || terminal.state != domain.OperationCancelled || terminal.kind != domain.OperationKindProjectStop {
		t.Fatalf("terminal error = %v", err)
	}
	if !strings.Contains(output.String(), `"state": "cancelled"`) || strings.Contains(output.String(), "Stopped:") {
		t.Fatalf("JSON output = %q", output.String())
	}
}

// TestProjectLifecycleCommandsPreserveRetryIntentOnUncertainCall verifies the generated identity remains usable after an indeterminate transport result.
func TestProjectLifecycleCommandsPreserveRetryIntentOnUncertainCall(t *testing.T) {
	callErr := errors.New("connection closed after request")
	connection := &fakeDaemonControlClient{stopLifecycleErr: callErr}
	command, output := newStopCommandFixture(t, connection)
	err := command.Run(t.Context())
	if !errors.Is(err, callErr) || !strings.Contains(err.Error(), "retry with --intent intent-stop") {
		t.Fatalf("call error = %v", err)
	}
	if output.Len() != 0 || connection.stopLifecycleCalls != 1 {
		t.Fatalf("output = %q, stop calls = %d", output.String(), connection.stopLifecycleCalls)
	}
}

// TestProjectLifecycleCommandsRejectInvalidInputBeforeConnecting verifies invalid identities do not contact the daemon or consume entropy.
func TestProjectLifecycleCommandsRejectInvalidInputBeforeConnecting(t *testing.T) {
	connectCalls := 0
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connectCalls++
		return &fakeDaemonControlClient{}, nil
	})
	command := NewStartCmd(client)
	command.ProjectID = " bad "
	factoryCalls := 0
	command.newIntentID = func(domain.OperationKind) (domain.IntentID, error) {
		factoryCalls++
		return "intent-start", nil
	}
	if err := command.Run(t.Context()); err == nil {
		t.Fatal("invalid project error = nil")
	}
	if connectCalls != 0 || factoryCalls != 0 {
		t.Fatalf("calls = connect:%d factory:%d, want zero", connectCalls, factoryCalls)
	}

	command = NewStartCmd(client)
	command.ProjectID = "project-orders"
	command.Intent = " bad "
	if err := command.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "validate intent override") {
		t.Fatalf("invalid intent error = %v", err)
	}
	if connectCalls != 0 {
		t.Fatalf("connect calls = %d, want 0", connectCalls)
	}
}

// TestRunProjectLifecycleRejectsUnsupportedKind verifies no future action can silently reuse Start or Stop transport semantics.
func TestRunProjectLifecycleRejectsUnsupportedKind(t *testing.T) {
	connectCalls := 0
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connectCalls++
		return &fakeDaemonControlClient{}, nil
	})
	err := runProjectLifecycle(t.Context(), projectLifecycleCommand{
		projectID: "project-orders",
		kind:      domain.OperationKindProjectUnregister,
		client:    client,
		output:    &bytes.Buffer{},
		newIntent: func(domain.OperationKind) (domain.IntentID, error) { return "intent-unsupported", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported project lifecycle kind") {
		t.Fatalf("unsupported lifecycle error = %v", err)
	}
	if connectCalls != 0 {
		t.Fatalf("connect calls = %d, want 0", connectCalls)
	}
}

// TestProjectLifecycleCommandsKongSurfaceAcceptsExplicitRetryIntent verifies both top-level user commands parse the documented automation shape.
func TestProjectLifecycleCommandsKongSurfaceAcceptsExplicitRetryIntent(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		root func(*fakeDaemonControlClient) any
	}{
		{
			name: "start", args: []string{"start", "project-orders", "--intent", "intent-start"},
			root: func(connection *fakeDaemonControlClient) any {
				connection.startLifecycle = projectLifecycleTestOperation(t, domain.OperationKindProjectStart, domain.OperationSucceeded)
				command, _ := newStartCommandFixture(t, connection)
				command.ProjectID = ""
				return &struct {
					Start StartCmd `cmd:""`
				}{Start: *command}
			},
		},
		{
			name: "stop", args: []string{"stop", "project-orders", "--json"},
			root: func(connection *fakeDaemonControlClient) any {
				connection.stopLifecycle = projectLifecycleTestOperation(t, domain.OperationKindProjectStop, domain.OperationSucceeded)
				command, _ := newStopCommandFixture(t, connection)
				command.ProjectID = ""
				return &struct {
					Stop StopCmd `cmd:""`
				}{Stop: *command}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := &fakeDaemonControlClient{}
			parser, err := kong.New(test.root(connection))
			if err != nil {
				t.Fatalf("create parser: %v", err)
			}
			parsed, err := parser.Parse(test.args)
			if err != nil {
				t.Fatalf("parse %v: %v", test.args, err)
			}
			parsed.BindTo(t.Context(), (*context.Context)(nil))
			if err := parsed.Run(); err != nil {
				t.Fatalf("run %v: %v", test.args, err)
			}
		})
	}
}

// TestNewProjectLifecycleIntentIDCreatesDistinctValidActionIdentities verifies independent CLI launches cannot collide by default.
func TestNewProjectLifecycleIntentIDCreatesDistinctValidActionIdentities(t *testing.T) {
	first, err := newProjectLifecycleIntentID(domain.OperationKindProjectStart)
	if err != nil {
		t.Fatalf("create first intent: %v", err)
	}
	second, err := newProjectLifecycleIntentID(domain.OperationKindProjectStop)
	if err != nil {
		t.Fatalf("create second intent: %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first intent invalid: %v", err)
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("second intent invalid: %v", err)
	}
	if first == second || !strings.HasPrefix(string(first), "intent-start-") || !strings.HasPrefix(string(second), "intent-stop-") {
		t.Fatalf("intents = %q and %q", first, second)
	}
}
