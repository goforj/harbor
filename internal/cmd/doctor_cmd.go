package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
)

const doctorReportSchemaVersion uint16 = 1

// DoctorCheckStatus describes the evidence level of one control-plane doctor check.
type DoctorCheckStatus string

const (
	// DoctorCheckPass means the requested control-plane fact was observed and validated.
	DoctorCheckPass DoctorCheckStatus = "pass"
	// DoctorCheckObserved means the daemon returned a raw project fact without Harbor inferring health from it.
	DoctorCheckObserved DoctorCheckStatus = "observed"
	// DoctorCheckWarning means the evidence changed during a read and should be collected again.
	DoctorCheckWarning DoctorCheckStatus = "warning"
)

// DoctorCheck is one stable, bounded finding in a control-plane doctor report.
type DoctorCheck struct {
	ID      string            `json:"id"`
	Status  DoctorCheckStatus `json:"status"`
	Message string            `json:"message"`
}

// DoctorProjectEvidence is the authoritative snapshot summary for one selected or listed project.
type DoctorProjectEvidence struct {
	ID        domain.ProjectID    `json:"id"`
	Name      string              `json:"name"`
	Path      string              `json:"path"`
	State     domain.ProjectState `json:"state"`
	Apps      int                 `json:"apps"`
	Services  int                 `json:"services"`
	Resources int                 `json:"resources"`
}

// DoctorReport is a versioned control-plane diagnostic, not a claim about native host health.
type DoctorReport struct {
	SchemaVersion    uint16                  `json:"schema_version"`
	Scope            string                  `json:"scope"`
	CapturedAt       time.Time               `json:"captured_at"`
	Daemon           control.DaemonStatus    `json:"daemon"`
	SnapshotSequence domain.Sequence         `json:"snapshot_sequence"`
	Projects         []DoctorProjectEvidence `json:"projects"`
	Checks           []DoctorCheck           `json:"checks"`
}

// DoctorCmd reports only authenticated daemon and authoritative snapshot evidence.
type DoctorCmd struct {
	ProjectID domain.ProjectID `arg:"" optional:"" name:"project" help:"Optional registered Harbor project ID"`
	JSON      bool             `help:"Print the versioned machine-readable doctor report"`

	client *DaemonClient
	output io.Writer
}

// NewDoctorCmd creates the control-plane doctor command without contacting the daemon.
func NewDoctorCmd(client *DaemonClient) *DoctorCmd {
	return &DoctorCmd{client: client, output: os.Stdout}
}

// Signature defines CLI metadata for the doctor command.
func (*DoctorCmd) Signature() string {
	return `name:"doctor" help:"Inspect Harbor control-plane state"`
}

// Run reads daemon status and one authoritative snapshot before presenting the selected scope.
func (command *DoctorCmd) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if command.ProjectID != "" {
		if err := command.ProjectID.Validate(); err != nil {
			return fmt.Errorf("doctor: %w", err)
		}
	}
	status, err := command.client.Status(ctx)
	if err != nil {
		return fmt.Errorf("doctor: read daemon status: %w", err)
	}
	snapshot, err := command.client.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("doctor: read daemon snapshot: %w", err)
	}
	projects, err := doctorProjects(snapshot, command.ProjectID)
	if err != nil {
		return err
	}
	report := newDoctorReport(status, snapshot, command.ProjectID, projects)
	if err := validateDoctorReport(report); err != nil {
		return fmt.Errorf("doctor: validate report: %w", err)
	}
	if command.JSON {
		return writeDaemonJSON(command.output, report)
	}
	return writeDoctorReport(command.output, report)
}

// doctorProjects selects one project or preserves the complete registered-project list without inferring health.
func doctorProjects(snapshot domain.Snapshot, selected domain.ProjectID) ([]DoctorProjectEvidence, error) {
	projects := make([]DoctorProjectEvidence, 0, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		if selected != "" && project.ID != selected {
			continue
		}
		projects = append(projects, DoctorProjectEvidence{
			ID:        project.ID,
			Name:      project.Name,
			Path:      project.Path,
			State:     project.State,
			Apps:      len(project.Apps),
			Services:  len(project.Services),
			Resources: len(project.Resources),
		})
	}
	if selected != "" && len(projects) == 0 {
		return nil, fmt.Errorf("doctor: project %q was not found", selected)
	}
	return projects, nil
}

// newDoctorReport builds stable checks from two authenticated reads without pretending they were one atomic wire call.
func newDoctorReport(
	status control.DaemonStatus,
	snapshot domain.Snapshot,
	selected domain.ProjectID,
	projects []DoctorProjectEvidence,
) DoctorReport {
	checks := []DoctorCheck{
		{ID: "daemon.control_endpoint", Status: DoctorCheckPass, Message: "authenticated ready daemon control is available"},
		{ID: "snapshot.integrity", Status: DoctorCheckPass, Message: "authoritative snapshot passed schema and ownership validation"},
		{ID: "projects.observed", Status: DoctorCheckObserved, Message: fmt.Sprintf("observed %d registered project(s)", len(snapshot.Projects))},
	}
	sequenceStatus := DoctorCheckPass
	sequenceMessage := fmt.Sprintf("daemon and snapshot sequence are %d", status.Sequence)
	if status.Sequence != snapshot.Sequence {
		sequenceStatus = DoctorCheckWarning
		sequenceMessage = fmt.Sprintf("daemon sequence %d and snapshot sequence %d changed during the check; run doctor again", status.Sequence, snapshot.Sequence)
	}
	checks = append(checks, DoctorCheck{ID: "snapshot.sequence", Status: sequenceStatus, Message: sequenceMessage})
	if selected != "" {
		checks = append(checks,
			DoctorCheck{ID: "project.registered", Status: DoctorCheckPass, Message: fmt.Sprintf("project %q is registered", selected)},
			DoctorCheck{ID: "project.state", Status: DoctorCheckObserved, Message: fmt.Sprintf("authoritative project state is %s", projects[0].State)},
		)
	}
	return DoctorReport{
		SchemaVersion:    doctorReportSchemaVersion,
		Scope:            doctorScope(selected),
		CapturedAt:       time.Now().UTC(),
		Daemon:           status,
		SnapshotSequence: snapshot.Sequence,
		Projects:         projects,
		Checks:           checks,
	}
}

// doctorScope labels whether the report covers all registered projects or one selected project.
func doctorScope(selected domain.ProjectID) string {
	if selected == "" {
		return "control-plane"
	}
	return "control-plane/project"
}

// writeDoctorReport renders a compact diagnostic without turning raw project state into an inferred health verdict.
func writeDoctorReport(output io.Writer, report DoctorReport) error {
	if _, err := fmt.Fprintf(output, "Harbor doctor (%s)\nDaemon: %s (%s)\nSnapshot sequence: %d\n", report.Scope, report.Daemon.State, report.Daemon.Build.Version, report.SnapshotSequence); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(output, "[%s] %s: %s\n", check.Status, check.ID, check.Message); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(output, "Projects:\n"); err != nil {
		return err
	}
	for _, project := range report.Projects {
		if _, err := fmt.Fprintf(output, "- %s [%s] apps=%d services=%d resources=%d\n", project.Name, project.State, project.Apps, project.Services, project.Resources); err != nil {
			return err
		}
	}
	return nil
}

// validateDoctorReport protects future clients from accepting an unversioned or structurally incomplete report.
func validateDoctorReport(report DoctorReport) error {
	if report.SchemaVersion != doctorReportSchemaVersion {
		return fmt.Errorf("unsupported doctor report schema version %d", report.SchemaVersion)
	}
	if report.Scope != "control-plane" && report.Scope != "control-plane/project" {
		return fmt.Errorf("unsupported doctor report scope %q", report.Scope)
	}
	if report.CapturedAt.IsZero() || report.CapturedAt.Location() != time.UTC {
		return fmt.Errorf("doctor report capture time must be canonical UTC")
	}
	if err := report.Daemon.Validate(); err != nil {
		return fmt.Errorf("doctor report daemon: %w", err)
	}
	if uint64(report.SnapshotSequence) > uint64(domain.MaximumSequence) {
		return fmt.Errorf("doctor report snapshot sequence exceeds %d", domain.MaximumSequence)
	}
	if report.Projects == nil || report.Checks == nil {
		return fmt.Errorf("doctor report collections must not be nil")
	}
	seen := make(map[string]struct{}, len(report.Checks))
	for _, check := range report.Checks {
		if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Message) == "" {
			return fmt.Errorf("doctor report check must contain an ID and message")
		}
		if check.Status != DoctorCheckPass && check.Status != DoctorCheckObserved && check.Status != DoctorCheckWarning {
			return fmt.Errorf("doctor report check %q has unsupported status %q", check.ID, check.Status)
		}
		if _, exists := seen[check.ID]; exists {
			return fmt.Errorf("doctor report contains duplicate check %q", check.ID)
		}
		seen[check.ID] = struct{}{}
	}
	for _, project := range report.Projects {
		if err := project.ID.Validate(); err != nil {
			return fmt.Errorf("doctor report project: %w", err)
		}
		if err := project.State.Validate(); err != nil {
			return fmt.Errorf("doctor report project %q: %w", project.ID, err)
		}
		if project.Apps < 0 || project.Services < 0 || project.Resources < 0 {
			return fmt.Errorf("doctor report project %q contains a negative count", project.ID)
		}
	}
	return nil
}
