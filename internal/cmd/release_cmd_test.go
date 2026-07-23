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
	"github.com/goforj/harbor/internal/networkreleaseapproval"
)

// releaseApprovalStub records unexpected approval calls in release command tests.
type releaseApprovalStub struct{}

// Execute rejects calls because these tests exercise coordinator-owned checkpoints.
func (releaseApprovalStub) Execute(context.Context, networkreleaseapproval.Request) (networkreleaseapproval.Outcome, error) {
	return networkreleaseapproval.Outcome{}, errors.New("approval should not run")
}

// releaseApprovalRunnerStub records helper-backed release checkpoints and returns their configured progress.
type releaseApprovalRunnerStub struct {
	outcomes []networkreleaseapproval.Outcome
	requests []networkreleaseapproval.Request
}

// Execute records the selected checkpoint before returning its configured durable progress.
func (runner *releaseApprovalRunnerStub) Execute(
	_ context.Context,
	request networkreleaseapproval.Request,
) (networkreleaseapproval.Outcome, error) {
	runner.requests = append(runner.requests, request)
	if len(runner.outcomes) == 0 {
		return networkreleaseapproval.Outcome{}, errors.New("unexpected release approval")
	}
	outcome := runner.outcomes[0]
	runner.outcomes = runner.outcomes[1:]
	return outcome, nil
}

// TestReleaseCommandCompletesFullHappyPath covers daemon and helper-owned release checkpoints end to end.
func TestReleaseCommandCompletesFullHappyPath(t *testing.T) {
	runtime := releaseCommandOperation(t, control.NetworkReleasePhaseRuntimeRelease)
	lowPorts := releaseCommandProgress(t, control.NetworkReleasePhaseLowPorts, 6, 7)
	resolver := releaseCommandProgress(t, control.NetworkReleasePhaseResolver, 7, 8)
	trust := releaseCommandProgress(t, control.NetworkReleasePhaseTrust, 8, 9)
	loopbacks := releaseCommandProgress(t, control.NetworkReleasePhaseLoopbacks, 9, 10)
	ownership := releaseCommandProgress(t, control.NetworkReleasePhaseOwnership, 10, 11)
	terminal := releaseCommandTerminal(t, ownership)
	approval := &releaseApprovalRunnerStub{
		outcomes: []networkreleaseapproval.Outcome{
			{
				State:     networkreleaseapproval.Succeeded,
				Operation: &resolver,
			},
			{
				State:     networkreleaseapproval.Succeeded,
				Operation: &trust,
			},
			{
				State:     networkreleaseapproval.Succeeded,
				Operation: &loopbacks,
			},
			{
				State:     networkreleaseapproval.Succeeded,
				Operation: &ownership,
			},
		},
	}
	starts := 0
	var startRequests []control.StartNetworkReleaseRequest
	connection := &fakeDaemonControlClient{
		networkReleaseOwnershipConfirmation: terminal,
		networkReleaseStartHook: func(_ context.Context, request control.StartNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
			starts++
			startRequests = append(startRequests, request)
			switch starts {
			case 1:
				return runtime, nil
			case 2:
				return lowPorts, nil
			default:
				return control.NetworkReleaseOperation{}, errors.New("unexpected network release resume")
			}
		},
	}
	command := NewReleaseCmd(newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil }), approval)
	var output bytes.Buffer
	command.output = &output

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if starts != 2 {
		t.Fatalf("starts = %d, want 2", starts)
	}
	expectedStarts := []control.StartNetworkReleaseRequest{
		{
			IntentID: networkReleaseIntentID,
		},
		{
			IntentID: networkReleaseIntentID,
		},
	}
	if !reflect.DeepEqual(startRequests, expectedStarts) {
		t.Fatalf("start requests = %#v, want %#v", startRequests, expectedStarts)
	}
	expectedApprovals := []networkreleaseapproval.Request{
		{
			OperationID:                lowPorts.Operation.ID,
			ExpectedCheckpointRevision: lowPorts.CheckpointRevision,
			Phase:                      control.NetworkReleasePhaseLowPorts,
		},
		{
			OperationID:                resolver.Operation.ID,
			ExpectedCheckpointRevision: resolver.CheckpointRevision,
			Phase:                      control.NetworkReleasePhaseResolver,
		},
		{
			OperationID:                trust.Operation.ID,
			ExpectedCheckpointRevision: trust.CheckpointRevision,
			Phase:                      control.NetworkReleasePhaseTrust,
		},
		{
			OperationID:                loopbacks.Operation.ID,
			ExpectedCheckpointRevision: loopbacks.CheckpointRevision,
			Phase:                      control.NetworkReleasePhaseLoopbacks,
		},
	}
	if !reflect.DeepEqual(approval.requests, expectedApprovals) {
		t.Fatalf("approval requests = %#v, want %#v", approval.requests, expectedApprovals)
	}
	if len(approval.outcomes) != 0 {
		t.Fatalf("remaining approval outcomes = %d", len(approval.outcomes))
	}
	if len(connection.networkReleaseOwnershipConfirmations) != 1 {
		t.Fatalf("ownership confirmations = %d, want 1", len(connection.networkReleaseOwnershipConfirmations))
	}
	confirmation := connection.networkReleaseOwnershipConfirmations[0]
	if confirmation.OperationID != ownership.Operation.ID || confirmation.ExpectedCheckpointRevision != ownership.CheckpointRevision {
		t.Fatalf("ownership confirmation = %#v", confirmation)
	}
	if output.String() != "Network release complete.\n" {
		t.Fatalf("output = %q", output.String())
	}
}

// TestReleaseCommandConfirmsOwnershipBeforeReportingSuccess protects the authenticated terminal checkpoint.
func TestReleaseCommandConfirmsOwnershipBeforeReportingSuccess(t *testing.T) {
	operation := releaseCommandOperation(t, control.NetworkReleasePhaseOwnership)
	terminal := releaseCommandTerminal(t, operation)
	connection := &fakeDaemonControlClient{
		networkRelease:                      operation,
		networkReleaseOwnershipConfirmation: terminal,
	}
	starts := 0
	connection.networkReleaseStartHook = func(context.Context, control.StartNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
		starts++
		return operation, nil
	}
	command := NewReleaseCmd(
		newDaemonClient(func(context.Context) (daemonControlClient, error) {
			return connection, nil
		}),
		releaseApprovalStub{},
	)
	var output bytes.Buffer
	command.output = &output
	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(connection.networkReleaseOwnershipConfirmations) != 1 {
		t.Fatalf("ownership confirmations = %d, want 1", len(connection.networkReleaseOwnershipConfirmations))
	}
	if starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}
	confirmation := connection.networkReleaseOwnershipConfirmations[0]
	if confirmation.OperationID != operation.Operation.ID || confirmation.ExpectedCheckpointRevision != operation.CheckpointRevision {
		t.Fatalf("ownership confirmation = %#v", confirmation)
	}
	if output.String() != "Network release complete.\n" {
		t.Fatalf("output = %q", output.String())
	}
}

// TestReleaseCommandReadsButDoesNotInventCoordinatorProgress protects replay when the coordinator has not advanced yet.
func TestReleaseCommandReadsButDoesNotInventCoordinatorProgress(t *testing.T) {
	operation := releaseCommandOperation(t, control.NetworkReleasePhaseRuntimeRelease)
	connection := &fakeDaemonControlClient{networkRelease: operation}
	command := NewReleaseCmd(
		newDaemonClient(func(context.Context) (daemonControlClient, error) {
			return connection, nil
		}),
		releaseApprovalStub{},
	)
	err := command.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "has not advanced") {
		t.Fatalf("Run() error = %v", err)
	}
}

// releaseCommandOperation builds one valid running retained-plan projection for a stable CLI intent.
func releaseCommandOperation(t *testing.T, phase control.NetworkReleasePhase) control.NetworkReleaseOperation {
	return releaseCommandProgress(t, phase, 5, 6)
}

// releaseCommandProgress builds one valid running retained-plan projection at the selected checkpoint.
func releaseCommandProgress(
	t *testing.T,
	phase control.NetworkReleasePhase,
	revision domain.Sequence,
	checkpointRevision domain.Sequence,
) control.NetworkReleaseOperation {
	t.Helper()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	operation, err := domain.NewOperation("operation-release", networkReleaseIntentID, domain.OperationKindNetworkRelease, "", now)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "releasing network runtime", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	return control.NetworkReleaseOperation{
		Operation:          operation,
		Revision:           revision,
		Phase:              phase,
		CheckpointRevision: checkpointRevision,
		NetworkRevision:    4,
	}
}

// releaseCommandTerminal builds the compact terminal fence returned after projection retirement.
func releaseCommandTerminal(t *testing.T, running control.NetworkReleaseOperation) control.NetworkReleaseOperation {
	t.Helper()
	operation, err := running.Operation.Transition(domain.OperationSucceeded, "network released", running.Operation.RequestedAt.Add(time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	return control.NetworkReleaseOperation{
		Operation:          operation,
		Revision:           running.Revision + 3,
		Phase:              control.NetworkReleasePhaseProjection,
		CheckpointRevision: running.CheckpointRevision,
		NetworkRevision:    running.NetworkRevision,
	}
}
