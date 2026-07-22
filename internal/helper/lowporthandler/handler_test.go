package lowporthandler

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
	"github.com/goforj/harbor/internal/platform/lowport"
)

// handlerFixture holds one valid signed low-port authority and its exact native request.
type handlerFixture struct {
	ticket    helper.Ticket
	admission helper.TicketAdmission
	request   lowport.Request
	record    ownership.Record
}

// testAdapter records the exact request and compare-and-swap fingerprint presented by the handler.
type testAdapter struct {
	operation string
	request   lowport.Request
	expected  string
	change    lowport.Change
	err       error
	calls     int
}

// EnsureIfObserved records one conditional ensure call.
func (adapter *testAdapter) EnsureIfObserved(_ context.Context, request lowport.Request, expected string) (lowport.Change, error) {
	adapter.calls++
	adapter.operation = "ensure"
	adapter.request = request
	adapter.expected = expected
	return adapter.change, adapter.err
}

// ReleaseIfObserved records one conditional release call.
func (adapter *testAdapter) ReleaseIfObserved(_ context.Context, request lowport.Request, expected string) (lowport.Change, error) {
	adapter.calls++
	adapter.operation = "release"
	adapter.request = request
	adapter.expected = expected
	return adapter.change, adapter.err
}

// TestHandlerReturnsExactBoundedEvidence covers successful mutation and no-op postconditions for both operations.
func TestHandlerReturnsExactBoundedEvidence(t *testing.T) {
	tests := []struct {
		name          string
		operation     helper.Operation
		before        lowport.Observation
		after         lowport.Observation
		attempted     bool
		changed       bool
		wantOperation string
		wantResult    helper.LowPortPostcondition
	}{
		{name: "ensure mutation", operation: helper.OperationEnsureLowPorts, before: observationAbsent(), after: observationExact(), attempted: true, changed: true, wantOperation: "ensure", wantResult: helper.LowPortPostconditionExact},
		{name: "ensure repair", operation: helper.OperationEnsureLowPorts, before: observationDrifted(), after: observationExact(), attempted: true, changed: true, wantOperation: "ensure", wantResult: helper.LowPortPostconditionExact},
		{name: "ensure no-op", operation: helper.OperationEnsureLowPorts, before: observationExact(), after: observationExact(), wantOperation: "ensure", wantResult: helper.LowPortPostconditionExact},
		{name: "release mutation", operation: helper.OperationReleaseLowPorts, before: observationExact(), after: observationAbsent(), attempted: true, changed: true, wantOperation: "release", wantResult: helper.LowPortPostconditionOwnedAbsent},
		{name: "release drifted", operation: helper.OperationReleaseLowPorts, before: observationDrifted(), after: observationAbsent(), attempted: true, changed: true, wantOperation: "release", wantResult: helper.LowPortPostconditionOwnedAbsent},
		{name: "release no-op", operation: helper.OperationReleaseLowPorts, before: observationAbsent(), after: observationAbsent(), wantOperation: "release", wantResult: helper.LowPortPostconditionOwnedAbsent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHandlerFixture(t, test.operation)
			before := correlateObservation(test.before, fixture.request)
			after := correlateObservation(test.after, fixture.request)
			expected, err := before.Fingerprint()
			if err != nil {
				t.Fatalf("Fingerprint() error = %v", err)
			}
			fixture.ticket.ExpectedLowPortObservation = &helper.ExpectedLowPortObservation{Fingerprint: expected}
			adapter := &testAdapter{change: lowport.Change{Attempted: test.attempted, Changed: test.changed, Before: before, After: after}}
			handler := newHandler(adapter)

			var evidence helper.LowPortMutationEvidence
			if test.operation == helper.OperationEnsureLowPorts {
				evidence, err = handler.EnsureLowPorts(context.Background(), fixture.ticket, fixture.admission)
			} else {
				evidence, err = handler.ReleaseLowPorts(context.Background(), fixture.ticket, fixture.admission)
			}
			if err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if adapter.calls != 1 || adapter.operation != test.wantOperation || adapter.request != fixture.request || adapter.expected != expected {
				t.Fatalf("adapter call = (%d, %q, %#v, %q), want (1, %q, %#v, %q)", adapter.calls, adapter.operation, adapter.request, adapter.expected, test.wantOperation, fixture.request, expected)
			}
			afterFingerprint, fingerprintErr := after.Fingerprint()
			if fingerprintErr != nil {
				t.Fatalf("after.Fingerprint() error = %v", fingerprintErr)
			}
			want := helper.LowPortMutationEvidence{
				Changed:                test.changed,
				PolicyFingerprint:      fixture.ticket.NetworkPolicyFingerprint,
				OwnershipFingerprint:   fixture.admission.TargetOwnershipFingerprint,
				ObservationFingerprint: afterFingerprint,
				Postcondition:          test.wantResult,
			}
			if evidence != want {
				t.Fatalf("evidence = %#v, want %#v", evidence, want)
			}
		})
	}
}

// TestHandlerRejectsInvalidOrCrossDomainAuthority exhausts ticket and protected-ownership checks before native mutation.
func TestHandlerRejectsInvalidOrCrossDomainAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *handlerFixture)
	}{
		{name: "wrong operation", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.Operation = helper.OperationReleaseLowPorts
		}},
		{name: "schema one", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.OwnershipSchemaVersion = ownership.IdentitySchemaVersion
		}},
		{name: "missing policy", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.ticket.NetworkPolicy = nil }},
		{name: "invalid policy", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.ticket.NetworkPolicy.Suffix = ".invalid" }},
		{name: "policy fingerprint mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.NetworkPolicyFingerprint = strings.Repeat("f", 64)
		}},
		{name: "missing expected observation", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.ticket.ExpectedLowPortObservation = nil }},
		{name: "invalid expected observation", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.ExpectedLowPortObservation.Fingerprint = "bad"
		}},
		{name: "approved address authority", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.ticket.ApprovedAddress = "127.0.0.2" }},
		{name: "loopback observation authority", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.ExpectedObservation = helper.ExpectedObservation{State: helper.ObservationAbsent, Fingerprint: strings.Repeat("a", 64)}
		}},
		{name: "pre-assignment authority", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.ExpectedPreAssignment = &helper.ExpectedPreAssignment{Fingerprint: strings.Repeat("a", 64), Requirements: []helper.SocketRequirement{}}
		}},
		{name: "pool authority", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.ExpectedLoopbackPool = &helper.ExpectedLoopbackPool{Identities: []helper.ExpectedLoopbackIdentity{}}
		}},
		{name: "resolver authority", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.ExpectedResolverObservation = &helper.ExpectedResolverObservation{Fingerprint: strings.Repeat("a", 64)}
		}},
		{name: "trust root authority", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.ticket.TrustRoot = &helper.TrustRoot{} }},
		{name: "trust observation authority", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.ticket.ExpectedTrustObservation = &helper.ExpectedTrustObservation{Fingerprint: strings.Repeat("a", 64)}
		}},
		{name: "requester admission mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.admission.RequesterIdentity = "502" }},
		{name: "installation admission mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.admission.InstallationID = "other-installation" }},
		{name: "generation admission mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.admission.OwnershipGeneration++ }},
		{name: "schema admission mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.admission.OwnershipSchemaVersion = ownership.IdentitySchemaVersion
		}},
		{name: "policy admission mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.admission.NetworkPolicyFingerprint = strings.Repeat("f", 64)
		}},
		{name: "pool admission mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.admission.ApprovedPool = "127.1.0.0/29" }},
		{name: "schema transition", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.admission.OwnershipState = helper.OwnershipAdmissionSchema1To2
		}},
		{name: "invalid ownership state", mutate: func(_ *testing.T, fixture *handlerFixture) { fixture.admission.OwnershipState = "invalid" }},
		{name: "target ownership mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.admission.TargetOwnershipFingerprint = strings.Repeat("f", 64)
		}},
		{name: "current ownership mismatch", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.admission.OwnershipFingerprint = strings.Repeat("f", 64)
		}},
		{name: "invalid verifier key", mutate: func(_ *testing.T, fixture *handlerFixture) {
			fixture.admission.TicketVerifierKey = "bad"
			refreshOwnershipFingerprints(t, fixture)
		}},
		{name: "root owner", mutate: func(t *testing.T, fixture *handlerFixture) {
			fixture.ticket.RequesterIdentity = "0"
			fixture.admission.RequesterIdentity = "0"
			refreshOwnershipFingerprints(t, fixture)
		}},
		{name: "windows owner", mutate: func(t *testing.T, fixture *handlerFixture) {
			fixture.ticket.RequesterIdentity = "S-1-5-21"
			fixture.admission.RequesterIdentity = "S-1-5-21"
			refreshOwnershipFingerprints(t, fixture)
		}},
		{name: "unsupported platform mechanism", mutate: func(t *testing.T, fixture *handlerFixture) {
			policy := mustPolicy(t, networkpolicy.UbuntuMechanisms())
			fixture.ticket.NetworkPolicy = &policy
			fingerprint, err := policy.Fingerprint()
			if err != nil {
				t.Fatalf("policy.Fingerprint() error = %v", err)
			}
			fixture.ticket.NetworkPolicyFingerprint = fingerprint
			fixture.admission.NetworkPolicyFingerprint = fingerprint
			refreshOwnershipFingerprints(t, fixture)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHandlerFixture(t, helper.OperationEnsureLowPorts)
			test.mutate(t, &fixture)
			adapter := &testAdapter{}

			_, err := newHandler(adapter).EnsureLowPorts(context.Background(), fixture.ticket, fixture.admission)
			if err == nil {
				t.Fatal("EnsureLowPorts() error = nil, want rejection")
			}
			if adapter.calls != 0 {
				t.Fatalf("adapter calls = %d, want 0", adapter.calls)
			}
		})
	}
}

// TestHandlerCancellationAndAdapterErrorsFailClosed proves cancellation and native errors cannot produce evidence.
func TestHandlerCancellationAndAdapterErrorsFailClosed(t *testing.T) {
	for _, operation := range []helper.Operation{helper.OperationEnsureLowPorts, helper.OperationReleaseLowPorts} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newHandlerFixture(t, operation)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			adapter := &testAdapter{}
			handler := newHandler(adapter)
			var err error
			if operation == helper.OperationEnsureLowPorts {
				_, err = handler.EnsureLowPorts(ctx, fixture.ticket, fixture.admission)
			} else {
				_, err = handler.ReleaseLowPorts(ctx, fixture.ticket, fixture.admission)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("handler error = %v, want context.Canceled", err)
			}
			if adapter.calls != 0 {
				t.Fatalf("adapter calls = %d, want 0", adapter.calls)
			}

			wantErr := errors.New("native failure")
			adapter.err = wantErr
			if operation == helper.OperationEnsureLowPorts {
				_, err = handler.EnsureLowPorts(nil, fixture.ticket, fixture.admission)
			} else {
				_, err = handler.ReleaseLowPorts(nil, fixture.ticket, fixture.admission)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("handler nil-context error = %v, want %v", err, wantErr)
			}
			if adapter.calls != 1 {
				t.Fatalf("adapter calls = %d, want 1", adapter.calls)
			}
		})
	}
}

// TestEvidenceFromChangeRejectsUntrustedAdapterResults covers correlation, CAS flags, artifact invariants, and postconditions.
func TestEvidenceFromChangeRejectsUntrustedAdapterResults(t *testing.T) {
	fixture := newHandlerFixture(t, helper.OperationEnsureLowPorts)
	before := correlateObservation(observationAbsent(), fixture.request)
	after := correlateObservation(observationExact(), fixture.request)
	expected, err := before.Fingerprint()
	if err != nil {
		t.Fatalf("before.Fingerprint() error = %v", err)
	}
	valid := lowport.Change{Attempted: true, Changed: true, Before: before, After: after}
	other := newHandlerFixtureForOwner(t, helper.OperationEnsureLowPorts, "502")

	tests := []struct {
		name      string
		operation helper.Operation
		expected  string
		mutate    func(*lowport.Change)
	}{
		{name: "indeterminate flag", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.Indeterminate = true }},
		{name: "invalid before", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.Before = lowport.Observation{} }},
		{name: "foreign before request", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.Before.Request = other.request }},
		{name: "incomplete before", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.Before = correlateObservation(observationIncomplete(), fixture.request)
		}},
		{name: "foreign before", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.Before = correlateObservation(observationForeign(), fixture.request)
		}},
		{name: "ambiguous before", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.Before = correlateObservation(observationAmbiguous(), fixture.request)
		}},
		{name: "stale signed observation", operation: helper.OperationEnsureLowPorts, expected: strings.Repeat("f", 64)},
		{name: "invalid after", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.After = lowport.Observation{} }},
		{name: "foreign after request", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.After.Request = other.request }},
		{name: "changed flag false", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.Changed = false }},
		{name: "changed without attempted", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.Attempted = false }},
		{name: "uppercase artifact fingerprint", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.After.Artifacts[0].Fingerprint = strings.Repeat("A", 64) }},
		{name: "absent artifact owned", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.After.Artifacts[0].Present = false }},
		{name: "exact artifact foreign", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) { change.After.Artifacts[0].Owned = false }},
		{name: "ensure absent", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationAbsent(), fixture.request)
			change.Changed = false
			change.Attempted = false
		}},
		{name: "ensure drifted", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationDrifted(), fixture.request)
		}},
		{name: "ensure foreign", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationForeign(), fixture.request)
		}},
		{name: "ensure ambiguous", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationAmbiguous(), fixture.request)
		}},
		{name: "ensure incomplete", operation: helper.OperationEnsureLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationIncomplete(), fixture.request)
		}},
		{name: "release exact", operation: helper.OperationReleaseLowPorts, expected: expected},
		{name: "release drifted", operation: helper.OperationReleaseLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationDrifted(), fixture.request)
		}},
		{name: "release foreign", operation: helper.OperationReleaseLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationForeign(), fixture.request)
		}},
		{name: "release ambiguous", operation: helper.OperationReleaseLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationAmbiguous(), fixture.request)
		}},
		{name: "release incomplete", operation: helper.OperationReleaseLowPorts, expected: expected, mutate: func(change *lowport.Change) {
			change.After = correlateObservation(observationIncomplete(), fixture.request)
		}},
		{name: "unsupported operation", operation: helper.OperationEnsureTrust, expected: expected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			change := cloneChange(valid)
			if test.mutate != nil {
				test.mutate(&change)
			}
			if _, err := evidenceFromChange(test.operation, fixture.admission.TargetOwnershipFingerprint, fixture.request, test.expected, change); err == nil {
				t.Fatal("evidenceFromChange() error = nil, want rejection")
			}
		})
	}
}

// TestConstructorsAndClose pin fail-fast wiring and the adapter's resource-free lifecycle.
func TestConstructorsAndClose(t *testing.T) {
	assertPanics(t, func() { New(nil) })
	assertPanics(t, func() { newHandler(nil) })
	var nilHandler *Handler
	if err := nilHandler.Close(); err != nil {
		t.Fatalf("nil Handler.Close() error = %v", err)
	}
	if err := newHandler(&testAdapter{}).Close(); err != nil {
		t.Fatalf("Handler.Close() error = %v", err)
	}
}

// newHandlerFixture constructs one valid schema-2 authority for a non-root Darwin UID.
func newHandlerFixture(t *testing.T, operation helper.Operation) handlerFixture {
	return newHandlerFixtureForOwner(t, operation, "501")
}

// newHandlerFixtureForOwner constructs a fixture whose owner can distinguish request correlation.
func newHandlerFixtureForOwner(t *testing.T, operation helper.Operation, owner string) handlerFixture {
	t.Helper()
	policy := mustPolicy(t, networkpolicy.MacOSMechanisms())
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("policy.Fingerprint() error = %v", err)
	}
	public := make(ed25519.PublicKey, ed25519.PublicKeySize)
	for index := range public {
		public[index] = byte(index + 1)
	}
	record := ownership.Record{
		SchemaVersion:            ownership.NetworkPolicySchemaVersion,
		InstallationID:           "harbor-test-installation",
		OwnerIdentity:            owner,
		Generation:               7,
		LoopbackPoolPrefix:       "127.77.0.0/29",
		NetworkPolicyFingerprint: policyFingerprint,
		TicketVerifierKey:        base64.StdEncoding.EncodeToString(public),
	}
	ownershipFingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("record.Fingerprint() error = %v", err)
	}
	request, err := lowport.NewRequest(record, policy)
	if err != nil {
		t.Fatalf("lowport.NewRequest() error = %v", err)
	}
	absent := correlateObservation(observationAbsent(), request)
	expected, err := absent.Fingerprint()
	if err != nil {
		t.Fatalf("absent.Fingerprint() error = %v", err)
	}
	ticket := helper.Ticket{
		Version:                    helper.ProtocolVersion,
		Operation:                  operation,
		InstallationID:             record.InstallationID,
		RequesterIdentity:          record.OwnerIdentity,
		OwnershipGeneration:        record.Generation,
		OwnershipSchemaVersion:     record.SchemaVersion,
		NetworkPolicyFingerprint:   policyFingerprint,
		NetworkPolicy:              &policy,
		ApprovedPool:               record.LoopbackPoolPrefix,
		ExpectedLowPortObservation: &helper.ExpectedLowPortObservation{Fingerprint: expected},
		Nonce:                      strings.Repeat("n", 32),
		ExpiresAt:                  time.Now().UTC().Add(time.Minute),
	}
	admission := helper.TicketAdmission{
		RequesterIdentity:          record.OwnerIdentity,
		InstallationID:             record.InstallationID,
		OwnershipGeneration:        record.Generation,
		OwnershipSchemaVersion:     record.SchemaVersion,
		NetworkPolicyFingerprint:   policyFingerprint,
		ApprovedPool:               record.LoopbackPoolPrefix,
		OwnershipState:             helper.OwnershipAdmissionAlreadyCurrent,
		OwnershipFingerprint:       ownershipFingerprint,
		TargetOwnershipFingerprint: ownershipFingerprint,
		TicketVerifierKey:          record.TicketVerifierKey,
	}
	return handlerFixture{ticket: ticket, admission: admission, request: request, record: record}
}

// refreshOwnershipFingerprints updates protected evidence after a test intentionally changes target dimensions.
func refreshOwnershipFingerprints(t *testing.T, fixture *handlerFixture) {
	t.Helper()
	record := ownership.Record{
		SchemaVersion:            fixture.ticket.OwnershipSchemaVersion,
		InstallationID:           fixture.ticket.InstallationID,
		OwnerIdentity:            fixture.ticket.RequesterIdentity,
		Generation:               fixture.ticket.OwnershipGeneration,
		LoopbackPoolPrefix:       fixture.ticket.ApprovedPool,
		NetworkPolicyFingerprint: fixture.ticket.NetworkPolicyFingerprint,
		TicketVerifierKey:        fixture.admission.TicketVerifierKey,
	}
	fingerprint, err := record.Fingerprint()
	if err != nil {
		fixture.admission.OwnershipFingerprint = strings.Repeat("e", 64)
		fixture.admission.TargetOwnershipFingerprint = strings.Repeat("e", 64)
		return
	}
	fixture.record = record
	fixture.admission.OwnershipFingerprint = fingerprint
	fixture.admission.TargetOwnershipFingerprint = fingerprint
}

// mustPolicy returns one valid policy with distinct high-port upstreams.
func mustPolicy(t *testing.T, mechanisms networkpolicy.Mechanisms) networkpolicy.Policy {
	t.Helper()
	loopback := netip.MustParseAddr("127.0.0.1")
	policy, err := networkpolicy.New(
		strings.Repeat("a", 64),
		mechanisms,
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 21053), Bind: netip.AddrPortFrom(loopback, 21053)},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 80), Bind: netip.AddrPortFrom(loopback, 21080)},
		networkpolicy.Listener{Advertised: netip.AddrPortFrom(loopback, 443), Bind: netip.AddrPortFrom(loopback, 21443)},
	)
	if err != nil {
		t.Fatalf("networkpolicy.New() error = %v", err)
	}
	return policy
}

// correlateObservation binds an observation template to the immutable request under test.
func correlateObservation(observation lowport.Observation, request lowport.Request) lowport.Observation {
	observation.Request = request
	return observation
}

// observationAbsent returns a complete observation with no service artifact.
func observationAbsent() lowport.Observation {
	return lowport.Observation{Complete: true, Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Fingerprint: strings.Repeat("1", 64)},
		{Kind: lowport.ArtifactKindService, Fingerprint: strings.Repeat("8", 64)},
	}}
}

// observationExact returns complete exact owned plist and service artifacts.
func observationExact() lowport.Observation {
	return lowport.Observation{Complete: true, Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("2", 64)},
		{Kind: lowport.ArtifactKindService, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("9", 64)},
	}}
}

// observationDrifted returns an exact plist whose matching loaded service has drifted.
func observationDrifted() lowport.Observation {
	return lowport.Observation{Complete: true, Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("2", 64)},
		{Kind: lowport.ArtifactKindService, Present: true, Owned: true, Fingerprint: strings.Repeat("3", 64)},
	}}
}

// observationForeign returns one complete artifact that is outside Harbor ownership.
func observationForeign() lowport.Observation {
	return lowport.Observation{Complete: true, Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Present: true, Fingerprint: strings.Repeat("4", 64)},
		{Kind: lowport.ArtifactKindService, Fingerprint: strings.Repeat("8", 64)},
	}}
}

// observationAmbiguous returns an explicitly ambiguous plist fact so mutation must fail closed.
func observationAmbiguous() lowport.Observation {
	return lowport.Observation{Complete: true, Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Present: true, Ambiguous: true, Fingerprint: strings.Repeat("5", 64)},
		{Kind: lowport.ArtifactKindService, Fingerprint: strings.Repeat("8", 64)},
	}}
}

// observationIncomplete returns a bounded observation whose native read did not complete.
func observationIncomplete() lowport.Observation {
	return lowport.Observation{Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Fingerprint: strings.Repeat("7", 64)},
		{Kind: lowport.ArtifactKindService, Fingerprint: strings.Repeat("8", 64)},
	}}
}

// cloneChange prevents table mutations from sharing artifact backing arrays.
func cloneChange(change lowport.Change) lowport.Change {
	change.Before.Artifacts = append([]lowport.Artifact(nil), change.Before.Artifacts...)
	change.After.Artifacts = append([]lowport.Artifact(nil), change.After.Artifacts...)
	return change
}

// assertPanics proves a constructor rejects missing mandatory wiring.
func assertPanics(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("call did not panic")
		}
	}()
	call()
}
