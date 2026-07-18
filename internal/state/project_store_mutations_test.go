package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// TestStorePutProjectReplacesAggregateInForeignKeySafeOrder verifies complete replacement preserves every surviving identity.
func TestStorePutProjectReplacesAggregateInForeignKeySafeOrder(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	initial := projectStoreMutationTestProject("project-put")

	first, err := store.PutProject(nil, initial)
	if err != nil {
		t.Fatalf("put initial project: %v", err)
	}
	if first.Revision != 1 || !reflect.DeepEqual(first.Project, canonicalProjectStoreMutationProject(initial)) {
		t.Fatalf("initial record = %#v", first)
	}
	rootID := projectStoreMutationRowID(t, connection, "projects", "project_id", string(initial.ID))
	resourceID := projectStoreMutationScopedRowID(t, connection, "project_resources", "project_id", string(initial.ID), "resource_id", "docs")

	recent, err := store.RecordRecentResource(context.Background(), domain.ResourceRef{ProjectID: initial.ID, ResourceID: "docs"})
	if err != nil {
		t.Fatalf("record initial recent resource: %v", err)
	}
	if recent.Sequence != 2 {
		t.Fatalf("initial recent sequence = %d, want 2", recent.Sequence)
	}
	recentID := projectStoreMutationScopedRowID(t, connection, "recent_resources", "project_id", string(initial.ID), "resource_id", "docs")

	replacement := domain.ProjectSnapshot{
		ID:        initial.ID,
		Name:      "Replacement",
		Path:      initial.Path,
		Slug:      initial.Slug,
		State:     domain.ProjectDegraded,
		Favorite:  true,
		UpdatedAt: initial.UpdatedAt.Add(time.Hour),
		Apps: []domain.AppSnapshot{
			{ID: "worker", Name: "Worker", State: domain.EntityWorking, Active: true, Required: true},
		},
		Services: []domain.ServiceSnapshot{
			{ID: "redis", Name: "Redis", Kind: "cache", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true},
		},
		Resources: []domain.ResourceSnapshot{
			{ID: "docs", Name: "Documentation", Kind: "documentation", URL: "https://replacement.test/docs", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "redis"}},
		},
	}
	second, err := store.PutProject(context.Background(), replacement)
	if err != nil {
		t.Fatalf("replace project while moving resource owner: %v", err)
	}
	if second.Revision != 3 || !reflect.DeepEqual(second.Project, replacement) {
		t.Fatalf("replacement record = %#v, want %#v at revision 3", second, replacement)
	}
	if got := projectStoreMutationRowID(t, connection, "projects", "project_id", string(initial.ID)); got != rootID {
		t.Fatalf("project surrogate ID = %d, want preserved %d", got, rootID)
	}
	if got := projectStoreMutationScopedRowID(t, connection, "project_resources", "project_id", string(initial.ID), "resource_id", "docs"); got != resourceID {
		t.Fatalf("resource surrogate ID = %d, want preserved %d", got, resourceID)
	}
	if got := projectStoreMutationScopedRowID(t, connection, "recent_resources", "project_id", string(initial.ID), "resource_id", "docs"); got != recentID {
		t.Fatalf("recent surrogate ID = %d, want preserved %d", got, recentID)
	}
	for _, stale := range []struct {
		table  string
		column string
		value  string
	}{
		{table: "project_apps", column: "app_id", value: "api"},
		{table: "project_apps", column: "app_id", value: "old-worker"},
		{table: "project_services", column: "service_id", value: "mysql"},
		{table: "project_services", column: "service_id", value: "old-cache"},
		{table: "project_resources", column: "resource_id", value: "old-app-resource"},
		{table: "project_resources", column: "resource_id", value: "old-service-resource"},
	} {
		if count := projectStoreMutationScopedCount(t, connection, stale.table, "project_id", string(initial.ID), stale.column, stale.value); count != 0 {
			t.Fatalf("stale %s %q count = %d, want 0", stale.table, stale.value, count)
		}
	}

	empty := replacement
	empty.Apps = []domain.AppSnapshot{}
	empty.Services = []domain.ServiceSnapshot{}
	empty.Resources = []domain.ResourceSnapshot{}
	empty.UpdatedAt = empty.UpdatedAt.Add(time.Hour)
	third, err := store.PutProject(context.Background(), empty)
	if err != nil {
		t.Fatalf("replace project with empty child sets: %v", err)
	}
	if third.Revision != 4 || !reflect.DeepEqual(third.Project, empty) {
		t.Fatalf("empty replacement = %#v, want %#v at revision 4", third, empty)
	}
	for _, table := range []string{"project_apps", "project_services", "project_resources", "recent_resources"} {
		if count := projectStoreMutationCount(t, connection, table); count != 0 {
			t.Fatalf("%s count after empty replacement = %d, want 0", table, count)
		}
	}

	repeated, err := store.PutProject(context.Background(), empty)
	if err != nil {
		t.Fatalf("repeat same project shape: %v", err)
	}
	if repeated.Revision != 5 || projectStoreMutationRowID(t, connection, "projects", "project_id", string(initial.ID)) != rootID {
		t.Fatalf("repeated record = %#v or root identity changed", repeated)
	}
}

// TestStorePutProjectValidatesBeforeStorage verifies invalid input and cancelled work consume no sequence.
func TestStorePutProjectValidatesBeforeStorage(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	invalid := projectStoreMutationTestProject("project-invalid")
	invalid.Resources = nil
	if _, err := store.PutProject(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "project collections must not be nil: resources") {
		t.Fatalf("invalid project error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.PutProject(cancelled, projectStoreMutationTestProject("project-cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled put error = %v, want context.Canceled", err)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
		t.Fatalf("sequence after rejected puts = %d, want 0", sequence)
	}
}

// TestStorePutProjectRollsBackLateFailures verifies partial normalized writes and sequence allocation never escape a transaction.
func TestStorePutProjectRollsBackLateFailures(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	mustProjectStoreReadExec(t, connection, `CREATE TRIGGER fail_project_resource BEFORE INSERT ON project_resources BEGIN SELECT RAISE(ABORT, 'late resource failure'); END`)

	project := projectStoreMutationTestProject("project-rollback")
	if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "late resource failure") {
		t.Fatalf("late put error = %v", err)
	}
	for _, table := range []string{"projects", "project_apps", "project_services", "project_resources"} {
		if count := projectStoreMutationCount(t, connection, table); count != 0 {
			t.Fatalf("%s count after rollback = %d, want 0", table, count)
		}
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
		t.Fatalf("sequence after rolled-back put = %d, want 0", sequence)
	}

	mustProjectStoreReadExec(t, connection, "DROP TRIGGER fail_project_resource")
	result, err := store.PutProject(context.Background(), project)
	if err != nil || result.Revision != 1 {
		t.Fatalf("recovered put = %#v, error %v, want revision 1", result, err)
	}
}

// TestStorePutProjectRollsBackUniqueConflicts verifies one project's path or slug cannot displace another project.
func TestStorePutProjectRollsBackUniqueConflicts(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	first := projectStoreMutationTestProject("project-first")
	if _, err := store.PutProject(context.Background(), first); err != nil {
		t.Fatalf("put first project: %v", err)
	}
	conflict := projectStoreMutationTestProject("project-conflict")
	conflict.Path = first.Path
	if _, err := store.PutProject(context.Background(), conflict); err == nil {
		t.Fatal("conflicting project path unexpectedly committed")
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
		t.Fatalf("sequence after unique conflict = %d, want 1", sequence)
	}
	if count := projectStoreMutationCount(t, connection, "projects"); count != 1 {
		t.Fatalf("project count after unique conflict = %d, want 1", count)
	}
}

// TestStoreProjectWritersFreezeStagedNetworkRelease verifies topology and recency cannot move after route suppression.
func TestStoreProjectWritersFreezeStagedNetworkRelease(t *testing.T) {
	store, _, _, running, request, _ := newNetworkReleaseTestHarness(t, 1)
	staged, err := store.BeginProjectNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("stage network release: %v", err)
	}
	wantSequence := staged.Record.Revision
	target, err := store.Project(context.Background(), request.ProjectID)
	if err != nil {
		t.Fatalf("read target project: %v", err)
	}
	changed := target.Project
	changed.Name = "changed during release"
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Minute)
	if _, err := store.PutProject(context.Background(), changed); err == nil {
		t.Fatal("put during release unexpectedly succeeded")
	} else {
		assertProjectNetworkReleaseActive(
			t, err, request.ProjectID, running.Operation.ID, ProjectNetworkReleaseReleasing, "put project",
		)
	}
	reference := domain.ResourceRef{ProjectID: request.ProjectID, ResourceID: "docs"}
	if _, err := store.RecordRecentResource(context.Background(), reference); err == nil {
		t.Fatal("recency during release unexpectedly succeeded")
	} else {
		assertProjectNetworkReleaseActive(
			t, err, request.ProjectID, running.Operation.ID, ProjectNetworkReleaseReleasing, "record recent resource",
		)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != wantSequence {
		t.Fatalf("sequence after frozen writers = %d, want %d", sequence, wantSequence)
	}
	persisted, err := store.Project(context.Background(), request.ProjectID)
	if err != nil || persisted.Project.Name == changed.Name {
		t.Fatalf("target after frozen put = %#v, error %v", persisted, err)
	}

	other, err := store.Project(context.Background(), "project-beta")
	if err != nil {
		t.Fatalf("read other project: %v", err)
	}
	other.Project.Favorite = !other.Project.Favorite
	other.Project.UpdatedAt = other.Project.UpdatedAt.Add(time.Minute)
	updated, err := store.PutProject(context.Background(), other.Project)
	if err != nil {
		t.Fatalf("put unrelated project during release: %v", err)
	}
	if updated.Revision != wantSequence+1 {
		t.Fatalf("unrelated project revision = %d, want %d", updated.Revision, wantSequence+1)
	}
}

// TestStorePutProjectRejectsCompletedReleaseTombstone verifies deletion cannot make a project ID writable again.
func TestStorePutProjectRejectsCompletedReleaseTombstone(t *testing.T) {
	store, connection, _, running, request, _ := newNetworkReleaseTestHarness(t, 1)
	project, err := store.Project(context.Background(), request.ProjectID)
	if err != nil {
		t.Fatalf("read project before release: %v", err)
	}
	staged, err := store.BeginProjectNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("stage network release: %v", err)
	}
	completed := completeNetworkReleaseGuardTest(t, store, running, request, staged)
	removedAt := completed.Release.Completion.CompletedAt.Add(time.Minute)
	removed, err := store.CompleteProjectUnregister(
		context.Background(), request.ProjectID, request.OperationID, running.Revision, "project removed", removedAt,
	)
	if err != nil || removed.Operation.State != domain.OperationSucceeded {
		t.Fatalf("complete project removal = %#v, error %v", removed, err)
	}
	var sourceProjectID string
	var activeProjectID *string
	if err := connection.Raw(
		"SELECT source_project_id, project_id FROM network_project_releases WHERE operation_id = ?",
		string(request.OperationID),
	).Row().Scan(&sourceProjectID, &activeProjectID); err != nil {
		t.Fatalf("read completed tombstone identity: %v", err)
	}
	if sourceProjectID != string(request.ProjectID) || activeProjectID != nil {
		t.Fatalf("completed tombstone identity = source %q active %#v", sourceProjectID, activeProjectID)
	}
	wantSequence := removed.Revision
	if _, err := store.PutProject(context.Background(), project.Project); err == nil {
		t.Fatal("project resurrection over tombstone unexpectedly succeeded")
	} else {
		assertProjectNetworkReleaseActive(
			t, err, request.ProjectID, request.OperationID, ProjectNetworkReleaseCompleted, "put project",
		)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != wantSequence {
		t.Fatalf("sequence after rejected resurrection = %d, want %d", sequence, wantSequence)
	}
}

// TestStoreProjectWritersPreserveOptionalNetworkLifecycle verifies legacy and fully migrated empty databases stay writable.
func TestStoreProjectWritersPreserveOptionalNetworkLifecycle(t *testing.T) {
	for _, test := range []struct {
		name    string
		install func(*testing.T, *gorm.DB)
	}{
		{name: "legacy"},
		{name: "migrated empty", install: installNetworkReleaseGuardSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			if test.install != nil {
				test.install(t, connection)
			}
			project := projectStoreMutationTestProject(domain.ProjectID("project-" + strings.ReplaceAll(test.name, " ", "-")))
			put, err := store.PutProject(context.Background(), project)
			if err != nil || put.Revision != 1 {
				t.Fatalf("put optional-network project = %#v, error %v", put, err)
			}
			recent, err := store.RecordRecentResource(
				context.Background(),
				domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"},
			)
			if err != nil || recent.Sequence != 2 {
				t.Fatalf("record optional-network recency = %#v, error %v", recent, err)
			}
		})
	}
}

// TestStorePutProjectRejectsPartialNetworkSchema verifies optional persistence cannot be mistaken for migrated emptiness.
func TestStorePutProjectRejectsPartialNetworkSchema(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	if err := connection.Exec(networkStoreReadTestSchema[0]).Error; err != nil {
		t.Fatalf("install partial network schema: %v", err)
	}
	_, err := store.PutProject(context.Background(), projectStoreMutationTestProject("project-partial-network"))
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "network persistence schema is incomplete") {
		t.Fatalf("partial-schema put error = %v", err)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
		t.Fatalf("sequence after partial-schema put = %d, want 0", sequence)
	}
}

// TestStoreCompleteProjectUnregisterCommitsAndReplaysAtomically verifies removal and success share one sequence and exact retries consume none.
func TestStoreCompleteProjectUnregisterCommitsAndReplaysAtomically(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-unregister")
	journal, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-unregister")

	completed, err := store.CompleteProjectUnregister(
		context.Background(),
		project.ID,
		running.Operation.ID,
		running.Revision,
		"project removed",
		completedAt,
	)
	if err != nil {
		t.Fatalf("complete project unregister: %v", err)
	}
	if completed.Revision != 4 || completed.Operation.State != domain.OperationSucceeded || completed.Operation.Phase != "project removed" {
		t.Fatalf("completed unregister = %#v, want succeeded revision 4", completed)
	}
	for _, table := range []string{"projects", "project_apps", "project_services", "project_resources", "recent_resources"} {
		if count := projectStoreMutationCount(t, connection, table); count != 0 {
			t.Fatalf("%s count after unregister = %d, want 0", table, count)
		}
	}
	history, err := journal.Transitions(context.Background(), running.Operation.ID)
	if err != nil {
		t.Fatalf("read unregister history: %v", err)
	}
	if len(history) != 3 || history[2].Sequence != 4 || history[2].State != domain.OperationSucceeded {
		t.Fatalf("unregister history = %#v, want one succeeded edge at sequence 4", history)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("read snapshot after unregister: %v", err)
	}
	if snapshot.Sequence != 4 || len(snapshot.Projects) != 0 || len(snapshot.Operations) != 0 {
		t.Fatalf("snapshot after unregister = %#v", snapshot)
	}

	replayed, err := store.CompleteProjectUnregister(
		context.Background(),
		project.ID,
		running.Operation.ID,
		running.Revision,
		"project removed",
		completedAt.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("replay project unregister: %v", err)
	}
	if !reflect.DeepEqual(replayed, completed) {
		t.Fatalf("replayed unregister = %#v, want %#v", replayed, completed)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 4 {
		t.Fatalf("sequence after unregister replay = %d, want 4", sequence)
	}
	if history, err = journal.Transitions(context.Background(), running.Operation.ID); err != nil || len(history) != 3 {
		t.Fatalf("history after replay = %#v, error %v", history, err)
	}

	for _, revision := range []domain.Sequence{completed.Revision, running.Revision - 1} {
		_, err = store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, revision, "project removed", completedAt.Add(2*time.Hour),
		)
		var stale *StaleRevisionError
		if !errors.As(err, &stale) || stale.Expected != revision || stale.Actual != running.Revision {
			t.Fatalf("replay at revision %d error = %v, want stale against %d", revision, err, running.Revision)
		}
	}
	if _, err := store.CompleteProjectUnregister(
		context.Background(), project.ID, running.Operation.ID, running.Revision, "different phase", completedAt.Add(3*time.Hour),
	); err == nil || !strings.Contains(err.Error(), "completed project unregister phase") {
		t.Fatalf("phase-mismatched replay error = %v", err)
	}
}

// TestStoreCompleteProjectUnregisterCommitsInitializedNetworkBoundary verifies final deletion retains exact release evidence.
func TestStoreCompleteProjectUnregisterCommitsInitializedNetworkBoundary(t *testing.T) {
	fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
	beforeRows := networkReplaceTestRows(t, fixture.connection)
	beforeMarker := networkReleaseTestMarker(t, beforeRows.Releases, fixture.running.Operation.ID)
	beforeNetwork, initialized, err := fixture.store.Network(context.Background())
	if err != nil || !initialized || beforeNetwork.Revision != 12 {
		t.Fatalf("network before final unregister = %#v, %t, %v", beforeNetwork, initialized, err)
	}

	completed, err := fixture.store.CompleteProjectUnregister(
		context.Background(),
		fixture.begin.ProjectID,
		fixture.running.Operation.ID,
		fixture.running.Revision,
		"project removed",
		fixture.completedAt,
	)
	if err != nil {
		t.Fatalf("complete initialized project unregister: %v", err)
	}
	if completed.Revision != 13 || completed.Operation.State != domain.OperationSucceeded ||
		completed.Operation.Phase != "project removed" {
		t.Fatalf("completed initialized unregister = %#v", completed)
	}
	afterRows := networkReplaceTestRows(t, fixture.connection)
	if len(afterRows.Projects) != len(beforeRows.Projects)-1 {
		t.Fatalf("network project rows after unregister = %#v", afterRows.Projects)
	}
	for _, row := range afterRows.Projects {
		if row.ProjectId == string(fixture.begin.ProjectID) {
			t.Fatalf("deleted project remains in network owner rows: %#v", row)
		}
	}
	afterMarker := networkReleaseTestMarker(t, afterRows.Releases, fixture.running.Operation.ID)
	if !reflect.DeepEqual(afterMarker, beforeMarker) || !afterMarker.ReleaseSetDigest.Valid {
		t.Fatalf("release marker changed during final unregister: got %#v want %#v", afterMarker, beforeMarker)
	}
	if !reflect.DeepEqual(afterRows.States, beforeRows.States) {
		t.Fatalf("network root advanced during project unregister: got %#v want %#v", afterRows.States, beforeRows.States)
	}
	afterNetwork, initialized, err := fixture.store.Network(context.Background())
	if err != nil || !initialized || !reflect.DeepEqual(afterNetwork, beforeNetwork) {
		t.Fatalf("network after final unregister = %#v, %t, %v; want %#v", afterNetwork, initialized, err, beforeNetwork)
	}
	release, found, err := fixture.store.ProjectNetworkRelease(context.Background(), fixture.running.Operation.ID)
	if err != nil || !found || !reflect.DeepEqual(release, fixture.release.Release) {
		t.Fatalf("release after final unregister = %#v, %t, %v", release, found, err)
	}
	if _, err := fixture.store.Project(context.Background(), fixture.begin.ProjectID); err == nil {
		t.Fatal("deleted project remained readable")
	} else {
		var missing *ProjectNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("deleted project read error = %v", err)
		}
	}
	history, err := fixture.journal.Transitions(context.Background(), fixture.running.Operation.ID)
	if err != nil || len(history) != 3 || history[2].Sequence != completed.Revision ||
		history[2].State != domain.OperationSucceeded {
		t.Fatalf("initialized unregister history = %#v, %v", history, err)
	}
	if sequence := projectStoreMutationSequence(t, fixture.store); sequence != completed.Revision {
		t.Fatalf("Harbor sequence after initialized unregister = %d, want %d", sequence, completed.Revision)
	}

	beforeReplay := networkReplaceTestRows(t, fixture.connection)
	replayed, err := fixture.store.CompleteProjectUnregister(
		context.Background(),
		fixture.begin.ProjectID,
		fixture.running.Operation.ID,
		fixture.running.Revision,
		"project removed",
		fixture.completedAt.Add(time.Hour),
	)
	if err != nil || !reflect.DeepEqual(replayed, completed) {
		t.Fatalf("initialized unregister replay = %#v, %v; want %#v", replayed, err, completed)
	}
	if afterReplay := networkReplaceTestRows(t, fixture.connection); !reflect.DeepEqual(afterReplay, beforeReplay) {
		t.Fatal("initialized unregister replay changed durable network rows")
	}
	if sequence := projectStoreMutationSequence(t, fixture.store); sequence != completed.Revision {
		t.Fatalf("Harbor sequence after initialized replay = %d, want %d", sequence, completed.Revision)
	}
}

// TestStoreCompleteProjectUnregisterRequiresCompletedNetworkRelease verifies every initialized teardown gate.
func TestStoreCompleteProjectUnregisterRequiresCompletedNetworkRelease(t *testing.T) {
	t.Run("missing marker", func(t *testing.T) {
		store, connection, _, running, begin, _ := newNetworkReleaseTestHarness(t, 1)
		before := networkReplaceTestRows(t, connection)
		_, err := store.CompleteProjectUnregister(
			context.Background(), begin.ProjectID, running.Operation.ID, running.Revision, "project removed", begin.At.Add(time.Minute),
		)
		var missing *ProjectNetworkReleaseNotFoundError
		if !errors.As(err, &missing) || missing.ProjectID != begin.ProjectID || missing.OperationID != running.Operation.ID {
			t.Fatalf("missing network release error = %v", err)
		}
		assertProjectStoreMutationNetworkUnregisterUnchanged(t, store, connection, before, running, 10)
	})

	t.Run("releasing marker", func(t *testing.T) {
		store, connection, _, running, begin, _ := newNetworkReleaseTestHarness(t, 1)
		if _, err := store.BeginProjectNetworkRelease(context.Background(), begin); err != nil {
			t.Fatalf("begin project network release: %v", err)
		}
		before := networkReplaceTestRows(t, connection)
		_, err := store.CompleteProjectUnregister(
			context.Background(), begin.ProjectID, running.Operation.ID, running.Revision, "project removed", begin.At.Add(time.Minute),
		)
		var incomplete *ProjectNetworkReleaseIncompleteError
		if !errors.As(err, &incomplete) || incomplete.State != ProjectNetworkReleaseReleasing {
			t.Fatalf("incomplete network release error = %v", err)
		}
		assertProjectStoreMutationNetworkUnregisterUnchanged(t, store, connection, before, running, 11)
	})

	t.Run("wrong operation", func(t *testing.T) {
		fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
		before := networkReplaceTestRows(t, fixture.connection)
		_, err := fixture.store.CompleteProjectUnregister(
			context.Background(), fixture.begin.ProjectID, "operation-other", fixture.running.Revision, "project removed", fixture.completedAt,
		)
		var conflict *ProjectNetworkReleaseConflictError
		if !errors.As(err, &conflict) || conflict.Difference != "operation owner" {
			t.Fatalf("wrong release operation error = %v", err)
		}
		assertProjectStoreMutationNetworkUnregisterUnchanged(
			t, fixture.store, fixture.connection, before, fixture.running, 12,
		)
	})

	t.Run("owner requires approval", func(t *testing.T) {
		fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
		approval, err := fixture.journal.Transition(
			context.Background(),
			fixture.running.Operation.ID,
			fixture.running.Revision,
			domain.OperationRequiresApproval,
			"waiting for final approval",
			fixture.completedAt,
			nil,
		)
		if err != nil || approval.Revision != 13 {
			t.Fatalf("approval transition = %#v, %v", approval, err)
		}
		before := networkReplaceTestRows(t, fixture.connection)
		_, err = fixture.store.CompleteProjectUnregister(
			context.Background(), fixture.begin.ProjectID, approval.Operation.ID, approval.Revision, "project removed", fixture.completedAt.Add(time.Second),
		)
		if err == nil || !strings.Contains(err.Error(), "must be running") {
			t.Fatalf("approval-owned unregister error = %v", err)
		}
		assertProjectStoreMutationNetworkUnregisterUnchanged(
			t, fixture.store, fixture.connection, before, approval, 13,
		)
	})

	t.Run("stale owner", func(t *testing.T) {
		fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
		before := networkReplaceTestRows(t, fixture.connection)
		_, err := fixture.store.CompleteProjectUnregister(
			context.Background(), fixture.begin.ProjectID, fixture.running.Operation.ID, fixture.running.Revision-1, "project removed", fixture.completedAt,
		)
		var stale *StaleRevisionError
		if !errors.As(err, &stale) || stale.Actual != fixture.running.Revision {
			t.Fatalf("stale initialized unregister error = %v", err)
		}
		assertProjectStoreMutationNetworkUnregisterUnchanged(
			t, fixture.store, fixture.connection, before, fixture.running, 12,
		)
	})
}

// TestStoreCompleteProjectUnregisterPreservesUninitializedNetworkCompatibility verifies migrations alone do not require teardown.
func TestStoreCompleteProjectUnregisterPreservesUninitializedNetworkCompatibility(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-uninitialized-network")
	_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-uninitialized-network")
	installNetworkReleaseGuardSchema(t, connection)

	completed, err := store.CompleteProjectUnregister(
		context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
	)
	if err != nil || completed.Revision != 4 || completed.Operation.State != domain.OperationSucceeded {
		t.Fatalf("uninitialized-network unregister = %#v, %v", completed, err)
	}
	if network, initialized, err := store.Network(context.Background()); err != nil || initialized ||
		!reflect.DeepEqual(network, NetworkRecord{}) {
		t.Fatalf("uninitialized network after unregister = %#v, %t, %v", network, initialized, err)
	}
}

// TestStoreCompleteProjectUnregisterRollsBackInitializedNetworkFailures verifies finalization cannot partially consume its release proof.
func TestStoreCompleteProjectUnregisterRollsBackInitializedNetworkFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		triggerSQL string
		want       string
	}{
		{
			name:       "project delete",
			triggerSQL: `CREATE TRIGGER fail_initialized_unregister_delete BEFORE DELETE ON projects BEGIN SELECT RAISE(ABORT, 'initialized delete failure'); END`,
			want:       "initialized delete failure",
		},
		{
			name:       "operation update",
			triggerSQL: `CREATE TRIGGER fail_initialized_unregister_operation BEFORE UPDATE OF state ON operations BEGIN SELECT RAISE(ABORT, 'initialized operation failure'); END`,
			want:       "initialized operation failure",
		},
		{
			name:       "transition append",
			triggerSQL: `CREATE TRIGGER fail_initialized_unregister_transition BEFORE INSERT ON operation_transitions WHEN NEW.state = 'succeeded' BEGIN SELECT RAISE(ABORT, 'initialized transition failure'); END`,
			want:       "initialized transition failure",
		},
		{
			name: "release marker readback",
			triggerSQL: `CREATE TRIGGER corrupt_initialized_unregister_marker AFTER DELETE ON projects
				BEGIN
					UPDATE network_project_releases
					SET release_set_digest = '0000000000000000000000000000000000000000000000000000000000000000'
					WHERE source_project_id = OLD.project_id;
				END`,
			want: "changed durable network facts",
		},
		{
			name: "release boundary disappearance",
			triggerSQL: `CREATE TRIGGER remove_initialized_unregister_marker AFTER DELETE ON projects
				BEGIN DELETE FROM network_project_releases WHERE source_project_id = OLD.project_id; END`,
			want: "completed boundary disappeared",
		},
		{
			name: "network root readback",
			triggerSQL: `CREATE TRIGGER corrupt_initialized_unregister_root AFTER DELETE ON projects
				BEGIN UPDATE network_state SET revision = revision + 1 WHERE id = 1; END`,
			want: "network state",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
			before := networkReplaceTestRows(t, fixture.connection)
			beforeHistory, err := fixture.journal.Transitions(context.Background(), fixture.running.Operation.ID)
			if err != nil {
				t.Fatalf("read history before rollback test: %v", err)
			}
			mustProjectStoreReadExec(t, fixture.connection, test.triggerSQL)
			_, err = fixture.store.CompleteProjectUnregister(
				context.Background(),
				fixture.begin.ProjectID,
				fixture.running.Operation.ID,
				fixture.running.Revision,
				"project removed",
				fixture.completedAt,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s error = %v, want %q", test.name, err, test.want)
			}
			assertProjectStoreMutationNetworkUnregisterUnchanged(
				t, fixture.store, fixture.connection, before, fixture.running, 12,
			)
			afterHistory, readErr := fixture.journal.Transitions(context.Background(), fixture.running.Operation.ID)
			if readErr != nil || !reflect.DeepEqual(afterHistory, beforeHistory) {
				t.Fatalf("history after %s rollback = %#v, %v; want %#v", test.name, afterHistory, readErr, beforeHistory)
			}
		})
	}
}

// TestStoreCompleteProjectUnregisterRejectsInitializedNetworkCorruption verifies every durable authority fails closed.
func TestStoreCompleteProjectUnregisterRejectsInitializedNetworkCorruption(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, OperationRecord)
	}{
		{
			name: "network root",
			mutate: func(t *testing.T, connection *gorm.DB, _ OperationRecord) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "PRAGMA ignore_check_constraints = ON")
				mustProjectStoreReadExec(t, connection, "UPDATE network_state SET dns_suffix = '.invalid' WHERE id = 1")
				mustProjectStoreReadExec(t, connection, "PRAGMA ignore_check_constraints = OFF")
			},
		},
		{
			name: "release marker",
			mutate: func(t *testing.T, connection *gorm.DB, operation OperationRecord) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "PRAGMA ignore_check_constraints = ON")
				mustProjectStoreReadExec(t, connection, "UPDATE network_project_releases SET release_set_digest = 'invalid' WHERE operation_id = ?", operation.Operation.ID)
				mustProjectStoreReadExec(t, connection, "PRAGMA ignore_check_constraints = OFF")
			},
		},
		{
			name: "release owner",
			mutate: func(t *testing.T, connection *gorm.DB, operation OperationRecord) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "UPDATE operations SET kind = 'maintenance.run' WHERE id = ?", operation.Operation.ID)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
			test.mutate(t, fixture.connection, fixture.running)
			before := networkReplaceTestRows(t, fixture.connection)
			_, err := fixture.store.CompleteProjectUnregister(
				context.Background(),
				fixture.begin.ProjectID,
				fixture.running.Operation.ID,
				fixture.running.Revision,
				"project removed",
				fixture.completedAt,
			)
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) {
				t.Fatalf("%s corruption error = %v", test.name, err)
			}
			if after := networkReplaceTestRows(t, fixture.connection); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s rejection changed network rows", test.name)
			}
			if _, readErr := fixture.store.Project(context.Background(), fixture.begin.ProjectID); readErr != nil {
				t.Fatalf("project disappeared after %s rejection: %v", test.name, readErr)
			}
			if sequence := projectStoreMutationSequence(t, fixture.store); sequence != 12 {
				t.Fatalf("sequence after %s rejection = %d, want 12", test.name, sequence)
			}
		})
	}

	t.Run("replay marker", func(t *testing.T) {
		fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 1)
		completed, err := fixture.store.CompleteProjectUnregister(
			context.Background(),
			fixture.begin.ProjectID,
			fixture.running.Operation.ID,
			fixture.running.Revision,
			"project removed",
			fixture.completedAt,
		)
		if err != nil || completed.Revision != 13 {
			t.Fatalf("seed initialized unregister replay = %#v, %v", completed, err)
		}
		mustProjectStoreReadExec(
			t,
			fixture.connection,
			"PRAGMA ignore_check_constraints = ON",
		)
		mustProjectStoreReadExec(
			t,
			fixture.connection,
			"UPDATE network_project_releases SET release_set_digest = 'invalid' WHERE operation_id = ?",
			fixture.running.Operation.ID,
		)
		mustProjectStoreReadExec(t, fixture.connection, "PRAGMA ignore_check_constraints = OFF")
		_, err = fixture.store.CompleteProjectUnregister(
			context.Background(),
			fixture.begin.ProjectID,
			fixture.running.Operation.ID,
			fixture.running.Revision,
			"project removed",
			fixture.completedAt.Add(time.Hour),
		)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) {
			t.Fatalf("corrupt initialized replay error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, fixture.store); sequence != 13 {
			t.Fatalf("sequence after corrupt initialized replay = %d, want 13", sequence)
		}
	})
}

// TestStoreCompleteProjectUnregisterInitializedConcurrentRetryAllocatesOnce verifies one final transition under contention.
func TestStoreCompleteProjectUnregisterInitializedConcurrentRetryAllocatesOnce(t *testing.T) {
	fixture := newProjectStoreMutationNetworkUnregisterFixture(t, 4)
	before := networkReplaceTestRows(t, fixture.connection)
	start := make(chan struct{})
	results := make(chan struct {
		record OperationRecord
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			record, err := fixture.store.CompleteProjectUnregister(
				context.Background(),
				fixture.begin.ProjectID,
				fixture.running.Operation.ID,
				fixture.running.Revision,
				"project removed",
				fixture.completedAt,
			)
			results <- struct {
				record OperationRecord
				err    error
			}{record: record, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent initialized unregister errors = %v and %v", first.err, second.err)
	}
	if !reflect.DeepEqual(first.record, second.record) || first.record.Revision != 13 {
		t.Fatalf("concurrent initialized unregister records = %#v and %#v", first.record, second.record)
	}
	after := networkReplaceTestRows(t, fixture.connection)
	if !reflect.DeepEqual(after.States, before.States) || !reflect.DeepEqual(after.Releases, before.Releases) {
		t.Fatalf("concurrent initialized unregister changed root or release: before %#v after %#v", before, after)
	}
	if sequence := projectStoreMutationSequence(t, fixture.store); sequence != 13 {
		t.Fatalf("sequence after concurrent initialized unregister = %d, want 13", sequence)
	}
	history, err := fixture.journal.Transitions(context.Background(), fixture.running.Operation.ID)
	if err != nil || len(history) != 3 || history[2].Sequence != 13 {
		t.Fatalf("history after concurrent initialized unregister = %#v, %v", history, err)
	}
	if _, err := fixture.store.Project(context.Background(), fixture.begin.ProjectID); err == nil {
		t.Fatal("project survived concurrent initialized unregister")
	}
}

// TestStoreCompleteProjectUnregisterRejectsCompetingWork verifies the owning operation is excluded while every other active operation blocks removal.
func TestStoreCompleteProjectUnregisterRejectsCompetingWork(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-unregister-busy")
	journal, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-owner")
	for _, identity := range []struct {
		operation domain.OperationID
		intent    domain.IntentID
	}{
		{operation: "operation-z", intent: "intent-z"},
		{operation: "operation-a", intent: "intent-a"},
	} {
		operation, err := domain.NewOperation(identity.operation, identity.intent, "project.start", project.ID, completedAt.Add(time.Minute))
		if err != nil {
			t.Fatalf("create competing operation: %v", err)
		}
		if _, err := journal.Enqueue(context.Background(), operation); err != nil {
			t.Fatalf("enqueue competing operation: %v", err)
		}
	}

	_, err := store.CompleteProjectUnregister(
		context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
	)
	var busy *ProjectBusyError
	wantIDs := []domain.OperationID{"operation-a", "operation-z"}
	if !errors.As(err, &busy) || busy.ProjectID != project.ID || !reflect.DeepEqual(busy.OperationIDs, wantIDs) {
		t.Fatalf("busy unregister error = %v, want %#v", err, wantIDs)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 5 {
		t.Fatalf("sequence after busy unregister = %d, want 5", sequence)
	}
	if count := projectStoreMutationCount(t, connection, "projects"); count != 1 {
		t.Fatalf("project count after busy unregister = %d, want 1", count)
	}
	owner, err := journal.Operation(context.Background(), running.Operation.ID)
	if err != nil || !reflect.DeepEqual(owner, running) {
		t.Fatalf("owner after busy unregister = %#v, error %v", owner, err)
	}
}

// TestStoreCompleteProjectUnregisterRollsBackLateFailures verifies deletion, operation update, and transition append remain one transaction.
func TestStoreCompleteProjectUnregisterRollsBackLateFailures(t *testing.T) {
	tests := []struct {
		name       string
		triggerSQL string
		want       string
	}{
		{
			name:       "delete",
			triggerSQL: `CREATE TRIGGER fail_project_delete BEFORE DELETE ON projects BEGIN SELECT RAISE(ABORT, 'delete failure'); END`,
			want:       "delete failure",
		},
		{
			name:       "operation update",
			triggerSQL: `CREATE TRIGGER fail_unregister_update BEFORE UPDATE OF state ON operations BEGIN SELECT RAISE(ABORT, 'operation update failure'); END`,
			want:       "operation update failure",
		},
		{
			name:       "zero-row operation update",
			triggerSQL: `CREATE TRIGGER ignore_unregister_update BEFORE UPDATE OF state ON operations BEGIN SELECT RAISE(IGNORE); END`,
			want:       "revision is 3, not expected revision 3",
		},
		{
			name:       "transition append",
			triggerSQL: `CREATE TRIGGER fail_unregister_transition BEFORE INSERT ON operation_transitions WHEN NEW.ordinal = 3 BEGIN SELECT RAISE(ABORT, 'transition failure'); END`,
			want:       "transition failure",
		},
		{
			name:       "zero-row delete",
			triggerSQL: `CREATE TRIGGER consume_project_delete BEFORE DELETE ON projects BEGIN DELETE FROM projects WHERE id = OLD.id; SELECT RAISE(IGNORE); END`,
			want:       "delete affected 0 rows",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-unregister-" + strings.ReplaceAll(test.name, " ", "-")))
			journal, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, domain.OperationID("operation-"+strings.ReplaceAll(test.name, " ", "-")))
			mustProjectStoreReadExec(t, connection, test.triggerSQL)

			if _, err := store.CompleteProjectUnregister(
				context.Background(), project.ID, running.Operation.ID, running.Revision, "project removed", completedAt,
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("failed unregister error = %v, want %q", err, test.want)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 3 {
				t.Fatalf("sequence after rolled-back unregister = %d, want 3", sequence)
			}
			if count := projectStoreMutationCount(t, connection, "projects"); count != 1 {
				t.Fatalf("project count after rolled-back unregister = %d, want 1", count)
			}
			persisted, err := journal.Operation(context.Background(), running.Operation.ID)
			if err != nil || !reflect.DeepEqual(persisted, running) {
				t.Fatalf("operation after rolled-back unregister = %#v, error %v", persisted, err)
			}
			history, err := journal.Transitions(context.Background(), running.Operation.ID)
			if err != nil || len(history) != 2 {
				t.Fatalf("history after rolled-back unregister = %#v, error %v", history, err)
			}
		})
	}
}

// TestStoreCompleteProjectUnregisterValidatesBeforeStorage verifies malformed calls and cancellation cannot reach mutation authority.
func TestStoreCompleteProjectUnregisterValidatesBeforeStorage(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	validAt := projectStoreMutationTestTime()
	tests := []struct {
		name      string
		projectID domain.ProjectID
		operation domain.OperationID
		revision  domain.Sequence
		phase     string
		at        time.Time
		want      string
	}{
		{name: "project ID", projectID: "", operation: "operation-valid", revision: 1, phase: "removed", at: validAt, want: "project ID must not be empty"},
		{name: "operation ID", projectID: "project-valid", operation: "", revision: 1, phase: "removed", at: validAt, want: "operation ID must not be empty"},
		{name: "zero revision", projectID: "project-valid", operation: "operation-valid", revision: 0, phase: "removed", at: validAt, want: "expected operation revision must be positive"},
		{name: "large revision", projectID: "project-valid", operation: "operation-valid", revision: domain.MaximumSequence + 1, phase: "removed", at: validAt, want: "exceeds the cross-client ordering range"},
		{name: "phase", projectID: "project-valid", operation: "operation-valid", revision: 1, phase: " \t", at: validAt, want: "operation phase must not be empty"},
		{name: "zero time", projectID: "project-valid", operation: "operation-valid", revision: 1, phase: "removed", at: time.Time{}, want: "operation transition time must not be zero"},
		{
			name:      "non-UTC time",
			projectID: "project-valid",
			operation: "operation-valid",
			revision:  1,
			phase:     "removed",
			at:        time.Date(2026, time.July, 18, 12, 0, 0, 0, time.FixedZone("local", 60*60)),
			want:      "operation transition time must use UTC",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.CompleteProjectUnregister(
				context.Background(), test.projectID, test.operation, test.revision, test.phase, test.at,
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.CompleteProjectUnregister(
		ctx, "project-valid", "operation-valid", 1, "removed", validAt,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled unregister error = %v, want context.Canceled", err)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
		t.Fatalf("sequence after rejected unregister calls = %d, want 0", sequence)
	}
}

// TestStoreCompleteProjectUnregisterRejectsInvalidLifecycle verifies only the exact running owner and a valid present project may commit.
func TestStoreCompleteProjectUnregisterRejectsInvalidLifecycle(t *testing.T) {
	t.Run("missing operation", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		_, err := store.CompleteProjectUnregister(
			context.Background(), "project-missing-operation", "operation-missing", 1, "removed", projectStoreMutationTestTime(),
		)
		var missing *OperationNotFoundError
		if !errors.As(err, &missing) || missing.OperationID != "operation-missing" {
			t.Fatalf("missing operation error = %v", err)
		}
	})

	t.Run("queued owner", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-queued-unregister")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		journal := projectStoreMutationJournal(store)
		requestedAt := projectStoreMutationTestTime().Add(time.Hour)
		operation, err := domain.NewOperation("operation-queued-unregister", "intent-queued-unregister", domain.OperationKindProjectUnregister, project.ID, requestedAt)
		if err != nil {
			t.Fatalf("create operation: %v", err)
		}
		queued, err := journal.Enqueue(context.Background(), operation)
		if err != nil {
			t.Fatalf("enqueue operation: %v", err)
		}
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, operation.ID, queued.Revision, "removed", requestedAt.Add(time.Second),
		); err == nil || !strings.Contains(err.Error(), "must be running") {
			t.Fatalf("queued completion error = %v", err)
		}
	})

	t.Run("stale owner", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-stale-unregister")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-stale-unregister")
		_, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision-1, "removed", completedAt,
		)
		var stale *StaleRevisionError
		if !errors.As(err, &stale) || stale.Actual != running.Revision {
			t.Fatalf("stale completion error = %v", err)
		}
	})

	t.Run("wrong kind", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-wrong-kind")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		journal := projectStoreMutationJournal(store)
		requestedAt := projectStoreMutationTestTime().Add(time.Hour)
		operation, err := domain.NewOperation("operation-wrong-kind", "intent-wrong-kind", "project.start", project.ID, requestedAt)
		if err != nil {
			t.Fatalf("create operation: %v", err)
		}
		queued, err := journal.Enqueue(context.Background(), operation)
		if err != nil {
			t.Fatalf("enqueue operation: %v", err)
		}
		running, err := journal.Transition(context.Background(), operation.ID, queued.Revision, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
		if err != nil {
			t.Fatalf("start operation: %v", err)
		}
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, operation.ID, running.Revision, "removed", requestedAt.Add(2*time.Second),
		); err == nil || !strings.Contains(err.Error(), `not "project.unregister"`) {
			t.Fatalf("wrong-kind completion error = %v", err)
		}
	})

	t.Run("wrong project", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-owner")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-wrong-project")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), "project-other", running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), `belongs to project "project-owner"`) {
			t.Fatalf("wrong-project completion error = %v", err)
		}
	})

	t.Run("completion precedes start", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-early-completion")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-early-completion")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt.Add(-2*time.Second),
		); err == nil || !strings.Contains(err.Error(), "must not precede its start time") {
			t.Fatalf("early completion error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 3 {
			t.Fatalf("sequence after early completion = %d, want 3", sequence)
		}
	})

	t.Run("absent project with active owner", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-absent-active")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-absent-active")
		mustProjectStoreReadExec(t, connection, "DELETE FROM projects WHERE project_id = ?", project.ID)
		_, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "project is absent") {
			t.Fatalf("absent active completion error = %v", err)
		}
	})

	for _, terminal := range []domain.OperationState{domain.OperationFailed, domain.OperationCancelled} {
		terminal := terminal
		t.Run("present project with "+string(terminal)+" owner", func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-present-" + string(terminal)))
			journal, running, completedAt := projectStoreMutationRunningUnregister(
				t, store, project, domain.OperationID("operation-present-"+string(terminal)),
			)
			var problem *domain.Problem
			if terminal == domain.OperationFailed {
				problem = &domain.Problem{Code: "remove_failed", Message: "The project could not be removed."}
			}
			terminalRecord, err := journal.Transition(
				context.Background(), running.Operation.ID, running.Revision, terminal, "remove "+string(terminal), completedAt, problem,
			)
			if err != nil {
				t.Fatalf("transition unregister operation to %s: %v", terminal, err)
			}
			_, err = store.CompleteProjectUnregister(
				context.Background(), project.ID, terminalRecord.Operation.ID, terminalRecord.Revision, "removed", completedAt.Add(time.Second),
			)
			var corrupt *CorruptStateError
			if err == nil || errors.As(err, &corrupt) || !strings.Contains(err.Error(), "must be running") {
				t.Fatalf("present %s completion error = %v, want ordinary lifecycle failure", terminal, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 4 {
				t.Fatalf("sequence after %s completion attempt = %d, want 4", terminal, sequence)
			}
			if count := projectStoreMutationCount(t, connection, "projects"); count != 1 {
				t.Fatalf("project count after %s completion attempt = %d, want 1", terminal, count)
			}
			persisted, readErr := journal.Operation(context.Background(), terminalRecord.Operation.ID)
			if readErr != nil || !reflect.DeepEqual(persisted, terminalRecord) {
				t.Fatalf("terminal owner after completion attempt = %#v, error %v", persisted, readErr)
			}
		})
	}

	t.Run("present project with succeeded owner", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-present-succeeded")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-present-succeeded")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err != nil {
			t.Fatalf("complete unregister: %v", err)
		}
		insertEmptyProjectStoreReadProject(t, connection, string(project.ID), project.Path, project.Slug, 5)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 5 WHERE id = 1")
		_, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt.Add(time.Second),
		)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "project remains present after succeeded operation") {
			t.Fatalf("present succeeded completion error = %v", err)
		}
	})

	t.Run("malformed project aggregate", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-malformed-unregister")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-malformed-unregister")
		mustProjectStoreReadExec(t, connection, "UPDATE projects SET state = 'unknown' WHERE project_id = ?", project.ID)
		_, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) {
			t.Fatalf("malformed project completion error = %v", err)
		}
	})
}

// TestStoreCompleteProjectUnregisterRejectsStorageCorruption verifies every durable dependency fails closed before removal can commit.
func TestStoreCompleteProjectUnregisterRejectsStorageCorruption(t *testing.T) {
	t.Run("operation query", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, "DROP TABLE operations")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), "project-query", "operation-query", 1, "removed", projectStoreMutationTestTime(),
		); err == nil || !strings.Contains(err.Error(), "find operation") {
			t.Fatalf("operation query error = %v", err)
		}
	})

	t.Run("operation row", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-corrupt-operation")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-corrupt-row")
		mustProjectStoreReadExec(t, connection, "UPDATE operations SET state = 'unknown' WHERE id = ?", running.Operation.ID)
		_, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) {
			t.Fatalf("corrupt operation error = %v", err)
		}
	})

	t.Run("operation history query", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-history-query")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-history-query")
		mustProjectStoreReadExec(t, connection, "DROP TABLE operation_transitions")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "read operation transition history") {
			t.Fatalf("history query error = %v", err)
		}
	})

	t.Run("project query", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-query-failure")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-project-query")
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection, "DROP TABLE projects")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "verify unregister history project owners") {
			t.Fatalf("project query error = %v", err)
		}
	})

	t.Run("project sequence owner", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-sequence-collision")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-sequence-collision")
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES (?, 'docs', ?, 1)`,
			project.ID, completedAt,
		)
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "reuses revision owned by recent resource") {
			t.Fatalf("project sequence collision error = %v", err)
		}
	})

	for _, collision := range []struct {
		name   string
		offset domain.Sequence
	}{
		{name: "queued transition", offset: 1},
		{name: "running transition", offset: 0},
	} {
		collision := collision
		t.Run("target recent reuses "+collision.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-target-recent-" + strings.ReplaceAll(collision.name, " ", "-")))
			journal, running, completedAt := projectStoreMutationRunningUnregister(
				t, store, project, domain.OperationID("operation-target-recent-"+strings.ReplaceAll(collision.name, " ", "-")),
			)
			sequence := running.Revision - collision.offset
			mustProjectStoreReadExec(t, connection,
				`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES (?, 'docs', ?, ?)`,
				project.ID, completedAt, sequence,
			)
			_, err := store.CompleteProjectUnregister(
				context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
			)
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "reuses revision owned by recent resource") {
				t.Fatalf("target recency collision error = %v", err)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 3 {
				t.Fatalf("high-water after target recency collision = %d, want 3", highWater)
			}
			if count := projectStoreMutationCount(t, connection, "projects"); count != 1 {
				t.Fatalf("project count after target recency collision = %d, want 1", count)
			}
			assertProjectStoreMutationRecent(t, connection, domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}, int(sequence))
			persisted, readErr := journal.Operation(context.Background(), running.Operation.ID)
			if readErr != nil || !reflect.DeepEqual(persisted, running) {
				t.Fatalf("operation after target recency collision = %#v, error %v", persisted, readErr)
			}
		})
	}

	t.Run("active operation query", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-active-query")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-active-query")
		const callback = "harbor:test_unregister_active_query"
		if err := connection.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table != "operations" || !strings.Contains(tx.Statement.SQL.String(), "state NOT IN") {
				return
			}
			if _, ok := tx.Statement.Dest.(*[]models.Operation); ok {
				tx.AddError(errors.New("active operation query failure"))
			}
		}); err != nil {
			t.Fatalf("register active query failure: %v", err)
		}
		t.Cleanup(func() {
			_ = connection.Callback().Query().Remove(callback)
		})
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "read active project operations") {
			t.Fatalf("active operation query error = %v", err)
		}
	})

	t.Run("sequence allocator query", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-sequence-query")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-sequence-query")
		mustProjectStoreReadExec(t, connection, "DROP TABLE harbor_state")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "read Harbor snapshot sequence") {
			t.Fatalf("sequence allocator query error = %v", err)
		}
		if count := projectStoreMutationCount(t, connection, "projects"); count != 1 {
			t.Fatalf("project count after allocator failure = %d, want 1", count)
		}
	})

	t.Run("duplicate project identity", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-duplicate-unregister")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-duplicate-project")
		weakenProjectStoreReadProjectsTable(t, connection)
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO projects (project_id, name, path, slug, state, favorite, updated_at, revision)
			 SELECT project_id, name, path, slug, state, favorite, updated_at, revision FROM projects_strict`,
		)
		insertEmptyProjectStoreReadProject(t, connection, string(project.ID), "/work/duplicate", "duplicate", 4)
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "project ID is duplicated") {
			t.Fatalf("duplicate project completion error = %v", err)
		}
	})

	t.Run("duplicate owner operation identity", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-duplicate-owner")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-duplicate-owner")
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection, "ALTER TABLE operations RENAME TO operations_strict")
		mustProjectStoreReadExec(t, connection, `CREATE TABLE operations (
			id TEXT NOT NULL,
			intent_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			project_id TEXT,
			state TEXT NOT NULL,
			phase TEXT NOT NULL,
			problem_code TEXT,
			problem_message TEXT,
			problem_retryable BOOLEAN,
			requested_at DATETIME NOT NULL,
			started_at DATETIME,
			finished_at DATETIME,
			revision INTEGER NOT NULL
		)`)
		mustProjectStoreReadExec(t, connection, "INSERT INTO operations SELECT * FROM operations_strict")
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO operations
			 (id, intent_id, kind, project_id, state, phase, problem_code, problem_message, problem_retryable, requested_at, started_at, finished_at, revision)
			 SELECT id, 'intent-duplicate-owner', kind, project_id, state, phase, problem_code, problem_message, problem_retryable, requested_at, started_at, finished_at, 4
			 FROM operations_strict WHERE id = ?`,
			running.Operation.ID,
		)
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "operation ID is duplicated") {
			t.Fatalf("duplicate owner completion error = %v", err)
		}
	})

	t.Run("duplicate active operation identity", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-duplicate-operation")
		_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-duplicate-owner")
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection, "ALTER TABLE operations RENAME TO operations_strict")
		mustProjectStoreReadExec(t, connection, `CREATE TABLE operations (
			id TEXT NOT NULL,
			intent_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			project_id TEXT,
			state TEXT NOT NULL,
			phase TEXT NOT NULL,
			problem_code TEXT,
			problem_message TEXT,
			problem_retryable BOOLEAN,
			requested_at DATETIME NOT NULL,
			started_at DATETIME,
			finished_at DATETIME,
			revision INTEGER NOT NULL
		)`)
		mustProjectStoreReadExec(t, connection, "INSERT INTO operations SELECT * FROM operations_strict")
		for _, revision := range []int{4, 5} {
			mustProjectStoreReadExec(t, connection,
				`INSERT INTO operations (id, intent_id, kind, project_id, state, phase, requested_at, revision)
				 VALUES ('operation-duplicate', ?, 'project.start', ?, 'queued', 'queued', ?, ?)`,
				fmt.Sprintf("intent-duplicate-%d", revision), project.ID, completedAt, revision,
			)
		}
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 5 WHERE id = 1")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err == nil || !strings.Contains(err.Error(), "operation ID is duplicated") {
			t.Fatalf("duplicate active operation completion error = %v", err)
		}
	})
}

// TestStoreCompleteProjectUnregisterReplayRejectsCorruption verifies a missing project is not enough to bless damaged completion evidence.
func TestStoreCompleteProjectUnregisterReplayRejectsCorruption(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(*testing.T, *gorm.DB)
		want    string
	}{
		{
			name: "history",
			corrupt: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "UPDATE operation_transitions SET phase = 'damaged' WHERE ordinal = 3")
			},
			want: "phase does not match latest transition",
		},
		{
			name: "retained high-water",
			corrupt: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 3 WHERE id = 1")
			},
			want: "operation revision maximum sequence exceeds Harbor high-water 3",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-replay-" + strings.ReplaceAll(test.name, " ", "-")))
			_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, domain.OperationID("operation-replay-"+strings.ReplaceAll(test.name, " ", "-")))
			if _, err := store.CompleteProjectUnregister(
				context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
			); err != nil {
				t.Fatalf("complete unregister: %v", err)
			}
			test.corrupt(t, connection)
			if _, err := store.CompleteProjectUnregister(
				context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt.Add(time.Hour),
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("corrupt replay error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("active peer", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-replay-active-peer")
		journal, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-replay-active-peer")
		if _, err := store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
		); err != nil {
			t.Fatalf("complete unregister: %v", err)
		}
		peer, err := domain.NewOperation(
			"operation-after-unregister",
			"intent-after-unregister",
			"project.start",
			project.ID,
			completedAt.Add(time.Minute),
		)
		if err != nil {
			t.Fatalf("create active peer: %v", err)
		}
		if _, err := journal.Enqueue(context.Background(), peer); err != nil {
			t.Fatalf("enqueue active peer: %v", err)
		}
		_, err = store.CompleteProjectUnregister(
			context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt.Add(time.Hour),
		)
		var corrupt *CorruptStateError
		if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "project is absent while active operations remain") {
			t.Fatalf("active-peer replay error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 5 {
			t.Fatalf("sequence after active-peer replay = %d, want 5", sequence)
		}
	})

	for _, collision := range []struct {
		name     string
		sequence func(OperationRecord, OperationRecord) domain.Sequence
	}{
		{name: "running transition", sequence: func(running, _ OperationRecord) domain.Sequence { return running.Revision }},
		{name: "succeeded transition", sequence: func(_, completed OperationRecord) domain.Sequence { return completed.Revision }},
	} {
		collision := collision
		t.Run("foreign recent reuses "+collision.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-replay-owner-" + strings.ReplaceAll(collision.name, " ", "-")))
			_, running, completedAt := projectStoreMutationRunningUnregister(
				t, store, project, domain.OperationID("operation-replay-owner-"+strings.ReplaceAll(collision.name, " ", "-")),
			)
			completed, err := store.CompleteProjectUnregister(
				context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
			)
			if err != nil {
				t.Fatalf("complete unregister: %v", err)
			}
			foreign := projectStoreMutationTestProject(domain.ProjectID("project-foreign-" + strings.ReplaceAll(collision.name, " ", "-")))
			if _, err := store.PutProject(context.Background(), foreign); err != nil {
				t.Fatalf("put foreign project: %v", err)
			}
			sequence := collision.sequence(running, completed)
			mustProjectStoreReadExec(t, connection,
				`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES (?, 'docs', ?, ?)`,
				foreign.ID, completedAt, sequence,
			)
			_, err = store.CompleteProjectUnregister(
				context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt.Add(time.Hour),
			)
			var corrupt *CorruptStateError
			if !errors.As(err, &corrupt) || !strings.Contains(err.Error(), "reuses revision owned by recent resource") {
				t.Fatalf("replay sequence collision error = %v", err)
			}
			if highWater := projectStoreMutationSequence(t, store); highWater != 5 {
				t.Fatalf("high-water after replay sequence collision = %d, want 5", highWater)
			}
			assertProjectStoreMutationRecent(t, connection, domain.ResourceRef{ProjectID: foreign.ID, ResourceID: "docs"}, int(sequence))
		})
	}
}

// TestStoreCompleteProjectUnregisterConcurrentRetryAllocatesOnce verifies simultaneous exact requests converge on one durable completion.
func TestStoreCompleteProjectUnregisterConcurrentRetryAllocatesOnce(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 4, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-concurrent-unregister")
	journal, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-concurrent-unregister")

	start := make(chan struct{})
	results := make(chan struct {
		record OperationRecord
		err    error
	}, 2)
	for range 2 {
		go func() {
			<-start
			record, err := store.CompleteProjectUnregister(
				context.Background(), project.ID, running.Operation.ID, running.Revision, "removed", completedAt,
			)
			results <- struct {
				record OperationRecord
				err    error
			}{record: record, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent unregister errors = %v and %v", first.err, second.err)
	}
	if !reflect.DeepEqual(first.record, second.record) || first.record.Revision != 4 {
		t.Fatalf("concurrent unregister records = %#v and %#v", first.record, second.record)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 4 {
		t.Fatalf("sequence after concurrent unregister = %d, want 4", sequence)
	}
	history, err := journal.Transitions(context.Background(), running.Operation.ID)
	if err != nil || len(history) != 3 {
		t.Fatalf("history after concurrent unregister = %#v, error %v", history, err)
	}
	if count := projectStoreMutationCount(t, connection, "projects"); count != 0 {
		t.Fatalf("project count after concurrent unregister = %d, want 0", count)
	}
}

// TestStoreRecordRecentResourceCreatesAndUpdatesInPlace verifies recency uses UTC daemon time and preserves row identity.
func TestStoreRecordRecentResourceCreatesAndUpdatesInPlace(t *testing.T) {
	zone := time.FixedZone("local", -7*60*60)
	now := time.Date(2026, time.July, 18, 4, 30, 0, 123, zone)
	store, connection := newProjectStoreReadTestHarness(t, 1, func() time.Time { return now })
	project := projectStoreMutationTestProject("project-recent")
	if _, err := store.PutProject(context.Background(), project); err != nil {
		t.Fatalf("put project: %v", err)
	}
	reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
	first, err := store.RecordRecentResource(nil, reference)
	if err != nil {
		t.Fatalf("create recent resource: %v", err)
	}
	if first.Sequence != 2 || first.AccessedAt != now.UTC() || first.Reference != reference {
		t.Fatalf("first recent record = %#v", first)
	}
	id := projectStoreMutationScopedRowID(t, connection, "recent_resources", "project_id", string(project.ID), "resource_id", "docs")

	now = now.Add(time.Hour)
	second, err := store.RecordRecentResource(context.Background(), reference)
	if err != nil {
		t.Fatalf("update recent resource: %v", err)
	}
	if second.Sequence != 3 || second.AccessedAt != now.UTC() {
		t.Fatalf("second recent record = %#v", second)
	}
	if got := projectStoreMutationScopedRowID(t, connection, "recent_resources", "project_id", string(project.ID), "resource_id", "docs"); got != id {
		t.Fatalf("recent surrogate ID = %d, want preserved %d", got, id)
	}
}

// TestStoreRecordRecentResourceRejectsInvalidMissingAndClockFailures verifies rejected recency requests do not advance ordering.
func TestStoreRecordRecentResourceRejectsInvalidMissingAndClockFailures(t *testing.T) {
	var clockCalls atomic.Int64
	store, _ := newProjectStoreReadTestHarness(t, 1, func() time.Time {
		clockCalls.Add(1)
		return time.Time{}
	})
	if _, err := store.RecordRecentResource(context.Background(), domain.ResourceRef{}); err == nil || !strings.Contains(err.Error(), "project ID must not be empty") {
		t.Fatalf("invalid reference error = %v", err)
	}
	if clockCalls.Load() != 0 {
		t.Fatalf("clock calls for invalid reference = %d, want 0", clockCalls.Load())
	}
	if _, err := store.RecordRecentResource(context.Background(), domain.ResourceRef{ProjectID: "project-clock", ResourceID: "docs"}); err == nil || !strings.Contains(err.Error(), "recent resource access time must not be zero") {
		t.Fatalf("invalid clock error = %v", err)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
		t.Fatalf("sequence after invalid recency requests = %d, want 0", sequence)
	}

	validStore, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	reference := domain.ResourceRef{ProjectID: "project-missing", ResourceID: "docs"}
	_, err := validStore.RecordRecentResource(context.Background(), reference)
	var missing *ResourceNotFoundError
	if !errors.As(err, &missing) || missing.Reference != reference {
		t.Fatalf("missing recent resource error = %v, want typed reference", err)
	}
	if sequence := projectStoreMutationSequence(t, validStore); sequence != 0 {
		t.Fatalf("sequence after missing resource = %d, want 0", sequence)
	}
}

// TestStoreRecordRecentResourceRollsBackLateFailures verifies both create and update failures restore recency and sequence.
func TestStoreRecordRecentResourceRollsBackLateFailures(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-recent-rollback")
	if _, err := store.PutProject(context.Background(), project); err != nil {
		t.Fatalf("put project: %v", err)
	}
	reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
	mustProjectStoreReadExec(t, connection, `CREATE TRIGGER fail_recent_create BEFORE INSERT ON recent_resources BEGIN SELECT RAISE(ABORT, 'recent create failure'); END`)
	if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "recent create failure") {
		t.Fatalf("recent create failure error = %v", err)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
		t.Fatalf("sequence after failed recent create = %d, want 1", sequence)
	}
	mustProjectStoreReadExec(t, connection, "DROP TRIGGER fail_recent_create")
	first, err := store.RecordRecentResource(context.Background(), reference)
	if err != nil || first.Sequence != 2 {
		t.Fatalf("successful recent create = %#v, error %v", first, err)
	}

	mustProjectStoreReadExec(t, connection, `CREATE TRIGGER fail_recent_update BEFORE UPDATE ON recent_resources BEGIN SELECT RAISE(ABORT, 'recent update failure'); END`)
	if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "recent update failure") {
		t.Fatalf("recent update failure error = %v", err)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 2 {
		t.Fatalf("sequence after failed recent update = %d, want 2", sequence)
	}
	var persisted models.RecentResource
	if err := connection.Where("project_id = ? AND resource_id = ?", project.ID, "docs").First(&persisted).Error; err != nil {
		t.Fatalf("read persisted recent row: %v", err)
	}
	if persisted.Sequence != 2 {
		t.Fatalf("persisted recent sequence after failed update = %d, want 2", persisted.Sequence)
	}
}

// TestStoreMutationsHonorCancellationWhileWaiting verifies queued writers leave the active transaction undisturbed.
func TestStoreMutationsHonorCancellationWhileWaiting(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	<-store.mutations.permit
	t.Cleanup(func() { store.mutations.permit <- struct{}{} })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := store.PutProject(ctx, projectStoreMutationTestProject("project-waiting"))
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting put error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled put remained blocked on mutation authority")
	}
}

// TestStoreAndJournalMutationsShareOneGlobalOrder verifies high-concurrency writers cannot reuse durable sequences.
func TestStoreAndJournalMutationsShareOneGlobalOrder(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 4, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-concurrent")
	initial, err := store.PutProject(context.Background(), project)
	if err != nil || initial.Revision != 1 {
		t.Fatalf("put concurrent seed = %#v, error %v", initial, err)
	}
	journal := projectStoreMutationJournal(store)

	const mutationsPerKind = 12
	sequences := make(chan domain.Sequence, mutationsPerKind*3)
	errorsFound := make(chan error, mutationsPerKind*3)
	var group sync.WaitGroup
	for index := 0; index < mutationsPerKind; index++ {
		index := index
		group.Add(3)
		go func() {
			defer group.Done()
			replacement := project
			replacement.Name = fmt.Sprintf("Concurrent %02d", index)
			replacement.UpdatedAt = project.UpdatedAt.Add(time.Duration(index+1) * time.Second)
			record, err := store.PutProject(context.Background(), replacement)
			if err != nil {
				errorsFound <- err
				return
			}
			sequences <- record.Revision
		}()
		go func() {
			defer group.Done()
			record, err := store.RecordRecentResource(context.Background(), domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"})
			if err != nil {
				errorsFound <- err
				return
			}
			sequences <- record.Sequence
		}()
		go func() {
			defer group.Done()
			operation, err := domain.NewOperation(
				domain.OperationID(fmt.Sprintf("operation-%02d", index)),
				domain.IntentID(fmt.Sprintf("intent-%02d", index)),
				"maintenance.run",
				project.ID,
				projectStoreMutationTestTime().Add(time.Duration(index)*time.Second),
			)
			if err != nil {
				errorsFound <- err
				return
			}
			record, err := journal.Enqueue(context.Background(), operation)
			if err != nil {
				errorsFound <- err
				return
			}
			sequences <- record.Revision
		}()
	}
	group.Wait()
	close(errorsFound)
	close(sequences)
	for err := range errorsFound {
		t.Errorf("concurrent mutation: %v", err)
	}

	got := make([]int, 0, mutationsPerKind*3)
	for sequence := range sequences {
		got = append(got, int(sequence))
	}
	sort.Ints(got)
	if len(got) != mutationsPerKind*3 {
		t.Fatalf("successful concurrent sequences = %d, want %d", len(got), mutationsPerKind*3)
	}
	for index, sequence := range got {
		if want := index + 2; sequence != want {
			t.Fatalf("sorted sequence[%d] = %d, want %d; all = %v", index, sequence, want, got)
		}
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != domain.Sequence(1+mutationsPerKind*3) {
		t.Fatalf("final global sequence = %d, want %d", sequence, 1+mutationsPerKind*3)
	}
	if _, err := store.Snapshot(context.Background()); err != nil {
		t.Fatalf("read snapshot after concurrent mutations: %v", err)
	}
}

// TestStorePutProjectReportsNormalizedQueryFailures verifies every projection query rolls back its tentative revision.
func TestStorePutProjectReportsNormalizedQueryFailures(t *testing.T) {
	tables := []string{
		"harbor_state",
		"projects",
		"project_apps",
		"project_services",
		"project_resources",
		"recent_resources",
		"operations",
		"operation_transitions",
	}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, "DROP TABLE "+table)

			if _, err := store.PutProject(context.Background(), projectStoreMutationTestProject("project-query")); err == nil {
				t.Fatalf("put unexpectedly succeeded without %s", table)
			}
		})
	}
}

// TestStorePutProjectRollsBackEveryUpdateFailure verifies in-place writes remain one atomic aggregate mutation.
func TestStorePutProjectRollsBackEveryUpdateFailure(t *testing.T) {
	tests := []struct {
		name    string
		table   string
		trigger string
	}{
		{name: "project", table: "projects", trigger: "fail_project_update"},
		{name: "App", table: "project_apps", trigger: "fail_app_update"},
		{name: "service", table: "project_services", trigger: "fail_service_update"},
		{name: "resource", table: "project_resources", trigger: "fail_resource_update"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-update-" + strings.ToLower(test.name)))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put initial project: %v", err)
			}
			persistedBefore := canonicalProjectStoreMutationProject(project)
			mustProjectStoreReadExec(t, connection, fmt.Sprintf(
				"CREATE TRIGGER %s BEFORE UPDATE ON %s BEGIN SELECT RAISE(ABORT, 'update failure'); END",
				test.trigger,
				test.table,
			))
			changed := project
			changed.Name = "Changed Project"
			changed.UpdatedAt = changed.UpdatedAt.Add(time.Minute)
			changed.Apps[0].Name = "Changed App"
			changed.Services[0].Name = "Changed Service"
			changed.Resources[0].Name = "Changed Resource"

			if _, err := store.PutProject(context.Background(), changed); err == nil || !strings.Contains(err.Error(), "update failure") {
				t.Fatalf("%s update error = %v", test.name, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
				t.Fatalf("sequence after %s update failure = %d, want 1", test.name, sequence)
			}
			persisted, err := store.Project(context.Background(), project.ID)
			if err != nil || !reflect.DeepEqual(persisted.Project, persistedBefore) {
				t.Fatalf("persisted project after %s update failure = %#v, error %v", test.name, persisted, err)
			}
		})
	}
}

// TestStorePutProjectRollsBackEveryCreateFailure verifies child inserts cannot leave a partially created aggregate.
func TestStorePutProjectRollsBackEveryCreateFailure(t *testing.T) {
	for _, table := range []string{"project_apps", "project_services"} {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			mustProjectStoreReadExec(t, connection, fmt.Sprintf(
				"CREATE TRIGGER fail_child_create BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'child create failure'); END",
				table,
			))
			if _, err := store.PutProject(context.Background(), projectStoreMutationTestProject("project-create-failure")); err == nil || !strings.Contains(err.Error(), "child create failure") {
				t.Fatalf("%s create error = %v", table, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
				t.Fatalf("sequence after %s create failure = %d, want 0", table, sequence)
			}
		})
	}
}

// TestStorePutProjectRollsBackEveryStaleDeleteFailure verifies cleanup runs inside the aggregate transaction.
func TestStorePutProjectRollsBackEveryStaleDeleteFailure(t *testing.T) {
	for _, table := range []string{"project_resources", "project_apps", "project_services"} {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-delete-" + strings.TrimPrefix(table, "project_")))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put initial project: %v", err)
			}
			mustProjectStoreReadExec(t, connection, fmt.Sprintf(
				"CREATE TRIGGER fail_stale_delete BEFORE DELETE ON %s BEGIN SELECT RAISE(ABORT, 'stale delete failure'); END",
				table,
			))
			empty := project
			empty.UpdatedAt = empty.UpdatedAt.Add(time.Minute)
			empty.Apps = []domain.AppSnapshot{}
			empty.Services = []domain.ServiceSnapshot{}
			empty.Resources = []domain.ResourceSnapshot{}

			if _, err := store.PutProject(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "stale delete failure") {
				t.Fatalf("%s stale delete error = %v", table, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
				t.Fatalf("sequence after %s stale delete failure = %d, want 1", table, sequence)
			}
			persisted, err := store.Project(context.Background(), project.ID)
			if err != nil || !reflect.DeepEqual(persisted.Project, canonicalProjectStoreMutationProject(project)) {
				t.Fatalf("persisted project after %s stale delete failure = %#v, error %v", table, persisted, err)
			}
		})
	}
}

// TestStoreMutationsRejectDuplicateNaturalIdentities verifies weakened schemas cannot turn one request into a multi-row mutation.
func TestStoreMutationsRejectDuplicateNaturalIdentities(t *testing.T) {
	t.Run("projects", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		weakenProjectStoreReadProjectsTable(t, connection)
		insertEmptyProjectStoreReadProject(t, connection, "project-duplicate", "/work/one", "one", 1)
		insertEmptyProjectStoreReadProject(t, connection, "project-duplicate", "/work/two", "two", 2)
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 2 WHERE id = 1")

		project := projectStoreMutationTestProject("project-duplicate")
		if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "project ID is duplicated") {
			t.Fatalf("duplicate project put error = %v", err)
		}
	})

	for _, child := range []struct {
		name      string
		table     string
		identity  string
		createSQL string
		copySQL   string
	}{
		{
			name: "Apps", table: "project_apps", identity: "app_id",
			createSQL: `CREATE TABLE project_apps (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, app_id TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL, active BOOLEAN NOT NULL, required BOOLEAN NOT NULL)`,
			copySQL:   `INSERT INTO project_apps (project_id, app_id, name, state, active, required) SELECT project_id, app_id, name, state, active, required FROM project_apps_strict WHERE app_id = 'api'`,
		},
		{
			name: "services", table: "project_services", identity: "service_id",
			createSQL: `CREATE TABLE project_services (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, service_id TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, state TEXT NOT NULL, owner TEXT NOT NULL, selection TEXT NOT NULL, required BOOLEAN NOT NULL)`,
			copySQL:   `INSERT INTO project_services (project_id, service_id, name, kind, state, owner, selection, required) SELECT project_id, service_id, name, kind, state, owner, selection, required FROM project_services_strict WHERE service_id = 'mysql'`,
		},
		{
			name: "resources", table: "project_resources", identity: "resource_id",
			createSQL: `CREATE TABLE project_resources (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, resource_id TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, url TEXT NOT NULL, owner_kind TEXT NOT NULL, owner_app_id TEXT, owner_service_id TEXT)`,
			copySQL:   `INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_app_id, owner_service_id) SELECT project_id, resource_id, name, kind, url, owner_kind, owner_app_id, owner_service_id FROM project_resources_strict WHERE resource_id = 'docs'`,
		},
	} {
		t.Run(child.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-duplicate-" + strings.ToLower(child.name)))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put initial project: %v", err)
			}
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, "ALTER TABLE "+child.table+" RENAME TO "+child.table+"_strict")
			mustProjectStoreReadExec(t, connection, child.createSQL)
			mustProjectStoreReadExec(t, connection, child.copySQL)
			mustProjectStoreReadExec(t, connection, child.copySQL)

			if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "is duplicated") {
				t.Fatalf("duplicate %s put error = %v", child.identity, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
				t.Fatalf("sequence after duplicate %s = %d, want 1", child.identity, sequence)
			}
		})
	}
}

// TestStoreRecordRecentResourceReportsQueryFailures verifies each lookup and ownership query rolls back recency.
func TestStoreRecordRecentResourceReportsQueryFailures(t *testing.T) {
	for _, table := range []string{"project_resources", "recent_resources", "projects", "operations", "operation_transitions"} {
		t.Run(table, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject("project-recent-query")
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put project: %v", err)
			}
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, "DROP TABLE "+table)
			reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
			if _, err := store.RecordRecentResource(context.Background(), reference); err == nil {
				t.Fatalf("recency unexpectedly succeeded without %s", table)
			}
		})
	}
}

// TestStoreRecordRecentResourceRejectsDuplicateRows verifies weakened natural keys cannot update multiple resources or recency rows.
func TestStoreRecordRecentResourceRejectsDuplicateRows(t *testing.T) {
	t.Run("resource", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-duplicate-resource")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection, "ALTER TABLE project_resources RENAME TO project_resources_strict")
		mustProjectStoreReadExec(t, connection, `CREATE TABLE project_resources (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, resource_id TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, url TEXT NOT NULL, owner_kind TEXT NOT NULL, owner_app_id TEXT, owner_service_id TEXT)`)
		copyStatement := `INSERT INTO project_resources (project_id, resource_id, name, kind, url, owner_kind, owner_app_id, owner_service_id) SELECT project_id, resource_id, name, kind, url, owner_kind, owner_app_id, owner_service_id FROM project_resources_strict WHERE resource_id = 'docs'`
		mustProjectStoreReadExec(t, connection, copyStatement)
		mustProjectStoreReadExec(t, connection, copyStatement)

		reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "resource ID is duplicated") {
			t.Fatalf("duplicate resource recency error = %v", err)
		}
	})

	t.Run("recent", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-duplicate-recent")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err != nil {
			t.Fatalf("record initial recency: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection, "ALTER TABLE recent_resources RENAME TO recent_resources_strict")
		mustProjectStoreReadExec(t, connection, `CREATE TABLE recent_resources (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL, resource_id TEXT NOT NULL, accessed_at DATETIME NOT NULL, sequence INTEGER NOT NULL)`)
		copyStatement := `INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) SELECT project_id, resource_id, accessed_at, sequence FROM recent_resources_strict`
		mustProjectStoreReadExec(t, connection, copyStatement)
		mustProjectStoreReadExec(t, connection, copyStatement)

		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "resource reference is duplicated") {
			t.Fatalf("duplicate recency error = %v", err)
		}
	})
}

// TestStoreRecordRecentResourceRejectsCrossTableSequenceReuse verifies recency never claims another durable owner's revision.
func TestStoreRecordRecentResourceRejectsCrossTableSequenceReuse(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-sequence-reuse")
	if _, err := store.PutProject(context.Background(), project); err != nil {
		t.Fatalf("put project: %v", err)
	}
	mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 0 WHERE id = 1")
	reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
	if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "sequence exceeds Harbor high-water") {
		t.Fatalf("cross-table sequence error = %v", err)
	}
	if count := projectStoreMutationCount(t, connection, "recent_resources"); count != 0 {
		t.Fatalf("recent count after sequence collision = %d, want 0", count)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
		t.Fatalf("sequence after recency collision = %d, want rolled-back 0", sequence)
	}
}

// TestStoreMutationHelpersEnforceAffectedRows verifies defensive row-count checks return typed corruption.
func TestStoreMutationHelpersEnforceAffectedRows(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "update storage", err: requireOneMutation(&gorm.DB{Error: errors.New("update failed")}, "update row", "one")},
		{name: "update count", err: requireOneMutation(&gorm.DB{RowsAffected: 0}, "update row", "one")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("row-count helper unexpectedly succeeded")
			}
		})
	}

	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	_ = store
	if err := deleteProjectRows(connection, &models.ProjectApp{}, nil, "delete rows"); err != nil {
		t.Fatalf("empty stale delete: %v", err)
	}
	if err := deleteProjectRows(connection, &models.ProjectApp{}, []int{999}, "delete rows"); err == nil {
		t.Fatal("missing stale delete unexpectedly succeeded")
	}
}

// TestStoreRecordRecentResourceChecksCancellationBeforeClock verifies cancelled callers cannot capture a false access instant.
func TestStoreRecordRecentResourceChecksCancellationBeforeClock(t *testing.T) {
	var clockCalls atomic.Int64
	store, _ := newProjectStoreReadTestHarness(t, 1, func() time.Time {
		clockCalls.Add(1)
		return projectStoreMutationTestClock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.RecordRecentResource(ctx, domain.ResourceRef{ProjectID: "project-cancelled", ResourceID: "docs"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recency error = %v, want context.Canceled", err)
	}
	if clockCalls.Load() != 0 {
		t.Fatalf("clock calls for cancelled recency = %d, want 0", clockCalls.Load())
	}
}

// TestStorePutProjectRejectsReadbackCorruption verifies the return value must be the exact aggregate written at its revision.
func TestStorePutProjectRejectsReadbackCorruption(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER remove_new_project AFTER INSERT ON projects BEGIN DELETE FROM projects WHERE id = NEW.id; END`)
		empty := projectStoreMutationTestProject("project-readback-missing")
		empty.Apps = []domain.AppSnapshot{}
		empty.Services = []domain.ServiceSnapshot{}
		empty.Resources = []domain.ResourceSnapshot{}
		if _, err := store.PutProject(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "read project after put") {
			t.Fatalf("missing readback error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
			t.Fatalf("sequence after missing readback = %d, want 0", sequence)
		}
	})

	t.Run("revision", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER change_new_project_revision AFTER INSERT ON projects BEGIN UPDATE projects SET revision = NEW.revision + 10 WHERE id = NEW.id; END`)
		empty := projectStoreMutationTestProject("project-readback-revision")
		empty.Apps = []domain.AppSnapshot{}
		empty.Services = []domain.ServiceSnapshot{}
		empty.Resources = []domain.ResourceSnapshot{}
		if _, err := store.PutProject(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "readback revision") {
			t.Fatalf("revision readback error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
			t.Fatalf("sequence after revision readback = %d, want 0", sequence)
		}
	})
}

// TestStoreRecordRecentResourceRejectsReadbackCorruption verifies recency cannot return a row changed by database-side behavior.
func TestStoreRecordRecentResourceRejectsReadbackCorruption(t *testing.T) {
	for _, test := range []struct {
		name       string
		triggerSQL string
		want       string
	}{
		{
			name:       "missing",
			triggerSQL: `CREATE TRIGGER remove_new_recent AFTER INSERT ON recent_resources BEGIN DELETE FROM recent_resources WHERE id = NEW.id; END`,
			want:       "readback contains 0 rows",
		},
		{
			name:       "sequence",
			triggerSQL: `CREATE TRIGGER change_new_recent_sequence AFTER INSERT ON recent_resources BEGIN UPDATE recent_resources SET sequence = NEW.sequence + 10 WHERE id = NEW.id; END`,
			want:       "readback sequence",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-recent-readback-" + test.name))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put project: %v", err)
			}
			mustProjectStoreReadExec(t, connection, test.triggerSQL)
			reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
			if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s recent readback error = %v", test.name, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
				t.Fatalf("sequence after %s recent readback = %d, want 1", test.name, sequence)
			}
		})
	}
}

// TestStoreRecordRecentResourceRejectsOperationSequenceReuse verifies all retained journal owners participate in global ordering.
func TestStoreRecordRecentResourceRejectsOperationSequenceReuse(t *testing.T) {
	for _, test := range []struct {
		name      string
		seed      func(*testing.T, *gorm.DB)
		want      string
		highWater int
	}{
		{
			name: "operation",
			seed: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
					 VALUES ('operation-owner', 'intent-owner', 'maintenance.run', 'queued', 'queued', ?, 2)`,
					projectStoreMutationTestTime(),
				)
			},
			want:      "operation revision maximum sequence exceeds Harbor high-water 1",
			highWater: 1,
		},
		{
			name: "transition",
			seed: func(t *testing.T, connection *gorm.DB) {
				t.Helper()
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operations (id, intent_id, kind, state, phase, requested_at, revision)
					 VALUES ('operation-transition', 'intent-transition', 'maintenance.run', 'queued', 'queued', ?, 2)`,
					projectStoreMutationTestTime(),
				)
				mustProjectStoreReadExec(t, connection,
					`INSERT INTO operation_transitions (operation_id, ordinal, state, phase, occurred_at, sequence)
					 VALUES ('operation-transition', 1, 'queued', 'queued', ?, 3)`,
					projectStoreMutationTestTime(),
				)
			},
			want:      "operation transition sequence maximum sequence exceeds Harbor high-water 2",
			highWater: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			seedSingleProjectStoreReadState(t, connection, "project-sequence-owner", 1)
			mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = ? WHERE id = 1", test.highWater)
			test.seed(t, connection)

			reference := domain.ResourceRef{ProjectID: "project-sequence-owner", ResourceID: "docs"}
			if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s sequence collision error = %v", test.name, err)
			}
		})
	}
}

// TestStoreRecentMutationHelpersReportReadFailures verifies defensive readback helpers fail closed independently of write setup.
func TestStoreRecentMutationHelpersReportReadFailures(t *testing.T) {
	t.Run("missing readback", func(t *testing.T) {
		_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		_, err := readRecentResourceForMutation(connection, domain.ResourceRef{ProjectID: "project-missing", ResourceID: "docs"})
		if err == nil || !strings.Contains(err.Error(), "readback contains 0 rows") {
			t.Fatalf("missing helper readback error = %v", err)
		}
	})

	t.Run("readback query", func(t *testing.T) {
		_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, "DROP TABLE recent_resources")
		_, err := readRecentResourceForMutation(connection, domain.ResourceRef{ProjectID: "project-query", ResourceID: "docs"})
		if err == nil || !strings.Contains(err.Error(), "read recent resource after put") {
			t.Fatalf("helper readback query error = %v", err)
		}
	})

	t.Run("owner query", func(t *testing.T) {
		_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, "DROP TABLE recent_resources")
		err := validateRecentSequenceOwner(connection, RecentResourceRecord{
			Reference:  domain.ResourceRef{ProjectID: "project-query", ResourceID: "docs"},
			AccessedAt: projectStoreMutationTestTime(),
			Sequence:   1,
		})
		if err == nil || !strings.Contains(err.Error(), "verify recent resource sequence owner") {
			t.Fatalf("recent owner query error = %v", err)
		}
	})

	t.Run("exclusive owner", func(t *testing.T) {
		_, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		err := validateRecentSequenceOwner(connection, RecentResourceRecord{
			Reference:  domain.ResourceRef{ProjectID: "project-missing", ResourceID: "docs"},
			AccessedAt: projectStoreMutationTestTime(),
			Sequence:   1,
		})
		if err == nil || !strings.Contains(err.Error(), "does not exclusively own its sequence") {
			t.Fatalf("exclusive recent owner error = %v", err)
		}
	})
}

// TestStoreMutationsRejectInvalidSurrogateIDs verifies weakened storage cannot hide nonpositive row identities.
func TestStoreMutationsRejectInvalidSurrogateIDs(t *testing.T) {
	for _, test := range []struct {
		name   string
		table  string
		method func(*Store, domain.ProjectSnapshot) error
	}{
		{
			name:  "project",
			table: "projects",
			method: func(store *Store, project domain.ProjectSnapshot) error {
				_, err := store.PutProject(context.Background(), project)
				return err
			},
		},
		{
			name:  "App",
			table: "project_apps",
			method: func(store *Store, project domain.ProjectSnapshot) error {
				_, err := store.PutProject(context.Background(), project)
				return err
			},
		},
		{
			name:  "service",
			table: "project_services",
			method: func(store *Store, project domain.ProjectSnapshot) error {
				_, err := store.PutProject(context.Background(), project)
				return err
			},
		},
		{
			name:  "resource",
			table: "project_resources",
			method: func(store *Store, project domain.ProjectSnapshot) error {
				_, err := store.RecordRecentResource(context.Background(), domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"})
				return err
			},
		},
		{
			name:  "projection-resource",
			table: "project_resources",
			method: func(store *Store, project domain.ProjectSnapshot) error {
				_, err := store.PutProject(context.Background(), project)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-invalid-id-" + strings.ToLower(test.name)))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put project: %v", err)
			}
			statement := "UPDATE " + test.table + " SET id = -1 WHERE id = (SELECT min(id) FROM " + test.table + ")"
			if strings.Contains(test.name, "resource") {
				statement = "UPDATE project_resources SET id = -1 WHERE resource_id = 'docs'"
			}
			mustProjectStoreReadExec(t, connection, statement)
			if err := test.method(store, project); err == nil || !strings.Contains(err.Error(), "database ID must be positive") {
				t.Fatalf("invalid %s surrogate error = %v", test.name, err)
			}
		})
	}

	t.Run("recent", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-invalid-id-recent")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err != nil {
			t.Fatalf("record initial recency: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "UPDATE recent_resources SET id = -1")
		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "database ID must be positive") {
			t.Fatalf("invalid recent surrogate error = %v", err)
		}
	})
}

// TestStoreRecordRecentResourceRejectsMalformedTarget verifies recency is never attached to an unusable resource row.
func TestStoreRecordRecentResourceRejectsMalformedTarget(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := projectStoreMutationTestProject("project-malformed-resource")
	if _, err := store.PutProject(context.Background(), project); err != nil {
		t.Fatalf("put project: %v", err)
	}
	mustProjectStoreReadExec(t, connection, "UPDATE project_resources SET url = 'ftp://invalid.test' WHERE project_id = ? AND resource_id = 'docs'", project.ID)
	reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
	if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "resource URL must be an absolute HTTP or HTTPS URL") {
		t.Fatalf("malformed resource recency error = %v", err)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
		t.Fatalf("sequence after malformed resource = %d, want 1", sequence)
	}
}

// TestStoreMutationPreflightRejectsRewoundHighWater verifies every writer observes all retained owners before allocating.
func TestStoreMutationPreflightRejectsRewoundHighWater(t *testing.T) {
	t.Run("put target owner ahead", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-put-rewound")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 0 WHERE id = 1")
		changed := project
		changed.Name = "Changed"
		if _, err := store.PutProject(context.Background(), changed); err == nil || !strings.Contains(err.Error(), "project revision maximum sequence exceeds Harbor high-water 0") {
			t.Fatalf("rewound target put error = %v", err)
		}
		assertProjectStoreMutationRoot(t, connection, project.ID, project.Name, 1)
		if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
			t.Fatalf("rewound target put high-water = %d, want 0", sequence)
		}
	})

	t.Run("put other owner ahead", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		target := projectStoreMutationTestProject("project-put-target")
		other := projectStoreMutationTestProject("project-put-other")
		if _, err := store.PutProject(context.Background(), target); err != nil {
			t.Fatalf("put target: %v", err)
		}
		if _, err := store.PutProject(context.Background(), other); err != nil {
			t.Fatalf("put other: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
		if _, err := store.PutProject(context.Background(), target); err == nil || !strings.Contains(err.Error(), "project revision maximum sequence exceeds Harbor high-water 1") {
			t.Fatalf("other-owner put error = %v", err)
		}
		assertProjectStoreMutationRoot(t, connection, target.ID, target.Name, 1)
		if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
			t.Fatalf("other-owner put high-water = %d, want 1", sequence)
		}
	})

	t.Run("recent target owner ahead", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-recent-rewound")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err != nil {
			t.Fatalf("record initial recency: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "recent resource sequence maximum sequence exceeds Harbor high-water 1") {
			t.Fatalf("rewound target recency error = %v", err)
		}
		assertProjectStoreMutationRecent(t, connection, reference, 2)
		if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
			t.Fatalf("rewound target recency high-water = %d, want 1", sequence)
		}
	})

	t.Run("recent other owner ahead", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		target := projectStoreMutationTestProject("project-recent-target")
		other := projectStoreMutationTestProject("project-recent-other")
		if _, err := store.PutProject(context.Background(), target); err != nil {
			t.Fatalf("put target: %v", err)
		}
		if _, err := store.PutProject(context.Background(), other); err != nil {
			t.Fatalf("put other: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = 1 WHERE id = 1")
		reference := domain.ResourceRef{ProjectID: target.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "project revision maximum sequence exceeds Harbor high-water 1") {
			t.Fatalf("other-owner recency error = %v", err)
		}
		if count := projectStoreMutationCount(t, connection, "recent_resources"); count != 0 {
			t.Fatalf("recent count after other-owner failure = %d, want 0", count)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
			t.Fatalf("other-owner recency high-water = %d, want 1", sequence)
		}
	})

	t.Run("recent same sequence owner", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-recent-reuse")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		mustProjectStoreReadExec(t, connection,
			`INSERT INTO recent_resources (project_id, resource_id, accessed_at, sequence) VALUES (?, 'docs', ?, 1)`,
			project.ID, projectStoreMutationTestClock(),
		)
		reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "reuses revision owned by project") {
			t.Fatalf("same-sequence recency error = %v", err)
		}
		assertProjectStoreMutationRecent(t, connection, reference, 1)
		if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
			t.Fatalf("same-sequence recency high-water = %d, want 1", sequence)
		}
	})
}

// TestStoreMutationPreflightRejectsUnrepresentableOwners verifies weakened rows cannot bypass sequence bounds.
func TestStoreMutationPreflightRejectsUnrepresentableOwners(t *testing.T) {
	for _, test := range []struct {
		name      string
		revision  uint64
		highWater uint64
		want      string
	}{
		{name: "zero", revision: 0, highWater: 1, want: "uses a nonpositive sequence"},
		{name: "cross-client ceiling", revision: uint64(domain.MaximumSequence) + 1, highWater: uint64(domain.MaximumSequence), want: "exceeds the cross-client ordering range"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-sequence-" + strings.ReplaceAll(test.name, " ", "-")))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put project: %v", err)
			}
			mustProjectStoreReadExec(t, connection, "UPDATE projects SET revision = ? WHERE project_id = ?", test.revision, project.ID)
			mustProjectStoreReadExec(t, connection, "UPDATE harbor_state SET sequence = ? WHERE id = 1", test.highWater)
			if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s sequence owner error = %v", test.name, err)
			}
			var revision uint64
			if err := connection.Raw("SELECT revision FROM projects WHERE project_id = ?", project.ID).Scan(&revision).Error; err != nil {
				t.Fatalf("read retained revision: %v", err)
			}
			if revision != test.revision {
				t.Fatalf("retained revision after %s failure = %d, want %d", test.name, revision, test.revision)
			}
		})
	}
}

// TestStorePutProjectRejectsIgnoredAndIncompleteCreates verifies every normalized insert and final aggregate is exact.
func TestStorePutProjectRejectsIgnoredAndIncompleteCreates(t *testing.T) {
	for _, test := range []struct {
		name       string
		table      string
		project    func() domain.ProjectSnapshot
		triggerSQL string
	}{
		{
			name:  "project",
			table: "projects",
			project: func() domain.ProjectSnapshot {
				return emptyProjectStoreMutationProject("project-ignore-root")
			},
			triggerSQL: `CREATE TRIGGER ignore_root BEFORE INSERT ON projects BEGIN SELECT RAISE(IGNORE); END`,
		},
		{
			name:  "App",
			table: "project_apps",
			project: func() domain.ProjectSnapshot {
				project := emptyProjectStoreMutationProject("project-ignore-app")
				project.Apps = []domain.AppSnapshot{{ID: "api", Name: "API", State: domain.EntityReady, Active: true, Required: true}}
				return project
			},
			triggerSQL: `CREATE TRIGGER ignore_app BEFORE INSERT ON project_apps BEGIN SELECT RAISE(IGNORE); END`,
		},
		{
			name:  "service",
			table: "project_services",
			project: func() domain.ProjectSnapshot {
				project := emptyProjectStoreMutationProject("project-ignore-service")
				project.Services = []domain.ServiceSnapshot{{ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true}}
				return project
			},
			triggerSQL: `CREATE TRIGGER ignore_service BEFORE INSERT ON project_services BEGIN SELECT RAISE(IGNORE); END`,
		},
		{
			name:  "resource",
			table: "project_resources",
			project: func() domain.ProjectSnapshot {
				project := emptyProjectStoreMutationProject("project-ignore-resource")
				project.Apps = []domain.AppSnapshot{{ID: "api", Name: "API", State: domain.EntityReady, Active: true, Required: true}}
				project.Resources = []domain.ResourceSnapshot{{ID: "docs", Name: "Docs", Kind: "documentation", URL: "https://docs.test", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "api"}}}
				return project
			},
			triggerSQL: `CREATE TRIGGER ignore_resource BEFORE INSERT ON project_resources BEGIN SELECT RAISE(IGNORE); END`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			mustProjectStoreReadExec(t, connection, test.triggerSQL)
			project := test.project()
			if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "insert affected 0 rows") {
				t.Fatalf("ignored %s create error = %v", test.table, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
				t.Fatalf("ignored %s create sequence = %d, want 0", test.table, sequence)
			}
			if count := projectStoreMutationCount(t, connection, "projects"); count != 0 {
				t.Fatalf("project count after ignored %s create = %d, want 0", test.table, count)
			}
		})
	}

	t.Run("incomplete aggregate", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER remove_new_app AFTER INSERT ON project_apps BEGIN DELETE FROM project_apps WHERE id = NEW.id; END`)
		project := emptyProjectStoreMutationProject("project-incomplete")
		project.Apps = []domain.AppSnapshot{{ID: "api", Name: "API", State: domain.EntityReady, Active: true, Required: true}}
		if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "readback aggregate differs") {
			t.Fatalf("incomplete aggregate error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
			t.Fatalf("incomplete aggregate sequence = %d, want 0", sequence)
		}
	})

	t.Run("rewritten valid field", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		mustProjectStoreReadExec(t, connection, `CREATE TRIGGER rewrite_project_name AFTER INSERT ON projects BEGIN UPDATE projects SET name = 'Rewritten Project' WHERE id = NEW.id; END`)
		project := emptyProjectStoreMutationProject("project-rewritten")
		if _, err := store.PutProject(context.Background(), project); err == nil || !strings.Contains(err.Error(), "readback aggregate differs") {
			t.Fatalf("rewritten aggregate error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 0 {
			t.Fatalf("rewritten aggregate sequence = %d, want 0", sequence)
		}
	})
}

// TestStoreRecordRecentResourceRejectsIgnoredAndRewrittenCreates verifies recency insertion is exact before commit.
func TestStoreRecordRecentResourceRejectsIgnoredAndRewrittenCreates(t *testing.T) {
	for _, test := range []struct {
		name       string
		triggerSQL string
		want       string
	}{
		{
			name:       "ignored",
			triggerSQL: `CREATE TRIGGER ignore_recent BEFORE INSERT ON recent_resources BEGIN SELECT RAISE(IGNORE); END`,
			want:       "insert affected 0 rows",
		},
		{
			name:       "rewritten",
			triggerSQL: `CREATE TRIGGER rewrite_recent_time AFTER INSERT ON recent_resources BEGIN UPDATE recent_resources SET accessed_at = '2020-01-01T00:00:00Z' WHERE id = NEW.id; END`,
			want:       "readback record differs",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-recent-" + test.name))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put project: %v", err)
			}
			mustProjectStoreReadExec(t, connection, test.triggerSQL)
			reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
			if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s recent create error = %v", test.name, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
				t.Fatalf("%s recent create sequence = %d, want 1", test.name, sequence)
			}
			if count := projectStoreMutationCount(t, connection, "recent_resources"); count != 0 {
				t.Fatalf("%s recent row count = %d, want 0", test.name, count)
			}
		})
	}
}

// TestStoreRecordRecentResourceRejectsOrphanedAggregate verifies weakened foreign keys cannot authorize recency.
func TestStoreRecordRecentResourceRejectsOrphanedAggregate(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := projectStoreMutationTestProject("project-orphan-root")
		if _, err := store.PutProject(context.Background(), project); err != nil {
			t.Fatalf("put project: %v", err)
		}
		mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
		mustProjectStoreReadExec(t, connection, "DELETE FROM projects WHERE project_id = ?", project.ID)
		reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: "docs"}
		if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), "parent project is missing") {
			t.Fatalf("orphan project recency error = %v", err)
		}
		if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
			t.Fatalf("orphan project sequence = %d, want 1", sequence)
		}
	})

	for _, test := range []struct {
		name       string
		deleteSQL  string
		resourceID domain.ResourceID
		want       string
	}{
		{
			name:       "App owner",
			deleteSQL:  "DELETE FROM project_apps WHERE project_id = ? AND app_id = 'api'",
			resourceID: "docs",
			want:       `references unknown App "api"`,
		},
		{
			name:       "service owner",
			deleteSQL:  "DELETE FROM project_services WHERE project_id = ? AND service_id = 'old-cache'",
			resourceID: "old-service-resource",
			want:       `references unknown service "old-cache"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := projectStoreMutationTestProject(domain.ProjectID("project-orphan-" + strings.ReplaceAll(strings.ToLower(test.name), " ", "-")))
			if _, err := store.PutProject(context.Background(), project); err != nil {
				t.Fatalf("put project: %v", err)
			}
			mustProjectStoreReadExec(t, connection, "PRAGMA foreign_keys = OFF")
			mustProjectStoreReadExec(t, connection, test.deleteSQL, project.ID)
			reference := domain.ResourceRef{ProjectID: project.ID, ResourceID: test.resourceID}
			if _, err := store.RecordRecentResource(context.Background(), reference); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("orphan %s recency error = %v", test.name, err)
			}
			if sequence := projectStoreMutationSequence(t, store); sequence != 1 {
				t.Fatalf("orphan %s sequence = %d, want 1", test.name, sequence)
			}
		})
	}
}

// projectStoreMutationNetworkUnregisterFixture retains one completed network release ready for final project deletion.
type projectStoreMutationNetworkUnregisterFixture struct {
	store       *Store
	connection  *gorm.DB
	journal     *OperationJournal
	running     OperationRecord
	begin       BeginProjectNetworkReleaseRequest
	release     ProjectNetworkReleaseMutationResult
	completedAt time.Time
}

// newProjectStoreMutationNetworkUnregisterFixture builds the exact initialized boundary required by finalization tests.
func newProjectStoreMutationNetworkUnregisterFixture(
	t *testing.T,
	maximumConnections int,
) projectStoreMutationNetworkUnregisterFixture {
	t.Helper()
	store, connection, journal, running, begin, _ := newNetworkReleaseTestHarness(t, maximumConnections)
	staged, err := store.BeginProjectNetworkRelease(context.Background(), begin)
	if err != nil {
		t.Fatalf("begin fixture network release: %v", err)
	}
	request := networkReleaseTestCompleteRequest(begin, staged.Release)
	release, err := store.CompleteProjectNetworkRelease(context.Background(), request)
	if err != nil {
		t.Fatalf("complete fixture network release: %v", err)
	}
	return projectStoreMutationNetworkUnregisterFixture{
		store:       store,
		connection:  connection,
		journal:     journal,
		running:     running,
		begin:       begin,
		release:     release,
		completedAt: request.At.Add(time.Minute),
	}
}

// assertProjectStoreMutationNetworkUnregisterUnchanged proves a rejected finalization wrote no authority.
func assertProjectStoreMutationNetworkUnregisterUnchanged(
	t *testing.T,
	store *Store,
	connection *gorm.DB,
	before networkModelRows,
	operation OperationRecord,
	wantSequence domain.Sequence,
) {
	t.Helper()
	if after := networkReplaceTestRows(t, connection); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected project unregister changed durable network rows")
	}
	if _, err := store.Project(context.Background(), operation.Operation.ProjectID); err != nil {
		t.Fatalf("rejected project unregister removed project: %v", err)
	}
	if persisted := networkReleaseTestOperation(t, store, operation.Operation.ID); !reflect.DeepEqual(persisted, operation) {
		t.Fatalf("operation after rejected project unregister = %#v, want %#v", persisted, operation)
	}
	if sequence := projectStoreMutationSequence(t, store); sequence != wantSequence {
		t.Fatalf("sequence after rejected project unregister = %d, want %d", sequence, wantSequence)
	}
}

// emptyProjectStoreMutationProject returns one valid aggregate with explicit empty normalized child sets.
func emptyProjectStoreMutationProject(projectID domain.ProjectID) domain.ProjectSnapshot {
	return domain.ProjectSnapshot{
		ID:        projectID,
		Name:      "Empty Project",
		Path:      "/work/" + string(projectID),
		Slug:      strings.TrimPrefix(string(projectID), "project-"),
		State:     domain.ProjectStopped,
		UpdatedAt: projectStoreMutationTestTime(),
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
}

// assertProjectStoreMutationRoot verifies rollback retained the exact root generation expected by a high-water test.
func assertProjectStoreMutationRoot(
	t *testing.T,
	connection *gorm.DB,
	projectID domain.ProjectID,
	name string,
	revision int,
) {
	t.Helper()
	var rows []models.Project
	if err := connection.Where("project_id = ?", string(projectID)).Find(&rows).Error; err != nil {
		t.Fatalf("read project root after mutation failure: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != name || rows[0].Revision != revision {
		t.Fatalf("project root after mutation failure = %#v, want one %q row at revision %d", rows, name, revision)
	}
}

// assertProjectStoreMutationRecent verifies rollback retained one recency row at its prior sequence.
func assertProjectStoreMutationRecent(
	t *testing.T,
	connection *gorm.DB,
	reference domain.ResourceRef,
	sequence int,
) {
	t.Helper()
	var rows []models.RecentResource
	if err := connection.
		Where("project_id = ? AND resource_id = ?", string(reference.ProjectID), string(reference.ResourceID)).
		Find(&rows).Error; err != nil {
		t.Fatalf("read recency after mutation failure: %v", err)
	}
	if len(rows) != 1 || rows[0].Sequence != sequence {
		t.Fatalf("recency after mutation failure = %#v, want one row at sequence %d", rows, sequence)
	}
}

// projectStoreMutationTestProject returns one deliberately unsorted aggregate with stale-owner coverage for replacement tests.
func projectStoreMutationTestProject(projectID domain.ProjectID) domain.ProjectSnapshot {
	return domain.ProjectSnapshot{
		ID:        projectID,
		Name:      "Initial Project",
		Path:      "/work/" + string(projectID),
		Slug:      strings.TrimPrefix(string(projectID), "project-"),
		State:     domain.ProjectReady,
		UpdatedAt: projectStoreMutationTestTime(),
		Apps: []domain.AppSnapshot{
			{ID: "old-worker", Name: "Old Worker", State: domain.EntityStopped, Required: false},
			{ID: "api", Name: "API", State: domain.EntityReady, Active: true, Required: true},
		},
		Services: []domain.ServiceSnapshot{
			{ID: "old-cache", Name: "Old Cache", Kind: "cache", State: domain.EntityStopped, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceAvailable},
			{ID: "mysql", Name: "MySQL", Kind: "database", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected, Required: true},
		},
		Resources: []domain.ResourceSnapshot{
			{ID: "old-service-resource", Name: "Old Service", Kind: "admin", URL: "https://old-service.test", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "old-cache"}},
			{ID: "docs", Name: "Docs", Kind: "documentation", URL: "https://initial.test/docs", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "api"}},
			{ID: "old-app-resource", Name: "Old App", Kind: "admin", URL: "https://old-app.test", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "old-worker"}},
		},
	}
}

// canonicalProjectStoreMutationProject returns the identity ordering produced by normalized readback.
func canonicalProjectStoreMutationProject(project domain.ProjectSnapshot) domain.ProjectSnapshot {
	canonical := project
	canonical.Apps = append([]domain.AppSnapshot(nil), project.Apps...)
	canonical.Services = append([]domain.ServiceSnapshot(nil), project.Services...)
	canonical.Resources = append([]domain.ResourceSnapshot(nil), project.Resources...)
	sort.Slice(canonical.Apps, func(left, right int) bool { return canonical.Apps[left].ID < canonical.Apps[right].ID })
	sort.Slice(canonical.Services, func(left, right int) bool { return canonical.Services[left].ID < canonical.Services[right].ID })
	sort.Slice(canonical.Resources, func(left, right int) bool { return canonical.Resources[left].ID < canonical.Resources[right].ID })
	return canonical
}

// projectStoreMutationRunningUnregister creates the one valid operation state from which atomic unregister may complete.
func projectStoreMutationRunningUnregister(
	t *testing.T,
	store *Store,
	project domain.ProjectSnapshot,
	operationID domain.OperationID,
) (*OperationJournal, OperationRecord, time.Time) {
	t.Helper()
	if _, err := store.PutProject(context.Background(), project); err != nil {
		t.Fatalf("put unregister project: %v", err)
	}
	journal := projectStoreMutationJournal(store)
	requestedAt := projectStoreMutationTestTime().Add(time.Hour)
	operation, err := domain.NewOperation(
		operationID,
		domain.IntentID("intent-"+operationID),
		domain.OperationKindProjectUnregister,
		project.ID,
		requestedAt,
	)
	if err != nil {
		t.Fatalf("create unregister operation: %v", err)
	}
	queued, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue unregister operation: %v", err)
	}
	running, err := journal.Transition(
		context.Background(),
		operation.ID,
		queued.Revision,
		domain.OperationRunning,
		"removing project",
		requestedAt.Add(time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("start unregister operation: %v", err)
	}
	return journal, running, requestedAt.Add(2 * time.Second)
}

// projectStoreMutationJournal builds the operation writer over the exact coordinator shared by the Store.
func projectStoreMutationJournal(store *Store) *OperationJournal {
	connections := store.mutations.connections
	return NewOperationJournal(
		connections,
		models.NewOperationRepo(connections),
		models.NewOperationTransitionRepo(connections),
		models.NewHarborStateRepo(connections),
		store.mutations,
	)
}

// projectStoreMutationRowID returns one root surrogate ID or fails when test setup is ambiguous.
func projectStoreMutationRowID(t *testing.T, connection *gorm.DB, table, identityColumn, identity string) int {
	t.Helper()
	var rows []struct{ ID int }
	if err := connection.Table(table).Select("id").Where(identityColumn+" = ?", identity).Find(&rows).Error; err != nil {
		t.Fatalf("read %s surrogate ID: %v", table, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s identity %q row count = %d, want 1", table, identity, len(rows))
	}
	return rows[0].ID
}

// projectStoreMutationScopedRowID returns one child surrogate ID or fails when test setup is ambiguous.
func projectStoreMutationScopedRowID(t *testing.T, connection *gorm.DB, table, parentColumn, parent, identityColumn, identity string) int {
	t.Helper()
	var rows []struct{ ID int }
	if err := connection.Table(table).Select("id").Where(parentColumn+" = ? AND "+identityColumn+" = ?", parent, identity).Find(&rows).Error; err != nil {
		t.Fatalf("read %s scoped surrogate ID: %v", table, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s scoped identity %q/%q row count = %d, want 1", table, parent, identity, len(rows))
	}
	return rows[0].ID
}

// projectStoreMutationScopedCount counts one natural identity without relying on generated repository helpers.
func projectStoreMutationScopedCount(t *testing.T, connection *gorm.DB, table, parentColumn, parent, identityColumn, identity string) int64 {
	t.Helper()
	var count int64
	if err := connection.Table(table).Where(parentColumn+" = ? AND "+identityColumn+" = ?", parent, identity).Count(&count).Error; err != nil {
		t.Fatalf("count %s scoped rows: %v", table, err)
	}
	return count
}

// projectStoreMutationCount counts one projection table after a mutation boundary.
func projectStoreMutationCount(t *testing.T, connection *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := connection.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}

// projectStoreMutationSequence reads the public global sequence after a mutation assertion.
func projectStoreMutationSequence(t *testing.T, store *Store) domain.Sequence {
	t.Helper()
	sequence, err := store.CurrentSequence(context.Background())
	if err != nil {
		t.Fatalf("read mutation sequence: %v", err)
	}
	return sequence
}

// projectStoreMutationTestClock returns a stable daemon clock for mutations that do not inspect time behavior.
func projectStoreMutationTestClock() time.Time {
	return projectStoreMutationTestTime().Add(24 * time.Hour)
}

// projectStoreMutationTestTime returns a stable UTC aggregate instant.
func projectStoreMutationTestTime() time.Time {
	return time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
}
