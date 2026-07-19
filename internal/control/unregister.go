package control

import (
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
)

// UnregisterProjectRequest identifies one project and the client-stable intent that owns its removal attempt.
type UnregisterProjectRequest struct {
	ProjectID domain.ProjectID `json:"project_id"`
	IntentID  domain.IntentID  `json:"intent_id"`
}

// Validate reports whether the request can identify one stable idempotent project mutation.
func (request UnregisterProjectRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	return request.IntentID.Validate()
}

// ProjectUnregistration is the authoritative durable operation reached by an unregister request.
type ProjectUnregistration struct {
	Operation domain.Operation `json:"operation"`
	Revision  domain.Sequence  `json:"revision"`
}

// Validate reports whether the result describes one bounded project-unregister operation revision.
func (unregistration ProjectUnregistration) Validate() error {
	if err := unregistration.Operation.Validate(); err != nil {
		return err
	}
	if unregistration.Operation.Kind != domain.OperationKindProjectUnregister {
		return errors.New("project unregistration must contain a project unregister operation")
	}
	if unregistration.Revision == 0 || unregistration.Revision > domain.MaximumSequence {
		return fmt.Errorf(
			"project unregistration revision must be between 1 and %d",
			domain.MaximumSequence,
		)
	}
	return nil
}

// projectUnregistrationResponse keeps the method result extensible around its reviewed durable operation.
type projectUnregistrationResponse struct {
	Unregistration ProjectUnregistration `json:"unregistration"`
}
