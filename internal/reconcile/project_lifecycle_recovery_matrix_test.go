package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// projectLifecycleRecoveryBoundary names one process-backed operation shape that a daemon restart must settle.
type projectLifecycleRecoveryBoundary struct {
	name string
	seed func(*testing.T, *state.Store, *state.OperationJournal) projectLifecycleRecoverySeed
}

// TestProjectLifecycleRecoverSettlesProcessBackedOperations covers every currently durable process-backed restart boundary.
func TestProjectLifecycleRecoverSettlesProcessBackedOperations(t *testing.T) {
	for _, boundary := range projectLifecycleRecoveryBoundaries() {
		boundary := boundary
		for _, outcome := range projectLifecycleRecoverySettlementOutcomes() {
			outcome := outcome
			t.Run(boundary.name+"/"+string(outcome), func(t *testing.T) {
				store, journal := newProjectLifecycleIntegrationState(t)
				seed := boundary.seed(t, store, journal)
				supervisor := &projectLifecycleRecoverySupervisor{
					settlement: projectprocess.PriorProcessSettlement{Outcome: outcome},
				}
				coordinator, routeCalls := newProjectLifecycleRecoveryMatrixCoordinator(
					t,
					store,
					journal,
					supervisor,
					seed.recoverAt,
				)

				if err := coordinator.Recover(t.Context()); err != nil {
					t.Fatalf("Recover() error = %v", err)
				}
				assertProjectLifecycleRecoverySettled(t, store, journal, seed)
				assertProjectLifecycleRecoverySettlementCall(t, supervisor, seed.evidence)
				if *routeCalls != 0 {
					t.Fatalf("daemon recovery reconciled routes before runtime startup %d times", *routeCalls)
				}
			})
		}
	}
}

// TestProjectLifecycleRecoverQuarantinesUnsettledProcessBackedOperations preserves authority without aborting daemon recovery.
func TestProjectLifecycleRecoverQuarantinesUnsettledProcessBackedOperations(t *testing.T) {
	sentinel := errors.New("prior process settlement unavailable")
	tests := []struct {
		name          string
		settlement    projectprocess.PriorProcessSettlement
		settlementErr error
	}{
		{
			name:       "unknown outcome",
			settlement: projectprocess.PriorProcessSettlement{Outcome: projectprocess.PriorProcessSettlementOutcome("unknown")},
		},
		{name: "settlement error", settlementErr: sentinel},
	}

	for _, boundary := range projectLifecycleRecoveryBoundaries() {
		boundary := boundary
		for _, test := range tests {
			test := test
			t.Run(boundary.name+"/"+test.name, func(t *testing.T) {
				store, journal := newProjectLifecycleIntegrationState(t)
				seed := boundary.seed(t, store, journal)
				supervisor := &projectLifecycleRecoverySupervisor{
					settlement:    test.settlement,
					settlementErr: test.settlementErr,
				}
				coordinator, routeCalls := newProjectLifecycleRecoveryMatrixCoordinator(
					t,
					store,
					journal,
					supervisor,
					seed.recoverAt,
				)

				if err := coordinator.Recover(t.Context()); err != nil {
					t.Fatalf("Recover() error = %v", err)
				}
				assertProjectLifecycleRecoveryQuarantined(t, store, journal, seed)
				assertProjectLifecycleRecoverySettlementCall(t, supervisor, seed.evidence)
				if *routeCalls != 0 {
					t.Fatalf("failed recovery reconciled routes %d times", *routeCalls)
				}
			})
		}
	}
}

// projectLifecycleRecoveryBoundaries returns the operation states that can retain exact process authority across daemon replacement.
func projectLifecycleRecoveryBoundaries() []projectLifecycleRecoveryBoundary {
	return []projectLifecycleRecoveryBoundary{
		{name: "running start", seed: seedProjectLifecycleRecoveryRunningStart},
		{name: "queued stop", seed: seedProjectLifecycleRecoveryQueuedStop},
		{name: "running stop", seed: seedProjectLifecycleRecoveryRunningStop},
	}
}

// projectLifecycleRecoverySettlementOutcomes returns every safe terminal classification produced by the process supervisor.
func projectLifecycleRecoverySettlementOutcomes() []projectprocess.PriorProcessSettlementOutcome {
	return []projectprocess.PriorProcessSettlementOutcome{
		projectprocess.PriorProcessSettlementAbsent,
		projectprocess.PriorProcessSettlementReplaced,
		projectprocess.PriorProcessSettlementTerminated,
	}
}

// seedProjectLifecycleRecoveryRunningStart creates a process-backed start interrupted before readiness.
func seedProjectLifecycleRecoveryRunningStart(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
) projectLifecycleRecoverySeed {
	t.Helper()
	return seedProjectLifecycleRecoveryStart(t, store, journal, true)
}

// seedProjectLifecycleRecoveryQueuedStop creates a ready session whose stop intent was durable before its first state transition.
func seedProjectLifecycleRecoveryQueuedStop(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
) projectLifecycleRecoverySeed {
	t.Helper()
	seed := completeProjectLifecycleRecoveryStart(t, store, seedProjectLifecycleRecoveryStart(t, store, journal, true))
	requestedAt := seed.recoverAt
	operation, err := domain.NewOperation(
		"operation-recovery-stop",
		"intent-recovery-stop",
		domain.OperationKindProjectStop,
		seed.project.ID,
		requestedAt,
	)
	if err != nil {
		t.Fatalf("create recovery stop operation: %v", err)
	}
	queued, err := journal.Enqueue(t.Context(), operation)
	if err != nil {
		t.Fatalf("enqueue recovery stop operation: %v", err)
	}
	seed.operation = queued
	seed.recoverAt = requestedAt.Add(lifecyclePersistenceDelay)
	return seed
}

// seedProjectLifecycleRecoveryRunningStop creates a process-backed stop interrupted after durable process fencing.
func seedProjectLifecycleRecoveryRunningStop(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
) projectLifecycleRecoverySeed {
	t.Helper()
	seed := seedProjectLifecycleRecoveryQueuedStop(t, store, journal)
	stopAt := seed.recoverAt
	stopping, err := store.BeginProjectStop(t.Context(), state.BeginProjectStopRequest{
		ProjectID:                 seed.project.ID,
		OperationID:               seed.operation.Operation.ID,
		ExpectedOperationRevision: seed.operation.Revision,
		SessionID:                 seed.session.ID,
		ExpectedSessionGeneration: seed.session.Generation,
		Phase:                     "stopping",
		At:                        stopAt,
	})
	if err != nil || stopping.Session == nil {
		t.Fatalf("begin recovery stop = %#v, %v", stopping, err)
	}
	seed.project = stopping.Project.Project
	seed.operation = stopping.Operation
	seed.session = *stopping.Session
	seed.recoverAt = stopAt.Add(lifecyclePersistenceDelay)
	return seed
}

// newProjectLifecycleRecoveryMatrixCoordinator wires a fail-if-called route seam around one deterministic recovery instant.
func newProjectLifecycleRecoveryMatrixCoordinator(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
	supervisor *projectLifecycleRecoverySupervisor,
	recoverAt time.Time,
) (*ProjectLifecycleCoordinator, *int) {
	t.Helper()
	coordinator := newProjectLifecycleAdmissionTestCoordinator(
		store,
		journal,
		store,
		supervisor,
		netip.MustParseAddr("127.0.0.1"),
	)
	routeCalls := 0
	coordinator.routes = projectLifecycleRouteReconcilerFunc(func(context.Context) error {
		routeCalls++
		return errors.New("route controller is not started during daemon recovery")
	})
	coordinator.now = func() time.Time { return recoverAt }
	t.Cleanup(func() {
		if err := coordinator.Close(context.Background()); err != nil {
			t.Errorf("close recovery coordinator: %v", err)
		}
	})
	return coordinator, &routeCalls
}

// assertProjectLifecycleRecoverySettled verifies recovery reached the terminal projection appropriate to the interrupted intent.
func assertProjectLifecycleRecoverySettled(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
	seed projectLifecycleRecoverySeed,
) {
	t.Helper()
	operation, err := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
	if err != nil {
		t.Fatalf("read settled recovery operation: %v", err)
	}
	wantOperation := domain.OperationSucceeded
	wantProject := domain.ProjectStopped
	if seed.operation.Operation.Kind == domain.OperationKindProjectStart {
		wantOperation = domain.OperationFailed
		wantProject = domain.ProjectFailed
	}
	if operation.Operation.State != wantOperation {
		t.Fatalf("settled operation state = %q, want %q", operation.Operation.State, wantOperation)
	}
	project, err := store.Project(t.Context(), seed.project.ID)
	if err != nil || project.Project.State != wantProject {
		t.Fatalf("settled project = %#v, %v, want state %q", project.Project, err, wantProject)
	}
	if _, err := store.ActiveProjectSession(t.Context(), seed.project.ID); err == nil {
		t.Fatal("settled project retained an active session")
	} else {
		var missing *state.ProjectSessionNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("active session error = %v", err)
		}
	}
}

// assertProjectLifecycleRecoveryQuarantined verifies inconclusive settlement becomes route-free without discarding evidence.
func assertProjectLifecycleRecoveryQuarantined(
	t *testing.T,
	store *state.Store,
	journal *state.OperationJournal,
	seed projectLifecycleRecoverySeed,
) {
	t.Helper()
	operation, err := journal.OperationByIntent(t.Context(), seed.operation.Operation.IntentID)
	if err != nil || operation.Operation.State != domain.OperationFailed || operation.Operation.Problem == nil ||
		operation.Operation.Problem.Code != projectRecoveryAmbiguousLaunchCode {
		t.Fatalf("operation after quarantine = %#v, %v", operation, err)
	}
	project, err := store.Project(t.Context(), seed.project.ID)
	if err != nil || project.Project.State != domain.ProjectUnavailable {
		t.Fatalf("project after quarantine = %#v, %v", project.Project, err)
	}
	session, err := store.ActiveProjectSession(t.Context(), seed.project.ID)
	if err != nil || session.Process == nil || *session.Process != seed.evidence {
		t.Fatalf("session after quarantine = %#v, %v, want evidence %#v", session, err, seed.evidence)
	}
}

// assertProjectLifecycleRecoverySettlementCall verifies recovery touched only the exact persisted process birth.
func assertProjectLifecycleRecoverySettlementCall(
	t *testing.T,
	supervisor *projectLifecycleRecoverySupervisor,
	want domain.ProcessEvidence,
) {
	t.Helper()
	if len(supervisor.settled) != 1 || supervisor.settled[0] != want {
		t.Fatalf("settled evidence = %#v, want %#v", supervisor.settled, want)
	}
	if len(supervisor.observed) != 0 {
		t.Fatalf("recovery separately observed evidence = %#v", supervisor.observed)
	}
}
