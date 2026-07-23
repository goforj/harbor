package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/host/ownershipreleaseproof"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/state"
)

// TestGlobalNetworkReleaseVerifyEffectsAdvancesFreshCompleteAbsence proves ownership remains exact
// while every released host effect is independently absent.
func TestGlobalNetworkReleaseVerifyEffectsAdvancesFreshCompleteAbsence(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	durable := fixture.journal.plan

	advanced, err := fixture.coordinator.verifyReleaseEffects(t.Context(), durable)
	if err != nil {
		t.Fatalf("verifyReleaseEffects() error = %v", err)
	}
	if advanced.Phase != state.GlobalNetworkReleasePlanPhaseOwnership || advanced.EffectsReceipt == nil {
		t.Fatalf("verifyReleaseEffects() = %#v, want ownership receipt", advanced)
	}
	if !sameEffectsRuntimeVerification(fixture, durable) {
		t.Fatalf(
			"runtime verification = (%d, %q, %d, %d), want exact effects checkpoint",
			fixture.runtimeRelease.verifyCalls,
			fixture.runtimeRelease.verifyOperation,
			fixture.runtimeRelease.verifyCheckpoint,
			fixture.runtimeRelease.verifyNetwork,
		)
	}
	if fixture.journal.effectsCalls != 1 {
		t.Fatalf("effects advances = %d, want 1", fixture.journal.effectsCalls)
	}
	want := state.AdvanceGlobalNetworkReleaseEffectsRequest{
		OperationID:        durable.Operation.Operation.ID,
		CheckpointRevision: durable.CheckpointRevision,
		NetworkRevision:    durable.NetworkRevision,
		Receipt: state.GlobalNetworkReleaseEffectsReceipt{
			SourceCheckpointRevision:        durable.CheckpointRevision,
			RuntimeObservationDigest:        fixture.runtimeRelease.verifyDigest,
			OwnershipObservationFingerprint: fixture.ownership.observation.Fingerprint,
			LowPortObservationFingerprint:   effectsLowPortFingerprint(t, fixture),
			ResolverObservationFingerprint:  effectsResolverFingerprint(t, fixture),
			TrustObservationFingerprint:     effectsTrustFingerprint(t, fixture),
			LoopbackObservationDigest:       effectsLoopbackDigest(t, fixture),
			VerifiedAt:                      fixture.clock.now,
		},
	}
	if !reflect.DeepEqual(fixture.journal.effectsRequest, want) {
		t.Fatalf("AdvanceGlobalNetworkReleaseEffects() request = %#v, want %#v", fixture.journal.effectsRequest, want)
	}
	if len(fixture.loopback.addresses) != len(durable.Authority.LoopbackTargets) {
		t.Fatalf("loopback observations = %d, want %d", len(fixture.loopback.addresses), len(durable.Authority.LoopbackTargets))
	}
	for index, target := range durable.Authority.LoopbackTargets {
		if fixture.loopback.addresses[index] != target.Address {
			t.Fatalf("loopback observation %d = %s, want %s", index, fixture.loopback.addresses[index], target.Address)
		}
	}
}

// TestGlobalNetworkReleaseConfirmOwnershipRejectsUnverifiedAndReplaysCommittedCheckpoint keeps projection retirement fenced to one owner-confirmed absence.
func TestGlobalNetworkReleaseConfirmOwnershipRejectsUnverifiedAndReplaysCommittedCheckpoint(t *testing.T) {
	for _, test := range []struct {
		name         string
		mutate       func(*globalNetworkReleaseEffectsFixture, *GlobalNetworkReleaseConfirmOwnershipRequest)
		wantTerminal bool
		wantNotFound bool
	}{
		{
			name: "stale checkpoint",
			mutate: func(_ *globalNetworkReleaseEffectsFixture, request *GlobalNetworkReleaseConfirmOwnershipRequest) {
				request.ExpectedCheckpointRevision++
			},
		},
		{
			name: "wrong requester",
			mutate: func(_ *globalNetworkReleaseEffectsFixture, request *GlobalNetworkReleaseConfirmOwnershipRequest) {
				request.RequesterIdentity = "different-owner"
			},
			wantNotFound: true,
		},
		{
			name: "missing plan",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture, _ *GlobalNetworkReleaseConfirmOwnershipRequest) {
				fixture.journal.found = false
			},
			wantNotFound: true,
		},
		{
			name: "ownership still present",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture, _ *GlobalNetworkReleaseConfirmOwnershipRequest) {
				fixture.protectedOwnership.observation.Exists = true
			},
		},
		{
			name: "ownership observation error",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture, _ *GlobalNetworkReleaseConfirmOwnershipRequest) {
				fixture.protectedOwnership.err = errors.New("ownership observation failed")
			},
		},
		{
			name: "projection present and protected ownership absent",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture, _ *GlobalNetworkReleaseConfirmOwnershipRequest) {
				fixture.protectedOwnership.observation.Exists = false
			},
			wantTerminal: true,
		},
		{
			name: "committed projection replay",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture, request *GlobalNetworkReleaseConfirmOwnershipRequest) {
				fixture.protectedOwnership.observation.Exists = false
				fixture.journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseProjection
				fixture.journal.plan.OwnershipReceipt = &state.GlobalNetworkReleaseOwnershipReceipt{
					SourceCheckpointRevision:     request.ExpectedCheckpointRevision,
					ReleasedOwnershipFingerprint: fixture.journal.plan.Authority.ExpectedOwnershipFingerprint,
					VerifiedAt:                   fixture.clock.now,
				}
			},
			wantTerminal: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseEffectsFixture(t)
			advanced, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan)
			if err != nil {
				t.Fatalf("verifyReleaseEffects() error = %v", err)
			}
			fixture.journal.plan = advanced
			request := GlobalNetworkReleaseConfirmOwnershipRequest{
				OperationID:                advanced.Operation.Operation.ID,
				ExpectedCheckpointRevision: advanced.CheckpointRevision,
				RequesterIdentity:          advanced.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
				OwnershipEvidence: helper.OwnershipMutationEvidence{
					ReleaseOperationID:           string(advanced.Operation.Operation.ID),
					ReleaseOperationRevision:     uint64(advanced.Operation.Revision),
					ReleaseCheckpointRevision:    uint64(advanced.CheckpointRevision),
					ReleasedOwnershipFingerprint: advanced.Authority.ExpectedOwnershipFingerprint,
					Postcondition:                helper.OwnershipPostconditionOwnedAbsent,
				},
			}
			fixture.coordinator.proofObserver = testGlobalNetworkReleaseProofObserver{}
			test.mutate(fixture, &request)
			if test.wantTerminal && !fixture.ownership.observation.Exists {
				t.Fatal("durable ownership projection disappeared before terminal finalization")
			}
			result, err := fixture.coordinator.ConfirmOwnership(t.Context(), request)
			if test.wantTerminal {
				if err != nil || result.Operation.Operation.State != "succeeded" || result.SourceCheckpointRevision != request.ExpectedCheckpointRevision {
					t.Fatalf("ConfirmOwnership() = %#v, %v", result, err)
				}
				if fixture.journal.finalizeCalls != 1 {
					t.Fatalf("projection finalizations = %d, want 1", fixture.journal.finalizeCalls)
				}
				return
			}
			if err == nil {
				t.Fatal("ConfirmOwnership() error = nil")
			}
			if test.wantNotFound {
				var missing *state.OperationNotFoundError
				if !errors.As(err, &missing) {
					t.Fatalf("ConfirmOwnership() error = %v, want not found", err)
				}
			}
		})
	}
}

// TestGlobalNetworkReleaseResumeRecoversHelperCompletedOwnership proves a lost confirmation cannot strand an already released machine.
func TestGlobalNetworkReleaseResumeRecoversHelperCompletedOwnership(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	advanced, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan)
	if err != nil {
		t.Fatalf("verifyReleaseEffects() error = %v", err)
	}
	fixture.journal.plan = advanced
	fixture.coordinator.proofObserver = testGlobalNetworkReleaseProofObserver{}
	fixture.protectedOwnership.observation.Exists = false
	if !fixture.ownership.observation.Exists {
		t.Fatal("durable ownership projection disappeared before recovery")
	}

	completed, err := fixture.coordinator.resume(
		t.Context(),
		advanced.Operation.Operation.ID,
		advanced.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
	)
	if err != nil {
		t.Fatalf("resume() error = %v", err)
	}
	if completed.Operation.State != domain.OperationSucceeded {
		t.Fatalf("resume() operation = %#v, want succeeded", completed)
	}
	if fixture.journal.ownershipCalls != 1 || fixture.journal.finalizeCalls != 1 {
		t.Fatalf(
			"ownership advances/finalizations = %d/%d, want 1/1",
			fixture.journal.ownershipCalls,
			fixture.journal.finalizeCalls,
		)
	}
	if fixture.journal.ownershipRequest.Receipt.ReleasedOwnershipFingerprint != advanced.Authority.ExpectedOwnershipFingerprint {
		t.Fatalf(
			"released ownership fingerprint = %q, want %q",
			fixture.journal.ownershipRequest.Receipt.ReleasedOwnershipFingerprint,
			advanced.Authority.ExpectedOwnershipFingerprint,
		)
	}
}

// TestGlobalNetworkReleaseResumeLeavesUnreleasedOwnershipAtApproval proves an ordinary checkpoint still waits for its helper.
func TestGlobalNetworkReleaseResumeLeavesUnreleasedOwnershipAtApproval(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	advanced, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan)
	if err != nil {
		t.Fatalf("verifyReleaseEffects() error = %v", err)
	}
	fixture.journal.plan = advanced
	fixture.coordinator.proofObserver = testGlobalNetworkReleaseAbsentProofObserver{}
	fixture.protectedOwnership.err = errors.New("ownership must not be observed without root proof")

	current, err := fixture.coordinator.resume(
		t.Context(),
		advanced.Operation.Operation.ID,
		advanced.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
	)
	if err != nil {
		t.Fatalf("resume() error = %v", err)
	}
	if current != advanced.Operation {
		t.Fatalf("resume() = %#v, want %#v", current, advanced.Operation)
	}
	if fixture.journal.ownershipCalls != 0 || fixture.journal.finalizeCalls != 0 {
		t.Fatalf(
			"ownership advances/finalizations = %d/%d, want 0/0",
			fixture.journal.ownershipCalls,
			fixture.journal.finalizeCalls,
		)
	}
}

// TestGlobalNetworkReleaseConfirmOwnershipReplaysOnlyItsTerminalFence preserves client retries after the projection transaction deletes its active plan.
func TestGlobalNetworkReleaseConfirmOwnershipReplaysOnlyItsTerminalFence(t *testing.T) {
	operation := testGlobalNetworkReleaseOperation(t)
	completed, err := operation.Operation.Transition(domain.OperationSucceeded, "network released", operation.Operation.RequestedAt.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	terminal := state.GlobalNetworkReleaseTerminalRecord{
		Operation: state.OperationRecord{
			Operation: completed,
			Revision:  operation.Revision + 3,
		},
		OwnerIdentity:                "501",
		ReleasedOwnershipFingerprint: strings.Repeat("a", 64),
		SourceCheckpointRevision:     8,
		NetworkRevision:              3,
	}
	for _, test := range []struct {
		name                string
		requester           string
		terminalFingerprint string
		evidenceFingerprint string
		wantNotFound        bool
	}{
		{
			name:                "exact replay",
			requester:           "501",
			terminalFingerprint: terminal.ReleasedOwnershipFingerprint,
			evidenceFingerprint: terminal.ReleasedOwnershipFingerprint,
		},
		{
			name:                "wrong owner",
			requester:           "different-owner",
			terminalFingerprint: terminal.ReleasedOwnershipFingerprint,
			evidenceFingerprint: terminal.ReleasedOwnershipFingerprint,
			wantNotFound:        true,
		},
		{
			name:                "wrong fingerprint",
			requester:           "501",
			terminalFingerprint: terminal.ReleasedOwnershipFingerprint,
			evidenceFingerprint: strings.Repeat("b", 64),
			wantNotFound:        true,
		},
		{
			name:                "legacy terminal",
			requester:           "501",
			evidenceFingerprint: strings.Repeat("b", 64),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testTerminal := terminal
			testTerminal.ReleasedOwnershipFingerprint = test.terminalFingerprint
			journal := &testGlobalNetworkReleaseJournal{
				terminal:      testTerminal,
				terminalFound: true,
			}
			coordinator := &GlobalNetworkReleaseCoordinator{
				journal: journal,
				ownershipProjection: testGlobalNetworkReleaseOwnershipObserver{
					err: errors.New("terminal replay must not reobserve mutable ownership"),
				},
			}
			coordinator.proofObserver = globalNetworkReleaseUnavailableOwnershipProofObserver{}
			result, confirmErr := coordinator.ConfirmOwnership(t.Context(), GlobalNetworkReleaseConfirmOwnershipRequest{
				OperationID:                operation.Operation.ID,
				ExpectedCheckpointRevision: terminal.SourceCheckpointRevision,
				RequesterIdentity:          test.requester,
				OwnershipEvidence: helper.OwnershipMutationEvidence{
					ReleaseOperationID:           string(operation.Operation.ID),
					ReleaseOperationRevision:     uint64(terminal.Operation.Revision),
					ReleaseCheckpointRevision:    uint64(terminal.SourceCheckpointRevision),
					ReleasedOwnershipFingerprint: test.evidenceFingerprint,
					Postcondition:                helper.OwnershipPostconditionOwnedAbsent,
				},
			})
			if test.wantNotFound {
				var missing *state.OperationNotFoundError
				if !errors.As(confirmErr, &missing) {
					t.Fatalf("ConfirmOwnership() error = %v, want not found", confirmErr)
				}
				return
			}
			if confirmErr != nil || result != testTerminal {
				t.Fatalf("ConfirmOwnership() = %#v, %v", result, confirmErr)
			}
		})
	}
}

// testGlobalNetworkReleaseOwnershipObserver supplies a fresh terminal ownership observation.
type testGlobalNetworkReleaseOwnershipObserver struct {
	observation ownership.Observation
	err         error
}

// Observe returns the configured terminal ownership observation.
func (observer testGlobalNetworkReleaseOwnershipObserver) Observe(context.Context) (ownership.Observation, error) {
	return observer.observation, observer.err
}

// testGlobalNetworkReleaseProofObserver confirms fixture-owned proof authority.
type testGlobalNetworkReleaseProofObserver struct{}

// ConfirmReleased supplies terminal proof for focused coordinator tests.
func (testGlobalNetworkReleaseProofObserver) ConfirmReleased(_ context.Context, authority ownershipreleaseproof.Authority) (ownershipreleaseproof.Proof, error) {
	return ownershipreleaseproof.Proof{
		TicketReferenceHash:        strings.Repeat("a", 64),
		NonceHash:                  strings.Repeat("b", 64),
		ReleaseOperationID:         authority.ReleaseOperationID,
		OperationRevision:          authority.OperationRevision,
		CheckpointRevision:         authority.CheckpointRevision,
		RequesterIdentity:          authority.RequesterIdentity,
		TargetOwnershipFingerprint: authority.TargetOwnershipFingerprint,
		State:                      ownershipreleaseproof.StateReleased,
		VerifiedAt:                 time.Now().UTC(),
	}, nil
}

// testGlobalNetworkReleaseAbsentProofObserver reports that the privileged helper has not released ownership.
type testGlobalNetworkReleaseAbsentProofObserver struct{}

// ConfirmReleased returns the sentinel that keeps an ordinary ownership checkpoint waiting for approval.
func (testGlobalNetworkReleaseAbsentProofObserver) ConfirmReleased(context.Context, ownershipreleaseproof.Authority) (ownershipreleaseproof.Proof, error) {
	return ownershipreleaseproof.Proof{}, ownershipreleaseproof.ErrAbsentProof
}

// TestGlobalNetworkReleaseVerifyEffectsPreservesForeignReleasedNamespaces proves released ownership
// markers may disappear without requiring foreign state removal.
func TestGlobalNetworkReleaseVerifyEffectsPreservesForeignReleasedNamespaces(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	fixture.resolver.observation = networkResolverSetupTestExactObservation(
		t,
		fixture.journal.plan.Authority.Projection.ConfirmedOwnership.Record,
		fixture.journal.plan.Authority.Policy,
	)
	rule := fixture.resolver.observation.Rules[0]
	rule.Owner = nil
	fixture.resolver.observation.Rules = []resolver.RuleFact{rule}
	request := fixture.trust.request
	fixture.trust.observation = &trust.Observation{
		Request:  request,
		Complete: true,
		Entries: []trust.Entry{{
			Mechanism:              request.Mechanism(),
			NativeID:               "foreign-root",
			CertificateFingerprint: strings.Repeat("f", 64),
			NativeExact:            true,
			NativeAttributesSHA256: strings.Repeat("e", 64),
		}},
	}
	if _, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan); err != nil {
		t.Fatalf("verifyReleaseEffects() error = %v", err)
	}
}

// TestGlobalNetworkReleaseVerifyEffectsPreservesExactPreexistingTrust proves an unowned public root remains byte-for-byte unchanged.
func TestGlobalNetworkReleaseVerifyEffectsPreservesExactPreexistingTrust(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	request := fixture.trust.request
	fixture.trust.observation = &trust.Observation{
		Request:  request,
		Complete: true,
		Entries: []trust.Entry{
			{
				Mechanism:              request.Mechanism(),
				NativeID:               "preexisting-root",
				CertificateFingerprint: request.AuthorityFingerprint(),
				NativeExact:            true,
				NativeAttributesSHA256: strings.Repeat("c", 64),
			},
		},
	}
	observation, err := fixture.trust.Observe(t.Context(), fixture.trust.request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fixture.journal.plan.Authority.TrustDisposition = state.GlobalNetworkReleaseTrustPreexistingUnowned
	fixture.journal.plan.Authority.TrustObservationFingerprint = fingerprint
	if _, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan); err != nil {
		t.Fatalf("verifyReleaseEffects() error = %v", err)
	}
}

// TestGlobalNetworkReleaseVerifyEffectsRejectsUnsafeFreshFacts proves every verification boundary
// fails closed before durable ownership advance.
func TestGlobalNetworkReleaseVerifyEffectsRejectsUnsafeFreshFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseEffectsFixture)
	}{
		{
			name: "runtime error",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.runtimeRelease.verifyErr = errors.New("runtime failed")
			},
		},
		{
			name: "ownership mismatch",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.ownership.observation.Exists = false
			},
		},
		{
			name: "low ports remain exact",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.low.observation.Artifacts[0].Present = true
				fixture.low.observation.Artifacts[0].Owned = true
				fixture.low.observation.Artifacts[0].Exact = true
			},
		},
		{
			name: "low ports indeterminate",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.low.observation.Complete = false
			},
		},
		{
			name: "resolver owned rule remains",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.resolver.observation = networkResolverSetupTestExactObservation(
					t,
					fixture.journal.plan.Authority.Projection.ConfirmedOwnership.Record,
					fixture.journal.plan.Authority.Policy,
				)
			},
		},
		{
			name: "resolver indeterminate",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.resolver.observation.Complete = false
			},
		},
		{
			name: "trust owned entry remains",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.trust.observation = nil
				fixture.trust.owned = true
			},
		},
		{
			name: "trust foreign same root remains",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				request := fixture.trust.request
				fixture.trust.observation = &trust.Observation{
					Request:  request,
					Complete: true,
					Entries: []trust.Entry{
						{
							Mechanism:              request.Mechanism(),
							NativeID:               "foreign-same-root",
							CertificateFingerprint: request.AuthorityFingerprint(),
							NativeExact:            true,
							NativeAttributesSHA256: strings.Repeat("c", 64),
						},
					},
				}
			},
		},
		{
			name: "loopback remains exact",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.loopback.state = loopback.StateExact
			},
		},
		{
			name: "loopback observation error",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.loopback.err = errors.New("loopback failed")
			},
		},
		{
			name: "advance error",
			mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
				fixture.journal.effectsErr = errors.New("advance failed")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseEffectsFixture(t)
			test.mutate(fixture)
			if _, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan); err == nil {
				t.Fatal("verifyReleaseEffects() error = nil")
			}
			if test.name != "advance error" && fixture.journal.effectsCalls != 0 {
				t.Fatalf("effects advances = %d, want zero", fixture.journal.effectsCalls)
			}
		})
	}
}

// TestGlobalNetworkReleaseVerifyEffectsCancellationAndOwnershipReplayAvoidNewObservations proves
// terminal replay is read-only and cancellation precedes every dependency.
func TestGlobalNetworkReleaseVerifyEffectsCancellationAndOwnershipReplayAvoidNewObservations(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.coordinator.verifyReleaseEffects(ctx, fixture.journal.plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("verifyReleaseEffects() error = %v, want context cancellation", err)
	}
	if fixture.runtimeRelease.verifyCalls != 0 || fixture.journal.effectsCalls != 0 {
		t.Fatalf("verification calls = %d, advances = %d, want zero", fixture.runtimeRelease.verifyCalls, fixture.journal.effectsCalls)
	}
	fixture = newGlobalNetworkReleaseEffectsFixture(t)
	fixture.journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseOwnership
	effectsCheckpoint := fixture.journal.plan.CheckpointRevision
	fixture.journal.plan.CheckpointRevision++
	fixture.journal.plan.EffectsReceipt = &state.GlobalNetworkReleaseEffectsReceipt{
		SourceCheckpointRevision:        effectsCheckpoint,
		RuntimeObservationDigest:        strings.Repeat("a", 64),
		OwnershipObservationFingerprint: fixture.journal.plan.Authority.ExpectedOwnershipFingerprint,
		LowPortObservationFingerprint:   strings.Repeat("c", 64),
		ResolverObservationFingerprint:  strings.Repeat("d", 64),
		TrustObservationFingerprint:     strings.Repeat("e", 64),
		LoopbackObservationDigest:       strings.Repeat("f", 64),
		VerifiedAt:                      fixture.clock.now,
	}
	if _, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan); err != nil {
		t.Fatalf("ownership replay error = %v", err)
	}
	if fixture.runtimeRelease.verifyCalls != 0 || fixture.journal.effectsCalls != 0 ||
		len(fixture.calls) != 0 {
		t.Fatalf(
			"ownership replay calls = runtime %d, advances %d, boundaries %#v; want zero",
			fixture.runtimeRelease.verifyCalls,
			fixture.journal.effectsCalls,
			fixture.calls,
		)
	}
	fixture.journal.plan.EffectsReceipt.OwnershipObservationFingerprint = strings.Repeat("0", 64)
	if _, err := fixture.coordinator.verifyReleaseEffects(t.Context(), fixture.journal.plan); err == nil {
		t.Fatal("ownership replay accepted an effects receipt for another ownership authority")
	}
}

// TestGlobalNetworkReleaseRecoverVerifyEffectsUsesFreshFencedVerification proves recovery advances
// only a newly verified effects checkpoint.
func TestGlobalNetworkReleaseRecoverVerifyEffectsUsesFreshFencedVerification(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	durable := fixture.journal.plan
	if err := fixture.coordinator.Recover(t.Context()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if !sameEffectsRuntimeVerification(fixture, durable) {
		t.Fatalf(
			"recovery runtime verification = (%d, %q, %d, %d), want exact effects checkpoint",
			fixture.runtimeRelease.verifyCalls,
			fixture.runtimeRelease.verifyOperation,
			fixture.runtimeRelease.verifyCheckpoint,
			fixture.runtimeRelease.verifyNetwork,
		)
	}
	if fixture.journal.effectsCalls != 1 ||
		fixture.journal.effectsRequest.CheckpointRevision != durable.CheckpointRevision ||
		fixture.journal.effectsRequest.NetworkRevision != durable.NetworkRevision {
		t.Fatalf("recovery effects advance = %#v, want exact durable fence", fixture.journal.effectsRequest)
	}
	for _, failure := range []struct {
		name   string
		mutate func(*globalNetworkReleaseEffectsFixture)
	}{
		{name: "runtime verification", mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
			fixture.runtimeRelease.verifyErr = errors.New("verify failed")
		}},
		{name: "effects advance", mutate: func(fixture *globalNetworkReleaseEffectsFixture) {
			fixture.journal.effectsErr = errors.New("advance failed")
		}},
	} {
		t.Run(failure.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseEffectsFixture(t)
			failure.mutate(fixture)
			if err := fixture.coordinator.Recover(t.Context()); err == nil {
				t.Fatal("Recover() error = nil")
			}
		})
	}
}

// TestGlobalNetworkReleaseResumeVerifyEffectsRetriesDaemonOwnedVerification proves an
// idempotent start retry resumes the approval-free effects checkpoint.
func TestGlobalNetworkReleaseResumeVerifyEffectsRetriesDaemonOwnedVerification(t *testing.T) {
	fixture := newGlobalNetworkReleaseEffectsFixture(t)
	durable := fixture.journal.plan
	operation, err := fixture.coordinator.resume(
		t.Context(),
		durable.Operation.Operation.ID,
		durable.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
	)
	if err != nil {
		t.Fatalf("resume() error = %v", err)
	}
	if !reflect.DeepEqual(operation, durable.Operation) {
		t.Fatalf("resume() = %#v, want %#v", operation, durable.Operation)
	}
	if !sameEffectsRuntimeVerification(fixture, durable) || fixture.journal.effectsCalls != 1 {
		t.Fatalf(
			"resume verification = runtime (%d, %q, %d, %d), effects advances %d",
			fixture.runtimeRelease.verifyCalls,
			fixture.runtimeRelease.verifyOperation,
			fixture.runtimeRelease.verifyCheckpoint,
			fixture.runtimeRelease.verifyNetwork,
			fixture.journal.effectsCalls,
		)
	}
}

// globalNetworkReleaseEffectsFixture prepares a fully released host-effect checkpoint with only ownership still retained.
type globalNetworkReleaseEffectsFixture struct {
	*globalNetworkReleaseStartFixture
}

// sameEffectsRuntimeVerification reports whether runtime verification used the exact durable fence.
func sameEffectsRuntimeVerification(fixture *globalNetworkReleaseEffectsFixture, durable state.GlobalNetworkReleasePlanRecord) bool {
	return fixture.runtimeRelease.verifyCalls == 1 &&
		fixture.runtimeRelease.verifyOperation == durable.Operation.Operation.ID &&
		fixture.runtimeRelease.verifyCheckpoint == durable.CheckpointRevision &&
		fixture.runtimeRelease.verifyNetwork == durable.NetworkRevision
}

// newGlobalNetworkReleaseEffectsFixture constructs exact fresh absent observations for the effects checkpoint.
func newGlobalNetworkReleaseEffectsFixture(t *testing.T) *globalNetworkReleaseEffectsFixture {
	t.Helper()
	base := newGlobalNetworkReleaseStartFixture(t)
	base.tStageAuthority()
	plan := base.journal.plan
	plan.Phase = state.GlobalNetworkReleasePlanPhaseVerifyEffects
	plan.CheckpointRevision = 15
	plan.LowPortReceipt = &state.GlobalNetworkReleaseLowPortReceipt{
		SourceCheckpointRevision:          11,
		OwnedAbsentObservationFingerprint: strings.Repeat("a", 64),
		VerifiedAt:                        base.clock.now,
	}
	plan.ResolverReceipt = &state.GlobalNetworkReleaseResolverReceipt{
		SourceCheckpointRevision:          12,
		OwnedAbsentObservationFingerprint: strings.Repeat("b", 64),
		VerifiedAt:                        base.clock.now,
	}
	plan.TrustReceipt = &state.GlobalNetworkReleaseTrustReceipt{
		SourceCheckpointRevision: 13,
		ObservationFingerprint:   strings.Repeat("c", 64),
		VerifiedAt:               base.clock.now,
	}
	plan.LoopbackReceipt = &state.GlobalNetworkReleaseLoopbackReceipt{
		SourceCheckpointRevision:     14,
		LoopbackEvidenceDigest:       strings.Repeat("d", 64),
		OwnedAbsentObservationDigest: strings.Repeat("e", 64),
		VerifiedAt:                   base.clock.now,
	}
	base.journal.plan = plan
	base.low.observation.Artifacts[0].Present = false
	base.low.observation.Artifacts[0].Owned = false
	base.low.observation.Artifacts[0].Exact = false
	base.low.observation.Artifacts[1].Present = false
	base.low.observation.Artifacts[1].Owned = false
	base.low.observation.Artifacts[1].Exact = false
	base.resolver.observation.Rules = nil
	trustRequest, err := trust.NewRequestForRequester(
		plan.Authority.Projection.ConfirmedOwnership.Record.InstallationID,
		plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		plan.Authority.Policy.Mechanisms.Trust,
		plan.Authority.Root,
	)
	if err != nil {
		t.Fatal(err)
	}
	trustObservation := trust.Observation{
		Request:  trustRequest,
		Complete: true,
	}
	base.trust.observation = &trustObservation
	base.loopback.state = loopback.StateAbsent
	base.loopback.addresses = nil
	base.runtimeRelease.verifyDigest = strings.Repeat("f", 64)
	base.calls = nil
	return &globalNetworkReleaseEffectsFixture{globalNetworkReleaseStartFixture: base}
}

// effectsLowPortFingerprint returns the independently computed low-port fact used by the receipt assertion.
func effectsLowPortFingerprint(t *testing.T, fixture *globalNetworkReleaseEffectsFixture) string {
	t.Helper()
	fingerprint, err := fixture.low.observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// effectsResolverFingerprint returns the independently computed resolver fact used by the receipt assertion.
func effectsResolverFingerprint(t *testing.T, fixture *globalNetworkReleaseEffectsFixture) string {
	t.Helper()
	fingerprint, err := fixture.resolver.observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// effectsTrustFingerprint returns the independently computed trust fact used by the receipt assertion.
func effectsTrustFingerprint(t *testing.T, fixture *globalNetworkReleaseEffectsFixture) string {
	t.Helper()
	observation, err := fixture.trust.Observe(t.Context(), fixture.trust.request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// effectsLoopbackDigest returns the canonical fresh absent pool digest used by the receipt assertion.
func effectsLoopbackDigest(t *testing.T, fixture *globalNetworkReleaseEffectsFixture) string {
	t.Helper()
	evidence := helper.PoolMutationEvidence{
		Pool:       fixture.journal.plan.Authority.Projection.ConfirmedOwnership.Record.LoopbackPoolPrefix,
		Identities: make([]helper.MutationEvidence, 0, len(fixture.journal.plan.Authority.LoopbackTargets)),
	}
	for _, target := range fixture.journal.plan.Authority.LoopbackTargets {
		observation := networkSetupTestObservation(target.Address)
		observation.State = loopback.StateAbsent
		observation.Assignments = nil
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		evidence.Identities = append(evidence.Identities, helper.MutationEvidence{
			Address: target.Address.String(),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationAbsent,
				Fingerprint: fingerprint,
			},
		})
	}
	digest, err := state.NetworkDataPlaneSetupEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
