package loopbackhandler

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/platform/hostconflict"
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
	events              *[]string
}

// Observe returns the configured precondition without consulting the test host.
func (adapter *fakeAdapter) Observe(context.Context, netip.Addr) (loopback.Observation, error) {
	adapter.observeCalls++
	adapter.record("loopback.observe")
	return adapter.observation, adapter.observeErr
}

// EnsureIfObserved records the exact conditional ensure request and returns its configured result.
func (adapter *fakeAdapter) EnsureIfObserved(_ context.Context, address netip.Addr, fingerprint string) (loopback.Change, error) {
	adapter.ensureCalls++
	adapter.record("loopback.ensure")
	adapter.mutationAddress = address
	adapter.mutationFingerprint = fingerprint
	return adapter.ensureChange, adapter.ensureErr
}

// ReleaseIfObserved records the exact conditional release request and returns its configured result.
func (adapter *fakeAdapter) ReleaseIfObserved(_ context.Context, address netip.Addr, fingerprint string) (loopback.Change, error) {
	adapter.releaseCalls++
	adapter.record("loopback.release")
	adapter.mutationAddress = address
	adapter.mutationFingerprint = fingerprint
	return adapter.releaseChange, adapter.releaseErr
}

// record appends one effect boundary when the test requests ordering evidence.
func (adapter *fakeAdapter) record(event string) {
	if adapter.events != nil {
		*adapter.events = append(*adapter.events, event)
	}
}

// fakePreAssignmentObserver records the exact request and supplies deterministic native facts.
type fakePreAssignmentObserver struct {
	observation       hostconflict.Observation
	err               error
	calls             int
	request           hostconflict.Request
	requesterIdentity string
	events            *[]string
}

// observe records the pre-assignment boundary before returning its configured fact set.
func (observer *fakePreAssignmentObserver) observe(_ context.Context, request hostconflict.Request, requesterIdentity string) (hostconflict.Observation, error) {
	observer.calls++
	observer.request = request
	observer.requesterIdentity = requesterIdentity
	if observer.events != nil {
		*observer.events = append(*observer.events, "preassignment.observe")
	}
	return observer.observation, observer.err
}

// poolEnsureCall records one address-bound compare-and-swap request.
type poolEnsureCall struct {
	address     netip.Addr
	fingerprint string
}

// poolFakeAdapter keeps per-address state so pool failures and repair can be exercised without host effects.
type poolFakeAdapter struct {
	observations       map[netip.Addr]loopback.Observation
	observeErrors      map[netip.Addr]error
	ensureErrors       map[netip.Addr]error
	ensureEffectsOnErr map[netip.Addr]bool
	observeCalls       []netip.Addr
	ensureCalls        []poolEnsureCall
	releaseCalls       int
	events             *[]string
}

// Observe returns the configured address-specific assignment state.
func (adapter *poolFakeAdapter) Observe(_ context.Context, address netip.Addr) (loopback.Observation, error) {
	adapter.observeCalls = append(adapter.observeCalls, address)
	adapter.record("loopback.observe:" + address.String())
	if err := adapter.observeErrors[address]; err != nil {
		return loopback.Observation{}, err
	}
	observation, found := adapter.observations[address]
	if !found {
		return loopback.Observation{}, errors.New("pool fake observation is missing")
	}
	return observation, nil
}

// EnsureIfObserved applies one exact fake assignment while preserving injected ambiguous-error outcomes.
func (adapter *poolFakeAdapter) EnsureIfObserved(_ context.Context, address netip.Addr, fingerprint string) (loopback.Change, error) {
	adapter.ensureCalls = append(adapter.ensureCalls, poolEnsureCall{address: address, fingerprint: fingerprint})
	adapter.record("loopback.ensure:" + address.String())
	before, found := adapter.observations[address]
	if !found {
		return loopback.Change{}, errors.New("pool fake ensure observation is missing")
	}
	after := before
	if before.State == loopback.StateAbsent {
		after = handlerObservationAt(address, loopback.StateExact)
	}
	if err := adapter.ensureErrors[address]; err != nil {
		if adapter.ensureEffectsOnErr[address] {
			adapter.observations[address] = after
			return loopback.Change{
				Attempted: true,
				Changed:   before.State != after.State,
				Before:    before,
				After:     after,
			}, err
		}
		return loopback.Change{Before: before, After: before}, err
	}
	adapter.observations[address] = after
	return loopback.Change{
		Attempted: before.State == loopback.StateAbsent,
		Changed:   before.State != after.State,
		Before:    before,
		After:     after,
	}, nil
}

// ReleaseIfObserved records an unexpected compensating effect and fails closed.
func (adapter *poolFakeAdapter) ReleaseIfObserved(_ context.Context, address netip.Addr, _ string) (loopback.Change, error) {
	adapter.releaseCalls++
	adapter.record("loopback.release:" + address.String())
	return loopback.Change{}, errors.New("pool ensure must not compensate with release")
}

// record appends one address-specific pool effect boundary.
func (adapter *poolFakeAdapter) record(event string) {
	if adapter.events != nil {
		*adapter.events = append(*adapter.events, event)
	}
}

// poolFakePreAssignmentObserver supplies candidate-specific native safety observations.
type poolFakePreAssignmentObserver struct {
	observations        map[netip.Addr]hostconflict.Observation
	errors              map[netip.Addr]error
	requests            []hostconflict.Request
	requesterIdentities []string
	events              *[]string
}

// observe records the exact pool candidate checked immediately before its conditional ensure.
func (observer *poolFakePreAssignmentObserver) observe(_ context.Context, request hostconflict.Request, requesterIdentity string) (hostconflict.Observation, error) {
	address := request.Candidate()
	observer.requests = append(observer.requests, request)
	observer.requesterIdentities = append(observer.requesterIdentities, requesterIdentity)
	if observer.events != nil {
		*observer.events = append(*observer.events, "preassignment.observe:"+address.String())
	}
	if err := observer.errors[address]; err != nil {
		return hostconflict.Observation{}, err
	}
	observation, found := observer.observations[address]
	if !found {
		return hostconflict.Observation{}, errors.New("pool fake pre-assignment observation is missing")
	}
	return observation, nil
}

// TestNewPreAssignmentRequestConvertsCanonicalRequirements proves the signed helper vocabulary maps exactly to native observation requests.
func TestNewPreAssignmentRequestConvertsCanonicalRequirements(t *testing.T) {
	tests := []struct {
		name         string
		requirements []helper.SocketRequirement
		want         []hostconflict.SocketRequirement
	}{
		{name: "route only", requirements: []helper.SocketRequirement{}, want: []hostconflict.SocketRequirement{}},
		{
			name: "TCP and UDP",
			requirements: []helper.SocketRequirement{
				{Transport: helper.SocketTransportTCP4, Port: 80},
				{Transport: helper.SocketTransportTCP4, Port: 443},
				{Transport: helper.SocketTransportUDP4, Port: 53},
			},
			want: []hostconflict.SocketRequirement{
				{Transport: hostconflict.TransportTCP4, Port: 80},
				{Transport: hostconflict.TransportTCP4, Port: 443},
				{Transport: hostconflict.TransportUDP4, Port: 53},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := helper.ExpectedPreAssignment{
				Fingerprint:  strings.Repeat("0", 64),
				Requirements: test.requirements,
			}
			request, err := newPreAssignmentRequest(handlerTestAddress, expected)
			if err != nil {
				t.Fatalf("newPreAssignmentRequest() error = %v", err)
			}
			if request.Purpose() != hostconflict.PurposePreAssignment || request.Candidate() != handlerTestAddress || !slices.Equal(request.Requirements(), test.want) {
				t.Fatalf("newPreAssignmentRequest() = %q %s %#v", request.Purpose(), request.Candidate(), request.Requirements())
			}
		})
	}
}

// TestNewPreAssignmentRequestRejectsInvalidAuthority covers every malformed helper conversion boundary.
func TestNewPreAssignmentRequestRejectsInvalidAuthority(t *testing.T) {
	valid := helper.ExpectedPreAssignment{
		Fingerprint: strings.Repeat("0", 64),
		Requirements: []helper.SocketRequirement{
			{Transport: helper.SocketTransportTCP4, Port: 443},
		},
	}
	tests := []struct {
		name     string
		address  netip.Addr
		expected helper.ExpectedPreAssignment
	}{
		{name: "foreign candidate", address: netip.MustParseAddr("192.0.2.1"), expected: valid},
		{name: "invalid fingerprint", address: handlerTestAddress, expected: helper.ExpectedPreAssignment{Fingerprint: "bad", Requirements: []helper.SocketRequirement{}}},
		{name: "implicit requirements", address: handlerTestAddress, expected: helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("0", 64)}},
		{name: "invalid transport", address: handlerTestAddress, expected: helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("0", 64), Requirements: []helper.SocketRequirement{{Transport: helper.SocketTransport("sctp4"), Port: 443}}}},
		{name: "zero port", address: handlerTestAddress, expected: helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("0", 64), Requirements: []helper.SocketRequirement{{Transport: helper.SocketTransportTCP4}}}},
		{name: "unsorted requirements", address: handlerTestAddress, expected: helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("0", 64), Requirements: []helper.SocketRequirement{{Transport: helper.SocketTransportUDP4, Port: 53}, {Transport: helper.SocketTransportTCP4, Port: 443}}}},
		{name: "duplicate requirements", address: handlerTestAddress, expected: helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("0", 64), Requirements: []helper.SocketRequirement{{Transport: helper.SocketTransportTCP4, Port: 443}, {Transport: helper.SocketTransportTCP4, Port: 443}}}},
	}
	overLimit := valid
	overLimit.Requirements = make([]helper.SocketRequirement, helper.MaximumSocketRequirements+1)
	for index := range overLimit.Requirements {
		overLimit.Requirements[index] = helper.SocketRequirement{Transport: helper.SocketTransportTCP4, Port: uint16(index + 1)}
	}
	tests = append(tests, struct {
		name     string
		address  netip.Addr
		expected helper.ExpectedPreAssignment
	}{name: "requirement limit", address: handlerTestAddress, expected: overLimit})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if request, err := newPreAssignmentRequest(test.address, test.expected); err == nil {
				t.Fatalf("newPreAssignmentRequest() = %#v, want error", request)
			}
		})
	}
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
		wantPre     int
		wantEvents  []string
	}{
		{name: "ensure absent", operation: helper.OperationEnsureLoopbackIdentity, before: absent, after: exact, changed: true, wantState: helper.ObservationOwned, wantEnsure: 1, wantPre: 1, wantEvents: []string{"loopback.observe", "preassignment.observe", "loopback.ensure"}},
		{name: "ensure owned", operation: helper.OperationEnsureLoopbackIdentity, before: exact, after: exact, wantState: helper.ObservationOwned, wantEnsure: 1, wantEvents: []string{"loopback.observe", "loopback.ensure"}},
		{name: "release owned", operation: helper.OperationReleaseLoopbackIdentity, before: exact, after: absent, changed: true, wantState: helper.ObservationAbsent, wantRelease: 1, wantEvents: []string{"loopback.observe", "loopback.release"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			adapter := &fakeAdapter{observation: test.before, events: &events}
			change := loopback.Change{Attempted: test.changed, Changed: test.changed, Before: test.before, After: test.after}
			adapter.ensureChange = change
			adapter.releaseChange = change
			ticket := handlerTicket(t, test.operation, test.before)
			observer := handlerPreAssignmentObserver(t, ticket)
			observer.events = &events
			handler := newHandler(adapter, observer.observe)

			var (
				evidence helper.MutationEvidence
				err      error
			)
			if test.operation == helper.OperationEnsureLoopbackIdentity {
				evidence, err = handler.EnsureLoopbackIdentity(context.Background(), ticket)
			} else {
				evidence, err = handler.ReleaseLoopbackIdentity(context.Background(), ticket)
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
			if adapter.observeCalls != 1 || adapter.ensureCalls != test.wantEnsure || adapter.releaseCalls != test.wantRelease || observer.calls != test.wantPre {
				t.Fatalf("adapter calls = loopback %d, pre-assignment %d, ensure %d, release %d", adapter.observeCalls, observer.calls, adapter.ensureCalls, adapter.releaseCalls)
			}
			if adapter.mutationAddress != handlerTestAddress || adapter.mutationFingerprint != ticket.ExpectedObservation.Fingerprint {
				t.Fatalf("conditional mutation = %s, %q", adapter.mutationAddress, adapter.mutationFingerprint)
			}
			if !slices.Equal(events, test.wantEvents) {
				t.Fatalf("operation events = %#v, want %#v", events, test.wantEvents)
			}
			if test.wantPre == 1 {
				wantRequirements := []hostconflict.SocketRequirement{
					{Transport: hostconflict.TransportTCP4, Port: 443},
					{Transport: hostconflict.TransportUDP4, Port: 53},
				}
				if observer.request.Candidate() != handlerTestAddress || !slices.Equal(observer.request.Requirements(), wantRequirements) || observer.requesterIdentity != "1000" {
					t.Fatalf("pre-assignment request = %s %#v for %q", observer.request.Candidate(), observer.request.Requirements(), observer.requesterIdentity)
				}
			}
		})
	}
}

// TestHandlerEnsuresLoopbackPoolInCanonicalTwoPhaseOrder proves all assignment preconditions precede every ordered conditional ensure.
func TestHandlerEnsuresLoopbackPoolInCanonicalTwoPhaseOrder(t *testing.T) {
	addresses := handlerPoolAddresses()
	observations := handlerPoolObservations(func(index int) loopback.State {
		if index%2 == 0 {
			return loopback.StateAbsent
		}
		return loopback.StateExact
	})
	ticket := handlerPoolTicket(t, observations)
	events := []string{}
	adapter := &poolFakeAdapter{observations: observations, events: &events}
	observer := handlerPoolPreAssignmentObserver(t, ticket)
	observer.events = &events

	evidence, err := newHandler(adapter, observer.observe).EnsureLoopbackPool(context.Background(), ticket)
	if err != nil {
		t.Fatalf("EnsureLoopbackPool() error = %v", err)
	}
	if evidence.Pool != ticket.ApprovedPool || len(evidence.Identities) != len(addresses) {
		t.Fatalf("pool evidence = %#v", evidence)
	}
	if !slices.Equal(adapter.observeCalls, addresses) {
		t.Fatalf("initial observation order = %#v, want %#v", adapter.observeCalls, addresses)
	}
	if len(adapter.ensureCalls) != len(addresses) {
		t.Fatalf("ensure calls = %d, want %d", len(adapter.ensureCalls), len(addresses))
	}

	wantEvents := make([]string, 0, len(addresses)*3)
	for _, address := range addresses {
		wantEvents = append(wantEvents, "loopback.observe:"+address.String())
	}
	absentIndex := 0
	for index, address := range addresses {
		identity := ticket.ExpectedLoopbackPool.Identities[index]
		if identity.ExpectedObservation.State == helper.ObservationAbsent {
			wantEvents = append(wantEvents, "preassignment.observe:"+address.String())
			if observer.requests[absentIndex].Candidate() != address || len(observer.requests[absentIndex].Requirements()) != 0 || observer.requesterIdentities[absentIndex] != ticket.RequesterIdentity {
				t.Fatalf("pre-assignment call %d = %s %#v for %q", absentIndex, observer.requests[absentIndex].Candidate(), observer.requests[absentIndex].Requirements(), observer.requesterIdentities[absentIndex])
			}
			absentIndex++
		}
		wantEvents = append(wantEvents, "loopback.ensure:"+address.String())

		call := adapter.ensureCalls[index]
		if call.address != address || call.fingerprint != identity.ExpectedObservation.Fingerprint {
			t.Fatalf("ensure call %d = %s %q, want %s %q", index, call.address, call.fingerprint, address, identity.ExpectedObservation.Fingerprint)
		}
		identityEvidence := evidence.Identities[index]
		if identityEvidence.Address != address.String() || identityEvidence.Observation.State != helper.ObservationOwned || identityEvidence.Changed != (index%2 == 0) {
			t.Fatalf("identity evidence %d = %#v", index, identityEvidence)
		}
		wantFingerprint, fingerprintErr := adapter.observations[address].Fingerprint()
		if fingerprintErr != nil {
			t.Fatalf("Fingerprint(%s) error = %v", address, fingerprintErr)
		}
		if identityEvidence.Observation.Fingerprint != wantFingerprint {
			t.Fatalf("identity evidence %d fingerprint = %q, want %q", index, identityEvidence.Observation.Fingerprint, wantFingerprint)
		}
	}
	if absentIndex != len(observer.requests) {
		t.Fatalf("pre-assignment calls = %d, want %d", len(observer.requests), absentIndex)
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("pool operation events = %#v, want %#v", events, wantEvents)
	}
	if adapter.releaseCalls != 0 {
		t.Fatalf("release calls = %d, want 0", adapter.releaseCalls)
	}
}

// TestHandlerRejectsInvalidLoopbackPoolAuthorityWithoutHostEffects keeps direct handler calls inside the exact signed /29 shape.
func TestHandlerRejectsInvalidLoopbackPoolAuthorityWithoutHostEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*helper.Ticket)
	}{
		{name: "wrong operation", mutate: func(ticket *helper.Ticket) { ticket.Operation = helper.OperationEnsureLoopbackIdentity }},
		{name: "wrong prefix size", mutate: func(ticket *helper.Ticket) { ticket.ApprovedPool = "127.77.0.0/28" }},
		{name: "noncanonical prefix", mutate: func(ticket *helper.Ticket) { ticket.ApprovedPool = "127.77.0.9/29" }},
		{name: "legacy address", mutate: func(ticket *helper.Ticket) { ticket.ApprovedAddress = "127.77.0.8" }},
		{name: "legacy observation", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedObservation = helper.ExpectedObservation{State: helper.ObservationAbsent, Fingerprint: strings.Repeat("0", 64)}
		}},
		{name: "legacy pre-assignment", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedPreAssignment = &helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("0", 64), Requirements: []helper.SocketRequirement{}}
		}},
		{name: "missing pool authority", mutate: func(ticket *helper.Ticket) { ticket.ExpectedLoopbackPool = nil }},
		{name: "seven identities", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities = ticket.ExpectedLoopbackPool.Identities[:7]
		}},
		{name: "out of order identities", mutate: func(ticket *helper.Ticket) {
			identities := ticket.ExpectedLoopbackPool.Identities
			identities[2], identities[3] = identities[3], identities[2]
		}},
		{name: "socket authority", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment.Requirements = []helper.SocketRequirement{{Transport: helper.SocketTransportTCP4, Port: 443}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observations := handlerPoolObservations(func(int) loopback.State { return loopback.StateAbsent })
			ticket := handlerPoolTicket(t, observations)
			test.mutate(&ticket)
			events := []string{}
			adapter := &poolFakeAdapter{observations: observations, events: &events}
			observer := &poolFakePreAssignmentObserver{events: &events}

			if evidence, err := newHandler(adapter, observer.observe).EnsureLoopbackPool(context.Background(), ticket); err == nil {
				t.Fatalf("EnsureLoopbackPool() evidence = %#v, want error", evidence)
			}
			if len(events) != 0 || len(adapter.observeCalls) != 0 || len(adapter.ensureCalls) != 0 || len(observer.requests) != 0 || adapter.releaseCalls != 0 {
				t.Fatalf("invalid authority touched host: events %#v", events)
			}
		})
	}
}

// TestValidatePoolMutationTicketCopiesAuthority proves host effects cannot reread mutable ticket-backed pool fields after admission.
func TestValidatePoolMutationTicketCopiesAuthority(t *testing.T) {
	observations := handlerPoolObservations(func(int) loopback.State { return loopback.StateAbsent })
	ticket := handlerPoolTicket(t, observations)
	wantObservation := ticket.ExpectedLoopbackPool.Identities[0].ExpectedObservation
	wantPreAssignment := *ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment

	plan, err := validatePoolMutationTicket(ticket)
	if err != nil {
		t.Fatalf("validatePoolMutationTicket() error = %v", err)
	}
	ticket.RequesterIdentity = "changed"
	ticket.ExpectedLoopbackPool.Identities[0].Address = "127.77.0.15"
	ticket.ExpectedLoopbackPool.Identities[0].ExpectedObservation = helper.ExpectedObservation{}
	ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment.Fingerprint = strings.Repeat("f", 64)
	ticket.ExpectedLoopbackPool.Identities[0].ExpectedPreAssignment.Requirements = []helper.SocketRequirement{{Transport: helper.SocketTransportTCP4, Port: 443}}

	identity := plan.identities[0]
	if plan.pool.String() != "127.77.0.8/29" || plan.requesterIdentity != "1000" || len(plan.identities) != loopbackPoolIdentityCount {
		t.Fatalf("copied pool plan = %#v", plan)
	}
	if identity.address != handlerPoolAddresses()[0] || identity.expectedObservation != wantObservation || identity.expectedPreAssignment.Fingerprint != wantPreAssignment.Fingerprint || identity.expectedPreAssignment.Requirements == nil || len(identity.expectedPreAssignment.Requirements) != 0 {
		t.Fatalf("copied identity plan = %#v", identity)
	}
}

// TestHandlerPoolObservationFailurePreventsEveryMutation proves a late stale read cannot leave avoidable partial progress.
func TestHandlerPoolObservationFailurePreventsEveryMutation(t *testing.T) {
	addresses := handlerPoolAddresses()
	observations := handlerPoolObservations(func(int) loopback.State { return loopback.StateAbsent })
	ticket := handlerPoolTicket(t, observations)
	cause := errors.New("last pool identity observation failed")
	events := []string{}
	adapter := &poolFakeAdapter{
		observations:  observations,
		observeErrors: map[netip.Addr]error{addresses[len(addresses)-1]: cause},
		events:        &events,
	}
	observer := handlerPoolPreAssignmentObserver(t, ticket)
	observer.events = &events

	if evidence, err := newHandler(adapter, observer.observe).EnsureLoopbackPool(context.Background(), ticket); !errors.Is(err, cause) {
		t.Fatalf("EnsureLoopbackPool() = %#v, %v, want observation cause", evidence, err)
	}
	if !slices.Equal(adapter.observeCalls, addresses) || len(adapter.ensureCalls) != 0 || len(observer.requests) != 0 || adapter.releaseCalls != 0 {
		t.Fatalf("failure calls = observe %#v, pre-assignment %d, ensure %d, release %d", adapter.observeCalls, len(observer.requests), len(adapter.ensureCalls), adapter.releaseCalls)
	}
}

// TestHandlerPoolFailureLeavesRepairablePartialProgress proves failures stop without rollback and a fresh mixed ticket completes the pool.
func TestHandlerPoolFailureLeavesRepairablePartialProgress(t *testing.T) {
	cause := errors.New("injected pool effect failure")
	tests := []struct {
		name           string
		configure      func(netip.Addr, *poolFakeAdapter, *poolFakePreAssignmentObserver)
		wantExact      int
		wantEnsures    int
		wantLastPrefix string
	}{
		{
			name: "pre-assignment failure",
			configure: func(address netip.Addr, _ *poolFakeAdapter, observer *poolFakePreAssignmentObserver) {
				observer.errors = map[netip.Addr]error{address: cause}
			},
			wantExact:      3,
			wantEnsures:    3,
			wantLastPrefix: "preassignment.observe:",
		},
		{
			name: "ensure error after effect landed",
			configure: func(address netip.Addr, adapter *poolFakeAdapter, _ *poolFakePreAssignmentObserver) {
				adapter.ensureErrors = map[netip.Addr]error{address: cause}
				adapter.ensureEffectsOnErr = map[netip.Addr]bool{address: true}
			},
			wantExact:      4,
			wantEnsures:    4,
			wantLastPrefix: "loopback.ensure:",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addresses := handlerPoolAddresses()
			failureAddress := addresses[3]
			observations := handlerPoolObservations(func(int) loopback.State { return loopback.StateAbsent })
			ticket := handlerPoolTicket(t, observations)
			events := []string{}
			adapter := &poolFakeAdapter{observations: observations, events: &events}
			observer := handlerPoolPreAssignmentObserver(t, ticket)
			observer.events = &events
			test.configure(failureAddress, adapter, observer)

			evidence, err := newHandler(adapter, observer.observe).EnsureLoopbackPool(context.Background(), ticket)
			if !errors.Is(err, cause) {
				t.Fatalf("EnsureLoopbackPool() error = %v, want injected cause", err)
			}
			if evidence.Pool != "" || evidence.Identities != nil {
				t.Fatalf("failed pool evidence = %#v, want zero value", evidence)
			}
			if len(adapter.observeCalls) != len(addresses) || len(adapter.ensureCalls) != test.wantEnsures || len(observer.requests) != 4 || adapter.releaseCalls != 0 {
				t.Fatalf("failure calls = observe %d, pre-assignment %d, ensure %d, release %d", len(adapter.observeCalls), len(observer.requests), len(adapter.ensureCalls), adapter.releaseCalls)
			}
			if events[len(events)-1] != test.wantLastPrefix+failureAddress.String() {
				t.Fatalf("last failure event = %q", events[len(events)-1])
			}
			for index, address := range addresses {
				wantState := loopback.StateAbsent
				if index < test.wantExact {
					wantState = loopback.StateExact
				}
				if adapter.observations[address].State != wantState {
					t.Fatalf("partial state %s = %q, want %q", address, adapter.observations[address].State, wantState)
				}
			}

			repairTicket := handlerPoolTicket(t, adapter.observations)
			repairEvents := []string{}
			repairAdapter := &poolFakeAdapter{observations: adapter.observations, events: &repairEvents}
			repairObserver := handlerPoolPreAssignmentObserver(t, repairTicket)
			repairObserver.events = &repairEvents
			repaired, repairErr := newHandler(repairAdapter, repairObserver.observe).EnsureLoopbackPool(context.Background(), repairTicket)
			if repairErr != nil {
				t.Fatalf("repair EnsureLoopbackPool() error = %v", repairErr)
			}
			if repaired.Pool != repairTicket.ApprovedPool || len(repaired.Identities) != len(addresses) || len(repairAdapter.ensureCalls) != len(addresses) || len(repairObserver.requests) != len(addresses)-test.wantExact || repairAdapter.releaseCalls != 0 {
				t.Fatalf("repair result = %#v, pre-assignment %d, ensure %d, release %d", repaired, len(repairObserver.requests), len(repairAdapter.ensureCalls), repairAdapter.releaseCalls)
			}
			for index, address := range addresses {
				if repairAdapter.observations[address].State != loopback.StateExact || repaired.Identities[index].Address != address.String() || repaired.Identities[index].Observation.State != helper.ObservationOwned {
					t.Fatalf("repaired identity %d = %#v with state %q", index, repaired.Identities[index], repairAdapter.observations[address].State)
				}
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
		mutate    func(*testing.T, *helper.Ticket)
		observes  int
	}{
		{name: "wrong operation", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.Operation = helper.OperationReleaseLoopbackIdentity }},
		{name: "invalid fingerprint", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.ExpectedObservation.Fingerprint = "bad" }},
		{name: "invalid address", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.ApprovedAddress = "192.0.2.1" }},
		{name: "invalid pool", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.ApprovedPool = "not-a-prefix" }},
		{name: "address outside pool", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.ApprovedPool = "127.78.0.0/24" }},
		{name: "missing pre-assignment", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.ExpectedPreAssignment = nil }},
		{name: "invalid pre-assignment", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.ExpectedPreAssignment.Fingerprint = "bad" }},
		{name: "owned ensure with pre-assignment", operation: helper.OperationEnsureLoopbackIdentity, before: exact, mutate: func(t *testing.T, ticket *helper.Ticket) {
			ticket.ExpectedPreAssignment = handlerTicket(t, helper.OperationEnsureLoopbackIdentity, absent).ExpectedPreAssignment
		}},
		{name: "release with pre-assignment", operation: helper.OperationReleaseLoopbackIdentity, before: exact, mutate: func(t *testing.T, ticket *helper.Ticket) {
			ticket.ExpectedPreAssignment = handlerTicket(t, helper.OperationEnsureLoopbackIdentity, absent).ExpectedPreAssignment
		}},
		{name: "single address with pool authority", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) {
			ticket.ExpectedLoopbackPool = &helper.ExpectedLoopbackPool{}
		}},
		{name: "changed fingerprint", operation: helper.OperationEnsureLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) {
			ticket.ExpectedObservation.Fingerprint = strings.Repeat("a", 64)
		}, observes: 1},
		{name: "changed state", operation: helper.OperationEnsureLoopbackIdentity, before: exact, mutate: func(t *testing.T, ticket *helper.Ticket) {
			ticket.ExpectedObservation.State = helper.ObservationAbsent
			ticket.ExpectedPreAssignment = handlerTicket(t, helper.OperationEnsureLoopbackIdentity, absent).ExpectedPreAssignment
		}, observes: 1},
		{name: "release requires owned", operation: helper.OperationReleaseLoopbackIdentity, before: absent, mutate: func(_ *testing.T, ticket *helper.Ticket) { ticket.ExpectedObservation.State = helper.ObservationAbsent }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &fakeAdapter{observation: test.before}
			ticket := handlerTicket(t, test.operation, test.before)
			observer := handlerPreAssignmentObserver(t, ticket)
			test.mutate(t, &ticket)
			handler := newHandler(adapter, observer.observe)

			var err error
			if test.operation == helper.OperationReleaseLoopbackIdentity {
				_, err = handler.ReleaseLoopbackIdentity(context.Background(), ticket)
			} else {
				_, err = handler.EnsureLoopbackIdentity(context.Background(), ticket)
			}
			if err == nil {
				t.Fatal("handler rejected precondition error = nil")
			}
			if adapter.observeCalls != test.observes || observer.calls != 0 || adapter.ensureCalls != 0 || adapter.releaseCalls != 0 {
				t.Fatalf("adapter calls = loopback %d, pre-assignment %d, ensure %d, release %d", adapter.observeCalls, observer.calls, adapter.ensureCalls, adapter.releaseCalls)
			}
		})
	}
}

// TestHandlerRejectsUnsafeOrChangedPreAssignment proves a fresh absent assignment cannot outlive its native safety facts.
func TestHandlerRejectsUnsafeOrChangedPreAssignment(t *testing.T) {
	absent := handlerObservation(loopback.StateAbsent)
	cause := errors.New("native observation unavailable")
	tests := []struct {
		name      string
		mutate    func(*helper.Ticket, *fakePreAssignmentObserver)
		wantCause bool
	}{
		{name: "observer failure", mutate: func(_ *helper.Ticket, observer *fakePreAssignmentObserver) { observer.err = cause }, wantCause: true},
		{name: "malformed observation", mutate: func(_ *helper.Ticket, observer *fakePreAssignmentObserver) {
			observer.observation.Routes.Selected = nil
		}},
		{name: "socket conflict", mutate: func(_ *helper.Ticket, observer *fakePreAssignmentObserver) {
			observer.observation.Sockets.Endpoints = []hostconflict.SocketFact{{
				Protocol:     hostconflict.SocketProtocolTCP,
				Address:      handlerTestAddress,
				Port:         443,
				TCPAccepting: true,
				IPv6Only:     hostconflict.IPv6OnlyNotApplicable,
			}}
		}},
		{name: "indeterminate sockets", mutate: func(_ *helper.Ticket, observer *fakePreAssignmentObserver) {
			observer.observation.Sockets.Complete = false
		}},
		{name: "changed fingerprint", mutate: func(ticket *helper.Ticket, _ *fakePreAssignmentObserver) {
			replacement := "0"
			if strings.HasPrefix(ticket.ExpectedPreAssignment.Fingerprint, replacement) {
				replacement = "1"
			}
			ticket.ExpectedPreAssignment.Fingerprint = replacement + ticket.ExpectedPreAssignment.Fingerprint[1:]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			ticket := handlerTicket(t, helper.OperationEnsureLoopbackIdentity, absent)
			observer := handlerPreAssignmentObserver(t, ticket)
			observer.events = &events
			test.mutate(&ticket, observer)
			adapter := &fakeAdapter{observation: absent, events: &events}

			_, err := newHandler(adapter, observer.observe).EnsureLoopbackIdentity(context.Background(), ticket)
			if err == nil {
				t.Fatal("EnsureLoopbackIdentity() error = nil")
			}
			if test.wantCause && !errors.Is(err, cause) {
				t.Fatalf("EnsureLoopbackIdentity() error = %v, want observer cause", err)
			}
			if adapter.observeCalls != 1 || observer.calls != 1 || adapter.ensureCalls != 0 || adapter.releaseCalls != 0 {
				t.Fatalf("adapter calls = loopback %d, pre-assignment %d, ensure %d, release %d", adapter.observeCalls, observer.calls, adapter.ensureCalls, adapter.releaseCalls)
			}
			wantEvents := []string{"loopback.observe", "preassignment.observe"}
			if !slices.Equal(events, wantEvents) {
				t.Fatalf("operation events = %#v, want %#v", events, wantEvents)
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
	if _, err := newHandler(adapter, handlerPreAssignmentObserver(t, ticket).observe).EnsureLoopbackIdentity(context.Background(), ticket); !errors.Is(err, cause) {
		t.Fatalf("Observe failure error = %v, want cause", err)
	}

	adapter = &fakeAdapter{observation: absent, ensureErr: cause}
	if _, err := newHandler(adapter, handlerPreAssignmentObserver(t, ticket).observe).EnsureLoopbackIdentity(context.Background(), ticket); !errors.Is(err, cause) {
		t.Fatalf("Ensure failure error = %v, want cause", err)
	}

	adapter = &fakeAdapter{observation: exact, observeErr: cause}
	if _, err := newHandler(adapter, handlerPreAssignmentObserver(t, releaseTicket).observe).ReleaseLoopbackIdentity(context.Background(), releaseTicket); !errors.Is(err, cause) {
		t.Fatalf("release Observe failure error = %v, want cause", err)
	}

	adapter = &fakeAdapter{observation: exact, releaseErr: cause}
	if _, err := newHandler(adapter, handlerPreAssignmentObserver(t, releaseTicket).observe).ReleaseLoopbackIdentity(context.Background(), releaseTicket); !errors.Is(err, cause) {
		t.Fatalf("Release failure error = %v, want cause", err)
	}

	malformed := absent
	malformed.Loopback = loopback.InterfaceFact{}
	adapter = &fakeAdapter{observation: malformed}
	if _, err := newHandler(adapter, handlerPreAssignmentObserver(t, ticket).observe).EnsureLoopbackIdentity(context.Background(), ticket); err == nil {
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
		if evidence, err := newHandler(adapter, handlerPreAssignmentObserver(t, ticket).observe).EnsureLoopbackIdentity(context.Background(), ticket); err == nil {
			t.Fatalf("invalid change %d evidence = %#v, want error", index, evidence)
		}
	}

	if evidence, err := evidenceFromChange(helper.Operation("unknown"), handlerTestAddress, loopback.Change{After: exact}); err == nil {
		t.Fatalf("unsupported operation evidence = %#v, want error", evidence)
	}
}

// handlerObservation returns one canonical absent or exact native-loopback snapshot.
func handlerObservation(state loopback.State) loopback.Observation {
	return handlerObservationAt(handlerTestAddress, state)
}

// handlerObservationAt returns one canonical absent or exact native-loopback snapshot for an explicit address.
func handlerObservationAt(address netip.Addr, state loopback.State) loopback.Observation {
	observation := loopback.Observation{Address: address, Loopback: handlerTestLoopback, State: state, Assignments: []loopback.AssignmentFact{}}
	if state == loopback.StateExact {
		observation.Assignments = []loopback.AssignmentFact{{
			Address:        address,
			PrefixLength:   32,
			InterfaceName:  handlerTestLoopback.Name,
			InterfaceIndex: handlerTestLoopback.Index,
			NativeLoopback: true,
			InterfaceKind:  handlerTestLoopback.Kind,
			Linux: &loopback.LinuxAssignmentFact{
				Scope:                    loopback.LinuxAddressScopeHost,
				Flags:                    1 << 7,
				Label:                    handlerTestLoopback.Name,
				AddressMatchesLocal:      true,
				CacheInfoPresent:         true,
				ValidLifetimeSeconds:     ^uint32(0),
				PreferredLifetimeSeconds: ^uint32(0),
			},
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
	ticket := helper.Ticket{
		Operation:         operation,
		RequesterIdentity: "1000",
		ApprovedPool:      "127.77.0.0/24",
		ApprovedAddress:   handlerTestAddress.String(),
		ExpectedObservation: helper.ExpectedObservation{
			State:       state,
			Fingerprint: fingerprint,
		},
	}
	if operation == helper.OperationEnsureLoopbackIdentity && state == helper.ObservationAbsent {
		expected := helper.ExpectedPreAssignment{
			Fingerprint: strings.Repeat("0", 64),
			Requirements: []helper.SocketRequirement{
				{Transport: helper.SocketTransportTCP4, Port: 443},
				{Transport: helper.SocketTransportUDP4, Port: 53},
			},
		}
		request, err := newPreAssignmentRequest(handlerTestAddress, expected)
		if err != nil {
			t.Fatalf("newPreAssignmentRequest(ticket) error = %v", err)
		}
		preAssignment := handlerSafePreAssignmentObservation(request)
		expected.Fingerprint, err = preAssignment.Fingerprint()
		if err != nil {
			t.Fatalf("Fingerprint(pre-assignment ticket observation) error = %v", err)
		}
		ticket.ExpectedPreAssignment = &expected
	}
	return ticket
}

// handlerPoolAddresses returns every address from the canonical pool fixture in numeric order.
func handlerPoolAddresses() []netip.Addr {
	pool := netip.MustParsePrefix("127.77.0.8/29")
	addresses := make([]netip.Addr, 0, loopbackPoolIdentityCount)
	address := pool.Addr()
	for range loopbackPoolIdentityCount {
		addresses = append(addresses, address)
		address = address.Next()
	}
	return addresses
}

// handlerPoolObservations returns address-specific fixture facts for the requested state pattern.
func handlerPoolObservations(stateForIndex func(int) loopback.State) map[netip.Addr]loopback.Observation {
	addresses := handlerPoolAddresses()
	observations := make(map[netip.Addr]loopback.Observation, len(addresses))
	for index, address := range addresses {
		observations[address] = handlerObservationAt(address, stateForIndex(index))
	}
	return observations
}

// handlerPoolTicket binds one exact signed /29 authority to the supplied current assignment facts.
func handlerPoolTicket(t *testing.T, observations map[netip.Addr]loopback.Observation) helper.Ticket {
	t.Helper()
	identities := make([]helper.ExpectedLoopbackIdentity, 0, loopbackPoolIdentityCount)
	for _, address := range handlerPoolAddresses() {
		observation, found := observations[address]
		if !found {
			t.Fatalf("pool ticket observation for %s is missing", address)
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatalf("Fingerprint(pool ticket %s) error = %v", address, err)
		}
		state := helper.ObservationAbsent
		switch observation.State {
		case loopback.StateAbsent:
		case loopback.StateExact:
			state = helper.ObservationOwned
		default:
			t.Fatalf("pool ticket observation %s has unsupported state %q", address, observation.State)
		}
		identity := helper.ExpectedLoopbackIdentity{
			Address: address.String(),
			ExpectedObservation: helper.ExpectedObservation{
				State:       state,
				Fingerprint: fingerprint,
			},
		}
		if state == helper.ObservationAbsent {
			expected := helper.ExpectedPreAssignment{
				Fingerprint:  strings.Repeat("0", 64),
				Requirements: []helper.SocketRequirement{},
			}
			request, requestErr := newPreAssignmentRequest(address, expected)
			if requestErr != nil {
				t.Fatalf("newPreAssignmentRequest(pool ticket %s) error = %v", address, requestErr)
			}
			preAssignment := handlerSafePreAssignmentObservation(request)
			expected.Fingerprint, err = preAssignment.Fingerprint()
			if err != nil {
				t.Fatalf("Fingerprint(pool pre-assignment %s) error = %v", address, err)
			}
			identity.ExpectedPreAssignment = &expected
		}
		identities = append(identities, identity)
	}

	return helper.Ticket{
		Operation:            helper.OperationEnsureLoopbackPool,
		RequesterIdentity:    "1000",
		ApprovedPool:         "127.77.0.8/29",
		ExpectedLoopbackPool: &helper.ExpectedLoopbackPool{Identities: identities},
	}
}

// handlerPoolPreAssignmentObserver returns candidate-specific facts matching every absent pool identity.
func handlerPoolPreAssignmentObserver(t *testing.T, ticket helper.Ticket) *poolFakePreAssignmentObserver {
	t.Helper()
	observer := &poolFakePreAssignmentObserver{
		observations: make(map[netip.Addr]hostconflict.Observation),
	}
	if ticket.ExpectedLoopbackPool == nil {
		return observer
	}
	for _, identity := range ticket.ExpectedLoopbackPool.Identities {
		if identity.ExpectedPreAssignment == nil {
			continue
		}
		address, err := netip.ParseAddr(identity.Address)
		if err != nil {
			t.Fatalf("ParseAddr(pool pre-assignment %q) error = %v", identity.Address, err)
		}
		request, err := newPreAssignmentRequest(address, *identity.ExpectedPreAssignment)
		if err != nil {
			t.Fatalf("newPreAssignmentRequest(pool observer %s) error = %v", address, err)
		}
		observer.observations[address] = handlerSafePreAssignmentObservation(request)
	}
	return observer
}

// handlerPreAssignmentObserver returns native facts matching an absent ensure ticket and a fail-closed sentinel for all other operations.
func handlerPreAssignmentObserver(t *testing.T, ticket helper.Ticket) *fakePreAssignmentObserver {
	t.Helper()
	if ticket.ExpectedPreAssignment == nil {
		return &fakePreAssignmentObserver{}
	}
	request, err := newPreAssignmentRequest(handlerTestAddress, *ticket.ExpectedPreAssignment)
	if err != nil {
		t.Fatalf("newPreAssignmentRequest(observer) error = %v", err)
	}
	return &fakePreAssignmentObserver{observation: handlerSafePreAssignmentObservation(request)}
}

// handlerSafePreAssignmentObservation returns one complete conflict-free native fact set for the supplied request.
func handlerSafePreAssignmentObservation(request hostconflict.Request) hostconflict.Observation {
	loopbackIdentity := hostconflict.LoopbackIdentity{
		Interface: hostconflict.InterfaceIdentity{Name: "lo0", Index: 1},
		Kind:      hostconflict.LoopbackKindMacOSNative,
	}
	baseline := hostconflict.RouteFact{
		Destination:    netip.MustParsePrefix("127.0.0.0/8"),
		Interface:      loopbackIdentity.Interface,
		NativeLoopback: true,
		Normalization:  hostconflict.RouteNormalizationDirect,
		NativeFlags:    1,
	}
	return hostconflict.Observation{
		Request:  request,
		Scope:    hostconflict.NewMacOSScope(),
		Loopback: loopbackIdentity,
		Routes: hostconflict.RouteSnapshot{
			Complete: true,
			Selected: &baseline,
			Matching: []hostconflict.RouteFact{baseline},
		},
		Sockets: hostconflict.SocketSnapshot{Complete: true},
	}
}
