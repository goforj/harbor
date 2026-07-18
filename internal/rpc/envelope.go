package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	// MaximumSequence keeps protocol counters representable by Harbor's durable
	// SQLite journal and every supported platform's signed integer boundary.
	MaximumSequence uint64 = math.MaxInt64
)

// Kind identifies the stable semantic shape carried by an envelope.
type Kind string

const (
	// KindHello starts protocol negotiation.
	KindHello Kind = "hello"
	// KindWelcome completes successful protocol negotiation.
	KindWelcome Kind = "welcome"
	// KindReject terminates unsuccessful protocol negotiation.
	KindReject Kind = "reject"
	// KindRequest invokes one bounded daemon or session operation.
	KindRequest Kind = "request"
	// KindResponse completes a request with either a payload or error.
	KindResponse Kind = "response"
	// KindCancel asks the receiver to cancel an in-flight request.
	KindCancel Kind = "cancel"
	// KindEvent publishes one ordered state or log event.
	KindEvent Kind = "event"
)

// Envelope is the stable outer IPC message. Unknown JSON fields are ignored by
// Go's decoder so newer peers can extend a protocol major additively.
type Envelope struct {
	Kind      Kind            `json:"kind"`
	Protocol  *Version        `json:"protocol,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Deadline  *time.Time      `json:"deadline,omitempty"`
	Name      string          `json:"name,omitempty"`
	Sequence  *uint64         `json:"sequence,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *WireError      `json:"error,omitempty"`
}

// Validate rejects ambiguous envelopes before they reach dispatch.
func (e Envelope) Validate() error {
	switch e.Kind {
	case KindHello:
		return e.validateHello()
	case KindWelcome:
		return e.validateWelcome()
	case KindReject:
		return e.validateReject()
	case KindRequest:
		return e.validateRequest()
	case KindResponse:
		return e.validateResponse()
	case KindCancel:
		return e.validateCancel()
	case KindEvent:
		return e.validateEvent()
	default:
		return fmt.Errorf("unsupported envelope kind %q", e.Kind)
	}
}

// RequestContext derives the request deadline without conflating a wire cancel
// envelope with Go context ownership.
func (e Envelope) RequestContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	return e.RequestContextAt(parent, time.Now())
}

// RequestContextAt derives a deadline relative to an explicit clock value so
// dispatch expiry behavior is deterministic in tests.
func (e Envelope) RequestContextAt(
	parent context.Context,
	now time.Time,
) (context.Context, context.CancelFunc, error) {
	if e.Kind != KindRequest {
		return nil, nil, errors.New("only request envelopes have a request context")
	}
	if err := e.validateRequest(); err != nil {
		return nil, nil, err
	}
	if !now.Before(*e.Deadline) {
		return nil, nil, context.DeadlineExceeded
	}
	if parent == nil {
		parent = context.Background()
	}

	requestContext, cancel := context.WithDeadline(parent, *e.Deadline)

	return requestContext, cancel, nil
}

// DecodePayload decodes a typed payload while tolerating unknown additive JSON fields.
func DecodePayload[T any](envelope Envelope) (T, error) {
	var result T
	if len(envelope.Payload) == 0 {
		return result, errors.New("envelope payload is required")
	}
	if err := json.Unmarshal(envelope.Payload, &result); err != nil {
		return result, fmt.Errorf("decode envelope payload: %w", err)
	}

	return result, nil
}

// NewHelloEnvelope creates the unversioned first message on a connection.
func NewHelloEnvelope(hello Hello) (Envelope, error) {
	ranges, err := CanonicalVersionRanges(hello.ProtocolRanges)
	if err != nil {
		return Envelope{}, fmt.Errorf("protocol ranges: %w", err)
	}
	capabilities, err := CanonicalCapabilities(hello.Capabilities)
	if err != nil {
		return Envelope{}, err
	}
	hello.ProtocolRanges = ranges
	hello.Capabilities = capabilities
	if err := hello.Validate(); err != nil {
		return Envelope{}, err
	}

	return envelopeWithPayload(KindHello, hello)
}

// NewWelcomeEnvelope creates the daemon's negotiated handshake response.
func NewWelcomeEnvelope(welcome Welcome) (Envelope, error) {
	ranges, err := CanonicalVersionRanges(welcome.ProtocolRanges)
	if err != nil {
		return Envelope{}, fmt.Errorf("protocol ranges: %w", err)
	}
	capabilities, err := CanonicalCapabilities(welcome.Capabilities)
	if err != nil {
		return Envelope{}, err
	}
	welcome.ProtocolRanges = ranges
	welcome.Capabilities = capabilities
	if err := welcome.Validate(); err != nil {
		return Envelope{}, err
	}

	envelope, err := envelopeWithPayload(KindWelcome, welcome)
	if err != nil {
		return Envelope{}, err
	}
	envelope.Protocol = versionPointer(welcome.Protocol)

	return envelope, nil
}

// NewRejectEnvelope creates an unversioned handshake failure.
func NewRejectEnvelope(rejection Reject) (Envelope, error) {
	if len(rejection.ProtocolRanges) > 0 {
		ranges, err := CanonicalVersionRanges(rejection.ProtocolRanges)
		if err != nil {
			return Envelope{}, fmt.Errorf("protocol ranges: %w", err)
		}
		rejection.ProtocolRanges = ranges
	}
	if err := rejection.Validate(); err != nil {
		return Envelope{}, err
	}

	return envelopeWithPayload(KindReject, rejection)
}

// NewRequestEnvelope creates a request with a caller-supplied stable ID and deadline.
func NewRequestEnvelope(
	protocol Version,
	requestID string,
	method string,
	deadline time.Time,
	payload any,
) (Envelope, error) {
	if payload == nil {
		payload = struct{}{}
	}
	envelope, err := envelopeWithPayload(KindRequest, payload)
	if err != nil {
		return Envelope{}, err
	}
	deadline = deadline.UTC()
	envelope.Protocol = versionPointer(protocol)
	envelope.RequestID = requestID
	envelope.Method = method
	envelope.Deadline = &deadline

	return envelope, envelope.Validate()
}

// NewResponseEnvelope creates a successful response for one request.
func NewResponseEnvelope(protocol Version, requestID string, payload any) (Envelope, error) {
	envelope, err := envelopeWithPayload(KindResponse, payload)
	if err != nil {
		return Envelope{}, err
	}
	envelope.Protocol = versionPointer(protocol)
	envelope.RequestID = requestID

	return envelope, envelope.Validate()
}

// NewErrorResponseEnvelope creates a redacted response without serializing the cause.
func NewErrorResponseEnvelope(
	protocol Version,
	requestID string,
	code ErrorCode,
	cause error,
) (Envelope, error) {
	wireError := NewWireErrorFromCause(code, cause)
	envelope := Envelope{
		Kind:      KindResponse,
		Protocol:  versionPointer(protocol),
		RequestID: requestID,
		Error:     &wireError,
	}

	return envelope, envelope.Validate()
}

// NewCancelEnvelope creates a cancellation request for one in-flight request ID.
func NewCancelEnvelope(protocol Version, requestID string) (Envelope, error) {
	envelope := Envelope{
		Kind:      KindCancel,
		Protocol:  versionPointer(protocol),
		RequestID: requestID,
	}

	return envelope, envelope.Validate()
}

// NewEventEnvelope creates an ordered event with a connection-scoped sequence.
func NewEventEnvelope(protocol Version, name string, sequence uint64, payload any) (Envelope, error) {
	envelope, err := envelopeWithPayload(KindEvent, payload)
	if err != nil {
		return Envelope{}, err
	}
	envelope.Protocol = versionPointer(protocol)
	envelope.Name = name
	envelope.Sequence = &sequence

	return envelope, envelope.Validate()
}

// validateHello enforces the unversioned first-message shape.
func (e Envelope) validateHello() error {
	if err := e.requireHandshakeShape(false); err != nil {
		return err
	}
	hello, err := DecodePayload[Hello](e)
	if err != nil {
		return err
	}

	return hello.Validate()
}

// validateWelcome enforces agreement between the envelope and handshake payload.
func (e Envelope) validateWelcome() error {
	if err := e.requireHandshakeShape(true); err != nil {
		return err
	}
	welcome, err := DecodePayload[Welcome](e)
	if err != nil {
		return err
	}
	if err := welcome.Validate(); err != nil {
		return err
	}
	if e.Protocol.Compare(welcome.Protocol) != 0 {
		return errors.New("welcome envelope protocol does not match its payload")
	}

	return nil
}

// validateReject enforces an unversioned terminal handshake shape.
func (e Envelope) validateReject() error {
	if err := e.requireHandshakeShape(false); err != nil {
		return err
	}
	rejection, err := DecodePayload[Reject](e)
	if err != nil {
		return err
	}

	return rejection.Validate()
}

// validateRequest enforces bounded dispatch metadata and a required deadline.
func (e Envelope) validateRequest() error {
	if err := e.validateNegotiatedProtocol(); err != nil {
		return err
	}
	if err := validateWireToken("request ID", e.RequestID, maxRequestIDLength); err != nil {
		return err
	}
	if err := validateWireToken("method", e.Method, maxMethodLength); err != nil {
		return err
	}
	if e.Deadline == nil || e.Deadline.IsZero() {
		return errors.New("request deadline is required")
	}
	if _, offset := e.Deadline.Zone(); offset != 0 {
		return errors.New("request deadline must use UTC")
	}
	if err := validatePayload(e.Payload); err != nil {
		return err
	}
	if e.Name != "" || e.Sequence != nil || e.Error != nil {
		return errors.New("request contains fields belonging to another envelope kind")
	}

	return nil
}

// validateResponse requires exactly one success payload or safe error.
func (e Envelope) validateResponse() error {
	if err := e.validateNegotiatedProtocol(); err != nil {
		return err
	}
	if err := validateWireToken("request ID", e.RequestID, maxRequestIDLength); err != nil {
		return err
	}
	if e.Method != "" || e.Deadline != nil || e.Name != "" || e.Sequence != nil {
		return errors.New("response contains fields belonging to another envelope kind")
	}
	hasPayload := len(e.Payload) > 0
	if hasPayload == (e.Error != nil) {
		return errors.New("response must contain exactly one payload or error")
	}
	if hasPayload {
		return validatePayload(e.Payload)
	}

	return e.Error.Validate()
}

// validateCancel keeps cancellation scoped to one negotiated request ID.
func (e Envelope) validateCancel() error {
	if err := e.validateNegotiatedProtocol(); err != nil {
		return err
	}
	if err := validateWireToken("request ID", e.RequestID, maxRequestIDLength); err != nil {
		return err
	}
	if e.Method != "" || e.Deadline != nil || e.Name != "" || e.Sequence != nil || len(e.Payload) > 0 || e.Error != nil {
		return errors.New("cancel contains fields belonging to another envelope kind")
	}

	return nil
}

// validateEvent requires a monotonic sequence and typed event name.
func (e Envelope) validateEvent() error {
	if err := e.validateNegotiatedProtocol(); err != nil {
		return err
	}
	if err := validateWireToken("event name", e.Name, maxEventNameLength); err != nil {
		return err
	}
	if e.Sequence == nil || *e.Sequence == 0 || *e.Sequence > MaximumSequence {
		return fmt.Errorf("event sequence must be between 1 and %d", MaximumSequence)
	}
	if err := validatePayload(e.Payload); err != nil {
		return err
	}
	if e.RequestID != "" || e.Method != "" || e.Deadline != nil || e.Error != nil {
		return errors.New("event contains fields belonging to another envelope kind")
	}

	return nil
}

// requireHandshakeShape keeps negotiation independent from request routing fields.
func (e Envelope) requireHandshakeShape(protocolRequired bool) error {
	if protocolRequired {
		if err := e.validateNegotiatedProtocol(); err != nil {
			return err
		}
	} else if e.Protocol != nil {
		return errors.New("unnegotiated handshake envelope cannot set protocol")
	}
	if err := validatePayload(e.Payload); err != nil {
		return err
	}
	if e.RequestID != "" || e.Method != "" || e.Deadline != nil || e.Name != "" || e.Sequence != nil || e.Error != nil {
		return errors.New("handshake contains fields belonging to another envelope kind")
	}

	return nil
}

// validateNegotiatedProtocol rejects messages sent before a version is selected.
func (e Envelope) validateNegotiatedProtocol() error {
	if e.Protocol == nil {
		return errors.New("negotiated protocol is required")
	}

	return e.Protocol.Validate()
}

// envelopeWithPayload marshals a payload through one deterministic JSON path.
func envelopeWithPayload(kind Kind, payload any) (Envelope, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode envelope payload: %w", err)
	}

	return Envelope{Kind: kind, Payload: encoded}, nil
}

// validatePayload requires one complete JSON value while leaving its schema to dispatch.
func validatePayload(payload json.RawMessage) error {
	if len(payload) == 0 {
		return errors.New("envelope payload is required")
	}
	if !json.Valid(payload) {
		return errors.New("envelope payload is not valid JSON")
	}

	return nil
}

// versionPointer makes the copied negotiated value explicit at construction sites.
func versionPointer(version Version) *Version {
	return &version
}
