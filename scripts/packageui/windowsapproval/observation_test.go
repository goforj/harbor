package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// TestObserveProjectFencesFailedSettlement rejects crossed selections and preserves active or uncertain state.
func TestObserveProjectFencesFailedSettlement(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "project")
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation("operation-fixture", "intent-fixture", domain.OperationKindProjectStart, "project-fixture", at)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationRunning, "starting", at, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationFailed, "failed", at.Add(time.Second), &domain.Problem{Code: "project.readiness.timeout", Message: "startup deadline", Retryable: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.Snapshot{Projects: []domain.ProjectSnapshot{{ID: "project-fixture", Path: directory, State: domain.ProjectFailed}}, Operations: []domain.Operation{operation}}
	activity := control.ProjectActivity{ProjectID: "project-fixture"}
	got, err := observeProject(snapshot, activity, "project-fixture", directory)
	if err != nil || !got.SettledFailed {
		t.Fatalf("settled failed: %+v %v", got, err)
	}
	for _, test := range []struct {
		name      string
		mutate    func(*domain.Snapshot, *control.ProjectActivity)
		wantError bool
	}{
		{"extra project", func(s *domain.Snapshot, a *control.ProjectActivity) { s.Projects = append(s.Projects, s.Projects[0]) }, true},
		{"invalid activity", func(s *domain.Snapshot, a *control.ProjectActivity) {
			a.Session = &control.ProjectSessionActivity{ID: "session-fixture"}
		}, true},
		{"wrong project", func(s *domain.Snapshot, a *control.ProjectActivity) { s.Projects[0].ID = "project-foreign" }, true},
		{"wrong path", func(s *domain.Snapshot, a *control.ProjectActivity) {
			s.Projects[0].Path = filepath.Join(directory, "foreign")
		}, true},
		{"wrong activity", func(s *domain.Snapshot, a *control.ProjectActivity) { a.ProjectID = "project-foreign" }, true},
		{"active session", func(s *domain.Snapshot, a *control.ProjectActivity) {
			a.Session = &control.ProjectSessionActivity{ID: "session-fixture", State: domain.SessionPlanned, Generation: 1}
		}, false},
		{"quarantined", func(s *domain.Snapshot, a *control.ProjectActivity) { s.Projects[0].State = domain.ProjectUnavailable }, false},
		{"invalid operation", func(s *domain.Snapshot, a *control.ProjectActivity) { s.Operations[0].ID = "" }, true},
		{"missing failed start", func(s *domain.Snapshot, a *control.ProjectActivity) { s.Operations = nil }, false},
		{"nonterminal operation", func(s *domain.Snapshot, a *control.ProjectActivity) {
			s.Operations[0].State = domain.OperationRunning
			s.Operations[0].FinishedAt = nil
			s.Operations[0].Problem = nil
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := snapshot
			s.Projects = append([]domain.ProjectSnapshot(nil), snapshot.Projects...)
			s.Operations = append([]domain.Operation(nil), snapshot.Operations...)
			a := activity
			test.mutate(&s, &a)
			got, err := observeProject(s, a, "project-fixture", directory)
			if (err != nil) != test.wantError || got.SettledFailed {
				t.Fatalf("observation=%+v err=%v", got, err)
			}
		})
	}
	newer := operation
	newer.ID = "operation-newer"
	newer.State = domain.OperationSucceeded
	newer.Problem = nil
	newer.RequestedAt = at.Add(2 * time.Second)
	started, finished := at.Add(2*time.Second), at.Add(3*time.Second)
	newer.StartedAt, newer.FinishedAt = &started, &finished
	withNewer := snapshot
	withNewer.Operations = append(append([]domain.Operation(nil), snapshot.Operations...), newer)
	if got, err := observeProject(withNewer, activity, "project-fixture", directory); err != nil || got.SettledFailed {
		t.Fatalf("accepted historical failed start: %+v %v", got, err)
	}
	if _, err := observeProject(snapshot, activity, "invalid/id", directory); err == nil {
		t.Fatal("accepted invalid project ID")
	}
	if _, err := observeProject(snapshot, activity, "project-fixture", directory+string(filepath.Separator)+".."); err == nil {
		t.Fatal("accepted noncanonical absolute directory")
	}
	if _, err := observeProject(snapshot, activity, "project-fixture", "relative"); err == nil {
		t.Fatal("accepted relative directory")
	}
}
