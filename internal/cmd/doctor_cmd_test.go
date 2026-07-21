package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/goforj/harbor/internal/domain"
)

// newDoctorCommandFixture creates a doctor command around one fake daemon connection and captured output.
func newDoctorCommandFixture(connection *fakeDaemonControlClient) (*DoctorCmd, *bytes.Buffer) {
	client := newDaemonClient(func(context.Context) (daemonControlClient, error) { return connection, nil })
	command := NewDoctorCmd(client)
	output := &bytes.Buffer{}
	command.output = output
	return command, output
}

// doctorSnapshotWithProject returns one canonical snapshot containing a selected project.
func doctorSnapshotWithProject(t *testing.T, state domain.ProjectState) domain.Snapshot {
	t.Helper()
	project := addTestRegistration(t.TempDir(), true).Project
	project.State = state
	project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true}}
	project.Services = []domain.ServiceSnapshot{{
		ID:        "mysql",
		Name:      "MySQL",
		Kind:      "database",
		State:     domain.EntityReady,
		Owner:     domain.ServiceOwnerCompose,
		Selection: domain.ServiceSelected,
		Required:  true,
	}}
	snapshot := daemonTestSnapshot()
	snapshot.Projects = []domain.ProjectSnapshot{project}
	return snapshot
}

// TestDoctorCommandPrintsControlPlaneEvidence verifies the default report does not invent host or project health claims.
func TestDoctorCommandPrintsControlPlaneEvidence(t *testing.T) {
	connection := &fakeDaemonControlClient{status: daemonTestStatus(), snapshot: daemonTestSnapshot()}
	command, output := newDoctorCommandFixture(connection)

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	for _, want := range []string{
		"Harbor doctor (control-plane)",
		"Daemon: ready (v1.2.3)",
		"[pass] daemon.control_endpoint:",
		"[pass] snapshot.integrity:",
		"[observed] projects.observed: observed 0 registered project(s)",
		"Projects:",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, output.String())
		}
	}
	if connection.statusCalls != 1 || connection.snapshotCalls != 1 || connection.closeCalls != 2 {
		t.Fatalf("calls = status:%d snapshot:%d close:%d", connection.statusCalls, connection.snapshotCalls, connection.closeCalls)
	}
}

// TestDoctorCommandScopesOneProjectAndSupportsJSON verifies project selection remains snapshot-bound in both presentations.
func TestDoctorCommandScopesOneProjectAndSupportsJSON(t *testing.T) {
	snapshot := doctorSnapshotWithProject(t, domain.ProjectReady)
	other := doctorSnapshotWithProject(t, domain.ProjectDegraded).Projects[0]
	other.ID = "project-billing"
	other.Name = "Billing"
	snapshot.Projects = append(snapshot.Projects, other)
	connection := &fakeDaemonControlClient{status: daemonTestStatus(), snapshot: snapshot}
	command, output := newDoctorCommandFixture(connection)
	command.ProjectID = "project-orders"
	command.JSON = true

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run scoped doctor: %v", err)
	}
	var report DoctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor JSON: %v\n%s", err, output.String())
	}
	if err := validateDoctorReport(report); err != nil {
		t.Fatalf("validate doctor JSON: %v", err)
	}
	if report.Scope != "control-plane/project" || len(report.Projects) != 1 || report.Projects[0].ID != "project-orders" {
		t.Fatalf("scoped report = %#v", report)
	}
	if !strings.Contains(output.String(), `"project.state"`) || strings.Contains(output.String(), "project-billing") {
		t.Fatalf("scoped JSON leaked another project:\n%s", output.String())
	}
}

// TestDoctorCommandWarnsWhenStatusAndSnapshotSequencesDiffer verifies the two one-shot reads are not presented as atomic.
func TestDoctorCommandWarnsWhenStatusAndSnapshotSequencesDiffer(t *testing.T) {
	snapshot := daemonTestSnapshot()
	snapshot.Sequence++
	connection := &fakeDaemonControlClient{status: daemonTestStatus(), snapshot: snapshot}
	command, output := newDoctorCommandFixture(connection)

	if err := command.Run(t.Context()); err != nil {
		t.Fatalf("run drifting doctor: %v", err)
	}
	if !strings.Contains(output.String(), "[warning] snapshot.sequence:") || !strings.Contains(output.String(), "run doctor again") {
		t.Fatalf("sequence drift was not surfaced:\n%s", output.String())
	}
}

// TestDoctorCommandRejectsInvalidAndMissingProjectsBeforePresentingEvidence verifies selection failures remain explicit.
func TestDoctorCommandRejectsInvalidAndMissingProjectsBeforePresentingEvidence(t *testing.T) {
	t.Run("invalid before connect", func(t *testing.T) {
		connectCalls := 0
		client := newDaemonClient(func(context.Context) (daemonControlClient, error) {
			connectCalls++
			return &fakeDaemonControlClient{}, nil
		})
		command := NewDoctorCmd(client)
		command.ProjectID = " project "
		if err := command.Run(t.Context()); err == nil {
			t.Fatal("invalid project error = nil")
		}
		if connectCalls != 0 {
			t.Fatalf("connect calls = %d, want 0", connectCalls)
		}
	})

	t.Run("missing after snapshot", func(t *testing.T) {
		connection := &fakeDaemonControlClient{status: daemonTestStatus(), snapshot: daemonTestSnapshot()}
		command, output := newDoctorCommandFixture(connection)
		command.ProjectID = "project-orders"
		err := command.Run(t.Context())
		if err == nil || !strings.Contains(err.Error(), `project "project-orders" was not found`) {
			t.Fatalf("missing project error = %v", err)
		}
		if output.Len() != 0 || connection.statusCalls != 1 || connection.snapshotCalls != 1 || connection.closeCalls != 2 {
			t.Fatalf("missing project side effects: output=%q status=%d snapshot=%d close=%d", output.String(), connection.statusCalls, connection.snapshotCalls, connection.closeCalls)
		}
	})
}

// TestDoctorCommandReturnsControlFailuresWithoutFabricatingAReport verifies transport failures remain terminal.
func TestDoctorCommandReturnsControlFailuresWithoutFabricatingAReport(t *testing.T) {
	statusErr := errors.New("daemon unavailable")
	connection := &fakeDaemonControlClient{statusErr: statusErr}
	command, output := newDoctorCommandFixture(connection)
	if err := command.Run(t.Context()); !errors.Is(err, statusErr) {
		t.Fatalf("status error = %v, want %v", err, statusErr)
	}
	if output.Len() != 0 || connection.snapshotCalls != 0 {
		t.Fatalf("status failure fabricated output or snapshot read: output=%q snapshot=%d", output.String(), connection.snapshotCalls)
	}

	snapshotErr := errors.New("snapshot unavailable")
	connection = &fakeDaemonControlClient{status: daemonTestStatus(), snapshotErr: snapshotErr}
	command, output = newDoctorCommandFixture(connection)
	if err := command.Run(t.Context()); !errors.Is(err, snapshotErr) {
		t.Fatalf("snapshot error = %v, want %v", err, snapshotErr)
	}
	if output.Len() != 0 || connection.statusCalls != 1 || connection.snapshotCalls != 1 {
		t.Fatalf("snapshot failure fabricated output: output=%q status=%d snapshot=%d", output.String(), connection.statusCalls, connection.snapshotCalls)
	}
}

// TestValidateDoctorReportRejectsMalformedReports covers version, scope, time, collection, status, and project boundaries.
func TestValidateDoctorReportRejectsMalformedReports(t *testing.T) {
	base := newDoctorReport(daemonTestStatus(), daemonTestSnapshot(), "", []DoctorProjectEvidence{})
	tests := []struct {
		name   string
		mutate func(*DoctorReport)
		want   string
	}{
		{name: "schema", mutate: func(report *DoctorReport) { report.SchemaVersion++ }, want: "schema version"},
		{name: "scope", mutate: func(report *DoctorReport) { report.Scope = "machine" }, want: "scope"},
		{name: "time", mutate: func(report *DoctorReport) { report.CapturedAt = time.Time{} }, want: "capture time"},
		{name: "nil projects", mutate: func(report *DoctorReport) { report.Projects = nil }, want: "collections"},
		{name: "nil checks", mutate: func(report *DoctorReport) { report.Checks = nil }, want: "collections"},
		{name: "bad check status", mutate: func(report *DoctorReport) { report.Checks[0].Status = "fail" }, want: "unsupported status"},
		{name: "duplicate check", mutate: func(report *DoctorReport) { report.Checks = append(report.Checks, report.Checks[0]) }, want: "duplicate check"},
		{name: "bad project", mutate: func(report *DoctorReport) { report.Projects = []DoctorProjectEvidence{{ID: ""}} }, want: "project ID"},
		{name: "negative count", mutate: func(report *DoctorReport) {
			report.Projects = []DoctorProjectEvidence{{ID: "project-orders", State: domain.ProjectStopped, Apps: -1}}
		}, want: "negative count"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := base
			report.Checks = append([]DoctorCheck{}, base.Checks...)
			report.Projects = append([]DoctorProjectEvidence{}, base.Projects...)
			test.mutate(&report)
			if err := validateDoctorReport(report); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateDoctorReport() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestDoctorCommandKongSurfaceAcceptsOptionalScope verifies the documented argument and JSON flag shape.
func TestDoctorCommandKongSurfaceAcceptsOptionalScope(t *testing.T) {
	command, _ := newDoctorCommandFixture(&fakeDaemonControlClient{})
	root := struct {
		Doctor DoctorCmd `cmd:""`
	}{Doctor: *command}
	parser, err := kong.New(&root)
	if err != nil {
		t.Fatalf("create parser: %v", err)
	}
	for _, args := range [][]string{{"doctor"}, {"doctor", "project-orders", "--json"}} {
		parsed, err := parser.Parse(args)
		if err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		if !strings.HasPrefix(parsed.Command(), "doctor") {
			t.Fatalf("parsed command for %v = %q", args, parsed.Command())
		}
	}
}
