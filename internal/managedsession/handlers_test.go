package managedsession

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// recordingManagedAuthority records typed calls while retaining independently configurable responses.
type recordingManagedAuthority struct {
	registerResponse    RegisterResponse
	publicationResponse ReplacePublicationsResponse
	barrierResponse     BarrierResponse
	registerRequest     RegisterRequest
	publicationRequest  ReplacePublicationsRequest
	barrierRequest      BarrierRequest
	peer                local.PeerIdentity
	registerCalls       int
	publicationCalls    int
	barrierCalls        int
	err                 error
}

// recordingManagedEventAuthority adds the optional ordered event sink without changing the base fixture's capability set.
type recordingManagedEventAuthority struct {
	recordingManagedAuthority
	event      Event
	eventCalls int
	eventErr   error
}

// PublishManagedEvent records one event after the generic session boundary has validated its envelope.
func (authority *recordingManagedEventAuthority) PublishManagedEvent(_ context.Context, _ local.PeerIdentity, event Event) error {
	authority.event = event
	authority.eventCalls++
	return authority.eventErr
}

// RegisterManagedSession records and returns the configured registration response.
func (authority *recordingManagedAuthority) RegisterManagedSession(_ context.Context, peer local.PeerIdentity, request RegisterRequest) (RegisterResponse, error) {
	authority.peer = peer
	authority.registerRequest = request
	authority.registerCalls++
	return authority.registerResponse, authority.err
}

// ReplaceManagedPublications records and returns the configured publication response.
func (authority *recordingManagedAuthority) ReplaceManagedPublications(_ context.Context, peer local.PeerIdentity, request ReplacePublicationsRequest) (ReplacePublicationsResponse, error) {
	authority.peer = peer
	authority.publicationRequest = request
	authority.publicationCalls++
	return authority.publicationResponse, authority.err
}

// AcknowledgeManagedBarrier records and returns the configured barrier response.
func (authority *recordingManagedAuthority) AcknowledgeManagedBarrier(_ context.Context, peer local.PeerIdentity, request BarrierRequest) (BarrierResponse, error) {
	authority.peer = peer
	authority.barrierRequest = request
	authority.barrierCalls++
	return authority.barrierResponse, authority.err
}

// managedSessionHandlerTestPeer returns the authenticated GoForj identity used by handler fixtures.
func managedSessionHandlerTestPeer() local.PeerIdentity {
	return local.PeerIdentity{UserID: "501", ProcessID: 8123}
}

// managedSessionHandlerTestSessionPeer returns one negotiated session peer with the managed capability.
func managedSessionHandlerTestSessionPeer() session.Peer {
	return session.Peer{
		Role:         rpc.RoleGoForjSession,
		BuildVersion: "v0.20.1",
		Protocol:     managedSessionProtocolV1,
		Capabilities: []rpc.Capability{CapabilityV1},
	}
}

// managedSessionHandlerTestAuthority returns valid responses correlated to the protocol fixtures.
func managedSessionHandlerTestAuthority() *recordingManagedAuthority {
	fence := managedSessionTestFence()
	return &recordingManagedAuthority{
		registerResponse:    RegisterResponse{SchemaVersion: SchemaVersion, Fence: fence, AttachmentTicket: "ticket-orders-1"},
		publicationResponse: ReplacePublicationsResponse{SchemaVersion: SchemaVersion, Fence: fence, Accepted: true, PublicationCount: 1},
		barrierResponse:     BarrierResponse{SchemaVersion: SchemaVersion, Fence: fence, Phase: BarrierPhaseCompose, Acknowledged: true},
	}
}

// TestManagedSessionHandlerSetDispatchesTypedRequests proves all methods retain authenticated peer identity and exact payloads.
func TestManagedSessionHandlerSetDispatchesTypedRequests(t *testing.T) {
	authority := managedSessionHandlerTestAuthority()
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet() error = %v", err)
	}
	handlers := set.Handlers()
	if len(handlers) != 3 {
		t.Fatalf("handler count = %d, want 3", len(handlers))
	}
	for _, method := range []string{MethodRegister, MethodReplacePublications, MethodBarrier} {
		if handlers[method] == nil {
			t.Fatalf("missing handler %q", method)
		}
	}

	registration := managedSessionTestRequest()
	registrationPayload, err := MarshalRegisterRequest(registration)
	if err != nil {
		t.Fatalf("MarshalRegisterRequest() error = %v", err)
	}
	gotRegistration, err := handlers[MethodRegister](t.Context(), session.Request{Method: MethodRegister, Payload: registrationPayload, Peer: managedSessionHandlerTestSessionPeer()})
	if err != nil {
		t.Fatalf("register handler error = %v", err)
	}
	if !reflect.DeepEqual(gotRegistration, authority.registerResponse) || !reflect.DeepEqual(authority.registerRequest, registration) {
		t.Fatalf("registration dispatch = %#v / %#v, want %#v / %#v", gotRegistration, authority.registerRequest, authority.registerResponse, registration)
	}

	fence := managedSessionTestFence()
	publicationRequest := ReplacePublicationsRequest{SchemaVersion: SchemaVersion, Fence: fence, Publications: []harbordruntime.ManagedEndpointPublication{managedSessionTestPublication(fence, "service:mysql")}}
	publicationPayload, err := MarshalReplacePublicationsRequest(publicationRequest)
	if err != nil {
		t.Fatalf("MarshalReplacePublicationsRequest() error = %v", err)
	}
	gotPublication, err := handlers[MethodReplacePublications](t.Context(), session.Request{Method: MethodReplacePublications, Payload: publicationPayload, Peer: managedSessionHandlerTestSessionPeer()})
	if err != nil {
		t.Fatalf("publication handler error = %v", err)
	}
	if !reflect.DeepEqual(gotPublication, authority.publicationResponse) || !reflect.DeepEqual(authority.publicationRequest, publicationRequest) {
		t.Fatalf("publication dispatch = %#v / %#v", gotPublication, authority.publicationRequest)
	}

	barrierRequest := BarrierRequest{SchemaVersion: SchemaVersion, Fence: fence, Phase: BarrierPhaseCompose, AcceptedProjectIdentity: "orders-dev"}
	barrierPayload, err := MarshalBarrierRequest(barrierRequest)
	if err != nil {
		t.Fatalf("MarshalBarrierRequest() error = %v", err)
	}
	gotBarrier, err := handlers[MethodBarrier](t.Context(), session.Request{Method: MethodBarrier, Payload: barrierPayload, Peer: managedSessionHandlerTestSessionPeer()})
	if err != nil {
		t.Fatalf("barrier handler error = %v", err)
	}
	if !reflect.DeepEqual(gotBarrier, authority.barrierResponse) || !reflect.DeepEqual(authority.barrierRequest, barrierRequest) {
		t.Fatalf("barrier dispatch = %#v / %#v", gotBarrier, authority.barrierRequest)
	}
	if authority.peer != managedSessionHandlerTestPeer() {
		t.Fatalf("authority peer = %#v, want %#v", authority.peer, managedSessionHandlerTestPeer())
	}
}

// TestManagedSessionEventHandlerDispatchesOnlyNegotiatedEvents proves events
// use the same role and capability fence as request methods.
func TestManagedSessionEventHandlerDispatchesOnlyNegotiatedEvents(t *testing.T) {
	authority := &recordingManagedEventAuthority{recordingManagedAuthority: *managedSessionHandlerTestAuthority()}
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet() error = %v", err)
	}
	if !reflect.DeepEqual(set.Capabilities(), []rpc.Capability{CapabilityEventsV1}) {
		t.Fatalf("Capabilities() = %#v, want events capability", set.Capabilities())
	}
	event := Event{
		SchemaVersion: SchemaVersion,
		ProjectID:     "orders",
		SessionID:     "session-orders",
		Sequence:      1,
		Timestamp:     "2026-07-21T18:00:00Z",
		Kind:          EventKindLogChunk,
		AppID:         "api",
		Stream:        EventStreamStdout,
		Text:          "ready",
	}
	payload, err := MarshalEvent(event)
	if err != nil {
		t.Fatalf("MarshalEvent() error = %v", err)
	}
	peer := managedSessionHandlerTestSessionPeer()
	peer.Capabilities = append(peer.Capabilities, CapabilityEventsV1)
	if err := set.EventHandler()(t.Context(), session.Event{
		Name:     string(EventKindLogChunk),
		Sequence: event.Sequence,
		Payload:  payload,
		Peer:     peer,
	}); err != nil {
		t.Fatalf("EventHandler() error = %v", err)
	}
	if authority.eventCalls != 1 || !reflect.DeepEqual(authority.event, event) {
		t.Fatalf("event dispatch = %#v / %d, want %#v / 1", authority.event, authority.eventCalls, event)
	}
	if err := set.EventHandler()(t.Context(), session.Event{
		Name:     string(EventKindLogChunk),
		Sequence: event.Sequence + 1,
		Payload:  payload,
		Peer:     peer,
	}); err == nil {
		t.Fatal("EventHandler accepted an envelope sequence mismatch")
	}
	if authority.eventCalls != 1 {
		t.Fatalf("event calls after mismatch = %d, want 1", authority.eventCalls)
	}
	if err := set.EventHandler()(t.Context(), session.Event{
		Name:     string(EventKindOutputGap),
		Sequence: event.Sequence,
		Payload:  payload,
		Peer:     peer,
	}); err == nil {
		t.Fatal("EventHandler accepted an event name that disagreed with its kind")
	}
	if authority.eventCalls != 1 {
		t.Fatalf("event calls after name mismatch = %d, want 1", authority.eventCalls)
	}
}

// TestManagedSessionHandlersRejectUnauthenticatedOrMalformedCalls proves no authority method runs before method admission.
func TestManagedSessionHandlersRejectUnauthenticatedOrMalformedCalls(t *testing.T) {
	methods := []struct {
		name      string
		method    string
		valid     []byte
		callCount func(*recordingManagedAuthority) int
	}{
		{name: "register", method: MethodRegister, valid: mustManagedSessionHandlerPayload(t, MarshalRegisterRequest, managedSessionTestRequest), callCount: func(authority *recordingManagedAuthority) int { return authority.registerCalls }},
		{name: "publication", method: MethodReplacePublications, valid: mustManagedSessionHandlerPayload(t, MarshalReplacePublicationsRequest, func() ReplacePublicationsRequest {
			fence := managedSessionTestFence()
			return ReplacePublicationsRequest{SchemaVersion: SchemaVersion, Fence: fence, Publications: []harbordruntime.ManagedEndpointPublication{managedSessionTestPublication(fence, "service:mysql")}}
		}), callCount: func(authority *recordingManagedAuthority) int { return authority.publicationCalls }},
		{name: "barrier", method: MethodBarrier, valid: mustManagedSessionHandlerPayload(t, MarshalBarrierRequest, func() BarrierRequest {
			return BarrierRequest{SchemaVersion: SchemaVersion, Fence: managedSessionTestFence(), Phase: BarrierPhaseCompose, AcceptedProjectIdentity: "orders-dev"}
		}), callCount: func(authority *recordingManagedAuthority) int { return authority.barrierCalls }},
	}
	for _, method := range methods {
		t.Run(method.name, func(t *testing.T) {
			authority := managedSessionHandlerTestAuthority()
			set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
			if err != nil {
				t.Fatalf("NewHandlerSet() error = %v", err)
			}
			for _, test := range []struct {
				name string
				peer session.Peer
				body []byte
			}{
				{name: "wrong role", peer: session.Peer{Role: rpc.RoleCLI, Protocol: managedSessionProtocolV1, Capabilities: []rpc.Capability{CapabilityV1}}, body: method.valid},
				{name: "wrong protocol", peer: session.Peer{Role: rpc.RoleGoForjSession, Protocol: rpc.Version{Major: 1, Minor: 1}, Capabilities: []rpc.Capability{CapabilityV1}}, body: method.valid},
				{name: "missing capability", peer: session.Peer{Role: rpc.RoleGoForjSession, Protocol: managedSessionProtocolV1}, body: method.valid},
				{name: "malformed body", peer: managedSessionHandlerTestSessionPeer(), body: []byte(`{"schema_version":1}`)},
			} {
				t.Run(test.name, func(t *testing.T) {
					_, err := set.Handlers()[method.method](t.Context(), session.Request{Method: method.method, Payload: test.body, Peer: test.peer})
					if err == nil {
						t.Fatal("handler accepted invalid call")
					}
					var handlerError *session.HandlerError
					if !errors.As(err, &handlerError) {
						t.Fatalf("handler error = %v, want HandlerError", err)
					}
					if test.name == "malformed body" {
						if handlerError.Code() != rpc.ErrorCodeInvalidRequest {
							t.Fatalf("handler error code = %s, want invalid_request", handlerError.Code())
						}
					} else if handlerError.Code() != rpc.ErrorCodePermissionDenied {
						t.Fatalf("handler error code = %s, want permission_denied", handlerError.Code())
					}
					if calls := method.callCount(authority); calls != 0 {
						t.Fatalf("authority calls = %d, want 0", calls)
					}
				})
			}
		})
	}
}

// TestManagedSessionHandlerRejectsUnnegotiatedLaunchTicket prevents a client from smuggling optional proof past handshake negotiation.
func TestManagedSessionHandlerRejectsUnnegotiatedLaunchTicket(t *testing.T) {
	authority := managedSessionHandlerTestAuthority()
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet() error = %v", err)
	}
	request := managedSessionTestRequest()
	request.Capabilities = []rpc.Capability{CapabilityLaunchContextV1, CapabilityV1, "project-descriptor.v1"}
	request.LaunchTicket = strings.Repeat("b", 64)
	payload, err := MarshalRegisterRequest(request)
	if err != nil {
		t.Fatalf("MarshalRegisterRequest() error = %v", err)
	}
	_, err = set.Handlers()[MethodRegister](t.Context(), session.Request{
		Method:  MethodRegister,
		Payload: payload,
		Peer:    managedSessionHandlerTestSessionPeer(),
	})
	if err == nil {
		t.Fatal("handler accepted unnegotiated launch ticket")
	}
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodePermissionDenied {
		t.Fatalf("handler error = %v, want permission denied", err)
	}
	if authority.registerCalls != 0 {
		t.Fatalf("authority register calls = %d, want zero", authority.registerCalls)
	}
}

// TestManagedSessionHandlersRedactAuthorityFailuresAndRejectBadResponses keeps implementation errors off the wire.
func TestManagedSessionHandlersRedactAuthorityFailuresAndRejectBadResponses(t *testing.T) {
	registrationPayload := mustManagedSessionHandlerPayload(t, MarshalRegisterRequest, managedSessionTestRequest)
	tests := []struct {
		name   string
		method string
		body   []byte
		mutate func(*recordingManagedAuthority)
	}{
		{name: "authority failure", method: MethodRegister, body: registrationPayload, mutate: func(authority *recordingManagedAuthority) { authority.err = errors.New("private database path") }},
		{name: "registration drift", method: MethodRegister, body: registrationPayload, mutate: func(authority *recordingManagedAuthority) { authority.registerResponse.Fence.ProjectID = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := managedSessionHandlerTestAuthority()
			test.mutate(authority)
			set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
			if err != nil {
				t.Fatalf("NewHandlerSet() error = %v", err)
			}
			_, err = set.Handlers()[test.method](t.Context(), session.Request{Method: test.method, Payload: test.body, Peer: managedSessionHandlerTestSessionPeer()})
			if err == nil {
				t.Fatal("handler accepted unsafe authority result")
			}
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeInternal {
				t.Fatalf("handler error = %#v, want redacted internal HandlerError", err)
			}
			// HandlerError intentionally retains the daemon-local cause for logging; the generic RPC server redacts it when encoding the response.
		})
	}
}

// TestManagedSessionHandlerClassifiesPlannedStartupAsUnavailable preserves a narrow retryable race category.
func TestManagedSessionHandlerClassifiesPlannedStartupAsUnavailable(t *testing.T) {
	authority := managedSessionHandlerTestAuthority()
	authority.err = ErrManagedSessionAwaitingAttach
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet() error = %v", err)
	}
	body := mustManagedSessionHandlerPayload(t, MarshalRegisterRequest, managedSessionTestRequest)
	_, err = set.Handlers()[MethodRegister](t.Context(), session.Request{Method: MethodRegister, Payload: body, Peer: managedSessionHandlerTestSessionPeer()})
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeUnavailable {
		t.Fatalf("handler error = %#v, want unavailable HandlerError", err)
	}
}

// TestManagedSessionHandlerClassifiesRuntimeSettlementAsUnavailable keeps a startup observation race retryable for GoForj.
func TestManagedSessionHandlerClassifiesRuntimeSettlementAsUnavailable(t *testing.T) {
	authority := managedSessionHandlerTestAuthority()
	authority.err = ErrManagedSessionNotReady
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet() error = %v", err)
	}
	request := BarrierRequest{
		SchemaVersion:           SchemaVersion,
		Fence:                   managedSessionTestFence(),
		Phase:                   BarrierPhaseCompose,
		AcceptedProjectIdentity: "orders-dev",
	}
	body, err := MarshalBarrierRequest(request)
	if err != nil {
		t.Fatalf("MarshalBarrierRequest() error = %v", err)
	}
	_, err = set.Handlers()[MethodBarrier](t.Context(), session.Request{Method: MethodBarrier, Payload: body, Peer: managedSessionHandlerTestSessionPeer()})
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeUnavailable {
		t.Fatalf("handler error = %#v, want unavailable HandlerError", err)
	}
}

// TestManagedSessionHandlerClassifiesIncompleteNetworkAuthorityAsTerminal prevents GoForj from polling an impossible barrier.
func TestManagedSessionHandlerClassifiesIncompleteNetworkAuthorityAsTerminal(t *testing.T) {
	authority := managedSessionHandlerTestAuthority()
	authority.err = ErrManagedSessionNetworkSetupRequired
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet() error = %v", err)
	}
	request := BarrierRequest{
		SchemaVersion:           SchemaVersion,
		Fence:                   managedSessionTestFence(),
		Phase:                   BarrierPhaseCompose,
		AcceptedProjectIdentity: "orders-dev",
	}
	body, err := MarshalBarrierRequest(request)
	if err != nil {
		t.Fatalf("MarshalBarrierRequest() error = %v", err)
	}
	_, err = set.Handlers()[MethodBarrier](t.Context(), session.Request{Method: MethodBarrier, Payload: body, Peer: managedSessionHandlerTestSessionPeer()})
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodeConflict {
		t.Fatalf("handler error = %#v, want conflict HandlerError", err)
	}
}

// TestNewHandlerSetValidatesAuthenticatedPeerAndAuthority proves construction cannot create an unbound handler set.
func TestNewHandlerSetValidatesAuthenticatedPeerAndAuthority(t *testing.T) {
	authority := managedSessionHandlerTestAuthority()
	for _, test := range []struct {
		name string
		peer local.PeerIdentity
		want string
	}{
		{name: "missing user", peer: local.PeerIdentity{ProcessID: 1}, want: "user identity"},
		{name: "missing process", peer: local.PeerIdentity{UserID: "501"}, want: "process identity"},
		{name: "surrounding user", peer: local.PeerIdentity{UserID: " 501", ProcessID: 1}, want: "user identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewHandlerSet(test.peer, authority); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewHandlerSet() error = %v, want %q", err, test.want)
			}
		})
	}
	if _, err := NewHandlerSet(managedSessionHandlerTestPeer(), nil); err == nil {
		t.Fatal("NewHandlerSet accepted nil authority")
	}
	var nilSet *HandlerSet
	if handlers := nilSet.Handlers(); handlers != nil {
		t.Fatalf("nil handler set map = %#v, want nil", handlers)
	}
}

// mustManagedSessionHandlerPayload calls one typed encoder and fails the fixture immediately if it cannot encode.
func mustManagedSessionHandlerPayload[T any](t *testing.T, marshal func(T) ([]byte, error), value func() T) []byte {
	t.Helper()
	payload, err := marshal(value())
	if err != nil {
		t.Fatalf("marshal managed session payload: %v", err)
	}
	return payload
}
