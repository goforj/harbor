package state

import (
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/models"
	"gorm.io/gorm"
)

// projectNetworkReleaseBoundary is the fully validated teardown owner for one immutable project identity.
type projectNetworkReleaseBoundary struct {
	found bool
	row   models.NetworkProjectRelease
	owner projectNetworkReleaseOwner
}

// readProjectNetworkReleaseBoundary validates the complete optional network aggregate before authorizing a project writer.
func readProjectNetworkReleaseBoundary(
	tx *gorm.DB,
	projectID domain.ProjectID,
) (projectNetworkReleaseBoundary, error) {
	present, err := inspectNetworkSchema(tx)
	if err != nil || !present {
		return projectNetworkReleaseBoundary{}, err
	}
	rows, err := readNetworkModelRows(tx)
	if err != nil {
		return projectNetworkReleaseBoundary{}, err
	}
	_, initialized, err := networkRecordFromModels(rows)
	if err != nil {
		return projectNetworkReleaseBoundary{}, err
	}
	if !initialized {
		return projectNetworkReleaseBoundary{}, nil
	}
	highWater, err := validateRetainedSequenceBounds(tx)
	if err != nil {
		return projectNetworkReleaseBoundary{}, err
	}
	owners, err := validateProjectNetworkReleaseOwners(tx, highWater, rows.Releases)
	if err != nil {
		return projectNetworkReleaseBoundary{}, err
	}
	boundary := projectNetworkReleaseBoundary{}
	if projectID == "" {
		return boundary, nil
	}
	for _, row := range rows.Releases {
		if row.SourceProjectId != string(projectID) {
			continue
		}
		boundary.found = true
		boundary.row = row
		boundary.owner = owners[domain.OperationID(row.OperationId)]
		return boundary, nil
	}
	return boundary, nil
}

// rejectProjectNetworkReleaseMutation freezes one project once teardown has a durable marker.
func rejectProjectNetworkReleaseMutation(
	boundary projectNetworkReleaseBoundary,
	projectID domain.ProjectID,
	action string,
) error {
	if !boundary.found {
		return nil
	}
	return &ProjectNetworkReleaseActiveError{
		ProjectID:   projectID,
		OperationID: domain.OperationID(boundary.row.OperationId),
		State:       ProjectNetworkReleaseState(boundary.row.State),
		Action:      action,
	}
}

// validateProjectNetworkReleaseTransition allows only the owner approval cycle after teardown starts.
func validateProjectNetworkReleaseTransition(
	boundary projectNetworkReleaseBoundary,
	operation domain.Operation,
	next domain.OperationState,
) error {
	if !boundary.found {
		return nil
	}
	ownerID := boundary.owner.operation.Operation.ID
	if operation.ID != ownerID {
		return corruptStateError(
			"network project release",
			string(operation.ProjectID),
			fmt.Errorf("active operation %q does not own release operation %q", operation.ID, ownerID),
		)
	}
	if operation.State == domain.OperationRunning && next == domain.OperationRequiresApproval {
		return nil
	}
	if operation.State == domain.OperationRequiresApproval && next == domain.OperationRunning {
		return nil
	}
	return rejectProjectNetworkReleaseMutation(boundary, operation.ProjectID, "transition operation")
}
