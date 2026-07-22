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
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/state"
)

// TestGlobalNetworkReleaseTrustPrepareBindsOwnedTicket proves publication is limited to the exact retained owner and checkpoint.
func TestGlobalNetworkReleaseTrustPrepareBindsOwnedTicket(t *testing.T) {
	fixture := newGlobalNetworkReleaseTrustFixture(t)
	wantRequest := ticketissuer.TrustRequest{
		OperationID: fixture.prepareRequest().OperationID,
	}
	got, err := fixture.coordinator.PrepareTrust(t.Context(), fixture.prepareRequest())
	if err != nil || got.Disposition != state.GlobalNetworkReleaseTrustOwned || got.Ticket == nil ||
		!reflect.DeepEqual(*got.Ticket, fixture.issuer.result()) || fixture.issuer.issues != 1 || fixture.issuer.closes != 1 ||
		fixture.issuer.requester != fixture.prepareRequest().RequesterIdentity ||
		fixture.issuer.request != wantRequest {
		t.Fatalf("PrepareTrust() = %#v, %v; issues/closes = %d/%d", got, err, fixture.issuer.issues, fixture.issuer.closes)
	}
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseTrustFixture, *GlobalNetworkReleasePrepareTrustRequest)
	}{
		{
			name: "stale",
			mutate: func(_ *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleasePrepareTrustRequest) {
				request.ExpectedCheckpointRevision++
			},
		},
		{
			name: "requester",
			mutate: func(_ *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleasePrepareTrustRequest) {
				request.RequesterIdentity = "other"
			},
		},
		{
			name: "canceled",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleasePrepareTrustRequest) {
				fixture.canceled = true
			},
		},
		{
			name: "policy drift",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleasePrepareTrustRequest) {
				fixture.plans.drift = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseTrustFixture(t)
			request := fixture.prepareRequest()
			test.mutate(fixture, &request)
			ctx := t.Context()
			if fixture.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := fixture.coordinator.PrepareTrust(ctx, request); err == nil || fixture.issuer.issues != 0 || fixture.issuer.closes != 0 {
				t.Fatalf("PrepareTrust() error = %v; issues/closes = %d/%d", err, fixture.issuer.issues, fixture.issuer.closes)
			}
		})
	}
}

// TestGlobalNetworkReleaseTrustPreparePreexistingNeverIssues proves preservation has no helper capability lifecycle.
func TestGlobalNetworkReleaseTrustPreparePreexistingNeverIssues(t *testing.T) {
	fixture := newGlobalNetworkReleaseTrustFixture(t)
	fixture.journal.plan.Authority.TrustDisposition = state.GlobalNetworkReleaseTrustPreexistingUnowned
	got, err := fixture.coordinator.PrepareTrust(t.Context(), fixture.prepareRequest())
	if err != nil || got.Disposition != state.GlobalNetworkReleaseTrustPreexistingUnowned || got.Ticket != nil ||
		fixture.opens != 0 || fixture.plans.calls != 0 || fixture.issuer.issues != 0 || fixture.issuer.closes != 0 {
		t.Fatalf("PrepareTrust() = %#v, %v; opens/plans/issues/closes = %d/%d/%d/%d", got, err, fixture.opens, fixture.plans.calls, fixture.issuer.issues, fixture.issuer.closes)
	}
}

// TestGlobalNetworkReleaseTrustPrepareIssuerLifecycle preserves only valid indeterminate results and closes every opened issuer.
func TestGlobalNetworkReleaseTrustPrepareIssuerLifecycle(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*globalNetworkReleaseTrustFixture)
		wantTicket bool
		wantClose  int
		wantError  string
	}{
		{
			name: "opener error",
			configure: func(fixture *globalNetworkReleaseTrustFixture) {
				fixture.openErr = errors.New("open")
			},
			wantError: "open",
		},
		{
			name: "nil issuer",
			configure: func(fixture *globalNetworkReleaseTrustFixture) {
				fixture.nilIssuer = true
			},
			wantError: "issuer is nil",
		},
		{
			name: "issue error",
			configure: func(fixture *globalNetworkReleaseTrustFixture) {
				fixture.issuer.issueErr = errors.New("issue")
			},
			wantClose: 1,
			wantError: "issue",
		},
		{
			name: "close after success",
			configure: func(fixture *globalNetworkReleaseTrustFixture) {
				fixture.issuer.closeErr = errors.New("close")
			},
			wantTicket: true,
			wantClose:  1,
			wantError:  ticketissuer.ErrTrustPublicationIndeterminate.Error(),
		},
		{
			name: "indeterminate issue and close",
			configure: func(fixture *globalNetworkReleaseTrustFixture) {
				fixture.issuer.issueErr = ticketissuer.ErrTrustPublicationIndeterminate
				fixture.issuer.closeErr = errors.New("close")
			},
			wantTicket: true,
			wantClose:  1,
			wantError:  ticketissuer.ErrTrustPublicationIndeterminate.Error(),
		},
		{
			name: "indeterminate malformed result",
			configure: func(fixture *globalNetworkReleaseTrustFixture) {
				fixture.issuer.issueErr = ticketissuer.ErrTrustPublicationIndeterminate
				fixture.issuer.mutateResult = func(result *ticketissuer.TrustResult) {
					result.AuthorityFingerprint = strings.Repeat("a", 64)
				}
			},
			wantClose: 1,
			wantError: "another authority",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseTrustFixture(t)
			test.configure(fixture)
			got, err := fixture.coordinator.PrepareTrust(t.Context(), fixture.prepareRequest())
			if err == nil || !strings.Contains(err.Error(), test.wantError) || (got.Ticket != nil) != test.wantTicket || fixture.issuer.closes != test.wantClose {
				t.Fatalf("PrepareTrust() = %#v, %v; closes = %d", got, err, fixture.issuer.closes)
			}
		})
	}
}

// TestGlobalNetworkReleaseTrustConfirmOwnedAdvancesAndReplays proves independent absence advances once and uses its retained time on replay.
func TestGlobalNetworkReleaseTrustConfirmOwnedAdvancesAndReplays(t *testing.T) {
	fixture := newGlobalNetworkReleaseTrustFixture(t)
	fixture.observer.observation = fixture.absentObservation(t)
	request := fixture.confirmRequest(t)
	advanced, err := fixture.coordinator.ConfirmTrust(t.Context(), request)
	if err != nil || advanced.Phase != state.GlobalNetworkReleasePlanPhaseLoopbacks || advanced.TrustReceipt == nil || advanced.TrustReceipt.VerifiedAt != fixture.plan.ResolverReceipt.VerifiedAt {
		t.Fatalf("ConfirmTrust() = %#v, %v", advanced, err)
	}
	fixture.clock.now = fixture.clock.now.Add(time.Hour)
	replayed, err := fixture.coordinator.ConfirmTrust(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replayed, advanced) || fixture.journal.advances != 2 {
		t.Fatalf("replay = %#v, %v; advances = %d", replayed, err, fixture.journal.advances)
	}
	request.TrustEvidence.Changed = true
	if _, err := fixture.coordinator.ConfirmTrust(t.Context(), request); err == nil {
		t.Fatal("ConfirmTrust() accepted altered replay evidence")
	}
}

// TestGlobalNetworkReleaseTrustCancellationPreventsDependencies proves canceled confirmation has no observation or durable side effect.
func TestGlobalNetworkReleaseTrustCancellationPreventsDependencies(t *testing.T) {
	fixture := newGlobalNetworkReleaseTrustFixture(t)
	fixture.observer.observation = fixture.absentObservation(t)
	request := fixture.confirmRequest(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.coordinator.ConfirmTrust(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConfirmTrust() error = %v", err)
	}
	if fixture.observer.calls != 0 || fixture.journal.advances != 0 {
		t.Fatalf("observer calls/advances = %d/%d", fixture.observer.calls, fixture.journal.advances)
	}
}

// TestGlobalNetworkReleaseTrustRejectsForgedDurableRecords proves malformed retained boundaries cannot open issuers, observe native state, or advance.
func TestGlobalNetworkReleaseTrustRejectsForgedDurableRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "checkpoint at operation revision",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.CheckpointRevision = plan.Operation.Revision
			},
		},
		{
			name: "network boundary drift",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.NetworkRevision++
			},
		},
		{
			name: "invalid low port receipt",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.LowPortReceipt.LowPortEvidenceDigest = "invalid"
			},
		},
		{
			name: "misordered predecessor checkpoints",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.ResolverReceipt.SourceCheckpointRevision = plan.LowPortReceipt.SourceCheckpointRevision
			},
		},
		{
			name: "predecessor time before network",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.ResolverReceipt.VerifiedAt = plan.NetworkUpdatedAt.Add(-time.Second)
			},
		},
		{
			name: "premature trust receipt",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.TrustReceipt = &state.GlobalNetworkReleaseTrustReceipt{
					SourceCheckpointRevision: plan.CheckpointRevision,
					Disposition:              plan.Authority.TrustDisposition,
					ConfirmationDigest:       strings.Repeat("a", 64),
					ObservationFingerprint:   strings.Repeat("b", 64),
					VerifiedAt:               plan.ResolverReceipt.VerifiedAt,
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseTrustFixture(t)
			test.mutate(&fixture.journal.plan)
			fixture.observer.observation = fixture.absentObservation(t)
			if _, err := fixture.coordinator.PrepareTrust(t.Context(), fixture.prepareRequest()); err == nil {
				t.Fatal("PrepareTrust() error = nil")
			}
			if _, err := fixture.coordinator.ConfirmTrust(t.Context(), fixture.confirmRequest(t)); err == nil {
				t.Fatal("ConfirmTrust() error = nil")
			}
			if fixture.opens != 0 || fixture.plans.calls != 0 || fixture.observer.calls != 0 || fixture.journal.advances != 0 {
				t.Fatalf("opens/plans/observations/advances = %d/%d/%d/%d", fixture.opens, fixture.plans.calls, fixture.observer.calls, fixture.journal.advances)
			}
		})
	}
}

// TestGlobalNetworkReleaseTrustAllowsEarlierCheckpointGaps preserves state-compatible global sequence interleaving.
func TestGlobalNetworkReleaseTrustAllowsEarlierCheckpointGaps(t *testing.T) {
	fixture := newGlobalNetworkReleaseTrustFixture(t)
	fixture.plan.CheckpointRevision = 15
	fixture.journal.plan.CheckpointRevision = 15
	fixture.journal.plan.ResolverReceipt.SourceCheckpointRevision = 14
	fixture.observer.observation = fixture.absentObservation(t)
	prepare := fixture.prepareRequest()
	if _, err := fixture.coordinator.PrepareTrust(t.Context(), prepare); err != nil {
		t.Fatalf("PrepareTrust() error = %v", err)
	}
	confirm := fixture.confirmRequest(t)
	if _, err := fixture.coordinator.ConfirmTrust(t.Context(), confirm); err != nil {
		t.Fatalf("ConfirmTrust() error = %v", err)
	}
	if _, err := fixture.coordinator.ConfirmTrust(t.Context(), confirm); err != nil {
		t.Fatalf("ConfirmTrust() replay error = %v", err)
	}
}

// TestGlobalNetworkReleaseTrustRejectsForgedLoopbackRecords proves replay records retain the exact trust receipt ordering and disposition.
func TestGlobalNetworkReleaseTrustRejectsForgedLoopbackRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "missing trust receipt",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.TrustReceipt = nil
			},
		},
		{
			name: "wrong disposition",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.TrustReceipt.Disposition = state.GlobalNetworkReleaseTrustPreexistingUnowned
			},
		},
		{
			name: "loopback checkpoint mismatch",
			mutate: func(plan *state.GlobalNetworkReleasePlanRecord) {
				plan.CheckpointRevision++
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseTrustFixture(t)
			fixture.observer.observation = fixture.absentObservation(t)
			request := fixture.confirmRequest(t)
			if _, err := fixture.coordinator.ConfirmTrust(t.Context(), request); err != nil {
				t.Fatalf("ConfirmTrust() error = %v", err)
			}
			test.mutate(&fixture.journal.plan)
			if _, err := fixture.coordinator.ConfirmTrust(t.Context(), request); err == nil {
				t.Fatal("ConfirmTrust() error = nil")
			}
			if fixture.observer.calls != 1 || fixture.journal.advances != 1 {
				t.Fatalf("observer calls/advances = %d/%d", fixture.observer.calls, fixture.journal.advances)
			}
		})
	}
}

// TestGlobalNetworkReleaseTrustConfirmRejectsUnsafeOwnedEvidence proves evidence and native facts must both bind exactly.
func TestGlobalNetworkReleaseTrustConfirmRejectsUnsafeOwnedEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseTrustFixture, *GlobalNetworkReleaseConfirmTrustRequest)
	}{
		{
			name: "authority",
			mutate: func(_ *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleaseConfirmTrustRequest) {
				request.TrustEvidence.AuthorityFingerprint = strings.Repeat("a", 64)
			},
		},
		{
			name: "mechanism",
			mutate: func(_ *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleaseConfirmTrustRequest) {
				request.TrustEvidence.Mechanism = "unsupported"
			},
		},
		{
			name: "fingerprint",
			mutate: func(_ *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleaseConfirmTrustRequest) {
				request.TrustEvidence.ObservationFingerprint = strings.Repeat("b", 64)
			},
		},
		{
			name: "postcondition",
			mutate: func(_ *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleaseConfirmTrustRequest) {
				request.TrustEvidence.Postcondition = helper.TrustPostconditionExact
			},
		},
		{
			name: "wrong native request",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleaseConfirmTrustRequest) {
				different := fixture.differentRequest(t)
				fixture.observer.wrongRequest = &different
			},
		},
		{
			name: "incomplete",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleaseConfirmTrustRequest) {
				fixture.observer.observation.Complete = false
			},
		},
		{
			name: "owned state",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleaseConfirmTrustRequest) {
				fixture.observer.observation = fixture.ownedObservation(t)
				fingerprint, err := fixture.observer.observation.Fingerprint()
				if err != nil {
					t.Fatal(err)
				}
				request.TrustEvidence.ObservationFingerprint = fingerprint
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseTrustFixture(t)
			fixture.observer.observation = fixture.absentObservation(t)
			request := fixture.confirmRequest(t)
			test.mutate(fixture, &request)
			if _, err := fixture.coordinator.ConfirmTrust(t.Context(), request); err == nil || fixture.journal.advances != 0 {
				t.Fatalf("ConfirmTrust() error = %v; advances = %d", err, fixture.journal.advances)
			}
		})
	}
}

// TestGlobalNetworkReleaseTrustConfirmPreexistingRequiresFreshIdenticalRoot proves preservation constructs and replays only coordinator-derived evidence.
func TestGlobalNetworkReleaseTrustConfirmPreexistingRequiresFreshIdenticalRoot(t *testing.T) {
	fixture := newGlobalNetworkReleaseTrustFixture(t)
	fixture.setPreexisting(t)
	request := fixture.confirmRequest(t)
	request.TrustEvidence = helper.TrustMutationEvidence{}
	advanced, err := fixture.coordinator.ConfirmTrust(t.Context(), request)
	if err != nil || advanced.TrustReceipt == nil || advanced.TrustReceipt.ConfirmationDigest == "" || advanced.TrustReceipt.VerifiedAt != fixture.plan.ResolverReceipt.VerifiedAt {
		t.Fatalf("ConfirmTrust(preexisting) = %#v, %v", advanced, err)
	}
	if _, err := fixture.coordinator.ConfirmTrust(t.Context(), request); err != nil || fixture.journal.advances != 2 {
		t.Fatalf("ConfirmTrust(preexisting replay) error = %v; advances = %d", err, fixture.journal.advances)
	}
	for _, test := range []struct {
		name   string
		mutate func(*globalNetworkReleaseTrustFixture, *GlobalNetworkReleaseConfirmTrustRequest)
	}{
		{
			name: "caller evidence",
			mutate: func(_ *globalNetworkReleaseTrustFixture, request *GlobalNetworkReleaseConfirmTrustRequest) {
				request.TrustEvidence.Postcondition = helper.TrustPostconditionPreexisting
			},
		},
		{
			name: "owned",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleaseConfirmTrustRequest) {
				fixture.observer.observation = fixture.ownedObservation(t)
			},
		},
		{
			name: "drifted",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleaseConfirmTrustRequest) {
				fixture.observer.observation = fixture.foreignObservation(t)
				fixture.observer.observation.Entries[0].NativeExact = false
			},
		},
		{
			name: "missing",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleaseConfirmTrustRequest) {
				fixture.observer.observation = fixture.absentObservation(t)
			},
		},
		{
			name: "malformed",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleaseConfirmTrustRequest) {
				fixture.observer.observation = fixture.foreignObservation(t)
				fixture.observer.observation.Complete = false
			},
		},
		{
			name: "fresh fingerprint drift",
			mutate: func(fixture *globalNetworkReleaseTrustFixture, _ *GlobalNetworkReleaseConfirmTrustRequest) {
				fixture.observer.observation = fixture.foreignObservation(t)
				fixture.observer.observation.Entries = append(
					fixture.observer.observation.Entries,
					trust.Entry{
						Mechanism:              fixture.observer.observation.Request.Mechanism(),
						NativeID:               "unrelated-root",
						CertificateFingerprint: strings.Repeat("d", 64),
						NativeExact:            true,
						NativeAttributesSHA256: strings.Repeat("e", 64),
					},
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGlobalNetworkReleaseTrustFixture(t)
			fixture.setPreexisting(t)
			request := fixture.confirmRequest(t)
			request.TrustEvidence = helper.TrustMutationEvidence{}
			test.mutate(fixture, &request)
			if _, err := fixture.coordinator.ConfirmTrust(t.Context(), request); err == nil || fixture.journal.advances != 0 {
				t.Fatalf("ConfirmTrust() error = %v; advances = %d", err, fixture.journal.advances)
			}
		})
	}
}

// globalNetworkReleaseTrustFixture supplies a retained trust checkpoint with all predecessor receipts committed.
type globalNetworkReleaseTrustFixture struct {
	base        *globalNetworkReleaseStartFixture
	coordinator *GlobalNetworkReleaseCoordinator
	journal     *globalNetworkReleaseTrustJournal
	plans       *globalNetworkReleaseTrustPlans
	issuer      *globalNetworkReleaseTrustIssuer
	observer    *globalNetworkReleaseTrustObserver
	clock       *globalNetworkReleaseClock
	plan        state.GlobalNetworkReleasePlanRecord
	opens       int
	canceled    bool
	openErr     error
	nilIssuer   bool
}

// newGlobalNetworkReleaseTrustFixture constructs the exact trust release boundary.
func newGlobalNetworkReleaseTrustFixture(t *testing.T) *globalNetworkReleaseTrustFixture {
	t.Helper()
	base := newGlobalNetworkReleaseStartFixture(t)
	base.tStageAuthority()
	plan := base.journal.plan
	plan.Phase = state.GlobalNetworkReleasePlanPhaseTrust
	plan.CheckpointRevision = 14
	plan.LowPortReceipt = &state.GlobalNetworkReleaseLowPortReceipt{
		SourceCheckpointRevision:          12,
		LowPortEvidenceDigest:             strings.Repeat("d", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("e", 64),
		VerifiedAt:                        base.clock.now,
	}
	plan.ResolverReceipt = &state.GlobalNetworkReleaseResolverReceipt{
		SourceCheckpointRevision:          13,
		ResolverEvidenceDigest:            strings.Repeat("f", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("a", 64),
		VerifiedAt:                        base.clock.now,
	}
	fixture := &globalNetworkReleaseTrustFixture{
		base:  base,
		clock: base.clock,
		plan:  plan,
	}
	fixture.journal = &globalNetworkReleaseTrustJournal{
		plan: plan,
	}
	fixture.plans = &globalNetworkReleaseTrustPlans{
		fixture: fixture,
	}
	fixture.issuer = &globalNetworkReleaseTrustIssuer{
		fixture: fixture,
	}
	fixture.observer = &globalNetworkReleaseTrustObserver{}
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
		fixture.plans,
		func() (GlobalNetworkReleaseTrustIssuer, error) {
			fixture.opens++
			if fixture.openErr != nil {
				return nil, fixture.openErr
			}
			if fixture.nilIssuer {
				return nil, nil
			}
			return fixture.issuer, nil
		},
		globalNetworkReleaseUnavailableLoopbackPlans{},
		func() (GlobalNetworkReleaseLoopbackIssuer, error) {
			return nil, errors.New("unexpected release loopback issuer")
		},
		base.resolver,
		fixture.observer,
		base.loopback,
		base.runtimeRelease,
		base.coordinator.platform,
		fixture.clock,
	)
	return fixture
}

// prepareRequest returns the fixture owner's retained publication request.
func (fixture *globalNetworkReleaseTrustFixture) prepareRequest() GlobalNetworkReleasePrepareTrustRequest {
	return GlobalNetworkReleasePrepareTrustRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
	}
}

// confirmRequest derives caller evidence from the current native observation.
func (fixture *globalNetworkReleaseTrustFixture) confirmRequest(t *testing.T) GlobalNetworkReleaseConfirmTrustRequest {
	t.Helper()
	fingerprint, err := fixture.observer.observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return GlobalNetworkReleaseConfirmTrustRequest{
		OperationID:                fixture.plan.Operation.Operation.ID,
		ExpectedCheckpointRevision: fixture.plan.CheckpointRevision,
		RequesterIdentity:          fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		TrustEvidence: helper.TrustMutationEvidence{
			AuthorityFingerprint:   fixture.plan.Authority.Root.Fingerprint,
			Mechanism:              fixture.plan.Authority.Policy.Mechanisms.Trust,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.TrustPostconditionOwnedAbsent,
		},
	}
}

// absentObservation returns a complete native absence observation.
func (fixture *globalNetworkReleaseTrustFixture) absentObservation(t *testing.T) trust.Observation {
	t.Helper()
	request, err := trust.NewRequestForRequester(
		fixture.plan.Authority.Projection.ConfirmedOwnership.Record.InstallationID,
		fixture.plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity,
		fixture.plan.Authority.Policy.Mechanisms.Trust,
		fixture.plan.Authority.Root,
	)
	if err != nil {
		t.Fatal(err)
	}
	return trust.Observation{
		Request:  request,
		Complete: true,
	}
}

// differentRequest returns a valid request for another authenticated owner.
func (fixture *globalNetworkReleaseTrustFixture) differentRequest(t *testing.T) trust.Request {
	t.Helper()
	request, err := trust.NewRequestForRequester(
		fixture.plan.Authority.Projection.ConfirmedOwnership.Record.InstallationID,
		"other-owner",
		fixture.plan.Authority.Policy.Mechanisms.Trust,
		fixture.plan.Authority.Root,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// ownedObservation returns one exact Harbor-owned entry.
func (fixture *globalNetworkReleaseTrustFixture) ownedObservation(t *testing.T) trust.Observation {
	t.Helper()
	observation := fixture.foreignObservation(t)
	owner := observation.Request.OwnerMarker()
	observation.Entries[0].Owner = &owner
	return observation
}

// foreignObservation returns one identical unowned entry.
func (fixture *globalNetworkReleaseTrustFixture) foreignObservation(t *testing.T) trust.Observation {
	t.Helper()
	observation := fixture.absentObservation(t)
	observation.Entries = []trust.Entry{{
		Mechanism:              observation.Request.Mechanism(),
		NativeID:               "foreign-root",
		CertificateFingerprint: observation.Request.AuthorityFingerprint(),
		NativeExact:            true,
		NativeAttributesSHA256: strings.Repeat("c", 64),
	}}
	return observation
}

// setPreexisting stages a preserved root with the fingerprint observed at staging.
func (fixture *globalNetworkReleaseTrustFixture) setPreexisting(t *testing.T) {
	t.Helper()
	observation := fixture.foreignObservation(t)
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fixture.journal.plan.Authority.TrustDisposition = state.GlobalNetworkReleaseTrustPreexistingUnowned
	fixture.journal.plan.Authority.TrustObservationFingerprint = fingerprint
	fixture.observer.observation = observation
}

// globalNetworkReleaseTrustPlans resolves exact retained owned authority.
type globalNetworkReleaseTrustPlans struct {
	fixture *globalNetworkReleaseTrustFixture
	drift   bool
	calls   int
}

// Resolve returns the fixture's immutable trust plan.
func (source *globalNetworkReleaseTrustPlans) Resolve(_ context.Context, request ticketissuer.TrustRequest) (ticketissuer.TrustPlan, error) {
	source.calls++
	plan := source.fixture.journal.plan
	if request.OperationID != plan.Operation.Operation.ID {
		return ticketissuer.TrustPlan{}, errors.New("unavailable")
	}
	policy := plan.Authority.Policy
	if source.drift {
		policy.Suffix = ".drift.test"
	}
	return ticketissuer.TrustPlan{
		Purpose:            ticketissuer.TrustPlanPurposeGlobalNetworkRelease,
		Operation:          plan.Operation.Operation,
		OperationRevision:  plan.Operation.Revision,
		CheckpointRevision: plan.CheckpointRevision,
		CheckpointPhase:    ticketissuer.TrustCheckpointPhaseGlobalRelease,
		Mutation:           helper.OperationReleaseTrust,
		TargetOwnership:    plan.Authority.Projection.ConfirmedOwnership.Record,
		Policy:             policy,
		Root:               plan.Authority.Root,
	}, nil
}

// globalNetworkReleaseTrustIssuer records trust issuance and cleanup.
type globalNetworkReleaseTrustIssuer struct {
	fixture      *globalNetworkReleaseTrustFixture
	issues       int
	closes       int
	issueErr     error
	closeErr     error
	requester    string
	request      ticketissuer.TrustRequest
	mutateResult func(*ticketissuer.TrustResult)
}

// Issue returns the exact release ticket.
func (issuer *globalNetworkReleaseTrustIssuer) Issue(_ context.Context, requester string, request ticketissuer.TrustRequest) (ticketissuer.TrustResult, error) {
	issuer.issues++
	issuer.requester = requester
	issuer.request = request
	result := issuer.result()
	if issuer.mutateResult != nil {
		issuer.mutateResult(&result)
	}
	return result, issuer.issueErr
}

// Close records cleanup.
func (issuer *globalNetworkReleaseTrustIssuer) Close() error {
	issuer.closes++
	return issuer.closeErr
}

// result derives the ticket from durable authority.
func (issuer *globalNetworkReleaseTrustIssuer) result() ticketissuer.TrustResult {
	plan := issuer.fixture.journal.plan
	policy, _ := plan.Authority.Policy.Fingerprint()
	owner, _ := plan.Authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	return ticketissuer.TrustResult{
		OperationID:          plan.Operation.Operation.ID,
		Reference:            helper.TicketReference(strings.Repeat("b", 64)),
		Operation:            helper.OperationReleaseTrust,
		PolicyFingerprint:    policy,
		OwnershipFingerprint: owner,
		AuthorityFingerprint: plan.Authority.Root.Fingerprint,
		Mechanism:            plan.Authority.Policy.Mechanisms.Trust,
		ExpiresAt:            issuer.fixture.clock.now.Add(time.Minute),
	}
}

// globalNetworkReleaseTrustObserver supplies independent native observations.
type globalNetworkReleaseTrustObserver struct {
	observation  trust.Observation
	wrongRequest *trust.Request
	calls        int
}

// Observe returns the scripted observation.
func (observer *globalNetworkReleaseTrustObserver) Observe(_ context.Context, request trust.Request) (trust.Observation, error) {
	observation := observer.observation
	observer.calls++
	if observer.wrongRequest != nil {
		observation.Request = *observer.wrongRequest
	}
	return observation, nil
}

// globalNetworkReleaseTrustJournal persists and replays only exact trust receipts.
type globalNetworkReleaseTrustJournal struct {
	plan     state.GlobalNetworkReleasePlanRecord
	advances int
}

// OperationByIntent is unused by trust checkpoint tests.
func (*globalNetworkReleaseTrustJournal) OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// StageGlobalNetworkRelease is unused by trust checkpoint tests.
func (*globalNetworkReleaseTrustJournal) StageGlobalNetworkRelease(context.Context, state.StageGlobalNetworkReleaseRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected")
}

// ReadActiveGlobalNetworkReleasePlan is unused by trust checkpoint tests.
func (*globalNetworkReleaseTrustJournal) ReadActiveGlobalNetworkReleasePlan(context.Context) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	return state.GlobalNetworkReleasePlanRecord{}, false, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseLowPorts is unused by trust checkpoint tests.
func (*globalNetworkReleaseTrustJournal) AdvanceGlobalNetworkReleaseLowPorts(context.Context, state.AdvanceGlobalNetworkReleaseLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// AdvanceGlobalNetworkReleaseResolver is unused by trust checkpoint tests.
func (*globalNetworkReleaseTrustJournal) AdvanceGlobalNetworkReleaseResolver(context.Context, state.AdvanceGlobalNetworkReleaseResolverRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}

// ReadGlobalNetworkReleasePlan returns the retained plan.
func (journal *globalNetworkReleaseTrustJournal) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error) {
	if operationID != journal.plan.Operation.Operation.ID {
		return state.GlobalNetworkReleasePlanRecord{}, false, nil
	}
	return journal.plan, true, nil
}

// AdvanceGlobalNetworkReleaseTrust persists the exact trust receipt or replays it.
func (journal *globalNetworkReleaseTrustJournal) AdvanceGlobalNetworkReleaseTrust(_ context.Context, request state.AdvanceGlobalNetworkReleaseTrustRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	journal.advances++
	if journal.plan.Phase == state.GlobalNetworkReleasePlanPhaseLoopbacks {
		if journal.plan.TrustReceipt == nil || request.Receipt != *journal.plan.TrustReceipt {
			return state.GlobalNetworkReleasePlanRecord{}, errors.New("replay differs")
		}
		return journal.plan, nil
	}
	journal.plan.Phase = state.GlobalNetworkReleasePlanPhaseLoopbacks
	journal.plan.TrustReceipt = &request.Receipt
	journal.plan.CheckpointRevision++
	return journal.plan, nil
}

// AdvanceGlobalNetworkReleaseLoopbacks is unused by trust checkpoint tests.
func (*globalNetworkReleaseTrustJournal) AdvanceGlobalNetworkReleaseLoopbacks(context.Context, state.AdvanceGlobalNetworkReleaseLoopbacksRequest) (state.GlobalNetworkReleasePlanRecord, error) {
	return state.GlobalNetworkReleasePlanRecord{}, errors.New("unexpected")
}
