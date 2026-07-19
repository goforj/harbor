package control

import (
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
)

// StartProjectRequest identifies one project and the client-stable intent that owns its start attempt.
type StartProjectRequest struct {
	ProjectID domain.ProjectID `json:"project_id"`
	IntentID  domain.IntentID  `json:"intent_id"`
}

// Validate reports whether the request can identify one idempotent project start.
func (request StartProjectRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	return request.IntentID.Validate()
}

// StopProjectRequest identifies one project and the client-stable intent that owns its stop attempt.
type StopProjectRequest struct {
	ProjectID domain.ProjectID `json:"project_id"`
	IntentID  domain.IntentID  `json:"intent_id"`
}

// Validate reports whether the request can identify one idempotent project stop.
func (request StopProjectRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	return request.IntentID.Validate()
}

// ProjectLifecycleOperation is the authoritative durable progress reached by a start or stop request.
type ProjectLifecycleOperation struct {
	Operation domain.Operation `json:"operation"`
	Revision  domain.Sequence  `json:"revision"`
}

// Validate reports whether the result contains one bounded project start or stop operation revision.
func (result ProjectLifecycleOperation) Validate() error {
	if err := result.Operation.Validate(); err != nil {
		return err
	}
	if result.Operation.Kind != domain.OperationKindProjectStart && result.Operation.Kind != domain.OperationKindProjectStop {
		return errors.New("project lifecycle result must contain a project start or stop operation")
	}
	if result.Revision == 0 || result.Revision > domain.MaximumSequence {
		return fmt.Errorf("project lifecycle revision must be between 1 and %d", domain.MaximumSequence)
	}
	return nil
}

// projectLifecycleResponse keeps the method result extensible around its durable operation.
type projectLifecycleResponse struct {
	Lifecycle ProjectLifecycleOperation `json:"lifecycle"`
}

// validateProjectLifecycleCorrelation binds daemon progress to the method, project, and client-owned intent.
func validateProjectLifecycleCorrelation(
	projectID domain.ProjectID,
	intentID domain.IntentID,
	kind domain.OperationKind,
	result ProjectLifecycleOperation,
) error {
	if result.Operation.ProjectID != projectID || result.Operation.IntentID != intentID || result.Operation.Kind != kind {
		return errors.New("project lifecycle result does not match the requested action, project, and intent")
	}
	return nil
}
