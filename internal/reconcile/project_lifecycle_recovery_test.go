package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// projectLifecycleRecoverySupervisor exposes one deterministic prior-process observation without owning a live process.
type projectLifecycleRecoverySupervisor struct {
	observation    projectprocess.PriorProcessObservation
	observationErr error
	observed       []domain.ProcessEvidence
	settlement     projectprocess.PriorProcessSettlement
	settlementErr  error
	settled        []domain.ProcessEvidence
}

// Start rejects launches because recovery must never replay a process-backed running start.
func (*projectLifecycleRecoverySupervisor) Start(
	context.Context,
	projectprocess.StartRequest,
) (*projectprocess.Handle, error) {
	return nil, errors.New("unexpected process launch during project lifecycle recovery")
}

// Stop rejects in-memory supervision because prior processes are observed without claiming authority over them.
func (*projectLifecycleRecoverySupervisor) Stop(context.Context, domain.ProjectID, domain.SessionID) error {
	return errors.New("unexpected supervised stop during project lifecycle recovery")
}

// ReadOutput returns no transcript because recovery fixtures never own the prior process.
func (*projectLifecycleRecoverySupervisor) ReadOutput(
	domain.ProjectID,
	domain.SessionID,
	uint64,
) projectprocess.OutputChunk {
	return projectprocess.OutputChunk{}
}

// ObservePriorProcess returns the configured host classification and records the exact evidence inspected.
func (supervisor *projectLifecycleRecoverySupervisor) ObservePriorProcess(
	_ context.Context,
	evidence domain.ProcessEvidence,
) (projectprocess.PriorProcessObservation, error) {
	supervisor.observed = append(supervisor.observed, evidence)
	return supervisor.observation, supervisor.observationErr
}

// SettlePriorProcess returns the configured terminal settlement and records the exact evidence retired.
func (supervisor *projectLifecycleRecoverySupervisor) SettlePriorProcess(
	_ context.Context,
	evidence domain.ProcessEvidence,
) (projectprocess.PriorProcessSettlement, error) {
	supervisor.settled = append(supervisor.settled, evidence)
	return supervisor.settlement, supervisor.settlementErr
}

// Close is inert because the recovery fixture never owns a process.
func (*projectLifecycleRecoverySupervisor) Close(context.Context) error {
	return nil
}

// projectLifecycleRecoverySeed records one running start at either side of the durable process-evidence boundary.
type projectLifecycleRecoverySeed struct {
	project   domain.ProjectSnapshot
	operation state.OperationRecord
	session   domain.ProjectSession
	evidence  domain.ProcessEvidence
	recoverAt time.Time
}

// TestProjectLifecycleRecoverDefersQueuedStartsUntilResume proves recovery cannot publish routes through an unstarted runtime.
func TestProjectLifecycleRecoverDefersQueuedStartsUntilResume(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	root, _ := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	projectRecord, err := store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("read registered project: %v", err)
	}
	operation, err := domain.NewOperation(
		"operation-recovered-queued-start",
		"intent-recovered-queued-start",
		domain.OperationKindProjectStart,
		project.ID,
		projectRecord.Project.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("create queued start: %v", err)
	}
	if _, err := journal.Enqueue(t.Context(), operation); err != nil {
		t.Fatalf("enqueue queued start: %v", err)
	}

	admissionEntered := make(chan struct{})
	coordinator := newProjectLifecycleAdmissionTestCoordinator(
		store,
		journal,
		&projectLifecycleBlockingLeaseState{entered: admissionEntered},
		&projectLifecycleRecoverySupervisor{},
		netip.MustParseAddr("127.0.0.1"),
	)
	t.Cleanup(func() {
		if err := coordinator.Close(context.Background()); err != nil {
			t.Errorf("close lifecycle coordinator: %v", err)
		}
	})

	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	coordinator.mutex.Lock()
	dispatched := len(coordinator.dispatched)
	coordinator.mutex.Unlock()
	if dispatched != 0 {
		t.Fatalf("Recover() dispatched %d queued starts before runtime startup", dispatched)
	}
	recovered, err := journal.OperationByIntent(t.Context(), operation.IntentID)
	if err != nil || recovered.Operation.State != domain.OperationQueued {
		t.Fatalf("operation after Recover() = %#v, %v", recovered.Operation, err)
	}

	if err := coordinator.Resume(t.Context()); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	select {
	case <-admissionEntered:
	case <-time.After(time.Second):
		t.Fatal("Resume() did not dispatch the recovered queued start")
	}
}

// TestProjectLifecycleResumeRejectsUnavailableAuthority covers cancellation and shutdown before recovered work dispatch.
func TestProjectLifecycleResumeRejectsUnavailableAuthority(t *testing.T) {
	newCoordinator := func(t *testing.T) *ProjectLifecycleCoordinator {
		t.Helper()
		store, journal := newProjectLifecycleIntegrationState(t)
		return newProjectLifecycleAdmissionTestCoordinator(
			store,
			journal,
			store,
			&projectLifecycleRecoverySupervisor{},
			netip.MustParseAddr("127.0.0.1"),
		)
	}

	t.Run("cancelled", func(t *testing.T) {
		coordinator := newCoordinator(t)
		t.Cleanup(func() { _ = coordinator.Close(context.Background()) })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := coordinator.Resume(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Resume(cancelled) error = %v, want context cancellation", err)
		}
	})

	t.Run("closed", func(t *testing.T) {
		coordinator := newCoordinator(t)
		if err := coordinator.Close(t.Context()); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := coordinator.Recover(t.Context()); err == nil || !strings.Contains(err.Error(), "coordinator is closed") {
			t.Fatalf("Recover(closed) error = %v, want closed coordinator", err)
		}
		if err := coordinator.Resume(t.Context()); err == nil || !strings.Contains(err.Error(), "coordinator is closed") {
			t.Fatalf("Resume(closed) error = %v, want closed coordinator", err)
		}
	})
}

// TestProjectLifecycleRecoverFailsStartAfterPriorProcessSettlement proves interrupted starts converge without deleting unrelated state.
func TestProjectLifecycleRecoverFailsStartAfterPriorProcessSettlement(t *testing.T) {
	for _, outcome := range []projectprocess.PriorProcessSettlementOutcome{
		projectprocess.PriorProcessSettlementAbsent,
		projectprocess.PriorProcessSettlementReplaced,
		projectprocess.PriorProcessSettlementTerminated,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			seed := seedProjectLifecycleRecoveryStart(t, store, journal, true)
			supervisor := &projectLifecycleRecoverySupervisor{
				settlement: projectprocess.PriorProcessSettlement{Outcome: outcome},
			}
			routeCalls := 0
			coordinator := newProjectLifecycleAdmissionTestCoordinator(
				store,
				journal,
				store,
				supervisor,
				netip.MustParseAddr("127.0.0.1"),
			)
			coordinator.routes = projectLifecycleRouteReconcilerFunc(func(context.Context) error {
				routeCalls++
				return errors.New("route controller is not started during daemon recovery")
			})
			coordinator.now = func() time.Time { return seed.recoverAt }
			t.Cleanup(func() {
				if err := coordinator.Close(context.Background()); err != nil {
					t.Errorf("close recovery coordinator: %v", err)
				}
			})

			if err := coordinator.Recover(t.Context()); err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			operation, err := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
			if err != nil {
				t.Fatalf("read recovered operation: %v", err)
			}
			if operation.Operation.State != domain.OperationFailed || operation.Operation.Problem == nil ||
				operation.Operation.Problem.Code != "project.recovery.process_absent" || !operation.Operation.Problem.Retryable {
				t.Fatalf("recovered operation = %#v", operation.Operation)
			}
			project, err := store.Project(t.Context(), seed.project.ID)
			if err != nil || project.Project.State != domain.ProjectFailed {
				t.Fatalf("recovered project = %#v, %v", project.Project, err)
			}
			if _, err := store.ActiveProjectSession(t.Context(), seed.project.ID); err == nil {
				t.Fatal("recovered project retained an active session")
			} else {
				var missing *state.ProjectSessionNotFoundError
				if !errors.As(err, &missing) {
					t.Fatalf("active session error = %v", err)
				}
			}
			if len(supervisor.settled) != 1 || supervisor.settled[0] != seed.evidence {
				t.Fatalf("settled evidence = %#v, want %#v", supervisor.settled, seed.evidence)
			}
			if routeCalls != 0 {
				t.Fatalf("daemon recovery reconciled routes before runtime startup %d times", routeCalls)
			}
		})
	}
}

// TestProjectLifecycleRecoverRetiresSettledTerminalSession proves a completed start cannot brick the next daemon launch.
func TestProjectLifecycleRecoverRetiresSettledTerminalSession(t *testing.T) {
	for _, outcome := range []projectprocess.PriorProcessSettlementOutcome{
		projectprocess.PriorProcessSettlementAbsent,
		projectprocess.PriorProcessSettlementReplaced,
		projectprocess.PriorProcessSettlementTerminated,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			seed := seedProjectLifecycleRecoveryStart(t, store, journal, true)
			seed = completeProjectLifecycleRecoveryStart(t, store, seed)
			supervisor := &projectLifecycleRecoverySupervisor{
				settlement: projectprocess.PriorProcessSettlement{Outcome: outcome},
			}
			routeCalls := 0
			coordinator := newProjectLifecycleAdmissionTestCoordinator(
				store,
				journal,
				store,
				supervisor,
				netip.MustParseAddr("127.0.0.1"),
			)
			coordinator.routes = projectLifecycleRouteReconcilerFunc(func(context.Context) error {
				routeCalls++
				return errors.New("route controller is not started during daemon recovery")
			})
			coordinator.now = func() time.Time { return seed.recoverAt }

			if err := coordinator.Recover(t.Context()); err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			operation, err := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
			if err != nil || operation.Operation.State != domain.OperationSucceeded {
				t.Fatalf("terminal operation after recovery = %#v, %v", operation.Operation, err)
			}
			project, err := store.Project(t.Context(), seed.project.ID)
			if err != nil || project.Project.State != domain.ProjectFailed {
				t.Fatalf("recovered project = %#v, %v", project.Project, err)
			}
			if _, err := store.ActiveProjectSession(t.Context(), seed.project.ID); err == nil {
				t.Fatal("recovered project retained an active session")
			} else {
				var missing *state.ProjectSessionNotFoundError
				if !errors.As(err, &missing) {
					t.Fatalf("active session error = %v", err)
				}
			}
			if len(supervisor.settled) != 1 || supervisor.settled[0] != seed.evidence {
				t.Fatalf("settled evidence = %#v, want %#v", supervisor.settled, seed.evidence)
			}
			if len(supervisor.observed) != 0 {
				t.Fatalf("terminal recovery separately observed evidence = %#v", supervisor.observed)
			}
			if routeCalls != 0 {
				t.Fatalf("daemon recovery reconciled routes before runtime startup %d times", routeCalls)
			}
		})
	}
}

// TestProjectLifecycleRecoverKeepsUnsettledTerminalSessionsFailClosed verifies failed settlement never retires durable authority.
func TestProjectLifecycleRecoverKeepsUnsettledTerminalSessionsFailClosed(t *testing.T) {
	sentinel := errors.New("host observation unavailable")
	tests := []struct {
		name          string
		settlement    projectprocess.PriorProcessSettlement
		settlementErr error
		want          string
	}{
		{name: "unknown", settlement: projectprocess.PriorProcessSettlement{Outcome: projectprocess.PriorProcessSettlementOutcome("unknown")}, want: "unsupported outcome"},
		{name: "settlement failure", settlementErr: sentinel, want: sentinel.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			seed := seedProjectLifecycleRecoveryStart(t, store, journal, true)
			seed = completeProjectLifecycleRecoveryStart(t, store, seed)
			supervisor := &projectLifecycleRecoverySupervisor{
				settlement:    test.settlement,
				settlementErr: test.settlementErr,
			}
			routeCalls := 0
			coordinator := newProjectLifecycleAdmissionTestCoordinator(
				store,
				journal,
				store,
				supervisor,
				netip.MustParseAddr("127.0.0.1"),
			)
			coordinator.routes = projectLifecycleRouteReconcilerFunc(func(context.Context) error {
				routeCalls++
				return nil
			})
			coordinator.now = func() time.Time { return seed.recoverAt }

			err := coordinator.Recover(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Recover() error = %v, want %q", err, test.want)
			}
			operation, readErr := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
			if readErr != nil || !reflect.DeepEqual(operation, seed.operation) {
				t.Fatalf("operation after failed recovery = %#v, %v", operation, readErr)
			}
			project, readErr := store.Project(t.Context(), seed.project.ID)
			if readErr != nil || !reflect.DeepEqual(project.Project, seed.project) {
				t.Fatalf("project after failed recovery = %#v, %v", project.Project, readErr)
			}
			session, readErr := store.ActiveProjectSession(t.Context(), seed.project.ID)
			if readErr != nil || !reflect.DeepEqual(session, seed.session) {
				t.Fatalf("session after failed recovery = %#v, %v", session, readErr)
			}
			if len(supervisor.settled) != 1 || supervisor.settled[0] != seed.evidence {
				t.Fatalf("settled evidence = %#v, want %#v", supervisor.settled, seed.evidence)
			}
			if routeCalls != 0 {
				t.Fatalf("failed recovery reconciled routes %d times", routeCalls)
			}
		})
	}
}

// TestProjectLifecycleRecoverKeepsUnsettledRunningStartsFailClosed verifies settlement failures never retire durable authority.
func TestProjectLifecycleRecoverKeepsUnsettledRunningStartsFailClosed(t *testing.T) {
	sentinel := errors.New("host observation unavailable")
	tests := []struct {
		name          string
		settlement    projectprocess.PriorProcessSettlement
		settlementErr error
		want          string
	}{
		{name: "unknown", settlement: projectprocess.PriorProcessSettlement{Outcome: projectprocess.PriorProcessSettlementOutcome("unknown")}, want: "unsupported outcome"},
		{name: "settlement failure", settlementErr: sentinel, want: sentinel.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			seed := seedProjectLifecycleRecoveryStart(t, store, journal, true)
			supervisor := &projectLifecycleRecoverySupervisor{
				settlement:    test.settlement,
				settlementErr: test.settlementErr,
			}
			routeCalls := 0
			coordinator := newProjectLifecycleAdmissionTestCoordinator(
				store,
				journal,
				store,
				supervisor,
				netip.MustParseAddr("127.0.0.1"),
			)
			coordinator.routes = projectLifecycleRouteReconcilerFunc(func(context.Context) error {
				routeCalls++
				return nil
			})
			coordinator.now = func() time.Time { return seed.recoverAt }

			err := coordinator.Recover(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Recover() error = %v, want %q", err, test.want)
			}
			operation, readErr := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
			if readErr != nil || operation.Operation.State != domain.OperationRunning {
				t.Fatalf("operation after failed recovery = %#v, %v", operation.Operation, readErr)
			}
			project, readErr := store.Project(t.Context(), seed.project.ID)
			if readErr != nil || project.Project.State != domain.ProjectStarting {
				t.Fatalf("project after failed recovery = %#v, %v", project.Project, readErr)
			}
			session, readErr := store.ActiveProjectSession(t.Context(), seed.project.ID)
			if readErr != nil || !reflect.DeepEqual(session, seed.session) {
				t.Fatalf("session after failed recovery = %#v, %v", session, readErr)
			}
			if len(supervisor.settled) != 1 || supervisor.settled[0] != seed.evidence {
				t.Fatalf("settled evidence = %#v, want %#v", supervisor.settled, seed.evidence)
			}
			if routeCalls != 0 {
				t.Fatalf("failed recovery reconciled routes %d times", routeCalls)
			}
		})
	}
}

// TestProjectLifecycleRecoverQuarantinesPlannedStart proves one pre-evidence launch cannot prevent daemon recovery.
func TestProjectLifecycleRecoverQuarantinesPlannedStart(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	seed := seedProjectLifecycleRecoveryStart(t, store, journal, false)
	queuedRoot, queuedPort := newProjectLifecycleIntegrationCheckout(t)
	if err := os.WriteFile(filepath.Join(queuedRoot, ".goforj.yml"), []byte("project_name: Reports\n"), 0o600); err != nil {
		t.Fatalf("write queued recovery marker: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(queuedRoot, ".env"),
		[]byte(fmt.Sprintf("APP_NAME=Reports\nAPI_HTTP_PORT=%d\n", queuedPort)),
		0o600,
	); err != nil {
		t.Fatalf("write queued recovery environment: %v", err)
	}
	queuedDiscovery, err := projectdiscovery.NewDiscoverer().Discover(t.Context(), queuedRoot)
	if err != nil {
		t.Fatalf("discover queued recovery project: %v", err)
	}
	queuedProject, err := queuedDiscovery.ProjectSnapshot("project-recovery-queued", time.Now().UTC())
	if err != nil {
		t.Fatalf("create queued recovery project: %v", err)
	}
	if _, err := store.RegisterProject(t.Context(), queuedProject); err != nil {
		t.Fatalf("register queued recovery project: %v", err)
	}
	queuedProjectRecord, err := store.Project(t.Context(), queuedProject.ID)
	if err != nil {
		t.Fatalf("read queued recovery project: %v", err)
	}
	queuedOperation, err := domain.NewOperation(
		"operation-recovery-queued-start",
		"intent-recovery-queued-start",
		domain.OperationKindProjectStart,
		queuedProject.ID,
		lifecycleTime(queuedProjectRecord.Project.UpdatedAt.Add(time.Second)),
	)
	if err != nil {
		t.Fatalf("create queued recovery operation: %v", err)
	}
	if _, err := journal.Enqueue(t.Context(), queuedOperation); err != nil {
		t.Fatalf("enqueue queued recovery operation: %v", err)
	}
	admissionEntered := make(chan struct{})
	supervisor := &projectLifecycleRecoverySupervisor{
		observation: projectprocess.PriorProcessObservation{State: projectprocess.PriorProcessAbsent},
	}
	coordinator := newProjectLifecycleAdmissionTestCoordinator(
		store,
		journal,
		&projectLifecycleBlockingLeaseState{entered: admissionEntered},
		supervisor,
		netip.MustParseAddr("127.0.0.1"),
	)
	t.Cleanup(func() {
		if err := coordinator.Close(context.Background()); err != nil {
			t.Errorf("close lifecycle coordinator: %v", err)
		}
	})

	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(supervisor.observed) != 0 || len(supervisor.settled) != 0 {
		t.Fatalf("planned recovery touched process evidence: observed %#v settled %#v", supervisor.observed, supervisor.settled)
	}
	operation, readErr := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
	if readErr != nil || operation.Operation.State != domain.OperationFailed ||
		operation.Operation.Problem == nil || operation.Operation.Problem.Code != projectRecoveryAmbiguousLaunchCode ||
		operation.Operation.Problem.Retryable {
		t.Fatalf("planned operation after recovery = %#v, %v", operation.Operation, readErr)
	}
	project, readErr := store.Project(t.Context(), seed.project.ID)
	if readErr != nil || project.Project.State != domain.ProjectUnavailable {
		t.Fatalf("planned project after recovery = %#v, %v", project.Project, readErr)
	}
	session, readErr := store.ActiveProjectSession(t.Context(), seed.project.ID)
	if readErr != nil || !reflect.DeepEqual(session, seed.session) {
		t.Fatalf("planned session after recovery = %#v, %v, want %#v", session, readErr, seed.session)
	}

	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("repeated Recover() error = %v", err)
	}
	queued, readErr := journal.OperationByIntent(t.Context(), queuedOperation.IntentID)
	if readErr != nil || queued.Operation.State != domain.OperationQueued {
		t.Fatalf("unrelated queued operation after recovery = %#v, %v", queued.Operation, readErr)
	}
	if err := coordinator.Resume(t.Context()); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	select {
	case <-admissionEntered:
	case <-time.After(time.Second):
		t.Fatal("Resume() did not dispatch the unrelated queued start")
	}
}

// seedProjectLifecycleRecoveryStart creates the exact durable crash boundary consumed by Recover.
func seedProjectLifecycleRecoveryStart(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
	attachProcess bool,
) projectLifecycleRecoverySeed {
	t.Helper()
	root, _ := newProjectLifecycleIntegrationCheckout(t)
	project := registerProjectLifecycleIntegrationProject(t, store, root)
	projectRecord, err := store.Project(t.Context(), project.ID)
	if err != nil {
		t.Fatalf("read recovery project: %v", err)
	}
	startAt := lifecycleTime(projectRecord.Project.UpdatedAt.Add(time.Second))
	operation, err := domain.NewOperation(
		"operation-recovery-start",
		"intent-recovery-start",
		domain.OperationKindProjectStart,
		project.ID,
		startAt,
	)
	if err != nil {
		t.Fatalf("create recovery operation: %v", err)
	}
	queued, err := journal.Enqueue(t.Context(), operation)
	if err != nil {
		t.Fatalf("enqueue recovery operation: %v", err)
	}
	session, err := newHarborProjectSession(project.ID, root, startAt)
	if err != nil {
		t.Fatalf("create recovery session: %v", err)
	}
	begun, err := store.BeginProjectStart(t.Context(), state.BeginProjectStartRequest{
		ProjectID:                 project.ID,
		OperationID:               queued.Operation.ID,
		ExpectedOperationRevision: queued.Revision,
		ExpectedProjectRevision:   projectRecord.Revision,
		Session:                   session,
		Phase:                     "launching",
		At:                        startAt,
	})
	if err != nil || begun.Session == nil {
		t.Fatalf("begin recovery start = %#v, %v", begun, err)
	}
	seed := projectLifecycleRecoverySeed{
		project:   begun.Project.Project,
		operation: begun.Operation,
		session:   *begun.Session,
		recoverAt: startAt.Add(2 * time.Second),
	}
	if !attachProcess {
		return seed
	}
	evidence := domain.ProcessEvidence{
		PID:                4242,
		BirthToken:         "test-process-birth",
		ExecutableIdentity: filepath.Join(root, "forj"),
		ArgumentDigest:     strings.Repeat("a", 64),
	}
	attached, err := store.AttachProjectProcess(t.Context(), state.AttachProjectProcessRequest{
		ProjectID:                 project.ID,
		SessionID:                 session.ID,
		ExpectedSessionGeneration: session.Generation,
		Process:                   evidence,
		At:                        startAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("attach recovery process: %v", err)
	}
	seed.session = attached
	seed.evidence = evidence
	return seed
}

// completeProjectLifecycleRecoveryStart advances a process-backed seed through the terminal readiness boundary.
func completeProjectLifecycleRecoveryStart(
	t *testing.T,
	store *state.Store,
	seed projectLifecycleRecoverySeed,
) projectLifecycleRecoverySeed {
	t.Helper()
	if seed.session.State != domain.SessionAwaitingAttach {
		t.Fatalf("recovery session state = %q, want %q", seed.session.State, domain.SessionAwaitingAttach)
	}
	target, err := projectdiscovery.NewRuntimeTarget(
		"app",
		"App",
		netip.MustParseAddr("127.77.0.11"),
		3000,
	)
	if err != nil {
		t.Fatalf("create recovery runtime target: %v", err)
	}
	readyAt := seed.session.UpdatedAt.Add(time.Second)
	completed, err := store.CompleteProjectStart(t.Context(), state.CompleteProjectStartRequest{
		ProjectID:                 seed.project.ID,
		OperationID:               seed.operation.Operation.ID,
		ExpectedOperationRevision: seed.operation.Revision,
		SessionID:                 seed.session.ID,
		ExpectedSessionGeneration: seed.session.Generation,
		Runtime:                   defaultRuntime(target),
		Phase:                     "ready",
		At:                        readyAt,
	})
	if err != nil || completed.Session == nil {
		t.Fatalf("complete recovery start = %#v, %v", completed, err)
	}
	seed.project = completed.Project.Project
	seed.operation = completed.Operation
	seed.session = *completed.Session
	seed.recoverAt = readyAt.Add(time.Second)
	return seed
}
