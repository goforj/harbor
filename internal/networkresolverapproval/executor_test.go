package networkresolverapproval

import (
	"context"
	"errors"
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
	testResolverOperationID            domain.OperationID = "operation-network-resolver-setup"
	testResolverRevision               domain.Sequence    = 7
	testResolverPolicyFingerprint                         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testResolverOwnershipFingerprint                      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testResolverObservationFingerprint                    = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var errResolverTestFailure = errors.New("test failure")

// TestExecuteConfirmsExactResolverEvidence verifies the complete one-effect success path.
func TestExecuteConfirmsExactResolverEvidence(t *testing.T) {
	t.Parallel()

	request := validRequest()
	preparation := validPreparation(t)
	evidence := validResolverEvidence()
	confirmation := validConfirmation(t, testResolverOperationID, testResolverRevision)
	wantTicket, err := launcher.NewResolverLaunchTicket(
		preparation.Ticket.OperationID,
		preparation.Ticket.Reference,
		preparation.Ticket.Operation,
		preparation.Ticket.PolicyFingerprint,
		preparation.Ticket.TargetOwnershipFingerprint,
		preparation.Ticket.ExpiresAt,
	)
	if err != nil {
		t.Fatalf("construct expected launch ticket: %v", err)
	}

	client := &fakeClient{preparation: preparation, confirmation: confirmation}
	client.confirm = func(
		ctx context.Context,
		got control.ConfirmNetworkResolverSetupApprovalRequest,
	) (control.NetworkResolverSetupApprovalConfirmation, error) {
		if ctx == nil {
			t.Fatal("confirm context is nil")
		}
		want := control.ConfirmNetworkResolverSetupApprovalRequest{
			OperationID:               request.OperationID,
			ExpectedOperationRevision: request.ExpectedOperationRevision,
			ResolverEvidence:          evidence,
		}
		if got != want {
			t.Fatalf("confirmation request = %#v, want %#v", got, want)
		}
		return confirmation, nil
	}
	helperLauncher := &fakeLauncher{outcome: successfulLaunch(evidence)}
	helperLauncher.invoke = func(ctx context.Context, got launcher.ResolverLaunchTicket) (launcher.Outcome, error) {
		if ctx == nil {
			t.Fatal("launch context is nil")
		}
		if !reflect.DeepEqual(got, wantTicket) {
			t.Fatalf("launch ticket = %#v, want %#v", got, wantTicket)
		}
		return helperLauncher.outcome, nil
	}

	outcome, err := New(client, helperLauncher).Execute(nil, request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome.State != Succeeded || outcome.Confirmation == nil || !reflect.DeepEqual(*outcome.Confirmation, confirmation) {
		t.Fatalf("Execute() outcome = %#v, want succeeded confirmation %#v", outcome, confirmation)
	}
	if outcome.HelperFailure != nil {
		t.Fatalf("Execute() helper failure = %#v, want nil", outcome.HelperFailure)
	}
	if client.prepareCalls != 1 || helperLauncher.calls != 1 || client.confirmCalls != 1 {
		t.Fatalf(
			"call counts = prepare %d, launch %d, confirm %d; want 1, 1, 1",
			client.prepareCalls,
			helperLauncher.calls,
			client.confirmCalls,
		)
	}
	wantPrepare := control.PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	}
	if client.prepareRequests[0] != wantPrepare {
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
		{
			name:      "indeterminate",
			launch:    launcher.Outcome{State: launcher.Indeterminate, Exit: &launcher.ProcessExit{Code: 73}},
			wantState: Indeterminate,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeClient{preparation: validPreparation(t)}
			helperLauncher := &fakeLauncher{outcome: test.launch}
			outcome, err := New(client, helperLauncher).Execute(context.Background(), validRequest())
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if outcome.State != test.wantState || !reflect.DeepEqual(outcome.HelperFailure, test.wantFailure) {
				t.Fatalf("Execute() outcome = %#v, want state %q and failure %#v", outcome, test.wantState, test.wantFailure)
			}
			if outcome.Confirmation != nil {
				t.Fatalf("Execute() confirmation = %#v, want nil", outcome.Confirmation)
			}
			if client.prepareCalls != 1 || helperLauncher.calls != 1 || client.confirmCalls != 0 {
				t.Fatalf(
					"call counts = prepare %d, launch %d, confirm %d; want 1, 1, 0",
					client.prepareCalls,
					helperLauncher.calls,
					client.confirmCalls,
				)
			}
		})
	}
}

// TestExecuteFailureBoundaries verifies uncertainty begins only after a helper launch is attempted.
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
		{name: "prepare", prepareErr: errResolverTestFailure, wantPrepare: 1},
		{
			name:        "launch",
			launchErr:   errResolverTestFailure,
			wantState:   Indeterminate,
			wantPrepare: 1,
			wantLaunch:  1,
		},
		{
			name:        "confirm",
			confirmErr:  errResolverTestFailure,
			wantState:   Indeterminate,
			wantPrepare: 1,
			wantLaunch:  1,
			wantConfirm: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeClient{
				preparation:  validPreparation(t),
				prepareErr:   test.prepareErr,
				confirmation: validConfirmation(t, testResolverOperationID, testResolverRevision),
				confirmErr:   test.confirmErr,
			}
			helperLauncher := &fakeLauncher{outcome: successfulLaunch(validResolverEvidence()), err: test.launchErr}
			outcome, err := New(client, helperLauncher).Execute(context.Background(), validRequest())
			if !errors.Is(err, errResolverTestFailure) {
				t.Fatalf("Execute() error = %v, want %v", err, errResolverTestFailure)
			}
			if outcome.State != test.wantState || outcome.Confirmation != nil || outcome.HelperFailure != nil {
				t.Fatalf("Execute() outcome = %#v, want state %q without details", outcome, test.wantState)
			}
			if client.prepareCalls != test.wantPrepare ||
				helperLauncher.calls != test.wantLaunch ||
				client.confirmCalls != test.wantConfirm {
				t.Fatalf(
					"call counts = prepare %d, launch %d, confirm %d; want %d, %d, %d",
					client.prepareCalls,
					helperLauncher.calls,
					client.confirmCalls,
					test.wantPrepare,
					test.wantLaunch,
					test.wantConfirm,
				)
			}
		})
	}
}

// TestExecuteRejectsMismatchedResponses verifies strict correlation before and after the only helper launch.
func TestExecuteRejectsMismatchedResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		mutatePreparation  func(*control.NetworkResolverSetupApprovalPreparation)
		mutateLaunch       func(*launcher.Outcome)
		mutateConfirmation func(*control.NetworkResolverSetupApprovalConfirmation)
		wantState          State
		wantLaunch         int
		wantConfirm        int
	}{
		{
			name: "preparation revision",
			mutatePreparation: func(preparation *control.NetworkResolverSetupApprovalPreparation) {
				preparation.OperationRevision++
			},
		},
		{
			name: "preparation ticket",
			mutatePreparation: func(preparation *control.NetworkResolverSetupApprovalPreparation) {
				preparation.Ticket.PolicyFingerprint = "invalid"
			},
		},
		{
			name: "launcher policy",
			mutateLaunch: func(outcome *launcher.Outcome) {
				outcome.Response.Result.ResolverEvidence.PolicyFingerprint = strings.Repeat("d", 64)
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "launcher ownership",
			mutateLaunch: func(outcome *launcher.Outcome) {
				outcome.Response.Result.ResolverEvidence.OwnershipFingerprint = strings.Repeat("d", 64)
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "launcher postcondition",
			mutateLaunch: func(outcome *launcher.Outcome) {
				outcome.Response.Result.ResolverEvidence.Postcondition = helper.ResolverPostconditionOwnedAbsent
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "launcher unrelated evidence",
			mutateLaunch: func(outcome *launcher.Outcome) {
				outcome.Response.Result.Evidence = helper.MutationEvidence{Address: "127.0.0.1"}
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
			name: "launcher declined with child evidence",
			mutateLaunch: func(outcome *launcher.Outcome) {
				*outcome = launcher.Outcome{
					State: launcher.Declined,
					Exit:  &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded},
				}
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "launcher indeterminate with trusted response",
			mutateLaunch: func(outcome *launcher.Outcome) {
				outcome.State = launcher.Indeterminate
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "launcher unsupported state",
			mutateLaunch: func(outcome *launcher.Outcome) {
				*outcome = launcher.Outcome{State: launcher.OutcomeState("unsupported")}
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "helper failure lifecycle evidence",
			mutateLaunch: func(outcome *launcher.Outcome) {
				failure := helper.ResponseError{
					Code:    helper.ErrorCodeMutationFailed,
					Message: "mutation failed safely",
				}
				*outcome = launcher.Outcome{
					State: launcher.HelperFailed,
					Response: helper.Response{
						Version: helper.ProtocolVersion,
						Error:   &failure,
					},
					Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded},
				}
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "helper failure invalid response",
			mutateLaunch: func(outcome *launcher.Outcome) {
				failure := helper.ResponseError{Code: helper.ErrorCodeMutationFailed}
				*outcome = launcher.Outcome{
					State: launcher.HelperFailed,
					Response: helper.Response{
						Version: helper.ProtocolVersion,
						Error:   &failure,
					},
					Exit: &launcher.ProcessExit{Code: launcher.ExitCodeHelperFailed},
				}
			},
			wantState:  Indeterminate,
			wantLaunch: 1,
		},
		{
			name: "confirmation operation",
			mutateConfirmation: func(confirmation *control.NetworkResolverSetupApprovalConfirmation) {
				*confirmation = validConfirmation(t, "operation-other-network-resolver-setup", testResolverRevision)
			},
			wantState:   Indeterminate,
			wantLaunch:  1,
			wantConfirm: 1,
		},
		{
			name: "confirmation lifecycle revision",
			mutateConfirmation: func(confirmation *control.NetworkResolverSetupApprovalConfirmation) {
				confirmation.NetworkRevision--
				confirmation.Revision--
			},
			wantState:   Indeterminate,
			wantLaunch:  1,
			wantConfirm: 1,
		},
		{
			name: "confirmation invalid relation",
			mutateConfirmation: func(confirmation *control.NetworkResolverSetupApprovalConfirmation) {
				confirmation.Revision++
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
			launch := successfulLaunch(validResolverEvidence())
			if test.mutateLaunch != nil {
				test.mutateLaunch(&launch)
			}
			confirmation := validConfirmation(t, testResolverOperationID, testResolverRevision)
			if test.mutateConfirmation != nil {
				test.mutateConfirmation(&confirmation)
			}

			client := &fakeClient{preparation: preparation, confirmation: confirmation}
			helperLauncher := &fakeLauncher{outcome: launch}
			outcome, err := New(client, helperLauncher).Execute(context.Background(), validRequest())
			if !errors.Is(err, ErrInconsistentResponse) {
				t.Fatalf("Execute() error = %v, want ErrInconsistentResponse", err)
			}
			if outcome.State != test.wantState || outcome.Confirmation != nil || outcome.HelperFailure != nil {
				t.Fatalf("Execute() outcome = %#v, want state %q without details", outcome, test.wantState)
			}
			if helperLauncher.calls != test.wantLaunch || client.confirmCalls != test.wantConfirm {
				t.Fatalf(
					"call counts = launch %d, confirm %d; want %d, %d",
					helperLauncher.calls,
					client.confirmCalls,
					test.wantLaunch,
					test.wantConfirm,
				)
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
		helperLauncher := &fakeLauncher{}
		outcome, err := New(client, helperLauncher).Execute(ctx, validRequest())
		if !errors.Is(err, context.Canceled) || outcome != (Outcome{}) {
			t.Fatalf("Execute() = (%#v, %v), want zero outcome and context.Canceled", outcome, err)
		}
		if client.prepareCalls != 0 || helperLauncher.calls != 0 || client.confirmCalls != 0 {
			t.Fatalf(
				"call counts = prepare %d, launch %d, confirm %d; want 0, 0, 0",
				client.prepareCalls,
				helperLauncher.calls,
				client.confirmCalls,
			)
		}
	})

	t.Run("before launch", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		client := &fakeClient{preparation: validPreparation(t)}
		client.prepare = func(
			context.Context,
			control.PrepareNetworkResolverSetupApprovalRequest,
		) (control.NetworkResolverSetupApprovalPreparation, error) {
			cancel()
			return client.preparation, nil
		}
		helperLauncher := &fakeLauncher{}
		outcome, err := New(client, helperLauncher).Execute(ctx, validRequest())
		if !errors.Is(err, context.Canceled) || outcome != (Outcome{}) {
			t.Fatalf("Execute() = (%#v, %v), want zero outcome and context.Canceled", outcome, err)
		}
		if client.prepareCalls != 1 || helperLauncher.calls != 0 || client.confirmCalls != 0 {
			t.Fatalf(
				"call counts = prepare %d, launch %d, confirm %d; want 1, 0, 0",
				client.prepareCalls,
				helperLauncher.calls,
				client.confirmCalls,
			)
		}
	})

	t.Run("after launch", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		client := &fakeClient{preparation: validPreparation(t)}
		helperLauncher := &fakeLauncher{outcome: successfulLaunch(validResolverEvidence())}
		helperLauncher.invoke = func(context.Context, launcher.ResolverLaunchTicket) (launcher.Outcome, error) {
			cancel()
			return helperLauncher.outcome, nil
		}
		outcome, err := New(client, helperLauncher).Execute(ctx, validRequest())
		if !errors.Is(err, context.Canceled) || outcome.State != Indeterminate {
			t.Fatalf("Execute() = (%#v, %v), want indeterminate and context.Canceled", outcome, err)
		}
		if client.prepareCalls != 1 || helperLauncher.calls != 1 || client.confirmCalls != 0 {
			t.Fatalf(
				"call counts = prepare %d, launch %d, confirm %d; want 1, 1, 0",
				client.prepareCalls,
				helperLauncher.calls,
				client.confirmCalls,
			)
		}
	})
}

// TestExecuteRejectsInvalidRequest verifies request validation occurs before daemon interaction.
func TestExecuteRejectsInvalidRequest(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	helperLauncher := &fakeLauncher{}
	outcome, err := New(client, helperLauncher).Execute(context.Background(), Request{})
	if err == nil || outcome != (Outcome{}) {
		t.Fatalf("Execute() = (%#v, %v), want zero outcome and validation error", outcome, err)
	}
	if client.prepareCalls != 0 || helperLauncher.calls != 0 || client.confirmCalls != 0 {
		t.Fatalf(
			"call counts = prepare %d, launch %d, confirm %d; want 0, 0, 0",
			client.prepareCalls,
			helperLauncher.calls,
			client.confirmCalls,
		)
	}
}

// fakeClient records daemon calls and supplies bounded configured responses.
type fakeClient struct {
	preparation     control.NetworkResolverSetupApprovalPreparation
	prepareErr      error
	confirmation    control.NetworkResolverSetupApprovalConfirmation
	confirmErr      error
	prepare         func(context.Context, control.PrepareNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalPreparation, error)
	confirm         func(context.Context, control.ConfirmNetworkResolverSetupApprovalRequest) (control.NetworkResolverSetupApprovalConfirmation, error)
	prepareCalls    int
	confirmCalls    int
	prepareRequests []control.PrepareNetworkResolverSetupApprovalRequest
	confirmRequests []control.ConfirmNetworkResolverSetupApprovalRequest
}

// PrepareNetworkResolverSetupApproval records and returns the configured preparation response.
func (client *fakeClient) PrepareNetworkResolverSetupApproval(
	ctx context.Context,
	request control.PrepareNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalPreparation, error) {
	client.prepareCalls++
	client.prepareRequests = append(client.prepareRequests, request)
	if client.prepare != nil {
		return client.prepare(ctx, request)
	}
	return client.preparation, client.prepareErr
}

// ConfirmNetworkResolverSetupApproval records and returns the configured confirmation response.
func (client *fakeClient) ConfirmNetworkResolverSetupApproval(
	ctx context.Context,
	request control.ConfirmNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalConfirmation, error) {
	client.confirmCalls++
	client.confirmRequests = append(client.confirmRequests, request)
	if client.confirm != nil {
		return client.confirm(ctx, request)
	}
	return client.confirmation, client.confirmErr
}

// fakeLauncher records resolver helper launches and supplies one configured outcome.
type fakeLauncher struct {
	outcome launcher.Outcome
	err     error
	invoke  func(context.Context, launcher.ResolverLaunchTicket) (launcher.Outcome, error)
	calls   int
	tickets []launcher.ResolverLaunchTicket
}

// InvokeResolver records and returns the configured resolver helper outcome.
func (helperLauncher *fakeLauncher) InvokeResolver(
	ctx context.Context,
	ticket launcher.ResolverLaunchTicket,
) (launcher.Outcome, error) {
	helperLauncher.calls++
	helperLauncher.tickets = append(helperLauncher.tickets, ticket)
	if helperLauncher.invoke != nil {
		return helperLauncher.invoke(ctx, ticket)
	}
	return helperLauncher.outcome, helperLauncher.err
}

// validRequest returns the exact resolver setup approval selection shared by fixtures.
func validRequest() Request {
	return Request{OperationID: testResolverOperationID, ExpectedOperationRevision: testResolverRevision}
}

// validPreparation returns one validated resolver approval capability.
func validPreparation(t *testing.T) control.NetworkResolverSetupApprovalPreparation {
	t.Helper()

	preparation := control.NetworkResolverSetupApprovalPreparation{
		OperationID:       testResolverOperationID,
		OperationRevision: testResolverRevision,
		Ticket: control.NetworkResolverSetupApprovalTicket{
			OperationID:                testResolverOperationID,
			Reference:                  helper.TicketReference(strings.Repeat("e", 64)),
			Operation:                  helper.OperationEnsureResolver,
			PolicyFingerprint:          testResolverPolicyFingerprint,
			TargetOwnershipFingerprint: testResolverOwnershipFingerprint,
			ExpiresAt:                  time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
	}
	if err := preparation.Validate(); err != nil {
		t.Fatalf("valid preparation fixture: %v", err)
	}
	return preparation
}

// validResolverEvidence returns one exact resolver ensure postcondition.
func validResolverEvidence() helper.ResolverMutationEvidence {
	return helper.ResolverMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      testResolverPolicyFingerprint,
		OwnershipFingerprint:   testResolverOwnershipFingerprint,
		ObservationFingerprint: testResolverObservationFingerprint,
		Postcondition:          helper.ResolverPostconditionExact,
	}
}

// successfulLaunch returns one correlated successful resolver helper exchange.
func successfulLaunch(evidence helper.ResolverMutationEvidence) launcher.Outcome {
	return launcher.Outcome{
		State: launcher.Succeeded,
		Response: helper.Response{
			Version: helper.ProtocolVersion,
			OK:      true,
			Result: &helper.OperationResult{
				Operation:        helper.OperationEnsureResolver,
				ResolverEvidence: &evidence,
			},
		},
		Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded},
	}
}

// validConfirmation returns one validated success at the fixed approval lifecycle revision offset.
func validConfirmation(
	t *testing.T,
	operationID domain.OperationID,
	approvalRevision domain.Sequence,
) control.NetworkResolverSetupApprovalConfirmation {
	t.Helper()

	requestedAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Minute)
	finishedAt := startedAt.Add(time.Minute)
	confirmation := control.NetworkResolverSetupApprovalConfirmation{
		Operation: domain.Operation{
			ID:          operationID,
			IntentID:    "intent-network-resolver-setup",
			Kind:        domain.OperationKindNetworkResolverSetup,
			State:       domain.OperationSucceeded,
			Phase:       "resolver ready",
			RequestedAt: requestedAt,
			StartedAt:   &startedAt,
			FinishedAt:  &finishedAt,
		},
		Revision:        approvalRevision + 3,
		NetworkRevision: approvalRevision + 2,
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("valid confirmation fixture: %v", err)
	}
	return confirmation
}
