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
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/state"
)

// TestGlobalNetworkReleaseResolverPrepare proves issuance is bound to one durable resolver checkpoint.
func TestGlobalNetworkReleaseResolverPrepare(t *testing.T) {
	fixture := newGlobalNetworkReleaseResolverFixture(t)
	want := fixture.issuer.result(fixture.plan)
	got, err := fixture.coordinator.PrepareResolver(t.Context(), fixture.prepareRequest())
	if err != nil {
		t.Fatalf("PrepareResolver() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) || fixture.issuer.issues != 1 || fixture.issuer.closes != 1 {
		t.Fatalf("result = %#v; issues/closes = %d/%d", got, fixture.issuer.issues, fixture.issuer.closes)
	}

	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseResolverFixture, *GlobalNetworkReleasePrepareResolverRequest)
		stale  bool
	}{
		{
			name: "wrong owner",
			mutate: func(_ *globalNetworkReleaseResolverFixture, request *GlobalNetworkReleasePrepareResolverRequest) {
				request.RequesterIdentity = "other-owner"
			},
		},
		{
			name: "stale checkpoint",
			mutate: func(_ *globalNetworkReleaseResolverFixture, request *GlobalNetworkReleasePrepareResolverRequest) {
				request.ExpectedCheckpointRevision++
			},
			stale: true,
		},
		{
			name: "plan durable policy drift",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleasePrepareResolverRequest) {
				fixture.plans.policyDrift = true
			},
		},
		{
			name: "plan durable ownership drift",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleasePrepareResolverRequest) {
				fixture.plans.ownershipDrift = true
			},
		},
		{
			name: "missing low-port receipt",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleasePrepareResolverRequest) {
				fixture.journal.plan.LowPortReceipt = nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseResolverFixture(t)
			request := fixture.prepareRequest()
			test.mutate(fixture, &request)
			_, err := fixture.coordinator.PrepareResolver(t.Context(), request)
			if err == nil {
				t.Fatal("PrepareResolver() error = nil")
			}
			var stale *state.StaleRevisionError
			if errors.As(err, &stale) != test.stale || fixture.issuer.issues != 0 || fixture.issuer.closes != 0 {
				t.Fatalf("error = %v; stale = %t; issues/closes = %d/%d", err, test.stale, fixture.issuer.issues, fixture.issuer.closes)
			}
		})
	}
}

// TestGlobalNetworkReleaseResolverPrepareIssuerFailures preserves the sole indeterminate capability and always closes an opened issuer.
func TestGlobalNetworkReleaseResolverPrepareIssuerFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		openErr    error
		issuer     func(*globalNetworkReleaseResolverFixture)
		wantResult bool
		wantError  string
	}{
		{
			name:      "nil opener result",
			issuer:    func(fixture *globalNetworkReleaseResolverFixture) { fixture.nilIssuer = true },
			wantError: "issuer is nil",
		},
		{
			name:      "opener error",
			openErr:   errors.New("open issuer"),
			wantError: "open issuer",
		},
		{
			name: "issue error",
			issuer: func(fixture *globalNetworkReleaseResolverFixture) {
				fixture.issuer.issueErr = errors.New("issue")
			},
			wantError: "issue",
		},
		{
			name: "close after success",
			issuer: func(fixture *globalNetworkReleaseResolverFixture) {
				fixture.issuer.closeErr = errors.New("close")
			},
			wantResult: true,
			wantError:  ticketissuer.ErrResolverPublicationIndeterminate.Error(),
		},
		{
			name: "indeterminate issue and close",
			issuer: func(fixture *globalNetworkReleaseResolverFixture) {
				fixture.issuer.issueErr = ticketissuer.ErrResolverPublicationIndeterminate
				fixture.issuer.closeErr = errors.New("close")
			},
			wantResult: true,
			wantError:  ticketissuer.ErrResolverPublicationIndeterminate.Error(),
		},
		{
			name: "indeterminate issue with unbound result",
			issuer: func(fixture *globalNetworkReleaseResolverFixture) {
				fixture.issuer.issueErr = ticketissuer.ErrResolverPublicationIndeterminate
				fixture.issuer.mutateResult = func(result *ticketissuer.ResolverResult) {
					result.PolicyFingerprint = strings.Repeat("f", 64)
				}
			},
			wantError: "another policy or ownership",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseResolverFixture(t)
			fixture.openErr = test.openErr
			if test.issuer != nil {
				test.issuer(fixture)
			}
			got, err := fixture.coordinator.PrepareResolver(t.Context(), fixture.prepareRequest())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("PrepareResolver() error = %v, want message %q", err, test.wantError)
			}
			if test.wantResult != !reflect.DeepEqual(got, ticketissuer.ResolverResult{}) {
				t.Fatalf("result = %#v; want result = %t", got, test.wantResult)
			}
			if fixture.openErr == nil && !fixture.nilIssuer && fixture.issuer.closes != 1 {
				t.Fatalf("Close() calls = %d, want 1", fixture.issuer.closes)
			}
		})
	}
}

// TestGlobalNetworkReleaseResolverPrepareRejectsCommittedTrust proves completed resolver authority cannot republish a capability.
func TestGlobalNetworkReleaseResolverPrepareRejectsCommittedTrust(t *testing.T) {
	fixture := newGlobalNetworkReleaseResolverFixture(t)
	fixture.journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseTrust
	fixture.journal.plan.ResolverReceipt = &state.GlobalNetworkReleaseResolverReceipt{
		SourceCheckpointRevision:          fixture.plan.CheckpointRevision,
		ResolverEvidenceDigest:            strings.Repeat("a", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("b", 64),
		VerifiedAt:                        fixture.clock.now,
	}
	if _, err := fixture.coordinator.PrepareResolver(t.Context(), fixture.prepareRequest()); err == nil {
		t.Fatal("PrepareResolver() unexpectedly accepted committed trust")
	}
	if fixture.issuer.issues != 0 || fixture.issuer.closes != 0 {
		t.Fatalf("issues/closes = %d/%d, want zero", fixture.issuer.issues, fixture.issuer.closes)
	}
}

// TestGlobalNetworkReleaseResolverAcceptsEqualOperationWithDistinctStartedAt proves operation comparison is semantic rather than pointer-identity based.
func TestGlobalNetworkReleaseResolverAcceptsEqualOperationWithDistinctStartedAt(t *testing.T) {
	fixture := newGlobalNetworkReleaseResolverFixture(t)
	fixture.plans.distinctStartedAt = true
	if _, err := fixture.coordinator.PrepareResolver(t.Context(), fixture.prepareRequest()); err != nil {
		t.Fatalf("PrepareResolver() error = %v", err)
	}
}

// TestGlobalNetworkReleaseResolverConfirmAdvancesAndReplays proves owned absence advances once and exact replay retains its original verification time.
func TestGlobalNetworkReleaseResolverConfirmAdvancesAndReplays(t *testing.T) {
	fixture := newGlobalNetworkReleaseResolverFixture(t)
	fixture.observer.observation = fixture.absentObservation()
	request := fixture.confirmRequest(t)
	advanced, err := fixture.coordinator.ConfirmResolver(t.Context(), request)
	if err != nil {
		t.Fatalf("ConfirmResolver() error = %v", err)
	}
	if advanced.Phase != state.GlobalNetworkReleasePlanPhaseTrust || advanced.ResolverReceipt == nil || advanced.ResolverReceipt.VerifiedAt != fixture.plan.LowPortReceipt.VerifiedAt {
		t.Fatalf("advanced = %#v", advanced)
	}
	fixture.clock.now = fixture.clock.now.Add(time.Hour)
	fixture.plans.calls = 0
	replayed, err := fixture.coordinator.ConfirmResolver(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) || fixture.plans.calls != 0 || fixture.journal.advances != 2 {
		t.Fatalf("replay = %#v; error = %v; plan calls = %d; advances = %d", replayed, err, fixture.plans.calls, fixture.journal.advances)
	}
	request.ResolverEvidence.Changed = !request.ResolverEvidence.Changed
	if _, err := fixture.coordinator.ConfirmResolver(t.Context(), request); err == nil {
		t.Fatal("ConfirmResolver() accepted altered replay evidence")
	}
}

// TestGlobalNetworkReleaseResolverConfirmRejectsUnsafeEvidence proves only independent exact owned absence may advance durable state.
func TestGlobalNetworkReleaseResolverConfirmRejectsUnsafeEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseResolverFixture, *GlobalNetworkReleaseConfirmResolverRequest)
	}{
		{
			name: "policy mismatch",
			mutate: func(_ *globalNetworkReleaseResolverFixture, request *GlobalNetworkReleaseConfirmResolverRequest) {
				request.ResolverEvidence.PolicyFingerprint = strings.Repeat("a", 64)
			},
		},
		{
			name: "ownership mismatch",
			mutate: func(_ *globalNetworkReleaseResolverFixture, request *GlobalNetworkReleaseConfirmResolverRequest) {
				request.ResolverEvidence.OwnershipFingerprint = strings.Repeat("b", 64)
			},
		},
		{
			name: "observation fingerprint mismatch",
			mutate: func(_ *globalNetworkReleaseResolverFixture, request *GlobalNetworkReleaseConfirmResolverRequest) {
				request.ResolverEvidence.ObservationFingerprint = strings.Repeat("c", 64)
			},
		},
		{
			name: "wrong request",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleaseConfirmResolverRequest) {
				fixture.observer.wrongRequest = true
			},
		},
		{
			name: "incomplete observation",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleaseConfirmResolverRequest) {
				fixture.observer.observation.Complete = false
			},
		},
		{
			name: "indeterminate observation",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleaseConfirmResolverRequest) {
				fixture.observer.observation.Truncated = true
			},
		},
		{
			name: "owned exact",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleaseConfirmResolverRequest) {
				fixture.observer.observation = fixture.base.resolver.observation
			},
		},
		{
			name: "owned drifted",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleaseConfirmResolverRequest) {
				fixture.observer.observation = fixture.base.resolver.observation
				fixture.observer.observation.Rules[0].NativeExact = false
			},
		},
		{
			name: "owned ambiguous",
			mutate: func(fixture *globalNetworkReleaseResolverFixture, _ *GlobalNetworkReleaseConfirmResolverRequest) {
				fixture.observer.observation = fixture.base.resolver.observation
				rule := fixture.observer.observation.Rules[0]
				rule.NativeID = "another-owned-rule"
				fixture.observer.observation.Rules = append(fixture.observer.observation.Rules, rule)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseResolverFixture(t)
			fixture.observer.observation = fixture.absentObservation()
			request := fixture.confirmRequest(t)
			test.mutate(fixture, &request)
			if _, err := fixture.coordinator.ConfirmResolver(t.Context(), request); err == nil {
				t.Fatal("ConfirmResolver() error = nil")
			}
			if fixture.journal.advances != 0 {
				t.Fatalf("advances = %d, want zero", fixture.journal.advances)
			}
		})
	}
}

// TestGlobalNetworkReleaseResolverConfirmAcceptsForeignOnlyAbsence proves foreign suffix claims do not prevent release of already-absent owned state.
func TestGlobalNetworkReleaseResolverConfirmAcceptsForeignOnlyAbsence(t *testing.T) {
	fixture := newGlobalNetworkReleaseResolverFixture(t)
	observation := fixture.absentObservation()
	rule := fixture.base.resolver.observation.Rules[0]
	rule.Owner = nil
	observation.Rules = []resolver.RuleFact{rule}
	fixture.observer.observation = observation
	request := fixture.confirmRequest(t)
	if _, err := fixture.coordinator.ConfirmResolver(t.Context(), request); err != nil {
		t.Fatalf("ConfirmResolver() error = %v", err)
	}
}

// TestGlobalNetworkReleaseResolverCancellationPreventsDependencies proves canceled calls neither issue capabilities nor advance durable state.
func TestGlobalNetworkReleaseResolverCancellationPreventsDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fixture := newGlobalNetworkReleaseResolverFixture(t)
	if _, err := fixture.coordinator.PrepareResolver(ctx, fixture.prepareRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareResolver() error = %v", err)
	}
	fixture.observer.observation = fixture.absentObservation()
	if _, err := fixture.coordinator.ConfirmResolver(ctx, fixture.confirmRequest(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConfirmResolver() error = %v", err)
	}
	if fixture.issuer.issues != 0 || fixture.journal.advances != 0 || fixture.plans.calls != 0 {
		t.Fatalf("issues/advances/plan calls = %d/%d/%d", fixture.issuer.issues, fixture.journal.advances, fixture.plans.calls)
	}
}

// globalNetworkReleaseResolverFixture supplies an independent resolver-release checkpoint.
type globalNetworkReleaseResolverFixture struct {
	base        *globalNetworkReleaseStartFixture
	coordinator *GlobalNetworkReleaseCoordinator
	journal     *globalNetworkReleaseResolverJournal
	plans       *globalNetworkReleaseResolverPlans
	issuer      *globalNetworkReleaseResolverIssuer
	observer    *globalNetworkReleaseResolverObserver
	clock       *globalNetworkReleaseClock
	plan        state.GlobalNetworkReleasePlanRecord
	openErr     error
	nilIssuer   bool
}

// newGlobalNetworkReleaseResolverFixture constructs a valid resolver-release coordinator boundary.
func newGlobalNetworkReleaseResolverFixture(t *testing.T) *globalNetworkReleaseResolverFixture {
	t.Helper()
	base := newGlobalNetworkReleaseStartFixture(t)
	base.tStageAuthority()
	plan := base.journal.plan
	plan.Phase = state.GlobalNetworkReleasePlanPhaseResolver
	plan.CheckpointRevision = 13
	plan.LowPortReceipt = &state.GlobalNetworkReleaseLowPortReceipt{
		SourceCheckpointRevision:          12,
		LowPortEvidenceDigest:             strings.Repeat("d", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("e", 64),
		VerifiedAt:                        base.clock.now,
	}
	fixture := &globalNetworkReleaseResolverFixture{
		base:  base,
		clock: base.clock,
		plan:  plan,
	}
	fixture.journal = &globalNetworkReleaseResolverJournal{
		plan: plan,
	}
	fixture.plans = &globalNetworkReleaseResolverPlans{
		fixture: fixture,
	}
	fixture.issuer = &globalNetworkReleaseResolverIssuer{
		fixture: fixture,
	}
	fixture.observer = &globalNetworkReleaseResolverObserver{}
	fixture.coordinator = NewGlobalNetworkReleaseCoordinator(
		fixture.journal,
		base.source,
		base.projections,
		base.roots,
		base.ownership,
		base.low,
		globalNetworkReleaseUnavailableLowPortPlans{},
		func() (GlobalNetworkReleaseLowPortIssuer, error) {
			return nil, errors.New("unexpected low-port issuer")
		},
		fixture.plans,
		func() (GlobalNetworkReleaseResolverIssuer, error) {
			if fixture.openErr != nil {
				return nil, fixture.openErr
			}
			if fixture.nilIssuer {
				return nil, nil
			}
			return fixture.issuer, nil
		},
		globalNetworkReleaseUnavailableTrustPlans{},
		func() (GlobalNetworkReleaseTrustIssuer, error) {
			return nil, errors.New("unexpected release trust issuer")
		},
		globalNetworkReleaseUnavailableLoopbackPlans{},
		func() (GlobalNetworkReleaseLoopbackIssuer, error) {
			return nil, errors.New("unexpected release loopback issuer")
		},
		fixture.observer,
		base.trust,
		base.loopback,
		base.runtimeRelease,
		base.coordinator.platform,
		fixture.clock,
	)
	return fixture
}

// prepareRequest returns the fixture owner's exact resolver publication request.
func (fixture *globalNetworkReleaseResolverFixture) prepareRequest() GlobalNetworkReleasePrepareResolverRequest {
	return GlobalNetworkReleasePrepareResolverRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
	}
}

// confirmRequest derives evidence from the observer's current independent fact set.
func (fixture *globalNetworkReleaseResolverFixture) confirmRequest(t *testing.T) GlobalNetworkReleaseConfirmResolverRequest {
	t.Helper()
	fingerprint, err := fixture.observer.observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := fixture.plan.Authority.Policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.plan.Authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return GlobalNetworkReleaseConfirmResolverRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		ResolverEvidence: helper.ResolverMutationEvidence{
			PolicyFingerprint:      policy,
			OwnershipFingerprint:   owner,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.ResolverPostconditionOwnedAbsent,
		},
	}
}

// absentObservation returns complete absence for the fixture's canonical request.
func (fixture *globalNetworkReleaseResolverFixture) absentObservation() resolver.Observation {
	request, err := resolver.NewRequest(fixture.plan.Authority.Projection.ConfirmedOwnership.Record.InstallationID, fixture.plan.Authority.Policy)
	if err != nil {
		panic(err)
	}
	return resolver.Observation{
		Request:  request,
		Complete: true,
		Rules:    []resolver.RuleFact{},
	}
}

// globalNetworkReleaseResolverPlans resolves the fixture's exact durable resolver authority.
type globalNetworkReleaseResolverPlans struct {
	fixture           *globalNetworkReleaseResolverFixture
	calls             int
	policyDrift       bool
	ownershipDrift    bool
	distinctStartedAt bool
}

// Resolve returns the resolver-release plan for the currently durable resolver checkpoint.
func (source *globalNetworkReleaseResolverPlans) Resolve(_ context.Context, request ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
	source.calls++
	plan := source.fixture.journal.plan
	if request.OperationID != plan.Operation.Operation.ID || plan.Phase != state.GlobalNetworkReleasePlanPhaseResolver {
		return ticketissuer.ResolverPlan{}, errors.New("resolver release plan unavailable")
	}
	policy := plan.Authority.Policy
	if source.policyDrift {
		policy.Suffix = ".drift.test"
	}
	target := plan.Authority.Projection.ConfirmedOwnership.Record
	if source.ownershipDrift {
		target.OwnerIdentity = "other-owner"
	}
	operation := plan.Operation.Operation
	if source.distinctStartedAt && operation.StartedAt != nil {
		startedAt := *operation.StartedAt
		operation.StartedAt = &startedAt
	}
	return ticketissuer.ResolverPlan{
		Purpose:                            ticketissuer.ResolverPlanPurposeGlobalRelease,
		Operation:                          operation,
		OperationRevision:                  plan.Operation.Revision,
		CheckpointRevision:                 plan.CheckpointRevision,
		CheckpointPhase:                    ticketissuer.ResolverCheckpointPhaseGlobalRelease,
		Mutation:                           helper.OperationReleaseResolver,
		ExpectedSourceOwnershipFingerprint: resolverFixtureOwnershipFingerprint(plan),
		TargetOwnership:                    target,
		Policy:                             policy,
	}, nil
}

// resolverFixtureOwnershipFingerprint returns the release source fingerprint required by resolver plans.
func resolverFixtureOwnershipFingerprint(plan state.GlobalNetworkReleasePlanRecord) string {
	fingerprint, err := plan.Authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		panic(err)
	}
	return fingerprint
}

// globalNetworkReleaseResolverIssuer scripts issuance and closure semantics.
type globalNetworkReleaseResolverIssuer struct {
	fixture      *globalNetworkReleaseResolverFixture
	issues       int
	closes       int
	issueErr     error
	closeErr     error
	mutateResult func(*ticketissuer.ResolverResult)
}

// Issue returns the one exact bounded resolver-release result.
func (issuer *globalNetworkReleaseResolverIssuer) Issue(_ context.Context, _ string, _ ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error) {
	issuer.issues++
	result := issuer.result(issuer.fixture.journal.plan)
	if issuer.mutateResult != nil {
		issuer.mutateResult(&result)
	}
	return result, issuer.issueErr
}

// Close records resource cleanup after every issuer attempt.
func (issuer *globalNetworkReleaseResolverIssuer) Close() error {
	issuer.closes++
	return issuer.closeErr
}

// result derives an issuer result from one exact plan.
func (issuer *globalNetworkReleaseResolverIssuer) result(plan state.GlobalNetworkReleasePlanRecord) ticketissuer.ResolverResult {
	policy, _ := plan.Authority.Policy.Fingerprint()
	owner, _ := plan.Authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	return ticketissuer.ResolverResult{
		OperationID:          plan.Operation.Operation.ID,
		Reference:            helper.TicketReference(strings.Repeat("a", 64)),
		Operation:            helper.OperationReleaseResolver,
		PolicyFingerprint:    policy,
		OwnershipFingerprint: owner,
		ExpiresAt:            issuer.fixture.clock.now.Add(time.Minute),
	}
}

// globalNetworkReleaseResolverObserver supplies mutable independent native resolver facts.
type globalNetworkReleaseResolverObserver struct {
	observation  resolver.Observation
	wrongRequest bool
}

// Observe returns the configured resolver observation for the supplied request.
func (observer *globalNetworkReleaseResolverObserver) Observe(_ context.Context, request resolver.Request) (resolver.Observation, error) {
	observation := observer.observation
	if observer.wrongRequest {
		observation.Request = resolver.Request{}
	}
	return observation, nil
}

// globalNetworkReleaseResolverJournal records exact resolver receipt advances and replays.
type globalNetworkReleaseResolverJournal struct {
	plan     state.GlobalNetworkReleasePlanRecord
	advances int
}

// OperationByIntent is unused by resolver-release tests.
func (*globalNetworkReleaseResolverJournal) OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// StageGlobalNetworkRelease is unused by resolver-release tests.
func (*globalNetworkReleaseResolverJournal) StageGlobalNetworkRelease(context.Context, state.StageGlobalNetworkReleaseRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// ReadActiveGlobalNetworkReleasePlan is unused by resolver-release tests.
func (*globalNetworkReleaseResolverJournal) ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	return state.GlobalNetworkReleasePlanRecord{}, false, errors.New("unexpected")
}

// ReadGlobalNetworkReleasePlan returns the retained plan for its exact operation.
func (journal *globalNetworkReleaseResolverJournal) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	if journal.plan.Operation.Operation.ID != operationID {
		return state.GlobalNetworkReleasePlanRecord{}, false, nil
	}
	return journal.plan, true, nil
}

// AdvanceGlobalNetworkReleaseLowPorts is unused by resolver-release tests.
func (*globalNetworkReleaseResolverJournal) AdvanceGlobalNetworkReleaseLowPorts(context.Context, state.AdvanceGlobalNetworkReleaseLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseResolver persists or replays only its exact resolver receipt.
func (journal *globalNetworkReleaseResolverJournal) AdvanceGlobalNetworkReleaseResolver(_ context.Context, request state.AdvanceGlobalNetworkReleaseResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	journal.advances++
	if journal.plan.Phase == state.GlobalNetworkReleasePlanPhaseTrust {
		if journal.plan.ResolverReceipt == nil || request.Receipt != *journal.plan.ResolverReceipt {
			return state.GlobalNetworkReleasePlanRecord{}, errors.New("resolver replay receipt differs")
		}
		return journal.plan, nil
	}
	if request.CheckpointRevision != journal.plan.CheckpointRevision || request.NetworkRevision != journal.plan.NetworkRevision {
		return state.GlobalNetworkReleasePlanRecord{}, errors.New("resolver advance fence differs")
	}
	journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseTrust
	journal.plan.ResolverReceipt = &request.Receipt
	journal.plan.CheckpointRevision++
	return journal.plan, nil
}

// AdvanceGlobalNetworkReleaseTrust is unused by resolver-release tests.
func (*globalNetworkReleaseResolverJournal) AdvanceGlobalNetworkReleaseTrust(context.Context, state.AdvanceGlobalNetworkReleaseTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected trust advance")
}

// AdvanceGlobalNetworkReleaseLoopbacks is unused by resolver-release tests.
func (*globalNetworkReleaseResolverJournal) AdvanceGlobalNetworkReleaseLoopbacks(context.Context, state.AdvanceGlobalNetworkReleaseLoopbacksRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseEffects is unused by resolver-release tests.
func (*globalNetworkReleaseResolverJournal) AdvanceGlobalNetworkReleaseEffects(
	context.Context,
	state.AdvanceGlobalNetworkReleaseEffectsRequest,
) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}
