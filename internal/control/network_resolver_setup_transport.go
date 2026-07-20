package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

const (
	// maximumNetworkResolverSetupSelectionRequestBytes covers one maximally escaped domain identifier and a revision.
	maximumNetworkResolverSetupSelectionRequestBytes = 2048
	// maximumNetworkResolverSetupConfirmationEnvelopeBytes covers the escaped selection surrounding one helper-bounded evidence value.
	maximumNetworkResolverSetupConfirmationEnvelopeBytes = 2048
	// maximumNetworkResolverSetupConfirmationRequestBytes keeps helper evidence under its native bound plus its control envelope.
	maximumNetworkResolverSetupConfirmationRequestBytes = helper.MaxResponseBytes + maximumNetworkResolverSetupConfirmationEnvelopeBytes
)

// networkResolverSetupResponse keeps resolver setup initiation extensible around its durable operation.
type networkResolverSetupResponse struct {
	Setup NetworkResolverSetupOperation `json:"setup"`
}

// networkResolverSetupApprovalPreparationResponse keeps helper launch metadata extensible around its reviewed result.
type networkResolverSetupApprovalPreparationResponse struct {
	Preparation NetworkResolverSetupApprovalPreparation `json:"preparation"`
}

// networkResolverSetupApprovalConfirmationResponse keeps terminal resolver state extensible around its reviewed result.
type networkResolverSetupApprovalConfirmationResponse struct {
	Confirmation NetworkResolverSetupApprovalConfirmation `json:"confirmation"`
}

// StartNetworkResolverSetup starts or replays one client-stable machine resolver setup intent.
func (client *Client) StartNetworkResolverSetup(
	ctx context.Context,
	request StartNetworkResolverSetupRequest,
) (NetworkResolverSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkResolverSetupOperation{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkResolverSetupV1) {
		return NetworkResolverSetupOperation{}, errors.New("Harbor daemon does not support network resolver setup; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodNetworkResolverSetupStart, request)
	if err != nil {
		return NetworkResolverSetupOperation{}, err
	}
	var response networkResolverSetupResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkResolverSetupOperation{}, fmt.Errorf("decode network resolver setup response: %w", err)
	}
	if err := response.Setup.Validate(); err != nil {
		return NetworkResolverSetupOperation{}, fmt.Errorf("validate network resolver setup response: %w", err)
	}
	if err := validateNetworkResolverSetupCorrelation(request, response.Setup); err != nil {
		return NetworkResolverSetupOperation{}, fmt.Errorf("validate network resolver setup response: %w", err)
	}
	return response.Setup, nil
}

// PrepareNetworkResolverSetupApproval requests one caller-bound helper capability for an exact resolver setup revision.
func (client *Client) PrepareNetworkResolverSetupApproval(
	ctx context.Context,
	request PrepareNetworkResolverSetupApprovalRequest,
) (NetworkResolverSetupApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return NetworkResolverSetupApprovalPreparation{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkResolverSetupV1) {
		return NetworkResolverSetupApprovalPreparation{}, errors.New("Harbor daemon does not support network resolver setup; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodNetworkResolverSetupApprovalPrepare, request)
	if err != nil {
		return NetworkResolverSetupApprovalPreparation{}, err
	}
	var response networkResolverSetupApprovalPreparationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkResolverSetupApprovalPreparation{}, fmt.Errorf("decode network resolver setup approval preparation response: %w", err)
	}
	if err := response.Preparation.Validate(); err != nil {
		return NetworkResolverSetupApprovalPreparation{}, fmt.Errorf("validate network resolver setup approval preparation response: %w", err)
	}
	if err := validateNetworkResolverSetupApprovalPreparationCorrelation(request, response.Preparation); err != nil {
		return NetworkResolverSetupApprovalPreparation{}, fmt.Errorf("validate network resolver setup approval preparation response: %w", err)
	}
	return response.Preparation, nil
}

// ConfirmNetworkResolverSetupApproval submits independently observed helper evidence and finishes resolver setup.
func (client *Client) ConfirmNetworkResolverSetupApproval(
	ctx context.Context,
	request ConfirmNetworkResolverSetupApprovalRequest,
) (NetworkResolverSetupApprovalConfirmation, error) {
	if err := request.Validate(); err != nil {
		return NetworkResolverSetupApprovalConfirmation{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkResolverSetupV1) {
		return NetworkResolverSetupApprovalConfirmation{}, errors.New("Harbor daemon does not support network resolver setup; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodNetworkResolverSetupApprovalConfirm, request)
	if err != nil {
		return NetworkResolverSetupApprovalConfirmation{}, err
	}
	var response networkResolverSetupApprovalConfirmationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkResolverSetupApprovalConfirmation{}, fmt.Errorf("decode network resolver setup approval confirmation response: %w", err)
	}
	if err := response.Confirmation.Validate(); err != nil {
		return NetworkResolverSetupApprovalConfirmation{}, fmt.Errorf("validate network resolver setup approval confirmation response: %w", err)
	}
	if err := validateNetworkResolverSetupApprovalConfirmationCorrelation(request, response.Confirmation); err != nil {
		return NetworkResolverSetupApprovalConfirmation{}, fmt.Errorf("validate network resolver setup approval confirmation response: %w", err)
	}
	return response.Confirmation, nil
}

// networkResolverSetupStartHandler admits one bounded machine-global resolver setup intent.
func (server *Server) networkResolverSetupStartHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkResolverSetupV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("network resolver setup capability was not negotiated"),
			)
		}
		setupRequest, err := decodeStartNetworkResolverSetupRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		setup, err := server.config.Authority.StartNetworkResolverSetup(ctx, caller, setupRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := setup.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver setup: %w", err))
		}
		if err := validateNetworkResolverSetupCorrelation(setupRequest, setup); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver setup: %w", err))
		}
		return networkResolverSetupResponse{Setup: setup}, nil
	}
}

// networkResolverSetupApprovalPrepareHandler admits one exact resolver setup revision for helper authorization.
func (server *Server) networkResolverSetupApprovalPrepareHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkResolverSetupV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("network resolver setup capability was not negotiated"),
			)
		}
		approvalRequest, err := decodePrepareNetworkResolverSetupApprovalRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		preparation, err := server.config.Authority.PrepareNetworkResolverSetupApproval(ctx, caller, approvalRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := preparation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver setup approval preparation: %w", err))
		}
		if err := validateNetworkResolverSetupApprovalPreparationCorrelation(approvalRequest, preparation); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver setup approval preparation: %w", err))
		}
		return networkResolverSetupApprovalPreparationResponse{Preparation: preparation}, nil
	}
}

// networkResolverSetupApprovalConfirmHandler admits only canonical exact resolver evidence for one setup revision.
func (server *Server) networkResolverSetupApprovalConfirmHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkResolverSetupV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("network resolver setup capability was not negotiated"),
			)
		}
		approvalRequest, err := decodeConfirmNetworkResolverSetupApprovalRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		confirmation, err := server.config.Authority.ConfirmNetworkResolverSetupApproval(ctx, caller, approvalRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := confirmation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver setup approval confirmation: %w", err))
		}
		if err := validateNetworkResolverSetupApprovalConfirmationCorrelation(approvalRequest, confirmation); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver setup approval confirmation: %w", err))
		}
		return networkResolverSetupApprovalConfirmationResponse{Confirmation: confirmation}, nil
	}
}

// validateNetworkResolverSetupCorrelation binds daemon-selected resolver setup progress to the client-owned intent.
func validateNetworkResolverSetupCorrelation(
	request StartNetworkResolverSetupRequest,
	setup NetworkResolverSetupOperation,
) error {
	if setup.Operation.IntentID != request.IntentID {
		return errors.New("network resolver setup does not match the requested intent")
	}
	return nil
}

// validateNetworkResolverSetupApprovalPreparationCorrelation binds helper authority to the exact selected operation revision.
func validateNetworkResolverSetupApprovalPreparationCorrelation(
	request PrepareNetworkResolverSetupApprovalRequest,
	preparation NetworkResolverSetupApprovalPreparation,
) error {
	if preparation.OperationID != request.OperationID ||
		preparation.OperationRevision != request.ExpectedOperationRevision {
		return errors.New("network resolver setup approval preparation does not match the requested operation revision")
	}
	return nil
}

// validateNetworkResolverSetupApprovalConfirmationCorrelation binds terminal resolver state to the exact selected operation revision.
func validateNetworkResolverSetupApprovalConfirmationCorrelation(
	request ConfirmNetworkResolverSetupApprovalRequest,
	confirmation NetworkResolverSetupApprovalConfirmation,
) error {
	if confirmation.Operation.ID != request.OperationID ||
		confirmation.NetworkRevision <= request.ExpectedOperationRevision+1 ||
		confirmation.Revision != confirmation.NetworkRevision+1 {
		return errors.New("network resolver setup approval confirmation does not match the requested operation revision")
	}
	return nil
}

// decodeStartNetworkResolverSetupRequest rejects authority beyond one client-stable resolver setup intent.
func decodeStartNetworkResolverSetupRequest(payload []byte) (StartNetworkResolverSetupRequest, error) {
	decoder, err := newNetworkSetupRequestDecoder(
		payload,
		maximumNetworkResolverSetupSelectionRequestBytes,
		"network resolver setup start",
	)
	if err != nil {
		return StartNetworkResolverSetupRequest{}, err
	}
	var request StartNetworkResolverSetupRequest
	intentSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return StartNetworkResolverSetupRequest{}, fmt.Errorf("decode network resolver setup start field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return StartNetworkResolverSetupRequest{}, errors.New("network resolver setup start field name must be a string")
		}
		if field != "intent_id" {
			return StartNetworkResolverSetupRequest{}, fmt.Errorf("network resolver setup start request contains unknown field %q", field)
		}
		if intentSeen {
			return StartNetworkResolverSetupRequest{}, errors.New("network resolver setup start request contains duplicate field \"intent_id\"")
		}
		if err := decoder.Decode(&request.IntentID); err != nil {
			return StartNetworkResolverSetupRequest{}, fmt.Errorf("decode network resolver setup start intent ID: %w", err)
		}
		intentSeen = true
	}
	if err := finishNetworkSetupRequestDecoder(decoder, "network resolver setup start"); err != nil {
		return StartNetworkResolverSetupRequest{}, err
	}
	if !intentSeen {
		return StartNetworkResolverSetupRequest{}, errors.New("network resolver setup start request requires intent_id")
	}
	if err := request.Validate(); err != nil {
		return StartNetworkResolverSetupRequest{}, err
	}
	return request, nil
}

// decodePrepareNetworkResolverSetupApprovalRequest rejects authority beyond one exact resolver setup revision.
func decodePrepareNetworkResolverSetupApprovalRequest(payload []byte) (PrepareNetworkResolverSetupApprovalRequest, error) {
	decoder, err := newNetworkSetupRequestDecoder(
		payload,
		maximumNetworkResolverSetupSelectionRequestBytes,
		"network resolver setup approval preparation",
	)
	if err != nil {
		return PrepareNetworkResolverSetupApprovalRequest{}, err
	}
	var request PrepareNetworkResolverSetupApprovalRequest
	operationSeen := false
	revisionSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return PrepareNetworkResolverSetupApprovalRequest{}, fmt.Errorf("decode network resolver setup approval preparation field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return PrepareNetworkResolverSetupApprovalRequest{}, errors.New("network resolver setup approval preparation field name must be a string")
		}
		switch field {
		case "operation_id":
			if operationSeen {
				return PrepareNetworkResolverSetupApprovalRequest{}, fmt.Errorf("network resolver setup approval preparation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.OperationID); err != nil {
				return PrepareNetworkResolverSetupApprovalRequest{}, fmt.Errorf("decode network resolver setup approval preparation operation ID: %w", err)
			}
			operationSeen = true
		case "expected_operation_revision":
			if revisionSeen {
				return PrepareNetworkResolverSetupApprovalRequest{}, fmt.Errorf("network resolver setup approval preparation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.ExpectedOperationRevision); err != nil {
				return PrepareNetworkResolverSetupApprovalRequest{}, fmt.Errorf("decode network resolver setup approval preparation revision: %w", err)
			}
			revisionSeen = true
		default:
			return PrepareNetworkResolverSetupApprovalRequest{}, fmt.Errorf("network resolver setup approval preparation request contains unknown field %q", field)
		}
	}
	if err := finishNetworkSetupRequestDecoder(decoder, "network resolver setup approval preparation"); err != nil {
		return PrepareNetworkResolverSetupApprovalRequest{}, err
	}
	if !operationSeen || !revisionSeen {
		return PrepareNetworkResolverSetupApprovalRequest{}, errors.New("network resolver setup approval preparation request requires operation_id and expected_operation_revision")
	}
	if err := request.Validate(); err != nil {
		return PrepareNetworkResolverSetupApprovalRequest{}, err
	}
	return request, nil
}

// decodeConfirmNetworkResolverSetupApprovalRequest rejects noncanonical helper evidence and any additional authority.
func decodeConfirmNetworkResolverSetupApprovalRequest(payload []byte) (ConfirmNetworkResolverSetupApprovalRequest, error) {
	decoder, err := newNetworkSetupRequestDecoder(
		payload,
		maximumNetworkResolverSetupConfirmationRequestBytes,
		"network resolver setup approval confirmation",
	)
	if err != nil {
		return ConfirmNetworkResolverSetupApprovalRequest{}, err
	}
	var request ConfirmNetworkResolverSetupApprovalRequest
	var resolverEvidenceJSON json.RawMessage
	operationSeen := false
	revisionSeen := false
	evidenceSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("decode network resolver setup approval confirmation field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return ConfirmNetworkResolverSetupApprovalRequest{}, errors.New("network resolver setup approval confirmation field name must be a string")
		}
		switch field {
		case "operation_id":
			if operationSeen {
				return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("network resolver setup approval confirmation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.OperationID); err != nil {
				return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("decode network resolver setup approval confirmation operation ID: %w", err)
			}
			operationSeen = true
		case "expected_operation_revision":
			if revisionSeen {
				return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("network resolver setup approval confirmation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.ExpectedOperationRevision); err != nil {
				return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("decode network resolver setup approval confirmation revision: %w", err)
			}
			revisionSeen = true
		case "resolver_evidence":
			if evidenceSeen {
				return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("network resolver setup approval confirmation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&resolverEvidenceJSON); err != nil {
				return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("decode network resolver setup approval confirmation resolver evidence: %w", err)
			}
			evidenceSeen = true
		default:
			return ConfirmNetworkResolverSetupApprovalRequest{}, fmt.Errorf("network resolver setup approval confirmation request contains unknown field %q", field)
		}
	}
	if err := finishNetworkSetupRequestDecoder(decoder, "network resolver setup approval confirmation"); err != nil {
		return ConfirmNetworkResolverSetupApprovalRequest{}, err
	}
	if !operationSeen || !revisionSeen || !evidenceSeen {
		return ConfirmNetworkResolverSetupApprovalRequest{}, errors.New("network resolver setup approval confirmation request requires operation_id, expected_operation_revision, and resolver_evidence")
	}
	request.ResolverEvidence, err = decodeNetworkResolverSetupEvidence(resolverEvidenceJSON)
	if err != nil {
		return ConfirmNetworkResolverSetupApprovalRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return ConfirmNetworkResolverSetupApprovalRequest{}, err
	}
	return request, nil
}

// decodeNetworkResolverSetupEvidence reuses the helper protocol's exact nested evidence contract without accepting its envelope fields.
func decodeNetworkResolverSetupEvidence(body json.RawMessage) (helper.ResolverMutationEvidence, error) {
	envelope := make([]byte, 0, len(body)+128)
	envelope = fmt.Appendf(
		envelope,
		`{"version":%d,"ok":true,"result":{"operation":"ensure_resolver","resolver_evidence":`,
		helper.ProtocolVersion,
	)
	envelope = append(envelope, body...)
	envelope = append(envelope, '}', '}')
	response, err := helper.DecodeResponse(bytes.NewReader(envelope))
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("network resolver setup approval confirmation resolver evidence is invalid: %w", err)
	}
	if !response.OK || response.Error != nil || response.Result == nil ||
		response.Result.Operation != helper.OperationEnsureResolver || response.Result.ResolverEvidence == nil {
		return helper.ResolverMutationEvidence{}, errors.New("network resolver setup approval confirmation resolver evidence is not a successful resolver ensure result")
	}
	return *response.Result.ResolverEvidence, nil
}
