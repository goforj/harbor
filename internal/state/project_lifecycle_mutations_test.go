package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// TestProjectLifecycleMutationsCommitStartAndStopWithExactReplay exercises the complete supervised process boundary.
func TestProjectLifecycleMutationsCommitStartAndStopWithExactReplay(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := emptyProjectStoreMutationProject("project-lifecycle")
	if _, err := store.PutProject(t.Context(), project); err != nil {
		t.Fatalf("PutProject() error = %v", err)
	}

	startQueued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStart, project.ID, "start")
	startAt := startQueued.Operation.RequestedAt.Add(time.Second)
	session := projectLifecycleTestPlannedSession(t, project.ID, startAt)
	beginRequest := BeginProjectStartRequest{
		ProjectID:                 project.ID,
		OperationID:               startQueued.Operation.ID,
		ExpectedOperationRevision: startQueued.Revision,
		Session:                   session,
		Phase:                     "launching forj dev",
		At:                        startAt,
	}
	begin, err := store.BeginProjectStart(t.Context(), beginRequest)
	if err != nil {
		t.Fatalf("BeginProjectStart() error = %v", err)
	}
	if begin.Operation.Operation.State != domain.OperationRunning || begin.Project.Project.State != domain.ProjectStarting || begin.Session == nil || !reflect.DeepEqual(*begin.Session, session) {
		t.Fatalf("BeginProjectStart() = %#v", begin)
	}
	sequenceAfterBegin := projectStoreMutationSequence(t, store)
	replayedBegin, err := store.BeginProjectStart(t.Context(), beginRequest)
	if err != nil || !reflect.DeepEqual(replayedBegin, begin) || projectStoreMutationSequence(t, store) != sequenceAfterBegin {
		t.Fatalf("BeginProjectStart(replay) = %#v, %v", replayedBegin, err)
	}

	process := projectLifecycleTestProcess(t)
	attachAt := startAt.Add(time.Second)
	attachRequest := AttachProjectProcessRequest{
		ProjectID:                 project.ID,
		SessionID:                 session.ID,
		ExpectedSessionGeneration: session.Generation,
		Process:                   process,
		At:                        attachAt,
	}
	attached, err := store.AttachProjectProcess(t.Context(), attachRequest)
	if err != nil {
		t.Fatalf("AttachProjectProcess() error = %v", err)
	}
	if attached.State != domain.SessionAwaitingAttach || attached.Generation != 2 || attached.Process == nil || *attached.Process != process {
		t.Fatalf("AttachProjectProcess() = %#v", attached)
	}
	replayedAttach, err := store.AttachProjectProcess(t.Context(), attachRequest)
	if err != nil || !reflect.DeepEqual(replayedAttach, attached) {
		t.Fatalf("AttachProjectProcess(replay) = %#v, %v", replayedAttach, err)
	}

	runtime := projectLifecycleTestRuntime()
	readyAt := attachAt.Add(time.Second)
	completeStartRequest := CompleteProjectStartRequest{
		ProjectID:                 project.ID,
		OperationID:               begin.Operation.Operation.ID,
		ExpectedOperationRevision: begin.Operation.Revision,
		SessionID:                 session.ID,
		ExpectedSessionGeneration: attached.Generation,
		Runtime:                   runtime,
		Phase:                     "default App ready",
		At:                        readyAt,
	}
	ready, err := store.CompleteProjectStart(t.Context(), completeStartRequest)
	if err != nil {
		t.Fatalf("CompleteProjectStart() error = %v", err)
	}
	if ready.Operation.Operation.State != domain.OperationSucceeded || !projectMatchesReadyRuntime(ready.Project.Project, runtime, readyAt) || ready.Session == nil || ready.Session.State != domain.SessionAwaitingAttach {
		t.Fatalf("CompleteProjectStart() = %#v", ready)
	}
	if snapshot, snapshotErr := store.Snapshot(t.Context()); snapshotErr != nil || len(snapshot.Projects) != 1 || snapshot.Projects[0].State != domain.ProjectReady {
		t.Fatalf("Snapshot() after ready = %#v, %v", snapshot, snapshotErr)
	}
	sequenceAfterReady := projectStoreMutationSequence(t, store)
	replayedReady, err := store.CompleteProjectStart(t.Context(), completeStartRequest)
	if err != nil || !reflect.DeepEqual(replayedReady, ready) || projectStoreMutationSequence(t, store) != sequenceAfterReady {
		t.Fatalf("CompleteProjectStart(replay) = %#v, %v", replayedReady, err)
	}

	stopQueued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStop, project.ID, "stop")
	stopAt := stopQueued.Operation.RequestedAt.Add(time.Second)
	beginStopRequest := BeginProjectStopRequest{
		ProjectID:                 project.ID,
		OperationID:               stopQueued.Operation.ID,
		ExpectedOperationRevision: stopQueued.Revision,
		SessionID:                 session.ID,
		ExpectedSessionGeneration: attached.Generation,
		Phase:                     "stopping forj dev",
		At:                        stopAt,
	}
	stopping, err := store.BeginProjectStop(t.Context(), beginStopRequest)
	if err != nil {
		t.Fatalf("BeginProjectStop() error = %v", err)
	}
	if stopping.Project.Project.State != domain.ProjectStopping || stopping.Session == nil || stopping.Session.State != domain.SessionStopping || stopping.Session.Generation != 3 {
		t.Fatalf("BeginProjectStop() = %#v", stopping)
	}
	sequenceAfterStopping := projectStoreMutationSequence(t, store)
	replayedStopping, err := store.BeginProjectStop(t.Context(), beginStopRequest)
	if err != nil || !reflect.DeepEqual(replayedStopping, stopping) || projectStoreMutationSequence(t, store) != sequenceAfterStopping {
		t.Fatalf("BeginProjectStop(replay) = %#v, %v", replayedStopping, err)
	}

	stoppedAt := stopAt.Add(time.Second)
	completeStopRequest := CompleteProjectStopRequest{
		ProjectID:                 project.ID,
		OperationID:               stopping.Operation.Operation.ID,
		ExpectedOperationRevision: stopping.Operation.Revision,
		Exit: ConfirmedProjectProcessExit{
			SessionID:                 session.ID,
			ExpectedSessionGeneration: stopping.Session.Generation,
			Process:                   &process,
			ExitedAt:                  stoppedAt,
		},
		Phase: "forj dev stopped",
	}
	stopped, err := store.CompleteProjectStop(t.Context(), completeStopRequest)
	if err != nil {
		t.Fatalf("CompleteProjectStop() error = %v", err)
	}
	if stopped.Operation.Operation.State != domain.OperationSucceeded || stopped.Session != nil || !projectMatchesInactiveState(stopped.Project.Project, domain.ProjectStopped, stoppedAt) {
		t.Fatalf("CompleteProjectStop() = %#v", stopped)
	}
	if snapshot, snapshotErr := store.Snapshot(t.Context()); snapshotErr != nil || len(snapshot.Projects) != 1 || snapshot.Projects[0].State != domain.ProjectStopped {
		t.Fatalf("Snapshot() after stop = %#v, %v", snapshot, snapshotErr)
	}
	sequenceAfterStopped := projectStoreMutationSequence(t, store)
	replayedStopped, err := store.CompleteProjectStop(t.Context(), completeStopRequest)
	if err != nil || !reflect.DeepEqual(replayedStopped, stopped) || projectStoreMutationSequence(t, store) != sequenceAfterStopped {
		t.Fatalf("CompleteProjectStop(replay) = %#v, %v", replayedStopped, err)
	}
}

// TestProjectLifecycleFailureAndUnexpectedExitRetireOnlyConfirmedProcessAuthority covers both abnormal process boundaries.
func TestProjectLifecycleFailureAndUnexpectedExitRetireOnlyConfirmedProcessAuthority(t *testing.T) {
	t.Run("start failure", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := emptyProjectStoreMutationProject("project-failed-start")
		if _, err := store.PutProject(t.Context(), project); err != nil {
			t.Fatalf("PutProject() error = %v", err)
		}
		queued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStart, project.ID, "failed-start")
		at := queued.Operation.RequestedAt.Add(time.Second)
		session := projectLifecycleTestPlannedSession(t, project.ID, at)
		running, err := store.BeginProjectStart(t.Context(), BeginProjectStartRequest{
			ProjectID: project.ID, OperationID: queued.Operation.ID, ExpectedOperationRevision: queued.Revision,
			Session: session, Phase: "launching forj dev", At: at,
		})
		if err != nil {
			t.Fatalf("BeginProjectStart() error = %v", err)
		}
		failure := FailProjectStartRequest{
			ProjectID: project.ID, OperationID: queued.Operation.ID, ExpectedOperationRevision: running.Operation.Revision,
			Exit:  ConfirmedProjectProcessExit{SessionID: session.ID, ExpectedSessionGeneration: 1, ExitedAt: at.Add(time.Second)},
			Phase: "forj dev launch failed", Problem: domain.Problem{Code: "launch_failed", Message: "GoForj development process could not start.", Retryable: true},
		}
		failed, err := store.FailProjectStart(t.Context(), failure)
		if err != nil {
			t.Fatalf("FailProjectStart() error = %v", err)
		}
		if failed.Operation.Operation.State != domain.OperationFailed || !projectMatchesInactiveState(failed.Project.Project, domain.ProjectFailed, failure.Exit.ExitedAt) {
			t.Fatalf("FailProjectStart() = %#v", failed)
		}
		sequence := projectStoreMutationSequence(t, store)
		replayed, err := store.FailProjectStart(t.Context(), failure)
		if err != nil || !reflect.DeepEqual(replayed, failed) || projectStoreMutationSequence(t, store) != sequence {
			t.Fatalf("FailProjectStart(replay) = %#v, %v", replayed, err)
		}
	})

	t.Run("unexpected ready exit", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project, session, process := projectLifecycleTestReadyProject(t, store, "project-unexpected")
		exitedAt := session.UpdatedAt.Add(time.Second)
		request := RecordUnexpectedProjectExitRequest{
			ProjectID: project.ID,
			Exit: ConfirmedProjectProcessExit{
				SessionID: session.ID, ExpectedSessionGeneration: session.Generation, Process: &process, ExitedAt: exitedAt,
			},
		}
		failed, err := store.RecordUnexpectedProjectExit(t.Context(), request)
		if err != nil {
			t.Fatalf("RecordUnexpectedProjectExit() error = %v", err)
		}
		if !projectMatchesInactiveState(failed.Project, domain.ProjectFailed, exitedAt) {
			t.Fatalf("RecordUnexpectedProjectExit() = %#v", failed)
		}
		sequence := projectStoreMutationSequence(t, store)
		replayed, err := store.RecordUnexpectedProjectExit(t.Context(), request)
		if err != nil || !reflect.DeepEqual(replayed, failed) || projectStoreMutationSequence(t, store) != sequence {
			t.Fatalf("RecordUnexpectedProjectExit(replay) = %#v, %v", replayed, err)
		}
	})
}

// TestProjectLifecycleRequestValidationRejectsEveryUnfencedBoundary covers request-only failures before writer admission.
func TestProjectLifecycleRequestValidationRejectsEveryUnfencedBoundary(t *testing.T) {
	at := time.Date(2026, 7, 19, 7, 0, 0, 0, time.UTC)
	projectID := domain.ProjectID("project-validation")
	session := projectLifecycleTestPlannedSession(t, projectID, at)
	process := projectLifecycleTestProcess(t)
	runtime := projectLifecycleTestRuntime()
	problem := domain.Problem{Code: "launch_failed", Message: "GoForj development process could not start.", Retryable: true}

	validBegin := BeginProjectStartRequest{
		ProjectID: projectID, OperationID: "operation-validation", ExpectedOperationRevision: 1,
		Session: session, Phase: "launching", At: at,
	}
	for _, test := range []struct {
		name   string
		mutate func(*BeginProjectStartRequest)
	}{
		{name: "project", mutate: func(request *BeginProjectStartRequest) { request.ProjectID = "" }},
		{name: "operation", mutate: func(request *BeginProjectStartRequest) { request.OperationID = "" }},
		{name: "revision", mutate: func(request *BeginProjectStartRequest) { request.ExpectedOperationRevision = 0 }},
		{name: "phase", mutate: func(request *BeginProjectStartRequest) { request.Phase = " " }},
		{name: "time", mutate: func(request *BeginProjectStartRequest) { request.At = time.Time{} }},
		{name: "session", mutate: func(request *BeginProjectStartRequest) { request.Session.ID = "" }},
		{name: "session project", mutate: func(request *BeginProjectStartRequest) { request.Session.ProjectID = "project-other" }},
		{name: "session owner", mutate: func(request *BeginProjectStartRequest) { request.Session.Owner = domain.SessionOwnerTerminal }},
		{name: "session state", mutate: func(request *BeginProjectStartRequest) {
			request.Session.State = domain.SessionAwaitingAttach
			request.Session.Process = &process
		}},
		{name: "session generation", mutate: func(request *BeginProjectStartRequest) { request.Session.Generation = 2 }},
		{name: "session timestamps", mutate: func(request *BeginProjectStartRequest) { request.Session.UpdatedAt = at.Add(time.Second) }},
	} {
		t.Run("begin start "+test.name, func(t *testing.T) {
			request := validBegin
			test.mutate(&request)
			if err := validateBeginProjectStartRequest(request); err == nil {
				t.Fatal("validateBeginProjectStartRequest() error = nil")
			}
		})
	}

	validAttach := AttachProjectProcessRequest{ProjectID: projectID, SessionID: session.ID, ExpectedSessionGeneration: 1, Process: process, At: at}
	for _, mutate := range []func(*AttachProjectProcessRequest){
		func(request *AttachProjectProcessRequest) { request.ProjectID = "" },
		func(request *AttachProjectProcessRequest) { request.SessionID = "" },
		func(request *AttachProjectProcessRequest) { request.ExpectedSessionGeneration = 0 },
		func(request *AttachProjectProcessRequest) { request.Process.PID = 0 },
		func(request *AttachProjectProcessRequest) { request.At = time.Time{} },
	} {
		request := validAttach
		mutate(&request)
		if err := validateAttachProjectProcessRequest(request); err == nil {
			t.Fatal("validateAttachProjectProcessRequest() error = nil")
		}
	}

	validCompleteStart := CompleteProjectStartRequest{
		ProjectID: projectID, OperationID: "operation-validation", ExpectedOperationRevision: 1,
		SessionID: session.ID, ExpectedSessionGeneration: 2, Runtime: runtime, Phase: "ready", At: at,
	}
	for _, mutate := range []func(*CompleteProjectStartRequest){
		func(request *CompleteProjectStartRequest) { request.ProjectID = "" },
		func(request *CompleteProjectStartRequest) { request.SessionID = "" },
		func(request *CompleteProjectStartRequest) { request.ExpectedSessionGeneration = 0 },
		func(request *CompleteProjectStartRequest) { request.Runtime.App.Active = false },
	} {
		request := validCompleteStart
		mutate(&request)
		if err := validateCompleteProjectStartRequest(request); err == nil {
			t.Fatal("validateCompleteProjectStartRequest() error = nil")
		}
	}

	for _, mutate := range []func(*DefaultProjectRuntime){
		func(candidate *DefaultProjectRuntime) { candidate.App.ID = "" },
		func(candidate *DefaultProjectRuntime) { candidate.App.Active = false },
		func(candidate *DefaultProjectRuntime) { candidate.Resource.ID = "" },
		func(candidate *DefaultProjectRuntime) { candidate.Resource.Owner.AppID = "other" },
	} {
		candidate := runtime
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("DefaultProjectRuntime.Validate() error = nil")
		}
	}

	validExit := ConfirmedProjectProcessExit{SessionID: session.ID, ExpectedSessionGeneration: 2, Process: &process, ExitedAt: at}
	for _, mutate := range []func(*ConfirmedProjectProcessExit){
		func(exit *ConfirmedProjectProcessExit) { exit.SessionID = "" },
		func(exit *ConfirmedProjectProcessExit) { exit.ExpectedSessionGeneration = 0 },
		func(exit *ConfirmedProjectProcessExit) { exit.Process.PID = 0 },
		func(exit *ConfirmedProjectProcessExit) { exit.ExitedAt = time.Time{} },
	} {
		exit := validExit
		processCopy := process
		exit.Process = &processCopy
		mutate(&exit)
		if err := validateConfirmedProjectProcessExit(exit); err == nil {
			t.Fatal("validateConfirmedProjectProcessExit() error = nil")
		}
	}

	validFailure := FailProjectStartRequest{
		ProjectID: projectID, OperationID: "operation-validation", ExpectedOperationRevision: 1,
		Exit: validExit, Phase: "failed", Problem: problem,
	}
	for _, mutate := range []func(*FailProjectStartRequest){
		func(request *FailProjectStartRequest) { request.ProjectID = "" },
		func(request *FailProjectStartRequest) { request.OperationID = "" },
		func(request *FailProjectStartRequest) { request.ExpectedOperationRevision = 0 },
		func(request *FailProjectStartRequest) { request.Phase = "" },
		func(request *FailProjectStartRequest) { request.Problem.Code = "" },
		func(request *FailProjectStartRequest) { request.Exit.SessionID = "" },
	} {
		request := validFailure
		mutate(&request)
		if err := validateFailProjectStartRequest(request); err == nil {
			t.Fatal("validateFailProjectStartRequest() error = nil")
		}
	}

	validBeginStop := BeginProjectStopRequest{
		ProjectID: projectID, OperationID: "operation-validation", ExpectedOperationRevision: 1,
		SessionID: session.ID, ExpectedSessionGeneration: 2, Phase: "stopping", At: at,
	}
	for _, mutate := range []func(*BeginProjectStopRequest){
		func(request *BeginProjectStopRequest) { request.ProjectID = "" },
		func(request *BeginProjectStopRequest) { request.SessionID = "" },
		func(request *BeginProjectStopRequest) { request.ExpectedSessionGeneration = 0 },
	} {
		request := validBeginStop
		mutate(&request)
		if err := validateBeginProjectStopRequest(request); err == nil {
			t.Fatal("validateBeginProjectStopRequest() error = nil")
		}
	}

	validCompleteStop := CompleteProjectStopRequest{
		ProjectID: projectID, OperationID: "operation-validation", ExpectedOperationRevision: 1,
		Exit: validExit, Phase: "stopped",
	}
	for _, mutate := range []func(*CompleteProjectStopRequest){
		func(request *CompleteProjectStopRequest) { request.ProjectID = "" },
		func(request *CompleteProjectStopRequest) { request.OperationID = "" },
		func(request *CompleteProjectStopRequest) { request.ExpectedOperationRevision = 0 },
		func(request *CompleteProjectStopRequest) { request.Phase = "" },
		func(request *CompleteProjectStopRequest) { request.Exit.SessionID = "" },
	} {
		request := validCompleteStop
		mutate(&request)
		if err := validateCompleteProjectStopRequest(request); err == nil {
			t.Fatal("validateCompleteProjectStopRequest() error = nil")
		}
	}

	if err := validateRecordUnexpectedProjectExitRequest(RecordUnexpectedProjectExitRequest{ProjectID: "", Exit: validExit}); err == nil {
		t.Fatal("validateRecordUnexpectedProjectExitRequest(invalid project) error = nil")
	}
	withoutProcess := validExit
	withoutProcess.Process = nil
	if err := validateRecordUnexpectedProjectExitRequest(RecordUnexpectedProjectExitRequest{ProjectID: projectID, Exit: withoutProcess}); err == nil {
		t.Fatal("validateRecordUnexpectedProjectExitRequest(missing process) error = nil")
	}
}

// TestProjectLifecycleMutationsFenceGenerationAndRollbackLateFailures proves process identity and transaction atomicity.
func TestProjectLifecycleMutationsFenceGenerationAndRollbackLateFailures(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := emptyProjectStoreMutationProject("project-fenced")
	if _, err := store.PutProject(t.Context(), project); err != nil {
		t.Fatalf("PutProject() error = %v", err)
	}
	queued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStart, project.ID, "fenced")
	at := queued.Operation.RequestedAt.Add(time.Second)
	session := projectLifecycleTestPlannedSession(t, project.ID, at)
	running, err := store.BeginProjectStart(t.Context(), BeginProjectStartRequest{
		ProjectID: project.ID, OperationID: queued.Operation.ID, ExpectedOperationRevision: queued.Revision,
		Session: session, Phase: "launching forj dev", At: at,
	})
	if err != nil {
		t.Fatalf("BeginProjectStart() error = %v", err)
	}
	process := projectLifecycleTestProcess(t)
	_, err = store.AttachProjectProcess(t.Context(), AttachProjectProcessRequest{
		ProjectID: project.ID, SessionID: session.ID, ExpectedSessionGeneration: 9, Process: process, At: at.Add(time.Second),
	})
	var stale *StaleSessionGenerationError
	if !errors.As(err, &stale) {
		t.Fatalf("AttachProjectProcess(stale) error = %v", err)
	}
	unchanged, err := store.ProjectSession(t.Context(), project.ID, session.ID)
	if err != nil || !reflect.DeepEqual(unchanged, session) {
		t.Fatalf("session after stale attachment = %#v, %v", unchanged, err)
	}

	if err := connection.Exec(`CREATE TRIGGER fail_ready_projection BEFORE UPDATE OF state ON projects
		WHEN NEW.state = 'ready' BEGIN SELECT RAISE(ABORT, 'injected ready failure'); END`).Error; err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	attached, err := store.AttachProjectProcess(t.Context(), AttachProjectProcessRequest{
		ProjectID: project.ID, SessionID: session.ID, ExpectedSessionGeneration: 1, Process: process, At: at.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("AttachProjectProcess() error = %v", err)
	}
	beforeSequence := projectStoreMutationSequence(t, store)
	_, err = store.CompleteProjectStart(t.Context(), CompleteProjectStartRequest{
		ProjectID: project.ID, OperationID: queued.Operation.ID, ExpectedOperationRevision: running.Operation.Revision,
		SessionID: session.ID, ExpectedSessionGeneration: attached.Generation, Runtime: projectLifecycleTestRuntime(),
		Phase: "default App ready", At: at.Add(2 * time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), "injected ready failure") {
		t.Fatalf("CompleteProjectStart(injected failure) error = %v", err)
	}
	operation := networkReleaseTestOperation(t, store, queued.Operation.ID)
	if operation.Operation.State != domain.OperationRunning || operation.Revision != running.Operation.Revision {
		t.Fatalf("operation after rollback = %#v", operation)
	}
	persistedProject, err := store.Project(t.Context(), project.ID)
	if err != nil || persistedProject.Project.State != domain.ProjectStarting {
		t.Fatalf("project after rollback = %#v, %v", persistedProject, err)
	}
	if projectStoreMutationSequence(t, store) != beforeSequence {
		t.Fatal("failed lifecycle mutation advanced the global sequence")
	}
}

// TestBeginProjectStartConcurrentExactRetryCommitsOnce proves serialized writers share one durable lifecycle result.
func TestBeginProjectStartConcurrentExactRetryCommitsOnce(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 4, projectStoreMutationTestClock)
	project := emptyProjectStoreMutationProject("project-concurrent-start")
	if _, err := store.PutProject(t.Context(), project); err != nil {
		t.Fatalf("PutProject() error = %v", err)
	}
	queued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStart, project.ID, "concurrent")
	at := queued.Operation.RequestedAt.Add(time.Second)
	request := BeginProjectStartRequest{
		ProjectID: project.ID, OperationID: queued.Operation.ID, ExpectedOperationRevision: queued.Revision,
		Session: projectLifecycleTestPlannedSession(t, project.ID, at), Phase: "launching forj dev", At: at,
	}

	const workers = 8
	results := make(chan ProjectLifecycleMutation, workers)
	errorsChannel := make(chan error, workers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := store.BeginProjectStart(context.Background(), request)
			results <- result
			errorsChannel <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("BeginProjectStart(concurrent) error = %v", err)
		}
	}
	var first *ProjectLifecycleMutation
	for result := range results {
		if first == nil {
			copy := result
			first = &copy
			continue
		}
		if !reflect.DeepEqual(result, *first) {
			t.Fatalf("concurrent lifecycle results differ: %#v / %#v", result, *first)
		}
	}
	if got := projectStoreMutationSequence(t, store); got != queued.Revision+2 {
		t.Fatalf("sequence after concurrent exact retries = %d, want %d", got, queued.Revision+2)
	}
}

// TestCompleteProjectUnregisterRejectsActiveSession prevents registration deletion from bypassing process ownership.
func TestCompleteProjectUnregisterRejectsActiveSession(t *testing.T) {
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := emptyProjectStoreMutationProject("project-unregister-session")
	_, running, completedAt := projectStoreMutationRunningUnregister(t, store, project, "operation-unregister-session")
	session := projectLifecycleTestPlannedSession(t, project.ID, completedAt.Add(-time.Second))
	row, err := projectSessionModelFromDomain(session)
	if err != nil || connection.Create(&row).Error != nil {
		t.Fatalf("create active session fixture: %v", err)
	}
	_, err = store.CompleteProjectUnregister(t.Context(), project.ID, running.Operation.ID, running.Revision, "remove project", completedAt)
	var active *ProjectSessionActiveError
	if !errors.As(err, &active) || active.SessionID != session.ID {
		t.Fatalf("CompleteProjectUnregister(active session) error = %v", err)
	}
}

// enqueueProjectLifecycleTestOperation creates one exact queued operation through production journal authority.
func enqueueProjectLifecycleTestOperation(
	t *testing.T,
	store *Store,
	kind domain.OperationKind,
	projectID domain.ProjectID,
	suffix string,
) OperationRecord {
	t.Helper()
	requestedAt := projectStoreMutationTestTime().Add(24*time.Hour + time.Duration(projectStoreMutationSequence(t, store)+1)*time.Minute)
	operation, err := domain.NewOperation(
		domain.OperationID("operation-"+suffix),
		domain.IntentID("intent-"+suffix),
		kind,
		projectID,
		requestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	record, err := projectStoreMutationJournal(store).Enqueue(t.Context(), operation)
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	return record
}

// projectLifecycleTestPlannedSession creates one canonical pre-launch session.
func projectLifecycleTestPlannedSession(t *testing.T, projectID domain.ProjectID, at time.Time) domain.ProjectSession {
	t.Helper()
	return domain.ProjectSession{
		ID: domain.SessionID("session-" + strings.TrimPrefix(string(projectID), "project-")), ProjectID: projectID,
		Owner: domain.SessionOwnerHarbor, State: domain.SessionPlanned,
		DescriptorDigest: strings.Repeat("a", 64), CredentialDigest: strings.Repeat("b", 64), Generation: 1,
		CreatedAt: at.UTC().Round(0), UpdatedAt: at.UTC().Round(0),
	}
}

// projectLifecycleTestProcess creates portable exact evidence around the current test executable.
func projectLifecycleTestProcess(t *testing.T) domain.ProcessEvidence {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	return domain.ProcessEvidence{
		PID: 4102, BirthToken: "test-process-birth", ExecutableIdentity: filepath.Clean(executable), ArgumentDigest: strings.Repeat("c", 64),
	}
}

// projectLifecycleTestRuntime creates the default App projection produced after a successful local readiness probe.
func projectLifecycleTestRuntime() DefaultProjectRuntime {
	return DefaultProjectRuntime{
		App: domain.AppSnapshot{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true},
		Resource: domain.ResourceSnapshot{
			ID: "app", Name: "App", Kind: "http", URL: "http://127.0.0.1:3000",
			Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"},
		},
	}
}

// projectLifecycleTestReadyProject drives a project through readiness for unexpected-exit tests.
func projectLifecycleTestReadyProject(
	t *testing.T,
	store *Store,
	projectID domain.ProjectID,
) (domain.ProjectSnapshot, domain.ProjectSession, domain.ProcessEvidence) {
	t.Helper()
	project := emptyProjectStoreMutationProject(projectID)
	if _, err := store.PutProject(t.Context(), project); err != nil {
		t.Fatalf("PutProject() error = %v", err)
	}
	queued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStart, project.ID, "ready-"+strings.TrimPrefix(string(projectID), "project-"))
	at := queued.Operation.RequestedAt.Add(time.Second)
	session := projectLifecycleTestPlannedSession(t, project.ID, at)
	running, err := store.BeginProjectStart(t.Context(), BeginProjectStartRequest{
		ProjectID: project.ID, OperationID: queued.Operation.ID, ExpectedOperationRevision: queued.Revision,
		Session: session, Phase: "launching forj dev", At: at,
	})
	if err != nil {
		t.Fatalf("BeginProjectStart() error = %v", err)
	}
	process := projectLifecycleTestProcess(t)
	attached, err := store.AttachProjectProcess(t.Context(), AttachProjectProcessRequest{
		ProjectID: project.ID, SessionID: session.ID, ExpectedSessionGeneration: 1, Process: process, At: at.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("AttachProjectProcess() error = %v", err)
	}
	ready, err := store.CompleteProjectStart(t.Context(), CompleteProjectStartRequest{
		ProjectID: project.ID, OperationID: queued.Operation.ID, ExpectedOperationRevision: running.Operation.Revision,
		SessionID: session.ID, ExpectedSessionGeneration: attached.Generation, Runtime: projectLifecycleTestRuntime(),
		Phase: "default App ready", At: at.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("CompleteProjectStart() error = %v", err)
	}
	return ready.Project.Project, *ready.Session, process
}

// requireLifecycleTestSessionCount reports the exact session cardinality for rollback assertions.
func requireLifecycleTestSessionCount(t *testing.T, connection *gorm.DB, projectID domain.ProjectID, want int64) {
	t.Helper()
	var count int64
	if err := connection.Model(&struct{}{}).Table("project_sessions").Where("project_id = ?", string(projectID)).Count(&count).Error; err != nil {
		t.Fatalf("count project sessions: %v", err)
	}
	if count != want {
		t.Fatalf("project session count = %d, want %d", count, want)
	}
}
