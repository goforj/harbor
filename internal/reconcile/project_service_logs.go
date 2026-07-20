package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

const maximumProjectServiceLogWait = 25 * time.Second

// ProjectServiceLogsRequest selects one current-session Compose service and bounded output cursor.
type ProjectServiceLogsRequest struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
	ServiceID domain.ServiceID
	Cursor    uint64
	Wait      time.Duration
}

// ProjectServiceLogs is one current-session view of a durable project's Compose service output.
type ProjectServiceLogs struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
	ServiceID domain.ServiceID
	Supported bool
	Available bool
	Problem   *projectprocess.ServiceLogProblem
	Output    projectprocess.OutputChunk
}

// ProjectServiceNotFoundError reports that a requested service is not present in the current project projection.
type ProjectServiceNotFoundError struct {
	ProjectID domain.ProjectID
	ServiceID domain.ServiceID
}

// Error describes the missing project-scoped service identity.
func (err *ProjectServiceNotFoundError) Error() string {
	return fmt.Sprintf("service %q was not found in project %q", err.ServiceID, err.ProjectID)
}

// projectServiceLogState limits service selection to current durable project and session facts.
type projectServiceLogState interface {
	Project(context.Context, domain.ProjectID) (state.ProjectRecord, error)
	ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error)
}

// projectServiceLogReader limits runtime access to one exact supervised session and Compose service.
type projectServiceLogReader interface {
	ReadServiceLogs(context.Context, domain.ProjectID, domain.SessionID, domain.ServiceID, uint64) (projectprocess.ServiceLogSelection, error)
	WaitServiceLogs(context.Context, domain.ProjectID, domain.SessionID, domain.ServiceID, uint64) (projectprocess.ServiceLogSelection, error)
}

// ServiceLogs reads only one selected Compose service in the current durable project session.
func (coordinator *ProjectLifecycleCoordinator) ServiceLogs(
	ctx context.Context,
	request ProjectServiceLogsRequest,
) (ProjectServiceLogs, error) {
	return readCurrentProjectServiceLogs(ctx, coordinator.state, coordinator.supervisor, request)
}

// readCurrentProjectServiceLogs keeps durable project, service, and session selection ahead of runtime access.
func readCurrentProjectServiceLogs(
	ctx context.Context,
	source projectServiceLogState,
	reader projectServiceLogReader,
	request ProjectServiceLogsRequest,
) (ProjectServiceLogs, error) {
	ctx = normalizeLifecycleContext(ctx)
	if err := validateProjectServiceLogsRequest(request); err != nil {
		return ProjectServiceLogs{}, err
	}
	project, err := source.Project(ctx, request.ProjectID)
	if err != nil {
		return ProjectServiceLogs{}, err
	}
	if project.Project.ID != request.ProjectID {
		return ProjectServiceLogs{}, errors.New("current project record does not match the selected project")
	}
	if err := validateSelectedComposeService(project.Project, request.ServiceID); err != nil {
		return ProjectServiceLogs{}, err
	}

	base := ProjectServiceLogs{
		ProjectID: request.ProjectID,
		ServiceID: request.ServiceID,
		Supported: true,
	}
	session, err := source.ActiveProjectSession(ctx, request.ProjectID)
	if err != nil {
		var missing *state.ProjectSessionNotFoundError
		if errors.As(err, &missing) {
			return base, nil
		}
		return ProjectServiceLogs{}, err
	}
	if err := session.Validate(); err != nil {
		return ProjectServiceLogs{}, fmt.Errorf("validate current project session: %w", err)
	}
	if session.ProjectID != request.ProjectID {
		return ProjectServiceLogs{}, errors.New("current project session does not match the selected project")
	}
	base.SessionID = session.ID

	cursor := request.Cursor
	changedSession := request.SessionID != "" && request.SessionID != session.ID
	if changedSession {
		cursor = 0
	}
	if request.Wait > 0 && request.SessionID == session.ID {
		waitContext, cancel := context.WithTimeout(ctx, request.Wait)
		waited, waitErr := reader.WaitServiceLogs(
			waitContext,
			request.ProjectID,
			session.ID,
			request.ServiceID,
			cursor,
		)
		cancel()
		if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ProjectServiceLogs{}, ctxErr
			}
			return ProjectServiceLogs{}, fmt.Errorf("wait for current project service logs: %w", waitErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProjectServiceLogs{}, ctxErr
		}

		// Durable reselection prevents a stopped and restarted project from crossing process transcripts.
		request.Wait = 0
		current, err := readCurrentProjectServiceLogs(ctx, source, reader, request)
		if err != nil {
			return ProjectServiceLogs{}, err
		}
		if waitErr == nil && current.SessionID == session.ID {
			return projectServiceLogsFromSelection(base, waited, false), nil
		}
		return current, nil
	}
	selection, err := reader.ReadServiceLogs(
		ctx,
		request.ProjectID,
		session.ID,
		request.ServiceID,
		cursor,
	)
	if err != nil {
		return ProjectServiceLogs{}, fmt.Errorf("read current project service logs: %w", err)
	}
	return projectServiceLogsFromSelection(base, selection, changedSession), nil
}

// projectServiceLogsFromSelection copies optional runtime state while preserving durable identities.
func projectServiceLogsFromSelection(
	base ProjectServiceLogs,
	selection projectprocess.ServiceLogSelection,
	changedSession bool,
) ProjectServiceLogs {
	base.Supported = selection.Supported
	base.Available = selection.Available
	base.Problem = selection.Problem
	base.Output = selection.Output
	if changedSession {
		base.Output.Reset = true
	}
	return base
}

// validateSelectedComposeService rejects unknown and externally owned service identities before host runtime access.
func validateSelectedComposeService(project domain.ProjectSnapshot, serviceID domain.ServiceID) error {
	for _, service := range project.Services {
		if service.ID != serviceID {
			continue
		}
		if service.Owner != domain.ServiceOwnerCompose {
			return fmt.Errorf("service %q is not owned by the project's Compose lifecycle", serviceID)
		}
		return nil
	}
	return &ProjectServiceNotFoundError{ProjectID: project.ID, ServiceID: serviceID}
}

// validateProjectServiceLogsRequest rejects history selection and non-portable cursors before durable reads.
func validateProjectServiceLogsRequest(request ProjectServiceLogsRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.ServiceID.Validate(); err != nil {
		return err
	}
	if request.Wait < 0 || request.Wait > maximumProjectServiceLogWait {
		return fmt.Errorf("project service log wait must be between 0 and %s", maximumProjectServiceLogWait)
	}
	if request.SessionID == "" {
		if request.Cursor != 0 {
			return errors.New("project service log cursor requires a session ID")
		}
		return nil
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if request.Cursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("project service log cursor exceeds %d", domain.MaximumSequence)
	}
	return nil
}
