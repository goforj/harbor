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
	"gorm.io/gorm"
)

// TestAdvanceGlobalNetworkReleaseResolverPersistsAndReplays proves the receipt and trust checkpoint are one durable, exact boundary.
func TestAdvanceGlobalNetworkReleaseResolverPersistsAndReplays(t *testing.T) {
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
	resolver, err := journal.AdvanceGlobalNetworkReleaseLowPorts(
		context.Background(),
		validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision),
	)
	if err != nil {
		t.Fatalf("advance low ports: %v", err)
	}
	request := validAdvanceGlobalNetworkReleaseResolverRequest(stage, resolver.CheckpointRevision)
	before := globalNetworkReleaseStageSnapshot(t, connection)
	advanced, err := journal.AdvanceGlobalNetworkReleaseResolver(context.Background(), request)
	if err != nil {
		t.Fatalf("advance resolver: %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseTrust ||
		advanced.CheckpointRevision != request.CheckpointRevision+1 ||
		!reflect.DeepEqual(advanced.ResolverReceipt, &request.Receipt) {
		t.Fatalf("advanced plan = %#v", advanced)
	}
	after := globalNetworkReleaseStageSnapshot(t, connection)
	if len(after["network_global_release_resolver_receipts"]) != 1 {
		t.Fatalf("resolver receipt rows = %#v", after["network_global_release_resolver_receipts"])
	}
	globalNetworkReleaseResolverAssertOnlyExpectedChange(t, before, after, request.CheckpointRevision)
	replayed, err := journal.AdvanceGlobalNetworkReleaseResolver(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, after)
}

// TestAdvanceGlobalNetworkReleaseResolverRejectsInvalidInputsWithoutMutation covers caller fencing and receipt validation.
func TestAdvanceGlobalNetworkReleaseResolverRejectsInvalidInputsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseResolverRequest, GlobalNetworkReleaseLowPortReceipt)
	}{
		{
			name: "operation",
			mutate: func(request *AdvanceGlobalNetworkReleaseResolverRequest, _ GlobalNetworkReleaseLowPortReceipt) {
				request.OperationID = "operation-foreign"
			},
		},
		{
			name: "checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseResolverRequest, _ GlobalNetworkReleaseLowPortReceipt) {
				request.CheckpointRevision++
			},
		},
		{
			name: "network",
			mutate: func(request *AdvanceGlobalNetworkReleaseResolverRequest, _ GlobalNetworkReleaseLowPortReceipt) {
				request.NetworkRevision++
			},
		},
		{
			name: "source",
			mutate: func(request *AdvanceGlobalNetworkReleaseResolverRequest, _ GlobalNetworkReleaseLowPortReceipt) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "evidence",
			mutate: func(request *AdvanceGlobalNetworkReleaseResolverRequest, _ GlobalNetworkReleaseLowPortReceipt) {
				request.Receipt.ResolverEvidenceDigest = "invalid"
			},
		},
		{
			name: "observation",
			mutate: func(request *AdvanceGlobalNetworkReleaseResolverRequest, _ GlobalNetworkReleaseLowPortReceipt) {
				request.Receipt.OwnedAbsentObservationFingerprint = "invalid"
			},
		},
		{
			name: "before low port",
			mutate: func(request *AdvanceGlobalNetworkReleaseResolverRequest, lowPort GlobalNetworkReleaseLowPortReceipt) {
				request.Receipt.VerifiedAt = lowPort.VerifiedAt.Add(-time.Nanosecond)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, resolver := advanceGlobalNetworkReleaseResolverFixture(t)
			request := validAdvanceGlobalNetworkReleaseResolverRequest(stage, resolver.CheckpointRevision)
			test.mutate(&request, *resolver.LowPortReceipt)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseResolver(context.Background(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseResolver() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseResolverRejectsCancellationAndCompetingWriters proves the admitted rail is cancellation-aware and idempotent under contention.
func TestAdvanceGlobalNetworkReleaseResolverRejectsCancellationAndCompetingWriters(t *testing.T) {
	journal, _, stage, resolver := advanceGlobalNetworkReleaseResolverFixture(t)
	request := validAdvanceGlobalNetworkReleaseResolverRequest(stage, resolver.CheckpointRevision)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := journal.AdvanceGlobalNetworkReleaseResolver(ctx, request); !errors.Is(err, context.Canceled) {
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
			result, err := journal.AdvanceGlobalNetworkReleaseResolver(context.Background(), request)
			results <- result
			errorsByCaller <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("concurrent advance error = %v", err)
		}
	}
	for result := range results {
		if result.Phase != GlobalNetworkReleasePlanPhaseTrust ||
			result.CheckpointRevision != request.CheckpointRevision+1 ||
			!reflect.DeepEqual(result.ResolverReceipt, &request.Receipt) {
			t.Fatalf("concurrent result = %#v", result)
		}
	}
}

// TestAdvanceGlobalNetworkReleaseResolverRollsBackLateFailures proves receipt creation, checkpoint allocation, and post-write validation share one transaction.
func TestAdvanceGlobalNetworkReleaseResolverRollsBackLateFailures(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_global_release_resolver_receipt BEFORE INSERT ON network_global_release_resolver_receipts BEGIN SELECT RAISE(ABORT, 'forced receipt failure'); END`,
		`CREATE TRIGGER corrupt_global_release_resolver_receipt AFTER UPDATE OF phase ON network_global_release_plans WHEN NEW.phase = 'trust' BEGIN UPDATE network_global_release_resolver_receipts SET resolver_evidence_digest = 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee' WHERE id = 1; END`,
	} {
		t.Run(trigger[:32], func(t *testing.T) {
			journal, connection, stage, resolver := advanceGlobalNetworkReleaseResolverFixture(t)
			globalNetworkReleaseStageExec(t, connection, trigger)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			request := validAdvanceGlobalNetworkReleaseResolverRequest(stage, resolver.CheckpointRevision)
			if _, err := journal.AdvanceGlobalNetworkReleaseResolver(context.Background(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseResolver() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// validAdvanceGlobalNetworkReleaseResolverRequest returns the exact resolver checkpoint and independent resolver release facts.
func validAdvanceGlobalNetworkReleaseResolverRequest(stage StageGlobalNetworkReleaseRequest, checkpoint domain.Sequence) AdvanceGlobalNetworkReleaseResolverRequest {
	return AdvanceGlobalNetworkReleaseResolverRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: checkpoint,
		NetworkRevision:    stage.Authority.Projection.NetworkRevision,
		Receipt: GlobalNetworkReleaseResolverReceipt{
			SourceCheckpointRevision:          checkpoint,
			ResolverEvidenceDigest:            strings.Repeat("c", 64),
			OwnedAbsentObservationFingerprint: strings.Repeat("d", 64),
			VerifiedAt:                        stage.Authority.Projection.NetworkUpdatedAt.Add(2 * time.Minute),
		},
	}
}

// advanceGlobalNetworkReleaseResolverFixture advances the release through the required low-port boundary.
func advanceGlobalNetworkReleaseResolverFixture(t *testing.T) (*OperationJournal, *gorm.DB, StageGlobalNetworkReleaseRequest, GlobalNetworkReleasePlanRecord) {
	t.Helper()
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
	resolver, err := journal.AdvanceGlobalNetworkReleaseLowPorts(
		context.Background(),
		validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision),
	)
	if err != nil {
		t.Fatalf("advance low ports: %v", err)
	}
	return journal, connection, stage, resolver
}

// globalNetworkReleaseResolverAssertOnlyExpectedChange verifies the atomic receipt/checkpoint mutation leaves all other durable rows intact.
func globalNetworkReleaseResolverAssertOnlyExpectedChange(t *testing.T, before map[string][]map[string]any, after map[string][]map[string]any, checkpoint domain.Sequence) {
	t.Helper()
	for table, beforeRows := range before {
		if table == "harbor_state" || table == "network_global_release_plans" || table == "network_global_release_resolver_receipts" {
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
