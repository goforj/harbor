package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

const maximumServiceLogsRequestBytes = 4096

// serviceLogsHandler returns only validated current-session service output to a negotiated caller.
func (server *Server) serviceLogsHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityServiceLogsV1) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("service logs capability was not negotiated"))
		}
		selection, err := decodeServiceLogsRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		if selection.WaitMilliseconds > 0 && !containsCapability(caller.Session.Capabilities, CapabilityServiceLogsWaitV1) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("service logs wait capability was not negotiated"))
		}
		logs, err := server.config.Authority.ServiceLogs(ctx, caller, selection)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := logs.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate service logs: %w", err))
		}
		if err := validateServiceLogsCorrelation(selection, logs); err != nil {
			return nil, authorityError(fmt.Errorf("validate service logs: %w", err))
		}
		if err := validateServiceLogsResponseSize(logs); err != nil {
			return nil, authorityError(fmt.Errorf("validate service logs: %w", err))
		}
		return serviceLogsResponse{Logs: logs}, nil
	}
}

// decodeServiceLogsRequest rejects history selection and hidden fields before authority dispatch.
func decodeServiceLogsRequest(payload []byte) (ServiceLogsRequest, error) {
	if len(payload) == 0 || len(payload) > maximumServiceLogsRequestBytes {
		return ServiceLogsRequest{}, errors.New("service logs request exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return ServiceLogsRequest{}, fmt.Errorf("decode service logs request: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return ServiceLogsRequest{}, errors.New("service logs request must be an object")
	}

	var result ServiceLogsRequest
	seen := make(map[string]bool, 5)
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return ServiceLogsRequest{}, fmt.Errorf("decode service logs field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return ServiceLogsRequest{}, errors.New("service logs field name must be a string")
		}
		if seen[field] {
			return ServiceLogsRequest{}, fmt.Errorf("service logs request contains duplicate field %q", field)
		}
		seen[field] = true
		switch field {
		case "project_id":
			if err := decoder.Decode(&result.ProjectID); err != nil {
				return ServiceLogsRequest{}, fmt.Errorf("decode service logs project ID: %w", err)
			}
		case "session_id":
			if err := decoder.Decode(&result.SessionID); err != nil {
				return ServiceLogsRequest{}, fmt.Errorf("decode service logs session ID: %w", err)
			}
		case "service_id":
			if err := decoder.Decode(&result.ServiceID); err != nil {
				return ServiceLogsRequest{}, fmt.Errorf("decode service logs service ID: %w", err)
			}
		case "cursor":
			if err := decoder.Decode(&result.Cursor); err != nil {
				return ServiceLogsRequest{}, fmt.Errorf("decode service logs cursor: %w", err)
			}
		case "wait_milliseconds":
			if err := decoder.Decode(&result.WaitMilliseconds); err != nil {
				return ServiceLogsRequest{}, fmt.Errorf("decode service logs wait: %w", err)
			}
		default:
			return ServiceLogsRequest{}, fmt.Errorf("service logs request contains unknown field %q", field)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return ServiceLogsRequest{}, fmt.Errorf("decode service logs request end: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return ServiceLogsRequest{}, errors.New("service logs request object is not terminated")
	}
	if !seen["project_id"] || !seen["service_id"] || !seen["cursor"] {
		return ServiceLogsRequest{}, errors.New("service logs request requires project_id, service_id, and cursor")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return ServiceLogsRequest{}, err
	}
	if err := result.Validate(); err != nil {
		return ServiceLogsRequest{}, err
	}
	return result, nil
}
