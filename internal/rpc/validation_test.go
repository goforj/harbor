package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestEnvelopeConstructorsProduceEveryStableKind verifies all protocol kinds
// pass the same validation used after decoding.
func TestEnvelopeConstructorsProduceEveryStableKind(t *testing.T) {
	protocol := Version{Major: 1}
	deadline := time.Now().UTC().Add(time.Minute)
	ranges := []VersionRange{{Min: protocol, Max: protocol}}
	rejection := Reject{
		ProtocolRanges: ranges,
		Role:           RoleDaemon,
		DaemonVersion:  "0.1.0",
		Error:          NewWireError(ErrorCodeUnsupportedProtocol),
	}
	hello, err := NewHelloEnvelope(Hello{ProtocolRanges: ranges, Role: RoleCLI, ClientVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("create hello: %v", err)
	}
	welcome, err := NewWelcomeEnvelope(Welcome{Protocol: protocol, ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0"})
	if err != nil {
		t.Fatalf("create welcome: %v", err)
	}
	rejected, err := NewRejectEnvelope(rejection)
	if err != nil {
		t.Fatalf("create rejection: %v", err)
	}
	request, err := NewRequestEnvelope(protocol, "req-1", "projects.list", deadline, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	response, err := NewResponseEnvelope(protocol, "req-1", nil)
	if err != nil {
		t.Fatalf("create response: %v", err)
	}
	cancel, err := NewCancelEnvelope(protocol, "req-1")
	if err != nil {
		t.Fatalf("create cancel: %v", err)
	}
	event, err := NewEventEnvelope(protocol, "snapshot.changed", 1, struct{}{})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	for _, envelope := range []Envelope{hello, welcome, rejected, request, response, cancel, event} {
		if err := envelope.Validate(); err != nil {
			t.Fatalf("validate %s: %v", envelope.Kind, err)
		}
	}
	if NewWireError(ErrorCode("future")).Code != ErrorCodeInternal {
		t.Fatal("unknown outgoing error code was not redacted to internal")
	}
	if NewWireError(ErrorCodeUnavailable).Retryable != true {
		t.Fatal("unavailable error is not retryable")
	}
}

// TestEnvelopeConstructorFailures verifies invalid negotiation, routing, and
// non-JSON payload branches fail before anything reaches the transport.
func TestEnvelopeConstructorFailures(t *testing.T) {
	protocol := Version{Major: 1}
	ranges := []VersionRange{{Min: protocol, Max: protocol}}
	invalidPayload := func() {}
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "hello range", run: func() error { _, err := NewHelloEnvelope(Hello{}); return err }},
		{name: "hello capability", run: func() error {
			_, err := NewHelloEnvelope(Hello{ProtocolRanges: ranges, Role: RoleCLI, ClientVersion: "0.1.0", Capabilities: []Capability{"bad capability"}})
			return err
		}},
		{name: "hello role", run: func() error {
			_, err := NewHelloEnvelope(Hello{ProtocolRanges: ranges, Role: RoleDaemon, ClientVersion: "0.1.0"})
			return err
		}},
		{name: "welcome range", run: func() error { _, err := NewWelcomeEnvelope(Welcome{}); return err }},
		{name: "welcome capability", run: func() error {
			_, err := NewWelcomeEnvelope(Welcome{Protocol: protocol, ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0", Capabilities: []Capability{"bad capability"}})
			return err
		}},
		{name: "welcome selected", run: func() error {
			_, err := NewWelcomeEnvelope(Welcome{Protocol: Version{Major: 1, Minor: 2}, ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0"})
			return err
		}},
		{name: "reject range", run: func() error {
			_, err := NewRejectEnvelope(Reject{ProtocolRanges: []VersionRange{{}}, Role: RoleDaemon, Error: NewWireError(ErrorCodeInternal)})
			return err
		}},
		{name: "reject role", run: func() error {
			_, err := NewRejectEnvelope(Reject{Role: RoleCLI, Error: NewWireError(ErrorCodeInternal)})
			return err
		}},
		{name: "request payload", run: func() error {
			_, err := NewRequestEnvelope(protocol, "req-1", "projects.list", time.Now().UTC().Add(time.Minute), invalidPayload)
			return err
		}},
		{name: "response payload", run: func() error { _, err := NewResponseEnvelope(protocol, "req-1", invalidPayload); return err }},
		{name: "event payload", run: func() error { _, err := NewEventEnvelope(protocol, "snapshot.changed", 1, invalidPayload); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil {
				t.Fatal("constructor succeeded")
			}
		})
	}
}

// TestEnvelopeValidationFailures verifies required fields and discriminator
// agreement remain strict while unknown additive fields stay tolerated.
func TestEnvelopeValidationFailures(t *testing.T) {
	protocol := versionPointer(Version{Major: 1})
	deadline := time.Now().UTC().Add(time.Minute)
	sequence := uint64(1)
	safeError := NewWireError(ErrorCodeInternal)
	tests := []Envelope{
		{},
		{Kind: KindHello, Protocol: protocol, Payload: json.RawMessage(`{}`)},
		{Kind: KindHello, Payload: json.RawMessage(`not-json`)},
		{Kind: KindWelcome, Protocol: protocol, Payload: json.RawMessage(`{"protocol":{"major":1,"minor":1},"protocol_ranges":[{"min":{"major":1,"minor":0},"max":{"major":1,"minor":1}}],"role":"daemon","daemon_version":"0.1.0","capabilities":[]}`)},
		{Kind: KindReject, Payload: json.RawMessage(`{}`)},
		{Kind: KindRequest, Protocol: protocol, RequestID: "", Method: "projects.list", Deadline: &deadline, Payload: json.RawMessage(`{}`)},
		{Kind: KindRequest, Protocol: protocol, RequestID: "req-1", Method: "", Deadline: &deadline, Payload: json.RawMessage(`{}`)},
		{Kind: KindRequest, Protocol: protocol, RequestID: "req-1", Method: "projects.list", Payload: json.RawMessage(`{}`)},
		{Kind: KindRequest, Protocol: protocol, RequestID: "req-1", Method: "projects.list", Deadline: &deadline},
		{Kind: KindResponse, Protocol: protocol, RequestID: "req-1"},
		{Kind: KindResponse, Protocol: protocol, RequestID: "req-1", Error: &WireError{}},
		{Kind: KindCancel, Protocol: protocol, RequestID: ""},
		{Kind: KindEvent, Protocol: protocol, Name: "", Sequence: &sequence, Payload: json.RawMessage(`{}`)},
		{Kind: KindEvent, Protocol: protocol, Name: "snapshot.changed", Payload: json.RawMessage(`{}`)},
		{Kind: KindEvent, Protocol: protocol, Name: "snapshot.changed", Sequence: &sequence, Payload: json.RawMessage(`{}`), Error: &safeError},
	}
	for _, envelope := range tests {
		if err := envelope.Validate(); err == nil {
			t.Fatalf("envelope %#v accepted", envelope)
		}
	}
}

// TestRequestContextConvenience verifies the real-clock helper and non-request
// rejection paths share the deterministic context derivation behavior.
func TestRequestContextConvenience(t *testing.T) {
	envelope, err := NewRequestEnvelope(Version{Major: 1}, "req-1", "projects.list", time.Now().UTC().Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	requestContext, cancel, err := envelope.RequestContext(context.Background())
	if err != nil {
		t.Fatalf("derive request context: %v", err)
	}
	cancel()
	if requestContext.Err() == nil {
		t.Fatal("cancel did not close request context")
	}
	if _, _, err := (Envelope{Kind: KindCancel}).RequestContext(context.Background()); err == nil {
		t.Fatal("cancel envelope received request context")
	}
}

// TestFrameErrorSurfaces verifies frame diagnostics and validation failures are
// bounded and leave subsequent valid operations well-defined.
func TestFrameErrorSurfaces(t *testing.T) {
	sizeError := FrameSizeError{Size: 2, Limit: 1}
	if sizeError.Error() != "frame size 2 exceeds limit 1" {
		t.Fatalf("size error = %q", sizeError.Error())
	}
	if NewWireError(ErrorCodeInternal).Error() != "Harbor could not complete the request." {
		t.Fatal("wire error text changed")
	}

	writer := NewDefaultFrameWriter(&bytes.Buffer{})
	if err := writer.WriteFrame(nil); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("empty write error = %v", err)
	}
	if err := writer.WriteFrame([]byte(`x`)); !errors.Is(err, ErrInvalidFrameJSON) {
		t.Fatalf("invalid JSON error = %v", err)
	}
	if err := writer.WriteEnvelope(Envelope{}); err == nil {
		t.Fatal("invalid envelope written")
	}

	var encoded bytes.Buffer
	frameWriter, err := NewFrameWriter(&encoded, 64)
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	if err := frameWriter.WriteFrame([]byte(`[]`)); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
	frameReader, err := NewFrameReader(&encoded, 64)
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	if _, err := frameReader.ReadEnvelope(); err == nil || !strings.Contains(err.Error(), "envelope") {
		t.Fatalf("read error = %v", err)
	}
}

// TestPayloadAndTokenDiagnostics verifies malformed typed payloads and bounded
// token failures return stable diagnostics without panics.
func TestPayloadAndTokenDiagnostics(t *testing.T) {
	if (Version{Major: 2, Minor: 3}).String() != "2.3" {
		t.Fatal("version string changed")
	}
	if _, err := DecodePayload[struct{}](Envelope{}); err == nil {
		t.Fatal("empty payload decoded")
	}
	if _, err := DecodePayload[struct{}](Envelope{Payload: json.RawMessage(`not-json`)}); err == nil {
		t.Fatal("malformed payload decoded")
	}
	if err := validateWireToken("build version", "v1.2.3+linux.amd64", maxVersionLength); err != nil {
		t.Fatalf("semantic build metadata was rejected: %v", err)
	}
	for _, token := range []string{"", strings.Repeat("a", maxRequestIDLength+1), "unicode-⚓"} {
		if err := validateWireToken("request ID", token, maxRequestIDLength); err == nil {
			t.Fatalf("token %q accepted", token)
		}
	}
}
