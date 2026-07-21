package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// LogsCmd reads bounded current-session output for one project or Compose service.
type LogsCmd struct {
	ProjectID domain.ProjectID `arg:"" name:"project" help:"Registered Harbor project ID"`
	ServiceID domain.ServiceID `name:"service" help:"Project service ID (default: GoForj project output)"`
	Follow    bool             `help:"Wait for and print new output until interrupted"`

	client *DaemonClient
	output io.Writer
}

// NewLogsCmd creates a project log command without contacting the daemon.
func NewLogsCmd(client *DaemonClient) *LogsCmd {
	return &LogsCmd{client: client, output: os.Stdout}
}

// Signature defines CLI metadata for the project log command.
func (*LogsCmd) Signature() string {
	return `name:"logs" help:"Read current project or service logs"`
}

// Run reads the current session before opening a bounded project or service cursor.
func (command *LogsCmd) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := command.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project logs: %w", err)
	}
	snapshot, err := command.client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read Harbor snapshot: %w", err)
	}
	project, found := projectFromSnapshot(snapshot, command.ProjectID)
	if !found {
		return fmt.Errorf("project logs: project %q was not found", command.ProjectID)
	}

	service, err := selectProjectLogService(project, command.ServiceID)
	if err != nil {
		return err
	}
	if service == nil {
		return command.runProjectLogs(ctx)
	}
	return command.runServiceLogs(ctx, service.ID)
}

// runProjectLogs streams the current GoForj process output through its durable session cursor.
func (command *LogsCmd) runProjectLogs(ctx context.Context) error {
	activity, err := command.client.ProjectActivity(ctx, control.ProjectActivityRequest{
		ProjectID: command.ProjectID,
		Cursor:    0,
	})
	if err != nil {
		return fmt.Errorf("read project logs: %w", err)
	}
	session, err := currentProjectSession(activity)
	if err != nil {
		return err
	}
	if !session.Output.Available {
		return fmt.Errorf("project logs: current session output is unavailable (state %s)", session.State)
	}
	if err := writeProjectLogChunk(command.output, session.Output); err != nil {
		return err
	}
	if !command.Follow {
		return nil
	}

	cursor := session.Output.NextCursor
	sessionID := session.ID
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		activity, err := command.client.ProjectActivity(ctx, control.ProjectActivityRequest{
			ProjectID:        command.ProjectID,
			SessionID:        sessionID,
			Cursor:           cursor,
			WaitMilliseconds: control.MaximumProjectActivityWaitMilliseconds,
		})
		if err != nil {
			return fmt.Errorf("follow project logs: %w", err)
		}
		session, err = currentProjectSession(activity)
		if err != nil {
			return err
		}
		if session.ID != sessionID {
			cursor = 0
			sessionID = session.ID
		}
		if !session.Output.Available {
			return fmt.Errorf("project logs: current session output is unavailable (state %s)", session.State)
		}
		if err := writeProjectLogChunk(command.output, session.Output); err != nil {
			return err
		}
		cursor = session.Output.NextCursor
	}
}

// runServiceLogs streams one Compose service through the daemon-owned current-session cursor.
func (command *LogsCmd) runServiceLogs(ctx context.Context, serviceID domain.ServiceID) error {
	activity, err := command.client.ProjectActivity(ctx, control.ProjectActivityRequest{
		ProjectID: command.ProjectID,
		Cursor:    0,
	})
	if err != nil {
		return fmt.Errorf("read service logs session: %w", err)
	}
	session, err := currentProjectSession(activity)
	if err != nil {
		return err
	}

	sessionID := session.ID
	cursor := uint64(0)
	for {
		wait := uint32(0)
		if command.Follow {
			wait = control.MaximumServiceLogsWaitMilliseconds
		}
		logs, err := command.client.ServiceLogs(ctx, control.ServiceLogsRequest{
			ProjectID:        command.ProjectID,
			SessionID:        sessionID,
			ServiceID:        serviceID,
			Cursor:           cursor,
			WaitMilliseconds: wait,
		})
		if err != nil {
			return fmt.Errorf("read service logs: %w", err)
		}
		if err := validateAvailableServiceLogs(logs, command.ProjectID, serviceID); err != nil {
			return err
		}
		if logs.SessionID != "" && logs.SessionID != sessionID {
			sessionID = logs.SessionID
			cursor = 0
		}
		if err := writeServiceLogChunk(command.output, logs.Output); err != nil {
			return err
		}
		cursor = logs.Output.NextCursor
		if !command.Follow {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// currentProjectSession requires the daemon to return one current session before a cursor can be followed.
func currentProjectSession(activity control.ProjectActivity) (*control.ProjectSessionActivity, error) {
	if activity.Session == nil {
		return nil, errors.New("project logs: the project has no current session")
	}
	return activity.Session, nil
}

// selectProjectLogService resolves an optional stable service ID without accepting a service outside the project.
func selectProjectLogService(project domain.ProjectSnapshot, serviceID domain.ServiceID) (*domain.ServiceSnapshot, error) {
	if serviceID == "" {
		return nil, nil
	}
	if err := serviceID.Validate(); err != nil {
		return nil, fmt.Errorf("project logs: service ID: %w", err)
	}
	for index := range project.Services {
		service := &project.Services[index]
		if service.ID == serviceID || service.Name == string(serviceID) {
			return service, nil
		}
	}
	return nil, fmt.Errorf("project logs: service %q was not found in project %q", serviceID, project.ID)
}

// validateAvailableServiceLogs turns daemon availability and bounded problems into actionable CLI errors.
func validateAvailableServiceLogs(logs control.ServiceLogs, projectID domain.ProjectID, serviceID domain.ServiceID) error {
	if logs.ProjectID != projectID || logs.ServiceID != serviceID {
		return errors.New("read service logs: daemon returned output for another project or service")
	}
	if logs.Problem != nil {
		return fmt.Errorf("read service logs: %s", logs.Problem.Message)
	}
	if !logs.Supported {
		return errors.New("read service logs: the daemon does not support service logs")
	}
	if !logs.Available || !logs.Output.Available {
		return errors.New("read service logs: service output is unavailable")
	}
	return nil
}

// writeProjectLogChunk preserves daemon ordering and source formatting for terminal consumers.
func writeProjectLogChunk(output io.Writer, chunk control.ProjectOutputChunk) error {
	if _, err := io.WriteString(output, chunk.Text); err != nil {
		return fmt.Errorf("write project logs: %w", err)
	}
	return nil
}

// writeServiceLogChunk preserves daemon ordering and source formatting for terminal consumers.
func writeServiceLogChunk(output io.Writer, chunk control.ServiceLogOutputChunk) error {
	if _, err := io.WriteString(output, chunk.Text); err != nil {
		return fmt.Errorf("write service logs: %w", err)
	}
	return nil
}
