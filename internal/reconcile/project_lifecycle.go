package reconcile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/projectreadiness"
	"github.com/goforj/harbor/internal/state"
)

const (
	defaultProjectStartupTimeout = 2 * time.Minute
	defaultReadinessInterval     = 150 * time.Millisecond
	defaultReadinessHTTPTimeout  = time.Second
	lifecyclePersistenceAttempts = 3
	lifecyclePersistenceDelay    = 20 * time.Millisecond
)

const (
	// projectRecoveryAmbiguousLaunchCode identifies a launch whose process identity was not durable before daemon replacement.
	projectRecoveryAmbiguousLaunchCode domain.ProblemCode = "project.recovery.ambiguous_launch"
	// projectRecoveryQuarantinePhase keeps the terminal operation distinct from an ordinary launch failure.
	projectRecoveryQuarantinePhase = "recovery required"
)

// ProjectStartRequest identifies one daemon-owned start operation and its client-stable intent.
type ProjectStartRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	IntentID    domain.IntentID
}

// ProjectStopRequest identifies one daemon-owned stop operation and its client-stable intent.
type ProjectStopRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	IntentID    domain.IntentID
}

// projectLifecycleState is the durable aggregate surface required by managed process reconciliation.
type projectLifecycleState interface {
	Project(context.Context, domain.ProjectID) (state.ProjectRecord, error)
	Snapshot(context.Context) (domain.Snapshot, error)
	ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error)
	BeginProjectStart(context.Context, state.BeginProjectStartRequest) (state.ProjectLifecycleMutation, error)
	AttachProjectProcess(context.Context, state.AttachProjectProcessRequest) (domain.ProjectSession, error)
	CompleteProjectStart(context.Context, state.CompleteProjectStartRequest) (state.ProjectLifecycleMutation, error)
	FailProjectStart(context.Context, state.FailProjectStartRequest) (state.ProjectLifecycleMutation, error)
	QuarantinePlannedProjectStart(context.Context, state.QuarantinePlannedProjectStartRequest) (state.ProjectLifecycleMutation, error)
	BeginProjectStop(context.Context, state.BeginProjectStopRequest) (state.ProjectLifecycleMutation, error)
	CompleteProjectStop(context.Context, state.CompleteProjectStopRequest) (state.ProjectLifecycleMutation, error)
	RecordUnexpectedProjectExit(context.Context, state.RecordUnexpectedProjectExitRequest) (state.ProjectRecord, error)
}

// projectLifecycleJournal is the durable idempotency and recovery surface required by project lifecycle operations.
type projectLifecycleJournal interface {
	Enqueue(context.Context, domain.Operation) (state.OperationRecord, error)
	Transition(context.Context, domain.OperationID, domain.Sequence, domain.OperationState, string, time.Time, *domain.Problem) (state.OperationRecord, error)
	FailQueued(context.Context, domain.OperationID, domain.Sequence, string, string, time.Time, domain.Problem) (state.OperationRecord, error)
	OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error)
	LatestProjectLifecycleOperation(context.Context, domain.ProjectID) (state.OperationRecord, error)
	ActiveOperations(context.Context) ([]state.OperationRecord, error)
}

// projectReadinessProber performs one bounded readiness observation.
type projectReadinessProber interface {
	Probe(context.Context, projectdiscovery.RuntimeTarget) (projectreadiness.State, error)
}

// projectProcessSupervisor owns exact project process trees for the daemon lifetime.
type projectProcessSupervisor interface {
	Start(context.Context, projectprocess.StartRequest) (*projectprocess.Handle, error)
	Stop(context.Context, domain.ProjectID, domain.SessionID) error
	ReadOutput(domain.ProjectID, domain.SessionID, uint64) projectprocess.OutputChunk
	ObservePriorProcess(context.Context, domain.ProcessEvidence) (projectprocess.PriorProcessObservation, error)
	SettlePriorProcess(context.Context, domain.ProcessEvidence) (projectprocess.PriorProcessSettlement, error)
	Close(context.Context) error
}

// ProjectRouteReconciler projects durable project lifecycle changes into Harbor's live route table.
type ProjectRouteReconciler interface {
	Reconcile(context.Context) error
}

// ProjectLifecycleCoordinator turns durable start and stop intents into supervised GoForj development processes.
type ProjectLifecycleCoordinator struct {
	state             projectLifecycleState
	operations        projectLifecycleJournal
	primaryLeases     *projectPrimaryLeaseCoordinator
	readiness         projectReadinessProber
	supervisor        projectProcessSupervisor
	routes            ProjectRouteReconciler
	now               func() time.Time
	newOperationID    func() (domain.OperationID, error)
	newIntentID       func() (domain.IntentID, error)
	newSession        func(domain.ProjectID, string, time.Time) (domain.ProjectSession, error)
	startupTimeout    time.Duration
	readinessInterval time.Duration
	ctx               context.Context
	cancel            context.CancelFunc
	mutex             sync.Mutex
	closed            bool
	closeDone         chan struct{}
	closeErr          error
	asyncErr          error
	dispatched        map[domain.OperationID]struct{}
	recoveredStarts   []state.OperationRecord
	handles           map[domain.ProjectID]*projectprocess.Handle
	wait              sync.WaitGroup
}

// NewProjectLifecycleCoordinator creates the production managed-process reconciler.
func NewProjectLifecycleCoordinator(
	projectState *state.Store,
	operations *state.OperationJournal,
	supervisor *projectprocess.Supervisor,
	routes ProjectRouteReconciler,
) *ProjectLifecycleCoordinator {
	if projectState == nil || operations == nil || supervisor == nil || nilDependency(routes) {
		panic("reconcile.NewProjectLifecycleCoordinator requires non-nil state, journal, supervisor, and route dependencies")
	}
	discoverer := projectdiscovery.NewDiscoverer()
	return newProjectLifecycleCoordinator(
		projectState,
		operations,
		newSystemProjectPrimaryLeaseCoordinator(projectState, discoverer),
		projectreadiness.NewProber(&http.Client{Timeout: defaultReadinessHTTPTimeout}),
		supervisor,
		routes,
		time.Now,
		newLifecycleOperationID,
		newLifecycleIntentID,
		newHarborProjectSession,
		defaultProjectStartupTimeout,
		defaultReadinessInterval,
	)
}

// newProjectLifecycleCoordinator keeps clocks, identity, discovery, process, and readiness boundaries deterministic in tests.
func newProjectLifecycleCoordinator(
	projectState projectLifecycleState,
	operations projectLifecycleJournal,
	primaryLeases *projectPrimaryLeaseCoordinator,
	readiness projectReadinessProber,
	supervisor projectProcessSupervisor,
	routes ProjectRouteReconciler,
	now func() time.Time,
	newOperationID func() (domain.OperationID, error),
	newIntentID func() (domain.IntentID, error),
	newSession func(domain.ProjectID, string, time.Time) (domain.ProjectSession, error),
	startupTimeout time.Duration,
	readinessInterval time.Duration,
) *ProjectLifecycleCoordinator {
	if nilDependency(projectState) || nilDependency(operations) || nilDependency(primaryLeases) ||
		nilDependency(readiness) || nilDependency(supervisor) || nilDependency(routes) || nilDependency(now) ||
		nilDependency(newOperationID) || nilDependency(newIntentID) || nilDependency(newSession) {
		panic("reconcile.newProjectLifecycleCoordinator requires every dependency")
	}
	if startupTimeout <= 0 || readinessInterval <= 0 {
		panic("reconcile.newProjectLifecycleCoordinator requires positive readiness bounds")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ProjectLifecycleCoordinator{
		state:             projectState,
		operations:        operations,
		primaryLeases:     primaryLeases,
		readiness:         readiness,
		supervisor:        supervisor,
		routes:            routes,
		now:               now,
		newOperationID:    newOperationID,
		newIntentID:       newIntentID,
		newSession:        newSession,
		startupTimeout:    startupTimeout,
		readinessInterval: readinessInterval,
		ctx:               ctx,
		cancel:            cancel,
		dispatched:        make(map[domain.OperationID]struct{}),
		handles:           make(map[domain.ProjectID]*projectprocess.Handle),
		closeDone:         make(chan struct{}),
	}
}

// Start durably journals one idempotent intent before scheduling its supervised process launch.
func (coordinator *ProjectLifecycleCoordinator) Start(ctx context.Context, request ProjectStartRequest) (state.OperationRecord, error) {
	if err := validateProjectStartRequest(request); err != nil {
		return state.OperationRecord{}, err
	}
	return coordinator.enqueue(ctx, request.ProjectID, request.OperationID, request.IntentID, domain.OperationKindProjectStart)
}

// Stop durably journals one idempotent intent before scheduling exact-session process shutdown.
func (coordinator *ProjectLifecycleCoordinator) Stop(ctx context.Context, request ProjectStopRequest) (state.OperationRecord, error) {
	if err := validateProjectStopRequest(request); err != nil {
		return state.OperationRecord{}, err
	}
	return coordinator.enqueue(ctx, request.ProjectID, request.OperationID, request.IntentID, domain.OperationKindProjectStop)
}

// enqueue preserves exact client idempotency while rejecting new lifecycle work after shutdown begins.
func (coordinator *ProjectLifecycleCoordinator) enqueue(
	ctx context.Context,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	intentID domain.IntentID,
	kind domain.OperationKind,
) (state.OperationRecord, error) {
	ctx = normalizeLifecycleContext(ctx)
	coordinator.mutex.Lock()
	closed := coordinator.closed
	coordinator.mutex.Unlock()
	if closed {
		return state.OperationRecord{}, errors.New("project lifecycle coordinator is closed")
	}

	existing, err := coordinator.operations.OperationByIntent(ctx, intentID)
	if err == nil {
		if existing.Operation.ProjectID != projectID || existing.Operation.Kind != kind {
			return state.OperationRecord{}, &state.IntentConflictError{
				IntentID:            intentID,
				ExistingOperationID: existing.Operation.ID,
				ExistingKind:        existing.Operation.Kind,
				ExistingProjectID:   existing.Operation.ProjectID,
				RequestedKind:       kind,
				RequestedProjectID:  projectID,
			}
		}
		coordinator.dispatch(existing)
		return existing, nil
	}
	var missingIntent *state.OperationIntentNotFoundError
	if !errors.As(err, &missingIntent) {
		return state.OperationRecord{}, err
	}

	project, err := coordinator.state.Project(ctx, projectID)
	if err != nil {
		return state.OperationRecord{}, err
	}
	if err := validateNewLifecycleState(coordinator.state, ctx, project.Project, kind); err != nil {
		return state.OperationRecord{}, err
	}
	requestedAt := lifecycleTime(coordinator.now())
	if requestedAt.Before(project.Project.UpdatedAt) {
		requestedAt = project.Project.UpdatedAt
	}
	operation, err := domain.NewOperation(operationID, intentID, kind, projectID, requestedAt)
	if err != nil {
		return state.OperationRecord{}, err
	}
	record, err := coordinator.operations.Enqueue(ctx, operation)
	if err != nil {
		return state.OperationRecord{}, err
	}
	coordinator.dispatch(record)
	return record, nil
}

// dispatch ensures concurrent retries share one process worker per durable operation.
func (coordinator *ProjectLifecycleCoordinator) dispatch(record state.OperationRecord) {
	if record.Operation.State.IsTerminal() {
		return
	}
	coordinator.mutex.Lock()
	if coordinator.closed {
		coordinator.mutex.Unlock()
		return
	}
	if _, exists := coordinator.dispatched[record.Operation.ID]; exists {
		coordinator.mutex.Unlock()
		return
	}
	coordinator.dispatched[record.Operation.ID] = struct{}{}
	coordinator.wait.Add(1)
	coordinator.mutex.Unlock()

	go func() {
		defer coordinator.wait.Done()
		defer coordinator.finishDispatch(record.Operation.ID)
		switch record.Operation.Kind {
		case domain.OperationKindProjectStart:
			coordinator.runStart(record)
		case domain.OperationKindProjectStop:
			coordinator.runStop(record)
		}
	}()
}

// finishDispatch releases one operation key after its worker reaches a durable boundary.
func (coordinator *ProjectLifecycleCoordinator) finishDispatch(operationID domain.OperationID) {
	coordinator.mutex.Lock()
	delete(coordinator.dispatched, operationID)
	coordinator.mutex.Unlock()
}

// cancelQueued makes a pre-effect worker failure terminal so recovery never inherits unexplained queued work.
func (coordinator *ProjectLifecycleCoordinator) cancelQueued(record state.OperationRecord, cause error) {
	if err := coordinator.transitionQueuedCancellation(record); err != nil {
		coordinator.recordAsyncError(err)
		if !lifecycleContextEnded(cause) {
			coordinator.recordAsyncError(cause)
		}
		return
	}
	if !lifecycleContextEnded(cause) {
		coordinator.recordAsyncError(cause)
	}
}

// failQueuedAdmission records a correctable pre-launch rejection without treating it as daemon health failure.
func (coordinator *ProjectLifecycleCoordinator) failQueuedAdmission(
	record state.OperationRecord,
	problem domain.Problem,
) {
	if err := coordinator.transitionQueuedAdmissionFailure(record, problem); err != nil {
		coordinator.recordAsyncError(err)
	}
}

// transitionQueuedAdmissionFailure gives a failed operation its required running edge without creating process authority.
func (coordinator *ProjectLifecycleCoordinator) transitionQueuedAdmissionFailure(
	record state.OperationRecord,
	problem domain.Problem,
) error {
	at := lifecycleTime(coordinator.now())
	if at.Before(record.Operation.RequestedAt) {
		at = record.Operation.RequestedAt
	}
	if _, err := coordinator.operations.FailQueued(
		context.Background(),
		record.Operation.ID,
		record.Revision,
		"checking project network",
		"network admission failed",
		at,
		problem,
	); err != nil {
		return fmt.Errorf("fail rejected lifecycle operation %q: %w", record.Operation.ID, err)
	}
	return nil
}

// lifecycleContextEnded distinguishes intentional caller or daemon cancellation from operational failure.
func lifecycleContextEnded(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// transitionQueuedCancellation commits the only safe terminal edge before lifecycle effects begin.
func (coordinator *ProjectLifecycleCoordinator) transitionQueuedCancellation(record state.OperationRecord) error {
	at := lifecycleTime(coordinator.now())
	if at.Before(record.Operation.RequestedAt) {
		at = record.Operation.RequestedAt
	}
	if _, err := coordinator.operations.Transition(
		context.Background(),
		record.Operation.ID,
		record.Revision,
		domain.OperationCancelled,
		"lifecycle prerequisites unavailable",
		at,
		nil,
	); err != nil {
		return fmt.Errorf("cancel queued lifecycle operation %q: %w", record.Operation.ID, err)
	}
	return nil
}

// recordAsyncError retains background failures until daemon shutdown can report them to its authority owner.
func (coordinator *ProjectLifecycleCoordinator) recordAsyncError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	coordinator.mutex.Lock()
	coordinator.asyncErr = errors.Join(coordinator.asyncErr, err)
	coordinator.mutex.Unlock()
}

// retryLifecycleResult retries idempotent durable mutations so a transient writer failure cannot strand a lifecycle edge.
func retryLifecycleResult[T any](call func() (T, error)) (T, error) {
	var zero T
	var err error
	for attempt := 0; attempt < lifecyclePersistenceAttempts; attempt++ {
		result, callErr := call()
		if callErr == nil {
			return result, nil
		}
		err = callErr
		if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
			return zero, callErr
		}
		if attempt+1 < lifecyclePersistenceAttempts {
			time.Sleep(lifecyclePersistenceDelay)
		}
	}
	return zero, err
}

// reconcileProjectRoutes retries one state-derived route edge before reporting that lifecycle publication is incomplete.
func (coordinator *ProjectLifecycleCoordinator) reconcileProjectRoutes(ctx context.Context, phase string) error {
	ctx = normalizeLifecycleContext(ctx)
	var err error
	for attempt := 0; attempt < lifecyclePersistenceAttempts; attempt++ {
		if callErr := coordinator.routes.Reconcile(ctx); callErr == nil {
			return nil
		} else {
			err = callErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", phase, ctxErr)
		}
		if attempt+1 < lifecyclePersistenceAttempts {
			timer := time.NewTimer(lifecyclePersistenceDelay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return fmt.Errorf("%s: %w", phase, ctx.Err())
			}
		}
	}
	return fmt.Errorf("%s after %d attempts: %w", phase, lifecyclePersistenceAttempts, err)
}

// runStart advances one queued operation through launch evidence and proven readiness.
func (coordinator *ProjectLifecycleCoordinator) runStart(record state.OperationRecord) {
	if record.Operation.State != domain.OperationQueued {
		return
	}
	admission, err := coordinator.primaryLeases.Ensure(coordinator.ctx, record.Operation.ProjectID)
	if err != nil {
		if ctxErr := coordinator.ctx.Err(); ctxErr != nil {
			coordinator.cancelQueued(record, ctxErr)
			return
		}
		if lifecycleContextEnded(err) {
			coordinator.cancelQueued(record, err)
			return
		}
		var rejection *projectPrimaryLeaseRejection
		if errors.As(err, &rejection) {
			coordinator.failQueuedAdmission(record, rejection.Problem())
			return
		}
		coordinator.cancelQueued(record, err)
		return
	}
	project := admission.Project
	at := lifecycleTime(coordinator.now())
	if at.Before(project.Project.UpdatedAt) {
		at = project.Project.UpdatedAt
	}
	if at.Before(admission.NetworkUpdatedAt) {
		at = admission.NetworkUpdatedAt
	}
	session, err := coordinator.newSession(record.Operation.ProjectID, project.Project.Path, at)
	if err != nil {
		coordinator.cancelQueued(record, err)
		return
	}
	begun, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.BeginProjectStart(coordinator.ctx, state.BeginProjectStartRequest{
			ProjectID:                 record.Operation.ProjectID,
			OperationID:               record.Operation.ID,
			ExpectedOperationRevision: record.Revision,
			ExpectedProjectRevision:   project.Revision,
			Session:                   session,
			Phase:                     "launching",
			At:                        at,
		})
	})
	if err != nil {
		coordinator.cancelQueued(record, err)
		return
	}

	handle, err := coordinator.supervisor.Start(coordinator.ctx, projectprocess.StartRequest{
		ProjectID:            record.Operation.ProjectID,
		SessionID:            session.ID,
		CheckoutRoot:         project.Project.Path,
		EnvironmentOverrides: projectRuntimeEnvironmentOverrides(admission.Target),
		Stdout:               os.Stdout,
		Stderr:               os.Stderr,
	})
	if err != nil {
		coordinator.failStartWithoutProcess(begun, session, "project.launch.failed", err)
		return
	}
	coordinator.retainHandle(record.Operation.ProjectID, handle)
	evidence := processEvidence(handle.Info())
	attachedAt := lifecycleTime(coordinator.now())
	if attachedAt.Before(session.UpdatedAt) {
		attachedAt = session.UpdatedAt
	}
	attached, err := retryLifecycleResult(func() (domain.ProjectSession, error) {
		return coordinator.state.AttachProjectProcess(coordinator.ctx, state.AttachProjectProcessRequest{
			ProjectID:                 record.Operation.ProjectID,
			SessionID:                 session.ID,
			ExpectedSessionGeneration: session.Generation,
			Process:                   evidence,
			At:                        attachedAt,
		})
	})
	if err != nil {
		coordinator.stopAndFailUnattached(begun, session, handle, err)
		return
	}
	coordinator.waitForReadiness(begun, attached, handle, admission.Target)
}

// projectRuntimeEnvironmentOverrides keeps App and project-owned service publications on one assigned identity.
func projectRuntimeEnvironmentOverrides(target projectdiscovery.RuntimeTarget) projectprocess.EnvironmentOverrides {
	return projectprocess.EnvironmentOverrides{
		"API_HTTP_HOST":          target.Address.String(),
		"DEV_SERVICE_IP_ADDRESS": target.Address.String(),
		"IP_ADDRESS":             target.Address.String(),
		"LIGHTHOUSE_URL": fmt.Sprintf(
			"ws://%s:%d/lighthouse/ws/agent",
			target.Address,
			target.Port,
		),
	}
}

// waitForReadiness owns startup until the exact App proves ready or the supervised process exits.
func (coordinator *ProjectLifecycleCoordinator) waitForReadiness(
	mutation state.ProjectLifecycleMutation,
	session domain.ProjectSession,
	handle *projectprocess.Handle,
	target projectdiscovery.RuntimeTarget,
) {
	deadline := time.NewTimer(coordinator.startupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(coordinator.readinessInterval)
	defer ticker.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(coordinator.ctx, defaultReadinessHTTPTimeout)
		readinessState, err := coordinator.readiness.Probe(probeCtx, target)
		cancel()
		if err != nil {
			coordinator.stopAndFailAttached(mutation, session, handle, "project.readiness.invalid", err)
			return
		}
		if readinessState == projectreadiness.StateReady {
			readyAt := lifecycleTime(coordinator.now())
			if readyAt.Before(session.UpdatedAt) {
				readyAt = session.UpdatedAt
			}
			_, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
				return coordinator.state.CompleteProjectStart(coordinator.ctx, state.CompleteProjectStartRequest{
					ProjectID:                 mutation.Operation.Operation.ProjectID,
					OperationID:               mutation.Operation.Operation.ID,
					ExpectedOperationRevision: mutation.Operation.Revision,
					SessionID:                 session.ID,
					ExpectedSessionGeneration: session.Generation,
					Runtime:                   defaultRuntime(target),
					Phase:                     "ready",
					At:                        readyAt,
				})
			})
			if err != nil {
				coordinator.stopAndFailAttached(mutation, session, handle, "project.state.failed", err)
				return
			}
			if err := coordinator.reconcileProjectRoutes(coordinator.ctx, "publish ready project routes"); err != nil {
				coordinator.recordAsyncError(err)
			}
			coordinator.watchReadyProcess(session, handle)
			return
		}

		select {
		case <-handle.Done():
			coordinator.failExitedStart(mutation, session, handle, "project.process.exited", errors.New("forj dev exited before readiness"))
			return
		case <-deadline.C:
			coordinator.stopAndFailAttached(mutation, session, handle, "project.readiness.timeout", errors.New("forj dev did not become ready before the startup deadline"))
			return
		case <-ticker.C:
		case <-coordinator.ctx.Done():
			coordinator.stopAndFailAttached(mutation, session, handle, "project.daemon.stopping", coordinator.ctx.Err())
			return
		}
	}
}

// watchReadyProcess records a process loss only when no exact stop request owned the exit.
func (coordinator *ProjectLifecycleCoordinator) watchReadyProcess(session domain.ProjectSession, handle *projectprocess.Handle) {
	exit, err := handle.Wait(context.Background())
	if err != nil {
		coordinator.recordAsyncError(fmt.Errorf("observe project %q process exit: %w", session.ProjectID, err))
		coordinator.releaseHandle(session.ProjectID, handle)
		return
	}
	if exit.StopRequested {
		coordinator.mutex.Lock()
		closed := coordinator.closed
		coordinator.mutex.Unlock()
		if closed {
			coordinator.completeDaemonStop(session, handle, exit)
		}
		coordinator.releaseHandle(session.ProjectID, handle)
		return
	}
	_, persistErr := retryLifecycleResult(func() (state.ProjectRecord, error) {
		return coordinator.state.RecordUnexpectedProjectExit(context.Background(), state.RecordUnexpectedProjectExitRequest{
			ProjectID: session.ProjectID,
			Exit:      confirmedExit(session, handle, exit),
		})
	})
	coordinator.releaseHandle(session.ProjectID, handle)
	if persistErr != nil {
		coordinator.recordAsyncError(persistErr)
		return
	}
	if err := coordinator.reconcileProjectRoutes(context.Background(), "withdraw unexpectedly exited project routes"); err != nil {
		coordinator.recordAsyncError(err)
	}
}

// runStop fences one exact durable session before asking the supervisor to terminate its process tree.
func (coordinator *ProjectLifecycleCoordinator) runStop(record state.OperationRecord) {
	if record.Operation.State != domain.OperationQueued {
		return
	}
	session, err := coordinator.state.ActiveProjectSession(coordinator.ctx, record.Operation.ProjectID)
	if err != nil {
		coordinator.cancelQueued(record, err)
		return
	}
	project, err := coordinator.state.Project(coordinator.ctx, record.Operation.ProjectID)
	if err != nil {
		coordinator.cancelQueued(record, err)
		return
	}
	handle := coordinator.handle(record.Operation.ProjectID, session.ID)
	if handle == nil {
		coordinator.cancelQueued(record, fmt.Errorf("stop project %q session %q: supervised process handle is unavailable", record.Operation.ProjectID, session.ID))
		return
	}
	at := lifecycleTime(coordinator.now())
	if at.Before(project.Project.UpdatedAt) {
		at = project.Project.UpdatedAt
	}
	if at.Before(session.UpdatedAt) {
		at = session.UpdatedAt
	}
	begun, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.BeginProjectStop(coordinator.ctx, state.BeginProjectStopRequest{
			ProjectID:                 record.Operation.ProjectID,
			OperationID:               record.Operation.ID,
			ExpectedOperationRevision: record.Revision,
			SessionID:                 session.ID,
			ExpectedSessionGeneration: session.Generation,
			Phase:                     "stopping",
			At:                        at,
		})
	})
	if err != nil {
		coordinator.cancelQueued(record, err)
		return
	}
	if err := coordinator.reconcileProjectRoutes(coordinator.ctx, "withdraw stopping project routes"); err != nil {
		coordinator.recordAsyncError(err)
		return
	}
	if err := coordinator.supervisor.Stop(context.Background(), record.Operation.ProjectID, session.ID); err != nil && !errors.Is(err, projectprocess.ErrNotRunning) {
		coordinator.recordAsyncError(fmt.Errorf("stop project %q process: %w", record.Operation.ProjectID, err))
		return
	}
	exit, err := handle.Wait(context.Background())
	if err != nil {
		coordinator.recordAsyncError(fmt.Errorf("join project %q process: %w", record.Operation.ProjectID, err))
		return
	}
	coordinator.releaseHandle(record.Operation.ProjectID, handle)
	if _, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.CompleteProjectStop(context.Background(), state.CompleteProjectStopRequest{
			ProjectID:                 record.Operation.ProjectID,
			OperationID:               record.Operation.ID,
			ExpectedOperationRevision: begun.Operation.Revision,
			Exit:                      confirmedExit(*begun.Session, handle, exit),
			Phase:                     "stopped",
		})
	}); err != nil {
		coordinator.recordAsyncError(err)
		return
	}
	if err := coordinator.reconcileProjectRoutes(context.Background(), "confirm stopped project route withdrawal"); err != nil {
		coordinator.recordAsyncError(err)
	}
}

// completeDaemonStop records clean daemon shutdown as a stopped project instead of stale ready process authority.
func (coordinator *ProjectLifecycleCoordinator) completeDaemonStop(session domain.ProjectSession, handle *projectprocess.Handle, exit projectprocess.Exit) {
	project, err := coordinator.state.Project(context.Background(), session.ProjectID)
	if err != nil {
		coordinator.recordAsyncError(err)
		return
	}
	if project.Project.State == domain.ProjectStopped {
		return
	}
	if project.Project.State != domain.ProjectReady && project.Project.State != domain.ProjectFailed && project.Project.State != domain.ProjectDegraded && project.Project.State != domain.ProjectStopping {
		coordinator.recordAsyncError(fmt.Errorf("settle daemon stop for project %q from state %q", session.ProjectID, project.Project.State))
		return
	}
	at := lifecycleTime(exit.ExitedAt)
	if at.Before(project.Project.UpdatedAt) {
		at = project.Project.UpdatedAt
	}
	begun, err := coordinator.beginDaemonStop(project, session, at)
	if err != nil || begun.Session == nil {
		if err == nil {
			err = errors.New("daemon stop did not retain its exact session")
		}
		coordinator.recordAsyncError(err)
		return
	}
	confirmed := confirmedExit(*begun.Session, handle, exit)
	if confirmed.ExitedAt.Before(begun.Session.UpdatedAt) {
		confirmed.ExitedAt = begun.Session.UpdatedAt
	}
	if _, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.CompleteProjectStop(context.Background(), state.CompleteProjectStopRequest{
			ProjectID:                 session.ProjectID,
			OperationID:               begun.Operation.Operation.ID,
			ExpectedOperationRevision: begun.Operation.Revision,
			Exit:                      confirmed,
			Phase:                     "stopped",
		})
	}); err != nil {
		coordinator.recordAsyncError(err)
		return
	}
	if err := coordinator.reconcileProjectRoutes(context.Background(), "withdraw daemon-stopped project routes"); err != nil {
		coordinator.recordAsyncError(err)
	}
}

// beginDaemonStop reuses a client stop already in flight before creating a daemon-shutdown intent.
func (coordinator *ProjectLifecycleCoordinator) beginDaemonStop(
	project state.ProjectRecord,
	session domain.ProjectSession,
	at time.Time,
) (state.ProjectLifecycleMutation, error) {
	records, err := coordinator.operations.ActiveOperations(context.Background())
	if err != nil {
		return state.ProjectLifecycleMutation{}, err
	}
	for _, record := range records {
		if record.Operation.ProjectID != session.ProjectID || record.Operation.Kind != domain.OperationKindProjectStop {
			continue
		}
		if record.Operation.State == domain.OperationRunning {
			current, err := coordinator.state.ActiveProjectSession(context.Background(), session.ProjectID)
			if err != nil {
				return state.ProjectLifecycleMutation{}, err
			}
			return state.ProjectLifecycleMutation{Operation: record, Project: project, Session: &current}, nil
		}
		if record.Operation.State == domain.OperationQueued {
			return coordinator.beginQueuedDaemonStop(record, session, at)
		}
	}

	operationID, err := coordinator.newOperationID()
	if err != nil {
		return state.ProjectLifecycleMutation{}, err
	}
	intentID, err := coordinator.newIntentID()
	if err != nil {
		return state.ProjectLifecycleMutation{}, err
	}
	operation, err := domain.NewOperation(operationID, intentID, domain.OperationKindProjectStop, session.ProjectID, at)
	if err != nil {
		return state.ProjectLifecycleMutation{}, err
	}
	queued, err := coordinator.operations.Enqueue(context.Background(), operation)
	if err != nil {
		return state.ProjectLifecycleMutation{}, err
	}
	return coordinator.beginQueuedDaemonStop(queued, session, at)
}

// beginQueuedDaemonStop fences the same exact process generation already joined by daemon shutdown.
func (coordinator *ProjectLifecycleCoordinator) beginQueuedDaemonStop(
	queued state.OperationRecord,
	session domain.ProjectSession,
	at time.Time,
) (state.ProjectLifecycleMutation, error) {
	if at.Before(session.UpdatedAt) {
		at = session.UpdatedAt
	}
	return coordinator.state.BeginProjectStop(context.Background(), state.BeginProjectStopRequest{
		ProjectID:                 session.ProjectID,
		OperationID:               queued.Operation.ID,
		ExpectedOperationRevision: queued.Revision,
		SessionID:                 session.ID,
		ExpectedSessionGeneration: session.Generation,
		Phase:                     "daemon stopping",
		At:                        at,
	})
}

// Recover validates durable lifecycle state while retaining effect-free queued starts until network authority is ready.
func (coordinator *ProjectLifecycleCoordinator) Recover(ctx context.Context) error {
	ctx = normalizeLifecycleContext(ctx)
	records, err := coordinator.operations.ActiveOperations(ctx)
	if err != nil {
		return err
	}
	queuedStarts := make([]state.OperationRecord, 0, len(records))
	for _, record := range records {
		if record.Operation.Kind != domain.OperationKindProjectStart && record.Operation.Kind != domain.OperationKindProjectStop {
			continue
		}
		switch record.Operation.State {
		case domain.OperationQueued:
			if record.Operation.Kind == domain.OperationKindProjectStart {
				queuedStarts = append(queuedStarts, record)
				continue
			}
			session, sessionErr := coordinator.state.ActiveProjectSession(ctx, record.Operation.ProjectID)
			if sessionErr != nil {
				var missing *state.ProjectSessionNotFoundError
				if errors.As(sessionErr, &missing) {
					if err := coordinator.transitionQueuedCancellation(record); err != nil {
						return err
					}
					continue
				}
				return sessionErr
			}
			if err := coordinator.recoverQueuedProjectStop(ctx, record, session); err != nil {
				return err
			}
			continue
		case domain.OperationRunning:
			session, sessionErr := coordinator.state.ActiveProjectSession(ctx, record.Operation.ProjectID)
			if sessionErr != nil {
				return sessionErr
			}
			switch record.Operation.Kind {
			case domain.OperationKindProjectStart:
				if session.Process == nil {
					if err := coordinator.quarantinePlannedProjectStart(ctx, record, session); err != nil {
						return err
					}
					continue
				}
				recovered, recoveryErr := coordinator.recoverRunningProjectStart(ctx, record, session)
				if recoveryErr != nil {
					return recoveryErr
				}
				if recovered {
					continue
				}
			case domain.OperationKindProjectStop:
				if err := coordinator.recoverRunningProjectStop(ctx, record, session); err != nil {
					return err
				}
				continue
			}
			return priorProcessOwnershipError(record, session)
		default:
			return fmt.Errorf("recover project lifecycle operation %q from unsupported active state %q", record.Operation.ID, record.Operation.State)
		}
	}
	snapshot, err := coordinator.state.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, project := range snapshot.Projects {
		session, sessionErr := coordinator.state.ActiveProjectSession(ctx, project.ID)
		if sessionErr == nil {
			recovered, recoveryErr := coordinator.recoverTerminalProjectSession(ctx, project, session)
			if recoveryErr != nil {
				return recoveryErr
			}
			if recovered {
				continue
			}
			quarantined, quarantineErr := coordinator.isPlannedProjectStartQuarantined(ctx, project, session)
			if quarantineErr != nil {
				return quarantineErr
			}
			if quarantined {
				continue
			}
			return priorSessionOwnershipError(project, session)
		}
		var missing *state.ProjectSessionNotFoundError
		if !errors.As(sessionErr, &missing) {
			return sessionErr
		}
		if project.State == domain.ProjectStarting || project.State == domain.ProjectReady || project.State == domain.ProjectRebuilding || project.State == domain.ProjectDegraded || project.State == domain.ProjectStopping {
			return fmt.Errorf("recover project %q in runtime-bearing state %q without durable session authority", project.ID, project.State)
		}
	}
	coordinator.mutex.Lock()
	if coordinator.closed {
		coordinator.mutex.Unlock()
		return errors.New("recover project lifecycle coordinator: coordinator is closed")
	}
	coordinator.recoveredStarts = append(coordinator.recoveredStarts[:0], queuedStarts...)
	coordinator.mutex.Unlock()
	return nil
}

// Resume dispatches starts proven effect-free during recovery after Harbor's routes can serve their ready edge.
func (coordinator *ProjectLifecycleCoordinator) Resume(ctx context.Context) error {
	ctx = normalizeLifecycleContext(ctx)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("resume recovered project lifecycle operations: %w", err)
	}

	coordinator.mutex.Lock()
	if coordinator.closed {
		coordinator.mutex.Unlock()
		return errors.New("resume recovered project lifecycle operations: coordinator is closed")
	}
	records := append([]state.OperationRecord(nil), coordinator.recoveredStarts...)
	coordinator.recoveredStarts = nil
	coordinator.mutex.Unlock()

	for _, record := range records {
		coordinator.dispatch(record)
	}
	return nil
}

// recoverTerminalProjectSession retires exact prior-process authority only after the host proves that birth is gone.
func (coordinator *ProjectLifecycleCoordinator) recoverTerminalProjectSession(
	ctx context.Context,
	project domain.ProjectSnapshot,
	session domain.ProjectSession,
) (bool, error) {
	if (project.State != domain.ProjectReady && project.State != domain.ProjectDegraded) || session.Process == nil {
		return false, nil
	}
	_, err := coordinator.settleRecoveredProjectProcess(
		ctx,
		fmt.Sprintf("project %q terminal session %q", project.ID, session.ID),
		*session.Process,
	)
	if err != nil {
		return false, err
	}

	at := lifecycleTime(coordinator.now())
	if at.Before(project.UpdatedAt) {
		at = project.UpdatedAt
	}
	if at.Before(session.UpdatedAt) {
		at = session.UpdatedAt
	}
	evidence := *session.Process
	request := state.RecordUnexpectedProjectExitRequest{
		ProjectID: project.ID,
		Exit: state.ConfirmedProjectProcessExit{
			SessionID:                 session.ID,
			ExpectedSessionGeneration: session.Generation,
			Process:                   &evidence,
			ExitedAt:                  at,
		},
	}
	if _, err := retryLifecycleResult(func() (state.ProjectRecord, error) {
		return coordinator.state.RecordUnexpectedProjectExit(ctx, request)
	}); err != nil {
		return false, fmt.Errorf("recover project %q terminal session %q after prior process absence: %w", project.ID, session.ID, err)
	}
	// Runtime startup reconciles routes from the settled durable state after recovery returns.
	return true, nil
}

// recoverRunningProjectStart settles a process-backed start before retiring its interrupted durable operation.
func (coordinator *ProjectLifecycleCoordinator) recoverRunningProjectStart(
	ctx context.Context,
	record state.OperationRecord,
	session domain.ProjectSession,
) (bool, error) {
	settlement, err := coordinator.settleRecoveredProjectProcess(
		ctx,
		fmt.Sprintf("running project start operation %q", record.Operation.ID),
		*session.Process,
	)
	if err != nil {
		return false, err
	}

	project, err := coordinator.state.Project(ctx, record.Operation.ProjectID)
	if err != nil {
		return false, err
	}
	at := lifecycleTime(coordinator.now())
	for _, lowerBound := range []time.Time{record.Operation.RequestedAt, project.Project.UpdatedAt, session.UpdatedAt} {
		if at.Before(lowerBound) {
			at = lowerBound
		}
	}
	if record.Operation.StartedAt != nil && at.Before(*record.Operation.StartedAt) {
		at = *record.Operation.StartedAt
	}
	evidence := *session.Process
	cause := errors.New("previous Harbor-managed process was absent during daemon recovery")
	switch settlement.Outcome {
	case projectprocess.PriorProcessSettlementReplaced:
		cause = errors.New("previous Harbor-managed process birth was replaced before daemon recovery")
	case projectprocess.PriorProcessSettlementTerminated:
		cause = errors.New("previous Harbor-managed process was terminated during daemon recovery")
	}
	request := state.FailProjectStartRequest{
		ProjectID:                 record.Operation.ProjectID,
		OperationID:               record.Operation.ID,
		ExpectedOperationRevision: record.Revision,
		Exit: state.ConfirmedProjectProcessExit{
			SessionID:                 session.ID,
			ExpectedSessionGeneration: session.Generation,
			Process:                   &evidence,
			ExitedAt:                  at,
		},
		Phase:   "recovered absent process",
		Problem: lifecycleProblem("project.recovery.process_absent", cause),
	}
	if _, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.FailProjectStart(ctx, request)
	}); err != nil {
		return false, fmt.Errorf("recover project lifecycle operation %q after prior process absence: %w", record.Operation.ID, err)
	}
	// Daemon recovery runs before the route controller starts; its first reconciliation reads this settled state.
	return true, nil
}

// priorSessionOwnershipError rejects terminal-operation projections whose prior daemon process is not owned in memory.
func priorSessionOwnershipError(project domain.ProjectSnapshot, session domain.ProjectSession) error {
	return fmt.Errorf(
		"recover project %q in state %q with session %q state %q: prior process ownership requires exact host reconciliation",
		project.ID,
		project.State,
		session.ID,
		session.State,
	)
}

// priorProcessOwnershipError makes unsafe restart state actionable without guessing from a reusable PID.
func priorProcessOwnershipError(record state.OperationRecord, session domain.ProjectSession) error {
	return fmt.Errorf(
		"recover project lifecycle operation %q for project %q session %q: prior process ownership in state %q requires exact host reconciliation",
		record.Operation.ID,
		record.Operation.ProjectID,
		session.ID,
		session.State,
	)
}

// Close stops every owned process tree and waits for lifecycle workers to relinquish process authority.
func (coordinator *ProjectLifecycleCoordinator) Close(ctx context.Context) error {
	ctx = normalizeLifecycleContext(ctx)
	coordinator.mutex.Lock()
	if !coordinator.closed {
		coordinator.closed = true
		coordinator.cancel()
		go coordinator.finishClose(ctx)
	}
	done := coordinator.closeDone
	coordinator.mutex.Unlock()
	select {
	case <-done:
		coordinator.mutex.Lock()
		err := coordinator.closeErr
		coordinator.mutex.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done closes only after every supervised process and lifecycle worker has relinquished authority.
func (coordinator *ProjectLifecycleCoordinator) Done() <-chan struct{} {
	return coordinator.closeDone
}

// Err returns the retained lifecycle cleanup failure after or during shutdown.
func (coordinator *ProjectLifecycleCoordinator) Err() error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if coordinator.closeErr != nil {
		return coordinator.closeErr
	}
	return coordinator.asyncErr
}

// finishClose continues joined cleanup after a caller deadline so a later close can still observe the terminal result.
func (coordinator *ProjectLifecycleCoordinator) finishClose(ctx context.Context) {
	err := coordinator.supervisor.Close(ctx)
	coordinator.wait.Wait()
	coordinator.mutex.Lock()
	coordinator.closeErr = errors.Join(err, coordinator.asyncErr)
	close(coordinator.closeDone)
	coordinator.mutex.Unlock()
}

// retainHandle publishes one accepted process only after its operating-system evidence is available.
func (coordinator *ProjectLifecycleCoordinator) retainHandle(projectID domain.ProjectID, handle *projectprocess.Handle) {
	coordinator.mutex.Lock()
	coordinator.handles[projectID] = handle
	coordinator.mutex.Unlock()
}

// releaseHandle removes only the process generation observed by the caller.
func (coordinator *ProjectLifecycleCoordinator) releaseHandle(projectID domain.ProjectID, handle *projectprocess.Handle) {
	coordinator.mutex.Lock()
	if coordinator.handles[projectID] == handle {
		delete(coordinator.handles, projectID)
	}
	coordinator.mutex.Unlock()
}

// handle returns the in-memory authority only when both durable identities match.
func (coordinator *ProjectLifecycleCoordinator) handle(projectID domain.ProjectID, sessionID domain.SessionID) *projectprocess.Handle {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	handle := coordinator.handles[projectID]
	if handle == nil || handle.Info().SessionID != sessionID {
		return nil
	}
	return handle
}

// failStartWithoutProcess retires a planned session after a pre-launch failure proves no process was accepted.
func (coordinator *ProjectLifecycleCoordinator) failStartWithoutProcess(mutation state.ProjectLifecycleMutation, session domain.ProjectSession, code string, cause error) {
	at := lifecycleTime(coordinator.now())
	if at.Before(session.UpdatedAt) {
		at = session.UpdatedAt
	}
	if _, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.FailProjectStart(context.Background(), state.FailProjectStartRequest{
			ProjectID:                 session.ProjectID,
			OperationID:               mutation.Operation.Operation.ID,
			ExpectedOperationRevision: mutation.Operation.Revision,
			Exit: state.ConfirmedProjectProcessExit{
				SessionID:                 session.ID,
				ExpectedSessionGeneration: session.Generation,
				ExitedAt:                  at,
			},
			Phase:   "failed",
			Problem: lifecycleProblem(code, cause),
		})
	}); err != nil {
		coordinator.recordAsyncError(err)
		coordinator.recordAsyncError(cause)
	}
}

// stopAndFailUnattached joins an accepted process that never reached durable evidence attachment.
func (coordinator *ProjectLifecycleCoordinator) stopAndFailUnattached(mutation state.ProjectLifecycleMutation, session domain.ProjectSession, handle *projectprocess.Handle, cause error) {
	if err := coordinator.supervisor.Stop(context.Background(), session.ProjectID, session.ID); err != nil && !errors.Is(err, projectprocess.ErrNotRunning) {
		coordinator.recordAsyncError(err)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		coordinator.recordAsyncError(err)
	}
	coordinator.releaseHandle(session.ProjectID, handle)
	coordinator.failStartWithoutProcess(mutation, session, "project.state.failed", cause)
}

// stopAndFailAttached joins an exact process before retiring its durable process-backed session.
func (coordinator *ProjectLifecycleCoordinator) stopAndFailAttached(mutation state.ProjectLifecycleMutation, session domain.ProjectSession, handle *projectprocess.Handle, code string, cause error) {
	if err := coordinator.supervisor.Stop(context.Background(), session.ProjectID, session.ID); err != nil && !errors.Is(err, projectprocess.ErrNotRunning) {
		coordinator.recordAsyncError(err)
	}
	coordinator.failExitedStart(mutation, session, handle, code, cause)
}

// failExitedStart records failure only after the supervised process tree has joined.
func (coordinator *ProjectLifecycleCoordinator) failExitedStart(mutation state.ProjectLifecycleMutation, session domain.ProjectSession, handle *projectprocess.Handle, code string, cause error) {
	exit, err := handle.Wait(context.Background())
	coordinator.releaseHandle(session.ProjectID, handle)
	if err != nil {
		coordinator.recordAsyncError(err)
		return
	}
	if _, err := retryLifecycleResult(func() (state.ProjectLifecycleMutation, error) {
		return coordinator.state.FailProjectStart(context.Background(), state.FailProjectStartRequest{
			ProjectID:                 session.ProjectID,
			OperationID:               mutation.Operation.Operation.ID,
			ExpectedOperationRevision: mutation.Operation.Revision,
			Exit:                      confirmedExit(session, handle, exit),
			Phase:                     "failed",
			Problem:                   lifecycleProblem(code, cause),
		})
	}); err != nil {
		coordinator.recordAsyncError(err)
		coordinator.recordAsyncError(cause)
	}
}

// confirmedExit binds a joined process result to the exact evidence captured at launch.
func confirmedExit(session domain.ProjectSession, handle *projectprocess.Handle, exit projectprocess.Exit) state.ConfirmedProjectProcessExit {
	evidence := processEvidence(handle.Info())
	return state.ConfirmedProjectProcessExit{
		SessionID:                 session.ID,
		ExpectedSessionGeneration: session.Generation,
		Process:                   &evidence,
		ExitedAt:                  lifecycleTime(exit.ExitedAt),
	}
}

// processEvidence converts immutable supervisor evidence without weakening its exact-birth correlation.
func processEvidence(info projectprocess.Info) domain.ProcessEvidence {
	return domain.ProcessEvidence{
		PID:                info.Evidence.PID,
		BirthToken:         info.Evidence.BirthToken,
		ExecutableIdentity: info.Evidence.ExecutableIdentity,
		ArgumentDigest:     info.Evidence.ArgumentsSHA256,
	}
}

// defaultRuntime projects the one App and resource proved by the generated readiness endpoint.
func defaultRuntime(target projectdiscovery.RuntimeTarget) state.DefaultProjectRuntime {
	return state.DefaultProjectRuntime{
		App: domain.AppSnapshot{
			ID:       target.AppID,
			Name:     target.Name,
			State:    domain.EntityReady,
			Active:   true,
			Required: true,
		},
		Resource: domain.ResourceSnapshot{
			ID:   "app-http",
			Name: target.Name,
			Kind: "application",
			Owner: domain.ResourceOwner{
				Kind:  domain.ResourceOwnedByApp,
				AppID: target.AppID,
			},
			URL: target.ResourceURL,
		},
	}
}

// validateProjectStartRequest rejects incomplete daemon and client identity before journaling.
func validateProjectStartRequest(request ProjectStartRequest) error {
	return validateProjectLifecycleIdentity(request.ProjectID, request.OperationID, request.IntentID)
}

// validateProjectStopRequest rejects incomplete daemon and client identity before journaling.
func validateProjectStopRequest(request ProjectStopRequest) error {
	return validateProjectLifecycleIdentity(request.ProjectID, request.OperationID, request.IntentID)
}

// validateProjectLifecycleIdentity keeps operation ownership explicit across asynchronous dispatch.
func validateProjectLifecycleIdentity(projectID domain.ProjectID, operationID domain.OperationID, intentID domain.IntentID) error {
	if err := projectID.Validate(); err != nil {
		return err
	}
	if err := operationID.Validate(); err != nil {
		return err
	}
	return intentID.Validate()
}

// validateNewLifecycleState prevents a new queued operation from becoming durable when its first transition cannot run.
func validateNewLifecycleState(projectState projectLifecycleState, ctx context.Context, project domain.ProjectSnapshot, kind domain.OperationKind) error {
	switch kind {
	case domain.OperationKindProjectStart:
		if project.State != domain.ProjectStopped && project.State != domain.ProjectFailed && project.State != domain.ProjectUnavailable {
			return fmt.Errorf("project %q cannot start from state %q", project.ID, project.State)
		}
	case domain.OperationKindProjectStop:
		if project.State != domain.ProjectReady && project.State != domain.ProjectFailed && project.State != domain.ProjectDegraded {
			return fmt.Errorf("project %q cannot stop from state %q", project.ID, project.State)
		}
		if _, err := projectState.ActiveProjectSession(ctx, project.ID); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported project lifecycle kind %q", kind)
	}
	return nil
}

// newHarborProjectSession binds launch shape to a digest while keeping fresh credential material out of durable state.
func newHarborProjectSession(projectID domain.ProjectID, checkoutRoot string, at time.Time) (domain.ProjectSession, error) {
	sessionID, err := newLifecycleSessionID()
	if err != nil {
		return domain.ProjectSession{}, err
	}
	if strings.TrimSpace(checkoutRoot) == "" {
		return domain.ProjectSession{}, errors.New("project lifecycle descriptor requires a checkout root")
	}
	descriptorHash := sha256.Sum256([]byte(checkoutRoot + "\x00forj\x00dev"))
	descriptor := hex.EncodeToString(descriptorHash[:])
	credential, err := randomLifecycleDigest()
	if err != nil {
		return domain.ProjectSession{}, err
	}
	session := domain.ProjectSession{
		ID:               sessionID,
		ProjectID:        projectID,
		Owner:            domain.SessionOwnerHarbor,
		State:            domain.SessionPlanned,
		DescriptorDigest: descriptor,
		CredentialDigest: credential,
		Generation:       1,
		CreatedAt:        lifecycleTime(at),
		UpdatedAt:        lifecycleTime(at),
	}
	if err := session.Validate(); err != nil {
		return domain.ProjectSession{}, err
	}
	return session, nil
}

// newLifecycleOperationID creates a daemon-owned operation identity independent of client idempotency.
func newLifecycleOperationID() (domain.OperationID, error) {
	value, err := randomLifecycleIdentity("operation-")
	return domain.OperationID(value), err
}

// newLifecycleIntentID creates an internal shutdown intent that cannot collide with a client-provided operation ID.
func newLifecycleIntentID() (domain.IntentID, error) {
	value, err := randomLifecycleIdentity("intent-")
	return domain.IntentID(value), err
}

// newLifecycleSessionID creates one process-independent durable session identity.
func newLifecycleSessionID() (domain.SessionID, error) {
	value, err := randomLifecycleIdentity("session-")
	return domain.SessionID(value), err
}

// randomLifecycleIdentity returns a 128-bit opaque identity with a domain-specific prefix.
func randomLifecycleIdentity(prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}

// randomLifecycleDigest hashes fresh credential material so only a one-way verifier reaches durable state.
func randomLifecycleDigest() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	digest := sha256.Sum256(random)
	return hex.EncodeToString(digest[:]), nil
}

// lifecycleProblem bounds asynchronous failure text before it becomes client-visible durable state.
func lifecycleProblem(code string, cause error) domain.Problem {
	message := "project lifecycle failed"
	if cause != nil {
		message = strings.Join(strings.Fields(strings.ToValidUTF8(cause.Error(), "�")), " ")
		if message == "" {
			message = "project lifecycle failed"
		}
	}
	if len(message) > 4096 {
		message = message[:4096]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return domain.Problem{Code: domain.ProblemCode(code), Message: message, Retryable: true}
}

// lifecycleTime removes local-zone and monotonic metadata before values cross the persistence boundary.
func lifecycleTime(value time.Time) time.Time {
	return value.UTC().Round(0)
}

// normalizeLifecycleContext keeps direct coordinator calls usable while preserving caller cancellation.
func normalizeLifecycleContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
