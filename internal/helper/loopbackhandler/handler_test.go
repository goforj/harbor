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
