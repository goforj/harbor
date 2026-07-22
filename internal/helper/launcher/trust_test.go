package launcher

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// TestInvokeTrustWritesCanonicalRequestAndCorrelatesEvidence verifies the existing transport accepts only the issued trust authority.
func TestInvokeTrustWritesCanonicalRequestAndCorrelatesEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	issued := validTrustLaunchTicket(t, now)
	transport := transportFunc(func(_ context.Context, request io.Reader, response io.Writer) TransportResult {
		decoded, err := helper.DecodeRequest(request)
		if err != nil {
			t.Fatalf("DecodeRequest() error = %v", err)
		}
		if decoded.TicketReference != issued.reference {
			t.Fatalf("helper request = %#v", decoded)
		}
		if err := helper.WriteResponse(response, trustSuccessResponse(issued)); err != nil {
			t.Fatalf("WriteResponse() error = %v", err)
		}
		return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
	})

	outcome, err := New(transport, fixedClock{now: now}).InvokeTrust(t.Context(), issued)
	if err != nil {
		t.Fatalf("InvokeTrust() error = %v", err)
	}
	if outcome.State != Succeeded || outcome.Response.Result == nil || outcome.Response.Result.TrustEvidence == nil {
		t.Fatalf("InvokeTrust() outcome = %#v", outcome)
	}
}

// TestInvokeTrustReleaseWritesCanonicalRequestAndCorrelatesOwnedAbsence proves release trust launch accepts only the requested destructive postcondition.
func TestInvokeTrustReleaseWritesCanonicalRequestAndCorrelatesOwnedAbsence(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	issued, err := NewTrustLaunchTicket(
		"operation-trust-release",
		helper.TicketReference(strings.Repeat("e", 64)),
		helper.OperationReleaseTrust,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		strings.Repeat("c", 64),
		string(networkpolicy.DarwinCurrentUserTrust),
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewTrustLaunchTicket() error = %v", err)
	}
	transport := transportFunc(func(_ context.Context, request io.Reader, response io.Writer) TransportResult {
		decoded, err := helper.DecodeRequest(request)
		if err != nil {
			t.Fatalf("DecodeRequest() error = %v", err)
		}
		if decoded.TicketReference != issued.reference {
			t.Fatalf("helper request = %#v", decoded)
		}
		if err := helper.WriteResponse(response, helper.Response{
			Version: helper.ProtocolVersion,
			OK:      true,
			Result: &helper.OperationResult{
				Operation: helper.OperationReleaseTrust,
				TrustEvidence: &helper.TrustMutationEvidence{
					AuthorityFingerprint:   issued.authorityFingerprint,
					Mechanism:              networkpolicy.TrustMechanism(issued.trustMechanism),
					ObservationFingerprint: strings.Repeat("d", 64),
					Postcondition:          helper.TrustPostconditionOwnedAbsent,
				},
			},
		}); err != nil {
			t.Fatalf("WriteResponse() error = %v", err)
		}
		return TransportResult{
			State:    TransportCompleted,
			ExitCode: ExitCodeSucceeded,
		}
	})

	outcome, err := New(transport, fixedClock{now: now}).InvokeTrust(t.Context(), issued)
	if err != nil {
		t.Fatalf("InvokeTrust() error = %v", err)
	}
	if outcome.State != Succeeded ||
		outcome.Response.Result == nil ||
		outcome.Response.Result.Operation != helper.OperationReleaseTrust ||
		outcome.Response.Result.TrustEvidence == nil ||
		outcome.Response.Result.TrustEvidence.Postcondition != helper.TrustPostconditionOwnedAbsent {
		t.Fatalf("InvokeTrust() outcome = %#v", outcome)
	}
}

// TestInvokeTrustRejectsUncorrelatedEvidence verifies an effect for another CA or trust mechanism is indeterminate.
func TestInvokeTrustRejectsUncorrelatedEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	issued := validTrustLaunchTicket(t, now)
	for _, test := range []struct {
		name   string
		mutate func(*helper.TrustMutationEvidence)
	}{
		{name: "authority", mutate: func(evidence *helper.TrustMutationEvidence) { evidence.AuthorityFingerprint = strings.Repeat("f", 64) }},
		{name: "mechanism", mutate: func(evidence *helper.TrustMutationEvidence) {
			evidence.Mechanism = networkpolicy.DarwinAdministratorTrust
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := transportFunc(func(_ context.Context, _ io.Reader, response io.Writer) TransportResult {
				responseValue := trustSuccessResponse(issued)
				test.mutate(responseValue.Result.TrustEvidence)
				if err := helper.WriteResponse(response, responseValue); err != nil {
					t.Fatalf("WriteResponse() error = %v", err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
			})
			outcome, err := New(transport, fixedClock{now: now}).InvokeTrust(t.Context(), issued)
			if err != nil {
				t.Fatalf("InvokeTrust() error = %v", err)
			}
			if outcome.State != Indeterminate || outcome.Exit == nil {
				t.Fatalf("InvokeTrust() outcome = %#v", outcome)
			}
		})
	}
}

// TestNewTrustLaunchTicketValidatesMetadata verifies malformed approval metadata cannot open native consent.
func TestNewTrustLaunchTicketValidatesMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	valid := trustLaunchTicketFixture{operationID: "operation-trust", reference: helper.TicketReference(strings.Repeat("e", 64)), operation: helper.OperationEnsureTrust, policyFingerprint: strings.Repeat("a", 64), ownershipFingerprint: strings.Repeat("b", 64), authorityFingerprint: strings.Repeat("c", 64), mechanism: string(networkpolicy.DarwinCurrentUserTrust), expiresAt: now.Add(time.Minute)}
	administrator := valid
	administrator.mechanism = string(networkpolicy.DarwinAdministratorTrust)
	if _, err := newTrustLaunchTicket(administrator); err != nil {
		t.Fatalf("administrator newTrustLaunchTicket() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*trustLaunchTicketFixture)
	}{
		{name: "valid", mutate: func(*trustLaunchTicketFixture) {}},
		{name: "operation ID", mutate: func(value *trustLaunchTicketFixture) { value.operationID = "" }},
		{name: "reference", mutate: func(value *trustLaunchTicketFixture) { value.reference = "bad" }},
		{
			name: "release operation",
			mutate: func(value *trustLaunchTicketFixture) {
				value.operation = helper.OperationReleaseTrust
			},
		},
		{
			name: "operation",
			mutate: func(value *trustLaunchTicketFixture) {
				value.operation = helper.OperationEnsureLowPorts
			},
		},
		{name: "policy fingerprint", mutate: func(value *trustLaunchTicketFixture) { value.policyFingerprint = "bad" }},
		{name: "policy uppercase", mutate: func(value *trustLaunchTicketFixture) { value.policyFingerprint = strings.Repeat("A", 64) }},
		{name: "ownership fingerprint", mutate: func(value *trustLaunchTicketFixture) { value.ownershipFingerprint = "bad" }},
		{name: "ownership uppercase", mutate: func(value *trustLaunchTicketFixture) { value.ownershipFingerprint = strings.Repeat("B", 64) }},
		{name: "authority fingerprint", mutate: func(value *trustLaunchTicketFixture) { value.authorityFingerprint = "bad" }},
		{name: "authority uppercase", mutate: func(value *trustLaunchTicketFixture) { value.authorityFingerprint = strings.Repeat("C", 64) }},
		{name: "unknown mechanism", mutate: func(value *trustLaunchTicketFixture) { value.mechanism = "unsupported" }},
		{name: "mixed mechanism", mutate: func(value *trustLaunchTicketFixture) {
			value.mechanism = string(networkpolicy.DarwinAdministratorTrust) + "," + string(networkpolicy.DarwinCurrentUserTrust)
		}},
		{name: "zero expiry", mutate: func(value *trustLaunchTicketFixture) { value.expiresAt = time.Time{} }},
		{name: "local expiry", mutate: func(value *trustLaunchTicketFixture) {
			value.expiresAt = value.expiresAt.In(time.FixedZone("local", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			_, err := newTrustLaunchTicket(value)
			if (err != nil) != (test.name != "valid" && test.name != "release operation") {
				t.Fatalf("newTrustLaunchTicket() error = %v", err)
			}
		})
	}
}

// TestInvokeTrustRejectsExpiredAndCanceledAuthority verifies stale or canceled launches cannot reach the helper transport.
func TestInvokeTrustRejectsExpiredAndCanceledAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		ticket TrustLaunchTicket
		ctx    context.Context
	}{
		{name: "expired", ticket: trustLaunchTicketWithExpiry(t, now)},
		{name: "overlong", ticket: trustLaunchTicketWithExpiry(t, now.Add(helper.MaxTicketLifetime+time.Nanosecond))},
		{name: "canceled", ticket: validTrustLaunchTicket(t, now), ctx: canceledTrustLaunchContext()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := transportFunc(func(context.Context, io.Reader, io.Writer) TransportResult { calls++; return TransportResult{} })
			ctx := test.ctx
			if ctx == nil {
				ctx = t.Context()
			}
			if _, err := New(transport, fixedClock{now: now}).InvokeTrust(ctx, test.ticket); err == nil || calls != 0 {
				t.Fatalf("InvokeTrust() error/calls = %v/%d", err, calls)
			}
		})
	}
}

// validTrustLaunchTicket creates one short-lived ensure-trust capability for launcher tests.
func validTrustLaunchTicket(t *testing.T, now time.Time) TrustLaunchTicket {
	t.Helper()
	ticket := trustLaunchTicketWithExpiry(t, now.Add(time.Minute))
	return ticket
}

// trustLaunchTicketFixture holds typed approval metadata for constructor validation tests.
type trustLaunchTicketFixture struct {
	operationID          domain.OperationID
	reference            helper.TicketReference
	operation            helper.Operation
	policyFingerprint    string
	ownershipFingerprint string
	authorityFingerprint string
	mechanism            string
	expiresAt            time.Time
}

// newTrustLaunchTicket converts one typed fixture into validated opaque launch metadata.
func newTrustLaunchTicket(value trustLaunchTicketFixture) (TrustLaunchTicket, error) {
	return NewTrustLaunchTicket(value.operationID, value.reference, value.operation, value.policyFingerprint, value.ownershipFingerprint, value.authorityFingerprint, value.mechanism, value.expiresAt)
}

// trustLaunchTicketWithExpiry creates valid structured metadata with a caller-selected lifetime.
func trustLaunchTicketWithExpiry(t *testing.T, expiresAt time.Time) TrustLaunchTicket {
	t.Helper()
	ticket, err := NewTrustLaunchTicket("operation-trust", helper.TicketReference(strings.Repeat("e", 64)), helper.OperationEnsureTrust, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), string(networkpolicy.DarwinCurrentUserTrust), expiresAt)
	if err != nil {
		t.Fatalf("NewTrustLaunchTicket() error = %v", err)
	}
	return ticket
}

// canceledTrustLaunchContext returns a context that cannot authorize a helper transport attempt.
func canceledTrustLaunchContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// trustSuccessResponse creates one protocol-valid ensure-trust response matching ticket.
func trustSuccessResponse(ticket TrustLaunchTicket) helper.Response {
	return helper.Response{Version: helper.ProtocolVersion, OK: true, Result: &helper.OperationResult{Operation: helper.OperationEnsureTrust, TrustEvidence: &helper.TrustMutationEvidence{AuthorityFingerprint: ticket.authorityFingerprint, Mechanism: networkpolicy.TrustMechanism(ticket.trustMechanism), ObservationFingerprint: strings.Repeat("d", 64), Postcondition: helper.TrustPostconditionExact}}}
}
