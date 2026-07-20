package state

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// plannedStartQuarantineFixture owns one running start whose launch has not accepted process evidence.
type plannedStartQuarantineFixture struct {
	store      *Store
	connection *gorm.DB
	project    domain.ProjectSnapshot
	running    ProjectLifecycleMutation
	session    domain.ProjectSession
	request    QuarantinePlannedProjectStartRequest
}

// terminalSessionQuarantineFixture owns one formerly ready project whose legacy session omitted exact-process evidence.
type terminalSessionQuarantineFixture struct {
	store      *Store
	connection *gorm.DB
	project    domain.ProjectSnapshot
	session    domain.ProjectSession
	request    QuarantineTerminalProjectSessionRequest
}

// TestQuarantinePlannedProjectStartRetainsAuthorityAndReplays proves quarantine preserves the exact unresolved boundary.
func TestQuarantinePlannedProjectStartRetainsAuthorityAndReplays(t *testing.T) {
	fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-replay")
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)

	quarantined, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("QuarantinePlannedProjectStart() error = %v", err)
	}
	if quarantined.Operation.Operation.State != domain.OperationFailed ||
		quarantined.Operation.Operation.Problem == nil ||
		*quarantined.Operation.Operation.Problem != fixture.request.Problem {
		t.Fatalf("quarantined operation = %#v", quarantined.Operation)
	}
	if quarantined.Project.Project.State != domain.ProjectUnavailable ||
		!quarantined.Project.Project.UpdatedAt.Equal(fixture.request.At) {
		t.Fatalf("quarantined project = %#v", quarantined.Project)
	}
	if quarantined.Session == nil || !reflect.DeepEqual(*quarantined.Session, fixture.session) {
		t.Fatalf("quarantined session = %#v, want %#v", quarantined.Session, fixture.session)
	}
	persistedSession, err := fixture.store.ActiveProjectSession(t.Context(), fixture.project.ID)
	if err != nil || !reflect.DeepEqual(persistedSession, fixture.session) {
		t.Fatalf("ActiveProjectSession() = %#v, %v", persistedSession, err)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore+2 {
		t.Fatalf("sequence after quarantine = %d, want %d", got, sequenceBefore+2)
	}

	sequenceAfter := projectStoreMutationSequence(t, fixture.store)
	replayed, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), fixture.request)
	if err != nil || !reflect.DeepEqual(replayed, quarantined) {
		t.Fatalf("QuarantinePlannedProjectStart(replay) = %#v, %v", replayed, err)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceAfter {
		t.Fatalf("sequence after quarantine replay = %d, want %d", got, sequenceAfter)
	}

	mismatch := fixture.request
	mismatch.Phase = "different recovery decision"
	if _, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), mismatch); err == nil ||
		!strings.Contains(err.Error(), "retry does not match") {
		t.Fatalf("QuarantinePlannedProjectStart(mismatched replay) error = %v", err)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceAfter {
		t.Fatalf("sequence after mismatched replay = %d, want %d", got, sequenceAfter)
	}
}

// TestQuarantineTerminalProjectSessionWithholdsRoutesAndReplays proves legacy missing evidence becomes one durable actionable project failure.
func TestQuarantineTerminalProjectSessionWithholdsRoutesAndReplays(t *testing.T) {
	fixture := newTerminalSessionQuarantineFixture(t, "project-terminal-quarantine-replay")
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)

	quarantined, err := fixture.store.QuarantineTerminalProjectSession(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("QuarantineTerminalProjectSession() error = %v", err)
	}
	if quarantined.Operation.Operation.State != domain.OperationFailed ||
		quarantined.Operation.Operation.Problem == nil ||
		*quarantined.Operation.Operation.Problem != fixture.request.Problem {
		t.Fatalf("quarantine operation = %#v", quarantined.Operation)
	}
	if !projectMatchesInactiveState(quarantined.Project.Project, domain.ProjectUnavailable, fixture.request.At) {
		t.Fatalf("quarantined project is not route-free = %#v", quarantined.Project.Project)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore+4 {
		t.Fatalf("sequence after quarantine = %d, want %d", got, sequenceBefore+4)
	}
	assertTerminalSessionQuarantineEvidenceRetained(t, fixture)
	snapshot, err := fixture.store.Snapshot(t.Context())
	if err != nil || len(snapshot.Projects) != 1 || len(snapshot.Projects[0].Resources) != 0 || len(snapshot.Operations) != 2 {
		t.Fatalf("snapshot after quarantine = %#v, %v", snapshot, err)
	}
	latest := snapshot.Operations[len(snapshot.Operations)-1]
	if latest.ID != fixture.request.Operation.ID || latest.Problem == nil || latest.Problem.Code != fixture.request.Problem.Code {
		t.Fatalf("current recovery problem = %#v", latest)
	}

	sequenceAfter := projectStoreMutationSequence(t, fixture.store)
	replayed, err := fixture.store.QuarantineTerminalProjectSession(t.Context(), fixture.request)
	if err != nil || !reflect.DeepEqual(replayed, quarantined) {
		t.Fatalf("QuarantineTerminalProjectSession(replay) = %#v, %v", replayed, err)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceAfter {
		t.Fatalf("sequence after quarantine replay = %d, want %d", got, sequenceAfter)
	}

	mismatch := fixture.request
	mismatch.FailurePhase = "different recovery decision"
	if _, err := fixture.store.QuarantineTerminalProjectSession(t.Context(), mismatch); err == nil ||
		!strings.Contains(err.Error(), "retry does not match") {
		t.Fatalf("QuarantineTerminalProjectSession(mismatched replay) error = %v", err)
	}
}

// TestQuarantineTerminalProjectSessionRollsBackLateFailure proves route withdrawal and the recovery problem share one transaction.
func TestQuarantineTerminalProjectSessionRollsBackLateFailure(t *testing.T) {
	fixture := newTerminalSessionQuarantineFixture(t, "project-terminal-quarantine-atomic")
	if err := fixture.connection.Exec(`CREATE TRIGGER fail_terminal_quarantine BEFORE UPDATE OF state ON projects
		WHEN NEW.state = 'unavailable' BEGIN SELECT RAISE(ABORT, 'injected terminal quarantine failure'); END`).Error; err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)

	_, err := fixture.store.QuarantineTerminalProjectSession(t.Context(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), "injected terminal quarantine failure") {
		t.Fatalf("QuarantineTerminalProjectSession(injected failure) error = %v", err)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore {
		t.Fatalf("sequence after rollback = %d, want %d", got, sequenceBefore)
	}
	project, readErr := fixture.store.Project(t.Context(), fixture.project.ID)
	if readErr != nil || !reflect.DeepEqual(project.Project, fixture.project) {
		t.Fatalf("project after rollback = %#v, %v", project.Project, readErr)
	}
	var count int64
	if readErr := fixture.connection.Model(&models.Operation{}).Where("id = ?", fixture.request.Operation.ID).Count(&count).Error; readErr != nil || count != 0 {
		t.Fatalf("recovery operation after rollback count = %d, %v", count, readErr)
	}
	assertTerminalSessionQuarantineEvidenceRetained(t, fixture)
}

// TestQuarantineTerminalProjectSessionRejectsExactProcessEvidence protects the normal exact-process settlement path.
func TestQuarantineTerminalProjectSessionRejectsExactProcessEvidence(t *testing.T) {
	store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project, session, _ := projectLifecycleTestReadyProject(t, store, "project-terminal-quarantine-exact")
	projectRecord, err := store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("read exact-evidence project: %v", err)
	}
	at := project.UpdatedAt.Add(time.Second)
	operation, err := domain.NewOperation("operation-terminal-quarantine-exact", "intent-terminal-quarantine-exact", domain.OperationKindProjectStart, project.ID, at)
	if err != nil {
		t.Fatalf("create exact-evidence recovery operation: %v", err)
	}
	request := QuarantineTerminalProjectSessionRequest{
		ProjectID: project.ID, ExpectedProjectRevision: projectRecord.Revision,
		SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
		Operation: operation, RunningPhase: "isolating unresolved process authority",
		FailurePhase: "recovery required", Problem: plannedStartQuarantineTestProblem(), At: at,
	}
	sequenceBefore := projectStoreMutationSequence(t, store)
	if _, err := store.QuarantineTerminalProjectSession(t.Context(), request); err == nil || !strings.Contains(err.Error(), "retains exact process evidence") {
		t.Fatalf("QuarantineTerminalProjectSession(exact evidence) error = %v", err)
	}
	if got := projectStoreMutationSequence(t, store); got != sequenceBefore {
		t.Fatalf("sequence after exact-evidence rejection = %d, want %d", got, sequenceBefore)
	}
}

// TestValidateQuarantineTerminalProjectSessionRequestRejectsIncompleteFences covers every caller-owned recovery boundary.
func TestValidateQuarantineTerminalProjectSessionRequestRejectsIncompleteFences(t *testing.T) {
	valid := newTerminalSessionQuarantineFixture(t, "project-terminal-quarantine-validation").request
	tests := []struct {
		name   string
		mutate func(*QuarantineTerminalProjectSessionRequest)
	}{
		{name: "project", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.ProjectID = "" }},
		{name: "project revision", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.ExpectedProjectRevision = 0 }},
		{name: "session", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.SessionID = "" }},
		{name: "session generation", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.ExpectedSessionGeneration = 0 }},
		{name: "operation", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.Operation.ID = "" }},
		{name: "operation kind", mutate: func(request *QuarantineTerminalProjectSessionRequest) {
			request.Operation.Kind = domain.OperationKindProjectStop
		}},
		{name: "operation project", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.Operation.ProjectID = "project-other" }},
		{name: "operation state", mutate: func(request *QuarantineTerminalProjectSessionRequest) {
			startedAt := request.At
			request.Operation.State = domain.OperationRunning
			request.Operation.Phase = "running"
			request.Operation.StartedAt = &startedAt
		}},
		{name: "operation time", mutate: func(request *QuarantineTerminalProjectSessionRequest) {
			request.Operation.RequestedAt = request.At.Add(time.Second)
		}},
		{name: "running phase", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.RunningPhase = " " }},
		{name: "failure phase", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.FailurePhase = "" }},
		{name: "problem", mutate: func(request *QuarantineTerminalProjectSessionRequest) { request.Problem.Code = "" }},
		{name: "time", mutate: func(request *QuarantineTerminalProjectSessionRequest) {
			request.At = time.Time{}
			request.Operation.RequestedAt = time.Time{}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := validateQuarantineTerminalProjectSessionRequest(request); err == nil {
				t.Fatal("validateQuarantineTerminalProjectSessionRequest() error = nil")
			}
		})
	}
}

// TestQuarantinePlannedProjectStartRollsBackLateFailure proves the operation cannot fail without the unavailable projection.
func TestQuarantinePlannedProjectStartRollsBackLateFailure(t *testing.T) {
	fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-atomic")
	if err := fixture.connection.Exec(`CREATE TRIGGER fail_unavailable_projection BEFORE UPDATE OF state ON projects
		WHEN NEW.state = 'unavailable' BEGIN SELECT RAISE(ABORT, 'injected unavailable failure'); END`).Error; err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	sequenceBefore := projectStoreMutationSequence(t, fixture.store)

	_, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), "injected unavailable failure") {
		t.Fatalf("QuarantinePlannedProjectStart(injected failure) error = %v", err)
	}
	operation := networkReleaseTestOperation(t, fixture.store, fixture.request.OperationID)
	if operation.Operation.State != domain.OperationRunning || operation.Revision != fixture.running.Operation.Revision {
		t.Fatalf("operation after rollback = %#v", operation)
	}
	project, err := fixture.store.Project(t.Context(), fixture.project.ID)
	if err != nil || !reflect.DeepEqual(project, fixture.running.Project) {
		t.Fatalf("project after rollback = %#v, %v", project, err)
	}
	session, err := fixture.store.ActiveProjectSession(t.Context(), fixture.project.ID)
	if err != nil || !reflect.DeepEqual(session, fixture.session) {
		t.Fatalf("session after rollback = %#v, %v", session, err)
	}
	if got := projectStoreMutationSequence(t, fixture.store); got != sequenceBefore {
		t.Fatalf("sequence after rollback = %d, want %d", got, sequenceBefore)
	}
}

// TestQuarantinePlannedProjectStartFencesEveryDurableBoundary covers stale revisions and session generations.
func TestQuarantinePlannedProjectStartFencesEveryDurableBoundary(t *testing.T) {
	t.Run("operation revision", func(t *testing.T) {
		fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-operation-fence")
		request := fixture.request
		request.ExpectedOperationRevision--
		request.ExpectedProjectRevision--
		_, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), request)
		var stale *StaleRevisionError
		if !errors.As(err, &stale) {
			t.Fatalf("stale operation error = %v", err)
		}
		assertPlannedStartQuarantineUnchanged(t, fixture)
	})

	t.Run("project revision", func(t *testing.T) {
		fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-project-fence")
		drifted := fixture.running.Project.Project
		drifted.UpdatedAt = fixture.request.At
		current, err := fixture.store.PutProject(t.Context(), drifted)
		if err != nil {
			t.Fatalf("PutProject(drifted) error = %v", err)
		}
		_, err = fixture.store.QuarantinePlannedProjectStart(t.Context(), fixture.request)
		var conflict *ProjectRevisionConflictError
		if !errors.As(err, &conflict) || conflict.Actual != current.Revision {
			t.Fatalf("stale project error = %#v, want actual revision %d", err, current.Revision)
		}
		operation := networkReleaseTestOperation(t, fixture.store, fixture.request.OperationID)
		if operation.Operation.State != domain.OperationRunning {
			t.Fatalf("operation after project fence = %#v", operation)
		}
	})

	t.Run("session generation", func(t *testing.T) {
		fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-session-fence")
		request := fixture.request
		request.ExpectedSessionGeneration++
		_, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), request)
		var stale *StaleSessionGenerationError
		if !errors.As(err, &stale) {
			t.Fatalf("stale session generation error = %v", err)
		}
		assertPlannedStartQuarantineUnchanged(t, fixture)
	})
}

// TestQuarantinePlannedProjectStartRejectsStateAndSessionMismatches keeps unrelated or process-backed authority untouched.
func TestQuarantinePlannedProjectStartRejectsStateAndSessionMismatches(t *testing.T) {
	t.Run("operation is not running", func(t *testing.T) {
		store, _ := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
		project := emptyProjectStoreMutationProject("project-quarantine-queued")
		if _, err := store.PutProject(t.Context(), project); err != nil {
			t.Fatalf("PutProject() error = %v", err)
		}
		queued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStart, project.ID, "quarantine-queued")
		request := QuarantinePlannedProjectStartRequest{
			ProjectID: project.ID, OperationID: queued.Operation.ID,
			ExpectedOperationRevision: queued.Revision, ExpectedProjectRevision: queued.Revision + 1,
			SessionID: "session-quarantine-queued", ExpectedSessionGeneration: 1,
			Phase: "launch authority unresolved", Problem: plannedStartQuarantineTestProblem(), At: queued.Operation.RequestedAt.Add(time.Second),
		}
		if _, err := store.QuarantinePlannedProjectStart(t.Context(), request); err == nil || !strings.Contains(err.Error(), "must be running") {
			t.Fatalf("queued operation error = %v", err)
		}
	})

	t.Run("project is not starting", func(t *testing.T) {
		fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-state")
		if err := fixture.connection.Model(&models.Project{}).
			Where("project_id = ?", string(fixture.project.ID)).
			Update("state", string(domain.ProjectStopped)).Error; err != nil {
			t.Fatalf("change project state: %v", err)
		}
		if _, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), fixture.request); err == nil ||
			!strings.Contains(err.Error(), "must be starting") {
			t.Fatalf("project state mismatch error = %v", err)
		}
	})

	t.Run("session identity does not match", func(t *testing.T) {
		fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-session-id")
		request := fixture.request
		request.SessionID = "session-other"
		_, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), request)
		var missing *ProjectSessionNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("session mismatch error = %v", err)
		}
		assertPlannedStartQuarantineUnchanged(t, fixture)
	})

	t.Run("session already has process evidence", func(t *testing.T) {
		fixture := newPlannedStartQuarantineFixture(t, "project-quarantine-process")
		attached, err := fixture.store.AttachProjectProcess(t.Context(), AttachProjectProcessRequest{
			ProjectID: fixture.project.ID, SessionID: fixture.session.ID,
			ExpectedSessionGeneration: fixture.session.Generation,
			Process:                   projectLifecycleTestProcess(t),
			At:                        fixture.request.At,
		})
		if err != nil {
			t.Fatalf("AttachProjectProcess() error = %v", err)
		}
		request := fixture.request
		request.ExpectedSessionGeneration = attached.Generation
		if _, err := fixture.store.QuarantinePlannedProjectStart(t.Context(), request); err == nil ||
			!strings.Contains(err.Error(), "must be planned without process evidence") {
			t.Fatalf("process-backed session error = %v", err)
		}
		operation := networkReleaseTestOperation(t, fixture.store, fixture.request.OperationID)
		if operation.Operation.State != domain.OperationRunning {
			t.Fatalf("operation after process-backed rejection = %#v", operation)
		}
	})
}

// TestValidateQuarantinePlannedProjectStartRequestRejectsUnfencedInput covers every caller-provided identity and outcome.
func TestValidateQuarantinePlannedProjectStartRequestRejectsUnfencedInput(t *testing.T) {
	at := time.Date(2026, 7, 19, 20, 0, 0, 0, time.UTC)
	valid := QuarantinePlannedProjectStartRequest{
		ProjectID: "project-validation", OperationID: "operation-validation",
		ExpectedOperationRevision: 4, ExpectedProjectRevision: 5,
		SessionID: "session-validation", ExpectedSessionGeneration: 1,
		Phase: "launch authority unresolved", Problem: plannedStartQuarantineTestProblem(), At: at,
	}
	for _, test := range []struct {
		name   string
		mutate func(*QuarantinePlannedProjectStartRequest)
	}{
		{name: "project", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.ProjectID = "" }},
		{name: "operation", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.OperationID = "" }},
		{name: "operation revision", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.ExpectedOperationRevision = 0 }},
		{name: "project revision", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.ExpectedProjectRevision = 0 }},
		{name: "revision boundary", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.ExpectedProjectRevision++ }},
		{name: "session", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.SessionID = "" }},
		{name: "generation", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.ExpectedSessionGeneration = 0 }},
		{name: "phase", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.Phase = " " }},
		{name: "problem", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.Problem.Code = "" }},
		{name: "time", mutate: func(request *QuarantinePlannedProjectStartRequest) { request.At = time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := validateQuarantinePlannedProjectStartRequest(request); err == nil {
				t.Fatal("validateQuarantinePlannedProjectStartRequest() error = nil")
			}
		})
	}
}

// newPlannedStartQuarantineFixture creates the exact pre-process recovery boundary the mutation accepts.
func newPlannedStartQuarantineFixture(t *testing.T, projectID domain.ProjectID) plannedStartQuarantineFixture {
	t.Helper()
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project := emptyProjectStoreMutationProject(projectID)
	if _, err := store.PutProject(t.Context(), project); err != nil {
		t.Fatalf("PutProject() error = %v", err)
	}
	queued := enqueueProjectLifecycleTestOperation(t, store, domain.OperationKindProjectStart, project.ID, "quarantine-"+strings.TrimPrefix(string(project.ID), "project-"))
	startedAt := queued.Operation.RequestedAt.Add(time.Second)
	session := projectLifecycleTestPlannedSession(t, project.ID, startedAt)
	running, err := store.BeginProjectStart(t.Context(), BeginProjectStartRequest{
		ProjectID: project.ID, OperationID: queued.Operation.ID,
		ExpectedOperationRevision: queued.Revision,
		ExpectedProjectRevision:   projectLifecycleTestProjectRevision(t, store, project.ID),
		Session:                   session, Phase: "launching forj dev", At: startedAt,
	})
	if err != nil {
		t.Fatalf("BeginProjectStart() error = %v", err)
	}
	return plannedStartQuarantineFixture{
		store: store, connection: connection, project: project, running: running, session: session,
		request: QuarantinePlannedProjectStartRequest{
			ProjectID: project.ID, OperationID: running.Operation.Operation.ID,
			ExpectedOperationRevision: running.Operation.Revision,
			ExpectedProjectRevision:   running.Project.Revision,
			SessionID:                 session.ID, ExpectedSessionGeneration: session.Generation,
			Phase: "launch authority unresolved", Problem: plannedStartQuarantineTestProblem(), At: startedAt.Add(time.Second),
		},
	}
}

// newTerminalSessionQuarantineFixture creates a legacy awaiting-attach row whose exact-process tuple is wholly absent.
func newTerminalSessionQuarantineFixture(t *testing.T, projectID domain.ProjectID) terminalSessionQuarantineFixture {
	t.Helper()
	store, connection := newProjectStoreReadTestHarness(t, 1, projectStoreMutationTestClock)
	project, session, _ := projectLifecycleTestReadyProject(t, store, projectID)
	if err := connection.Exec(
		`UPDATE project_sessions
		 SET pid = NULL, birth_token = NULL, executable_identity = NULL, argument_digest = NULL
		 WHERE project_id = ? AND session_id = ?`,
		string(projectID),
		string(session.ID),
	).Error; err != nil {
		t.Fatalf("remove legacy process evidence: %v", err)
	}
	projectRecord, err := store.Project(t.Context(), projectID)
	if err != nil {
		t.Fatalf("read terminal quarantine project: %v", err)
	}
	at := project.UpdatedAt.Add(time.Second)
	operation, err := domain.NewOperation(
		domain.OperationID("operation-"+strings.TrimPrefix(string(projectID), "project-")),
		domain.IntentID("intent-"+strings.TrimPrefix(string(projectID), "project-")),
		domain.OperationKindProjectStart,
		projectID,
		at,
	)
	if err != nil {
		t.Fatalf("create terminal quarantine operation: %v", err)
	}
	return terminalSessionQuarantineFixture{
		store: store, connection: connection, project: project, session: session,
		request: QuarantineTerminalProjectSessionRequest{
			ProjectID: projectID, ExpectedProjectRevision: projectRecord.Revision,
			SessionID: session.ID, ExpectedSessionGeneration: session.Generation,
			Operation: operation, RunningPhase: "isolating unresolved process authority",
			FailurePhase: "recovery required", Problem: plannedStartQuarantineTestProblem(), At: at,
		},
	}
}

// assertTerminalSessionQuarantineEvidenceRetained proves recovery did not erase or reinterpret unidentified process authority.
func assertTerminalSessionQuarantineEvidenceRetained(t *testing.T, fixture terminalSessionQuarantineFixture) {
	t.Helper()
	_, err := fixture.store.ActiveProjectSession(t.Context(), fixture.project.ID)
	var missing *ProjectSessionProcessEvidenceMissingError
	if !errors.As(err, &missing) ||
		missing.ProjectID != fixture.project.ID ||
		missing.SessionID != fixture.session.ID ||
		missing.Generation != fixture.session.Generation {
		t.Fatalf("retained terminal session evidence boundary = %#v, %v", missing, err)
	}
}

// assertPlannedStartQuarantineUnchanged verifies a rejected request did not mutate any owned boundary.
func assertPlannedStartQuarantineUnchanged(t *testing.T, fixture plannedStartQuarantineFixture) {
	t.Helper()
	operation := networkReleaseTestOperation(t, fixture.store, fixture.request.OperationID)
	if !reflect.DeepEqual(operation, fixture.running.Operation) {
		t.Fatalf("operation after rejection = %#v, want %#v", operation, fixture.running.Operation)
	}
	project, err := fixture.store.Project(t.Context(), fixture.project.ID)
	if err != nil || !reflect.DeepEqual(project, fixture.running.Project) {
		t.Fatalf("project after rejection = %#v, %v", project, err)
	}
	session, err := fixture.store.ActiveProjectSession(t.Context(), fixture.project.ID)
	if err != nil || !reflect.DeepEqual(session, fixture.session) {
		t.Fatalf("session after rejection = %#v, %v", session, err)
	}
}

// plannedStartQuarantineTestProblem returns the stable operator-facing reason used by recovery tests.
func plannedStartQuarantineTestProblem() domain.Problem {
	return domain.Problem{
		Code:      "project.lifecycle.process_identity_unresolved",
		Message:   "Harbor could not prove whether the planned project process started.",
		Retryable: false,
	}
}
