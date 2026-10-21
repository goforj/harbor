package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// retainedActivityTestState supplies daemon-selected history separately from absent process authority.
type retainedActivityTestState struct {
	projectActivityTestState
	history    *state.ProjectOutputHistory
	historyErr error
}

// RetainedProjectOutput never accepts a session selected by the client.
func (source *retainedActivityTestState) RetainedProjectOutput(context.Context, domain.ProjectID) (*state.ProjectOutputHistory, error) {
	return source.history, source.historyErr
}

// TestRetainedProjectActivityShowsFailedLogsWithoutProcessAuthority covers the missing-session production failure.
func TestRetainedProjectActivityShowsFailedLogsWithoutProcessAuthority(t *testing.T) {
	for _, scenario := range []string{"failed", "stopped", "missing", "wrong project", "stale", "bad generation", "bad session", "state error", "corrupt spool", "missing spool", "live spoof", "same cursor", "foreign cursor"} {
		t.Run(scenario, func(t *testing.T) {
			project := projectActivityTestProject()
			project.Project.State = domain.ProjectFailed
			if scenario == "stopped" {
				project.Project.State = domain.ProjectStopped
			}
			source := &retainedActivityTestState{projectActivityTestState: projectActivityTestState{project: project, sessionErr: &state.ProjectSessionNotFoundError{ProjectID: project.Project.ID}}, history: &state.ProjectOutputHistory{ProjectID: project.Project.ID, SessionID: "session-failed", Generation: 2, EndedAt: project.Project.UpdatedAt}}
			reader := &projectActivityHistoryTestReader{historyOutput: projectprocess.OutputChunk{Historical: true, Text: "Bind for 0.0.0.0:1025 failed: port is already allocated\n", NextCursor: 100}}
			request := ProjectActivityRequest{ProjectID: project.Project.ID}
			switch scenario {
			case "missing":
				source.history = nil
			case "wrong project":
				source.history.ProjectID = "project-foreign"
			case "stale":
				source.history.EndedAt = source.history.EndedAt.Add(time.Second)
			case "bad generation":
				source.history.Generation = 0
			case "bad session":
				source.history.SessionID = " invalid "
			case "state error":
				source.historyErr = errors.New("state unavailable")
			case "corrupt spool":
				reader.historyErr = errors.New("corrupt spool")
			case "missing spool":
				reader.historyOutput = projectprocess.OutputChunk{}
			case "live spoof":
				reader.historyOutput.Available = true
			case "same cursor":
				request.SessionID = "session-failed"
				request.Cursor = 50
			case "foreign cursor":
				request.SessionID = "session-foreign"
				request.Cursor = 50
			}
			got, err := readCurrentProjectActivity(t.Context(), source, reader, request)
			wantError := scenario == "state error" || scenario == "bad generation" || scenario == "bad session"
			if (err != nil) != wantError {
				t.Fatalf("activity = %#v, %v", got, err)
			}
			visible := scenario == "failed" || scenario == "stopped" || scenario == "same cursor" || scenario == "foreign cursor"
			if (got.Session != nil) != visible {
				t.Fatalf("activity = %#v", got)
			}
			if !visible {
				return
			}
			if got.Session.Output.Available || !got.Session.Output.Historical || got.Session.State != domain.SessionDisconnected {
				t.Fatal("retained bytes claimed live authority")
			}
			if len(reader.calls) != 1 || len(reader.waitCalls) != 0 || reader.calls[0].sessionID != "session-failed" {
				t.Fatalf("calls = %#v", reader.calls)
			}
			wantCursor := uint64(0)
			if scenario == "same cursor" {
				wantCursor = 50
			}
			if reader.calls[0].cursor != wantCursor || got.Session.Output.Reset != (scenario == "foreign cursor") {
				t.Fatal("client selected history or advanced a foreign cursor")
			}
		})
	}
}
