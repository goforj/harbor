package resolverhandler

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// TestHandlerAppliesObservationBoundResolverOperations covers ensure and release evidence conversion.
func TestHandlerAppliesObservationBoundResolverOperations(t *testing.T) {
	request, policy := resolverHandlerTestRequest(t)
	exact := resolverHandlerExactObservation(request)
	absent := resolver.Observation{Request: request, Complete: true}

	tests := []struct {
		name          string
		operation     helper.Operation
		after         resolver.Observation
		postcondition helper.ResolverPostcondition
	}{
		{
			name:          "ensure",
			operation:     helper.OperationEnsureResolver,
			after:         exact,
			postcondition: helper.ResolverPostconditionExact,
		},
		{
			name:          "release",
			operation:     helper.OperationReleaseResolver,
			after:         absent,
			postcondition: helper.ResolverPostconditionOwnedAbsent,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := absent
			if test.operation == helper.OperationReleaseResolver {
				before = exact
			}
			beforeFingerprint, err := before.Fingerprint()
			if err != nil {
				t.Fatalf("Observation.Fingerprint() before error = %v", err)
			}
			adapter := &testConditionalAdapter{change: resolver.Change{
				Attempted: true,
				Changed:   true,
				Before:    before,
				After:     test.after,
			}}
			handler := newHandler(adapter, &testOwnershipUpgrader{})
			ticket := resolverHandlerTestTicket(t, policy, test.operation, beforeFingerprint)
			admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent)

			var evidence helper.ResolverMutationEvidence
			if test.operation == helper.OperationEnsureResolver {
				evidence, err = handler.EnsureResolver(t.Context(), ticket, admission)
			} else {
				evidence, err = handler.ReleaseResolver(t.Context(), ticket, admission)
			}
			if err != nil {
				t.Fatalf("resolver handler error = %v", err)
			}
			afterFingerprint, err := test.after.Fingerprint()
			if err != nil {
				t.Fatalf("Observation.Fingerprint() after error = %v", err)
			}
			if !evidence.Changed || evidence.PolicyFingerprint != request.PolicyFingerprint() ||
				evidence.OwnershipFingerprint != admission.TargetOwnershipFingerprint ||
				evidence.ObservationFingerprint != afterFingerprint || evidence.Postcondition != test.postcondition {
				t.Fatalf("resolver evidence = %#v", evidence)
			}
			if adapter.operation != test.operation || adapter.request.Policy() != request.Policy() ||
				adapter.request.InstallationID() != request.InstallationID() || adapter.expected != beforeFingerprint {
				t.Fatalf("resolver adapter call = operation %q, request %#v, expected %q", adapter.operation, adapter.request, adapter.expected)
			}
		})
	}
}

// TestHandlerRejectsInvalidResolverAuthority keeps direct handler use inside the signed policy boundary.
func TestHandlerRejectsInvalidResolverAuthority(t *testing.T) {
	request, policy := resolverHandlerTestRequest(t)
	absent := resolver.Observation{Request: request, Complete: true}
	fingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*helper.Ticket)
	}{
		{name: "wrong operation", mutate: func(ticket *helper.Ticket) { ticket.Operation = helper.OperationReleaseResolver }},
		{name: "identity ownership", mutate: func(ticket *helper.Ticket) { ticket.OwnershipSchemaVersion = ownership.IdentitySchemaVersion }},
		{name: "missing policy", mutate: func(ticket *helper.Ticket) { ticket.NetworkPolicy = nil }},
		{name: "invalid policy", mutate: func(ticket *helper.Ticket) { ticket.NetworkPolicy.Suffix = ".invalid" }},
		{name: "policy mismatch", mutate: func(ticket *helper.Ticket) { ticket.NetworkPolicyFingerprint = strings.Repeat("f", 64) }},
		{name: "missing observation", mutate: func(ticket *helper.Ticket) { ticket.ExpectedResolverObservation = nil }},
		{name: "invalid observation", mutate: func(ticket *helper.Ticket) { ticket.ExpectedResolverObservation.Fingerprint = "bad" }},
		{name: "loopback authority", mutate: func(ticket *helper.Ticket) { ticket.ApprovedAddress = "127.77.0.10" }},
		{name: "invalid installation", mutate: func(ticket *helper.Ticket) { ticket.InstallationID = "../other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := resolverHandlerTestTicket(t, policy, helper.OperationEnsureResolver, fingerprint)
			admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent)
			test.mutate(&ticket)
			adapter := &testConditionalAdapter{}
			if _, err := newHandler(adapter, &testOwnershipUpgrader{}).EnsureResolver(t.Context(), ticket, admission); err == nil {
				t.Fatal("EnsureResolver() accepted invalid ticket")
			}
			if adapter.operation != "" {
				t.Fatalf("resolver adapter operation = %q, want no call", adapter.operation)
			}
		})
	}
}

// TestHandlerRejectsUnverifiedResolverPostconditions covers indeterminate and wrong-state adapter outcomes.
func TestHandlerRejectsUnverifiedResolverPostconditions(t *testing.T) {
	request, policy := resolverHandlerTestRequest(t)
	absent := resolver.Observation{Request: request, Complete: true}
	exact := resolverHandlerExactObservation(request)
	absentFingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	exactFingerprint, err := exact.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		operation helper.Operation
		expected  string
		change    resolver.Change
	}{
		{
			name:      "indeterminate ensure",
			operation: helper.OperationEnsureResolver,
			expected:  absentFingerprint,
			change:    resolver.Change{Indeterminate: true, Before: absent},
		},
		{
			name:      "ensure remains absent",
			operation: helper.OperationEnsureResolver,
			expected:  absentFingerprint,
			change:    resolver.Change{Before: absent, After: absent},
		},
		{
			name:      "release remains owned",
			operation: helper.OperationReleaseResolver,
			expected:  exactFingerprint,
			change:    resolver.Change{Before: exact, After: exact},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &testConditionalAdapter{change: test.change}
			handler := newHandler(adapter, &testOwnershipUpgrader{})
			ticket := resolverHandlerTestTicket(t, policy, test.operation, test.expected)
			admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent)
			var err error
			if test.operation == helper.OperationEnsureResolver {
				_, err = handler.EnsureResolver(t.Context(), ticket, admission)
			} else {
				_, err = handler.ReleaseResolver(t.Context(), ticket, admission)
			}
			if err == nil {
				t.Fatal("resolver handler accepted an unverified postcondition")
			}
		})
	}
}

// TestHandlerPropagatesConditionalAdapterFailure preserves typed native failures for privileged diagnostics.
func TestHandlerPropagatesConditionalAdapterFailure(t *testing.T) {
	request, policy := resolverHandlerTestRequest(t)
	absent := resolver.Observation{Request: request, Complete: true}
	fingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("resolver mutation failed")
	adapter := &testConditionalAdapter{err: cause}
	ticket := resolverHandlerTestTicket(t, policy, helper.OperationEnsureResolver, fingerprint)
	_, err = newHandler(adapter, &testOwnershipUpgrader{}).EnsureResolver(
		t.Context(),
		ticket,
		resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent),
	)
	if !errors.Is(err, cause) {
		t.Fatalf("EnsureResolver() error = %v, want %v", err, cause)
	}
}

// TestHandlerTransitionsOwnershipBeforeResolverEnsure proves replay-admitted schema migration precedes native mutation.
func TestHandlerTransitionsOwnershipBeforeResolverEnsure(t *testing.T) {
	request, policy := resolverHandlerTestRequest(t)
	absent := resolver.Observation{Request: request, Complete: true}
	absentFingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	events := make([]string, 0, 2)
	adapter := &testConditionalAdapter{
		change: resolver.Change{Before: absent, After: resolverHandlerExactObservation(request)},
		events: &events,
	}
	upgrader := &testOwnershipUpgrader{events: &events}
	ticket := resolverHandlerTestTicket(t, policy, helper.OperationEnsureResolver, absentFingerprint)
	admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionSchema1To2)
	if _, err := newHandler(adapter, upgrader).EnsureResolver(t.Context(), ticket, admission); err != nil {
		t.Fatalf("EnsureResolver() error = %v", err)
	}
	if len(events) != 2 || events[0] != "upgrade ownership" || events[1] != "ensure resolver" {
		t.Fatalf("events = %#v, want ownership upgrade before resolver ensure", events)
	}
	if upgrader.calls != 1 || upgrader.expected != admission.OwnershipFingerprint ||
		upgrader.target.SchemaVersion != ownership.NetworkPolicySchemaVersion ||
		upgrader.target.NetworkPolicyFingerprint != ticket.NetworkPolicyFingerprint {
		t.Fatalf("ownership upgrade = calls %d, expected %q, target %#v", upgrader.calls, upgrader.expected, upgrader.target)
	}
}

// TestDispatcherReplayFailurePreventsOwnershipUpgrade proves the real handler receives no mutation authority before consumption.
func TestDispatcherReplayFailurePreventsOwnershipUpgrade(t *testing.T) {
	now := time.Date(2026, time.July, 20, 2, 0, 0, 0, time.UTC)
	request, policy := resolverHandlerTestRequest(t)
	absent := resolver.Observation{Request: request, Complete: true}
	absentFingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	ticket := resolverHandlerTestTicket(t, policy, helper.OperationEnsureResolver, absentFingerprint)
	ticket.Version = helper.ProtocolVersion
	ticket.Nonce = strings.Repeat("e", 32)
	ticket.ExpiresAt = now.Add(time.Minute)
	reference := helper.TicketReference(strings.Repeat("e", 64))
	admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionSchema1To2)
	admission.TicketReference = reference
	upgrader := &testOwnershipUpgrader{}
	adapter := &testConditionalAdapter{}
	dispatcher := helper.NewDispatcherWithResolver(
		resolverHandlerRedemption{reference: reference, ticket: ticket, admission: admission},
		resolverHandlerClock{now: now},
		resolverHandlerRejectedReplay{},
		helper.UnavailableLoopbackIdentityHandler{},
		newHandler(adapter, upgrader),
	)

	response, err := dispatcher.Dispatch(t.Context(), helper.Request{
		Version:         helper.ProtocolVersion,
		TicketReference: reference,
	})
	if !errors.Is(err, helper.ErrReplayProtectionUnavailable) || response.Error == nil ||
		response.Error.Code != helper.ErrorCodeReplayProtectionUnavailable {
		t.Fatalf("Dispatch() = %#v, %v, want replay protection failure", response, err)
	}
	if upgrader.calls != 0 || adapter.calls != 0 {
		t.Fatalf("upgrade/resolver calls = %d/%d, want 0/0", upgrader.calls, adapter.calls)
	}
}

// TestHandlerRetriesResolverAfterCompletedOwnershipUpgrade proves a fresh schema-2 admission replays the upgrade as a no-op.
func TestHandlerRetriesResolverAfterCompletedOwnershipUpgrade(t *testing.T) {
	request, policy := resolverHandlerTestRequest(t)
	absent := resolver.Observation{Request: request, Complete: true}
	absentFingerprint, err := absent.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("first resolver ensure failed")
	adapter := &testConditionalAdapter{
		changes: []resolver.Change{
			{},
			{Before: absent, After: resolverHandlerExactObservation(request)},
		},
		errors: []error{cause, nil},
	}
	upgrader := &testOwnershipUpgrader{}
	handler := newHandler(adapter, upgrader)
	ticket := resolverHandlerTestTicket(t, policy, helper.OperationEnsureResolver, absentFingerprint)
	transition := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionSchema1To2)
	if _, err := handler.EnsureResolver(t.Context(), ticket, transition); !errors.Is(err, cause) {
		t.Fatalf("first EnsureResolver() error = %v, want %v", err, cause)
	}
	current := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent)
	if _, err := handler.EnsureResolver(t.Context(), ticket, current); err != nil {
		t.Fatalf("retry EnsureResolver() error = %v", err)
	}
	if upgrader.calls != 2 || adapter.calls != 2 {
		t.Fatalf("upgrade/resolver calls = %d/%d, want idempotent 2/2", upgrader.calls, adapter.calls)
	}
}

// TestHandlerCurrentOwnershipReplaysUpgradeAndReleaseRejectsTransition covers idempotence and the release prohibition.
func TestHandlerCurrentOwnershipReplaysUpgradeAndReleaseRejectsTransition(t *testing.T) {
	request, policy := resolverHandlerTestRequest(t)
	exact := resolverHandlerExactObservation(request)
	exactFingerprint, err := exact.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("schema two ensure", func(t *testing.T) {
		adapter := &testConditionalAdapter{change: resolver.Change{Before: exact, After: exact}}
		upgrader := &testOwnershipUpgrader{}
		ticket := resolverHandlerTestTicket(t, policy, helper.OperationEnsureResolver, exactFingerprint)
		admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionAlreadyCurrent)
		if _, err := newHandler(adapter, upgrader).EnsureResolver(t.Context(), ticket, admission); err != nil {
			t.Fatalf("EnsureResolver() error = %v", err)
		}
		if upgrader.calls != 1 || adapter.calls != 1 {
			t.Fatalf("upgrade/resolver calls = %d/%d, want idempotent 1/1", upgrader.calls, adapter.calls)
		}
	})

	t.Run("schema one release", func(t *testing.T) {
		adapter := &testConditionalAdapter{}
		upgrader := &testOwnershipUpgrader{}
		ticket := resolverHandlerTestTicket(t, policy, helper.OperationReleaseResolver, exactFingerprint)
		admission := resolverHandlerTestAdmission(t, ticket, helper.OwnershipAdmissionSchema1To2)
		if _, err := newHandler(adapter, upgrader).ReleaseResolver(t.Context(), ticket, admission); err == nil {
			t.Fatal("ReleaseResolver() accepted schema-1 transition admission")
		}
		if upgrader.calls != 0 || adapter.calls != 0 {
			t.Fatalf("upgrade/resolver calls = %d/%d, want 0/0", upgrader.calls, adapter.calls)
		}
	})
}

// TestOpenDefaultRequiresResolverAdapter keeps production composition fail-fast.
func TestOpenDefaultRequiresResolverAdapter(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("OpenDefault() accepted a nil resolver adapter")
		}
	}()
	_, _ = OpenDefault(nil)
}

// testConditionalAdapter records one resolver operation and returns configured evidence.
type testConditionalAdapter struct {
	change    resolver.Change
	err       error
	changes   []resolver.Change
	errors    []error
	events    *[]string
	calls     int
	operation helper.Operation
	request   resolver.Request
	expected  string
}

// Observe returns the change's before observation for interface completeness.
func (adapter *testConditionalAdapter) Observe(context.Context, resolver.Request) (resolver.Observation, error) {
	return adapter.change.Before, adapter.err
}

// EnsureIfObserved records one ensure call and returns the configured outcome.
func (adapter *testConditionalAdapter) EnsureIfObserved(
	_ context.Context,
	request resolver.Request,
	expected string,
) (resolver.Change, error) {
	call := adapter.calls
	adapter.calls++
	adapter.operation = helper.OperationEnsureResolver
	adapter.request = request
	adapter.expected = expected
	if adapter.events != nil {
		*adapter.events = append(*adapter.events, "ensure resolver")
	}
	if call < len(adapter.changes) || call < len(adapter.errors) {
		var change resolver.Change
		var err error
		if call < len(adapter.changes) {
			change = adapter.changes[call]
		}
		if call < len(adapter.errors) {
			err = adapter.errors[call]
		}
		return change, err
	}
	return adapter.change, adapter.err
}

// ReleaseIfObserved records one release call and returns the configured outcome.
func (adapter *testConditionalAdapter) ReleaseIfObserved(
	_ context.Context,
	request resolver.Request,
	expected string,
) (resolver.Change, error) {
	adapter.calls++
	adapter.operation = helper.OperationReleaseResolver
	adapter.request = request
	adapter.expected = expected
	return adapter.change, adapter.err
}

// testOwnershipUpgrader records the protected compare-and-swap without opening a filesystem path.
type testOwnershipUpgrader struct {
	events   *[]string
	err      error
	calls    int
	expected string
	target   ownership.Record
	closed   int
}

// resolverHandlerRedemption returns one independently bound transition admission.
type resolverHandlerRedemption struct {
	reference helper.TicketReference
	ticket    helper.Ticket
	admission helper.TicketAdmission
}

// Redeem returns the configured target without mutating ownership.
func (redeemer resolverHandlerRedemption) Redeem(
	_ context.Context,
	reference helper.TicketReference,
) (helper.TicketRedemption, error) {
	if reference != redeemer.reference {
		return helper.TicketRedemption{}, helper.ErrTicketRedemptionFailed
	}
	return helper.TicketRedemption{Ticket: redeemer.ticket, Admission: redeemer.admission}, nil
}

// resolverHandlerClock supplies deterministic dispatcher time.
type resolverHandlerClock struct {
	now time.Time
}

// Now returns the fixed admission instant.
func (clock resolverHandlerClock) Now() time.Time {
	return clock.now
}

// resolverHandlerRejectedReplay fails before the resolver handler can receive the ticket.
type resolverHandlerRejectedReplay struct{}

// Consume rejects the claim to exercise the durable boundary ordering.
func (resolverHandlerRejectedReplay) Consume(context.Context, helper.ReplayClaim) error {
	return helper.ErrReplayProtectionUnavailable
}

// Upgrade records one exact transition and returns the canonical target observation.
func (upgrader *testOwnershipUpgrader) Upgrade(
	_ context.Context,
	expected string,
	target ownership.Record,
) (ownership.Observation, error) {
	upgrader.calls++
	upgrader.expected = expected
	upgrader.target = target
	if upgrader.events != nil {
		*upgrader.events = append(*upgrader.events, "upgrade ownership")
	}
	if upgrader.err != nil {
		return ownership.Observation{}, upgrader.err
	}
	fingerprint, err := target.Fingerprint()
	if err != nil {
		return ownership.Observation{}, err
	}
	return ownership.Observation{Exists: true, Record: target, Fingerprint: fingerprint}, nil
}

// Close records release of the injected ownership authority.
func (upgrader *testOwnershipUpgrader) Close() error {
	upgrader.closed++
	return nil
}

// resolverHandlerTestRequest constructs one exact Darwin resolver authority.
func resolverHandlerTestRequest(t *testing.T) (resolver.Request, networkpolicy.Policy) {
	t.Helper()
	localhost := netip.MustParseAddr("127.0.0.1")
	dns := netip.AddrPortFrom(localhost, 25000)
	policy, err := networkpolicy.New(
		strings.Repeat("a", 64),
		networkpolicy.MacOSMechanisms(),
		networkpolicy.Listener{Advertised: dns, Bind: dns},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(localhost, 80),
			Bind:       netip.AddrPortFrom(localhost, 25001),
		},
		networkpolicy.Listener{
			Advertised: netip.AddrPortFrom(localhost, 443),
			Bind:       netip.AddrPortFrom(localhost, 25002),
		},
	)
	if err != nil {
		t.Fatalf("networkpolicy.New() fixture error = %v", err)
	}
	request, err := resolver.NewRequest("resolver-handler-test", policy)
	if err != nil {
		t.Fatalf("resolver.NewRequest() fixture error = %v", err)
	}
	return request, policy
}

// resolverHandlerTestTicket constructs one minimal policy-bound handler authority.
func resolverHandlerTestTicket(
	t *testing.T,
	policy networkpolicy.Policy,
	operation helper.Operation,
	expected string,
) helper.Ticket {
	t.Helper()
	fingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("Policy.Fingerprint() fixture error = %v", err)
	}
	return helper.Ticket{
		Operation:                operation,
		InstallationID:           "resolver-handler-test",
		RequesterIdentity:        "501",
		OwnershipGeneration:      7,
		OwnershipSchemaVersion:   ownership.NetworkPolicySchemaVersion,
		NetworkPolicyFingerprint: fingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		ExpectedResolverObservation: &helper.ExpectedResolverObservation{
			Fingerprint: expected,
		},
	}
}

// resolverHandlerTestAdmission derives one independently authenticated ownership binding for a ticket target.
func resolverHandlerTestAdmission(
	t *testing.T,
	ticket helper.Ticket,
	state helper.OwnershipAdmissionState,
) helper.TicketAdmission {
	t.Helper()
	verifierKey := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	target := ownership.Record{
		SchemaVersion:            ticket.OwnershipSchemaVersion,
		InstallationID:           ticket.InstallationID,
		OwnerIdentity:            ticket.RequesterIdentity,
		Generation:               ticket.OwnershipGeneration,
		LoopbackPoolPrefix:       ticket.ApprovedPool,
		NetworkPolicyFingerprint: ticket.NetworkPolicyFingerprint,
		TicketVerifierKey:        verifierKey,
	}
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatalf("ownership target admission fixture error = %v", err)
	}
	protected := target
	if state == helper.OwnershipAdmissionSchema1To2 {
		protected.SchemaVersion = ownership.IdentitySchemaVersion
		protected.NetworkPolicyFingerprint = ""
	}
	fingerprint, err := protected.Fingerprint()
	if err != nil {
		t.Fatalf("ownership admission fixture error = %v", err)
	}
	return helper.TicketAdmission{
		RequesterIdentity:          ticket.RequesterIdentity,
		InstallationID:             ticket.InstallationID,
		OwnershipGeneration:        ticket.OwnershipGeneration,
		OwnershipSchemaVersion:     ticket.OwnershipSchemaVersion,
		NetworkPolicyFingerprint:   ticket.NetworkPolicyFingerprint,
		ApprovedPool:               ticket.ApprovedPool,
		OwnershipState:             state,
		OwnershipFingerprint:       fingerprint,
		TargetOwnershipFingerprint: targetFingerprint,
		TicketVerifierKey:          verifierKey,
	}
}

// resolverHandlerExactObservation returns one exact policy-owned resolver rule.
func resolverHandlerExactObservation(request resolver.Request) resolver.Observation {
	owner := request.OwnerMarker()
	return resolver.Observation{
		Request:  request,
		Complete: true,
		Rules: []resolver.RuleFact{
			{
				Mechanism:              request.Mechanism(),
				NativeID:               "resolver-handler-native-rule",
				Namespace:              request.Suffix(),
				Servers:                []netip.AddrPort{request.Endpoint()},
				RouteOnly:              true,
				NativeExact:            true,
				NativeAttributesSHA256: strings.Repeat("b", 64),
				Owner:                  &owner,
			},
		},
	}
}

var _ conditionalAdapter = (*testConditionalAdapter)(nil)
var _ ownershipUpgrader = (*testOwnershipUpgrader)(nil)
var _ helper.TicketRedeemer = resolverHandlerRedemption{}
var _ helper.Clock = resolverHandlerClock{}
var _ helper.ReplayGuard = resolverHandlerRejectedReplay{}
