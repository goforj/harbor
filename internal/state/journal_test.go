package state

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// operationJournalTestSchema stays intentionally permissive so corruption tests can bypass production constraints and prove reads fail closed.
var operationJournalTestSchema = []string{
	`CREATE TABLE harbor_state (
		id INTEGER PRIMARY KEY,
		sequence INTEGER NOT NULL
	)`,
	`INSERT INTO harbor_state (id, sequence) VALUES (1, 0)`,
	`CREATE TABLE operations (
		id TEXT PRIMARY KEY,
		intent_id TEXT NOT NULL UNIQUE,
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
	)`,
	`CREATE TABLE operation_transitions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		operation_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		previous_state TEXT,
		state TEXT NOT NULL,
		phase TEXT NOT NULL,
		problem_code TEXT,
		problem_message TEXT,
		problem_retryable BOOLEAN,
		occurred_at DATETIME NOT NULL,
		sequence INTEGER NOT NULL,
		UNIQUE (operation_id, ordinal)
	)`,
	`CREATE TABLE projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		revision INTEGER NOT NULL
	)`,
	`CREATE TABLE recent_resources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		resource_id TEXT NOT NULL,
		sequence INTEGER NOT NULL
	)`,
}

// TestOperationJournalRejectsRewoundGlobalHighWater verifies the shared allocator protects journal writers too.
func TestOperationJournalRejectsRewoundGlobalHighWater(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	if err := connection.Exec("INSERT INTO projects (project_id, revision) VALUES ('project-ahead', 1)").Error; err != nil {
		t.Fatalf("insert retained project owner: %v", err)
	}
	operation := newOperationJournalTestOperation(t, "operation-rewound", "intent-rewound", "project-ahead", "project.start", operationJournalTestTime())
	if _, err := journal.Enqueue(context.Background(), operation); err == nil || !strings.Contains(err.Error(), "sequence exceeds Harbor high-water 0") {
		t.Fatalf("rewound journal error = %v", err)
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 0 {
		t.Fatalf("journal high-water after rejection = %d, want 0", sequence)
	}
	var count int64
	if err := connection.Model(&models.Operation{}).Count(&count).Error; err != nil {
		t.Fatalf("count operations after rejection: %v", err)
	}
	if count != 0 {
		t.Fatalf("operation count after rejection = %d, want 0", count)
	}
}

// TestOperationJournalEnqueueReplaysMatchingIntent verifies retries return the original durable identity without consuming sequence.
func TestOperationJournalEnqueueReplaysMatchingIntent(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	original := newOperationJournalTestOperation(t, "operation-original", "intent-retry", "project-a", "project.start", requestedAt)

	first, err := journal.Enqueue(context.Background(), original)
	if err != nil {
		t.Fatalf("enqueue original operation: %v", err)
	}
	retry := newOperationJournalTestOperation(t, "operation-retry", "intent-retry", "project-a", "project.start", requestedAt.Add(time.Minute))
	replayed, err := journal.Enqueue(context.Background(), retry)
	if err != nil {
		t.Fatalf("replay operation: %v", err)
	}
	if replayed.Operation.ID != original.ID || replayed.Revision != 1 {
		t.Fatalf("replayed record = %#v, want original operation at revision 1", replayed)
	}
	if replayed.Operation.RequestedAt != requestedAt {
		t.Fatalf("replayed requested time = %s, want %s", replayed.Operation.RequestedAt, requestedAt)
	}
	if first.Operation.ID != replayed.Operation.ID || first.Revision != replayed.Revision {
		t.Fatalf("first record %#v and replay %#v differ", first, replayed)
	}

	sequence := mustOperationJournalSequence(t, journal)
	if sequence != 1 {
		t.Fatalf("sequence after replay = %d, want 1", sequence)
	}
	transitions, err := journal.Transitions(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("read transitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].Ordinal != 1 || transitions[0].Sequence != 1 || transitions[0].PreviousState != nil {
		t.Fatalf("initial transition history = %#v", transitions)
	}
}

// TestOperationJournalEnqueueReplayValidatesDurableEvidence verifies idempotent reads cannot bypass retained ordering or operation history checks.
func TestOperationJournalEnqueueReplayValidatesDurableEvidence(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(*testing.T, *gorm.DB, domain.OperationID)
		want    string
	}{
		{
			name: "history",
			corrupt: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID) {
				t.Helper()
				if err := connection.Model(&models.OperationTransition{}).
					Where("operation_id = ? AND ordinal = 1", string(operationID)).
					Update("phase", "damaged").Error; err != nil {
					t.Fatalf("corrupt replay history: %v", err)
				}
			},
			want: "phase does not match latest transition",
		},
		{
			name: "retained bounds",
			corrupt: func(t *testing.T, connection *gorm.DB, _ domain.OperationID) {
				t.Helper()
				if err := connection.Exec("INSERT INTO projects (project_id, revision) VALUES ('project-ahead', 2)").Error; err != nil {
					t.Fatalf("insert future retained owner: %v", err)
				}
			},
			want: "project revision maximum sequence exceeds Harbor high-water 1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, connection := newOperationJournalTestHarness(t)
			requestedAt := operationJournalTestTime()
			original := newOperationJournalTestOperation(t, "operation-replay-evidence", "intent-replay-evidence", "project-a", "project.start", requestedAt)
			if _, err := journal.Enqueue(context.Background(), original); err != nil {
				t.Fatalf("enqueue original operation: %v", err)
			}
			test.corrupt(t, connection, original.ID)
			retry := newOperationJournalTestOperation(t, "operation-retry-evidence", original.IntentID, original.ProjectID, original.Kind, requestedAt.Add(time.Minute))
			if _, err := journal.Enqueue(context.Background(), retry); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("damaged replay error = %v, want %q", err, test.want)
			}
			if sequence := mustOperationJournalSequence(t, journal); sequence != 1 {
				t.Fatalf("sequence after damaged replay = %d, want 1", sequence)
			}
		})
	}
}

// TestOperationJournalReservesUnregisterSuccessForStore verifies only the atomic project Store path may publish removal success.
func TestOperationJournalReservesUnregisterSuccessForStore(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(
		t,
		"operation-unregister-reserved",
		"intent-unregister-reserved",
		"project-a",
		domain.OperationKindProjectUnregister,
		requestedAt,
	)
	queued, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue unregister operation: %v", err)
	}
	running := mustOperationJournalTransition(t, journal, queued, domain.OperationRunning, "removing", requestedAt.Add(time.Second), nil)
	if _, err := journal.Transition(
		context.Background(), operation.ID, running.Revision, domain.OperationSucceeded, "removed", requestedAt.Add(2*time.Second), nil,
	); err == nil || !strings.Contains(err.Error(), "must complete through the project Store") {
		t.Fatalf("generic unregister success error = %v", err)
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != running.Revision {
		t.Fatalf("sequence after reserved success = %d, want %d", sequence, running.Revision)
	}
	persisted, err := journal.Operation(context.Background(), operation.ID)
	if err != nil || persisted.Revision != running.Revision || persisted.Operation.State != domain.OperationRunning {
		t.Fatalf("operation after reserved success = %#v, error %v", persisted, err)
	}

	problem := &domain.Problem{Code: "remove_failed", Message: "The project could not be removed."}
	failed, err := journal.Transition(
		context.Background(), operation.ID, running.Revision, domain.OperationFailed, "remove failed", requestedAt.Add(2*time.Second), problem,
	)
	if err != nil {
		t.Fatalf("fail unregister through generic journal: %v", err)
	}
	if failed.Revision != running.Revision+1 || failed.Operation.State != domain.OperationFailed {
		t.Fatalf("failed unregister = %#v", failed)
	}
}

// TestOperationJournalRejectsIntentConflict verifies an idempotency key cannot silently change mutation meaning.
func TestOperationJournalRejectsIntentConflict(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	original := newOperationJournalTestOperation(t, "operation-a", "intent-a", "project-a", "project.start", requestedAt)
	if _, err := journal.Enqueue(context.Background(), original); err != nil {
		t.Fatalf("enqueue original operation: %v", err)
	}

	conflicts := []domain.Operation{
		newOperationJournalTestOperation(t, "operation-b", "intent-a", "project-a", "project.stop", requestedAt),
		newOperationJournalTestOperation(t, "operation-c", "intent-a", "project-b", "project.start", requestedAt),
	}
	for _, conflict := range conflicts {
		_, err := journal.Enqueue(context.Background(), conflict)
		var typed *IntentConflictError
		if !errors.As(err, &typed) {
			t.Fatalf("enqueue conflict error = %v, want IntentConflictError", err)
		}
		if typed.IntentID != original.IntentID || typed.ExistingOperationID != original.ID {
			t.Fatalf("intent conflict = %#v", typed)
		}
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 1 {
		t.Fatalf("sequence after conflicts = %d, want 1", sequence)
	}
}

// TestOperationJournalRejectsOperationIDConflict verifies daemon operation identities cannot move between intents.
func TestOperationJournalRejectsOperationIDConflict(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	original := newOperationJournalTestOperation(t, "operation-a", "intent-a", "project-a", "project.start", requestedAt)
	if _, err := journal.Enqueue(context.Background(), original); err != nil {
		t.Fatalf("enqueue original operation: %v", err)
	}
	conflict := newOperationJournalTestOperation(t, "operation-a", "intent-b", "project-a", "project.start", requestedAt)

	_, err := journal.Enqueue(context.Background(), conflict)
	var typed *OperationIDConflictError
	if !errors.As(err, &typed) {
		t.Fatalf("enqueue conflict error = %v, want OperationIDConflictError", err)
	}
	if typed.ExistingIntentID != original.IntentID || typed.RequestedIntentID != conflict.IntentID {
		t.Fatalf("operation ID conflict = %#v", typed)
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 1 {
		t.Fatalf("sequence after conflict = %d, want 1", sequence)
	}
}

// TestOperationJournalPersistsLifecycleAndHistory verifies domain transitions, failure details, and lookup APIs remain aligned.
func TestOperationJournalPersistsLifecycleAndHistory(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-lifecycle", "intent-lifecycle", "project-a", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}

	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "starting services", requestedAt.Add(time.Second), nil)
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRequiresApproval, "installing trust", requestedAt.Add(2*time.Second), nil)
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "verifying routes", requestedAt.Add(3*time.Second), nil)
	problem := &domain.Problem{Code: "route_unavailable", Message: "The project route did not become ready.", Retryable: true}
	record = mustOperationJournalTransition(t, journal, record, domain.OperationFailed, "route verification failed", requestedAt.Add(4*time.Second), problem)

	if record.Revision != 5 || record.Operation.State != domain.OperationFailed {
		t.Fatalf("terminal record = %#v", record)
	}
	if record.Operation.StartedAt == nil || *record.Operation.StartedAt != requestedAt.Add(time.Second) {
		t.Fatalf("operation start time = %v", record.Operation.StartedAt)
	}
	if record.Operation.FinishedAt == nil || *record.Operation.FinishedAt != requestedAt.Add(4*time.Second) {
		t.Fatalf("operation finish time = %v", record.Operation.FinishedAt)
	}
	if record.Operation.Problem == nil || *record.Operation.Problem != *problem {
		t.Fatalf("operation problem = %#v, want %#v", record.Operation.Problem, problem)
	}

	byID, err := journal.Operation(context.Background(), operation.ID)
	if err != nil {
		t.Fatalf("read operation by ID: %v", err)
	}
	byIntent, err := journal.OperationByIntent(context.Background(), operation.IntentID)
	if err != nil {
		t.Fatalf("read operation by intent: %v", err)
	}
	if byID.Revision != record.Revision || byIntent.Revision != record.Revision || byIntent.Operation.ID != operation.ID {
		t.Fatalf("lookup records = by ID %#v, by intent %#v", byID, byIntent)
	}

	history, err := journal.Transitions(context.Background(), operation.ID)
	if err != nil {
		t.Fatalf("read operation history: %v", err)
	}
	wantStates := []domain.OperationState{
		domain.OperationQueued,
		domain.OperationRunning,
		domain.OperationRequiresApproval,
		domain.OperationRunning,
		domain.OperationFailed,
	}
	if len(history) != len(wantStates) {
		t.Fatalf("history length = %d, want %d", len(history), len(wantStates))
	}
	for index, transition := range history {
		if transition.Ordinal != uint64(index+1) || transition.Sequence != domain.Sequence(index+1) || transition.State != wantStates[index] {
			t.Fatalf("history[%d] = %#v", index, transition)
		}
		if index > 0 && (transition.PreviousState == nil || *transition.PreviousState != wantStates[index-1]) {
			t.Fatalf("history[%d] previous state = %v", index, transition.PreviousState)
		}
	}
	if history[len(history)-1].Problem == nil || *history[len(history)-1].Problem != *problem {
		t.Fatalf("failed history problem = %#v", history[len(history)-1].Problem)
	}
}

// TestOperationJournalActiveOperationsOrdersByRevision verifies clients receive deterministic work ordering and no terminal rows.
func TestOperationJournalActiveOperationsOrdersByRevision(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	first := newOperationJournalTestOperation(t, "operation-z", "intent-z", "project-z", "project.start", requestedAt)
	second := newOperationJournalTestOperation(t, "operation-a", "intent-a", "project-a", "project.start", requestedAt)
	firstRecord, err := journal.Enqueue(context.Background(), first)
	if err != nil {
		t.Fatalf("enqueue first operation: %v", err)
	}
	secondRecord, err := journal.Enqueue(context.Background(), second)
	if err != nil {
		t.Fatalf("enqueue second operation: %v", err)
	}
	firstRecord = mustOperationJournalTransition(t, journal, firstRecord, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)

	active, err := journal.ActiveOperations(context.Background())
	if err != nil {
		t.Fatalf("read active operations: %v", err)
	}
	if len(active) != 2 || active[0].Operation.ID != second.ID || active[1].Operation.ID != first.ID {
		t.Fatalf("active operations = %#v, want revision order [%q, %q]", active, second.ID, first.ID)
	}
	if _, err := journal.Transition(context.Background(), second.ID, secondRecord.Revision, domain.OperationCancelled, "cancelled", requestedAt.Add(2*time.Second), nil); err != nil {
		t.Fatalf("cancel second operation: %v", err)
	}
	active, err = journal.ActiveOperations(context.Background())
	if err != nil {
		t.Fatalf("read remaining active operations: %v", err)
	}
	if len(active) != 1 || active[0].Operation.ID != first.ID || active[0].Revision != firstRecord.Revision {
		t.Fatalf("remaining active operations = %#v", active)
	}
}

// TestOperationJournalStaleAndInvalidTransitionsDoNotConsumeSequence verifies rejected lifecycle writes leave all journal rows untouched.
func TestOperationJournalStaleAndInvalidTransitionsDoNotConsumeSequence(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-stale", "intent-stale", "project-a", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}

	_, err = journal.Transition(context.Background(), operation.ID, 0, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
	var stale *StaleRevisionError
	if !errors.As(err, &stale) || stale.Actual != 1 || stale.Expected != 0 {
		t.Fatalf("stale transition error = %#v (%v)", stale, err)
	}
	_, err = journal.Transition(context.Background(), operation.ID, record.Revision, domain.OperationSucceeded, "impossible", requestedAt.Add(time.Second), nil)
	if err == nil {
		t.Fatal("queued-to-succeeded transition unexpectedly succeeded")
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 1 {
		t.Fatalf("sequence after rejected transitions = %d, want 1", sequence)
	}
	history, err := journal.Transitions(context.Background(), operation.ID)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history after rejected transitions = %#v", history)
	}
}

// TestOperationJournalRollsBackAllocatedSequence verifies a late append failure rolls back the counter and operation update together.
func TestOperationJournalRollsBackAllocatedSequence(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-rollback", "intent-rollback", "project-a", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
	if err := connection.Exec(`CREATE TRIGGER reject_late_transition
		BEFORE INSERT ON operation_transitions
		WHEN NEW.ordinal > 2
		BEGIN
			SELECT RAISE(ABORT, 'forced transition failure');
		END`).Error; err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	_, err = journal.Transition(context.Background(), operation.ID, record.Revision, domain.OperationSucceeded, "ready", requestedAt.Add(2*time.Second), nil)
	if err == nil {
		t.Fatal("transition unexpectedly survived failure trigger")
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 2 {
		t.Fatalf("sequence after rollback = %d, want 2", sequence)
	}
	stored, err := journal.Operation(context.Background(), operation.ID)
	if err != nil {
		t.Fatalf("read operation after rollback: %v", err)
	}
	if stored.Revision != 2 || stored.Operation.State != domain.OperationRunning {
		t.Fatalf("operation after rollback = %#v", stored)
	}
	history, err := journal.Transitions(context.Background(), operation.ID)
	if err != nil {
		t.Fatalf("read history after rollback: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history after rollback = %#v", history)
	}
}

// TestOperationJournalConcurrentEnqueueReplaysOneCommit verifies concurrent client retries create one operation and one sequence.
func TestOperationJournalConcurrentEnqueueReplaysOneCommit(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operations := []domain.Operation{
		newOperationJournalTestOperation(t, "operation-concurrent-a", "intent-concurrent", "project-a", "project.start", requestedAt),
		newOperationJournalTestOperation(t, "operation-concurrent-b", "intent-concurrent", "project-a", "project.start", requestedAt),
	}

	results := make(chan OperationRecord, len(operations))
	errorsChannel := make(chan error, len(operations))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, operation := range operations {
		operation := operation
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			record, err := journal.Enqueue(context.Background(), operation)
			results <- record
			errorsChannel <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsChannel)

	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent enqueue: %v", err)
		}
	}
	var durableID domain.OperationID
	for record := range results {
		if record.Revision != 1 {
			t.Fatalf("concurrent enqueue revision = %d, want 1", record.Revision)
		}
		if durableID == "" {
			durableID = record.Operation.ID
		} else if record.Operation.ID != durableID {
			t.Fatalf("concurrent enqueues returned IDs %q and %q", durableID, record.Operation.ID)
		}
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 1 {
		t.Fatalf("sequence after concurrent replay = %d, want 1", sequence)
	}
}

// TestOperationJournalConcurrentTransitionAllowsOneRevision verifies optimistic concurrency commits one edge and rejects the stale peer.
func TestOperationJournalConcurrentTransitionAllowsOneRevision(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-race", "intent-race", "project-a", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}

	errorsChannel := make(chan error, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := journal.Transition(context.Background(), operation.ID, record.Revision, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
			errorsChannel <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsChannel)

	succeeded := 0
	staleCount := 0
	for err := range errorsChannel {
		if err == nil {
			succeeded++
			continue
		}
		var stale *StaleRevisionError
		if errors.As(err, &stale) {
			staleCount++
			continue
		}
		t.Fatalf("concurrent transition error = %v", err)
	}
	if succeeded != 1 || staleCount != 1 {
		t.Fatalf("concurrent outcomes = %d success, %d stale", succeeded, staleCount)
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 2 {
		t.Fatalf("sequence after concurrent transition = %d, want 2", sequence)
	}
	history, err := journal.Transitions(context.Background(), operation.ID)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history after concurrent transition = %#v", history)
	}
}

// TestOperationJournalReportsNotFoundLookups verifies absent operation identities remain distinguishable from storage failures.
func TestOperationJournalReportsNotFoundLookups(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)

	_, err := journal.Operation(context.Background(), "missing-operation")
	var missingOperation *OperationNotFoundError
	if !errors.As(err, &missingOperation) {
		t.Fatalf("operation lookup error = %v, want OperationNotFoundError", err)
	}
	_, err = journal.OperationByIntent(context.Background(), "missing-intent")
	var missingIntent *OperationIntentNotFoundError
	if !errors.As(err, &missingIntent) {
		t.Fatalf("intent lookup error = %v, want OperationIntentNotFoundError", err)
	}
	_, err = journal.Transitions(context.Background(), "missing-operation")
	if !errors.As(err, &missingOperation) {
		t.Fatalf("transition lookup error = %v, want OperationNotFoundError", err)
	}
}

// TestOperationJournalRejectsCorruptOperationRows verifies generated model reads cannot leak invalid domain state.
func TestOperationJournalRejectsCorruptOperationRows(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	operation := newOperationJournalTestOperation(t, "operation-corrupt", "intent-corrupt", "project-a", "project.start", operationJournalTestTime())
	if _, err := journal.Enqueue(context.Background(), operation); err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}
	if err := connection.Model(&models.Operation{}).Where("id = ?", string(operation.ID)).Update("state", "unknown").Error; err != nil {
		t.Fatalf("corrupt operation state: %v", err)
	}

	_, err := journal.Operation(context.Background(), operation.ID)
	assertCorruptStateError(t, err, "operation")
	_, err = journal.OperationByIntent(context.Background(), operation.IntentID)
	assertCorruptStateError(t, err, "operation")
	_, err = journal.ActiveOperations(context.Background())
	if err != nil {
		t.Fatalf("unknown terminal-like state should be excluded from active query, got %v", err)
	}
	if err := connection.Model(&models.Operation{}).Where("id = ?", string(operation.ID)).Update("state", string(domain.OperationQueued)).Error; err != nil {
		t.Fatalf("restore operation state: %v", err)
	}
	if err := connection.Model(&models.Operation{}).Where("id = ?", string(operation.ID)).Update("revision", 0).Error; err != nil {
		t.Fatalf("corrupt operation revision: %v", err)
	}
	_, err = journal.ActiveOperations(context.Background())
	assertCorruptStateError(t, err, "operation")
}

// TestOperationJournalRejectsCorruptTransitionHistory verifies every history row and cross-row edge is validated.
func TestOperationJournalRejectsCorruptTransitionHistory(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-history", "intent-history", "project-a", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}
	if err := connection.Model(&models.OperationTransition{}).
		Where("operation_id = ? AND ordinal = ?", string(operation.ID), 1).
		Update("sequence", 0).Error; err != nil {
		t.Fatalf("corrupt transition sequence: %v", err)
	}
	_, err = journal.Transitions(context.Background(), operation.ID)
	assertCorruptStateError(t, err, "operation transition")

	if err := connection.Model(&models.OperationTransition{}).
		Where("operation_id = ? AND ordinal = ?", string(operation.ID), 1).
		Update("sequence", 1).Error; err != nil {
		t.Fatalf("restore transition sequence: %v", err)
	}
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
	if err := connection.Model(&models.OperationTransition{}).
		Where("operation_id = ? AND ordinal = ?", string(operation.ID), 2).
		Update("previous_state", string(domain.OperationRequiresApproval)).Error; err != nil {
		t.Fatalf("corrupt transition predecessor: %v", err)
	}
	_, err = journal.Transitions(context.Background(), operation.ID)
	assertCorruptStateError(t, err, "operation transition")
}

// TestOperationJournalRejectsNonChronologicalHistory verifies time-traveling edges cannot be read or extended.
func TestOperationJournalRejectsNonChronologicalHistory(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-time", "intent-time", "project-a", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
	if err := connection.Model(&models.OperationTransition{}).
		Where("operation_id = ? AND ordinal = ?", string(operation.ID), 2).
		Update("occurred_at", requestedAt.Add(-time.Second)).Error; err != nil {
		t.Fatalf("corrupt transition occurrence time: %v", err)
	}

	_, err = journal.Transitions(context.Background(), operation.ID)
	assertCorruptStateError(t, err, "operation transition")
	_, err = journal.Transition(context.Background(), operation.ID, record.Revision, domain.OperationSucceeded, "ready", requestedAt.Add(2*time.Second), nil)
	assertCorruptStateError(t, err, "operation transition")
	if sequence := mustOperationJournalSequence(t, journal); sequence != 2 {
		t.Fatalf("sequence after corrupt history rejection = %d, want 2", sequence)
	}
}

// TestOperationJournalRejectsAggregateHistoryMismatch verifies every materialized lifecycle field is derived from the final history edge.
func TestOperationJournalRejectsAggregateHistoryMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, domain.OperationID, time.Time)
	}{
		{
			name: "state",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID, _ time.Time) {
				t.Helper()
				if err := connection.Exec(
					"UPDATE operations SET state = ?, problem_code = NULL, problem_message = NULL, problem_retryable = NULL WHERE id = ?",
					string(domain.OperationSucceeded),
					string(operationID),
				).Error; err != nil {
					t.Fatalf("change aggregate state: %v", err)
				}
			},
		},
		{
			name: "revision",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID, _ time.Time) {
				t.Helper()
				if err := connection.Model(&models.Operation{}).Where("id = ?", string(operationID)).Update("revision", 99).Error; err != nil {
					t.Fatalf("change aggregate revision: %v", err)
				}
			},
		},
		{
			name: "phase",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID, _ time.Time) {
				t.Helper()
				if err := connection.Model(&models.Operation{}).Where("id = ?", string(operationID)).Update("phase", "different phase").Error; err != nil {
					t.Fatalf("change aggregate phase: %v", err)
				}
			},
		},
		{
			name: "problem",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID, _ time.Time) {
				t.Helper()
				if err := connection.Model(&models.Operation{}).Where("id = ?", string(operationID)).Update("problem_message", "A different failure occurred.").Error; err != nil {
					t.Fatalf("change aggregate problem: %v", err)
				}
			},
		},
		{
			name: "requested time",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID, requestedAt time.Time) {
				t.Helper()
				if err := connection.Model(&models.Operation{}).Where("id = ?", string(operationID)).Update("requested_at", requestedAt.Add(-time.Second)).Error; err != nil {
					t.Fatalf("change aggregate requested time: %v", err)
				}
			},
		},
		{
			name: "started time",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID, requestedAt time.Time) {
				t.Helper()
				if err := connection.Model(&models.Operation{}).Where("id = ?", string(operationID)).Update("started_at", requestedAt.Add(500*time.Millisecond)).Error; err != nil {
					t.Fatalf("change aggregate started time: %v", err)
				}
			},
		},
		{
			name: "finished time",
			mutate: func(t *testing.T, connection *gorm.DB, operationID domain.OperationID, requestedAt time.Time) {
				t.Helper()
				if err := connection.Model(&models.Operation{}).Where("id = ?", string(operationID)).Update("finished_at", requestedAt.Add(3*time.Second)).Error; err != nil {
					t.Fatalf("change aggregate finished time: %v", err)
				}
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, connection := newOperationJournalTestHarness(t)
			requestedAt := operationJournalTestTime().Add(time.Duration(index) * time.Hour)
			operationID := domain.OperationID("operation-aggregate-" + strings.ReplaceAll(test.name, " ", "-"))
			intentID := domain.IntentID("intent-aggregate-" + strings.ReplaceAll(test.name, " ", "-"))
			operation := newOperationJournalTestOperation(t, operationID, intentID, "project-a", "project.start", requestedAt)
			record, err := journal.Enqueue(context.Background(), operation)
			if err != nil {
				t.Fatalf("enqueue operation: %v", err)
			}
			record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
			problem := &domain.Problem{Code: "failed", Message: "The operation failed.", Retryable: true}
			mustOperationJournalTransition(t, journal, record, domain.OperationFailed, "failed", requestedAt.Add(2*time.Second), problem)
			test.mutate(t, connection, operation.ID, requestedAt)

			_, err = journal.Transitions(context.Background(), operation.ID)
			assertCorruptStateError(t, err, "operation")
		})
	}
}

// TestOperationJournalRejectsHistoryGapsBeforeAppend verifies an ordinal gap cannot be hidden by appending a later edge.
func TestOperationJournalRejectsHistoryGapsBeforeAppend(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-gap", "intent-gap", "project-a", "project.start", requestedAt)
	record, err := journal.Enqueue(context.Background(), operation)
	if err != nil {
		t.Fatalf("enqueue operation: %v", err)
	}
	record = mustOperationJournalTransition(t, journal, record, domain.OperationRunning, "starting", requestedAt.Add(time.Second), nil)
	if err := connection.Where("operation_id = ? AND ordinal = ?", string(operation.ID), 1).Delete(&models.OperationTransition{}).Error; err != nil {
		t.Fatalf("remove initial transition: %v", err)
	}

	_, err = journal.Transition(context.Background(), operation.ID, record.Revision, domain.OperationSucceeded, "ready", requestedAt.Add(2*time.Second), nil)
	assertCorruptStateError(t, err, "operation transition")
	if sequence := mustOperationJournalSequence(t, journal); sequence != 2 {
		t.Fatalf("sequence after history gap rejection = %d, want 2", sequence)
	}
}

// TestOperationJournalRejectsCorruptSingleton verifies the global sequence cannot be read from malformed durable state.
func TestOperationJournalRejectsCorruptSingleton(t *testing.T) {
	journal, connection := newOperationJournalTestHarness(t)
	if err := connection.Model(&models.HarborState{}).Where("id = 1").Update("sequence", -1).Error; err != nil {
		t.Fatalf("corrupt journal singleton: %v", err)
	}
	_, err := journal.CurrentSequence(context.Background())
	assertCorruptStateError(t, err, "harbor state")

	if err := connection.Where("id = 1").Delete(&models.HarborState{}).Error; err != nil {
		t.Fatalf("remove journal singleton: %v", err)
	}
	_, err = journal.CurrentSequence(context.Background())
	assertCorruptStateError(t, err, "harbor state")
}

// TestOperationJournalHonorsContext verifies cancellation prevents mutations and nil contexts remain usable.
func TestOperationJournalHonorsContext(t *testing.T) {
	journal, _ := newOperationJournalTestHarness(t)
	requestedAt := operationJournalTestTime()
	operation := newOperationJournalTestOperation(t, "operation-context", "intent-context", "project-a", "project.start", requestedAt)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := journal.Enqueue(cancelled, operation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled enqueue error = %v, want context.Canceled", err)
	}
	if sequence := mustOperationJournalSequence(t, journal); sequence != 0 {
		t.Fatalf("sequence after cancelled enqueue = %d, want 0", sequence)
	}
	if _, err := journal.Enqueue(nil, operation); err != nil {
		t.Fatalf("enqueue with nil context: %v", err)
	}
	if _, err := journal.Operation(cancelled, operation.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error = %v, want context.Canceled", err)
	}
}

// TestValidateRetainedSequenceBoundsRejectsMixedNonpositiveOwners verifies MIN checks cover every retained ordering table.
func TestValidateRetainedSequenceBoundsRejectsMixedNonpositiveOwners(t *testing.T) {
	tests := []struct {
		name   string
		insert func(*testing.T, *gorm.DB, int)
		want   string
	}{
		{
			name: "projects",
			insert: func(t *testing.T, connection *gorm.DB, nonpositive int) {
				t.Helper()
				if err := connection.Exec("INSERT INTO projects (project_id, revision) VALUES ('project-positive', 1), ('project-nonpositive', ?)", nonpositive).Error; err != nil {
					t.Fatalf("insert project bounds: %v", err)
				}
			},
			want: "project revision minimum uses a nonpositive sequence",
		},
		{
			name: "recent resources",
			insert: func(t *testing.T, connection *gorm.DB, nonpositive int) {
				t.Helper()
				if err := connection.Exec(`INSERT INTO recent_resources (project_id, resource_id, sequence)
					VALUES ('project-a', 'resource-positive', 1), ('project-a', 'resource-nonpositive', ?)`, nonpositive).Error; err != nil {
					t.Fatalf("insert recency bounds: %v", err)
				}
			},
			want: "recent resource sequence minimum uses a nonpositive sequence",
		},
		{
			name: "operations",
			insert: func(t *testing.T, connection *gorm.DB, nonpositive int) {
				t.Helper()
				at := operationJournalTestTime()
				if err := connection.Exec(`INSERT INTO operations
					(id, intent_id, kind, state, phase, requested_at, revision)
					VALUES ('operation-positive', 'intent-positive', 'project.start', 'queued', 'queued', ?, 1),
					       ('operation-nonpositive', 'intent-nonpositive', 'project.start', 'queued', 'queued', ?, ?)`, at, at, nonpositive).Error; err != nil {
					t.Fatalf("insert operation bounds: %v", err)
				}
			},
			want: "operation revision minimum uses a nonpositive sequence",
		},
		{
			name: "operation transitions",
			insert: func(t *testing.T, connection *gorm.DB, nonpositive int) {
				t.Helper()
				at := operationJournalTestTime()
				if err := connection.Exec(`INSERT INTO operation_transitions
					(operation_id, ordinal, state, phase, occurred_at, sequence)
					VALUES ('operation-positive', 1, 'queued', 'queued', ?, 1),
					       ('operation-nonpositive', 1, 'queued', 'queued', ?, ?)`, at, at, nonpositive).Error; err != nil {
					t.Fatalf("insert transition bounds: %v", err)
				}
			},
			want: "operation transition sequence minimum uses a nonpositive sequence",
		},
	}
	for _, test := range tests {
		for _, nonpositive := range []int{0, -1} {
			boundName := "zero"
			if nonpositive < 0 {
				boundName = "negative"
			}
			t.Run(test.name+"/"+boundName, func(t *testing.T) {
				_, connection := newOperationJournalTestHarness(t)
				if err := connection.Exec("UPDATE harbor_state SET sequence = 1 WHERE id = 1").Error; err != nil {
					t.Fatalf("set sequence high-water: %v", err)
				}
				test.insert(t, connection, nonpositive)
				if _, err := validateRetainedSequenceBounds(connection); err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("mixed bound error = %v, want %q", err, test.want)
				}
			})
		}
	}
}

// TestValidateRetainedSequenceBoundsRejectsNullOwner verifies the indexed minimum exposes NULL values without scanning retained rows.
func TestValidateRetainedSequenceBoundsRejectsNullOwner(t *testing.T) {
	_, connection := newOperationJournalTestHarness(t)
	if err := connection.Exec("ALTER TABLE projects RENAME TO projects_strict").Error; err != nil {
		t.Fatalf("rename projects table: %v", err)
	}
	if err := connection.Exec(`CREATE TABLE projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		revision INTEGER
	)`).Error; err != nil {
		t.Fatalf("create nullable projects table: %v", err)
	}
	if err := connection.Exec("INSERT INTO projects (project_id, revision) VALUES ('project-positive', 1), ('project-null', NULL)").Error; err != nil {
		t.Fatalf("insert nullable project bounds: %v", err)
	}
	if err := connection.Exec("UPDATE harbor_state SET sequence = 1 WHERE id = 1").Error; err != nil {
		t.Fatalf("set sequence high-water: %v", err)
	}
	if _, err := validateRetainedSequenceBounds(connection); err == nil || !strings.Contains(err.Error(), "project revision") || !strings.Contains(err.Error(), "must not be NULL") {
		t.Fatalf("NULL retained owner error = %v", err)
	}
}

// TestValidateRetainedSequenceBoundsUsesIndexedExtrema verifies retained table size cannot turn preflight into a full-history scan.
func TestValidateRetainedSequenceBoundsUsesIndexedExtrema(t *testing.T) {
	_, connection := newOperationJournalTestHarness(t)
	const callback = "harbor:test_retained_bounds_queries"
	queries := make([]string, 0, 8)
	if err := connection.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		query := strings.ToLower(tx.Statement.SQL.String())
		if strings.Contains(query, " as value") {
			queries = append(queries, query)
		}
	}); err != nil {
		t.Fatalf("register retained bounds query observer: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Callback().Query().Remove(callback)
	})

	if _, err := validateRetainedSequenceBounds(connection); err != nil {
		t.Fatalf("validate clean retained bounds: %v", err)
	}
	if len(queries) != 8 {
		t.Fatalf("retained bounds query count = %d, want 8: %#v", len(queries), queries)
	}
	for index, query := range queries {
		for _, fragment := range []string{" as value", "order by", "limit 1"} {
			if !strings.Contains(query, fragment) {
				t.Fatalf("retained bounds query %d = %q, missing %q", index, query, fragment)
			}
		}
		for _, scan := range []string{"count(", "min(", "max("} {
			if strings.Contains(query, scan) {
				t.Fatalf("retained bounds query %d = %q, contains full-history aggregate %q", index, query, scan)
			}
		}
	}
}

// newOperationJournalTestHarness constructs generated repositories over an already-migrated temporary named SQLite database.
func newOperationJournalTestHarness(t *testing.T) (*OperationJournal, *gorm.DB) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "harbor.db")
	t.Setenv("DB_HARBORD_DRIVER", "sqlite")
	t.Setenv("DB_HARBORD_DSN", databasePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	t.Setenv("DB_HARBORD_MAX_OPEN_CONNECTIONS", "1")
	t.Setenv("DB_HARBORD_MAX_IDLE_CONNECTIONS", "1")

	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	connection, err := connections.GetHarbord()
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	for _, statement := range operationJournalTestSchema {
		if err := connection.Exec(statement).Error; err != nil {
			t.Fatalf("apply test schema statement: %v", err)
		}
	}

	return NewOperationJournal(
		connections,
		models.NewOperationRepo(connections),
		models.NewOperationTransitionRepo(connections),
		models.NewHarborStateRepo(connections),
		NewMutationCoordinator(connections),
	), connection
}

// newOperationJournalTestOperation creates one valid queued domain operation or fails its test immediately.
func newOperationJournalTestOperation(
	t *testing.T,
	operationID domain.OperationID,
	intentID domain.IntentID,
	projectID domain.ProjectID,
	kind domain.OperationKind,
	requestedAt time.Time,
) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation(operationID, intentID, kind, projectID, requestedAt)
	if err != nil {
		t.Fatalf("create test operation: %v", err)
	}
	return operation
}

// mustOperationJournalTransition advances an operation or fails its test immediately.
func mustOperationJournalTransition(
	t *testing.T,
	journal *OperationJournal,
	record OperationRecord,
	next domain.OperationState,
	phase string,
	at time.Time,
	problem *domain.Problem,
) OperationRecord {
	t.Helper()
	nextRecord, err := journal.Transition(context.Background(), record.Operation.ID, record.Revision, next, phase, at, problem)
	if err != nil {
		t.Fatalf("transition operation to %s: %v", next, err)
	}
	return nextRecord
}

// mustOperationJournalSequence reads the global sequence or fails its test immediately.
func mustOperationJournalSequence(t *testing.T, journal *OperationJournal) domain.Sequence {
	t.Helper()
	sequence, err := journal.CurrentSequence(context.Background())
	if err != nil {
		t.Fatalf("read Harbor sequence: %v", err)
	}
	return sequence
}

// operationJournalTestTime returns a stable UTC instant without a monotonic component.
func operationJournalTestTime() time.Time {
	return time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC)
}

// assertCorruptStateError requires a typed corruption boundary for an expected entity.
func assertCorruptStateError(t *testing.T, err error, entity string) {
	t.Helper()
	var corrupt *CorruptStateError
	if !errors.As(err, &corrupt) {
		t.Fatalf("error = %v, want CorruptStateError", err)
	}
	if corrupt.Entity != entity {
		t.Fatalf("corrupt entity = %q, want %q", corrupt.Entity, entity)
	}
}
