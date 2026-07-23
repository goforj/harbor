package state

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// TestOperationJournalGlobalNetworkReleaseGuard blocks every ordinary journal mutation behind release authority.
func TestOperationJournalGlobalNetworkReleaseGuard(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	active := insertActiveGlobalNetworkReleaseGuardOperation(t, connection, "operation-global-release", "intent-global-release", domain.OperationRunning, requestedAt)
	if err := connection.Exec("UPDATE harbor_state SET sequence = 1 WHERE id = 1").Error; err != nil {
		t.Fatalf("set global release sequence: %v", err)
	}

	blocked := []struct {
		name      string
		kind      domain.OperationKind
		projectID domain.ProjectID
	}{
		{
			name: "network setup",
			kind: domain.OperationKindNetworkSetup,
		},
		{
			name: "resolver setup",
			kind: domain.OperationKindNetworkResolverSetup,
		},
		{
			name: "data-plane setup",
			kind: domain.OperationKindNetworkDataPlaneSetup,
		},
		{
			name: "release",
			kind: domain.OperationKindNetworkRelease,
		},
		{
			name:      "project start",
			kind:      domain.OperationKindProjectStart,
			projectID: "project-alpha",
		},
		{
			name:      "project stop",
			kind:      domain.OperationKindProjectStop,
			projectID: "project-alpha",
		},
		{
			name:      "project restart",
			kind:      domain.OperationKindProjectRestart,
			projectID: "project-alpha",
		},
		{
			name:      "project unregister",
			kind:      domain.OperationKindProjectUnregister,
			projectID: "project-alpha",
		},
	}
	for index, test := range blocked {
		t.Run(test.name, func(t *testing.T) {
			operation := newOperationJournalTestOperation(t, domain.OperationID(fmt.Sprintf("operation-blocked-%d", index)), domain.IntentID(fmt.Sprintf("intent-blocked-%d", index)), test.projectID, test.kind, requestedAt.Add(time.Minute))
			_, err := journal.Enqueue(context.Background(), operation)
			assertGlobalNetworkReleaseActive(t, err, active.Operation.ID, active.Operation.State, "operation journal")
			if sequence := mustOperationJournalSequence(t, journal); sequence != active.Revision {
				t.Fatalf("sequence after blocked %s = %d, want %d", test.name, sequence, active.Revision)
			}
		})
	}

}

// TestOperationJournalGlobalNetworkReleaseGuardRejectsReplay keeps ordinary journal reads from bypassing release authority.
func TestOperationJournalGlobalNetworkReleaseGuardRejectsReplay(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	original := newOperationJournalTestOperation(t, "operation-replay", "intent-replay", "project-alpha", domain.OperationKindProjectStart, requestedAt)
	first, err := journal.Enqueue(context.Background(), original)
	if err != nil {
		t.Fatalf("enqueue original operation: %v", err)
	}
	insertActiveGlobalNetworkReleaseGuardOperation(t, connection, "operation-global-release", "intent-global-release", domain.OperationRequiresApproval, requestedAt.Add(time.Minute))

	retry := newOperationJournalTestOperation(t, "operation-retry", original.IntentID, original.ProjectID, original.Kind, requestedAt.Add(2*time.Minute))
	_, err = journal.Enqueue(context.Background(), retry)
	assertGlobalNetworkReleaseActive(t, err, "operation-global-release", domain.OperationRequiresApproval, "operation journal")
	if sequence := mustOperationJournalSequence(t, journal); sequence != first.Revision {
		t.Fatalf("sequence after rejected replay = %d, want %d", sequence, first.Revision)
	}
}

// TestOperationJournalResolverPolicyMigrationGuardRejectsNewRuntime prevents routes from reappearing during approval.
func TestOperationJournalResolverPolicyMigrationGuardRejectsNewRuntime(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	globalNetworkReleaseStageInsertOperation(
		t,
		connection,
		"operation-resolver-policy-migration",
		"intent-resolver-policy-migration",
		"",
		domain.OperationKindNetworkResolverPolicyMigration,
		domain.OperationRequiresApproval,
		requestedAt,
	)
	if err := connection.Exec("UPDATE harbor_state SET sequence = 1 WHERE id = 1").Error; err != nil {
		t.Fatalf("set resolver policy migration sequence: %v", err)
	}
	start := newOperationJournalTestOperation(
		t,
		"operation-project-start",
		"intent-project-start",
		"project-alpha",
		domain.OperationKindProjectStart,
		requestedAt.Add(time.Minute),
	)

	_, err := journal.Enqueue(context.Background(), start)
	var active *NetworkResolverPolicyMigrationActiveError
	if !errors.As(err, &active) ||
		active.OperationID != "operation-resolver-policy-migration" ||
		active.State != domain.OperationRequiresApproval {
		t.Fatalf("Enqueue() error = %v, want active resolver policy migration", err)
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 1 {
		t.Fatalf("sequence after blocked project start = %d, want 1", sequence)
	}
}

// TestGlobalNetworkReleaseCoordinatorFreezesOrdinaryWriters proves every ordinary writer stops before changing durable state.
func TestGlobalNetworkReleaseCoordinatorFreezesOrdinaryWriters(t *testing.T) {
	journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
	staged, err := journal.StageGlobalNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	connections := journal.mutations.connections
	store := newStore(
		models.NewHarborStateRepo(connections),
		models.NewProjectRepo(connections),
		models.NewProjectSessionRepo(connections),
		models.NewNetworkStateRepo(connections),
		journal.mutations,
		projectStoreMutationTestClock,
	)
	project := registrationTestProject(
		"project-release-guard",
		"/work/release-guard",
		"release-guard",
		request.Operation.RequestedAt,
	)
	before := globalNetworkReleaseStageSnapshot(t, connection)

	_, err = store.RegisterProject(context.Background(), project)
	assertGlobalNetworkReleaseActive(t, err, staged.Operation.ID, staged.Operation.State, "project registration")
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)

	_, err = store.PutProject(context.Background(), project)
	assertGlobalNetworkReleaseActive(t, err, staged.Operation.ID, staged.Operation.State, "project projection")
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)

	_, err = store.ReplaceProjectNetwork(context.Background(), networkMutationTestReplaceRequest())
	assertGlobalNetworkReleaseActive(t, err, staged.Operation.ID, staged.Operation.State, "project network replacement")
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)

	_, err = journal.Transition(
		context.Background(),
		staged.Operation.ID,
		staged.Revision,
		domain.OperationRequiresApproval,
		"ordinary transition",
		request.Operation.RequestedAt.Add(time.Minute),
		nil,
	)
	assertGlobalNetworkReleaseActive(t, err, staged.Operation.ID, staged.Operation.State, "operation journal")
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)

	replayed, err := journal.StageGlobalNetworkRelease(context.Background(), request)
	if err != nil || replayed.Operation.ID != staged.Operation.ID || replayed.Revision != staged.Revision {
		t.Fatalf("stage replay through release bypass = %#v, error %v, want %#v", replayed, err, staged)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestGlobalNetworkReleaseCoordinatorRejectsCorruptCheckpointBeforeOrdinaryWriter proves the writer guard fails closed on an invalid release boundary.
func TestGlobalNetworkReleaseCoordinatorRejectsCorruptCheckpointBeforeOrdinaryWriter(t *testing.T) {
	journal, connection, request := newGlobalNetworkReleaseStageFixture(t)
	if _, err := journal.StageGlobalNetworkRelease(context.Background(), request); err != nil {
		t.Fatalf("stage global network release: %v", err)
	}
	globalNetworkReleaseStageExec(
		t,
		connection,
		"UPDATE network_global_release_plans SET checkpoint_revision = 999 WHERE id = 1",
	)
	before := globalNetworkReleaseStageSnapshot(t, connection)
	store := newStore(
		models.NewHarborStateRepo(journal.mutations.connections),
		models.NewProjectRepo(journal.mutations.connections),
		models.NewProjectSessionRepo(journal.mutations.connections),
		models.NewNetworkStateRepo(journal.mutations.connections),
		journal.mutations,
		projectStoreMutationTestClock,
	)
	project := registrationTestProject(
		"project-corrupt-release-guard",
		"/work/corrupt-release-guard",
		"corrupt-release-guard",
		request.Operation.RequestedAt,
	)

	_, err := store.RegisterProject(context.Background(), project)
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("project registration error = %v, want CorruptStateError", err)
	}
	globalNetworkReleaseStageAssertUnchanged(t, connection, before)
}

// TestOperationJournalGlobalNetworkReleaseGuardRejectsDirectRelease keeps the authority receipt inseparable from release enqueue.
func TestOperationJournalGlobalNetworkReleaseGuardRejectsDirectRelease(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	operation := newOperationJournalTestOperation(t, "operation-release", "intent-release", "", domain.OperationKindNetworkRelease, operationJournalTestTime())
	_, err := journal.Enqueue(context.Background(), operation)
	var required *GlobalNetworkReleaseAuthorityRequiredError
	if !errors.As(err, &required) || required.Action != "enqueue" {
		t.Fatalf("direct global release error = %v, want GlobalNetworkReleaseAuthorityRequiredError", err)
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 0 {
		t.Fatalf("sequence after direct release rejection = %d, want 0", sequence)
	}
}

// TestFindActiveGlobalNetworkReleaseOperationRejectsMultipleRows keeps a weakened schema from silently choosing an owner.
func TestFindActiveGlobalNetworkReleaseOperationRejectsMultipleRows(t *testing.T) {
	_, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	insertActiveGlobalNetworkReleaseGuardOperation(t, connection, "operation-global-release-a", "intent-global-release-a", domain.OperationQueued, requestedAt)
	insertActiveGlobalNetworkReleaseGuardOperation(t, connection, "operation-global-release-b", "intent-global-release-b", domain.OperationRunning, requestedAt.Add(time.Minute))
	_, _, err := findActiveGlobalNetworkReleaseOperation(connection)
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || corrupt.Entity != "global network release operation" {
		t.Fatalf("multiple active global releases error = %v, want CorruptStateError", err)
	}
}

// TestFindActiveGlobalNetworkReleaseOperationRejectsCorruptRow validates the selected owner before returning it.
func TestFindActiveGlobalNetworkReleaseOperationRejectsCorruptRow(t *testing.T) {
	_, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	insertActiveGlobalNetworkReleaseGuardOperation(t, connection, "operation-global-release", "intent-global-release", domain.OperationQueued, requestedAt)
	if err := connection.Model(&models.Operation{}).Where("id = ?", "operation-global-release").Update("revision", 0).Error; err != nil {
		t.Fatalf("corrupt active release revision: %v", err)
	}
	_, _, err := findActiveGlobalNetworkReleaseOperation(connection)
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || corrupt.Entity != "operation" {
		t.Fatalf("corrupt active global release error = %v, want operation corruption", err)
	}
}

// assertGlobalNetworkReleaseActive checks the stable owner details without coupling tests to prose.
func assertGlobalNetworkReleaseActive(t *testing.T, err error, operationID domain.OperationID, state domain.OperationState, action string) {
	t.Helper()
	var active *GlobalNetworkReleaseActiveError
	if !errors.As(err, &active) {
		t.Fatalf("global release guard error = %v, want GlobalNetworkReleaseActiveError", err)
	}
	if active.OperationID != operationID || active.State != state || active.Action != action {
		t.Fatalf("global release guard error = %#v", active)
	}
}

// insertActiveGlobalNetworkReleaseGuardOperation installs a release row directly so generic enqueue remains unable to bypass staging.
func insertActiveGlobalNetworkReleaseGuardOperation(t *testing.T, connection *gorm.DB, id domain.OperationID, intentID domain.IntentID, state domain.OperationState, requestedAt time.Time) OperationRecord {
	t.Helper()
	operation, err := domain.NewOperation(id, intentID, domain.OperationKindNetworkRelease, "", requestedAt)
	if err != nil {
		t.Fatalf("create active global release: %v", err)
	}
	if state == domain.OperationRequiresApproval {
		operation, err = operation.Transition(domain.OperationRunning, "release running", requestedAt.Add(time.Second), nil)
		if err == nil {
			operation, err = operation.Transition(state, fmt.Sprintf("release %s", state), requestedAt.Add(2*time.Second), nil)
		}
	} else if state != domain.OperationQueued {
		operation, err = operation.Transition(state, fmt.Sprintf("release %s", state), requestedAt.Add(time.Second), nil)
	}
	if err != nil {
		t.Fatalf("advance active global release: %v", err)
	}
	row, err := operationModelFromDomain(operation, 1)
	if err != nil {
		t.Fatalf("model active global release: %v", err)
	}
	if err := connection.Create(&row).Error; err != nil {
		t.Fatalf("insert active global release: %v", err)
	}
	return OperationRecord{
		Operation: operation,
		Revision:  1,
	}
}
