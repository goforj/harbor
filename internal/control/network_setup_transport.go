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
	// maximumNetworkSetupSelectionRequestBytes covers one maximally escaped domain identifier and a revision.
	maximumNetworkSetupSelectionRequestBytes = 2048
	// maximumNetworkSetupConfirmationEnvelopeBytes covers the escaped selection surrounding one helper-bounded evidence value.
	maximumNetworkSetupConfirmationEnvelopeBytes = 2048
	// maximumNetworkSetupConfirmationRequestBytes keeps helper evidence under its native bound plus its control envelope.
	maximumNetworkSetupConfirmationRequestBytes = helper.MaxResponseBytes + maximumNetworkSetupConfirmationEnvelopeBytes
)

// networkSetupResponse keeps setup initiation extensible around its durable operation.
type networkSetupResponse struct {
	Setup NetworkSetupOperation `json:"setup"`
}

// networkSetupApprovalPreparationResponse keeps helper launch metadata extensible around its reviewed result.
type networkSetupApprovalPreparationResponse struct {
	Preparation NetworkSetupApprovalPreparation `json:"preparation"`
}

// networkSetupApprovalConfirmationResponse keeps terminal setup state extensible around its reviewed result.
type networkSetupApprovalConfirmationResponse struct {
	Confirmation NetworkSetupApprovalConfirmation `json:"confirmation"`
}

// StartNetworkSetup starts or replays one client-stable machine setup intent.
func (client *Client) StartNetworkSetup(
	ctx context.Context,
	request StartNetworkSetupRequest,
) (NetworkSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkSetupOperation{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkSetupV1) {
		return NetworkSetupOperation{}, errors.New("Harbor daemon does not support network setup; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodNetworkSetupStart, request)
	if err != nil {
		return NetworkSetupOperation{}, err
	}
	var response networkSetupResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkSetupOperation{}, fmt.Errorf("decode network setup response: %w", err)
	}
	if err := response.Setup.Validate(); err != nil {
		return NetworkSetupOperation{}, fmt.Errorf("validate network setup response: %w", err)
	}
	if err := validateNetworkSetupCorrelation(request, response.Setup); err != nil {
		return NetworkSetupOperation{}, fmt.Errorf("validate network setup response: %w", err)
	}
	return response.Setup, nil
}

// PrepareNetworkSetupApproval requests one caller-bound helper capability for an exact setup revision.
func (client *Client) PrepareNetworkSetupApproval(
	ctx context.Context,
	request PrepareNetworkSetupApprovalRequest,
) (NetworkSetupApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return NetworkSetupApprovalPreparation{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkSetupV1) {
		return NetworkSetupApprovalPreparation{}, errors.New("Harbor daemon does not support network setup; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodNetworkSetupApprovalPrepare, request)
	if err != nil {
		return NetworkSetupApprovalPreparation{}, err
	}
	var response networkSetupApprovalPreparationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkSetupApprovalPreparation{}, fmt.Errorf("decode network setup approval preparation response: %w", err)
	}
	if err := response.Preparation.Validate(); err != nil {
		return NetworkSetupApprovalPreparation{}, fmt.Errorf("validate network setup approval preparation response: %w", err)
	}
	if err := validateNetworkSetupApprovalPreparationCorrelation(request, response.Preparation); err != nil {
		return NetworkSetupApprovalPreparation{}, fmt.Errorf("validate network setup approval preparation response: %w", err)
	}
	return response.Preparation, nil
}

// ConfirmNetworkSetupApproval submits independently observed helper evidence and finishes setup.
func (client *Client) ConfirmNetworkSetupApproval(
	ctx context.Context,
	request ConfirmNetworkSetupApprovalRequest,
) (NetworkSetupApprovalConfirmation, error) {
	if err := request.Validate(); err != nil {
		return NetworkSetupApprovalConfirmation{}, err
	}
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkSetupV1) {
		return NetworkSetupApprovalConfirmation{}, errors.New("Harbor daemon does not support network setup; upgrade or restart harbord")
	}
	payload, err := client.session.Call(ctx, methodNetworkSetupApprovalConfirm, request)
	if err != nil {
		return NetworkSetupApprovalConfirmation{}, err
	}
	var response networkSetupApprovalConfirmationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkSetupApprovalConfirmation{}, fmt.Errorf("decode network setup approval confirmation response: %w", err)
	}
	if err := response.Confirmation.Validate(); err != nil {
		return NetworkSetupApprovalConfirmation{}, fmt.Errorf("validate network setup approval confirmation response: %w", err)
	}
	if err := validateNetworkSetupApprovalConfirmationCorrelation(request, response.Confirmation); err != nil {
		return NetworkSetupApprovalConfirmation{}, fmt.Errorf("validate network setup approval confirmation response: %w", err)
	}
	return response.Confirmation, nil
}

// networkSetupStartHandler admits one bounded machine-global setup intent.
func (server *Server) networkSetupStartHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkSetupV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("network setup capability was not negotiated"),
			)
		}
		setupRequest, err := decodeStartNetworkSetupRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		setup, err := server.config.Authority.StartNetworkSetup(ctx, caller, setupRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := setup.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network setup: %w", err))
		}
		if err := validateNetworkSetupCorrelation(setupRequest, setup); err != nil {
			return nil, authorityError(fmt.Errorf("validate network setup: %w", err))
		}
		return networkSetupResponse{Setup: setup}, nil
	}
}

// networkSetupApprovalPrepareHandler admits one exact setup operation revision for helper authorization.
func (server *Server) networkSetupApprovalPrepareHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkSetupV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("network setup capability was not negotiated"),
			)
		}
		approvalRequest, err := decodePrepareNetworkSetupApprovalRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		preparation, err := server.config.Authority.PrepareNetworkSetupApproval(ctx, caller, approvalRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := preparation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network setup approval preparation: %w", err))
		}
		if err := validateNetworkSetupApprovalPreparationCorrelation(approvalRequest, preparation); err != nil {
			return nil, authorityError(fmt.Errorf("validate network setup approval preparation: %w", err))
		}
		return networkSetupApprovalPreparationResponse{Preparation: preparation}, nil
	}
}

// networkSetupApprovalConfirmHandler admits only canonical complete helper pool evidence for one setup revision.
func (server *Server) networkSetupApprovalConfirmHandler(transportPeer local.PeerIdentity) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(transportPeer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkSetupV1) {
			return nil, session.NewHandlerError(
				rpc.ErrorCodePermissionDenied,
				errors.New("network setup capability was not negotiated"),
			)
		}
		approvalRequest, err := decodeConfirmNetworkSetupApprovalRequest(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		confirmation, err := server.config.Authority.ConfirmNetworkSetupApproval(ctx, caller, approvalRequest)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := confirmation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network setup approval confirmation: %w", err))
		}
		if err := validateNetworkSetupApprovalConfirmationCorrelation(approvalRequest, confirmation); err != nil {
			return nil, authorityError(fmt.Errorf("validate network setup approval confirmation: %w", err))
		}
		return networkSetupApprovalConfirmationResponse{Confirmation: confirmation}, nil
	}
}

// validateNetworkSetupCorrelation binds daemon-selected setup progress to the client-owned intent.
func validateNetworkSetupCorrelation(request StartNetworkSetupRequest, setup NetworkSetupOperation) error {
	if setup.Operation.IntentID != request.IntentID {
		return errors.New("network setup does not match the requested intent")
	}
	return nil
}

// validateNetworkSetupApprovalPreparationCorrelation binds helper authority to the exact selected operation revision.
func validateNetworkSetupApprovalPreparationCorrelation(
	request PrepareNetworkSetupApprovalRequest,
	preparation NetworkSetupApprovalPreparation,
) error {
	if preparation.OperationID != request.OperationID ||
		preparation.OperationRevision != request.ExpectedOperationRevision {
		return errors.New("network setup approval preparation does not match the requested operation revision")
	}
	return nil
}

// validateNetworkSetupApprovalConfirmationCorrelation binds terminal setup state to the exact selected operation revision.
func validateNetworkSetupApprovalConfirmationCorrelation(
	request ConfirmNetworkSetupApprovalRequest,
	confirmation NetworkSetupApprovalConfirmation,
) error {
	if confirmation.Operation.ID != request.OperationID ||
		confirmation.NetworkRevision != request.ExpectedOperationRevision+2 ||
		confirmation.Revision != request.ExpectedOperationRevision+3 ||
		confirmation.Pool != request.PoolEvidence.Pool {
		return errors.New("network setup approval confirmation does not match the requested operation revision")
	}
	return nil
}

// decodeStartNetworkSetupRequest rejects authority beyond one client-stable setup intent.
func decodeStartNetworkSetupRequest(payload []byte) (StartNetworkSetupRequest, error) {
	decoder, err := newNetworkSetupRequestDecoder(payload, maximumNetworkSetupSelectionRequestBytes, "network setup start")
	if err != nil {
		return StartNetworkSetupRequest{}, err
	}
	var request StartNetworkSetupRequest
	intentSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return StartNetworkSetupRequest{}, fmt.Errorf("decode network setup start field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return StartNetworkSetupRequest{}, errors.New("network setup start field name must be a string")
		}
		if field != "intent_id" {
			return StartNetworkSetupRequest{}, fmt.Errorf("network setup start request contains unknown field %q", field)
		}
		if intentSeen {
			return StartNetworkSetupRequest{}, errors.New("network setup start request contains duplicate field \"intent_id\"")
		}
		if err := decoder.Decode(&request.IntentID); err != nil {
			return StartNetworkSetupRequest{}, fmt.Errorf("decode network setup start intent ID: %w", err)
		}
		intentSeen = true
	}
	if err := finishNetworkSetupRequestDecoder(decoder, "network setup start"); err != nil {
		return StartNetworkSetupRequest{}, err
	}
	if !intentSeen {
		return StartNetworkSetupRequest{}, errors.New("network setup start request requires intent_id")
	}
	if err := request.Validate(); err != nil {
		return StartNetworkSetupRequest{}, err
	}
	return request, nil
}

// decodePrepareNetworkSetupApprovalRequest rejects authority beyond one exact setup operation revision.
func decodePrepareNetworkSetupApprovalRequest(payload []byte) (PrepareNetworkSetupApprovalRequest, error) {
	decoder, err := newNetworkSetupRequestDecoder(payload, maximumNetworkSetupSelectionRequestBytes, "network setup approval preparation")
	if err != nil {
		return PrepareNetworkSetupApprovalRequest{}, err
	}
	var request PrepareNetworkSetupApprovalRequest
	operationSeen := false
	revisionSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return PrepareNetworkSetupApprovalRequest{}, fmt.Errorf("decode network setup approval preparation field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return PrepareNetworkSetupApprovalRequest{}, errors.New("network setup approval preparation field name must be a string")
		}
		switch field {
		case "operation_id":
			if operationSeen {
				return PrepareNetworkSetupApprovalRequest{}, fmt.Errorf("network setup approval preparation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.OperationID); err != nil {
				return PrepareNetworkSetupApprovalRequest{}, fmt.Errorf("decode network setup approval preparation operation ID: %w", err)
			}
			operationSeen = true
		case "expected_operation_revision":
			if revisionSeen {
				return PrepareNetworkSetupApprovalRequest{}, fmt.Errorf("network setup approval preparation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.ExpectedOperationRevision); err != nil {
				return PrepareNetworkSetupApprovalRequest{}, fmt.Errorf("decode network setup approval preparation revision: %w", err)
			}
			revisionSeen = true
		default:
			return PrepareNetworkSetupApprovalRequest{}, fmt.Errorf("network setup approval preparation request contains unknown field %q", field)
		}
	}
	if err := finishNetworkSetupRequestDecoder(decoder, "network setup approval preparation"); err != nil {
		return PrepareNetworkSetupApprovalRequest{}, err
	}
	if !operationSeen || !revisionSeen {
		return PrepareNetworkSetupApprovalRequest{}, errors.New("network setup approval preparation request requires operation_id and expected_operation_revision")
	}
	if err := request.Validate(); err != nil {
		return PrepareNetworkSetupApprovalRequest{}, err
	}
	return request, nil
}

// decodeConfirmNetworkSetupApprovalRequest rejects noncanonical helper evidence and any additional authority.
func decodeConfirmNetworkSetupApprovalRequest(payload []byte) (ConfirmNetworkSetupApprovalRequest, error) {
	decoder, err := newNetworkSetupRequestDecoder(payload, maximumNetworkSetupConfirmationRequestBytes, "network setup approval confirmation")
	if err != nil {
		return ConfirmNetworkSetupApprovalRequest{}, err
	}
	var request ConfirmNetworkSetupApprovalRequest
	var poolEvidenceJSON json.RawMessage
	operationSeen := false
	revisionSeen := false
	evidenceSeen := false
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("decode network setup approval confirmation field: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return ConfirmNetworkSetupApprovalRequest{}, errors.New("network setup approval confirmation field name must be a string")
		}
		switch field {
		case "operation_id":
			if operationSeen {
				return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("network setup approval confirmation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.OperationID); err != nil {
				return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("decode network setup approval confirmation operation ID: %w", err)
			}
			operationSeen = true
		case "expected_operation_revision":
			if revisionSeen {
				return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("network setup approval confirmation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&request.ExpectedOperationRevision); err != nil {
				return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("decode network setup approval confirmation revision: %w", err)
			}
			revisionSeen = true
		case "pool_evidence":
			if evidenceSeen {
				return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("network setup approval confirmation request contains duplicate field %q", field)
			}
			if err := decoder.Decode(&poolEvidenceJSON); err != nil {
				return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("decode network setup approval confirmation pool evidence: %w", err)
			}
			evidenceSeen = true
		default:
			return ConfirmNetworkSetupApprovalRequest{}, fmt.Errorf("network setup approval confirmation request contains unknown field %q", field)
		}
	}
	if err := finishNetworkSetupRequestDecoder(decoder, "network setup approval confirmation"); err != nil {
		return ConfirmNetworkSetupApprovalRequest{}, err
	}
	if !operationSeen || !revisionSeen || !evidenceSeen {
		return ConfirmNetworkSetupApprovalRequest{}, errors.New("network setup approval confirmation request requires operation_id, expected_operation_revision, and pool_evidence")
	}
	request.PoolEvidence, err = decodeNetworkSetupPoolEvidence(poolEvidenceJSON)
	if err != nil {
		return ConfirmNetworkSetupApprovalRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return ConfirmNetworkSetupApprovalRequest{}, err
	}
	return request, nil
}

// decodeNetworkSetupPoolEvidence reuses the helper protocol's exact nested evidence contract without accepting its envelope fields.
func decodeNetworkSetupPoolEvidence(body json.RawMessage) (helper.PoolMutationEvidence, error) {
	envelope := make([]byte, 0, len(body)+128)
	envelope = fmt.Appendf(
		envelope,
		`{"version":%d,"ok":true,"result":{"operation":"ensure_loopback_pool","pool_evidence":`,
		helper.ProtocolVersion,
	)
	envelope = append(envelope, body...)
	envelope = append(envelope, '}', '}')
	response, err := helper.DecodeResponse(bytes.NewReader(envelope))
	if err != nil {
		return helper.PoolMutationEvidence{}, fmt.Errorf("network setup approval confirmation pool evidence is invalid: %w", err)
	}
	if !response.OK || response.Error != nil || response.Result == nil ||
		response.Result.Operation != helper.OperationEnsureLoopbackPool || response.Result.PoolEvidence == nil {
		return helper.PoolMutationEvidence{}, errors.New("network setup approval confirmation pool evidence is not a successful pool ensure result")
	}
	return *response.Result.PoolEvidence, nil
}

// newNetworkSetupRequestDecoder opens one size-bounded JSON object without accepting another value kind.
func newNetworkSetupRequestDecoder(payload []byte, maximum int, name string) (*json.Decoder, error) {
	if len(payload) == 0 || len(payload) > maximum {
		return nil, fmt.Errorf("%s request exceeds its bounded object shape", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode %s request: %w", name, err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("%s request must be an object", name)
	}
	return decoder, nil
}

// finishNetworkSetupRequestDecoder requires the object terminator and no second JSON value.
func finishNetworkSetupRequestDecoder(decoder *json.Decoder, name string) error {
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode %s request end: %w", name, err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return fmt.Errorf("%s request object is not terminated", name)
	}
	return requireJSONEnd(decoder)
}
