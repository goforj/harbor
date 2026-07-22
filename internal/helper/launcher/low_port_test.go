package launcher

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

// TestInvokeLowPortsCorrelatesTheEffectWithoutEquatingPreAndPostObservations proves a successful mutation may change native evidence.
func TestInvokeLowPortsCorrelatesTheEffectWithoutEquatingPreAndPostObservations(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	for _, operation := range []helper.Operation{helper.OperationEnsureLowPorts, helper.OperationReleaseLowPorts} {
		t.Run(string(operation), func(t *testing.T) {
			issued := validLowPortLaunchTicket(t, now, operation)
			postFingerprint := strings.Repeat("d", 64)
			if postFingerprint == issued.observationFingerprint {
				t.Fatal("test postcondition fingerprint unexpectedly equals the authorized precondition")
			}
			transport := transportFunc(func(_ context.Context, request io.Reader, response io.Writer) TransportResult {
				decoded, err := helper.DecodeRequest(request)
				if err != nil {
					t.Fatalf("DecodeRequest() error = %v", err)
				}
				if decoded.TicketReference != issued.reference {
					t.Fatalf("helper request = %#v", decoded)
				}
				if err := helper.WriteResponse(response, lowPortSuccessResponse(issued, postFingerprint)); err != nil {
					t.Fatalf("WriteResponse() error = %v", err)
				}
				return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
			})

			outcome, err := New(transport, fixedClock{now: now}).InvokeLowPorts(t.Context(), issued)
			if err != nil {
				t.Fatalf("InvokeLowPorts() error = %v", err)
			}
			if outcome.State != Succeeded || outcome.Response.Result == nil || outcome.Response.Result.LowPortEvidence == nil ||
				outcome.Response.Result.LowPortEvidence.ObservationFingerprint != postFingerprint {
				t.Fatalf("InvokeLowPorts() outcome = %#v", outcome)
			}
		})
	}
}

// TestMatchLowPortLaunchTicketRejectsCrossedAuthority covers every result field that remains stable across mutation.
func TestMatchLowPortLaunchTicketRejectsCrossedAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	issued := validLowPortLaunchTicket(t, now, helper.OperationEnsureLowPorts)
	valid := lowPortSuccessResponse(issued, strings.Repeat("d", 64)).Result
	if !matchLowPortLaunchTicket(issued)(valid) {
		t.Fatal("matchLowPortLaunchTicket() rejected valid post-mutation evidence")
	}

	tests := []struct {
		name   string
		result func() *helper.OperationResult
	}{
		{name: "nil result", result: func() *helper.OperationResult { return nil }},
		{name: "operation", result: func() *helper.OperationResult {
			candidate := cloneLowPortOperationResult(valid)
			candidate.Operation = helper.OperationReleaseLowPorts
			return candidate
		}},
		{name: "missing evidence", result: func() *helper.OperationResult {
			candidate := cloneLowPortOperationResult(valid)
			candidate.LowPortEvidence = nil
			return candidate
		}},
		{name: "policy", result: func() *helper.OperationResult {
			candidate := cloneLowPortOperationResult(valid)
			candidate.LowPortEvidence.PolicyFingerprint = strings.Repeat("e", 64)
			return candidate
		}},
		{name: "ownership", result: func() *helper.OperationResult {
			candidate := cloneLowPortOperationResult(valid)
			candidate.LowPortEvidence.OwnershipFingerprint = strings.Repeat("e", 64)
			return candidate
		}},
		{name: "postcondition", result: func() *helper.OperationResult {
			candidate := cloneLowPortOperationResult(valid)
			candidate.LowPortEvidence.Postcondition = helper.LowPortPostconditionOwnedAbsent
			return candidate
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if matchLowPortLaunchTicket(issued)(test.result()) {
				t.Fatal("matchLowPortLaunchTicket() accepted crossed authority")
			}
		})
	}
}

// TestNewLowPortLaunchTicketValidatesMetadata covers every caller-supplied launch boundary.
func TestNewLowPortLaunchTicketValidatesMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	valid := lowPortLaunchTicketFixture{
		operationID:            "operation-low-ports",
		reference:              helper.TicketReference(strings.Repeat("e", 64)),
		operation:              helper.OperationEnsureLowPorts,
		policyFingerprint:      strings.Repeat("a", 64),
		ownershipFingerprint:   strings.Repeat("b", 64),
		observationFingerprint: strings.Repeat("c", 64),
		expiresAt:              now.Add(time.Minute),
	}
	tests := []struct {
		name   string
		mutate func(*lowPortLaunchTicketFixture)
	}{
		{name: "ensure", mutate: func(*lowPortLaunchTicketFixture) {}},
		{name: "release", mutate: func(value *lowPortLaunchTicketFixture) { value.operation = helper.OperationReleaseLowPorts }},
		{name: "operation ID", mutate: func(value *lowPortLaunchTicketFixture) { value.operationID = "" }},
		{name: "reference", mutate: func(value *lowPortLaunchTicketFixture) { value.reference = "bad" }},
		{name: "operation", mutate: func(value *lowPortLaunchTicketFixture) { value.operation = helper.OperationEnsureTrust }},
		{name: "policy fingerprint", mutate: func(value *lowPortLaunchTicketFixture) { value.policyFingerprint = "bad" }},
		{name: "policy uppercase", mutate: func(value *lowPortLaunchTicketFixture) { value.policyFingerprint = strings.Repeat("A", 64) }},
		{name: "ownership fingerprint", mutate: func(value *lowPortLaunchTicketFixture) { value.ownershipFingerprint = "bad" }},
		{name: "ownership uppercase", mutate: func(value *lowPortLaunchTicketFixture) { value.ownershipFingerprint = strings.Repeat("B", 64) }},
		{name: "observation fingerprint", mutate: func(value *lowPortLaunchTicketFixture) { value.observationFingerprint = "bad" }},
		{name: "observation uppercase", mutate: func(value *lowPortLaunchTicketFixture) { value.observationFingerprint = strings.Repeat("C", 64) }},
		{name: "zero expiry", mutate: func(value *lowPortLaunchTicketFixture) { value.expiresAt = time.Time{} }},
		{name: "local expiry", mutate: func(value *lowPortLaunchTicketFixture) {
			value.expiresAt = value.expiresAt.In(time.FixedZone("local", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			_, err := newLowPortLaunchTicket(candidate)
			wantError := test.name != "ensure" && test.name != "release"
			if (err != nil) != wantError {
				t.Fatalf("NewLowPortLaunchTicket() error = %v, wantError = %t", err, wantError)
			}
		})
	}
}

// TestInvokeLowPortsRejectsExpiredAndCanceledAuthority keeps stale consent away from native transport.
func TestInvokeLowPortsRejectsExpiredAndCanceledAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		expiresAt time.Time
		canceled  bool
	}{
		{name: "expired", expiresAt: now},
		{name: "overlong", expiresAt: now.Add(helper.MaxTicketLifetime + time.Nanosecond)},
		{name: "canceled", expiresAt: now.Add(time.Minute), canceled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := lowPortLaunchTicketWithExpiry(t, test.expiresAt)
			ctx := context.Background()
			if test.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			calls := 0
			transport := transportFunc(func(context.Context, io.Reader, io.Writer) TransportResult {
				calls++
				return TransportResult{}
			})
			if _, err := New(transport, fixedClock{now: now}).InvokeLowPorts(ctx, ticket); err == nil || calls != 0 {
				t.Fatalf("InvokeLowPorts() error/calls = %v/%d", err, calls)
			}
		})
	}
}

// lowPortLaunchTicketFixture holds typed metadata for constructor branch tests.
type lowPortLaunchTicketFixture struct {
	operationID            domain.OperationID
	reference              helper.TicketReference
	operation              helper.Operation
	policyFingerprint      string
	ownershipFingerprint   string
	observationFingerprint string
	expiresAt              time.Time
}

// newLowPortLaunchTicket converts a test fixture through the production validation boundary.
func newLowPortLaunchTicket(value lowPortLaunchTicketFixture) (LowPortLaunchTicket, error) {
	return NewLowPortLaunchTicket(
		value.operationID,
		value.reference,
		value.operation,
		value.policyFingerprint,
		value.ownershipFingerprint,
		value.observationFingerprint,
		value.expiresAt,
	)
}

// validLowPortLaunchTicket creates one short-lived low-port capability.
func validLowPortLaunchTicket(t *testing.T, now time.Time, operation helper.Operation) LowPortLaunchTicket {
	t.Helper()
	value := lowPortLaunchTicketFixture{
		operationID:            "operation-low-ports",
		reference:              helper.TicketReference(strings.Repeat("e", 64)),
		operation:              operation,
		policyFingerprint:      strings.Repeat("a", 64),
		ownershipFingerprint:   strings.Repeat("b", 64),
		observationFingerprint: strings.Repeat("c", 64),
		expiresAt:              now.Add(time.Minute),
	}
	ticket, err := newLowPortLaunchTicket(value)
	if err != nil {
		t.Fatalf("NewLowPortLaunchTicket() error = %v", err)
	}
	return ticket
}

// lowPortLaunchTicketWithExpiry creates valid structured metadata with a selected expiry.
func lowPortLaunchTicketWithExpiry(t *testing.T, expiresAt time.Time) LowPortLaunchTicket {
	t.Helper()
	ticket, err := NewLowPortLaunchTicket(
		"operation-low-ports",
		helper.TicketReference(strings.Repeat("e", 64)),
		helper.OperationEnsureLowPorts,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		strings.Repeat("c", 64),
		expiresAt,
	)
	if err != nil {
		t.Fatalf("NewLowPortLaunchTicket() error = %v", err)
	}
	return ticket
}

// lowPortSuccessResponse creates one protocol-valid response with an independently observed postcondition.
func lowPortSuccessResponse(ticket LowPortLaunchTicket, postFingerprint string) helper.Response {
	postcondition := helper.LowPortPostconditionExact
	if ticket.operation == helper.OperationReleaseLowPorts {
		postcondition = helper.LowPortPostconditionOwnedAbsent
	}
	return helper.Response{
		Version: helper.ProtocolVersion,
		OK:      true,
		Result: &helper.OperationResult{
			Operation: ticket.operation,
			LowPortEvidence: &helper.LowPortMutationEvidence{
				Changed:                true,
				PolicyFingerprint:      ticket.policyFingerprint,
				OwnershipFingerprint:   ticket.ownershipFingerprint,
				ObservationFingerprint: postFingerprint,
				Postcondition:          postcondition,
			},
		},
	}
}

// cloneLowPortOperationResult copies nested evidence so table cases cannot alias each other.
func cloneLowPortOperationResult(source *helper.OperationResult) *helper.OperationResult {
	if source == nil {
		return nil
	}
	clone := *source
	if source.LowPortEvidence != nil {
		evidence := *source.LowPortEvidence
		clone.LowPortEvidence = &evidence
	}
	return &clone
}
