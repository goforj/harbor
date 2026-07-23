package helper

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDispatcherOwnershipReleaseConsumesReplayBeforeExecution proves the ownership effect remains behind durable admission.
func TestDispatcherOwnershipReleaseConsumesReplayBeforeExecution(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestOwnershipReleaseTicket(now)
	redemption := redemptionForTicket(reference, ticket)
	redemption.Admission.OwnershipFingerprint = ticket.ExpectedOwnershipFingerprint
	redemption.Admission.TargetOwnershipFingerprint = ticket.ExpectedOwnershipFingerprint
	redemption.Admission.PostOwnershipFingerprint = ticket.ExpectedOwnershipFingerprint
	replay := newTestReplayGuard()
	executorCalls := 0
	dispatcher := NewDispatcherWithAdmittedOperationExecutors(
		&testTicketRedeemer{redemption: redemption},
		newTestClock(now),
		replay,
		AdmittedOperationExecutors{
			Trust:    unusedAdmittedTrustExecutor(t),
			Resolver: unusedAdmittedResolverExecutor(t),
			LowPorts: unusedAdmittedLowPortExecutor(t),
			Ownership: func(_ context.Context, admitted AdmittedOwnershipOperation) (OperationResult, error) {
				executorCalls++
				if replay.consumeCount() != 1 {
					t.Fatalf("replay consumption count at ownership executor = %d", replay.consumeCount())
				}
				return OperationResult{
					Operation: admitted.ticket.Operation,
					OwnershipEvidence: &OwnershipMutationEvidence{
						ReleaseOperationID:           admitted.ticket.ReleaseOperationID,
						ReleaseOperationRevision:     admitted.ticket.ReleaseOperationRevision,
						ReleaseCheckpointRevision:    admitted.ticket.ReleaseCheckpointRevision,
						ReleasedOwnershipFingerprint: admitted.ticket.ExpectedOwnershipFingerprint,
						Postcondition:                OwnershipPostconditionOwnedAbsent,
					},
				}, nil
			},
			Loopback: unusedAdmittedLoopbackExecutor(t),
		},
	)

	response, err := dispatcher.Dispatch(t.Context(), validTestRequest(reference))
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !response.OK || response.Result == nil || response.Result.OwnershipEvidence == nil {
		t.Fatalf("Dispatch() response = %#v", response)
	}
	if executorCalls != 1 || replay.consumeCount() != 1 {
		t.Fatalf("executor calls = %d, replay consumes = %d", executorCalls, replay.consumeCount())
	}
}

// TestAdmittedOwnershipOperationRejectsMismatchedEvidence proves executor callbacks cannot substitute another release checkpoint.
func TestAdmittedOwnershipOperationRejectsMismatchedEvidence(t *testing.T) {
	ticket := validTestOwnershipReleaseTicket(time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC))
	admission := TicketAdmission{
		TargetOwnershipFingerprint: ticket.ExpectedOwnershipFingerprint,
	}
	handler := ownershipHandlerFunc(func(context.Context, Ticket, TicketAdmission) (OwnershipMutationEvidence, error) {
		return OwnershipMutationEvidence{
			ReleaseOperationID:           ticket.ReleaseOperationID,
			ReleaseOperationRevision:     ticket.ReleaseOperationRevision,
			ReleaseCheckpointRevision:    ticket.ReleaseCheckpointRevision + 1,
			ReleasedOwnershipFingerprint: ticket.ExpectedOwnershipFingerprint,
			Postcondition:                OwnershipPostconditionOwnedAbsent,
		}, nil
	})
	if _, err := (AdmittedOwnershipOperation{ticket: ticket, admission: admission}).ExecuteOwnership(t.Context(), handler); err == nil {
		t.Fatal("ExecuteOwnership() accepted mismatched release evidence")
	}
}

// validTestOwnershipReleaseTicket returns a release ticket with no handler-selected mutation authority.
func validTestOwnershipReleaseTicket(now time.Time) Ticket {
	ticket := validTestResolverTicket(now, OperationReleaseNetworkOwnership)
	ticket.NetworkPolicy = nil
	ticket.ExpectedResolverObservation = nil
	ticket.ReleaseOperationID = "release-network-ownership"
	ticket.ReleaseOperationRevision = 1
	ticket.ReleaseCheckpointRevision = 2
	ticket.ExpectedOwnershipFingerprint = strings.Repeat("a", fingerprintLength)
	return ticket
}

// ownershipHandlerFunc adapts one focused ownership handler callback.
type ownershipHandlerFunc func(context.Context, Ticket, TicketAdmission) (OwnershipMutationEvidence, error)

// ReleaseNetworkOwnership invokes the configured focused handler callback.
func (handler ownershipHandlerFunc) ReleaseNetworkOwnership(ctx context.Context, ticket Ticket, admission TicketAdmission) (OwnershipMutationEvidence, error) {
	return handler(ctx, ticket, admission)
}

// unusedAdmittedTrustExecutor fails tests that route a release through trust execution.
func unusedAdmittedTrustExecutor(t *testing.T) AdmittedTrustExecutor {
	t.Helper()
	return func(context.Context, AdmittedTrustOperation) (OperationResult, error) {
		t.Fatal("trust executor unexpectedly called")
		return OperationResult{}, nil
	}
}

// unusedAdmittedResolverExecutor fails tests that route a release through resolver execution.
func unusedAdmittedResolverExecutor(t *testing.T) AdmittedResolverExecutor {
	t.Helper()
	return func(context.Context, AdmittedResolverOperation) (OperationResult, error) {
		t.Fatal("resolver executor unexpectedly called")
		return OperationResult{}, nil
	}
}

// unusedAdmittedLowPortExecutor fails tests that route a release through low-port execution.
func unusedAdmittedLowPortExecutor(t *testing.T) AdmittedLowPortExecutor {
	t.Helper()
	return func(context.Context, AdmittedLowPortOperation) (OperationResult, error) {
		t.Fatal("low-port executor unexpectedly called")
		return OperationResult{}, nil
	}
}

// unusedAdmittedLoopbackExecutor fails tests that route a release through loopback execution.
func unusedAdmittedLoopbackExecutor(t *testing.T) AdmittedLoopbackExecutor {
	t.Helper()
	return func(context.Context, AdmittedLoopbackOperation) (OperationResult, error) {
		t.Fatal("loopback executor unexpectedly called")
		return OperationResult{}, nil
	}
}
