package reconcile

import (
	"context"
	"errors"
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
	output projectprocess.OutputChunk
	calls  []projectActivityOutputCall
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
			ExecutableIdentity: "/usr/bin/forj",
			ArgumentDigest:     strings.Repeat("c", 64),
		},
		CreatedAt: at,
		UpdatedAt: at,
	}
}
