package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// TestOperationJournalSnapshotBuildsDomainState verifies sequence and client-visible operations can directly populate a valid domain snapshot.
func TestOperationJournalSnapshotBuildsDomainState(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	first := newOperationJournalTestOperation(t, "operation-first", "intent-first", "", "host.setup", requestedAt)
	second := newOperationJournalTestOperation(t, "operation-second", "intent-second", "", "host.teardown", requestedAt.Add(time.Second))
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
	wantIDs := []domain.OperationID{first.ID, second.ID, third.ID}
	gotIDs := make([]domain.OperationID, 0, len(snapshot.Operations))
	for _, operation := range snapshot.Operations {
		gotIDs = append(gotIDs, operation.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("operation order = %v, want %v", gotIDs, wantIDs)
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

// TestOperationJournalSnapshotBoundsRecentTerminalHistoryAndRetainsActive verifies outcomes are capped independently from unbounded live work.
func TestOperationJournalSnapshotBoundsRecentTerminalHistoryAndRetainsActive(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	terminalCount := domain.SnapshotRecentTerminalOperationLimit + 2
	activeCount := domain.SnapshotRecentTerminalOperationLimit + 3
	for index := 0; index < terminalCount; index++ {
		operation := newOperationJournalTestOperation(
			t,
			domain.OperationID(fmt.Sprintf("operation-terminal-%02d", index)),
			domain.IntentID(fmt.Sprintf("intent-terminal-%02d", index)),
			"",
			"maintenance.run",
			requestedAt.Add(time.Duration(index)*time.Minute),
		)
		record, err := journal.Enqueue(context.Background(), operation)
		if err != nil {
			t.Fatalf("enqueue terminal operation %d: %v", index, err)
		}
		record = mustOperationJournalTransition(
			t,
			journal,
			record,
			domain.OperationRunning,
			"running",
			operation.RequestedAt.Add(time.Second),
			nil,
		)
		if index == terminalCount-1 {
			problem := &domain.Problem{
				Code:      "project.network.setup_required",
				Message:   "Complete network setup and try again.",
				Retryable: true,
			}
			mustOperationJournalTransition(
				t,
				journal,
				record,
				domain.OperationFailed,
				"network admission failed",
				operation.RequestedAt.Add(2*time.Second),
				problem,
			)
			continue
		}
		mustOperationJournalTransition(
			t,
			journal,
			record,
			domain.OperationSucceeded,
			"complete",
			operation.RequestedAt.Add(2*time.Second),
			nil,
		)
	}
	for index := 0; index < activeCount; index++ {
		operation := newOperationJournalTestOperation(
			t,
			domain.OperationID(fmt.Sprintf("operation-active-%02d", index)),
			domain.IntentID(fmt.Sprintf("intent-active-%02d", index)),
			"",
			"maintenance.run",
			requestedAt.Add(time.Duration(terminalCount+index)*time.Minute),
		)
		if _, err := journal.Enqueue(context.Background(), operation); err != nil {
			t.Fatalf("enqueue active operation %d: %v", index, err)
		}
	}

	snapshot, err := journal.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("read bounded operation snapshot: %v", err)
	}
	if got, want := len(snapshot.Operations), domain.SnapshotRecentTerminalOperationLimit+activeCount; got != want {
		t.Fatalf("operation count = %d, want %d", got, want)
	}
	wantIDs := make([]domain.OperationID, 0, len(snapshot.Operations))
	for index := terminalCount - domain.SnapshotRecentTerminalOperationLimit; index < terminalCount; index++ {
		wantIDs = append(wantIDs, domain.OperationID(fmt.Sprintf("operation-terminal-%02d", index)))
	}
	for index := 0; index < activeCount; index++ {
		wantIDs = append(wantIDs, domain.OperationID(fmt.Sprintf("operation-active-%02d", index)))
	}
	gotIDs := make([]domain.OperationID, 0, len(snapshot.Operations))
	for _, operation := range snapshot.Operations {
		gotIDs = append(gotIDs, operation.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("bounded operation order = %v, want %v", gotIDs, wantIDs)
	}
	failed := snapshot.Operations[domain.SnapshotRecentTerminalOperationLimit-1]
	if failed.State != domain.OperationFailed || failed.Problem == nil ||
		failed.Problem.Code != "project.network.setup_required" ||
		failed.Problem.Message != "Complete network setup and try again." ||
		!failed.Problem.Retryable {
		t.Fatalf("recent failed operation = %#v", failed)
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
				operation := newOperationJournalTestOperation(t, "operation-future", "intent-future", "", "host.setup", operationJournalTestTime())
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
				first := newOperationJournalTestOperation(t, "operation-a", "intent-a", "", "host.setup", requestedAt)
				second := newOperationJournalTestOperation(t, "operation-b", "intent-b", "", "host.teardown", requestedAt)
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
				operation := newOperationJournalTestOperation(t, "operation-unknown", "intent-unknown", "", "host.setup", operationJournalTestTime())
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
	operation := newOperationJournalTestOperation(t, "operation-rollback-read", "intent-rollback-read", "", "host.setup", operationJournalTestTime())
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

// TestOperationJournalSnapshotPreservesTerminalQueryFailure keeps the bounded-history read failure observable through the journal boundary.
func TestOperationJournalSnapshotPreservesTerminalQueryFailure(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	terminalErr := errors.New("terminal operation query failure")
	const callback = "harbor:test_terminal_operation_snapshot_query"
	if err := connection.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "operations" && strings.Contains(tx.Statement.SQL.String(), "state IN") {
			tx.AddError(terminalErr)
		}
	}); err != nil {
		t.Fatalf("register terminal operation query failure: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Callback().Query().Remove(callback)
	})

	_, err := journal.Snapshot(context.Background())
	if !errors.Is(err, terminalErr) || !strings.Contains(err.Error(), "read terminal operation snapshot") {
		t.Fatalf("terminal operation snapshot error = %v", err)
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
	operation := newOperationJournalTestOperation(t, "operation-racing-snapshot", "intent-racing-snapshot", "", "host.setup", requestedAt)
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
