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
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"gorm.io/gorm"
)

// TestAdvanceGlobalNetworkReleaseTrustPersistsAndReplays proves trust acknowledgement is an exact loopback checkpoint boundary.
func TestAdvanceGlobalNetworkReleaseTrustPersistsAndReplays(t *testing.T) {
	journal, connection, stage, trust := advanceGlobalNetworkReleaseTrustFixture(t)
	request := validAdvanceGlobalNetworkReleaseTrustRequest(stage, trust.CheckpointRevision, *trust.ResolverReceipt)
	advanced, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request)
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseTrust() error = %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseLoopbacks ||
		advanced.CheckpointRevision != request.CheckpointRevision+1 ||
		!reflect.DeepEqual(advanced.TrustReceipt, &request.Receipt) {
		t.Fatalf("advanced plan = %#v", advanced)
	}
	after := globalNetworkReleaseStageSnapshot(t, connection)
	if len(after["network_global_release_trust_receipts"]) != 1 {
		t.Fatalf("trust receipt rows = %#v", after["network_global_release_trust_receipts"])
	}
	replayed, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, after)
	changed := request
	changed.Receipt.ConfirmationDigest = strings.Repeat("a", 64)
	if _, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), changed); err == nil {
		t.Fatal("AdvanceGlobalNetworkReleaseTrust() accepted changed replay receipt")
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, after)
	clone := advanced.Clone()
	clone.TrustReceipt.ConfirmationDigest = strings.Repeat("b", 64)
	if advanced.TrustReceipt.ConfirmationDigest == clone.TrustReceipt.ConfirmationDigest {
		t.Fatal("Clone() aliases trust receipt")
	}
}

// TestAdvanceGlobalNetworkReleaseTrustAdvancesPreexistingUnowned proves preserved trust advances without creating destructive ticket authority.
func TestAdvanceGlobalNetworkReleaseTrustAdvancesPreexistingUnowned(t *testing.T) {
	journal, connection, stage := newGlobalNetworkReleaseStageFixture(t)
	stage.Authority.TrustDisposition = GlobalNetworkReleaseTrustPreexistingUnowned
	staged, err := journal.StageGlobalNetworkRelease(t.Context(), stage)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	lowPorts, err := journal.AdvanceGlobalNetworkReleaseRuntime(
		t.Context(),
		validAdvanceGlobalNetworkReleaseRuntimeRequest(stage, staged.Revision),
	)
	if err != nil {
		t.Fatalf("advance runtime: %v", err)
	}
	resolver, err := journal.AdvanceGlobalNetworkReleaseLowPorts(
		t.Context(),
		validAdvanceGlobalNetworkReleaseLowPortsRequest(stage, lowPorts.CheckpointRevision),
	)
	if err != nil {
		t.Fatalf("advance low ports: %v", err)
	}
	trust, err := journal.AdvanceGlobalNetworkReleaseResolver(
		t.Context(),
		validAdvanceGlobalNetworkReleaseResolverRequest(stage, resolver.CheckpointRevision),
	)
	if err != nil {
		t.Fatalf("advance resolver: %v", err)
	}
	request := validAdvanceGlobalNetworkReleaseTrustRequest(stage, trust.CheckpointRevision, *trust.ResolverReceipt)
	advanced, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request)
	if err != nil {
		t.Fatalf("advance preexisting trust: %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseLoopbacks ||
		advanced.TrustReceipt == nil ||
		advanced.TrustReceipt.Disposition != GlobalNetworkReleaseTrustPreexistingUnowned {
		t.Fatalf("advanced plan = %#v", advanced)
	}
	replayed, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("preexisting replay = %#v, %v", replayed, err)
	}
	if _, err := NewGlobalNetworkReleaseTrustPlanSource(&scriptedGlobalNetworkReleaseTrustPlanReader{
		plan:  trust,
		found: true,
	}).Resolve(t.Context(), ticketissuer.TrustRequest{
		OperationID: trust.Operation.Operation.ID,
	}); err == nil {
		t.Fatal("Resolve() granted destructive authority for preexisting trust")
	}
	if len(globalNetworkReleaseStageSnapshot(t, connection)["network_global_release_trust_receipts"]) != 1 {
		t.Fatal("preexisting trust receipt was not persisted")
	}
}

// TestAdvanceGlobalNetworkReleaseTrustRejectsUnfencedOrMalformedInputs proves no rejected acknowledgement mutates durable state.
func TestAdvanceGlobalNetworkReleaseTrustRejectsUnfencedOrMalformedInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseTrustRequest, *GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "stale checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseTrustRequest, _ *GlobalNetworkReleasePlanRecord) {
				request.CheckpointRevision++
			},
		},
		{
			name: "network revision",
			mutate: func(request *AdvanceGlobalNetworkReleaseTrustRequest, _ *GlobalNetworkReleasePlanRecord) {
				request.NetworkRevision++
			},
		},
		{
			name: "wrong disposition",
			mutate: func(request *AdvanceGlobalNetworkReleaseTrustRequest, _ *GlobalNetworkReleasePlanRecord) {
				request.Receipt.Disposition = GlobalNetworkReleaseTrustPreexistingUnowned
			},
		},
		{
			name: "invalid confirmation",
			mutate: func(request *AdvanceGlobalNetworkReleaseTrustRequest, _ *GlobalNetworkReleasePlanRecord) {
				request.Receipt.ConfirmationDigest = "invalid"
			},
		},
		{
			name: "invalid observation",
			mutate: func(request *AdvanceGlobalNetworkReleaseTrustRequest, _ *GlobalNetworkReleasePlanRecord) {
				request.Receipt.ObservationFingerprint = "invalid"
			},
		},
		{
			name: "source checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseTrustRequest, _ *GlobalNetworkReleasePlanRecord) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "before resolver",
			mutate: func(request *AdvanceGlobalNetworkReleaseTrustRequest, plan *GlobalNetworkReleasePlanRecord) {
				request.Receipt.VerifiedAt = plan.ResolverReceipt.VerifiedAt.Add(-time.Nanosecond)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, trust := advanceGlobalNetworkReleaseTrustFixture(t)
			request := validAdvanceGlobalNetworkReleaseTrustRequest(stage, trust.CheckpointRevision, *trust.ResolverReceipt)
			test.mutate(&request, &trust)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseTrust() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseTrustRejectsWrongPhaseAndCorruptPredecessors keeps trust advance behind prior durable receipts.
func TestAdvanceGlobalNetworkReleaseTrustRejectsWrongPhaseAndCorruptPredecessors(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*gorm.DB)
	}{
		{
			name: "wrong phase",
			prepare: func(connection *gorm.DB) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_global_release_plans SET phase = 'resolver' WHERE id = 1")
			},
		},
		{
			name: "missing resolver receipt",
			prepare: func(connection *gorm.DB) {
				globalNetworkReleaseStageExec(t, connection, "DELETE FROM network_global_release_resolver_receipts WHERE id = 1")
			},
		},
		{
			name: "corrupt resolver receipt",
			prepare: func(connection *gorm.DB) {
				globalNetworkReleaseStageExec(t, connection, `CREATE TRIGGER corrupt_predecessor AFTER UPDATE OF phase ON network_global_release_plans WHEN NEW.phase = 'loopbacks' BEGIN UPDATE network_global_release_resolver_receipts SET verified_at = '2000-01-01T00:00:00Z' WHERE id = 1; END`)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, trust := advanceGlobalNetworkReleaseTrustFixture(t)
			test.prepare(connection)
			request := validAdvanceGlobalNetworkReleaseTrustRequest(stage, trust.CheckpointRevision, *trust.ResolverReceipt)
			if _, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseTrust() error = nil")
			}
		})
	}
}

// TestGlobalNetworkReleaseTrustReceiptFencesLaterPhases proves every later checkpoint retains an ordered trust receipt.
func TestGlobalNetworkReleaseTrustReceiptFencesLaterPhases(t *testing.T) {
	for _, test := range []struct {
		name          string
		phase         GlobalNetworkReleasePlanPhase
		deleteReceipt bool
		advance       bool
		wantError     bool
	}{
		{
			name:    "verify effects",
			phase:   GlobalNetworkReleasePlanPhaseVerifyEffects,
			advance: true,
		},
		{
			name:    "ownership",
			phase:   GlobalNetworkReleasePlanPhaseOwnership,
			advance: true,
		},
		{
			name:    "projection",
			phase:   GlobalNetworkReleasePlanPhaseProjection,
			advance: true,
		},
		{
			name:          "missing receipt",
			phase:         GlobalNetworkReleasePlanPhaseVerifyEffects,
			advance:       true,
			deleteReceipt: true,
			wantError:     true,
		},
		{
			name:      "unordered checkpoint",
			phase:     GlobalNetworkReleasePlanPhaseVerifyEffects,
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, trust := advanceGlobalNetworkReleaseTrustFixture(t)
			request := validAdvanceGlobalNetworkReleaseTrustRequest(
				stage,
				trust.CheckpointRevision,
				*trust.ResolverReceipt,
			)
			advanced, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request)
			if err != nil {
				t.Fatalf("advance trust: %v", err)
			}
			loopbackRequest := validAdvanceGlobalNetworkReleaseLoopbacksRequest(stage, advanced)
			advanced, err = journal.AdvanceGlobalNetworkReleaseLoopbacks(t.Context(), loopbackRequest)
			if err != nil {
				t.Fatalf("advance loopbacks: %v", err)
			}
			effectsRequest := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, advanced)
			if test.deleteReceipt {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"DELETE FROM network_global_release_trust_receipts WHERE id = 1",
				)
			}
			if test.advance {
				if test.phase != GlobalNetworkReleasePlanPhaseVerifyEffects {
					advanced, err = journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), effectsRequest)
					if err != nil {
						t.Fatalf("advance effects: %v", err)
					}
				}
				if test.phase == GlobalNetworkReleasePlanPhaseProjection {
					globalNetworkReleaseStageExec(
						t,
						connection,
						"UPDATE harbor_state SET sequence = sequence + 1 WHERE id = 1",
					)
					globalNetworkReleaseStageExec(
						t,
						connection,
						"UPDATE network_global_release_plans SET phase = ?, checkpoint_revision = checkpoint_revision + 1 WHERE id = 1",
						test.phase,
					)
				}
			} else {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_loopback_receipts SET source_checkpoint_revision = source_checkpoint_revision - 1 WHERE id = 1",
				)
			}

			plan, found, err := journal.ReadGlobalNetworkReleasePlan(t.Context(), stage.Operation.ID)
			if test.wantError {
				if err == nil {
					t.Fatal("ReadGlobalNetworkReleasePlan() error = nil")
				}
				return
			}
			if err != nil || !found {
				t.Fatalf("ReadGlobalNetworkReleasePlan() = %#v, %t, %v", plan, found, err)
			}
			expectedCheckpoint := advanced.CheckpointRevision
			if test.phase == GlobalNetworkReleasePlanPhaseProjection {
				expectedCheckpoint++
			}
			if plan.Phase != test.phase ||
				plan.CheckpointRevision != expectedCheckpoint ||
				plan.TrustReceipt == nil ||
				*plan.TrustReceipt != request.Receipt ||
				plan.LoopbackReceipt == nil ||
				*plan.LoopbackReceipt != loopbackRequest.Receipt ||
				(test.phase != GlobalNetworkReleasePlanPhaseVerifyEffects &&
					(plan.EffectsReceipt == nil || *plan.EffectsReceipt != effectsRequest.Receipt)) {
				t.Fatalf("later release plan = %#v", plan)
			}
		})
	}
}

// TestAdvanceGlobalNetworkReleaseTrustIsCancellationAwareAndConcurrent proves admitted exact callers allocate only one checkpoint.
func TestAdvanceGlobalNetworkReleaseTrustIsCancellationAwareAndConcurrent(t *testing.T) {
	journal, _, stage, trust := advanceGlobalNetworkReleaseTrustFixture(t)
	request := validAdvanceGlobalNetworkReleaseTrustRequest(stage, trust.CheckpointRevision, *trust.ResolverReceipt)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := journal.AdvanceGlobalNetworkReleaseTrust(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled advance error = %v", err)
	}
	const callers = 8
	var group sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := journal.AdvanceGlobalNetworkReleaseTrust(context.Background(), request)
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

// TestAdvanceGlobalNetworkReleaseTrustRollsBackLateFailures proves receipt creation and post-write validation share one transaction.
func TestAdvanceGlobalNetworkReleaseTrustRollsBackLateFailures(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER fail_global_release_trust_receipt BEFORE INSERT ON network_global_release_trust_receipts BEGIN SELECT RAISE(ABORT, 'forced receipt failure'); END`,
		`CREATE TRIGGER corrupt_global_release_trust_receipt AFTER UPDATE OF phase ON network_global_release_plans WHEN NEW.phase = 'loopbacks' BEGIN UPDATE network_global_release_trust_receipts SET confirmation_digest = 'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' WHERE id = 1; END`,
	} {
		t.Run(trigger[:32], func(t *testing.T) {
			journal, connection, stage, trust := advanceGlobalNetworkReleaseTrustFixture(t)
			globalNetworkReleaseStageExec(t, connection, trigger)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			request := validAdvanceGlobalNetworkReleaseTrustRequest(stage, trust.CheckpointRevision, *trust.ResolverReceipt)
			if _, err := journal.AdvanceGlobalNetworkReleaseTrust(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseTrust() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// validAdvanceGlobalNetworkReleaseTrustRequest returns exact trust checkpoint facts ordered after resolver retirement.
func validAdvanceGlobalNetworkReleaseTrustRequest(stage StageGlobalNetworkReleaseRequest, checkpoint domain.Sequence, resolverReceipt GlobalNetworkReleaseResolverReceipt) AdvanceGlobalNetworkReleaseTrustRequest {
	return AdvanceGlobalNetworkReleaseTrustRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: checkpoint,
		NetworkRevision:    stage.Authority.Projection.NetworkRevision,
		Receipt: GlobalNetworkReleaseTrustReceipt{
			SourceCheckpointRevision: checkpoint,
			Disposition:              stage.Authority.TrustDisposition,
			ConfirmationDigest:       strings.Repeat("e", 64),
			ObservationFingerprint:   strings.Repeat("f", 64),
			VerifiedAt:               resolverReceipt.VerifiedAt,
		},
	}
}

// advanceGlobalNetworkReleaseTrustFixture advances the release through resolver retirement.
func advanceGlobalNetworkReleaseTrustFixture(t *testing.T) (*OperationJournal, *gorm.DB, StageGlobalNetworkReleaseRequest, GlobalNetworkReleasePlanRecord) {
	t.Helper()
	journal, connection, stage, resolver := advanceGlobalNetworkReleaseResolverFixture(t)
	trust, err := journal.AdvanceGlobalNetworkReleaseResolver(t.Context(), validAdvanceGlobalNetworkReleaseResolverRequest(stage, resolver.CheckpointRevision))
	if err != nil {
		t.Fatalf("advance resolver: %v", err)
	}
	return journal, connection, stage, trust
}
