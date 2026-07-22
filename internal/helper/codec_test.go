package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestDecodeRequestAcceptsStrictEnvelope verifies the canonical request round trip.
func TestDecodeRequestAcceptsStrictEnvelope(t *testing.T) {
	want := validTestRequest(testTicketReference())
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	got, err := DecodeRequest(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got != want {
		t.Fatalf("decoded request = %#v, want %#v", got, want)
	}
}

// TestWriteRequestWritesValidatedCanonicalJSON verifies launcher input uses one stable newline-delimited envelope.
func TestWriteRequestWritesValidatedCanonicalJSON(t *testing.T) {
	request := validTestRequest(testTicketReference())
	want, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	want = append(want, '\n')

	var output bytes.Buffer
	if err := WriteRequest(&output, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("request body = %q, want %q", output.Bytes(), want)
	}
	decoded, err := DecodeRequest(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("decode written request: %v", err)
	}
	if decoded != request {
		t.Fatalf("decoded request = %#v, want %#v", decoded, request)
	}
}

// TestWriteRequestRejectsInvalidAndFailedWrites verifies invalid authority never reaches helper input.
func TestWriteRequestRejectsInvalidAndFailedWrites(t *testing.T) {
	invalid := Request{Version: ProtocolVersion, TicketReference: "short"}
	var output bytes.Buffer
	if err := WriteRequest(&output, invalid); err == nil {
		t.Fatal("expected invalid request error")
	}
	if output.Len() != 0 {
		t.Fatalf("invalid request wrote %d bytes", output.Len())
	}

	request := validTestRequest(testTicketReference())
	if err := WriteRequest(errorWriter{}, request); err == nil {
		t.Fatal("expected writer error")
	}
	if err := WriteRequest(shortWriter{}, request); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want io.ErrShortWrite", err)
	}
}

// TestDecodeRequestRejectsAmbiguousJSON covers size, shape, duplicate, and framing failures.
func TestDecodeRequestRejectsAmbiguousJSON(t *testing.T) {
	reference := strings.Repeat("a", ticketReferenceLength)
	validBody := `{"version":3,"ticket_reference":"` + reference + `"}`
	tests := []struct {
		name string
		body string
		code ErrorCode
	}{
		{name: "empty", body: "   ", code: ErrorCodeInvalidJSON},
		{name: "malformed", body: `{"version":`, code: ErrorCodeInvalidJSON},
		{name: "null", body: `null`, code: ErrorCodeInvalidJSON},
		{name: "array", body: `[]`, code: ErrorCodeInvalidJSON},
		{name: "string", body: `"request"`, code: ErrorCodeInvalidJSON},
		{name: "number", body: `1`, code: ErrorCodeInvalidJSON},
		{name: "missing version", body: `{"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "missing reference", body: `{"version":3}`, code: ErrorCodeInvalidJSON},
		{name: "null version", body: `{"version":null,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "null reference", body: `{"version":3,"ticket_reference":null}`, code: ErrorCodeInvalidJSON},
		{name: "case alias version", body: `{"Version":3,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "case alias reference", body: `{"version":3,"Ticket_Reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "camel alias reference", body: `{"version":3,"ticketReference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "case-fold collision", body: `{"version":3,"Version":3,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "duplicate version", body: `{"version":3,"version":3,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "duplicate escaped version", body: `{"version":3,"\u0076ersion":3,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "direct ticket", body: strings.TrimSuffix(validBody, "}") + `,"ticket":{}}`, code: ErrorCodeInvalidJSON},
		{name: "direct operation", body: strings.TrimSuffix(validBody, "}") + `,"operation":"ensure_loopback_identity"}`, code: ErrorCodeInvalidJSON},
		{name: "direct address", body: strings.TrimSuffix(validBody, "}") + `,"approved_address":"127.0.0.2"}`, code: ErrorCodeInvalidJSON},
		{name: "selectable adapter", body: strings.TrimSuffix(validBody, "}") + `,"adapter":"file"}`, code: ErrorCodeInvalidJSON},
		{name: "selectable path", body: strings.TrimSuffix(validBody, "}") + `,"path":"/tmp/ticket"}`, code: ErrorCodeInvalidJSON},
		{name: "trailing object", body: validBody + `{}`, code: ErrorCodeInvalidJSON},
		{name: "excessive nesting", body: strings.Repeat("[", maximumJSONDepth+2) + "0" + strings.Repeat("]", maximumJSONDepth+2), code: ErrorCodeInvalidJSON},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeRequest(strings.NewReader(test.body))
			if err == nil {
				t.Fatal("expected decode error")
			}
			if got := requestErrorCode(t, err); got != test.code {
				t.Fatalf("error code = %q, want %q", got, test.code)
			}
		})
	}
}

// TestDecodeRequestRejectsOversizedAndFailedReads verifies the bound applies before decoding.
func TestDecodeRequestRejectsOversizedAndFailedReads(t *testing.T) {
	_, err := DecodeRequest(strings.NewReader(strings.Repeat("x", MaxRequestBytes+1)))
	if err == nil || requestErrorCode(t, err) != ErrorCodeRequestTooLarge {
		t.Fatalf("oversized request error = %v", err)
	}

	_, err = DecodeRequest(errorReader{})
	if err == nil || requestErrorCode(t, err) != ErrorCodeInvalidJSON {
		t.Fatalf("failed read error = %v", err)
	}
}

// TestDecodeResponseAcceptsStrictSuccessAndFailure verifies both protocol envelope shapes round trip.
func TestDecodeResponseAcceptsStrictSuccessAndFailure(t *testing.T) {
	responses := []Response{
		validTestSuccessResponse(),
		validTestPoolSuccessResponse(),
		validTestPoolReleaseSuccessResponse(),
		validTestFailureResponse(),
	}
	for _, want := range responses {
		body, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		got, err := DecodeResponse(bytes.NewReader(append(body, '\n')))
		if err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded response = %#v, want %#v", got, want)
		}
	}
}

// TestDecodeResponseRejectsAmbiguousPoolEvidenceJSON covers the aggregate result's conditional field set and exact nesting.
func TestDecodeResponseRejectsAmbiguousPoolEvidenceJSON(t *testing.T) {
	validBody, err := json.Marshal(validTestPoolSuccessResponse())
	if err != nil {
		t.Fatalf("marshal pool response: %v", err)
	}
	valid := string(validBody)
	nullIdentity := func() string {
		var envelope map[string]any
		if err := json.Unmarshal(validBody, &envelope); err != nil {
			t.Fatalf("unmarshal pool response: %v", err)
		}
		result := envelope["result"].(map[string]any)
		poolEvidence := result["pool_evidence"].(map[string]any)
		identities := poolEvidence["identities"].([]any)
		identities[0] = nil
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal null identity response: %v", err)
		}
		return string(body)
	}()

	tests := []struct {
		name string
		body string
	}{
		{name: "null pool evidence", body: `{"version":3,"ok":true,"result":{"operation":"ensure_loopback_pool","pool_evidence":null}}`},
		{name: "scalar evidence instead", body: strings.Replace(valid, `"pool_evidence"`, `"evidence"`, 1)},
		{name: "both evidence fields", body: strings.Replace(valid, `"pool_evidence":`, `"evidence":{"changed":true,"address":"127.77.0.8","observation":{"state":"owned","fingerprint":"`+strings.Repeat("a", fingerprintLength)+`"}},"pool_evidence":`, 1)},
		{name: "pool evidence alias", body: strings.Replace(valid, `"pool_evidence"`, `"Pool_Evidence"`, 1)},
		{name: "duplicate pool evidence", body: strings.Replace(valid, `"pool_evidence":`, `"pool_evidence":null,"pool_evidence":`, 1)},
		{name: "null pool", body: strings.Replace(valid, `"pool":"127.77.0.8/29"`, `"pool":null`, 1)},
		{name: "pool alias", body: strings.Replace(valid, `"pool"`, `"Pool"`, 1)},
		{name: "duplicate pool", body: strings.Replace(valid, `"pool":`, `"pool":null,"pool":`, 1)},
		{name: "null identities", body: `{"version":3,"ok":true,"result":{"operation":"ensure_loopback_pool","pool_evidence":{"pool":"127.77.0.8/29","identities":null}}}`},
		{name: "identities alias", body: strings.Replace(valid, `"identities"`, `"Identities"`, 1)},
		{name: "unknown pool field", body: strings.Replace(valid, `"identities":`, `"interface":"lo0","identities":`, 1)},
		{name: "identity field alias", body: strings.Replace(valid, `"changed"`, `"Changed"`, 1)},
		{name: "null identity", body: nullIdentity},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeResponse(strings.NewReader(test.body)); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

// TestDecodeResponseRejectsAmbiguousJSON covers aliases, extra fields, duplicates, and framing failures.
func TestDecodeResponseRejectsAmbiguousJSON(t *testing.T) {
	fingerprint := strings.Repeat("a", fingerprintLength)
	success := `{"version":3,"ok":true,"result":{"operation":"release_loopback_identity","evidence":{"changed":true,"address":"127.77.0.10","observation":{"state":"absent","fingerprint":"` + fingerprint + `"}}}}`
	failure := `{"version":3,"ok":false,"error":{"code":"mutation_failed","message":"helper operation failed"}}`
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: "   "},
		{name: "malformed", body: `{"version":`},
		{name: "null", body: `null`},
		{name: "array", body: `[]`},
		{name: "missing ok", body: `{"version":3,"result":{}}`},
		{name: "null ok", body: `{"version":3,"ok":null,"error":{}}`},
		{name: "case alias ok", body: `{"version":3,"OK":false,"error":{"code":"mutation_failed","message":"failed"}}`},
		{name: "case alias version", body: strings.Replace(failure, `"version"`, `"Version"`, 1)},
		{name: "duplicate version", body: strings.Replace(failure, `{"version":3`, `{"version":3,"version":3`, 1)},
		{name: "case fold collision", body: strings.Replace(failure, `{"version":3`, `{"version":3,"Version":3`, 1)},
		{name: "unknown top level", body: strings.TrimSuffix(failure, "}") + `,"pid":42}`},
		{name: "both result and error", body: strings.TrimSuffix(success, "}") + `,"error":{"code":"mutation_failed","message":"failed"}}`},
		{name: "result alias", body: strings.Replace(success, `"result"`, `"Result"`, 1)},
		{name: "operation alias", body: strings.Replace(success, `"operation"`, `"Operation"`, 1)},
		{name: "unknown result field", body: strings.Replace(success, `"evidence":`, `"pid":42,"evidence":`, 1)},
		{name: "evidence alias", body: strings.Replace(success, `"evidence"`, `"Evidence"`, 1)},
		{name: "unknown evidence field", body: strings.Replace(success, `"changed":true`, `"changed":true,"interface":"lo0"`, 1)},
		{name: "observation alias", body: strings.Replace(success, `"observation"`, `"Observation"`, 1)},
		{name: "unknown observation field", body: strings.Replace(success, `"state":"absent"`, `"state":"absent","owner":"harbor"`, 1)},
		{name: "error alias", body: strings.Replace(failure, `"error"`, `"Error"`, 1)},
		{name: "unknown error field", body: strings.Replace(failure, `"message":`, `"host":"local","message":`, 1)},
		{name: "trailing object", body: failure + `{}`},
		{name: "excessive nesting", body: strings.Repeat("[", maximumJSONDepth+2) + "0" + strings.Repeat("]", maximumJSONDepth+2)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeResponse(strings.NewReader(test.body)); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

// TestDecodeResponseRejectsInvalidDomainValues verifies decoded values cannot escape protocol semantics.
func TestDecodeResponseRejectsInvalidDomainValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Response)
	}{
		{name: "previous version", mutate: func(response *Response) { response.Version-- }},
		{name: "unsupported version", mutate: func(response *Response) { response.Version++ }},
		{name: "success missing result", mutate: func(response *Response) { response.Result = nil }},
		{name: "success with error", mutate: func(response *Response) {
			response.Error = &ResponseError{Code: ErrorCodeMutationFailed, Message: "failed"}
		}},
		{name: "unknown operation", mutate: func(response *Response) { response.Result.Operation = "run_command" }},
		{name: "non loopback address", mutate: func(response *Response) { response.Result.Evidence.Address = "192.0.2.10" }},
		{name: "IPv6 loopback address", mutate: func(response *Response) { response.Result.Evidence.Address = "::1" }},
		{name: "noncanonical address", mutate: func(response *Response) { response.Result.Evidence.Address = "127.077.0.10" }},
		{name: "unknown observation", mutate: func(response *Response) { response.Result.Evidence.Observation.State = "foreign" }},
		{name: "bad fingerprint", mutate: func(response *Response) { response.Result.Evidence.Observation.Fingerprint = "bad" }},
		{name: "state mismatch", mutate: func(response *Response) { response.Result.Evidence.Observation.State = ObservationOwned }},
		{name: "legacy with pool evidence", mutate: func(response *Response) {
			poolEvidence := testPoolMutationEvidence("127.77.0.8/29")
			response.Result.PoolEvidence = &poolEvidence
		}},
		{name: "failure with result", mutate: func(response *Response) {
			response.OK = false
			response.Error = &ResponseError{Code: ErrorCodeMutationFailed, Message: "failed"}
		}},
		{name: "failure missing error", mutate: func(response *Response) {
			response.OK = false
			response.Result = nil
		}},
		{name: "unknown error code", mutate: func(response *Response) {
			*response = validTestFailureResponse()
			response.Error.Code = "host_failed"
		}},
		{name: "blank error message", mutate: func(response *Response) {
			*response = validTestFailureResponse()
			response.Error.Message = " \t "
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := validTestSuccessResponse()
			test.mutate(&response)
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			if _, err := DecodeResponse(bytes.NewReader(body)); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

// TestDecodeResponseRejectsInvalidPoolEvidenceValues enforces the exact-eight ordered owned pool postcondition.
func TestDecodeResponseRejectsInvalidPoolEvidenceValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Response)
	}{
		{name: "missing pool evidence", mutate: func(response *Response) { response.Result.PoolEvidence = nil }},
		{name: "scalar evidence present", mutate: func(response *Response) {
			response.Result.Evidence = MutationEvidence{
				Address: "127.77.0.8",
				Observation: ExpectedObservation{
					State:       ObservationOwned,
					Fingerprint: strings.Repeat("a", fingerprintLength),
				},
			}
		}},
		{name: "noncanonical pool", mutate: func(response *Response) { response.Result.PoolEvidence.Pool = "127.77.0.9/29" }},
		{name: "non loopback pool", mutate: func(response *Response) { response.Result.PoolEvidence.Pool = "192.0.2.8/29" }},
		{name: "wrong prefix", mutate: func(response *Response) { response.Result.PoolEvidence.Pool = "127.77.0.0/24" }},
		{name: "seven identities", mutate: func(response *Response) {
			response.Result.PoolEvidence.Identities = response.Result.PoolEvidence.Identities[:7]
		}},
		{name: "nine identities", mutate: func(response *Response) {
			response.Result.PoolEvidence.Identities = append(response.Result.PoolEvidence.Identities, response.Result.PoolEvidence.Identities[7])
		}},
		{name: "wrong address", mutate: func(response *Response) { response.Result.PoolEvidence.Identities[4].Address = "127.77.0.13" }},
		{name: "wrong order", mutate: func(response *Response) {
			identities := response.Result.PoolEvidence.Identities
			identities[1], identities[2] = identities[2], identities[1]
		}},
		{name: "absent postcondition", mutate: func(response *Response) {
			response.Result.PoolEvidence.Identities[6].Observation.State = ObservationAbsent
		}},
		{name: "bad fingerprint", mutate: func(response *Response) {
			response.Result.PoolEvidence.Identities[7].Observation.Fingerprint = "bad"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := validTestPoolSuccessResponse()
			test.mutate(&response)
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			if _, err := DecodeResponse(bytes.NewReader(body)); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

// TestDecodeResponseRejectsOversizedAndFailedReads verifies the response bound applies before decoding.
func TestDecodeResponseRejectsOversizedAndFailedReads(t *testing.T) {
	if _, err := DecodeResponse(strings.NewReader(strings.Repeat("x", MaxResponseBytes+1))); err == nil {
		t.Fatal("expected oversized response error")
	}
	if _, err := DecodeResponse(errorReader{}); err == nil {
		t.Fatal("expected failed read error")
	}
}

// TestWriteResponseWritesBoundedJSON verifies the response envelope and newline framing.
func TestWriteResponseWritesBoundedJSON(t *testing.T) {
	var output bytes.Buffer
	response := Response{
		Version: ProtocolVersion,
		OK:      false,
		Error: &ResponseError{
			Code:    ErrorCodeMutationUnavailable,
			Message: "helper platform mutation is unavailable",
		},
	}
	if err := WriteResponse(&output, response); err != nil {
		t.Fatalf("write response: %v", err)
	}
	if !bytes.HasSuffix(output.Bytes(), []byte{'\n'}) {
		t.Fatalf("response is not newline terminated: %q", output.String())
	}
	var decoded Response
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Error == nil || decoded.Error.Code != ErrorCodeMutationUnavailable {
		t.Fatalf("unexpected decoded response: %#v", decoded)
	}
}

// TestWriteResponsePreservesLegacySuccessJSON guards the byte shape consumed by existing single-address clients.
func TestWriteResponsePreservesLegacySuccessJSON(t *testing.T) {
	want := `{"version":3,"ok":true,"result":{"operation":"release_loopback_identity","evidence":{"changed":true,"address":"127.77.0.10","observation":{"state":"absent","fingerprint":"` + strings.Repeat("a", fingerprintLength) + `"}}}}` + "\n"
	var output bytes.Buffer
	if err := WriteResponse(&output, validTestSuccessResponse()); err != nil {
		t.Fatalf("write response: %v", err)
	}
	if output.String() != want {
		t.Fatalf("response body = %q, want %q", output.String(), want)
	}
}

// TestWriteResponseWritesPoolEvidenceWithinBound verifies the complete eight-address result fits the unchanged response limit.
func TestWriteResponseWritesPoolEvidenceWithinBound(t *testing.T) {
	want := validTestPoolSuccessResponse()
	var output bytes.Buffer
	if err := WriteResponse(&output, want); err != nil {
		t.Fatalf("write response: %v", err)
	}
	if output.Len() > MaxResponseBytes {
		t.Fatalf("pool response size = %d, want at most %d", output.Len(), MaxResponseBytes)
	}
	if bytes.Contains(output.Bytes(), []byte(`"evidence":`)) {
		t.Fatalf("pool response contains scalar evidence: %s", output.Bytes())
	}
	got, err := DecodeResponse(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("decode pool response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded response = %#v, want %#v", got, want)
	}
}

// TestWriteResponseRejectsOversizedAndFailedWrites verifies output cannot escape its protocol bound.
func TestWriteResponseRejectsOversizedAndFailedWrites(t *testing.T) {
	response := Response{
		Version: ProtocolVersion,
		Error: &ResponseError{
			Code:    ErrorCodeMutationFailed,
			Message: strings.Repeat("x", MaxResponseBytes),
		},
	}
	if err := WriteResponse(io.Discard, response); err == nil {
		t.Fatal("expected oversized response error")
	}
	valid := validTestSuccessResponse()
	if err := WriteResponse(errorWriter{}, valid); err == nil {
		t.Fatal("expected writer error")
	}
	if err := WriteResponse(shortWriter{}, valid); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want io.ErrShortWrite", err)
	}
}

// TestWriteResponseRejectsInvalidShape verifies the helper cannot emit contradictory envelopes.
func TestWriteResponseRejectsInvalidShape(t *testing.T) {
	response := validTestSuccessResponse()
	response.Error = &ResponseError{Code: ErrorCodeMutationFailed, Message: "failed"}
	var output bytes.Buffer
	if err := WriteResponse(&output, response); err == nil {
		t.Fatal("expected invalid response error")
	}
	if output.Len() != 0 {
		t.Fatalf("invalid response wrote %d bytes", output.Len())
	}
}

// TestServeOnceWritesSuccessAndFailure verifies one-shot callers always receive a structured response.
func TestServeOnceWritesSuccessAndFailure(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, validTestTicket(now, OperationEnsureLoopbackIdentity)), newTestClock(now), newTestReplayGuard(), newTestLoopbackHandler())
	requestBody, err := json.Marshal(validTestRequest(reference))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var output bytes.Buffer
	if err := ServeOnce(context.Background(), bytes.NewReader(requestBody), &output, dispatcher); err != nil {
		t.Fatalf("serve valid request: %v", err)
	}
	if response := decodeTestResponse(t, output.Bytes()); !response.OK || response.Result == nil {
		t.Fatalf("unexpected success response: %#v", response)
	}

	output.Reset()
	err = ServeOnce(context.Background(), strings.NewReader(`{"path":"/tmp/escape"}`), &output, dispatcher)
	if err == nil {
		t.Fatal("expected invalid request error")
	}
	response := decodeTestResponse(t, output.Bytes())
	if response.OK || response.Error == nil || response.Error.Code != ErrorCodeInvalidJSON {
		t.Fatalf("unexpected failure response: %#v", response)
	}
}

// TestServeOnceReturnsDispatchAndWriteFailures verifies callers receive both operation and transport failures.
func TestServeOnceReturnsDispatchAndWriteFailures(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
	requestBody, err := json.Marshal(validTestRequest(reference))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var output bytes.Buffer
	dispatcher := NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), UnavailableReplayGuard{}, newTestLoopbackHandler())
	err = ServeOnce(context.Background(), bytes.NewReader(requestBody), &output, dispatcher)
	if !errors.Is(err, ErrReplayProtectionUnavailable) {
		t.Fatalf("dispatch error = %v, want replay protection unavailable", err)
	}
	response := decodeTestResponse(t, output.Bytes())
	if response.Error == nil || response.Error.Code != ErrorCodeReplayProtectionUnavailable {
		t.Fatalf("unexpected unavailable response: %#v", response)
	}

	dispatcher = NewDispatcher(newTestTicketRedeemer(reference, ticket), newTestClock(now), newTestReplayGuard(), newTestLoopbackHandler())
	err = ServeOnce(context.Background(), bytes.NewReader(requestBody), errorWriter{}, dispatcher)
	if err == nil {
		t.Fatal("expected response writer error")
	}

	err = ServeOnce(context.Background(), strings.NewReader(`{}`), errorWriter{}, dispatcher)
	if err == nil {
		t.Fatal("expected joined decode and writer error")
	}
}

// TestServeOnceRequiresDispatcher verifies the security dependency cannot be omitted.
func TestServeOnceRequiresDispatcher(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected nil dispatcher panic")
		}
	}()
	_ = ServeOnce(context.Background(), strings.NewReader(`{}`), io.Discard, nil)
}

type errorReader struct{}

// Read injects a source failure before the decoder can observe a complete request.
func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failure")
}

type errorWriter struct{}

// Write injects a destination failure for response handling tests.
func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failure")
}

type shortWriter struct{}

// Write reports an incomplete success so response framing can reject silent truncation.
func (shortWriter) Write(body []byte) (int, error) {
	return len(body) - 1, nil
}

// decodeTestResponse parses one helper response emitted by ServeOnce.
func decodeTestResponse(t *testing.T, body []byte) Response {
	t.Helper()
	response, err := DecodeResponse(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

// validTestSuccessResponse returns canonical release evidence for codec tests.
func validTestSuccessResponse() Response {
	return Response{
		Version: ProtocolVersion,
		OK:      true,
		Result: &OperationResult{
			Operation: OperationReleaseLoopbackIdentity,
			Evidence: MutationEvidence{
				Changed: true,
				Address: "127.77.0.10",
				Observation: ExpectedObservation{
					State:       ObservationAbsent,
					Fingerprint: strings.Repeat("a", fingerprintLength),
				},
			},
		},
	}
}

// validTestPoolSuccessResponse returns the complete canonical aggregate evidence for codec tests.
func validTestPoolSuccessResponse() Response {
	poolEvidence := testPoolMutationEvidence("127.77.0.8/29")
	return Response{
		Version: ProtocolVersion,
		OK:      true,
		Result: &OperationResult{
			Operation:    OperationEnsureLoopbackPool,
			PoolEvidence: &poolEvidence,
		},
	}
}

// validTestPoolReleaseSuccessResponse returns complete absent evidence for one aggregate release.
func validTestPoolReleaseSuccessResponse() Response {
	poolEvidence := testPoolReleaseEvidence("127.77.0.8/29")
	return Response{
		Version: ProtocolVersion,
		OK:      true,
		Result: &OperationResult{
			Operation:    OperationReleaseLoopbackPool,
			PoolEvidence: &poolEvidence,
		},
	}
}

// validTestFailureResponse returns a canonical structured helper failure for codec tests.
func validTestFailureResponse() Response {
	return Response{
		Version: ProtocolVersion,
		OK:      false,
		Error: &ResponseError{
			Code:    ErrorCodeMutationFailed,
			Message: "helper operation failed",
		},
	}
}
