package managedsession

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/rpc"
)

// managedSessionTestFence returns the attached-session authority used by protocol fixtures.
func managedSessionTestFence() harbordruntime.ManagedPublicationFence {
	return harbordruntime.ManagedPublicationFence{
		ProjectID:         "orders",
		SessionID:         "session-orders",
		SessionGeneration: 2,
	}
}

// managedSessionTestProcess returns complete immutable process evidence for output-reattachment fixtures.
func managedSessionTestProcess() domain.ProcessEvidence {
	return domain.ProcessEvidence{
		PID:                4102,
		BirthToken:         "linux:boot-1:process-4102",
		ExecutableIdentity: "/usr/local/bin/forj",
		ArgumentDigest:     strings.Repeat("b", 64),
	}
}

// managedSessionTestOutputReattachBegin returns one bounded output-reattachment identity.
func managedSessionTestOutputReattachBegin() OutputReattachBeginRequest {
	return OutputReattachBeginRequest{
		SchemaVersion:     SchemaVersion,
		Fence:             managedSessionTestFence(),
		SessionProcess:    managedSessionTestProcess(),
		EndpointReference: "/tmp/harbor-output-orders.sock",
		ClientNonce:       "output-client-nonce-1",
	}
}

// managedSessionTestOutputReattachChallenge returns one challenge correlated to the begin fixture.
func managedSessionTestOutputReattachChallenge(begin OutputReattachBeginRequest) OutputReattachChallengeResponse {
	return OutputReattachChallengeResponse{
		SchemaVersion:     SchemaVersion,
		Fence:             begin.Fence,
		SessionProcess:    begin.SessionProcess,
		EndpointReference: begin.EndpointReference,
		ClientNonce:       begin.ClientNonce,
		Challenge:         "output-server-challenge-1",
	}
}

// managedSessionTestOutputReattachConfirm returns one confirmation correlated to the challenge fixture.
func managedSessionTestOutputReattachConfirm(challenge OutputReattachChallengeResponse) OutputReattachConfirmRequest {
	return OutputReattachConfirmRequest{
		SchemaVersion:     SchemaVersion,
		Fence:             challenge.Fence,
		SessionProcess:    challenge.SessionProcess,
		EndpointReference: challenge.EndpointReference,
		ClientNonce:       challenge.ClientNonce,
		Challenge:         challenge.Challenge,
	}
}

// managedSessionTestOutputReattachResponse returns one accepted response correlated to the confirmation fixture.
func managedSessionTestOutputReattachResponse(confirm OutputReattachConfirmRequest) OutputReattachResponse {
	return OutputReattachResponse{
		SchemaVersion:     SchemaVersion,
		Fence:             confirm.Fence,
		SessionProcess:    confirm.SessionProcess,
		EndpointReference: confirm.EndpointReference,
		ClientNonce:       confirm.ClientNonce,
		Challenge:         confirm.Challenge,
		Accepted:          true,
		AttachmentTicket:  "output-attachment-ticket-1",
	}
}

// managedSessionTestRequest returns a complete deterministic registration request.
func managedSessionTestRequest() RegisterRequest {
	return RegisterRequest{
		SchemaVersion:             SchemaVersion,
		ProjectID:                 "orders",
		SessionID:                 "session-orders",
		ProjectRoot:               "/private/tmp/orders",
		ExpectedSessionGeneration: 1,
		DescriptorDigest:          strings.Repeat("a", 64),
		ClientNonce:               "nonce-orders-1",
		Owner:                     domain.SessionOwnerHarbor,
		Capabilities:              []rpc.Capability{CapabilityV1, "project-descriptor.v1"},
		ActiveApps: []ActiveApp{
			{ID: "api", RuntimeIDs: []string{"http"}},
			{ID: "worker", RuntimeIDs: []string{"queue", "scheduler"}},
		},
	}
}

// managedSessionTestPublication returns one valid private loopback publication.
func managedSessionTestPublication(fence harbordruntime.ManagedPublicationFence, endpointID string) harbordruntime.ManagedEndpointPublication {
	return harbordruntime.ManagedEndpointPublication{
		Fence:                 fence,
		EndpointID:            endpointID,
		ReservationGeneration: 3,
		Upstream:              netip.MustParseAddrPort("127.0.0.1:43106"),
	}
}

// TestManagedSessionProtocolRoundTripsBoundedMessages proves every current method has a strict typed wire shape.
func TestManagedSessionProtocolRoundTripsBoundedMessages(t *testing.T) {
	fence := managedSessionTestFence()
	registration := managedSessionTestRequest()
	registrationResponse := RegisterResponse{SchemaVersion: SchemaVersion, Fence: fence, AttachmentTicket: "ticket-orders-1"}
	publication := managedSessionTestPublication(fence, "service:mysql")
	publicationRequest := ReplacePublicationsRequest{SchemaVersion: SchemaVersion, Fence: fence, Publications: []harbordruntime.ManagedEndpointPublication{publication}}
	publicationResponse := ReplacePublicationsResponse{SchemaVersion: SchemaVersion, Fence: fence, Accepted: true, PublicationCount: 1}
	barrierRequest := BarrierRequest{
		SchemaVersion:           SchemaVersion,
		Fence:                   fence,
		Phase:                   BarrierPhaseCompose,
		AcceptedProjectIdentity: "orders-dev",
	}
	barrierResponse := BarrierResponse{SchemaVersion: SchemaVersion, Fence: fence, Phase: BarrierPhaseCompose, Acknowledged: true}
	outputBegin := managedSessionTestOutputReattachBegin()
	outputChallenge := managedSessionTestOutputReattachChallenge(outputBegin)
	outputConfirm := managedSessionTestOutputReattachConfirm(outputChallenge)
	outputResponse := managedSessionTestOutputReattachResponse(outputConfirm)
	logEvent := Event{
		SchemaVersion: SchemaVersion,
		ProjectID:     "orders",
		SessionID:     "session-orders",
		Sequence:      42,
		Timestamp:     "2026-07-21T04:51:00Z",
		Kind:          EventKindLogChunk,
		AppID:         "api",
		WatcherID:     "api.runtime",
		Stream:        EventStreamStdout,
		Text:          "ready\n",
	}
	gapEvent := Event{
		SchemaVersion: SchemaVersion,
		ProjectID:     "orders",
		SessionID:     "session-orders",
		Sequence:      46,
		Timestamp:     "2026-07-21T04:51:01Z",
		Kind:          EventKindOutputGap,
		WatcherID:     "api.runtime",
		Stream:        EventStreamStdout,
		DroppedFrom:   43,
		DroppedTo:     45,
		DroppedCount:  3,
	}

	tests := []struct {
		name    string
		value   any
		marshal func() ([]byte, error)
		decode  func([]byte) (any, error)
		want    any
	}{
		{name: "register request", value: registration, marshal: func() ([]byte, error) { return MarshalRegisterRequest(registration) }, decode: func(payload []byte) (any, error) { return DecodeRegisterRequest(payload) }, want: registration},
		{name: "register response", value: registrationResponse, marshal: func() ([]byte, error) { return MarshalRegisterResponse(registrationResponse) }, decode: func(payload []byte) (any, error) { return DecodeRegisterResponse(payload) }, want: registrationResponse},
		{name: "publication request", value: publicationRequest, marshal: func() ([]byte, error) { return MarshalReplacePublicationsRequest(publicationRequest) }, decode: func(payload []byte) (any, error) { return DecodeReplacePublicationsRequest(payload) }, want: publicationRequest},
		{name: "publication response", value: publicationResponse, marshal: func() ([]byte, error) { return MarshalReplacePublicationsResponse(publicationResponse) }, decode: func(payload []byte) (any, error) { return DecodeReplacePublicationsResponse(payload) }, want: publicationResponse},
		{name: "barrier request", value: barrierRequest, marshal: func() ([]byte, error) { return MarshalBarrierRequest(barrierRequest) }, decode: func(payload []byte) (any, error) { return DecodeBarrierRequest(payload) }, want: barrierRequest},
		{name: "barrier response", value: barrierResponse, marshal: func() ([]byte, error) { return MarshalBarrierResponse(barrierResponse) }, decode: func(payload []byte) (any, error) { return DecodeBarrierResponse(payload) }, want: barrierResponse},
		{name: "output reattach begin", value: outputBegin, marshal: func() ([]byte, error) { return MarshalOutputReattachBeginRequest(outputBegin) }, decode: func(payload []byte) (any, error) { return DecodeOutputReattachBeginRequest(payload) }, want: outputBegin},
		{name: "output reattach challenge", value: outputChallenge, marshal: func() ([]byte, error) { return MarshalOutputReattachChallengeResponse(outputChallenge) }, decode: func(payload []byte) (any, error) { return DecodeOutputReattachChallengeResponse(payload) }, want: outputChallenge},
		{name: "output reattach confirm", value: outputConfirm, marshal: func() ([]byte, error) { return MarshalOutputReattachConfirmRequest(outputConfirm) }, decode: func(payload []byte) (any, error) { return DecodeOutputReattachConfirmRequest(payload) }, want: outputConfirm},
		{name: "output reattach response", value: outputResponse, marshal: func() ([]byte, error) { return MarshalOutputReattachResponse(outputResponse) }, decode: func(payload []byte) (any, error) { return DecodeOutputReattachResponse(payload) }, want: outputResponse},
		{name: "log event", value: logEvent, marshal: func() ([]byte, error) { return MarshalEvent(logEvent) }, decode: func(payload []byte) (any, error) { return DecodeEvent(payload) }, want: logEvent},
		{name: "output gap event", value: gapEvent, marshal: func() ([]byte, error) { return MarshalEvent(gapEvent) }, decode: func(payload []byte) (any, error) { return DecodeEvent(payload) }, want: gapEvent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := test.marshal()
			if err != nil {
				t.Fatalf("marshal error = %v", err)
			}
			got, err := test.decode(payload)
			if err != nil {
				t.Fatalf("decode error = %v; payload = %s", err, payload)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("decoded = %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestManagedSessionOutputReattachCorrelationRejectsIdentityDrift keeps every handshake step bound to one exact process and endpoint.
func TestManagedSessionOutputReattachCorrelationRejectsIdentityDrift(t *testing.T) {
	begin := managedSessionTestOutputReattachBegin()
	challenge := managedSessionTestOutputReattachChallenge(begin)
	confirm := managedSessionTestOutputReattachConfirm(challenge)
	response := managedSessionTestOutputReattachResponse(confirm)

	for _, test := range []struct {
		name   string
		mutate func(*OutputReattachChallengeResponse)
	}{
		{name: "challenge fence", mutate: func(value *OutputReattachChallengeResponse) { value.Fence.SessionGeneration++ }},
		{name: "challenge process", mutate: func(value *OutputReattachChallengeResponse) { value.SessionProcess.PID++ }},
		{name: "challenge endpoint", mutate: func(value *OutputReattachChallengeResponse) { value.EndpointReference += ".other" }},
		{name: "challenge nonce", mutate: func(value *OutputReattachChallengeResponse) { value.ClientNonce += ".other" }},
	} {
		t.Run("begin to challenge/"+test.name, func(t *testing.T) {
			candidate := challenge
			test.mutate(&candidate)
			if err := ValidateOutputReattachChallengeCorrelation(begin, candidate); err == nil {
				t.Fatal("challenge identity drift passed correlation")
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*OutputReattachConfirmRequest)
	}{
		{name: "confirm fence", mutate: func(value *OutputReattachConfirmRequest) { value.Fence.SessionID = "other-session" }},
		{name: "confirm process", mutate: func(value *OutputReattachConfirmRequest) { value.SessionProcess.BirthToken += ".other" }},
		{name: "confirm endpoint", mutate: func(value *OutputReattachConfirmRequest) { value.EndpointReference += ".other" }},
		{name: "confirm nonce", mutate: func(value *OutputReattachConfirmRequest) { value.ClientNonce += ".other" }},
		{name: "confirm challenge", mutate: func(value *OutputReattachConfirmRequest) { value.Challenge += ".other" }},
	} {
		t.Run("challenge to confirm/"+test.name, func(t *testing.T) {
			candidate := confirm
			test.mutate(&candidate)
			if err := ValidateOutputReattachConfirmCorrelation(challenge, candidate); err == nil {
				t.Fatal("confirmation identity drift passed correlation")
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*OutputReattachResponse)
	}{
		{name: "response fence", mutate: func(value *OutputReattachResponse) { value.Fence.ProjectID = "other-project" }},
		{name: "response process", mutate: func(value *OutputReattachResponse) { value.SessionProcess.ArgumentDigest = strings.Repeat("c", 64) }},
		{name: "response endpoint", mutate: func(value *OutputReattachResponse) { value.EndpointReference += ".other" }},
		{name: "response nonce", mutate: func(value *OutputReattachResponse) { value.ClientNonce += ".other" }},
		{name: "response challenge", mutate: func(value *OutputReattachResponse) { value.Challenge += ".other" }},
	} {
		t.Run("confirm to response/"+test.name, func(t *testing.T) {
			candidate := response
			test.mutate(&candidate)
			if err := ValidateOutputReattachResponseCorrelation(confirm, candidate); err == nil {
				t.Fatal("response identity drift passed correlation")
			}
		})
	}
}

// TestManagedSessionOutputReattachValidationRejectsUnsafeValues keeps the future broker boundary bounded and opaque.
func TestManagedSessionOutputReattachValidationRejectsUnsafeValues(t *testing.T) {
	begin := managedSessionTestOutputReattachBegin()
	pipeEndpoint := begin
	pipeEndpoint.EndpointReference = `\\.\pipe\harbor-output-orders`
	if err := pipeEndpoint.Validate(); err != nil {
		t.Fatalf("valid Windows pipe endpoint rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*OutputReattachBeginRequest)
	}{
		{name: "empty nonce", mutate: func(value *OutputReattachBeginRequest) { value.ClientNonce = "" }},
		{name: "oversized nonce", mutate: func(value *OutputReattachBeginRequest) {
			value.ClientNonce = strings.Repeat("n", maximumManagedSessionTokenBytes+1)
		}},
		{name: "relative endpoint", mutate: func(value *OutputReattachBeginRequest) { value.EndpointReference = "harbor.sock" }},
		{name: "unclean endpoint", mutate: func(value *OutputReattachBeginRequest) { value.EndpointReference = "/tmp/../harbor.sock" }},
		{name: "control endpoint", mutate: func(value *OutputReattachBeginRequest) { value.EndpointReference = "/tmp/harbor\x00.sock" }},
		{name: "empty endpoint", mutate: func(value *OutputReattachBeginRequest) { value.EndpointReference = "" }},
		{name: "oversized endpoint", mutate: func(value *OutputReattachBeginRequest) {
			value.EndpointReference = "/" + strings.Repeat("x", maximumManagedSessionEndpointBytes)
		}},
		{name: "invalid process", mutate: func(value *OutputReattachBeginRequest) { value.SessionProcess.PID = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := begin
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("unsafe begin request passed validation")
			}
		})
	}

	challenge := managedSessionTestOutputReattachChallenge(begin)
	confirm := managedSessionTestOutputReattachConfirm(challenge)
	response := managedSessionTestOutputReattachResponse(confirm)
	for _, test := range []struct {
		name   string
		mutate func(*OutputReattachChallengeResponse)
	}{
		{name: "empty challenge", mutate: func(value *OutputReattachChallengeResponse) { value.Challenge = "" }},
		{name: "oversized challenge", mutate: func(value *OutputReattachChallengeResponse) {
			value.Challenge = strings.Repeat("c", maximumManagedSessionTokenBytes+1)
		}},
	} {
		t.Run("challenge "+test.name, func(t *testing.T) {
			candidate := challenge
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("unsafe challenge passed validation")
			}
		})
	}
	response.Accepted = false
	response.AttachmentTicket = "ticket-must-not-be-present"
	if err := response.Validate(); err == nil {
		t.Fatal("rejected response with a ticket passed validation")
	}
	response.AttachmentTicket = ""
	if err := response.Validate(); err != nil {
		t.Fatalf("valid rejected response failed validation: %v", err)
	}
	response.Accepted = true
	response.AttachmentTicket = ""
	if err := response.Validate(); err == nil {
		t.Fatal("accepted response without a ticket passed validation")
	}
	response.AttachmentTicket = strings.Repeat("t", maximumManagedSessionTokenBytes+1)
	if err := response.Validate(); err == nil {
		t.Fatal("accepted response with an oversized ticket passed validation")
	}
}

// TestManagedSessionOutputReattachDecodersRemainStrict prevents handshake fields from smuggling duplicate or trailing JSON values.
func TestManagedSessionOutputReattachDecodersRemainStrict(t *testing.T) {
	begin := managedSessionTestOutputReattachBegin()
	challenge := managedSessionTestOutputReattachChallenge(begin)
	confirm := managedSessionTestOutputReattachConfirm(challenge)
	response := managedSessionTestOutputReattachResponse(confirm)
	tests := []struct {
		name    string
		payload func() ([]byte, error)
		decode  func([]byte) error
	}{
		{name: "begin", payload: func() ([]byte, error) { return MarshalOutputReattachBeginRequest(begin) }, decode: func(payload []byte) error { _, err := DecodeOutputReattachBeginRequest(payload); return err }},
		{name: "challenge", payload: func() ([]byte, error) { return MarshalOutputReattachChallengeResponse(challenge) }, decode: func(payload []byte) error { _, err := DecodeOutputReattachChallengeResponse(payload); return err }},
		{name: "confirm", payload: func() ([]byte, error) { return MarshalOutputReattachConfirmRequest(confirm) }, decode: func(payload []byte) error { _, err := DecodeOutputReattachConfirmRequest(payload); return err }},
		{name: "response", payload: func() ([]byte, error) { return MarshalOutputReattachResponse(response) }, decode: func(payload []byte) error { _, err := DecodeOutputReattachResponse(payload); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := test.payload()
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			valid := strings.TrimSuffix(string(payload), "}")
			for _, invalid := range []string{
				valid + `,"unknown":true}`,
				valid + `,"schema_version":1}`,
				string(payload) + `{}`,
			} {
				if err := test.decode([]byte(invalid)); err == nil {
					t.Fatalf("decoder accepted invalid payload %s", invalid)
				}
			}
		})
	}
}

// TestManagedSessionEventValidationRejectsLossyOrAmbiguousRecords keeps future replay explicit about ordering and drops.
func TestManagedSessionEventValidationRejectsLossyOrAmbiguousRecords(t *testing.T) {
	valid := Event{
		SchemaVersion: SchemaVersion,
		ProjectID:     "orders",
		SessionID:     "session-orders",
		Sequence:      2,
		Timestamp:     "2026-07-21T04:51:00Z",
		Kind:          EventKindLogChunk,
		WatcherID:     "api.runtime",
		Stream:        EventStreamStdout,
		Text:          "ready",
	}
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "sequence", mutate: func(event *Event) { event.Sequence = 0 }},
		{name: "timestamp zone", mutate: func(event *Event) { event.Timestamp = "2026-07-21T04:51:00+01:00" }},
		{name: "source", mutate: func(event *Event) { event.AppID, event.WatcherID = "", "" }},
		{name: "stream", mutate: func(event *Event) { event.Stream = "combined" }},
		{name: "empty text", mutate: func(event *Event) { event.Text = "" }},
		{name: "oversized text", mutate: func(event *Event) { event.Text = strings.Repeat("x", maximumManagedSessionEventText+1) }},
		{name: "unexpected gap", mutate: func(event *Event) { event.DroppedFrom, event.DroppedTo, event.DroppedCount = 1, 1, 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid
			test.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("invalid event passed validation")
			}
		})
	}

	gap := valid
	gap.Kind = EventKindOutputGap
	gap.Text = ""
	gap.DroppedFrom, gap.DroppedTo, gap.DroppedCount = 2, 3, 1
	if err := gap.Validate(); err == nil {
		t.Fatal("gap with mismatched count passed validation")
	}
	gap.DroppedFrom, gap.DroppedTo, gap.DroppedCount = 3, 4, 2
	if err := gap.Validate(); err == nil {
		t.Fatal("gap that reaches its own sequence passed validation")
	}
}

// TestManagedSessionEventDecoderRemainsStrict prevents future event fields from smuggling duplicate or trailing data.
func TestManagedSessionEventDecoderRemainsStrict(t *testing.T) {
	event := Event{
		SchemaVersion: SchemaVersion,
		ProjectID:     "orders",
		SessionID:     "session-orders",
		Sequence:      1,
		Timestamp:     "2026-07-21T04:51:00Z",
		Kind:          EventKindLogChunk,
		AppID:         "api",
		Stream:        EventStreamStderr,
		Text:          "failure",
	}
	payload, err := MarshalEvent(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	valid := strings.TrimSuffix(string(payload), "}")
	for _, invalid := range []string{
		valid + `,"unknown":true}`,
		valid + `,"sequence":1}`,
		string(payload) + `{}`,
	} {
		if _, err := DecodeEvent([]byte(invalid)); err == nil {
			t.Fatalf("event decoder accepted invalid payload %s", invalid)
		}
	}
}

// TestManagedSessionRegistrationValidationRejectsUnsafeAuthority keeps process and descriptor identity explicit.
func TestManagedSessionRegistrationValidationRejectsUnsafeAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RegisterRequest)
	}{
		{name: "schema", mutate: func(request *RegisterRequest) { request.SchemaVersion = 2 }},
		{name: "project", mutate: func(request *RegisterRequest) { request.ProjectID = "" }},
		{name: "session", mutate: func(request *RegisterRequest) { request.SessionID = "" }},
		{name: "relative root", mutate: func(request *RegisterRequest) { request.ProjectRoot = "orders" }},
		{name: "unclean root", mutate: func(request *RegisterRequest) { request.ProjectRoot = "/private/tmp/orders/.." }},
		{name: "generation", mutate: func(request *RegisterRequest) { request.ExpectedSessionGeneration = 0 }},
		{name: "digest prefix", mutate: func(request *RegisterRequest) { request.DescriptorDigest = "sha256:" + request.DescriptorDigest }},
		{name: "digest uppercase", mutate: func(request *RegisterRequest) { request.DescriptorDigest = strings.Repeat("A", 64) }},
		{name: "nonce whitespace", mutate: func(request *RegisterRequest) { request.ClientNonce = "nonce orders" }},
		{name: "owner", mutate: func(request *RegisterRequest) { request.Owner = "other" }},
		{name: "nil capabilities", mutate: func(request *RegisterRequest) { request.Capabilities = nil }},
		{name: "unsorted capabilities", mutate: func(request *RegisterRequest) {
			request.Capabilities = []rpc.Capability{"project-descriptor.v1", CapabilityV1}
		}},
		{name: "nil Apps", mutate: func(request *RegisterRequest) { request.ActiveApps = nil }},
		{name: "unsorted Apps", mutate: func(request *RegisterRequest) {
			request.ActiveApps[0], request.ActiveApps[1] = request.ActiveApps[1], request.ActiveApps[0]
		}},
		{name: "nil runtimes", mutate: func(request *RegisterRequest) { request.ActiveApps[0].RuntimeIDs = nil }},
		{name: "unsorted runtimes", mutate: func(request *RegisterRequest) { request.ActiveApps[1].RuntimeIDs = []string{"scheduler", "queue"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := managedSessionTestRequest()
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("invalid registration passed validation")
			}
		})
	}
}

// TestManagedSessionLaunchTicketRequiresNegotiatedCapability keeps optional credential fields compatible with v1 peers.
func TestManagedSessionLaunchTicketRequiresNegotiatedCapability(t *testing.T) {
	request := managedSessionTestRequest()
	request.LaunchTicket = strings.Repeat("b", 64)
	if err := request.Validate(); err == nil {
		t.Fatal("launch ticket without capability passed validation")
	}
	request.Capabilities = []rpc.Capability{CapabilityLaunchContextV1, CapabilityV1, "project-descriptor.v1"}
	if err := request.Validate(); err != nil {
		t.Fatalf("negotiated launch ticket rejected: %v", err)
	}
	request.LaunchTicket = strings.Repeat(" ", 64)
	if err := request.Validate(); err == nil {
		t.Fatal("whitespace launch ticket passed validation")
	}
	request.LaunchTicket = ""
	request.Capabilities = []rpc.Capability{CapabilityV1, "project-descriptor.v1"}
	if err := request.Validate(); err != nil {
		t.Fatalf("empty optional launch ticket rejected: %v", err)
	}
	request.Capabilities = []rpc.Capability{CapabilityLaunchContextV1, CapabilityV1, "project-descriptor.v1"}
	if err := request.Validate(); err == nil {
		t.Fatal("negotiated launch capability without ticket passed validation")
	}
}

// TestManagedSessionStrictDecodersRejectUnknownDuplicateAndTrailingFields prevents schema smuggling through JSON.
func TestManagedSessionStrictDecodersRejectUnknownDuplicateAndTrailingFields(t *testing.T) {
	requestPayload, err := MarshalRegisterRequest(managedSessionTestRequest())
	if err != nil {
		t.Fatalf("marshal registration: %v", err)
	}
	valid := strings.TrimSuffix(string(requestPayload), "}")
	invalid := []string{
		valid + `,"unknown":true}`,
		valid + `,"schema_version":1}`,
		string(requestPayload) + `{}`,
		`[]`,
		`null`,
		`{"schema_version":1`,
	}
	for index, payload := range invalid {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			if _, err := DecodeRegisterRequest([]byte(payload)); err == nil {
				t.Fatalf("decoder accepted invalid payload %s", payload)
			}
		})
	}

	nestedDuplicate := `{"schema_version":1,"fence":{"project_id":"orders","session_id":"session-orders","session_generation":2,"project_id":"other"},"publications":[]}`
	if _, err := DecodeReplacePublicationsRequest([]byte(nestedDuplicate)); err == nil {
		t.Fatal("publication decoder accepted duplicate nested fence field")
	}
}

// TestManagedSessionPublicationAndBarrierValidationRejectFenceDrift proves downstream authority stays session-fenced.
func TestManagedSessionPublicationAndBarrierValidationRejectFenceDrift(t *testing.T) {
	fence := managedSessionTestFence()
	publicationRequest := ReplacePublicationsRequest{
		SchemaVersion: SchemaVersion,
		Fence:         fence,
		Publications:  []harbordruntime.ManagedEndpointPublication{managedSessionTestPublication(fence, "service:mysql")},
	}
	publicationTests := []struct {
		name   string
		mutate func(*ReplacePublicationsRequest)
	}{
		{name: "fence drift", mutate: func(request *ReplacePublicationsRequest) { request.Publications[0].Fence.SessionID = "other" }},
		{name: "duplicate endpoint", mutate: func(request *ReplacePublicationsRequest) {
			request.Publications = append(request.Publications, request.Publications[0])
		}},
		{name: "foreign upstream", mutate: func(request *ReplacePublicationsRequest) {
			request.Publications[0].Upstream = netip.MustParseAddrPort("0.0.0.0:43106")
		}},
		{name: "nil replacement", mutate: func(request *ReplacePublicationsRequest) { request.Publications = nil }},
	}
	for _, test := range publicationTests {
		t.Run(test.name, func(t *testing.T) {
			request := publicationRequest
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("invalid publication request passed validation")
			}
		})
	}

	barrier := BarrierRequest{SchemaVersion: SchemaVersion, Fence: fence, Phase: BarrierPhaseCompose, AcceptedProjectIdentity: "orders-dev"}
	for _, test := range []struct {
		name   string
		mutate func(*BarrierRequest)
	}{
		{name: "phase", mutate: func(request *BarrierRequest) { request.Phase = "post-migrate" }},
		{name: "identity", mutate: func(request *BarrierRequest) { request.AcceptedProjectIdentity = "orders dev" }},
		{name: "fence", mutate: func(request *BarrierRequest) { request.Fence.SessionGeneration = 0 }},
	} {
		t.Run("barrier "+test.name, func(t *testing.T) {
			request := barrier
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("invalid barrier request passed validation")
			}
		})
	}
}

// TestManagedSessionResponseValidationKeepsAcknowledgementsBounded proves responses cannot claim hidden authority.
func TestManagedSessionResponseValidationKeepsAcknowledgementsBounded(t *testing.T) {
	fence := managedSessionTestFence()
	registrationRequest := managedSessionTestRequest()
	registrationResponse := RegisterResponse{SchemaVersion: SchemaVersion, Fence: fence, AttachmentTicket: "ticket-orders-1"}
	if err := ValidateRegisterCorrelation(registrationRequest, registrationResponse); err != nil {
		t.Fatalf("ValidateRegisterCorrelation() error = %v", err)
	}
	registrationResponse.Fence.SessionGeneration++
	if err := ValidateRegisterCorrelation(registrationRequest, registrationResponse); err == nil {
		t.Fatal("registration response with generation drift passed correlation")
	}

	publicationRequest := ReplacePublicationsRequest{
		SchemaVersion: SchemaVersion,
		Fence:         fence,
		Publications:  []harbordruntime.ManagedEndpointPublication{managedSessionTestPublication(fence, "service:mysql")},
	}
	publicationResponse := ReplacePublicationsResponse{SchemaVersion: SchemaVersion, Fence: fence, Accepted: true, PublicationCount: 1}
	if err := ValidateReplacePublicationsCorrelation(publicationRequest, publicationResponse); err != nil {
		t.Fatalf("ValidateReplacePublicationsCorrelation() error = %v", err)
	}
	publicationResponse.PublicationCount = 0
	if err := ValidateReplacePublicationsCorrelation(publicationRequest, publicationResponse); err == nil {
		t.Fatal("publication response with count drift passed correlation")
	}

	barrierRequest := BarrierRequest{SchemaVersion: SchemaVersion, Fence: fence, Phase: BarrierPhaseCompose, AcceptedProjectIdentity: "orders-dev"}
	barrierResponse := BarrierResponse{SchemaVersion: SchemaVersion, Fence: fence, Phase: BarrierPhaseCompose, Acknowledged: true}
	if err := ValidateBarrierCorrelation(barrierRequest, barrierResponse); err != nil {
		t.Fatalf("ValidateBarrierCorrelation() error = %v", err)
	}
	barrierResponse.Phase = "post-migrate"
	if err := ValidateBarrierCorrelation(barrierRequest, barrierResponse); err == nil {
		t.Fatal("barrier response with phase drift passed correlation")
	}

	response := ReplacePublicationsResponse{SchemaVersion: SchemaVersion, Fence: fence, Accepted: false, PublicationCount: 0}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid rejected response: %v", err)
	}
	response.PublicationCount = 1
	if err := response.Validate(); err == nil {
		t.Fatal("rejected response with publication count passed validation")
	}
	if _, err := MarshalRegisterResponse(RegisterResponse{SchemaVersion: SchemaVersion, Fence: fence}); err == nil {
		t.Fatal("registration response without a ticket passed marshal validation")
	}

	encoded, err := json.Marshal(BarrierResponse{SchemaVersion: SchemaVersion, Fence: fence, Phase: BarrierPhaseCompose, Acknowledged: true})
	if err != nil {
		t.Fatalf("marshal barrier response: %v", err)
	}
	if _, err := DecodeBarrierResponse(encoded); err != nil {
		t.Fatalf("decode valid barrier response: %v", err)
	}
}
