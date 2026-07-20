package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// projectRuntimeRepairInspectHandler admits one authenticated project selection for daemon-owned inspection.
func (server *Server) projectRuntimeRepairInspectHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectRuntimeRepairV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project runtime repair capability was not negotiated"),
			)
		}
		inspectionRequest, err := decodeInspectProjectRuntimeRepairRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		inspection, err := server.config.Authority.InspectProjectRuntimeRepair(ctx, caller, inspectionRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := inspection.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project runtime repair inspection: %w", err))
		}
		if err := validateProjectRuntimeRepairInspectionCorrelation(inspectionRequest, inspection); err != nil {
			return nil, authorityError(fmt.Errorf("validate project runtime repair inspection: %w", err))
		}
		return projectRuntimeRepairInspectionResponse{Inspection: inspection}, nil
	}
}

// projectRuntimeRepairConfirmHandler admits only one authenticated opaque selection for immediate revalidation.
func (server *Server) projectRuntimeRepairConfirmHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityProjectRuntimeRepairV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("project runtime repair capability was not negotiated"),
			)
		}
		confirmationRequest, err := decodeConfirmProjectRuntimeRepairRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		confirmation, err := server.config.Authority.ConfirmProjectRuntimeRepair(ctx, caller, confirmationRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := confirmation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate project runtime repair confirmation: %w", err))
		}
		if err := validateProjectRuntimeRepairConfirmationCorrelation(confirmationRequest, confirmation); err != nil {
			return nil, authorityError(fmt.Errorf("validate project runtime repair confirmation: %w", err))
		}
		return projectRuntimeRepairConfirmationResponse{Confirmation: confirmation}, nil
	}
}
