package reconcile

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/state"
)

// newProjectUnregisterStartFixture removes the pre-staged approval boundary and selects an optional durable network claim.
func newProjectUnregisterStartFixture(
	t *testing.T,
	initialized bool,
	claimed bool,
) *projectUnregisterFixture {
	t.Helper()
	fixture := newProjectUnregisterFixture(t)
	fixture.state.active = []state.OperationRecord{}
	fixture.state.journalRecords = nil
	fixture.state.releases = make(map[domain.OperationID]state.ProjectNetworkReleaseRecord)
	fixture.state.runtime = state.RuntimeState{NetworkInitialized: initialized}
	if initialized {
		fixture.state.runtime.Network = state.NetworkRecord{
			Revision:    30,
			CreatedAt:   fixture.now.Add(-time.Hour),
			UpdatedAt:   fixture.now.Add(-5 * time.Minute),
			Ownership:   fixture.leases[0].Ownership,
			Leases:      []identity.Lease{},
			Quarantines: []identity.Quarantine{},
			Reservations: state.DataPlaneReservations{
				Endpoints:            []state.EndpointReservation{},
				SuppressedProjectIDs: []domain.ProjectID{},
			},
		}
		if claimed {
			fixture.state.runtime.Network.Leases = slices.Clone(fixture.leases)
		}
	}
	return fixture
}

// projectUnregisterStartRequest returns one valid daemon-side initiation identity set.
func projectUnregisterStartRequest(projectID domain.ProjectID) StartRequest {
	return StartRequest{
		ProjectID:   projectID,
		OperationID: "operation-start-unregister",
		IntentID:    "intent-start-unregister",
	}
}

// TestProjectUnregisterStartCompletesPendingProjectsWithoutNetworkEffects covers uninitialized and initialized claimless deletion.
func TestProjectUnregisterStartCompletesPendingProjectsWithoutNetworkEffects(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "uninitialized", true: "initialized"}[initialized], func(t *testing.T) {
			fixture := newProjectUnregisterStartFixture(t, initialized, false)
			request := projectUnregisterStartRequest(fixture.projectID)

			completed, err := fixture.coordinator.Start(context.Background(), request)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if completed.Operation.State != domain.OperationSucceeded || completed.Operation.ID != request.OperationID || completed.Operation.IntentID != request.IntentID {
				t.Fatalf("Start() operation = %#v", completed)
			}
			snapshot := fixture.state.snapshot()
			if len(snapshot.enqueueCalls) != 1 || len(snapshot.transitionCalls) != 1 || len(snapshot.beginCalls) != 0 || len(snapshot.stageCalls) != 0 || len(snapshot.completeNetworkCalls) != 0 || len(snapshot.completeProjectCalls) != 1 || len(snapshot.active) != 0 {
				t.Fatalf("Start() mutations = %#v", snapshot)
			}
			if snapshot.transitionCalls[0].next != domain.OperationRunning || snapshot.transitionCalls[0].phase != projectUnregisterStartPhase {
				t.Fatalf("Start() transition = %#v", snapshot.transitionCalls[0])
			}
			if withdrawals := fixture.withdrawal.callSnapshot(); len(withdrawals) != 0 {
				t.Fatalf("claimless Start() withdrawal calls = %#v", withdrawals)
			}
			if observations, _ := fixture.observer.callSnapshot(); len(observations) != 0 {
				t.Fatalf("claimless Start() observations = %#v", observations)
			}
			if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
				t.Fatalf("claimless Start() opened %d issuers", openCalls)
			}

			retry := request
			retry.OperationID = "operation-retry-unregister"
			projectCallsBeforeReplay := snapshot.projectCalls
			fixture.state.projectError = errors.New("project was removed")
			replayed, err := fixture.coordinator.Start(context.Background(), retry)
			if err != nil || !reflect.DeepEqual(replayed, completed) {
				t.Fatalf("replayed Start() = %#v, %v; want %#v", replayed, err, completed)
			}
			replayedSnapshot := fixture.state.snapshot()
			if len(replayedSnapshot.intentCalls) != 2 || len(replayedSnapshot.enqueueCalls) != 2 || len(replayedSnapshot.transitionCalls) != 1 || len(replayedSnapshot.completeProjectCalls) != 1 || replayedSnapshot.projectCalls != projectCallsBeforeReplay {
				t.Fatalf("replayed Start() mutations = %#v", replayedSnapshot)
			}
		})
	}
}

// TestProjectUnregisterStartStagesClaimedProjectApproval verifies initiation suppresses routes before returning interactive work.
func TestProjectUnregisterStartStagesClaimedProjectApproval(t *testing.T) {
	fixture := newProjectUnregisterStartFixture(t, true, true)
	request := projectUnregisterStartRequest(fixture.projectID)

	approval, err := fixture.coordinator.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if approval.Operation.State != domain.OperationRequiresApproval || approval.Operation.ID != request.OperationID {
		t.Fatalf("Start() operation = %#v", approval)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.beginCalls) != 1 || len(snapshot.stageCalls) != 1 || len(snapshot.completeNetworkCalls) != 0 || len(snapshot.completeProjectCalls) != 0 {
		t.Fatalf("claimed Start() mutations = %#v", snapshot)
	}
	begin := snapshot.beginCalls[0]
	if begin.ProjectID != request.ProjectID || begin.OperationID != request.OperationID || begin.ExpectedNetworkRevision != 30 || begin.ExpectedProjectRevision != fixture.state.project.Revision || begin.BeginGeneration != 8 {
		t.Fatalf("BeginProjectNetworkRelease() request = %#v", begin)
	}
	if len(fixture.withdrawal.callSnapshot()) != 1 {
		t.Fatalf("claimed Start() withdrawal calls = %#v", fixture.withdrawal.callSnapshot())
	}
	if observations, _ := fixture.observer.callSnapshot(); len(observations) != len(fixture.leases) {
		t.Fatalf("claimed Start() observations = %#v", observations)
	}
	if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
		t.Fatalf("claimed Start() opened %d issuers", openCalls)
	}

	replayed, err := fixture.coordinator.Start(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed, approval) {
		t.Fatalf("approval replay = %#v, %v; want %#v", replayed, err, approval)
	}
	replayedSnapshot := fixture.state.snapshot()
	if len(replayedSnapshot.enqueueCalls) != 2 || len(replayedSnapshot.beginCalls) != 1 || len(replayedSnapshot.stageCalls) != 1 {
		t.Fatalf("approval replay repeated mutations = %#v", replayedSnapshot)
	}
}

// TestProjectUnregisterStartRevalidatesJournalHistoryOnReplay proves the lookup shortcut cannot bypass Enqueue's retained-history checks.
func TestProjectUnregisterStartRevalidatesJournalHistoryOnReplay(t *testing.T) {
	fixture := newProjectUnregisterStartFixture(t, false, false)
	request := projectUnregisterStartRequest(fixture.projectID)
	completed, err := fixture.coordinator.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("initial Start() error = %v", err)
	}
	corrupt := &state.CorruptStateError{Entity: "operation history", Key: string(completed.Operation.ID), Cause: errors.New("retained transition is missing")}
	fixture.state.enqueueError = corrupt

	_, err = fixture.coordinator.Start(context.Background(), request)
	var typed *state.CorruptStateError
	if !errors.As(err, &typed) {
		t.Fatalf("replayed Start() error = %v, want CorruptStateError", err)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.intentCalls) != 2 || len(snapshot.enqueueCalls) != 2 || len(snapshot.transitionCalls) != 1 || len(snapshot.completeProjectCalls) != 1 {
		t.Fatalf("corrupt replay mutations = %#v", snapshot)
	}
}

// TestProjectUnregisterStartCompletesClaimedProjectWhenHostEffectsAreAbsent verifies no helper approval is invented after suppression.
func TestProjectUnregisterStartCompletesClaimedProjectWhenHostEffectsAreAbsent(t *testing.T) {
	fixture := newProjectUnregisterStartFixture(t, true, true)
	fixture.setAllObservations(loopback.StateAbsent)
	request := projectUnregisterStartRequest(fixture.projectID)

	completed, err := fixture.coordinator.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if completed.Operation.State != domain.OperationSucceeded {
		t.Fatalf("Start() operation = %#v", completed)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.beginCalls) != 1 || len(snapshot.stageCalls) != 0 || len(snapshot.completeNetworkCalls) != 1 || len(snapshot.completeProjectCalls) != 1 || len(snapshot.active) != 0 {
		t.Fatalf("absent-effect Start() mutations = %#v", snapshot)
	}
	if len(fixture.withdrawal.callSnapshot()) != 2 {
		t.Fatalf("absent-effect withdrawal calls = %#v", fixture.withdrawal.callSnapshot())
	}
	if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
		t.Fatalf("absent-effect Start() opened %d issuers", openCalls)
	}
}

// TestProjectUnregisterStartRejectsInvalidUnknownAndBusyRequestsBeforeEnqueue verifies bad initiation cannot poison recovery.
func TestProjectUnregisterStartRejectsInvalidUnknownAndBusyRequestsBeforeEnqueue(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*StartRequest)
	}{
		{name: "project", mutate: func(request *StartRequest) { request.ProjectID = " bad " }},
		{name: "operation", mutate: func(request *StartRequest) { request.OperationID = " bad " }},
		{name: "intent", mutate: func(request *StartRequest) { request.IntentID = " bad " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterStartFixture(t, false, false)
			request := projectUnregisterStartRequest(fixture.projectID)
			test.mutate(&request)
			if _, err := fixture.coordinator.Start(context.Background(), request); err == nil {
				t.Fatal("Start() error = nil, want validation failure")
			}
			if snapshot := fixture.state.snapshot(); len(snapshot.intentCalls) != 0 || len(snapshot.enqueueCalls) != 0 {
				t.Fatalf("invalid Start() reached journal = %#v", snapshot)
			}
		})
	}

	t.Run("unknown project", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		fixture.state.projectError = &state.ProjectNotFoundError{ProjectID: fixture.projectID}
		_, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID))
		var missing *state.ProjectNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("Start() error = %v, want ProjectNotFoundError", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.intentCalls) != 1 || len(snapshot.enqueueCalls) != 0 || len(snapshot.transitionCalls) != 0 {
			t.Fatalf("unknown-project Start() journal calls = %#v", snapshot)
		}
	})

	t.Run("busy project", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		first := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-z", domain.OperationQueued, 50)
		first.Operation.IntentID = "intent-z"
		second := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-a", domain.OperationRunning, 51)
		second.Operation.IntentID = "intent-a"
		fixture.state.active = []state.OperationRecord{first, second}
		fixture.state.journalRecords = nil

		_, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID))
		var busy *state.ProjectBusyError
		if !errors.As(err, &busy) || !reflect.DeepEqual(busy.OperationIDs, []domain.OperationID{"operation-a", "operation-z"}) {
			t.Fatalf("Start() error = %v, want sorted ProjectBusyError", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 0 || len(snapshot.transitionCalls) != 0 {
			t.Fatalf("busy Start() journal mutations = %#v", snapshot)
		}
	})
}

// TestProjectUnregisterStartFailsClosedAtReadAndJournalBoundaries verifies collaborator errors and mismatched readbacks cannot enqueue work.
func TestProjectUnregisterStartFailsClosedAtReadAndJournalBoundaries(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.coordinator.Start(ctx, projectUnregisterStartRequest(fixture.projectID)); !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want context cancellation", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.intentCalls) != 0 || len(snapshot.enqueueCalls) != 0 {
			t.Fatalf("cancelled Start() calls = %#v", snapshot)
		}
	})

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "intent read", err: errors.New("intent reader unavailable")},
		{name: "corrupt intent", err: &state.CorruptStateError{Entity: "operation", Key: "operation-corrupt", Cause: errors.New("invalid row")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterStartFixture(t, false, false)
			fixture.state.intentError = test.err
			if _, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID)); !errors.Is(err, test.err) {
				t.Fatalf("Start() error = %v, want %v", err, test.err)
			}
			if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 0 || snapshot.projectCalls != 0 {
				t.Fatalf("failed intent read reached preflight = %#v", snapshot)
			}
		})
	}

	t.Run("intent readback", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		request := projectUnregisterStartRequest(fixture.projectID)
		mismatch := projectUnregisterTestOperation(fixture.now, fixture.projectID, request.OperationID, domain.OperationRunning, 40)
		mismatch.Operation.IntentID = "intent-other"
		fixture.state.intentReadback = &mismatch
		_, err := fixture.coordinator.Start(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "intent readback differs") {
			t.Fatalf("Start() error = %v, want intent readback mismatch", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 0 || snapshot.projectCalls != 0 {
			t.Fatalf("mismatched intent readback mutations = %#v", snapshot)
		}
	})

	t.Run("intent conflict", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		request := projectUnregisterStartRequest(fixture.projectID)
		conflict := projectUnregisterTestOperation(fixture.now, "project-other", "operation-other", domain.OperationRunning, 40)
		conflict.Operation.IntentID = request.IntentID
		fixture.state.intentReadback = &conflict
		_, err := fixture.coordinator.Start(context.Background(), request)
		var typed *state.IntentConflictError
		if !errors.As(err, &typed) {
			t.Fatalf("Start() error = %v, want IntentConflictError", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 0 || snapshot.projectCalls != 0 {
			t.Fatalf("conflicting intent mutations = %#v", snapshot)
		}
	})

	t.Run("project readback", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		mismatch := fixture.state.project
		mismatch.Project.ID = "project-other"
		fixture.state.projectReadback = &mismatch
		_, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID))
		if err == nil || !strings.Contains(err.Error(), "project readback differs") {
			t.Fatalf("Start() error = %v, want project readback mismatch", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 0 || snapshot.activeCalls != 0 {
			t.Fatalf("mismatched project readback mutations = %#v", snapshot)
		}
	})

	t.Run("active read", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		failure := errors.New("active operation reader unavailable")
		fixture.state.activeError = failure
		if _, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID)); !errors.Is(err, failure) {
			t.Fatalf("Start() error = %v, want %v", err, failure)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 0 {
			t.Fatalf("failed active read mutations = %#v", snapshot)
		}
	})

	t.Run("enqueue", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		failure := errors.New("operation writer unavailable")
		fixture.state.enqueueError = failure
		if _, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID)); !errors.Is(err, failure) {
			t.Fatalf("Start() error = %v, want %v", err, failure)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 1 || len(snapshot.transitionCalls) != 0 {
			t.Fatalf("failed enqueue mutations = %#v", snapshot)
		}
	})
}

// TestProjectUnregisterStartRejectsMismatchedEnqueueReadbacks binds initiation to the journal's exact durable identity.
func TestProjectUnregisterStartRejectsMismatchedEnqueueReadbacks(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.OperationRecord)
	}{
		{name: "operation", mutate: func(record *state.OperationRecord) { record.Operation.ID = "operation-other" }},
		{name: "intent", mutate: func(record *state.OperationRecord) { record.Operation.IntentID = "intent-other" }},
		{name: "kind", mutate: func(record *state.OperationRecord) { record.Operation.Kind = "project.register" }},
		{name: "project", mutate: func(record *state.OperationRecord) { record.Operation.ProjectID = "project-other" }},
		{name: "revision", mutate: func(record *state.OperationRecord) { record.Revision = 0 }},
		{name: "fabricated terminal state", mutate: func(record *state.OperationRecord) {
			startedAt := record.Operation.RequestedAt.Add(time.Second)
			finishedAt := startedAt.Add(time.Second)
			record.Operation.State = domain.OperationSucceeded
			record.Operation.Phase = "project unregistered"
			record.Operation.StartedAt = &startedAt
			record.Operation.FinishedAt = &finishedAt
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterStartFixture(t, false, false)
			request := projectUnregisterStartRequest(fixture.projectID)
			candidate, err := domain.NewOperation(
				request.OperationID,
				request.IntentID,
				domain.OperationKindProjectUnregister,
				request.ProjectID,
				fixture.now,
			)
			if err != nil {
				t.Fatalf("new operation fixture: %v", err)
			}
			readback := state.OperationRecord{Operation: candidate, Revision: 40}
			test.mutate(&readback)
			fixture.state.enqueueReadback = &readback

			_, err = fixture.coordinator.Start(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), "enqueue readback") {
				t.Fatalf("Start() error = %v, want enqueue readback mismatch", err)
			}
			if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 1 || len(snapshot.transitionCalls) != 0 {
				t.Fatalf("mismatched Enqueue readback advanced operation = %#v", snapshot)
			}
		})
	}

	t.Run("existing intent operation", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		request := projectUnregisterStartRequest(fixture.projectID)
		prior := projectUnregisterTestOperation(fixture.now, request.ProjectID, "operation-prior", domain.OperationQueued, 40)
		prior.Operation.IntentID = request.IntentID
		fixture.state.intentReadback = &prior
		mismatch := prior
		mismatch.Operation.ID = request.OperationID
		fixture.state.enqueueReadback = &mismatch

		_, err := fixture.coordinator.Start(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "enqueue readback") {
			t.Fatalf("replayed Start() error = %v, want prior operation identity mismatch", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 1 || len(snapshot.transitionCalls) != 0 || snapshot.projectCalls != 0 {
			t.Fatalf("mismatched replay readback advanced operation = %#v", snapshot)
		}
	})

	t.Run("existing intent regression", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		request := projectUnregisterStartRequest(fixture.projectID)
		prior := projectUnregisterTestOperation(fixture.now, request.ProjectID, "operation-prior", domain.OperationQueued, 40)
		prior.Operation.IntentID = request.IntentID
		fixture.state.intentReadback = &prior
		regressed := prior
		regressed.Revision--
		fixture.state.enqueueReadback = &regressed

		_, err := fixture.coordinator.Start(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "enqueue readback") {
			t.Fatalf("replayed Start() error = %v, want replay regression", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.enqueueCalls) != 1 || len(snapshot.transitionCalls) != 0 || snapshot.projectCalls != 0 {
			t.Fatalf("regressed replay advanced operation = %#v", snapshot)
		}
	})
}

// TestValidateProjectUnregisterOperationReadbackRejectsInvalidExactRecords covers malformed journal results after exact correlation.
func TestValidateProjectUnregisterOperationReadbackRejectsInvalidExactRecords(t *testing.T) {
	operation, err := domain.NewOperation(
		"operation-readback",
		"intent-readback",
		domain.OperationKindProjectUnregister,
		"project-readback",
		time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("new operation fixture: %v", err)
	}
	invalidOperation := operation
	invalidOperation.Phase = ""
	if err := validateProjectUnregisterOperationReadback(
		state.OperationRecord{Operation: invalidOperation, Revision: 1},
		invalidOperation,
		nil,
	); err == nil || !strings.Contains(err.Error(), "operation is invalid") {
		t.Fatalf("invalid operation readback error = %v", err)
	}
	if err := validateProjectUnregisterOperationReadback(
		state.OperationRecord{Operation: operation},
		operation,
		nil,
	); err == nil || !strings.Contains(err.Error(), "revision is invalid") {
		t.Fatalf("invalid revision readback error = %v", err)
	}
}

// TestProjectUnregisterAdvanceQueuedRejectsMismatchedTransitionReadbacks proves a collaborator cannot redirect recovery after transition.
func TestProjectUnregisterAdvanceQueuedRejectsMismatchedTransitionReadbacks(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.OperationRecord, state.OperationRecord)
	}{
		{name: "operation", mutate: func(readback *state.OperationRecord, _ state.OperationRecord) {
			readback.Operation.ID = "operation-other"
		}},
		{name: "intent", mutate: func(readback *state.OperationRecord, _ state.OperationRecord) {
			readback.Operation.IntentID = "intent-other"
		}},
		{name: "kind", mutate: func(readback *state.OperationRecord, _ state.OperationRecord) {
			readback.Operation.Kind = "project.register"
		}},
		{name: "project", mutate: func(readback *state.OperationRecord, _ state.OperationRecord) {
			readback.Operation.ProjectID = "project-other"
		}},
		{name: "state", mutate: func(readback *state.OperationRecord, _ state.OperationRecord) {
			readback.Operation.State = domain.OperationQueued
		}},
		{name: "stale revision", mutate: func(readback *state.OperationRecord, queued state.OperationRecord) {
			readback.Revision = queued.Revision
		}},
		{name: "large revision", mutate: func(readback *state.OperationRecord, _ state.OperationRecord) {
			readback.Revision = domain.MaximumSequence + 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterStartFixture(t, false, false)
			queued := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-queued-readback", domain.OperationQueued, 60)
			expected, err := queued.Operation.Transition(domain.OperationRunning, projectUnregisterStartPhase, fixture.now, nil)
			if err != nil {
				t.Fatalf("transition fixture: %v", err)
			}
			readback := state.OperationRecord{Operation: expected, Revision: queued.Revision + 1}
			test.mutate(&readback, queued)
			fixture.state.transitionReadback = &readback

			_, err = fixture.coordinator.advanceQueued(context.Background(), queued)
			if err == nil || !strings.Contains(err.Error(), "started operation readback differs") {
				t.Fatalf("advanceQueued() error = %v, want transition readback mismatch", err)
			}
			if snapshot := fixture.state.snapshot(); len(snapshot.transitionCalls) != 1 || snapshot.runtimeCalls != 0 || len(snapshot.beginCalls) != 0 {
				t.Fatalf("mismatched Transition readback advanced release = %#v", snapshot)
			}
		})
	}
}

// TestProjectUnregisterAdvanceOperationReturnsTerminalRecordsAndRejectsInvalidKinds covers replay-only lifecycle branches.
func TestProjectUnregisterAdvanceOperationReturnsTerminalRecordsAndRejectsInvalidKinds(t *testing.T) {
	fixture := newProjectUnregisterStartFixture(t, false, false)
	running := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-terminal", domain.OperationRunning, 80)
	failedOperation, err := running.Operation.Transition(domain.OperationFailed, "unregister failed", fixture.now, &domain.Problem{
		Code:    "unregister_failed",
		Message: "Unregister failed.",
	})
	if err != nil {
		t.Fatalf("failed operation fixture: %v", err)
	}
	cancelledOperation, err := running.Operation.Transition(domain.OperationCancelled, "unregister cancelled", fixture.now, nil)
	if err != nil {
		t.Fatalf("cancelled operation fixture: %v", err)
	}
	for _, operation := range []domain.Operation{failedOperation, cancelledOperation} {
		record := state.OperationRecord{Operation: operation, Revision: running.Revision + 1}
		got, err := fixture.coordinator.advanceOperation(context.Background(), record)
		if err != nil || !reflect.DeepEqual(got, record) {
			t.Fatalf("advanceOperation(%q) = %#v, %v; want %#v", operation.State, got, err, record)
		}
	}

	wrongKind := running
	wrongKind.Operation.Kind = "project.register"
	if _, err := fixture.coordinator.advanceOperation(context.Background(), wrongKind); err == nil || !strings.Contains(err.Error(), "not \"project.unregister\"") {
		t.Fatalf("advanceOperation() wrong-kind error = %v", err)
	}
	unsupported := running
	unsupported.Operation.State = "waiting"
	if _, err := fixture.coordinator.advanceOperation(context.Background(), unsupported); err == nil || !strings.Contains(err.Error(), "unsupported state") {
		t.Fatalf("advanceOperation() unsupported-state error = %v", err)
	}
}

// TestProjectUnregisterStartAndAdvanceFailClosedOnDurableReadbackErrors covers every pre-release collaborator boundary.
func TestProjectUnregisterStartAndAdvanceFailClosedOnDurableReadbackErrors(t *testing.T) {
	t.Run("operation time", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		coordinator := NewProjectUnregisterCoordinator(
			fixture.state,
			fixture.state,
			fixture.plans,
			fixture.observer,
			fixture.withdrawal,
			fixture.issuers.Open,
			projectUnregisterTestClock{},
		)
		if _, err := coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID)); err == nil || !strings.Contains(err.Error(), "requested time must not be zero") {
			t.Fatalf("Start() error = %v, want invalid operation time", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.intentCalls) != 0 || len(snapshot.enqueueCalls) != 0 {
			t.Fatalf("invalid operation time reached journal = %#v", snapshot)
		}
	})

	t.Run("queued project read", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		failure := errors.New("project reader unavailable")
		fixture.state.projectError = failure
		queued := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-queued", domain.OperationQueued, 60)
		if _, err := fixture.coordinator.advanceQueued(context.Background(), queued); !errors.Is(err, failure) {
			t.Fatalf("advanceQueued() error = %v, want %v", err, failure)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.transitionCalls) != 0 {
			t.Fatalf("failed queued read transitioned operation = %#v", snapshot)
		}
	})

	t.Run("queued project readback", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		mismatch := fixture.state.project
		mismatch.Project.ID = "project-other"
		fixture.state.projectReadback = &mismatch
		queued := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-queued", domain.OperationQueued, 60)
		if _, err := fixture.coordinator.advanceQueued(context.Background(), queued); err == nil || !strings.Contains(err.Error(), "readback differs") {
			t.Fatalf("advanceQueued() error = %v, want readback mismatch", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.transitionCalls) != 0 {
			t.Fatalf("mismatched queued read transitioned operation = %#v", snapshot)
		}
	})

	t.Run("invalid queued operation", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		queued := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-invalid-queued", domain.OperationQueued, 60)
		queued.Operation.Phase = ""
		if _, err := fixture.coordinator.advanceQueued(context.Background(), queued); err == nil || !strings.Contains(err.Error(), "derive queued operation transition") {
			t.Fatalf("advanceQueued() error = %v, want invalid transition source", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.transitionCalls) != 0 {
			t.Fatalf("invalid queued operation reached Transition = %#v", snapshot)
		}
	})

	for _, test := range []struct {
		name      string
		configure func(*projectUnregisterFixture, error)
	}{
		{name: "runtime read", configure: func(fixture *projectUnregisterFixture, failure error) { fixture.state.runtimeError = failure }},
		{name: "project read", configure: func(fixture *projectUnregisterFixture, failure error) { fixture.state.projectError = failure }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectUnregisterStartFixture(t, true, true)
			failure := errors.New(test.name + " unavailable")
			test.configure(fixture, failure)
			running := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-running", domain.OperationRunning, 60)
			if _, err := fixture.coordinator.beginRunning(context.Background(), running); !errors.Is(err, failure) {
				t.Fatalf("beginRunning() error = %v, want %v", err, failure)
			}
			if snapshot := fixture.state.snapshot(); len(snapshot.beginCalls) != 0 || len(snapshot.completeProjectCalls) != 0 {
				t.Fatalf("failed running read mutated state = %#v", snapshot)
			}
		})
	}

	t.Run("running project readback", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, true, true)
		mismatch := fixture.state.project
		mismatch.Project.ID = "project-other"
		fixture.state.projectReadback = &mismatch
		running := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-running", domain.OperationRunning, 60)
		if _, err := fixture.coordinator.beginRunning(context.Background(), running); err == nil || !strings.Contains(err.Error(), "readback differs") {
			t.Fatalf("beginRunning() error = %v, want readback mismatch", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.beginCalls) != 0 || len(snapshot.completeProjectCalls) != 0 {
			t.Fatalf("mismatched running read mutated state = %#v", snapshot)
		}
	})

	t.Run("generation exhaustion", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, true, true)
		fixture.state.runtime.Network.Ownership.Generation = maximumPersistedNetworkGeneration
		running := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-running", domain.OperationRunning, 60)
		if _, err := fixture.coordinator.beginRunning(context.Background(), running); err == nil || !strings.Contains(err.Error(), "generation is exhausted") {
			t.Fatalf("beginRunning() error = %v, want generation exhaustion", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.beginCalls) != 0 {
			t.Fatalf("exhausted generation reached Begin = %#v", snapshot)
		}
	})

	t.Run("begin readback", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, true, true)
		fixture.state.beginReadback = &state.ProjectNetworkReleaseMutationResult{Release: state.ProjectNetworkReleaseRecord{
			ProjectID:   "project-other",
			OperationID: "operation-other",
		}}
		running := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-running", domain.OperationRunning, 60)
		if _, err := fixture.coordinator.beginRunning(context.Background(), running); err == nil || !strings.Contains(err.Error(), "release differs") {
			t.Fatalf("beginRunning() error = %v, want release readback mismatch", err)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.beginCalls) != 1 || len(snapshot.stageCalls) != 0 {
			t.Fatalf("mismatched Begin readback advanced release = %#v", snapshot)
		}
	})

	t.Run("pending completion", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		failure := errors.New("project completion unavailable")
		fixture.state.completeProjectError = failure
		running := projectUnregisterTestOperation(fixture.now, fixture.projectID, "operation-running", domain.OperationRunning, 60)
		if _, err := fixture.coordinator.beginRunning(context.Background(), running); !errors.Is(err, failure) {
			t.Fatalf("beginRunning() error = %v, want %v", err, failure)
		}
		if snapshot := fixture.state.snapshot(); len(snapshot.completeProjectCalls) != 1 {
			t.Fatalf("failed completion calls = %#v", snapshot)
		}
	})
}

// TestProjectUnregisterNetworkClaimAndGenerationHelpersCoverEveryAuthoritySource verifies initiation derives decisions from the full aggregate.
func TestProjectUnregisterNetworkClaimAndGenerationHelpersCoverEveryAuthoritySource(t *testing.T) {
	projectID := domain.ProjectID("project-generation")
	for _, test := range []struct {
		name   string
		record state.NetworkRecord
		want   bool
	}{
		{name: "none", record: state.NetworkRecord{}},
		{name: "lease", record: state.NetworkRecord{Leases: []identity.Lease{{Key: identity.LeaseKey{ProjectID: projectID}}}}, want: true},
		{name: "endpoint", record: state.NetworkRecord{Reservations: state.DataPlaneReservations{Endpoints: []state.EndpointReservation{{Key: state.EndpointReservationKey{ProjectID: projectID}}}}}, want: true},
		{name: "suppression", record: state.NetworkRecord{Reservations: state.DataPlaneReservations{SuppressedProjectIDs: []domain.ProjectID{projectID}}}, want: true},
		{name: "other project", record: state.NetworkRecord{
			Leases: []identity.Lease{{Key: identity.LeaseKey{ProjectID: "project-other"}}},
			Reservations: state.DataPlaneReservations{
				Endpoints:            []state.EndpointReservation{{Key: state.EndpointReservationKey{ProjectID: "project-other"}}},
				SuppressedProjectIDs: []domain.ProjectID{"project-other"},
			},
		}},
	} {
		t.Run("claims "+test.name, func(t *testing.T) {
			if got := projectHasRuntimeNetworkClaims(test.record, projectID); got != test.want {
				t.Fatalf("projectHasRuntimeNetworkClaims() = %t, want %t", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name   string
		record state.NetworkRecord
		want   uint64
	}{
		{name: "empty", want: 1},
		{name: "ownership", record: state.NetworkRecord{Ownership: identity.Ownership{Generation: 2}}, want: 3},
		{name: "lease", record: state.NetworkRecord{Leases: []identity.Lease{{Ownership: identity.Ownership{Generation: 4}}}}, want: 5},
		{name: "dns listener", record: state.NetworkRecord{Reservations: state.DataPlaneReservations{Listeners: state.SharedListenerReservations{DNS: state.ListenerReservation{Generation: 6}}}}, want: 7},
		{name: "http listener", record: state.NetworkRecord{Reservations: state.DataPlaneReservations{Listeners: state.SharedListenerReservations{HTTP: state.ListenerReservation{Generation: 8}}}}, want: 9},
		{name: "https listener", record: state.NetworkRecord{Reservations: state.DataPlaneReservations{Listeners: state.SharedListenerReservations{HTTPS: state.ListenerReservation{Generation: 10}}}}, want: 11},
		{name: "endpoint", record: state.NetworkRecord{Reservations: state.DataPlaneReservations{Endpoints: []state.EndpointReservation{{Generation: 12}}}}, want: 13},
		{name: "persisted ceiling", record: state.NetworkRecord{Ownership: identity.Ownership{Generation: maximumPersistedNetworkGeneration - 1}}, want: maximumPersistedNetworkGeneration},
	} {
		t.Run("generation "+test.name, func(t *testing.T) {
			got, err := nextProjectUnregisterBeginGeneration(test.record)
			if err != nil || got != test.want {
				t.Fatalf("nextProjectUnregisterBeginGeneration() = %d, %v; want %d", got, err, test.want)
			}
		})
	}
	exhausted := state.NetworkRecord{Ownership: identity.Ownership{Generation: maximumPersistedNetworkGeneration}}
	if _, err := nextProjectUnregisterBeginGeneration(exhausted); err == nil || !strings.Contains(err.Error(), "generation is exhausted") {
		t.Fatalf("nextProjectUnregisterBeginGeneration() exhaustion error = %v", err)
	}
}

// TestProjectUnregisterStartRecoversEveryPreApprovalCrashBoundary verifies queued, pre-Begin, and post-Begin retries converge.
func TestProjectUnregisterStartRecoversEveryPreApprovalCrashBoundary(t *testing.T) {
	t.Run("after enqueue", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, false, false)
		failure := errors.New("transition unavailable")
		fixture.state.transitionError = failure
		if _, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID)); !errors.Is(err, failure) {
			t.Fatalf("Start() error = %v, want %v", err, failure)
		}
		before := fixture.state.snapshot()
		if len(before.active) != 1 || before.active[0].Operation.State != domain.OperationQueued || len(before.enqueueCalls) != 1 {
			t.Fatalf("post-enqueue crash boundary = %#v", before)
		}
		fixture.state.transitionError = nil
		if err := fixture.coordinator.Recover(context.Background()); err != nil {
			t.Fatalf("Recover() error = %v", err)
		}
		after := fixture.state.snapshot()
		if len(after.transitionCalls) != 2 || len(after.completeProjectCalls) != 1 || len(after.active) != 0 {
			t.Fatalf("post-enqueue recovery = %#v", after)
		}
	})

	t.Run("before begin", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, true, true)
		failure := errors.New("network writer unavailable")
		fixture.state.beginError = failure
		if _, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID)); !errors.Is(err, failure) {
			t.Fatalf("Start() error = %v, want %v", err, failure)
		}
		before := fixture.state.snapshot()
		if len(before.active) != 1 || before.active[0].Operation.State != domain.OperationRunning || len(before.beginCalls) != 1 {
			t.Fatalf("pre-Begin crash boundary = %#v", before)
		}
		fixture.state.beginError = nil
		if err := fixture.coordinator.Recover(context.Background()); err != nil {
			t.Fatalf("Recover() error = %v", err)
		}
		after := fixture.state.snapshot()
		if len(after.beginCalls) != 2 || len(after.stageCalls) != 1 || len(after.active) != 1 || after.active[0].Operation.State != domain.OperationRequiresApproval {
			t.Fatalf("pre-Begin recovery = %#v", after)
		}
	})

	t.Run("after begin", func(t *testing.T) {
		fixture := newProjectUnregisterStartFixture(t, true, true)
		failure := errors.New("approval writer unavailable")
		fixture.state.stageError = failure
		if _, err := fixture.coordinator.Start(context.Background(), projectUnregisterStartRequest(fixture.projectID)); !errors.Is(err, failure) {
			t.Fatalf("Start() error = %v, want %v", err, failure)
		}
		before := fixture.state.snapshot()
		if len(before.beginCalls) != 1 || len(before.stageCalls) != 1 || len(before.active) != 1 || before.active[0].Operation.State != domain.OperationRunning {
			t.Fatalf("post-Begin crash boundary = %#v", before)
		}
		fixture.state.stageError = nil
		if err := fixture.coordinator.Recover(context.Background()); err != nil {
			t.Fatalf("Recover() error = %v", err)
		}
		after := fixture.state.snapshot()
		if len(after.beginCalls) != 1 || len(after.stageCalls) != 2 || len(after.active) != 1 || after.active[0].Operation.State != domain.OperationRequiresApproval {
			t.Fatalf("post-Begin recovery = %#v", after)
		}
	})
}
