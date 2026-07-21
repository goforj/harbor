package state

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// QuarantineProjectProcessScopeRequest identifies one lifecycle operation whose complete process scope is unresolved.
type QuarantineProjectProcessScopeRequest struct {
	ProjectID                 domain.ProjectID
	OperationID               domain.OperationID
	OperationKind             domain.OperationKind
	ExpectedOperationRevision domain.Sequence
	ExpectedProjectRevision   domain.Sequence
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Phase                     string
	Problem                   domain.Problem
	At                        time.Time
}

// QuarantineProjectProcessScope atomically fails an unresolved lifecycle while retaining its exact session authority.
func (store *Store) QuarantineProjectProcessScope(
	ctx context.Context,
	request QuarantineProjectProcessScopeRequest,
) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	if err := validateQuarantineProjectProcessScopeRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "quarantine project process scope", func(tx *gorm.DB) error {
		operationKind := request.OperationKind
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, operationKind)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationFailed {
			replayed, replayErr := replayQuarantineProjectProcessScope(tx, current, history, request)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			return nil
		}
		if current.Operation.State != domain.OperationRunning {
			return fmt.Errorf("project lifecycle operation %q must be running, got %q", request.OperationID, current.Operation.State)
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
		expectedProjectState := domain.ProjectStarting
		if operationKind == domain.OperationKindProjectStop || operationKind == domain.OperationKindProjectRestart {
			expectedProjectState = domain.ProjectStopping
		}
		if project.Project.State != expectedProjectState {
			return fmt.Errorf("project %q must be %q before lifecycle quarantine, got %q", request.ProjectID, expectedProjectState, project.Project.State)
		}

		session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if session.Generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, session.Generation)
		}
		if !sessionCanRemainInProcessScopeQuarantine(session) {
			return fmt.Errorf("session %q is not an unresolved process-scope boundary", request.SessionID)
		}
		if request.At.Before(session.UpdatedAt) {
			return fmt.Errorf("process-scope quarantine time precedes session generation")
		}
		if err := validateLifecycleProjectionTime(project.Project, request.At); err != nil {
			return err
		}

		failed, err := transitionOperationInTransaction(
			tx,
			request.OperationID,
			current.Revision,
			domain.OperationFailed,
			request.Phase,
			request.At,
			&request.Problem,
		)
		if err != nil {
			return err
		}
		nextProject := project.Project
		nextProject.State = domain.ProjectUnavailable
		nextProject.UpdatedAt = request.At
		persistedProject, err := persistLifecycleProject(tx, nextProject)
		if err != nil {
			return err
		}
		persistedSession, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(persistedSession, session) {
			return corruptStateError("project session", string(session.ID), fmt.Errorf("process-scope quarantine changed unresolved session authority"))
		}
		result = lifecycleMutation(failed, persistedProject, &persistedSession)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("quarantine project %q process scope: %w", request.ProjectID, err)
	}
	return result, nil
}

// validateQuarantineProjectProcessScopeRequest rejects quarantine intent without exact operation, project, and session fences.
func validateQuarantineProjectProcessScopeRequest(request QuarantineProjectProcessScopeRequest) error {
	if kind := request.OperationKind; kind != domain.OperationKindProjectStart && kind != domain.OperationKindProjectStop && kind != domain.OperationKindProjectRestart {
		return fmt.Errorf("lifecycle quarantine operation kind %q is unsupported", kind)
	}
	if err := validateLifecycleOperationRequest(
		request.ProjectID,
		request.OperationID,
		request.ExpectedOperationRevision,
		request.Phase,
		request.At,
	); err != nil {
		return err
	}
	if _, err := sequenceToModelInt("expected project revision", request.ExpectedProjectRevision, false); err != nil {
		return err
	}
	if request.ExpectedOperationRevision == domain.MaximumSequence || request.ExpectedProjectRevision != request.ExpectedOperationRevision+1 {
		return fmt.Errorf("expected project revision must immediately follow the running lifecycle operation revision")
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	return request.Problem.Validate()
}

// replayQuarantineProjectProcessScope accepts only the exact terminal edge and retained unresolved session authority.
func replayQuarantineProjectProcessScope(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request QuarantineProjectProcessScopeRequest,
) (ProjectLifecycleMutation, error) {
	if err := requireExactLifecycleReplay(
		history,
		request.ExpectedOperationRevision,
		domain.OperationFailed,
		request.Phase,
		request.At,
		&request.Problem,
	); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	project, err := readLifecycleProject(tx, request.ProjectID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if project.Project.State != domain.ProjectUnavailable || !project.Project.UpdatedAt.Equal(request.At) {
		return ProjectLifecycleMutation{}, fmt.Errorf("process-scope quarantine replay does not match the committed unavailable projection")
	}
	session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if session.Generation != request.ExpectedSessionGeneration || !sessionCanRemainInProcessScopeQuarantine(session) {
		return ProjectLifecycleMutation{}, fmt.Errorf("process-scope quarantine replay does not match the retained unresolved session")
	}
	return lifecycleMutation(operation, project, &session), nil
}

// sessionCanRemainInProcessScopeQuarantine retains any accepted launch or stop boundary whose scope is unresolved.
func sessionCanRemainInProcessScopeQuarantine(session domain.ProjectSession) bool {
	return session.State == domain.SessionPlanned && session.Process == nil ||
		session.State == domain.SessionAwaitingAttach && session.Process != nil ||
		session.State == domain.SessionStopping && session.Process != nil
}
