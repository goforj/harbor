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

// TestOperationJournalSnapshotBuildsDomainState verifies sequence and active operations can directly populate a valid domain snapshot.
func TestOperationJournalSnapshotBuildsDomainState(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	first := newOperationJournalTestOperation(t, "operation-first", "intent-first", "", "project.start", requestedAt)
	second := newOperationJournalTestOperation(t, "operation-second", "intent-second", "", "project.stop", requestedAt.Add(time.Second))
	third := newOperationJournalTestOperation(t, "operation-third", "intent-third", "", "project.restart", requestedAt.Add(2*time.Second))

	firstRecord, err := journal.Enqueue(context.Background(), first)
	if err != nil {
		t.Fatalf("enqueue first operation: %v", err)
	}
	secondRecord, err := journal.Enqueue(context.Background(), second)
	if err != nil {
		t.Fatalf("enqueue second operation: %v", err)
	}
	firstRecord = mustOperationJournalTransition(t, journal, firstRecord, domain.OperationRunning, "starting first", requestedAt.Add(3*time.Second), nil)
	secondRecord = mustOperationJournalTransition(t, journal, secondRecord, domain.OperationRunning, "starting second", requestedAt.Add(4*time.Second), nil)
	mustOperationJournalTransition(t, journal, secondRecord, domain.OperationSucceeded, "second complete", requestedAt.Add(5*time.Second), nil)
	if _, err := journal.Enqueue(context.Background(), third); err != nil {
		t.Fatalf("enqueue third operation: %v", err)
	}

	snapshot, err := journal.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("read operation snapshot: %v", err)
	}
	if snapshot.Sequence != 6 {
		t.Fatalf("snapshot sequence = %d, want 6", snapshot.Sequence)
	}
	wantIDs := []domain.OperationID{first.ID, third.ID}
	gotIDs := make([]domain.OperationID, 0, len(snapshot.Operations))
	for _, operation := range snapshot.Operations {
		gotIDs = append(gotIDs, operation.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("active operation order = %v, want %v", gotIDs, wantIDs)
	}

	domainSnapshot := domain.Snapshot{
		SchemaVersion:     domain.SnapshotSchemaVersion,
		Sequence:          snapshot.Sequence,
		CapturedAt:        requestedAt.Add(6 * time.Second),
		Projects:          []domain.ProjectSnapshot{},
		Operations:        snapshot.Operations,
		RecentResourceIDs: []domain.ResourceRef{},
	}
	if err := domainSnapshot.Validate(); err != nil {
		t.Fatalf("validate constructed domain snapshot: %v", err)
	}
}

// TestOperationJournalSnapshotReturnsCanonicalEmptyState verifies a fresh journal yields an initialized empty operation list.
func TestOperationJournalSnapshotReturnsCanonicalEmptyState(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)

	snapshot, err := journal.Snapshot(nil)
	if err != nil {
		t.Fatalf("read empty snapshot: %v", err)
	}
	if snapshot.Sequence != 0 || snapshot.Operations == nil || len(snapshot.Operations) != 0 {
		t.Fatalf("empty snapshot = %#v, want sequence zero and initialized empty operations", snapshot)
	}
}

// TestOperationJournalSnapshotRejectsCrossRecordCorruption verifies transaction-local aggregate invariants fail closed.
func TestOperationJournalSnapshotRejectsCrossRecordCorruption(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *OperationJournal)
		entity  string
		want    string
	}{
		{
			name: "missing singleton",
			corrupt: func(t *testing.T, journal *OperationJournal) {
				connection := operationJournalTestConnection(t, journal)
				if err := connection.Exec("DELETE FROM harbor_state").Error; err != nil {
					t.Fatalf("delete singleton: %v", err)
				}
			},
			entity: "harbor state",
			want:   "singleton row is missing",
		},
		{
			name: "extra singleton",
			corrupt: func(t *testing.T, journal *OperationJournal) {
				connection := operationJournalTestConnection(t, journal)
				if err := connection.Exec("INSERT INTO harbor_state (id, sequence) VALUES (2, 0)").Error; err != nil {
					t.Fatalf("insert extra singleton: %v", err)
				}
			},
			entity: "harbor state",
			want:   "singleton ID must be 1",
		},
		{
			name: "revision beyond sequence",
			corrupt: func(t *testing.T, journal *OperationJournal) {
				operation := newOperationJournalTestOperation(t, "operation-future", "intent-future", "", "project.start", operationJournalTestTime())
				if _, err := journal.Enqueue(context.Background(), operation); err != nil {
					t.Fatalf("enqueue operation: %v", err)
				}
				connection := operationJournalTestConnection(t, journal)
				if err := connection.Exec("UPDATE harbor_state SET sequence = 0 WHERE id = 1").Error; err != nil {
					t.Fatalf("rewind singleton: %v", err)
				}
			},
			entity: "operation",
			want:   "exceeds journal sequence",
		},
		{
			name: "duplicate revisions",
			corrupt: func(t *testing.T, journal *OperationJournal) {
				requestedAt := operationJournalTestTime()
				first := newOperationJournalTestOperation(t, "operation-a", "intent-a", "", "project.start", requestedAt)
				second := newOperationJournalTestOperation(t, "operation-b", "intent-b", "", "project.stop", requestedAt)
				if _, err := journal.Enqueue(context.Background(), first); err != nil {
					t.Fatalf("enqueue first operation: %v", err)
				}
				if _, err := journal.Enqueue(context.Background(), second); err != nil {
					t.Fatalf("enqueue second operation: %v", err)
				}
				connection := operationJournalTestConnection(t, journal)
				if err := connection.Exec("UPDATE operations SET revision = 1 WHERE id = ?", second.ID).Error; err != nil {
					t.Fatalf("duplicate operation revision: %v", err)
				}
			},
			entity: "operation",
			want:   "revision 1 is also used",
		},
		{
			name: "unknown active state",
			corrupt: func(t *testing.T, journal *OperationJournal) {
				operation := newOperationJournalTestOperation(t, "operation-unknown", "intent-unknown", "", "project.start", operationJournalTestTime())
				if _, err := journal.Enqueue(context.Background(), operation); err != nil {
					t.Fatalf("enqueue operation: %v", err)
				}
				connection := operationJournalTestConnection(t, journal)
				if err := connection.Exec("UPDATE operations SET state = 'unknown' WHERE id = ?", operation.ID).Error; err != nil {
					t.Fatalf("corrupt operation state: %v", err)
				}
			},
			entity: "operation",
			want:   "unknown operation state",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, _ := newOperationJournalTestHarness(t)
			test.corrupt(t, journal)

			_, err := journal.Snapshot(context.Background())
			assertCorruptStateError(t, err, test.entity)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("snapshot error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestOperationJournalSnapshotRollsBackFailedRead verifies conversion and storage failures release the single SQLite connection.
func TestOperationJournalSnapshotRollsBackFailedRead(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	operation := newOperationJournalTestOperation(t, "operation-rollback-read", "intent-rollback-read", "", "project.start", operationJournalTestTime())
	if _, err := journal.Enqueue(context.Background(), operation); err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}
	if err := connection.Exec("UPDATE operations SET phase = '   ' WHERE id = ?", operation.ID).Error; err != nil {
		t.Fatalf("corrupt operation phase: %v", err)
	}

	if _, err := journal.Snapshot(context.Background()); err == nil {
		t.Fatal("snapshot unexpectedly accepted corrupt operation")
	}
	repairCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := connection.WithContext(repairCtx).Exec("UPDATE operations SET phase = 'queued' WHERE id = ?", operation.ID).Error; err != nil {
		t.Fatalf("repair operation after failed snapshot: %v", err)
	}
	if _, err := journal.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot after repair: %v", err)
	}

	if err := connection.Exec("DROP TABLE operations").Error; err != nil {
		t.Fatalf("drop operations table: %v", err)
	}
	if _, err := journal.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "read active operation snapshot") {
		t.Fatalf("missing-table snapshot error = %v", err)
	}
	if err := connection.WithContext(repairCtx).Exec(operationJournalTestSchema[2]).Error; err != nil {
		t.Fatalf("restore operations table after failed snapshot: %v", err)
	}
	if _, err := journal.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot after table restore: %v", err)
	}
}

// TestOperationJournalSnapshotHonorsCancellation verifies cancelled reads stop before opening a transaction.
func TestOperationJournalSnapshotHonorsCancellation(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := journal.Snapshot(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot error = %v, want context.Canceled", err)
	}
}

// TestOperationJournalSnapshotRemainsConsistentDuringTransitions exercises concurrent readers against lifecycle writes.
func TestOperationJournalSnapshotRemainsConsistentDuringTransitions(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-racing-snapshot", "intent-racing-snapshot", "", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}

	var wait sync.WaitGroup
	errorsChannel := make(chan error, 1)
	wait.Add(1)
	go func() {
		defer wait.Done()
		var previous domain.Sequence
		for index := 0; index < 25; index++ {
			snapshot, snapshotErr := journal.Snapshot(context.Background())
			if snapshotErr != nil {
				select {
				case errorsChannel <- snapshotErr:
				default:
				}
				return
			}
			if snapshot.Sequence < previous {
				select {
				case errorsChannel <- errors.New("snapshot sequence moved backwards"):
				default:
				}
				return
			}
			previous = snapshot.Sequence
		}
	}()

	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRequiresApproval, "waiting for approval", requestedAt.Add(2*time.Second), nil)
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "resuming", requestedAt.Add(3*time.Second), nil)
	mustOperationJournalTransition(t, journal, record, domain.OperationSucceeded, "complete", requestedAt.Add(4*time.Second), nil)
	wait.Wait()
	close(errorsChannel)
	for snapshotErr := range errorsChannel {
		t.Fatalf("concurrent snapshot: %v", snapshotErr)
	}
}

// operationJournalTestConnection resolves the harness connection when a corruption case only receives the journal.
func operationJournalTestConnection(t *testing.T, journal *OperationJournal) *gorm.DB {
	t.Helper()
	connection, err := journal.connections.GetHarbord()
	if err != nil {
		t.Fatalf("open operation journal test connection: %v", err)
	}
	return connection
}
