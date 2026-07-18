package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEnvelopeGoldenFixtures locks the deterministic cross-client wire shape.
func TestEnvelopeGoldenFixtures(t *testing.T) {
	protocol := Version{Major: 1, Minor: 2}
	deadline := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ranges := []VersionRange{{Min: Version{Major: 1}, Max: Version{Major: 1, Minor: 3}}}

	hello, err := NewHelloEnvelope(Hello{
		ProtocolRanges: ranges,
		Role:           RoleCLI,
		ClientVersion:  "0.1.0",
		Capabilities:   []Capability{"operations.v1", "events.v1"},
	})
	if err != nil {
		t.Fatalf("create hello: %v", err)
	}
	welcome, err := NewWelcomeEnvelope(Welcome{
		Protocol:       protocol,
		ProtocolRanges: ranges,
		Role:           RoleDaemon,
		DaemonVersion:  "0.1.0",
		Capabilities:   []Capability{"operations.v1", "events.v1"},
	})
	if err != nil {
		t.Fatalf("create welcome: %v", err)
	}
	request, err := NewRequestEnvelope(protocol, "req-01J2", "projects.start", deadline, struct {
		ProjectID string `json:"project_id"`
	}{ProjectID: "project.orders"})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	failure, err := NewErrorResponseEnvelope(protocol, "req-01J2", ErrorCodeInternal, errors.New("APP_KEY=secret"))
	if err != nil {
		t.Fatalf("create failure: %v", err)
	}

	fixtures := map[string]Envelope{
		"hello.json":          hello,
		"welcome.json":        welcome,
		"request.json":        request,
		"error_response.json": failure,
	}
	for name, envelope := range fixtures {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		encoded = append(encoded, '\n')
		golden, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(encoded) != string(golden) {
			t.Fatalf("%s mismatch\n got: %s\nwant: %s", name, encoded, golden)
		}
	}
}

// TestEnvelopeToleratesUnknownAdditiveFields verifies an older peer can decode
// both outer and typed-payload additions within a protocol major.
func TestEnvelopeToleratesUnknownAdditiveFields(t *testing.T) {
	encoded := `{
		"kind":"request",
		"protocol":{"major":1,"minor":2,"patch":99},
		"request_id":"req-1",
		"method":"projects.list",
		"deadline":"2026-07-18T12:00:00Z",
		"trace_hint":{"future":true},
		"payload":{"project_id":"project.orders","future_filter":"active"}
	}`
	var envelope Envelope
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("validate envelope: %v", err)
	}
	payload, err := DecodePayload[struct {
		ProjectID string `json:"project_id"`
	}](envelope)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ProjectID != "project.orders" {
		t.Fatalf("project ID = %q", payload.ProjectID)
	}
}

// TestHandshakeEnvelopeToleratesUnknownPayloadFields verifies additive handshake
// metadata does not prevent an older daemon from negotiating known fields.
func TestHandshakeEnvelopeToleratesUnknownPayloadFields(t *testing.T) {
	encoded := `{
		"kind":"hello",
		"future_outer":true,
		"payload":{
			"protocol_ranges":[{"min":{"major":1,"minor":0},"max":{"major":1,"minor":2}}],
			"role":"goforj_session",
			"client_version":"0.1.0",
			"capabilities":["session.v1","future.v9"],
			"future_session_mode":"attached"
		}
	}`
	var envelope Envelope
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("validate envelope: %v", err)
	}
}

// TestRequestContextAppliesDeadline verifies nil parents are normalized and an
// expired request is rejected before dispatch starts work.
func TestRequestContextAppliesDeadline(t *testing.T) {
	deadline := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	envelope, err := NewRequestEnvelope(Version{Major: 1}, "req-1", "projects.list", deadline, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	requestContext, cancel, err := envelope.RequestContextAt(nil, deadline.Add(-time.Second))
	if err != nil {
		t.Fatalf("derive request context: %v", err)
	}
	defer cancel()
	gotDeadline, ok := requestContext.Deadline()
	if !ok || !gotDeadline.Equal(deadline) {
		t.Fatalf("deadline = %v, %t", gotDeadline, ok)
	}

	if _, _, err := envelope.RequestContextAt(context.Background(), deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired error = %v", err)
	}
}

// TestRequestRejectsNonUTCDeadline verifies all peers observe one timestamp
// convention regardless of local timezone.
func TestRequestRejectsNonUTCDeadline(t *testing.T) {
	deadline := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.FixedZone("local", 3600))
	envelope := Envelope{
		Kind:      KindRequest,
		Protocol:  versionPointer(Version{Major: 1}),
		RequestID: "req-1",
		Method:    "projects.list",
		Deadline:  &deadline,
		Payload:   json.RawMessage(`{}`),
	}
	if err := envelope.Validate(); err == nil || !strings.Contains(err.Error(), "UTC") {
		t.Fatalf("error = %v", err)
	}
}

// TestEventRejectsUnrepresentableSequence verifies every protocol counter remains exact in JavaScript clients.
func TestEventRejectsUnrepresentableSequence(t *testing.T) {
	if _, err := NewEventEnvelope(Version{Major: 1}, "snapshot.changed", MaximumSequence, struct{}{}); err != nil {
		t.Fatalf("maximum sequence rejected: %v", err)
	}
	if _, err := NewEventEnvelope(Version{Major: 1}, "snapshot.changed", MaximumSequence+1, struct{}{}); err == nil {
		t.Fatal("inexact JavaScript sequence accepted")
	}
}

// TestEnvelopeRejectsAmbiguousKindFields verifies request routing cannot be
// smuggled through another message kind.
func TestEnvelopeRejectsAmbiguousKindFields(t *testing.T) {
	protocol := versionPointer(Version{Major: 1})
	sequence := uint64(1)
	deadline := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []Envelope{
		{Kind: KindCancel, Protocol: protocol, RequestID: "req-1", Payload: json.RawMessage(`{}`)},
		{Kind: KindEvent, Protocol: protocol, RequestID: "req-1", Name: "snapshot.changed", Sequence: &sequence, Payload: json.RawMessage(`{}`)},
		{Kind: KindRequest, Protocol: protocol, RequestID: "req-1", Method: "projects.list", Deadline: &deadline, Payload: json.RawMessage(`{}`), Error: &WireError{Code: ErrorCodeInternal, Message: "Harbor could not complete the request."}},
		{Kind: KindResponse, Protocol: protocol, RequestID: "req-1", Payload: json.RawMessage(`{}`), Error: &WireError{Code: ErrorCodeInternal, Message: "Harbor could not complete the request."}},
	}
	for _, envelope := range tests {
		if err := envelope.Validate(); err == nil {
			t.Fatalf("envelope %#v accepted", envelope)
		}
	}
}
