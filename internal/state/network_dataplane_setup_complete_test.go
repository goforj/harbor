package state

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/migrations"
	"gorm.io/gorm"
)

const networkDataPlaneSetupPlansMigrationName = "2026_07_22_020000_create_network_dataplane_setup_plans"

// TestNetworkDataPlaneSetupLifecyclePersistsAndReplaysExactAuthority covers the crash-safe trusted-ingress boundary.
func TestNetworkDataPlaneSetupLifecyclePersistsAndReplaysExactAuthority(t *testing.T) {
	fixture := newNetworkDataPlaneSetupCompleteFixture(t)
	staged, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.stage)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	trustAt := fixture.stage.Projection.NetworkUpdatedAt.Add(time.Minute)
	trustDigest, err := NetworkDataPlaneSetupEvidenceDigest(struct{ Evidence string }{"trust"})
	if err != nil {
		t.Fatal(err)
	}
	trust := AdvanceNetworkDataPlaneSetupTrustRequest{OperationID: staged.Operation.ID, ExpectedOperationRevision: staged.Revision, RequesterIdentity: fixture.owner(), Projection: fixture.stage.Projection, Policy: fixture.stage.Policy, TrustEvidenceDigest: trustDigest, TrustVerifiedAt: trustAt}
	lowPort, err := fixture.journal.AdvanceNetworkDataPlaneSetupTrust(context.Background(), trust)
	if err != nil {
		t.Fatalf("advance trust: %v", err)
	}
	if lowPort.Operation.Phase != networkDataPlaneSetupLowPortApprovalPhase {
		t.Fatalf("trust phase = %q", lowPort.Operation.Phase)
	}
	plan, found, err := fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), lowPort.Operation.ID)
	if err != nil || !found {
		t.Fatalf("read trust plan = %#v, %t, %v", plan, found, err)
	}
	if plan.Operation.Revision != lowPort.Revision || plan.TrustEvidenceDigest != trustDigest || plan.Activation != nil {
		t.Fatalf("trust plan = %#v", plan)
	}
	activation := networkDataPlaneActivationTestRequest(t, fixture.stage.Projection.NetworkRevision)
	activation.ConfirmedOwnership = fixture.stage.Projection.ConfirmedOwnership
	activation.Policy = fixture.stage.Policy
	activation.Setup[0] = fixture.stage.Projection.ResolverProof
	activation.At = trustAt.Add(time.Minute)
	activation.Setup[1].VerifiedAt = activation.At
	activation.Listeners.DNS.VerifiedAt = activation.At
	activation.Listeners.HTTP.VerifiedAt = activation.At
	activation.Listeners.HTTPS.VerifiedAt = activation.At
	lowPortDigest, err := NetworkDataPlaneSetupEvidenceDigest(struct{ Evidence string }{"low-port"})
	if err != nil {
		t.Fatal(err)
	}
	activationStage, err := fixture.journal.StageNetworkDataPlaneActivation(context.Background(), StageNetworkDataPlaneActivationRequest{OperationID: lowPort.Operation.ID, ExpectedOperationRevision: lowPort.Revision, RequesterIdentity: fixture.owner(), LowPortEvidenceDigest: lowPortDigest, Activation: activation})
	if err != nil {
		t.Fatalf("stage activation: %v", err)
	}
	if activationStage.Operation.Operation.State != domain.OperationRunning || activationStage.Operation.Operation.Phase != networkDataPlaneSetupActivationPhase {
		t.Fatalf("activation operation = %#v", activationStage.Operation)
	}
	plan, found, err = fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), lowPort.Operation.ID)
	if err != nil || !found || plan.Activation == nil || !reflect.DeepEqual(*plan.Activation, activation) {
		t.Fatalf("activation plan = %#v, %t, %v", plan, found, err)
	}
	plan.Activation.Setup[0].Evidence = "caller mutation"
	again, found, err := fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), lowPort.Operation.ID)
	if err != nil || !found || again.Activation == nil || again.Activation.Setup[0].Evidence == "caller mutation" {
		t.Fatalf("plan read isolation failed: %#v, %v", again, err)
	}
	if _, err := fixture.store.ActivateNetworkDataPlane(context.Background(), activationStage.Activation); err != nil {
		t.Fatalf("activate durable network: %v", err)
	}
	completedActivation, err := fixture.journal.CompleteNetworkDataPlaneActivation(context.Background(), CompleteNetworkDataPlaneActivationRequest{OperationID: lowPort.Operation.ID, ExpectedOperationRevision: activationStage.Operation.Revision, RequesterIdentity: fixture.owner()})
	if err != nil {
		t.Fatalf("complete activation: %v", err)
	}
	terminalAt := activation.At.Add(time.Minute)
	completed, err := fixture.journal.CompleteNetworkDataPlaneSetup(context.Background(), CompleteNetworkDataPlaneSetupRequest{OperationID: lowPort.Operation.ID, ExpectedOperationRevision: completedActivation.Operation.Revision, RequesterIdentity: fixture.owner(), At: terminalAt})
	if err != nil {
		t.Fatalf("complete setup: %v", err)
	}
	if completed.Operation.State != domain.OperationSucceeded || completed.Operation.Phase != networkDataPlaneSetupCompletedPhase {
		t.Fatalf("terminal operation = %#v", completed)
	}
	if _, found, err := fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), completed.Operation.ID); err != nil || found {
		t.Fatalf("terminal plan = found %t, error %v", found, err)
	}
}

// TestNetworkDataPlaneSetupLifecycleRejectsRequestDrift exercises requester, revision, phase, and corrupt-plan fail-closed paths.
func TestNetworkDataPlaneSetupLifecycleRejectsRequestDrift(t *testing.T) {
	fixture := newNetworkDataPlaneSetupCompleteFixture(t)
	staged, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.stage)
	if err != nil {
		t.Fatal(err)
	}
	base := AdvanceNetworkDataPlaneSetupTrustRequest{OperationID: staged.Operation.ID, ExpectedOperationRevision: staged.Revision, RequesterIdentity: fixture.owner(), Projection: fixture.stage.Projection, Policy: fixture.stage.Policy, TrustEvidenceDigest: strings.Repeat("a", 64), TrustVerifiedAt: fixture.stage.Projection.NetworkUpdatedAt.Add(time.Minute)}
	for name, mutate := range map[string]func(*AdvanceNetworkDataPlaneSetupTrustRequest){
		"requester": func(r *AdvanceNetworkDataPlaneSetupTrustRequest) { r.RequesterIdentity = "foreign" },
		"revision":  func(r *AdvanceNetworkDataPlaneSetupTrustRequest) { r.ExpectedOperationRevision++ },
		"digest":    func(r *AdvanceNetworkDataPlaneSetupTrustRequest) { r.TrustEvidenceDigest = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if _, err := fixture.journal.AdvanceNetworkDataPlaneSetupTrust(context.Background(), candidate); err == nil {
				t.Fatal("advance trust unexpectedly succeeded")
			}
		})
	}
	lowPort, err := fixture.journal.AdvanceNetworkDataPlaneSetupTrust(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	mustProjectStoreReadExec(t, fixture.database, "UPDATE network_data_plane_setup_plans SET authority_digest = ? WHERE id = 1", strings.Repeat("b", 64))
	if _, _, err := fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), lowPort.Operation.ID); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt plan read error = %v", err)
	}
}

// TestReadNetworkDataPlaneSetupPlanRejectsForgedOwnerAndJSON covers owner scope and canonical payload corruption.
func TestReadNetworkDataPlaneSetupPlanRejectsForgedOwnerAndJSON(t *testing.T) {
	prepare := func(t *testing.T) (*networkDataPlaneSetupCompleteFixture, OperationRecord) {
		t.Helper()
		fixture := newNetworkDataPlaneSetupCompleteFixture(t)
		staged, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.stage)
		if err != nil {
			t.Fatal(err)
		}
		advanced, err := fixture.journal.AdvanceNetworkDataPlaneSetupTrust(context.Background(), AdvanceNetworkDataPlaneSetupTrustRequest{OperationID: staged.Operation.ID, ExpectedOperationRevision: staged.Revision, RequesterIdentity: fixture.owner(), Projection: fixture.stage.Projection, Policy: fixture.stage.Policy, TrustEvidenceDigest: strings.Repeat("a", 64), TrustVerifiedAt: fixture.stage.Projection.NetworkUpdatedAt.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return fixture, advanced
	}
	for _, test := range []struct {
		name, statement string
		args            []any
	}{
		{"foreign kind", "UPDATE operations SET kind = ? WHERE id = ?", []any{"network.resolver.setup", "operation-data-plane-setup"}},
		{"non-global", "UPDATE operations SET project_id = ? WHERE id = ?", []any{"project-alpha", "operation-data-plane-setup"}},
		{"extra history", "INSERT INTO operation_transitions (operation_id, ordinal, previous_state, state, phase, occurred_at, sequence) VALUES (?, 99, ?, ?, ?, ?, ?)", []any{"operation-data-plane-setup", "requires_approval", "requires_approval", "foreign", networkMutationTestTime(), 999}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, operation := prepare(t)
			mustProjectStoreReadExec(t, fixture.database, test.statement, test.args...)
			if _, _, err := fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), operation.Operation.ID); err == nil {
				t.Fatal("forged plan read succeeded")
			}
		})
	}
	t.Run("noncanonical JSON", func(t *testing.T) {
		fixture, operation := prepare(t)
		var payload string
		if err := fixture.database.Raw("SELECT authority_payload FROM network_data_plane_setup_plans WHERE id = 1").Scan(&payload).Error; err != nil {
			t.Fatal(err)
		}
		payload += " "
		mustProjectStoreReadExec(t, fixture.database, "UPDATE network_data_plane_setup_plans SET authority_payload = ?, authority_digest = ? WHERE id = 1", payload, digestNetworkDataPlaneSetupPayload(payload))
		if _, _, err := fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), operation.Operation.ID); err == nil || !strings.Contains(err.Error(), "canonical") {
			t.Fatalf("noncanonical read = %v", err)
		}
	})
}

// TestNetworkDataPlaneSetupTrustWriteFaultRollsBack proves no partial plan or transition escapes its atomic boundary.
func TestNetworkDataPlaneSetupTrustWriteFaultRollsBack(t *testing.T) {
	fixture := newNetworkDataPlaneSetupCompleteFixture(t)
	staged, err := fixture.journal.StageNetworkDataPlaneSetup(context.Background(), fixture.stage)
	if err != nil {
		t.Fatal(err)
	}
	mustProjectStoreReadExec(t, fixture.database, `CREATE TRIGGER fail_data_plane_plan BEFORE INSERT ON network_data_plane_setup_plans BEGIN SELECT RAISE(ABORT, 'forced plan failure'); END`)
	request := AdvanceNetworkDataPlaneSetupTrustRequest{OperationID: staged.Operation.ID, ExpectedOperationRevision: staged.Revision, RequesterIdentity: fixture.owner(), Projection: fixture.stage.Projection, Policy: fixture.stage.Policy, TrustEvidenceDigest: strings.Repeat("a", 64), TrustVerifiedAt: fixture.stage.Projection.NetworkUpdatedAt.Add(time.Minute)}
	if _, err := fixture.journal.AdvanceNetworkDataPlaneSetupTrust(context.Background(), request); err == nil || !strings.Contains(err.Error(), "forced plan failure") {
		t.Fatalf("write fault = %v", err)
	}
	recorded, err := fixture.journal.Operation(context.Background(), staged.Operation.ID)
	if err != nil || recorded.Revision != staged.Revision {
		t.Fatalf("rollback operation = %#v, %v", recorded, err)
	}
	if _, found, err := fixture.journal.ReadNetworkDataPlaneSetupPlan(context.Background(), staged.Operation.ID); err != nil || found {
		t.Fatalf("rollback plan = %t, %v", found, err)
	}
}

type networkDataPlaneSetupCompleteFixture struct {
	state    *networkDataPlaneSetupStageFixture
	journal  *OperationJournal
	store    *Store
	database *gorm.DB
	stage    StageNetworkDataPlaneSetupRequest
}

// newNetworkDataPlaneSetupCompleteFixture applies the plan migration over the established resolver-stage fixture.
func newNetworkDataPlaneSetupCompleteFixture(t *testing.T) *networkDataPlaneSetupCompleteFixture {
	t.Helper()
	state := newNetworkDataPlaneSetupStageFixture(t)
	applyNetworkDataPlaneSetupPlansMigration(t, state.database)
	return &networkDataPlaneSetupCompleteFixture{state: state, journal: state.journal, store: state.state.store, database: state.database, stage: state.request}
}

// owner returns the durable identity to which every lifecycle request is bound.
func (fixture *networkDataPlaneSetupCompleteFixture) owner() string {
	return fixture.stage.Projection.ConfirmedOwnership.Record.OwnerIdentity
}

// applyNetworkDataPlaneSetupPlansMigration installs the production persistence receipt schema.
func applyNetworkDataPlaneSetupPlansMigration(t *testing.T, databaseConnection *gorm.DB) {
	t.Helper()
	for _, migration := range migrations.GetMigrations() {
		if migration.Name() == networkDataPlaneSetupPlansMigrationName {
			if err := migration.Up(databaseConnection); err != nil {
				t.Fatalf("apply plan migration: %v", err)
			}
			return
		}
	}
	t.Fatalf("missing migration %q", networkDataPlaneSetupPlansMigrationName)
}
