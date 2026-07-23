package state

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/trust/certroot"
	"github.com/goforj/harbor/internal/trust/localca"
	"github.com/goforj/harbor/migrations"
	"gorm.io/gorm"
)

// TestStageGlobalNetworkReleaseStagesAndReplays verifies the transaction creates the fixed lifecycle and is exactly idempotent.
func TestStageGlobalNetworkReleaseStagesAndReplays(t *testing.T) {
	journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("StageGlobalNetworkRelease() error = %v", err)
	}
	if staged.Operation.State != domain.OperationRunning || staged.Operation.Phase != globalNetworkReleaseRuntimeOperationPhase || staged.Revision != 5 {
		t.Fatalf("staged record = %#v", staged)
	}
	plan, found, err := journal.ReadGlobalNetworkReleasePlan(context.Background(), request.Operation.ID)
	if err != nil || !found {
		t.Fatalf("ReadGlobalNetworkReleasePlan() = %#v, %t, %v", plan, found, err)
	}
	if plan.Operation.Operation.ID != staged.Operation.ID ||
		plan.Operation.Revision != staged.Revision ||
		plan.CheckpointRevision != staged.Revision ||
		plan.Phase != GlobalNetworkReleasePlanPhaseRuntimeRelease ||
		!reflect.DeepEqual(plan.Authority, request.Authority) {
		t.Fatalf("read plan = %#v", plan)
	}
	before := globalNetworkReleaseStageSnapshot(t, connection)
	replayed, err := journal.StageGlobalNetworkRelease(context.Background(), request)
	if err != nil || replayed.Operation.ID != staged.Operation.ID || replayed.Revision != staged.Revision {
		t.Fatalf("replay = %#v, %v, want %#v", replayed, err, staged)
	}
	if after := globalNetworkReleaseStageSnapshot(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatalf("exact replay changed durable rows\nbefore: %#v\nafter: %#v", before, after)
	}
}

// TestStageGlobalNetworkReleaseRejectsAdmissionDriftWithoutMutation covers independent authority and quiescence boundaries.
func TestStageGlobalNetworkReleaseRejectsAdmissionDriftWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest)
	}{
		{
			name: "network stage",
			mutate: func(t *testing.T, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_state SET stage = 'resolver'")
			},
		},
		{
			name: "network revision",
			mutate: func(t *testing.T, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE network_state SET revision = revision + 1")
			},
		},
		{
			name: "policy",
			mutate: func(t *testing.T, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageExec(t, connection, "UPDATE machine_ownership_projections SET network_policy_fingerprint = ? WHERE id = 1", strings.Repeat("e", 64))
			},
		},
		{
			name: "ownership",
			mutate: func(t *testing.T, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				request.Authority.ExpectedOwnershipFingerprint = strings.Repeat("f", 64)
			},
		},
		{
			name: "active project operation",
			mutate: func(t *testing.T, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageInsertOperation(t, connection, "operation-project-active", "intent-project-active", "project-raw", domain.OperationKindProjectStart, domain.OperationQueued, request.Operation.RequestedAt)
			},
		},
		{
			name: "active setup operation",
			mutate: func(t *testing.T, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageInsertOperation(t, connection, "operation-setup-active", "intent-setup-active", "", domain.OperationKindNetworkSetup, domain.OperationQueued, request.Operation.RequestedAt)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
			test.mutate(t, connection, &request)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			if _, err := journal.StageGlobalNetworkRelease(context.Background(), request); err == nil {
				t.Fatal("StageGlobalNetworkRelease() unexpectedly succeeded")
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestStageGlobalNetworkReleaseRejectsActiveResolverPolicyMigration verifies resolver retirement completes before global release staging writes.
func TestStageGlobalNetworkReleaseRejectsActiveResolverPolicyMigration(t *testing.T) {
	journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
	globalNetworkReleaseStageInsertOperation(
		t,
		connection,
		"operation-policy-migration",
		"intent-policy-migration",
		"",
		domain.OperationKindNetworkResolverPolicyMigration,
		domain.OperationRequiresApproval,
		request.Operation.RequestedAt,
	)
	before := globalNetworkReleaseStageSnapshot(t, connection)

	_, err := journal.StageGlobalNetworkRelease(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "active setup operations") {
		t.Fatalf("StageGlobalNetworkRelease() error = %v, want active resolver policy migration rejection", err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestStageGlobalNetworkReleaseRejectsConflictsWithoutMutation verifies retries cannot cross intent, ID, or active-owner boundaries.
func TestStageGlobalNetworkReleaseRejectsConflictsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, journal *OperationJournal, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest)
		check  func(error) bool
	}{
		{
			name: "intent conflict",
			mutate: func(t *testing.T, journal *OperationJournal, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageInsertOperation(t, connection, "operation-existing", request.Operation.IntentID, "project-alpha", domain.OperationKindProjectStart, domain.OperationSucceeded, request.Operation.RequestedAt)
			},
			check: func(err error) bool { var value *IntentConflictError; return errors.As(err, &value) },
		},
		{
			name: "ID conflict",
			mutate: func(t *testing.T, journal *OperationJournal, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageInsertOperation(t, connection, request.Operation.ID, "intent-existing", "", domain.OperationKindNetworkSetup, domain.OperationSucceeded, request.Operation.RequestedAt)
			},
			check: func(err error) bool { var value *OperationIDConflictError; return errors.As(err, &value) },
		},
		{
			name: "foreign active release",
			mutate: func(t *testing.T, journal *OperationJournal, connection *gorm.DB, request *StageGlobalNetworkReleaseRequest) {
				globalNetworkReleaseStageInsertOperation(t, connection, "operation-foreign", "intent-foreign", "", domain.OperationKindNetworkRelease, domain.OperationRunning, request.Operation.RequestedAt)
			},
			check: func(err error) bool { var value *GlobalNetworkReleaseActiveError; return errors.As(err, &value) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
			test.mutate(t, journal, connection, &request)
			before := globalNetworkReleaseStageSnapshot(t, connection)
			_, err := journal.StageGlobalNetworkRelease(context.Background(), request)
			if !test.check(err) {
				t.Fatalf("StageGlobalNetworkRelease() error = %v", err)
			}
			globalNetworkReleaseStageAssertUnchanged(t, connection, before)
		})
	}
}

// TestCloneStageGlobalNetworkReleaseRequestIsolatesAuthority verifies writer admission cannot retain caller-owned authority memory.
func TestCloneStageGlobalNetworkReleaseRequestIsolatesAuthority(t *testing.T) {
	request := StageGlobalNetworkReleaseRequest{
		Operation: domain.Operation{
			ID:          "operation-global-release",
			IntentID:    "intent-global-release",
			Kind:        domain.OperationKindNetworkRelease,
			State:       domain.OperationQueued,
			Phase:       string(domain.OperationQueued),
			RequestedAt: time.Now().UTC(),
		},
		Authority: validGlobalNetworkReleaseAuthority(t),
	}
	clone := cloneStageGlobalNetworkReleaseRequest(request)
	request.Authority.Root.CertificatePEM[0] ^= 1
	request.Authority.LoopbackTargets[0].ObservationFingerprint = strings.Repeat("e", 64)
	if clone.Authority.Root.CertificatePEM[0] == request.Authority.Root.CertificatePEM[0] || clone.Authority.LoopbackTargets[0].ObservationFingerprint == request.Authority.LoopbackTargets[0].ObservationFingerprint {
		t.Fatal("cloned request aliases caller authority")
	}
}

// newGlobalNetworkReleaseStageFixture builds a real full-network SQLite projection and applies the committed plan migration.
func newGlobalNetworkReleaseStageFixture(t *testing.T) (*OperationJournal, *gorm.DB, StageGlobalNetworkReleaseRequest) {
	t.Helper()
	fixture, _ := newFullNetworkDataPlaneSetupProjectionFixture(t)
	globalNetworkReleaseStageApplyPlanMigration(t, fixture.database)
	projection, err := fixture.source.Resolve(context.Background(), fixture.request.Policy)
	if err != nil {
		t.Fatalf("resolve full projection: %v", err)
	}
	issuer, err := localca.New(localca.Config{Now: func() time.Time { return projection.NetworkUpdatedAt }})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	material := issuer.Material()
	root := certroot.Root{
		CertificatePEM: material.CertificatePEM,
		Fingerprint:    material.Fingerprint,
		NotBefore:      material.NotBefore,
		NotAfter:       material.NotAfter,
	}
	policy := fixture.request.Policy
	policy.AuthorityFingerprint = root.Fingerprint
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint policy: %v", err)
	}
	projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = policyFingerprint
	ownershipFingerprint, err := projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint ownership: %v", err)
	}
	projection.ConfirmedOwnership.Fingerprint = ownershipFingerprint
	globalNetworkReleaseStageExec(t, fixture.database, "UPDATE machine_ownership_projections SET network_policy_fingerprint = ?, record_fingerprint = ? WHERE id = 1", policyFingerprint, ownershipFingerprint)
	pool, err := networkSetupIdentityPool(projection.ConfirmedOwnership.Record.LoopbackPoolPrefix)
	if err != nil {
		t.Fatalf("read loopback pool: %v", err)
	}
	targets := make([]GlobalNetworkReleaseLoopbackTarget, 0, pool.Capacity())
	for _, address := range pool.Candidates() {
		targets = append(targets, GlobalNetworkReleaseLoopbackTarget{
			Address:                address,
			ObservationFingerprint: strings.Repeat("a", 64),
		})
	}
	authority := GlobalNetworkReleaseAuthority{
		Projection:                     projection,
		Policy:                         policy,
		Root:                           root,
		ExpectedOwnershipFingerprint:   ownershipFingerprint,
		TrustDisposition:               GlobalNetworkReleaseTrustOwned,
		LowPortObservationFingerprint:  strings.Repeat("b", 64),
		ResolverObservationFingerprint: strings.Repeat("c", 64),
		TrustObservationFingerprint:    strings.Repeat("d", 64),
		LoopbackTargets:                targets,
		ProjectRevisions:               []NetworkProjectRevision{},
	}
	operation, err := domain.NewOperation("operation-global-release", "intent-global-release", domain.OperationKindNetworkRelease, "", projection.NetworkUpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("create release operation: %v", err)
	}
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() { _ = connections.Close(context.Background()) })
	if _, err := connections.GetHarbord(); err != nil {
		t.Fatalf("open journal connection: %v", err)
	}
	return NewOperationJournal(
			connections,
			models.NewOperationRepo(connections),
			models.NewOperationTransitionRepo(connections),
			models.NewHarborStateRepo(connections),
			NewMutationCoordinator(connections),
		), fixture.database, StageGlobalNetworkReleaseRequest{
			Operation: operation,
			Authority: authority,
		}
}

// globalNetworkReleaseStageApplyPlanMigration applies the release-plan migrations to the established full-network fixture.
func globalNetworkReleaseStageApplyPlanMigration(t *testing.T, connection *gorm.DB) {
	t.Helper()
	for _, name := range []string{
		"2026_07_19_001556_create_helper_approval_plans",
		"2026_07_22_020000_create_network_dataplane_setup_plans",
		"2026_07_22_040000_create_network_global_release_plans",
		"2026_07_22_041000_add_network_global_release_plan_checkpoint_revision",
		"2026_07_22_042000_create_network_global_release_low_port_receipts",
		"2026_07_22_043000_create_network_global_release_resolver_receipts",
		"2026_07_22_044000_create_network_global_release_trust_receipts",
		"2026_07_22_045000_create_network_global_release_loopback_receipts",
		"2026_07_22_046000_create_network_global_release_effects_receipts",
		"2026_07_22_047000_create_network_global_release_ownership_receipts",
		"2026_07_22_048000_add_network_resolver_setup_administrator_trust",
		"2026_07_22_049000_create_network_global_release_terminals",
	} {
		found := false
		for _, migration := range migrations.GetMigrations() {
			if migration.Name() == name &&
				migration.App() == "harbord" &&
				migration.Connection() == "default" {
				if err := migration.Up(connection); err != nil {
					t.Fatalf("apply global release plan migration %s: %v", name, err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("global release plan migration %q is not registered", name)
		}
	}
}

// globalNetworkReleaseStageSnapshot captures every stage-owned table so failed transactions prove rollback.
func globalNetworkReleaseStageSnapshot(t *testing.T, connection *gorm.DB) map[string][]map[string]any {
	t.Helper()
	result := make(map[string][]map[string]any)
	for _, table := range []string{
		"harbor_state",
		"network_state",
		"projects",
		"project_apps",
		"project_services",
		"project_resources",
		"project_sessions",
		"operations",
		"operation_transitions",
		"public_endpoint_leases",
		"network_project_releases",
		"network_global_release_plans",
		"network_global_release_low_port_receipts",
		"network_global_release_resolver_receipts",
		"network_global_release_trust_receipts",
		"network_global_release_loopback_receipts",
		"network_global_release_effects_receipts",
		"network_global_release_ownership_receipts",
		"network_global_release_terminals",
	} {
		var rows []map[string]any
		if err := connection.Table(table).Order("rowid ASC").Find(&rows).Error; err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		result[table] = rows
	}
	return result
}

// globalNetworkReleaseStageAssertUnchanged verifies rejected admission did not mutate the network, projects, or sequence.
func globalNetworkReleaseStageAssertUnchanged(t *testing.T, connection *gorm.DB, before map[string][]map[string]any) {
	t.Helper()
	if after := globalNetworkReleaseStageSnapshot(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected staging changed durable rows\nbefore: %#v\nafter: %#v", before, after)
	}
}

// globalNetworkReleaseStageExec applies one focused corruption fixture statement.
func globalNetworkReleaseStageExec(t *testing.T, connection *gorm.DB, statement string, values ...any) {
	t.Helper()
	if err := connection.Exec(statement, values...).Error; err != nil {
		t.Fatalf("execute fixture statement %q: %v", statement, err)
	}
}

// globalNetworkReleaseStageInsertOperation installs an independent operation conflict fixture without using the guarded enqueue path.
func globalNetworkReleaseStageInsertOperation(t *testing.T, connection *gorm.DB, id domain.OperationID, intent domain.IntentID, projectID domain.ProjectID, kind domain.OperationKind, state domain.OperationState, requestedAt time.Time) {
	t.Helper()
	operation := newOperationJournalTestOperation(t, id, intent, projectID, kind, requestedAt)
	if state != domain.OperationQueued {
		var err error
		operation, err = operation.Transition(domain.OperationRunning, "running", requestedAt, nil)
		if err == nil && state != domain.OperationRunning {
			operation, err = operation.Transition(state, string(state), requestedAt, nil)
		}
		if err != nil {
			t.Fatalf("advance fixture operation: %v", err)
		}
	}
	row, err := operationModelFromDomain(operation, 1)
	if err != nil {
		t.Fatalf("model fixture operation: %v", err)
	}
	if err := connection.Create(&row).Error; err != nil {
		t.Fatalf("insert fixture operation: %v", err)
	}
}
