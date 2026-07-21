package trusthandler

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/localca"
)

var (
	errTrustHandlerMutation    = errors.New("trust mutation failed")
	errTrustHandlerCAS         = errors.New("trust observation changed")
	errTrustHandlerUnavailable = errors.New("trust observation unavailable")
)

// TestHandlerAppliesObservationBoundTrustOperations covers exact ensure and release evidence conversion.
func TestHandlerAppliesObservationBoundTrustOperations(t *testing.T) {
	request, policy, root := trustHandlerTestRequest(t)
	exact := trustHandlerExactObservation(request)
	absent := trust.Observation{Request: request, Complete: true}

	tests := []struct {
		name          string
		operation     helper.Operation
		before        trust.Observation
		after         trust.Observation
		postcondition helper.TrustPostcondition
	}{
		{
			name:          "ensure",
			operation:     helper.OperationEnsureTrust,
			before:        absent,
			after:         exact,
			postcondition: helper.TrustPostconditionExact,
		},
		{
			name:          "release",
			operation:     helper.OperationReleaseTrust,
			before:        exact,
			after:         absent,
			postcondition: helper.TrustPostconditionOwnedAbsent,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeFingerprint := trustHandlerFingerprint(t, test.before)
			adapter := &testConditionalAdapter{
				change: trust.Change{
					Attempted: true,
					Changed:   true,
					Before:    test.before,
					After:     test.after,
				},
			}
			handler := newHandler(adapter)
			ticket := trustHandlerTestTicket(t, policy, root, test.operation, beforeFingerprint)

			var evidence helper.TrustMutationEvidence
			var err error
			if test.operation == helper.OperationEnsureTrust {
				evidence, err = handler.EnsureTrust(t.Context(), ticket)
			} else {
				evidence, err = handler.ReleaseTrust(t.Context(), ticket)
			}
			if err != nil {
				t.Fatalf("trust handler error = %v", err)
			}
			afterFingerprint := trustHandlerFingerprint(t, test.after)
			if !evidence.Changed || evidence.AuthorityFingerprint != request.AuthorityFingerprint() ||
				evidence.Mechanism != request.Mechanism() || evidence.ObservationFingerprint != afterFingerprint ||
				evidence.Postcondition != test.postcondition {
				t.Fatalf("trust evidence = %#v", evidence)
			}
			if adapter.operation != test.operation || !sameRequest(adapter.request, request) || adapter.expected != beforeFingerprint {
				t.Fatalf("trust adapter call = operation %q, request %#v, expected %q", adapter.operation, adapter.request, adapter.expected)
			}
		})
	}
}

// TestHandlerPreservesPreexistingUnownedRoot accepts the adapter's safe no-op without claiming Harbor ownership.
func TestHandlerPreservesPreexistingUnownedRoot(t *testing.T) {
	request, policy, root := trustHandlerTestRequest(t)
	preexisting := trustHandlerPreexistingObservation(request)
	fingerprint := trustHandlerFingerprint(t, preexisting)
	adapter := &testConditionalAdapter{change: trust.Change{Before: preexisting, After: preexisting}}
	ticket := trustHandlerTestTicket(t, policy, root, helper.OperationEnsureTrust, fingerprint)

	evidence, err := newHandler(adapter).EnsureTrust(t.Context(), ticket)
	if err != nil {
		t.Fatalf("EnsureTrust() error = %v", err)
	}
	if evidence.Changed || evidence.Postcondition != helper.TrustPostconditionPreexisting ||
		evidence.ObservationFingerprint != fingerprint {
		t.Fatalf("pre-existing trust evidence = %#v", evidence)
	}
}

// TestHandlerPreservesForeignRootOnRelease proves release evidence does not require removing unowned trust.
func TestHandlerPreservesForeignRootOnRelease(t *testing.T) {
	request, policy, root := trustHandlerTestRequest(t)
	preexisting := trustHandlerPreexistingObservation(request)
	fingerprint := trustHandlerFingerprint(t, preexisting)
	adapter := &testConditionalAdapter{change: trust.Change{Before: preexisting, After: preexisting}}
	ticket := trustHandlerTestTicket(t, policy, root, helper.OperationReleaseTrust, fingerprint)

	evidence, err := newHandler(adapter).ReleaseTrust(t.Context(), ticket)
	if err != nil {
		t.Fatalf("ReleaseTrust() error = %v", err)
	}
	if evidence.Changed || evidence.Postcondition != helper.TrustPostconditionOwnedAbsent ||
		evidence.ObservationFingerprint != fingerprint {
		t.Fatalf("foreign release evidence = %#v", evidence)
	}
}

// TestHandlerRejectsMismatchedTrustAuthority keeps native calls bound to every signed request dimension.
func TestHandlerRejectsMismatchedTrustAuthority(t *testing.T) {
	request, policy, root := trustHandlerTestRequest(t)
	absent := trust.Observation{Request: request, Complete: true}
	fingerprint := trustHandlerFingerprint(t, absent)
	tests := []struct {
		name   string
		mutate func(*helper.Ticket)
	}{
		{name: "wrong operation", mutate: func(ticket *helper.Ticket) { ticket.Operation = helper.OperationReleaseTrust }},
		{name: "identity ownership", mutate: func(ticket *helper.Ticket) { ticket.OwnershipSchemaVersion = ownership.IdentitySchemaVersion }},
		{name: "missing policy", mutate: func(ticket *helper.Ticket) { ticket.NetworkPolicy = nil }},
		{name: "policy mismatch", mutate: func(ticket *helper.Ticket) { ticket.NetworkPolicyFingerprint = strings.Repeat("f", 64) }},
		{name: "missing root", mutate: func(ticket *helper.Ticket) { ticket.TrustRoot = nil }},
		{name: "root mismatch", mutate: func(ticket *helper.Ticket) { ticket.TrustRoot.Fingerprint = strings.Repeat("f", 64) }},
		{name: "malformed root", mutate: func(ticket *helper.Ticket) { ticket.TrustRoot.CertificatePEM = []byte("not a certificate") }},
		{name: "missing observation", mutate: func(ticket *helper.Ticket) { ticket.ExpectedTrustObservation = nil }},
		{name: "invalid observation", mutate: func(ticket *helper.Ticket) { ticket.ExpectedTrustObservation.Fingerprint = "bad" }},
		{name: "loopback authority", mutate: func(ticket *helper.Ticket) { ticket.ApprovedAddress = "127.77.0.10" }},
		{name: "resolver authority", mutate: func(ticket *helper.Ticket) {
			ticket.ExpectedResolverObservation = &helper.ExpectedResolverObservation{Fingerprint: strings.Repeat("a", 64)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ticket := trustHandlerTestTicket(t, policy, root, helper.OperationEnsureTrust, fingerprint)
			test.mutate(&ticket)
			adapter := &testConditionalAdapter{}
			if _, err := newHandler(adapter).EnsureTrust(t.Context(), ticket); err == nil {
				t.Fatal("EnsureTrust() accepted invalid ticket")
			}
			if adapter.operation != "" {
				t.Fatalf("trust adapter operation = %q, want no call", adapter.operation)
			}
		})
	}
}

// TestHandlerRejectsMismatchedPostconditionRequest keeps backend evidence bound to the ticket's reconstructed request.
func TestHandlerRejectsMismatchedPostconditionRequest(t *testing.T) {
	request, policy, root := trustHandlerTestRequest(t)
	absent := trust.Observation{Request: request, Complete: true}
	fingerprint := trustHandlerFingerprint(t, absent)
	otherRequest, err := trust.NewRequest("different-installation", request.Mechanism(), root)
	if err != nil {
		t.Fatalf("trust.NewRequest() mismatch fixture error = %v", err)
	}
	adapter := &testConditionalAdapter{change: trust.Change{
		Before: absent,
		After:  trust.Observation{Request: otherRequest, Complete: true},
	}}
	ticket := trustHandlerTestTicket(t, policy, root, helper.OperationEnsureTrust, fingerprint)
	if _, err := newHandler(adapter).EnsureTrust(t.Context(), ticket); err == nil {
		t.Fatal("EnsureTrust() accepted a postcondition for another request")
	}
}

// TestHandlerRejectsUnverifiedTrustPostconditions covers indeterminate and wrong-state adapter outcomes.
func TestHandlerRejectsUnverifiedTrustPostconditions(t *testing.T) {
	request, policy, root := trustHandlerTestRequest(t)
	absent := trust.Observation{Request: request, Complete: true}
	exact := trustHandlerExactObservation(request)
	preexisting := trustHandlerPreexistingObservation(request)
	tests := []struct {
		name      string
		operation helper.Operation
		change    trust.Change
	}{
		{
			name:      "indeterminate ensure",
			operation: helper.OperationEnsureTrust,
			change:    trust.Change{Indeterminate: true, Before: absent},
		},
		{
			name:      "ensure remains absent",
			operation: helper.OperationEnsureTrust,
			change:    trust.Change{Before: absent, After: absent},
		},
		{
			name:      "ensure conflicts",
			operation: helper.OperationEnsureTrust,
			change: trust.Change{Before: preexisting, After: trust.Observation{Request: request, Complete: true, Entries: []trust.Entry{
				{Mechanism: request.Mechanism(), NativeID: "foreign", CertificateFingerprint: request.AuthorityFingerprint(), NativeExact: false, NativeAttributesSHA256: strings.Repeat("b", 64)},
			}}},
		},
		{
			name:      "release remains owned",
			operation: helper.OperationReleaseTrust,
			change:    trust.Change{Before: exact, After: exact},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.change.Before
			beforeFingerprint := trustHandlerFingerprint(t, before)
			adapter := &testConditionalAdapter{change: test.change}
			ticket := trustHandlerTestTicket(t, policy, root, test.operation, beforeFingerprint)
			var err error
			if test.operation == helper.OperationEnsureTrust {
				_, err = newHandler(adapter).EnsureTrust(t.Context(), ticket)
			} else {
				_, err = newHandler(adapter).ReleaseTrust(t.Context(), ticket)
			}
			if err == nil {
				t.Fatal("trust handler accepted an unverified postcondition")
			}
		})
	}
}

// TestHandlerPropagatesConditionalAdapterFailures preserves bounded adapter diagnostics and mutation causes.
func TestHandlerPropagatesConditionalAdapterFailures(t *testing.T) {
	request, policy, root := trustHandlerTestRequest(t)
	absent := trust.Observation{Request: request, Complete: true}
	fingerprint := trustHandlerFingerprint(t, absent)
	tests := []struct {
		name  string
		cause error
	}{
		{name: "mutation", cause: errTrustHandlerMutation},
		{name: "CAS drift", cause: errTrustHandlerCAS},
		{name: "indeterminate", cause: errTrustHandlerUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &testConditionalAdapter{err: test.cause}
			ticket := trustHandlerTestTicket(t, policy, root, helper.OperationEnsureTrust, fingerprint)
			_, err := newHandler(adapter).EnsureTrust(t.Context(), ticket)
			if !errors.Is(err, test.cause) {
				t.Fatalf("EnsureTrust() error = %v, want %v", err, test.cause)
			}
		})
	}
}

// TestHandlerRejectsNilDependencies keeps the helper boundary fail-fast for missing platform authority.
func TestHandlerRejectsNilDependencies(t *testing.T) {
	for name, construct := range map[string]func(){
		"New":        func() { _ = New(nil) },
		"newHandler": func() { _ = newHandler(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("constructor accepted a nil adapter")
				}
			}()
			construct()
		})
	}
}

// testConditionalAdapter records one conditional trust operation and returns configured evidence.
type testConditionalAdapter struct {
	change    trust.Change
	err       error
	operation helper.Operation
	request   trust.Request
	expected  string
}

// Observe returns the configured before observation for interface completeness.
func (adapter *testConditionalAdapter) Observe(_ context.Context, _ trust.Request) (trust.Observation, error) {
	return adapter.change.Before, adapter.err
}

// EnsureIfObserved records one ensure call and returns the configured outcome.
func (adapter *testConditionalAdapter) EnsureIfObserved(
	_ context.Context,
	request trust.Request,
	expected string,
) (trust.Change, error) {
	adapter.operation = helper.OperationEnsureTrust
	adapter.request = request
	adapter.expected = expected
	return adapter.change, adapter.err
}

// ReleaseIfObserved records one release call and returns the configured outcome.
func (adapter *testConditionalAdapter) ReleaseIfObserved(
	_ context.Context,
	request trust.Request,
	expected string,
) (trust.Change, error) {
	adapter.operation = helper.OperationReleaseTrust
	adapter.request = request
	adapter.expected = expected
	return adapter.change, adapter.err
}

// trustHandlerTestRequest constructs one exact Darwin trust authority and its matching host policy.
func trustHandlerTestRequest(t *testing.T) (trust.Request, networkpolicy.Policy, certificates.Root) {
	t.Helper()
	root := trustHandlerTestRoot(t)
	localhost := netip.MustParseAddr("127.0.0.1")
	dns := netip.AddrPortFrom(localhost, 25000)
	policy, err := networkpolicy.New(
		root.Fingerprint,
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
	request, err := trust.NewRequest("trust-handler-test", policy.Mechanisms.Trust, root)
	if err != nil {
		t.Fatalf("trust.NewRequest() fixture error = %v", err)
	}
	return request, policy, root
}

// trustHandlerTestRoot creates one deterministic public CA fixture without retaining private material.
func trustHandlerTestRoot(t *testing.T) certificates.Root {
	t.Helper()
	clock := time.Date(2032, time.March, 4, 12, 0, 0, 0, time.UTC)
	authority, err := localca.New(localca.Config{
		CAValidity:   24 * time.Hour,
		LeafValidity: time.Hour,
		Backdate:     time.Minute,
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	material := authority.Material()
	return certificates.Root{
		CertificatePEM: material.CertificatePEM,
		Fingerprint:    material.Fingerprint,
		NotBefore:      material.NotBefore,
		NotAfter:       material.NotAfter,
	}
}

// trustHandlerTestTicket constructs one minimal policy-bound trust authority.
func trustHandlerTestTicket(
	t *testing.T,
	policy networkpolicy.Policy,
	root certificates.Root,
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
		InstallationID:           "trust-handler-test",
		RequesterIdentity:        "501",
		OwnershipGeneration:      7,
		OwnershipSchemaVersion:   ownership.NetworkPolicySchemaVersion,
		NetworkPolicyFingerprint: fingerprint,
		NetworkPolicy:            &policy,
		ApprovedPool:             "127.77.0.0/24",
		TrustRoot: &helper.TrustRoot{
			CertificatePEM: append([]byte(nil), root.CertificatePEM...),
			Fingerprint:    root.Fingerprint,
			NotBefore:      root.NotBefore,
			NotAfter:       root.NotAfter,
		},
		ExpectedTrustObservation: &helper.ExpectedTrustObservation{Fingerprint: expected},
	}
}

// trustHandlerExactObservation returns one exact owned trust entry for the request.
func trustHandlerExactObservation(request trust.Request) trust.Observation {
	owner := request.OwnerMarker()
	return trust.Observation{
		Request:  request,
		Complete: true,
		Entries: []trust.Entry{{
			Mechanism:              request.Mechanism(),
			NativeID:               "owned-native",
			CertificateFingerprint: request.AuthorityFingerprint(),
			NativeExact:            true,
			NativeAttributesSHA256: strings.Repeat("b", 64),
			Owner:                  &owner,
		}},
	}
}

// trustHandlerPreexistingObservation returns one exact but explicitly unowned root that must be preserved.
func trustHandlerPreexistingObservation(request trust.Request) trust.Observation {
	entry := trustHandlerExactObservation(request).Entries[0]
	entry.NativeID = "preexisting-native"
	entry.Owner = nil
	unrelated := entry
	unrelated.NativeID = "unrelated-native"
	unrelated.CertificateFingerprint = strings.Repeat("c", 64)
	return trust.Observation{Request: request, Complete: true, Entries: []trust.Entry{entry, unrelated}}
}

// trustHandlerFingerprint returns canonical CAS evidence for one test observation.
func trustHandlerFingerprint(t *testing.T, observation trust.Observation) string {
	t.Helper()
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("trust observation fingerprint error = %v", err)
	}
	return fingerprint
}

var _ conditionalAdapter = (*testConditionalAdapter)(nil)
