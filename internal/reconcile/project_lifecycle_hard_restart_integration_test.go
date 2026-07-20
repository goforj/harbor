package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

const (
	projectLifecycleRestartModeEnvironment     = "HARBOR_PROJECT_LIFECYCLE_RESTART_MODE"
	projectLifecycleRestartDatabaseEnvironment = "HARBOR_PROJECT_LIFECYCLE_RESTART_DATABASE"
	projectLifecycleRestartProjectEnvironment  = "HARBOR_PROJECT_LIFECYCLE_RESTART_PROJECT"
	projectLifecycleRestartReadyEnvironment    = "HARBOR_PROJECT_LIFECYCLE_RESTART_READY"
	projectLifecycleRestartPortEnvironment     = "HARBOR_PROJECT_LIFECYCLE_RESTART_PORT"
	projectLifecycleRestartLaunchMode          = "launch"
	projectLifecycleRestartRecoverMode         = "recover"
	projectLifecycleRestartQuarantineMode      = "quarantine-missing-process-evidence"
	projectLifecycleRestartCleanupMode         = "cleanup"
	projectLifecycleRestartProjectID           = domain.ProjectID("project-orders")
	projectLifecycleRestartOperationID         = domain.OperationID("operation-hard-restart")
	projectLifecycleRestartIntentID            = domain.IntentID("intent-hard-restart")
	projectLifecycleRestartTimeout             = 20 * time.Second
)

// projectLifecycleRestartOutput retains subprocess diagnostics without racing forced-exit observation.
type projectLifecycleRestartOutput struct {
	mutex sync.Mutex
	body  bytes.Buffer
}

// Write appends subprocess diagnostics under the same lock used by failure rendering.
func (output *projectLifecycleRestartOutput) Write(contents []byte) (int, error) {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return output.body.Write(contents)
}

// String returns a stable copy of the subprocess diagnostics.
func (output *projectLifecycleRestartOutput) String() string {
	output.mutex.Lock()
	defer output.mutex.Unlock()
	return output.body.String()
}

// TestProjectLifecycleHardRestartConvergesManagedProcess proves process-backed state cannot brick the next daemon generation.
func TestProjectLifecycleHardRestartConvergesManagedProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve lifecycle test executable: %v", err)
	}
	sandbox := t.TempDir()
	projectRoot, port := newProjectLifecycleIntegrationCheckout(t)
	installProjectLifecycleIntegrationForj(t, port)
	databasePath := filepath.Join(sandbox, "harbord.db")
	readyPath := filepath.Join(sandbox, "launch-ready")

	first := exec.Command(executable, "-test.run=^TestProjectLifecycleHardRestartSubprocess$", "-test.v")
	first.Env = projectLifecycleRestartSubprocessEnvironment(
		os.Environ(),
		projectLifecycleRestartLaunchMode,
		databasePath,
		projectRoot,
		readyPath,
		port,
	)
	first.Dir = sandbox
	firstOutput := new(projectLifecycleRestartOutput)
	first.Stdout = firstOutput
	first.Stderr = firstOutput
	if err := first.Start(); err != nil {
		t.Fatalf("start first lifecycle generation: %v", err)
	}
	firstDone := make(chan error, 1)
	firstExited := make(chan struct{})
	recovered := false
	go func() {
		firstDone <- first.Wait()
		close(firstExited)
	}()
	t.Cleanup(func() {
		if first.Process != nil {
			_ = first.Process.Kill()
		}
		select {
		case <-firstExited:
		case <-time.After(5 * time.Second):
		}
		if recovered {
			return
		}
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		cleanup := exec.CommandContext(cleanupContext, executable, "-test.run=^TestProjectLifecycleHardRestartSubprocess$")
		cleanup.Env = projectLifecycleRestartSubprocessEnvironment(
			os.Environ(),
			projectLifecycleRestartCleanupMode,
			databasePath,
			projectRoot,
			readyPath,
			port,
		)
		cleanup.Dir = sandbox
		_ = cleanup.Run()
	})
	waitForProjectLifecycleRestartReady(t, firstDone, readyPath, firstOutput)

	if err := first.Process.Kill(); err != nil {
		t.Fatalf("hard-kill first lifecycle generation: %v", err)
	}
	if err := <-firstDone; err == nil {
		t.Fatal("hard-killed lifecycle generation exited successfully")
	}

	ctx, cancel := context.WithTimeout(t.Context(), projectLifecycleRestartTimeout)
	defer cancel()
	second := exec.CommandContext(ctx, executable, "-test.run=^TestProjectLifecycleHardRestartSubprocess$", "-test.v")
	second.Env = projectLifecycleRestartSubprocessEnvironment(
		os.Environ(),
		projectLifecycleRestartRecoverMode,
		databasePath,
		projectRoot,
		readyPath,
		port,
	)
	second.Dir = sandbox
	secondOutput, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("restart lifecycle generation: %v\nfirst generation:\n%s\nsecond generation:\n%s", err, firstOutput.String(), secondOutput)
	}
	recovered = true
}

// TestProjectLifecycleHardRestartQuarantinesMissingProcessEvidence proves an older incomplete session cannot abort a new daemon generation.
func TestProjectLifecycleHardRestartQuarantinesMissingProcessEvidence(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve lifecycle test executable: %v", err)
	}
	sandbox := t.TempDir()
	databasePath := filepath.Join(sandbox, "harbord.db")
	store, journal, connection := openProjectLifecycleIntegrationStateWithConnection(t, databasePath, true)
	seed := completeProjectLifecycleRecoveryStart(t, store, seedProjectLifecycleRecoveryStart(t, store, journal, true))
	if err := connection.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
		t.Fatalf("allow legacy hard-restart boundary: %v", err)
	}
	if err := connection.Exec(
		`UPDATE project_sessions
		 SET pid = NULL, birth_token = NULL, executable_identity = NULL, argument_digest = NULL
		 WHERE project_id = ? AND session_id = ?`,
		string(seed.project.ID),
		string(seed.session.ID),
	).Error; err != nil {
		t.Fatalf("remove hard-restart process evidence: %v", err)
	}
	if err := connection.Exec("PRAGMA ignore_check_constraints = OFF").Error; err != nil {
		t.Fatalf("restore hard-restart session constraints: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), projectLifecycleRestartTimeout)
	defer cancel()
	second := exec.CommandContext(ctx, executable, "-test.run=^TestProjectLifecycleHardRestartSubprocess$", "-test.v")
	second.Env = projectLifecycleRestartSubprocessEnvironment(
		os.Environ(),
		projectLifecycleRestartQuarantineMode,
		databasePath,
		seed.project.Path,
		filepath.Join(sandbox, "unused-ready-marker"),
		3000,
	)
	second.Dir = sandbox
	output, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("restart lifecycle generation with missing process evidence: %v\n%s", err, output)
	}
}

// TestProjectLifecycleHardRestartSubprocess runs one isolated daemon generation selected by the parent integration test.
func TestProjectLifecycleHardRestartSubprocess(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv(projectLifecycleRestartModeEnvironment))
	if mode == "" {
		t.Skip("project lifecycle restart subprocess mode is not selected")
	}
	databasePath := strings.TrimSpace(os.Getenv(projectLifecycleRestartDatabaseEnvironment))
	projectRoot := strings.TrimSpace(os.Getenv(projectLifecycleRestartProjectEnvironment))
	readyPath := strings.TrimSpace(os.Getenv(projectLifecycleRestartReadyEnvironment))
	port, err := strconv.Atoi(strings.TrimSpace(os.Getenv(projectLifecycleRestartPortEnvironment)))
	if databasePath == "" || projectRoot == "" || readyPath == "" || port < 1 || port > 65535 || err != nil {
		t.Fatalf("invalid hard-restart subprocess configuration: database=%q project=%q ready=%q port=%d error=%v", databasePath, projectRoot, readyPath, port, err)
	}

	switch mode {
	case projectLifecycleRestartLaunchMode:
		runProjectLifecycleRestartLaunch(t, databasePath, projectRoot, readyPath, port)
	case projectLifecycleRestartRecoverMode:
		runProjectLifecycleRestartRecovery(t, databasePath, uint16(port))
	case projectLifecycleRestartQuarantineMode:
		runProjectLifecycleRestartQuarantine(t, databasePath)
	case projectLifecycleRestartCleanupMode:
		runProjectLifecycleRestartCleanup(t, databasePath)
	default:
		t.Fatalf("unsupported project lifecycle restart mode %q", mode)
	}
}

// runProjectLifecycleRestartQuarantine replays recovery in a fresh process without touching an unidentified prior process.
func runProjectLifecycleRestartQuarantine(t *testing.T, databasePath string) {
	store, journal := openProjectLifecycleIntegrationState(t, databasePath, false)
	supervisor := &projectLifecycleRecoverySupervisor{}
	coordinator := newProjectLifecycleAdmissionTestCoordinator(
		store,
		journal,
		store,
		supervisor,
		netip.MustParseAddr("127.0.0.1"),
	)
	t.Cleanup(func() {
		if err := coordinator.Close(context.Background()); err != nil {
			t.Errorf("close quarantine recovery coordinator: %v", err)
		}
	})

	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("recover missing-evidence hard restart: %v", err)
	}
	sequence, err := store.CurrentSequence(t.Context())
	if err != nil {
		t.Fatalf("read quarantine recovery sequence: %v", err)
	}
	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("replay missing-evidence hard restart recovery: %v", err)
	}
	replayed, err := store.CurrentSequence(t.Context())
	if err != nil || replayed != sequence {
		t.Fatalf("hard-restart quarantine replay sequence = %d, %v, want %d", replayed, err, sequence)
	}
	if len(supervisor.observed) != 0 || len(supervisor.settled) != 0 {
		t.Fatalf("hard-restart quarantine touched process authority: observed %#v settled %#v", supervisor.observed, supervisor.settled)
	}
	project, err := store.Project(t.Context(), projectLifecycleRestartProjectID)
	if err != nil || project.Project.State != domain.ProjectUnavailable || len(project.Project.Resources) != 0 {
		t.Fatalf("hard-restart quarantined project = %#v, %v", project.Project, err)
	}
	_, sessionErr := store.ActiveProjectSession(t.Context(), projectLifecycleRestartProjectID)
	var missing *state.ProjectSessionProcessEvidenceMissingError
	if !errors.As(sessionErr, &missing) || missing.State != domain.SessionAwaitingAttach {
		t.Fatalf("hard-restart retained session boundary = %#v, %v", missing, sessionErr)
	}
	operation, err := journal.LatestProjectLifecycleOperation(t.Context(), projectLifecycleRestartProjectID)
	if err != nil || operation.Operation.State != domain.OperationFailed || operation.Operation.Problem == nil ||
		operation.Operation.Problem.Code != projectRecoveryAmbiguousLaunchCode {
		t.Fatalf("hard-restart recovery operation = %#v, %v", operation.Operation, err)
	}
	runtimeState, err := store.RuntimeState(t.Context())
	if err != nil {
		t.Fatalf("read hard-restart route-free runtime state: %v", err)
	}
	if err := runtimeState.Validate(); err != nil {
		t.Fatalf("validate hard-restart route-free runtime state: %v", err)
	}
	active, err := journal.ActiveOperations(t.Context())
	if err != nil || len(active) != 0 {
		t.Fatalf("hard-restart quarantine active operations = %#v, %v", active, err)
	}
}

// runProjectLifecycleRestartCleanup settles leaked process authority even when the recovery assertion itself fails.
func runProjectLifecycleRestartCleanup(t *testing.T, databasePath string) {
	store, _ := openProjectLifecycleIntegrationState(t, databasePath, false)
	session, err := store.ActiveProjectSession(t.Context(), projectLifecycleRestartProjectID)
	if err != nil || session.Process == nil {
		return
	}
	supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 500 * time.Millisecond})
	if _, err := supervisor.SettlePriorProcess(t.Context(), *session.Process); err != nil {
		t.Fatalf("settle hard-restart test process during cleanup: %v", err)
	}
}

// runProjectLifecycleRestartLaunch reaches one process-backed ready boundary and waits for a deliberate hard kill.
func runProjectLifecycleRestartLaunch(t *testing.T, databasePath string, projectRoot string, readyPath string, port int) {
	store, journal := openProjectLifecycleIntegrationState(t, databasePath, true)
	project := registerProjectLifecycleIntegrationProject(t, store, projectRoot)
	initializeProjectLifecycleIntegrationIdentity(t, store, project.ID, netip.MustParseAddr("127.0.0.1"))

	environment := projectLifecycleRestartChildEnvironment(projectprocess.CaptureEnvironment(), port)
	supervisor := projectprocess.NewWithExecutableVerifier(
		projectprocess.Options{GracePeriod: 500 * time.Millisecond, Environment: environment},
		func(string) error { return nil },
	)
	coordinator, _ := newProjectLifecycleIntegrationCoordinator(
		t,
		store,
		journal,
		supervisor,
		netip.MustParseAddr("127.0.0.1"),
		uint16(port),
	)
	queued, err := coordinator.Start(t.Context(), ProjectStartRequest{
		ProjectID:   project.ID,
		OperationID: projectLifecycleRestartOperationID,
		IntentID:    projectLifecycleRestartIntentID,
	})
	if err != nil || queued.Operation.State != domain.OperationQueued {
		t.Fatalf("start managed project before hard restart = %#v, %v", queued, err)
	}
	ready := waitForProjectLifecycleState(t, store, project.ID, domain.ProjectReady)
	if len(ready.Project.Resources) != 1 || ready.Project.Resources[0].URL != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("ready project before hard restart = %#v", ready.Project)
	}
	session, err := store.ActiveProjectSession(t.Context(), project.ID)
	if err != nil || session.State != domain.SessionAwaitingAttach || session.Process == nil {
		t.Fatalf("durable process boundary before hard restart = %#v, %v", session, err)
	}
	operation, err := journal.OperationByIntent(t.Context(), projectLifecycleRestartIntentID)
	if err != nil || operation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("start operation before hard restart = %#v, %v", operation.Operation, err)
	}
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
		t.Fatalf("publish hard-restart boundary: %v", err)
	}
	select {}
}

// runProjectLifecycleRestartRecovery reopens abrupt state and proves startup convergence leaves no active lifecycle work.
func runProjectLifecycleRestartRecovery(t *testing.T, databasePath string, port uint16) {
	store, journal := openProjectLifecycleIntegrationState(t, databasePath, false)
	supervisor := newProjectLifecycleIntegrationSupervisor(projectprocess.Options{GracePeriod: 500 * time.Millisecond})
	coordinator, _ := newProjectLifecycleIntegrationCoordinator(
		t,
		store,
		journal,
		supervisor,
		netip.MustParseAddr("127.0.0.1"),
		port,
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Close(ctx); err != nil {
			t.Errorf("close recovered lifecycle coordinator: %v", err)
		}
	})

	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("recover hard-restarted managed project: %v", err)
	}
	if err := coordinator.Resume(t.Context()); err != nil {
		t.Fatalf("resume after hard-restart recovery: %v", err)
	}
	if err := coordinator.Err(); err != nil {
		t.Fatalf("lifecycle health after hard-restart recovery: %v", err)
	}
	project, err := store.Project(t.Context(), projectLifecycleRestartProjectID)
	if err != nil || project.Project.State != domain.ProjectFailed {
		t.Fatalf("project after hard-restart recovery = %#v, %v", project.Project, err)
	}
	if _, err := store.ActiveProjectSession(t.Context(), projectLifecycleRestartProjectID); err == nil {
		t.Fatal("hard-restart recovery retained an active project session")
	} else {
		var missing *state.ProjectSessionNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("active session after hard-restart recovery: %v", err)
		}
	}
	active, err := journal.ActiveOperations(t.Context())
	if err != nil || len(active) != 0 {
		t.Fatalf("active operations after hard-restart recovery = %#v, %v", active, err)
	}
	operation, err := journal.OperationByIntent(t.Context(), projectLifecycleRestartIntentID)
	if err != nil || operation.Operation.State != domain.OperationSucceeded {
		t.Fatalf("terminal start operation after hard-restart recovery = %#v, %v", operation.Operation, err)
	}
}

// projectLifecycleRestartChildEnvironment selects fake-forj mode only for the managed child, never its daemon parent.
func projectLifecycleRestartChildEnvironment(environment projectprocess.Environment, port int) projectprocess.Environment {
	return projectprocess.Environment(projectLifecycleRestartReplaceEnvironment(
		projectLifecycleRestartReplaceEnvironment(
			[]string(environment),
			projectLifecycleHelperEnvironment,
			"1",
		),
		"HARBOR_PROJECT_LIFECYCLE_PORT",
		strconv.Itoa(port),
	))
}

// projectLifecycleRestartSubprocessEnvironment configures one isolated daemon generation over the shared sandbox.
func projectLifecycleRestartSubprocessEnvironment(
	environment []string,
	mode string,
	databasePath string,
	projectRoot string,
	readyPath string,
	port int,
) []string {
	result := append([]string(nil), environment...)
	for name, value := range map[string]string{
		projectLifecycleRestartModeEnvironment:     mode,
		projectLifecycleRestartDatabaseEnvironment: databasePath,
		projectLifecycleRestartProjectEnvironment:  projectRoot,
		projectLifecycleRestartReadyEnvironment:    readyPath,
		projectLifecycleRestartPortEnvironment:     strconv.Itoa(port),
		projectLifecycleHelperEnvironment:          "",
	} {
		result = projectLifecycleRestartReplaceEnvironment(result, name, value)
	}
	return result
}

// projectLifecycleRestartReplaceEnvironment replaces one exact environment assignment without retaining duplicates.
func projectLifecycleRestartReplaceEnvironment(environment []string, name string, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, assignment := range environment {
		if strings.HasPrefix(assignment, prefix) {
			continue
		}
		result = append(result, assignment)
	}
	return append(result, prefix+value)
}

// waitForProjectLifecycleRestartReady joins marker polling to unexpected first-generation exit.
func waitForProjectLifecycleRestartReady(
	t *testing.T,
	done <-chan error,
	readyPath string,
	output *projectLifecycleRestartOutput,
) {
	t.Helper()
	deadline := time.Now().Add(projectLifecycleRestartTimeout)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(readyPath)
		if err == nil && string(contents) == "ready\n" {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("first lifecycle generation exited before its durable boundary: %v\n%s", err, output.String())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("first lifecycle generation did not reach its durable boundary: %s", output.String())
}
