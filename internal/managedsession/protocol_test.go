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
