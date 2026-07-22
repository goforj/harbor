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

const maximumNetworkReleaseApprovalConfirmationRequestBytes = helper.MaxResponseBytes + maximumNetworkReleaseRequestBytes

// NetworkReleaseApprovalAuthority owns the optional low-port and resolver release approval control surface.
type NetworkReleaseApprovalAuthority interface {
	// PrepareNetworkReleaseApproval publishes one caller-bound low-port release capability.
	PrepareNetworkReleaseApproval(context.Context, Caller, PrepareNetworkReleaseApprovalRequest) (NetworkReleaseApprovalPreparation, error)
	// ConfirmNetworkReleaseApproval verifies low-port removal and advances the retained release plan.
	ConfirmNetworkReleaseApproval(context.Context, Caller, ConfirmNetworkReleaseApprovalRequest) (NetworkReleaseOperation, error)
	// PrepareNetworkReleaseResolverApproval publishes one caller-bound resolver release capability.
	PrepareNetworkReleaseResolverApproval(context.Context, Caller, PrepareNetworkReleaseResolverApprovalRequest) (NetworkReleaseResolverApprovalPreparation, error)
	// ConfirmNetworkReleaseResolverApproval verifies resolver removal and advances the retained release plan.
	ConfirmNetworkReleaseResolverApproval(context.Context, Caller, ConfirmNetworkReleaseResolverApprovalRequest) (NetworkReleaseOperation, error)
}

// networkReleaseApprovalAuthorityIsNil rejects typed-nil optional implementations before capability negotiation.
func networkReleaseApprovalAuthorityIsNil(authority NetworkReleaseApprovalAuthority) bool {
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

// networkReleaseApprovalPreparationResponse keeps helper launch metadata extensible around its reviewed result.
type networkReleaseApprovalPreparationResponse struct {
	// Preparation carries the reviewed helper capability.
	Preparation NetworkReleaseApprovalPreparation `json:"preparation"`
}

// networkReleaseResolverApprovalPreparationResponse keeps resolver launch metadata extensible around its reviewed result.
type networkReleaseResolverApprovalPreparationResponse struct {
	// Preparation carries the reviewed helper capability.
	Preparation NetworkReleaseResolverApprovalPreparation `json:"preparation"`
}

// PrepareNetworkReleaseApproval requests one caller-bound low-port release capability.
func (client *Client) PrepareNetworkReleaseApproval(ctx context.Context, request PrepareNetworkReleaseApprovalRequest) (NetworkReleaseApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return NetworkReleaseApprovalPreparation{}, err
	}
	payload, err := client.networkReleaseApprovalCall(ctx, CapabilityNetworkReleaseApprovalV1, methodNetworkReleaseLowPortPrepare, request)
	if err != nil {
		return NetworkReleaseApprovalPreparation{}, err
	}
	var response networkReleaseApprovalPreparationResponse
	if err := decodeNetworkReleaseApprovalPreparationResponse(payload, &response); err != nil {
		return NetworkReleaseApprovalPreparation{}, err
	}
	if err := validateNetworkReleaseApprovalPreparationCorrelation(request, response.Preparation); err != nil {
		return NetworkReleaseApprovalPreparation{}, err
	}
	return response.Preparation, nil
}

// decodeNetworkReleaseApprovalPreparationResponse rejects ambiguous response fields before validating ticket metadata.
func decodeNetworkReleaseApprovalPreparationResponse(payload []byte, response *networkReleaseApprovalPreparationResponse) error {
	if len(payload) == 0 || len(payload) > maximumNetworkReleaseResponseBytes {
		return errors.New("decode network release approval preparation response: response exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode network release approval preparation response: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return errors.New("decode network release approval preparation response: response must be an object")
	}
	var preparation json.RawMessage
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode network release approval preparation response: %w", err)
		}
		field, ok := token.(string)
		if !ok || field != "preparation" {
			return fmt.Errorf("decode network release approval preparation response: response contains unknown field %q", field)
		}
		if preparation != nil {
			return errors.New("decode network release approval preparation response: response contains duplicate field \"preparation\"")
		}
		if err := decoder.Decode(&preparation); err != nil {
			return fmt.Errorf("decode network release approval preparation response: %w", err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode network release approval preparation response: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return errors.New("decode network release approval preparation response: response object is not terminated")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return fmt.Errorf("decode network release approval preparation response: %w", err)
	}
	if preparation == nil {
		return errors.New("decode network release approval preparation response: response requires preparation")
	}
	if err := rejectDuplicateNetworkReleaseFields(preparation); err != nil {
		return fmt.Errorf("decode network release approval preparation response: %w", err)
	}
	preparationDecoder := json.NewDecoder(bytes.NewReader(preparation))
	preparationDecoder.DisallowUnknownFields()
	if err := preparationDecoder.Decode(&response.Preparation); err != nil {
		return fmt.Errorf("decode network release approval preparation response: %w", err)
	}
	if err := requireJSONEnd(preparationDecoder); err != nil {
		return fmt.Errorf("decode network release approval preparation response: %w", err)
	}
	if err := response.Preparation.Validate(); err != nil {
		return fmt.Errorf("validate network release approval preparation response: %w", err)
	}
	return nil
}

// ConfirmNetworkReleaseApproval submits exact low-port release evidence and advances the retained plan.
func (client *Client) ConfirmNetworkReleaseApproval(ctx context.Context, request ConfirmNetworkReleaseApprovalRequest) (NetworkReleaseOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkReleaseOperation{}, err
	}
	payload, err := client.networkReleaseApprovalCall(ctx, CapabilityNetworkReleaseApprovalV1, methodNetworkReleaseLowPortConfirm, request)
	if err != nil {
		return NetworkReleaseOperation{}, err
	}
	var response networkReleaseResponse
	if err := decodeNetworkReleaseResponse(payload, &response); err != nil {
		return NetworkReleaseOperation{}, err
	}
	if err := validateNetworkReleaseApprovalConfirmationCorrelation(request, response.Release); err != nil {
		return NetworkReleaseOperation{}, err
	}
	return response.Release, nil
}

// PrepareNetworkReleaseResolverApproval requests one caller-bound resolver release capability.
func (client *Client) PrepareNetworkReleaseResolverApproval(ctx context.Context, request PrepareNetworkReleaseResolverApprovalRequest) (NetworkReleaseResolverApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return NetworkReleaseResolverApprovalPreparation{}, err
	}
	payload, err := client.networkReleaseApprovalCall(ctx, CapabilityNetworkReleaseResolverApprovalV1, methodNetworkReleaseResolverPrepare, request)
	if err != nil {
		return NetworkReleaseResolverApprovalPreparation{}, err
	}
	var response networkReleaseResolverApprovalPreparationResponse
	if err := decodeNetworkReleaseResolverApprovalPreparationResponse(payload, &response); err != nil {
		return NetworkReleaseResolverApprovalPreparation{}, err
	}
	if err := validateNetworkReleaseResolverApprovalPreparationCorrelation(request, response.Preparation); err != nil {
		return NetworkReleaseResolverApprovalPreparation{}, err
	}
	return response.Preparation, nil
}

// decodeNetworkReleaseResolverApprovalPreparationResponse rejects ambiguous resolver preparation response fields before validation.
func decodeNetworkReleaseResolverApprovalPreparationResponse(payload []byte, response *networkReleaseResolverApprovalPreparationResponse) error {
	fields, err := decodeBoundedNetworkReleaseObject(
		payload,
		maximumNetworkReleaseResponseBytes,
		"network release resolver preparation response",
		"preparation",
	)
	if err != nil {
		return err
	}
	preparationFields, err := decodeBoundedNetworkReleaseObject(
		fields["preparation"],
		maximumNetworkReleaseResponseBytes,
		"network release resolver preparation",
		"operation_id",
		"checkpoint_revision",
		"publication_disposition",
		"ticket",
	)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(preparationFields["operation_id"], &response.Preparation.OperationID); err != nil {
		return fmt.Errorf("decode network release resolver preparation operation ID: %w", err)
	}
	if err := json.Unmarshal(preparationFields["checkpoint_revision"], &response.Preparation.CheckpointRevision); err != nil {
		return fmt.Errorf("decode network release resolver preparation checkpoint revision: %w", err)
	}
	if err := json.Unmarshal(preparationFields["publication_disposition"], &response.Preparation.PublicationDisposition); err != nil {
		return fmt.Errorf("decode network release resolver preparation publication disposition: %w", err)
	}
	ticketFields, err := decodeBoundedNetworkReleaseObject(
		preparationFields["ticket"],
		maximumNetworkReleaseResponseBytes,
		"network release resolver ticket",
		"operation_id",
		"reference",
		"operation",
		"policy_fingerprint",
		"target_ownership_fingerprint",
		"expires_at",
	)
	if err != nil {
		return err
	}
	ticket := &response.Preparation.Ticket
	if err := json.Unmarshal(ticketFields["operation_id"], &ticket.OperationID); err != nil {
		return fmt.Errorf("decode network release resolver ticket operation ID: %w", err)
	}
	if err := json.Unmarshal(ticketFields["reference"], &ticket.Reference); err != nil {
		return fmt.Errorf("decode network release resolver ticket reference: %w", err)
	}
	if err := json.Unmarshal(ticketFields["operation"], &ticket.Operation); err != nil {
		return fmt.Errorf("decode network release resolver ticket operation: %w", err)
	}
	if err := json.Unmarshal(ticketFields["policy_fingerprint"], &ticket.PolicyFingerprint); err != nil {
		return fmt.Errorf("decode network release resolver ticket policy fingerprint: %w", err)
	}
	if err := json.Unmarshal(ticketFields["target_ownership_fingerprint"], &ticket.TargetOwnershipFingerprint); err != nil {
		return fmt.Errorf("decode network release resolver ticket target ownership fingerprint: %w", err)
	}
	if err := json.Unmarshal(ticketFields["expires_at"], &ticket.ExpiresAt); err != nil {
		return fmt.Errorf("decode network release resolver ticket expiry: %w", err)
	}
	if err := response.Preparation.Validate(); err != nil {
		return fmt.Errorf("validate network release resolver preparation response: %w", err)
	}
	return nil
}

// ConfirmNetworkReleaseResolverApproval submits exact resolver release evidence and advances the retained plan.
func (client *Client) ConfirmNetworkReleaseResolverApproval(ctx context.Context, request ConfirmNetworkReleaseResolverApprovalRequest) (NetworkReleaseOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkReleaseOperation{}, err
	}
	payload, err := client.networkReleaseApprovalCall(ctx, CapabilityNetworkReleaseResolverApprovalV1, methodNetworkReleaseResolverConfirm, request)
	if err != nil {
		return NetworkReleaseOperation{}, err
	}
	var response networkReleaseResponse
	if err := decodeNetworkReleaseResponse(payload, &response); err != nil {
		return NetworkReleaseOperation{}, err
	}
	if err := validateNetworkReleaseResolverApprovalConfirmationCorrelation(request, response.Release); err != nil {
		return NetworkReleaseOperation{}, err
	}
	return response.Release, nil
}

// networkReleaseApprovalCall enforces independent approval capability negotiation before dispatch.
func (client *Client) networkReleaseApprovalCall(ctx context.Context, capability rpc.Capability, method string, request any) ([]byte, error) {
	if !containsCapability(client.peer.Session.Capabilities, capability) {
		return nil, errors.New("Harbor daemon does not support network release approval; upgrade or restart harbord")
	}
	return client.session.Call(ctx, method, request)
}

// networkReleaseLowPortPrepareHandler admits one exact release checkpoint for helper authorization.
func (server *Server) networkReleaseLowPortPrepareHandler(peer local.PeerIdentity) session.Handler {
	return networkReleaseApprovalHandler(server, peer, CapabilityNetworkReleaseApprovalV1, decodePrepareNetworkReleaseApprovalRequest, func(ctx context.Context, caller Caller, request PrepareNetworkReleaseApprovalRequest) (any, error) {
		preparation, err := server.config.NetworkReleaseApprovalAuthority.PrepareNetworkReleaseApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := preparation.Validate(); err != nil {
			return nil, err
		}
		if err := validateNetworkReleaseApprovalPreparationCorrelation(request, preparation); err != nil {
			return nil, err
		}
		return networkReleaseApprovalPreparationResponse{Preparation: preparation}, nil
	})
}

// networkReleaseLowPortConfirmHandler admits canonical owned-absent evidence for one release checkpoint.
func (server *Server) networkReleaseLowPortConfirmHandler(peer local.PeerIdentity) session.Handler {
	return networkReleaseApprovalHandler(server, peer, CapabilityNetworkReleaseApprovalV1, decodeConfirmNetworkReleaseApprovalRequest, func(ctx context.Context, caller Caller, request ConfirmNetworkReleaseApprovalRequest) (any, error) {
		release, err := server.config.NetworkReleaseApprovalAuthority.ConfirmNetworkReleaseApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkReleaseApprovalConfirmationCorrelation(request, release); err != nil {
			return nil, err
		}
		return networkReleaseResponse{Release: release}, nil
	})
}

// networkReleaseResolverPrepareHandler admits one exact release checkpoint for resolver helper authorization.
func (server *Server) networkReleaseResolverPrepareHandler(peer local.PeerIdentity) session.Handler {
	return networkReleaseApprovalHandler(server, peer, CapabilityNetworkReleaseResolverApprovalV1, decodePrepareNetworkReleaseResolverApprovalRequest, func(ctx context.Context, caller Caller, request PrepareNetworkReleaseResolverApprovalRequest) (any, error) {
		preparation, err := server.config.NetworkReleaseApprovalAuthority.PrepareNetworkReleaseResolverApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := preparation.Validate(); err != nil {
			return nil, err
		}
		if err := validateNetworkReleaseResolverApprovalPreparationCorrelation(request, preparation); err != nil {
			return nil, err
		}
		return networkReleaseResolverApprovalPreparationResponse{Preparation: preparation}, nil
	})
}

// networkReleaseResolverConfirmHandler admits canonical owned-absent resolver evidence for one release checkpoint.
func (server *Server) networkReleaseResolverConfirmHandler(peer local.PeerIdentity) session.Handler {
	return networkReleaseApprovalHandler(server, peer, CapabilityNetworkReleaseResolverApprovalV1, decodeConfirmNetworkReleaseResolverApprovalRequest, func(ctx context.Context, caller Caller, request ConfirmNetworkReleaseResolverApprovalRequest) (any, error) {
		release, err := server.config.NetworkReleaseApprovalAuthority.ConfirmNetworkReleaseResolverApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkReleaseResolverApprovalConfirmationCorrelation(request, release); err != nil {
			return nil, err
		}
		return networkReleaseResponse{Release: release}, nil
	})
}

// networkReleaseApprovalHandler establishes the caller and blocks every unnegotiated approval method.
func networkReleaseApprovalHandler[T any](server *Server, peer local.PeerIdentity, capability rpc.Capability, decode func([]byte) (T, error), call func(context.Context, Caller, T) (any, error)) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(peer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, capability) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("network release approval capability was not negotiated"))
		}
		decoded, err := decode(request.Payload)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodeInvalidRequest, err)
		}
		result, err := call(ctx, caller, decoded)
		if err != nil {
			return nil, authorityError(err)
		}
		return result, nil
	}
}

// validateNetworkReleaseApprovalPreparationCorrelation binds a ticket to the selected operation and checkpoint.
func validateNetworkReleaseApprovalPreparationCorrelation(request PrepareNetworkReleaseApprovalRequest, preparation NetworkReleaseApprovalPreparation) error {
	if preparation.OperationID != request.OperationID || preparation.CheckpointRevision != request.ExpectedCheckpointRevision {
		return errors.New("network release approval preparation does not match the requested checkpoint")
	}
	return nil
}

// validateNetworkReleaseApprovalConfirmationCorrelation binds resolver progress to a selected checkpoint after low-port release.
func validateNetworkReleaseApprovalConfirmationCorrelation(request ConfirmNetworkReleaseApprovalRequest, release NetworkReleaseOperation) error {
	if err := release.Validate(); err != nil {
		return err
	}
	if release.Operation.ID != request.OperationID || release.Phase != NetworkReleasePhaseResolver || release.CheckpointRevision <= request.ExpectedCheckpointRevision {
		return errors.New("network release approval confirmation does not match the requested checkpoint")
	}
	return nil
}

// validateNetworkReleaseResolverApprovalPreparationCorrelation binds a resolver ticket to the selected operation and checkpoint.
func validateNetworkReleaseResolverApprovalPreparationCorrelation(request PrepareNetworkReleaseResolverApprovalRequest, preparation NetworkReleaseResolverApprovalPreparation) error {
	if preparation.OperationID != request.OperationID || preparation.CheckpointRevision != request.ExpectedCheckpointRevision {
		return errors.New("network release resolver preparation does not match the requested checkpoint")
	}
	return nil
}

// validateNetworkReleaseResolverApprovalConfirmationCorrelation binds trust progress to a selected checkpoint after resolver release.
func validateNetworkReleaseResolverApprovalConfirmationCorrelation(request ConfirmNetworkReleaseResolverApprovalRequest, release NetworkReleaseOperation) error {
	if err := release.Validate(); err != nil {
		return err
	}
	if release.Operation.ID != request.OperationID || release.Phase != NetworkReleasePhaseTrust || release.CheckpointRevision <= request.ExpectedCheckpointRevision {
		return errors.New("network release resolver confirmation does not match the requested checkpoint")
	}
	return nil
}

// decodePrepareNetworkReleaseApprovalRequest rejects authority beyond one exact release checkpoint.
func decodePrepareNetworkReleaseApprovalRequest(payload []byte) (PrepareNetworkReleaseApprovalRequest, error) {
	var request PrepareNetworkReleaseApprovalRequest
	fields, err := decodeNetworkReleaseObject(payload, "network release low-port preparation", "operation_id", "expected_checkpoint_revision")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkReleaseApprovalSelection(fields, &request.OperationID, &request.ExpectedCheckpointRevision); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeConfirmNetworkReleaseApprovalRequest rejects extra authority and delegates evidence parsing to the helper decoder.
func decodeConfirmNetworkReleaseApprovalRequest(payload []byte) (ConfirmNetworkReleaseApprovalRequest, error) {
	var request ConfirmNetworkReleaseApprovalRequest
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseApprovalConfirmationRequestBytes, "network release low-port confirmation", "operation_id", "expected_checkpoint_revision", "low_port_evidence")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkReleaseApprovalSelection(fields, &request.OperationID, &request.ExpectedCheckpointRevision); err != nil {
		return request, err
	}
	request.LowPortEvidence, err = decodeNetworkReleaseLowPortEvidence(fields["low_port_evidence"])
	if err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodePrepareNetworkReleaseResolverApprovalRequest rejects authority beyond one exact release checkpoint.
func decodePrepareNetworkReleaseResolverApprovalRequest(payload []byte) (PrepareNetworkReleaseResolverApprovalRequest, error) {
	var request PrepareNetworkReleaseResolverApprovalRequest
	fields, err := decodeNetworkReleaseObject(payload, "network release resolver preparation", "operation_id", "expected_checkpoint_revision")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkReleaseApprovalSelection(fields, &request.OperationID, &request.ExpectedCheckpointRevision); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeConfirmNetworkReleaseResolverApprovalRequest rejects extra authority and delegates evidence parsing to the helper decoder.
func decodeConfirmNetworkReleaseResolverApprovalRequest(payload []byte) (ConfirmNetworkReleaseResolverApprovalRequest, error) {
	var request ConfirmNetworkReleaseResolverApprovalRequest
	fields, err := decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseApprovalConfirmationRequestBytes, "network release resolver confirmation", "operation_id", "expected_checkpoint_revision", "resolver_evidence")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkReleaseApprovalSelection(fields, &request.OperationID, &request.ExpectedCheckpointRevision); err != nil {
		return request, err
	}
	request.ResolverEvidence, err = decodeNetworkReleaseResolverEvidence(fields["resolver_evidence"])
	if err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeNetworkReleaseApprovalSelection decodes the shared operation and optimistic checkpoint selector.
func decodeNetworkReleaseApprovalSelection(fields map[string]json.RawMessage, operationID *domain.OperationID, revision *domain.Sequence) error {
	if err := json.Unmarshal(fields["operation_id"], operationID); err != nil {
		return fmt.Errorf("decode network release operation ID: %w", err)
	}
	if err := json.Unmarshal(fields["expected_checkpoint_revision"], revision); err != nil {
		return fmt.Errorf("decode network release checkpoint revision: %w", err)
	}
	return nil
}

// decodeNetworkReleaseLowPortEvidence reconstructs the authoritative helper response envelope for release evidence.
func decodeNetworkReleaseLowPortEvidence(body json.RawMessage) (helper.LowPortMutationEvidence, error) {
	response, err := decodeNetworkDataPlaneHelperEvidence(body, helper.OperationReleaseLowPorts, "low_port_evidence")
	if err != nil || response.Result.LowPortEvidence == nil {
		return helper.LowPortMutationEvidence{}, errors.New("network release low-port evidence is invalid")
	}
	evidence := *response.Result.LowPortEvidence
	if err := validateNetworkReleaseLowPortEvidence(evidence); err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	return evidence, nil
}

// decodeNetworkReleaseResolverEvidence reconstructs the authoritative helper response envelope for resolver release evidence.
func decodeNetworkReleaseResolverEvidence(body json.RawMessage) (helper.ResolverMutationEvidence, error) {
	envelope := make([]byte, 0, len(body)+128)
	envelope = fmt.Appendf(
		envelope,
		`{"version":%d,"ok":true,"result":{"operation":"release_resolver","resolver_evidence":`,
		helper.ProtocolVersion,
	)
	envelope = append(envelope, body...)
	envelope = append(envelope, '}', '}')
	response, err := helper.DecodeResponse(bytes.NewReader(envelope))
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("network release resolver evidence is invalid: %w", err)
	}
	if !response.OK || response.Error != nil || response.Result == nil || response.Result.Operation != helper.OperationReleaseResolver || response.Result.ResolverEvidence == nil {
		return helper.ResolverMutationEvidence{}, errors.New("network release resolver evidence is not a successful resolver release result")
	}
	evidence := *response.Result.ResolverEvidence
	if err := validateNetworkReleaseResolverEvidence(evidence); err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	return evidence, nil
}
