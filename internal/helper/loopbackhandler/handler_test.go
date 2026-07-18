package loopbackhandler

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/loopback"
)

var (
	handlerTestAddress  = netip.MustParseAddr("127.77.0.10")
	handlerTestLoopback = loopback.InterfaceFact{
		Name:           "native-loopback",
		Index:          1,
		Kind:           loopback.InterfaceKindLinuxNative,
		NativeLoopback: true,
	}
)

// fakeAdapter keeps every host observation and conditional effect explicit in handler tests.
type fakeAdapter struct {
	observation         loopback.Observation
	observeErr          error
	ensureChange        loopback.Change
	ensureErr           error
	releaseChange       loopback.Change
	releaseErr          error
	observeCalls        int
	ensureCalls         int
	releaseCalls        int
	mutationAddress     netip.Addr
	mutationFingerprint string
}

// Observe returns the configured precondition without consulting the test host.
func (adapter *fakeAdapter) Observe(context.Context, netip.Addr) (loopback.Observation, error) {
	adapter.observeCalls++
	return adapter.observation, adapter.observeErr
}

// EnsureIfObserved records the exact conditional ensure request and returns its configured result.
func (adapter *fakeAdapter) EnsureIfObserved(_ context.Context, address netip.Addr, fingerprint string) (loopback.Change, error) {
	adapter.ensureCalls++
	adapter.mutationAddress = address
	adapter.mutationFingerprint = fingerprint
	return adapter.ensureChange, adapter.ensureErr
}

// ReleaseIfObserved records the exact conditional release request and returns its configured result.
func (adapter *fakeAdapter) ReleaseIfObserved(_ context.Context, address netip.Addr, fingerprint string) (loopback.Change, error) {
	adapter.releaseCalls++
	adapter.mutationAddress = address
	adapter.mutationFingerprint = fingerprint
	return adapter.releaseChange, adapter.releaseErr
}

// TestHandlerAppliesObservationBoundOperations verifies ensure, idempotent ensure, and release protocol evidence.
func TestHandlerAppliesObservationBoundOperations(t *testing.T) {
	if New() == nil {
		t.Fatal("New() returned nil")
	}
	absent := handlerObservation(loopback.StateAbsent)
	exact := handlerObservation(loopback.StateExact)
	tests := []struct {
		name        string
		operation   helper.Operation
		before      loopback.Observation
		after       loopback.Observation
		changed     bool
		wantState   helper.ObservationState
		wantEnsure  int
		wantRelease int
	}{
		{name: "ensure absent", operation: helper.OperationEnsureLoopbackIdentity, before: absent, after: exact, changed: true, wantState: helper.ObservationOwned, wantEnsure: 1},
		{name: "ensure owned", operation: helper.OperationEnsureLoopbackIdentity, before: exact, after: exact, wantState: helper.ObservationOwned, wantEnsure: 1},
		{name: "release owned", operation: helper.OperationReleaseLoopbackIdentity, before: exact, after: absent, changed: true, wantState: helper.ObservationAbsent, wantRelease: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &fakeAdapter{observation: test.before}
			change := loopback.Change{Attempted: test.changed, Changed: test.changed, Before: test.before, After: test.after}
			adapter.ensureChange = change
			adapter.releaseChange = change
			ticket := handlerTicket(t, test.operation, test.before)

			var (
				evidence helper.MutationEvidence
				err      error
			)
			if test.operation == helper.OperationEnsureLoopbackIdentity {
				evidence, err = newHandler(adapter).EnsureLoopbackIdentity(context.Background(), ticket)
			} else {
				evidence, err = newHandler(adapter).ReleaseLoopbackIdentity(context.Background(), ticket)
			}
			if err != nil {
				t.Fatalf("handler operation error = %v", err)
			}
			if evidence.Changed != test.changed || evidence.Address != handlerTestAddress.String() || evidence.Observation.State != test.wantState {
				t.Fatalf("handler evidence = %#v", evidence)
			}
			wantFingerprint, err := test.after.Fingerprint()
			if err != nil {
				t.Fatalf("Fingerprint(after) error = %v", err)
			}
			if evidence.Observation.Fingerprint != wantFingerprint {
				t.Fatalf("evidence fingerprint = %q, want %q", evidence.Observation.Fingerprint, wantFingerprint)
			}
			if adapter.observeCalls != 1 || adapter.ensureCalls != test.wantEnsure || adapter.releaseCalls != test.wantRelease {
				t.Fatalf("adapter calls = observe %d, ensure %d, release %d", adapter.observeCalls, adapter.ensureCalls, adapter.releaseCalls)
			}
			if adapter.mutationAddress != handlerTestAddress || adapter.mutationFingerprint != ticket.ExpectedObservation.Fingerprint {
				t.Fatalf("conditional mutation = %s, %q", adapter.mutationAddress, adapter.mutationFingerprint)
			}
		})
	}
}

// TestHandlerRejectsInvalidOrChangedPreconditions proves no mutation follows malformed, stale, or contradictory authority.
func TestHandlerRejectsInvalidOrChangedPreconditions(t *testing.T) {
	absent := handlerObservation(loopback.StateAbsent)
	exact := handlerObservation(loopback.StateExact)
	tests := []struct {
		name      string
		operation helper.Operation
		before    loopback.Observation
		mutate    func(*helper.Ticket)
		observes  int
	}{
		{name: "wrong operation", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(ticket *helper.Ticket) { ticket.Operation = helper.OperationReleaseLoopbackIdentity }},
		{name: "invalid fingerprint", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(ticket *helper.Ticket) { ticket.ExpectedObservation.Fingerprint = "bad" }},
		{name: "invalid address", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(ticket *helper.Ticket) { ticket.ApprovedAddress = "192.0.2.1" }},
		{name: "invalid pool", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(ticket *helper.Ticket) { ticket.ApprovedPool = "not-a-prefix" }},
		{name: "address outside pool", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(ticket *helper.Ticket) { ticket.ApprovedPool = "127.78.0.0/24" }},
		{name: "changed fingerprint", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(ticket *helper.Ticket) { ticket.ExpectedObservation.Fingerprint = strings.Repeat("a", 64) }, observes: 1},
		{name: "changed state", operation: helper.OperationEnsureLoopbackIdentity, before: exact, mutate: func(ticket *helper.Ticket) { ticket.ExpectedObservation.State = helper.ObservationAbsent }, observes: 1},
		{name: "release requires owned", operation: helper.OperationReleaseLoopbackIdentity, before: absent, mutate: func(ticket *helper.Ticket) { ticket.ExpectedObservation.State = helper.ObservationAbsent }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &fakeAdapter{observation: test.before}
			ticket := handlerTicket(t, test.operation, test.before)
			test.mutate(&ticket)

			var err error
			if test.operation == helper.OperationReleaseLoopbackIdentity {
				_, err = newHandler(adapter).ReleaseLoopbackIdentity(context.Background(), ticket)
			} else {
				_, err = newHandler(adapter).EnsureLoopbackIdentity(context.Background(), ticket)
			}
			if err == nil {
				t.Fatal("handler rejected precondition error = nil")
			}
			if adapter.observeCalls != test.observes || adapter.ensureCalls != 0 || adapter.releaseCalls != 0 {
				t.Fatalf("adapter calls = observe %d, ensure %d, release %d", adapter.observeCalls, adapter.ensureCalls, adapter.releaseCalls)
			}
		})
	}
}

// TestHandlerPreservesAdapterFailuresAndRejectsUnverifiedPostconditions covers every effect boundary after admission.
func TestHandlerPreservesAdapterFailuresAndRejectsUnverifiedPostconditions(t *testing.T) {
	absent := handlerObservation(loopback.StateAbsent)
	exact := handlerObservation(loopback.StateExact)
	ticket := handlerTicket(t, helper.OperationEnsureLoopbackIdentity, absent)
	releaseTicket := handlerTicket(t, helper.OperationReleaseLoopbackIdentity, exact)
	cause := errors.New("platform unavailable")

	adapter := &fakeAdapter{observation: absent, observeErr: cause}
	if _, err := newHandler(adapter).EnsureLoopbackIdentity(context.Background(), ticket); !errors.Is(err, cause) {
		t.Fatalf("Observe failure error = %v, want cause", err)
	}

	adapter = &fakeAdapter{observation: absent, ensureErr: cause}
	if _, err := newHandler(adapter).EnsureLoopbackIdentity(context.Background(), ticket); !errors.Is(err, cause) {
		t.Fatalf("Ensure failure error = %v, want cause", err)
	}

	adapter = &fakeAdapter{observation: exact, observeErr: cause}
	if _, err := newHandler(adapter).ReleaseLoopbackIdentity(context.Background(), releaseTicket); !errors.Is(err, cause) {
		t.Fatalf("release Observe failure error = %v, want cause", err)
	}

	adapter = &fakeAdapter{observation: exact, releaseErr: cause}
	if _, err := newHandler(adapter).ReleaseLoopbackIdentity(context.Background(), releaseTicket); !errors.Is(err, cause) {
		t.Fatalf("Release failure error = %v, want cause", err)
	}

	malformed := absent
	malformed.Loopback = loopback.InterfaceFact{}
	adapter = &fakeAdapter{observation: malformed}
	if _, err := newHandler(adapter).EnsureLoopbackIdentity(context.Background(), ticket); err == nil {
		t.Fatal("malformed fresh observation error = nil")
	}

	invalidChanges := []loopback.Change{
		{Indeterminate: true, Before: absent, After: exact},
		{Before: absent, After: absent},
		{Before: absent, After: loopback.Observation{Address: netip.MustParseAddr("127.77.0.11"), Loopback: handlerTestLoopback, State: loopback.StateAbsent}},
		{Before: absent, After: loopback.Observation{Address: handlerTestAddress, State: loopback.StateExact}},
	}
	for index, change := range invalidChanges {
		adapter = &fakeAdapter{observation: absent, ensureChange: change}
		if evidence, err := newHandler(adapter).EnsureLoopbackIdentity(context.Background(), ticket); err == nil {
			t.Fatalf("invalid change %d evidence = %#v, want error", index, evidence)
		}
	}
}

// handlerObservation returns one canonical absent or exact native-loopback snapshot.
func handlerObservation(state loopback.State) loopback.Observation {
	observation := loopback.Observation{Address: handlerTestAddress, Loopback: handlerTestLoopback, State: state, Assignments: []loopback.AssignmentFact{}}
	if state == loopback.StateExact {
		observation.Assignments = []loopback.AssignmentFact{{
			Address:        handlerTestAddress,
			PrefixLength:   32,
			InterfaceName:  handlerTestLoopback.Name,
			InterfaceIndex: handlerTestLoopback.Index,
			NativeLoopback: true,
			InterfaceKind:  handlerTestLoopback.Kind,
		}}
	}
	return observation
}

// handlerTicket binds one valid helper operation to the supplied platform observation.
func handlerTicket(t *testing.T, operation helper.Operation, observation loopback.Observation) helper.Ticket {
	t.Helper()
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(ticket observation) error = %v", err)
	}
	state := helper.ObservationAbsent
	if observation.State == loopback.StateExact {
		state = helper.ObservationOwned
	}
	return helper.Ticket{
		Operation:       operation,
		ApprovedPool:    "127.77.0.0/24",
		ApprovedAddress: handlerTestAddress.String(),
		ExpectedObservation: helper.ExpectedObservation{
			State:       state,
			Fingerprint: fingerprint,
		},
	}
}
