package state

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"gorm.io/gorm"
)

// QuarantinePlannedProjectStartRequest identifies one ambiguous launch whose process authority cannot yet be resolved.
type QuarantinePlannedProjectStartRequest struct {
	ProjectID                 domain.ProjectID
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	ExpectedProjectRevision   domain.Sequence
	SessionID                 domain.SessionID
	ExpectedSessionGeneration uint64
	Phase                     string
	Problem                   domain.Problem
	At                        time.Time
}

// QuarantinePlannedProjectStart atomically fails an ambiguous start while retaining its unresolved planned session.
func (store *Store) QuarantinePlannedProjectStart(
	ctx context.Context,
	request QuarantinePlannedProjectStartRequest,
) (ProjectLifecycleMutation, error) {
	ctx = normalizeContext(ctx)
	if err := validateQuarantinePlannedProjectStartRequest(request); err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectLifecycleMutation{}, err
	}

	var result ProjectLifecycleMutation
	err := store.mutations.mutate(ctx, "quarantine planned project start", func(tx *gorm.DB) error {
		current, history, err := readLifecycleOperation(tx, request.OperationID, request.ProjectID, domain.OperationKindProjectStart)
		if err != nil {
			return err
		}
		if current.Operation.State == domain.OperationFailed {
			replayed, replayErr := replayQuarantinePlannedProjectStart(tx, current, history, request)
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
		if project.Project.State != domain.ProjectStarting {
			return fmt.Errorf("project %q must be starting before planned launch quarantine, got %q", request.ProjectID, project.Project.State)
		}

		session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
		if err != nil {
			return err
		}
		if session.Generation != request.ExpectedSessionGeneration {
			return staleSessionGeneration(request.ProjectID, request.SessionID, request.ExpectedSessionGeneration, session.Generation)
		}
		if session.State != domain.SessionPlanned || session.Process != nil {
			return fmt.Errorf("session %q must be planned without process evidence before launch quarantine", request.SessionID)
		}
		if request.At.Before(session.UpdatedAt) {
			return fmt.Errorf("launch quarantine time precedes session generation")
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
			return corruptStateError("project session", string(session.ID), fmt.Errorf("launch quarantine changed unresolved session authority"))
		}
		result = lifecycleMutation(failed, persistedProject, &persistedSession)
		return nil
	})
	if err != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("quarantine project %q planned start: %w", request.ProjectID, err)
	}
	return result, nil
}

// validateQuarantinePlannedProjectStartRequest rejects quarantine intent without exact operation, project, and session fences.
func validateQuarantinePlannedProjectStartRequest(request QuarantinePlannedProjectStartRequest) error {
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
		return fmt.Errorf("expected project revision must immediately follow the running start operation revision")
	}
	if err := request.SessionID.Validate(); err != nil {
		return err
	}
	if _, err := unsignedToModelInt("expected session generation", request.ExpectedSessionGeneration, false); err != nil {
		return err
	}
	return request.Problem.Validate()
}

// replayQuarantinePlannedProjectStart accepts only the exact terminal edge and retained unresolved session authority.
func replayQuarantinePlannedProjectStart(
	tx *gorm.DB,
	operation OperationRecord,
	history []OperationTransition,
	request QuarantinePlannedProjectStartRequest,
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
		return ProjectLifecycleMutation{}, fmt.Errorf("planned launch quarantine replay does not match the committed unavailable projection")
	}
	session, err := readExactProjectSession(tx, request.ProjectID, request.SessionID)
	if err != nil {
		return ProjectLifecycleMutation{}, err
	}
	if session.Generation != request.ExpectedSessionGeneration || session.State != domain.SessionPlanned || session.Process != nil {
		return ProjectLifecycleMutation{}, fmt.Errorf("planned launch quarantine replay does not match the retained unresolved session")
	}
	return lifecycleMutation(operation, project, &session), nil
}
