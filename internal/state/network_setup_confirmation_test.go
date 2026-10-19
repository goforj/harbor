package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/migrations"
)

// confirmationRecoveryFixture creates real pending authority using production migrations and native-shaped evidence.
func confirmationRecoveryFixture(t *testing.T) (*networkSetupStageFixture, *Store, CompleteNetworkSetupRequest) {
	t.Helper()
	fixture := newNetworkSetupCompletionFixture(t)
	for _, migration := range migrations.GetMigrations() {
		if migration.Name() == "2026_10_19_020000_create_network_setup_confirmation_receipts" {
			if err := migration.Up(fixture.database); err != nil {
				t.Fatal(err)
			}
		}
	}
	request := networkSetupStageRequest(t, "operation-resume-setup", "intent-resume-setup")
	approval, err := fixture.journal.StageNetworkSetup(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, networkSetupCompletionStore(fixture), networkSetupCompletionRequest(t, approval, request.Ownership)
}

// TestNetworkSetupConfirmationSurvivesFailedCommitAndRestart proves recovery never repeats already verified host effects.
func TestNetworkSetupConfirmationSurvivesFailedCommitAndRestart(t *testing.T) {
	fixture, store, request := confirmationRecoveryFixture(t)
	if _, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision); !errors.Is(err, ErrNetworkSetupConfirmationUnavailable) {
		t.Fatalf("missing receipt: %v", err)
	}
	before, err := store.RuntimeState(t.Context())
	if err != nil || before.NetworkSetupPending == nil || before.NetworkSetupPending.Phase != "approval" {
		t.Fatalf("pending approval: %v", err)
	}
	if err := store.RetainNetworkSetupConfirmation(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	retained, err := store.RuntimeState(t.Context())
	if err != nil || retained.NetworkSetupPending == nil || retained.NetworkSetupPending.Phase != "confirmation" {
		t.Fatalf("verified pending setup: %v", err)
	}
	if err := store.RetainNetworkSetupConfirmation(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.CurrentSequence(t.Context())
	if err != nil || unchanged != retained.Snapshot.Sequence {
		t.Fatal("receipt replay allocated another durable revision", err)
	}
	if err := fixture.database.Exec("CREATE TRIGGER fail_setup_completion BEFORE DELETE ON network_setup_plans BEGIN SELECT RAISE(ABORT, 'injected completion failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteNetworkSetup(t.Context(), request); err == nil {
		t.Fatal("completion failure was ignored")
	}
	fixture.restart(t)
	store = networkSetupCompletionStore(fixture)
	evidence, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision)
	if err != nil || evidence.Pool != request.HelperPoolEvidence.Pool {
		t.Fatalf("restart lost verified effects: %v", err)
	}
	evidence.Identities[0].Address = "127.0.0.1"
	fresh, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision)
	if err != nil || fresh.Identities[0].Address == evidence.Identities[0].Address {
		t.Fatal("receipt leaked caller-mutable evidence", err)
	}
	if err := fixture.database.Exec("DROP TRIGGER fail_setup_completion").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteNetworkSetup(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision); err != nil {
		t.Fatal("lost success response cannot replay", err)
	}
	if _, err := store.CompleteNetworkSetup(t.Context(), request); err != nil {
		t.Fatal("terminal completion is not idempotent", err)
	}
	done, err := store.RuntimeState(t.Context())
	if err != nil || done.NetworkSetupPending != nil {
		t.Fatal("completed recovery still appears pending", err)
	}
	operation, err := domain.NewOperation("operation-next-repair", "intent-next-repair", domain.OperationKindNetworkSetup, "", request.At.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.journal.StageNetworkSetup(t.Context(), StageNetworkSetupRequest{Operation: operation, Ownership: request.ConfirmedOwnership.Record, Repair: true}); err != nil {
		t.Fatal("retired receipt blocked a later repair", err)
	}
	var count int64
	fixture.database.Model(&networkSetupConfirmationRow{}).Count(&count)
	if count != 0 {
		t.Fatal("new plan retained another operation's confirmation")
	}
}

// TestNetworkSetupConfirmationRejectsUnverifiedAndSubstitutedAuthority ensures retention cannot grant new ownership.
func TestNetworkSetupConfirmationRejectsUnverifiedAndSubstitutedAuthority(t *testing.T) {
	fixture, store, request := confirmationRecoveryFixture(t)
	invalid := cloneCompleteNetworkSetupRequest(request)
	invalid.HelperPoolEvidence.Identities[0].Observation.Fingerprint = "invalid"
	if err := store.RetainNetworkSetupConfirmation(t.Context(), invalid); err == nil {
		t.Fatal("unverified evidence was retained")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.RetainNetworkSetupConfirmation(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled retention mutated state", err)
	}
	if err := store.RetainNetworkSetupConfirmation(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []domain.Sequence{0, request.ExpectedOperationRevision + 1} {
		if _, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, revision); err == nil {
			t.Fatal("receipt crossed its exact revision")
		}
	}
	if _, err := store.NetworkSetupConfirmation(t.Context(), "invalid id", request.ExpectedOperationRevision); err == nil {
		t.Fatal("invalid operation accepted")
	}
	for _, column := range []string{"ownership_fingerprint", "evidence_json", "verified_at"} {
		t.Run(column, func(t *testing.T) {
			var row networkSetupConfirmationRow
			if err := fixture.database.First(&row).Error; err != nil {
				t.Fatal(err)
			}
			change := map[string]any{"ownership_fingerprint": string(make([]byte, 64)), "evidence_json": `{"pool":"127.88.0.8/29","identities":[],"unexpected":true}`, "verified_at": time.Time{}}[column]
			if column == "ownership_fingerprint" {
				change = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			if err := fixture.database.Model(&row).Where("plan_id = ?", 1).Update(column, change).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision); err == nil {
				t.Fatal("corrupt retained proof was trusted")
			}
			if err := fixture.database.Model(&row).Where("plan_id = ?", 1).Updates(map[string]any{"ownership_fingerprint": row.OwnershipFingerprint, "evidence_json": row.EvidenceJSON, "verified_at": row.VerifiedAt}).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestDiscardNetworkSetupConfirmationRequiresExactPendingProof preserves authority while restoring the approval phase.
func TestDiscardNetworkSetupConfirmationRequiresExactPendingProof(t *testing.T) {
	_, store, request := confirmationRecoveryFixture(t)
	if err := store.RetainNetworkSetupConfirmation(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"invalid id", "invalid revision", "stale revision", "different proof", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			id, revision := request.OperationID, request.ExpectedOperationRevision
			evidence := cloneCompleteNetworkSetupRequest(request).HelperPoolEvidence
			ctx := t.Context()
			switch scenario {
			case "invalid id":
				id = "invalid id"
			case "invalid revision":
				revision = 0
			case "stale revision":
				revision++
			case "different proof":
				evidence.Identities[0].Changed = !evidence.Identities[0].Changed
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := store.DiscardNetworkSetupConfirmation(ctx, id, revision, evidence); err == nil {
				t.Fatal("invalid discard accepted")
			}
			if _, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision); err != nil {
				t.Fatal("invalid discard lost proof", err)
			}
		})
	}
	if err := store.DiscardNetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision, request.HelperPoolEvidence); err != nil {
		t.Fatal(err)
	}
	current, err := store.RuntimeState(t.Context())
	if err != nil || current.NetworkSetupPending == nil || current.NetworkSetupPending.Phase != "approval" {
		t.Fatal("discard did not restore pending approval", err)
	}
	if _, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision); !errors.Is(err, ErrNetworkSetupConfirmationUnavailable) {
		t.Fatal("discarded evidence still available", err)
	}
}

// TestNetworkSetupRecoveryRollbackRefusesPendingEffects prevents downgrade from orphaning verified host changes.
func TestNetworkSetupRecoveryRollbackRefusesPendingEffects(t *testing.T) {
	fixture, store, request := confirmationRecoveryFixture(t)
	if err := store.RetainNetworkSetupConfirmation(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations.GetMigrations() {
		if migration.Name() != "2026_10_19_020000_create_network_setup_confirmation_receipts" {
			continue
		}
		if err := migration.Down(fixture.database); err == nil {
			t.Fatal("rollback dropped pending effects")
		}
		if _, err := store.NetworkSetupConfirmation(t.Context(), request.OperationID, request.ExpectedOperationRevision); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteNetworkSetup(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		if err := migration.Down(fixture.database); err != nil {
			t.Fatal("completed receipt prevented rollback", err)
		}
		return
	}
	t.Fatal("recovery migration missing")
}
