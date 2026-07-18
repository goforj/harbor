package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// assertProjectNetworkReleaseActive requires the stable typed freeze boundary without depending on evidence text.
func assertProjectNetworkReleaseActive(
	t *testing.T,
	err error,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	state ProjectNetworkReleaseState,
	action string,
) {
	t.Helper()
	var active *ProjectNetworkReleaseActiveError
	if !errors.As(err, &active) {
		t.Fatalf("release guard error = %v, want ProjectNetworkReleaseActiveError", err)
	}
	if active.ProjectID != projectID || active.OperationID != operationID || active.State != state || active.Action != action {
		t.Fatalf("release guard error = %#v", active)
	}
}

// installNetworkReleaseGuardSchema installs the complete empty optional schema into a legacy test database.
func installNetworkReleaseGuardSchema(t *testing.T, connection *gorm.DB) {
	t.Helper()
	for _, statement := range networkStoreReadTestSchema {
		if err := connection.Exec(statement).Error; err != nil {
			t.Fatalf("install network guard schema: %v", err)
		}
	}
}

// completeNetworkReleaseGuardTest advances a valid staged boundary without hiding any request identity.
func completeNetworkReleaseGuardTest(
	t *testing.T,
	store *Store,
	running OperationRecord,
	begin BeginProjectNetworkReleaseRequest,
	staged ProjectNetworkReleaseMutationResult,
) ProjectNetworkReleaseMutationResult {
	t.Helper()
	completedAt := begin.At.Add(10 * time.Minute)
	releases := make([]NetworkLeaseRelease, 0, len(staged.Release.ActiveLeases))
	for index, ensure := range staged.Release.ActiveLeases {
		releasedAt := completedAt.Add(-3 * time.Minute)
		quarantinedAt := completedAt.Add(-2 * time.Minute)
		releases = append(releases, NetworkLeaseRelease{
			Lease:             ensure.Lease,
			ReleaseGeneration: ensure.Generation + uint64(index) + 100,
			ReleaseEvidence:   "verified guard release",
			ReleasedAt:        releasedAt,
			QuarantinedAt:     quarantinedAt,
			ReuseAfter:        completedAt.Add(time.Hour),
			QuarantineReason:  "project unregister pending safe reuse",
		})
	}
	request := CompleteProjectNetworkReleaseRequest{
		ProjectID:                 begin.ProjectID,
		OperationID:               begin.OperationID,
		ExpectedNetworkRevision:   staged.Record.Revision,
		ExpectedProjectRevision:   begin.ExpectedProjectRevision,
		ExpectedOperationRevision: running.Revision,
		ExpectedBeginGeneration:   begin.BeginGeneration,
		CompletionGeneration:      begin.BeginGeneration + 1000,
		Releases:                  releases,
		ReleaseEvidence:           "verified complete guard release",
		At:                        completedAt,
	}
	completed, err := store.CompleteProjectNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("complete guard network release: %v", err)
	}
	if completed.Release.State != ProjectNetworkReleaseCompleted || completed.Replayed {
		t.Fatalf("completed guard release = %#v", completed)
	}
	return completed
}

// networkReleaseGuardTestGlobalOperation returns a valid operation with no project lifecycle owner.
func networkReleaseGuardTestGlobalOperation(t *testing.T, id domain.OperationID, at time.Time) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation(id, domain.IntentID("intent-"+string(id)), "harbor.refresh", "", at)
	if err != nil {
		t.Fatalf("create global operation: %v", err)
	}
	return operation
}

// TestProjectNetworkReleaseGuardFreezesCompletedBoundary verifies host teardown does not unfreeze retained project state.
func TestProjectNetworkReleaseGuardFreezesCompletedBoundary(t *testing.T) {
	store, _, journal, running, begin, _ := newNetworkReleaseTestHarness(t, 1)
	staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
	if err != nil {
		t.Fatalf("stage network release: %v", err)
	}
	completed := completeNetworkReleaseGuardTest(t, store, running, begin, staged)
	wantSequence := completed.Record.Revision
	project, err := store.Project(context.Background(), begin.ProjectID)
	if err != nil {
		t.Fatalf("read completed-boundary project: %v", err)
	}
	project.Project.Favorite = !project.Project.Favorite
	if _, err := store.PutProject(context.Background(), project.Project); err == nil {
		t.Fatal("put behind completed release unexpectedly succeeded")
	} else {
		assertProjectNetworkReleaseActive(
			t, err, begin.ProjectID, begin.OperationID, ProjectNetworkReleaseCompleted, "put project",
		)
	}
	if _, err := store.RecordRecentResource(
		context.Background(),
		domain.ResourceRef{ProjectID: begin.ProjectID, ResourceID: "docs"},
	); err == nil {
		t.Fatal("recency behind completed release unexpectedly succeeded")
	} else {
		assertProjectNetworkReleaseActive(
			t, err, begin.ProjectID, begin.OperationID, ProjectNetworkReleaseCompleted, "record recent resource",
		)
	}
	operation, err := domain.NewOperation(
		"operation-completed-release-blocked",
		"intent-completed-release-blocked",
		"project.refresh",
		begin.ProjectID,
		completed.Release.Completion.CompletedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("create completed-boundary operation: %v", err)
	}
	if _, err := journal.Enqueue(context.Background(), operation); err == nil {
		t.Fatal("enqueue behind completed release unexpectedly succeeded")
	} else {
		assertProjectNetworkReleaseActive(
			t, err, begin.ProjectID, begin.OperationID, ProjectNetworkReleaseCompleted, "enqueue operation",
		)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != wantSequence {
		t.Fatalf("sequence after completed-boundary rejections = %d, want %d", sequence, wantSequence)
	}
	approval, err := journal.Transition(
		context.Background(),
		running.Operation.ID,
		running.Revision,
		domain.OperationRequiresApproval,
		"waiting for final approval",
		completed.Release.Completion.CompletedAt.Add(2*time.Minute),
		nil,
	)
	if err != nil || approval.Revision != wantSequence+1 {
		t.Fatalf("approval transition behind completed release = %#v, error %v", approval, err)
	}
	problem := &domain.Problem{Code: "completion_failed", Message: "The completed release needs recovery."}
	_, err = journal.Transition(
		context.Background(),
		approval.Operation.ID,
		approval.Revision,
		domain.OperationFailed,
		"completion failed",
		completed.Release.Completion.CompletedAt.Add(3*time.Minute),
		problem,
	)
	assertProjectNetworkReleaseActive(
		t, err, begin.ProjectID, begin.OperationID, ProjectNetworkReleaseCompleted, "transition operation",
	)
	if sequence := projectStoreMutationSequence(t, store); sequence != approval.Revision {
		t.Fatalf("sequence after rejected completed-owner failure = %d, want %d", sequence, approval.Revision)
	}
}

// TestReadProjectNetworkReleaseBoundaryPropagatesDurableFailures covers each preflight layer before allocation.
func TestReadProjectNetworkReleaseBoundaryPropagatesDurableFailures(t *testing.T) {
	t.Run("row query", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		installNetworkReleaseGuardSchema(t, connection)
		want := errors.New("network release guard query sentinel")
		callback := "harbor:test_network_release_guard_query"
		if err := connection.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "network_pool_candidates" {
				tx.AddError(want)
			}
		}); err != nil {
			t.Fatalf("register guard query callback: %v", err)
		}
		t.Cleanup(func() { _ = connection.Callback().Query().Remove(callback) })
		_, err := readProjectNetworkReleaseBoundary(connection, "project-query")
		if !errors.Is(err, want) {
			t.Fatalf("guard row query error = %v, want sentinel", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
			t.Fatalf("sequence after guard query failure = %d, want 0", sequence)
		}
	})

	t.Run("aggregate conversion", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		installNetworkReleaseGuardSchema(t, connection)
		if err := connection.Exec(
			"INSERT INTO network_pool_candidates (network_state_id, ordinal, address, generation) VALUES (1, 0, '127.0.0.2', 1)",
		).Error; err != nil {
			t.Fatalf("insert orphan network child: %v", err)
		}
		_, err := readProjectNetworkReleaseBoundary(connection, "project-conversion")
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || corrupt.Entity != "network state" {
			t.Fatalf("guard conversion error = %v, want CorruptStateError", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
			t.Fatalf("sequence after guard conversion failure = %d, want 0", sequence)
		}
	})

	t.Run("release owner", func(t *testing.T) {
		store, connection, _, _, request, _ := newNetworkReleaseTestHarness(t, 1)
		staged, err := store.BeginProjectNetworkRelease(context.Background(), request)
		if err != nil {
			t.Fatalf("stage network release: %v", err)
		}
		if err := connection.Where("operation_id = ?", string(request.OperationID)).Delete(&models.OperationTransition{}).Error; err != nil {
			t.Fatalf("delete release owner history: %v", err)
		}
		_, err = readProjectNetworkReleaseBoundary(connection, request.ProjectID)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || corrupt.Entity != "operation" {
			t.Fatalf("guard owner error = %v, want operation corruption", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != staged.Record.Revision {
			t.Fatalf("sequence after guard owner failure = %d, want %d", sequence, staged.Record.Revision)
		}
	})
}
