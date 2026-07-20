package resolverhandler

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

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
			handler := newHandler(adapter)
			ticket := resolverHandlerTestTicket(t, policy, test.operation, beforeFingerprint)

			var evidence helper.ResolverMutationEvidence
			if test.operation == helper.OperationEnsureResolver {
				evidence, err = handler.EnsureResolver(t.Context(), ticket)
			} else {
				evidence, err = handler.ReleaseResolver(t.Context(), ticket)
			}
			if err != nil {
				t.Fatalf("resolver handler error = %v", err)
			}
			afterFingerprint, err := test.after.Fingerprint()
			if err != nil {
				t.Fatalf("Observation.Fingerprint() after error = %v", err)
			}
			if !evidence.Changed || evidence.PolicyFingerprint != request.PolicyFingerprint() ||
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
			test.mutate(&ticket)
			adapter := &testConditionalAdapter{}
			if _, err := newHandler(adapter).EnsureResolver(t.Context(), ticket); err == nil {
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
			handler := newHandler(adapter)
			ticket := resolverHandlerTestTicket(t, policy, test.operation, test.expected)
			var err error
			if test.operation == helper.OperationEnsureResolver {
				_, err = handler.EnsureResolver(t.Context(), ticket)
			} else {
				_, err = handler.ReleaseResolver(t.Context(), ticket)
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
	_, err = newHandler(adapter).EnsureResolver(
		t.Context(),
		resolverHandlerTestTicket(t, policy, helper.OperationEnsureResolver, fingerprint),
	)
	if !errors.Is(err, cause) {
		t.Fatalf("EnsureResolver() error = %v, want %v", err, cause)
	}
}

// TestNewRequiresResolverAdapter keeps production composition fail-fast.
func TestNewRequiresResolverAdapter(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New() accepted a nil resolver adapter")
		}
	}()
	New(nil)
}

// testConditionalAdapter records one resolver operation and returns configured evidence.
type testConditionalAdapter struct {
	change    resolver.Change
	err       error
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
	adapter.operation = helper.OperationEnsureResolver
	adapter.request = request
	adapter.expected = expected
	return adapter.change, adapter.err
}

// ReleaseIfObserved records one release call and returns the configured outcome.
func (adapter *testConditionalAdapter) ReleaseIfObserved(
	_ context.Context,
	request resolver.Request,
	expected string,
) (resolver.Change, error) {
	adapter.operation = helper.OperationReleaseResolver
	adapter.request = request
	adapter.expected = expected
	return adapter.change, adapter.err
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
		OwnershipSchemaVersion:   ownership.NetworkPolicySchemaVersion,
		NetworkPolicyFingerprint: fingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		ExpectedResolverObservation: &helper.ExpectedResolverObservation{
			Fingerprint: expected,
		},
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
