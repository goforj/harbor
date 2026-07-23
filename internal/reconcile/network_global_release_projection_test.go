package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/state"
)

// AdvanceGlobalNetworkReleaseOwnership is unused by start-only release tests.
func (*testGlobalNetworkReleaseJournal) AdvanceGlobalNetworkReleaseOwnership(context.Context, state.AdvanceGlobalNetworkReleaseOwnershipRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected ownership advance")
}

// FinalizeGlobalNetworkReleaseProjection records terminal release recovery requests.
func (journal *testGlobalNetworkReleaseJournal) FinalizeGlobalNetworkReleaseProjection(_ context.Context, request state.FinalizeGlobalNetworkReleaseProjectionRequest) (state.OperationRecord, error) {
	journal.finalizeCalls++
	journal.finalizeRequest = request
	return journal.finalizeResult, journal.finalizeErr
}

// AdvanceGlobalNetworkReleaseOwnership is unused by low-port release tests.
func (*globalNetworkReleaseLowPortJournal) AdvanceGlobalNetworkReleaseOwnership(context.Context, state.AdvanceGlobalNetworkReleaseOwnershipRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected ownership advance")
}

// FinalizeGlobalNetworkReleaseProjection is unused by low-port release tests.
func (*globalNetworkReleaseLowPortJournal) FinalizeGlobalNetworkReleaseProjection(context.Context, state.FinalizeGlobalNetworkReleaseProjectionRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected projection finalization")
}

// AdvanceGlobalNetworkReleaseOwnership is unused by resolver release tests.
func (*globalNetworkReleaseResolverJournal) AdvanceGlobalNetworkReleaseOwnership(context.Context, state.AdvanceGlobalNetworkReleaseOwnershipRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected ownership advance")
}

// FinalizeGlobalNetworkReleaseProjection is unused by resolver release tests.
func (*globalNetworkReleaseResolverJournal) FinalizeGlobalNetworkReleaseProjection(context.Context, state.FinalizeGlobalNetworkReleaseProjectionRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected projection finalization")
}

// AdvanceGlobalNetworkReleaseOwnership is unused by trust release tests.
func (*globalNetworkReleaseTrustJournal) AdvanceGlobalNetworkReleaseOwnership(context.Context, state.AdvanceGlobalNetworkReleaseOwnershipRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected ownership advance")
}

// FinalizeGlobalNetworkReleaseProjection is unused by trust release tests.
func (*globalNetworkReleaseTrustJournal) FinalizeGlobalNetworkReleaseProjection(context.Context, state.FinalizeGlobalNetworkReleaseProjectionRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected projection finalization")
}

// AdvanceGlobalNetworkReleaseOwnership is unused by loopback release tests.
func (*globalNetworkReleaseLoopbackJournal) AdvanceGlobalNetworkReleaseOwnership(context.Context, state.AdvanceGlobalNetworkReleaseOwnershipRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected ownership advance")
}

// FinalizeGlobalNetworkReleaseProjection is unused by loopback release tests.
func (*globalNetworkReleaseLoopbackJournal) FinalizeGlobalNetworkReleaseProjection(context.Context, state.FinalizeGlobalNetworkReleaseProjectionRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected projection finalization")
}

// TestGlobalNetworkReleaseRecoverFinalizesProjection proves daemon recovery owns the retryable terminal projection transaction.
func TestGlobalNetworkReleaseRecoverFinalizesProjection(t *testing.T) {
	operation := testGlobalNetworkReleaseOperation(t)
	verifiedAt := operation.Operation.RequestedAt.Add(time.Minute)
	plan := state.GlobalNetworkReleasePlanRecord{
		Operation:          operation,
		Phase:              state.GlobalNetworkReleasePlanPhaseProjection,
		CheckpointRevision: 9,
		NetworkRevision:    3,
		OwnershipReceipt: &state.GlobalNetworkReleaseOwnershipReceipt{
			SourceCheckpointRevision:     8,
			ReleasedOwnershipFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			VerifiedAt:                   verifiedAt,
		},
	}
	completed := operation
	completed.Operation.State = "succeeded"
	journal := &testGlobalNetworkReleaseJournal{found: true, plan: plan, finalizeResult: completed}
	coordinator := &GlobalNetworkReleaseCoordinator{
		journal: journal,
		clock:   &globalNetworkReleaseClock{now: verifiedAt.Add(time.Second)},
	}
	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if journal.finalizeCalls != 1 {
		t.Fatalf("FinalizeGlobalNetworkReleaseProjection() calls = %d, want 1", journal.finalizeCalls)
	}
	if journal.finalizeRequest.OperationID != operation.Operation.ID ||
		journal.finalizeRequest.CheckpointRevision != plan.CheckpointRevision ||
		journal.finalizeRequest.NetworkRevision != plan.NetworkRevision ||
		!journal.finalizeRequest.At.Equal(verifiedAt.Add(time.Second)) {
		t.Fatalf("FinalizeGlobalNetworkReleaseProjection() request = %#v", journal.finalizeRequest)
	}
}
