package helper

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestExpectedLowPortObservationValidate covers the low-port compare-and-swap digest boundary.
func TestExpectedLowPortObservationValidate(t *testing.T) {
	for _, test := range []struct {
		name        string
		fingerprint string
		wantError   bool
	}{
		{name: "canonical", fingerprint: testFingerprint()},
		{name: "empty", fingerprint: "", wantError: true},
		{name: "short", fingerprint: "bad", wantError: true},
		{name: "uppercase", fingerprint: strings.Repeat("A", fingerprintLength), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (ExpectedLowPortObservation{Fingerprint: test.fingerprint}).Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("ExpectedLowPortObservation.Validate() error = %v, wantError = %t", err, test.wantError)
			}
		})
	}
}

// TestTicketValidateLowPortAuthority covers both operations and rejects every cross-domain authority field.
func TestTicketValidateLowPortAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{OperationEnsureLowPorts, OperationReleaseLowPorts} {
		t.Run(string(operation), func(t *testing.T) {
			if err := validTestLowPortTicket(now, operation).Validate(now); err != nil {
				t.Fatalf("Ticket.Validate() valid low-port ticket error = %v", err)
			}
			for _, test := range []struct {
				name   string
				mutate func(*Ticket)
			}{
				{name: "identity ownership", mutate: func(value *Ticket) { value.OwnershipSchemaVersion = identityOwnershipSchemaVersion }},
				{name: "missing policy", mutate: func(value *Ticket) { value.NetworkPolicy = nil }},
				{name: "policy mismatch", mutate: func(value *Ticket) { value.NetworkPolicyFingerprint = strings.Repeat("f", fingerprintLength) }},
				{name: "missing observation", mutate: func(value *Ticket) { value.ExpectedLowPortObservation = nil }},
				{name: "invalid observation", mutate: func(value *Ticket) { value.ExpectedLowPortObservation.Fingerprint = "bad" }},
				{name: "loopback address", mutate: func(value *Ticket) { value.ApprovedAddress = "127.77.0.10" }},
				{name: "loopback observation", mutate: func(value *Ticket) {
					value.ExpectedObservation = ExpectedObservation{State: ObservationAbsent, Fingerprint: testFingerprint()}
				}},
				{name: "pre-assignment", mutate: func(value *Ticket) { value.ExpectedPreAssignment = testExpectedPreAssignment() }},
				{name: "loopback pool", mutate: func(value *Ticket) { value.ExpectedLoopbackPool = &ExpectedLoopbackPool{} }},
				{name: "resolver observation", mutate: func(value *Ticket) {
					value.ExpectedResolverObservation = &ExpectedResolverObservation{Fingerprint: testFingerprint()}
				}},
				{name: "trust root", mutate: func(value *Ticket) { value.TrustRoot = &TrustRoot{} }},
				{name: "trust observation", mutate: func(value *Ticket) {
					value.ExpectedTrustObservation = &ExpectedTrustObservation{Fingerprint: testFingerprint()}
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					candidate := validTestLowPortTicket(now, operation)
					test.mutate(&candidate)
					if err := candidate.Validate(now); err == nil {
						t.Fatal("Ticket.Validate() accepted invalid low-port authority")
					}
				})
			}
		})
	}
}

// TestDispatcherDispatchLowPortOperations verifies only a redeemed policy and ownership target reach the handler.
func TestDispatcherDispatchLowPortOperations(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{OperationEnsureLowPorts, OperationReleaseLowPorts} {
		t.Run(string(operation), func(t *testing.T) {
			ticket := validTestLowPortTicket(now, operation)
			postcondition := LowPortPostconditionExact
			if operation == OperationReleaseLowPorts {
				postcondition = LowPortPostconditionOwnedAbsent
			}
			reference := testTicketReference()
			admission := redemptionForTicket(reference, ticket).Admission
			handler := &testLowPortHandler{evidence: LowPortMutationEvidence{
				Changed:                true,
				PolicyFingerprint:      ticket.NetworkPolicyFingerprint,
				OwnershipFingerprint:   admission.TargetOwnershipFingerprint,
				ObservationFingerprint: strings.Repeat("d", fingerprintLength),
				Postcondition:          postcondition,
			}}
			dispatcher := newLowPortDispatcher(reference, ticket, handler)
			response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
			if err != nil {
				t.Fatalf("Dispatch() error = %v", err)
			}
			if !response.OK || response.Result == nil || response.Result.Operation != operation || response.Result.LowPortEvidence == nil || *response.Result.LowPortEvidence != handler.evidence || response.Result.Evidence != (MutationEvidence{}) || response.Result.PoolEvidence != nil || response.Result.ResolverEvidence != nil || response.Result.TrustEvidence != nil {
				t.Fatalf("Dispatch() response = %#v", response)
			}
			if handler.calls != 1 || handler.operation != operation {
				t.Fatalf("low-port handler calls/operation = %d/%q", handler.calls, handler.operation)
			}
		})
	}
}

// TestDispatcherLowPortEvidenceFailsClosed rejects unavailable handlers and every uncorrelated evidence branch.
func TestDispatcherLowPortEvidenceFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestLowPortTicket(now, OperationEnsureLowPorts)
	reference := testTicketReference()
	newDispatcher := func(handler LowPortHandler) *Dispatcher {
		return newLowPortDispatcher(reference, ticket, handler)
	}
	response, err := newDispatcher(UnavailableLowPortHandler{}).Dispatch(context.Background(), validTestRequest(reference))
	if !errors.Is(err, ErrMutationUnavailable) || response.Error == nil || response.Error.Code != ErrorCodeMutationUnavailable {
		t.Fatalf("unavailable low-port Dispatch() = %#v, %v", response, err)
	}
	response, err = newDispatcher(&testLowPortHandler{err: errors.New("native mutation failed")}).Dispatch(context.Background(), validTestRequest(reference))
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
		t.Fatalf("failed low-port Dispatch() = %#v, %v", response, err)
	}

	admission := redemptionForTicket(reference, ticket).Admission
	valid := LowPortMutationEvidence{PolicyFingerprint: ticket.NetworkPolicyFingerprint, OwnershipFingerprint: admission.TargetOwnershipFingerprint, ObservationFingerprint: testFingerprint(), Postcondition: LowPortPostconditionExact}
	for _, test := range []struct {
		name   string
		mutate func(*LowPortMutationEvidence)
	}{
		{name: "policy", mutate: func(value *LowPortMutationEvidence) { value.PolicyFingerprint = strings.Repeat("f", fingerprintLength) }},
		{name: "ownership", mutate: func(value *LowPortMutationEvidence) {
			value.OwnershipFingerprint = strings.Repeat("f", fingerprintLength)
		}},
		{name: "observation", mutate: func(value *LowPortMutationEvidence) { value.ObservationFingerprint = "bad" }},
		{name: "postcondition", mutate: func(value *LowPortMutationEvidence) { value.Postcondition = LowPortPostconditionOwnedAbsent }},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := valid
			test.mutate(&evidence)
			response, err := newDispatcher(&testLowPortHandler{evidence: evidence}).Dispatch(context.Background(), validTestRequest(reference))
			if err == nil || response.Error == nil || response.Error.Code != ErrorCodeMutationFailed {
				t.Fatalf("Dispatch() = %#v, %v, want mutation failure", response, err)
			}
		})
	}
}

// TestLowPortMutationEvidenceValidateShape covers both operation postconditions and every standalone failure branch.
func TestLowPortMutationEvidenceValidateShape(t *testing.T) {
	valid := LowPortMutationEvidence{PolicyFingerprint: testFingerprint(), OwnershipFingerprint: strings.Repeat("b", fingerprintLength), ObservationFingerprint: strings.Repeat("c", fingerprintLength), Postcondition: LowPortPostconditionExact}
	for _, test := range []struct {
		name      string
		operation Operation
		mutate    func(*LowPortMutationEvidence)
		wantError bool
	}{
		{name: "ensure exact", operation: OperationEnsureLowPorts},
		{name: "ensure absent", operation: OperationEnsureLowPorts, mutate: func(value *LowPortMutationEvidence) { value.Postcondition = LowPortPostconditionOwnedAbsent }, wantError: true},
		{name: "release absent", operation: OperationReleaseLowPorts, mutate: func(value *LowPortMutationEvidence) { value.Postcondition = LowPortPostconditionOwnedAbsent }},
		{name: "release exact", operation: OperationReleaseLowPorts, wantError: true},
		{name: "invalid fingerprint", operation: OperationEnsureLowPorts, mutate: func(value *LowPortMutationEvidence) { value.ObservationFingerprint = "bad" }, wantError: true},
		{name: "unsupported operation", operation: OperationEnsureTrust, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := valid
			if test.mutate != nil {
				test.mutate(&evidence)
			}
			err := evidence.validateShape(test.operation)
			if (err != nil) != test.wantError {
				t.Fatalf("LowPortMutationEvidence.validateShape() error = %v, wantError = %t", err, test.wantError)
			}
		})
	}
}

// TestLowPortAdmissionRejectsOwnershipTransition proves only resolver ensure may perform the guarded schema transition.
func TestLowPortAdmissionRejectsOwnershipTransition(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestLowPortTicket(now, OperationEnsureLowPorts)
	reference := testTicketReference()
	redeemer := newTestTicketRedeemer(reference, ticket)
	redemption := redemptionForTicket(reference, ticket)
	redemption.Admission.OwnershipState = OwnershipAdmissionSchema1To2
	redeemer.redemption = redemption
	handler := &testLowPortHandler{}
	dispatcher := NewDispatcherWithResolverTrustAndLowPorts(redeemer, newTestClock(now), newTestReplayGuard(), UnavailableLoopbackIdentityHandler{}, UnavailableResolverHandler{}, UnavailableTrustHandler{}, handler)
	response, err := dispatcher.Dispatch(context.Background(), validTestRequest(reference))
	if !errors.Is(err, ErrTicketRedemptionFailed) || response.Error == nil || response.Error.Code != ErrorCodeAuthenticationFailed {
		t.Fatalf("Dispatch() = %#v, %v, want ticket redemption failure", response, err)
	}
	if handler.calls != 0 {
		t.Fatalf("low-port handler calls = %d, want 0", handler.calls)
	}
}

// TestLowPortResponseCodecRoundTrip pins strict low-port evidence fields and postconditions.
func TestLowPortResponseCodecRoundTrip(t *testing.T) {
	evidence := LowPortMutationEvidence{Changed: true, PolicyFingerprint: testFingerprint(), OwnershipFingerprint: strings.Repeat("b", fingerprintLength), ObservationFingerprint: strings.Repeat("c", fingerprintLength), Postcondition: LowPortPostconditionExact}
	response := Response{Version: ProtocolVersion, OK: true, Result: &OperationResult{Operation: OperationEnsureLowPorts, LowPortEvidence: &evidence}}
	var encoded bytes.Buffer
	if err := WriteResponse(&encoded, response); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}
	decoded, err := DecodeResponse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if decoded.Result == nil || decoded.Result.LowPortEvidence == nil || *decoded.Result.LowPortEvidence != evidence {
		t.Fatalf("DecodeResponse() = %#v", decoded)
	}
	for _, body := range []string{
		strings.Replace(encoded.String(), `"postcondition":"exact"`, `"postcondition":"owned_absent"`, 1),
		strings.Replace(encoded.String(), `"low_port_evidence":`, `"low_port_evidence":null,"low_port_evidence":`, 1),
		strings.Replace(encoded.String(), `"low_port_evidence":`, `"low_ports_evidence":`, 1),
		strings.Replace(encoded.String(), `"observation_fingerprint":`, `"observation":`, 1),
	} {
		if _, err := DecodeResponse(strings.NewReader(body)); err == nil {
			t.Fatal("DecodeResponse() accepted invalid low-port evidence")
		}
	}
}

// TestNewDispatcherWithResolverTrustAndLowPortsRequiresEveryDependency keeps the authority graph fail-fast.
func TestNewDispatcherWithResolverTrustAndLowPortsRequiresEveryDependency(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	reference := testTicketReference()
	ticket := validTestLowPortTicket(now, OperationEnsureLowPorts)
	for _, dependency := range []string{"redeemer", "clock", "replay guard", "loopback", "resolver", "trust", "low ports"} {
		t.Run(dependency, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewDispatcherWithResolverTrustAndLowPorts() did not panic")
				}
			}()
			redeemer := TicketRedeemer(newTestTicketRedeemer(reference, ticket))
			clock := Clock(newTestClock(now))
			replayGuard := ReplayGuard(newTestReplayGuard())
			loopback := LoopbackIdentityHandler(UnavailableLoopbackIdentityHandler{})
			resolver := ResolverHandler(UnavailableResolverHandler{})
			trust := TrustHandler(UnavailableTrustHandler{})
			lowPorts := LowPortHandler(UnavailableLowPortHandler{})
			switch dependency {
			case "redeemer":
				redeemer = nil
			case "clock":
				clock = nil
			case "replay guard":
				replayGuard = nil
			case "loopback":
				loopback = nil
			case "resolver":
				resolver = nil
			case "trust":
				trust = nil
			case "low ports":
				lowPorts = nil
			}
			NewDispatcherWithResolverTrustAndLowPorts(redeemer, clock, replayGuard, loopback, resolver, trust, lowPorts)
		})
	}
}

// testLowPortHandler returns configured evidence without native mutation.
type testLowPortHandler struct {
	evidence  LowPortMutationEvidence
	err       error
	calls     int
	operation Operation
}

// EnsureLowPorts records ensure dispatch and returns the configured outcome.
func (handler *testLowPortHandler) EnsureLowPorts(context.Context, Ticket, TicketAdmission) (LowPortMutationEvidence, error) {
	handler.calls++
	handler.operation = OperationEnsureLowPorts
	return handler.evidence, handler.err
}

// ReleaseLowPorts records release dispatch and returns the configured outcome.
func (handler *testLowPortHandler) ReleaseLowPorts(context.Context, Ticket, TicketAdmission) (LowPortMutationEvidence, error) {
	handler.calls++
	handler.operation = OperationReleaseLowPorts
	return handler.evidence, handler.err
}

var _ LowPortHandler = (*testLowPortHandler)(nil)

// newLowPortDispatcher creates a dispatcher with all unrelated mutation domains unavailable.
func newLowPortDispatcher(reference TicketReference, ticket Ticket, handler LowPortHandler) *Dispatcher {
	return NewDispatcherWithResolverTrustAndLowPorts(newTestTicketRedeemer(reference, ticket), newTestClock(ticket.ExpiresAt.Add(-time.Minute)), newTestReplayGuard(), UnavailableLoopbackIdentityHandler{}, UnavailableResolverHandler{}, UnavailableTrustHandler{}, handler)
}

// validTestLowPortTicket returns one policy-bound ticket with no handler-selected service inputs.
func validTestLowPortTicket(now time.Time, operation Operation) Ticket {
	policy := testResolverPolicy()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		panic(err)
	}
	return Ticket{Version: ProtocolVersion, Operation: operation, InstallationID: "harbor-test-installation", RequesterIdentity: "uid-1000", OwnershipGeneration: 7, OwnershipSchemaVersion: networkPolicyOwnershipSchemaVersion, NetworkPolicyFingerprint: fingerprint, NetworkPolicy: &policy, ApprovedPool: "127.77.0.0/24", ExpectedLowPortObservation: &ExpectedLowPortObservation{Fingerprint: testFingerprint()}, Nonce: strings.Repeat("n", minimumNonceLength), ExpiresAt: now.Add(time.Minute)}
}

// TestResolverTicketRejectsLowPortAuthority keeps resolver tickets unable to carry a second mutation domain.
func TestResolverTicketRejectsLowPortAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestResolverTicket(now, OperationEnsureResolver)
	ticket.ExpectedLowPortObservation = &ExpectedLowPortObservation{Fingerprint: testFingerprint()}
	if err := ticket.Validate(now); err == nil {
		t.Fatal("Ticket.Validate() accepted low-port authority on a resolver ticket")
	}
}
