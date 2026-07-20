package projectprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
)

const (
	serviceObservationSchemaVersion  = 1
	maximumServiceObservationBytes   = 1 << 20
	maximumServiceDiagnosticBytes    = 16 << 10
	maximumObservedProjectServices   = 256
	maximumServiceObservationProblem = 4096
	serviceObservationWaitDelay      = time.Second
)

// ServiceObservation is one complete replacement view of the active Compose services reported by GoForj.
type ServiceObservation struct {
	Supported bool
	Services  []domain.ServiceSnapshot
}

// goForjServiceObservationReport mirrors the versioned machine contract without importing GoForj implementation packages.
type goForjServiceObservationReport struct {
	SchemaVersion uint16                         `json:"schema_version"`
	Supported     bool                           `json:"supported"`
	Problem       string                         `json:"problem,omitempty"`
	Services      []goForjServiceObservationItem `json:"services"`
}

// goForjServiceObservationItem contains the semantic service facts Harbor persists from a richer GoForj runtime observation.
type goForjServiceObservationItem struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Kind     string             `json:"kind"`
	State    domain.EntityState `json:"state"`
	Active   bool               `json:"active"`
	Required bool               `json:"required"`
}

// boundedObservationBuffer prevents a malfunctioning child command from consuming unbounded daemon memory.
type boundedObservationBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

// Write retains output only while it remains inside the parser budget.
func (buffer *boundedObservationBuffer) Write(value []byte) (int, error) {
	if buffer.exceeded {
		return len(value), nil
	}
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = buffer.buffer.Write(value[:remaining])
		buffer.exceeded = true
		return len(value), nil
	}
	_, _ = buffer.buffer.Write(value)
	return len(value), nil
}

// Bytes returns the retained prefix for parsing or bounded diagnostics.
func (buffer *boundedObservationBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

// ObserveServices asks the exact GoForj executable supervising one project for its current typed Compose service view.
func (supervisor *Supervisor) ObserveServices(
	ctx context.Context,
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (ServiceObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ServiceObservation{}, err
	}
	if err := projectID.Validate(); err != nil {
		return ServiceObservation{}, fmt.Errorf("observe project services: %w", err)
	}
	if err := sessionID.Validate(); err != nil {
		return ServiceObservation{}, fmt.Errorf("observe project services: %w", err)
	}

	executable, checkoutRoot, environment, found := supervisor.serviceObservationCommand(projectID, sessionID)
	if !found {
		return ServiceObservation{}, ErrNotRunning
	}
	return runServiceObservationCommand(ctx, executable, checkoutRoot, environment)
}

// serviceObservationCommand snapshots immutable launch facts without retaining the supervisor lock while a child command runs.
func (supervisor *Supervisor) serviceObservationCommand(
	projectID domain.ProjectID,
	sessionID domain.SessionID,
) (string, string, []string, bool) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	projectProcess, projectExists := supervisor.projects[projectID]
	sessionProcess, sessionExists := supervisor.sessions[sessionID]
	if !projectExists || !sessionExists || projectProcess != sessionProcess ||
		!projectProcess.acceptingStop || projectProcess.stopRequested.Load() {
		return "", "", nil, false
	}
	return projectProcess.command.Path,
		projectProcess.command.Dir,
		append([]string(nil), projectProcess.command.Env...),
		true
}

// runServiceObservationCommand invokes the private machine surface without a shell or terminal parser.
func runServiceObservationCommand(
	ctx context.Context,
	executable string,
	checkoutRoot string,
	environment []string,
) (ServiceObservation, error) {
	stdout := &boundedObservationBuffer{maximum: maximumServiceObservationBytes}
	stderr := &boundedObservationBuffer{maximum: maximumServiceDiagnosticBytes}
	err := executeOwnedServiceObservationCommand(
		ctx,
		executable,
		[]string{"dev:status", "--json"},
		checkoutRoot,
		environment,
		stdout,
		stderr,
	)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ServiceObservation{}, ctxErr
	}
	if stdout.exceeded {
		return ServiceObservation{}, fmt.Errorf("GoForj service observation exceeds %d bytes", maximumServiceObservationBytes)
	}
	if stderr.exceeded {
		return ServiceObservation{}, fmt.Errorf("GoForj service observation diagnostic exceeds %d bytes", maximumServiceDiagnosticBytes)
	}
	if err != nil {
		// Accepted older GoForj builds do not expose this additive command. They keep the historical empty service projection.
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ProcessState != nil && exitError.ProcessState.Exited() {
			return ServiceObservation{Supported: false, Services: []domain.ServiceSnapshot{}}, nil
		}
		return ServiceObservation{}, fmt.Errorf("run GoForj service observation: %w", err)
	}
	return decodeServiceObservation(stdout.Bytes())
}

// executeOwnedServiceObservationCommand gives every status-command descendant the same exact cleanup boundary as a managed project launch.
func executeOwnedServiceObservationCommand(
	ctx context.Context,
	executable string,
	arguments []string,
	checkoutRoot string,
	environment []string,
	stdout *boundedObservationBuffer,
	stderr *boundedObservationBuffer,
) error {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = checkoutRoot
	command.Env = append([]string(nil), environment...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = serviceObservationWaitDelay
	platform, err := preparePlatformProcess(command)
	if err != nil {
		return fmt.Errorf("prepare service observation process ownership: %w", err)
	}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return forceServiceObservationCommand(command, platform)
	}
	if err := command.Start(); err != nil {
		platform.close()
		return err
	}
	if _, err := platform.attach(command.Process); err != nil {
		cleanupErr := terminateStartedCommand(command, platform)
		platform.close()
		return errors.Join(fmt.Errorf("attach service observation process: %w", err), cleanupErr)
	}
	if err := platform.resume(command.Process); err != nil {
		cleanupErr := terminateStartedCommand(command, platform)
		platform.close()
		return errors.Join(fmt.Errorf("resume service observation process: %w", err), cleanupErr)
	}
	waitErr := command.Wait()
	contextErr := ctx.Err()
	alive, observeErr := platform.treeAlive(command.Process.Pid)
	var cleanupErr error
	if alive {
		cleanupErr = platform.force(command.Process.Pid)
	}
	platform.close()
	return errors.Join(contextErr, waitErr, observeErr, cleanupErr)
}

// forceServiceObservationCommand retires the complete short-lived command tree when its context expires.
func forceServiceObservationCommand(command *exec.Cmd, platform *platformProcess) error {
	forceErr := platform.force(command.Process.Pid)
	rootErr := command.Process.Kill()
	if errors.Is(rootErr, os.ErrProcessDone) {
		rootErr = nil
	}
	return errors.Join(forceErr, rootErr)
}

// decodeServiceObservation validates the complete replacement snapshot before it can enter durable Harbor state.
func decodeServiceObservation(encoded []byte) (ServiceObservation, error) {
	if len(bytes.TrimSpace(encoded)) == 0 {
		return ServiceObservation{}, errors.New("GoForj service observation is empty")
	}
	var report goForjServiceObservationReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		return ServiceObservation{}, fmt.Errorf("decode GoForj service observation: %w", err)
	}
	if report.SchemaVersion != serviceObservationSchemaVersion {
		return ServiceObservation{}, fmt.Errorf("GoForj service observation schema is %d, want %d", report.SchemaVersion, serviceObservationSchemaVersion)
	}
	if report.Services == nil {
		return ServiceObservation{}, errors.New("GoForj service observation services must not be null")
	}
	if len(report.Services) > maximumObservedProjectServices {
		return ServiceObservation{}, fmt.Errorf("GoForj service observation exceeds %d services", maximumObservedProjectServices)
	}
	problem := strings.TrimSpace(report.Problem)
	if report.Problem != problem || len(problem) > maximumServiceObservationProblem {
		return ServiceObservation{}, errors.New("GoForj service observation problem is not bounded canonical text")
	}
	if !report.Supported {
		return ServiceObservation{Supported: false, Services: []domain.ServiceSnapshot{}}, nil
	}
	if problem != "" {
		return ServiceObservation{}, fmt.Errorf("GoForj could not observe project services: %s", problem)
	}

	services := make([]domain.ServiceSnapshot, 0, len(report.Services))
	identities := make(map[domain.ServiceID]struct{}, len(report.Services))
	for _, observed := range report.Services {
		if !observed.Active {
			continue
		}
		service := domain.ServiceSnapshot{
			ID:        domain.ServiceID(observed.ID),
			Name:      observed.Name,
			Kind:      observed.Kind,
			State:     observed.State,
			Owner:     domain.ServiceOwnerCompose,
			Selection: domain.ServiceSelected,
			Required:  observed.Required,
		}
		if err := service.Validate(); err != nil {
			return ServiceObservation{}, fmt.Errorf("validate GoForj service %q: %w", observed.ID, err)
		}
		if service.State == domain.EntityStopped {
			return ServiceObservation{}, fmt.Errorf("active GoForj service %q cannot be stopped", observed.ID)
		}
		if _, exists := identities[service.ID]; exists {
			return ServiceObservation{}, fmt.Errorf("GoForj service observation contains duplicate service %q", service.ID)
		}
		identities[service.ID] = struct{}{}
		services = append(services, service)
	}
	sort.Slice(services, func(left int, right int) bool {
		return services[left].ID < services[right].ID
	})
	return ServiceObservation{Supported: true, Services: services}, nil
}
