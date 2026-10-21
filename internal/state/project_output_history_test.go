package state

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// assertProjectOutputHistory proves retirement preserved diagnostics without retaining process authority.
func assertProjectOutputHistory(t *testing.T, store *Store, project domain.ProjectID, session domain.SessionID, generation uint64, endedAt time.Time) {
	t.Helper()
	history, err := store.RetainedProjectOutput(t.Context(), project)
	if err != nil || history == nil || history.SessionID != session || history.Generation != generation || !history.EndedAt.Equal(endedAt) {
		t.Fatalf("retained output = %#v, %v", history, err)
	}
	if _, err := store.ActiveProjectSession(t.Context(), project); err == nil {
		t.Fatal("retained output kept active process authority")
	}
}

// TestRetainedProjectOutputRejectsStaleOrInvalidSelection fences diagnostics against later attempts and missing registrations.
func TestRetainedProjectOutputRejectsStaleOrInvalidSelection(t *testing.T) {
	for _, scenario := range []string{"valid", "missing history", "new attempt", "stale completion", "bad session", "bad generation", "missing completion", "missing project", "canceled", "invalid project"} {
		t.Run(scenario, func(t *testing.T) {
			store, db := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
			project := emptyProjectStoreMutationProject("project-history")
			project.State = domain.ProjectFailed
			if _, err := store.PutProject(t.Context(), project); err != nil {
				t.Fatal(err)
			}
			history := ProjectOutputHistory{ProjectID: project.ID, SessionID: "session-failed", Generation: 2, EndedAt: project.UpdatedAt}
			switch scenario {
			case "new attempt":
				if err := db.Exec("UPDATE projects SET state = 'starting' WHERE project_id = ?", string(project.ID)).Error; err != nil {
					t.Fatal(err)
				}
			case "stale completion":
				history.EndedAt = history.EndedAt.Add(-time.Second)
			case "bad session":
				history.SessionID = " invalid "
			case "bad generation":
				history.Generation = 0
			case "missing completion":
				history.EndedAt = time.Time{}
			}
			if scenario != "missing history" {
				if err := db.Create(&history).Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if scenario == "missing project" {
				project.ID = "project-missing"
			}
			if scenario == "invalid project" {
				project.ID = ""
			}
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := store.RetainedProjectOutput(ctx, project.ID)
			wantError := scenario == "bad session" || scenario == "bad generation" || scenario == "missing completion" || scenario == "missing project" || scenario == "invalid project" || scenario == "canceled"
			if (err != nil) != wantError {
				t.Fatalf("selection = %#v, %v", got, err)
			}
			if (got != nil) != (scenario == "valid") {
				t.Fatalf("selection = %#v", got)
			}
			if scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
