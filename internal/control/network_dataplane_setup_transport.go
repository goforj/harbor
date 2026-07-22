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

const (
	// maximumNetworkDataPlaneSetupSelectionRequestBytes prevents selectors from becoming an input-amplification channel.
	maximumNetworkDataPlaneSetupSelectionRequestBytes = 2048
	// maximumNetworkDataPlaneSetupConfirmationRequestBytes retains the helper evidence limit while bounding its control envelope.
	maximumNetworkDataPlaneSetupConfirmationRequestBytes = helper.MaxResponseBytes + 2048
)

// NetworkDataPlaneSetupAuthority owns only the optional trusted-ingress setup control surface.
type NetworkDataPlaneSetupAuthority interface {
	StartNetworkDataPlaneSetup(context.Context, Caller, StartNetworkDataPlaneSetupRequest) (NetworkDataPlaneSetupOperation, error)
	ReadNetworkDataPlaneSetup(context.Context, Caller, ReadNetworkDataPlaneSetupRequest) (NetworkDataPlaneSetupOperation, error)
	PrepareNetworkDataPlaneTrustApproval(context.Context, Caller, PrepareNetworkDataPlaneTrustApprovalRequest) (NetworkDataPlaneTrustApprovalPreparation, error)
	ConfirmNetworkDataPlaneTrustApproval(context.Context, Caller, ConfirmNetworkDataPlaneTrustApprovalRequest) (NetworkDataPlaneSetupOperation, error)
	PrepareNetworkDataPlaneLowPortApproval(context.Context, Caller, PrepareNetworkDataPlaneLowPortApprovalRequest) (NetworkDataPlaneLowPortApprovalPreparation, error)
	ConfirmNetworkDataPlaneLowPortApproval(context.Context, Caller, ConfirmNetworkDataPlaneLowPortApprovalRequest) (NetworkDataPlaneSetupConfirmation, error)
}

// networkDataPlaneSetupAuthorityIsNil rejects typed-nil optional implementations before capability negotiation.
func networkDataPlaneSetupAuthorityIsNil(authority NetworkDataPlaneSetupAuthority) bool {
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

// networkDataPlaneSetupResponse leaves room for additive setup metadata without changing the result shape.
type networkDataPlaneSetupResponse struct {
	Setup NetworkDataPlaneSetupOperation `json:"setup"`
}

// networkDataPlaneTrustPreparationResponse wraps the reviewed trust ticket result.
type networkDataPlaneTrustPreparationResponse struct {
	Preparation NetworkDataPlaneTrustApprovalPreparation `json:"preparation"`
}

// networkDataPlaneLowPortPreparationResponse wraps the reviewed low-port ticket result.
type networkDataPlaneLowPortPreparationResponse struct {
	Preparation NetworkDataPlaneLowPortApprovalPreparation `json:"preparation"`
}

// networkDataPlaneTrustConfirmationResponse returns the post-trust operation without implying completion.
type networkDataPlaneTrustConfirmationResponse struct {
	Setup NetworkDataPlaneSetupOperation `json:"setup"`
}

// networkDataPlaneLowPortConfirmationResponse returns terminal setup only after both authority boundaries complete.
type networkDataPlaneLowPortConfirmationResponse struct {
	Confirmation NetworkDataPlaneSetupConfirmation `json:"confirmation"`
}

// StartNetworkDataPlaneSetup starts or replays one client-stable trusted-ingress intent.
func (client *Client) StartNetworkDataPlaneSetup(ctx context.Context, request StartNetworkDataPlaneSetupRequest) (NetworkDataPlaneSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	payload, err := client.networkDataPlaneSetupCall(ctx, methodNetworkDataPlaneSetupStart, request)
	if err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	var response networkDataPlaneSetupResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkDataPlaneSetupOperation{}, fmt.Errorf("decode network data-plane setup response: %w", err)
	}
	if err := validateNetworkDataPlaneSetupStartCorrelation(request, response.Setup); err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	return response.Setup, nil
}

// ReadNetworkDataPlaneSetup reads the current durable trusted-ingress setup operation.
func (client *Client) ReadNetworkDataPlaneSetup(ctx context.Context, request ReadNetworkDataPlaneSetupRequest) (NetworkDataPlaneSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	payload, err := client.networkDataPlaneSetupCall(ctx, methodNetworkDataPlaneSetupRead, request)
	if err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	var response networkDataPlaneSetupResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkDataPlaneSetupOperation{}, fmt.Errorf("decode network data-plane setup read response: %w", err)
	}
	if err := response.Setup.Validate(); err != nil || response.Setup.Operation.ID != request.OperationID {
		return NetworkDataPlaneSetupOperation{}, errors.New("network data-plane setup read does not match the requested operation")
	}
	return response.Setup, nil
}

// PrepareNetworkDataPlaneTrustApproval requests one caller-bound trust helper capability.
func (client *Client) PrepareNetworkDataPlaneTrustApproval(ctx context.Context, request PrepareNetworkDataPlaneTrustApprovalRequest) (NetworkDataPlaneTrustApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneTrustApprovalPreparation{}, err
	}
	payload, err := client.networkDataPlaneSetupCall(ctx, methodNetworkDataPlaneTrustPrepare, request)
	if err != nil {
		return NetworkDataPlaneTrustApprovalPreparation{}, err
	}
	var response networkDataPlaneTrustPreparationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkDataPlaneTrustApprovalPreparation{}, fmt.Errorf("decode network data-plane trust preparation: %w", err)
	}
	if err := validateNetworkDataPlaneTrustPreparationCorrelation(request, response.Preparation); err != nil {
		return NetworkDataPlaneTrustApprovalPreparation{}, err
	}
	return response.Preparation, nil
}

// ConfirmNetworkDataPlaneTrustApproval submits exact trust evidence and returns the low-port approval state.
func (client *Client) ConfirmNetworkDataPlaneTrustApproval(ctx context.Context, request ConfirmNetworkDataPlaneTrustApprovalRequest) (NetworkDataPlaneSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	payload, err := client.networkDataPlaneSetupCall(ctx, methodNetworkDataPlaneTrustConfirm, request)
	if err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	var response networkDataPlaneTrustConfirmationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkDataPlaneSetupOperation{}, fmt.Errorf("decode network data-plane trust confirmation: %w", err)
	}
	if err := validateNetworkDataPlaneTrustConfirmationCorrelation(request, response.Setup); err != nil {
		return NetworkDataPlaneSetupOperation{}, err
	}
	return response.Setup, nil
}

// PrepareNetworkDataPlaneLowPortApproval requests one caller-bound low-port helper capability.
func (client *Client) PrepareNetworkDataPlaneLowPortApproval(ctx context.Context, request PrepareNetworkDataPlaneLowPortApprovalRequest) (NetworkDataPlaneLowPortApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneLowPortApprovalPreparation{}, err
	}
	payload, err := client.networkDataPlaneSetupCall(ctx, methodNetworkDataPlaneLowPortPrepare, request)
	if err != nil {
		return NetworkDataPlaneLowPortApprovalPreparation{}, err
	}
	var response networkDataPlaneLowPortPreparationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkDataPlaneLowPortApprovalPreparation{}, fmt.Errorf("decode network data-plane low-port preparation: %w", err)
	}
	if err := validateNetworkDataPlaneLowPortPreparationCorrelation(request, response.Preparation); err != nil {
		return NetworkDataPlaneLowPortApprovalPreparation{}, err
	}
	return response.Preparation, nil
}

// ConfirmNetworkDataPlaneLowPortApproval submits exact paired-listener evidence and finishes setup.
func (client *Client) ConfirmNetworkDataPlaneLowPortApproval(ctx context.Context, request ConfirmNetworkDataPlaneLowPortApprovalRequest) (NetworkDataPlaneSetupConfirmation, error) {
	if err := request.Validate(); err != nil {
		return NetworkDataPlaneSetupConfirmation{}, err
	}
	payload, err := client.networkDataPlaneSetupCall(ctx, methodNetworkDataPlaneLowPortConfirm, request)
	if err != nil {
		return NetworkDataPlaneSetupConfirmation{}, err
	}
	var response networkDataPlaneLowPortConfirmationResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return NetworkDataPlaneSetupConfirmation{}, fmt.Errorf("decode network data-plane low-port confirmation: %w", err)
	}
	if err := validateNetworkDataPlaneLowPortConfirmationCorrelation(request, response.Confirmation); err != nil {
		return NetworkDataPlaneSetupConfirmation{}, err
	}
	return response.Confirmation, nil
}

// networkDataPlaneSetupCall enforces the optional capability before a client sends an authority-bearing request.
func (client *Client) networkDataPlaneSetupCall(ctx context.Context, method string, request any) ([]byte, error) {
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkDataPlaneSetupV1) {
		return nil, errors.New("Harbor daemon does not support network data-plane setup; upgrade or restart harbord")
	}
	return client.session.Call(ctx, method, request)
}

// networkDataPlaneSetupStartHandler admits one bounded machine-global trusted-ingress intent.
func (server *Server) networkDataPlaneSetupStartHandler(peer local.PeerIdentity) session.Handler {
	return networkDataPlaneSetupHandler(server, peer, decodeStartNetworkDataPlaneSetupRequest, func(ctx context.Context, caller Caller, request StartNetworkDataPlaneSetupRequest) (any, error) {
		result, err := server.config.NetworkDataPlaneSetupAuthority.StartNetworkDataPlaneSetup(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkDataPlaneSetupStartCorrelation(request, result); err != nil {
			return nil, err
		}
		return networkDataPlaneSetupResponse{Setup: result}, nil
	})
}

// networkDataPlaneSetupReadHandler reads only one daemon-owned trusted-ingress operation.
func (server *Server) networkDataPlaneSetupReadHandler(peer local.PeerIdentity) session.Handler {
	return networkDataPlaneSetupHandler(server, peer, decodeReadNetworkDataPlaneSetupRequest, func(ctx context.Context, caller Caller, request ReadNetworkDataPlaneSetupRequest) (any, error) {
		result, err := server.config.NetworkDataPlaneSetupAuthority.ReadNetworkDataPlaneSetup(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := result.Validate(); err != nil || result.Operation.ID != request.OperationID {
			return nil, errors.New("network data-plane setup read does not match the requested operation")
		}
		return networkDataPlaneSetupResponse{Setup: result}, nil
	})
}

// networkDataPlaneTrustPrepareHandler admits one exact trust-approval revision.
func (server *Server) networkDataPlaneTrustPrepareHandler(peer local.PeerIdentity) session.Handler {
	return networkDataPlaneSetupHandler(server, peer, decodePrepareNetworkDataPlaneTrustApprovalRequest, func(ctx context.Context, caller Caller, request PrepareNetworkDataPlaneTrustApprovalRequest) (any, error) {
		result, err := server.config.NetworkDataPlaneSetupAuthority.PrepareNetworkDataPlaneTrustApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkDataPlaneTrustPreparationCorrelation(request, result); err != nil {
			return nil, err
		}
		return networkDataPlaneTrustPreparationResponse{Preparation: result}, nil
	})
}

// networkDataPlaneTrustConfirmHandler accepts only canonical trust evidence for the selected revision.
func (server *Server) networkDataPlaneTrustConfirmHandler(peer local.PeerIdentity) session.Handler {
	return networkDataPlaneSetupHandler(server, peer, decodeConfirmNetworkDataPlaneTrustApprovalRequest, func(ctx context.Context, caller Caller, request ConfirmNetworkDataPlaneTrustApprovalRequest) (any, error) {
		result, err := server.config.NetworkDataPlaneSetupAuthority.ConfirmNetworkDataPlaneTrustApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkDataPlaneTrustConfirmationCorrelation(request, result); err != nil {
			return nil, err
		}
		return networkDataPlaneTrustConfirmationResponse{Setup: result}, nil
	})
}

// networkDataPlaneLowPortPrepareHandler admits one exact low-port-approval revision.
func (server *Server) networkDataPlaneLowPortPrepareHandler(peer local.PeerIdentity) session.Handler {
	return networkDataPlaneSetupHandler(server, peer, decodePrepareNetworkDataPlaneLowPortApprovalRequest, func(ctx context.Context, caller Caller, request PrepareNetworkDataPlaneLowPortApprovalRequest) (any, error) {
		result, err := server.config.NetworkDataPlaneSetupAuthority.PrepareNetworkDataPlaneLowPortApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkDataPlaneLowPortPreparationCorrelation(request, result); err != nil {
			return nil, err
		}
		return networkDataPlaneLowPortPreparationResponse{Preparation: result}, nil
	})
}

// networkDataPlaneLowPortConfirmHandler accepts only canonical low-port evidence for the selected revision.
func (server *Server) networkDataPlaneLowPortConfirmHandler(peer local.PeerIdentity) session.Handler {
	return networkDataPlaneSetupHandler(server, peer, decodeConfirmNetworkDataPlaneLowPortApprovalRequest, func(ctx context.Context, caller Caller, request ConfirmNetworkDataPlaneLowPortApprovalRequest) (any, error) {
		result, err := server.config.NetworkDataPlaneSetupAuthority.ConfirmNetworkDataPlaneLowPortApproval(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkDataPlaneLowPortConfirmationCorrelation(request, result); err != nil {
			return nil, err
		}
		return networkDataPlaneLowPortConfirmationResponse{Confirmation: result}, nil
	})
}

// networkDataPlaneSetupHandler establishes the caller once and prevents unnegotiated access to every optional method.
func networkDataPlaneSetupHandler[T any](server *Server, peer local.PeerIdentity, decode func([]byte) (T, error), call func(context.Context, Caller, T) (any, error)) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(peer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkDataPlaneSetupV1) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("network data-plane setup capability was not negotiated"))
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

// validateNetworkDataPlaneSetupStartCorrelation prevents a daemon response from crossing the client-owned intent boundary.
func validateNetworkDataPlaneSetupStartCorrelation(request StartNetworkDataPlaneSetupRequest, setup NetworkDataPlaneSetupOperation) error {
	if err := setup.Validate(); err != nil {
		return err
	}
	if setup.Operation.IntentID != request.IntentID {
		return errors.New("network data-plane setup does not match the requested intent")
	}
	return nil
}

// validateNetworkDataPlaneTrustPreparationCorrelation binds a trust ticket to exactly the revision requested by the client.
func validateNetworkDataPlaneTrustPreparationCorrelation(request PrepareNetworkDataPlaneTrustApprovalRequest, preparation NetworkDataPlaneTrustApprovalPreparation) error {
	if err := preparation.Validate(); err != nil {
		return err
	}
	if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
		return errors.New("network data-plane trust preparation does not match the requested operation revision")
	}
	return nil
}

// validateNetworkDataPlaneTrustConfirmationCorrelation requires trust to advance the same operation to low-port approval.
func validateNetworkDataPlaneTrustConfirmationCorrelation(request ConfirmNetworkDataPlaneTrustApprovalRequest, setup NetworkDataPlaneSetupOperation) error {
	if err := setup.Validate(); err != nil {
		return err
	}
	if setup.Operation.ID != request.OperationID || setup.Revision <= request.ExpectedOperationRevision || !RequiresNetworkDataPlaneLowPortApproval(setup) {
		return errors.New("network data-plane trust confirmation does not match the selected low-port approval state")
	}
	return nil
}

// validateNetworkDataPlaneLowPortPreparationCorrelation binds a low-port ticket to exactly the revision requested by the client.
func validateNetworkDataPlaneLowPortPreparationCorrelation(request PrepareNetworkDataPlaneLowPortApprovalRequest, preparation NetworkDataPlaneLowPortApprovalPreparation) error {
	if err := preparation.Validate(); err != nil {
		return err
	}
	if preparation.OperationID != request.OperationID || preparation.OperationRevision != request.ExpectedOperationRevision {
		return errors.New("network data-plane low-port preparation does not match the requested operation revision")
	}
	return nil
}

// validateNetworkDataPlaneLowPortConfirmationCorrelation requires terminal setup to remain tied to the selected operation.
func validateNetworkDataPlaneLowPortConfirmationCorrelation(request ConfirmNetworkDataPlaneLowPortApprovalRequest, confirmation NetworkDataPlaneSetupConfirmation) error {
	if err := confirmation.Validate(); err != nil {
		return err
	}
	if confirmation.Operation.ID != request.OperationID || confirmation.Revision <= request.ExpectedOperationRevision {
		return errors.New("network data-plane low-port confirmation does not match the requested operation revision")
	}
	return nil
}

// decodeStartNetworkDataPlaneSetupRequest accepts only the client-owned idempotency intent.
func decodeStartNetworkDataPlaneSetupRequest(payload []byte) (StartNetworkDataPlaneSetupRequest, error) {
	var request StartNetworkDataPlaneSetupRequest
	fields, err := decodeNetworkDataPlaneSetupObject(payload, maximumNetworkDataPlaneSetupSelectionRequestBytes, "network data-plane setup start", "intent_id")
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal(fields["intent_id"], &request.IntentID); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeReadNetworkDataPlaneSetupRequest accepts only one daemon-owned operation selector.
func decodeReadNetworkDataPlaneSetupRequest(payload []byte) (ReadNetworkDataPlaneSetupRequest, error) {
	var request ReadNetworkDataPlaneSetupRequest
	fields, err := decodeNetworkDataPlaneSetupObject(payload, maximumNetworkDataPlaneSetupSelectionRequestBytes, "network data-plane setup read", "operation_id")
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal(fields["operation_id"], &request.OperationID); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodePrepareNetworkDataPlaneTrustApprovalRequest admits only an optimistic trust-approval selection.
func decodePrepareNetworkDataPlaneTrustApprovalRequest(payload []byte) (PrepareNetworkDataPlaneTrustApprovalRequest, error) {
	var request PrepareNetworkDataPlaneTrustApprovalRequest
	fields, err := decodeNetworkDataPlaneSetupObject(payload, maximumNetworkDataPlaneSetupSelectionRequestBytes, "network data-plane trust preparation", "operation_id", "expected_operation_revision")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkDataPlaneSelection(fields, &request.OperationID, &request.ExpectedOperationRevision); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodePrepareNetworkDataPlaneLowPortApprovalRequest admits only an optimistic low-port-approval selection.
func decodePrepareNetworkDataPlaneLowPortApprovalRequest(payload []byte) (PrepareNetworkDataPlaneLowPortApprovalRequest, error) {
	var request PrepareNetworkDataPlaneLowPortApprovalRequest
	fields, err := decodeNetworkDataPlaneSetupObject(payload, maximumNetworkDataPlaneSetupSelectionRequestBytes, "network data-plane low-port preparation", "operation_id", "expected_operation_revision")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkDataPlaneSelection(fields, &request.OperationID, &request.ExpectedOperationRevision); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeConfirmNetworkDataPlaneTrustApprovalRequest rejects any evidence envelope that could carry extra authority.
func decodeConfirmNetworkDataPlaneTrustApprovalRequest(payload []byte) (ConfirmNetworkDataPlaneTrustApprovalRequest, error) {
	var request ConfirmNetworkDataPlaneTrustApprovalRequest
	fields, err := decodeNetworkDataPlaneSetupObject(payload, maximumNetworkDataPlaneSetupConfirmationRequestBytes, "network data-plane trust confirmation", "operation_id", "expected_operation_revision", "trust_evidence")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkDataPlaneSelection(fields, &request.OperationID, &request.ExpectedOperationRevision); err != nil {
		return request, err
	}
	request.TrustEvidence, err = decodeNetworkDataPlaneTrustEvidence(fields["trust_evidence"])
	if err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeConfirmNetworkDataPlaneLowPortApprovalRequest rejects any evidence envelope that could carry extra authority.
func decodeConfirmNetworkDataPlaneLowPortApprovalRequest(payload []byte) (ConfirmNetworkDataPlaneLowPortApprovalRequest, error) {
	var request ConfirmNetworkDataPlaneLowPortApprovalRequest
	fields, err := decodeNetworkDataPlaneSetupObject(payload, maximumNetworkDataPlaneSetupConfirmationRequestBytes, "network data-plane low-port confirmation", "operation_id", "expected_operation_revision", "low_port_evidence")
	if err != nil {
		return request, err
	}
	if err := decodeNetworkDataPlaneSelection(fields, &request.OperationID, &request.ExpectedOperationRevision); err != nil {
		return request, err
	}
	request.LowPortEvidence, err = decodeNetworkDataPlaneLowPortEvidence(fields["low_port_evidence"])
	if err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeNetworkDataPlaneSelection decodes the shared exact operation-and-revision optimistic lock.
func decodeNetworkDataPlaneSelection(fields map[string]json.RawMessage, operationID *domain.OperationID, revision *domain.Sequence) error {
	if err := json.Unmarshal(fields["operation_id"], operationID); err != nil {
		return fmt.Errorf("decode network data-plane operation ID: %w", err)
	}
	if err := json.Unmarshal(fields["expected_operation_revision"], revision); err != nil {
		return fmt.Errorf("decode network data-plane operation revision: %w", err)
	}
	return nil
}

// decodeNetworkDataPlaneTrustEvidence reuses helper validation without accepting its outer response fields from control JSON.
func decodeNetworkDataPlaneTrustEvidence(body json.RawMessage) (helper.TrustMutationEvidence, error) {
	response, err := decodeNetworkDataPlaneHelperEvidence(body, helper.OperationEnsureTrust, "trust_evidence")
	if err != nil || response.Result.TrustEvidence == nil {
		return helper.TrustMutationEvidence{}, errors.New("network data-plane trust evidence is invalid")
	}
	return *response.Result.TrustEvidence, nil
}

// decodeNetworkDataPlaneLowPortEvidence reuses helper validation without accepting its outer response fields from control JSON.
func decodeNetworkDataPlaneLowPortEvidence(body json.RawMessage) (helper.LowPortMutationEvidence, error) {
	response, err := decodeNetworkDataPlaneHelperEvidence(body, helper.OperationEnsureLowPorts, "low_port_evidence")
	if err != nil || response.Result.LowPortEvidence == nil {
		return helper.LowPortMutationEvidence{}, errors.New("network data-plane low-port evidence is invalid")
	}
	return *response.Result.LowPortEvidence, nil
}

// decodeNetworkDataPlaneHelperEvidence reconstructs the fixed helper envelope so its decoder remains authoritative.
func decodeNetworkDataPlaneHelperEvidence(body json.RawMessage, operation helper.Operation, field string) (helper.Response, error) {
	envelope := fmt.Appendf([]byte{}, `{"version":%d,"ok":true,"result":{"operation":%q,%q:`, helper.ProtocolVersion, operation, field)
	envelope = append(envelope, body...)
	envelope = append(envelope, '}', '}')
	response, err := helper.DecodeResponse(bytes.NewReader(envelope))
	if err != nil || !response.OK || response.Result == nil || response.Result.Operation != operation {
		return helper.Response{}, errors.New("helper evidence is invalid")
	}
	return response, nil
}

// decodeNetworkDataPlaneSetupObject rejects duplicate, unknown, missing, or trailing JSON values before method decoding.
func decodeNetworkDataPlaneSetupObject(payload []byte, maximum int, name string, allowed ...string) (map[string]json.RawMessage, error) {
	if len(payload) == 0 || len(payload) > maximum {
		return nil, fmt.Errorf("%s request exceeds its bounded object shape", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("%s request must be an object", name)
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	allowedSet := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = true
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		field, ok := token.(string)
		if !ok || !allowedSet[field] {
			return nil, fmt.Errorf("%s request contains unknown field %q", name, field)
		}
		if _, present := fields[field]; present {
			return nil, fmt.Errorf("%s request contains duplicate field %q", name, field)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[field] = raw
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("%s request object is not terminated", name)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return nil, err
	}
	for _, field := range allowed {
		if _, present := fields[field]; !present {
			return nil, fmt.Errorf("%s request requires %s", name, field)
		}
	}
	return fields, nil
}
