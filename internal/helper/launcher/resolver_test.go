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

// TestInvokeResolverWritesCanonicalRequestAndCorrelatesPolicy proves native consent returns only the approved result.
func TestInvokeResolverWritesCanonicalRequestAndCorrelatesPolicy(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	issued := validResolverLaunchTicket(t, now, helper.OperationEnsureResolver)
	wantResponse := resolverSuccessResponse(issued.operation, issued.policyFingerprint)
	calls := 0
	transport := transportFunc(func(_ context.Context, request io.Reader, response io.Writer) TransportResult {
		calls++
		decoded, err := helper.DecodeRequest(request)
		if err != nil {
			t.Fatalf("DecodeRequest() error = %v", err)
		}
		if decoded.Version != helper.ProtocolVersion || decoded.TicketReference != issued.reference {
			t.Fatalf("helper request = %#v", decoded)
		}
		if err := helper.WriteResponse(response, wantResponse); err != nil {
			t.Fatalf("WriteResponse() error = %v", err)
		}
		return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
	})

	outcome, err := New(transport, fixedClock{now: now}).InvokeResolver(t.Context(), issued)
	if err != nil {
		t.Fatalf("InvokeResolver() error = %v", err)
	}
	if calls != 1 || outcome.State != Succeeded || outcome.Response.Result == nil ||
		outcome.Response.Result.ResolverEvidence == nil ||
		outcome.Response.Result.ResolverEvidence.PolicyFingerprint != issued.policyFingerprint {
		t.Fatalf("InvokeResolver() calls/outcome = %d/%#v", calls, outcome)
	}
}

// TestInvokeResolverRejectsUncorrelatedSuccess prevents another resolver policy from satisfying consent.
func TestInvokeResolverRejectsUncorrelatedSuccess(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	issued := validResolverLaunchTicket(t, now, helper.OperationReleaseResolver)
	transport := transportFunc(func(_ context.Context, _ io.Reader, response io.Writer) TransportResult {
		if err := helper.WriteResponse(response, resolverSuccessResponse(issued.operation, strings.Repeat("f", 64))); err != nil {
			t.Fatalf("WriteResponse() error = %v", err)
		}
		return TransportResult{State: TransportCompleted, ExitCode: ExitCodeSucceeded}
	})

	outcome, err := New(transport, fixedClock{now: now}).InvokeResolver(t.Context(), issued)
	if err != nil {
		t.Fatalf("InvokeResolver() error = %v", err)
	}
	if outcome.State != Indeterminate || outcome.Exit == nil {
		t.Fatalf("InvokeResolver() outcome = %#v, want indeterminate", outcome)
	}
}

// TestNewResolverLaunchTicketValidatesMetadata covers the operation, digest, identity, and lifetime shape.
func TestNewResolverLaunchTicketValidatesMetadata(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	operationID := domain.OperationID("operation-resolver")
	reference := helper.TicketReference(strings.Repeat("e", 64))
	fingerprint := strings.Repeat("a", 64)
	tests := []struct {
		name        string
		operationID domain.OperationID
		reference   helper.TicketReference
		operation   helper.Operation
		fingerprint string
		expiresAt   time.Time
		wantError   bool
	}{
		{name: "ensure", operationID: operationID, reference: reference, operation: helper.OperationEnsureResolver, fingerprint: fingerprint, expiresAt: now.Add(time.Minute)},
		{name: "release", operationID: operationID, reference: reference, operation: helper.OperationReleaseResolver, fingerprint: fingerprint, expiresAt: now.Add(time.Minute)},
		{name: "operation ID", reference: reference, operation: helper.OperationEnsureResolver, fingerprint: fingerprint, expiresAt: now.Add(time.Minute), wantError: true},
		{name: "reference", operationID: operationID, reference: "bad", operation: helper.OperationEnsureResolver, fingerprint: fingerprint, expiresAt: now.Add(time.Minute), wantError: true},
		{name: "operation", operationID: operationID, reference: reference, operation: helper.OperationEnsureLoopbackPool, fingerprint: fingerprint, expiresAt: now.Add(time.Minute), wantError: true},
		{name: "fingerprint", operationID: operationID, reference: reference, operation: helper.OperationEnsureResolver, fingerprint: "bad", expiresAt: now.Add(time.Minute), wantError: true},
		{name: "uppercase fingerprint", operationID: operationID, reference: reference, operation: helper.OperationEnsureResolver, fingerprint: strings.Repeat("A", 64), expiresAt: now.Add(time.Minute), wantError: true},
		{name: "expiry", operationID: operationID, reference: reference, operation: helper.OperationEnsureResolver, fingerprint: fingerprint, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewResolverLaunchTicket(
				test.operationID,
				test.reference,
				test.operation,
				test.fingerprint,
				test.expiresAt,
			)
			if (err != nil) != test.wantError {
				t.Fatalf("NewResolverLaunchTicket() error = %v, wantError = %t", err, test.wantError)
			}
		})
	}
}

// validResolverLaunchTicket creates one short-lived resolver capability for launcher tests.
func validResolverLaunchTicket(t *testing.T, now time.Time, operation helper.Operation) ResolverLaunchTicket {
	t.Helper()
	ticket, err := NewResolverLaunchTicket(
		"operation-resolver",
		helper.TicketReference(strings.Repeat("e", 64)),
		operation,
		strings.Repeat("a", 64),
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("NewResolverLaunchTicket() error = %v", err)
	}
	return ticket
}

// resolverSuccessResponse returns one valid resolver postcondition for launcher correlation.
func resolverSuccessResponse(operation helper.Operation, policyFingerprint string) helper.Response {
	postcondition := helper.ResolverPostconditionExact
	if operation == helper.OperationReleaseResolver {
		postcondition = helper.ResolverPostconditionOwnedAbsent
	}
	return helper.Response{
		Version: helper.ProtocolVersion,
		OK:      true,
		Result: &helper.OperationResult{
			Operation: operation,
			ResolverEvidence: &helper.ResolverMutationEvidence{
				Changed:                true,
				PolicyFingerprint:      policyFingerprint,
				ObservationFingerprint: strings.Repeat("b", 64),
				Postcondition:          postcondition,
			},
		},
	}
}
