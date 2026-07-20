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

const maximumProjectActivityRequestBytes = 4096

// projectActivityHandler returns only validated current-session activity to a negotiated caller.
func (server *Server) projectActivityHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectActivityV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project activity capability was not negotiated"),
			)
		}
		activityRequest, err := decodeProjectActivityRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		activity, err := server.config.Authority.ProjectActivity(ctx, caller, activityRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := activity.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project activity: %w", err))
		}
		if err := validateProjectActivityCorrelation(activityRequest, activity); err != nil {
			return nil, authorityError(fmt.Errorf("validate project activity: %w", err))
		}
		if err := validateProjectActivityResponseSize(activity); err != nil {
			return nil, authorityError(fmt.Errorf("validate project activity: %w", err))
		}
		return projectActivityResponse{Activity: activity}, nil
	}
}

// decodeProjectActivityRequest rejects history selection and hidden fields before authority dispatch.
func decodeProjectActivityRequest(payload []byte) (ProjectActivityRequest, error) {
	if len(payload) == 0 || len(payload) > maximumProjectActivityRequestBytes {
		return ProjectActivityRequest{}, errors.New("project activity request exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return ProjectActivityRequest{}, fmt.Errorf("decode project activity request: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return ProjectActivityRequest{}, errors.New("project activity request must be an object")
	}

	var result ProjectActivityRequest
	var projectSeen bool
	var sessionSeen bool
	var cursorSeen bool
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return ProjectActivityRequest{}, fmt.Errorf("decode project activity field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return ProjectActivityRequest{}, errors.New("project activity field name must be a string")
		}
		switch field {
		case "project_id":
			if projectSeen {
				return ProjectActivityRequest{}, errors.New("project activity request contains duplicate field \"project_id\"")
			}
			if err := decoder.Decode(&result.ProjectID); err != nil {
				return ProjectActivityRequest{}, fmt.Errorf("decode project activity project ID: %w", err)
			}
			projectSeen = true
		case "session_id":
			if sessionSeen {
				return ProjectActivityRequest{}, errors.New("project activity request contains duplicate field \"session_id\"")
			}
			if err := decoder.Decode(&result.SessionID); err != nil {
				return ProjectActivityRequest{}, fmt.Errorf("decode project activity session ID: %w", err)
			}
			sessionSeen = true
		case "cursor":
			if cursorSeen {
				return ProjectActivityRequest{}, errors.New("project activity request contains duplicate field \"cursor\"")
			}
			if err := decoder.Decode(&result.Cursor); err != nil {
				return ProjectActivityRequest{}, fmt.Errorf("decode project activity cursor: %w", err)
			}
			cursorSeen = true
		default:
			return ProjectActivityRequest{}, fmt.Errorf("project activity request contains unknown field %q", field)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return ProjectActivityRequest{}, fmt.Errorf("decode project activity request end: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return ProjectActivityRequest{}, errors.New("project activity request object is not terminated")
	}
	if !projectSeen || !cursorSeen {
		return ProjectActivityRequest{}, errors.New("project activity request requires project_id and cursor")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return ProjectActivityRequest{}, err
	}
	if err := result.Validate(); err != nil {
		return ProjectActivityRequest{}, err
	}
	return result, nil
}
