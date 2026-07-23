package networkresolverpolicymigrationapproval

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
	testOperationID            domain.OperationID = "operation-network-resolver-policy-migration"
	testRevision               domain.Sequence    = 7
	testPolicyFingerprint                         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testOwnershipFingerprint                      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testObservationFingerprint                    = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var errTestFailure = errors.New("test failure")

// TestExecuteConfirmsExactOwnedAbsentEvidence verifies the complete durable retirement path.
func TestExecuteConfirmsExactOwnedAbsentEvidence(t *testing.T) {
	t.Parallel()

	request := validRequest()
	evidence := validEvidence()
	confirmation := validConfirmation(t)
	client := &fakeClient{
		preparation:  validPreparation(t),
		confirmation: confirmation,
	}
	launcher := &fakeLauncher{outcome: successfulLaunch(evidence)}

	outcome, err := New(client, launcher).Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome.State != Succeeded || outcome.Confirmation == nil || !reflect.DeepEqual(*outcome.Confirmation, confirmation) {
		t.Fatalf("Execute() outcome = %#v, want durable success %#v", outcome, confirmation)
	}
	want := control.ConfirmNetworkResolverPolicyMigrationApprovalRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ResolverEvidence:          evidence,
	}
	if len(client.confirmRequests) != 1 || client.confirmRequests[0] != want {
		t.Fatalf("confirmation requests = %#v, want %#v", client.confirmRequests, want)
	}
	if launcher.calls != 1 {
		t.Fatalf("launcher calls = %d, want 1", launcher.calls)
	}
}

// TestExecutePublishesIndeterminateWithoutLaunching verifies an uncertain capability publication cannot be redeemed or reissued here.
func TestExecutePublishesIndeterminateWithoutLaunching(t *testing.T) {
	t.Parallel()

	preparation := validPreparation(t)
	preparation.PublicationDisposition = control.NetworkResolverPolicyMigrationPublicationIndeterminate
	client := &fakeClient{preparation: preparation}
	launcher := &fakeLauncher{}

	outcome, err := New(client, launcher).Execute(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome != (Outcome{State: Indeterminate}) {
		t.Fatalf("Execute() outcome = %#v, want indeterminate", outcome)
	}
	if launcher.calls != 0 || len(client.confirmRequests) != 0 {
		t.Fatalf("calls = launch %d, confirm %d; want 0, 0", launcher.calls, len(client.confirmRequests))
	}
}

// TestExecuteHandlesLostResponseAndCancellation verifies uncertain effects never publish a durable completion.
func TestExecuteHandlesLostResponseAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("lost response", func(t *testing.T) {
		t.Parallel()
		client := &fakeClient{preparation: validPreparation(t)}
		launcher := &fakeLauncher{outcome: launcher.Outcome{State: launcher.Indeterminate}}
		outcome, err := New(client, launcher).Execute(context.Background(), validRequest())
		if err != nil || outcome != (Outcome{State: Indeterminate}) {
			t.Fatalf("Execute() = (%#v, %v), want indeterminate without error", outcome, err)
		}
		if len(client.confirmRequests) != 0 {
			t.Fatalf("confirmation calls = %d, want 0", len(client.confirmRequests))
		}
	})

	t.Run("cancelled after launch", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		client := &fakeClient{preparation: validPreparation(t)}
		helperLauncher := &fakeLauncher{}
		helperLauncher.invoke = func(context.Context, launcher.ResolverLaunchTicket) (launcher.Outcome, error) {
			cancel()
			return successfulLaunch(validEvidence()), nil
		}
		outcome, err := New(client, helperLauncher).Execute(ctx, validRequest())
		if !errors.Is(err, context.Canceled) || outcome != (Outcome{State: Indeterminate}) {
			t.Fatalf("Execute() = (%#v, %v), want indeterminate and context.Canceled", outcome, err)
		}
		if len(client.confirmRequests) != 0 {
			t.Fatalf("confirmation calls = %d, want 0", len(client.confirmRequests))
		}
	})
}

// TestExecuteRejectsAlternateOrMismatchedRetirementEvidence verifies helper evidence cannot cross the selected effect boundary.
func TestExecuteRejectsAlternateOrMismatchedRetirementEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*launcher.Outcome)
	}{
		{
			name: "ensure operation",
			mutate: func(outcome *launcher.Outcome) {
				outcome.Response.Result.Operation = helper.OperationEnsureResolver
			},
		},
		{
			name: "exact postcondition",
			mutate: func(outcome *launcher.Outcome) {
				outcome.Response.Result.ResolverEvidence.Postcondition = helper.ResolverPostconditionExact
			},
		},
		{
			name: "ownership mismatch",
			mutate: func(outcome *launcher.Outcome) {
				outcome.Response.Result.ResolverEvidence.OwnershipFingerprint = strings.Repeat("d", 64)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outcome := successfulLaunch(validEvidence())
			test.mutate(&outcome)
			client := &fakeClient{preparation: validPreparation(t)}
			helperLauncher := &fakeLauncher{outcome: outcome}
			got, err := New(client, helperLauncher).Execute(context.Background(), validRequest())
			if !errors.Is(err, ErrInconsistentResponse) || got != (Outcome{State: Indeterminate}) {
				t.Fatalf("Execute() = (%#v, %v), want indeterminate inconsistent response", got, err)
			}
			if len(client.confirmRequests) != 0 {
				t.Fatalf("confirmation calls = %d, want 0", len(client.confirmRequests))
			}
		})
	}
}

// TestExecuteTreatsConfirmationFailureAsIndeterminate verifies only a validated durable confirmation is published.
func TestExecuteTreatsConfirmationFailureAsIndeterminate(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		preparation: validPreparation(t),
		confirmErr:  errTestFailure,
	}
	helperLauncher := &fakeLauncher{
		outcome: successfulLaunch(validEvidence()),
	}
	outcome, err := New(client, helperLauncher).Execute(context.Background(), validRequest())
	if !errors.Is(err, errTestFailure) || outcome != (Outcome{State: Indeterminate}) {
		t.Fatalf("Execute() = (%#v, %v), want indeterminate and test failure", outcome, err)
	}
}

// TestExecuteRejectsConfirmationOutsideSelectedRevision verifies durable completion cannot skip or cross the selected approval transition.
func TestExecuteRejectsConfirmationOutsideSelectedRevision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*control.NetworkResolverPolicyMigrationApprovalConfirmation)
	}{
		{
			name: "network revision skips one transition",
			mutate: func(confirmation *control.NetworkResolverPolicyMigrationApprovalConfirmation) {
				confirmation.NetworkRevision++
				confirmation.Revision++
			},
		},
		{
			name: "terminal operation revision skips network revision",
			mutate: func(confirmation *control.NetworkResolverPolicyMigrationApprovalConfirmation) {
				confirmation.Revision++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			confirmation := validConfirmation(t)
			test.mutate(&confirmation)
			client := &fakeClient{
				preparation:  validPreparation(t),
				confirmation: confirmation,
			}
			outcome, err := New(client, &fakeLauncher{outcome: successfulLaunch(validEvidence())}).Execute(context.Background(), validRequest())
			if !errors.Is(err, ErrInconsistentResponse) || outcome != (Outcome{State: Indeterminate}) {
				t.Fatalf("Execute() = (%#v, %v), want indeterminate inconsistent response", outcome, err)
			}
		})
	}
}

// fakeClient records the exact control calls made by Executor.
type fakeClient struct {
	preparation     control.NetworkResolverPolicyMigrationApprovalPreparation
	prepareErr      error
	confirmation    control.NetworkResolverPolicyMigrationApprovalConfirmation
	confirmErr      error
	confirmRequests []control.ConfirmNetworkResolverPolicyMigrationApprovalRequest
}

// PrepareNetworkResolverPolicyMigrationApproval returns the configured preparation.
func (client *fakeClient) PrepareNetworkResolverPolicyMigrationApproval(context.Context, control.PrepareNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalPreparation, error) {
	return client.preparation, client.prepareErr
}

// ConfirmNetworkResolverPolicyMigrationApproval records and returns the configured confirmation.
func (client *fakeClient) ConfirmNetworkResolverPolicyMigrationApproval(_ context.Context, request control.ConfirmNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error) {
	client.confirmRequests = append(client.confirmRequests, request)
	return client.confirmation, client.confirmErr
}

// fakeLauncher records resolver helper launches and returns one configured outcome.
type fakeLauncher struct {
	outcome launcher.Outcome
	invoke  func(context.Context, launcher.ResolverLaunchTicket) (launcher.Outcome, error)
	calls   int
}

// InvokeResolver records and returns the configured resolver helper result.
func (launcher *fakeLauncher) InvokeResolver(ctx context.Context, ticket launcher.ResolverLaunchTicket) (launcher.Outcome, error) {
	launcher.calls++
	if launcher.invoke != nil {
		return launcher.invoke(ctx, ticket)
	}
	return launcher.outcome, nil
}

// validRequest returns the exact migration approval selection shared by fixtures.
func validRequest() Request {
	return Request{
		OperationID:               testOperationID,
		ExpectedOperationRevision: testRevision,
	}
}

// validPreparation returns a durably published retirement capability.
func validPreparation(t *testing.T) control.NetworkResolverPolicyMigrationApprovalPreparation {
	t.Helper()
	preparation := control.NetworkResolverPolicyMigrationApprovalPreparation{
		OperationID:            testOperationID,
		OperationRevision:      testRevision,
		PublicationDisposition: control.NetworkResolverPolicyMigrationPublicationDurable,
		Ticket: control.NetworkResolverPolicyMigrationApprovalTicket{
			OperationID:              testOperationID,
			Reference:                helper.TicketReference(strings.Repeat("e", 64)),
			Operation:                helper.OperationRetireResolver,
			PolicyFingerprint:        testPolicyFingerprint,
			PostOwnershipFingerprint: testOwnershipFingerprint,
			ExpiresAt:                time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
	}
	if err := preparation.Validate(); err != nil {
		t.Fatalf("valid preparation: %v", err)
	}
	return preparation
}

// validEvidence returns one exact owned-absent retirement postcondition.
func validEvidence() helper.ResolverMutationEvidence {
	return helper.ResolverMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      testPolicyFingerprint,
		OwnershipFingerprint:   testOwnershipFingerprint,
		ObservationFingerprint: testObservationFingerprint,
		Postcondition:          helper.ResolverPostconditionOwnedAbsent,
	}
}

// successfulLaunch returns one correlated successful retirement helper exchange.
func successfulLaunch(evidence helper.ResolverMutationEvidence) launcher.Outcome {
	return launcher.Outcome{
		State: launcher.Succeeded,
		Response: helper.Response{
			Version: helper.ProtocolVersion,
			OK:      true,
			Result: &helper.OperationResult{
				Operation:        helper.OperationRetireResolver,
				ResolverEvidence: &evidence,
			},
		},
		Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded},
	}
}

// validConfirmation returns one terminal migration completion following the selected approval transition.
func validConfirmation(t *testing.T) control.NetworkResolverPolicyMigrationApprovalConfirmation {
	t.Helper()
	requestedAt := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	startedAt := requestedAt.Add(time.Minute)
	finishedAt := startedAt.Add(time.Minute)
	confirmation := control.NetworkResolverPolicyMigrationApprovalConfirmation{
		Operation: domain.Operation{
			ID:          testOperationID,
			IntentID:    "intent-network-resolver-policy-migration",
			Kind:        domain.OperationKindNetworkResolverPolicyMigration,
			State:       domain.OperationSucceeded,
			Phase:       "completed",
			RequestedAt: requestedAt,
			StartedAt:   &startedAt,
			FinishedAt:  &finishedAt,
		},
		Revision:        testRevision + 3,
		NetworkRevision: testRevision + 2,
	}
	if err := confirmation.Validate(); err != nil {
		t.Fatalf("valid confirmation: %v", err)
	}
	return confirmation
}
