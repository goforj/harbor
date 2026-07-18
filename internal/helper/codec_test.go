package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

// TestDecodeRequestRejectsAmbiguousJSON covers size, shape, duplicate, and framing failures.
func TestDecodeRequestRejectsAmbiguousJSON(t *testing.T) {
	reference := strings.Repeat("a", ticketReferenceLength)
	validBody := `{"version":1,"ticket_reference":"` + reference + `"}`
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
		{name: "missing reference", body: `{"version":1}`, code: ErrorCodeInvalidJSON},
		{name: "null version", body: `{"version":null,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "null reference", body: `{"version":1,"ticket_reference":null}`, code: ErrorCodeInvalidJSON},
		{name: "case alias version", body: `{"Version":1,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "case alias reference", body: `{"version":1,"Ticket_Reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "camel alias reference", body: `{"version":1,"ticketReference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "case-fold collision", body: `{"version":1,"Version":1,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "duplicate version", body: `{"version":1,"version":1,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
		{name: "duplicate escaped version", body: `{"version":1,"\u0076ersion":1,"ticket_reference":"` + reference + `"}`, code: ErrorCodeInvalidJSON},
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
	if err := WriteResponse(errorWriter{}, Response{Version: ProtocolVersion, OK: true}); err == nil {
		t.Fatal("expected writer error")
	}
	if err := WriteResponse(shortWriter{}, Response{Version: ProtocolVersion, OK: true}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want io.ErrShortWrite", err)
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
	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}
