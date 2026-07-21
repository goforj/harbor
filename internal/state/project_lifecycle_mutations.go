package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/null/v6"
	"gorm.io/gorm"
)

// ProjectLifecycleMutation is one atomically committed operation, project projection, and optional active session.
type ProjectLifecycleMutation struct {
	Operation OperationRecord
	Project   ProjectRecord
	Session   *domain.ProjectSession
}

// DefaultProjectRuntime is the ready default App, observed services, and their launchable HTTP resources.
type DefaultProjectRuntime struct {
	App       domain.AppSnapshot
	Services  []domain.ServiceSnapshot
	Resources []domain.ResourceSnapshot
}

// Validate reports whether the projection proves one ready, active, required default App and canonical owned topology.
func (runtime DefaultProjectRuntime) Validate() error {
	if err := runtime.App.Validate(); err != nil {
		return err
	}
	if runtime.App.State != domain.EntityReady || !runtime.App.Active || !runtime.App.Required {
		return fmt.Errorf("default App must be ready, active, and required")
	}
	if err := validateDefaultProjectRuntimeServices(runtime.Services); err != nil {
		return err
	}
	return validateDefaultProjectRuntimeResources(runtime.App, runtime.Services, runtime.Resources)
}

// validateDefaultProjectRuntimeServices requires deterministic service identities before runtime observations become durable.
func validateDefaultProjectRuntimeServices(services []domain.ServiceSnapshot) error {
	if services == nil {
		return errors.New("default runtime services must not be nil")
	}
	seen := make(map[domain.ServiceID]struct{}, len(services))
	var previous domain.ServiceID
	for index, service := range services {
		if err := service.Validate(); err != nil {
			return fmt.Errorf("default runtime service %q: %w", service.ID, err)
		}
		if _, exists := seen[service.ID]; exists {
			return fmt.Errorf("duplicate default runtime service ID %q", service.ID)
		}
		if index > 0 && previous > service.ID {
			return errors.New("default runtime services must use canonical service ID order")
		}
		seen[service.ID] = struct{}{}
		previous = service.ID
	}
	return nil
}

// validateDefaultProjectRuntimeResources admits only canonical resources owned by the projected App or an observed service.
func validateDefaultProjectRuntimeResources(
	app domain.AppSnapshot,
	services []domain.ServiceSnapshot,
	resources []domain.ResourceSnapshot,
) error {
	if resources == nil {
		return errors.New("default runtime resources must not be nil")
	}
	if len(resources) == 0 {
		return errors.New("default runtime must contain its launchable App resource")
	}
	serviceIDs := make(map[domain.ServiceID]struct{}, len(services))
	for _, service := range services {
		serviceIDs[service.ID] = struct{}{}
	}
	seen := make(map[domain.ResourceID]struct{}, len(resources))
	var previous domain.ResourceID
	appResource := false
	for index, resource := range resources {
		if err := resource.Validate(); err != nil {
			return fmt.Errorf("default runtime resource %q: %w", resource.ID, err)
		}
		if _, exists := seen[resource.ID]; exists {
			return fmt.Errorf("duplicate default runtime resource ID %q", resource.ID)
		}
		if index > 0 && previous > resource.ID {
			return errors.New("default runtime resources must use canonical resource ID order")
		}
		switch resource.Owner.Kind {
		case domain.ResourceOwnedByApp:
			if resource.Owner.AppID != app.ID {
				return fmt.Errorf("default runtime resource %q belongs to unknown App %q", resource.ID, resource.Owner.AppID)
			}
			appResource = true
		case domain.ResourceOwnedByService:
			if _, exists := serviceIDs[resource.Owner.ServiceID]; !exists {
				return fmt.Errorf("default runtime resource %q belongs to unknown service %q", resource.ID, resource.Owner.ServiceID)
			}
		}
		seen[resource.ID] = struct{}{}
		previous = resource.ID
	}
	if !appResource {
		return errors.New("default runtime must contain a launchable default App resource")
	}
	return nil
}

// BeginProjectStartRequest binds one queued start operation to the planned session it creates.
type BeginProjectStartRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	// OperationKind identifies whether this is a new start or the start half of a durable restart.
	OperationKind             domain.OperationKind
	ExpectedOperationRevision domain.Sequence
	ExpectedProjectRevision   domain.Sequence
	Session                   domain.ProjectSession
	Phase                     string
	At                        time.Time
}

// AttachProjectProcessRequest binds accepted operating-system evidence to one planned session generation.
type AttachProjectProcessRequest struct {
	ProjectID                 domain.ProjectID
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Process                   domain.ProcessEvidence
	OutputBroker              *domain.OutputBrokerSession
	At                        time.Time
}

// CompleteManagedSessionAttachmentRequest binds authenticated managed-session proof to one awaiting process generation.
type CompleteManagedSessionAttachmentRequest struct {
	ProjectID                 domain.ProjectID
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Process                   domain.ProcessEvidence
	At                        time.Time
}

// CompleteProjectStartRequest publishes proven readiness for one process-backed start operation.
type CompleteProjectStartRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	// OperationKind identifies whether readiness completes a start or a restart.
	OperationKind             domain.OperationKind
	ExpectedOperationRevision domain.Sequence
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Runtime                   DefaultProjectRuntime
	Phase                     string
	At                        time.Time
}

// ConfirmedProjectProcessExit records the exact process identity after the caller has joined its process tree.
type ConfirmedProjectProcessExit struct {
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Process                   *domain.ProcessEvidence
	ExitedAt                  time.Time
}

// FailProjectStartRequest terminates one running start after launch rejection or confirmed process exit.
type FailProjectStartRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	// OperationKind identifies whether failure belongs to a start or a restart.
	OperationKind             domain.OperationKind
	ExpectedOperationRevision domain.Sequence
	Exit                      ConfirmedProjectProcessExit
	Phase                     string
	Problem                   domain.Problem
}

// FailProjectRestartRequest records a restart failure after its old session was retired but before a replacement session existed.
type FailProjectRestartRequest struct {
	ProjectID                 domain.ProjectID
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	ExpectedProjectRevision   domain.Sequence
	Phase                     string
	Problem                   domain.Problem
	At                        time.Time
}

// BeginProjectStopRequest binds one queued stop operation to an exact process-backed session generation.
type BeginProjectStopRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	// OperationKind identifies whether this is a stop or the stop half of a durable restart.
	OperationKind             domain.OperationKind
	ExpectedOperationRevision domain.Sequence
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Phase                     string
	At                        time.Time
}

// CompleteProjectStopRequest retires one stopping session only after its exact process tree has joined.
type CompleteProjectStopRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	// OperationKind identifies whether completion terminates a stop or advances a restart to its start half.
	OperationKind             domain.OperationKind
	ExpectedOperationRevision domain.Sequence
	Exit                      ConfirmedProjectProcessExit
	Phase                     string
}

// RecordUnexpectedProjectExitRequest retires one process-backed terminal session after an independently observed exit.
type RecordUnexpectedProjectExitRequest struct {
	ProjectID domain.ProjectID
	Exit      ConfirmedProjectProcessExit
}

// BeginProjectStart atomically starts an existing queued operation, creates its planned session, and marks the project starting.
func (store *Store) BeginProjectStart(ctx context.Context, request BeginProjectStartRequest) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	request.OperationKind = normalizedProjectStartOperationKind(request.OperationKind)
	if err := validateBeginProjectStartRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "begin project start", func(tx *gorm.DB) error {
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, request.OperationKind)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationRunning {
			if request.OperationKind == domain.OperationKindProjectRestart {
				replayed, replayErr := beginRunningProjectRestartStart(tx, current, request)
				if replayErr != nil {
					return replayErr
				}
				result = replayed
				return nil
			}
			replayed, replayErr := replayBeginProjectStart(tx, current, history, request)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			return nil
		}
		if current.Operation.State != domain.OperationQueued {
			return fmt.Errorf("project start operation %q must be queued, got %q", request.OperationID, current.Operation.State)
		}
		if current.Revision != request.ExpectedOperationRevision {
			return staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, current.Revision)
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Revision != request.ExpectedProjectRevision {
			return &ProjectRevisionConflictError{
				ProjectID: request.ProjectID,
				Expected:  request.ExpectedProjectRevision,
				Actual:    project.Revision,
			}
		}
		if !projectCanStart(project.Project.State) {
			return fmt.Errorf("project %q cannot start from state %q", request.ProjectID, project.Project.State)
		}
		if err := validateLifecycleProjectionTime(project.Project, request.At); err != nil {
			return err
		}
		if err := requireNoActiveProjectSession(tx, request.ProjectID); err != nil {
			return err
		}
		if err := requireNoCompetingProjectOperation(tx, request.ProjectID, request.OperationID); err != nil {
			return err
		}
		row, err := projectSessionModelFromDomain(request.Session)
		if err != nil {
			return err
		}
		if err := requireOneCreate(tx.Create(&row), "create planned project session", string(request.Session.ID)); err != nil {
			return err
		}
		running, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationRunning, request.Phase, request.At, nil)
		if err != nil {
			return err
		}
		nextProject := project.Project
		nextProject.State = domain.ProjectStarting
		nextProject.UpdatedAt = request.At
		persistedProject, err := persistLifecycleProject(tx, nextProject)
		if err != nil {
			return err
		}
		persistedSession, err := readExactProjectSession(tx, request.ProjectID, request.Session.ID)
		if err != nil {
			return err
		}
		result = lifecycleMutation(running, persistedProject, &persistedSession)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("begin project %q start: %w", request.ProjectID, err)
	}
	return result, nil
}

// AttachProjectProcess persists exact process evidence before Harbor begins readiness observation.
func (store *Store) AttachProjectProcess(ctx context.Context, request AttachProjectProcessRequest) (domain.ProjectSession, error) {
	ctx = normalizeContext(ctx)
	if err := validateAttachProjectProcessRequest(request); err != nil {
		return domain.ProjectSession{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ProjectSession{}, err
	}

	var result domain.ProjectSession
	err := store.mutations.mutate(ctx, "attach project process", func(tx *gorm.DB) error {
		current, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if current.State == domain.SessionAwaitingAttach && current.Generation == request.ExpectedSessionGeneration+1 {
			if current.Process != nil && *current.Process == request.Process && outputBrokerSessionsEqual(current.OutputBroker, request.OutputBroker) && current.UpdatedAt.Equal(request.At) {
				result = current
				return nil
			}
		}
		if current.State != domain.SessionPlanned {
			return fmt.Errorf("session %q must be planned before process attachment, got %q", request.SessionID, current.State)
		}
		if current.Generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, current.Generation)
		}
		if request.At.Before(current.UpdatedAt) {
			return fmt.Errorf("process attachment time precedes session generation")
		}
		next := current
		next.State = domain.SessionAwaitingAttach
		next.Generation++
		next.Process = cloneProcessEvidence(&request.Process)
		next.OutputBroker = cloneOutputBrokerSession(request.OutputBroker)
		next.UpdatedAt = request.At
		updated, err := updateExactProjectSession(tx, current, next)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return domain.ProjectSession{}, fmt.Errorf("attach project %q process: %w", request.ProjectID, err)
	}
	return result, nil
}

// CompleteManagedSessionAttachment advances one process-backed session after its external managed-session proof succeeds.
func (store *Store) CompleteManagedSessionAttachment(ctx context.Context, request CompleteManagedSessionAttachmentRequest) (domain.ProjectSession, error) {
	ctx = normalizeContext(ctx)
	if err := validateCompleteManagedSessionAttachmentRequest(request); err != nil {
		return domain.ProjectSession{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.ProjectSession{}, err
	}

	var result domain.ProjectSession
	err := store.mutations.mutate(ctx, "complete managed session attachment", func(tx *gorm.DB) error {
		current, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if current.State == domain.SessionAttached && current.Generation == request.ExpectedSessionGeneration+1 &&
			current.Process != nil && *current.Process == request.Process && current.UpdatedAt.Equal(request.At) {
			result = current
			return nil
		}
		if current.Generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, current.Generation)
		}
		if current.State != domain.SessionAwaitingAttach {
			return fmt.Errorf("session %q must await managed attachment, got %q", request.SessionID, current.State)
		}
		if current.Process == nil || *current.Process != request.Process {
			return fmt.Errorf("managed attachment process evidence does not match session %q", request.SessionID)
		}
		if request.At.Before(current.UpdatedAt) {
			return fmt.Errorf("managed attachment time precedes session generation")
		}
		next := current
		next.State = domain.SessionAttached
		next.Generation++
		next.UpdatedAt = request.At
		updated, err := updateExactProjectSession(tx, current, next)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return domain.ProjectSession{}, fmt.Errorf("complete managed attachment for project %q: %w", request.ProjectID, err)
	}
	return result, nil
}

// CompleteProjectStart atomically succeeds the running operation and publishes the ready default runtime.
func (store *Store) CompleteProjectStart(ctx context.Context, request CompleteProjectStartRequest) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	request.OperationKind = normalizedProjectStartOperationKind(request.OperationKind)
	if err := validateCompleteProjectStartRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "complete project start", func(tx *gorm.DB) error {
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, request.OperationKind)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationSucceeded {
			replayed, replayErr := replayCompleteProjectStart(tx, current, history, request)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			return nil
		}
		if current.Operation.State != domain.OperationRunning {
			return fmt.Errorf("project start operation %q must be running, got %q", request.OperationID, current.Operation.State)
		}
		if current.Revision != request.ExpectedOperationRevision {
			return staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, current.Revision)
		}
		session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if !sessionCanPublishReadiness(session) {
			return fmt.Errorf("session %q must contain a process-backed launch before readiness", request.SessionID)
		}
		if session.Generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, session.Generation)
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Project.State != domain.ProjectStarting {
			return fmt.Errorf("project %q must be starting before readiness, got %q", request.ProjectID, project.Project.State)
		}
		if err := validateLifecycleProjectionTime(project.Project, request.At); err != nil {
			return err
		}
		succeeded, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationSucceeded, request.Phase, request.At, nil)
		if err != nil {
			return err
		}
		nextProject := readyProjectProjection(project.Project, request.Runtime, request.At)
		persistedProject, err := persistLifecycleProject(tx, nextProject)
		if err != nil {
			return err
		}
		runtimeState, err := store.runtimeStateCandidate(tx)
		if err != nil {
			return fmt.Errorf("read ready runtime candidate: %w", err)
		}
		if err := runtimeState.Validate(); err != nil {
			return fmt.Errorf("validate ready runtime candidate: %w", err)
		}
		result = lifecycleMutation(succeeded, persistedProject, &session)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("complete project %q start: %w", request.ProjectID, err)
	}
	return result, nil
}

// FailProjectStart atomically fails the running start and removes its session only after process absence is confirmed.
func (store *Store) FailProjectStart(ctx context.Context, request FailProjectStartRequest) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	request.OperationKind = normalizedProjectStartOperationKind(request.OperationKind)
	if err := validateFailProjectStartRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "fail project start", func(tx *gorm.DB) error {
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, request.OperationKind)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationFailed {
			replayed, replayErr := replayFailProjectStart(tx, current, history, request)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			return nil
		}
		if current.Operation.State != domain.OperationRunning {
			return fmt.Errorf("project start operation %q must be running, got %q", request.OperationID, current.Operation.State)
		}
		if current.Revision != request.ExpectedOperationRevision {
			return staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, current.Revision)
		}
		session, err := readExactProjectSession(tx, request.ProjectID, request.Exit.SessionID)
		if err != nil {
			return err
		}
		if err := validateConfirmedSessionExit(session, request.Exit); err != nil {
			return err
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Project.State != domain.ProjectStarting {
			return fmt.Errorf("project %q must be starting before start failure, got %q", request.ProjectID, project.Project.State)
		}
		if err := validateLifecycleProjectionTime(project.Project, request.Exit.ExitedAt); err != nil {
			return err
		}
		failed, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationFailed, request.Phase, request.Exit.ExitedAt, &request.Problem)
		if err != nil {
			return err
		}
		if err := deleteExactProjectSession(tx, session); err != nil {
			return err
		}
		nextProject := failedProjectProjection(project.Project, request.Exit.ExitedAt)
		persistedProject, err := persistLifecycleProject(tx, nextProject)
		if err != nil {
			return err
		}
		result = lifecycleMutation(failed, persistedProject, nil)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("fail project %q start: %w", request.ProjectID, err)
	}
	return result, nil
}

// FailProjectRestart records a bounded failure while preserving the stopped, route-free project projection.
func (store *Store) FailProjectRestart(ctx context.Context, request FailProjectRestartRequest) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	if err := validateFailProjectRestartRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "fail project restart", func(tx *gorm.DB) error {
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, domain.OperationKindProjectRestart)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationFailed {
			if err := requireExactLifecycleReplay(history, request.ExpectedOperationRevision, domain.OperationFailed, request.Phase, request.At, &request.Problem); err != nil {
				return err
			}
			if err := requireNoActiveProjectSession(tx, request.ProjectID); err != nil {
				return err
			}
			project, projectErr := readLifecycleProject(tx, request.ProjectID)
			if projectErr != nil {
				return projectErr
			}
			if !projectMatchesInactiveState(project.Project, domain.ProjectFailed, request.At) {
				return fmt.Errorf("restart failure replay does not match the committed failed projection")
			}
			result = lifecycleMutation(current, project, nil)
			return nil
		}
		if current.Operation.State != domain.OperationRunning {
			return fmt.Errorf("project restart operation %q must be running, got %q", request.OperationID, current.Operation.State)
		}
		if current.Revision != request.ExpectedOperationRevision {
			return staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, current.Revision)
		}
		if err := requireNoActiveProjectSession(tx, request.ProjectID); err != nil {
			return err
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Revision != request.ExpectedProjectRevision {
			return &ProjectRevisionConflictError{ProjectID: request.ProjectID, Expected: request.ExpectedProjectRevision, Actual: project.Revision}
		}
		if project.Project.State != domain.ProjectStopped {
			return fmt.Errorf("project %q must be stopped before restart failure, got %q", request.ProjectID, project.Project.State)
		}
		if err := validateLifecycleProjectionTime(project.Project, request.At); err != nil {
			return err
		}
		failed, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationFailed, request.Phase, request.At, &request.Problem)
		if err != nil {
			return err
		}
		persistedProject, err := persistLifecycleProject(tx, failedProjectProjection(project.Project, request.At))
		if err != nil {
			return err
		}
		result = lifecycleMutation(failed, persistedProject, nil)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("fail project %q restart: %w", request.ProjectID, err)
	}
	return result, nil
}

// BeginProjectStop atomically starts an existing queued stop, fences its process session, and marks the project stopping.
func (store *Store) BeginProjectStop(ctx context.Context, request BeginProjectStopRequest) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	request.OperationKind = normalizedProjectStopOperationKind(request.OperationKind)
	if err := validateBeginProjectStopRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "begin project stop", func(tx *gorm.DB) error {
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, request.OperationKind)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationRunning {
			replayed, replayErr := replayBeginProjectStop(tx, current, history, request)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			return nil
		}
		if current.Operation.State != domain.OperationQueued {
			return fmt.Errorf("project stop operation %q must be queued, got %q", request.OperationID, current.Operation.State)
		}
		if current.Revision != request.ExpectedOperationRevision {
			return staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, current.Revision)
		}
		if err := requireNoCompetingProjectOperation(tx, request.ProjectID, request.OperationID); err != nil {
			return err
		}
		session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if !sessionCanStop(session) {
			return fmt.Errorf("session %q must be process-backed before stop, got %q", request.SessionID, session.State)
		}
		if session.Generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, session.Generation)
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Project.State != domain.ProjectReady && project.Project.State != domain.ProjectFailed && project.Project.State != domain.ProjectDegraded {
			return fmt.Errorf("project %q cannot stop from state %q", request.ProjectID, project.Project.State)
		}
		if request.At.Before(session.UpdatedAt) {
			return fmt.Errorf("stop transition time precedes session generation")
		}
		if err := validateLifecycleProjectionTime(project.Project, request.At); err != nil {
			return err
		}
		running, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationRunning, request.Phase, request.At, nil)
		if err != nil {
			return err
		}
		nextSession := session
		nextSession.State = domain.SessionStopping
		nextSession.Generation++
		nextSession.UpdatedAt = request.At
		persistedSession, err := updateExactProjectSession(tx, session, nextSession)
		if err != nil {
			return err
		}
		nextProject := project.Project
		nextProject.State = domain.ProjectStopping
		nextProject.UpdatedAt = request.At
		persistedProject, err := persistLifecycleProject(tx, nextProject)
		if err != nil {
			return err
		}
		result = lifecycleMutation(running, persistedProject, &persistedSession)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("begin project %q stop: %w", request.ProjectID, err)
	}
	return result, nil
}

// CompleteProjectStop atomically retires a joined session, stops its projection, and succeeds the stop operation.
func (store *Store) CompleteProjectStop(ctx context.Context, request CompleteProjectStopRequest) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	request.OperationKind = normalizedProjectStopOperationKind(request.OperationKind)
	if err := validateCompleteProjectStopRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "complete project stop", func(tx *gorm.DB) error {
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, request.OperationKind)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationSucceeded {
			replayed, replayErr := replayCompleteProjectStop(tx, current, history, request)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			return nil
		}
		if current.Operation.State != domain.OperationRunning {
			return fmt.Errorf("project stop operation %q must be running, got %q", request.OperationID, current.Operation.State)
		}
		if current.Revision != request.ExpectedOperationRevision {
			return staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, current.Revision)
		}
		if request.OperationKind == domain.OperationKindProjectRestart {
			if replayed, replayErr := replayCompleteProjectRestartStop(tx, current, request); replayErr == nil {
				result = replayed
				return nil
			}
		}
		session, err := readExactProjectSession(tx, request.ProjectID, request.Exit.SessionID)
		if err != nil {
			return err
		}
		if session.State != domain.SessionStopping {
			return fmt.Errorf("session %q must be stopping before completion, got %q", request.Exit.SessionID, session.State)
		}
		if err := validateConfirmedSessionExit(session, request.Exit); err != nil {
			return err
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Project.State != domain.ProjectStopping {
			return fmt.Errorf("project %q must be stopping before completion, got %q", request.ProjectID, project.Project.State)
		}
		if err := validateLifecycleProjectionTime(project.Project, request.Exit.ExitedAt); err != nil {
			return err
		}
		if request.OperationKind == domain.OperationKindProjectRestart {
			if err := deleteExactProjectSession(tx, session); err != nil {
				return err
			}
			nextProject := stoppedProjectProjection(project.Project, request.Exit.ExitedAt)
			persistedProject, err := persistLifecycleProject(tx, nextProject)
			if err != nil {
				return err
			}
			result = lifecycleMutation(current, persistedProject, nil)
			return nil
		}
		succeeded, err := transitionOperationInTransaction(tx, request.OperationID, current.Revision, domain.OperationSucceeded, request.Phase, request.Exit.ExitedAt, nil)
		if err != nil {
			return err
		}
		if err := deleteExactProjectSession(tx, session); err != nil {
			return err
		}
		nextProject := stoppedProjectProjection(project.Project, request.Exit.ExitedAt)
		persistedProject, err := persistLifecycleProject(tx, nextProject)
		if err != nil {
			return err
		}
		result = lifecycleMutation(succeeded, persistedProject, nil)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("complete project %q stop: %w", request.ProjectID, err)
	}
	return result, nil
}

// RecordUnexpectedProjectExit retires a joined process-backed terminal session and publishes failure without fabricating an operation.
func (store *Store) RecordUnexpectedProjectExit(ctx context.Context, request RecordUnexpectedProjectExitRequest) (ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := validateRecordUnexpectedProjectExitRequest(request); err != nil {
		return ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRecord{}, err
	}

	var result ProjectRecord
	err := store.mutations.mutate(ctx, "record unexpected project exit", func(tx *gorm.DB) error {
		session, err := readExactProjectSession(tx, request.ProjectID, request.Exit.SessionID)
		if err != nil {
			var missing *ProjectSessionNotFoundError
			if errors.As(err, &missing) {
				project, projectErr := readLifecycleProject(tx, request.ProjectID)
				if projectErr == nil && project.Project.State == domain.ProjectFailed && project.Project.UpdatedAt.Equal(request.Exit.ExitedAt) {
					result = project
					return nil
				}
			}
			return err
		}
		if !sessionCanStop(session) || session.State == domain.SessionStopping {
			return fmt.Errorf("session %q is not a ready process-backed session", request.Exit.SessionID)
		}
		if err := validateConfirmedSessionExit(session, request.Exit); err != nil {
			return err
		}
		project, err := readLifecycleProject(tx, request.ProjectID)
		if err != nil {
			return err
		}
		if project.Project.State != domain.ProjectReady && project.Project.State != domain.ProjectDegraded && project.Project.State != domain.ProjectFailed {
			return fmt.Errorf("project %q cannot record an unexpected exit from state %q", request.ProjectID, project.Project.State)
		}
		if err := validateLifecycleProjectionTime(project.Project, request.Exit.ExitedAt); err != nil {
			return err
		}
		if err := requireNoCompetingProjectOperation(tx, request.ProjectID, ""); err != nil {
			return err
		}
		if err := deleteExactProjectSession(tx, session); err != nil {
			return err
		}
		result, err = persistLifecycleProject(tx, failedProjectProjection(project.Project, request.Exit.ExitedAt))
		return err
	})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("record project %q unexpected exit: %w", request.ProjectID, err)
	}
	return result, nil
}

// normalizedProjectStartOperationKind keeps the existing state request shape compatible while admitting restart start phases.
func normalizedProjectStartOperationKind(kind domain.OperationKind) domain.OperationKind {
	if kind == "" {
		return domain.OperationKindProjectStart
	}
	return kind
}

// normalizedProjectStopOperationKind keeps the existing state request shape compatible while admitting restart stop phases.
func normalizedProjectStopOperationKind(kind domain.OperationKind) domain.OperationKind {
	if kind == "" {
		return domain.OperationKindProjectStop
	}
	return kind
}

// validateProjectStartOperationKind limits start-shaped mutations to start and restart operations.
func validateProjectStartOperationKind(kind domain.OperationKind) error {
	if kind != domain.OperationKindProjectStart && kind != domain.OperationKindProjectRestart {
		return fmt.Errorf("project start operation kind %q is unsupported", kind)
	}
	return nil
}

// validateProjectStopOperationKind limits stop-shaped mutations to stop and restart operations.
func validateProjectStopOperationKind(kind domain.OperationKind) error {
	if kind != domain.OperationKindProjectStop && kind != domain.OperationKindProjectRestart {
		return fmt.Errorf("project stop operation kind %q is unsupported", kind)
	}
	return nil
}

// validateBeginProjectStartRequest rejects uncorrelated session intent before writer admission.
func validateBeginProjectStartRequest(request BeginProjectStartRequest) error {
	if err := validateProjectStartOperationKind(request.OperationKind); err != nil {
		return err
	}
	if err := validateLifecycleOperationRequest(request.ProjectID, request.OperationID, request.ExpectedOperationRevision, request.Phase, request.At); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected project revision", request.ExpectedProjectRevision, false); err != nil {
		return err
	}
	if err := request.Session.Validate(); err != nil {
		return err
	}
	if request.Session.ProjectID != request.ProjectID {
		return fmt.Errorf("session project %q does not match requested project %q", request.Session.ProjectID, request.ProjectID)
	}
	if request.Session.Owner != domain.SessionOwnerHarbor || request.Session.State != domain.SessionPlanned || request.Session.Generation != 1 {
		return fmt.Errorf("Harbor start session must be Harbor-owned, planned, and generation 1")
	}
	if !request.Session.CreatedAt.Equal(request.At) || !request.Session.UpdatedAt.Equal(request.At) {
		return fmt.Errorf("planned session timestamps must equal start transition time")
	}
	return nil
}

// validateAttachProjectProcessRequest rejects stale or malformed process evidence before writer admission.
func validateAttachProjectProcessRequest(request AttachProjectProcessRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	if err := request.Process.Validate(); err != nil {
		return err
	}
	if request.OutputBroker != nil {
		if err := request.OutputBroker.Validate(); err != nil {
			return err
		}
	}
	return validateStoredTime("process attachment time", request.At)
}

// cloneOutputBrokerSession keeps lifecycle mutations from retaining mutable request-owned broker metadata.
func cloneOutputBrokerSession(broker *domain.OutputBrokerSession) *domain.OutputBrokerSession {
	if broker == nil {
		return nil
	}
	clone := *broker
	return &clone
}

// outputBrokerSessionsEqual compares optional broker metadata without treating nil and zero evidence as equivalent.
func outputBrokerSessionsEqual(left, right *domain.OutputBrokerSession) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// validateCompleteManagedSessionAttachmentRequest rejects incomplete authenticated-process proof before mutation.
func validateCompleteManagedSessionAttachmentRequest(request CompleteManagedSessionAttachmentRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	if err := request.Process.Validate(); err != nil {
		return err
	}
	return validateStoredTime("managed attachment time", request.At)
}

// validateCompleteProjectStartRequest rejects readiness that is not tied to one exact durable boundary.
func validateCompleteProjectStartRequest(request CompleteProjectStartRequest) error {
	if err := validateProjectStartOperationKind(request.OperationKind); err != nil {
		return err
	}
	if err := validateLifecycleOperationRequest(request.ProjectID, request.OperationID, request.ExpectedOperationRevision, request.Phase, request.At); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	return request.Runtime.Validate()
}

// validateConfirmedProjectProcessExit requires a generation fence and exact evidence whenever a process was accepted.
func validateConfirmedProjectProcessExit(exit ConfirmedProjectProcessExit) error {
	if err := exit.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", exit.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	if exit.Process != nil {
		if err := exit.Process.Validate(); err != nil {
			return err
		}
	}
	return validateStoredTime("process exit time", exit.ExitedAt)
}

// validateFailProjectStartRequest keeps terminal failure evidence correlated to its running operation.
func validateFailProjectStartRequest(request FailProjectStartRequest) error {
	if err := validateProjectStartOperationKind(request.OperationKind); err != nil {
		return err
	}
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.Phase) == "" {
		return fmt.Errorf("operation phase must not be empty")
	}
	if err := request.Problem.Validate(); err != nil {
		return err
	}
	return validateConfirmedProjectProcessExit(request.Exit)
}

// validateFailProjectRestartRequest rejects a restart failure without the post-stop project fence.
func validateFailProjectRestartRequest(request FailProjectRestartRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected project revision", request.ExpectedProjectRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.Phase) == "" {
		return fmt.Errorf("operation phase must not be empty")
	}
	if err := request.Problem.Validate(); err != nil {
		return err
	}
	return validateStoredTime("restart failure time", request.At)
}

// validateBeginProjectStopRequest rejects stop intent without exact operation and session fences.
func validateBeginProjectStopRequest(request BeginProjectStopRequest) error {
	if err := validateProjectStopOperationKind(request.OperationKind); err != nil {
		return err
	}
	if err := validateLifecycleOperationRequest(request.ProjectID, request.OperationID, request.ExpectedOperationRevision, request.Phase, request.At); err != nil {
		return err
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	_, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false)
	return err
}

// validateCompleteProjectStopRequest rejects completion without exact joined-process evidence.
func validateCompleteProjectStopRequest(request CompleteProjectStopRequest) error {
	if err := validateProjectStopOperationKind(request.OperationKind); err != nil {
		return err
	}
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", request.ExpectedOperationRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(request.Phase) == "" {
		return fmt.Errorf("operation phase must not be empty")
	}
	return validateConfirmedProjectProcessExit(request.Exit)
}

// validateRecordUnexpectedProjectExitRequest rejects observations that cannot identify one exact session birth.
func validateRecordUnexpectedProjectExitRequest(request RecordUnexpectedProjectExitRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if request.Exit.Process == nil {
		return fmt.Errorf("unexpected process exit must include exact process evidence")
	}
	return validateConfirmedProjectProcessExit(request.Exit)
}

// validateLifecycleOperationRequest applies shared optimistic identity, phase, and time constraints.
func validateLifecycleOperationRequest(
	projectID domain.ProjectID,
	operationID domain.OperationID,
	expectedRevision domain.Sequence,
	phase string,
	at time.Time,
) error {
	if err := projectID.Validate(); err != nil {
		return err
	}
	if err := operationID.Validate(); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected operation revision", expectedRevision, false); err != nil {
		return err
	}
	if strings.TrimSpace(phase) == "" {
		return fmt.Errorf("operation phase must not be empty")
	}
	return validateStoredTime("operation transition time", at)
}

// validateLifecycleProjectionTime prevents process observations from moving project chronology backward.
func validateLifecycleProjectionTime(project domain.ProjectSnapshot, at time.Time) error {
	if at.Before(project.UpdatedAt) {
		return fmt.Errorf("project lifecycle time precedes its current projection")
	}
	return nil
}

// readLifecycleOperation reconstructs the materialized operation and its exact retained history.
func readLifecycleOperation(
	tx *gorm.DB,
	operationID domain.OperationID,
	projectID domain.ProjectID,
	kind domain.OperationKind,
) (OperationRecord, []OperationTransition, error) {
	row, found, err := findOperationByID(tx, operationID)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	if !found {
		return OperationRecord{}, nil, &OperationNotFoundError{OperationID: operationID}
	}
	record, err := operationRecordFromModel(row)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	if record.Operation.Kind != kind || record.Operation.ProjectID != projectID {
		return OperationRecord{}, nil, fmt.Errorf("operation %q is %q for project %q, not %q for project %q", operationID, record.Operation.Kind, record.Operation.ProjectID, kind, projectID)
	}
	history, err := operationHistoryInTransaction(tx, record)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	return record, history, nil
}

// readLifecycleProject validates the aggregate and the unique global sequence it currently owns.
func readLifecycleProject(tx *gorm.DB, projectID domain.ProjectID) (ProjectRecord, error) {
	record, err := readProjectRecord(tx, projectID)
	if err != nil {
		return ProjectRecord{}, err
	}
	if err := validateProjectSequenceOwner(tx, record); err != nil {
		return ProjectRecord{}, err
	}
	return record, nil
}

// persistLifecycleProject allocates a distinct projection revision within the caller's lifecycle transaction.
func persistLifecycleProject(tx *gorm.DB, project domain.ProjectSnapshot) (ProjectRecord, error) {
	project = canonicalProjectForMutation(project)
	if err := project.Validate(); err != nil {
		return ProjectRecord{}, err
	}
	sequence, err := allocateHarborSequence(tx)
	if err != nil {
		return ProjectRecord{}, err
	}
	projectRow, appRows, serviceRows, resourceRows, err := projectModelsFromDomain(project, sequence)
	if err != nil {
		return ProjectRecord{}, err
	}
	if err := putProjectAggregate(tx, projectRow, appRows, serviceRows, resourceRows); err != nil {
		return ProjectRecord{}, err
	}
	persisted, err := readProjectRecord(tx, project.ID)
	if err != nil {
		return ProjectRecord{}, err
	}
	if persisted.Revision != sequence || !reflect.DeepEqual(persisted.Project, project) {
		return ProjectRecord{}, corruptStateError("project", string(project.ID), fmt.Errorf("lifecycle projection readback differs from committed revision"))
	}
	if err := validateProjectSequenceOwner(tx, persisted); err != nil {
		return ProjectRecord{}, err
	}
	return persisted, nil
}

// readOptionalProjectSession distinguishes absence from malformed or duplicated durable authority.
func readOptionalProjectSession(tx *gorm.DB, projectID domain.ProjectID) (domain.ProjectSession, bool, error) {
	var rows []models.ProjectSession
	if err := tx.Where("project_id = ?", string(projectID)).Order("id ASC").Find(&rows).Error; err != nil {
		return domain.ProjectSession{}, false, fmt.Errorf("read project session row: %w", err)
	}
	if len(rows) == 0 {
		return domain.ProjectSession{}, false, nil
	}
	if len(rows) != 1 {
		return domain.ProjectSession{}, false, corruptStateError("project session", string(projectID), fmt.Errorf("project owns multiple active sessions"))
	}
	session, err := projectSessionFromModel(rows[0])
	return session, true, err
}

// readExactProjectSession requires both project and session identities to match one durable row.
func readExactProjectSession(tx *gorm.DB, projectID domain.ProjectID, sessionID domain.SessionID) (domain.ProjectSession, error) {
	session, found, err := readOptionalProjectSession(tx, projectID)
	if err != nil {
		return domain.ProjectSession{}, err
	}
	if !found || session.ID != sessionID {
		return domain.ProjectSession{}, &ProjectSessionNotFoundError{ProjectID: projectID, SessionID: sessionID}
	}
	return session, nil
}

// requireNoActiveProjectSession prevents one start from replacing process authority it does not own.
func requireNoActiveProjectSession(tx *gorm.DB, projectID domain.ProjectID) error {
	session, found, err := readOptionalProjectSession(tx, projectID)
	if err != nil {
		return err
	}
	if found {
		return &ProjectSessionActiveError{ProjectID: projectID, SessionID: session.ID}
	}
	return nil
}

// requireNoCompetingProjectOperation makes the owning lifecycle operation the only active writer for its project.
func requireNoCompetingProjectOperation(tx *gorm.DB, projectID domain.ProjectID, excluded domain.OperationID) error {
	operationIDs, err := activeProjectOperationIDsExcluding(tx, projectID, excluded)
	if err != nil {
		return err
	}
	if len(operationIDs) != 0 {
		return &ProjectBusyError{ProjectID: projectID, OperationIDs: operationIDs}
	}
	return nil
}

// updateExactProjectSession advances one row only while its prior generation still owns authority.
func updateExactProjectSession(tx *gorm.DB, current domain.ProjectSession, next domain.ProjectSession) (domain.ProjectSession, error) {
	if err := next.Validate(); err != nil {
		return domain.ProjectSession{}, err
	}
	if current.ID != next.ID || current.ProjectID != next.ProjectID || next.Generation != current.Generation+1 {
		return domain.ProjectSession{}, fmt.Errorf("session update must preserve identity and advance generation exactly once")
	}
	row, err := projectSessionModelFromDomain(next)
	if err != nil {
		return domain.ProjectSession{}, err
	}
	updates := map[string]any{
		"state":                             row.State,
		"generation":                        row.Generation,
		"pid":                               nullableProjectSessionInt(row.Pid),
		"birth_token":                       nullableProjectSessionString(row.BirthToken),
		"executable_identity":               nullableProjectSessionString(row.ExecutableIdentity),
		"argument_digest":                   nullableProjectSessionString(row.ArgumentDigest),
		"output_broker_endpoint_reference":  nullableProjectSessionString(row.OutputBrokerEndpointReference),
		"output_broker_ticket_digest":       nullableProjectSessionString(row.OutputBrokerTicketDigest),
		"output_broker_manifest_path":       nullableProjectSessionString(row.OutputBrokerManifestPath),
		"output_broker_pid":                 nullableProjectSessionInt(row.OutputBrokerPid),
		"output_broker_birth_token":         nullableProjectSessionString(row.OutputBrokerBirthToken),
		"output_broker_executable_identity": nullableProjectSessionString(row.OutputBrokerExecutableIdentity),
		"output_broker_argument_digest":     nullableProjectSessionString(row.OutputBrokerArgumentDigest),
		"updated_at":                        row.UpdatedAt,
	}
	updated := tx.Model(&models.ProjectSession{}).
		Where("session_id = ? AND project_id = ? AND generation = ?", string(current.ID), string(current.ProjectID), int(current.Generation)).
		Updates(updates)
	if updated.Error != nil {
		return domain.ProjectSession{}, fmt.Errorf("update project session: %w", updated.Error)
	}
	if updated.RowsAffected != 1 {
		persisted, readErr := readExactProjectSession(tx, current.ProjectID, current.ID)
		if readErr != nil {
			return domain.ProjectSession{}, readErr
		}
		return domain.ProjectSession{}, staleSessionGeneration(current.ProjectID, current.ID, current.Generation, persisted.Generation)
	}
	persisted, err := readExactProjectSession(tx, current.ProjectID, current.ID)
	if err != nil {
		return domain.ProjectSession{}, err
	}
	if !reflect.DeepEqual(persisted, next) {
		return domain.ProjectSession{}, corruptStateError("project session", string(current.ID), fmt.Errorf("session update readback differs from requested generation"))
	}
	return persisted, nil
}

// nullableProjectSessionString maps generated nullable strings to SQL NULL instead of an empty authority value.
func nullableProjectSessionString(value null.String) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

// nullableProjectSessionInt maps generated nullable integers to SQL NULL instead of a zero PID.
func nullableProjectSessionInt(value null.Int) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

// deleteExactProjectSession removes only the process authority whose generation was joined by the caller.
func deleteExactProjectSession(tx *gorm.DB, session domain.ProjectSession) error {
	deleted := tx.Where(
		"session_id = ? AND project_id = ? AND generation = ?",
		string(session.ID),
		string(session.ProjectID),
		int(session.Generation),
	).Delete(&models.ProjectSession{})
	if deleted.Error != nil {
		return fmt.Errorf("delete project session: %w", deleted.Error)
	}
	if deleted.RowsAffected != 1 {
		return staleSessionGeneration(session.ProjectID, session.ID, session.Generation, 0)
	}
	return nil
}

// validateConfirmedSessionExit matches both generation and immutable birth evidence before retirement.
func validateConfirmedSessionExit(session domain.ProjectSession, exit ConfirmedProjectProcessExit) error {
	if session.Generation != exit.ExpectedSessionGeneration {
		return staleSessionGeneration(session.ProjectID, session.ID, exit.ExpectedSessionGeneration, session.Generation)
	}
	if session.Process == nil {
		if exit.Process != nil {
			return fmt.Errorf("planned session %q cannot match process evidence", session.ID)
		}
		return nil
	}
	if exit.Process == nil || *session.Process != *exit.Process {
		return fmt.Errorf("confirmed process evidence does not match session %q", session.ID)
	}
	if exit.ExitedAt.Before(session.UpdatedAt) {
		return fmt.Errorf("process exit time precedes session generation")
	}
	return nil
}

// replayBeginProjectStart accepts only the exact queued-to-running edge and planned session originally committed.
func replayBeginProjectStart(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request BeginProjectStartRequest,
) (ProjectLifecycleMutation, error) {
	if err := requireExactLifecycleReplay(history, request.ExpectedOperationRevision, domain.OperationRunning, request.Phase, request.At, nil); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if project.Project.State != domain.ProjectStarting || !project.Project.UpdatedAt.Equal(request.At) {
		return ProjectLifecycleMutation{}, fmt.Errorf("start replay does not match the committed starting project projection")
	}
	session, err := readExactProjectSession(tx, request.ProjectID, request.Session.ID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if !reflect.DeepEqual(session, request.Session) {
		return ProjectLifecycleMutation{}, fmt.Errorf("start replay does not match the committed planned session")
	}
	return lifecycleMutation(operation, project, &session), nil
}

// beginRunningProjectRestartStart crosses the restart operation's durable stop-to-start boundary.
// The operation remains running while the project projection proves that the old session is gone and a new planned session owns the next launch.
func beginRunningProjectRestartStart(
	tx *gorm.DB,
	operation OperationRecord,
	request BeginProjectStartRequest,
) (ProjectLifecycleMutation, error) {
	if operation.Operation.Kind != domain.OperationKindProjectRestart || operation.Operation.State != domain.OperationRunning {
		return ProjectLifecycleMutation{}, fmt.Errorf("restart start continuation requires a running restart operation")
	}
	if operation.Revision != request.ExpectedOperationRevision {
		return ProjectLifecycleMutation{}, staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, operation.Revision)
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if project.Project.State == domain.ProjectStarting {
		session, sessionErr := readExactProjectSession(tx, request.ProjectID, request.Session.ID)
		if sessionErr != nil {
			return ProjectLifecycleMutation{}, sessionErr
		}
		if !reflect.DeepEqual(session, request.Session) {
			return ProjectLifecycleMutation{}, fmt.Errorf("restart start replay does not match the committed planned session")
		}
		return lifecycleMutation(operation, project, &session), nil
	}
	if project.Project.State != domain.ProjectStopped {
		return ProjectLifecycleMutation{}, fmt.Errorf("project %q must be stopped before restart start, got %q", request.ProjectID, project.Project.State)
	}
	if err := requireNoActiveProjectSession(tx, request.ProjectID); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := requireNoCompetingProjectOperation(tx, request.ProjectID, request.OperationID); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if project.Revision != request.ExpectedProjectRevision {
		return ProjectLifecycleMutation{}, &ProjectRevisionConflictError{
			ProjectID: request.ProjectID,
			Expected:  request.ExpectedProjectRevision,
			Actual:    project.Revision,
		}
	}
	if err := validateLifecycleProjectionTime(project.Project, request.At); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := request.Session.Validate(); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if request.Session.ProjectID != request.ProjectID || request.Session.State != domain.SessionPlanned || request.Session.Generation != 1 {
		return ProjectLifecycleMutation{}, fmt.Errorf("restart planned session is not correlated to project %q", request.ProjectID)
	}
	if !request.Session.CreatedAt.Equal(request.At) || !request.Session.UpdatedAt.Equal(request.At) {
		return ProjectLifecycleMutation{}, fmt.Errorf("restart planned session timestamps must equal start transition time")
	}
	row, err := projectSessionModelFromDomain(request.Session)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := requireOneCreate(tx.Create(&row), "create planned restart project session", string(request.Session.ID)); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	nextProject := project.Project
	nextProject.State = domain.ProjectStarting
	nextProject.UpdatedAt = request.At
	persistedProject, err := persistLifecycleProject(tx, nextProject)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	persistedSession, err := readExactProjectSession(tx, request.ProjectID, request.Session.ID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	return lifecycleMutation(operation, persistedProject, &persistedSession), nil
}

// replayCompleteProjectStart accepts only the exact readiness edge and runtime projection originally committed.
func replayCompleteProjectStart(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request CompleteProjectStartRequest,
) (ProjectLifecycleMutation, error) {
	if err := requireExactLifecycleReplay(history, request.ExpectedOperationRevision, domain.OperationSucceeded, request.Phase, request.At, nil); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if !projectMatchesReadyRuntime(project.Project, request.Runtime, request.At) {
		return ProjectLifecycleMutation{}, fmt.Errorf("start completion replay does not match the committed ready projection")
	}
	session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if !sessionCanPublishReadiness(session) || session.Generation != request.ExpectedSessionGeneration {
		return ProjectLifecycleMutation{}, fmt.Errorf("start completion replay does not match the committed process session")
	}
	return lifecycleMutation(operation, project, &session), nil
}

// replayFailProjectStart accepts only the exact terminal failure after its session was retired.
func replayFailProjectStart(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request FailProjectStartRequest,
) (ProjectLifecycleMutation, error) {
	if err := requireExactLifecycleReplay(
		history,
		request.ExpectedOperationRevision,
		domain.OperationFailed,
		request.Phase,
		request.Exit.ExitedAt,
		&request.Problem,
	); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := requireNoActiveProjectSession(tx, request.ProjectID); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if !projectMatchesInactiveState(project.Project, domain.ProjectFailed, request.Exit.ExitedAt) {
		return ProjectLifecycleMutation{}, fmt.Errorf("start failure replay does not match the committed failed projection")
	}
	return lifecycleMutation(operation, project, nil), nil
}

// replayBeginProjectStop accepts only the exact queued-to-running edge and stopping session generation.
func replayBeginProjectStop(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request BeginProjectStopRequest,
) (ProjectLifecycleMutation, error) {
	if err := requireExactLifecycleReplay(history, request.ExpectedOperationRevision, domain.OperationRunning, request.Phase, request.At, nil); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if project.Project.State != domain.ProjectStopping || !project.Project.UpdatedAt.Equal(request.At) {
		return ProjectLifecycleMutation{}, fmt.Errorf("stop replay does not match the committed stopping project projection")
	}
	session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if session.State != domain.SessionStopping || session.Generation != request.ExpectedSessionGeneration+1 || !session.UpdatedAt.Equal(request.At) {
		return ProjectLifecycleMutation{}, fmt.Errorf("stop replay does not match the committed stopping session")
	}
	return lifecycleMutation(operation, project, &session), nil
}

// replayCompleteProjectStop accepts only the exact terminal edge after session retirement and stopped projection publication.
func replayCompleteProjectStop(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request CompleteProjectStopRequest,
) (ProjectLifecycleMutation, error) {
	if err := requireExactLifecycleReplay(
		history,
		request.ExpectedOperationRevision,
		domain.OperationSucceeded,
		request.Phase,
		request.Exit.ExitedAt,
		nil,
	); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := requireNoActiveProjectSession(tx, request.ProjectID); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if !projectMatchesInactiveState(project.Project, domain.ProjectStopped, request.Exit.ExitedAt) {
		return ProjectLifecycleMutation{}, fmt.Errorf("stop completion replay does not match the committed stopped projection")
	}
	return lifecycleMutation(operation, project, nil), nil
}

// replayCompleteProjectRestartStop recognizes the restart boundary after the old session has been retired.
func replayCompleteProjectRestartStop(
	tx *gorm.DB,
	operation OperationRecord,
	request CompleteProjectStopRequest,
) (ProjectLifecycleMutation, error) {
	if operation.Operation.Kind != domain.OperationKindProjectRestart || operation.Operation.State != domain.OperationRunning {
		return ProjectLifecycleMutation{}, fmt.Errorf("restart stop replay requires a running restart operation")
	}
	if operation.Revision != request.ExpectedOperationRevision {
		return ProjectLifecycleMutation{}, staleLifecycleRevision(request.OperationID, request.ExpectedOperationRevision, operation.Revision)
	}
	if err := requireNoActiveProjectSession(tx, request.ProjectID); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if !projectMatchesInactiveState(project.Project, domain.ProjectStopped, request.Exit.ExitedAt) {
		return ProjectLifecycleMutation{}, fmt.Errorf("restart stop replay does not match the committed stopped projection")
	}
	return lifecycleMutation(operation, project, nil), nil
}

// requireExactLifecycleReplay proves a retry names the transition immediately following its expected revision.
func requireExactLifecycleReplay(
	history []OperationTransition,
	expectedPreviousRevision domain.Sequence,
	state domain.OperationState,
	phase string,
	at time.Time,
	problem *domain.Problem,
) error {
	if len(history) < 2 {
		return fmt.Errorf("lifecycle replay requires a retained predecessor transition")
	}
	previous := history[len(history)-2]
	last := history[len(history)-1]
	if previous.Sequence != expectedPreviousRevision || last.State != state || last.Phase != phase || !last.OccurredAt.Equal(at) || !operationProblemsEqual(last.Problem, problem) {
		return fmt.Errorf("lifecycle retry does not match the exact committed transition")
	}
	return nil
}

// projectMatchesReadyRuntime compares the user-visible readiness facts without depending on surrogate persistence IDs.
func projectMatchesReadyRuntime(project domain.ProjectSnapshot, runtime DefaultProjectRuntime, at time.Time) bool {
	if project.State != domain.ProjectReady || !project.UpdatedAt.Equal(at) || len(project.Apps) != 1 {
		return false
	}
	return project.Apps[0] == runtime.App &&
		reflect.DeepEqual(project.Services, runtime.Services) &&
		reflect.DeepEqual(project.Resources, runtime.Resources)
}

// projectMatchesInactiveState proves joined process-owned entities are no longer presented as active.
func projectMatchesInactiveState(project domain.ProjectSnapshot, state domain.ProjectState, at time.Time) bool {
	if project.State != state || !project.UpdatedAt.Equal(at) || len(project.Resources) != 0 {
		return false
	}
	for _, app := range project.Apps {
		if app.State != domain.EntityStopped || app.Active {
			return false
		}
	}
	for _, service := range project.Services {
		if service.State != domain.EntityStopped {
			return false
		}
	}
	return true
}

// projectCanStart permits retryable terminal outcomes while excluding states that may retain process authority.
func projectCanStart(state domain.ProjectState) bool {
	return state == domain.ProjectStopped || state == domain.ProjectFailed || state == domain.ProjectUnavailable
}

// sessionCanPublishReadiness accepts supervised and authenticated process-backed launch states without upgrading either one implicitly.
func sessionCanPublishReadiness(session domain.ProjectSession) bool {
	if session.Process == nil {
		return false
	}
	return session.State == domain.SessionAwaitingAttach || session.State == domain.SessionAttached
}

// sessionCanStop accepts every durable state that still retains exact process authority.
func sessionCanStop(session domain.ProjectSession) bool {
	if session.Process == nil {
		return false
	}
	switch session.State {
	case domain.SessionAwaitingAttach, domain.SessionAttached, domain.SessionDisconnected, domain.SessionStopping:
		return true
	default:
		return false
	}
}

// readyProjectProjection atomically replaces prior topology with the runtime Harbor actually observed.
func readyProjectProjection(project domain.ProjectSnapshot, runtime DefaultProjectRuntime, at time.Time) domain.ProjectSnapshot {
	project.State = domain.ProjectReady
	project.UpdatedAt = at
	project.Apps = []domain.AppSnapshot{runtime.App}
	project.Services = append(make([]domain.ServiceSnapshot, 0, len(runtime.Services)), runtime.Services...)
	project.Resources = append(make([]domain.ResourceSnapshot, 0, len(runtime.Resources)), runtime.Resources...)
	return project
}

// stoppedProjectProjection retains launch metadata while truthfully marking every process-owned entity inactive.
func stoppedProjectProjection(project domain.ProjectSnapshot, at time.Time) domain.ProjectSnapshot {
	project.State = domain.ProjectStopped
	project.UpdatedAt = at
	for index := range project.Apps {
		project.Apps[index].State = domain.EntityStopped
		project.Apps[index].Active = false
	}
	for index := range project.Services {
		project.Services[index].State = domain.EntityStopped
	}
	project.Resources = []domain.ResourceSnapshot{}
	return project
}

// failedProjectProjection preserves the abnormal aggregate outcome while recording that its joined entities are stopped.
func failedProjectProjection(project domain.ProjectSnapshot, at time.Time) domain.ProjectSnapshot {
	project = stoppedProjectProjection(project, at)
	project.State = domain.ProjectFailed
	return project
}

// lifecycleMutation copies an optional session so callers cannot mutate transaction-local result storage.
func lifecycleMutation(operation OperationRecord, project ProjectRecord, session *domain.ProjectSession) ProjectLifecycleMutation {
	return ProjectLifecycleMutation{Operation: operation, Project: project, Session: cloneProjectSession(session)}
}

// cloneProjectSession copies nested process evidence at the state API boundary.
func cloneProjectSession(session *domain.ProjectSession) *domain.ProjectSession {
	if session == nil {
		return nil
	}
	copy := *session
	copy.Process = cloneProcessEvidence(session.Process)
	return &copy
}

// cloneProcessEvidence copies optional immutable process evidence for caller isolation.
func cloneProcessEvidence(process *domain.ProcessEvidence) *domain.ProcessEvidence {
	if process == nil {
		return nil
	}
	copy := *process
	return &copy
}

// staleLifecycleRevision formats optimistic operation failures through the established typed boundary.
func staleLifecycleRevision(operationID domain.OperationID, expected, actual domain.Sequence) error {
	return &StaleRevisionError{OperationID: operationID, Expected: expected, Actual: actual}
}

// staleSessionGeneration distinguishes process-authority races from operation revision races.
func staleSessionGeneration(projectID domain.ProjectID, sessionID domain.SessionID, expected, actual uint64) error {
	return &StaleSessionGenerationError{
		ProjectID: projectID,
		SessionID: sessionID,
		Expected:  expected,
		Actual:    actual,
	}
}
