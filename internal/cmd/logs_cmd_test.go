package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

// newLogsCommandFixture creates one logs command around a fake daemon and captured terminal output.
func newLogsCommandFixture(t *testing.T, connection *fakeDaemonControlClient, withService bool) (*LogsCmd, *bytes.Buffer) {
	t.Helper()
	project := addTestRegistration(t.TempDir(), true).Project
	project.State = domain.ProjectReady
	if withService {
		project.Services = []domain.ServiceSnapshot{{
			ID:        "mysql",
			Name:      "mysql",
			Kind:      "database",
			State:     domain.EntityReady,
			Owner:     domain.ServiceOwnerCompose,
			Selection: domain.ServiceSelected,
			Required:  true,
		}}
	}
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	connection.snapshot = snapshot
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewLogsCmd(client)
	command.ProjectID = project.ID
	output := &bytes.Buffer{}
	command.output = output
	return command, output
}

// projectActivityLogFixture returns one valid current-session project output chunk.
func projectActivityLogFixture(text string) control.ProjectActivity {
	return control.ProjectActivity{
		ProjectID: "project-orders",
		Session: &control.ProjectSessionActivity{
			ID:         "session-orders",
			State:      domain.SessionAttached,
			Generation: 1,
			Output: control.ProjectOutputChunk{
				Available:  true,
				NextCursor: uint64(len(text)),
				Text:       text,
			},
		},
	}
}

// serviceLogsFixture returns one valid current-session Compose service output chunk.
func serviceLogsFixture(text string) control.ServiceLogs {
	return control.ServiceLogs{
		ProjectID: "project-orders",
		ServiceID: "mysql",
		SessionID: "session-orders",
		Supported: true,
		Available: true,
		Output: control.ServiceLogOutputChunk{
			Available:  true,
			NextCursor: uint64(len(text)),
			Text:       text,
		},
		Ports: []control.ServicePort{{
			Private:  3306,
			Protocol: "tcp",
			Replica:  1,
		}},
	}
}

// TestLogsCommandPrintsProjectOutput verifies the default path remains one bounded read with no local inference.
func TestLogsCommandPrintsProjectOutput(t *testing.T) {
	connection := &fakeDaemonControlClient{projectActivity: projectActivityLogFixture("boot\nready\n")}
	command, output := newLogsCommandFixture(t, connection, false)

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run project logs: %v", err)
	}
	if output.String() != "boot\nready\n" {
		t.Fatalf("output = %q", output.String())
	}
	if connection.snapshotCalls != 1 || connection.projectActivityCalls != 1 || connection.closeCalls != 2 {
		t.Fatalf("calls = snapshot:%d activity:%d close:%d", connection.snapshotCalls, connection.projectActivityCalls, connection.closeCalls)
	}
	if got := connection.projectActivityRequests[0]; got != (control.ProjectActivityRequest{ProjectID: "project-orders", Cursor: 0}) {
		t.Fatalf("activity request = %#v", got)
	}
}

// TestLogsCommandSelectsProjectServiceByStableID verifies service output cannot escape the selected project scope.
func TestLogsCommandSelectsProjectServiceByStableID(t *testing.T) {
	connection := &fakeDaemonControlClient{
		projectActivity: projectActivityLogFixture("ignored project output\n"),
		serviceLogs:     serviceLogsFixture("mysql ready\n"),
	}
	command, output := newLogsCommandFixture(t, connection, true)
	command.ServiceID = "mysql"

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run service logs: %v", err)
	}
	if output.String() != "mysql ready\n" {
		t.Fatalf("output = %q", output.String())
	}
	if connection.projectActivityCalls != 1 || connection.serviceLogsCalls != 1 || connection.closeCalls != 3 {
		t.Fatalf("calls = activity:%d service:%d close:%d", connection.projectActivityCalls, connection.serviceLogsCalls, connection.closeCalls)
	}
	want := control.ServiceLogsRequest{ProjectID: "project-orders", SessionID: "session-orders", ServiceID: "mysql"}
	if got := connection.serviceLogsRequests[0]; got != want {
		t.Fatalf("service request = %#v, want %#v", got, want)
	}
}

// TestLogsCommandFollowUsesHeldCursorAndStopsOnContext verifies follow never rewinds output and remains interruptible.
func TestLogsCommandFollowUsesHeldCursorAndStopsOnContext(t *testing.T) {
	connection := &fakeDaemonControlClient{}
	command, output := newLogsCommandFixture(t, connection, false)
	command.Follow = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	call := 0
	connection.projectActivityHook = func(_ context.Context, request control.ProjectActivityRequest) (control.ProjectActivity, error) {
		call++
		switch call {
		case 1:
			return projectActivityLogFixture("first\n"), nil
		case 2:
			if request.SessionID != "session-orders" || request.Cursor != uint64(len("first\n")) || request.WaitMilliseconds != control.MaximumProjectActivityWaitMilliseconds {
				t.Fatalf("follow request = %#v", request)
			}
			cancel()
			next := projectActivityLogFixture("second\n")
			next.Session.ID = "session-replaced"
			next.Session.Output.Reset = true
			return next, nil
		default:
			return control.ProjectActivity{}, errors.New("unexpected third activity read")
		}
	}

	err := command.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("follow error = %v, want context cancellation", err)
	}
	if output.String() != "first\nsecond\n" {
		t.Fatalf("follow output = %q", output.String())
	}
	if connection.projectActivityCalls != 2 {
		t.Fatalf("activity calls = %d, want 2", connection.projectActivityCalls)
	}
}

// TestLogsCommandFollowResetsServiceCursorOnSessionReplacement verifies service output never crosses lifecycle generations.
func TestLogsCommandFollowResetsServiceCursorOnSessionReplacement(t *testing.T) {
	connection := &fakeDaemonControlClient{}
	command, output := newLogsCommandFixture(t, connection, true)
	command.ServiceID = "mysql"
	command.Follow = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	call := 0
	connection.projectActivity = projectActivityLogFixture("ignored\n")
	connection.serviceLogsHook = func(_ context.Context, request control.ServiceLogsRequest) (control.ServiceLogs, error) {
		call++
		switch call {
		case 1:
			if request.SessionID != "session-orders" || request.Cursor != 0 || request.WaitMilliseconds != control.MaximumServiceLogsWaitMilliseconds {
				t.Fatalf("initial service follow request = %#v", request)
			}
			return serviceLogsFixture("first\n"), nil
		case 2:
			if request.SessionID != "session-orders" || request.Cursor != uint64(len("first\n")) || request.WaitMilliseconds != control.MaximumServiceLogsWaitMilliseconds {
				t.Fatalf("replacement service follow request = %#v", request)
			}
			cancel()
			next := serviceLogsFixture("second\n")
			next.SessionID = "session-replaced"
			next.Output.Reset = true
			return next, nil
		default:
			return control.ServiceLogs{}, errors.New("unexpected third service log read")
		}
	}

	err := command.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("service follow error = %v, want context cancellation", err)
	}
	if output.String() != "first\nsecond\n" {
		t.Fatalf("service follow output = %q", output.String())
	}
	if connection.serviceLogsCalls != 2 {
		t.Fatalf("service log calls = %d, want 2", connection.serviceLogsCalls)
	}
}

// TestLogsCommandRejectsUnknownServiceBeforeOpeningActivity verifies service selection is project-scoped and read-only.
func TestLogsCommandRejectsUnknownServiceBeforeOpeningActivity(t *testing.T) {
	connection := &fakeDaemonControlClient{}
	command, output := newLogsCommandFixture(t, connection, true)
	command.ServiceID = "redis"

	err := command.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), `service "redis" was not found`) {
		t.Fatalf("unknown service error = %v", err)
	}
	if output.Len() != 0 || connection.projectActivityCalls != 0 || connection.closeCalls != 1 {
		t.Fatalf("unknown service side effects: output=%q activity=%d close=%d", output.String(), connection.projectActivityCalls, connection.closeCalls)
	}
}

// TestLogsCommandKongSurfaceAcceptsProjectAndFollowFlags verifies the documented top-level CLI shape.
func TestLogsCommandKongSurfaceAcceptsProjectAndFollowFlags(t *testing.T) {
	connection := &fakeDaemonControlClient{projectActivity: projectActivityLogFixture("ready\n")}
	command, _ := newLogsCommandFixture(t, connection, false)
	root := struct {
		Logs LogsCmd `cmd:""`
	}{Logs: *command}
	parser, err := kong.New(&root)
	if err != nil {
		t.Fatalf("create parser: %v", err)
	}
	parsed, err := parser.Parse([]string{"logs", "project-orders", "--service", "mysql", "--follow"})
	if err != nil {
		t.Fatalf("parse logs: %v", err)
	}
	if !strings.HasPrefix(parsed.Command(), "logs") {
		t.Fatalf("parsed command = %q", parsed.Command())
	}
}
