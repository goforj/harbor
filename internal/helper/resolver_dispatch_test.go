package helper

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestResolverResponseCodecRoundTrip pins the strict resolver evidence wire shape.
func TestResolverResponseCodecRoundTrip(t *testing.T) {
	evidence := ResolverMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      strings.Repeat("a", fingerprintLength),
		OwnershipFingerprint:   strings.Repeat("c", fingerprintLength),
		ObservationFingerprint: strings.Repeat("b", fingerprintLength),
		Postcondition:          ResolverPostconditionExact,
	}
	response := Response{
		Version: ProtocolVersion,
		OK:      true,
		Result: &OperationResult{
			Operation:        OperationEnsureResolver,
			ResolverEvidence: &evidence,
		},
	}
	var encoded bytes.Buffer
	if err := WriteResponse(&encoded, response); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}
	decoded, err := DecodeResponse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if decoded.Result == nil || decoded.Result.ResolverEvidence == nil ||
		*decoded.Result.ResolverEvidence != evidence {
		t.Fatalf("DecodeResponse() = %#v", decoded)
	}
	ownershipField := `"ownership_fingerprint":"` + evidence.OwnershipFingerprint + `"`
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing ownership", body: strings.Replace(encoded.String(), ownershipField+`,`, "", 1)},
		{name: "unknown ownership sibling", body: strings.Replace(encoded.String(), ownershipField, ownershipField+`,"ownership_alias":"`+evidence.OwnershipFingerprint+`"`, 1)},
		{name: "invalid ownership", body: strings.Replace(encoded.String(), ownershipField, `"ownership_fingerprint":"bad"`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeResponse(strings.NewReader(test.body)); err == nil {
				t.Fatal("DecodeResponse() accepted noncanonical ownership evidence")
			}
		})
	}
}

// TestDispatcherDispatchResolverOperations verifies both resolver paths return only correlated bounded evidence.
func TestDispatcherDispatchResolverOperations(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{OperationEnsureResolver, OperationReleaseResolver} {
		t.Run(string(operation), func(t *testing.T) {
			ticket := validTestResolverTicket(now, operation)
			postcondition := ResolverPostconditionExact
			if operation == OperationReleaseResolver {
				postcondition = ResolverPostconditionOwnedAbsent
			}
			handler := &testResolverHandler{evidence: ResolverMutationEvidence{
				Changed:                true,
				PolicyFingerprint:      ticket.NetworkPolicyFingerprint,
				OwnershipFingerprint:   strings.Repeat("a", fingerprintLength),
				ObservationFingerprint: strings.Repeat("d", fingerprintLength),
				Postcondition:          postcondition,
			}}
			reference := testTicketReference()
			dispatcher := NewDispatcherWithResolver(
				newTestTicketRedeemer(reference, ticket),
				newTestClock(now),
				newTestReplayGuard(),
				UnavailableLoopbackIdentityHandler{},
				handler,
			)

			response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
			if err != nil {
				t.Fatalf("Dispatch() error = %v", err)
			}
			if !response.OK || response.Result == nil || response.Result.Operation != operation ||
				response.Result.ResolverEvidence == nil || *response.Result.ResolverEvidence != handler.evidence ||
				response.Result.Evidence != (MutationEvidence{}) || response.Result.PoolEvidence != nil {
				t.Fatalf("Dispatch() response = %#v", response)
			}
			if handler.calls != 1 || handler.operation != operation {
				t.Fatalf("resolver handler calls/operation = %d/%q", handler.calls, handler.operation)
			}
		})
	}
}

// TestDispatcherResolverEvidenceFailsClosed rejects unavailable handlers and mismatched postconditions.
func TestDispatcherResolverEvidenceFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestResolverTicket(now, OperationEnsureResolver)
	reference := testTicketReference()

	t.Run("unavailable by default", func(t *testing.T) {
		dispatcher := NewDispatcher(
			newTestTicketRedeemer(reference, ticket),
			newTestClock(now),
			newTestReplayGuard(),
			UnavailableLoopbackIdentityHandler{},
		)
		response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
		if !errors.Is(err, ErrMutationUnavailable) || response.Error == nil || response.Error.Code != ErrorCodeMutationUnavailable {
			t.Fatalf("Dispatch() = %#v, %v, want resolver unavailable", response, err)
		}
	})

	t.Run("wrong policy", func(t *testing.T) {
		handler := &testResolverHandler{evidence: ResolverMutationEvidence{
			PolicyFingerprint:      strings.Repeat("f", fingerprintLength),
			OwnershipFingerprint:   strings.Repeat("a", fingerprintLength),
			ObservationFingerprint: strings.Repeat("d", fingerprintLength),
			Postcondition:          ResolverPostconditionExact,
		}}
		dispatcher := NewDispatcherWithResolver(
			newTestTicketRedeemer(reference, ticket),
			newTestClock(now),
			newTestReplayGuard(),
			UnavailableLoopbackIdentityHandler{},
			handler,
		)
		response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
		if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
			t.Fatalf("Dispatch() = %#v, %v, want mutation failure", response, err)
		}
	})

	t.Run("wrong ownership", func(t *testing.T) {
		handler := &testResolverHandler{evidence: ResolverMutationEvidence{
			PolicyFingerprint:      ticket.NetworkPolicyFingerprint,
			OwnershipFingerprint:   strings.Repeat("f", fingerprintLength),
			ObservationFingerprint: strings.Repeat("d", fingerprintLength),
			Postcondition:          ResolverPostconditionExact,
		}}
		dispatcher := NewDispatcherWithResolver(
			newTestTicketRedeemer(reference, ticket),
			newTestClock(now),
			newTestReplayGuard(),
			UnavailableLoopbackIdentityHandler{},
			handler,
		)
		response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
		if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
			t.Fatalf("Dispatch() = %#v, %v, want mutation failure", response, err)
		}
	})

	t.Run("wrong postcondition", func(t *testing.T) {
		handler := &testResolverHandler{evidence: ResolverMutationEvidence{
			PolicyFingerprint:      ticket.NetworkPolicyFingerprint,
			OwnershipFingerprint:   strings.Repeat("a", fingerprintLength),
			ObservationFingerprint: strings.Repeat("d", fingerprintLength),
			Postcondition:          ResolverPostconditionOwnedAbsent,
		}}
		dispatcher := NewDispatcherWithResolver(
			newTestTicketRedeemer(reference, ticket),
			newTestClock(now),
			newTestReplayGuard(),
			UnavailableLoopbackIdentityHandler{},
			handler,
		)
		response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
		if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
			t.Fatalf("Dispatch() = %#v, %v, want mutation failure", response, err)
		}
	})
}

// TestDispatcherReplayFailurePreventsResolverOwnershipTransition proves durable replay admission precedes the handler boundary.
func TestDispatcherReplayFailurePreventsResolverOwnershipTransition(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestResolverTicket(now, OperationEnsureResolver)
	reference := testTicketReference()
	redemption := redemptionForTicket(reference, ticket)
	redemption.Admission.OwnershipState = OwnershipAdmissionSchema1To2
	redeemer := newTestTicketRedeemer(reference, ticket)
	redeemer.redemption = redemption
	handler := &testResolverHandler{}
	dispatcher := NewDispatcherWithResolver(
		redeemer,
		newTestClock(now),
		UnavailableReplayGuard{},
		UnavailableLoopbackIdentityHandler{},
		handler,
	)

	response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
	if !errors.Is(err, ErrReplayProtectionUnavailable) || response.Error == nil ||
		response.Error.Code != ErrorCodeReplayProtectionUnavailable {
		t.Fatalf("Dispatch() = %#v, %v, want replay protection failure", response, err)
	}
	if handler.calls != 0 {
		t.Fatalf("resolver handler calls = %d, want 0 before ownership transition", handler.calls)
	}
}

// TestNewDispatcherWithResolverRequiresResolverHandler keeps the platform authority dependency fail-fast.
func TestNewDispatcherWithResolverRequiresResolverHandler(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewDispatcherWithResolver() accepted a nil resolver handler")
		}
	}()
	NewDispatcherWithResolver(
		newTestTicketRedeemer(testTicketReference(), Ticket{}),
		newTestClock(time.Now()),
		newTestReplayGuard(),
		UnavailableLoopbackIdentityHandler{},
		nil,
	)
}

// testResolverHandler returns configured resolver evidence without touching host state.
type testResolverHandler struct {
	evidence  ResolverMutationEvidence
	err       error
	calls     int
	operation Operation
	admission TicketAdmission
}

// EnsureResolver records the ensure dispatch and returns the configured outcome.
func (handler *testResolverHandler) EnsureResolver(_ context.Context, _ Ticket, admission TicketAdmission) (ResolverMutationEvidence, error) {
	handler.calls++
	handler.operation = OperationEnsureResolver
	handler.admission = admission
	return handler.evidence, handler.err
}

// ReleaseResolver records the release dispatch and returns the configured outcome.
func (handler *testResolverHandler) ReleaseResolver(_ context.Context, _ Ticket, admission TicketAdmission) (ResolverMutationEvidence, error) {
	handler.calls++
	handler.operation = OperationReleaseResolver
	handler.admission = admission
	return handler.evidence, handler.err
}

var _ ResolverHandler = (*testResolverHandler)(nil)
