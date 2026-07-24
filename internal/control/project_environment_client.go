package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ProjectEnvironment reads repository mappings and effective runtime inputs for one registered project.
func (client *Client) ProjectEnvironment(
	ctx context.Context,
	request ProjectEnvironmentRequest,
) (ProjectEnvironment, error) {
	if err := request.Validate(); err != nil {
		return ProjectEnvironment{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityProjectEnvironmentV1) {
		return ProjectEnvironment{}, errors.New("Harbor daemon does not support project environments; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodProjectEnvironment, request)
	if err != nil {
		return ProjectEnvironment{}, err
	}
	var response projectEnvironmentResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return ProjectEnvironment{}, fmt.Errorf("decode project environment response: %w", err)
	}
	if err := response.Environment.Validate(); err != nil {
		return ProjectEnvironment{}, fmt.Errorf("validate project environment response: %w", err)
	}
	if response.Environment.ProjectID != request.ProjectID {
		return ProjectEnvironment{}, errors.New("validate project environment response: project differs from its request")
	}
	return response.Environment, nil
}

// SaveProjectEnvironmentFile writes one revision-fenced repository or provider environment file.
func (client *Client) SaveProjectEnvironmentFile(
	ctx context.Context,
	request SaveProjectEnvironmentFileRequest,
) (ProjectEnvironmentFile, error) {
	if err := request.Validate(); err != nil {
		return ProjectEnvironmentFile{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityProjectEnvironmentV1) {
		return ProjectEnvironmentFile{}, errors.New("Harbor daemon does not support project environments; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodProjectEnvironmentFileSave, request)
	if err != nil {
		return ProjectEnvironmentFile{}, err
	}
	var response projectEnvironmentFileResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return ProjectEnvironmentFile{}, fmt.Errorf("decode saved project environment file response: %w", err)
	}
	if err := response.File.Validate(); err != nil {
		return ProjectEnvironmentFile{}, fmt.Errorf("validate saved project environment file response: %w", err)
	}
	if response.File.Name != request.Name {
		return ProjectEnvironmentFile{}, errors.New("validate saved project environment file response: filename differs from its request")
	}
	return response.File, nil
}
