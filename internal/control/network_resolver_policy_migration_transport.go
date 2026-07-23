package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

const maximumNetworkResolverPolicyMigrationConfirmationRequestBytes = helper.MaxResponseBytes + maximumNetworkReleaseRequestBytes

// ErrNetworkResolverPolicyMigrationUnsupported reports that the selected daemon session did not negotiate legacy resolver-policy retirement.
var ErrNetworkResolverPolicyMigrationUnsupported = errors.New("Harbor daemon does not support network resolver policy migration")

// networkResolverPolicyMigrationAuthorityIsNil rejects typed-nil optional implementations before capability negotiation.
func networkResolverPolicyMigrationAuthorityIsNil(authority NetworkResolverPolicyMigrationAuthority) bool {
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

type networkResolverPolicyMigrationResponse struct {
	Migration NetworkResolverPolicyMigrationOperation `json:"migration"`
}

type networkResolverPolicyMigrationApprovalPreparationResponse struct {
	Preparation NetworkResolverPolicyMigrationApprovalPreparation `json:"preparation"`
}

type networkResolverPolicyMigrationApprovalConfirmationResponse struct {
	Confirmation NetworkResolverPolicyMigrationApprovalConfirmation `json:"confirmation"`
}

// StartNetworkResolverPolicyMigration starts or replays one legacy resolver-policy retirement intent.
func (client *Client) StartNetworkResolverPolicyMigration(ctx context.Context, request StartNetworkResolverPolicyMigrationRequest) (NetworkResolverPolicyMigrationOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkResolverPolicyMigrationOperation{}, err
	}
	payload, err := client.networkResolverPolicyMigrationCall(ctx, methodNetworkResolverPolicyMigrationStart, request)
	if err != nil {
		return NetworkResolverPolicyMigrationOperation{}, err
	}
	var response networkResolverPolicyMigrationResponse
	if err := decodeNetworkResolverPolicyMigrationResponse(payload, &response); err != nil {
		return NetworkResolverPolicyMigrationOperation{}, err
	}
	if response.Migration.Operation.IntentID != request.IntentID {
		return NetworkResolverPolicyMigrationOperation{}, errors.New("network resolver policy migration does not match the requested intent")
	}
	return response.Migration, nil
}

// PrepareNetworkResolverPolicyMigrationApproval requests one caller-bound retirement capability.
func (client *Client) PrepareNetworkResolverPolicyMigrationApproval(ctx context.Context, request PrepareNetworkResolverPolicyMigrationApprovalRequest) (NetworkResolverPolicyMigrationApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return NetworkResolverPolicyMigrationApprovalPreparation{}, err
	}
	payload, err := client.networkResolverPolicyMigrationCall(ctx, methodNetworkResolverPolicyMigrationApprovalPrepare, request)
	if err != nil {
		return NetworkResolverPolicyMigrationApprovalPreparation{}, err
	}
	var response networkResolverPolicyMigrationApprovalPreparationResponse
	if err := decodeNetworkResolverPolicyMigrationPreparationResponse(payload, &response); err != nil {
		return NetworkResolverPolicyMigrationApprovalPreparation{}, err
	}
	if response.Preparation.OperationID != request.OperationID || response.Preparation.OperationRevision != request.ExpectedOperationRevision {
		return NetworkResolverPolicyMigrationApprovalPreparation{}, errors.New("network resolver policy migration preparation does not match the requested operation revision")
	}
	return response.Preparation, nil
}

// ConfirmNetworkResolverPolicyMigrationApproval submits owned-absent retirement evidence and completes migration.
func (client *Client) ConfirmNetworkResolverPolicyMigrationApproval(ctx context.Context, request ConfirmNetworkResolverPolicyMigrationApprovalRequest) (NetworkResolverPolicyMigrationApprovalConfirmation, error) {
	if err := request.Validate(); err != nil {
		return NetworkResolverPolicyMigrationApprovalConfirmation{}, err
	}
	payload, err := client.networkResolverPolicyMigrationCall(ctx, methodNetworkResolverPolicyMigrationApprovalConfirm, request)
	if err != nil {
		return NetworkResolverPolicyMigrationApprovalConfirmation{}, err
	}
	var response networkResolverPolicyMigrationApprovalConfirmationResponse
	if err := decodeNetworkResolverPolicyMigrationConfirmationResponse(payload, &response); err != nil {
		return NetworkResolverPolicyMigrationApprovalConfirmation{}, err
	}
	if !networkResolverPolicyMigrationConfirmationMatchesSelection(
		response.Confirmation,
		request.OperationID,
		request.ExpectedOperationRevision,
	) {
		return NetworkResolverPolicyMigrationApprovalConfirmation{}, errors.New("network resolver policy migration confirmation does not match the requested operation revision")
	}
	return response.Confirmation, nil
}

// networkResolverPolicyMigrationCall checks the dedicated capability before sending authority-bearing requests.
func (client *Client) networkResolverPolicyMigrationCall(ctx context.Context, method string, request any) ([]byte, error) {
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkResolverPolicyMigrationV1) {
		return nil, fmt.Errorf("%w; upgrade or restart harbord", ErrNetworkResolverPolicyMigrationUnsupported)
	}
	return client.session.Call(ctx, method, request)
}

// decodeNetworkResolverPolicyMigrationResponse strictly decodes the one reviewed operation response field.
func decodeNetworkResolverPolicyMigrationResponse(payload []byte, response *networkResolverPolicyMigrationResponse) error {
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseResponseBytes, "network resolver policy migration response", "migration")
	if err != nil {
		return err
	}
	if err := decodeStrictNetworkResolverPolicyMigrationValue(fields["migration"], &response.Migration); err != nil {
		return fmt.Errorf("decode network resolver policy migration response: %w", err)
	}
	if err := response.Migration.Validate(); err != nil {
		return fmt.Errorf("validate network resolver policy migration response: %w", err)
	}
	return nil
}

// decodeNetworkResolverPolicyMigrationPreparationResponse strictly decodes nested ticket publication metadata.
func decodeNetworkResolverPolicyMigrationPreparationResponse(payload []byte, response *networkResolverPolicyMigrationApprovalPreparationResponse) error {
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseResponseBytes, "network resolver policy migration preparation response", "preparation")
	if err != nil {
		return err
	}
	if err := decodeStrictNetworkResolverPolicyMigrationValue(fields["preparation"], &response.Preparation); err != nil {
		return fmt.Errorf("decode network resolver policy migration preparation response: %w", err)
	}
	if err := response.Preparation.Validate(); err != nil {
		return fmt.Errorf("validate network resolver policy migration preparation response: %w", err)
	}
	return nil
}

// decodeNetworkResolverPolicyMigrationConfirmationResponse strictly decodes the terminal migration result.
func decodeNetworkResolverPolicyMigrationConfirmationResponse(payload []byte, response *networkResolverPolicyMigrationApprovalConfirmationResponse) error {
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseResponseBytes, "network resolver policy migration confirmation response", "confirmation")
	if err != nil {
		return err
	}
	if err := decodeStrictNetworkResolverPolicyMigrationValue(fields["confirmation"], &response.Confirmation); err != nil {
		return fmt.Errorf("decode network resolver policy migration confirmation response: %w", err)
	}
	if err := response.Confirmation.Validate(); err != nil {
		return fmt.Errorf("validate network resolver policy migration confirmation response: %w", err)
	}
	return nil
}

// decodeStrictNetworkResolverPolicyMigrationValue rejects unknown and duplicate fields at every object depth.
func decodeStrictNetworkResolverPolicyMigrationValue(payload []byte, target any) error {
	if err := rejectDuplicateNetworkReleaseFields(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEnd(decoder)
}

// networkResolverPolicyMigrationStartHandler admits one bounded global legacy resolver-policy retirement intent.
func (server *Server) networkResolverPolicyMigrationStartHandler(peer local.PeerIdentity) session.Handler {
	return networkResolverPolicyMigrationHandler(server, peer, decodeStartNetworkResolverPolicyMigrationRequest, func(ctx context.Context, caller Caller, request StartNetworkResolverPolicyMigrationRequest) (any, error) {
		migration, err := server.config.NetworkResolverPolicyMigrationAuthority.StartNetworkResolverPolicyMigration(ctx, caller, request)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := migration.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver policy migration: %w", err))
		}
		if migration.Operation.IntentID != request.IntentID {
			return nil, authorityError(errors.New("network resolver policy migration does not match the requested intent"))
		}
		return networkResolverPolicyMigrationResponse{Migration: migration}, nil
	})
}

// networkResolverPolicyMigrationApprovalPrepareHandler admits one exact migration revision for helper authorization.
func (server *Server) networkResolverPolicyMigrationApprovalPrepareHandler(peer local.PeerIdentity) session.Handler {
	return networkResolverPolicyMigrationHandler(server, peer, decodePrepareNetworkResolverPolicyMigrationApprovalRequest, func(ctx context.Context, caller Caller, request PrepareNetworkResolverPolicyMigrationApprovalRequest) (any, error) {
		preparation, err := server.config.NetworkResolverPolicyMigrationAuthority.PrepareNetworkResolverPolicyMigrationApproval(ctx, caller, request)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := preparation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver policy migration preparation: %w", err))
		}
		if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
			return nil, authorityError(errors.New("network resolver policy migration preparation does not match the requested operation revision"))
		}
		return networkResolverPolicyMigrationApprovalPreparationResponse{Preparation: preparation}, nil
	})
}

// networkResolverPolicyMigrationApprovalConfirmHandler admits only canonical owned-absent retirement evidence.
func (server *Server) networkResolverPolicyMigrationApprovalConfirmHandler(peer local.PeerIdentity) session.Handler {
	return networkResolverPolicyMigrationHandler(server, peer, decodeConfirmNetworkResolverPolicyMigrationApprovalRequest, func(ctx context.Context, caller Caller, request ConfirmNetworkResolverPolicyMigrationApprovalRequest) (any, error) {
		confirmation, err := server.config.NetworkResolverPolicyMigrationAuthority.ConfirmNetworkResolverPolicyMigrationApproval(ctx, caller, request)
		if err != nil {
			return nil, authorityError(err)
		}
		if err := confirmation.Validate(); err != nil {
			return nil, authorityError(fmt.Errorf("validate network resolver policy migration confirmation: %w", err))
		}
		if !networkResolverPolicyMigrationConfirmationMatchesSelection(
			confirmation,
			request.OperationID,
			request.ExpectedOperationRevision,
		) {
			return nil, authorityError(errors.New("network resolver policy migration confirmation does not match the requested operation revision"))
		}
		return networkResolverPolicyMigrationApprovalConfirmationResponse{Confirmation: confirmation}, nil
	})
}

// networkResolverPolicyMigrationConfirmationMatchesSelection keeps the three fixed completion writes contiguous without overflowing a sequence.
func networkResolverPolicyMigrationConfirmationMatchesSelection(
	confirmation NetworkResolverPolicyMigrationApprovalConfirmation,
	operationID domain.OperationID,
	approvalRevision domain.Sequence,
) bool {
	return confirmation.Operation.ID == operationID &&
		confirmation.NetworkRevision >= approvalRevision &&
		confirmation.NetworkRevision-approvalRevision == 2 &&
		confirmation.Revision >= confirmation.NetworkRevision &&
		confirmation.Revision-confirmation.NetworkRevision == 1
}

// networkResolverPolicyMigrationHandler establishes the caller and blocks every unnegotiated migration method.
func networkResolverPolicyMigrationHandler[T any](server *Server, peer local.PeerIdentity, decode func([]byte) (T, error), call func(context.Context, Caller, T) (any, error)) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(peer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkResolverPolicyMigrationV1) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("network resolver policy migration capability was not negotiated"))
		}
		decoded, err := decode(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		return call(ctx, caller, decoded)
	}
}

// decodeStartNetworkResolverPolicyMigrationRequest rejects fields beyond the idempotent migration intent.
func decodeStartNetworkResolverPolicyMigrationRequest(payload []byte) (StartNetworkResolverPolicyMigrationRequest, error) {
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseRequestBytes, "network resolver policy migration start", "intent_id")
	if err != nil {
		return StartNetworkResolverPolicyMigrationRequest{}, err
	}
	var request StartNetworkResolverPolicyMigrationRequest
	if err := json.Unmarshal(fields["intent_id"], &request.IntentID); err != nil {
		return request, fmt.Errorf("decode network resolver policy migration intent ID: %w", err)
	}
	return request, request.Validate()
}

// decodePrepareNetworkResolverPolicyMigrationApprovalRequest rejects authority beyond one migration approval revision.
func decodePrepareNetworkResolverPolicyMigrationApprovalRequest(payload []byte) (PrepareNetworkResolverPolicyMigrationApprovalRequest, error) {
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseRequestBytes, "network resolver policy migration preparation", "operation_id", "expected_operation_revision")
	if err != nil {
		return PrepareNetworkResolverPolicyMigrationApprovalRequest{}, err
	}
	var request PrepareNetworkResolverPolicyMigrationApprovalRequest
	if err := decodeNetworkResolverPolicyMigrationSelectionFields(fields, &request.OperationID, &request.ExpectedOperationRevision); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeConfirmNetworkResolverPolicyMigrationApprovalRequest rejects noncanonical evidence and extra authority.
func decodeConfirmNetworkResolverPolicyMigrationApprovalRequest(payload []byte) (ConfirmNetworkResolverPolicyMigrationApprovalRequest, error) {
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkResolverPolicyMigrationConfirmationRequestBytes, "network resolver policy migration confirmation", "operation_id", "expected_operation_revision", "resolver_evidence")
	if err != nil {
		return ConfirmNetworkResolverPolicyMigrationApprovalRequest{}, err
	}
	var request ConfirmNetworkResolverPolicyMigrationApprovalRequest
	if err := decodeNetworkResolverPolicyMigrationSelectionFields(fields, &request.OperationID, &request.ExpectedOperationRevision); err != nil {
		return request, err
	}
	request.ResolverEvidence, err = decodeNetworkResolverPolicyMigrationEvidence(fields["resolver_evidence"])
	if err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeNetworkResolverPolicyMigrationSelectionFields decodes the shared operation and optimistic revision selection.
func decodeNetworkResolverPolicyMigrationSelectionFields(fields map[string]json.RawMessage, operationID *domain.OperationID, revision *domain.Sequence) error {
	if err := json.Unmarshal(fields["operation_id"], operationID); err != nil {
		return fmt.Errorf("decode network resolver policy migration operation ID: %w", err)
	}
	if err := json.Unmarshal(fields["expected_operation_revision"], revision); err != nil {
		return fmt.Errorf("decode network resolver policy migration operation revision: %w", err)
	}
	return nil
}

// decodeNetworkResolverPolicyMigrationEvidence reuses the helper's exact nested evidence contract for retirement.
func decodeNetworkResolverPolicyMigrationEvidence(body json.RawMessage) (helper.ResolverMutationEvidence, error) {
	envelope := make([]byte, 0, len(body)+128)
	envelope = fmt.Appendf(envelope, `{"version":%d,"ok":true,"result":{"operation":"retire_resolver","resolver_evidence":`, helper.ProtocolVersion)
	envelope = append(envelope, body...)
	envelope = append(envelope, '}', '}')
	response, err := helper.DecodeResponse(bytes.NewReader(envelope))
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("network resolver policy migration evidence is invalid: %w", err)
	}
	if !response.OK || response.Error != nil || response.Result == nil || response.Result.Operation != helper.OperationRetireResolver || response.Result.ResolverEvidence == nil {
		return helper.ResolverMutationEvidence{}, errors.New("network resolver policy migration evidence is not a successful resolver retirement result")
	}
	return *response.Result.ResolverEvidence, nil
}
