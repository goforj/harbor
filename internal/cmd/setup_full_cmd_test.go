package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/networkdataplaneapproval"
	"github.com/goforj/harbor/internal/networkresolverapproval"
	"github.com/goforj/harbor/internal/networksetupapproval"
	"github.com/goforj/harbor/internal/rpc"
)

// resolverApprovalRunnerStub records resolver approval without invoking a host helper.
type resolverApprovalRunnerStub struct {
	outcome  networkresolverapproval.Outcome
	err      error
	execute  func(context.Context, networkresolverapproval.Request) (networkresolverapproval.Outcome, error)
	contexts []context.Context
	requests []networkresolverapproval.Request
}

// Execute records one resolver approval selection.
func (runner *resolverApprovalRunnerStub) Execute(
	ctx context.Context,
	request networkresolverapproval.Request,
) (networkresolverapproval.Outcome, error) {
	runner.contexts = append(runner.contexts, ctx)
	runner.requests = append(runner.requests, request)
	if runner.execute != nil {
		return runner.execute(ctx, request)
	}
	return runner.outcome, runner.err
}

// dataPlaneApprovalRunnerStub records trusted-ingress approval without invoking a host helper.
type dataPlaneApprovalRunnerStub struct {
	trust         networkdataplaneapproval.TrustOutcome
	lowPort       networkdataplaneapproval.LowPortOutcome
	trustErr      error
	lowPortErr    error
	executeTrust  func(context.Context, networkdataplaneapproval.Request) (networkdataplaneapproval.TrustOutcome, error)
	executeLow    func(context.Context, networkdataplaneapproval.Request) (networkdataplaneapproval.LowPortOutcome, error)
	contexts      []context.Context
	trustRequests []networkdataplaneapproval.Request
	lowRequests   []networkdataplaneapproval.Request
}

// ExecuteTrust records the exact trust revision.
func (runner *dataPlaneApprovalRunnerStub) ExecuteTrust(
	ctx context.Context,
	request networkdataplaneapproval.Request,
) (networkdataplaneapproval.TrustOutcome, error) {
	runner.contexts = append(runner.contexts, ctx)
	runner.trustRequests = append(runner.trustRequests, request)
	if runner.executeTrust != nil {
		return runner.executeTrust(ctx, request)
	}
	return runner.trust, runner.trustErr
}

// ExecuteLowPorts records the exact low-port revision.
func (runner *dataPlaneApprovalRunnerStub) ExecuteLowPorts(
	ctx context.Context,
	request networkdataplaneapproval.Request,
) (networkdataplaneapproval.LowPortOutcome, error) {
	runner.contexts = append(runner.contexts, ctx)
	runner.lowRequests = append(runner.lowRequests, request)
	if runner.executeLow != nil {
		return runner.executeLow(ctx, request)
	}
	return runner.lowPort, runner.lowPortErr
}

// fullSetupCommandFixture keeps every approval boundary and daemon call observable.
type fullSetupCommandFixture struct {
	command    *SetupCmd
	connection *fakeDaemonControlClient
	pool       *setupApprovalRunnerStub
	resolver   *resolverApprovalRunnerStub
	dataPlane  *dataPlaneApprovalRunnerStub
	output     *bytes.Buffer
}

// newFullSetupCommandFixture starts from a fully completed durable network foundation.
func newFullSetupCommandFixture(t *testing.T) *fullSetupCommandFixture {
	t.Helper()

	connection := &fakeDaemonControlClient{
		networkSetup: setupCommandTestOperation(t, domain.OperationSucceeded, 4),
		resolverSetup: setupCommandResolverOperation(
			t,
			domain.OperationSucceeded,
			"completed",
			8,
		),
		dataPlaneSetup: setupCommandDataPlaneOperation(
			t,
			domain.OperationSucceeded,
			"completed",
			12,
		),
	}
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})
	pool := &setupApprovalRunnerStub{}
	resolver := &resolverApprovalRunnerStub{}
	dataPlane := &dataPlaneApprovalRunnerStub{}
	command := NewFullSetupCmd(client, pool, resolver, dataPlane)
	output := &bytes.Buffer{}
	command.output = output

	return &fullSetupCommandFixture{
		command:    command,
		connection: connection,
		pool:       pool,
		resolver:   resolver,
		dataPlane:  dataPlane,
		output:     output,
	}
}

// setupCommandOperation creates one valid machine-global operation in the requested phase.
func setupCommandOperation(
	t *testing.T,
	id domain.OperationID,
	intentID domain.IntentID,
	kind domain.OperationKind,
	state domain.OperationState,
	phase string,
) domain.Operation {
	t.Helper()

	now := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation(id, intentID, kind, "", now)
	if err != nil {
		t.Fatalf("new operation: %v", err)
	}
	if state == domain.OperationQueued {
		return operation
	}
	if state == domain.OperationCancelled {
		operation, err = operation.Transition(state, phase, now.Add(time.Second), nil)
		if err != nil {
			t.Fatalf("cancel operation: %v", err)
		}
		return operation
	}

	operation, err = operation.Transition(
		domain.OperationRunning,
		"preparing",
		now.Add(time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("start operation: %v", err)
	}
	switch state {
	case domain.OperationRunning:
		return operation
	case domain.OperationRequiresApproval:
		operation, err = operation.Transition(state, phase, now.Add(2*time.Second), nil)
	case domain.OperationFailed:
		operation, err = operation.Transition(
			state,
			phase,
			now.Add(2*time.Second),
			&domain.Problem{
				Code:      "network.setup_failed",
				Message:   "network setup failed safely",
				Retryable: true,
			},
		)
	case domain.OperationSucceeded:
		operation, err = operation.Transition(
			domain.OperationRequiresApproval,
			"awaiting approval",
			now.Add(2*time.Second),
			nil,
		)
		if err == nil {
			operation, err = operation.Transition(
				domain.OperationRunning,
				"committing",
				now.Add(3*time.Second),
				nil,
			)
		}
		if err == nil {
			operation, err = operation.Transition(
				state,
				phase,
				now.Add(4*time.Second),
				nil,
			)
		}
	default:
		t.Fatalf("unsupported operation state %q", state)
	}
	if err != nil {
		t.Fatalf("transition operation to %s: %v", state, err)
	}
	return operation
}

// setupCommandResolverOperation creates one valid resolver setup snapshot.
func setupCommandResolverOperation(
	t *testing.T,
	state domain.OperationState,
	phase string,
	revision domain.Sequence,
) control.NetworkResolverSetupOperation {
	t.Helper()

	return control.NetworkResolverSetupOperation{
		Operation: setupCommandOperation(
			t,
			"operation-network-resolver",
			networkResolverSetupIntentID,
			domain.OperationKindNetworkResolverSetup,
			state,
			phase,
		),
		Revision: revision,
	}
}

// setupCommandDataPlaneOperation creates one valid trusted-ingress setup snapshot.
func setupCommandDataPlaneOperation(
	t *testing.T,
	state domain.OperationState,
	phase string,
	revision domain.Sequence,
) control.NetworkDataPlaneSetupOperation {
	t.Helper()

	return control.NetworkDataPlaneSetupOperation{
		Operation: setupCommandOperation(
			t,
			"operation-network-data-plane",
			networkDataPlaneSetupIntentID,
			domain.OperationKindNetworkDataPlaneSetup,
			state,
			phase,
		),
		Revision: revision,
	}
}

// setupCommandResolverConfirmation creates a valid completed resolver confirmation.
func setupCommandResolverConfirmation(
	t *testing.T,
	setup control.NetworkResolverSetupOperation,
) control.NetworkResolverSetupApprovalConfirmation {
	t.Helper()

	confirmation := control.NetworkResolverSetupApprovalConfirmation{
		Operation: setupCommandOperation(
			t,
			setup.Operation.ID,
			setup.Operation.IntentID,
			domain.OperationKindNetworkResolverSetup,
			domain.OperationSucceeded,
			"completed",
		),
		NetworkRevision: setup.Revision + 2,
		Revision:        setup.Revision + 3,
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("resolver confirmation: %v", err)
	}
	return confirmation
}

// setupCommandDataPlaneConfirmation creates a valid completed trusted-ingress confirmation.
func setupCommandDataPlaneConfirmation(
	t *testing.T,
	setup control.NetworkDataPlaneSetupOperation,
) control.NetworkDataPlaneSetupConfirmation {
	t.Helper()

	confirmation := control.NetworkDataPlaneSetupConfirmation{
		Operation: setupCommandOperation(
			t,
			setup.Operation.ID,
			setup.Operation.IntentID,
			domain.OperationKindNetworkDataPlaneSetup,
			domain.OperationSucceeded,
			"completed",
		),
		NetworkRevision: setup.Revision + 1,
		Revision:        setup.Revision + 2,
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("data-plane confirmation: %v", err)
	}
	return confirmation
}

// TestFullSetupCommandCompletesEveryApprovalInOrder proves success is withheld until trusted ingress completes.
func TestFullSetupCommandCompletesEveryApprovalInOrder(t *testing.T) {
	fixture := newFullSetupCommandFixture(t)
	poolSetup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 4)
	poolConfirmation := setupCommandTestConfirmation(t, poolSetup)
	resolverSetup := setupCommandResolverOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting resolver approval",
		8,
	)
	resolverConfirmation := setupCommandResolverConfirmation(t, resolverSetup)
	trustSetup := setupCommandDataPlaneOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting trust approval",
		12,
	)
	lowPortSetup := setupCommandDataPlaneOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting low-port approval",
		13,
	)
	lowPortConfirmation := setupCommandDataPlaneConfirmation(t, lowPortSetup)
	fixture.connection.networkSetup = poolSetup
	fixture.connection.resolverSetup = resolverSetup
	fixture.connection.dataPlaneSetup = trustSetup
	fixture.pool.outcome = networksetupapproval.Outcome{
		State:        networksetupapproval.Succeeded,
		Confirmation: &poolConfirmation,
	}
	fixture.resolver.outcome = networkresolverapproval.Outcome{
		State:        networkresolverapproval.Succeeded,
		Confirmation: &resolverConfirmation,
	}
	fixture.dataPlane.trust = networkdataplaneapproval.TrustOutcome{
		State: networkdataplaneapproval.Succeeded,
		Setup: &lowPortSetup,
	}
	fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
		State:        networkdataplaneapproval.Succeeded,
		Confirmation: &lowPortConfirmation,
	}

	var approvalOrder []string
	fixture.pool.execute = func(
		context.Context,
		networksetupapproval.Request,
	) (networksetupapproval.Outcome, error) {
		approvalOrder = append(approvalOrder, "pool")
		return fixture.pool.outcome, nil
	}
	fixture.resolver.execute = func(
		context.Context,
		networkresolverapproval.Request,
	) (networkresolverapproval.Outcome, error) {
		approvalOrder = append(approvalOrder, "resolver")
		return fixture.resolver.outcome, nil
	}
	fixture.dataPlane.executeTrust = func(
		context.Context,
		networkdataplaneapproval.Request,
	) (networkdataplaneapproval.TrustOutcome, error) {
		approvalOrder = append(approvalOrder, "trust")
		return fixture.dataPlane.trust, nil
	}
	fixture.dataPlane.executeLow = func(
		context.Context,
		networkdataplaneapproval.Request,
	) (networkdataplaneapproval.LowPortOutcome, error) {
		approvalOrder = append(approvalOrder, "low-port")
		return fixture.dataPlane.lowPort, nil
	}

	ctx := context.WithValue(t.Context(), setupCommandContextKey{}, "full setup")
	if err := fixture.command.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(approvalOrder, []string{"pool", "resolver", "trust", "low-port"}) {
		t.Fatalf("approval order = %#v", approvalOrder)
	}
	if got, want := fixture.pool.requests, []networksetupapproval.Request{
		{
			OperationID:               poolSetup.Operation.ID,
			ExpectedOperationRevision: poolSetup.Revision,
		},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pool requests = %#v, want %#v", got, want)
	}
	if got, want := fixture.resolver.requests, []networkresolverapproval.Request{
		{
			OperationID:               resolverSetup.Operation.ID,
			ExpectedOperationRevision: resolverSetup.Revision,
		},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolver requests = %#v, want %#v", got, want)
	}
	if got, want := fixture.dataPlane.trustRequests, []networkdataplaneapproval.Request{
		{
			OperationID:               trustSetup.Operation.ID,
			ExpectedOperationRevision: trustSetup.Revision,
		},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("trust requests = %#v, want %#v", got, want)
	}
	if got, want := fixture.dataPlane.lowRequests, []networkdataplaneapproval.Request{
		{
			OperationID:               lowPortSetup.Operation.ID,
			ExpectedOperationRevision: lowPortSetup.Revision,
		},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("low-port requests = %#v, want %#v", got, want)
	}
	if len(fixture.resolver.contexts) != 1 || fixture.resolver.contexts[0] != ctx ||
		len(fixture.dataPlane.contexts) != 2 || fixture.dataPlane.contexts[0] != ctx || fixture.dataPlane.contexts[1] != ctx {
		t.Fatal("full setup did not preserve the CLI context across approvals")
	}
	if fixture.output.String() != "Network setup complete.\n" {
		t.Fatalf("output = %q", fixture.output.String())
	}
}

// TestFullSetupCommandReplaysCompletedPhasesWithoutApproval proves reruns read durable success instead of reopening consent.
func TestFullSetupCommandReplaysCompletedPhasesWithoutApproval(t *testing.T) {
	fixture := newFullSetupCommandFixture(t)

	if err := fixture.command.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if fixture.pool.calls != 0 || len(fixture.resolver.requests) != 0 ||
		len(fixture.dataPlane.trustRequests) != 0 || len(fixture.dataPlane.lowRequests) != 0 {
		t.Fatal("completed setup reopened an approval boundary")
	}
	if fixture.connection.networkSetupCalls != 1 ||
		fixture.connection.resolverSetupCalls != 1 ||
		fixture.connection.dataPlaneSetupCalls != 1 {
		t.Fatalf(
			"start calls = pool:%d resolver:%d data-plane:%d",
			fixture.connection.networkSetupCalls,
			fixture.connection.resolverSetupCalls,
			fixture.connection.dataPlaneSetupCalls,
		)
	}
	if fixture.output.String() != "Network setup complete.\n" {
		t.Fatalf("output = %q", fixture.output.String())
	}
}

// TestFullSetupCommandReplaysLowPortsWithoutTrust preserves a durable trust result after an interrupted final phase.
func TestFullSetupCommandReplaysLowPortsWithoutTrust(t *testing.T) {
	fixture := newFullSetupCommandFixture(t)
	lowPortSetup := setupCommandDataPlaneOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting low-port approval",
		12,
	)
	confirmation := setupCommandDataPlaneConfirmation(t, lowPortSetup)
	fixture.connection.dataPlaneSetup = lowPortSetup
	fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
		State:        networkdataplaneapproval.Succeeded,
		Confirmation: &confirmation,
	}

	if err := fixture.command.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(fixture.dataPlane.trustRequests) != 0 {
		t.Fatalf("trust requests = %#v, want none", fixture.dataPlane.trustRequests)
	}
	if got, want := fixture.dataPlane.lowRequests, []networkdataplaneapproval.Request{
		{
			OperationID:               lowPortSetup.Operation.ID,
			ExpectedOperationRevision: lowPortSetup.Revision,
		},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("low-port requests = %#v, want %#v", got, want)
	}
}

// TestFullSetupCommandReturnsFinalOutputFailure keeps a completed host setup from being reported as a successful CLI write.
func TestFullSetupCommandReturnsFinalOutputFailure(t *testing.T) {
	fixture := newFullSetupCommandFixture(t)
	want := errors.New("write failed")
	fixture.command.output = failingDaemonWriter{err: want}

	if err := fixture.command.Run(t.Context()); !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want %v", err, want)
	}
}

// TestFullSetupCommandRequiresCompletedPoolPhase keeps later native approvals behind exact pool completion.
func TestFullSetupCommandRequiresCompletedPoolPhase(t *testing.T) {
	t.Run("replayed success", func(t *testing.T) {
		fixture := newFullSetupCommandFixture(t)
		fixture.connection.networkSetup.Operation.Phase = "committing pool"

		err := fixture.command.Run(t.Context())
		if err == nil || !strings.Contains(err.Error(), "succeeded in unsupported phase") {
			t.Fatalf("Run() error = %v, want unsupported pool phase", err)
		}
		if fixture.connection.resolverSetupCalls != 0 {
			t.Fatalf("resolver start calls = %d, want 0", fixture.connection.resolverSetupCalls)
		}
	})

	t.Run("approval confirmation", func(t *testing.T) {
		fixture := newFullSetupCommandFixture(t)
		setup := setupCommandTestOperation(t, domain.OperationRequiresApproval, 4)
		confirmation := setupCommandTestConfirmation(t, setup)
		confirmation.Operation.Phase = "committing pool"
		fixture.connection.networkSetup = setup
		fixture.pool.outcome = networksetupapproval.Outcome{
			State:        networksetupapproval.Succeeded,
			Confirmation: &confirmation,
		}

		err := fixture.command.Run(t.Context())
		if err == nil || !strings.Contains(err.Error(), "selected operation revision") {
			t.Fatalf("Run() error = %v, want crossed pool confirmation", err)
		}
		if fixture.connection.resolverSetupCalls != 0 {
			t.Fatalf("resolver start calls = %d, want 0", fixture.connection.resolverSetupCalls)
		}
	})
}

// TestNewFullSetupCmdRequiresEveryApprovalRunner rejects bad production assembly before consent can be reached.
func TestNewFullSetupCmdRequiresEveryApprovalRunner(t *testing.T) {
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return &fakeDaemonControlClient{}, nil
	})
	pool := &setupApprovalRunnerStub{}
	resolver := &resolverApprovalRunnerStub{}
	dataPlane := &dataPlaneApprovalRunnerStub{}
	var typedResolver *resolverApprovalRunnerStub
	var typedDataPlane *dataPlaneApprovalRunnerStub
	tests := []struct {
		name  string
		build func()
	}{
		{
			name: "nil resolver",
			build: func() {
				NewFullSetupCmd(client, pool, nil, dataPlane)
			},
		},
		{
			name: "typed nil resolver",
			build: func() {
				NewFullSetupCmd(client, pool, typedResolver, dataPlane)
			},
		},
		{
			name: "nil data plane",
			build: func() {
				NewFullSetupCmd(client, pool, resolver, nil)
			},
		},
		{
			name: "typed nil data plane",
			build: func() {
				NewFullSetupCmd(client, pool, resolver, typedDataPlane)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewFullSetupCmd() did not panic")
				}
			}()
			test.build()
		})
	}
}

// TestNewNetworkSetupRetryIntentUsesIndependentEntropy proves retry IDs retain their phase prefix and bounded shape.
func TestNewNetworkSetupRetryIntentUsesIndependentEntropy(t *testing.T) {
	randomIntent, err := newNetworkSetupRetryIntent(networkSetupIntentID)
	if err != nil {
		t.Fatalf("newNetworkSetupRetryIntent() error = %v", err)
	}
	if !strings.HasPrefix(string(randomIntent), string(networkSetupIntentID)+"-") {
		t.Fatalf("random intent = %q", randomIntent)
	}

	intentID, err := newNetworkSetupRetryIntentFrom(
		bytes.NewReader(bytes.Repeat([]byte{0xab}, 16)),
		networkResolverSetupIntentID,
	)
	if err != nil {
		t.Fatalf("newNetworkSetupRetryIntentFrom() error = %v", err)
	}
	if got, want := intentID, domain.IntentID("intent-network-resolver-setup-abababababababababababababababab"); got != want {
		t.Fatalf("intent = %q, want %q", got, want)
	}
	if err := intentID.Validate(); err != nil {
		t.Fatalf("intent validation = %v", err)
	}

	want := errors.New("entropy unavailable")
	if _, err := newNetworkSetupRetryIntentFrom(
		&failingSetupEntropyReader{err: want},
		networkSetupIntentID,
	); !errors.Is(err, want) {
		t.Fatalf("entropy error = %v, want %v", err, want)
	}
}

// failingSetupEntropyReader makes retry identity entropy failure deterministic.
type failingSetupEntropyReader struct {
	err error
}

// Read returns the configured entropy failure without producing identity bytes.
func (reader *failingSetupEntropyReader) Read([]byte) (int, error) {
	return 0, reader.err
}

// TestFullSetupApprovalOutcomeErrorsPreserveBoundedGuidance covers every non-success phase conclusion.
func TestFullSetupApprovalOutcomeErrorsPreserveBoundedGuidance(t *testing.T) {
	states := []struct {
		name  string
		state networkdataplaneapproval.State
		want  string
	}{
		{
			name:  "declined",
			state: networkdataplaneapproval.Declined,
			want:  "declined",
		},
		{
			name:  "unavailable",
			state: networkdataplaneapproval.Unavailable,
			want:  "unavailable",
		},
		{
			name:  "failed",
			state: networkdataplaneapproval.HelperFailed,
			want:  "without a bounded",
		},
		{
			name:  "indeterminate",
			state: networkdataplaneapproval.Indeterminate,
			want:  "indeterminate",
		},
		{
			name:  "unsupported",
			state: "unknown",
			want:  "unsupported",
		},
	}
	for _, test := range states {
		t.Run(test.name, func(t *testing.T) {
			trust := networkDataPlaneTrustApprovalOutcomeError{
				outcome: networkdataplaneapproval.TrustOutcome{
					State: test.state,
				},
			}
			lowPort := networkDataPlaneLowPortApprovalOutcomeError{
				outcome: networkdataplaneapproval.LowPortOutcome{
					State: test.state,
				},
			}
			if !strings.Contains(trust.Error(), test.want) || !strings.Contains(lowPort.Error(), test.want) {
				t.Fatalf("errors = %q / %q, want %q", trust.Error(), lowPort.Error(), test.want)
			}
		})
	}

	failure := &networkdataplaneapproval.HelperFailure{
		Code:    helper.ErrorCodeMutationFailed,
		Message: "bounded detail",
	}
	if got := networkDataPlaneApprovalError(
		"trust",
		networkdataplaneapproval.HelperFailed,
		failure,
	); !strings.Contains(got, "bounded detail") {
		t.Fatal("helper failure detail was lost")
	}
	resolverStates := []networkresolverapproval.State{
		networkresolverapproval.Declined,
		networkresolverapproval.Unavailable,
		networkresolverapproval.HelperFailed,
		networkresolverapproval.Indeterminate,
		"unknown",
	}
	for _, state := range resolverStates {
		failure := networkResolverSetupApprovalOutcomeError{
			outcome: networkresolverapproval.Outcome{
				State: state,
			},
		}
		if failure.Error() == "" {
			t.Fatalf("resolver state %q produced empty error", state)
		}
	}
}

// TestFullSetupCommandRejectsResolverFailures proves resolver setup cannot be skipped or falsely reported complete.
func TestFullSetupCommandRejectsResolverFailures(t *testing.T) {
	startErr := errors.New("resolver start failed")
	approvalErr := errors.New("resolver approval failed")
	tests := []struct {
		name    string
		mutate  func(*testing.T, *fullSetupCommandFixture)
		want    string
		wantErr error
	}{
		{
			name: "start failure",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.resolverSetupErr = startErr
			},
			wantErr: startErr,
		},
		{
			name: "invalid start response",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.resolverSetup = control.NetworkResolverSetupOperation{}
			},
			want: "validate response",
		},
		{
			name: "crossed start intent",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.resolverSetup.Operation.IntentID = "intent-crossed"
			},
			want: "selected intent",
		},
		{
			name: "success in wrong phase",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.resolverSetup.Operation.Phase = "committing resolver"
			},
			want: "succeeded in unsupported phase",
		},
		{
			name: "nonapproval state",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.resolverSetup = setupCommandResolverOperation(
					t,
					domain.OperationRunning,
					"preparing resolver",
					8,
				)
			},
			want: "is running",
		},
		{
			name: "unsupported approval phase",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.resolverSetup = setupCommandResolverOperation(
					t,
					domain.OperationRequiresApproval,
					"awaiting another approval",
					8,
				)
			},
			want: "requires approval in unsupported phase",
		},
		{
			name: "approval failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.err = approvalErr
			},
			wantErr: approvalErr,
		},
		{
			name: "declined approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome = networkresolverapproval.Outcome{
					State: networkresolverapproval.Declined,
				}
			},
			want: "was declined",
		},
		{
			name: "unavailable approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome = networkresolverapproval.Outcome{
					State: networkresolverapproval.Unavailable,
				}
			},
			want: "is unavailable",
		},
		{
			name: "helper failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome = networkresolverapproval.Outcome{
					State: networkresolverapproval.HelperFailed,
					HelperFailure: &networkresolverapproval.HelperFailure{
						Code:    helper.ErrorCodeMutationFailed,
						Message: "bounded resolver failure",
					},
				}
			},
			want: "bounded resolver failure",
		},
		{
			name: "indeterminate approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome = networkresolverapproval.Outcome{
					State: networkresolverapproval.Indeterminate,
				}
			},
			want: "is indeterminate",
		},
		{
			name: "successful approval without confirmation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.Confirmation = nil
			},
			want: "inconsistent evidence",
		},
		{
			name: "successful approval with helper failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.HelperFailure = &networkresolverapproval.HelperFailure{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "unexpected",
				}
			},
			want: "inconsistent evidence",
		},
		{
			name: "invalid confirmation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.Confirmation.Operation.RequestedAt = time.Time{}
			},
			want: "validate network resolver setup confirmation",
		},
		{
			name: "crossed confirmation operation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.Confirmation.Operation.ID = "operation-crossed"
			},
			want: "selected operation revision",
		},
		{
			name: "crossed confirmation intent",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.Confirmation.Operation.IntentID = "intent-crossed"
			},
			want: "selected operation revision",
		},
		{
			name: "crossed confirmation phase",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.Confirmation.Operation.Phase = "committing resolver"
			},
			want: "selected operation revision",
		},
		{
			name: "nonadvancing network revision",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setup := setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.Confirmation.NetworkRevision = setup.Revision
				fixture.resolver.outcome.Confirmation.Revision = setup.Revision + 1
			},
			want: "selected operation revision",
		},
		{
			name: "noncontiguous operation revision",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupResolverApproval(t, fixture)
				fixture.resolver.outcome.Confirmation.Revision++
			},
			want: "immediately follow",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			test.mutate(t, fixture)

			err := fixture.command.Run(t.Context())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Run() error = %v, want %v", err, test.wantErr)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
			if fixture.connection.dataPlaneSetupCalls != 0 || fixture.output.Len() != 0 {
				t.Fatalf(
					"data-plane start calls = %d, output = %q, want neither",
					fixture.connection.dataPlaneSetupCalls,
					fixture.output.String(),
				)
			}
		})
	}
}

// TestFullSetupCommandRejectsTrustFailures proves trust must advance the exact selected operation to low-port approval.
func TestFullSetupCommandRejectsTrustFailures(t *testing.T) {
	startErr := errors.New("data-plane start failed")
	approvalErr := errors.New("trust approval failed")
	tests := []struct {
		name    string
		mutate  func(*testing.T, *fullSetupCommandFixture)
		want    string
		wantErr error
	}{
		{
			name: "start failure",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.dataPlaneSetupErr = startErr
			},
			wantErr: startErr,
		},
		{
			name: "invalid start response",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.dataPlaneSetup = control.NetworkDataPlaneSetupOperation{}
			},
			want: "validate response",
		},
		{
			name: "crossed start intent",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.dataPlaneSetup.Operation.IntentID = "intent-crossed"
			},
			want: "selected intent",
		},
		{
			name: "success in wrong phase",
			mutate: func(_ *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.dataPlaneSetup.Operation.Phase = "committing listeners"
			},
			want: "succeeded in unsupported phase",
		},
		{
			name: "nonapproval state",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.dataPlaneSetup = setupCommandDataPlaneOperation(
					t,
					domain.OperationRunning,
					"preparing trusted ingress",
					12,
				)
			},
			want: "is running",
		},
		{
			name: "unsupported approval phase",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				fixture.connection.dataPlaneSetup = setupCommandDataPlaneOperation(
					t,
					domain.OperationRequiresApproval,
					"awaiting another approval",
					12,
				)
			},
			want: "requires approval in unsupported phase",
		},
		{
			name: "approval failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trustErr = approvalErr
			},
			wantErr: approvalErr,
		},
		{
			name: "declined approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust = networkdataplaneapproval.TrustOutcome{
					State: networkdataplaneapproval.Declined,
				}
			},
			want: "was declined",
		},
		{
			name: "unavailable approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust = networkdataplaneapproval.TrustOutcome{
					State: networkdataplaneapproval.Unavailable,
				}
			},
			want: "is unavailable",
		},
		{
			name: "helper failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust = networkdataplaneapproval.TrustOutcome{
					State: networkdataplaneapproval.HelperFailed,
					HelperFailure: &networkdataplaneapproval.HelperFailure{
						Code:    helper.ErrorCodeMutationFailed,
						Message: "bounded trust failure",
					},
				}
			},
			want: "bounded trust failure",
		},
		{
			name: "indeterminate approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust = networkdataplaneapproval.TrustOutcome{
					State: networkdataplaneapproval.Indeterminate,
				}
			},
			want: "is indeterminate",
		},
		{
			name: "successful approval without setup",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust.Setup = nil
			},
			want: "inconsistent evidence",
		},
		{
			name: "successful approval with helper failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust.HelperFailure = &networkdataplaneapproval.HelperFailure{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "unexpected",
				}
			},
			want: "inconsistent evidence",
		},
		{
			name: "invalid trust confirmation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust.Setup.Operation.RequestedAt = time.Time{}
			},
			want: "validate network data-plane trust confirmation",
		},
		{
			name: "crossed trust operation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust.Setup.Operation.ID = "operation-crossed"
			},
			want: "selected operation revision",
		},
		{
			name: "crossed trust intent",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust.Setup.Operation.IntentID = "intent-crossed"
			},
			want: "selected operation revision",
		},
		{
			name: "nonadvancing trust revision",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setup, _ := setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust.Setup.Revision = setup.Revision
			},
			want: "selected operation revision",
		},
		{
			name: "trust remains at trust approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setup, _ := setFullSetupTrustApproval(t, fixture)
				fixture.dataPlane.trust.Setup = &setup
			},
			want: "selected operation revision",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			test.mutate(t, fixture)

			err := fixture.command.Run(t.Context())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Run() error = %v, want %v", err, test.wantErr)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
			if len(fixture.dataPlane.lowRequests) != 0 || fixture.output.Len() != 0 {
				t.Fatalf(
					"low-port requests = %#v, output = %q, want neither",
					fixture.dataPlane.lowRequests,
					fixture.output.String(),
				)
			}
		})
	}
}

// TestFullSetupCommandRejectsLowPortFailures proves final success requires an exact completed confirmation.
func TestFullSetupCommandRejectsLowPortFailures(t *testing.T) {
	approvalErr := errors.New("low-port approval failed")
	tests := []struct {
		name    string
		mutate  func(*testing.T, *fullSetupCommandFixture)
		want    string
		wantErr error
	}{
		{
			name: "approval failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPortErr = approvalErr
			},
			wantErr: approvalErr,
		},
		{
			name: "declined approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
					State: networkdataplaneapproval.Declined,
				}
			},
			want: "was declined",
		},
		{
			name: "unavailable approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
					State: networkdataplaneapproval.Unavailable,
				}
			},
			want: "is unavailable",
		},
		{
			name: "helper failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
					State: networkdataplaneapproval.HelperFailed,
					HelperFailure: &networkdataplaneapproval.HelperFailure{
						Code:    helper.ErrorCodeMutationFailed,
						Message: "bounded low-port failure",
					},
				}
			},
			want: "bounded low-port failure",
		},
		{
			name: "indeterminate approval",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
					State: networkdataplaneapproval.Indeterminate,
				}
			},
			want: "is indeterminate",
		},
		{
			name: "successful approval without confirmation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort.Confirmation = nil
			},
			want: "inconsistent evidence",
		},
		{
			name: "successful approval with helper failure",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort.HelperFailure = &networkdataplaneapproval.HelperFailure{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "unexpected",
				}
			},
			want: "inconsistent evidence",
		},
		{
			name: "invalid confirmation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort.Confirmation.Operation.RequestedAt = time.Time{}
			},
			want: "validate network data-plane low-port confirmation",
		},
		{
			name: "crossed confirmation operation",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort.Confirmation.Operation.ID = "operation-crossed"
			},
			want: "selected operation revision",
		},
		{
			name: "crossed confirmation intent",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort.Confirmation.Operation.IntentID = "intent-crossed"
			},
			want: "selected operation revision",
		},
		{
			name: "nonadvancing network revision",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setup := setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort.Confirmation.NetworkRevision = setup.Revision
				fixture.dataPlane.lowPort.Confirmation.Revision = setup.Revision + 1
			},
			want: "selected operation revision",
		},
		{
			name: "noncontiguous operation revision",
			mutate: func(t *testing.T, fixture *fullSetupCommandFixture) {
				setFullSetupLowPortApproval(t, fixture)
				fixture.dataPlane.lowPort.Confirmation.Revision++
			},
			want: "selected operation revision",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			test.mutate(t, fixture)

			err := fixture.command.Run(t.Context())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Run() error = %v, want %v", err, test.wantErr)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run() error = %v, want containing %q", err, test.want)
			}
			if fixture.output.Len() != 0 {
				t.Fatalf("output = %q, want empty", fixture.output.String())
			}
		})
	}
}

// setFullSetupResolverApproval configures one successful resolver approval boundary.
func setFullSetupResolverApproval(
	t *testing.T,
	fixture *fullSetupCommandFixture,
) control.NetworkResolverSetupOperation {
	t.Helper()

	setup := setupCommandResolverOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting resolver approval",
		8,
	)
	confirmation := setupCommandResolverConfirmation(t, setup)
	fixture.connection.resolverSetup = setup
	fixture.resolver.outcome = networkresolverapproval.Outcome{
		State:        networkresolverapproval.Succeeded,
		Confirmation: &confirmation,
	}
	return setup
}

// setFullSetupTrustApproval configures trust followed by one successful low-port approval.
func setFullSetupTrustApproval(
	t *testing.T,
	fixture *fullSetupCommandFixture,
) (control.NetworkDataPlaneSetupOperation, control.NetworkDataPlaneSetupOperation) {
	t.Helper()

	trustSetup := setupCommandDataPlaneOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting trust approval",
		12,
	)
	lowPortSetup := setupCommandDataPlaneOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting low-port approval",
		13,
	)
	confirmation := setupCommandDataPlaneConfirmation(t, lowPortSetup)
	fixture.connection.dataPlaneSetup = trustSetup
	fixture.dataPlane.trust = networkdataplaneapproval.TrustOutcome{
		State: networkdataplaneapproval.Succeeded,
		Setup: &lowPortSetup,
	}
	fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
		State:        networkdataplaneapproval.Succeeded,
		Confirmation: &confirmation,
	}
	return trustSetup, lowPortSetup
}

// setFullSetupLowPortApproval configures one directly replayed low-port approval boundary.
func setFullSetupLowPortApproval(
	t *testing.T,
	fixture *fullSetupCommandFixture,
) control.NetworkDataPlaneSetupOperation {
	t.Helper()

	setup := setupCommandDataPlaneOperation(
		t,
		domain.OperationRequiresApproval,
		"awaiting low-port approval",
		12,
	)
	confirmation := setupCommandDataPlaneConfirmation(t, setup)
	fixture.connection.dataPlaneSetup = setup
	fixture.dataPlane.lowPort = networkdataplaneapproval.LowPortOutcome{
		State:        networkdataplaneapproval.Succeeded,
		Confirmation: &confirmation,
	}
	return setup
}

// TestFullSetupCommandRetriesRecoverableTerminalOperations gives each setup phase a fresh durable identity.
func TestFullSetupCommandRetriesRecoverableTerminalOperations(t *testing.T) {
	states := []domain.OperationState{
		domain.OperationCancelled,
		domain.OperationFailed,
	}
	for _, phase := range []string{"pool", "resolver", "data-plane"} {
		for _, state := range states {
			t.Run(phase+"/"+string(state), func(t *testing.T) {
				fixture := newFullSetupCommandFixture(t)
				configureFullSetupRetry(t, fixture, phase, state, nil, false)
				retryIntent := domain.IntentID(fullSetupPhasePrefix(phase) + "-retry")
				fixture.command.newRetryIntent = func(prefix domain.IntentID) (domain.IntentID, error) {
					if prefix != fullSetupPhasePrefix(phase) {
						t.Fatalf("retry prefix = %q", prefix)
					}
					return retryIntent, nil
				}

				if err := fixture.command.Run(t.Context()); err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if got, want := fullSetupPhaseRequestIntents(fixture, phase), []domain.IntentID{
					fullSetupPhasePrefix(phase),
					retryIntent,
				}; !reflect.DeepEqual(got, want) {
					t.Fatalf("start intents = %#v, want %#v", got, want)
				}
			})
		}
	}
}

// TestFullSetupCommandDoesNotRetryOpaqueStartFailures preserves the stable intent after an unclassified daemon failure.
func TestFullSetupCommandDoesNotRetryOpaqueStartFailures(t *testing.T) {
	for _, phase := range []string{"pool", "resolver", "data-plane"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			startErr := rpc.NewWireError(rpc.ErrorCodeInternal)
			switch phase {
			case "pool":
				fixture.connection.networkSetupErr = startErr
			case "resolver":
				fixture.connection.resolverSetupErr = startErr
			case "data-plane":
				fixture.connection.dataPlaneSetupErr = startErr
			default:
				t.Fatalf("unsupported full setup phase %q", phase)
			}
			fixture.command.newRetryIntent = func(domain.IntentID) (domain.IntentID, error) {
				t.Fatal("opaque start failure created a retry identity")
				return "", nil
			}

			var got rpc.WireError
			if err := fixture.command.Run(t.Context()); !errors.As(err, &got) || got.Code != rpc.ErrorCodeInternal {
				t.Fatalf("Run() error = %v, want internal wire error", err)
			}
			if intents := fullSetupPhaseRequestIntents(fixture, phase); len(intents) != 1 {
				t.Fatalf("start intents = %#v, want one", intents)
			}
		})
	}
}

// TestFullSetupCommandValidatesTerminalResponsesBeforeRetry prevents malformed state from causing a second mutation.
func TestFullSetupCommandValidatesTerminalResponsesBeforeRetry(t *testing.T) {
	for _, phase := range []string{"pool", "resolver", "data-plane"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			setMalformedFullSetupTerminal(t, fixture, phase)
			fixture.command.newRetryIntent = func(domain.IntentID) (domain.IntentID, error) {
				t.Fatal("retry identity created from an invalid response")
				return "", nil
			}

			err := fixture.command.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), "validate response") {
				t.Fatalf("Run() error = %v, want response validation", err)
			}
			if got := fullSetupPhaseRequestIntents(fixture, phase); len(got) != 1 {
				t.Fatalf("start intents = %#v, want one", got)
			}
		})
	}
}

// TestFullSetupCommandSurfacesRetryFailures keeps every retry boundary fail-closed and phase-specific.
func TestFullSetupCommandSurfacesRetryFailures(t *testing.T) {
	for _, phase := range []string{"pool", "resolver", "data-plane"} {
		t.Run(phase+"/identity", func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			configureFullSetupRetry(t, fixture, phase, domain.OperationCancelled, nil, false)
			want := errors.New("entropy unavailable")
			fixture.command.newRetryIntent = func(domain.IntentID) (domain.IntentID, error) {
				return "", want
			}

			if err := fixture.command.Run(t.Context()); !errors.Is(err, want) {
				t.Fatalf("Run() error = %v, want %v", err, want)
			}
			if got := fullSetupPhaseRequestIntents(fixture, phase); len(got) != 1 {
				t.Fatalf("start intents = %#v, want one", got)
			}
		})

		t.Run(phase+"/second start", func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			want := errors.New("retry start failed")
			configureFullSetupRetry(t, fixture, phase, domain.OperationCancelled, want, false)
			fixture.command.newRetryIntent = func(prefix domain.IntentID) (domain.IntentID, error) {
				return domain.IntentID(string(prefix) + "-retry"), nil
			}

			if err := fixture.command.Run(t.Context()); !errors.Is(err, want) {
				t.Fatalf("Run() error = %v, want %v", err, want)
			}
			if got := fullSetupPhaseRequestIntents(fixture, phase); len(got) != 2 {
				t.Fatalf("start intents = %#v, want two", got)
			}
		})

		t.Run(phase+"/crossed response", func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			configureFullSetupRetry(t, fixture, phase, domain.OperationCancelled, nil, true)
			fixture.command.newRetryIntent = func(prefix domain.IntentID) (domain.IntentID, error) {
				return domain.IntentID(string(prefix) + "-retry"), nil
			}

			err := fixture.command.Run(t.Context())
			if err == nil || !strings.Contains(err.Error(), "intent") {
				t.Fatalf("Run() error = %v, want crossed intent", err)
			}
		})
	}
}

// TestFullSetupCommandDoesNotRetryNonrecoverableFailures preserves the daemon's terminal decision.
func TestFullSetupCommandDoesNotRetryNonrecoverableFailures(t *testing.T) {
	for _, phase := range []string{"pool", "resolver", "data-plane"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newFullSetupCommandFixture(t)
			setFullSetupNonretryableFailure(t, fixture, phase)
			fixture.command.newRetryIntent = func(domain.IntentID) (domain.IntentID, error) {
				t.Fatal("nonretryable operation created a retry identity")
				return "", nil
			}

			if err := fixture.command.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "failed") {
				t.Fatalf("Run() error = %v, want terminal failure", err)
			}
			if got := fullSetupPhaseRequestIntents(fixture, phase); len(got) != 1 {
				t.Fatalf("start intents = %#v, want one", got)
			}
		})
	}
}

// fullSetupPhasePrefix returns the stable intent prefix for one test phase.
func fullSetupPhasePrefix(phase string) domain.IntentID {
	switch phase {
	case "pool":
		return networkSetupIntentID
	case "resolver":
		return networkResolverSetupIntentID
	case "data-plane":
		return networkDataPlaneSetupIntentID
	default:
		panic("unsupported full setup phase " + phase)
	}
}

// fullSetupPhaseRequestIntents returns every start intent observed for one test phase.
func fullSetupPhaseRequestIntents(fixture *fullSetupCommandFixture, phase string) []domain.IntentID {
	var intents []domain.IntentID
	switch phase {
	case "pool":
		for _, request := range fixture.connection.networkSetupRequests {
			intents = append(intents, request.IntentID)
		}
	case "resolver":
		for _, request := range fixture.connection.resolverSetupRequests {
			intents = append(intents, request.IntentID)
		}
	case "data-plane":
		for _, request := range fixture.connection.dataPlaneSetupRequests {
			intents = append(intents, request.IntentID)
		}
	default:
		panic("unsupported full setup phase " + phase)
	}
	return intents
}

// configureFullSetupRetry makes one phase return a recoverable first result and a configurable retry result.
func configureFullSetupRetry(
	t *testing.T,
	fixture *fullSetupCommandFixture,
	phase string,
	state domain.OperationState,
	retryErr error,
	wrongIntent bool,
) {
	t.Helper()

	switch phase {
	case "pool":
		terminal := setupCommandTestOperation(t, state, 4)
		fixture.connection.networkSetupHook = func(
			_ context.Context,
			request control.StartNetworkSetupRequest,
		) (control.NetworkSetupOperation, error) {
			if len(fixture.connection.networkSetupRequests) == 1 {
				return terminal, nil
			}
			if retryErr != nil {
				return control.NetworkSetupOperation{}, retryErr
			}
			result := setupCommandTestOperation(t, domain.OperationSucceeded, 7)
			result.Operation.IntentID = request.IntentID
			if wrongIntent {
				result.Operation.IntentID = "intent-crossed"
			}
			return result, nil
		}
	case "resolver":
		terminal := setupCommandResolverOperation(t, state, string(state), 8)
		fixture.connection.resolverSetupHook = func(
			_ context.Context,
			request control.StartNetworkResolverSetupRequest,
		) (control.NetworkResolverSetupOperation, error) {
			if len(fixture.connection.resolverSetupRequests) == 1 {
				return terminal, nil
			}
			if retryErr != nil {
				return control.NetworkResolverSetupOperation{}, retryErr
			}
			result := setupCommandResolverOperation(t, domain.OperationSucceeded, "completed", 11)
			result.Operation.IntentID = request.IntentID
			if wrongIntent {
				result.Operation.IntentID = "intent-crossed"
			}
			return result, nil
		}
	case "data-plane":
		terminal := setupCommandDataPlaneOperation(t, state, string(state), 12)
		fixture.connection.dataPlaneSetupHook = func(
			_ context.Context,
			request control.StartNetworkDataPlaneSetupRequest,
		) (control.NetworkDataPlaneSetupOperation, error) {
			if len(fixture.connection.dataPlaneSetupRequests) == 1 {
				return terminal, nil
			}
			if retryErr != nil {
				return control.NetworkDataPlaneSetupOperation{}, retryErr
			}
			result := setupCommandDataPlaneOperation(t, domain.OperationSucceeded, "completed", 15)
			result.Operation.IntentID = request.IntentID
			if wrongIntent {
				result.Operation.IntentID = "intent-crossed"
			}
			return result, nil
		}
	default:
		t.Fatalf("unsupported full setup phase %q", phase)
	}
}

// setMalformedFullSetupTerminal installs one invalid retryable terminal snapshot for a phase.
func setMalformedFullSetupTerminal(t *testing.T, fixture *fullSetupCommandFixture, phase string) {
	t.Helper()

	switch phase {
	case "pool":
		setup := setupCommandTestOperation(t, domain.OperationFailed, 4)
		setup.Operation.RequestedAt = time.Time{}
		fixture.connection.networkSetup = setup
	case "resolver":
		setup := setupCommandResolverOperation(t, domain.OperationFailed, "failed", 8)
		setup.Operation.RequestedAt = time.Time{}
		fixture.connection.resolverSetup = setup
	case "data-plane":
		setup := setupCommandDataPlaneOperation(t, domain.OperationFailed, "failed", 12)
		setup.Operation.RequestedAt = time.Time{}
		fixture.connection.dataPlaneSetup = setup
	default:
		t.Fatalf("unsupported full setup phase %q", phase)
	}
}

// setFullSetupNonretryableFailure installs one valid terminal operation that must not mint another intent.
func setFullSetupNonretryableFailure(t *testing.T, fixture *fullSetupCommandFixture, phase string) {
	t.Helper()

	switch phase {
	case "pool":
		setup := setupCommandTestOperation(t, domain.OperationFailed, 4)
		setup.Operation.Problem.Retryable = false
		fixture.connection.networkSetup = setup
	case "resolver":
		setup := setupCommandResolverOperation(t, domain.OperationFailed, "failed", 8)
		setup.Operation.Problem.Retryable = false
		fixture.connection.resolverSetup = setup
	case "data-plane":
		setup := setupCommandDataPlaneOperation(t, domain.OperationFailed, "failed", 12)
		setup.Operation.Problem.Retryable = false
		fixture.connection.dataPlaneSetup = setup
	default:
		t.Fatalf("unsupported full setup phase %q", phase)
	}
}
