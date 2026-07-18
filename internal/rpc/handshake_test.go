package rpc

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestNegotiateHelloSelectsProtocolAndCapabilities verifies a connection gets
// only the capabilities advertised by both role participants.
func TestNegotiateHelloSelectsProtocolAndCapabilities(t *testing.T) {
	hello := Hello{
		ProtocolRanges: []VersionRange{{
			Min: Version{Major: 1, Minor: 0},
			Max: Version{Major: 1, Minor: 4},
		}},
		Role:          RoleDesktop,
		ClientVersion: "0.1.0",
		Capabilities:  []Capability{"unknown.future.v1", "events.v1", "events.v1"},
	}
	welcome, rejection := NegotiateHello(
		hello,
		"0.2.0",
		[]VersionRange{{Min: Version{Major: 1, Minor: 2}, Max: Version{Major: 1, Minor: 6}}},
		[]Capability{"operations.v1", "unknown.future.v1", "events.v1"},
	)
	if rejection != nil {
		t.Fatalf("rejected: %v", rejection.Error)
	}
	if want := (Version{Major: 1, Minor: 4}); welcome.Protocol != want {
		t.Fatalf("protocol = %v, want %v", welcome.Protocol, want)
	}
	if want := []Capability{"events.v1", "unknown.future.v1"}; !reflect.DeepEqual(welcome.Capabilities, want) {
		t.Fatalf("capabilities = %#v, want %#v", welcome.Capabilities, want)
	}
	if err := welcome.Validate(); err != nil {
		t.Fatalf("validate welcome: %v", err)
	}
}

// TestNegotiateHelloReturnsUpgradeGuidance verifies a major mismatch returns
// safe guidance and the daemon ranges needed by the UI.
func TestNegotiateHelloReturnsUpgradeGuidance(t *testing.T) {
	hello := Hello{
		ProtocolRanges: []VersionRange{{Min: Version{Major: 1}, Max: Version{Major: 1, Minor: 2}}},
		Role:           RoleCLI,
		ClientVersion:  "0.1.0",
	}
	_, rejection := NegotiateHello(
		hello,
		"0.2.0",
		[]VersionRange{{Min: Version{Major: 2}, Max: Version{Major: 2, Minor: 1}}},
		nil,
	)
	if rejection == nil {
		t.Fatal("negotiation succeeded")
	}
	if rejection.Error.Code != ErrorCodeUnsupportedProtocol {
		t.Fatalf("error code = %q", rejection.Error.Code)
	}
	if len(rejection.ProtocolRanges) != 1 || rejection.ProtocolRanges[0].Min.Major != 2 {
		t.Fatalf("daemon ranges = %#v", rejection.ProtocolRanges)
	}
	if err := rejection.Validate(); err != nil {
		t.Fatalf("validate rejection: %v", err)
	}
}

// TestNegotiateHelloRedactsInvalidDaemonConfiguration verifies startup details
// are not disclosed before a protocol is established.
func TestNegotiateHelloRedactsInvalidDaemonConfiguration(t *testing.T) {
	_, rejection := NegotiateHello(Hello{}, "secret/path", nil, nil)
	if rejection == nil || rejection.Error.Code != ErrorCodeInternal {
		t.Fatalf("rejection = %#v", rejection)
	}
	encoded, err := json.Marshal(rejection)
	if err != nil {
		t.Fatalf("marshal rejection: %v", err)
	}
	if string(encoded) != `{"role":"daemon","error":{"code":"internal","message":"Harbor could not complete the request.","retryable":false}}` {
		t.Fatalf("rejection = %s", encoded)
	}
}

// TestRoleValidationKeepsAuthorizationRolesExplicit verifies unknown role values
// do not gain a default capability set.
func TestRoleValidationKeepsAuthorizationRolesExplicit(t *testing.T) {
	for _, role := range []Role{"", "future", RoleDaemon} {
		if err := role.ValidateClient(); err == nil {
			t.Fatalf("role %q accepted as a client", role)
		}
	}
}

// TestHandshakeValidationFailureModes verifies every client- and daemon-owned
// handshake field is checked independently before authorization.
func TestHandshakeValidationFailureModes(t *testing.T) {
	protocol := Version{Major: 1}
	ranges := []VersionRange{{Min: protocol, Max: protocol}}
	validHello := Hello{ProtocolRanges: ranges, Role: RoleCLI, ClientVersion: "0.1.0"}
	validWelcome := Welcome{Protocol: protocol, ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0"}
	validReject := Reject{ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0", Error: NewWireError(ErrorCodeInternal)}

	helloFailures := []Hello{
		{Role: RoleCLI, ClientVersion: "0.1.0"},
		{ProtocolRanges: ranges, Role: "future", ClientVersion: "0.1.0"},
		{ProtocolRanges: ranges, Role: RoleCLI},
		{ProtocolRanges: ranges, Role: RoleCLI, ClientVersion: "0.1.0", Capabilities: []Capability{"bad capability"}},
	}
	for _, hello := range helloFailures {
		if err := hello.Validate(); err == nil {
			t.Fatalf("hello %#v accepted", hello)
		}
	}
	if err := validHello.Validate(); err != nil {
		t.Fatalf("valid hello: %v", err)
	}

	welcomeFailures := []Welcome{
		{ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0"},
		{Protocol: protocol, Role: RoleDaemon, DaemonVersion: "0.1.0"},
		{Protocol: Version{Major: 1, Minor: 1}, ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0"},
		{Protocol: protocol, ProtocolRanges: ranges, Role: RoleCLI, DaemonVersion: "0.1.0"},
		{Protocol: protocol, ProtocolRanges: ranges, Role: RoleDaemon},
		{Protocol: protocol, ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0", Capabilities: []Capability{"bad capability"}},
	}
	for _, welcome := range welcomeFailures {
		if err := welcome.Validate(); err == nil {
			t.Fatalf("welcome %#v accepted", welcome)
		}
	}
	if err := validWelcome.Validate(); err != nil {
		t.Fatalf("valid welcome: %v", err)
	}

	rejectFailures := []Reject{
		{ProtocolRanges: []VersionRange{{}}, Role: RoleDaemon, Error: validReject.Error},
		{ProtocolRanges: ranges, Role: RoleCLI, Error: validReject.Error},
		{ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "bad version", Error: validReject.Error},
		{ProtocolRanges: ranges, Role: RoleDaemon, DaemonVersion: "0.1.0", Error: WireError{}},
	}
	for _, rejection := range rejectFailures {
		if err := rejection.Validate(); err == nil {
			t.Fatalf("rejection %#v accepted", rejection)
		}
	}
	if err := validReject.Validate(); err != nil {
		t.Fatalf("valid rejection: %v", err)
	}
}

// TestNegotiateHelloRejectsInvalidClientAndServerMetadata verifies all
// pre-negotiation failures use reviewed wire errors rather than Go error text.
func TestNegotiateHelloRejectsInvalidClientAndServerMetadata(t *testing.T) {
	protocol := Version{Major: 1}
	ranges := []VersionRange{{Min: protocol, Max: protocol}}
	validHello := Hello{ProtocolRanges: ranges, Role: RoleCLI, ClientVersion: "0.1.0"}
	tests := []struct {
		hello        Hello
		daemon       string
		ranges       []VersionRange
		capabilities []Capability
		code         ErrorCode
	}{
		{hello: Hello{}, daemon: "0.1.0", ranges: ranges, code: ErrorCodeInvalidHandshake},
		{hello: validHello, daemon: "bad version", ranges: ranges, code: ErrorCodeInternal},
		{hello: validHello, daemon: "0.1.0", ranges: ranges, capabilities: []Capability{"bad capability"}, code: ErrorCodeInternal},
	}
	for _, test := range tests {
		_, rejection := NegotiateHello(test.hello, test.daemon, test.ranges, test.capabilities)
		if rejection == nil || rejection.Error.Code != test.code {
			t.Fatalf("rejection = %#v, want %q", rejection, test.code)
		}
	}
}
