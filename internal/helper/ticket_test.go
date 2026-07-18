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

// TestTicketValidate covers every ticket field and operation-specific precondition.
func TestTicketValidate(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Ticket)
		code   ErrorCode
	}{
		{name: "unsupported version", mutate: func(ticket *Ticket) { ticket.Version++ }, code: ErrorCodeInvalidTicket},
		{name: "unknown operation", mutate: func(ticket *Ticket) { ticket.Operation = "run_command" }, code: ErrorCodeInvalidTicket},
		{name: "empty daemon", mutate: func(ticket *Ticket) { ticket.DaemonIdentity = "" }, code: ErrorCodeInvalidTicket},
		{name: "path daemon", mutate: func(ticket *Ticket) { ticket.DaemonIdentity = "../daemon" }, code: ErrorCodeInvalidTicket},
		{name: "empty installation", mutate: func(ticket *Ticket) { ticket.InstallationID = "" }, code: ErrorCodeInvalidTicket},
		{name: "dot installation", mutate: func(ticket *Ticket) { ticket.InstallationID = "." }, code: ErrorCodeInvalidTicket},
		{name: "punctuated installation boundary", mutate: func(ticket *Ticket) { ticket.InstallationID = "-harbor" }, code: ErrorCodeInvalidTicket},
		{name: "path installation", mutate: func(ticket *Ticket) { ticket.InstallationID = "../other" }, code: ErrorCodeInvalidTicket},
		{name: "long installation", mutate: func(ticket *Ticket) { ticket.InstallationID = strings.Repeat("a", maximumIDLength+1) }, code: ErrorCodeInvalidTicket},
		{name: "empty requester", mutate: func(ticket *Ticket) { ticket.RequesterIdentity = "" }, code: ErrorCodeInvalidTicket},
		{name: "path requester", mutate: func(ticket *Ticket) { ticket.RequesterIdentity = "../peer" }, code: ErrorCodeInvalidTicket},
		{name: "zero generation", mutate: func(ticket *Ticket) { ticket.OwnershipGeneration = 0 }, code: ErrorCodeInvalidTicket},
		{name: "malformed address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "not-an-address" }, code: ErrorCodeInvalidTicket},
		{name: "non loopback address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "192.0.2.10" }, code: ErrorCodeInvalidTicket},
		{name: "IPv6 loopback address", mutate: func(ticket *Ticket) { ticket.ApprovedAddress = "::1" }, code: ErrorCodeInvalidTicket},
		{name: "invalid observation", mutate: func(ticket *Ticket) { ticket.ExpectedObservation.Fingerprint = "bad" }, code: ErrorCodeInvalidTicket},
		{name: "release absent observation", mutate: func(ticket *Ticket) {
			ticket.Operation = OperationReleaseLoopbackIdentity
			ticket.ExpectedObservation.State = ObservationAbsent
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
		{name: "unsupported version", request: Request{Version: ProtocolVersion + 1, TicketReference: testTicketReference()}},
		{name: "empty reference", request: Request{Version: ProtocolVersion}},
		{name: "short reference", request: Request{Version: ProtocolVersion, TicketReference: "short"}},
		{name: "long reference", request: Request{Version: ProtocolVersion, TicketReference: TicketReference(strings.Repeat("r", maximumReferenceLength+1))}},
		{name: "path reference", request: Request{Version: ProtocolVersion, TicketReference: TicketReference(strings.Repeat("r", minimumReferenceLength-2) + "/x")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); err == nil || requestErrorCode(t, err) != ErrorCodeInvalidTicket {
				t.Fatalf("validate request error = %v, want invalid ticket", err)
			}
		})
	}

	for _, length := range []int{minimumReferenceLength, maximumReferenceLength} {
		request := validTestRequest(TicketReference(strings.Repeat("r", length)))
		if err := request.Validate(); err != nil {
			t.Fatalf("validate %d-byte reference: %v", length, err)
		}
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
	tickets = append(tickets, ownedEnsure)
	maximumExpiry := validTestTicket(now, OperationEnsureLoopbackIdentity)
	maximumExpiry.ExpiresAt = now.Add(MaxTicketLifetime)
	tickets = append(tickets, maximumExpiry)

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
	return Ticket{
		Version:             ProtocolVersion,
		Operation:           operation,
		DaemonIdentity:      "harbord-test-daemon",
		InstallationID:      "harbor-test-installation",
		RequesterIdentity:   "uid-1000",
		OwnershipGeneration: 7,
		ApprovedAddress:     "127.77.0.10",
		ExpectedObservation: ExpectedObservation{
			State:       state,
			Fingerprint: testFingerprint(),
		},
		Nonce:     strings.Repeat("n", minimumNonceLength),
		ExpiresAt: now.Add(time.Minute),
	}
}

// validTestRequest returns the complete wire envelope for an opaque reference.
func validTestRequest(reference TicketReference) Request {
	return Request{Version: ProtocolVersion, TicketReference: reference}
}

// testTicketReference returns one canonical high-entropy-shaped opaque handle.
func testTicketReference() TicketReference {
	return TicketReference(strings.Repeat("r", minimumReferenceLength))
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
