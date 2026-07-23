package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

const (
	maximumProjectEnvironmentRequestBytes     = 2048
	maximumProjectEnvironmentSaveRequestBytes = maximumProjectEnvironmentFileBytes*6 + 4096
)

// projectEnvironmentAuthorityIsNil rejects typed-nil optional implementations before capability negotiation.
func projectEnvironmentAuthorityIsNil(authority ProjectEnvironmentAuthority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// projectEnvironmentHandler returns the provider environment view for one registered project.
func (server *Server) projectEnvironmentHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectEnvironmentV1) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("project environment capability was not negotiated"))
		}
		selection, err := decodeProjectEnvironmentRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		environment, err := server.config.ProjectEnvironmentAuthority.ProjectEnvironment(ctx, caller, selection)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := environment.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project environment: %w", err))
		}
		if environment.ProjectID != selection.ProjectID {
			return nil, authorityError(errors.New("project environment result differs from its requested project"))
		}
		return projectEnvironmentResponse{Environment: environment}, nil
	}
}

// saveProjectEnvironmentFileHandler publishes one revision-fenced provider environment file edit.
func (server *Server) saveProjectEnvironmentFileHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectEnvironmentV1) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("project environment capability was not negotiated"))
		}
		save, err := decodeSaveProjectEnvironmentFileRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		file, err := server.config.ProjectEnvironmentAuthority.SaveProjectEnvironmentFile(ctx, caller, save)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := file.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate saved project environment file: %w", err))
		}
		if file.Name != save.Name {
			return nil, authorityError(errors.New("saved project environment file differs from its request"))
		}
		return projectEnvironmentFileResponse{File: file}, nil
	}
}

// decodeProjectEnvironmentRequest rejects hidden selectors beyond one project identity.
func decodeProjectEnvironmentRequest(payload []byte) (ProjectEnvironmentRequest, error) {
	fields, err := decodeProjectEnvironmentFields(
		payload,
		maximumProjectEnvironmentRequestBytes,
		"project environment request",
		"project_id",
	)
	if err != nil {
		return ProjectEnvironmentRequest{}, err
	}
	var request ProjectEnvironmentRequest
	if err := json.Unmarshal(fields["project_id"], &request.ProjectID); err != nil {
		return ProjectEnvironmentRequest{}, fmt.Errorf("decode project environment project ID: %w", err)
	}
	if err := request.Validate(); err != nil {
		return ProjectEnvironmentRequest{}, err
	}
	return request, nil
}

// decodeSaveProjectEnvironmentFileRequest rejects paths and hidden write authority before provider dispatch.
func decodeSaveProjectEnvironmentFileRequest(payload []byte) (SaveProjectEnvironmentFileRequest, error) {
	fields, err := decodeProjectEnvironmentFields(
		payload,
		maximumProjectEnvironmentSaveRequestBytes,
		"project environment save request",
		"project_id",
		"name",
		"contents",
		"revision",
	)
	if err != nil {
		return SaveProjectEnvironmentFileRequest{}, err
	}
	var request SaveProjectEnvironmentFileRequest
	if err := json.Unmarshal(fields["project_id"], &request.ProjectID); err != nil {
		return SaveProjectEnvironmentFileRequest{}, fmt.Errorf("decode project environment save project ID: %w", err)
	}
	if err := json.Unmarshal(fields["name"], &request.Name); err != nil {
		return SaveProjectEnvironmentFileRequest{}, fmt.Errorf("decode project environment save filename: %w", err)
	}
	if err := json.Unmarshal(fields["contents"], &request.Contents); err != nil {
		return SaveProjectEnvironmentFileRequest{}, fmt.Errorf("decode project environment save contents: %w", err)
	}
	if err := json.Unmarshal(fields["revision"], &request.Revision); err != nil {
		return SaveProjectEnvironmentFileRequest{}, fmt.Errorf("decode project environment save revision: %w", err)
	}
	if err := request.Validate(); err != nil {
		return SaveProjectEnvironmentFileRequest{}, err
	}
	return request, nil
}

// decodeProjectEnvironmentFields reads one exact flat object while rejecting duplicate, missing, unknown, and trailing fields.
func decodeProjectEnvironmentFields(
	payload []byte,
	maximumBytes int,
	name string,
	allowed ...string,
) (map[string]json.RawMessage, error) {
	if len(payload) == 0 || len(payload) > maximumBytes {
		return nil, fmt.Errorf("%s exceeds its bounded object shape", name)
	}
	allowedFields := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedFields[field] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode %s field: %w", name, err)
		}
		field, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%s field name must be a string", name)
		}
		if _, permitted := allowedFields[field]; !permitted {
			return nil, fmt.Errorf("%s contains unknown field %q", name, field)
		}
		if _, duplicate := fields[field]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate field %q", name, field)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decode %s field %q: %w", name, field, err)
		}
		fields[field] = raw
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s end: %w", name, err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("%s object is not terminated", name)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return nil, err
	}
	for _, field := range allowed {
		if _, present := fields[field]; !present {
			return nil, fmt.Errorf("%s requires %q", name, field)
		}
	}
	return fields, nil
}
