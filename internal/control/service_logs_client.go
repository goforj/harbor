package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ServiceLogs reads one bounded chunk from a selected Compose service.
func (client *Client) ServiceLogs(ctx context.Context, request ServiceLogsRequest) (ServiceLogs, error) {
	if err := request.Validate(); err != nil {
		return ServiceLogs{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityServiceLogsV1) {
		return ServiceLogs{}, errors.New("Harbor daemon does not support service logs; upgrade or restart harbord")
	}
	if request.WaitMilliseconds > 0 && !containsCapability(client.peer.Session.Capabilities, CapabilityServiceLogsWaitV1) {
		return ServiceLogs{}, errors.New("Harbor daemon does not support live service logs; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodServiceLogs, request)
	if err != nil {
		return ServiceLogs{}, err
	}
	var response serviceLogsResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return ServiceLogs{}, fmt.Errorf("decode service logs response: %w", err)
	}
	if err := response.Logs.Validate(); err != nil {
		return ServiceLogs{}, fmt.Errorf("validate service logs response: %w", err)
	}
	if err := validateServiceLogsCorrelation(request, response.Logs); err != nil {
		return ServiceLogs{}, fmt.Errorf("validate service logs response: %w", err)
	}
	if err := validateServiceLogsResponseSize(response.Logs); err != nil {
		return ServiceLogs{}, fmt.Errorf("validate service logs response: %w", err)
	}
	return response.Logs, nil
}
