package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ProjectActivity reads one bounded chunk from a project's current durable session.
func (client *Client) ProjectActivity(ctx context.Context, request ProjectActivityRequest) (ProjectActivity, error) {
	if err := request.Validate(); err != nil {
		return ProjectActivity{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityProjectActivityV1) {
		return ProjectActivity{}, errors.New("Harbor daemon does not support project activity; upgrade or restart harbord")
	}
	if request.WaitMilliseconds > 0 && !containsCapability(client.peer.Session.Capabilities, CapabilityProjectActivityWaitV1) {
		return ProjectActivity{}, errors.New("Harbor daemon does not support live project activity; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodProjectActivity, request)
	if err != nil {
		return ProjectActivity{}, err
	}
	var response projectActivityResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return ProjectActivity{}, fmt.Errorf("decode project activity response: %w", err)
	}
	if err := response.Activity.Validate(); err != nil {
		return ProjectActivity{}, fmt.Errorf("validate project activity response: %w", err)
	}
	if err := validateProjectActivityCorrelation(request, response.Activity); err != nil {
		return ProjectActivity{}, fmt.Errorf("validate project activity response: %w", err)
	}
	if err := validateProjectActivityResponseSize(response.Activity); err != nil {
		return ProjectActivity{}, fmt.Errorf("validate project activity response: %w", err)
	}
	return response.Activity, nil
}
