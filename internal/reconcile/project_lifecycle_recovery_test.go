package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// projectLifecycleRecoverySupervisor exposes one deterministic prior-process observation without owning a live process.
type projectLifecycleRecoverySupervisor struct {
	observation    projectprocess.PriorProcessObservation
	observationErr error
	observed       []domain.ProcessEvidence
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

// ObservePriorProcess returns the configured host classification and records the exact evidence inspected.
func (supervisor *projectLifecycleRecoverySupervisor) ObservePriorProcess(
	_ context.Context,
	evidence domain.ProcessEvidence,
) (projectprocess.PriorProcessObservation, error) {
	supervisor.observed = append(supervisor.observed, evidence)
	return supervisor.observation, supervisor.observationErr
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

// TestProjectLifecycleRecoverFailsStartAfterPriorProcessAbsence proves absent and reused PIDs converge without deleting unrelated state.
func TestProjectLifecycleRecoverFailsStartAfterPriorProcessAbsence(t *testing.T) {
	for _, priorState := range []projectprocess.PriorProcessState{
		projectprocess.PriorProcessAbsent,
		projectprocess.PriorProcessReplaced,
	} {
		t.Run(string(priorState), func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			seed := seedProjectLifecycleRecoveryStart(t, store, journal, true)
			supervisor := &projectLifecycleRecoverySupervisor{
				observation: projectprocess.PriorProcessObservation{State: priorState},
			}
			routes := &projectLifecycleRecordingRouteReconciler{store: store, projectID: seed.project.ID}
			coordinator := newProjectLifecycleAdmissionTestCoordinator(
				store,
				journal,
				store,
				supervisor,
				netip.MustParseAddr("127.0.0.1"),
			)
			coordinator.routes = routes
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
			if len(supervisor.observed) != 1 || supervisor.observed[0] != seed.evidence {
				t.Fatalf("observed evidence = %#v, want %#v", supervisor.observed, seed.evidence)
			}
			if got := routes.States(); len(got) != 1 || got[0] != domain.ProjectFailed {
				t.Fatalf("route reconciliation states = %v, want [%s]", got, domain.ProjectFailed)
			}
		})
	}
}

// TestProjectLifecycleRecoverKeepsUncertainRunningStartsFailClosed verifies host uncertainty never retires durable authority.
func TestProjectLifecycleRecoverKeepsUncertainRunningStartsFailClosed(t *testing.T) {
	sentinel := errors.New("host observation unavailable")
	tests := []struct {
		name           string
		observation    projectprocess.PriorProcessObservation
		observationErr error
		want           string
	}{
		{name: "present", observation: projectprocess.PriorProcessObservation{State: projectprocess.PriorProcessPresent}, want: "prior process ownership"},
		{name: "unknown", observation: projectprocess.PriorProcessObservation{State: projectprocess.PriorProcessState("unknown")}, want: "unsupported state"},
		{name: "observation failure", observationErr: sentinel, want: sentinel.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, journal := newProjectLifecycleIntegrationState(t)
			seed := seedProjectLifecycleRecoveryStart(t, store, journal, true)
			supervisor := &projectLifecycleRecoverySupervisor{
				observation:    test.observation,
				observationErr: test.observationErr,
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
			if routeCalls != 0 {
				t.Fatalf("failed recovery reconciled routes %d times", routeCalls)
			}
		})
	}
}

// TestProjectLifecycleRecoverLeavesPlannedStartFailClosed proves recovery never guesses across the pre-evidence launch window.
func TestProjectLifecycleRecoverLeavesPlannedStartFailClosed(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	seed := seedProjectLifecycleRecoveryStart(t, store, journal, false)
	supervisor := &projectLifecycleRecoverySupervisor{
		observation: projectprocess.PriorProcessObservation{State: projectprocess.PriorProcessAbsent},
	}
	coordinator := newProjectLifecycleAdmissionTestCoordinator(
		store,
		journal,
		store,
		supervisor,
		netip.MustParseAddr("127.0.0.1"),
	)

	err := coordinator.Recover(t.Context())
	if err == nil || !strings.Contains(err.Error(), "state \"planned\"") {
		t.Fatalf("Recover() error = %v, want planned-session ownership failure", err)
	}
	if len(supervisor.observed) != 0 {
		t.Fatalf("planned recovery observed process evidence = %#v", supervisor.observed)
	}
	operation, readErr := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
	if readErr != nil || operation.Operation.State != domain.OperationRunning {
		t.Fatalf("planned operation after recovery = %#v, %v", operation.Operation, readErr)
	}
}

// TestProjectLifecycleRecoverReportsRouteFailureAfterDurableSettlement proves publication errors cannot restore stale process authority.
func TestProjectLifecycleRecoverReportsRouteFailureAfterDurableSettlement(t *testing.T) {
	store, journal := newProjectLifecycleIntegrationState(t)
	seed := seedProjectLifecycleRecoveryStart(t, store, journal, true)
	supervisor := &projectLifecycleRecoverySupervisor{
		observation: projectprocess.PriorProcessObservation{State: projectprocess.PriorProcessAbsent},
	}
	sentinel := errors.New("route publication unavailable")
	coordinator := newProjectLifecycleAdmissionTestCoordinator(
		store,
		journal,
		store,
		supervisor,
		netip.MustParseAddr("127.0.0.1"),
	)
	coordinator.routes = projectLifecycleRouteReconcilerFunc(func(context.Context) error { return sentinel })
	coordinator.now = func() time.Time { return seed.recoverAt }

	err := coordinator.Recover(t.Context())
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("Recover() error = %v, want %v", err, sentinel)
	}
	operation, readErr := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
	if readErr != nil || operation.Operation.State != domain.OperationFailed {
		t.Fatalf("settled operation = %#v, %v", operation.Operation, readErr)
	}
	if _, readErr := store.ActiveProjectSession(t.Context(), seed.project.ID); readErr == nil {
		t.Fatal("route failure restored the retired project session")
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
		project:   project,
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
