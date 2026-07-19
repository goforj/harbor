package projectapproval

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
)

// TestExecuteConfirmsAlreadyReleasedApproval verifies zero pending leases bypass native consent.
func TestExecuteConfirmsAlreadyReleasedApproval(t *testing.T) {
	preparation := approvalPreparation(2, 2, "", "", 'a')
	confirmation := approvalConfirmation(t, preparation.OperationID, preparation.ProjectID)
	client := &scriptedClient{
		preparations:  []prepareStep{{preparation: preparation}},
		confirmations: []confirmStep{{confirmation: confirmation}},
	}
	helperLauncher := &scriptedLauncher{}

	outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome.State != Succeeded || outcome.Confirmation == nil || !reflect.DeepEqual(*outcome.Confirmation, confirmation) {
		t.Fatalf("Execute() outcome = %#v", outcome)
	}
	if outcome.Progress != progressFor(preparation) {
		t.Fatalf("Execute() progress = %#v", outcome.Progress)
	}
	if len(helperLauncher.tickets) != 0 || len(client.prepareRequests) != 1 || len(client.confirmRequests) != 1 {
		t.Fatalf("prepare = %d, launch = %d, confirm = %d", len(client.prepareRequests), len(helperLauncher.tickets), len(client.confirmRequests))
	}
	assertApprovalRequests(t, client)
}

// TestExecuteLaunchesOneTicketAtATimeUntilDaemonProvesCompletion verifies sequential release and final confirmation.
func TestExecuteLaunchesOneTicketAtATimeUntilDaemonProvesCompletion(t *testing.T) {
	first := approvalPreparation(3, 1, "lease-a", "127.77.0.10", 'a')
	second := approvalPreparation(3, 2, "lease-b", "127.77.0.11", 'b')
	complete := approvalPreparation(3, 3, "", "", 'c')
	confirmation := approvalConfirmation(t, first.OperationID, first.ProjectID)
	client := &scriptedClient{
		preparations: []prepareStep{
			{preparation: first},
			{preparation: second},
			{preparation: complete},
		},
		confirmations: []confirmStep{{confirmation: confirmation}},
	}
	helperLauncher := &scriptedLauncher{steps: []launchStep{
		{outcome: successfulLaunch(*first.Ticket)},
		{outcome: successfulLaunch(*second.Ticket)},
	}}

	outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome.State != Succeeded || outcome.Progress != progressFor(complete) || outcome.Confirmation == nil {
		t.Fatalf("Execute() outcome = %#v", outcome)
	}
	if len(client.prepareRequests) != 3 || len(helperLauncher.tickets) != 2 || len(client.confirmRequests) != 1 {
		t.Fatalf("prepare = %d, launch = %d, confirm = %d", len(client.prepareRequests), len(helperLauncher.tickets), len(client.confirmRequests))
	}
	wantFirst := expectedLaunchTicket(t, *first.Ticket)
	wantSecond := expectedLaunchTicket(t, *second.Ticket)
	if helperLauncher.tickets[0] != wantFirst || helperLauncher.tickets[1] != wantSecond {
		t.Fatalf("launch tickets differ from explicit control conversion")
	}
	formatted := fmt.Sprintf("%#v", outcome)
	if strings.Contains(formatted, string(first.Ticket.Reference)) || strings.Contains(formatted, string(second.Ticket.Reference)) {
		t.Fatal("outcome exposed an opaque ticket reference")
	}
	assertApprovalRequests(t, client)
}

// TestExecuteReturnsTerminalLaunchOutcomes verifies no-child and trusted helper failures stop the workflow.
func TestExecuteReturnsTerminalLaunchOutcomes(t *testing.T) {
	preparation := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
	tests := []struct {
		name          string
		launchOutcome launcher.Outcome
		want          State
		wantFailure   bool
	}{
		{name: "declined", launchOutcome: launcher.Outcome{State: launcher.Declined}, want: Declined},
		{name: "unavailable", launchOutcome: launcher.Outcome{State: launcher.Unavailable}, want: Unavailable},
		{name: "helper failed", launchOutcome: failedLaunch(), want: HelperFailed, wantFailure: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedClient{preparations: []prepareStep{{preparation: preparation}}}
			helperLauncher := &scriptedLauncher{steps: []launchStep{{outcome: test.launchOutcome}}}
			outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if outcome.State != test.want || outcome.Progress != progressFor(preparation) {
				t.Fatalf("Execute() outcome = %#v", outcome)
			}
			if (outcome.HelperFailure != nil) != test.wantFailure {
				t.Fatalf("Execute() helper failure = %#v", outcome.HelperFailure)
			}
			if test.wantFailure && (outcome.HelperFailure.Code != helper.ErrorCodeMutationFailed || outcome.HelperFailure.Message != "helper mutation failed") {
				t.Fatalf("Execute() helper failure = %#v", outcome.HelperFailure)
			}
			if len(client.prepareRequests) != 1 || len(helperLauncher.tickets) != 1 || len(client.confirmRequests) != 0 {
				t.Fatalf("prepare = %d, launch = %d, confirm = %d", len(client.prepareRequests), len(helperLauncher.tickets), len(client.confirmRequests))
			}
		})
	}
}

// TestExecuteReobservesIndeterminateWithoutRelaunching verifies an unproven lease never receives a second capability automatically.
func TestExecuteReobservesIndeterminateWithoutRelaunching(t *testing.T) {
	first := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
	reobserved := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'b')
	client := &scriptedClient{preparations: []prepareStep{{preparation: first}, {preparation: reobserved}}}
	helperLauncher := &scriptedLauncher{steps: []launchStep{{outcome: launcher.Outcome{State: launcher.Indeterminate}}}}

	outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome.State != Indeterminate || outcome.Progress != progressFor(reobserved) {
		t.Fatalf("Execute() outcome = %#v", outcome)
	}
	if len(client.prepareRequests) != 2 || len(helperLauncher.tickets) != 1 || len(client.confirmRequests) != 0 {
		t.Fatalf("prepare = %d, launch = %d, confirm = %d", len(client.prepareRequests), len(helperLauncher.tickets), len(client.confirmRequests))
	}
}

// TestExecuteContinuesAfterIndeterminateEffectIsProven verifies daemon observation can safely advance to a different lease.
func TestExecuteContinuesAfterIndeterminateEffectIsProven(t *testing.T) {
	first := approvalPreparation(2, 0, "lease-a", "127.77.0.10", 'a')
	second := approvalPreparation(2, 1, "lease-b", "127.77.0.11", 'b')
	complete := approvalPreparation(2, 2, "", "", 'c')
	client := &scriptedClient{
		preparations:  []prepareStep{{preparation: first}, {preparation: second}, {preparation: complete}},
		confirmations: []confirmStep{{confirmation: approvalConfirmation(t, first.OperationID, first.ProjectID)}},
	}
	helperLauncher := &scriptedLauncher{steps: []launchStep{
		{outcome: launcher.Outcome{State: launcher.Indeterminate}},
		{outcome: successfulLaunch(*second.Ticket)},
	}}

	outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if outcome.State != Succeeded || len(helperLauncher.tickets) != 2 || len(client.confirmRequests) != 1 {
		t.Fatalf("Execute() outcome = %#v, launches = %d", outcome, len(helperLauncher.tickets))
	}
}

// TestExecuteRejectsReportedSuccessWithoutDaemonProgress verifies helper evidence cannot replace daemon observation.
func TestExecuteRejectsReportedSuccessWithoutDaemonProgress(t *testing.T) {
	first := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
	reobserved := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'b')
	client := &scriptedClient{preparations: []prepareStep{{preparation: first}, {preparation: reobserved}}}
	helperLauncher := &scriptedLauncher{steps: []launchStep{{outcome: successfulLaunch(*first.Ticket)}}}

	outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
	if !errors.Is(err, ErrNoProgress) {
		t.Fatalf("Execute() error = %v, want ErrNoProgress", err)
	}
	if outcome.State != Indeterminate || outcome.Progress != progressFor(reobserved) || len(helperLauncher.tickets) != 1 {
		t.Fatalf("Execute() outcome = %#v, launches = %d", outcome, len(helperLauncher.tickets))
	}
}

// TestExecuteRejectsInconsistentProgress verifies stable project, total, counts, and lease bindings across observations.
func TestExecuteRejectsInconsistentProgress(t *testing.T) {
	tests := []struct {
		name    string
		current control.ProjectUnregisterApprovalPreparation
		next    control.ProjectUnregisterApprovalPreparation
	}{
		{
			name:    "project changed",
			current: approvalPreparation(2, 0, "lease-a", "127.77.0.10", 'a'),
			next:    approvalPreparationFor("project-2", 2, 1, "lease-b", "127.77.0.11", 'b'),
		},
		{
			name:    "total changed",
			current: approvalPreparation(2, 0, "lease-a", "127.77.0.10", 'a'),
			next:    approvalPreparation(3, 1, "lease-b", "127.77.0.11", 'b'),
		},
		{
			name:    "progress regressed",
			current: approvalPreparation(2, 1, "lease-a", "127.77.0.10", 'a'),
			next:    approvalPreparation(2, 0, "lease-a", "127.77.0.10", 'b'),
		},
		{
			name:    "ticket changed without progress",
			current: approvalPreparation(2, 0, "lease-a", "127.77.0.10", 'a'),
			next:    approvalPreparation(2, 0, "lease-b", "127.77.0.11", 'b'),
		},
		{
			name:    "same lease changed address",
			current: approvalPreparation(2, 0, "lease-a", "127.77.0.10", 'a'),
			next:    approvalPreparation(2, 1, "lease-a", "127.77.0.11", 'b'),
		},
		{
			name:    "different lease reused address",
			current: approvalPreparation(2, 0, "lease-a", "127.77.0.10", 'a'),
			next:    approvalPreparation(2, 1, "lease-b", "127.77.0.10", 'b'),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedClient{preparations: []prepareStep{{preparation: test.current}, {preparation: test.next}}}
			helperLauncher := &scriptedLauncher{steps: []launchStep{{outcome: successfulLaunch(*test.current.Ticket)}}}
			outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
			if !errors.Is(err, ErrInconsistentResponse) {
				t.Fatalf("Execute() error = %v", err)
			}
			if outcome.State != Indeterminate || len(helperLauncher.tickets) != 1 || len(client.confirmRequests) != 0 {
				t.Fatalf("Execute() outcome = %#v, launches = %d", outcome, len(helperLauncher.tickets))
			}
		})
	}
}

// TestExecuteRejectsInvalidRequest verifies malformed selections never reach the daemon or launcher.
func TestExecuteRejectsInvalidRequest(t *testing.T) {
	client := &scriptedClient{}
	helperLauncher := &scriptedLauncher{}
	outcome, err := New(client, helperLauncher).Execute(t.Context(), Request{})
	if err == nil || outcome.State != "" || len(client.prepareRequests) != 0 || len(helperLauncher.tickets) != 0 {
		t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
	}
}

// TestExecuteRejectsPreparationCorrelationFailures verifies faked clients cannot cross operation or revision boundaries.
func TestExecuteRejectsPreparationCorrelationFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*control.ProjectUnregisterApprovalPreparation)
	}{
		{name: "operation", mutate: func(preparation *control.ProjectUnregisterApprovalPreparation) {
			preparation.OperationID = "operation-2"
			preparation.Ticket.OperationID = "operation-2"
		}},
		{name: "revision", mutate: func(preparation *control.ProjectUnregisterApprovalPreparation) { preparation.OperationRevision++ }},
		{name: "counts", mutate: func(preparation *control.ProjectUnregisterApprovalPreparation) { preparation.PendingLeases++ }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preparation := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
			test.mutate(&preparation)
			client := &scriptedClient{preparations: []prepareStep{{preparation: preparation}}}
			helperLauncher := &scriptedLauncher{}
			outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
			if !errors.Is(err, ErrInconsistentResponse) || outcome.State != "" || len(helperLauncher.tickets) != 0 {
				t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
			}
		})
	}
}

// TestExecuteRejectsConfirmationCorrelationFailures verifies success cannot cross operation or project boundaries.
func TestExecuteRejectsConfirmationCorrelationFailures(t *testing.T) {
	preparation := approvalPreparation(1, 1, "", "", 'a')
	tests := []struct {
		name      string
		operation domain.Operation
	}{
		{name: "operation", operation: succeededUnregisterOperation(t, "operation-2", preparation.ProjectID)},
		{name: "project", operation: succeededUnregisterOperation(t, preparation.OperationID, "project-2")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedClient{
				preparations:  []prepareStep{{preparation: preparation}},
				confirmations: []confirmStep{{confirmation: control.ProjectUnregisterApprovalConfirmation{Operation: test.operation, Revision: 9}}},
			}
			outcome, err := New(client, &scriptedLauncher{}).Execute(t.Context(), approvalRequest())
			if !errors.Is(err, ErrInconsistentResponse) || outcome.State != Indeterminate {
				t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
			}
		})
	}
}

// TestExecuteReturnsConfirmationFailuresAsIndeterminate verifies durable completion errors never become success.
func TestExecuteReturnsConfirmationFailuresAsIndeterminate(t *testing.T) {
	preparation := approvalPreparation(1, 1, "", "", 'a')
	tests := []struct {
		name string
		step confirmStep
	}{
		{name: "call failed", step: confirmStep{err: context.Canceled}},
		{name: "invalid response", step: confirmStep{confirmation: control.ProjectUnregisterApprovalConfirmation{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedClient{
				preparations:  []prepareStep{{preparation: preparation}},
				confirmations: []confirmStep{test.step},
			}
			outcome, err := New(client, &scriptedLauncher{}).Execute(t.Context(), approvalRequest())
			if err == nil || outcome.State != Indeterminate || len(client.confirmRequests) != 1 {
				t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
			}
		})
	}
}

// TestExecuteRejectsMalformedLauncherOutcomes verifies executor correlation survives alternate launcher implementations.
func TestExecuteRejectsMalformedLauncherOutcomes(t *testing.T) {
	preparation := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
	tests := []struct {
		name    string
		outcome launcher.Outcome
	}{
		{name: "unknown state", outcome: launcher.Outcome{}},
		{name: "success without exchange", outcome: launcher.Outcome{State: launcher.Succeeded}},
		{name: "mismatched operation", outcome: successfulLaunchFor(helper.OperationEnsureLoopbackIdentity, preparation.Ticket.Address)},
		{name: "mismatched address", outcome: successfulLaunchFor(preparation.Ticket.Operation, "127.77.0.11")},
		{name: "decline with process", outcome: launcher.Outcome{State: launcher.Declined, Exit: &launcher.ProcessExit{Code: 1}}},
		{name: "failure without error", outcome: launcher.Outcome{State: launcher.HelperFailed, Exit: &launcher.ProcessExit{Code: 1}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedClient{preparations: []prepareStep{{preparation: preparation}}}
			helperLauncher := &scriptedLauncher{steps: []launchStep{{outcome: test.outcome}}}
			outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
			if !errors.Is(err, ErrInconsistentResponse) || outcome.State != Indeterminate || len(client.prepareRequests) != 1 {
				t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
			}
		})
	}
}

// TestExecuteHonorsCancellation verifies cancellation prevents new consent and remains typed after an uncertain launch.
func TestExecuteHonorsCancellation(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		preparation := approvalPreparation(1, 1, "", "", 'a')
		client := &scriptedClient{
			preparations:  []prepareStep{{preparation: preparation}},
			confirmations: []confirmStep{{confirmation: approvalConfirmation(t, preparation.OperationID, preparation.ProjectID)}},
		}
		outcome, err := New(client, &scriptedLauncher{}).Execute(nil, approvalRequest())
		if err != nil || outcome.State != Succeeded {
			t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
		}
	})

	t.Run("before prepare", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		client := &scriptedClient{}
		outcome, err := New(client, &scriptedLauncher{}).Execute(ctx, approvalRequest())
		if !errors.Is(err, context.Canceled) || outcome.State != "" || len(client.prepareRequests) != 0 {
			t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
		}
	})

	t.Run("during launch", func(t *testing.T) {
		preparation := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
		client := &scriptedClient{preparations: []prepareStep{{preparation: preparation}}}
		helperLauncher := &scriptedLauncher{steps: []launchStep{{err: context.Canceled}}}
		outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
		if !errors.Is(err, context.Canceled) || outcome.State != Indeterminate || outcome.Progress != progressFor(preparation) ||
			len(helperLauncher.tickets) != 1 || len(client.prepareRequests) != 1 {
			t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
		}
	})

	t.Run("after prepare", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		preparation := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
		client := &scriptedClient{preparations: []prepareStep{{preparation: preparation, beforeReturn: cancel}}}
		helperLauncher := &scriptedLauncher{}
		outcome, err := New(client, helperLauncher).Execute(ctx, approvalRequest())
		if !errors.Is(err, context.Canceled) || outcome.State != "" || len(client.prepareRequests) != 1 || len(helperLauncher.tickets) != 0 {
			t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
		}
	})

	t.Run("after uncertain launch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		preparation := approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a')
		client := &scriptedClient{preparations: []prepareStep{{preparation: preparation}}}
		helperLauncher := &scriptedLauncher{steps: []launchStep{{
			outcome:      launcher.Outcome{State: launcher.Indeterminate},
			beforeReturn: cancel,
		}}}
		outcome, err := New(client, helperLauncher).Execute(ctx, approvalRequest())
		if !errors.Is(err, context.Canceled) || outcome.State != Indeterminate || len(client.prepareRequests) != 2 || len(helperLauncher.tickets) != 1 {
			t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
		}
	})
}

// TestConvertTicketRejectsInvalidMetadata verifies conversion never bypasses launcher-owned validation.
func TestConvertTicketRejectsInvalidMetadata(t *testing.T) {
	ticket := *approvalPreparation(1, 0, "lease-a", "127.77.0.10", 'a').Ticket
	ticket.Reference = "short"
	converted, effect, err := convertTicket(ticket)
	if err == nil || converted != (launcher.LaunchTicket{}) || effect != (launchEffect{}) {
		t.Fatalf("convertTicket() = %#v, %#v, %v", converted, effect, err)
	}
}

// TestExecuteRejectsRepeatedPreviouslyLaunchedLease verifies loop bounds cannot be bypassed by cycling tickets.
func TestExecuteRejectsRepeatedPreviouslyLaunchedLease(t *testing.T) {
	first := approvalPreparation(3, 0, "lease-a", "127.77.0.10", 'a')
	second := approvalPreparation(3, 1, "lease-b", "127.77.0.11", 'b')
	repeated := approvalPreparation(3, 2, "lease-a", "127.77.0.10", 'c')
	client := &scriptedClient{preparations: []prepareStep{{preparation: first}, {preparation: second}, {preparation: repeated}}}
	helperLauncher := &scriptedLauncher{steps: []launchStep{
		{outcome: successfulLaunch(*first.Ticket)},
		{outcome: successfulLaunch(*second.Ticket)},
	}}

	outcome, err := New(client, helperLauncher).Execute(t.Context(), approvalRequest())
	if !errors.Is(err, ErrInconsistentResponse) || outcome.State != Indeterminate || len(helperLauncher.tickets) != 2 {
		t.Fatalf("Execute() outcome = %#v, error = %v", outcome, err)
	}
}

// TestNewRequiresDependencies verifies client and launcher wiring fail fast before consent.
func TestNewRequiresDependencies(t *testing.T) {
	client := &scriptedClient{}
	helperLauncher := &scriptedLauncher{}
	var typedNilClient *scriptedClient
	var typedNilLauncher *scriptedLauncher
	tests := []struct {
		name  string
		build func()
	}{
		{name: "client", build: func() { New(nil, helperLauncher) }},
		{name: "typed nil client", build: func() { New(typedNilClient, helperLauncher) }},
		{name: "launcher", build: func() { New(client, nil) }},
		{name: "typed nil launcher", build: func() { New(client, typedNilLauncher) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected constructor panic")
				}
			}()
			test.build()
		})
	}
	if requiredInterfaceIsNil(1) {
		t.Fatal("concrete value was treated as nil")
	}
}

type prepareStep struct {
	preparation  control.ProjectUnregisterApprovalPreparation
	err          error
	beforeReturn func()
}

type confirmStep struct {
	confirmation control.ProjectUnregisterApprovalConfirmation
	err          error
}

type scriptedClient struct {
	preparations    []prepareStep
	confirmations   []confirmStep
	prepareRequests []control.PrepareProjectUnregisterApprovalRequest
	confirmRequests []control.ConfirmProjectUnregisterApprovalRequest
}

// PrepareProjectUnregisterApproval returns one scripted daemon observation after recording exact correlation input.
func (client *scriptedClient) PrepareProjectUnregisterApproval(
	ctx context.Context,
	request control.PrepareProjectUnregisterApprovalRequest,
) (control.ProjectUnregisterApprovalPreparation, error) {
	client.prepareRequests = append(client.prepareRequests, request)
	if err := ctx.Err(); err != nil {
		return control.ProjectUnregisterApprovalPreparation{}, err
	}
	index := len(client.prepareRequests) - 1
	if index >= len(client.preparations) {
		return control.ProjectUnregisterApprovalPreparation{}, errors.New("unexpected prepare call")
	}
	step := client.preparations[index]
	if step.beforeReturn != nil {
		step.beforeReturn()
	}
	return step.preparation, step.err
}

// ConfirmProjectUnregisterApproval returns one scripted durable completion after recording exact correlation input.
func (client *scriptedClient) ConfirmProjectUnregisterApproval(
	ctx context.Context,
	request control.ConfirmProjectUnregisterApprovalRequest,
) (control.ProjectUnregisterApprovalConfirmation, error) {
	client.confirmRequests = append(client.confirmRequests, request)
	if err := ctx.Err(); err != nil {
		return control.ProjectUnregisterApprovalConfirmation{}, err
	}
	index := len(client.confirmRequests) - 1
	if index >= len(client.confirmations) {
		return control.ProjectUnregisterApprovalConfirmation{}, errors.New("unexpected confirm call")
	}
	return client.confirmations[index].confirmation, client.confirmations[index].err
}

type launchStep struct {
	outcome      launcher.Outcome
	err          error
	beforeReturn func()
}

type scriptedLauncher struct {
	steps   []launchStep
	tickets []launcher.LaunchTicket
}

// Invoke records the immutable launch ticket and returns one synchronous native-launch conclusion.
func (helperLauncher *scriptedLauncher) Invoke(_ context.Context, ticket launcher.LaunchTicket) (launcher.Outcome, error) {
	helperLauncher.tickets = append(helperLauncher.tickets, ticket)
	index := len(helperLauncher.tickets) - 1
	if index >= len(helperLauncher.steps) {
		return launcher.Outcome{}, errors.New("unexpected launcher call")
	}
	step := helperLauncher.steps[index]
	if step.beforeReturn != nil {
		step.beforeReturn()
	}
	return step.outcome, step.err
}

// approvalRequest returns the exact operation revision shared by every test fixture.
func approvalRequest() Request {
	return Request{OperationID: "operation-1", ExpectedOperationRevision: 7}
}

// approvalPreparation returns one valid progress response for the common test project.
func approvalPreparation(total, released int, secondaryID, address string, marker byte) control.ProjectUnregisterApprovalPreparation {
	return approvalPreparationFor("project-1", total, released, secondaryID, address, marker)
}

// approvalPreparationFor returns one valid progress response for an explicitly selected project.
func approvalPreparationFor(
	projectID domain.ProjectID,
	total int,
	released int,
	secondaryID string,
	address string,
	marker byte,
) control.ProjectUnregisterApprovalPreparation {
	preparation := control.ProjectUnregisterApprovalPreparation{
		OperationID:       "operation-1",
		OperationRevision: 7,
		ProjectID:         projectID,
		TotalLeases:       total,
		ReleasedLeases:    released,
		PendingLeases:     total - released,
	}
	if preparation.PendingLeases > 0 {
		preparation.Ticket = &control.HelperApprovalTicket{
			OperationID: preparation.OperationID,
			LeaseKey: control.HelperApprovalLeaseKey{
				ProjectID:   projectID,
				SecondaryID: secondaryID,
			},
			Reference: helper.TicketReference(strings.Repeat(string(marker), 64)),
			Operation: helper.OperationReleaseLoopbackIdentity,
			Address:   address,
			ExpiresAt: time.Date(2026, time.July, 19, 12, 1, 0, 0, time.UTC),
		}
	}
	return preparation
}

// successfulLaunch returns one fully correlated helper success conclusion.
func successfulLaunch(ticket control.HelperApprovalTicket) launcher.Outcome {
	return successfulLaunchFor(ticket.Operation, ticket.Address)
}

// successfulLaunchFor returns a helper success conclusion with explicit correlation fields.
func successfulLaunchFor(operation helper.Operation, address string) launcher.Outcome {
	return launcher.Outcome{
		State: launcher.Succeeded,
		Response: helper.Response{
			Version: helper.ProtocolVersion,
			OK:      true,
			Result: &helper.OperationResult{
				Operation: operation,
				Evidence:  helper.MutationEvidence{Address: address},
			},
		},
		Exit: &launcher.ProcessExit{Code: launcher.ExitCodeSucceeded},
	}
}

// failedLaunch returns one trusted bounded helper failure conclusion.
func failedLaunch() launcher.Outcome {
	return launcher.Outcome{
		State: launcher.HelperFailed,
		Response: helper.Response{
			Version: helper.ProtocolVersion,
			OK:      false,
			Error: &helper.ResponseError{
				Code:    helper.ErrorCodeMutationFailed,
				Message: "helper mutation failed",
			},
		},
		Exit: &launcher.ProcessExit{Code: launcher.ExitCodeHelperFailed},
	}
}

// expectedLaunchTicket explicitly mirrors the production control-to-launcher conversion for assertions.
func expectedLaunchTicket(t *testing.T, ticket control.HelperApprovalTicket) launcher.LaunchTicket {
	t.Helper()
	converted, _, err := convertTicket(ticket)
	if err != nil {
		t.Fatalf("convert launch ticket: %v", err)
	}
	return converted
}

// approvalConfirmation returns one valid succeeded unregister confirmation.
func approvalConfirmation(
	t *testing.T,
	operationID domain.OperationID,
	projectID domain.ProjectID,
) control.ProjectUnregisterApprovalConfirmation {
	t.Helper()
	return control.ProjectUnregisterApprovalConfirmation{
		Operation: succeededUnregisterOperation(t, operationID, projectID),
		Revision:  9,
	}
}

// succeededUnregisterOperation returns one valid terminal operation for confirmation correlation tests.
func succeededUnregisterOperation(t *testing.T, operationID domain.OperationID, projectID domain.ProjectID) domain.Operation {
	t.Helper()
	requestedAt := time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation(operationID, "intent-1", domain.OperationKindProjectUnregister, projectID, requestedAt)
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "releasing", requestedAt.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("start operation: %v", err)
	}
	operation, err = operation.Transition(domain.OperationSucceeded, "complete", requestedAt.Add(2*time.Minute), nil)
	if err != nil {
		t.Fatalf("finish operation: %v", err)
	}
	return operation
}

// assertApprovalRequests verifies every daemon call remains bound to the original operation revision.
func assertApprovalRequests(t *testing.T, client *scriptedClient) {
	t.Helper()
	wantPrepare := control.PrepareProjectUnregisterApprovalRequest{OperationID: "operation-1", ExpectedOperationRevision: 7}
	for _, request := range client.prepareRequests {
		if request != wantPrepare {
			t.Fatalf("prepare request = %#v", request)
		}
	}
	wantConfirm := control.ConfirmProjectUnregisterApprovalRequest{OperationID: "operation-1", ExpectedOperationRevision: 7}
	for _, request := range client.confirmRequests {
		if request != wantConfirm {
			t.Fatalf("confirm request = %#v", request)
		}
	}
}
