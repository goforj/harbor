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

// TestAdvanceGlobalNetworkReleaseEffectsRequestValidate covers every caller-controlled effects receipt boundary.
func TestAdvanceGlobalNetworkReleaseEffectsRequestValidate(t *testing.T) {
	_, _, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
	valid := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects)
	for _, test := range []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseEffectsRequest)
	}{
		{
			name: "empty operation ID",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.OperationID = ""
			},
		},
		{
			name: "zero checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.CheckpointRevision = 0
			},
		},
		{
			name: "excessive checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.CheckpointRevision = domain.MaximumSequence + 1
			},
		},
		{
			name: "zero network revision",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.NetworkRevision = 0
			},
		},
		{
			name: "excessive network revision",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.NetworkRevision = domain.MaximumSequence + 1
			},
		},
		{
			name: "source mismatch",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "malformed runtime digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.RuntimeObservationDigest = "bad"
			},
		},
		{
			name: "uppercase runtime digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.RuntimeObservationDigest = strings.Repeat("A", 64)
			},
		},
		{
			name: "malformed ownership fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.OwnershipObservationFingerprint = "bad"
			},
		},
		{
			name: "malformed resolver fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.ResolverObservationFingerprint = "bad"
			},
		},
		{
			name: "malformed trust fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.TrustObservationFingerprint = "bad"
			},
		},
		{
			name: "malformed loopback digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.LoopbackObservationDigest = "bad"
			},
		},
		{
			name: "malformed low-port fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.LowPortObservationFingerprint = "bad"
			},
		},
		{
			name: "uppercase low-port fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.LowPortObservationFingerprint = strings.Repeat("B", 64)
			},
		},
		{
			name: "zero verification time",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.VerifiedAt = time.Time{}
			},
		},
		{
			name: "non UTC verification time",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest) {
				request.Receipt.VerifiedAt = time.Date(2026, time.July, 22, 12, 0, 0, 0, time.FixedZone("offset", 3600))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// TestAdvanceGlobalNetworkReleaseEffectsPersistsAndReplays proves a successful compare-and-swap retains only the exact receipt and ownership checkpoint.
func TestAdvanceGlobalNetworkReleaseEffectsPersistsAndReplays(t *testing.T) {
	journal, connection, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
	request := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects)
	before := globalNetworkReleaseStageSnapshot(t, connection)
	advanced, err := journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), request)
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseEffects() error = %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseOwnership ||
		advanced.CheckpointRevision != effects.CheckpointRevision+1 ||
		!reflect.DeepEqual(advanced.EffectsReceipt, &request.Receipt) {
		t.Fatalf("advanced effects plan = %#v", advanced)
	}
	after := globalNetworkReleaseStageSnapshot(t, connection)
	if len(after["network_global_release_effects_receipts"]) != 1 {
		t.Fatalf("effects receipt rows = %#v", after["network_global_release_effects_receipts"])
	}
	for table, rows := range before {
		if table == "harbor_state" ||
			table == "network_global_release_plans" ||
			table == "network_global_release_effects_receipts" {
			continue
		}
		if !reflect.DeepEqual(rows, after[table]) {
			t.Fatalf("table %s changed\nbefore: %#v\nafter: %#v", table, rows, after[table])
		}
	}
	replayed, err := journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, after)
	for _, mutate := range []func(*GlobalNetworkReleaseEffectsReceipt){
		func(receipt *GlobalNetworkReleaseEffectsReceipt) { receipt.SourceCheckpointRevision++ },
		func(receipt *GlobalNetworkReleaseEffectsReceipt) {
			receipt.RuntimeObservationDigest = strings.Repeat("c", 64)
		},
		func(receipt *GlobalNetworkReleaseEffectsReceipt) {
			receipt.OwnershipObservationFingerprint = strings.Repeat("d", 64)
		},
		func(receipt *GlobalNetworkReleaseEffectsReceipt) {
			receipt.LowPortObservationFingerprint = strings.Repeat("d", 64)
		},
		func(receipt *GlobalNetworkReleaseEffectsReceipt) {
			receipt.ResolverObservationFingerprint = strings.Repeat("e", 64)
		},
		func(receipt *GlobalNetworkReleaseEffectsReceipt) {
			receipt.TrustObservationFingerprint = strings.Repeat("f", 64)
		},
		func(receipt *GlobalNetworkReleaseEffectsReceipt) {
			receipt.LoopbackObservationDigest = strings.Repeat("a", 64)
		},
		func(receipt *GlobalNetworkReleaseEffectsReceipt) {
			receipt.VerifiedAt = receipt.VerifiedAt.Add(time.Nanosecond)
		},
	} {
		changed := request
		mutate(&changed.Receipt)
		if _, err := journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), changed); err == nil {
			t.Fatal("AdvanceGlobalNetworkReleaseEffects() accepted changed replay receipt")
		}
	}
	clone := advanced.Clone()
	clone.EffectsReceipt.RuntimeObservationDigest = strings.Repeat("d", 64)
	if advanced.EffectsReceipt.RuntimeObservationDigest == clone.EffectsReceipt.RuntimeObservationDigest {
		t.Fatal("Clone() shared effects receipt")
	}
}

// TestAdvanceGlobalNetworkReleaseEffectsRejectsDriftWithoutMutation proves stale authority, receipt drift, and time ordering cannot write a receipt.
func TestAdvanceGlobalNetworkReleaseEffectsRejectsDriftWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseEffectsRequest, GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "operation",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.OperationID = "operation-foreign"
			},
		},
		{
			name: "checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.CheckpointRevision++
			},
		},
		{
			name: "network revision",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.NetworkRevision++
			},
		},
		{
			name: "receipt source",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "receipt runtime digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.Receipt.RuntimeObservationDigest = "bad"
			},
		},
		{
			name: "receipt ownership fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.Receipt.OwnershipObservationFingerprint = "bad"
			},
		},
		{
			name: "receipt ownership fingerprint differs from retained authority",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.Receipt.OwnershipObservationFingerprint = strings.Repeat("a", 64)
			},
		},
		{
			name: "receipt low-port fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, _ GlobalNetworkReleasePlanRecord) {
				request.Receipt.LowPortObservationFingerprint = "bad"
			},
		},
		{
			name: "before loopback",
			mutate: func(request *AdvanceGlobalNetworkReleaseEffectsRequest, plan GlobalNetworkReleasePlanRecord) {
				request.Receipt.VerifiedAt = plan.LoopbackReceipt.VerifiedAt.Add(-time.Nanosecond)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
			request := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects)
			test.mutate(&request, effects)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseEffects() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseEffectsRejectsWrongPhaseAndPredecessorCorruption keeps ownership behind exact verify-effects evidence.
func TestAdvanceGlobalNetworkReleaseEffectsRejectsWrongPhaseAndPredecessorCorruption(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*gorm.DB)
	}{
		{
			name: "wrong phase",
			configure: func(connection *gorm.DB) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_plans SET phase = ? WHERE id = 1",
					GlobalNetworkReleasePlanPhaseLoopbacks,
				)
			},
		},
		{
			name: "missing loopback receipt",
			configure: func(connection *gorm.DB) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"DELETE FROM network_global_release_loopback_receipts WHERE id = 1",
				)
			},
		},
		{
			name: "loopback receipt precedes trust",
			configure: func(connection *gorm.DB) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_loopback_receipts SET verified_at = ? WHERE id = 1",
					"2000-01-01T00:00:00Z",
				)
			},
		},
		{
			name: "checkpoint receipt drift",
			configure: func(connection *gorm.DB) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_plans SET checkpoint_revision = checkpoint_revision + 1 WHERE id = 1",
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
			test.configure(connection)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseEffects(
				t.Context(),
				validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects),
			); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseEffects() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestGlobalNetworkReleaseEffectsReceiptFencesLaterPhases proves later projection work retains an ordered effect-verification receipt.
func TestGlobalNetworkReleaseEffectsReceiptFencesLaterPhases(t *testing.T) {
	journal, _, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
	request := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects)
	advanced, err := journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), request)
	if err != nil {
		t.Fatalf("advance effects: %v", err)
	}
	ownershipRequest := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, advanced)
	advanced, err = journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), ownershipRequest)
	if err != nil {
		t.Fatalf("advance ownership: %v", err)
	}
	plan, found, err := journal.ReadGlobalNetworkReleasePlan(t.Context(), stage.Operation.ID)
	if err != nil ||
		!found ||
		plan.EffectsReceipt == nil ||
		*plan.EffectsReceipt != request.Receipt ||
		plan.OwnershipReceipt == nil ||
		*plan.OwnershipReceipt != ownershipRequest.Receipt ||
		plan.CheckpointRevision != advanced.CheckpointRevision {
		t.Fatalf("later release plan = %#v, %t, %v", plan, found, err)
	}
}

// TestGlobalNetworkReleaseEffectsReceiptRejectsDurableOwnershipFingerprintDrift proves retained effects evidence remains bound to staged ownership authority.
func TestGlobalNetworkReleaseEffectsReceiptRejectsDurableOwnershipFingerprintDrift(t *testing.T) {
	journal, connection, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
	request := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects)
	if _, err := journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), request); err != nil {
		t.Fatalf("advance effects: %v", err)
	}
	globalNetworkReleaseStageExec(
		t,
		connection,
		"UPDATE network_global_release_effects_receipts SET ownership_observation_fingerprint = ? WHERE id = 1",
		strings.Repeat("a", 64),
	)
	if _, found, err := journal.ReadGlobalNetworkReleasePlan(t.Context(), stage.Operation.ID); err == nil || found {
		t.Fatalf("ReadGlobalNetworkReleasePlan() = found %t, error %v", found, err)
	}
}

// TestAdvanceGlobalNetworkReleaseEffectsIsCancellationAwareAndConcurrent proves exact competing callers allocate one checkpoint.
func TestAdvanceGlobalNetworkReleaseEffectsIsCancellationAwareAndConcurrent(t *testing.T) {
	journal, connection, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
	request := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := journal.AdvanceGlobalNetworkReleaseEffects(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled advance error = %v", err)
	}
	before := globalNetworkReleaseStageSnapshot(t, connection)
	const callers = 8
	var group sync.WaitGroup
	results := make(chan GlobalNetworkReleasePlanRecord, callers)
	errs := make(chan error, callers)
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := journal.AdvanceGlobalNetworkReleaseEffects(context.Background(), request)
			results <- result
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent advance error = %v", err)
		}
	}
	var expected GlobalNetworkReleasePlanRecord
	foundExpected := false
	for result := range results {
		if result.Phase != GlobalNetworkReleasePlanPhaseOwnership ||
			result.CheckpointRevision != request.CheckpointRevision+1 ||
			!reflect.DeepEqual(result.EffectsReceipt, &request.Receipt) {
			t.Fatalf("concurrent advance result = %#v", result)
		}
		if !foundExpected {
			expected = result
			foundExpected = true
			continue
		}
		if !reflect.DeepEqual(result, expected) {
			t.Fatalf("concurrent result = %#v, want %#v", result, expected)
		}
	}
	plan, found, err := journal.ReadGlobalNetworkReleasePlan(t.Context(), stage.Operation.ID)
	if err != nil ||
		!found ||
		plan.Phase != GlobalNetworkReleasePlanPhaseOwnership ||
		plan.CheckpointRevision != request.CheckpointRevision+1 ||
		!reflect.DeepEqual(plan.EffectsReceipt, &request.Receipt) {
		t.Fatalf("durable concurrent plan = %#v, %t, %v", plan, found, err)
	}
	after := globalNetworkReleaseStageSnapshot(t, connection)
	if len(after["network_global_release_effects_receipts"]) != 1 {
		t.Fatal("concurrent callers persisted more than one effects receipt")
	}
	if after["harbor_state"][0]["sequence"] != int64(request.CheckpointRevision+1) {
		t.Fatalf("concurrent sequence = %#v, want %d", after["harbor_state"], request.CheckpointRevision+1)
	}
	if before["harbor_state"][0]["sequence"] != int64(request.CheckpointRevision) {
		t.Fatalf("pre-concurrency sequence = %#v, want %d", before["harbor_state"], request.CheckpointRevision)
	}
}

// TestAdvanceGlobalNetworkReleaseEffectsRollsBackLateFailures proves receipt, compare-and-swap, and readback validation share one transaction.
func TestAdvanceGlobalNetworkReleaseEffectsRollsBackLateFailures(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_global_release_effects_receipt BEFORE INSERT ON network_global_release_effects_receipts BEGIN SELECT RAISE(ABORT, 'forced receipt failure'); END`,
		`CREATE TRIGGER fail_global_release_effects_cas BEFORE UPDATE OF phase ON network_global_release_plans WHEN NEW.phase = 'ownership' BEGIN SELECT RAISE(ABORT, 'forced compare-and-swap failure'); END`,
		`CREATE TRIGGER corrupt_global_release_effects_receipt AFTER UPDATE OF phase ON network_global_release_plans WHEN NEW.phase = 'ownership' BEGIN UPDATE network_global_release_effects_receipts SET runtime_observation_digest = 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' WHERE id = 1; END`,
	} {
		t.Run(trigger[:32], func(t *testing.T) {
			journal, connection, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
			globalNetworkReleaseStageExec(t, connection, trigger)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseEffects(
				t.Context(),
				validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects),
			); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseEffects() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// validAdvanceGlobalNetworkReleaseEffectsRequest returns exact effect-verification facts ordered after loopback release.
func validAdvanceGlobalNetworkReleaseEffectsRequest(
	stage StageGlobalNetworkReleaseRequest,
	effects GlobalNetworkReleasePlanRecord,
) AdvanceGlobalNetworkReleaseEffectsRequest {
	return AdvanceGlobalNetworkReleaseEffectsRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: effects.CheckpointRevision,
		NetworkRevision:    stage.Authority.Projection.NetworkRevision,
		Receipt: GlobalNetworkReleaseEffectsReceipt{
			SourceCheckpointRevision:        effects.CheckpointRevision,
			RuntimeObservationDigest:        strings.Repeat("a", 64),
			OwnershipObservationFingerprint: effects.Authority.ExpectedOwnershipFingerprint,
			LowPortObservationFingerprint:   strings.Repeat("c", 64),
			ResolverObservationFingerprint:  strings.Repeat("d", 64),
			TrustObservationFingerprint:     strings.Repeat("e", 64),
			LoopbackObservationDigest:       strings.Repeat("f", 64),
			VerifiedAt:                      effects.LoopbackReceipt.VerifiedAt,
		},
	}
}

// advanceGlobalNetworkReleaseEffectsFixture advances the release through loopback retirement.
func advanceGlobalNetworkReleaseEffectsFixture(t *testing.T) (*OperationJournal, *gorm.DB, StageGlobalNetworkReleaseRequest, GlobalNetworkReleasePlanRecord) {
	t.Helper()
	journal, connection, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
	advanced, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(
		t.Context(),
		validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks),
	)
	if err != nil {
		t.Fatalf("advance loopbacks: %v", err)
	}
	return journal, connection, stage, advanced
}
