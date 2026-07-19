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
	"github.com/goforj/harbor/internal/rpc"
)

// removeTestUnregistration returns one valid durable removal operation in the requested state.
func removeTestUnregistration(t *testing.T, operationState domain.OperationState) control.ProjectUnregistration {
	t.Helper()
	requestedAt := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation(
		"operation-remove",
		"intent-remove",
		domain.OperationKindProjectUnregister,
		"project-orders",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("create queued operation: %v", err)
	}
	if operationState == domain.OperationQueued {
		return control.ProjectUnregistration{Operation: operation, Revision: 43}
	}
	if operationState == domain.OperationCancelled {
		operation, err = operation.Transition(operationState, "project removal cancelled", requestedAt.Add(time.Second), nil)
		if err != nil {
			t.Fatalf("cancel operation: %v", err)
		}
		return control.ProjectUnregistration{Operation: operation, Revision: 43}
	}
	operation, err = operation.Transition(
		domain.OperationRunning,
		"releasing project network",
		requestedAt.Add(time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("start operation: %v", err)
	}
	if operationState == domain.OperationRunning {
		return control.ProjectUnregistration{Operation: operation, Revision: 43}
	}

	phase := "project unregistered"
	var problem *domain.Problem
	if operationState == domain.OperationRequiresApproval {
		phase = "awaiting host network release approval"
	}
	if operationState == domain.OperationFailed {
		phase = "project network release failed"
		problem = &domain.Problem{
			Code:      "network_release_failed",
			Message:   "Harbor could not verify the project network release",
			Retryable: true,
		}
	}
	operation, err = operation.Transition(operationState, phase, requestedAt.Add(2*time.Second), problem)
	if err != nil {
		t.Fatalf("finish operation as %s: %v", operationState, err)
	}
	return control.ProjectUnregistration{Operation: operation, Revision: 43}
}

// newRemoveCommandFixture creates one command with deterministic intent and observable output.
func newRemoveCommandFixture(t *testing.T, connection *fakeDaemonControlClient) (*RemoveCmd, *bytes.Buffer) {
	t.Helper()
	return newRemoveCommandFixtureWithDirectory(t, connection, t.TempDir())
}

// newRemoveCommandFixtureWithDirectory creates an independent command process view over one shared intent journal.
func newRemoveCommandFixtureWithDirectory(
	t *testing.T,
	connection *fakeDaemonControlClient,
	dataDirectory string,
) (*RemoveCmd, *bytes.Buffer) {
	t.Helper()
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewRemoveCmd(client)
	command.ProjectID = "project-orders"
	command.newIntentID = func() (domain.IntentID, error) { return "intent-remove", nil }
	command.intents = newFilesystemProjectRemovalIntentJournalWithDirectory(func() (string, error) {
		return dataDirectory, nil
	})
	output := &bytes.Buffer{}
	command.output = output
	return command, output
}

// TestRemoveCommandPrintsCompletedClaimlessRemoval verifies immediate daemon completion is the only state labeled removed.
func TestRemoveCommandPrintsCompletedClaimlessRemoval(t *testing.T) {
	unregistration := removeTestUnregistration(t, domain.OperationSucceeded)
	connection := &fakeDaemonControlClient{unregistration: unregistration}
	dataDirectory := t.TempDir()
	command, output := newRemoveCommandFixtureWithDirectory(t, connection, dataDirectory)

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run remove: %v", err)
	}
	want := strings.Join([]string{
		"Removed: project-orders",
		"State: succeeded",
		"Phase: project unregistered",
		"Operation: operation-remove",
		"Intent: intent-remove",
		"Revision: 43",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	wantRequest := control.UnregisterProjectRequest{ProjectID: "project-orders", IntentID: "intent-remove"}
	if !reflect.DeepEqual(connection.unregistrationRequests, []control.UnregisterProjectRequest{wantRequest}) {
		t.Fatalf("requests = %#v, want %#v", connection.unregistrationRequests, []control.UnregisterProjectRequest{wantRequest})
	}
	if connection.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", connection.closeCalls)
	}
	journal := newFilesystemProjectRemovalIntentJournalWithDirectory(func() (string, error) { return dataDirectory, nil })
	intentID, err := journal.LoadOrCreate(t.Context(), "project-orders", "", func() (domain.IntentID, error) {
		return "intent-next", nil
	})
	if err != nil || intentID != "intent-next" {
		t.Fatalf("intent after terminal success = %q, %v, want fresh intent-next", intentID, err)
	}
}

// TestRemoveCommandReportsApprovalWithoutClaimingCompletion verifies host cleanup remains visibly pending until an interactive launcher exists.
func TestRemoveCommandReportsApprovalWithoutClaimingCompletion(t *testing.T) {
	connection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationRequiresApproval)}
	dataDirectory := t.TempDir()
	command, output := newRemoveCommandFixtureWithDirectory(t, connection, dataDirectory)

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run approval-bound remove: %v", err)
	}
	want := strings.Join([]string{
		"Removal requires approval: project-orders",
		"State: requires_approval",
		"Phase: awaiting host network release approval",
		"Operation: operation-remove",
		"Intent: intent-remove",
		"Revision: 43",
		"Approval: interactive approval is not available in this CLI yet",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if strings.Contains(output.String(), "Removed:") {
		t.Fatalf("approval-bound output claimed completion: %q", output.String())
	}
	journal := newFilesystemProjectRemovalIntentJournalWithDirectory(func() (string, error) { return dataDirectory, nil })
	intentID, err := journal.LoadOrCreate(t.Context(), "project-orders", "", func() (domain.IntentID, error) {
		return "intent-unexpected", nil
	})
	if err != nil || intentID != "intent-remove" {
		t.Fatalf("approval intent = %q, %v, want retained intent-remove", intentID, err)
	}
}

// TestRemoveCommandRendersEveryNonSuccessState honestly distinguishes accepted, failed, and cancelled operations.
func TestRemoveCommandRendersEveryNonSuccessState(t *testing.T) {
	for _, test := range []struct {
		state     domain.OperationState
		want      []string
		wantError bool
	}{
		{state: domain.OperationQueued, want: []string{"Removal started: project-orders", "State: queued"}},
		{state: domain.OperationRunning, want: []string{"Removal started: project-orders", "State: running"}},
		{state: domain.OperationFailed, want: []string{
			"Removal failed: project-orders",
			"Problem: Harbor could not verify the project network release",
			"Retryable: true",
		}, wantError: true},
		{state: domain.OperationCancelled, want: []string{"Removal cancelled: project-orders", "State: cancelled"}, wantError: true},
	} {
		t.Run(string(test.state), func(t *testing.T) {
			connection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, test.state)}
			dataDirectory := t.TempDir()
			command, output := newRemoveCommandFixtureWithDirectory(t, connection, dataDirectory)
			err := command.Run(t.Context())
			if test.wantError {
				var terminal projectRemovalTerminalError
				if !errors.As(err, &terminal) || terminal.state != test.state {
					t.Fatalf("terminal error = %v, want state %s", err, test.state)
				}
			} else if err != nil {
				t.Fatalf("accepted removal error = %v", err)
			}
			for _, expected := range test.want {
				if !strings.Contains(output.String(), expected+"\n") {
					t.Fatalf("output missing %q:\n%s", expected, output.String())
				}
			}
			if strings.Contains(output.String(), "Removed:") {
				t.Fatalf("non-success output claimed completion: %q", output.String())
			}
			journal := newFilesystemProjectRemovalIntentJournalWithDirectory(func() (string, error) { return dataDirectory, nil })
			intentID, journalErr := journal.LoadOrCreate(t.Context(), "project-orders", "", func() (domain.IntentID, error) {
				return "intent-next", nil
			})
			wantIntent := domain.IntentID("intent-remove")
			if test.wantError {
				wantIntent = "intent-next"
			}
			if journalErr != nil || intentID != wantIntent {
				t.Fatalf("intent after %s = %q, %v, want %q", test.state, intentID, journalErr, wantIntent)
			}
		})
	}
}

// TestRemoveCommandPrintsTypedJSON verifies automation receives the control object without a CLI wrapper.
func TestRemoveCommandPrintsTypedJSON(t *testing.T) {
	connection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationRequiresApproval)}
	command, output := newRemoveCommandFixture(t, connection)
	command.JSON = true

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run JSON remove: %v", err)
	}
	for _, expected := range []string{
		`"id": "operation-remove"`,
		`"state": "requires_approval"`,
		`"revision": 43`,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("JSON output missing %s:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "Approval:") {
		t.Fatalf("JSON output included human presentation text:\n%s", output.String())
	}
}

// TestRemoveCommandReusesOneIntentAcrossWorkflowRetries verifies an indeterminate caller retry cannot create another operation.
func TestRemoveCommandReusesOneIntentAcrossWorkflowRetries(t *testing.T) {
	connection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationRunning)}
	command, _ := newRemoveCommandFixture(t, connection)
	factoryCalls := 0
	command.newIntentID = func() (domain.IntentID, error) {
		factoryCalls++
		return "intent-remove", nil
	}

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("retry remove: %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("intent factory calls = %d, want 1", factoryCalls)
	}
	if len(connection.unregistrationRequests) != 2 {
		t.Fatalf("request count = %d, want 2", len(connection.unregistrationRequests))
	}
	if connection.unregistrationRequests[0].IntentID != connection.unregistrationRequests[1].IntentID {
		t.Fatalf("retry intents = %q and %q, want stable identity", connection.unregistrationRequests[0].IntentID, connection.unregistrationRequests[1].IntentID)
	}
}

// TestRemoveCommandUsesExplicitRetryIntentWithoutGeneratingAnother verifies a new CLI process can resume the same daemon operation.
func TestRemoveCommandUsesExplicitRetryIntentWithoutGeneratingAnother(t *testing.T) {
	connection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationRunning)}
	dataDirectory := t.TempDir()
	command, _ := newRemoveCommandFixtureWithDirectory(t, connection, dataDirectory)
	command.Intent = "intent-remove"
	factoryCalls := 0
	command.newIntentID = func() (domain.IntentID, error) {
		factoryCalls++
		return "intent-unexpected", nil
	}

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("resume remove: %v", err)
	}
	restarted, _ := newRemoveCommandFixtureWithDirectory(t, connection, dataDirectory)
	restarted.newIntentID = func() (domain.IntentID, error) {
		factoryCalls++
		return "intent-unexpected", nil
	}
	if err := restarted.Run(t.Context()); err != nil {
		t.Fatalf("retry resumed removal from a new command: %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("intent factory calls = %d, want 0", factoryCalls)
	}
	if len(connection.unregistrationRequests) != 2 || connection.unregistrationRequests[0].IntentID != "intent-remove" || connection.unregistrationRequests[1].IntentID != "intent-remove" {
		t.Fatalf("requests = %#v, want explicit retry intent", connection.unregistrationRequests)
	}
}

// TestRemoveCommandPersistsGeneratedIntentBeforeAnUncertainCall verifies process loss cannot orphan a daemon commit identity.
func TestRemoveCommandPersistsGeneratedIntentBeforeAnUncertainCall(t *testing.T) {
	dataDirectory := t.TempDir()
	callErr := errors.New("connection closed after request")
	firstConnection := &fakeDaemonControlClient{unregistrationErr: callErr}
	first, _ := newRemoveCommandFixtureWithDirectory(t, firstConnection, dataDirectory)
	if err := first.Run(t.Context()); !errors.Is(err, callErr) || !strings.Contains(err.Error(), "--intent intent-remove") {
		t.Fatalf("uncertain call error = %v, want cause and retained intent guidance", err)
	}

	secondConnection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationRunning)}
	second, _ := newRemoveCommandFixtureWithDirectory(t, secondConnection, dataDirectory)
	factoryCalls := 0
	second.newIntentID = func() (domain.IntentID, error) {
		factoryCalls++
		return "intent-unexpected", nil
	}
	if err := second.Run(t.Context()); err != nil {
		t.Fatalf("resume uncertain removal: %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("restart intent factory calls = %d, want 0", factoryCalls)
	}
	if len(secondConnection.unregistrationRequests) != 1 || secondConnection.unregistrationRequests[0].IntentID != "intent-remove" {
		t.Fatalf("restart requests = %#v, want retained intent-remove", secondConnection.unregistrationRequests)
	}
}

// TestRemoveCommandPreservesEveryWireFailureWithRetryIntent verifies guidance does not erase machine-readable RPC identity.
func TestRemoveCommandPreservesEveryWireFailureWithRetryIntent(t *testing.T) {
	for _, code := range []rpc.ErrorCode{
		rpc.ErrorCodeNotFound,
		rpc.ErrorCodeConflict,
		rpc.ErrorCodeUnavailable,
		rpc.ErrorCodeInternal,
	} {
		t.Run(string(code), func(t *testing.T) {
			wireFailure := rpc.NewWireError(code)
			connection := &fakeDaemonControlClient{unregistrationErr: wireFailure}
			command, _ := newRemoveCommandFixture(t, connection)
			err := command.Run(t.Context())
			var preserved rpc.WireError
			if !errors.As(err, &preserved) || preserved.Code != code {
				t.Fatalf("wire failure = %v, want preserved code %s", err, code)
			}
			if !strings.Contains(err.Error(), "retry with --intent intent-remove") {
				t.Fatalf("wire failure guidance = %v, want retained intent", err)
			}
		})
	}
}

// TestRemoveCommandRetainsIntentUntilTerminalOutputCompletes closes the response-to-output crash window before allowing a fresh attempt.
func TestRemoveCommandRetainsIntentUntilTerminalOutputCompletes(t *testing.T) {
	dataDirectory := t.TempDir()
	writeErr := errors.New("output failed")
	firstConnection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationSucceeded)}
	first, _ := newRemoveCommandFixtureWithDirectory(t, firstConnection, dataDirectory)
	first.output = failingDaemonWriter{err: writeErr}
	if err := first.Run(t.Context()); !errors.Is(err, writeErr) {
		t.Fatalf("first terminal output error = %v, want %v", err, writeErr)
	}

	secondConnection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationSucceeded)}
	second, _ := newRemoveCommandFixtureWithDirectory(t, secondConnection, dataDirectory)
	second.newIntentID = func() (domain.IntentID, error) { return "intent-unexpected", nil }
	if err := second.Run(t.Context()); err != nil {
		t.Fatalf("replay terminal output: %v", err)
	}
	if len(secondConnection.unregistrationRequests) != 1 || secondConnection.unregistrationRequests[0].IntentID != "intent-remove" {
		t.Fatalf("terminal replay requests = %#v, want retained intent-remove", secondConnection.unregistrationRequests)
	}

	thirdConnection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationRunning)}
	third, _ := newRemoveCommandFixtureWithDirectory(t, thirdConnection, dataDirectory)
	third.newIntentID = func() (domain.IntentID, error) { return "intent-next", nil }
	if err := third.Run(t.Context()); err != nil {
		t.Fatalf("start fresh attempt after terminal output: %v", err)
	}
	if len(thirdConnection.unregistrationRequests) != 1 || thirdConnection.unregistrationRequests[0].IntentID != "intent-next" {
		t.Fatalf("fresh requests = %#v, want intent-next", thirdConnection.unregistrationRequests)
	}
}

// TestRemoveCommandRejectsInvalidInputsBeforeConnecting verifies malformed identities cannot consume daemon or entropy resources.
func TestRemoveCommandRejectsInvalidInputsBeforeConnecting(t *testing.T) {
	connectCalls := 0
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		connectCalls++
		return &fakeDaemonControlClient{}, nil
	})
	command := NewRemoveCmd(client)
	command.ProjectID = " bad "
	factoryCalls := 0
	command.newIntentID = func() (domain.IntentID, error) {
		factoryCalls++
		return "intent-remove", nil
	}

	if err := command.Run(t.Context()); err == nil {
		t.Fatal("invalid project error = nil")
	}
	if connectCalls != 0 || factoryCalls != 0 {
		t.Fatalf("calls = connect:%d factory:%d, want zero", connectCalls, factoryCalls)
	}

	command = NewRemoveCmd(client)
	command.ProjectID = "project-orders"
	command.Intent = " bad "
	if err := command.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "validate intent override") {
		t.Fatalf("invalid intent override error = %v", err)
	}
	if connectCalls != 0 || factoryCalls != 0 {
		t.Fatalf("override failure calls = connect:%d factory:%d, want zero", connectCalls, factoryCalls)
	}
}

// TestRemoveCommandReturnsIntentDaemonAndOutputFailures verifies every pre-completion failure remains visible.
func TestRemoveCommandReturnsIntentDaemonAndOutputFailures(t *testing.T) {
	intentErr := errors.New("entropy unavailable")
	connection := &fakeDaemonControlClient{}
	command, _ := newRemoveCommandFixture(t, connection)
	command.newIntentID = func() (domain.IntentID, error) { return "", intentErr }
	if err := command.Run(t.Context()); !errors.Is(err, intentErr) {
		t.Fatalf("intent error = %v, want %v", err, intentErr)
	}
	if connection.unregistrationCalls != 0 || connection.closeCalls != 0 {
		t.Fatalf("intent failure calls = unregister:%d close:%d, want zero", connection.unregistrationCalls, connection.closeCalls)
	}

	command, _ = newRemoveCommandFixture(t, connection)
	command.newIntentID = func() (domain.IntentID, error) { return " bad ", nil }
	if err := command.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "validate generated intent") {
		t.Fatalf("invalid generated intent error = %v", err)
	}
	if connection.unregistrationCalls != 0 || connection.closeCalls != 0 {
		t.Fatalf("invalid intent calls = unregister:%d close:%d, want zero", connection.unregistrationCalls, connection.closeCalls)
	}

	requestErr := errors.New("daemon rejected removal")
	connection = &fakeDaemonControlClient{unregistrationErr: requestErr}
	command, output := newRemoveCommandFixture(t, connection)
	err := command.Run(t.Context())
	if !errors.Is(err, requestErr) {
		t.Fatalf("daemon error = %v, want %v", err, requestErr)
	}
	if !strings.Contains(err.Error(), "retry with --intent intent-remove") {
		t.Fatalf("ambiguous retry guidance = %v", err)
	}
	if output.Len() != 0 || connection.closeCalls != 1 {
		t.Fatalf("daemon failure output = %q, close calls = %d", output.String(), connection.closeCalls)
	}

	definitiveErr := rpc.NewWireError(rpc.ErrorCodeNotFound)
	connection = &fakeDaemonControlClient{unregistrationErr: definitiveErr}
	command, _ = newRemoveCommandFixture(t, connection)
	err = command.Run(t.Context())
	var wireErr rpc.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != rpc.ErrorCodeNotFound || !strings.Contains(err.Error(), "retry with --intent intent-remove") {
		t.Fatalf("wire daemon error = %v, want preserved code and retry intent", err)
	}

	writeErr := errors.New("output failed")
	connection = &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationSucceeded)}
	command, _ = newRemoveCommandFixture(t, connection)
	command.output = failingDaemonWriter{err: writeErr}
	if err := command.Run(t.Context()); !errors.Is(err, writeErr) {
		t.Fatalf("output error = %v, want %v", err, writeErr)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("output failure close calls = %d, want 1", connection.closeCalls)
	}
}

// TestRemoveCommandKongSurfaceAcceptsProjectAndJSON verifies the product-design command shape remains top-level and explicit.
func TestRemoveCommandKongSurfaceAcceptsProjectAndJSON(t *testing.T) {
	for _, args := range [][]string{
		{"remove", "project-orders"},
		{"remove", "project-orders", "--json"},
		{"remove", "project-orders", "--intent", "intent-remove"},
	} {
		connection := &fakeDaemonControlClient{unregistration: removeTestUnregistration(t, domain.OperationSucceeded)}
		command, _ := newRemoveCommandFixture(t, connection)
		command.ProjectID = ""
		root := struct {
			Remove RemoveCmd `cmd:""`
		}{Remove: *command}
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
		if connection.unregistrationCalls != 1 {
			t.Fatalf("unregistration calls = %d, want 1", connection.unregistrationCalls)
		}
	}
}

// TestNewRemoveIntentIDCreatesValidDistinctValues verifies production workflows do not share deterministic idempotency identities.
func TestNewRemoveIntentIDCreatesValidDistinctValues(t *testing.T) {
	first, err := newRemoveIntentID()
	if err != nil {
		t.Fatalf("create first intent: %v", err)
	}
	second, err := newRemoveIntentID()
	if err != nil {
		t.Fatalf("create second intent: %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first intent is invalid: %v", err)
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("second intent is invalid: %v", err)
	}
	if first == second || !strings.HasPrefix(string(first), "intent-remove-") || !strings.HasPrefix(string(second), "intent-remove-") {
		t.Fatalf("intents = %q and %q, want distinct removal identities", first, second)
	}
	if _, err := newRemoveIntentIDFrom(strings.NewReader("short")); err == nil {
		t.Fatal("short entropy error = nil")
	}
}
