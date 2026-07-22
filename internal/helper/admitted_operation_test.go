package helper

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestDispatcherExecutorRunsOnlyAfterReplayConsumption proves injected execution begins after one durable replay admission.
func TestDispatcherExecutorRunsOnlyAfterReplayConsumption(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestTrustTicket(t, now, OperationEnsureTrust)
	reference := testTicketReference()
	replay := newTestReplayGuard()
	handler := &testTrustHandler{
		evidence: TrustMutationEvidence{
			Changed:                true,
			AuthorityFingerprint:   ticket.TrustRoot.Fingerprint,
			Mechanism:              ticket.NetworkPolicy.Mechanisms.Trust,
			ObservationFingerprint: strings.Repeat("d", fingerprintLength),
			Postcondition:          TrustPostconditionExact,
		},
	}
	executorCalls := 0
	dispatcher := NewDispatcherWithAdmittedOperationExecutors(
		newTestTicketRedeemer(reference, ticket),
		newTestClock(now),
		replay,
		AdmittedOperationExecutors{
			Trust: func(ctx context.Context, admitted AdmittedTrustOperation) (OperationResult, error) {
				executorCalls++
				if replay.consumeCount() != 1 {
					t.Fatalf("replay consumption count at executor = %d", replay.consumeCount())
				}
				if admitted.RequesterIdentity() != ticket.RequesterIdentity {
					t.Fatalf("admitted requester = %q", admitted.RequesterIdentity())
				}
				return admitted.ExecuteTrust(ctx, handler)
			},
			Resolver: func(context.Context, AdmittedResolverOperation) (OperationResult, error) {
				t.Fatal("resolver executor unexpectedly called")
				return OperationResult{}, nil
			},
			LowPorts: func(context.Context, AdmittedLowPortOperation) (OperationResult, error) {
				t.Fatal("low-port executor unexpectedly called")
				return OperationResult{}, nil
			},
			Loopback: func(context.Context, AdmittedLoopbackOperation) (OperationResult, error) {
				t.Fatal("loopback executor unexpectedly called")
				return OperationResult{}, nil
			},
		},
	)

	response, err := dispatcher.Dispatch(t.Context(), validTestRequest(reference))
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !response.OK || response.Result == nil || response.Result.TrustEvidence == nil {
		t.Fatalf("Dispatch() response = %#v", response)
	}
	if executorCalls != 1 || handler.calls != 1 || replay.consumeCount() != 1 {
		t.Fatalf("executor calls = %d, handler calls = %d, replay consumes = %d", executorCalls, handler.calls, replay.consumeCount())
	}
}

// TestOneShotDispatcherDetachesAdmissionBeforeTrustExecutor proves no root admission object remains reachable across a trust transition.
func TestOneShotDispatcherDetachesAdmissionBeforeTrustExecutor(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestTrustTicket(t, now, OperationEnsureTrust)
	reference := testTicketReference()
	handler := &testTrustHandler{
		evidence: TrustMutationEvidence{
			Changed:                true,
			AuthorityFingerprint:   ticket.TrustRoot.Fingerprint,
			Mechanism:              ticket.NetworkPolicy.Mechanisms.Trust,
			ObservationFingerprint: strings.Repeat("d", fingerprintLength),
			Postcondition:          TrustPostconditionExact,
		},
	}
	var dispatcher *Dispatcher
	dispatcher = NewOneShotDispatcherWithAdmittedOperationExecutors(
		newTestTicketRedeemer(reference, ticket),
		newTestClock(now),
		newTestReplayGuard(),
		AdmittedOperationExecutors{
			Trust: func(ctx context.Context, admitted AdmittedTrustOperation) (OperationResult, error) {
				if dispatcher.redeemer != nil || dispatcher.replayGuard != nil {
					t.Fatal("one-shot dispatcher retained admission authority inside trust executor")
				}
				return admitted.ExecuteTrust(ctx, handler)
			},
			Resolver: func(context.Context, AdmittedResolverOperation) (OperationResult, error) {
				t.Fatal("resolver executor unexpectedly called")
				return OperationResult{}, nil
			},
			LowPorts: func(context.Context, AdmittedLowPortOperation) (OperationResult, error) {
				t.Fatal("low-port executor unexpectedly called")
				return OperationResult{}, nil
			},
			Loopback: func(context.Context, AdmittedLoopbackOperation) (OperationResult, error) {
				t.Fatal("loopback executor unexpectedly called")
				return OperationResult{}, nil
			},
		},
	)

	if _, err := dispatcher.Dispatch(t.Context(), validTestRequest(reference)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	response, err := dispatcher.Dispatch(t.Context(), validTestRequest(reference))
	if err == nil || response.Error == nil || response.Error.Code != ErrorCodeAuthenticationFailed {
		t.Fatalf("second Dispatch() response = %#v, error = %v", response, err)
	}
}

// TestAdmittedOperationTypesSealMetadata keeps each callback limited to its own family execution method and requester identity.
func TestAdmittedOperationTypesSealMetadata(t *testing.T) {
	for _, test := range []struct {
		name    string
		typeOf  reflect.Type
		methods []string
	}{
		{
			name:   "trust",
			typeOf: reflect.TypeFor[AdmittedTrustOperation](),
			methods: []string{
				"ExecuteTrust",
				"RequesterIdentity",
			},
		},
		{
			name:   "resolver",
			typeOf: reflect.TypeFor[AdmittedResolverOperation](),
			methods: []string{
				"ExecuteResolver",
				"RequesterIdentity",
			},
		},
		{
			name:   "low ports",
			typeOf: reflect.TypeFor[AdmittedLowPortOperation](),
			methods: []string{
				"ExecuteLowPorts",
				"RequesterIdentity",
			},
		},
		{
			name:   "loopback",
			typeOf: reflect.TypeFor[AdmittedLoopbackOperation](),
			methods: []string{
				"ExecuteLoopback",
				"RequesterIdentity",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for index := range test.typeOf.NumField() {
				if test.typeOf.Field(index).IsExported() {
					t.Fatalf("%s exported field = %q", test.typeOf.Name(), test.typeOf.Field(index).Name)
				}
			}
			if test.typeOf.NumMethod() != len(test.methods) {
				t.Fatalf("%s exported method count = %d", test.typeOf.Name(), test.typeOf.NumMethod())
			}
			for _, method := range test.methods {
				if _, found := test.typeOf.MethodByName(method); !found {
					t.Fatalf("%s missing method %q", test.typeOf.Name(), method)
				}
			}
		})
	}
}

// TestAdmittedTrustOperationRejectsWrongTicket prevents a trust callback from applying a non-trust ticket.
func TestAdmittedTrustOperationRejectsWrongTicket(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	admitted := AdmittedTrustOperation{
		ticket: validTestTrustTicket(t, now, OperationEnsureTrust),
	}
	admitted.ticket.Operation = OperationEnsureResolver
	if _, err := admitted.ExecuteTrust(t.Context(), UnavailableTrustHandler{}); err == nil {
		t.Fatal("ExecuteTrust() unexpectedly accepted a resolver ticket")
	}
}

// TestAdmittedOperationExecuteTrustSelectsExactOperation verifies ensure and release each invoke only their matching trust handler once.
func TestAdmittedOperationExecuteTrustSelectsExactOperation(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	for _, operation := range []Operation{
		OperationEnsureTrust,
		OperationReleaseTrust,
	} {
		t.Run(string(operation), func(t *testing.T) {
			ticket := validTestTrustTicket(t, now, operation)
			postcondition := TrustPostconditionExact
			if operation == OperationReleaseTrust {
				postcondition = TrustPostconditionOwnedAbsent
			}
			handler := &testTrustHandler{
				evidence: TrustMutationEvidence{
					Changed:                true,
					AuthorityFingerprint:   ticket.TrustRoot.Fingerprint,
					Mechanism:              ticket.NetworkPolicy.Mechanisms.Trust,
					ObservationFingerprint: strings.Repeat("d", fingerprintLength),
					Postcondition:          postcondition,
				},
			}
			result, err := (AdmittedTrustOperation{ticket: ticket}).ExecuteTrust(t.Context(), handler)
			if err != nil {
				t.Fatalf("ExecuteTrust() error = %v", err)
			}
			if result.Operation != operation || result.TrustEvidence == nil || handler.calls != 1 || handler.operation != operation {
				t.Fatalf("result = %#v, calls = %d, handler operation = %q", result, handler.calls, handler.operation)
			}
		})
	}
}

// TestAdmittedOperationExecuteTrustRejectsInvalidEvidence preserves dispatcher evidence correlation after executor injection.
func TestAdmittedOperationExecuteTrustRejectsInvalidEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	ticket := validTestTrustTicket(t, now, OperationReleaseTrust)
	handler := &testTrustHandler{
		evidence: TrustMutationEvidence{
			Changed:                true,
			AuthorityFingerprint:   ticket.TrustRoot.Fingerprint,
			Mechanism:              ticket.NetworkPolicy.Mechanisms.Trust,
			ObservationFingerprint: strings.Repeat("d", fingerprintLength),
			Postcondition:          TrustPostconditionExact,
		},
	}
	if _, err := (AdmittedTrustOperation{ticket: ticket}).ExecuteTrust(t.Context(), handler); err == nil {
		t.Fatal("ExecuteTrust() unexpectedly accepted invalid release evidence")
	}
	if handler.calls != 1 || handler.operation != OperationReleaseTrust {
		t.Fatalf("handler calls = %d, operation = %q", handler.calls, handler.operation)
	}
}
