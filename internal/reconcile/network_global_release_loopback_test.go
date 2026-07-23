package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/state"
)

// TestGlobalNetworkReleaseLoopbacksPrepare exercises the fenced complete-pool publication boundary.
func TestGlobalNetworkReleaseLoopbacksPrepare(t *testing.T) {
	fixture := newGlobalNetworkReleaseLoopbackFixture(t)
	got, err := fixture.coordinator.PrepareLoopbacks(t.Context(), fixture.prepareRequest())
	if err != nil || !reflect.DeepEqual(got, fixture.issuer.result()) || fixture.issuer.issues != 1 || fixture.issuer.closes != 1 {
		t.Fatalf("PrepareLoopbacks() = %#v, %v; issues/closes = %d/%d", got, err, fixture.issuer.issues, fixture.issuer.closes)
	}
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseLoopbackFixture, *GlobalNetworkReleasePrepareLoopbacksRequest)
	}{
		{
			name: "stale checkpoint",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, request *GlobalNetworkReleasePrepareLoopbacksRequest) {
				request.ExpectedCheckpointRevision++
			},
		},
		{
			name: "requester drift",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, request *GlobalNetworkReleasePrepareLoopbacksRequest) {
				request.RequesterIdentity = "other"
			},
		},
		{
			name: "phase drift",
			mutate: func(fixture *globalNetworkReleaseLoopbackFixture, _ *GlobalNetworkReleasePrepareLoopbacksRequest) {
				fixture.journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseVerifyEffects
			},
		},
		{
			name: "plan drift",
			mutate: func(fixture *globalNetworkReleaseLoopbackFixture, _ *GlobalNetworkReleasePrepareLoopbacksRequest) {
				fixture.plans.drift = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseLoopbackFixture(t)
			request := fixture.prepareRequest()
			test.mutate(fixture, &request)
			if _, err := fixture.coordinator.PrepareLoopbacks(t.Context(), request); err == nil || fixture.issuer.issues != 0 || fixture.issuer.closes != 0 {
				t.Fatalf("PrepareLoopbacks() error = %v; issues/closes = %d/%d", err, fixture.issuer.issues, fixture.issuer.closes)
			}
		})
	}
}

// TestGlobalNetworkReleaseLoopbacksPrepareIssuerFailures retains only valid indeterminate capability results.
func TestGlobalNetworkReleaseLoopbacksPrepareIssuerFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*globalNetworkReleaseLoopbackFixture)
		wantResult bool
		want       string
	}{
		{
			name: "open failure",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.openErr = errors.New("open")
			},
			want: "open",
		},
		{
			name: "nil issuer",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.nilIssuer = true
			},
			want: "issuer is nil",
		},
		{
			name: "typed nil issuer",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.typedNilIssuer = true
			},
			want: "issuer is nil",
		},
		{
			name: "ordinary issue failure",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.issuer.issueErr = errors.New("issue")
			},
			want: "issue",
		},
		{
			name: "close failure",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.issuer.closeErr = errors.New("close")
			},
			wantResult: true,
			want:       ticketissuer.ErrPoolPublicationIndeterminate.Error(),
		},
		{
			name: "indeterminate issue",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.issuer.issueErr = ticketissuer.ErrPoolPublicationIndeterminate
			},
			wantResult: true,
			want:       ticketissuer.ErrPoolPublicationIndeterminate.Error(),
		},
		{
			name: "invalid successful result",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.issuer.mutate = func(r *ticketissuer.PoolResult) {
					r.Operation = helper.OperationEnsureLoopbackPool
				}
			},
			want: "another authority",
		},
		{
			name: "invalid indeterminate result",
			configure: func(f *globalNetworkReleaseLoopbackFixture) {
				f.issuer.issueErr = ticketissuer.ErrPoolPublicationIndeterminate
				f.issuer.mutate = func(r *ticketissuer.PoolResult) {
					r.Pool = netip.MustParsePrefix("127.0.1.0/29")
				}
			},
			want: "another authority",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseLoopbackFixture(t)
			test.configure(fixture)
			got, err := fixture.coordinator.PrepareLoopbacks(t.Context(), fixture.prepareRequest())
			if err == nil || !strings.Contains(err.Error(), test.want) || (got != (ticketissuer.PoolResult{})) != test.wantResult {
				t.Fatalf("PrepareLoopbacks() = %#v, %v", got, err)
			}
			if fixture.openErr == nil && !fixture.nilIssuer && !fixture.typedNilIssuer && fixture.issuer.closes != 1 {
				t.Fatalf("Close() calls = %d, want 1", fixture.issuer.closes)
			}
		})
	}
}

// TestGlobalNetworkReleaseLoopbacksConfirmAdvancesAndReplays proves effect verification follows durable loopback confirmation.
func TestGlobalNetworkReleaseLoopbacksConfirmAdvancesAndReplays(t *testing.T) {
	fixture := newGlobalNetworkReleaseLoopbackFixture(t)
	fixture.base.runtimeRelease.verifyDigest = strings.Repeat("1", 64)
	request := fixture.confirmRequest(t)
	request.LoopbackEvidence.Identities[0].Changed = true
	evidenceDigest, err := state.NetworkDataPlaneSetupEvidenceDigest(request.LoopbackEvidence)
	if err != nil {
		t.Fatal(err)
	}
	observedEvidence := helper.PoolMutationEvidence{
		Pool:       request.LoopbackEvidence.Pool,
		Identities: slices.Clone(request.LoopbackEvidence.Identities),
	}
	for index := range observedEvidence.Identities {
		observedEvidence.Identities[index].Changed = false
	}
	observationDigest, err := state.NetworkDataPlaneSetupEvidenceDigest(observedEvidence)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), request)
	if err != nil ||
		advanced.Phase != state.GlobalNetworkReleasePlanPhaseOwnership ||
		advanced.LoopbackReceipt == nil ||
		advanced.EffectsReceipt == nil ||
		len(fixture.observer.addresses) != 16 {
		t.Fatalf(
			"ConfirmLoopbacks() = %#v, %v; observations = %d",
			advanced,
			err,
			len(fixture.observer.addresses),
		)
	}
	wantReceipt := state.GlobalNetworkReleaseLoopbackReceipt{
		SourceCheckpointRevision:     fixture.plan.CheckpointRevision,
		LoopbackEvidenceDigest:       evidenceDigest,
		OwnedAbsentObservationDigest: observationDigest,
		VerifiedAt:                   fixture.plan.TrustReceipt.VerifiedAt,
	}
	wantAdvance := state.AdvanceGlobalNetworkReleaseLoopbacksRequest{
		OperationID:        fixture.plan.Operation.Operation.ID,
		CheckpointRevision: fixture.plan.CheckpointRevision,
		NetworkRevision:    fixture.plan.NetworkRevision,
		Receipt:            wantReceipt,
	}
	if *advanced.LoopbackReceipt != wantReceipt || fixture.journal.lastRequest != wantAdvance {
		t.Fatalf("receipt/advance = %#v / %#v, want %#v / %#v", *advanced.LoopbackReceipt, fixture.journal.lastRequest, wantReceipt, wantAdvance)
	}
	wantEffects := state.GlobalNetworkReleaseEffectsReceipt{
		SourceCheckpointRevision:        wantReceipt.SourceCheckpointRevision + 1,
		RuntimeObservationDigest:        fixture.base.runtimeRelease.verifyDigest,
		OwnershipObservationFingerprint: fixture.plan.Authority.ExpectedOwnershipFingerprint,
		LowPortObservationFingerprint:   mustGlobalNetworkReleaseLowPortFingerprint(t, fixture.base.low.observation),
		ResolverObservationFingerprint:  mustGlobalNetworkReleaseResolverFingerprint(t, fixture.base.resolver.observation),
		TrustObservationFingerprint:     mustGlobalNetworkReleaseTrustFingerprint(t, fixture.base.trust.observedRequest, false),
		LoopbackObservationDigest:       observationDigest,
		VerifiedAt:                      fixture.plan.TrustReceipt.VerifiedAt,
	}
	wantEffectsAdvance := state.AdvanceGlobalNetworkReleaseEffectsRequest{
		OperationID:        fixture.plan.Operation.Operation.ID,
		CheckpointRevision: wantEffects.SourceCheckpointRevision,
		NetworkRevision:    fixture.plan.NetworkRevision,
		Receipt:            wantEffects,
	}
	if *advanced.EffectsReceipt != wantEffects || fixture.journal.lastEffectsRequest != wantEffectsAdvance {
		t.Fatalf(
			"effects receipt/advance = %#v / %#v, want %#v / %#v",
			*advanced.EffectsReceipt,
			fixture.journal.lastEffectsRequest,
			wantEffects,
			wantEffectsAdvance,
		)
	}
	if fixture.base.runtimeRelease.verifyCalls != 1 ||
		!reflect.DeepEqual(
			fixture.base.calls,
			[]string{"verify runtime", "ownership", "low", "resolver", "trust"},
		) {
		t.Fatalf("runtime/calls = %d/%#v", fixture.base.runtimeRelease.verifyCalls, fixture.base.calls)
	}
	for round := range 2 {
		for index, target := range fixture.plan.Authority.LoopbackTargets {
			observedIndex := round*len(fixture.plan.Authority.LoopbackTargets) + index
			if fixture.observer.addresses[observedIndex] != target.Address {
				t.Fatalf("observation %d address = %s, want %s", observedIndex, fixture.observer.addresses[observedIndex], target.Address)
			}
		}
	}
	fixture.plans.calls = 0
	fixture.observer.addresses = nil
	fixture.base.calls = nil
	fixture.base.runtimeRelease.verifyCalls = 0
	replayed, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), request)
	if err != nil ||
		!reflect.DeepEqual(replayed, advanced) ||
		fixture.plans.calls != 0 ||
		len(fixture.observer.addresses) != 0 ||
		fixture.issuer.issues != 0 ||
		fixture.journal.advances != 1 ||
		fixture.journal.effectsAdvances != 1 ||
		fixture.base.runtimeRelease.verifyCalls != 0 ||
		len(fixture.base.calls) != 0 ||
		fixture.journal.lastRequest != wantAdvance ||
		fixture.journal.lastEffectsRequest != wantEffectsAdvance {
		t.Fatalf(
			"replay = %#v, %v; plan/observation/issue/loopback advance/effects advance = %d/%d/%d/%d/%d",
			replayed,
			err,
			fixture.plans.calls,
			len(fixture.observer.addresses),
			fixture.issuer.issues,
			fixture.journal.advances,
			fixture.journal.effectsAdvances,
		)
	}
	request.LoopbackEvidence.Identities[0].Changed = true
	request.LoopbackEvidence.Identities[1].Changed = true
	if _, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), request); err == nil || fixture.journal.advances != 1 {
		t.Fatal("ConfirmLoopbacks() accepted altered replay evidence")
	}
}

// TestGlobalNetworkReleaseLegacyMacOSCompletesEffects proves a legacy current-user-trust release reaches the ownership handoff with complete effects evidence.
func TestGlobalNetworkReleaseLegacyMacOSCompletesEffects(t *testing.T) {
	base := newGlobalNetworkReleaseStartFixture(t)
	base.setLegacyMacOSAuthority(t)
	base.tStageAuthority()
	fixture := newGlobalNetworkReleaseLoopbackFixtureFromBase(t, base)
	fixture.base.runtimeRelease.verifyDigest = strings.Repeat("1", 64)
	advanced, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), fixture.confirmRequest(t))
	if err != nil || advanced.Phase != state.GlobalNetworkReleasePlanPhaseOwnership ||
		advanced.TrustReceipt == nil || advanced.LoopbackReceipt == nil || advanced.EffectsReceipt == nil {
		t.Fatalf("ConfirmLoopbacks() = %#v, %v", advanced, err)
	}
	if advanced.Authority.Policy.Mechanisms.Trust != networkpolicy.DarwinCurrentUserTrust ||
		advanced.TrustReceipt.Disposition != state.GlobalNetworkReleaseTrustOwned ||
		fixture.journal.effectsAdvances != 1 {
		t.Fatalf("legacy effects authority = %#v", advanced)
	}
	wantTrustFingerprint := mustGlobalNetworkReleaseTrustFingerprint(t, fixture.base.trust.observedRequest, false)
	if advanced.EffectsReceipt.TrustObservationFingerprint != wantTrustFingerprint ||
		fixture.journal.lastEffectsRequest.Receipt.TrustObservationFingerprint != wantTrustFingerprint {
		t.Fatalf("legacy trust absence fingerprint = %q/%q, want %q", advanced.EffectsReceipt.TrustObservationFingerprint, fixture.journal.lastEffectsRequest.Receipt.TrustObservationFingerprint, wantTrustFingerprint)
	}
}

// mustGlobalNetworkReleaseLowPortFingerprint returns one valid low-port observation digest for effect assertions.
func mustGlobalNetworkReleaseLowPortFingerprint(t *testing.T, observation lowport.Observation) string {
	t.Helper()
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// mustGlobalNetworkReleaseResolverFingerprint returns one valid resolver observation digest for effect assertions.
func mustGlobalNetworkReleaseResolverFingerprint(t *testing.T, observation resolver.Observation) string {
	t.Helper()
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// mustGlobalNetworkReleaseTrustFingerprint returns the released fixture's disposition-safe trust digest.
func mustGlobalNetworkReleaseTrustFingerprint(t *testing.T, request trust.Request, owned bool) string {
	t.Helper()
	if !owned {
		observation := trust.Observation{
			Request:  request,
			Complete: true,
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}
	entry := trust.Entry{
		Mechanism:              request.Mechanism(),
		NativeID:               "entry",
		CertificateFingerprint: request.AuthorityFingerprint(),
		NativeExact:            true,
		NativeAttributesSHA256: strings.Repeat("c", 64),
	}
	owner := request.OwnerMarker()
	entry.Owner = &owner
	observation := trust.Observation{
		Request:  request,
		Complete: true,
		Entries:  []trust.Entry{entry},
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// TestGlobalNetworkReleaseLoopbacksConfirmRetriesEffectVerification proves an effects
// advance failure retains exact loopback replay authority.
func TestGlobalNetworkReleaseLoopbacksConfirmRetriesEffectVerification(t *testing.T) {
	fixture := newGlobalNetworkReleaseLoopbackFixture(t)
	fixture.base.runtimeRelease.verifyDigest = strings.Repeat("1", 64)
	request := fixture.confirmRequest(t)
	fixture.journal.effectsAdvanceErr = errors.New("advance effects")
	if _, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), request); err == nil ||
		fixture.journal.plan.Phase != state.GlobalNetworkReleasePlanPhaseVerifyEffects ||
		fixture.journal.plan.LoopbackReceipt == nil ||
		fixture.journal.plan.EffectsReceipt != nil ||
		fixture.journal.advances != 1 ||
		fixture.journal.effectsAdvances != 1 ||
		len(fixture.observer.addresses) != 16 {
		t.Fatalf(
			"ConfirmLoopbacks() error = %v; phase/receipts/advances/observations = %q/%#v/%#v/%d/%d/%d",
			err,
			fixture.journal.plan.Phase,
			fixture.journal.plan.LoopbackReceipt,
			fixture.journal.plan.EffectsReceipt,
			fixture.journal.advances,
			fixture.journal.effectsAdvances,
			len(fixture.observer.addresses),
		)
	}
	fixture.journal.effectsAdvanceErr = nil
	fixture.plans.calls = 0
	fixture.observer.addresses = nil
	fixture.base.calls = nil
	fixture.base.runtimeRelease.verifyCalls = 0
	advanced, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), request)
	if err != nil ||
		advanced.Phase != state.GlobalNetworkReleasePlanPhaseOwnership ||
		fixture.plans.calls != 0 ||
		fixture.issuer.issues != 0 ||
		fixture.journal.advances != 2 ||
		fixture.journal.effectsAdvances != 2 ||
		len(fixture.observer.addresses) != 8 ||
		fixture.base.runtimeRelease.verifyCalls != 1 ||
		!reflect.DeepEqual(
			fixture.base.calls,
			[]string{"verify runtime", "ownership", "low", "resolver", "trust"},
		) {
		t.Fatalf(
			"retry = %#v, %v; plan/issue/advances/observations/runtime/calls = %d/%d/%d/%d/%d/%d/%#v",
			advanced,
			err,
			fixture.plans.calls,
			fixture.issuer.issues,
			fixture.journal.advances,
			fixture.journal.effectsAdvances,
			len(fixture.observer.addresses),
			fixture.base.runtimeRelease.verifyCalls,
			fixture.base.calls,
		)
	}
}

// TestGlobalNetworkReleaseLoopbacksConfirmPropagatesEffectsObservationFailure proves
// uncommitted effects verification stops at its failed observer.
func TestGlobalNetworkReleaseLoopbacksConfirmPropagatesEffectsObservationFailure(t *testing.T) {
	fixture := newGlobalNetworkReleaseLoopbackFixture(t)
	fixture.base.runtimeRelease.verifyDigest = strings.Repeat("1", 64)
	fixture.base.low.err = errors.New("observe low")
	if _, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), fixture.confirmRequest(t)); err == nil ||
		!strings.Contains(err.Error(), "observe release low ports: observe low") ||
		fixture.journal.plan.Phase != state.GlobalNetworkReleasePlanPhaseVerifyEffects ||
		fixture.journal.effectsAdvances != 0 ||
		len(fixture.observer.addresses) != 8 ||
		!reflect.DeepEqual(fixture.base.calls, []string{"verify runtime", "ownership", "low"}) {
		t.Fatalf(
			"ConfirmLoopbacks() error = %v; phase/effects/observations/calls = %q/%d/%d/%#v",
			err,
			fixture.journal.plan.Phase,
			fixture.journal.effectsAdvances,
			len(fixture.observer.addresses),
			fixture.base.calls,
		)
	}
}

// TestGlobalNetworkReleaseLoopbacksConfirmRejectsUnsafeFacts rejects evidence and native observations that escape exact owned absence.
func TestGlobalNetworkReleaseLoopbacksConfirmRejectsUnsafeFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseLoopbackFixture, *GlobalNetworkReleaseConfirmLoopbacksRequest)
	}{
		{
			name: "wrong pool",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, r *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				r.LoopbackEvidence.Pool = "127.0.1.0/29"
			},
		},
		{
			name: "wrong count",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, r *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				r.LoopbackEvidence.Identities = r.LoopbackEvidence.Identities[:7]
			},
		},
		{
			name: "wrong order",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, r *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				r.LoopbackEvidence.Identities[0], r.LoopbackEvidence.Identities[1] = r.LoopbackEvidence.Identities[1], r.LoopbackEvidence.Identities[0]
			},
		},
		{
			name: "wrong address",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, r *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				r.LoopbackEvidence.Identities[0].Address = "127.0.1.1"
			},
		},
		{
			name: "wrong state",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, r *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				r.LoopbackEvidence.Identities[0].Observation.State = helper.ObservationOwned
			},
		},
		{
			name: "wrong fingerprint",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, r *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				r.LoopbackEvidence.Identities[0].Observation.Fingerprint = strings.Repeat("a", 64)
			},
		},
		{
			name: "malformed fingerprint",
			mutate: func(_ *globalNetworkReleaseLoopbackFixture, r *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				r.LoopbackEvidence.Identities[0].Observation.Fingerprint = "invalid"
			},
		},
		{
			name: "native address",
			mutate: func(f *globalNetworkReleaseLoopbackFixture, _ *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				f.observer.addressMismatch = true
			},
		},
		{
			name: "native state",
			mutate: func(f *globalNetworkReleaseLoopbackFixture, _ *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				f.observer.state = loopback.StateExact
			},
		},
		{
			name: "native observation failure",
			mutate: func(f *globalNetworkReleaseLoopbackFixture, _ *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				f.observer.err = errors.New("observe")
			},
		},
		{
			name: "native fingerprint",
			mutate: func(f *globalNetworkReleaseLoopbackFixture, _ *GlobalNetworkReleaseConfirmLoopbacksRequest) {
				f.observer.fingerprintDrift = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseLoopbackFixture(t)
			request := fixture.confirmRequest(t)
			test.mutate(fixture, &request)
			if _, err := fixture.coordinator.ConfirmLoopbacks(t.Context(), request); err == nil || fixture.journal.advances != 0 {
				t.Fatalf("ConfirmLoopbacks() error = %v; advances = %d", err, fixture.journal.advances)
			}
		})
	}
}

// TestGlobalNetworkReleaseLoopbacksCancellationPreventsDependencies ensures cancellation precedes plan and native reads.
func TestGlobalNetworkReleaseLoopbacksCancellationPreventsDependencies(t *testing.T) {
	fixture := newGlobalNetworkReleaseLoopbackFixture(t)
	request := fixture.confirmRequest(t)
	fixture.plans.calls = 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.coordinator.ConfirmLoopbacks(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConfirmLoopbacks() error = %v", err)
	}
	if fixture.plans.calls != 0 || len(fixture.observer.addresses) != 0 || fixture.journal.advances != 0 {
		t.Fatalf("plan/observations/advances = %d/%d/%d", fixture.plans.calls, len(fixture.observer.addresses), fixture.journal.advances)
	}
}

// globalNetworkReleaseLoopbackFixture supplies retained loopback authority with all predecessor receipts.
type globalNetworkReleaseLoopbackFixture struct {
	base           *globalNetworkReleaseStartFixture
	coordinator    *GlobalNetworkReleaseCoordinator
	journal        *globalNetworkReleaseLoopbackJournal
	plans          *globalNetworkReleaseLoopbackPlans
	issuer         *globalNetworkReleaseLoopbackIssuer
	observer       *globalNetworkReleaseLoopbackObserver
	clock          *globalNetworkReleaseClock
	plan           state.GlobalNetworkReleasePlanRecord
	openErr        error
	nilIssuer      bool
	typedNilIssuer bool
}

// newGlobalNetworkReleaseLoopbackFixture constructs a valid complete release-pool checkpoint.
func newGlobalNetworkReleaseLoopbackFixture(t *testing.T) *globalNetworkReleaseLoopbackFixture {
	t.Helper()
	base := newGlobalNetworkReleaseStartFixture(t)
	base.tStageAuthority()
	return newGlobalNetworkReleaseLoopbackFixtureFromBase(t, base)
}

// newGlobalNetworkReleaseLoopbackFixtureFromBase constructs a complete pool-release checkpoint from staged authority.
func newGlobalNetworkReleaseLoopbackFixtureFromBase(t *testing.T, base *globalNetworkReleaseStartFixture) *globalNetworkReleaseLoopbackFixture {
	t.Helper()
	plan := base.journal.plan
	plan.Phase = state.GlobalNetworkReleasePlanPhaseLoopbacks
	plan.CheckpointRevision = 15
	plan.LowPortReceipt = &state.GlobalNetworkReleaseLowPortReceipt{
		SourceCheckpointRevision:          12,
		LowPortEvidenceDigest:             strings.Repeat("a", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("b", 64),
		VerifiedAt:                        base.clock.now,
	}
	plan.ResolverReceipt = &state.GlobalNetworkReleaseResolverReceipt{
		SourceCheckpointRevision:          13,
		ResolverEvidenceDigest:            strings.Repeat("c", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("d", 64),
		VerifiedAt:                        base.clock.now,
	}
	plan.TrustReceipt = &state.GlobalNetworkReleaseTrustReceipt{
		SourceCheckpointRevision: 14,
		Disposition:              plan.Authority.TrustDisposition,
		ConfirmationDigest:       strings.Repeat("e", 64),
		ObservationFingerprint:   strings.Repeat("f", 64),
		VerifiedAt:               base.clock.now,
	}
	fixture := &globalNetworkReleaseLoopbackFixture{
		base:  base,
		clock: base.clock,
		plan:  plan,
	}
	fixture.journal = &globalNetworkReleaseLoopbackJournal{
		plan: plan,
	}
	fixture.plans = &globalNetworkReleaseLoopbackPlans{
		fixture: fixture,
	}
	fixture.issuer = &globalNetworkReleaseLoopbackIssuer{
		fixture: fixture,
	}
	fixture.observer = &globalNetworkReleaseLoopbackObserver{}
	fixture.base.low.observation.Artifacts[0].Present = false
	fixture.base.low.observation.Artifacts[0].Owned = false
	fixture.base.low.observation.Artifacts[0].Exact = false
	fixture.base.low.observation.Artifacts[1].Present = false
	fixture.base.low.observation.Artifacts[1].Owned = false
	fixture.base.low.observation.Artifacts[1].Exact = false
	fixture.base.resolver.observation.Rules = nil
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
	fixture.base.trust.observation = &trustObservation
	fixture.coordinator = NewGlobalNetworkReleaseCoordinator(
		fixture.journal,
		base.source,
		base.projections,
		base.roots,
		base.ownership,
		base.low,
		globalNetworkReleaseUnavailableLowPortPlans{},
		func() (GlobalNetworkReleaseLowPortIssuer, error) {
			return nil, errors.New("unexpected")
		},
		globalNetworkReleaseUnavailableResolverPlans{},
		func() (GlobalNetworkReleaseResolverIssuer, error) {
			return nil, errors.New("unexpected")
		},
		globalNetworkReleaseUnavailableTrustPlans{},
		func() (GlobalNetworkReleaseTrustIssuer, error) {
			return nil, errors.New("unexpected")
		},
		fixture.plans,
		func() (GlobalNetworkReleaseLoopbackIssuer, error) {
			if fixture.openErr != nil {
				return nil, fixture.openErr
			}
			if fixture.nilIssuer {
				return nil, nil
			}
			if fixture.typedNilIssuer {
				var issuer *globalNetworkReleaseLoopbackIssuer
				return issuer, nil
			}
			return fixture.issuer, nil
		},
		globalNetworkReleaseUnavailableOwnershipPlans{},
		func() (GlobalNetworkReleaseOwnershipIssuer, error) {
			return nil, errors.New("unexpected release ownership issuer")
		},
		globalNetworkReleaseUnavailableOwnershipProofObserver{},
		base.resolver,
		base.trust,
		fixture.observer,
		base.runtimeRelease,
		base.coordinator.platform,
		fixture.clock,
	)
	return fixture
}

// prepareRequest returns the retained owner's exact publication request.
func (fixture *globalNetworkReleaseLoopbackFixture) prepareRequest() GlobalNetworkReleasePrepareLoopbacksRequest {
	return GlobalNetworkReleasePrepareLoopbacksRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
	}
}

// confirmRequest returns independently matching canonical absence evidence.
func (fixture *globalNetworkReleaseLoopbackFixture) confirmRequest(t *testing.T) GlobalNetworkReleaseConfirmLoopbacksRequest {
	t.Helper()
	plan, err := fixture.plans.Resolve(t.Context(), ticketissuer.PoolReleaseRequest{
		OperationID: fixture.plan.Operation.Operation.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	identities := make([]helper.MutationEvidence, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		observation := fixture.observer.observation(target.Address)
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, helper.MutationEvidence{
			Address: target.Address.String(),
			Observation: helper.ExpectedObservation{
				State:       helper.ObservationAbsent,
				Fingerprint: fingerprint,
			},
		})
	}
	return GlobalNetworkReleaseConfirmLoopbacksRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		LoopbackEvidence: helper.PoolMutationEvidence{
			Pool:       plan.Pool.Prefix().String(),
			Identities: identities,
		},
	}
}

// globalNetworkReleaseLoopbackPlans reads the exact retained pool authority.
type globalNetworkReleaseLoopbackPlans struct {
	fixture *globalNetworkReleaseLoopbackFixture
	calls   int
	drift   bool
}

// Resolve returns the fixture's exact retained complete pool authority.
func (source *globalNetworkReleaseLoopbackPlans) Resolve(_ context.Context, request ticketissuer.PoolReleaseRequest) (ticketissuer.PoolReleasePlan, error) {
	source.calls++
	plan := source.fixture.journal.plan
	if request.OperationID != plan.Operation.Operation.ID {
		return ticketissuer.PoolReleasePlan{}, errors.New("unavailable")
	}
	targets := make([]ticketissuer.PoolReleaseTarget, len(plan.Authority.LoopbackTargets))
	candidates := make([]netip.Addr, len(plan.Authority.LoopbackTargets))
	for index, target := range plan.Authority.LoopbackTargets {
		targets[index] = ticketissuer.PoolReleaseTarget{
			Address:                target.Address,
			ObservationFingerprint: target.ObservationFingerprint,
		}
		candidates[index] = target.Address
	}
	prefix, err := netip.ParsePrefix(plan.Authority.Projection.ConfirmedOwnership.Record.LoopbackPoolPrefix)
	if err != nil {
		return ticketissuer.PoolReleasePlan{}, err
	}
	pool, err := identity.NewPool(prefix, candidates)
	if err != nil {
		return ticketissuer.PoolReleasePlan{}, err
	}
	if source.drift {
		targets[0].ObservationFingerprint = strings.Repeat("a", 64)
	}
	return ticketissuer.PoolReleasePlan{
		Operation:          plan.Operation.Operation,
		OperationRevision:  plan.Operation.Revision,
		CheckpointRevision: plan.CheckpointRevision,
		TargetOwnership:    plan.Authority.Projection.ConfirmedOwnership.Record,
		Pool:               pool,
		Targets:            targets,
	}, nil
}

// globalNetworkReleaseLoopbackIssuer records publication and cleanup.
type globalNetworkReleaseLoopbackIssuer struct {
	fixture  *globalNetworkReleaseLoopbackFixture
	issues   int
	closes   int
	issueErr error
	closeErr error
	mutate   func(*ticketissuer.PoolResult)
}

// Issue returns the scripted publication result so tests can isolate coordinator validation.
func (issuer *globalNetworkReleaseLoopbackIssuer) Issue(_ context.Context, _ string, _ ticketissuer.PoolReleaseRequest) (ticketissuer.PoolResult, error) {
	issuer.issues++
	result := issuer.result()
	if issuer.mutate != nil {
		issuer.mutate(&result)
	}
	return result, issuer.issueErr
}

// Close records cleanup so tests can verify publisher lifecycles on each outcome.
func (issuer *globalNetworkReleaseLoopbackIssuer) Close() error {
	issuer.closes++
	return issuer.closeErr
}

// result derives a result from the retained plan to keep issued authority consistent with the fixture.
func (issuer *globalNetworkReleaseLoopbackIssuer) result() ticketissuer.PoolResult {
	plan, err := issuer.fixture.plans.Resolve(context.Background(), ticketissuer.PoolReleaseRequest{
		OperationID: issuer.fixture.plan.Operation.Operation.ID,
	})
	if err != nil {
		panic(err)
	}
	return ticketissuer.PoolResult{
		OperationID: plan.Operation.ID,
		Reference:   helper.TicketReference(strings.Repeat("a", 64)),
		Operation:   helper.OperationReleaseLoopbackPool,
		Pool:        plan.Pool.Prefix(),
		ExpiresAt:   issuer.fixture.clock.now.Add(time.Minute),
	}
}

// globalNetworkReleaseLoopbackObserver returns fresh scripted native loopback facts.
type globalNetworkReleaseLoopbackObserver struct {
	addresses        []netip.Addr
	state            loopback.State
	addressMismatch  bool
	fingerprintDrift bool
	err              error
}

// observation supplies mutable native facts so rejection paths can be tested without platform access.
func (observer *globalNetworkReleaseLoopbackObserver) observation(address netip.Addr) loopback.Observation {
	observation := networkSetupTestObservation(address)
	observation.State = loopback.StateAbsent
	observation.Assignments = nil
	if observer.state != "" {
		observation.State = observer.state
	}
	if observer.addressMismatch {
		observation.Address = address.Next()
	}
	if observer.fingerprintDrift {
		observation.Loopback.Name = "other"
	}
	return observation
}

// Observe records each native read to prove the coordinator's sequencing and coverage.
func (observer *globalNetworkReleaseLoopbackObserver) Observe(_ context.Context, address netip.Addr) (loopback.Observation, error) {
	observer.addresses = append(observer.addresses, address)
	if observer.err != nil {
		return loopback.Observation{}, observer.err
	}
	return observer.observation(address), nil
}

// globalNetworkReleaseLoopbackJournal advances and replays exact loopback receipts.
type globalNetworkReleaseLoopbackJournal struct {
	plan               state.GlobalNetworkReleasePlanRecord
	advances           int
	lastRequest        state.AdvanceGlobalNetworkReleaseLoopbacksRequest
	effectsAdvances    int
	lastEffectsRequest state.AdvanceGlobalNetworkReleaseEffectsRequest
	effectsAdvanceErr  error
}

// OperationByIntent rejects unused journal paths so tests fail if the coordinator reaches them.
func (*globalNetworkReleaseLoopbackJournal) OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// StageGlobalNetworkRelease rejects unused staging paths so tests remain scoped to loopback confirmation.
func (*globalNetworkReleaseLoopbackJournal) StageGlobalNetworkRelease(context.Context, state.StageGlobalNetworkReleaseRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// ReadActiveGlobalNetworkReleasePlan rejects unused active-plan reads during loopback tests.
func (*globalNetworkReleaseLoopbackJournal) ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	return state.GlobalNetworkReleasePlanRecord{}, false, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseLowPorts rejects unrelated phase advances during loopback tests.
func (*globalNetworkReleaseLoopbackJournal) AdvanceGlobalNetworkReleaseLowPorts(context.Context, state.AdvanceGlobalNetworkReleaseLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseResolver rejects unrelated phase advances during loopback tests.
func (*globalNetworkReleaseLoopbackJournal) AdvanceGlobalNetworkReleaseResolver(context.Context, state.AdvanceGlobalNetworkReleaseResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseTrust rejects unrelated phase advances during loopback tests.
func (*globalNetworkReleaseLoopbackJournal) AdvanceGlobalNetworkReleaseTrust(context.Context, state.AdvanceGlobalNetworkReleaseTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// ReadGlobalNetworkReleasePlan serves the retained plan only to its matching operation.
func (journal *globalNetworkReleaseLoopbackJournal) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	if operationID != journal.plan.Operation.Operation.ID {
		return state.GlobalNetworkReleasePlanRecord{}, false, nil
	}
	return journal.plan, true, nil
}

// AdvanceGlobalNetworkReleaseLoopbacks records the receipt and permits only exact replay after advancement.
func (journal *globalNetworkReleaseLoopbackJournal) AdvanceGlobalNetworkReleaseLoopbacks(_ context.Context, request state.AdvanceGlobalNetworkReleaseLoopbacksRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	journal.advances++
	journal.lastRequest = request
	if journal.plan.Phase == state.GlobalNetworkReleasePlanPhaseVerifyEffects {
		if journal.plan.LoopbackReceipt == nil || request.Receipt != *journal.plan.LoopbackReceipt {
			return state.GlobalNetworkReleasePlanRecord{}, errors.New("replay differs")
		}
		return journal.plan, nil
	}
	journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseVerifyEffects
	journal.plan.CheckpointRevision++
	journal.plan.LoopbackReceipt = &request.Receipt
	return journal.plan, nil
}

// AdvanceGlobalNetworkReleaseEffects records fresh release verification and permits only exact receipt replay.
func (journal *globalNetworkReleaseLoopbackJournal) AdvanceGlobalNetworkReleaseEffects(
	_ context.Context,
	request state.AdvanceGlobalNetworkReleaseEffectsRequest,
) (state.GlobalNetworkReleasePlanRecord, error) {
	journal.effectsAdvances++
	journal.lastEffectsRequest = request
	if journal.effectsAdvanceErr != nil {
		return state.GlobalNetworkReleasePlanRecord{}, journal.effectsAdvanceErr
	}
	if journal.plan.Phase == state.GlobalNetworkReleasePlanPhaseOwnership {
		if journal.plan.EffectsReceipt == nil || request != (state.AdvanceGlobalNetworkReleaseEffectsRequest{
			OperationID:        journal.plan.Operation.Operation.ID,
			CheckpointRevision: journal.plan.EffectsReceipt.SourceCheckpointRevision,
			NetworkRevision:    journal.plan.NetworkRevision,
			Receipt:            *journal.plan.EffectsReceipt,
		}) {
			return state.GlobalNetworkReleasePlanRecord{}, errors.New("effects replay differs")
		}
		return journal.plan, nil
	}
	if journal.plan.Phase != state.GlobalNetworkReleasePlanPhaseVerifyEffects {
		return state.GlobalNetworkReleasePlanRecord{}, errors.New("effects phase differs")
	}
	journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseOwnership
	journal.plan.CheckpointRevision++
	journal.plan.EffectsReceipt = &request.Receipt
	return journal.plan, nil
}
