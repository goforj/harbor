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

// projectServiceLogTestState supplies one current service projection and optional active session.
type projectServiceLogTestState struct {
	project    state.ProjectRecord
	session    domain.ProjectSession
	projectErr error
	sessionErr error
}

// Project returns the configured durable project projection.
func (source *projectServiceLogTestState) Project(context.Context, domain.ProjectID) (state.ProjectRecord, error) {
	return source.project, source.projectErr
}

// ActiveProjectSession returns the configured current session.
func (source *projectServiceLogTestState) ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error) {
	return source.session, source.sessionErr
}

// projectServiceLogCall records one exact runtime selection.
type projectServiceLogCall struct {
	projectID domain.ProjectID
	sessionID domain.SessionID
	serviceID domain.ServiceID
	cursor    uint64
}

// projectServiceLogTestReader returns configured immediate and held selections.
type projectServiceLogTestReader struct {
	selection     projectprocess.ServiceLogSelection
	waitSelection projectprocess.ServiceLogSelection
	waitErr       error
	wait          func()
	reads         []projectServiceLogCall
	waits         []projectServiceLogCall
}

// ReadServiceLogs records one immediate selection.
func (reader *projectServiceLogTestReader) ReadServiceLogs(
	_ context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	serviceID domain.ServiceID,
	cursor uint64,
) (projectprocess.ServiceLogSelection, error) {
	reader.reads = append(reader.reads, projectServiceLogCall{projectID, sessionID, serviceID, cursor})
	return reader.selection, nil
}

// WaitServiceLogs records one held selection and optional durable-state transition.
func (reader *projectServiceLogTestReader) WaitServiceLogs(
	_ context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
	serviceID domain.ServiceID,
	cursor uint64,
) (projectprocess.ServiceLogSelection, error) {
	reader.waits = append(reader.waits, projectServiceLogCall{projectID, sessionID, serviceID, cursor})
	if reader.wait != nil {
		reader.wait()
	}
	return reader.waitSelection, reader.waitErr
}

// TestProjectServiceLogsSelectsCurrentSessionAndResetsStaleCursor prevents old process output from crossing a restart.
func TestProjectServiceLogsSelectsCurrentSessionAndResetsStaleCursor(t *testing.T) {
	source := projectServiceLogStateFixture()
	reader := &projectServiceLogTestReader{selection: projectprocess.ServiceLogSelection{
		Supported: true,
		Available: true,
		Output: projectprocess.OutputChunk{
			Available: true, NextCursor: 6, Text: "ready\n",
		},
	}}
	logs, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
		ProjectID: "project-orders", SessionID: "session-prior", ServiceID: "mysql", Cursor: 900, Wait: time.Second,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectServiceLogs() error = %v", err)
	}
	want := projectServiceLogCall{"project-orders", "session-current", "mysql", 0}
	if logs.SessionID != "session-current" || !logs.Output.Reset || len(reader.reads) != 1 || reader.reads[0] != want || len(reader.waits) != 0 {
		t.Fatalf("logs/reads/waits = %#v / %#v / %#v", logs, reader.reads, reader.waits)
	}
}

// TestProjectServiceLogsEmptySessionReturnsConcreteCurrentIdentity supports first reads before the UI owns a cursor.
func TestProjectServiceLogsEmptySessionReturnsConcreteCurrentIdentity(t *testing.T) {
	source := projectServiceLogStateFixture()
	reader := &projectServiceLogTestReader{selection: projectprocess.ServiceLogSelection{Supported: true}}
	logs, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
		ProjectID: "project-orders", ServiceID: "mysql",
	})
	if err != nil || logs.SessionID != "session-current" || len(reader.reads) != 1 || reader.reads[0].cursor != 0 {
		t.Fatalf("logs/reads = %#v / %#v, %v", logs, reader.reads, err)
	}
}

// TestProjectServiceLogsResetsAStaleSessionBeforeContainersAppear keeps unavailable retries on the new process generation.
func TestProjectServiceLogsResetsAStaleSessionBeforeContainersAppear(t *testing.T) {
	source := projectServiceLogStateFixture()
	reader := &projectServiceLogTestReader{selection: projectprocess.ServiceLogSelection{Supported: true}}
	logs, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
		ProjectID: "project-orders", SessionID: "session-prior", ServiceID: "mysql", Cursor: 22,
	})
	if err != nil || logs.SessionID != "session-current" || !logs.Output.Reset || logs.Output.Available {
		t.Fatalf("logs = %#v, %v", logs, err)
	}
}

// TestProjectServiceLogsWaitReselectsDurableSession prevents a held request from returning a retired process generation.
func TestProjectServiceLogsWaitReselectsDurableSession(t *testing.T) {
	source := projectServiceLogStateFixture()
	reader := &projectServiceLogTestReader{selection: projectprocess.ServiceLogSelection{
		Supported: true,
		Available: true,
		Output:    projectprocess.OutputChunk{Available: true, NextCursor: 4, Text: "next"},
	}}
	reader.wait = func() {
		source.session.ID = "session-next"
		source.session.Generation++
	}
	logs, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
		ProjectID: "project-orders", SessionID: "session-current", ServiceID: "mysql", Cursor: 44, Wait: time.Second,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectServiceLogs() error = %v", err)
	}
	if logs.SessionID != "session-next" || !logs.Output.Reset || len(reader.waits) != 1 || len(reader.reads) != 1 || reader.reads[0].cursor != 0 {
		t.Fatalf("logs/reads/waits = %#v / %#v / %#v", logs, reader.reads, reader.waits)
	}
}

// TestProjectServiceLogsWaitReturnsUnavailableDuringProjectionGap preserves a current follower while event refresh rebuilds its service row.
func TestProjectServiceLogsWaitReturnsUnavailableDuringProjectionGap(t *testing.T) {
	source := projectServiceLogStateFixture()
	reader := &projectServiceLogTestReader{selection: projectprocess.ServiceLogSelection{
		Supported: true,
		Available: true,
		Output:    projectprocess.OutputChunk{Available: true, NextCursor: 30, Text: "stale\n"},
	}}
	reader.waitSelection = reader.selection
	reader.wait = func() {
		source.project.Project.Services = []domain.ServiceSnapshot{
			{ID: "mail", Name: "Mail", Kind: "mail", State: domain.EntityReady, Owner: domain.ServiceOwnerExternal, Selection: domain.ServiceSelected},
		}
	}
	request := ProjectServiceLogsRequest{
		ProjectID: "project-orders", SessionID: "session-current", ServiceID: "mysql", Cursor: 17, Wait: time.Second,
	}
	logs, err := readCurrentProjectServiceLogs(t.Context(), source, reader, request)
	if err != nil {
		t.Fatalf("readCurrentProjectServiceLogs() error = %v", err)
	}
	if logs.SessionID != request.SessionID || logs.Available || logs.Output.Available || logs.Output.Text != "" || logs.Output.Reset {
		t.Fatalf("logs = %#v, want same-session unavailable response without output reset", logs)
	}
	if len(reader.waits) != 1 || len(reader.reads) != 0 {
		t.Fatalf("waits/reads = %#v / %#v, want one wait and no runtime reselection during gap", reader.waits, reader.reads)
	}

	source.project.Project.Services = append(source.project.Project.Services, domain.ServiceSnapshot{
		ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
	})
	recovered, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
		ProjectID: request.ProjectID, SessionID: request.SessionID, ServiceID: request.ServiceID, Cursor: 17,
	})
	if err != nil {
		t.Fatalf("readCurrentProjectServiceLogs() after re-add error = %v", err)
	}
	if recovered.Available != reader.selection.Available || len(reader.reads) != 1 || reader.reads[0].cursor != 17 {
		t.Fatalf("recovered logs/reads = %#v / %#v, want cursor-preserving retry", recovered, reader.reads)
	}
}

// TestProjectServiceLogsProjectionGapDoesNotCrossSession keeps a disappearing service from authorizing a retired session.
func TestProjectServiceLogsProjectionGapDoesNotCrossSession(t *testing.T) {
	source := projectServiceLogStateFixture()
	reader := &projectServiceLogTestReader{selection: projectprocess.ServiceLogSelection{Supported: true}}
	reader.wait = func() {
		source.project.Project.Services = []domain.ServiceSnapshot{
			{ID: "mail", Name: "Mail", Kind: "mail", State: domain.EntityReady, Owner: domain.ServiceOwnerExternal, Selection: domain.ServiceSelected},
		}
		source.session.ID = "session-next"
		source.session.Generation++
	}
	_, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
		ProjectID: "project-orders", SessionID: "session-current", ServiceID: "mysql", Cursor: 4, Wait: time.Second,
	})
	var missing *ProjectServiceNotFoundError
	if !errors.As(err, &missing) || len(reader.reads) != 0 {
		t.Fatalf("error/reads = %v / %#v, want missing-service fence and no runtime read", err, reader.reads)
	}
}

// TestProjectServiceLogsReturnsCleanUnavailableWithoutActiveSession avoids selecting historical runtime output.
func TestProjectServiceLogsReturnsCleanUnavailableWithoutActiveSession(t *testing.T) {
	source := projectServiceLogStateFixture()
	source.sessionErr = &state.ProjectSessionNotFoundError{ProjectID: "project-orders"}
	reader := new(projectServiceLogTestReader)
	logs, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
		ProjectID: "project-orders", ServiceID: "mysql",
	})
	if err != nil || logs.SessionID != "" || !logs.Supported || logs.Available || len(reader.reads) != 0 {
		t.Fatalf("logs/reads = %#v / %#v, %v", logs, reader.reads, err)
	}
}

// TestProjectServiceLogsRejectsUnknownAndExternalServicesBeforeRuntimeAccess protects the checkout-scoped runtime boundary.
func TestProjectServiceLogsRejectsUnknownAndExternalServicesBeforeRuntimeAccess(t *testing.T) {
	for _, test := range []struct {
		name      string
		serviceID domain.ServiceID
	}{
		{name: "missing", serviceID: "redis"},
		{name: "external", serviceID: "mail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := projectServiceLogStateFixture()
			reader := new(projectServiceLogTestReader)
			_, err := readCurrentProjectServiceLogs(t.Context(), source, reader, ProjectServiceLogsRequest{
				ProjectID: "project-orders", ServiceID: test.serviceID,
			})
			if err == nil {
				t.Fatal("readCurrentProjectServiceLogs() error = nil")
			}
			if test.name == "missing" {
				var missing *ProjectServiceNotFoundError
				if !errors.As(err, &missing) {
					t.Fatalf("error = %v, want ProjectServiceNotFoundError", err)
				}
			}
			if len(reader.reads) != 0 || len(reader.waits) != 0 {
				t.Fatalf("invalid service reached runtime: %#v / %#v", reader.reads, reader.waits)
			}
		})
	}
}

// projectServiceLogStateFixture creates one current Compose service and one externally observed service.
func projectServiceLogStateFixture() *projectServiceLogTestState {
	project := projectActivityTestProject()
	project.Project.Services = []domain.ServiceSnapshot{
		{ID: "mail", Name: "Mail", Kind: "mail", State: domain.EntityReady, Owner: domain.ServiceOwnerExternal, Selection: domain.ServiceSelected},
		{ID: "mysql", Name: "MySQL", Kind: "compose", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected},
	}
	return &projectServiceLogTestState{project: project, session: projectActivityTestSession()}
}
