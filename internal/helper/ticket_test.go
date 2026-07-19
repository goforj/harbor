package helper

import (
	"errors"
	"strings"
	"testing"
	"time"
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
		validTestTicket(now, OperationReleaseLoopbackIdentity),
	}
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

	for _, ticket := range tickets {
		if err := ticket.Validate(now); err != nil {
			t.Fatalf("validate %q ticket: %v", ticket.Operation, err)
		}
	}
}

// validTestTicket returns a canonical ticket for focused mutation in tests.
func validTestTicket(now time.Time, operation Operation) Ticket {
	state := ObservationAbsent
	if operation == OperationReleaseLoopbackIdentity {
		state = ObservationOwned
	}
	ticket := Ticket{
		Version:             ProtocolVersion,
		Operation:           operation,
		InstallationID:      "harbor-test-installation",
		RequesterIdentity:   "uid-1000",
		OwnershipGeneration: 7,
		ApprovedPool:        "127.77.0.0/24",
		ApprovedAddress:     "127.77.0.10",
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
