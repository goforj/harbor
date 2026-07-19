package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxRequestBytes is the hard upper bound for one helper request body.
const MaxRequestBytes = 16 * 1024

// MaxResponseBytes is the hard upper bound for one helper response body, including its newline.
const MaxResponseBytes = 16 * 1024

const maximumJSONDepth = 32

// WriteRequest writes one validated canonical JSON request followed by a newline.
func WriteRequest(writer io.Writer, request Request) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate helper request: %w", err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal helper request: %w", err)
	}
	if len(body)+1 > MaxRequestBytes {
		return errors.New("helper request exceeds protocol bound")
	}
	body = append(body, '\n')
	written, err := writer.Write(body)
	if err != nil {
		return fmt.Errorf("write helper request: %w", err)
	}
	if written != len(body) {
		return io.ErrShortWrite
	}
	return nil
}

// DecodeRequest reads exactly one bounded strict JSON request.
func DecodeRequest(reader io.Reader) (Request, error) {
	body, err := readBounded(reader, MaxRequestBytes)
	if err != nil {
		return Request{}, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request is empty")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request contains invalid or duplicate JSON fields")
	}
	if err := validateCanonicalRequestObject(body); err != nil {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request JSON does not match the protocol")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, newRequestError(ErrorCodeInvalidJSON, "helper request JSON does not match the protocol")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Request{}, err
	}
	return request, nil
}

// DecodeResponse reads exactly one bounded strict JSON response.
func DecodeResponse(reader io.Reader) (Response, error) {
	body, err := readBoundedResponse(reader)
	if err != nil {
		return Response{}, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return Response{}, errors.New("helper response is empty")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return Response{}, errors.New("helper response contains invalid or duplicate JSON fields")
	}
	if err := validateCanonicalResponseObject(body); err != nil {
		return Response{}, fmt.Errorf("helper response JSON does not match the protocol: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, errors.New("helper response JSON does not match the protocol")
	}
	if err := requireResponseJSONEnd(decoder); err != nil {
		return Response{}, err
	}
	if err := validateResponse(response); err != nil {
		return Response{}, fmt.Errorf("helper response is invalid: %w", err)
	}
	return response, nil
}

// WriteResponse writes one bounded JSON response followed by a newline.
func WriteResponse(writer io.Writer, response Response) error {
	if err := validateResponse(response); err != nil {
		return fmt.Errorf("validate helper response: %w", err)
	}
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal helper response: %w", err)
	}
	if len(body)+1 > MaxResponseBytes {
		return errors.New("helper response exceeds protocol bound")
	}
	body = append(body, '\n')
	written, err := writer.Write(body)
	if err != nil {
		return fmt.Errorf("write helper response: %w", err)
	}
	if written != len(body) {
		return io.ErrShortWrite
	}
	return nil
}

// validateCanonicalResponseObject rejects aliases at every response object boundary.
func validateCanonicalResponseObject(body []byte) error {
	fields, err := decodeJSONObject(body)
	if err != nil {
		return err
	}
	okBody, found := fields["ok"]
	if !found || bytes.Equal(bytes.TrimSpace(okBody), []byte("null")) {
		return errors.New("helper response is missing ok")
	}
	var ok bool
	if err := json.Unmarshal(okBody, &ok); err != nil {
		return errors.New("helper response ok is not a boolean")
	}

	if ok {
		if err := requireCanonicalJSONFields(fields, "version", "ok", "result"); err != nil {
			return err
		}
		return validateCanonicalResultObject(fields["result"])
	}
	if err := requireCanonicalJSONFields(fields, "version", "ok", "error"); err != nil {
		return err
	}
	return validateCanonicalErrorObject(fields["error"])
}

// validateCanonicalResultObject verifies every success object uses exact protocol field names.
func validateCanonicalResultObject(body []byte) error {
	fields, err := decodeJSONObject(body)
	if err != nil {
		return err
	}
	if err := requireCanonicalJSONFields(fields, "operation", "evidence"); err != nil {
		return err
	}
	evidence, err := decodeJSONObject(fields["evidence"])
	if err != nil {
		return err
	}
	if err := requireCanonicalJSONFields(evidence, "changed", "address", "observation"); err != nil {
		return err
	}
	observation, err := decodeJSONObject(evidence["observation"])
	if err != nil {
		return err
	}
	return requireCanonicalJSONFields(observation, "state", "fingerprint")
}

// validateCanonicalErrorObject verifies every failure object uses exact protocol field names.
func validateCanonicalErrorObject(body []byte) error {
	fields, err := decodeJSONObject(body)
	if err != nil {
		return err
	}
	return requireCanonicalJSONFields(fields, "code", "message")
}

// decodeJSONObject decodes one raw object without accepting null or another JSON kind.
func decodeJSONObject(body []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, errors.New("helper response value is not an object")
	}
	return fields, nil
}

// requireCanonicalJSONFields requires one exact non-null field set at an object boundary.
func requireCanonicalJSONFields(fields map[string]json.RawMessage, names ...string) error {
	if len(fields) != len(names) {
		return errors.New("helper response object has the wrong field count")
	}
	for _, name := range names {
		value, found := fields[name]
		if !found || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("helper response object is missing %s", name)
		}
	}
	return nil
}

// validateResponse rejects response envelopes that cannot be produced by the helper protocol.
func validateResponse(response Response) error {
	if response.Version != ProtocolVersion {
		return errors.New("response version is unsupported")
	}
	if response.OK {
		if response.Result == nil || response.Error != nil {
			return errors.New("successful response must contain only a result")
		}
		return validateOperationResult(*response.Result)
	}
	if response.Result != nil || response.Error == nil {
		return errors.New("failed response must contain only an error")
	}
	if !validResponseErrorCode(response.Error.Code) {
		return errors.New("response error code is unsupported")
	}
	if strings.TrimSpace(response.Error.Message) == "" {
		return errors.New("response error message is empty")
	}
	return nil
}

// validateOperationResult verifies success evidence identifies the allowlisted operation postcondition.
func validateOperationResult(result OperationResult) error {
	if result.Operation != OperationEnsureLoopbackIdentity && result.Operation != OperationReleaseLoopbackIdentity {
		return errors.New("response operation is unsupported")
	}
	if !validApprovedAddress(result.Evidence.Address) {
		return errors.New("response evidence address is not canonical IPv4 loopback")
	}
	if err := result.Evidence.Observation.Validate(); err != nil {
		return errors.New("response evidence observation is invalid")
	}
	expectedState := ObservationOwned
	if result.Operation == OperationReleaseLoopbackIdentity {
		expectedState = ObservationAbsent
	}
	if result.Evidence.Observation.State != expectedState {
		return errors.New("response evidence state does not match the operation")
	}
	return nil
}

// validResponseErrorCode accepts only stable failure values declared by this protocol version.
func validResponseErrorCode(code ErrorCode) bool {
	switch code {
	case ErrorCodeInvalidJSON,
		ErrorCodeRequestTooLarge,
		ErrorCodeInvalidTicket,
		ErrorCodeAuthenticationUnavailable,
		ErrorCodeAuthenticationFailed,
		ErrorCodeReplayedTicket,
		ErrorCodeReplayProtectionUnavailable,
		ErrorCodeMutationUnavailable,
		ErrorCodeMutationFailed:
		return true
	default:
		return false
	}
}

// ServeOnce reads, dispatches, and responds to one helper request without a long-lived listener.
func ServeOnce(ctx context.Context, reader io.Reader, writer io.Writer, dispatcher *Dispatcher) error {
	if dispatcher == nil {
		panic("helper.ServeOnce requires a non-nil dispatcher")
	}

	request, err := DecodeRequest(reader)
	if err != nil {
		if writeErr := WriteResponse(writer, responseForError(err)); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		return err
	}
	response, dispatchErr := dispatcher.Dispatch(ctx, request)
	if writeErr := WriteResponse(writer, response); writeErr != nil {
		return errors.Join(dispatchErr, writeErr)
	}
	return dispatchErr
}

// validateCanonicalRequestObject rejects JSON aliases and keeps mutation data outside the wire protocol.
func validateCanonicalRequestObject(body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return errors.New("helper request is not an object")
	}
	if len(fields) != 2 {
		return errors.New("helper request has the wrong field count")
	}
	for _, field := range []string{"version", "ticket_reference"} {
		value, found := fields[field]
		if !found || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("helper request is missing a required field")
		}
	}
	return nil
}

// readBounded distinguishes an oversized body before JSON decoding allocates beyond the protocol limit.
func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, newRequestError(ErrorCodeInvalidJSON, "helper request could not be read")
	}
	if int64(len(body)) > maximum {
		return nil, newRequestError(ErrorCodeRequestTooLarge, "helper request exceeds the protocol bound")
	}
	return body, nil
}

// readBoundedResponse distinguishes an oversized response before JSON decoding allocates beyond the protocol limit.
func readBoundedResponse(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil {
		return nil, errors.New("helper response could not be read")
	}
	if len(body) > MaxResponseBytes {
		return nil, errors.New("helper response exceeds the protocol bound")
	}
	return body, nil
}

// requireJSONEnd rejects concatenated values that could be interpreted differently by another decoder.
func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return newRequestError(ErrorCodeInvalidJSON, "helper request must contain exactly one JSON value")
	}
	return nil
}

// requireResponseJSONEnd rejects response data after the one admitted JSON value.
func requireResponseJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("helper response must contain exactly one JSON value")
	}
	return nil
}

// rejectDuplicateJSONKeys rejects ambiguous objects before the typed decoder chooses a value.
func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

// walkJSONValue recursively tracks object keys while consuming one JSON value.
func walkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumJSONDepth {
		return errors.New("JSON nesting exceeds the protocol bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return errors.New("JSON object contains a duplicate key")
			}
			for existing := range keys {
				if strings.EqualFold(existing, key) {
					return errors.New("JSON object contains a case-folding key collision")
				}
			}
			keys[key] = struct{}{}
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON value has an unexpected delimiter")
	}
	return nil
}
