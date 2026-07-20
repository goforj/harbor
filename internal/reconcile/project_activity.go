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

const maximumProjectActivityWait = 25 * time.Second

// ProjectActivityRequest selects the current durable session and one bounded output cursor.
type ProjectActivityRequest struct {
	ProjectID domain.ProjectID
	SessionID domain.SessionID
	Cursor    uint64
	Wait      time.Duration
}

// ProjectActivity is the current session view for one registered project.
type ProjectActivity struct {
	ProjectID domain.ProjectID
	Session   *ProjectSessionActivity
}

// ProjectSessionActivity contains only current durable session state and daemon-owned output.
type ProjectSessionActivity struct {
	ID         domain.SessionID
	State      domain.SessionState
	Generation uint64
	Output     projectprocess.OutputChunk
}

// projectActivityState limits the read path to current registration and session facts.
type projectActivityState interface {
	Project(context.Context, domain.ProjectID) (state.ProjectRecord, error)
	ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error)
}

// projectActivityOutputReader limits process access to an exact identity and opaque cursor.
type projectActivityOutputReader interface {
	ReadOutput(domain.ProjectID, domain.SessionID, uint64) projectprocess.OutputChunk
	WaitOutput(context.Context, domain.ProjectID, domain.SessionID, uint64) (projectprocess.OutputChunk, error)
}

// ProjectActivity reads only the current durable session and its exact supervised process transcript.
func (coordinator *ProjectLifecycleCoordinator) ProjectActivity(
	ctx context.Context,
	request ProjectActivityRequest,
) (ProjectActivity, error) {
	return readCurrentProjectActivity(ctx, coordinator.state, coordinator.supervisor, request)
}

// readCurrentProjectActivity keeps durable selection ahead of any in-memory process access.
func readCurrentProjectActivity(
	ctx context.Context,
	source projectActivityState,
	reader projectActivityOutputReader,
	request ProjectActivityRequest,
) (ProjectActivity, error) {
	ctx = normalizeLifecycleContext(ctx)
	if err := validateProjectActivityRequest(request); err != nil {
		return ProjectActivity{}, err
	}
	project, err := source.Project(ctx, request.ProjectID)
	if err != nil {
		return ProjectActivity{}, err
	}
	if project.Project.ID != request.ProjectID {
		return ProjectActivity{}, errors.New("current project record does not match the selected project")
	}

	session, err := source.ActiveProjectSession(ctx, request.ProjectID)
	if err != nil {
		var missing *state.ProjectSessionNotFoundError
		if errors.As(err, &missing) {
			return ProjectActivity{ProjectID: request.ProjectID}, nil
		}
		return ProjectActivity{}, err
	}
	if err := session.Validate(); err != nil {
		return ProjectActivity{}, fmt.Errorf("validate current project session: %w", err)
	}
	if session.ProjectID != request.ProjectID {
		return ProjectActivity{}, errors.New("current project session does not match the selected project")
	}

	cursor := request.Cursor
	changedSession := request.SessionID != "" && request.SessionID != session.ID
	if changedSession {
		cursor = 0
	}
	if request.Wait > 0 && request.SessionID == session.ID {
		waitContext, cancel := context.WithTimeout(ctx, request.Wait)
		waitedOutput, waitErr := reader.WaitOutput(waitContext, request.ProjectID, session.ID, cursor)
		cancel()
		if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ProjectActivity{}, ctxErr
			}
			return ProjectActivity{}, fmt.Errorf("wait for current project activity: %w", waitErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProjectActivity{}, ctxErr
		}

		// A held request reselects durable state so session replacement cannot return a stale projection.
		request.Wait = 0
		activity, err := readCurrentProjectActivity(ctx, source, reader, request)
		if err != nil {
			return ProjectActivity{}, err
		}
		if waitErr == nil && waitedOutput.Available && activity.Session != nil && activity.Session.ID == session.ID {
			activity.Session.Output = waitedOutput
		}
		return activity, nil
	}
	output := reader.ReadOutput(request.ProjectID, session.ID, cursor)
	if changedSession {
		output.Reset = true
	}
	return ProjectActivity{
		ProjectID: request.ProjectID,
		Session: &ProjectSessionActivity{
			ID:         session.ID,
			State:      session.State,
			Generation: session.Generation,
			Output:     output,
		},
	}, nil
}

// validateProjectActivityRequest rejects history selection and non-portable cursors before durable reads.
func validateProjectActivityRequest(request ProjectActivityRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if request.Wait < 0 || request.Wait > maximumProjectActivityWait {
		return fmt.Errorf("project activity wait must be between 0 and %s", maximumProjectActivityWait)
	}
	if request.SessionID == "" {
		if request.Cursor != 0 {
			return errors.New("project activity cursor requires a session ID")
		}
		return nil
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if request.Cursor > uint64(domain.MaximumSequence) {
		return fmt.Errorf("project activity cursor exceeds %d", domain.MaximumSequence)
	}
	return nil
}
