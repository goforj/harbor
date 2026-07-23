package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
)

// daemonTestStatus returns one complete status fixture shared by command and client tests.
func daemonTestStatus() control.DaemonStatus {
	return control.DaemonStatus{
		State: control.DaemonStateReady,
		Build: control.Build{
			Version:  "v1.2.3",
			Revision: "abc123",
			Modified: true,
		},
		Protocol:              rpc.Version{Major: 1, Minor: 0},
		Capabilities:          []rpc.Capability{control.CapabilityV1, "logs.v1"},
		SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:              42,
	}
}

// daemonTestSnapshot returns a canonical empty-state snapshot for exact JSON assertions.
func daemonTestSnapshot() domain.Snapshot {
	return domain.Snapshot{
		SchemaVersion:     domain.SnapshotSchemaVersion,
		Sequence:          42,
		CapturedAt:        time.Date(2026, time.July, 18, 10, 30, 0, 0, time.UTC),
		Projects:          []domain.ProjectSnapshot{},
		Operations:        []domain.Operation{},
		RecentResourceIDs: []domain.ResourceRef{},
	}
}

// newDaemonCommandFixture creates both leaf commands around one observable connection.
func newDaemonCommandFixture(connection *fakeDaemonControlClient) (*DaemonStatusCmd, *DaemonSnapshotCmd, *bytes.Buffer, *bytes.Buffer) {
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})
	statusOutput := &bytes.Buffer{}
	snapshotOutput := &bytes.Buffer{}
	status := NewDaemonStatusCmd(client)
	status.output = statusOutput
	snapshot := NewDaemonSnapshotCmd(client)
	snapshot.output = snapshotOutput
	return status, snapshot, statusOutput, snapshotOutput
}

// newDaemonStopCommandFixture creates one daemon stop command around an observable connection.
func newDaemonStopCommandFixture(connection *fakeDaemonControlClient) (*DaemonStopCmd, *bytes.Buffer) {
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
		return connection, nil
	})
	output := &bytes.Buffer{}
	stop := NewDaemonStopCmd(client)
	stop.output = output
	return stop, output
}

// TestDaemonStopCommandPrintsAcknowledgedShutdown verifies success is visible only after one control call completes.
func TestDaemonStopCommandPrintsAcknowledgedShutdown(t *testing.T) {
	connection := &fakeDaemonControlClient{}
	stop, output := newDaemonStopCommandFixture(connection)

	if err := stop.Run(t.Context()); err != nil {
		t.Fatalf("run daemon stop: %v", err)
	}
	if output.String() != "Harbor daemon is stopping. Stable Harbor endpoints will be unavailable until it starts again.\n" {
		t.Fatalf("output = %q, want acknowledged shutdown", output.String())
	}
	if connection.stopCalls != 1 || connection.closeCalls != 1 {
		t.Fatalf("calls = stop:%d close:%d, want 1 each", connection.stopCalls, connection.closeCalls)
	}
}

// TestDaemonStatusCommandPrintsStableHumanOutput verifies the default output remains concise and script-readable by line.
func TestDaemonStatusCommandPrintsStableHumanOutput(t *testing.T) {
	connection := &fakeDaemonControlClient{status: daemonTestStatus()}
	status, _, output, _ := newDaemonCommandFixture(connection)

	if err := status.Run(t.Context()); err != nil {
		t.Fatalf("run daemon status: %v", err)
	}
	want := strings.Join([]string{
		"State: ready",
		"Version: v1.2.3",
		"Revision: abc123",
		"Modified: yes",
		"Protocol: 1.0",
		"Snapshot schema: 1",
		"Sequence: 42",
		"Capabilities: control.v1, logs.v1",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", connection.closeCalls)
	}
}

// TestDaemonStatusCommandExplainsUnavailableRevision verifies missing build metadata has an explicit human label.
func TestDaemonStatusCommandExplainsUnavailableRevision(t *testing.T) {
	daemonStatus := daemonTestStatus()
	daemonStatus.Build.Revision = ""
	daemonStatus.Build.Modified = false
	connection := &fakeDaemonControlClient{status: daemonStatus}
	status, _, output, _ := newDaemonCommandFixture(connection)

	if err := status.Run(t.Context()); err != nil {
		t.Fatalf("run daemon status: %v", err)
	}
	if !strings.Contains(output.String(), "Revision: unavailable\nModified: no\n") {
		t.Fatalf("output did not explain unavailable revision and clean build:\n%s", output.String())
	}
}

// TestDaemonStatusCommandPrintsTypedJSON verifies --json exposes DaemonStatus itself without a CLI wrapper.
func TestDaemonStatusCommandPrintsTypedJSON(t *testing.T) {
	connection := &fakeDaemonControlClient{status: daemonTestStatus()}
	status, _, output, _ := newDaemonCommandFixture(connection)
	status.JSON = true

	if err := status.Run(t.Context()); err != nil {
		t.Fatalf("run daemon status JSON: %v", err)
	}
	want := strings.Join([]string{
		"{",
		`  "state": "ready",`,
		`  "build": {`,
		`    "version": "v1.2.3",`,
		`    "revision": "abc123",`,
		`    "modified": true`,
		`  },`,
		`  "protocol": {`,
		`    "major": 1,`,
		`    "minor": 0`,
		`  },`,
		`  "capabilities": [`,
		`    "control.v1",`,
		`    "logs.v1"`,
		`  ],`,
		`  "snapshot_schema_version": 1,`,
		`  "sequence": 42`,
		"}",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

// TestDaemonSnapshotCommandPrintsCanonicalJSON verifies the snapshot object is not nested under a command-specific key.
func TestDaemonSnapshotCommandPrintsCanonicalJSON(t *testing.T) {
	connection := &fakeDaemonControlClient{snapshot: daemonTestSnapshot()}
	_, snapshot, _, output := newDaemonCommandFixture(connection)

	if err := snapshot.Run(t.Context()); err != nil {
		t.Fatalf("run daemon snapshot: %v", err)
	}
	want := strings.Join([]string{
		"{",
		`  "schema_version": 1,`,
		`  "sequence": 42,`,
		`  "captured_at": "2026-07-18T10:30:00Z",`,
		`  "projects": [],`,
		`  "operations": [],`,
		`  "recent_resource_ids": []`,
		"}",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", connection.closeCalls)
	}
}

// failingDaemonWriter makes output failures deterministic after the daemon connection has closed.
type failingDaemonWriter struct {
	err error
}

// Write returns the configured terminal output failure.
func (writer failingDaemonWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

// TestDaemonCommandsReturnOutputFailuresAfterClosing verifies terminal and JSON rendering cannot leak a connection.
func TestDaemonCommandsReturnOutputFailuresAfterClosing(t *testing.T) {
	writeErr := errors.New("write failed")

	for _, test := range []struct {
		name string
		run  func(*fakeDaemonControlClient) error
	}{
		{
			name: "stop",
			run: func(connection *fakeDaemonControlClient) error {
				stop, _ := newDaemonStopCommandFixture(connection)
				stop.output = failingDaemonWriter{err: writeErr}
				return stop.Run(t.Context())
			},
		},
		{
			name: "human status",
			run: func(connection *fakeDaemonControlClient) error {
				status, _, _, _ := newDaemonCommandFixture(connection)
				status.output = failingDaemonWriter{err: writeErr}
				return status.Run(t.Context())
			},
		},
		{
			name: "status JSON",
			run: func(connection *fakeDaemonControlClient) error {
				status, _, _, _ := newDaemonCommandFixture(connection)
				status.JSON = true
				status.output = failingDaemonWriter{err: writeErr}
				return status.Run(t.Context())
			},
		},
		{
			name: "snapshot JSON",
			run: func(connection *fakeDaemonControlClient) error {
				_, snapshot, _, _ := newDaemonCommandFixture(connection)
				snapshot.output = failingDaemonWriter{err: writeErr}
				return snapshot.Run(t.Context())
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := &fakeDaemonControlClient{status: daemonTestStatus(), snapshot: daemonTestSnapshot()}
			err := test.run(connection)
			if !errors.Is(err, writeErr) {
				t.Fatalf("error = %v, want %v", err, writeErr)
			}
			if connection.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", connection.closeCalls)
			}
		})
	}
}

// TestDaemonCommandsReturnControlFailuresWithoutOutput verifies a failed request cannot emit a misleading partial result.
func TestDaemonCommandsReturnControlFailuresWithoutOutput(t *testing.T) {
	requestErr := errors.New("request failed")

	for _, test := range []struct {
		name string
		run  func(*fakeDaemonControlClient) (string, error)
	}{
		{
			name: "stop",
			run: func(connection *fakeDaemonControlClient) (string, error) {
				stop, output := newDaemonStopCommandFixture(connection)
				err := stop.Run(t.Context())
				return output.String(), err
			},
		},
		{
			name: "status",
			run: func(connection *fakeDaemonControlClient) (string, error) {
				status, _, output, _ := newDaemonCommandFixture(connection)
				err := status.Run(t.Context())
				return output.String(), err
			},
		},
		{
			name: "snapshot",
			run: func(connection *fakeDaemonControlClient) (string, error) {
				_, snapshot, _, output := newDaemonCommandFixture(connection)
				err := snapshot.Run(t.Context())
				return output.String(), err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := &fakeDaemonControlClient{statusErr: requestErr, stopErr: requestErr, snapshotErr: requestErr}
			output, err := test.run(connection)
			if !errors.Is(err, requestErr) {
				t.Fatalf("error = %v, want %v", err, requestErr)
			}
			if output != "" {
				t.Fatalf("output = %q, want empty", output)
			}
			if connection.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", connection.closeCalls)
			}
		})
	}
}

// TestDaemonCommandGroupRoutesLifecycleCommands verifies Kong exposes the intended two-level CLI surface.
func TestDaemonCommandGroupRoutesLifecycleCommands(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "status", args: []string{"daemon", "status", "--json"}, want: `"state": "ready"`},
		{name: "stop", args: []string{"daemon", "stop"}, want: "Stable Harbor endpoints will be unavailable"},
		{name: "snapshot", args: []string{"daemon", "snapshot"}, want: `"schema_version": 1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection := &fakeDaemonControlClient{status: daemonTestStatus(), snapshot: daemonTestSnapshot()}
			status, snapshot, statusOutput, snapshotOutput := newDaemonCommandFixture(connection)
			stop, stopOutput := newDaemonStopCommandFixture(connection)
			root := struct {
				Daemon DaemonCmd `cmd:""`
			}{
				Daemon: *NewDaemonCmd(
					status,
					stop,
					snapshot,
					&ReleaseCmd{},
				),
			}
			parser, err := kong.New(&root)
			if err != nil {
				t.Fatalf("create parser: %v", err)
			}
			parsed, err := parser.Parse(test.args)
			if err != nil {
				t.Fatalf("parse %v: %v", test.args, err)
			}
			parsed.BindTo(t.Context(), (*context.Context)(nil))
			if err := parsed.Run(); err != nil {
				t.Fatalf("run %v: %v", test.args, err)
			}
			output := statusOutput.String() + stopOutput.String() + snapshotOutput.String()
			if !strings.Contains(output, test.want) {
				t.Fatalf("output = %q, want substring %q", output, test.want)
			}
		})
	}
}
