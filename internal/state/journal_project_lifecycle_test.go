package state

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

// TestLatestProjectLifecycleOperationReadsBeyondBoundedSnapshot proves recovery can find aged terminal authority.
func TestLatestProjectLifecycleOperationReadsBeyondBoundedSnapshot(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	projectID := domain.ProjectID("project-history")
	requestedAt := operationJournalTestTime()

	start := enqueueSucceededJournalOperation(
		t,
		journal,
		"operation-history-start",
		"intent-history-start",
		projectID,
		domain.OperationKindProjectStart,
		requestedAt,
	)
	stop := enqueueSucceededJournalOperation(
		t,
		journal,
		"operation-history-stop",
		"intent-history-stop",
		projectID,
		domain.OperationKindProjectStop,
		requestedAt.Add(time.Minute),
	)
	if stop.Revision <= start.Revision {
		t.Fatalf("stop revision = %d, want newer than start revision %d", stop.Revision, start.Revision)
	}

	for index := 0; index < domain.SnapshotRecentTerminalOperationLimit+1; index++ {
		enqueueSucceededJournalOperation(
			t,
			journal,
			domain.OperationID(fmt.Sprintf("operation-history-maintenance-%02d", index)),
			domain.IntentID(fmt.Sprintf("intent-history-maintenance-%02d", index)),
			projectID,
			"project.maintenance",
			requestedAt.Add(time.Duration(index+2)*time.Minute),
		)
	}
	enqueueSucceededJournalOperation(
		t,
		journal,
		"operation-other-project-start",
		"intent-other-project-start",
		"project-other",
		domain.OperationKindProjectStart,
		requestedAt.Add(30*time.Minute),
	)

	snapshot, err := journal.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	for _, operation := range snapshot.Operations {
		if operation.ID == stop.Operation.ID {
			t.Fatalf("bounded snapshot still contains aged lifecycle operation %q", operation.ID)
		}
	}

	latest, err := journal.LatestProjectLifecycleOperation(t.Context(), projectID)
	if err != nil {
		t.Fatalf("LatestProjectLifecycleOperation() error = %v", err)
	}
	if latest.Operation.ID != stop.Operation.ID || latest.Revision != stop.Revision || latest.Operation.Kind != domain.OperationKindProjectStop {
		t.Fatalf("LatestProjectLifecycleOperation() = %#v, want %#v", latest, stop)
	}
}

// TestLatestProjectLifecycleOperationValidatesIdentityAndReportsTypedAbsence covers the read boundary before query execution.
func TestLatestProjectLifecycleOperationValidatesIdentityAndReportsTypedAbsence(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)

	_, err := journal.LatestProjectLifecycleOperation(t.Context(), "project-missing")
	var missing *ProjectLifecycleOperationNotFoundError
	if !errors.As(err, &missing) || missing.ProjectID != "project-missing" {
		t.Fatalf("missing lifecycle operation error = %#v", err)
	}
	if _, err := journal.LatestProjectLifecycleOperation(t.Context(), ""); err == nil {
		t.Fatal("LatestProjectLifecycleOperation(empty project) error = nil")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := journal.LatestProjectLifecycleOperation(cancelled, "project-cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("LatestProjectLifecycleOperation(cancelled) error = %v", err)
	}
}

// enqueueSucceededJournalOperation creates one terminal record whose revision can participate in ordering tests.
func enqueueSucceededJournalOperation(
	t *testing.T,
	journal *OperationJournal,
	operationID domain.OperationID,
	intentID domain.IntentID,
	projectID domain.ProjectID,
	kind domain.OperationKind,
	requestedAt time.Time,
) OperationRecord {
	t.Helper()
	operation := newOperationJournalTestOperation(t, operationID, intentID, projectID, kind, requestedAt)
	record, err := journal.Enqueue(t.Context(), operation)
	if err != nil {
		t.Fatalf("Enqueue(%q) error = %v", operationID, err)
	}
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "running", requestedAt.Add(time.Second), nil)
	return mustOperationJournalTransition(t, journal, record, domain.OperationSucceeded, "complete", requestedAt.Add(2*time.Second), nil)
}
