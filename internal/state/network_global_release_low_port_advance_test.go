package state

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestAdvanceGlobalNetworkReleaseLowPortsPersistsAndReplays proves the receipt and resolver checkpoint are one durable, exact boundary.
func TestAdvanceGlobalNetworkReleaseLowPortsPersistsAndReplays(t *testing.T) {
	journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	lowPorts, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision))
	if err != nil {
		t.Fatalf("advance runtime: %v", err)
	}
	request := validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision)
	before := globalNetworkReleaseStageSnapshot(t, connection)
	advanced, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request)
	if err != nil {
		t.Fatalf("advance low ports: %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseResolver || advanced.CheckpointRevision != request.CheckpointRevision+1 || !reflect.DeepEqual(advanced.LowPortReceipt, &request.Receipt) {
		t.Fatalf("advanced plan = %#v", advanced)
	}
	after := globalNetworkReleaseStageSnapshot(t, connection)
	if len(after["network_global_release_low_port_receipts"]) != 1 {
		t.Fatalf("low-port receipt rows = %#v", after["network_global_release_low_port_receipts"])
	}
	globalNetworkReleaseLowPortAssertOnlyExpectedChange(t, before, after, request.CheckpointRevision)
	replayed, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, after)
}

// TestAdvanceGlobalNetworkReleaseLowPortsRejectsUnfencedOrMalformedInputsWithoutMutation covers every caller-supplied receipt boundary.
func TestAdvanceGlobalNetworkReleaseLowPortsRejectsUnfencedOrMalformedInputsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseLowPortsRequest)
	}{
		{
			name: "operation",
			mutate: func(request *AdvanceGlobalNetworkReleaseLowPortsRequest) {
				request.OperationID = "operation-foreign"
			},
		},
		{
			name: "checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseLowPortsRequest) {
				request.CheckpointRevision++
			},
		},
		{
			name: "network",
			mutate: func(request *AdvanceGlobalNetworkReleaseLowPortsRequest) {
				request.NetworkRevision++
			},
		},
		{
			name: "receipt source",
			mutate: func(request *AdvanceGlobalNetworkReleaseLowPortsRequest) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "evidence",
			mutate: func(request *AdvanceGlobalNetworkReleaseLowPortsRequest) {
				request.Receipt.LowPortEvidenceDigest = "invalid"
			},
		},
		{
			name: "observation",
			mutate: func(request *AdvanceGlobalNetworkReleaseLowPortsRequest) {
				request.Receipt.OwnedAbsentObservationFingerprint = "invalid"
			},
		},
		{
			name: "verification",
			mutate: func(request *AdvanceGlobalNetworkReleaseLowPortsRequest) {
				request.Receipt.VerifiedAt = request.Receipt.VerifiedAt.Add(-time.Hour)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
			staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
			if err != nil {
				t.Fatalf("stage global network release: %v", err)
			}
			lowPorts, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision))
			if err != nil {
				t.Fatalf("advance runtime: %v", err)
			}
			request := validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision)
			test.mutate(&request)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseLowPorts() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseLowPortsRejectsWrongPhaseAndReceiptCorruption verifies persisted phase and receipt invariants fail closed.
func TestAdvanceGlobalNetworkReleaseLowPortsRejectsWrongPhaseAndReceiptCorruption(t *testing.T) {
	journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	request := validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, staged.Revision)
	before := globalNetworkReleaseStageSnapshot(t, connection)
	if _, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request); err == nil {
		t.Fatal("runtime-phase low-port advance error = nil")
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
	lowPorts, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision))
	if err != nil {
		t.Fatalf("advance runtime: %v", err)
	}
	request = validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision)
	if _, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request); err != nil {
		t.Fatalf("advance low ports: %v", err)
	}
	globalNetworkReleaseStageExec(
		t,
		connection,
		"UPDATE network_global_release_low_port_receipts SET owned_absent_observation_fingerprint = ? WHERE id = 1",
		strings.Repeat("c", 64),
	)
	before = globalNetworkReleaseStageSnapshot(t, connection)
	if _, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request); err == nil {
		t.Fatal("corrupt receipt replay error = nil")
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestGlobalNetworkReleaseLowPortReceiptFencesLaterPhases preserves the resolver receipt while later checkpoints advance.
func TestGlobalNetworkReleaseLowPortReceiptFencesLaterPhases(t *testing.T) {
	for _, test := range []struct {
		name              string
		advanceCheckpoint bool
		wantError         bool
	}{
		{
			name:      "phase without checkpoint",
			wantError: true,
		},
		{
			name:              "later checkpoint",
			advanceCheckpoint: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
			staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
			if err != nil {
				t.Fatalf("stage global network release: %v", err)
			}
			lowPorts, err := journal.AdvanceGlobalNetworkReleaseRuntime(
				context.Background(),
				validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision),
			)
			if err != nil {
				t.Fatalf("advance runtime: %v", err)
			}
			request := validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision)
			if _, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request); err != nil {
				t.Fatalf("advance low ports: %v", err)
			}
			resolverRequest := AdvanceGlobalNetworkReleaseResolverRequest{}
			trustRequest := AdvanceGlobalNetworkReleaseTrustRequest{}
			if test.advanceCheckpoint {
				resolverRequest = validAdvanceGlobalNetworkReleaseResolverRequest(stage, request.CheckpointRevision+1)
				trust, err := journal.AdvanceGlobalNetworkReleaseResolver(context.Background(), resolverRequest)
				if err != nil {
					t.Fatalf("advance resolver: %v", err)
				}
				trustRequest = validAdvanceGlobalNetworkReleaseTrustRequest(
					stage,
					trust.CheckpointRevision,
					*trust.ResolverReceipt,
				)
				if _, err := journal.AdvanceGlobalNetworkReleaseTrust(context.Background(), trustRequest); err != nil {
					t.Fatalf("advance trust: %v", err)
				}
			} else {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_plans SET phase = ? WHERE id = 1",
					GlobalNetworkReleasePlanPhaseTrust,
				)
			}

			plan, found, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), stage.Operation.ID)
			if test.wantError {
				if err == nil {
					t.Fatal("ReadGlobalNetworkReleasePlan() error = nil")
				}
				return
			}
			if err != nil || !found {
				t.Fatalf("ReadGlobalNetworkReleasePlan() = %#v, %t, %v", plan, found, err)
			}
			if plan.Phase != GlobalNetworkReleasePlanPhaseLoopbacks ||
				plan.LowPortReceipt == nil ||
				*plan.LowPortReceipt != request.Receipt ||
				plan.ResolverReceipt == nil ||
				*plan.ResolverReceipt != resolverRequest.Receipt ||
				plan.TrustReceipt == nil ||
				*plan.TrustReceipt != trustRequest.Receipt {
				t.Fatalf("later release plan = %#v", plan)
			}
		})
	}
}

// TestAdvanceGlobalNetworkReleaseLowPortsRejectsCancellationAndCompetingWriters proves the admitted rail is cancellation-aware and idempotent under contention.
func TestAdvanceGlobalNetworkReleaseLowPortsRejectsCancellationAndCompetingWriters(t *testing.T) {
	journal, _, stage := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	lowPorts, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision))
	if err != nil {
		t.Fatalf("advance runtime: %v", err)
	}
	request := validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := journal.AdvanceGlobalNetworkReleaseLowPorts(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled advance error = %v", err)
	}
	const callers = 8
	results := make(chan GlobalNetworkReleasePlanRecord, callers)
	errorsByCaller := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, callErr := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), request)
			results <- result
			errorsByCaller <- callErr
		}()
	}
	group.Wait()
	close(results)
	close(errorsByCaller)
	for callErr := range errorsByCaller {
		if callErr != nil {
			t.Fatalf("concurrent advance error = %v", callErr)
		}
	}
	for result := range results {
		if result.Phase != GlobalNetworkReleasePlanPhaseResolver || result.CheckpointRevision != request.CheckpointRevision+1 || !reflect.DeepEqual(result.LowPortReceipt, &request.Receipt) {
			t.Fatalf("concurrent result = %#v", result)
		}
	}
}

// TestAdvanceGlobalNetworkReleaseLowPortsRollsBackLateFailures proves receipt creation, checkpoint allocation, and post-write validation share one transaction.
func TestAdvanceGlobalNetworkReleaseLowPortsRollsBackLateFailures(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{
			name: "receipt write",
			trigger: `CREATE TRIGGER fail_global_release_low_port_receipt
				BEFORE INSERT ON network_global_release_low_port_receipts
				BEGIN SELECT RAISE(ABORT, 'forced receipt failure'); END`,
		},
		{
			name: "plan update",
			trigger: `CREATE TRIGGER fail_global_release_low_port_plan
				BEFORE UPDATE OF phase ON network_global_release_plans
				WHEN NEW.phase = 'resolver'
				BEGIN SELECT RAISE(ABORT, 'forced plan failure'); END`,
		},
		{
			name: "post validation",
			trigger: `CREATE TRIGGER corrupt_global_release_low_port_receipt
				AFTER UPDATE OF phase ON network_global_release_plans
				WHEN NEW.phase = 'resolver'
				BEGIN UPDATE network_global_release_low_port_receipts
					SET low_port_evidence_digest = 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
					WHERE id = 1; END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
			staged, err := journal.StageGlobalNetworkRelease(context.Background(), stage)
			if err != nil {
				t.Fatalf("stage global network release: %v", err)
			}
			lowPorts, err := journal.AdvanceGlobalNetworkReleaseRuntime(context.Background(), validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision))
			if err != nil {
				t.Fatalf("advance runtime: %v", err)
			}
			globalNetworkReleaseStageExec(t, connection, test.trigger)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseLowPorts(context.Background(), validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision)); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseLowPorts() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// validAdvanceGlobalNetworkReleaseLowPortsRequest returns the exact runtime checkpoint and independent low-port release facts.
func validAdvanceGlobalNetworkReleaseLowPortsRequest(stage StageGlobalNetworkReleaseRequest, checkpoint domain.Sequence) AdvanceGlobalNetworkReleaseLowPortsRequest {
	return AdvanceGlobalNetworkReleaseLowPortsRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: checkpoint,
		NetworkRevision:    stage.Authority.Projection.NetworkRevision,
		Receipt: GlobalNetworkReleaseLowPortReceipt{
			SourceCheckpointRevision:          checkpoint,
			LowPortEvidenceDigest:             strings.Repeat("a", 64),
			OwnedAbsentObservationFingerprint: strings.Repeat("b", 64),
			VerifiedAt:                        stage.Authority.Projection.NetworkUpdatedAt.Add(time.Minute),
		},
	}
}

// globalNetworkReleaseLowPortAssertOnlyExpectedChange verifies the atomic receipt/checkpoint mutation leaves all other durable rows intact.
func globalNetworkReleaseLowPortAssertOnlyExpectedChange(t *testing.T, before map[string][]map[string]any, after map[string][]map[string]any, checkpoint domain.Sequence) {
	t.Helper()
	for table, beforeRows := range before {
		if table == "harbor_state" || table == "network_global_release_plans" || table == "network_global_release_low_port_receipts" {
			continue
		}
		if !reflect.DeepEqual(beforeRows, after[table]) {
			t.Fatalf("table %s changed\nbefore: %#v\nafter: %#v", table, beforeRows, after[table])
		}
	}
	if after["harbor_state"][0]["sequence"] != int64(checkpoint+1) {
		t.Fatalf("sequence row = %#v", after["harbor_state"])
	}
}
