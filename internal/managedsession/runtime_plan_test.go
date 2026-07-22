package managedsession

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// runtimePlanTestFence returns the attached-session fence used by runtime-plan fixtures.
func runtimePlanTestFence() RuntimePlanRequest {
	return RuntimePlanRequest{
		SchemaVersion: SchemaVersion,
		Fence:         managedSessionTestFence(),
		ActiveApps:    []ActiveApp{{ID: "app", RuntimeIDs: []string{"http"}}},
	}
}

// runtimePlanTestResponse returns one complete deterministic plan with App and service assignments.
func runtimePlanTestResponse(request RuntimePlanRequest) RuntimePlanResponse {
	return RuntimePlanResponse{
		SchemaVersion: SchemaVersion,
		Fence:         request.Fence,
		Plan: RuntimePlan{
			Apps: []RuntimePlanApp{{
				ID: "app", Active: true,
				Runtimes: []RuntimePlanRuntime{{
					ID: "http", BindHost: "127.0.0.10", BindPort: 43101, PublicURL: "https://orders.test",
					Routes: []RuntimePlanRoute{{Name: "health", Path: "/-/health"}, {Name: "ready", Path: "/-/ready"}},
				}},
			}},
			ServiceEndpoints: []RuntimePlanServiceEndpoint{{
				ID:            "endpoint.database.primary.tcp",
				RequirementID: "requirement.database.primary",
				Consumers:     []string{"app"},
				PublishHost:   "127.0.0.11",
				PublishPort:   43106,
				PublicHost:    "mysql.orders.test",
				PublicPort:    3306,
			}},
		},
	}
}

// TestRuntimePlanV1MatchesPinnedGoForjJSONShape keeps Harbor's response decodable by 55a1e575's strict v1 reader.
func TestRuntimePlanV1MatchesPinnedGoForjJSONShape(t *testing.T) {
	response := runtimePlanTestResponse(runtimePlanTestFence())
	payload, err := MarshalRuntimePlanResponse(response)
	if err != nil {
		t.Fatalf("MarshalRuntimePlanResponse() error = %v", err)
	}
	if bytes.Contains(payload, []byte(`"environment"`)) {
		t.Fatalf("runtime-plan v1 payload unexpectedly contains environment: %s", payload)
	}

	var pinned struct {
		SchemaVersion uint16 `json:"schema_version"`
		Fence         struct {
			ProjectID         string `json:"project_id"`
			SessionID         string `json:"session_id"`
			SessionGeneration uint64 `json:"session_generation"`
		} `json:"fence"`
		Plan struct {
			Apps []struct {
				ID       string `json:"id"`
				Active   bool   `json:"active"`
				Runtimes []struct {
					ID        string `json:"id"`
					BindHost  string `json:"bind_host"`
					BindPort  uint16 `json:"bind_port"`
					PublicURL string `json:"public_url"`
					Routes    []struct {
						Name string `json:"name"`
						Path string `json:"path"`
					} `json:"routes"`
				} `json:"runtimes"`
			} `json:"apps"`
			ServiceEndpoints []struct {
				ID            string   `json:"id"`
				RequirementID string   `json:"requirement_id"`
				Consumers     []string `json:"consumers"`
				PublishHost   string   `json:"publish_host"`
				PublishPort   uint16   `json:"publish_port"`
				PublicHost    string   `json:"public_host"`
				PublicPort    uint16   `json:"public_port"`
			} `json:"service_endpoints"`
		} `json:"plan"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pinned); err != nil {
		t.Fatalf("pinned GoForj v1 strict decode error = %v", err)
	}
}

// TestRuntimePlanV2EnvironmentCapabilityIsNotAdvertised keeps future environment assignments out of v1 negotiation.
func TestRuntimePlanV2EnvironmentCapabilityIsNotAdvertised(t *testing.T) {
	authority := &runtimePlanRecordingAuthority{
		recordingManagedAuthority: managedSessionHandlerTestAuthority(),
	}
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet() error = %v", err)
	}
	for _, capability := range set.Capabilities() {
		if capability == CapabilityRuntimePlanV2 {
			t.Fatalf("managed-session v1 advertised future environment capability %q", capability)
		}
	}
}

// TestRuntimePlanRoundTripPreservesSemanticAssignments protects the cross-repository JSON shape.
func TestRuntimePlanRoundTripPreservesSemanticAssignments(t *testing.T) {
	request := runtimePlanTestFence()
	response := runtimePlanTestResponse(request)
	payload, err := MarshalRuntimePlanResponse(response)
	if err != nil {
		t.Fatalf("MarshalRuntimePlanResponse() error = %v", err)
	}
	decoded, err := DecodeRuntimePlanResponse(payload)
	if err != nil {
		t.Fatalf("DecodeRuntimePlanResponse() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, response) {
		t.Fatalf("decoded response = %#v, want %#v", decoded, response)
	}
	requestPayload, err := MarshalRuntimePlanRequest(request)
	if err != nil {
		t.Fatalf("MarshalRuntimePlanRequest() error = %v", err)
	}
	decodedRequest, err := DecodeRuntimePlanRequest(requestPayload)
	if err != nil || !reflect.DeepEqual(decodedRequest, request) {
		t.Fatalf("DecodeRuntimePlanRequest() = %#v / %v, want %#v", decodedRequest, err, request)
	}
	if err := ValidateRuntimePlanCorrelation(request, response); err != nil {
		t.Fatalf("ValidateRuntimePlanCorrelation() error = %v", err)
	}
}

// TestRuntimePlanValidationRejectsUnsafeAssignments keeps private plan values out of process environments.
func TestRuntimePlanValidationRejectsUnsafeAssignments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RuntimePlanResponse)
		want   string
	}{
		{name: "non-loopback bind", mutate: func(response *RuntimePlanResponse) { response.Plan.Apps[0].Runtimes[0].BindHost = "0.0.0.0" }, want: "loopback"},
		{name: "low bind port", mutate: func(response *RuntimePlanResponse) { response.Plan.Apps[0].Runtimes[0].BindPort = 80 }, want: "bind port"},
		{name: "credential URL", mutate: func(response *RuntimePlanResponse) {
			response.Plan.Apps[0].Runtimes[0].PublicURL = "https://user:pass@orders.test"
		}, want: "credential"},
		{name: "unsorted routes", mutate: func(response *RuntimePlanResponse) {
			routes := response.Plan.Apps[0].Runtimes[0].Routes
			routes[0], routes[1] = routes[1], routes[0]
		}, want: "routes"},
		{name: "invalid upstream", mutate: func(response *RuntimePlanResponse) { response.Plan.ServiceEndpoints[0].PublishHost = "::1" }, want: "loopback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := runtimePlanTestResponse(runtimePlanTestFence())
			test.mutate(&response)
			if err := response.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RuntimePlanResponse.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRuntimePlanCorrelationRejectsMismatchedAssignments keeps Harbor from silently changing GoForj topology.
func TestRuntimePlanCorrelationRejectsMismatchedAssignments(t *testing.T) {
	request := runtimePlanTestFence()
	response := runtimePlanTestResponse(request)
	request.ActiveApps = []ActiveApp{{ID: "app", RuntimeIDs: []string{"worker"}}}
	if err := ValidateRuntimePlanCorrelation(request, response); err == nil || !strings.Contains(err.Error(), "runtime set") {
		t.Fatalf("runtime correlation error = %v", err)
	}
	request.ActiveApps = []ActiveApp{{ID: "app", RuntimeIDs: []string{"http"}}}
	response.Plan.Apps[0].Active = false
	if err := ValidateRuntimePlanCorrelation(request, response); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("inactive App correlation error = %v", err)
	}
}

// runtimePlanRecordingAuthority extends the legacy fixture with the optional plan method.
type runtimePlanRecordingAuthority struct {
	*recordingManagedAuthority
	response RuntimePlanResponse
	request  RuntimePlanRequest
}

// PlanManagedRuntime records and returns one configured runtime plan.
func (authority *runtimePlanRecordingAuthority) PlanManagedRuntime(_ context.Context, _ local.PeerIdentity, request RuntimePlanRequest) (RuntimePlanResponse, error) {
	authority.request = request
	return authority.response, nil
}

// TestRuntimePlanHandlerIsCapabilityGated keeps older Harbor/GoForj peers on the existing three methods.
func TestRuntimePlanHandlerIsCapabilityGated(t *testing.T) {
	legacy, err := NewHandlerSet(managedSessionHandlerTestPeer(), managedSessionHandlerTestAuthority())
	if err != nil {
		t.Fatalf("NewHandlerSet(legacy) error = %v", err)
	}
	if legacy.Handlers()[MethodRuntimePlan] != nil || len(legacy.Capabilities()) != 0 {
		t.Fatal("legacy handler advertised runtime-plan capability")
	}

	request := runtimePlanTestFence()
	authority := &runtimePlanRecordingAuthority{recordingManagedAuthority: managedSessionHandlerTestAuthority()}
	authority.response = runtimePlanTestResponse(request)
	set, err := NewHandlerSet(managedSessionHandlerTestPeer(), authority)
	if err != nil {
		t.Fatalf("NewHandlerSet(runtime plan) error = %v", err)
	}
	if set.Handlers()[MethodRuntimePlan] == nil || !reflect.DeepEqual(set.Capabilities(), []rpc.Capability{CapabilityRuntimePlanV1}) {
		t.Fatalf("runtime-plan handler capabilities = %v", set.Capabilities())
	}
	payload, err := MarshalRuntimePlanRequest(request)
	if err != nil {
		t.Fatalf("MarshalRuntimePlanRequest() error = %v", err)
	}
	peer := managedSessionHandlerTestSessionPeer()
	peer.Capabilities = []rpc.Capability{CapabilityRuntimePlanV1, CapabilityV1}
	got, err := set.Handlers()[MethodRuntimePlan](t.Context(), session.Request{Method: MethodRuntimePlan, Payload: payload, Peer: peer})
	if err != nil {
		t.Fatalf("runtime-plan handler error = %v", err)
	}
	if !reflect.DeepEqual(got, authority.response) || !reflect.DeepEqual(authority.request, request) {
		t.Fatalf("runtime-plan dispatch = %#v / %#v", got, authority.request)
	}
	missingCapabilityPeer := managedSessionHandlerTestSessionPeer()
	if _, err := set.Handlers()[MethodRuntimePlan](t.Context(), session.Request{Method: MethodRuntimePlan, Payload: payload, Peer: missingCapabilityPeer}); err == nil || !strings.Contains(err.Error(), "not negotiated") {
		t.Fatalf("runtime-plan handler accepted missing capability: %v", err)
	}
}
