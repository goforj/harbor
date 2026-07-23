package state

import (
	"reflect"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestFinalizeGlobalNetworkReleaseProjectionRetiresTheAggregateAndOwnerTogether proves the final projection deletion, release cleanup, and terminal operation edge share one transaction.
func TestFinalizeGlobalNetworkReleaseProjectionRetiresTheAggregateAndOwnerTogether(t *testing.T) {
	journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	projection, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership))
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseOwnership() error = %v", err)
	}
	const unrelatedOperationID = "operation-unrelated"
	globalNetworkReleaseStageInsertOperation(
		t,
		connection,
		unrelatedOperationID,
		"intent-unrelated",
		"",
		domain.OperationKindNetworkSetup,
		domain.OperationSucceeded,
		projection.OwnershipReceipt.VerifiedAt,
	)
	completed, err := journal.FinalizeGlobalNetworkReleaseProjection(t.Context(), FinalizeGlobalNetworkReleaseProjectionRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: projection.CheckpointRevision,
		NetworkRevision:    projection.NetworkRevision,
		At:                 projection.OwnershipReceipt.VerifiedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("FinalizeGlobalNetworkReleaseProjection() error = %v", err)
	}
	if completed.Operation.State != domain.OperationSucceeded || completed.Operation.Phase != globalNetworkReleaseSucceededPhase {
		t.Fatalf("completed operation = %#v", completed)
	}
	for _, table := range []string{
		"network_state",
		"network_pool_candidates",
		"network_setup_evidence",
		"network_shared_listeners",
		"loopback_address_leases",
		"public_endpoint_leases",
		"network_project_releases",
		"machine_ownership_projections",
		"network_global_release_plans",
		"network_global_release_low_port_receipts",
		"network_global_release_resolver_receipts",
		"network_global_release_trust_receipts",
		"network_global_release_loopback_receipts",
		"network_global_release_effects_receipts",
		"network_global_release_ownership_receipts",
	} {
		if got := globalNetworkReleaseStageSnapshot(t, connection)[table]; len(got) != 0 {
			t.Fatalf("%s rows = %#v", table, got)
		}
	}
	if _, found, err := journal.ReadGlobalNetworkReleasePlan(t.Context(), stage.Operation.ID); err != nil || found {
		t.Fatalf("ReadGlobalNetworkReleasePlan() = found %v, err %v", found, err)
	}
	if operation, err := journal.Operation(t.Context(), unrelatedOperationID); err != nil ||
		operation.Operation.Kind != domain.OperationKindNetworkSetup ||
		operation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("unrelated operation = %#v, err %v", operation, err)
	}
	terminal, found, err := journal.ReadGlobalNetworkReleaseTerminal(t.Context(), stage.Operation.ID)
	if err != nil || !found {
		t.Fatalf("ReadGlobalNetworkReleaseTerminal() = %#v, found %v, err %v", terminal, found, err)
	}
	if !sameGlobalNetworkReleaseTerminalOperation(terminal.Operation, completed) ||
		terminal.OwnerIdentity != projection.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity ||
		terminal.SourceCheckpointRevision != projection.OwnershipReceipt.SourceCheckpointRevision ||
		terminal.NetworkRevision != projection.NetworkRevision {
		t.Fatalf("terminal = %#v", terminal)
	}
}

// TestFinalizeGlobalNetworkReleaseProjectionRejectsCompletionBeforeOwnershipReceipt proves terminal history cannot predate its final release proof.
func TestFinalizeGlobalNetworkReleaseProjectionRejectsCompletionBeforeOwnershipReceipt(t *testing.T) {
	journal, _, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	projection, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership))
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseOwnership() error = %v", err)
	}
	_, err = journal.FinalizeGlobalNetworkReleaseProjection(t.Context(), FinalizeGlobalNetworkReleaseProjectionRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: projection.CheckpointRevision,
		NetworkRevision:    projection.NetworkRevision,
		At:                 projection.OwnershipReceipt.VerifiedAt.Add(-time.Second),
	})
	if err == nil {
		t.Fatal("FinalizeGlobalNetworkReleaseProjection() error = nil")
	}
}

// TestReadGlobalNetworkReleaseTerminalRejectsMissingSucceededRecord proves completed release history cannot lose its replay fence.
func TestReadGlobalNetworkReleaseTerminalRejectsMissingSucceededRecord(t *testing.T) {
	journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	projection, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership))
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseOwnership() error = %v", err)
	}
	_, err = journal.FinalizeGlobalNetworkReleaseProjection(t.Context(), FinalizeGlobalNetworkReleaseProjectionRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: projection.CheckpointRevision,
		NetworkRevision:    projection.NetworkRevision,
		At:                 projection.OwnershipReceipt.VerifiedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("FinalizeGlobalNetworkReleaseProjection() error = %v", err)
	}
	globalNetworkReleaseStageExec(t, connection, "DELETE FROM network_global_release_terminals WHERE operation_id = ?", string(stage.Operation.ID))
	if _, _, err := journal.ReadGlobalNetworkReleaseTerminal(t.Context(), stage.Operation.ID); err == nil {
		t.Fatal("ReadGlobalNetworkReleaseTerminal() error = nil")
	}
}

// TestFinalizeGlobalNetworkReleaseProjectionRollsBackDeletionFailure proves an active projection-phase plan is retained whenever final deletion cannot commit.
func TestFinalizeGlobalNetworkReleaseProjectionRollsBackDeletionFailure(t *testing.T) {
	journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	projection, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership))
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseOwnership() error = %v", err)
	}
	before := globalNetworkReleaseStageSnapshot(t, connection)
	globalNetworkReleaseStageExec(t, connection, "CREATE TRIGGER fail_global_release_projection_delete BEFORE DELETE ON network_state BEGIN SELECT RAISE(ABORT, 'forced projection deletion failure'); END")
	_, err = journal.FinalizeGlobalNetworkReleaseProjection(t.Context(), FinalizeGlobalNetworkReleaseProjectionRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: projection.CheckpointRevision,
		NetworkRevision:    projection.NetworkRevision,
		At:                 projection.OwnershipReceipt.VerifiedAt.Add(time.Second),
	})
	if err == nil {
		t.Fatal("FinalizeGlobalNetworkReleaseProjection() error = nil")
	}
	after := globalNetworkReleaseStageSnapshot(t, connection)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rollback snapshot differs\nbefore: %#v\nafter: %#v", before, after)
	}
	plan, found, err := journal.ReadGlobalNetworkReleasePlan(t.Context(), stage.Operation.ID)
	if err != nil || !found || plan.Phase != GlobalNetworkReleasePlanPhaseProjection {
		t.Fatalf("active projection plan = %#v, found %v, err %v", plan, found, err)
	}
}
