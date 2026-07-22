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

// TestAdvanceGlobalNetworkReleaseOwnershipRequestValidate rejects every caller-controlled ownership receipt boundary.
func TestAdvanceGlobalNetworkReleaseOwnershipRequestValidate(t *testing.T) {
	_, _, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	valid := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership)
	for _, test := range []struct {
		name   string
		mutate func(*AdvanceGlobalNetworkReleaseOwnershipRequest)
	}{
		{
			name: "operation",
			mutate: func(request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.OperationID = ""
			},
		},
		{
			name: "checkpoint",
			mutate: func(request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.CheckpointRevision = 0
			},
		},
		{
			name: "network",
			mutate: func(request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.NetworkRevision = domain.MaximumSequence + 1
			},
		},
		{
			name: "source",
			mutate: func(request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.Receipt.SourceCheckpointRevision++
			},
		},
		{
			name: "fingerprint",
			mutate: func(request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.Receipt.ReleasedOwnershipFingerprint = "bad"
			},
		},
		{
			name: "time",
			mutate: func(request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.Receipt.VerifiedAt = time.Time{}
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

// TestAdvanceGlobalNetworkReleaseOwnershipPersistsReplaysAndClones proves the
// atomic projection boundary retains the exact receipt.
func TestAdvanceGlobalNetworkReleaseOwnershipPersistsReplaysAndClones(t *testing.T) {
	journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	request := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership)
	advanced, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), request)
	if err != nil {
		t.Fatalf("AdvanceGlobalNetworkReleaseOwnership() error = %v", err)
	}
	if advanced.Phase != GlobalNetworkReleasePlanPhaseProjection ||
		advanced.CheckpointRevision != ownership.CheckpointRevision+1 ||
		!reflect.DeepEqual(advanced.OwnershipReceipt, &request.Receipt) ||
		!reflect.DeepEqual(advanced.EffectsReceipt, ownership.EffectsReceipt) {
		t.Fatalf("advanced ownership plan = %#v", advanced)
	}
	if len(globalNetworkReleaseStageSnapshot(t, connection)["network_global_release_ownership_receipts"]) != 1 {
		t.Fatal("ownership receipt was not persisted")
	}
	replayed, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	changed := request
	changed.Receipt.VerifiedAt = changed.Receipt.VerifiedAt.Add(time.Nanosecond)
	if _, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), changed); err == nil {
		t.Fatal("AdvanceGlobalNetworkReleaseOwnership() accepted receipt drift")
	}
	clone := advanced.Clone()
	clone.OwnershipReceipt.ReleasedOwnershipFingerprint = strings.Repeat("a", 64)
	if clone.OwnershipReceipt.ReleasedOwnershipFingerprint == advanced.OwnershipReceipt.ReleasedOwnershipFingerprint {
		t.Fatal("Clone() shared ownership receipt")
	}
}

// TestAdvanceGlobalNetworkReleaseOwnershipRejectsDriftWithoutMutation proves
// authority, predecessor, and phase fencing stay durable.
func TestAdvanceGlobalNetworkReleaseOwnershipRejectsDriftWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*gorm.DB, *AdvanceGlobalNetworkReleaseOwnershipRequest)
	}{
		{
			name: "operation",
			configure: func(_ *gorm.DB, request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.OperationID = "foreign"
			},
		},
		{
			name: "checkpoint",
			configure: func(_ *gorm.DB, request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.CheckpointRevision++
			},
		},
		{
			name: "network",
			configure: func(_ *gorm.DB, request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.NetworkRevision++
			},
		},
		{
			name: "wrong fingerprint",
			configure: func(_ *gorm.DB, request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.Receipt.ReleasedOwnershipFingerprint = strings.Repeat("a", 64)
			},
		},
		{
			name: "before effects",
			configure: func(_ *gorm.DB, request *AdvanceGlobalNetworkReleaseOwnershipRequest) {
				request.Receipt.VerifiedAt = request.Receipt.VerifiedAt.Add(-time.Nanosecond)
			},
		},
		{
			name: "wrong phase",
			configure: func(
				connection *gorm.DB,
				_ *AdvanceGlobalNetworkReleaseOwnershipRequest,
			) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"UPDATE network_global_release_plans SET phase = 'verify_effects' WHERE id = 1",
				)
			},
		},
		{
			name: "missing effects",
			configure: func(
				connection *gorm.DB,
				_ *AdvanceGlobalNetworkReleaseOwnershipRequest,
			) {
				globalNetworkReleaseStageExec(
					t,
					connection,
					"DELETE FROM network_global_release_effects_receipts WHERE id = 1",
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
			request := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership)
			test.configure(connection, &request)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), request); err == nil {
				t.Fatal("AdvanceGlobalNetworkReleaseOwnership() error = nil")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestAdvanceGlobalNetworkReleaseOwnershipCancellationConcurrencyAndRollback
// proves only one admitted writer can cross the projection boundary.
func TestAdvanceGlobalNetworkReleaseOwnershipCancellationConcurrencyAndRollback(t *testing.T) {
	journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	request := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := journal.AdvanceGlobalNetworkReleaseOwnership(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	const callers = 8
	results := make(chan GlobalNetworkReleasePlanRecord, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := journal.AdvanceGlobalNetworkReleaseOwnership(context.Background(), request)
			results <- result
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	var expected GlobalNetworkReleasePlanRecord
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent error = %v", err)
		}
	}
	for result := range results {
		if expected.Operation.Operation.ID == "" {
			expected = result
		} else if !reflect.DeepEqual(result, expected) {
			t.Fatalf("concurrent result = %#v, want %#v", result, expected)
		}
	}
	ownershipReceiptCount := len(
		globalNetworkReleaseStageSnapshot(t, connection)["network_global_release_ownership_receipts"],
	)
	if expected.Phase != GlobalNetworkReleasePlanPhaseProjection ||
		expected.CheckpointRevision != request.CheckpointRevision+1 ||
		ownershipReceiptCount != 1 {
		t.Fatalf("concurrent durable result = %#v", expected)
	}
}

// TestAdvanceGlobalNetworkReleaseOwnershipRollsBackLateFailures proves post-write receipt corruption cannot commit.
func TestAdvanceGlobalNetworkReleaseOwnershipRollsBackLateFailures(t *testing.T) {
	journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	globalNetworkReleaseStageExec(
		t,
		connection,
		`CREATE TRIGGER corrupt_global_release_ownership_receipt
		AFTER UPDATE OF phase ON network_global_release_plans
		WHEN NEW.phase = 'projection'
		BEGIN
			UPDATE network_global_release_ownership_receipts
			SET released_ownership_fingerprint = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
			WHERE id = 1;
		END`,
	)
	before := globalNetworkReleaseStageSnapshot(t, connection)
	request := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership)
	if _, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), request); err == nil {
		t.Fatal("AdvanceGlobalNetworkReleaseOwnership() error = nil")
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestGlobalNetworkReleaseOwnershipReceiptRejectsGappedPredecessor prevents a
// corrupt database from inventing an ownership checkpoint after old effects.
func TestGlobalNetworkReleaseOwnershipReceiptRejectsGappedPredecessor(t *testing.T) {
	journal, connection, stage, ownership := advanceGlobalNetworkReleaseOwnershipFixture(t)
	request := validAdvanceGlobalNetworkReleaseOwnershipRequest(stage, ownership)
	if _, err := journal.AdvanceGlobalNetworkReleaseOwnership(t.Context(), request); err != nil {
		t.Fatalf("advance ownership: %v", err)
	}
	globalNetworkReleaseStageExec(
		t,
		connection,
		`UPDATE network_global_release_effects_receipts
		SET source_checkpoint_revision = source_checkpoint_revision - 1
		WHERE id = 1`,
	)
	if _, _, err := journal.ReadGlobalNetworkReleasePlan(
		t.Context(),
		stage.Operation.ID,
	); err == nil {
		t.Fatal("ReadGlobalNetworkReleasePlan() accepted a gapped ownership predecessor")
	}
}

// validAdvanceGlobalNetworkReleaseOwnershipRequest returns exact ownership facts ordered after effect verification.
func validAdvanceGlobalNetworkReleaseOwnershipRequest(
	stage StageGlobalNetworkReleaseRequest,
	ownership GlobalNetworkReleasePlanRecord,
) AdvanceGlobalNetworkReleaseOwnershipRequest {
	return AdvanceGlobalNetworkReleaseOwnershipRequest{
		OperationID:        stage.Operation.ID,
		CheckpointRevision: ownership.CheckpointRevision,
		NetworkRevision:    stage.Authority.Projection.NetworkRevision,
		Receipt: GlobalNetworkReleaseOwnershipReceipt{
			SourceCheckpointRevision:     ownership.CheckpointRevision,
			ReleasedOwnershipFingerprint: stage.Authority.ExpectedOwnershipFingerprint,
			VerifiedAt:                   ownership.EffectsReceipt.VerifiedAt,
		},
	}
}

// advanceGlobalNetworkReleaseOwnershipFixture advances the release through effect verification.
func advanceGlobalNetworkReleaseOwnershipFixture(
	t *testing.T,
) (*OperationJournal, *gorm.DB, StageGlobalNetworkReleaseRequest, GlobalNetworkReleasePlanRecord) {
	t.Helper()
	journal, connection, stage, effects := advanceGlobalNetworkReleaseEffectsFixture(t)
	request := validAdvanceGlobalNetworkReleaseEffectsRequest(stage, effects)
	advanced, err := journal.AdvanceGlobalNetworkReleaseEffects(t.Context(), request)
	if err != nil {
		t.Fatalf("advance effects: %v", err)
	}
	return journal, connection, stage, advanced
}
