package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/networksetupapproval"
)

const setupCommandTestPool = "127.77.0.8/29"

// setupApprovalRunnerStub records the exact approval boundary delegated by SetupCmd.
type setupApprovalRunnerStub struct {
	outcome  networksetupapproval.Outcome
	err      error
	execute  func(context.Context, networksetupapproval.Request) (networksetupapproval.Outcome, error)
	calls    int
	contexts []context.Context
	requests []networksetupapproval.Request
}

// Execute records one selected revision before applying any test-specific result.
func (runner *setupApprovalRunnerStub) Execute(
	ctx context.Context,
	request networksetupapproval.Request,
) (networksetupapproval.Outcome, error) {
	runner.calls++
	runner.contexts = append(runner.contexts, ctx)
	runner.requests = append(runner.requests, request)
	if runner.execute != nil {
		return runner.execute(ctx, request)
	}
	return runner.outcome, runner.err
}

// setupCommandTestOperation constructs one valid global setup operation in the selected lifecycle state.
func setupCommandTestOperation(
	t *testing.T,
	state domain.OperationState,
	revision domain.Sequence,
) control.NetworkSetupOperation {
	t.Helper()

	requestedAt := time.Date(2026, time.July, 19, 14, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation(
		"operation-network-setup",
		networkSetupIntentID,
		domain.OperationKindNetworkSetup,
		"",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("construct network setup operation: %v", err)
	}
	if state == domain.OperationQueued {
		return control.NetworkSetupOperation{Operation: operation, Revision: revision}
	}
	if state == domain.OperationCancelled {
		operation, err = operation.Transition(state, "cancelled", requestedAt.Add(time.Second), nil)
		if err != nil {
			t.Fatalf("cancel network setup operation: %v", err)
		}
		return control.NetworkSetupOperation{Operation: operation, Revision: revision}
	}

	operation, err = operation.Transition(domain.OperationRunning, "preparing", requestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("start network setup operation: %v", err)
	}
	switch state {
	case domain.OperationRunning:
	case domain.OperationRequiresApproval:
		operation, err = operation.Transition(state, "awaiting approval", requestedAt.Add(2*time.Second), nil)
	case domain.OperationSucceeded:
		operation, err = operation.Transition(domain.OperationRequiresApproval, "awaiting approval", requestedAt.Add(2*time.Second), nil)
		if err == nil {
			operation, err = operation.Transition(domain.OperationRunning, "committing", requestedAt.Add(3*time.Second), nil)
		}
		if err == nil {
			operation, err = operation.Transition(state, "completed", requestedAt.Add(4*time.Second), nil)
		}
	case domain.OperationFailed:
		problem := &domain.Problem{Code: "network.setup_failed", Message: "network setup failed safely", Retryable: true}
		operation, err = operation.Transition(state, "failed", requestedAt.Add(2*time.Second), problem)
	default:
		t.Fatalf("unsupported setup command test state %q", state)
	}
	if err != nil {
		t.Fatalf("transition network setup operation to %s: %v", state, err)
	}
	return control.NetworkSetupOperation{Operation: operation, Revision: revision}
}

// setupCommandTestConfirmation constructs the exact successful confirmation following an approval revision.
func setupCommandTestConfirmation(
	t *testing.T,
	setup control.NetworkSetupOperation,
) control.NetworkSetupApprovalConfirmation {
	t.Helper()

	confirmation := control.NetworkSetupApprovalConfirmation{
		Operation:       setupCommandTestOperation(t, domain.OperationSucceeded, setup.Revision+3).Operation,
		Revision:        setup.Revision + 3,
		NetworkRevision: setup.Revision + 2,
		Pool:            setupCommandTestPool,
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("construct network setup confirmation: %v", err)
	}
	return confirmation
}

// newSetupCommandFixture creates a command around one observable one-shot daemon connection.
func newSetupCommandFixture(
	setup control.NetworkSetupOperation,
	runner NetworkSetupApprovalRunner,
) (*SetupCmd, *fakeDaemonControlClient, *bytes.Buffer) {
	connection := &fakeDaemonControlClient{networkSetup: setup}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewSetupCmd(client, runner)
	output := &bytes.Buffer{}
	command.output = output
	return command, connection, output
}

// TestSetupCommandCompletesExactApprovalRevision verifies start, consent, confirmation, and rendering stay correlated.
func TestSetupCommandCompletesExactApprovalRevision(t *testing.T) {
	setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 7)
	confirmation := setupCommandTestConfirmation(t, setup)
	runner := &setupApprovalRunnerStub{outcome: networksetupapproval.Outcome{
		State:        networksetupapproval.Succeeded,
		Confirmation: &confirmation,
	}}
	command, connection, output := newSetupCommandFixture(setup, runner)
	runner.execute = func(ctx context.Context, request networksetupapproval.Request) (networksetupapproval.Outcome, error) {
		if connection.closeCalls != 1 {
			t.Fatalf("daemon close calls before approval = %d, want 1", connection.closeCalls)
		}
		return runner.outcome, nil
	}

	ctx := context.WithValue(t.Context(), setupCommandContextKey{}, "setup")
	if err := command.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantRequest := networksetupapproval.Request{
		OperationID:               setup.Operation.ID,
		ExpectedOperationRevision: setup.Revision,
	}
	if runner.calls != 1 || len(runner.requests) != 1 || runner.requests[0] != wantRequest {
		t.Fatalf("approval calls = %d, requests = %#v, want one %#v", runner.calls, runner.requests, wantRequest)
	}
	if len(runner.contexts) != 1 || runner.contexts[0] != ctx {
		t.Fatalf("approval contexts = %#v, want exact CLI context", runner.contexts)
	}
	if connection.networkSetupCalls != 1 ||
		len(connection.networkSetupRequests) != 1 ||
		connection.networkSetupRequests[0] != (control.StartNetworkSetupRequest{IntentID: networkSetupIntentID}) {
		t.Fatalf("start calls = %d, requests = %#v", connection.networkSetupCalls, connection.networkSetupRequests)
	}
	wantOutput := strings.Join([]string{
		"Network setup complete.",
		"Pool: " + setupCommandTestPool,
		"Operation: operation-network-setup",
		"State: succeeded",
		"Network revision: 9",
		"Revision: 10",
		"",
	}, "\n")
	if output.String() != wantOutput {
		t.Fatalf("output = %q, want %q", output.String(), wantOutput)
	}
}

// TestSetupCommandReplaysCompletedSetupWithoutApproval verifies the fixed intent makes repeat setup idempotent.
func TestSetupCommandReplaysCompletedSetupWithoutApproval(t *testing.T) {
	setup := setupCommandTestOperation(t, domain.OperationSucceeded, 10)
	runner := &setupApprovalRunnerStub{}
	command, connection, output := newSetupCommandFixture(setup, runner)

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("approval calls = %d, want 0", runner.calls)
	}
	if connection.networkSetupCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("daemon calls = start:%d close:%d, want 1 each", connection.networkSetupCalls, connection.closeCalls)
	}
	wantOutput := strings.Join([]string{
		"Network setup is already complete.",
		"Operation: operation-network-setup",
		"State: succeeded",
		"Revision: 10",
		"",
	}, "\n")
	if output.String() != wantOutput {
		t.Fatalf("output = %q, want %q", output.String(), wantOutput)
	}
}

// TestSetupCommandRejectsMalformedStartResponses verifies substituted clients cannot open consent from invalid authority.
func TestSetupCommandRejectsMalformedStartResponses(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*control.NetworkSetupOperation)
		want   string
	}{
		{
			name: "invalid operation",
			mutate: func(setup *control.NetworkSetupOperation) {
				setup.Operation.StartedAt = nil
			},
			want: "validate response",
		},
		{
			name: "project operation",
			mutate: func(setup *control.NetworkSetupOperation) {
				setup.Operation.Kind = domain.OperationKindProjectStart
				setup.Operation.ProjectID = "project-orders"
			},
			want: "machine-global",
		},
		{
			name: "wrong intent",
			mutate: func(setup *control.NetworkSetupOperation) {
				setup.Operation.IntentID = "intent-other"
			},
			want: "response belongs to intent",
		},
		{
			name: "zero revision",
			mutate: func(setup *control.NetworkSetupOperation) {
				setup.Revision = 0
			},
			want: "network setup revision",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 7)
			test.mutate(&setup)
			runner := &setupApprovalRunnerStub{}
			command, connection, output := newSetupCommandFixture(setup, runner)

			err := command.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
			if runner.calls != 0 || output.Len() != 0 {
				t.Fatalf("approval calls = %d, output = %q, want neither", runner.calls, output.String())
			}
			if connection.closeCalls != 1 {
				t.Fatalf("daemon close calls = %d, want 1", connection.closeCalls)
			}
		})
	}
}

// TestSetupCommandMapsIncompleteApprovalOutcomes verifies every safe non-success state exits nonzero without false output.
func TestSetupCommandMapsIncompleteApprovalOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		outcome networksetupapproval.Outcome
		want    string
	}{
		{
			name:    "declined",
			outcome: networksetupapproval.Outcome{State: networksetupapproval.Declined},
			want:    "was declined",
		},
		{
			name:    "unavailable",
			outcome: networksetupapproval.Outcome{State: networksetupapproval.Unavailable},
			want:    "is unavailable",
		},
		{
			name: "helper failed",
			outcome: networksetupapproval.Outcome{
				State: networksetupapproval.HelperFailed,
				HelperFailure: &networksetupapproval.HelperFailure{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "loopback mutation failed safely",
				},
			},
			want: "helper failed (mutation_failed): loopback mutation failed safely",
		},
		{
			name:    "indeterminate",
			outcome: networksetupapproval.Outcome{State: networksetupapproval.Indeterminate},
			want:    "is indeterminate",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 7)
			runner := &setupApprovalRunnerStub{outcome: test.outcome}
			command, connection, output := newSetupCommandFixture(setup, runner)

			err := command.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want empty", output.String())
			}
			if runner.calls != 1 || connection.closeCalls != 1 {
				t.Fatalf("calls = approval:%d close:%d, want 1 each", runner.calls, connection.closeCalls)
			}
		})
	}
}

// TestSetupCommandReturnsStartAndApprovalFailures verifies workflow errors remain discoverable through wrapping.
func TestSetupCommandReturnsStartAndApprovalFailures(t *testing.T) {
	startErr := errors.New("start failed")
	connection := &fakeDaemonControlClient{networkSetupErr: startErr}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	runner := &setupApprovalRunnerStub{}
	command := NewSetupCmd(client, runner)
	command.output = &bytes.Buffer{}
	if err := command.Run(t.Context()); !errors.Is(err, startErr) {
		t.Fatalf("start error = %v, want %v", err, startErr)
	}
	if runner.calls != 0 || connection.closeCalls != 1 {
		t.Fatalf("start failure calls = approval:%d close:%d", runner.calls, connection.closeCalls)
	}

	approvalErr := errors.New("approval failed")
	setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 7)
	runner = &setupApprovalRunnerStub{err: approvalErr}
	command, connection, output := newSetupCommandFixture(setup, runner)
	if err := command.Run(t.Context()); !errors.Is(err, approvalErr) {
		t.Fatalf("approval error = %v, want %v", err, approvalErr)
	}
	if output.Len() != 0 || runner.calls != 1 || connection.closeCalls != 1 {
		t.Fatalf("approval failure output = %q, calls = approval:%d close:%d", output.String(), runner.calls, connection.closeCalls)
	}
}

// TestSetupCommandRejectsNonApprovalOperationStates verifies durable progress cannot open consent at another lifecycle edge.
func TestSetupCommandRejectsNonApprovalOperationStates(t *testing.T) {
	for _, state := range []domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationFailed,
		domain.OperationCancelled,
	} {
		t.Run(string(state), func(t *testing.T) {
			setup := setupCommandTestOperation(t, state, 7)
			runner := &setupApprovalRunnerStub{}
			command, _, output := newSetupCommandFixture(setup, runner)

			err := command.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), string(state)) {
				t.Fatalf("Run() error = %v, want state %q", err, state)
			}
			if state == domain.OperationFailed && !strings.Contains(err.Error(), "network setup failed safely") {
				t.Fatalf("Run() error = %v, want bounded daemon problem", err)
			}
			if runner.calls != 0 || output.Len() != 0 {
				t.Fatalf("approval calls = %d, output = %q, want neither", runner.calls, output.String())
			}
		})
	}
}

// TestSetupCommandRejectsMalformedSuccessfulOutcomes verifies injected runners cannot produce false completion output.
func TestSetupCommandRejectsMalformedSuccessfulOutcomes(t *testing.T) {
	setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 7)
	valid := setupCommandTestConfirmation(t, setup)
	tests := []struct {
		name    string
		outcome networksetupapproval.Outcome
		want    string
	}{
		{
			name:    "missing confirmation",
			outcome: networksetupapproval.Outcome{State: networksetupapproval.Succeeded},
			want:    "missing confirmation",
		},
		{
			name: "success with helper failure",
			outcome: networksetupapproval.Outcome{
				State:         networksetupapproval.Succeeded,
				Confirmation:  &valid,
				HelperFailure: &networksetupapproval.HelperFailure{Code: helper.ErrorCodeMutationFailed, Message: "unexpected failure"},
			},
			want: "contains a helper failure",
		},
		{
			name: "invalid confirmation",
			outcome: func() networksetupapproval.Outcome {
				confirmation := valid
				confirmation.Pool = "192.0.2.0/29"
				return networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
			}(),
			want: "validate confirmation",
		},
		{
			name: "wrong revision",
			outcome: func() networksetupapproval.Outcome {
				confirmation := valid
				confirmation.Revision++
				confirmation.NetworkRevision++
				return networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
			}(),
			want: "crossed the selected operation revision",
		},
		{
			name: "wrong confirmation intent",
			outcome: func() networksetupapproval.Outcome {
				confirmation := valid
				confirmation.Operation.IntentID = "intent-other"
				return networksetupapproval.Outcome{State: networksetupapproval.Succeeded, Confirmation: &confirmation}
			}(),
			want: "crossed the selected operation revision",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &setupApprovalRunnerStub{outcome: test.outcome}
			command, _, output := newSetupCommandFixture(setup, runner)

			err := command.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want empty", output.String())
			}
		})
	}
}

// TestSetupCommandReturnsOutputFailures verifies a terminal write failure cannot hide a completed daemon confirmation.
func TestSetupCommandReturnsOutputFailures(t *testing.T) {
	writeErr := errors.New("write failed")
	setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 7)
	confirmation := setupCommandTestConfirmation(t, setup)
	runner := &setupApprovalRunnerStub{outcome: networksetupapproval.Outcome{
		State:        networksetupapproval.Succeeded,
		Confirmation: &confirmation,
	}}
	command, _, _ := newSetupCommandFixture(setup, runner)
	command.output = failingDaemonWriter{err: writeErr}

	if err := command.Run(t.Context()); !errors.Is(err, writeErr) {
		t.Fatalf("Run() error = %v, want %v", err, writeErr)
	}
}

// TestSetupCommandHonorsPreflightCancellation verifies cancellation cannot open a daemon connection or consent UI.
func TestSetupCommandHonorsPreflightCancellation(t *testing.T) {
	setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 7)
	runner := &setupApprovalRunnerStub{}
	command, connection, output := newSetupCommandFixture(setup, runner)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := command.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want %v", err, context.Canceled)
	}
	if connection.networkSetupCalls != 0 || connection.closeCalls != 0 || runner.calls != 0 || output.Len() != 0 {
		t.Fatalf(
			"calls = start:%d close:%d approval:%d, output = %q, want none",
			connection.networkSetupCalls,
			connection.closeCalls,
			runner.calls,
			output.String(),
		)
	}
}

// TestNewSetupCmdRequiresDependencies verifies bad application assembly fails before command parsing or consent.
func TestNewSetupCmdRequiresDependencies(t *testing.T) {
	client := NewDaemonClient()
	runner := &setupApprovalRunnerStub{}
	var typedNilRunner *setupApprovalRunnerStub
	tests := []struct {
		name  string
		build func()
	}{
		{name: "daemon client", build: func() { NewSetupCmd(nil, runner) }},
		{name: "approval runner", build: func() { NewSetupCmd(client, nil) }},
		{name: "typed nil approval runner", build: func() { NewSetupCmd(client, typedNilRunner) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewSetupCmd() did not panic")
				}
			}()
			test.build()
		})
	}
}

// TestSetupCommandKongSurface verifies first-run setup remains one argument-free top-level command.
func TestSetupCommandKongSurface(t *testing.T) {
	setup := setupCommandTestOperation(t, domain.OperationSucceeded, 10)
	runner := &setupApprovalRunnerStub{}
	command, connection, _ := newSetupCommandFixture(setup, runner)
	root := struct {
		Setup SetupCmd `cmd:""`
	}{Setup: *command}
	parser, err := kong.New(&root)
	if err != nil {
		t.Fatalf("kong.New() error = %v", err)
	}
	parsed, err := parser.Parse([]string{"setup"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	parsed.BindTo(t.Context(), (*context.Context)(nil))
	if err := parsed.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if connection.networkSetupCalls != 1 || runner.calls != 0 {
		t.Fatalf("calls = start:%d approval:%d, want 1 and 0", connection.networkSetupCalls, runner.calls)
	}
}

// setupCommandContextKey keeps context identity assertions isolated from other command tests.
type setupCommandContextKey struct{}
