package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// projectActivityTestState supplies one current project and optional durable session.
type projectActivityTestState struct {
	project      state.ProjectRecord
	session      domain.ProjectSession
	projectErr   error
	sessionErr   error
	projectReads int
	sessionReads int
}

// Project returns the configured current project record.
func (source *projectActivityTestState) Project(context.Context, domain.ProjectID) (state.ProjectRecord, error) {
	source.projectReads++
	return source.project, source.projectErr
}

// ActiveProjectSession returns the configured current session.
func (source *projectActivityTestState) ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error) {
	source.sessionReads++
	return source.session, source.sessionErr
}

// projectActivityOutputCall records one exact transcript selection.
type projectActivityOutputCall struct {
	projectID domain.ProjectID
	sessionID domain.SessionID
	cursor    uint64
}

// projectActivityTestReader returns one configured bounded output chunk.
type projectActivityTestReader struct {
	output     projectprocess.OutputChunk
	waitOutput projectprocess.OutputChunk
	waitErr    error
	wait       func()
	calls      []projectActivityOutputCall
	waitCalls  []projectActivityOutputCall
}

// WaitOutput records one exact held transcript selection and returns the configured wake result.
func (reader *projectActivityTestReader) WaitOutput(
	_ context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	cursor uint64,
) (projectprocess.OutputChunk, error) {
	reader.waitCalls = append(reader.waitCalls, projectActivityOutputCall{projectID: projectID, sessionID: sessionID, cursor: cursor})
	if reader.wait != nil {
		reader.wait()
	}
	return reader.waitOutput, reader.waitErr
}

// ReadOutput records and returns one exact current-session transcript read.
func (reader *projectActivityTestReader) ReadOutput(
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	cursor uint64,
) projectprocess.OutputChunk {
	reader.calls = append(reader.calls, projectActivityOutputCall{projectID: projectID, sessionID: sessionID, cursor: cursor})
	return reader.output
}

// TestReadCurrentProjectActivityUsesOnlyTheDurableCurrentSession verifies caller session input cannot select history.
func TestReadCurrentProjectActivityUsesOnlyTheDurableCurrentSession(t *testing.T) {
	source := &projectActivityTestState{project: projectActivityTestProject(), session: projectActivityTestSession()}
	reader := &projectActivityTestReader{output: projectprocess.OutputChunk{
		Available:  true,
		HasMore:    true,
		NextCursor: 11,
		Text:       "current output",
	}}
	activity, err := readCurrentProjectActivity(t.Context(), source, reader, ProjectActivityRequest{
		ProjectID: "project-orders",
		SessionID: "session-prior",
		Cursor:    900,
		Wait:      25 * time.Second,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectActivity() error = %v", err)
	}
	if activity.ProjectID != "project-orders" || activity.Session == nil || activity.Session.ID != "session-current" || !activity.Session.Output.Reset {
		t.Fatalf("activity = %#v", activity)
	}
	wantCall := projectActivityOutputCall{projectID: "project-orders", sessionID: "session-current", cursor: 0}
	if len(reader.calls) != 1 || reader.calls[0] != wantCall {
		t.Fatalf("output calls = %#v, want %#v", reader.calls, wantCall)
	}
	if len(reader.waitCalls) != 0 {
		t.Fatalf("changed session waited on retired output: %#v", reader.waitCalls)
	}
}

// TestReadCurrentProjectActivityContinuesAnExactCursor verifies matching sessions preserve their opaque cursor.
func TestReadCurrentProjectActivityContinuesAnExactCursor(t *testing.T) {
	source := &projectActivityTestState{project: projectActivityTestProject(), session: projectActivityTestSession()}
	reader := &projectActivityTestReader{output: projectprocess.OutputChunk{Available: true, NextCursor: 48, Text: "next"}}
	activity, err := readCurrentProjectActivity(t.Context(), source, reader, ProjectActivityRequest{
		ProjectID: "project-orders",
		SessionID: "session-current",
		Cursor:    44,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectActivity() error = %v", err)
	}
	if activity.Session == nil || activity.Session.Output.Reset || activity.Session.Output.Text != "next" {
		t.Fatalf("activity = %#v", activity)
	}
	if len(reader.calls) != 1 || reader.calls[0].cursor != 44 {
		t.Fatalf("output calls = %#v", reader.calls)
	}
}

// TestReadCurrentProjectActivityWaitsAndReselectsDurableState verifies held reads cannot return a replaced session as current.
func TestReadCurrentProjectActivityWaitsAndReselectsDurableState(t *testing.T) {
	source := &projectActivityTestState{project: projectActivityTestProject(), session: projectActivityTestSession()}
	reader := &projectActivityTestReader{output: projectprocess.OutputChunk{Available: true, NextCursor: 4, Text: "next"}}
	reader.wait = func() {
		source.session.ID = "session-next"
		source.session.Generation++
	}
	activity, err := readCurrentProjectActivity(t.Context(), source, reader, ProjectActivityRequest{
		ProjectID: "project-orders",
		SessionID: "session-current",
		Cursor:    44,
		Wait:      25 * time.Second,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectActivity() error = %v", err)
	}
	wantWait := projectActivityOutputCall{projectID: "project-orders", sessionID: "session-current", cursor: 44}
	if len(reader.waitCalls) != 1 || reader.waitCalls[0] != wantWait {
		t.Fatalf("wait calls = %#v, want %#v", reader.waitCalls, wantWait)
	}
	wantRead := projectActivityOutputCall{projectID: "project-orders", sessionID: "session-next", cursor: 0}
	if len(reader.calls) != 1 || reader.calls[0] != wantRead {
		t.Fatalf("read calls = %#v, want %#v", reader.calls, wantRead)
	}
	if activity.Session == nil || activity.Session.ID != "session-next" || !activity.Session.Output.Reset {
		t.Fatalf("activity = %#v, want reset next session", activity)
	}
}

// TestReadCurrentProjectActivityReturnsTheWakingChunk verifies a process exit cannot erase output that already woke the held request.
func TestReadCurrentProjectActivityReturnsTheWakingChunk(t *testing.T) {
	source := &projectActivityTestState{project: projectActivityTestProject(), session: projectActivityTestSession()}
	reader := &projectActivityTestReader{
		output:     projectprocess.OutputChunk{},
		waitOutput: projectprocess.OutputChunk{Available: true, NextCursor: 48, Text: "wake"},
	}
	activity, err := readCurrentProjectActivity(t.Context(), source, reader, ProjectActivityRequest{
		ProjectID: "project-orders",
		SessionID: "session-current",
		Cursor:    44,
		Wait:      time.Second,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectActivity() error = %v", err)
	}
	if activity.Session == nil || activity.Session.Output.Text != "wake" || activity.Session.Output.NextCursor != 48 {
		t.Fatalf("activity = %#v, want waking output", activity)
	}
}

// TestReadCurrentProjectActivityTreatsItsWaitDeadlineAsAnEmptyWake verifies timeout is a normal long-poll response.
func TestReadCurrentProjectActivityTreatsItsWaitDeadlineAsAnEmptyWake(t *testing.T) {
	source := &projectActivityTestState{project: projectActivityTestProject(), session: projectActivityTestSession()}
	reader := &projectActivityTestReader{
		output:  projectprocess.OutputChunk{Available: true, NextCursor: 44},
		waitErr: context.DeadlineExceeded,
	}
	activity, err := readCurrentProjectActivity(t.Context(), source, reader, ProjectActivityRequest{
		ProjectID: "project-orders",
		SessionID: "session-current",
		Cursor:    44,
		Wait:      time.Second,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectActivity() error = %v", err)
	}
	if activity.Session == nil || activity.Session.Output.NextCursor != 44 || len(reader.calls) != 1 {
		t.Fatalf("activity/read calls = %#v / %#v", activity, reader.calls)
	}
}

// TestReadCurrentProjectActivityPropagatesCallerCancellation verifies disconnects do not become successful empty output.
func TestReadCurrentProjectActivityPropagatesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader := &projectActivityTestReader{waitErr: context.Canceled, wait: cancel}
	_, err := readCurrentProjectActivity(ctx,
		&projectActivityTestState{project: projectActivityTestProject(), session: projectActivityTestSession()},
		reader,
		ProjectActivityRequest{
			ProjectID: "project-orders",
			SessionID: "session-current",
			Wait:      time.Second,
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("readCurrentProjectActivity() error = %v, want context cancellation", err)
	}
}

// TestReadCurrentProjectActivityReturnsNoHistoryWithoutAnActiveSession verifies terminal projects cannot select retired sessions.
func TestReadCurrentProjectActivityReturnsNoHistoryWithoutAnActiveSession(t *testing.T) {
	source := &projectActivityTestState{
		project:    projectActivityTestProject(),
		sessionErr: &state.ProjectSessionNotFoundError{ProjectID: "project-orders"},
	}
	reader := new(projectActivityTestReader)
	activity, err := readCurrentProjectActivity(t.Context(), source, reader, ProjectActivityRequest{ProjectID: "project-orders"})
	if err != nil {
		t.Fatalf("readCurrentProjectActivity() error = %v", err)
	}
	if activity.ProjectID != "project-orders" || activity.Session != nil || len(reader.calls) != 0 {
		t.Fatalf("inactive activity/calls = %#v / %#v", activity, reader.calls)
	}
}

// TestReadCurrentProjectActivityRejectsInvalidOrMissingProjectsBeforeOutput verifies no in-memory read bypasses durable selection.
func TestReadCurrentProjectActivityRejectsInvalidOrMissingProjectsBeforeOutput(t *testing.T) {
	missing := &state.ProjectNotFoundError{ProjectID: "project-missing"}
	for _, test := range []struct {
		name    string
		source  *projectActivityTestState
		request ProjectActivityRequest
		want    error
	}{
		{name: "cursor without session", source: new(projectActivityTestState), request: ProjectActivityRequest{ProjectID: "project-orders", Cursor: 1}},
		{name: "wait too long", source: new(projectActivityTestState), request: ProjectActivityRequest{ProjectID: "project-orders", Wait: maximumProjectActivityWait + time.Millisecond}},
		{name: "missing project", source: &projectActivityTestState{projectErr: missing}, request: ProjectActivityRequest{ProjectID: "project-missing"}, want: missing},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := new(projectActivityTestReader)
			_, err := readCurrentProjectActivity(t.Context(), test.source, reader, test.request)
			if err == nil {
				t.Fatal("readCurrentProjectActivity() error = nil")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("readCurrentProjectActivity() error = %v, want %v", err, test.want)
			}
			if len(reader.calls) != 0 || (test.name == "cursor without session" && test.source.projectReads != 0) {
				t.Fatalf("invalid request reached state/output: project reads %d, calls %#v", test.source.projectReads, reader.calls)
			}
		})
	}
}

// TestReadCurrentProjectActivityRejectsMismatchedDurableSession verifies corrupted selection cannot expose another project's activity.
func TestReadCurrentProjectActivityRejectsMismatchedDurableSession(t *testing.T) {
	session := projectActivityTestSession()
	session.ProjectID = "project-other"
	source := &projectActivityTestState{project: projectActivityTestProject(), session: session}
	reader := new(projectActivityTestReader)
	_, err := readCurrentProjectActivity(t.Context(), source, reader, ProjectActivityRequest{ProjectID: "project-orders"})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("readCurrentProjectActivity() error = %v, want selection mismatch", err)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("mismatched session reached output reader: %#v", reader.calls)
	}
}

// projectActivityTestProject returns the registered identity required before a session read.
func projectActivityTestProject() state.ProjectRecord {
	return state.ProjectRecord{Project: domain.ProjectSnapshot{ID: "project-orders"}, Revision: 1}
}

// projectActivityTestSession returns one complete process-backed current session.
func projectActivityTestSession() domain.ProjectSession {
	at := time.Date(2026, time.July, 19, 18, 0, 0, 0, time.UTC)
	executable, err := filepath.Abs(filepath.Join("test", "bin", "forj"))
	if err != nil {
		panic(err)
	}
	return domain.ProjectSession{
		ID:               "session-current",
		ProjectID:        "project-orders",
		Owner:            domain.SessionOwnerHarbor,
		State:            domain.SessionAwaitingAttach,
		DescriptorDigest: strings.Repeat("a", 64),
		CredentialDigest: strings.Repeat("b", 64),
		Generation:       2,
		Process: &domain.ProcessEvidence{
			PID:                4102,
			BirthToken:         "birth-current",
			ExecutableIdentity: executable,
			ArgumentDigest:     strings.Repeat("c", 64),
		},
		CreatedAt: at,
		UpdatedAt: at,
	}
}
