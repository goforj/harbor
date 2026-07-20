package helper

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// TestExpectedObservationValidate covers the complete bounded observation schema.
func TestExpectedObservationValidate(t *testing.T) {
	tests := []struct {
		name        string
		observation ExpectedObservation
		wantError   bool
	}{
		{name: "absent", observation: ExpectedObservation{State: ObservationAbsent, Fingerprint: testFingerprint()}},
		{name: "owned", observation: ExpectedObservation{State: ObservationOwned, Fingerprint: testFingerprint()}},
		{name: "unknown state", observation: ExpectedObservation{State: "foreign", Fingerprint: testFingerprint()}, wantError: true},
		{name: "short fingerprint", observation: ExpectedObservation{State: ObservationAbsent, Fingerprint: "abcd"}, wantError: true},
		{name: "uppercase fingerprint", observation: ExpectedObservation{State: ObservationAbsent, Fingerprint: strings.Repeat("A", 64)}, wantError: true},
		{name: "non hexadecimal fingerprint", observation: ExpectedObservation{State: ObservationAbsent, Fingerprint: strings.Repeat("z", 64)}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.observation.Validate()
			if test.wantError && err == nil {
				t.Fatal("expected validation error")
			}
			if !test.wantError && err != nil {
				t.Fatalf("validate observation: %v", err)
			}
		})
	}
}

// TestExpectedResolverObservationValidate covers the resolver compare-and-swap digest boundary.
func TestExpectedResolverObservationValidate(t *testing.T) {
	tests := []struct {
		name        string
		fingerprint string
		wantError   bool
	}{
		{name: "canonical", fingerprint: testFingerprint()},
		{name: "short", fingerprint: "abcd", wantError: true},
		{name: "uppercase", fingerprint: strings.Repeat("A", 64), wantError: true},
		{name: "non hexadecimal", fingerprint: strings.Repeat("z", 64), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (ExpectedResolverObservation{Fingerprint: test.fingerprint}).Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("ExpectedResolverObservation.Validate() error = %v, wantError = %t", err, test.wantError)
			}
		})
	}
}

// TestTicketValidateResolverAuthority covers every signed resolver-specific boundary.
func TestTicketValidateResolverAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Ticket)
	}{
		{name: "identity ownership", mutate: func(ticket *Ticket) {
			ticket.OwnershipSchemaVersion = identityOwnershipSchemaVersion
			ticket.NetworkPolicyFingerprint = ""
		}},
		{name: "missing policy", mutate: func(ticket *Ticket) { ticket.NetworkPolicy = nil }},
		{name: "invalid policy", mutate: func(ticket *Ticket) { ticket.NetworkPolicy.Suffix = ".invalid" }},
		{name: "policy fingerprint mismatch", mutate: func(ticket *Ticket) {
			ticket.NetworkPolicyFingerprint = strings.Repeat("f", fingerprintLength)
		}},
		{name: "missing observation", mutate: func(ticket *Ticket) { ticket.ExpectedResolverObservation = nil }},
		{name: "invalid observation", mutate: func(ticket *Ticket) {
			ticket.ExpectedResolverObservation.Fingerprint = "bad"
		}},
		{name: "address authority", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "127.77.0.10" }},
		{name: "assignment authority", mutate: func(ticket *Ticket) {
			ticket.ExpectedObservation = ExpectedObservation{State: ObservationOwned, Fingerprint: testFingerprint()}
		}},
		{name: "pre-assignment authority", mutate: func(ticket *Ticket) { ticket.ExpectedPreAssignment = testExpectedPreAssignment() }},
		{name: "pool authority", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool = &ExpectedLoopbackPool{}
		}},
	}
	for _, operation := range []Operation{OperationEnsureResolver, OperationReleaseResolver} {
		t.Run(string(operation), func(t *testing.T) {
			if err := validTestResolverTicket(now, operation).Validate(now); err != nil {
				t.Fatalf("Ticket.Validate() valid resolver error = %v", err)
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					ticket := validTestResolverTicket(now, operation)
					test.mutate(&ticket)
					if err := ticket.Validate(now); err == nil {
						t.Fatalf("Ticket.Validate() accepted resolver mutation %#v", ticket)
					}
				})
			}
		})
	}
}

// TestSocketRequirementValidate covers the complete transport and port allowlist.
func TestSocketRequirementValidate(t *testing.T) {
	tests := []struct {
		name        string
		requirement SocketRequirement
		wantError   bool
	}{
		{name: "TCP", requirement: SocketRequirement{Transport: SocketTransportTCP4, Port: 443}},
		{name: "UDP", requirement: SocketRequirement{Transport: SocketTransportUDP4, Port: 53}},
		{name: "unknown transport", requirement: SocketRequirement{Transport: "tcp6", Port: 443}, wantError: true},
		{name: "empty transport", requirement: SocketRequirement{Port: 443}, wantError: true},
		{name: "zero port", requirement: SocketRequirement{Transport: SocketTransportTCP4}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.requirement.Validate()
			if test.wantError && err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !test.wantError && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

// TestExpectedPreAssignmentValidate enforces explicit bounded requirements in unique transport-then-port order.
func TestExpectedPreAssignmentValidate(t *testing.T) {
	maximum := make([]SocketRequirement, MaximumSocketRequirements)
	for index := range maximum {
		maximum[index] = SocketRequirement{Transport: SocketTransportTCP4, Port: uint16(index + 1)}
	}
	tests := []struct {
		name      string
		expected  ExpectedPreAssignment
		wantError bool
	}{
		{name: "route only", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: []SocketRequirement{}}},
		{name: "canonical", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: []SocketRequirement{
			{Transport: SocketTransportTCP4, Port: 53},
			{Transport: SocketTransportTCP4, Port: 3306},
			{Transport: SocketTransportUDP4, Port: 53},
		}}},
		{name: "maximum", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: maximum}},
		{name: "missing fingerprint", expected: ExpectedPreAssignment{Requirements: []SocketRequirement{}}, wantError: true},
		{name: "uppercase fingerprint", expected: ExpectedPreAssignment{Fingerprint: strings.Repeat("A", fingerprintLength), Requirements: []SocketRequirement{}}, wantError: true},
		{name: "non hexadecimal fingerprint", expected: ExpectedPreAssignment{Fingerprint: strings.Repeat("z", fingerprintLength), Requirements: []SocketRequirement{}}, wantError: true},
		{name: "implicit requirements", expected: ExpectedPreAssignment{Fingerprint: testFingerprint()}, wantError: true},
		{name: "too many", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: append(maximum, SocketRequirement{Transport: SocketTransportTCP4, Port: 129})}, wantError: true},
		{name: "unknown transport", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: []SocketRequirement{{Transport: "tcp6", Port: 443}}}, wantError: true},
		{name: "zero port", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: []SocketRequirement{{Transport: SocketTransportTCP4}}}, wantError: true},
		{name: "transport order", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: []SocketRequirement{
			{Transport: SocketTransportUDP4, Port: 53},
			{Transport: SocketTransportTCP4, Port: 53},
		}}, wantError: true},
		{name: "port order", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: []SocketRequirement{
			{Transport: SocketTransportTCP4, Port: 443},
			{Transport: SocketTransportTCP4, Port: 80},
		}}, wantError: true},
		{name: "duplicate", expected: ExpectedPreAssignment{Fingerprint: testFingerprint(), Requirements: []SocketRequirement{
			{Transport: SocketTransportTCP4, Port: 443},
			{Transport: SocketTransportTCP4, Port: 443},
		}}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.expected.Validate()
			if test.wantError && err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !test.wantError && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

// TestExpectedLoopbackPoolValidate accepts only the complete canonical /29 authority shape.
func TestExpectedLoopbackPoolValidate(t *testing.T) {
	pool := netip.MustParsePrefix("127.77.0.8/29")
	expected := testExpectedLoopbackPool(pool)
	if err := expected.Validate(pool); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	expected.Identities[3].ExpectedObservation.State = ObservationOwned
	expected.Identities[3].ExpectedPreAssignment = nil
	if err := expected.Validate(pool); err != nil {
		t.Fatalf("Validate() mixed state error = %v", err)
	}
	if err := expected.Validate(netip.MustParsePrefix("127.77.0.0/24")); err == nil {
		t.Fatal("Validate() accepted a non-/29 pool")
	}
}

// TestTicketValidateInstallationIDContract verifies helper admission uses the shared installation identity domain.
func TestTicketValidateInstallationIDContract(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	valid := []string{
		"a",
		"A0._-z",
		strings.Repeat("a", MaximumInstallationIDLength),
	}
	for _, installationID := range valid {
		ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
		ticket.InstallationID = installationID
		if err := ticket.Validate(now); err != nil {
			t.Fatalf("Ticket.Validate() installation ID %q error = %v", installationID, err)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", MaximumInstallationIDLength+1),
		".harbor",
		"_harbor",
		"harbor-",
		"harbor/local",
		"harbor+local",
		"hárbor",
	}
	for _, installationID := range invalid {
		ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
		ticket.InstallationID = installationID
		err := ticket.Validate(now)
		if err == nil || requestErrorCode(t, err) != ErrorCodeInvalidTicket {
			t.Fatalf("Ticket.Validate() installation ID %q error = %v, want invalid ticket", installationID, err)
		}
	}
}

// TestTicketValidate covers every ticket field and operation-specific precondition.
func TestTicketValidate(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Ticket)
		code   ErrorCode
	}{
		{name: "previous version", mutate: func(ticket *Ticket) { ticket.Version-- }, code: ErrorCodeInvalidTicket},
		{name: "unsupported version", mutate: func(ticket *Ticket) { ticket.Version++ }, code: ErrorCodeInvalidTicket},
		{name: "unknown operation", mutate: func(ticket *Ticket) { ticket.Operation = "run_command" }, code: ErrorCodeInvalidTicket},
		{name: "empty installation", mutate: func(ticket *Ticket) { ticket.InstallationID = "" }, code: ErrorCodeInvalidTicket},
		{name: "dot installation", mutate: func(ticket *Ticket) { ticket.InstallationID = "." }, code: ErrorCodeInvalidTicket},
		{name: "punctuated installation boundary", mutate: func(ticket *Ticket) { ticket.InstallationID = "-harbor" }, code: ErrorCodeInvalidTicket},
		{name: "path installation", mutate: func(ticket *Ticket) { ticket.InstallationID = "../other" }, code: ErrorCodeInvalidTicket},
		{name: "long installation", mutate: func(ticket *Ticket) { ticket.InstallationID = strings.Repeat("a", MaximumInstallationIDLength+1) }, code: ErrorCodeInvalidTicket},
		{name: "empty requester", mutate: func(ticket *Ticket) { ticket.RequesterIdentity = "" }, code: ErrorCodeInvalidTicket},
		{name: "path requester", mutate: func(ticket *Ticket) { ticket.RequesterIdentity = "../peer" }, code: ErrorCodeInvalidTicket},
		{name: "long path requester", mutate: func(ticket *Ticket) {
			ticket.RequesterIdentity = strings.Repeat("a", MaximumRequesterIdentityLength-2) + "/a"
		}, code: ErrorCodeInvalidTicket},
		{name: "long requester", mutate: func(ticket *Ticket) {
			ticket.RequesterIdentity = strings.Repeat("a", MaximumRequesterIdentityLength+1)
		}, code: ErrorCodeInvalidTicket},
		{name: "zero generation", mutate: func(ticket *Ticket) { ticket.OwnershipGeneration = 0 }, code: ErrorCodeInvalidTicket},
		{name: "missing ownership schema", mutate: func(ticket *Ticket) { ticket.OwnershipSchemaVersion = 0 }, code: ErrorCodeInvalidTicket},
		{name: "future ownership schema", mutate: func(ticket *Ticket) { ticket.OwnershipSchemaVersion = networkPolicyOwnershipSchemaVersion + 1 }, code: ErrorCodeInvalidTicket},
		{name: "identity schema with policy fingerprint", mutate: func(ticket *Ticket) {
			ticket.NetworkPolicyFingerprint = testFingerprint()
		}, code: ErrorCodeInvalidTicket},
		{name: "network policy schema without fingerprint", mutate: func(ticket *Ticket) {
			ticket.OwnershipSchemaVersion = networkPolicyOwnershipSchemaVersion
		}, code: ErrorCodeInvalidTicket},
		{name: "network policy schema with short fingerprint", mutate: func(ticket *Ticket) {
			ticket.OwnershipSchemaVersion = networkPolicyOwnershipSchemaVersion
			ticket.NetworkPolicyFingerprint = strings.Repeat("a", fingerprintLength-1)
		}, code: ErrorCodeInvalidTicket},
		{name: "network policy schema with uppercase fingerprint", mutate: func(ticket *Ticket) {
			ticket.OwnershipSchemaVersion = networkPolicyOwnershipSchemaVersion
			ticket.NetworkPolicyFingerprint = strings.Repeat("A", fingerprintLength)
		}, code: ErrorCodeInvalidTicket},
		{name: "network policy schema with nonhex fingerprint", mutate: func(ticket *Ticket) {
			ticket.OwnershipSchemaVersion = networkPolicyOwnershipSchemaVersion
			ticket.NetworkPolicyFingerprint = strings.Repeat("g", fingerprintLength)
		}, code: ErrorCodeInvalidTicket},
		{name: "missing pool", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "" }, code: ErrorCodeInvalidTicket},
		{name: "malformed pool", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "not-a-prefix" }, code: ErrorCodeInvalidTicket},
		{name: "non-loopback pool", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "192.0.2.0/24" }, code: ErrorCodeInvalidTicket},
		{name: "IPv6 loopback pool", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "::1/128" }, code: ErrorCodeInvalidTicket},
		{name: "noncanonical pool", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "127.77.0.10/24" }, code: ErrorCodeInvalidTicket},
		{name: "address outside pool", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "127.78.0.0/24" }, code: ErrorCodeInvalidTicket},
		{name: "malformed address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "not-an-address" }, code: ErrorCodeInvalidTicket},
		{name: "non loopback address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "192.0.2.10" }, code: ErrorCodeInvalidTicket},
		{name: "IPv6 loopback address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "::1" }, code: ErrorCodeInvalidTicket},
		{name: "invalid observation", mutate: func(ticket *Ticket) { ticket.ExpectedObservation.Fingerprint = "bad" }, code: ErrorCodeInvalidTicket},
		{name: "absent ensure without pre-assignment", mutate: func(ticket *Ticket) { ticket.ExpectedPreAssignment = nil }, code: ErrorCodeInvalidTicket},
		{name: "absent ensure with invalid pre-assignment", mutate: func(ticket *Ticket) { ticket.ExpectedPreAssignment.Fingerprint = "bad" }, code: ErrorCodeInvalidTicket},
		{name: "owned ensure with pre-assignment", mutate: func(ticket *Ticket) { ticket.ExpectedObservation.State = ObservationOwned }, code: ErrorCodeInvalidTicket},
		{name: "release with pre-assignment", mutate: func(ticket *Ticket) {
			ticket.Operation = OperationReleaseLoopbackIdentity
			ticket.ExpectedObservation.State = ObservationOwned
		}, code: ErrorCodeInvalidTicket},
		{name: "release absent observation", mutate: func(ticket *Ticket) {
			ticket.Operation = OperationReleaseLoopbackIdentity
			ticket.ExpectedObservation.State = ObservationAbsent
			ticket.ExpectedPreAssignment = nil
		}, code: ErrorCodeInvalidTicket},
		{name: "single address with pool authority", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool = &ExpectedLoopbackPool{}
		}, code: ErrorCodeInvalidTicket},
		{name: "short nonce", mutate: func(ticket *Ticket) { ticket.Nonce = "short" }, code: ErrorCodeInvalidTicket},
		{name: "path nonce", mutate: func(ticket *Ticket) { ticket.Nonce = strings.Repeat("a", 31) + "/" }, code: ErrorCodeInvalidTicket},
		{name: "long nonce", mutate: func(ticket *Ticket) { ticket.Nonce = strings.Repeat("a", maximumNonceLength+1) }, code: ErrorCodeInvalidTicket},
		{name: "zero expiry", mutate: func(ticket *Ticket) { ticket.ExpiresAt = time.Time{} }, code: ErrorCodeInvalidTicket},
		{name: "expired", mutate: func(ticket *Ticket) { ticket.ExpiresAt = now }, code: ErrorCodeInvalidTicket},
		{name: "non UTC expiry", mutate: func(ticket *Ticket) { ticket.ExpiresAt = ticket.ExpiresAt.In(time.FixedZone("offset", 3600)) }, code: ErrorCodeInvalidTicket},
		{name: "excessive lifetime", mutate: func(ticket *Ticket) { ticket.ExpiresAt = now.Add(MaxTicketLifetime + time.Nanosecond) }, code: ErrorCodeInvalidTicket},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := validTestTicket(now, OperationEnsureLoopbackIdentity)
			test.mutate(&ticket)
			err := ticket.Validate(now)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if got := requestErrorCode(t, err); got != test.code {
				t.Fatalf("error code = %q, want %q", got, test.code)
			}
		})
	}
}

// TestTicketValidateLoopbackPool covers every exact-pool boundary without weakening legacy single-address authority.
func TestTicketValidateLoopbackPool(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Ticket)
	}{
		{name: "wrong prefix size", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "127.77.0.0/28" }},
		{name: "noncanonical prefix", mutate: func(ticket *Ticket) { ticket.ApprovedPool = "127.77.0.9/29" }},
		{name: "missing authority", mutate: func(ticket *Ticket) { ticket.ExpectedLoopbackPool = nil }},
		{name: "legacy address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "127.77.0.8" }},
		{name: "legacy observation", mutate: func(ticket *Ticket) {
			ticket.ExpectedObservation = ExpectedObservation{State: ObservationAbsent, Fingerprint: testFingerprint()}
		}},
		{name: "legacy pre-assignment", mutate: func(ticket *Ticket) { ticket.ExpectedPreAssignment = testExpectedPreAssignment() }},
		{name: "seven identities", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities = ticket.ExpectedLoopbackPool.Identities[:7]
		}},
		{name: "nine identities", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities = append(
				ticket.ExpectedLoopbackPool.Identities,
				ticket.ExpectedLoopbackPool.Identities[7],
			)
		}},
		{name: "out of order", mutate: func(ticket *Ticket) {
			identities := ticket.ExpectedLoopbackPool.Identities
			identities[2], identities[3] = identities[3], identities[2]
		}},
		{name: "duplicate address", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[3].Address = ticket.ExpectedLoopbackPool.Identities[2].Address
		}},
		{name: "outside address", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[7].Address = "127.77.0.16"
		}},
		{name: "malformed address", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].Address = "not-an-address"
		}},
		{name: "invalid observation", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedObservation.Fingerprint = "bad"
		}},
		{name: "absent without pre-assignment", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment = nil
		}},
		{name: "absent with implicit requirements", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment.Requirements = nil
		}},
		{name: "absent with socket authority", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment.Requirements = []SocketRequirement{{
				Transport: SocketTransportTCP4,
				Port:      443,
			}}
		}},
		{name: "owned with pre-assignment", mutate: func(ticket *Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedObservation.State = ObservationOwned
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := validTestPoolTicket(now)
			test.mutate(&ticket)
			err := ticket.Validate(now)
			if err == nil || requestErrorCode(t, err) != ErrorCodeInvalidTicket {
				t.Fatalf("Ticket.Validate() error = %v, want invalid ticket", err)
			}
		})
	}
}

// TestRequestValidate verifies protocol and opaque-reference bounds before redemption.
func TestRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		request Request
	}{
		{name: "previous version", request: Request{Version: ProtocolVersion - 1, TicketReference: testTicketReference()}},
		{name: "unsupported version", request: Request{Version: ProtocolVersion + 1, TicketReference: testTicketReference()}},
		{name: "empty reference", request: Request{Version: ProtocolVersion}},
		{name: "short reference", request: Request{Version: ProtocolVersion, TicketReference: "short"}},
		{name: "long reference", request: Request{Version: ProtocolVersion, TicketReference: TicketReference(strings.Repeat("a", ticketReferenceLength+1))}},
		{name: "path reference", request: Request{Version: ProtocolVersion, TicketReference: TicketReference(strings.Repeat("a", ticketReferenceLength-2) + "/x")}},
		{name: "uppercase reference", request: Request{Version: ProtocolVersion, TicketReference: TicketReference(strings.Repeat("A", ticketReferenceLength))}},
		{name: "non hexadecimal reference", request: Request{Version: ProtocolVersion, TicketReference: TicketReference(strings.Repeat("r", ticketReferenceLength))}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); err == nil || requestErrorCode(t, err) != ErrorCodeInvalidTicket {
				t.Fatalf("validate request error = %v, want invalid ticket", err)
			}
		})
	}

	request := validTestRequest(TicketReference(strings.Repeat("a", ticketReferenceLength)))
	if err := request.Validate(); err != nil {
		t.Fatalf("validate canonical reference: %v", err)
	}
}

// TestRequestErrorError returns only the bounded client-facing message.
func TestRequestErrorError(t *testing.T) {
	err := newRequestError(ErrorCodeInvalidTicket, "bounded message")
	if got, want := err.Error(), "bounded message"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestTicketValidateAcceptsAllowlistedShapes verifies both operations and idempotent ensure observations.
func TestTicketValidateAcceptsAllowlistedShapes(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tickets := []Ticket{
		validTestTicket(now, OperationEnsureLoopbackIdentity),
		validTestPoolTicket(now),
		validTestTicket(now, OperationReleaseLoopbackIdentity),
	}
	mixedPool := validTestPoolTicket(now)
	mixedPool.ExpectedLoopbackPool.Identities[4].ExpectedObservation.State = ObservationOwned
	mixedPool.ExpectedLoopbackPool.Identities[4].ExpectedPreAssignment = nil
	tickets = append(tickets, mixedPool)
	ownedEnsure := validTestTicket(now, OperationEnsureLoopbackIdentity)
	ownedEnsure.ExpectedObservation.State = ObservationOwned
	ownedEnsure.ExpectedPreAssignment = nil
	tickets = append(tickets, ownedEnsure)
	portEnsure := validTestTicket(now, OperationEnsureLoopbackIdentity)
	portEnsure.ExpectedPreAssignment.Requirements = []SocketRequirement{
		{Transport: SocketTransportTCP4, Port: 443},
		{Transport: SocketTransportUDP4, Port: 53},
	}
	tickets = append(tickets, portEnsure)
	maximumExpiry := validTestTicket(now, OperationEnsureLoopbackIdentity)
	maximumExpiry.ExpiresAt = now.Add(MaxTicketLifetime)
	tickets = append(tickets, maximumExpiry)
	maximumRequester := validTestTicket(now, OperationEnsureLoopbackIdentity)
	maximumRequester.RequesterIdentity = strings.Repeat("a", MaximumRequesterIdentityLength)
	tickets = append(tickets, maximumRequester)
	policyBound := validTestTicket(now, OperationEnsureLoopbackIdentity)
	policyBound.OwnershipSchemaVersion = networkPolicyOwnershipSchemaVersion
	policyBound.NetworkPolicyFingerprint = strings.Repeat("c", fingerprintLength)
	tickets = append(tickets, policyBound)
	policyBoundPool := validTestPoolTicket(now)
	policyBoundPool.OwnershipSchemaVersion = networkPolicyOwnershipSchemaVersion
	policyBoundPool.NetworkPolicyFingerprint = strings.Repeat("d", fingerprintLength)
	tickets = append(tickets, policyBoundPool)

	for _, ticket := range tickets {
		if err := ticket.Validate(now); err != nil {
			t.Fatalf("validate %q ticket: %v", ticket.Operation, err)
		}
	}
}

// TestTicketJSONPinsV3IdentityShapeAndOmitsUnusedPoolFields pins the schema-bound v3 encoding.
func TestTicketJSONPinsV3IdentityShapeAndOmitsUnusedPoolFields(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	legacy, err := json.Marshal(validTestTicket(now, OperationEnsureLoopbackIdentity))
	if err != nil {
		t.Fatalf("json.Marshal(legacy) error = %v", err)
	}
	wantLegacy := `{"version":3,"operation":"ensure_loopback_identity","installation_id":"harbor-test-installation","requester_identity":"uid-1000","ownership_generation":7,"ownership_schema_version":1,"approved_pool":"127.77.0.0/24","approved_address":"127.77.0.10","expected_observation":{"state":"absent","fingerprint":"` + strings.Repeat("a", fingerprintLength) + `"},"expected_pre_assignment":{"fingerprint":"` + strings.Repeat("b", fingerprintLength) + `","requirements":[]},"nonce":"` + strings.Repeat("n", minimumNonceLength) + `","expires_at":"2026-07-18T12:01:00Z"}`
	if string(legacy) != wantLegacy {
		t.Fatalf("legacy ticket JSON = %s, want %s", legacy, wantLegacy)
	}

	pool, err := json.Marshal(validTestPoolTicket(now))
	if err != nil {
		t.Fatalf("json.Marshal(pool) error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(pool, &fields); err != nil {
		t.Fatalf("json.Unmarshal(pool) error = %v", err)
	}
	for _, field := range []string{"approved_address", "expected_observation", "expected_pre_assignment"} {
		if _, found := fields[field]; found {
			t.Fatalf("pool ticket JSON contains legacy field %q: %s", field, pool)
		}
	}
	if _, found := fields["expected_loopback_pool"]; !found {
		t.Fatalf("pool ticket JSON is missing expected_loopback_pool: %s", pool)
	}
}

// validTestTicket returns a canonical ticket for focused mutation in tests.
func validTestTicket(now time.Time, operation Operation) Ticket {
	state := ObservationAbsent
	if operation == OperationReleaseLoopbackIdentity {
		state = ObservationOwned
	}
	ticket := Ticket{
		Version:                ProtocolVersion,
		Operation:              operation,
		InstallationID:         "harbor-test-installation",
		RequesterIdentity:      "uid-1000",
		OwnershipGeneration:    7,
		OwnershipSchemaVersion: identityOwnershipSchemaVersion,
		ApprovedPool:           "127.77.0.0/24",
		ApprovedAddress:        "127.77.0.10",
		ExpectedObservation: ExpectedObservation{
			State:       state,
			Fingerprint: testFingerprint(),
		},
		Nonce:     strings.Repeat("n", minimumNonceLength),
		ExpiresAt: now.Add(time.Minute),
	}
	if operation == OperationEnsureLoopbackIdentity {
		ticket.ExpectedPreAssignment = testExpectedPreAssignment()
	}
	return ticket
}

// validTestPoolTicket returns one canonical exact-eight route-only pool authority.
func validTestPoolTicket(now time.Time) Ticket {
	pool := netip.MustParsePrefix("127.77.0.8/29")
	return Ticket{
		Version:                ProtocolVersion,
		Operation:              OperationEnsureLoopbackPool,
		InstallationID:         "harbor-test-installation",
		RequesterIdentity:      "uid-1000",
		OwnershipGeneration:    7,
		OwnershipSchemaVersion: identityOwnershipSchemaVersion,
		ApprovedPool:           pool.String(),
		ExpectedLoopbackPool: func() *ExpectedLoopbackPool {
			expected := testExpectedLoopbackPool(pool)
			return &expected
		}(),
		Nonce:     strings.Repeat("n", minimumNonceLength),
		ExpiresAt: now.Add(time.Minute),
	}
}

// validTestResolverTicket returns one canonical policy-bound resolver authority.
func validTestResolverTicket(now time.Time, operation Operation) Ticket {
	policy := testResolverPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		panic(err)
	}
	return Ticket{
		Version:                  ProtocolVersion,
		Operation:                operation,
		InstallationID:           "harbor-test-installation",
		RequesterIdentity:        "uid-1000",
		OwnershipGeneration:      7,
		OwnershipSchemaVersion:   networkPolicyOwnershipSchemaVersion,
		NetworkPolicyFingerprint: fingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		ExpectedResolverObservation: &ExpectedResolverObservation{
			Fingerprint: testFingerprint(),
		},
		Nonce:     strings.Repeat("n", minimumNonceLength),
		ExpiresAt: now.Add(time.Minute),
	}
}

// testResolverPolicy returns one complete macOS policy suitable for signed-ticket tests.
func testResolverPolicy() networkpolicy.Policy {
	localhost := netip.MustParseAddr("127.0.0.1")
	dns := netip.AddrPortFrom(localhost, 25000)
	policy, err := networkpolicy.New(
		strings.Repeat("a", fingerprintLength),
		networkpolicy.MacOSMechanisms(),
		networkpolicy.Listener{Advertised: dns, Bind: dns},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(localhost, 80),
			Bind:       netip.AddrPortFrom(localhost, 25001),
		},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(localhost, 443),
			Bind:       netip.AddrPortFrom(localhost, 25002),
		},
	)
	if err != nil {
		panic(err)
	}
	return policy
}

// testExpectedLoopbackPool returns all addresses from one canonical /29 with explicit route-only absent-state evidence.
func testExpectedLoopbackPool(pool netip.Prefix) ExpectedLoopbackPool {
	identities := make([]ExpectedLoopbackIdentity, 0, loopbackPoolIdentities)
	address := pool.Addr()
	for range loopbackPoolIdentities {
		identities = append(identities, ExpectedLoopbackIdentity{
			Address: address.String(),
			ExpectedObservation: ExpectedObservation{
				State:       ObservationAbsent,
				Fingerprint: testFingerprint(),
			},
			ExpectedPreAssignment: testExpectedPreAssignment(),
		})
		address = address.Next()
	}
	return ExpectedLoopbackPool{Identities: identities}
}

// testExpectedPreAssignment returns an explicit route-only safety observation.
func testExpectedPreAssignment() *ExpectedPreAssignment {
	return &ExpectedPreAssignment{Fingerprint: strings.Repeat("b", fingerprintLength), Requirements: []SocketRequirement{}}
}

// validTestRequest returns the complete wire envelope for an opaque reference.
func validTestRequest(reference TicketReference) Request {
	return Request{Version: ProtocolVersion, TicketReference: reference}
}

// testTicketReference returns one canonical high-entropy-shaped opaque handle.
func testTicketReference() TicketReference {
	return TicketReference(strings.Repeat("a", ticketReferenceLength))
}

// testFingerprint returns a canonical observation digest without coupling tests to hashing details.
func testFingerprint() string {
	return strings.Repeat("a", fingerprintLength)
}

// requestErrorCode extracts the stable protocol code asserted by validation tests.
func requestErrorCode(t *testing.T, err error) ErrorCode {
	t.Helper()
	var requestError *RequestError
	if !errors.As(err, &requestError) {
		t.Fatalf("error %T is not a RequestError: %v", err, err)
	}
	return requestError.Code
}
