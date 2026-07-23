package ticketissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	platformtrust "github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/localca"
)

// scriptedTrustPlanSource returns one trust plan or failure per durable read.
type scriptedTrustPlanSource struct {
	plans    []TrustPlan
	errors   []error
	requests []TrustRequest
}

// Resolve records the selected operation and returns the next scripted result.
func (source *scriptedTrustPlanSource) Resolve(_ context.Context, request TrustRequest) (TrustPlan, error) {
	index := len(source.requests)
	source.requests = append(source.requests, request)
	if index < len(source.errors) && source.errors[index] != nil {
		return TrustPlan{}, source.errors[index]
	}
	if len(source.plans) == 0 {
		return TrustPlan{}, errors.New("trust plan script is empty")
	}
	if index >= len(source.plans) {
		index = len(source.plans) - 1
	}
	return source.plans[index], nil
}

// scriptedTrustObserver returns one native trust observation or failure per read.
type scriptedTrustObserver struct {
	observations []platformtrust.Observation
	errors       []error
	requests     []platformtrust.Request
}

// Observe records the exact request and returns the next scripted native result.
func (observer *scriptedTrustObserver) Observe(_ context.Context, request platformtrust.Request) (platformtrust.Observation, error) {
	index := len(observer.requests)
	observer.requests = append(observer.requests, request)
	if index < len(observer.errors) && observer.errors[index] != nil {
		return platformtrust.Observation{}, observer.errors[index]
	}
	if len(observer.observations) == 0 {
		return platformtrust.Observation{}, errors.New("trust observation script is empty")
	}
	if index >= len(observer.observations) {
		index = len(observer.observations) - 1
	}
	return observer.observations[index], nil
}

// scriptedTrustClock returns one instant per call and then repeats the final instant.
type scriptedTrustClock struct {
	times []time.Time
	calls int
}

// Now returns the next scripted instant.
func (clock *scriptedTrustClock) Now() time.Time {
	index := clock.calls
	clock.calls++
	if index >= len(clock.times) {
		index = len(clock.times) - 1
	}
	return clock.times[index]
}

// trustIssuerFixture contains one valid trust approval and every replaceable authority boundary.
type trustIssuerFixture struct {
	now         time.Time
	plan        TrustPlan
	request     TrustRequest
	private     ed25519.PrivateKey
	plans       *scriptedTrustPlanSource
	ownership   *scriptedOwnershipObserver
	keys        *staticKeyLoader
	publisher   *capturingPublisher
	observer    *scriptedTrustObserver
	service     *TrustService
	observation platformtrust.Observation
}

// TestTrustServiceIssueBindsExactTrustAuthority proves every ticket and result field is policy correlated.
func TestTrustServiceIssueBindsExactTrustAuthority(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := result.Validate(fixture.now); err != nil {
		t.Fatalf("TrustResult.Validate() error = %v", err)
	}
	ownershipFingerprint, err := fixture.plan.TargetOwnership.Fingerprint()
	if err != nil {
		t.Fatalf("TargetOwnership.Fingerprint() error = %v", err)
	}
	if result.OperationID != fixture.plan.Operation.ID ||
		result.Reference != fixture.publisher.reference ||
		result.Operation != helper.OperationEnsureTrust ||
		result.PolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint ||
		result.OwnershipFingerprint != ownershipFingerprint ||
		result.AuthorityFingerprint != fixture.plan.Root.Fingerprint ||
		result.Mechanism != fixture.plan.Policy.Mechanisms.Trust ||
		result.ExpiresAt != fixture.now.Add(ticketLifetime) {
		t.Fatalf("Issue() result = %#v", result)
	}
	if len(fixture.plans.requests) != 2 || fixture.ownership.calls != 2 || fixture.keys.calls != 1 ||
		len(fixture.observer.requests) != 2 || fixture.publisher.calls != 1 {
		t.Fatalf(
			"Issue() calls = plans %d ownership %d keys %d trust %d publish %d",
			len(fixture.plans.requests),
			fixture.ownership.calls,
			fixture.keys.calls,
			len(fixture.observer.requests),
			fixture.publisher.calls,
		)
	}
	for index, request := range fixture.observer.requests {
		if !sameTrustRequest(request, fixture.observation.Request) {
			t.Fatalf("trust request %d does not match approved request", index)
		}
	}

	ticket := fixture.publisher.ticket
	if ticket.Operation != helper.OperationEnsureTrust ||
		ticket.InstallationID != fixture.plan.TargetOwnership.InstallationID ||
		ticket.RequesterIdentity != fixture.plan.TargetOwnership.OwnerIdentity ||
		ticket.OwnershipGeneration != fixture.plan.TargetOwnership.Generation ||
		ticket.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion ||
		ticket.NetworkPolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint ||
		ticket.ApprovedPool != fixture.plan.TargetOwnership.LoopbackPoolPrefix {
		t.Fatalf("published trust ownership = %#v", ticket)
	}
	if ticket.NetworkPolicy == nil || *ticket.NetworkPolicy != fixture.plan.Policy || ticket.TrustRoot == nil ||
		ticket.TrustRoot.Fingerprint != fixture.plan.Root.Fingerprint ||
		!bytes.Equal(ticket.TrustRoot.CertificatePEM, fixture.plan.Root.CertificatePEM) ||
		!ticket.TrustRoot.NotBefore.Equal(fixture.plan.Root.NotBefore) ||
		!ticket.TrustRoot.NotAfter.Equal(fixture.plan.Root.NotAfter) ||
		ticket.ExpectedTrustObservation == nil {
		t.Fatalf("published trust authority = %#v", ticket)
	}
	if ticket.ExpectedResolverObservation != nil || ticket.ExpectedLowPortObservation != nil ||
		ticket.ExpectedLoopbackPool != nil || ticket.ExpectedPreAssignment != nil ||
		ticket.ApprovedAddress != "" || ticket.ExpectedObservation != (helper.ExpectedObservation{}) {
		t.Fatalf("published trust ticket mixed authority = %#v", ticket)
	}
	wantObservationFingerprint, err := fixture.observation.Fingerprint()
	if err != nil {
		t.Fatalf("Observation.Fingerprint() error = %v", err)
	}
	if ticket.ExpectedTrustObservation.Fingerprint != wantObservationFingerprint ||
		ticket.Nonce != strings.Repeat("5a", ticketNonceBytes) ||
		ticket.ExpiresAt != fixture.now.Add(ticketLifetime) ||
		!bytes.Equal(fixture.publisher.key, fixture.private) {
		t.Fatalf("published trust correlation = %#v", ticket)
	}
}

// TestTrustRequestPlanAndResultValidation covers every public trust issuance contract boundary.
func TestTrustRequestPlanAndResultValidation(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	if err := fixture.request.Validate(); err != nil {
		t.Fatalf("TrustRequest.Validate() error = %v", err)
	}
	for _, request := range []TrustRequest{{}, {OperationID: " bad "}} {
		if err := request.Validate(); err == nil {
			t.Fatalf("TrustRequest.Validate(%#v) error = nil", request)
		}
	}
	if err := fixture.plan.Validate(); err != nil {
		t.Fatalf("TrustPlan.Validate() error = %v", err)
	}
	planTests := []struct {
		name   string
		want   string
		mutate func(*TrustPlan)
	}{
		{
			name: "operation ID",
			want: "operation",
			mutate: func(plan *TrustPlan) {
				plan.Operation.ID = ""
			},
		},
		{name: "zero revision", want: "revision", mutate: func(plan *TrustPlan) { plan.OperationRevision = 0 }},
		{name: "large revision", want: "revision", mutate: func(plan *TrustPlan) { plan.OperationRevision = domain.MaximumSequence + 1 }},
		{
			name: "state",
			want: "state",
			mutate: func(plan *TrustPlan) {
				plan.Operation.State = domain.OperationRunning
			},
		},
		{
			name: "mutation",
			want: "mutation",
			mutate: func(plan *TrustPlan) {
				plan.Mutation = helper.OperationReleaseTrust
			},
		},
		{name: "target record", want: "target ownership", mutate: func(plan *TrustPlan) { plan.TargetOwnership.InstallationID = "" }},
		{name: "target schema", want: "target ownership schema", mutate: func(plan *TrustPlan) {
			plan.TargetOwnership.SchemaVersion = ownership.IdentitySchemaVersion
			plan.TargetOwnership.NetworkPolicyFingerprint = ""
		}},
		{name: "policy", want: "approval policy", mutate: func(plan *TrustPlan) { plan.Policy.Suffix = ".invalid" }},
		{name: "target policy", want: "policy does not match", mutate: func(plan *TrustPlan) {
			plan.TargetOwnership.NetworkPolicyFingerprint = strings.Repeat("b", 64)
		}},
		{name: "public root", want: "public root", mutate: func(plan *TrustPlan) { plan.Root.CertificatePEM = nil }},
		{name: "root policy", want: "public root does not match policy authority", mutate: func(plan *TrustPlan) {
			plan.Policy.AuthorityFingerprint = strings.Repeat("b", 64)
			fingerprint, err := plan.Policy.Fingerprint()
			if err != nil {
				t.Fatalf("changed Policy.Fingerprint() error = %v", err)
			}
			plan.TargetOwnership.NetworkPolicyFingerprint = fingerprint
		}},
	}
	for _, test := range planTests {
		t.Run("plan "+test.name, func(t *testing.T) {
			plan := cloneTrustPlan(fixture.plan)
			test.mutate(&plan)
			if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("TrustPlan.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}

	ownershipFingerprint, err := fixture.plan.TargetOwnership.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	validResult := TrustResult{
		OperationID:          fixture.plan.Operation.ID,
		Reference:            helper.TicketReference(strings.Repeat("a", 64)),
		Operation:            helper.OperationEnsureTrust,
		PolicyFingerprint:    fixture.plan.TargetOwnership.NetworkPolicyFingerprint,
		OwnershipFingerprint: ownershipFingerprint,
		AuthorityFingerprint: fixture.plan.Root.Fingerprint,
		Mechanism:            fixture.plan.Policy.Mechanisms.Trust,
		ExpiresAt:            fixture.now.Add(time.Minute),
	}
	if err := validResult.Validate(fixture.now); err != nil {
		t.Fatalf("TrustResult.Validate() error = %v", err)
	}
	administratorResult := validResult
	administratorResult.Mechanism = networkpolicy.DarwinAdministratorTrust
	if err := administratorResult.Validate(fixture.now); err != nil {
		t.Fatalf("administrator TrustResult.Validate() error = %v", err)
	}
	resultTests := []struct {
		name   string
		want   string
		mutate func(*TrustResult)
	}{
		{name: "operation ID", want: "operation", mutate: func(result *TrustResult) { result.OperationID = "" }},
		{name: "reference", want: "reference", mutate: func(result *TrustResult) { result.Reference = "bad" }},
		{
			name: "operation",
			want: "unsupported",
			mutate: func(result *TrustResult) {
				result.Operation = helper.OperationEnsureLowPorts
			},
		},
		{name: "policy fingerprint length", want: "policy fingerprint", mutate: func(result *TrustResult) { result.PolicyFingerprint = "bad" }},
		{name: "policy fingerprint case", want: "policy fingerprint", mutate: func(result *TrustResult) { result.PolicyFingerprint = strings.Repeat("A", 64) }},
		{name: "ownership fingerprint length", want: "ownership fingerprint", mutate: func(result *TrustResult) { result.OwnershipFingerprint = "bad" }},
		{name: "ownership fingerprint case", want: "ownership fingerprint", mutate: func(result *TrustResult) { result.OwnershipFingerprint = strings.Repeat("A", 64) }},
		{name: "authority fingerprint length", want: "authority fingerprint", mutate: func(result *TrustResult) { result.AuthorityFingerprint = "bad" }},
		{name: "authority fingerprint case", want: "authority fingerprint", mutate: func(result *TrustResult) { result.AuthorityFingerprint = strings.Repeat("A", 64) }},
		{name: "unknown mechanism", want: "mechanism", mutate: func(result *TrustResult) { result.Mechanism = "unsupported" }},
		{name: "mixed mechanism", want: "mechanism", mutate: func(result *TrustResult) {
			result.Mechanism = networkpolicy.TrustMechanism(string(networkpolicy.DarwinAdministratorTrust) + "," + string(networkpolicy.DarwinCurrentUserTrust))
		}},
		{name: "expiry zero", want: "expiry is invalid", mutate: func(result *TrustResult) { result.ExpiresAt = time.Time{} }},
		{name: "expiry non-UTC", want: "expiry is invalid", mutate: func(result *TrustResult) {
			result.ExpiresAt = fixture.now.In(time.FixedZone("test", 60)).Add(time.Minute)
		}},
		{name: "expiry elapsed", want: "expiry is invalid", mutate: func(result *TrustResult) { result.ExpiresAt = fixture.now }},
		{name: "expiry excessive", want: "exceeds the protocol bound", mutate: func(result *TrustResult) {
			result.ExpiresAt = fixture.now.Add(helper.MaxTicketLifetime + time.Nanosecond)
		}},
	}
	for _, test := range resultTests {
		t.Run("result "+test.name, func(t *testing.T) {
			result := validResult
			test.mutate(&result)
			if err := result.Validate(fixture.now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("TrustResult.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestTrustServiceIssueRevalidatesEveryAuthority prevents publication across durable or observed drift.
func TestTrustServiceIssueRevalidatesEveryAuthority(t *testing.T) {
	cause := errors.New("scripted revalidation failure")
	tests := []struct {
		name   string
		want   string
		mutate func(*trustIssuerFixture)
	}{
		{name: "plan read", want: "revalidate approval plan", mutate: func(fixture *trustIssuerFixture) {
			fixture.plans.errors = []error{nil, cause}
		}},
		{name: "invalid confirmed plan", want: "invalid approval plan", mutate: func(fixture *trustIssuerFixture) {
			changed := cloneTrustPlan(fixture.plan)
			changed.Operation.State = domain.OperationRunning
			fixture.plans.plans = []TrustPlan{fixture.plan, changed}
		}},
		{name: "plan revision", want: "plan changed", mutate: func(fixture *trustIssuerFixture) {
			changed := cloneTrustPlan(fixture.plan)
			changed.OperationRevision++
			fixture.plans.plans = []TrustPlan{fixture.plan, changed}
		}},
		{name: "plan public root", want: "plan changed", mutate: func(fixture *trustIssuerFixture) {
			changed := cloneTrustPlan(fixture.plan)
			changed.Root.NotAfter = changed.Root.NotAfter.Add(time.Minute)
			fixture.plans.plans = []TrustPlan{fixture.plan, changed}
		}},
		{name: "ownership read", want: "revalidate ownership", mutate: func(fixture *trustIssuerFixture) {
			fixture.ownership.errors = []error{nil, cause}
		}},
		{name: "ownership state", want: "ownership projection is absent", mutate: func(fixture *trustIssuerFixture) {
			fixture.ownership.observations = append(fixture.ownership.observations, ownership.Observation{})
		}},
		{name: "trust read", want: "revalidate trust", mutate: func(fixture *trustIssuerFixture) {
			fixture.observer.errors = []error{nil, cause}
		}},
		{name: "invalid confirmed trust", want: "invalid trust observation", mutate: func(fixture *trustIssuerFixture) {
			changed := fixture.observation
			changed.Truncated = true
			fixture.observer.observations = []platformtrust.Observation{fixture.observation, changed}
		}},
		{name: "trust observation", want: "observation changed", mutate: func(fixture *trustIssuerFixture) {
			changed := trustObservationForState(t, fixture.observation.Request, platformtrust.StateOwnedDrifted)
			fixture.observer.observations = []platformtrust.Observation{fixture.observation, changed}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTrustIssuerFixture(t)
			test.mutate(fixture)
			result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Issue() = %#v, %v, want containing %q", result, err, test.want)
			}
			if result != (TrustResult{}) || fixture.publisher.calls != 0 {
				t.Fatalf("failed Issue() result/publisher = %#v / %d", result, fixture.publisher.calls)
			}
		})
	}
}

// TestSameTrustPlanAcceptsEquivalentOperationPointers proves revalidation compares durable operation values rather than pointer allocation.
func TestSameTrustPlanAcceptsEquivalentOperationPointers(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	started := now.Add(time.Second)
	finished := started.Add(time.Second)
	problem := domain.Problem{
		Code:      "failed",
		Message:   "scripted failure",
		Retryable: true,
	}
	left := TrustPlan{
		Operation: domain.Operation{
			ID:          "operation-trust",
			IntentID:    "intent-trust",
			Kind:        domain.OperationKindNetworkDataPlaneSetup,
			State:       domain.OperationFailed,
			Phase:       "failed",
			RequestedAt: now,
			StartedAt:   &started,
			FinishedAt:  &finished,
			Problem:     &problem,
		},
	}
	secondStarted := started
	secondFinished := finished
	secondProblem := problem
	right := TrustPlan{
		Operation: domain.Operation{
			ID:          "operation-trust",
			IntentID:    "intent-trust",
			Kind:        domain.OperationKindNetworkDataPlaneSetup,
			State:       domain.OperationFailed,
			Phase:       "failed",
			RequestedAt: now,
			StartedAt:   &secondStarted,
			FinishedAt:  &secondFinished,
			Problem:     &secondProblem,
		},
	}
	if !sameTrustPlan(left, right) {
		t.Fatal("sameTrustPlan() = false, want equivalent separately allocated operation values")
	}
}

// TestTrustServiceReleaseObservationRequiresOwnedTrustOrConfirmedAbsence prevents destructive release capability issuance after native trust drift.
func TestTrustServiceReleaseObservationRequiresOwnedTrustOrConfirmedAbsence(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	request := fixture.observation.Request
	for _, test := range []struct {
		name  string
		state platformtrust.State
		want  bool
	}{
		{
			name:  "exact owned",
			state: platformtrust.StateExact,
			want:  true,
		},
		{
			name:  "absent",
			state: platformtrust.StateAbsent,
			want:  true,
		},
		{
			name:  "owned drifted",
			state: platformtrust.StateOwnedDrifted,
		},
		{
			name:  "foreign",
			state: platformtrust.StateForeign,
		},
		{
			name:  "ambiguous",
			state: platformtrust.StateAmbiguous,
		},
		{
			name:  "indeterminate",
			state: platformtrust.StateIndeterminate,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture.observer.observations = []platformtrust.Observation{
				trustObservationForState(t, request, test.state),
			}
			fingerprint, err := fixture.service.observeTrust(
				t.Context(),
				request,
				TrustPlanPurposeGlobalNetworkRelease,
			)
			if test.want {
				if err != nil || !canonicalSHA256Fingerprint(fingerprint) {
					t.Fatalf("observeTrust() = %q, %v", fingerprint, err)
				}
				return
			}
			if err == nil || fingerprint != "" {
				t.Fatalf("observeTrust() = %q, %v, want rejected release state", fingerprint, err)
			}
		})
	}
}

// TestTrustPlanReleaseLifecycleValidation covers every release-only trust authority fence.
func TestTrustPlanReleaseLifecycleValidation(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	plan := validGlobalReleaseTrustPlan(t, fixture.plan)
	if err := plan.Validate(); err != nil {
		t.Fatalf("TrustPlan.Validate() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*TrustPlan)
	}{
		{
			name: "purpose",
			mutate: func(plan *TrustPlan) {
				plan.Purpose = "unsupported"
			},
		},
		{
			name: "checkpoint revision",
			mutate: func(plan *TrustPlan) {
				plan.CheckpointRevision = 0
			},
		},
		{
			name: "checkpoint phase",
			mutate: func(plan *TrustPlan) {
				plan.CheckpointPhase = TrustCheckpointPhaseSetupApproval
			},
		},
		{
			name: "operation kind",
			mutate: func(plan *TrustPlan) {
				plan.Operation.Kind = domain.OperationKindNetworkDataPlaneSetup
			},
		},
		{
			name: "operation state",
			mutate: func(plan *TrustPlan) {
				plan.Operation.State = domain.OperationRequiresApproval
			},
		},
		{
			name: "operation phase",
			mutate: func(plan *TrustPlan) {
				plan.Operation.Phase = "awaiting trust approval"
			},
		},
		{
			name: "project",
			mutate: func(plan *TrustPlan) {
				plan.Operation.ProjectID = "project-trust"
			},
		},
		{
			name: "mutation",
			mutate: func(plan *TrustPlan) {
				plan.Mutation = helper.OperationEnsureTrust
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneTrustPlan(plan)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("TrustPlan.Validate() unexpectedly succeeded")
			}
		})
	}
	result := TrustResult{
		OperationID:          plan.Operation.ID,
		Reference:            helper.TicketReference(strings.Repeat("a", 64)),
		Operation:            helper.OperationReleaseTrust,
		PolicyFingerprint:    plan.TargetOwnership.NetworkPolicyFingerprint,
		OwnershipFingerprint: mustOwnershipFingerprint(t, plan.TargetOwnership),
		AuthorityFingerprint: plan.Root.Fingerprint,
		Mechanism:            plan.Policy.Mechanisms.Trust,
		ExpiresAt:            fixture.now.Add(time.Minute),
	}
	if err := result.Validate(fixture.now); err != nil {
		t.Fatalf("TrustResult.Validate() error = %v", err)
	}
}

// TestTrustServiceIssuesGlobalReleaseTrust proves exact owned trust can publish its destructive release ticket.
func TestTrustServiceIssuesGlobalReleaseTrust(t *testing.T) {
	assertTrustServiceIssuesGlobalReleaseTrust(t, platformtrust.StateExact)
}

// TestTrustServiceIssuesGlobalReleaseTrustAfterLostResponse proves confirmed absence can publish a replacement release ticket.
func TestTrustServiceIssuesGlobalReleaseTrustAfterLostResponse(t *testing.T) {
	assertTrustServiceIssuesGlobalReleaseTrust(t, platformtrust.StateAbsent)
}

// assertTrustServiceIssuesGlobalReleaseTrust verifies publication for either release-safe native state.
func assertTrustServiceIssuesGlobalReleaseTrust(t *testing.T, trustState platformtrust.State) {
	t.Helper()
	fixture := newTrustIssuerFixture(t)
	fixture.plan = validGlobalReleaseTrustPlan(t, fixture.plan)
	fixture.request = TrustRequest{
		OperationID: fixture.plan.Operation.ID,
	}
	fixture.plans.plans = []TrustPlan{
		cloneTrustPlan(fixture.plan),
		cloneTrustPlan(fixture.plan),
	}
	observation := trustObservationForState(t, fixture.observation.Request, trustState)
	fixture.observer.observations = []platformtrust.Observation{
		observation,
		observation,
	}
	result, err := fixture.service.Issue(
		t.Context(),
		fixture.plan.TargetOwnership.OwnerIdentity,
		fixture.request,
	)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if result.OperationID != fixture.plan.Operation.ID ||
		result.Operation != helper.OperationReleaseTrust ||
		result.PolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint ||
		result.AuthorityFingerprint != fixture.plan.Root.Fingerprint {
		t.Fatalf("Issue() result = %#v", result)
	}
	if fixture.publisher.ticket.Operation != helper.OperationReleaseTrust ||
		fixture.publisher.ticket.NetworkPolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint ||
		fixture.publisher.ticket.TrustRoot == nil ||
		fixture.publisher.ticket.TrustRoot.Fingerprint != fixture.plan.Root.Fingerprint ||
		fixture.publisher.ticket.ExpectedTrustObservation == nil {
		t.Fatalf("published release ticket = %#v", fixture.publisher.ticket)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fixture.publisher.ticket.ExpectedTrustObservation.Fingerprint != fingerprint {
		t.Fatalf("published release ticket fingerprint = %q, want %q", fixture.publisher.ticket.ExpectedTrustObservation.Fingerprint, fingerprint)
	}
}

// TestTrustServiceRejectsEveryAuthorityFailure keeps publication behind each independent trust boundary.
func TestTrustServiceRejectsEveryAuthorityFailure(t *testing.T) {
	cause := errors.New("scripted authority failure")
	tests := []struct {
		name          string
		requester     string
		want          string
		wantCause     bool
		publisherCall int
		mutate        func(*trustIssuerFixture)
	}{
		{name: "plan read", want: "resolve approval plan", wantCause: true, mutate: func(fixture *trustIssuerFixture) { fixture.plans.errors = []error{cause} }},
		{
			name: "invalid plan",
			want: "invalid approval plan",
			mutate: func(fixture *trustIssuerFixture) {
				fixture.plans.plans[0].Operation.State = domain.OperationRunning
			},
		},
		{
			name: "wrong plan operation",
			want: "does not match requested operation",
			mutate: func(fixture *trustIssuerFixture) {
				fixture.plans.plans[0].Operation.ID = "operation-other"
			},
		},
		{name: "ownership read", want: "observe ownership projection", wantCause: true, mutate: func(fixture *trustIssuerFixture) { fixture.ownership.errors = []error{cause} }},
		{name: "ownership absent", want: "ownership projection is absent", mutate: func(fixture *trustIssuerFixture) { fixture.ownership.observations = []ownership.Observation{{}} }},
		{name: "ownership record", want: "differs from the approved target", mutate: func(fixture *trustIssuerFixture) {
			changed := fixture.ownership.observations[0]
			changed.Record.Generation++
			changed.Fingerprint = mustOwnershipFingerprint(t, changed.Record)
			fixture.ownership.observations = []ownership.Observation{changed}
		}},
		{name: "requester", requester: "502", want: "does not own"},
		{name: "ownership fingerprint", want: "does not match approved target", mutate: func(fixture *trustIssuerFixture) {
			fixture.ownership.observations[0].Fingerprint = strings.Repeat("b", 64)
		}},
		{name: "key read", want: "load established signing key", wantCause: true, mutate: func(fixture *trustIssuerFixture) { fixture.keys.err = cause }},
		{name: "key malformed", want: "signing key is invalid", mutate: func(fixture *trustIssuerFixture) { fixture.keys.key = ed25519.PrivateKey("short") }},
		{name: "key mismatch", want: "does not match machine ownership", mutate: func(fixture *trustIssuerFixture) {
			_, other, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatalf("GenerateKey() error = %v", err)
			}
			fixture.keys.key = other
		}},
		{name: "trust read", want: "observe trust", wantCause: true, mutate: func(fixture *trustIssuerFixture) { fixture.observer.errors = []error{cause} }},
		{name: "trust malformed", want: "invalid trust observation", mutate: func(fixture *trustIssuerFixture) {
			fixture.observer.observations[0].Truncated = true
		}},
		{name: "trust request", want: "belongs to another request", mutate: func(fixture *trustIssuerFixture) {
			fixture.observer.observations[0] = trustObservationForRequest(t, "installation-other", fixture.plan.TargetOwnership.OwnerIdentity, fixture.plan.Policy.Mechanisms.Trust, fixture.plan.Root)
		}},
		{name: "trust unsafe foreign", want: "cannot be safely ensured", mutate: func(fixture *trustIssuerFixture) {
			observation := trustObservationForState(t, fixture.observation.Request, platformtrust.StateForeign)
			observation.Entries[0].NativeExact = false
			fixture.observer.observations[0] = observation
		}},
		{name: "trust ambiguous", want: "cannot be safely ensured", mutate: func(fixture *trustIssuerFixture) {
			fixture.observer.observations[0] = trustObservationForState(t, fixture.observation.Request, platformtrust.StateAmbiguous)
		}},
		{name: "trust indeterminate", want: "cannot be safely ensured", mutate: func(fixture *trustIssuerFixture) {
			fixture.observer.observations[0] = trustObservationForState(t, fixture.observation.Request, platformtrust.StateIndeterminate)
		}},
		{name: "entropy", want: "generate nonce", wantCause: true, mutate: func(fixture *trustIssuerFixture) { fixture.service.entropy = errorReader{err: cause} }},
		{name: "publisher", want: "publish capability", wantCause: true, publisherCall: 1, mutate: func(fixture *trustIssuerFixture) { fixture.publisher.err = cause }},
		{name: "result validation", want: "invalid result", publisherCall: 1, mutate: func(fixture *trustIssuerFixture) {
			fixture.service.clock = &scriptedTrustClock{times: []time.Time{fixture.now, fixture.now.Add(ticketLifetime)}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTrustIssuerFixture(t)
			if test.mutate != nil {
				test.mutate(fixture)
			}
			requester := test.requester
			if requester == "" {
				requester = fixture.plan.TargetOwnership.OwnerIdentity
			}
			result, err := fixture.service.Issue(t.Context(), requester, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Issue() = %#v, %v, want containing %q", result, err, test.want)
			}
			if test.wantCause && !errors.Is(err, cause) {
				t.Fatalf("Issue() error = %v, want cause %v", err, cause)
			}
			if result != (TrustResult{}) || fixture.publisher.calls != test.publisherCall {
				t.Fatalf("failed Issue() result/publisher = %#v / %d, want 0 / %d", result, fixture.publisher.calls, test.publisherCall)
			}
		})
	}
}

// TestTrustServiceObserveTrustPinsCompleteRequestAndSafeStates exercises native classification and correlation directly.
func TestTrustServiceObserveTrustPinsCompleteRequestAndSafeStates(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	request := fixture.observation.Request
	root := request.Root()
	changedRoot := root
	changedRoot.NotAfter = changedRoot.NotAfter.Add(time.Minute)
	preexisting := trustObservationForState(t, request, platformtrust.StateForeign)
	multiplePreexisting := preexisting
	secondPreexisting := preexisting.Entries[0]
	secondPreexisting.NativeID = "preexisting-second"
	unrelated := secondPreexisting
	unrelated.NativeID = "unrelated"
	unrelated.CertificateFingerprint = strings.Repeat("d", 64)
	multiplePreexisting.Entries = append(multiplePreexisting.Entries, secondPreexisting, unrelated)
	nonExactPreexisting := preexisting
	nonExactPreexisting.Entries = append([]platformtrust.Entry(nil), preexisting.Entries...)
	nonExactPreexisting.Entries[0].NativeExact = false
	markedPreexisting := preexisting
	markedPreexisting.Entries = append([]platformtrust.Entry(nil), preexisting.Entries...)
	marker := request.OwnerMarker()
	marker.InstallationID = "installation-other"
	markedPreexisting.Entries[0].Owner = &marker
	competingOwner := preexisting
	competingOwner.Entries = append([]platformtrust.Entry(nil), preexisting.Entries...)
	competingEntry := unrelated
	competingEntry.NativeID = "competing-owner"
	competingEntry.Owner = &marker
	competingOwner.Entries = append(competingOwner.Entries, competingEntry)
	tests := []struct {
		name        string
		observation platformtrust.Observation
		observeErr  error
		want        string
	}{
		{name: "absent", observation: trustObservationForState(t, request, platformtrust.StateAbsent)},
		{name: "exact", observation: trustObservationForState(t, request, platformtrust.StateExact)},
		{name: "owned drifted", observation: trustObservationForState(t, request, platformtrust.StateOwnedDrifted)},
		{name: "preexisting identical", observation: preexisting},
		{name: "multiple preexisting identical and unrelated", observation: multiplePreexisting},
		{name: "nonexact preexisting", observation: nonExactPreexisting, want: "cannot be safely ensured"},
		{name: "marked preexisting", observation: markedPreexisting, want: "cannot be safely ensured"},
		{name: "competing owner", observation: competingOwner, want: "cannot be safely ensured"},
		{name: "ambiguous", observation: trustObservationForState(t, request, platformtrust.StateAmbiguous), want: "cannot be safely ensured"},
		{name: "indeterminate", observation: trustObservationForState(t, request, platformtrust.StateIndeterminate), want: "cannot be safely ensured"},
		{name: "invalid", observation: platformtrust.Observation{Request: request, Complete: true, Truncated: true}, want: "invalid trust observation"},
		{name: "installation", observation: trustObservationForRequest(t, "installation-other", request.RequesterIdentity(), request.Mechanism(), root), want: "another request"},
		{name: "requester", observation: trustObservationForRequest(t, request.InstallationID(), "502", request.Mechanism(), root), want: "another request"},
		{name: "mechanism", observation: trustObservationForRequest(t, request.InstallationID(), request.RequesterIdentity(), networkpolicy.UbuntuSystemTrust, root), want: "another request"},
		{name: "root validity", observation: trustObservationForRequest(t, request.InstallationID(), request.RequesterIdentity(), request.Mechanism(), changedRoot), want: "another request"},
		{name: "observer", observeErr: errors.New("observe failed"), want: "observe trust"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTrustIssuerFixture(t)
			fixture.observer.observations = []platformtrust.Observation{test.observation}
			if test.observeErr != nil {
				fixture.observer.errors = []error{test.observeErr}
			}
			fingerprint, err := fixture.service.observeTrust(t.Context(), request, TrustPlanPurposeDataPlaneSetup)
			if test.want == "" {
				if err != nil || !canonicalSHA256Fingerprint(fingerprint) {
					t.Fatalf("observeTrust() = %q, %v", fingerprint, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) || fingerprint != "" {
				t.Fatalf("observeTrust() = %q, %v, want containing %q", fingerprint, err, test.want)
			}
		})
	}
}

// TestTrustServiceAcceptsOrderIndependentStableObservations proves native entry order cannot create false drift.
func TestTrustServiceAcceptsOrderIndependentStableObservations(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	exact := trustObservationForState(t, fixture.observation.Request, platformtrust.StateExact)
	irrelevant := platformtrust.Entry{
		Mechanism:              exact.Request.Mechanism(),
		NativeID:               "irrelevant",
		CertificateFingerprint: strings.Repeat("d", 64),
		NativeExact:            true,
		NativeAttributesSHA256: strings.Repeat("e", 64),
	}
	exact.Entries = append(exact.Entries, irrelevant)
	reordered := exact
	reordered.Entries = append([]platformtrust.Entry(nil), exact.Entries...)
	slices.Reverse(reordered.Entries)
	first, err := exact.Fingerprint()
	if err != nil {
		t.Fatalf("first Fingerprint() error = %v", err)
	}
	second, err := reordered.Fingerprint()
	if err != nil {
		t.Fatalf("second Fingerprint() error = %v", err)
	}
	if first != second {
		t.Fatalf("order changed trust fingerprint: %s != %s", first, second)
	}
	fixture.observer.observations = []platformtrust.Observation{exact, reordered}
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if fixture.publisher.ticket.ExpectedTrustObservation.Fingerprint != first {
		t.Fatalf("published observation fingerprint = %q, want %q", fixture.publisher.ticket.ExpectedTrustObservation.Fingerprint, first)
	}
}

// TestTrustServiceIssuesAgainstStablePreexistingIdenticalTrust preserves an exact unowned CA without creating a competing owner marker.
func TestTrustServiceIssuesAgainstStablePreexistingIdenticalTrust(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	observation := trustObservationForState(t, fixture.observation.Request, platformtrust.StateForeign)
	second := observation.Entries[0]
	second.NativeID = "preexisting-second"
	unrelated := second
	unrelated.NativeID = "unrelated"
	unrelated.CertificateFingerprint = strings.Repeat("d", 64)
	observation.Entries = append(observation.Entries, second, unrelated)
	reordered := observation
	reordered.Entries = append([]platformtrust.Entry(nil), observation.Entries...)
	slices.Reverse(reordered.Entries)

	first, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("preexisting Fingerprint() error = %v", err)
	}
	secondFingerprint, err := reordered.Fingerprint()
	if err != nil {
		t.Fatalf("reordered preexisting Fingerprint() error = %v", err)
	}
	if first != secondFingerprint {
		t.Fatalf("preexisting entry order changed trust fingerprint: %s != %s", first, secondFingerprint)
	}
	fixture.observer.observations = []platformtrust.Observation{observation, reordered}
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if fixture.publisher.ticket.ExpectedTrustObservation == nil ||
		fixture.publisher.ticket.ExpectedTrustObservation.Fingerprint != first {
		t.Fatalf("published preexisting observation = %#v, want %q", fixture.publisher.ticket.ExpectedTrustObservation, first)
	}
}

// TestTrustServicePreservesOnlyCorrelatedDurabilityUncertainty keeps one possibly-published reference available.
func TestTrustServicePreservesOnlyCorrelatedDurabilityUncertainty(t *testing.T) {
	t.Run("correlated", func(t *testing.T) {
		fixture := newTrustIssuerFixture(t)
		cause := errors.New("scripted directory sync failure")
		fixture.publisher.err = errors.Join(ticketspool.ErrDurabilityUncertain, cause)
		result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
		if !errors.Is(err, ErrTrustPublicationIndeterminate) ||
			!errors.Is(err, ticketspool.ErrDurabilityUncertain) ||
			!errors.Is(err, cause) {
			t.Fatalf("Issue() error = %v, want trust and spool durability classifications", err)
		}
		if result.Reference != fixture.publisher.reference || result.OperationID != fixture.plan.Operation.ID {
			t.Fatalf("Issue() result = %#v", result)
		}
		if err := result.Validate(fixture.now); err != nil {
			t.Fatalf("TrustResult.Validate() error = %v", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		fixture := newTrustIssuerFixture(t)
		fixture.publisher.reference = ""
		fixture.publisher.err = ticketspool.ErrDurabilityUncertain
		result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
		if result != (TrustResult{}) || !errors.Is(err, ErrTrustPublicationIndeterminate) ||
			!errors.Is(err, ticketspool.ErrDurabilityUncertain) ||
			!strings.Contains(err.Error(), "invalid durability-uncertain publication result") {
			t.Fatalf("Issue() = %#v, %v", result, err)
		}
	})
}

// TestTrustServiceDefensivelyCopiesPublicRoots keeps durable-source memory outside tickets and comparisons.
func TestTrustServiceDefensivelyCopiesPublicRoots(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	resolved, err := fixture.service.resolvePlan(t.Context(), fixture.request)
	if err != nil {
		t.Fatalf("resolvePlan() error = %v", err)
	}
	wantResolved := append([]byte(nil), resolved.Root.CertificatePEM...)
	fixture.plans.plans[0].Root.CertificatePEM[0] ^= 0xff
	if !bytes.Equal(resolved.Root.CertificatePEM, wantResolved) {
		t.Fatal("resolved plan retained source-owned certificate bytes")
	}

	fixture = newTrustIssuerFixture(t)
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	wantTicketRoot := append([]byte(nil), fixture.publisher.ticket.TrustRoot.CertificatePEM...)
	fixture.plan.Root.CertificatePEM[0] ^= 0xff
	for index := range fixture.plans.plans {
		fixture.plans.plans[index].Root.CertificatePEM[0] ^= 0xff
	}
	if !bytes.Equal(fixture.publisher.ticket.TrustRoot.CertificatePEM, wantTicketRoot) {
		t.Fatal("published ticket retained plan-source certificate bytes")
	}
	returnedRoot := fixture.observer.requests[0].Root()
	returnedRoot.CertificatePEM[0] ^= 0xff
	if bytes.Equal(returnedRoot.CertificatePEM, fixture.observer.requests[0].Root().CertificatePEM) {
		t.Fatal("trust request Root() did not return a defensive copy")
	}
}

// TestTrustServicePrivateBuildersRejectCorruptInputs covers fail-closed branches protected by public invariants.
func TestTrustServicePrivateBuildersRejectCorruptInputs(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	if ticket, err := fixture.service.buildTicket(
		fixture.plan.TargetOwnership.OwnerIdentity,
		fixture.plan,
		"bad",
		fixture.private,
	); err == nil || !strings.Contains(err.Error(), "constructed ticket is invalid") || ticket != (helper.Ticket{}) {
		t.Fatalf("buildTicket(invalid observation) = %#v, %v", ticket, err)
	}
	_, other, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	fingerprint, err := fixture.observation.Fingerprint()
	if err != nil {
		t.Fatalf("Observation.Fingerprint() error = %v", err)
	}
	if ticket, err := fixture.service.buildTicket(
		fixture.plan.TargetOwnership.OwnerIdentity,
		fixture.plan,
		fingerprint,
		other,
	); err == nil || !strings.Contains(err.Error(), "signing key changed during construction") || ticket != (helper.Ticket{}) {
		t.Fatalf("buildTicket(changed key) = %#v, %v", ticket, err)
	}

	invalidPlan := fixture.plan
	invalidPlan.TargetOwnership.InstallationID = ""
	fixture.ownership.observations = []ownership.Observation{{
		Exists: true,
		Record: invalidPlan.TargetOwnership,
	}}
	if err := fixture.service.observeOwnership(t.Context(), invalidPlan.TargetOwnership.OwnerIdentity, invalidPlan); err == nil ||
		!strings.Contains(err.Error(), "fingerprint approved target ownership") {
		t.Fatalf("observeOwnership(invalid target) error = %v", err)
	}
}

// TestOpenDefaultTrustServiceOwnsBothStores covers validation, partial-open cleanup, and close ordering.
func TestOpenDefaultTrustServiceOwnsBothStores(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	log := &trustCloseLog{}
	keyCloseErr := errors.New("key close failed")
	publisherCloseErr := errors.New("publisher close failed")
	keyStore := &trustClosingKeyStore{KeyLoader: fixture.keys, closeErr: keyCloseErr, log: log}
	publisher := &trustClosingPublisher{Publisher: fixture.publisher, closeErr: publisherCloseErr, log: log}
	openers := trustDefaultOpeners{
		openKeys:      func() (defaultKeyStoreCloser, error) { return keyStore, nil },
		openPublisher: func() (defaultPublisherCloser, error) { return publisher, nil },
	}
	service, err := openDefaultTrustService(fixture.plans, fixture.ownership, fixture.observer, openers)
	if err != nil {
		t.Fatalf("openDefaultTrustService() error = %v", err)
	}
	if err := service.Close(); !errors.Is(err, keyCloseErr) || !errors.Is(err, publisherCloseErr) {
		t.Fatalf("Close() error = %v, want both close failures", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if keyStore.closeCalls.Load() != 1 || publisher.closeCalls.Load() != 1 || !slices.Equal(log.snapshot(), []string{"publisher", "keys"}) {
		t.Fatalf("close calls/order = %d/%d %v", keyStore.closeCalls.Load(), publisher.closeCalls.Load(), log.snapshot())
	}

	keyOpenErr := errors.New("key open failed")
	publisherOpens := 0
	if opened, err := openDefaultTrustService(
		fixture.plans,
		fixture.ownership,
		fixture.observer,
		trustDefaultOpeners{
			openKeys:      func() (defaultKeyStoreCloser, error) { return nil, keyOpenErr },
			openPublisher: func() (defaultPublisherCloser, error) { publisherOpens++; return publisher, nil },
		},
	); opened != nil || !errors.Is(err, keyOpenErr) || publisherOpens != 0 {
		t.Fatalf("key open = (%#v, %v), publisher opens %d", opened, err, publisherOpens)
	}
	if opened, err := openDefaultTrustService(
		fixture.plans,
		fixture.ownership,
		fixture.observer,
		trustDefaultOpeners{
			openKeys:      func() (defaultKeyStoreCloser, error) { return nil, nil },
			openPublisher: func() (defaultPublisherCloser, error) { return publisher, nil },
		},
	); opened != nil || err == nil || !strings.Contains(err.Error(), "key: opener returned nil") {
		t.Fatalf("nil key opener = (%#v, %v)", opened, err)
	}

	partialKey := &trustClosingKeyStore{KeyLoader: fixture.keys, closeErr: keyCloseErr}
	spoolOpenErr := errors.New("publisher open failed")
	if opened, err := openDefaultTrustService(
		fixture.plans,
		fixture.ownership,
		fixture.observer,
		trustDefaultOpeners{
			openKeys:      func() (defaultKeyStoreCloser, error) { return partialKey, nil },
			openPublisher: func() (defaultPublisherCloser, error) { return nil, spoolOpenErr },
		},
	); opened != nil || !errors.Is(err, spoolOpenErr) || !errors.Is(err, keyCloseErr) || partialKey.closeCalls.Load() != 1 {
		t.Fatalf("publisher open = (%#v, %v), key closes %d", opened, err, partialKey.closeCalls.Load())
	}
	partialKey = &trustClosingKeyStore{KeyLoader: fixture.keys, closeErr: keyCloseErr}
	if opened, err := openDefaultTrustService(
		fixture.plans,
		fixture.ownership,
		fixture.observer,
		trustDefaultOpeners{
			openKeys:      func() (defaultKeyStoreCloser, error) { return partialKey, nil },
			openPublisher: func() (defaultPublisherCloser, error) { return nil, nil },
		},
	); opened != nil || err == nil || !errors.Is(err, keyCloseErr) || !strings.Contains(err.Error(), "spool: opener returned nil") || partialKey.closeCalls.Load() != 1 {
		t.Fatalf("nil publisher opener = (%#v, %v), key closes %d", opened, err, partialKey.closeCalls.Load())
	}

	nilCases := []struct {
		name      string
		plans     TrustPlanSource
		ownership OwnershipObserver
		observer  TrustObserver
		openers   trustDefaultOpeners
		want      string
	}{
		{name: "plans", ownership: fixture.ownership, observer: fixture.observer, openers: openers, want: "durable plan source"},
		{name: "ownership", plans: fixture.plans, observer: fixture.observer, openers: openers, want: "ownership observer"},
		{name: "observer", plans: fixture.plans, ownership: fixture.ownership, openers: openers, want: "trust observer"},
		{name: "openers", plans: fixture.plans, ownership: fixture.ownership, observer: fixture.observer, want: "openers are incomplete"},
	}
	for _, test := range nilCases {
		t.Run(test.name, func(t *testing.T) {
			opened, err := openDefaultTrustService(test.plans, test.ownership, test.observer, test.openers)
			if opened != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("openDefaultTrustService() = (%#v, %v), want containing %q", opened, err, test.want)
			}
		})
	}
}

// TestTrustServiceConstructorsFailFast covers public composition and every explicit dependency.
func TestTrustServiceConstructorsFailFast(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	defaultCases := []struct {
		name      string
		plans     TrustPlanSource
		ownership OwnershipObserver
		observer  TrustObserver
		want      string
	}{
		{name: "plans", ownership: fixture.ownership, observer: fixture.observer, want: "durable plan source"},
		{name: "ownership", plans: fixture.plans, observer: fixture.observer, want: "ownership observer"},
		{name: "observer", plans: fixture.plans, ownership: fixture.ownership, want: "trust observer"},
	}
	for _, test := range defaultCases {
		t.Run("default "+test.name, func(t *testing.T) {
			service, err := OpenDefaultTrustService(test.plans, test.ownership, test.observer)
			if service != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenDefaultTrustService() = (%#v, %v)", service, err)
			}
		})
	}

	constructorCases := []struct {
		name string
		call func()
	}{
		{name: "plans", call: func() {
			NewTrustService(nil, fixture.ownership, fixture.keys, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		}},
		{name: "ownership", call: func() {
			NewTrustService(fixture.plans, nil, fixture.keys, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		}},
		{name: "keys", call: func() {
			NewTrustService(fixture.plans, fixture.ownership, nil, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		}},
		{name: "publisher", call: func() {
			NewTrustService(fixture.plans, fixture.ownership, fixture.keys, nil, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		}},
		{name: "observer", call: func() {
			NewTrustService(fixture.plans, fixture.ownership, fixture.keys, fixture.publisher, nil, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		}},
		{name: "clock", call: func() {
			NewTrustService(fixture.plans, fixture.ownership, fixture.keys, fixture.publisher, fixture.observer, nil, bytes.NewReader(nil))
		}},
		{name: "entropy", call: func() {
			NewTrustService(fixture.plans, fixture.ownership, fixture.keys, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, nil)
		}},
	}
	for _, test := range constructorCases {
		t.Run("constructor "+test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewTrustService() did not panic")
				}
			}()
			test.call()
		})
	}
}

// TestTrustServiceLifecycleAndInputValidation keeps cancellation and closure outside authority reads.
func TestTrustServiceLifecycleAndInputValidation(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.service.Issue(canceled, fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue(canceled) error = %v", err)
	}
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, TrustRequest{}); err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("Issue(invalid request) error = %v", err)
	}
	if len(fixture.plans.requests) != 0 {
		t.Fatalf("canceled or invalid issuance resolved %d plans", len(fixture.plans.requests))
	}
	closeErr := errors.New("close failed")
	closeCalls := 0
	fixture.service.closeStore = func() error { closeCalls++; return closeErr }
	if err := fixture.service.Close(); !errors.Is(err, closeErr) || closeCalls != 1 {
		t.Fatalf("Close() error/count = %v/%d", err, closeCalls)
	}
	if err := fixture.service.Close(); err != nil || closeCalls != 1 {
		t.Fatalf("second Close() error/count = %v/%d", err, closeCalls)
	}
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Issue(closed) error = %v", err)
	}
	fresh := newTrustIssuerFixture(t)
	if err := fresh.service.Close(); err != nil {
		t.Fatalf("Close(default no-op store) error = %v", err)
	}
}

// TestTrustServiceRechecksCancellationAfterSerializationWait avoids authority reads for an abandoned queued issue.
func TestTrustServiceRechecksCancellationAfterSerializationWait(t *testing.T) {
	fixture := newTrustIssuerFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	plans := &blockingFirstTrustPlanSource{plan: fixture.plan, entered: entered, release: release}
	fixture.service.plans = plans
	firstResult := make(chan error, 1)
	go func() {
		_, err := fixture.service.Issue(context.Background(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
		firstResult <- err
	}()
	waitTrustSignal(t, entered, "first plan read")

	queuedBase, cancelQueued := context.WithCancel(context.Background())
	queued := &signaledTrustContext{Context: queuedBase, checked: make(chan struct{})}
	queuedResult := make(chan error, 1)
	go func() {
		_, err := fixture.service.Issue(queued, fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
		queuedResult <- err
	}()
	waitTrustSignal(t, queued.checked, "queued pre-lock cancellation check")
	cancelQueued()
	close(release)
	if err := waitTrustError(t, firstResult, "first issuance"); err != nil {
		t.Fatalf("first Issue() error = %v", err)
	}
	if err := waitTrustError(t, queuedResult, "queued issuance"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Issue() error = %v, want %v", err, context.Canceled)
	}
	if plans.calls.Load() != 2 || fixture.publisher.calls != 1 {
		t.Fatalf("authority calls = plans %d publish %d, want 2/1", plans.calls.Load(), fixture.publisher.calls)
	}
}

// TestTrustServiceCloseWaitsForIssueAndConcurrentCloseRunsOnce proves serialized lifecycle ownership.
func TestTrustServiceCloseWaitsForIssueAndConcurrentCloseRunsOnce(t *testing.T) {
	t.Run("in-flight issue", func(t *testing.T) {
		fixture := newTrustIssuerFixture(t)
		publisher := &blockingTrustPublisher{
			reference: fixture.publisher.reference,
			entered:   make(chan struct{}),
			release:   make(chan struct{}),
		}
		fixture.service.publisher = publisher
		storeClosed := make(chan struct{})
		fixture.service.closeStore = func() error { close(storeClosed); return nil }
		issueResult := make(chan error, 1)
		go func() {
			_, err := fixture.service.Issue(context.Background(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			issueResult <- err
		}()
		waitTrustSignal(t, publisher.entered, "publisher entry")
		closeAttempted := make(chan struct{})
		closeResult := make(chan error, 1)
		go func() {
			close(closeAttempted)
			closeResult <- fixture.service.Close()
		}()
		waitTrustSignal(t, closeAttempted, "close attempt")
		select {
		case <-storeClosed:
			t.Fatal("Close() crossed an in-flight issuance")
		case <-time.After(25 * time.Millisecond):
		}
		close(publisher.release)
		if err := waitTrustError(t, issueResult, "in-flight issuance"); err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		if err := waitTrustError(t, closeResult, "serialized close"); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		waitTrustSignal(t, storeClosed, "store close")
		if _, err := fixture.service.Issue(context.Background(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("Issue(after Close) error = %v", err)
		}
	})

	t.Run("concurrent close", func(t *testing.T) {
		fixture := newTrustIssuerFixture(t)
		var closeCalls atomic.Int64
		entered := make(chan struct{})
		release := make(chan struct{})
		fixture.service.closeStore = func() error {
			if closeCalls.Add(1) == 1 {
				close(entered)
			}
			<-release
			return nil
		}
		const callers = 32
		results := make(chan error, callers)
		start := make(chan struct{})
		for range callers {
			go func() {
				<-start
				results <- fixture.service.Close()
			}()
		}
		close(start)
		waitTrustSignal(t, entered, "first store close")
		close(release)
		for range callers {
			if err := waitTrustError(t, results, "concurrent close"); err != nil {
				t.Fatalf("concurrent Close() error = %v", err)
			}
		}
		if closeCalls.Load() != 1 {
			t.Fatalf("closeStore calls = %d, want 1", closeCalls.Load())
		}
	})
}

// newTrustIssuerFixture creates one CA-backed macOS trust approval and stable absent observations.
func newTrustIssuerFixture(t *testing.T) *trustIssuerFixture {
	t.Helper()
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	authority, err := localca.New(localca.Config{
		CAValidity:   24 * time.Hour,
		LeafValidity: time.Hour,
		Backdate:     time.Minute,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	material := authority.Material()
	root := certificates.Root{
		CertificatePEM: append([]byte(nil), material.CertificatePEM...),
		Fingerprint:    material.Fingerprint,
		NotBefore:      material.NotBefore,
		NotAfter:       material.NotAfter,
	}
	policy, err := networkpolicy.New(
		root.Fingerprint,
		networkpolicy.MacOSMechanisms(),
		networkpolicy.Listener{
			Advertised: netip.MustParseAddrPort("127.0.0.1:21000"),
			Bind:       netip.MustParseAddrPort("127.0.0.1:21000"),
		},
		networkpolicy.Listener{
			Advertised: netip.MustParseAddrPort("127.0.0.1:80"),
			Bind:       netip.MustParseAddrPort("127.0.0.1:21001"),
		},
		networkpolicy.Listener{
			Advertised: netip.MustParseAddrPort("127.0.0.1:443"),
			Bind:       netip.MustParseAddrPort("127.0.0.1:21002"),
		},
	)
	if err != nil {
		t.Fatalf("networkpolicy.New() error = %v", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatalf("Policy.Fingerprint() error = %v", err)
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	target := ownership.Record{
		SchemaVersion:            ownership.NetworkPolicySchemaVersion,
		InstallationID:           "installation-trust-test",
		OwnerIdentity:            "501",
		Generation:               3,
		LoopbackPoolPrefix:       "127.77.0.0/29",
		NetworkPolicyFingerprint: policyFingerprint,
		TicketVerifierKey:        base64.StdEncoding.EncodeToString(public),
	}
	queued, err := domain.NewOperation(
		"operation-trust-setup",
		"intent-trust-setup",
		domain.OperationKindNetworkDataPlaneSetup,
		"",
		now,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	running, err := queued.Transition(
		domain.OperationRunning,
		"preparing trust",
		now.Add(time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	approval, err := running.Transition(
		domain.OperationRequiresApproval,
		string(TrustCheckpointPhaseSetupApproval),
		now.Add(2*time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("Transition(requires approval) error = %v", err)
	}
	plan := TrustPlan{
		Purpose:            TrustPlanPurposeDataPlaneSetup,
		Operation:          approval,
		OperationRevision:  4,
		CheckpointRevision: 0,
		CheckpointPhase:    TrustCheckpointPhaseSetupApproval,
		Mutation:           helper.OperationEnsureTrust,
		TargetOwnership:    target,
		Policy:             policy,
		Root:               root,
	}
	request, err := platformtrust.NewRequestForRequester(target.InstallationID, target.OwnerIdentity, policy.Mechanisms.Trust, root)
	if err != nil {
		t.Fatalf("trust.NewRequestForRequester() error = %v", err)
	}
	observation := trustObservationForState(t, request, platformtrust.StateAbsent)
	fingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatalf("TargetOwnership.Fingerprint() error = %v", err)
	}
	plans := &scriptedTrustPlanSource{plans: []TrustPlan{cloneTrustPlan(plan), cloneTrustPlan(plan)}}
	ownershipObserver := &scriptedOwnershipObserver{observations: []ownership.Observation{{Exists: true, Record: target, Fingerprint: fingerprint}}}
	keys := &staticKeyLoader{key: private}
	publisher := &capturingPublisher{reference: helper.TicketReference(strings.Repeat("a", 64))}
	observer := &scriptedTrustObserver{observations: []platformtrust.Observation{observation, observation}}
	service := NewTrustService(
		plans,
		ownershipObserver,
		keys,
		publisher,
		observer,
		fixedClock{now: now},
		bytes.NewReader(bytes.Repeat([]byte{0x5a}, ticketNonceBytes*4)),
	)
	return &trustIssuerFixture{
		now:  now,
		plan: cloneTrustPlan(plan),
		request: TrustRequest{
			OperationID: plan.Operation.ID,
		},
		private:     private,
		plans:       plans,
		ownership:   ownershipObserver,
		keys:        keys,
		publisher:   publisher,
		observer:    observer,
		service:     service,
		observation: observation,
	}
}

// validGlobalReleaseTrustPlan derives one exact release-trust plan from setup-owned policy and root fixtures.
func validGlobalReleaseTrustPlan(t *testing.T, setup TrustPlan) TrustPlan {
	t.Helper()
	queued, err := domain.NewOperation(
		"operation-trust-release",
		"intent-trust-release",
		domain.OperationKindNetworkRelease,
		"",
		setup.Operation.RequestedAt,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	running, err := queued.Transition(
		domain.OperationRunning,
		"releasing network runtime",
		setup.Operation.RequestedAt.Add(time.Second),
		nil,
	)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	return TrustPlan{
		Purpose:            TrustPlanPurposeGlobalNetworkRelease,
		Operation:          running,
		OperationRevision:  setup.OperationRevision,
		CheckpointRevision: 7,
		CheckpointPhase:    TrustCheckpointPhaseGlobalRelease,
		Mutation:           helper.OperationReleaseTrust,
		TargetOwnership:    setup.TargetOwnership,
		Policy:             setup.Policy,
		Root:               setup.Root,
	}
}

// trustObservationForState constructs one validated observation with the requested classification.
func trustObservationForState(t *testing.T, request platformtrust.Request, state platformtrust.State) platformtrust.Observation {
	t.Helper()
	owned := platformtrust.Entry{
		Mechanism:              request.Mechanism(),
		NativeID:               "owned",
		CertificateFingerprint: request.AuthorityFingerprint(),
		NativeExact:            true,
		NativeAttributesSHA256: strings.Repeat("c", 64),
		Owner:                  pointerToTrustOwner(request.OwnerMarker()),
	}
	observation := platformtrust.Observation{Request: request, Complete: true, Entries: []platformtrust.Entry{}}
	switch state {
	case platformtrust.StateAbsent:
	case platformtrust.StateExact:
		observation.Entries = []platformtrust.Entry{owned}
	case platformtrust.StateOwnedDrifted:
		owned.NativeExact = false
		observation.Entries = []platformtrust.Entry{owned}
	case platformtrust.StateForeign:
		owned.NativeID = "foreign"
		owned.Owner = nil
		observation.Entries = []platformtrust.Entry{owned}
	case platformtrust.StateAmbiguous:
		second := owned
		second.NativeID = "owned-second"
		observation.Entries = []platformtrust.Entry{owned, second}
	case platformtrust.StateIndeterminate:
		observation.Complete = false
	default:
		t.Fatalf("unsupported trust state fixture %q", state)
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("trust observation for %q is invalid: %v", state, err)
	}
	assessment, err := observation.Classify()
	if err != nil {
		t.Fatalf("Classify(%q) error = %v", state, err)
	}
	if assessment.State != state {
		t.Fatalf("trust observation state = %q, want %q", assessment.State, state)
	}
	return observation
}

// trustObservationForRequest creates one valid absent observation for a deliberately distinct request.
func trustObservationForRequest(
	t *testing.T,
	installationID string,
	requesterIdentity string,
	mechanism networkpolicy.TrustMechanism,
	root certificates.Root,
) platformtrust.Observation {
	t.Helper()
	request, err := platformtrust.NewRequestForRequester(installationID, requesterIdentity, mechanism, root)
	if err != nil {
		t.Fatalf("trust.NewRequestForRequester() error = %v", err)
	}
	return trustObservationForState(t, request, platformtrust.StateAbsent)
}

// pointerToTrustOwner allocates one immutable marker for a trust observation fixture.
func pointerToTrustOwner(marker platformtrust.OwnerMarker) *platformtrust.OwnerMarker {
	return &marker
}

// trustCloseLog records deterministic store release order.
type trustCloseLog struct {
	mutex   sync.Mutex
	entries []string
}

// record appends one store name under the log boundary.
func (log *trustCloseLog) record(entry string) {
	if log == nil {
		return
	}
	log.mutex.Lock()
	log.entries = append(log.entries, entry)
	log.mutex.Unlock()
}

// snapshot returns an isolated close-order observation.
func (log *trustCloseLog) snapshot() []string {
	if log == nil {
		return nil
	}
	log.mutex.Lock()
	defer log.mutex.Unlock()
	return append([]string(nil), log.entries...)
}

// trustClosingKeyStore adds observable closure to one test key loader.
type trustClosingKeyStore struct {
	KeyLoader
	closeErr   error
	closeCalls atomic.Int64
	log        *trustCloseLog
}

// Close records key-store release and returns its scripted failure.
func (store *trustClosingKeyStore) Close() error {
	store.closeCalls.Add(1)
	store.log.record("keys")
	return store.closeErr
}

// trustClosingPublisher adds observable closure to one test publisher.
type trustClosingPublisher struct {
	Publisher
	closeErr   error
	closeCalls atomic.Int64
	log        *trustCloseLog
}

// Close records publisher release and returns its scripted failure.
func (publisher *trustClosingPublisher) Close() error {
	publisher.closeCalls.Add(1)
	publisher.log.record("publisher")
	return publisher.closeErr
}

// blockingFirstTrustPlanSource holds the first durable read while counting every resolution.
type blockingFirstTrustPlanSource struct {
	plan    TrustPlan
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

// Resolve blocks only the first call and returns an isolated valid plan thereafter.
func (source *blockingFirstTrustPlanSource) Resolve(context.Context, TrustRequest) (TrustPlan, error) {
	if source.calls.Add(1) == 1 {
		close(source.entered)
		<-source.release
	}
	return cloneTrustPlan(source.plan), nil
}

// signaledTrustContext reports when Issue has completed its pre-lock cancellation check.
type signaledTrustContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

// Err delegates cancellation while publishing the first live observation.
func (ctx *signaledTrustContext) Err() error {
	err := ctx.Context.Err()
	if err == nil {
		ctx.once.Do(func() { close(ctx.checked) })
	}
	return err
}

// blockingTrustPublisher holds durable publication until its owner permits completion.
type blockingTrustPublisher struct {
	reference helper.TicketReference
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

// Publish exposes the in-flight serialized boundary before returning one reference.
func (publisher *blockingTrustPublisher) Publish(context.Context, helper.Ticket, ed25519.PrivateKey) (helper.TicketReference, error) {
	publisher.once.Do(func() { close(publisher.entered) })
	<-publisher.release
	return publisher.reference, nil
}

// waitTrustSignal bounds one concurrency checkpoint.
func waitTrustSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// waitTrustError bounds one concurrent lifecycle result.
func waitTrustError(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}
