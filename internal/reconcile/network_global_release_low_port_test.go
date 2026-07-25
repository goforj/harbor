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
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/state"
)

// TestGlobalNetworkReleaseLowPortsPrepare validates issuance is fenced to one owner-held release checkpoint.
func TestGlobalNetworkReleaseLowPortsPrepare(t *testing.T) {
	fixture := newGlobalNetworkReleaseLowPortFixture(t)
	request := fixture.prepareRequest()
	result, err := fixture.coordinator.PrepareLowPorts(t.Context(), request)
	if err != nil {
		t.Fatalf("PrepareLowPorts() error = %v", err)
	}
	if result.Operation != helper.OperationReleaseLowPorts || fixture.issuer.issues != 1 || fixture.issuer.closes != 1 {
		t.Fatalf("result=%#v issues=%d closes=%d", result, fixture.issuer.issues, fixture.issuer.closes)
	}
	for _, test := range []struct {
		name   string
		mutate func(*GlobalNetworkReleasePrepareLowPortsRequest)
		stale  bool
	}{
		{
			name: "stale checkpoint",
			mutate: func(request *GlobalNetworkReleasePrepareLowPortsRequest) {
				request.ExpectedCheckpointRevision++
			},
			stale: true,
		},
		{
			name: "wrong owner",
			mutate: func(request *GlobalNetworkReleasePrepareLowPortsRequest) {
				request.RequesterIdentity = "other-owner"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseLowPortFixture(t)
			request := fixture.prepareRequest()
			test.mutate(&request)
			_, err := fixture.coordinator.PrepareLowPorts(t.Context(), request)
			if err == nil {
				t.Fatal("PrepareLowPorts() error = nil")
			}
			var stale *state.StaleRevisionError
			if errors.As(err, &stale) != test.stale {
				t.Fatalf("PrepareLowPorts() error = %v, stale=%t", err, test.stale)
			}
			if fixture.issuer.issues != 0 {
				t.Fatalf("issuer calls = %d, want zero", fixture.issuer.issues)
			}
		})
	}
}

// TestGlobalNetworkReleaseLowPortsPrepareAcceptsEqualOperationWithDistinctStartedAt proves separate durable reads compare operation timestamps by value.
func TestGlobalNetworkReleaseLowPortsPrepareAcceptsEqualOperationWithDistinctStartedAt(t *testing.T) {
	fixture := newGlobalNetworkReleaseLowPortFixture(t)
	fixture.plans.distinctStartedAt = true
	if _, err := fixture.coordinator.PrepareLowPorts(t.Context(), fixture.prepareRequest()); err != nil {
		t.Fatalf("PrepareLowPorts() error = %v", err)
	}
}

// TestGlobalNetworkReleaseLowPortsPrepareRejectsCommittedResolver proves completed low-port authority cannot republish a capability.
func TestGlobalNetworkReleaseLowPortsPrepareRejectsCommittedResolver(t *testing.T) {
	fixture := newGlobalNetworkReleaseLowPortFixture(t)
	fixture.journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseResolver
	fixture.journal.plan.LowPortReceipt = &state.GlobalNetworkReleaseLowPortReceipt{
		SourceCheckpointRevision:          fixture.plan.CheckpointRevision,
		LowPortEvidenceDigest:             strings.Repeat("a", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("b", 64),
		VerifiedAt:                        fixture.clock.now,
	}
	if _, err := fixture.coordinator.PrepareLowPorts(t.Context(), fixture.prepareRequest()); err == nil {
		t.Fatal("PrepareLowPorts() unexpectedly accepted committed resolver")
	}
	if fixture.issuer.issues != 0 || fixture.issuer.closes != 0 {
		t.Fatalf("issues/closes = %d/%d, want zero", fixture.issuer.issues, fixture.issuer.closes)
	}
}

// TestGlobalNetworkReleaseLowPortsConfirmPersistsIndependentAbsence proves a crash-after-helper absent state can advance once.
func TestGlobalNetworkReleaseLowPortsConfirmPersistsIndependentAbsence(t *testing.T) {
	fixture := newGlobalNetworkReleaseLowPortFixture(t)
	fixture.low.observation = fixture.absentObservation()
	evidence := fixture.absentEvidence(t)
	request := GlobalNetworkReleaseConfirmLowPortsRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		LowPortEvidence:            evidence,
	}
	advanced, err := fixture.coordinator.ConfirmLowPorts(t.Context(), request)
	if err != nil {
		t.Fatalf("ConfirmLowPorts() error = %v", err)
	}
	digest, err := state.NetworkDataPlaneSetupEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Phase != state.GlobalNetworkReleasePlanPhaseResolver || advanced.LowPortReceipt == nil || advanced.LowPortReceipt.LowPortEvidenceDigest != digest || advanced.LowPortReceipt.OwnedAbsentObservationFingerprint != evidence.ObservationFingerprint || advanced.LowPortReceipt.SourceCheckpointRevision != request.ExpectedCheckpointRevision {
		t.Fatalf("advanced plan = %#v", advanced)
	}
	fixture.clock.now = fixture.clock.now.Add(time.Hour)
	replayed, err := fixture.coordinator.ConfirmLowPorts(t.Context(), request)
	if err != nil {
		t.Fatalf("ConfirmLowPorts() replay error = %v", err)
	}
	if !reflect.DeepEqual(replayed, advanced) {
		t.Fatalf("replayed plan = %#v, want %#v", replayed, advanced)
	}
	request.LowPortEvidence.Changed = !request.LowPortEvidence.Changed
	if _, err := fixture.coordinator.ConfirmLowPorts(t.Context(), request); err == nil {
		t.Fatal("ConfirmLowPorts() accepted changed replay evidence")
	}
}

// TestGlobalNetworkReleaseLowPortsConfirmRejectsUnprovenNativeFacts keeps durable advance behind independent proof.
func TestGlobalNetworkReleaseLowPortsConfirmRejectsUnprovenNativeFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseLowPortFixture, *helper.LowPortMutationEvidence)
	}{
		{
			name: "policy drift",
			mutate: func(_ *globalNetworkReleaseLowPortFixture, evidence *helper.LowPortMutationEvidence) {
				evidence.PolicyFingerprint = strings.Repeat("a", 64)
			},
		},
		{
			name: "incomplete observation",
			mutate: func(fixture *globalNetworkReleaseLowPortFixture, _ *helper.LowPortMutationEvidence) {
				fixture.low.observation.Complete = false
			},
		},
		{
			name: "non absent observation",
			mutate: func(fixture *globalNetworkReleaseLowPortFixture, _ *helper.LowPortMutationEvidence) {
				fixture.low.observation = fixture.base.low.observation
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseLowPortFixture(t)
			fixture.low.observation = fixture.absentObservation()
			evidence := fixture.absentEvidence(t)
			test.mutate(fixture, &evidence)
			_, err := fixture.coordinator.ConfirmLowPorts(t.Context(), GlobalNetworkReleaseConfirmLowPortsRequest{
				OperationID:                fixture.plan.Operation.Operation.ID,
				ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
				RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
				LowPortEvidence:            evidence,
			})
			if err == nil {
				t.Fatal("ConfirmLowPorts() error = nil")
			}
			if fixture.journal.advances != 0 {
				t.Fatalf("advances = %d, want zero", fixture.journal.advances)
			}
		})
	}
}

// globalNetworkReleaseLowPortFixture supplies a staged release plan at its low-port checkpoint.
type globalNetworkReleaseLowPortFixture struct {
	base        *globalNetworkReleaseStartFixture
	coordinator *GlobalNetworkReleaseCoordinator
	journal     *globalNetworkReleaseLowPortJournal
	plans       *globalNetworkReleaseLowPortPlans
	issuer      *globalNetworkReleaseLowPortIssuer
	low         *globalNetworkReleaseLowPortObserver
	clock       *globalNetworkReleaseClock
	plan        state.GlobalNetworkReleasePlanRecord
}

// newGlobalNetworkReleaseLowPortFixture constructs one valid low-port approval boundary.
func newGlobalNetworkReleaseLowPortFixture(t *testing.T) *globalNetworkReleaseLowPortFixture {
	t.Helper()
	base := newGlobalNetworkReleaseStartFixture(t)
	base.tStageAuthority()
	plan := base.journal.plan
	plan.Phase = state.GlobalNetworkReleasePlanPhaseLowPorts
	plan.CheckpointRevision = 12
	low := &globalNetworkReleaseLowPortObserver{
		observation: base.low.observation,
	}
	fixture := &globalNetworkReleaseLowPortFixture{
		base:  base,
		low:   low,
		clock: base.clock,
		plan:  plan,
	}
	fixture.journal = &globalNetworkReleaseLowPortJournal{
		plan: plan,
	}
	fixture.plans = &globalNetworkReleaseLowPortPlans{
		fixture: fixture,
	}
	fixture.issuer = &globalNetworkReleaseLowPortIssuer{
		fixture: fixture,
	}
	fixture.coordinator = NewGlobalNetworkReleaseCoordinator(
		fixture.journal,
		base.source,
		base.projections,
		base.roots,
		base.ownership,
		low,
		fixture.plans,
		func() (GlobalNetworkReleaseLowPortIssuer, error) {
			return fixture.issuer, nil
		},
		globalNetworkReleaseUnavailableResolverPlans{},
		func() (GlobalNetworkReleaseResolverIssuer, error) {
			return nil, errors.New("unexpected release resolver issuer")
		},
		globalNetworkReleaseUnavailableTrustPlans{},
		func() (GlobalNetworkReleaseTrustIssuer, error) {
			return nil, errors.New("unexpected release trust issuer")
		},
		globalNetworkReleaseUnavailableLoopbackPlans{},
		func() (GlobalNetworkReleaseLoopbackIssuer, error) {
			return nil, errors.New("unexpected release loopback issuer")
		},
		globalNetworkReleaseUnavailableOwnershipPlans{},
		func() (GlobalNetworkReleaseOwnershipIssuer, error) {
			return nil, errors.New("unexpected release ownership issuer")
		},
		globalNetworkReleaseUnavailableOwnershipProofObserver{},
		base.resolver,
		base.trust,
		base.loopback,
		base.runtimeRelease,
		base.coordinator.platform,
		fixture.clock,
	)
	return fixture
}

// prepareRequest returns the fixture owner's exact low-port publication request.
func (fixture *globalNetworkReleaseLowPortFixture) prepareRequest() GlobalNetworkReleasePrepareLowPortsRequest {
	return GlobalNetworkReleasePrepareLowPortsRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
	}
}

// absentObservation derives complete native absence from the fixed request's exact observation.
func (fixture *globalNetworkReleaseLowPortFixture) absentObservation() lowport.Observation {
	observation := fixture.base.low.observation
	observation.Artifacts = append([]lowport.Artifact(nil), observation.Artifacts...)
	for index := range observation.Artifacts {
		observation.Artifacts[index].Present = false
		observation.Artifacts[index].Owned = false
		observation.Artifacts[index].Exact = false
		observation.Artifacts[index].Ambiguous = false
	}
	return observation
}

// absentEvidence binds helper evidence to the independently observed absent service.
func (fixture *globalNetworkReleaseLowPortFixture) absentEvidence(t *testing.T) helper.LowPortMutationEvidence {
	t.Helper()
	fingerprint, err := fixture.low.observation.Fingerprint()
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
	return helper.LowPortMutationEvidence{
		PolicyFingerprint:      policy,
		OwnershipFingerprint:   owner,
		ObservationFingerprint: fingerprint,
		Postcondition:          helper.LowPortPostconditionOwnedAbsent,
	}
}

// globalNetworkReleaseLowPortPlans resolves the fixture plan only at the release checkpoint.
type globalNetworkReleaseLowPortPlans struct {
	fixture           *globalNetworkReleaseLowPortFixture
	distinctStartedAt bool
}

// Resolve returns the exact release-low-ports plan derived from durable fixture authority.
func (source *globalNetworkReleaseLowPortPlans) Resolve(_ context.Context, request ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
	plan := source.fixture.journal.plan
	if request.OperationID != plan.Operation.Operation.ID || plan.Phase != state.GlobalNetworkReleasePlanPhaseLowPorts {
		return ticketissuer.LowPortPlan{}, errors.New("release plan unavailable")
	}
	native, err := lowport.NewRequest(plan.Authority.Projection.ConfirmedOwnership.Record, plan.Authority.Policy)
	if err != nil {
		return ticketissuer.LowPortPlan{}, err
	}
	operation := plan.Operation.Operation
	if source.distinctStartedAt && operation.StartedAt != nil {
		startedAt := *operation.StartedAt
		operation.StartedAt = &startedAt
	}
	return ticketissuer.LowPortPlan{
		Purpose:            ticketissuer.LowPortPlanPurposeGlobalNetworkRelease,
		Operation:          operation,
		OperationRevision:  plan.Operation.Revision,
		CheckpointRevision: plan.CheckpointRevision,
		CheckpointPhase:    ticketissuer.LowPortCheckpointPhaseGlobalRelease,
		Mutation:           helper.OperationReleaseLowPorts,
		TargetOwnership:    plan.Authority.Projection.ConfirmedOwnership.Record,
		Policy:             plan.Authority.Policy,
		NativeRequest:      native,
	}, nil
}

// globalNetworkReleaseLowPortObserver returns controllable independent native facts.
type globalNetworkReleaseLowPortObserver struct {
	observation lowport.Observation
}

// Observe returns the configured facts for the supplied request.
func (observer *globalNetworkReleaseLowPortObserver) Observe(_ context.Context, request lowport.Request) (lowport.Observation, error) {
	observation := observer.observation
	observation.Request = request
	return observation, nil
}

// globalNetworkReleaseLowPortIssuer records bounded capability publication and closure.
type globalNetworkReleaseLowPortIssuer struct {
	fixture *globalNetworkReleaseLowPortFixture
	issues  int
	closes  int
}

// Issue returns a correlated bounded release capability.
func (issuer *globalNetworkReleaseLowPortIssuer) Issue(_ context.Context, _ string, request ticketissuer.LowPortRequest) (ticketissuer.LowPortResult, error) {
	issuer.issues++
	policy, _ := issuer.fixture.plan.Authority.Policy.Fingerprint()
	owner, _ := issuer.fixture.plan.Authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	return ticketissuer.LowPortResult{
		OperationID:            request.OperationID,
		Reference:              helper.TicketReference(strings.Repeat("a", 64)),
		Operation:              helper.OperationReleaseLowPorts,
		PolicyFingerprint:      policy,
		OwnershipFingerprint:   owner,
		ObservationFingerprint: strings.Repeat("b", 64),
		ExpiresAt:              issuer.fixture.clock.now.Add(time.Minute),
	}, nil
}

// Close records issuer cleanup.
func (issuer *globalNetworkReleaseLowPortIssuer) Close() error {
	issuer.closes++
	return nil
}

// globalNetworkReleaseLowPortJournal records the exact durable low-port receipt transition.
type globalNetworkReleaseLowPortJournal struct {
	plan     state.GlobalNetworkReleasePlanRecord
	advances int
}

// OperationByIntent is unused by focused low-port tests.
func (*globalNetworkReleaseLowPortJournal) OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// StageGlobalNetworkRelease is unused by focused low-port tests.
func (*globalNetworkReleaseLowPortJournal) StageGlobalNetworkRelease(context.Context, state.StageGlobalNetworkReleaseRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// ReadActiveGlobalNetworkReleasePlan is unused by focused low-port tests.
func (*globalNetworkReleaseLowPortJournal) ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	return state.GlobalNetworkReleasePlanRecord{}, false, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseResolver is unused by focused low-port tests.
func (*globalNetworkReleaseLowPortJournal) AdvanceGlobalNetworkReleaseResolver(context.Context, state.AdvanceGlobalNetworkReleaseResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected resolver advance")
}

// AdvanceGlobalNetworkReleaseTrust is unused by focused low-port tests.
func (*globalNetworkReleaseLowPortJournal) AdvanceGlobalNetworkReleaseTrust(context.Context, state.AdvanceGlobalNetworkReleaseTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected trust advance")
}

// AdvanceGlobalNetworkReleaseLoopbacks is unused by focused low-port tests.
func (*globalNetworkReleaseLowPortJournal) AdvanceGlobalNetworkReleaseLoopbacks(context.Context, state.AdvanceGlobalNetworkReleaseLoopbacksRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseEffects is unused by focused low-port tests.
func (*globalNetworkReleaseLowPortJournal) AdvanceGlobalNetworkReleaseEffects(
	context.Context,
	state.AdvanceGlobalNetworkReleaseEffectsRequest,
) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// ReadGlobalNetworkReleasePlan returns the fixture's active release plan.
func (journal *globalNetworkReleaseLowPortJournal) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	if journal.plan.Operation.Operation.ID != operationID {
		return state.GlobalNetworkReleasePlanRecord{}, false, nil
	}
	return journal.plan, true, nil
}

// AdvanceGlobalNetworkReleaseLowPorts persists or replays only one exact receipt.
func (journal *globalNetworkReleaseLowPortJournal) AdvanceGlobalNetworkReleaseLowPorts(_ context.Context, request state.AdvanceGlobalNetworkReleaseLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	journal.advances++
	if journal.plan.Phase == state.GlobalNetworkReleasePlanPhaseResolver {
		if journal.plan.LowPortReceipt == nil || request.Receipt != *journal.plan.LowPortReceipt {
			return state.GlobalNetworkReleasePlanRecord{}, errors.New("replay receipt differs")
		}
		return journal.plan, nil
	}
	if request.CheckpointRevision != journal.plan.CheckpointRevision {
		return state.GlobalNetworkReleasePlanRecord{}, errors.New("checkpoint differs")
	}
	journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseResolver
	journal.plan.LowPortReceipt = &request.Receipt
	journal.plan.CheckpointRevision++
	return journal.plan, nil
}
