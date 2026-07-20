package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// InspectProjectRuntimeRepair asks the daemon to derive one bounded repair inspection for a quarantined project.
func (client *Client) InspectProjectRuntimeRepair(
	ctx context.Context,
	request InspectProjectRuntimeRepairRequest,
) (ProjectRuntimeRepairInspection, error) {
	if err := request.Validate(); err != nil {
		return ProjectRuntimeRepairInspection{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityProjectRuntimeRepairV1) {
		return ProjectRuntimeRepairInspection{}, errors.New(
			"Harbor daemon does not support project runtime repair; upgrade or restart harbord",
		)
	}
	payload, err := client.session.Call(ctx, methodProjectRuntimeRepairInspect, request)
	if err != nil {
		return ProjectRuntimeRepairInspection{}, err
	}
	var response projectRuntimeRepairInspectionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("decode project runtime repair inspection response: %w", err)
	}
	if err := response.Inspection.Validate(); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("validate project runtime repair inspection response: %w", err)
	}
	if err := validateProjectRuntimeRepairInspectionCorrelation(request, response.Inspection); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("validate project runtime repair inspection response: %w", err)
	}
	return response.Inspection, nil
}

// ConfirmProjectRuntimeRepair commits one still-current daemon-owned repair plan selected by opaque identifiers.
func (client *Client) ConfirmProjectRuntimeRepair(
	ctx context.Context,
	request ConfirmProjectRuntimeRepairRequest,
) (ProjectRuntimeRepairConfirmation, error) {
	if err := request.Validate(); err != nil {
		return ProjectRuntimeRepairConfirmation{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityProjectRuntimeRepairV1) {
		return ProjectRuntimeRepairConfirmation{}, errors.New(
			"Harbor daemon does not support project runtime repair; upgrade or restart harbord",
		)
	}
	payload, err := client.session.Call(ctx, methodProjectRuntimeRepairConfirm, request)
	if err != nil {
		return ProjectRuntimeRepairConfirmation{}, err
	}
	var response projectRuntimeRepairConfirmationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return ProjectRuntimeRepairConfirmation{}, fmt.Errorf("decode project runtime repair confirmation response: %w", err)
	}
	if err := response.Confirmation.Validate(); err != nil {
		return ProjectRuntimeRepairConfirmation{}, fmt.Errorf("validate project runtime repair confirmation response: %w", err)
	}
	if err := validateProjectRuntimeRepairConfirmationCorrelation(request, response.Confirmation); err != nil {
		return ProjectRuntimeRepairConfirmation{}, fmt.Errorf("validate project runtime repair confirmation response: %w", err)
	}
	return response.Confirmation, nil
}
