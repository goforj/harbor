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

// TestAdvanceGlobalNetworkReleaseLoopbacksRequestValidate covers every caller-controlled receipt boundary.
func TestAdvanceGlobalNetworkReleaseLoopbacksRequestValidate(t *testing.T) {
	_, _, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
	valid := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks)
	for _, test := range []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseLoopbacksRequest)
	}{
		{
			name: "empty operation ID",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.OperationID = ""
			},
		},
		{
			name: "zero checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.CheckpointRevision = 0
			},
		},
		{
			name: "excessive checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.CheckpointRevision = domain.MaximumSequence + 1
			},
		},
		{
			name: "zero network revision",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.NetworkRevision = 0
			},
		},
		{
			name: "excessive network revision",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.NetworkRevision = domain.MaximumSequence + 1
			},
		},
		{
			name: "source mismatch",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "malformed evidence digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.LoopbackEvidenceDigest = "bad"
			},
		},
		{
			name: "uppercase evidence digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.LoopbackEvidenceDigest = strings.Repeat("A", 64)
			},
		},
		{
			name: "malformed observation digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.OwnedAbsentObservationDigest = "bad"
			},
		},
		{
			name: "uppercase observation digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.OwnedAbsentObservationDigest = strings.Repeat("B", 64)
			},
		},
		{
			name: "zero verification time",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.VerifiedAt = time.Time{}
			},
		},
		{
			name: "non UTC verification time",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
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

// TestAdvanceGlobalNetworkReleaseLoopbacksPersistsAndReplays proves loopback acknowledgement is an exact effect-verification boundary.
func TestAdvanceGlobalNetworkReleaseLoopbacksPersistsAndReplays(t *testing.T) {
	journal, connection, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
	request := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks)
	advanced, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), request)
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseLoopbacks() error = %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseVerifyEffects ||
		advanced.CheckpointRevision != loopbacks.CheckpointRevision+1 ||
		!reflect.DeepEqual(advanced.LoopbackReceipt, &request.Receipt) {
		t.Fatalf("advanced loopback plan = %#v", advanced)
	}
	if len(globalNetworkReleaseStageSnapshot(t, connection)["network_global_release_loopback_receipts"]) != 1 {
		t.Fatal("loopback receipt was not persisted")
	}
	replayed, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*GlobalNetworkReleaseLoopbackReceipt)
	}{
		{
			name: "source checkpoint",
			mutate: func(receipt *GlobalNetworkReleaseLoopbackReceipt) {
				receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "evidence digest",
			mutate: func(receipt *GlobalNetworkReleaseLoopbackReceipt) {
				receipt.LoopbackEvidenceDigest = strings.Repeat("c", 64)
			},
		},
		{
			name: "observation digest",
			mutate: func(receipt *GlobalNetworkReleaseLoopbackReceipt) {
				receipt.OwnedAbsentObservationDigest = strings.Repeat("d", 64)
			},
		},
		{
			name: "verification time",
			mutate: func(receipt *GlobalNetworkReleaseLoopbackReceipt) {
				receipt.VerifiedAt = receipt.VerifiedAt.Add(time.Nanosecond)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed.Receipt)
			if _, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), changed); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseLoopbacks() accepted changed replay receipt")
			}
		})
	}
	clone := advanced.Clone()
	clone.LoopbackReceipt.LoopbackEvidenceDigest = strings.Repeat("d", 64)
	if advanced.LoopbackReceipt.LoopbackEvidenceDigest == clone.LoopbackReceipt.LoopbackEvidenceDigest {
		t.Fatal("Clone() shared loopback receipt")
	}
}

// TestAdvanceGlobalNetworkReleaseLoopbacksRejectsMalformedInputs proves rejected acknowledgement does not mutate durable state.
func TestAdvanceGlobalNetworkReleaseLoopbacksRejectsMalformedInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseLoopbacksRequest)
	}{
		{
			name: "checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.CheckpointRevision++
			},
		},
		{
			name: "network revision",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.NetworkRevision++
			},
		},
		{
			name: "source checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "evidence digest",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.LoopbackEvidenceDigest = "bad"
			},
		},
		{
			name: "verification ordering",
			mutate: func(request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.Receipt.VerifiedAt = request.Receipt.VerifiedAt.Add(-1)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			request := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks)
			test.mutate(&request)
			if _, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseLoopbacks() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseLoopbacksRejectsInvalidDurablePredecessors keeps loopback acknowledgement behind its retained trust boundary.
func TestAdvanceGlobalNetworkReleaseLoopbacksRejectsInvalidDurablePredecessors(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*gorm.DB, *AdvanceGlobalNetworkReleaseLoopbacksRequest)
	}{
		{
			name: "wrong phase",
			configure: func(connection *gorm.DB, _ *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_plans SET phase = ? WHERE id = 1",
					GlobalNetworkReleasePlanPhaseTrust,
				)
			},
		},
		{
			name: "missing trust receipt",
			configure: func(connection *gorm.DB, _ *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				globalNetworkReleaseStageExec(t, connection, "DELETE FROM network_global_release_trust_receipts WHERE id = 1")
			},
		},
		{
			name: "trust verification before resolver receipt",
			configure: func(connection *gorm.DB, _ *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_trust_receipts SET verified_at = ? WHERE id = 1",
					"2000-01-01T00:00:00Z",
				)
			},
		},
		{
			name: "trust verification after loopback verification",
			configure: func(connection *gorm.DB, _ *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_trust_receipts SET verified_at = ? WHERE id = 1",
					"2030-01-01T00:00:00Z",
				)
			},
		},
		{
			name: "checkpoint mismatch",
			configure: func(_ *gorm.DB, request *AdvanceGlobalNetworkReleaseLoopbacksRequest) {
				request.CheckpointRevision++
				request.Receipt.SourceCheckpointRevision++
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			request := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks)
			test.configure(connection, &request)
			if _, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseLoopbacks() error = nil")
			}
			if test.name == "checkpoint mismatch" {
				globalNetworkReleaseStageAssertUnchanged(t, connection, before)
			}
		})
	}
}

// TestGlobalNetworkReleaseLoopbackReceiptFencesLaterPhases proves effect verification and later phases retain the ordered receipt.
func TestGlobalNetworkReleaseLoopbackReceiptFencesLaterPhases(t *testing.T) {
	for _, phase := range []GlobalNetworkReleasePlanPhase{
		GlobalNetworkReleasePlanPhaseOwnership,
		GlobalNetworkReleasePlanPhaseProjection,
	} {
		t.Run(string(phase), func(t *testing.T) {
			journal, _, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
			request := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks)
			advanced, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), request)
			if err != nil {
				t.Fatalf("advance loopbacks: %v", err)
			}
			effectsRequest := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, advanced)
			advanced, err = journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), effectsRequest)
			if err != nil {
				t.Fatalf("advance effects: %v", err)
			}
			if phase == GlobalNetworkReleasePlanPhaseProjection {
				ownershipRequest := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, advanced)
				advanced, err = journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), ownershipRequest)
				if err != nil {
					t.Fatalf("advance ownership: %v", err)
				}
			}
			plan, found, err := journal.ReadGlobalNetworkReleasePlan(t.Context(), stage.Operation.ID)
			if err != nil ||
				!found ||
				plan.LoopbackReceipt == nil ||
				*plan.LoopbackReceipt != request.Receipt ||
				plan.EffectsReceipt == nil ||
				*plan.EffectsReceipt != effectsRequest.Receipt ||
				(plan.Phase == GlobalNetworkReleasePlanPhaseProjection && plan.OwnershipReceipt == nil) ||
				(plan.Phase == GlobalNetworkReleasePlanPhaseProjection && plan.CheckpointRevision != advanced.CheckpointRevision) ||
				(plan.Phase == GlobalNetworkReleasePlanPhaseOwnership && plan.CheckpointRevision != advanced.CheckpointRevision) {
				t.Fatalf("later release plan = %#v, %t, %v", plan, found, err)
			}
		})
	}
}

// TestAdvanceGlobalNetworkReleaseLoopbacksIsCancellationAwareAndConcurrent proves admitted exact callers allocate only one checkpoint.
func TestAdvanceGlobalNetworkReleaseLoopbacksIsCancellationAwareAndConcurrent(t *testing.T) {
	journal, _, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
	request := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled advance error = %v", err)
	}
	const callers = 8
	var group sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(context.Background(), request)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent advance error = %v", err)
		}
	}
}

// TestAdvanceGlobalNetworkReleaseLoopbacksRollsBackLateFailures proves receipt creation and post-write validation share one transaction.
func TestAdvanceGlobalNetworkReleaseLoopbacksRollsBackLateFailures(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_global_release_loopback_receipt
			BEFORE INSERT ON network_global_release_loopback_receipts
			BEGIN
				SELECT RAISE(ABORT, 'forced receipt failure');
			END`,
		`CREATE TRIGGER invalidate_global_release_loopback_cas
			AFTER INSERT ON network_global_release_loopback_receipts
			BEGIN
				UPDATE network_global_release_plans
				SET checkpoint_revision = checkpoint_revision + 1
				WHERE id = 1;
			END`,
		`CREATE TRIGGER fail_global_release_loopback_cas
			BEFORE UPDATE OF phase ON network_global_release_plans
			WHEN NEW.phase = 'verify_effects'
			BEGIN
				SELECT RAISE(ABORT, 'forced compare-and-swap failure');
			END`,
		`CREATE TRIGGER corrupt_global_release_loopback_receipt
			AFTER UPDATE OF phase ON network_global_release_plans
			WHEN NEW.phase = 'verify_effects'
			BEGIN
				UPDATE network_global_release_loopback_receipts
				SET loopback_evidence_digest = 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
				WHERE id = 1;
			END`,
	} {
		t.Run(trigger[:32], func(t *testing.T) {
			journal, connection, stage, loopbacks := advanceGlobalNetworkReleaseLoopbackFixture(t)
			globalNetworkReleaseStageExec(t, connection, trigger)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			request := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, loopbacks)
			if _, err := journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseLoopbacks() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// validAdvanceGlobalNetworkReleaseLoopbacksRequest returns exact loopback checkpoint facts ordered after trust retirement.
func validAdvanceGlobalNetworkReleaseLoopbacksRequest(
	stage StageGlobalNetworkReleaseRequest,
	loopbacks GlobalNetworkReleasePlanRecord,
) AdvanceGlobalNetworkReleaseLoopbacksRequest {
	return AdvanceGlobalNetworkReleaseLoopbacksRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: loopbacks.CheckpointRevision,
		NetworkRevision:    stage.Authority.Projection.NetworkRevision,
		Receipt: GlobalNetworkReleaseLoopbackReceipt{
			SourceCheckpointRevision:     loopbacks.CheckpointRevision,
			LoopbackEvidenceDigest:       strings.Repeat("a", 64),
			OwnedAbsentObservationDigest: strings.Repeat("b", 64),
			VerifiedAt:                   loopbacks.TrustReceipt.VerifiedAt,
		},
	}
}

// advanceGlobalNetworkReleaseLoopbackFixture advances the release through trust retirement.
func advanceGlobalNetworkReleaseLoopbackFixture(
	t *testing.T,
) (*OperationJournal, *gorm.DB, StageGlobalNetworkReleaseRequest, GlobalNetworkReleasePlanRecord) {
	t.Helper()
	journal, connection, stage, trust := advanceGlobalNetworkReleaseTrustFixture(t)
	advanced, err := journal.AdvanceGlobalNetworkReleaseTrust(
		t.Context(),
		validAdvanceGlobalNetworkReleaseTrustRequest(
			stage,
			trust.CheckpointRevision,
			*trust.ResolverReceipt,
		),
	)
	if err != nil {
		t.Fatalf("advance trust: %v", err)
	}
	return journal, connection, stage, advanced
}
