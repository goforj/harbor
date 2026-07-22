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
	maximumNetworkReleaseRequestBytes  = 2048
	maximumNetworkReleaseResponseBytes = 4096
)

// NetworkReleaseAuthority owns only the optional machine-global network release control surface.
type NetworkReleaseAuthority interface {
	StartNetworkRelease(context.Context, Caller, StartNetworkReleaseRequest) (NetworkReleaseOperation, error)
	ReadNetworkRelease(context.Context, Caller, ReadNetworkReleaseRequest) (NetworkReleaseOperation, error)
}

// networkReleaseAuthorityIsNil rejects typed-nil optional implementations before capability negotiation.
func networkReleaseAuthorityIsNil(authority NetworkReleaseAuthority) bool {
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

// networkReleaseResponse keeps the release result extensible without changing its reviewed shape.
type networkReleaseResponse struct {
	Release NetworkReleaseOperation `json:"release"`
}

// StartNetworkRelease starts or replays one client-stable machine-global network release intent.
func (client *Client) StartNetworkRelease(ctx context.Context, request StartNetworkReleaseRequest) (NetworkReleaseOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkReleaseOperation{}, err
	}
	payload, err := client.networkReleaseCall(ctx, methodNetworkReleaseStart, request)
	if err != nil {
		return NetworkReleaseOperation{}, err
	}
	var response networkReleaseResponse
	if err := decodeNetworkReleaseResponse(payload, &response); err != nil {
		return NetworkReleaseOperation{}, err
	}
	if err := validateNetworkReleaseStartCorrelation(request, response.Release); err != nil {
		return NetworkReleaseOperation{}, err
	}
	return response.Release, nil
}

// ReadNetworkRelease reads one durable machine-global network release operation.
func (client *Client) ReadNetworkRelease(ctx context.Context, request ReadNetworkReleaseRequest) (NetworkReleaseOperation, error) {
	if err := request.Validate(); err != nil {
		return NetworkReleaseOperation{}, err
	}
	payload, err := client.networkReleaseCall(ctx, methodNetworkReleaseRead, request)
	if err != nil {
		return NetworkReleaseOperation{}, err
	}
	var response networkReleaseResponse
	if err := decodeNetworkReleaseResponse(payload, &response); err != nil {
		return NetworkReleaseOperation{}, err
	}
	if err := validateNetworkReleaseReadCorrelation(request, response.Release); err != nil {
		return NetworkReleaseOperation{}, err
	}
	return response.Release, nil
}

// networkReleaseCall enforces the optional capability before a client sends an authority-bearing request.
func (client *Client) networkReleaseCall(ctx context.Context, method string, request any) ([]byte, error) {
	if !containsCapability(client.peer.Session.Capabilities, CapabilityNetworkReleaseV1) {
		return nil, errors.New("Harbor daemon does not support network release; upgrade or restart harbord")
	}
	return client.session.Call(ctx, method, request)
}

// networkReleaseStartHandler admits one bounded machine-global release intent.
func (server *Server) networkReleaseStartHandler(peer local.PeerIdentity) session.Handler {
	return networkReleaseHandler(server, peer, decodeStartNetworkReleaseRequest, func(ctx context.Context, caller Caller, request StartNetworkReleaseRequest) (any, error) {
		result, err := server.config.NetworkReleaseAuthority.StartNetworkRelease(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkReleaseStartCorrelation(request, result); err != nil {
			return nil, err
		}
		return networkReleaseResponse{Release: result}, nil
	})
}

// networkReleaseReadHandler reads only one daemon-owned release operation.
func (server *Server) networkReleaseReadHandler(peer local.PeerIdentity) session.Handler {
	return networkReleaseHandler(server, peer, decodeReadNetworkReleaseRequest, func(ctx context.Context, caller Caller, request ReadNetworkReleaseRequest) (any, error) {
		result, err := server.config.NetworkReleaseAuthority.ReadNetworkRelease(ctx, caller, request)
		if err != nil {
			return nil, err
		}
		if err := validateNetworkReleaseReadCorrelation(request, result); err != nil {
			return nil, err
		}
		return networkReleaseResponse{Release: result}, nil
	})
}

// networkReleaseHandler establishes the caller once and prevents unnegotiated access to every optional method.
func networkReleaseHandler[T any](server *Server, peer local.PeerIdentity, decode func([]byte) (T, error), call func(context.Context, Caller, T) (any, error)) session.Handler {
	return func(ctx context.Context, request session.Request) (any, error) {
		caller, err := callerFromRequest(peer, request)
		if err != nil {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, err)
		}
		if !containsCapability(caller.Session.Capabilities, CapabilityNetworkReleaseV1) {
			return nil, session.NewHandlerError(rpc.ErrorCodePermissionDenied, errors.New("network release capability was not negotiated"))
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

// validateNetworkReleaseStartCorrelation prevents a daemon response from crossing the client-owned intent boundary.
func validateNetworkReleaseStartCorrelation(request StartNetworkReleaseRequest, release NetworkReleaseOperation) error {
	if err := release.Validate(); err != nil {
		return err
	}
	if release.Operation.IntentID != request.IntentID {
		return errors.New("network release does not match the requested intent")
	}
	return nil
}

// validateNetworkReleaseReadCorrelation prevents a daemon response from crossing the requested operation boundary.
func validateNetworkReleaseReadCorrelation(request ReadNetworkReleaseRequest, release NetworkReleaseOperation) error {
	if err := release.Validate(); err != nil {
		return err
	}
	if release.Operation.ID != request.OperationID {
		return errors.New("network release read does not match the requested operation")
	}
	return nil
}

// decodeStartNetworkReleaseRequest accepts only the client-owned idempotency intent.
func decodeStartNetworkReleaseRequest(payload []byte) (StartNetworkReleaseRequest, error) {
	var request StartNetworkReleaseRequest
	fields, err := decodeNetworkReleaseObject(payload, "network release start", "intent_id")
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal(fields["intent_id"], &request.IntentID); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeReadNetworkReleaseRequest accepts only one daemon-owned operation selector.
func decodeReadNetworkReleaseRequest(payload []byte) (ReadNetworkReleaseRequest, error) {
	var request ReadNetworkReleaseRequest
	fields, err := decodeNetworkReleaseObject(payload, "network release read", "operation_id")
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal(fields["operation_id"], &request.OperationID); err != nil {
		return request, err
	}
	return request, request.Validate()
}

// decodeNetworkReleaseResponse rejects response trailing data before validating the reviewed result shape.
func decodeNetworkReleaseResponse(payload []byte, response *networkReleaseResponse) error {
	if len(payload) == 0 || len(payload) > maximumNetworkReleaseResponseBytes {
		return errors.New("decode network release response: response exceeds its bounded object shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode network release response: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return errors.New("decode network release response: response must be an object")
	}
	var release json.RawMessage
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode network release response: %w", err)
		}
		field, ok := token.(string)
		if !ok || field != "release" {
			return fmt.Errorf("decode network release response: response contains unknown field %q", field)
		}
		if release != nil {
			return errors.New("decode network release response: response contains duplicate field \"release\"")
		}
		if err := decoder.Decode(&release); err != nil {
			return fmt.Errorf("decode network release response: %w", err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode network release response: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return errors.New("decode network release response: response object is not terminated")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return fmt.Errorf("decode network release response: %w", err)
	}
	if release == nil {
		return errors.New("decode network release response: response requires release")
	}
	if err := rejectDuplicateNetworkReleaseFields(release); err != nil {
		return fmt.Errorf("decode network release response: %w", err)
	}
	releaseDecoder := json.NewDecoder(bytes.NewReader(release))
	releaseDecoder.DisallowUnknownFields()
	if err := releaseDecoder.Decode(&response.Release); err != nil {
		return fmt.Errorf("decode network release response: %w", err)
	}
	if err := requireJSONEnd(releaseDecoder); err != nil {
		return fmt.Errorf("decode network release response: %w", err)
	}
	if err := response.Release.Validate(); err != nil {
		return fmt.Errorf("validate network release response: %w", err)
	}
	return nil
}

// rejectDuplicateNetworkReleaseFields rejects ambiguous keys anywhere inside the retained release result.
func rejectDuplicateNetworkReleaseFields(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := scanNetworkReleaseJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEnd(decoder)
}

// scanNetworkReleaseJSONValue walks one JSON value while retaining the key set for each nested object.
func scanNetworkReleaseJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		fields := make(map[string]struct{})
		for decoder.More() {
			fieldToken, err := decoder.Token()
			if err != nil {
				return err
			}
			field, ok := fieldToken.(string)
			if !ok {
				return errors.New("network release response object contains a non-string field")
			}
			if _, duplicate := fields[field]; duplicate {
				return fmt.Errorf("network release response contains duplicate field %q", field)
			}
			fields[field] = struct{}{}
			if err := scanNetworkReleaseJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("network release response object is not terminated")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := scanNetworkReleaseJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("network release response array is not terminated")
		}
		return nil
	default:
		return fmt.Errorf("network release response contains unexpected delimiter %q", delimiter)
	}
}

// decodeNetworkReleaseObject rejects duplicate, unknown, missing, or trailing JSON values before method decoding.
func decodeNetworkReleaseObject(payload []byte, name string, allowed ...string) (map[string]json.RawMessage, error) {
	return decodeBoundedNetworkReleaseObject(payload, maximumNetworkReleaseRequestBytes, name, allowed...)
}

// decodeBoundedNetworkReleaseObject rejects duplicate, unknown, missing, or trailing JSON values within one method bound.
func decodeBoundedNetworkReleaseObject(payload []byte, maximum int, name string, allowed ...string) (map[string]json.RawMessage, error) {
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
