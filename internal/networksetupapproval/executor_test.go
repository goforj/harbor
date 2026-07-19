package networksetupapproval

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
)

const (
	testOperationID domain.OperationID = "operation-network-setup"
	testPool                           = "127.0.0.0/29"
	testRevision    domain.Sequence    = 7
)

var errTestFailure = errors.New("test failure")

// TestExecuteConfirmsExactClonedEvidence verifies the complete one-effect success path and its alias boundary.
func TestExecuteConfirmsExactClonedEvidence(t *testing.T) {
	t.Parallel()

	request := validRequest()
	preparation := validPreparation(t)
	evidence := validPoolEvidence(testPool)
	wantEvidence := clonePoolEvidence(evidence)
	confirmation := validConfirmation(t, testOperationID, testPool, testRevision)
	wantTicket, err := launcher.NewPoolLaunchTicket(
		preparation.Ticket.OperationID,
		preparation.Ticket.Reference,
		preparation.Ticket.Operation,
		preparation.Ticket.Pool,
		preparation.Ticket.ExpiresAt,
	)
	if err != nil {
		t.Fatalf("construct expected launch ticket: %v", err)
	}

	client := &fakeClient{preparation: preparation, confirmation: confirmation}
	client.confirm = func(ctx context.Context, got control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error) {
		if ctx == nil {
			t.Fatal("confirm context is nil")
		}
		want := control.ConfirmNetworkSetupApprovalRequest{
			OperationID:               request.OperationID,
			ExpectedOperationRevision: request.ExpectedOperationRevision,
			PoolEvidence:              wantEvidence,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("confirmation request = %#v, want %#v", got, want)
		}
		got.PoolEvidence.Identities[0].Address = "127.0.0.99"
		return confirmation, nil
	}
	helpLauncher := &fakeLauncher{outcome: successfulLaunch(evidence)}
	helpLauncher.invoke = func(ctx context.Context, got launcher.PoolLaunchTicket) (launcher.Outcome, error) {
		if ctx == nil {
			t.Fatal("launch context is nil")
		}
		if !reflect.DeepEqual(got, wantTicket) {
			t.Fatalf("launch ticket = %#v, want %#v", got, wantTicket)
		}
		return helpLauncher.outcome, nil
	}

	outcome, err := New(client, helpLauncher).Execute(nil, request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome.State != Succeeded || outcome.Confirmation == nil || !reflect.DeepEqual(*outcome.Confirmation, confirmation) {
		t.Fatalf("Execute() outcome = %#v, want succeeded confirmation %#v", outcome, confirmation)
	}
	if outcome.HelperFailure != nil {
		t.Fatalf("Execute() helper failure = %#v, want nil", outcome.HelperFailure)
	}
	if !reflect.DeepEqual(evidence, wantEvidence) {
		t.Fatalf("launcher evidence was aliased into confirmation: got %#v, want %#v", evidence, wantEvidence)
	}
	if client.prepareCalls != 1 || helpLauncher.calls != 1 || client.confirmCalls != 1 {
		t.Fatalf("call counts = prepare %d, launch %d, confirm %d; want 1, 1, 1", client.prepareCalls, helpLauncher.calls, client.confirmCalls)
	}
	wantPrepare := control.PrepareNetworkSetupApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	}
	if !reflect.DeepEqual(client.prepareRequests[0], wantPrepare) {
		t.Fatalf("preparation request = %#v, want %#v", client.prepareRequests[0], wantPrepare)
	}
}

// TestExecuteMapsTerminalLauncherStates verifies every non-success launcher conclusion without confirmation.
func TestExecuteMapsTerminalLauncherStates(t *testing.T) {
	t.Parallel()

	failure := helper.ResponseError{Code: helper.ErrorCodeMutationFailed, Message: "mutation failed safely"}
	tests := []struct {
		name        string
		launch      launcher.Outcome
		wantState   State
		wantFailure *HelperFailure
	}{
		{name: "declined", launch: launcher.Outcome{State: launcher.Declined}, wantState: Declined},
		{name: "unavailable", launch: launcher.Outcome{State: launcher.Unavailable}, wantState: Unavailable},
		{
			name: "helper failed",
			launch: launcher.Outcome{
				State: launcher.HelperFailed,
				Response: helper.Response{
					Version: helper.ProtocolVersion,
					Error:   &failure,
				},
				Exit: &launcher.ProcessExit{Code: launcher.ExitCodeHelperFailed},
			},
			wantState:   HelperFailed,
			wantFailure: &HelperFailure{Code: failure.Code, Message: failure.Message},
		},
		{name: "indeterminate", launch: launcher.Outcome{State: launcher.Indeterminate, Exit: &launcher.ProcessExit{Code: 73}}, wantState: Indeterminate},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeClient{preparation: validPreparation(t)}
			helpLauncher := &fakeLauncher{outcome: test.launch}
			outcome, err := New(client, helpLauncher).Execute(context.Background(), validRequest())
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if outcome.State != test.wantState || !reflect.DeepEqual(outcome.HelperFailure, test.wantFailure) {
				t.Fatalf("Execute() outcome = %#v, want state %q and failure %#v", outcome, test.wantState, test.wantFailure)
			}
			if outcome.Confirmation != nil {
				t.Fatalf("Execute() confirmation = %#v, want nil", outcome.Confirmation)
			}
			if client.prepareCalls != 1 || helpLauncher.calls != 1 || client.confirmCalls != 0 {
				t.Fatalf("call counts = prepare %d, launch %d, confirm %d; want 1, 1, 0", client.prepareCalls, helpLauncher.calls, client.confirmCalls)
			}
		})
	}
}

// TestExecuteFailureBoundaries verifies that uncertainty begins only after a helper launch is attempted.
func TestExecuteFailureBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prepareErr  error
		launchErr   error
		confirmErr  error
		wantState   State
		wantPrepare int
		wantLaunch  int
		wantConfirm int
	}{
		{name: "prepare", prepareErr: errTestFailure, wantPrepare: 1},
		{name: "launch", launchErr: errTestFailure, wantState: Indeterminate, wantPrepare: 1, wantLaunch: 1},
		{name: "confirm", confirmErr: errTestFailure, wantState: Indeterminate, wantPrepare: 1, wantLaunch: 1, wantConfirm: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeClient{
				preparation:  validPreparation(t),
				prepareErr:   test.prepareErr,
				confirmation: validConfirmation(t, testOperationID, testPool, testRevision),
				confirmErr:   test.confirmErr,
			}
			helpLauncher := &fakeLauncher{outcome: successfulLaunch(validPoolEvidence(testPool)), err: test.launchErr}
			outcome, err := New(client, helpLauncher).Execute(context.Background(), validRequest())
			if !errors.Is(err, errTestFailure) {
				t.Fatalf("Execute() error = %v, want %v", err, errTestFailure)
			}
			if outcome.State != test.wantState || outcome.Confirmation != nil || outcome.HelperFailure != nil {
				t.Fatalf("Execute() outcome = %#v, want state %q without details", outcome, test.wantState)
			}
			if client.prepareCalls != test.wantPrepare || helpLauncher.calls != test.wantLaunch || client.confirmCalls != test.wantConfirm {
				t.Fatalf(
					"call counts = prepare %d, launch %d, confirm %d; want %d, %d, %d",
					client.prepareCalls,
					helpLauncher.calls,
					client.confirmCalls,
					test.wantPrepare,
					test.wantLaunch,
					test.wantConfirm,
				)
			}
		})
	}
}

// TestExecuteRejectsMismatchedResponses verifies correlation before and after the only helper launch.
func TestExecuteRejectsMismatchedResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		mutatePreparation  func(*control.NetworkSetupApprovalPreparation)
		mutateLaunch       func(*launcher.Outcome)
		mutateConfirmation func(*control.NetworkSetupApprovalConfirmation)
		wantState          State
		wantLaunch         int
		wantConfirm        int
	}{
		{
			name: "preparation revision",
			mutatePreparation: func(preparation *control.NetworkSetupApprovalPreparation) {
				preparation.OperationRevision++
			},
		},
		{
			name: "launcher pool",
			mutateLaunch: func(outcome *launcher.Outcome) {
				evidence := validPoolEvidence("127.0.0.8/29")
				outcome.Response.Result.PoolEvidence = &evidence
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "launcher lifecycle evidence",
			mutateLaunch: func(outcome *launcher.Outcome) {
				outcome.Exit.Code = launcher.ExitCodeHelperFailed
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "confirmation operation",
			mutateConfirmation: func(confirmation *control.NetworkSetupApprovalConfirmation) {
				*confirmation = validConfirmation(t, "operation-other-network-setup", testPool, testRevision)
			},
			wantState:   Indeterminate,
			wantLaunch:  1,
			wantConfirm: 1,
		},
		{
			name: "confirmation pool",
			mutateConfirmation: func(confirmation *control.NetworkSetupApprovalConfirmation) {
				confirmation.Pool = "127.0.0.8/29"
			},
			wantState:   Indeterminate,
			wantLaunch:  1,
			wantConfirm: 1,
		},
		{
			name: "confirmation lifecycle revision",
			mutateConfirmation: func(confirmation *control.NetworkSetupApprovalConfirmation) {
				confirmation.NetworkRevision--
				confirmation.Revision--
			},
			wantState:   Indeterminate,
			wantLaunch:  1,
			wantConfirm: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			preparation := validPreparation(t)
			if test.mutatePreparation != nil {
				test.mutatePreparation(&preparation)
			}
			launch := successfulLaunch(validPoolEvidence(testPool))
			if test.mutateLaunch != nil {
				test.mutateLaunch(&launch)
			}
			confirmation := validConfirmation(t, testOperationID, testPool, testRevision)
			if test.mutateConfirmation != nil {
				test.mutateConfirmation(&confirmation)
			}

			client := &fakeClient{preparation: preparation, confirmation: confirmation}
			helpLauncher := &fakeLauncher{outcome: launch}
			outcome, err := New(client, helpLauncher).Execute(context.Background(), validRequest())
			if !errors.Is(err, ErrInconsistentResponse) {
				t.Fatalf("Execute() error = %v, want ErrInconsistentResponse", err)
			}
			if outcome.State != test.wantState || outcome.Confirmation != nil || outcome.HelperFailure != nil {
				t.Fatalf("Execute() outcome = %#v, want state %q without details", outcome, test.wantState)
			}
			if helpLauncher.calls != test.wantLaunch || client.confirmCalls != test.wantConfirm {
				t.Fatalf("call counts = launch %d, confirm %d; want %d, %d", helpLauncher.calls, client.confirmCalls, test.wantLaunch, test.wantConfirm)
			}
		})
	}
}

// TestExecuteHonorsCancellationBoundaries verifies cancellation before preparation, before launch, and after launch.
func TestExecuteHonorsCancellationBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("before preparation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &fakeClient{}
		helpLauncher := &fakeLauncher{}
		outcome, err := New(client, helpLauncher).Execute(ctx, validRequest())
		if !errors.Is(err, context.Canceled) || outcome != (Outcome{}) {
			t.Fatalf("Execute() = (%#v, %v), want zero outcome and context.Canceled", outcome, err)
		}
		if client.prepareCalls != 0 || helpLauncher.calls != 0 || client.confirmCalls != 0 {
			t.Fatalf("call counts = prepare %d, launch %d, confirm %d; want 0, 0, 0", client.prepareCalls, helpLauncher.calls, client.confirmCalls)
		}
	})

	t.Run("before launch", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		client := &fakeClient{preparation: validPreparation(t)}
		client.prepare = func(context.Context, control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error) {
			cancel()
			return client.preparation, nil
		}
		helpLauncher := &fakeLauncher{}
		outcome, err := New(client, helpLauncher).Execute(ctx, validRequest())
		if !errors.Is(err, context.Canceled) || outcome != (Outcome{}) {
			t.Fatalf("Execute() = (%#v, %v), want zero outcome and context.Canceled", outcome, err)
		}
		if client.prepareCalls != 1 || helpLauncher.calls != 0 || client.confirmCalls != 0 {
			t.Fatalf("call counts = prepare %d, launch %d, confirm %d; want 1, 0, 0", client.prepareCalls, helpLauncher.calls, client.confirmCalls)
		}
	})

	t.Run("after launch", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		client := &fakeClient{preparation: validPreparation(t)}
		helpLauncher := &fakeLauncher{outcome: successfulLaunch(validPoolEvidence(testPool))}
		helpLauncher.invoke = func(context.Context, launcher.PoolLaunchTicket) (launcher.Outcome, error) {
			cancel()
			return helpLauncher.outcome, nil
		}
		outcome, err := New(client, helpLauncher).Execute(ctx, validRequest())
		if !errors.Is(err, context.Canceled) || outcome.State != Indeterminate {
			t.Fatalf("Execute() = (%#v, %v), want indeterminate and context.Canceled", outcome, err)
		}
		if client.prepareCalls != 1 || helpLauncher.calls != 1 || client.confirmCalls != 0 {
			t.Fatalf("call counts = prepare %d, launch %d, confirm %d; want 1, 1, 0", client.prepareCalls, helpLauncher.calls, client.confirmCalls)
		}
	})
}

// TestExecuteRejectsInvalidRequest verifies request validation occurs before daemon interaction.
func TestExecuteRejectsInvalidRequest(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	helpLauncher := &fakeLauncher{}
	outcome, err := New(client, helpLauncher).Execute(context.Background(), Request{})
	if err == nil || outcome != (Outcome{}) {
		t.Fatalf("Execute() = (%#v, %v), want zero outcome and validation error", outcome, err)
	}
	if client.prepareCalls != 0 || helpLauncher.calls != 0 || client.confirmCalls != 0 {
		t.Fatalf("call counts = prepare %d, launch %d, confirm %d; want 0, 0, 0", client.prepareCalls, helpLauncher.calls, client.confirmCalls)
	}
}

// TestNewRequiresDependencies verifies nil and typed-nil dependencies fail before workflow execution.
func TestNewRequiresDependencies(t *testing.T) {
	t.Parallel()

	var typedNilClient *fakeClient
	var typedNilLauncher *fakeLauncher
	tests := []struct {
		name     string
		client   Client
		launcher HelperLauncher
	}{
		{name: "nil client", launcher: &fakeLauncher{}},
		{name: "typed nil client", client: typedNilClient, launcher: &fakeLauncher{}},
		{name: "nil launcher", client: &fakeClient{}},
		{name: "typed nil launcher", client: &fakeClient{}, launcher: typedNilLauncher},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Fatal("New() did not panic")
				}
			}()
			_ = New(test.client, test.launcher)
		})
	}
}

// fakeClient records daemon calls and supplies bounded configured responses.
type fakeClient struct {
	preparation     control.NetworkSetupApprovalPreparation
	prepareErr      error
	confirmation    control.NetworkSetupApprovalConfirmation
	confirmErr      error
	prepare         func(context.Context, control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error)
	confirm         func(context.Context, control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error)
	prepareCalls    int
	confirmCalls    int
	prepareRequests []control.PrepareNetworkSetupApprovalRequest
	confirmRequests []control.ConfirmNetworkSetupApprovalRequest
}

// PrepareNetworkSetupApproval records and returns the configured preparation response.
func (client *fakeClient) PrepareNetworkSetupApproval(ctx context.Context, request control.PrepareNetworkSetupApprovalRequest) (control.NetworkSetupApprovalPreparation, error) {
	client.prepareCalls++
	client.prepareRequests = append(client.prepareRequests, request)
	if client.prepare != nil {
		return client.prepare(ctx, request)
	}
	return client.preparation, client.prepareErr
}

// ConfirmNetworkSetupApproval records and returns the configured confirmation response.
func (client *fakeClient) ConfirmNetworkSetupApproval(ctx context.Context, request control.ConfirmNetworkSetupApprovalRequest) (control.NetworkSetupApprovalConfirmation, error) {
	client.confirmCalls++
	client.confirmRequests = append(client.confirmRequests, request)
	if client.confirm != nil {
		return client.confirm(ctx, request)
	}
	return client.confirmation, client.confirmErr
}

// fakeLauncher records aggregate helper launches and supplies one configured outcome.
type fakeLauncher struct {
	outcome launcher.Outcome
	err     error
	invoke  func(context.Context, launcher.PoolLaunchTicket) (launcher.Outcome, error)
	calls   int
	tickets []launcher.PoolLaunchTicket
}

// InvokePool records and returns the configured aggregate helper outcome.
func (helpLauncher *fakeLauncher) InvokePool(ctx context.Context, ticket launcher.PoolLaunchTicket) (launcher.Outcome, error) {
	helpLauncher.calls++
	helpLauncher.tickets = append(helpLauncher.tickets, ticket)
	if helpLauncher.invoke != nil {
		return helpLauncher.invoke(ctx, ticket)
	}
	return helpLauncher.outcome, helpLauncher.err
}

// validRequest returns the exact network setup approval selection shared by the fixtures.
func validRequest() Request {
	return Request{OperationID: testOperationID, ExpectedOperationRevision: testRevision}
}

// validPreparation returns one validated aggregate approval capability.
func validPreparation(t *testing.T) control.NetworkSetupApprovalPreparation {
	t.Helper()

	preparation := control.NetworkSetupApprovalPreparation{
		OperationID:       testOperationID,
		OperationRevision: testRevision,
		Ticket: control.NetworkSetupApprovalTicket{
			OperationID: testOperationID,
			Reference:   helper.TicketReference(strings.Repeat("b", 64)),
			Operation:   helper.OperationEnsureLoopbackPool,
			Pool:        testPool,
			ExpiresAt:   time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
	}
	if err := preparation.Validate(); err != nil {
		t.Fatalf("valid preparation fixture: %v", err)
	}
	return preparation
}

// validPoolEvidence returns complete canonical owned evidence for one loopback pool.
func validPoolEvidence(pool string) helper.PoolMutationEvidence {
	prefix := netip.MustParsePrefix(pool)
	address := prefix.Addr()
	identities := make([]helper.MutationEvidence, 0, 8)
	for index := 0; index < 8; index++ {
		identities = append(identities, helper.MutationEvidence{
			Changed: index%2 == 0,
			Address: address.String(),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationOwned,
				Fingerprint: strings.Repeat("a", 64),
			},
		})
		address = address.Next()
	}
	return helper.PoolMutationEvidence{Pool: pool, Identities: identities}
}

// successfulLaunch returns one correlated successful aggregate helper exchange.
func successfulLaunch(evidence helper.PoolMutationEvidence) launcher.Outcome {
	return launcher.Outcome{
		State: launcher.Succeeded,
		Response: helper.Response{
			Version: helper.ProtocolVersion,
			OK:      true,
			Result: &helper.OperationResult{
				Operation:    helper.OperationEnsureLoopbackPool,
				PoolEvidence: &evidence,
			},
		},
		Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded},
	}
}

// validConfirmation returns one validated success at the fixed approval lifecycle revision offset.
func validConfirmation(
	t *testing.T,
	operationID domain.OperationID,
	pool string,
	approvalRevision domain.Sequence,
) control.NetworkSetupApprovalConfirmation {
	t.Helper()

	requestedAt := time.Date(2029, time.December, 1, 2, 3, 4, 0, time.UTC)
	operation, err := domain.NewOperation(
		operationID,
		"intent-network-setup",
		domain.OperationKindNetworkSetup,
		"",
		requestedAt,
	)
	if err != nil {
		t.Fatalf("construct network setup operation: %v", err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "ensure-loopback-pool", requestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatalf("start network setup operation: %v", err)
	}
	operation, err = operation.Transition(domain.OperationSucceeded, "network-setup-complete", requestedAt.Add(2*time.Second), nil)
	if err != nil {
		t.Fatalf("complete network setup operation: %v", err)
	}
	confirmation := control.NetworkSetupApprovalConfirmation{
		Operation:       operation,
		Revision:        approvalRevision + 3,
		NetworkRevision: approvalRevision + 2,
		Pool:            pool,
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("valid confirmation fixture: %v", err)
	}
	return confirmation
}
