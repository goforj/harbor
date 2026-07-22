package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkplan"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/resolver"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
)

var errNetworkResolverSetupTest = errors.New("network resolver setup test error")

// networkResolverSetupTestClock provides deterministic coordinator admission time.
type networkResolverSetupTestClock struct {
	now time.Time
}

// Now returns the fixture's deterministic instant.
func (clock networkResolverSetupTestClock) Now() time.Time {
	return clock.now
}

// networkResolverSetupTestJournal scripts each durable journal boundary independently.
type networkResolverSetupTestJournal struct {
	operation func(context.Context, domain.OperationID) (state.OperationRecord, error)
	byIntent  func(context.Context, domain.IntentID) (state.OperationRecord, error)
	stage     func(context.Context, state.StageNetworkResolverSetupRequest) (state.OperationRecord, error)
}

// Operation delegates one exact operation read to the fixture script.
func (journal *networkResolverSetupTestJournal) Operation(
	ctx context.Context,
	id domain.OperationID,
) (state.OperationRecord, error) {
	return journal.operation(ctx, id)
}

// OperationByIntent delegates one intent lookup to the fixture script.
func (journal *networkResolverSetupTestJournal) OperationByIntent(
	ctx context.Context,
	id domain.IntentID,
) (state.OperationRecord, error) {
	return journal.byIntent(ctx, id)
}

// StageNetworkResolverSetup delegates one staging mutation to the fixture script.
func (journal *networkResolverSetupTestJournal) StageNetworkResolverSetup(
	ctx context.Context,
	request state.StageNetworkResolverSetupRequest,
) (state.OperationRecord, error) {
	return journal.stage(ctx, request)
}

// networkResolverSetupTestNetwork scripts one complete network read.
type networkResolverSetupTestNetwork struct {
	read func(context.Context) (state.NetworkRecord, bool, error)
}

// Network delegates one aggregate read to the fixture script.
func (source *networkResolverSetupTestNetwork) Network(
	ctx context.Context,
) (state.NetworkRecord, bool, error) {
	return source.read(ctx)
}

// networkResolverSetupTestPlans scripts one immutable resolver-plan lookup.
type networkResolverSetupTestPlans struct {
	resolve func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error)
}

// Resolve delegates one plan lookup to the fixture script.
func (plans *networkResolverSetupTestPlans) Resolve(
	ctx context.Context,
	request ticketissuer.ResolverRequest,
) (ticketissuer.ResolverPlan, error) {
	return plans.resolve(ctx, request)
}

// networkResolverSetupTestStore scripts one atomic completion mutation.
type networkResolverSetupTestStore struct {
	complete func(context.Context, state.CompleteNetworkResolverSetupRequest) (state.CompleteNetworkResolverSetupResult, error)
}

// CompleteNetworkResolverSetup delegates one completion mutation to the fixture script.
func (store *networkResolverSetupTestStore) CompleteNetworkResolverSetup(
	ctx context.Context,
	request state.CompleteNetworkResolverSetupRequest,
) (state.CompleteNetworkResolverSetupResult, error) {
	return store.complete(ctx, request)
}

// networkResolverSetupTestRoots scripts one public certificate root read.
type networkResolverSetupTestRoots struct {
	read  func() (certificates.Root, error)
	calls int
}

// PublicRoot delegates one public root read to the fixture script.
func (roots *networkResolverSetupTestRoots) PublicRoot() (certificates.Root, error) {
	roots.calls++
	return roots.read()
}

// networkResolverSetupTestIssuer scripts capability publication and closure.
type networkResolverSetupTestIssuer struct {
	issue      func(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error)
	closeErr   error
	closeCalls int
}

// Issue delegates one capability publication to the fixture script.
func (issuer *networkResolverSetupTestIssuer) Issue(
	ctx context.Context,
	requester string,
	request ticketissuer.ResolverRequest,
) (ticketissuer.ResolverResult, error) {
	return issuer.issue(ctx, requester, request)
}

// Close records issuer closure and returns its scripted failure.
func (issuer *networkResolverSetupTestIssuer) Close() error {
	issuer.closeCalls++
	return issuer.closeErr
}

// networkResolverSetupTestOwnership scripts protected machine-ownership observations.
type networkResolverSetupTestOwnership struct {
	observe func(context.Context) (ownership.Observation, error)
	calls   int
}

// Observe delegates one protected ownership read to the fixture script.
func (observer *networkResolverSetupTestOwnership) Observe(
	ctx context.Context,
) (ownership.Observation, error) {
	observer.calls++
	return observer.observe(ctx)
}

// networkResolverSetupTestResolver scripts native resolver observations.
type networkResolverSetupTestResolver struct {
	observe func(context.Context, resolver.Request) (resolver.Observation, error)
	calls   int
}

// Observe delegates one native resolver read to the fixture script.
func (observer *networkResolverSetupTestResolver) Observe(
	ctx context.Context,
	request resolver.Request,
) (resolver.Observation, error) {
	observer.calls++
	return observer.observe(ctx, request)
}

// TestNetworkResolverSetupStartStagesCanonicalPolicyAndReplays verifies authority derivation and side-effect-free intent replay.
func TestNetworkResolverSetupStartStagesCanonicalPolicyAndReplays(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	var stagedRequest state.StageNetworkResolverSetupRequest
	fixture.journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
		return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-resolver"}
	}
	fixture.journal.stage = func(
		_ context.Context,
		request state.StageNetworkResolverSetupRequest,
	) (state.OperationRecord, error) {
		stagedRequest = request
		return networkResolverSetupTestApproval(request.Operation, 3), nil
	}

	started, err := fixture.coordinator.Start(t.Context(), NetworkResolverSetupStartRequest{
		OperationID:       "operation-resolver",
		IntentID:          "intent-resolver",
		RequesterIdentity: fixture.source.Record.OwnerIdentity,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if started.Operation.State != domain.OperationRequiresApproval || started.Revision != 3 {
		t.Fatalf("Start() = %#v", started)
	}
	policyFingerprint, err := fixture.policy.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	target := fixture.source.Record
	target.SchemaVersion = ownership.NetworkPolicySchemaVersion
	target.NetworkPolicyFingerprint = policyFingerprint
	if stagedRequest.Operation.ID != "operation-resolver" ||
		stagedRequest.Operation.IntentID != "intent-resolver" ||
		stagedRequest.Operation.Kind != domain.OperationKindNetworkResolverSetup ||
		stagedRequest.ExpectedNetworkRevision != fixture.network.Revision ||
		stagedRequest.ExpectedSourceOwnershipFingerprint != fixture.source.Fingerprint ||
		stagedRequest.TargetOwnership != target ||
		stagedRequest.Policy != fixture.policy {
		t.Fatalf("StageNetworkResolverSetup() request = %#v", stagedRequest)
	}
	if fixture.roots.calls != 1 || fixture.ownership.calls != 1 {
		t.Fatalf("Start() authority reads = roots %d, ownership %d", fixture.roots.calls, fixture.ownership.calls)
	}

	fixture.journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
		return started, nil
	}
	fixture.journal.stage = func(context.Context, state.StageNetworkResolverSetupRequest) (state.OperationRecord, error) {
		t.Fatal("replayed Start() staged another resolver operation")
		return state.OperationRecord{}, nil
	}
	fixture.networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
		t.Fatal("replayed Start() reread network authority")
		return state.NetworkRecord{}, false, nil
	}
	replayed, err := fixture.coordinator.Start(t.Context(), NetworkResolverSetupStartRequest{
		OperationID:       "operation-proposed",
		IntentID:          "intent-resolver",
		RequesterIdentity: fixture.source.Record.OwnerIdentity,
	})
	if err != nil || !reflect.DeepEqual(replayed, started) {
		t.Fatalf("replayed Start() = %#v, %v, want %#v", replayed, err, started)
	}
}

// TestNetworkResolverSetupStartRejectsUnownedOrUnreadyAuthority covers failures before staging can create approval authority.
func TestNetworkResolverSetupStartRejectsUnownedOrUnreadyAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkResolverSetupTestFixture)
	}{
		{name: "network read", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
				return state.NetworkRecord{}, false, errNetworkResolverSetupTest
			}
		}},
		{name: "network missing", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
				return state.NetworkRecord{}, false, nil
			}
		}},
		{name: "network invalid", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.network.Leases = nil
		}},
		{name: "wrong stage", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.network.Stage = state.NetworkStageResolver
		}},
		{name: "ownership read", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return ownership.Observation{}, errNetworkResolverSetupTest
			}
		}},
		{name: "ownership absent", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return ownership.Observation{}, nil
			}
		}},
		{name: "ownership crossed", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.source.Record.Generation++
			fixture.source.Fingerprint = networkResolverSetupTestOwnershipFingerprint(t, fixture.source.Record)
		}},
		{name: "wrong requester", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.source.Record.OwnerIdentity = "502"
			fixture.source.Fingerprint = networkResolverSetupTestOwnershipFingerprint(t, fixture.source.Record)
		}},
		{name: "root unavailable", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.roots.read = func() (certificates.Root, error) {
				return certificates.Root{}, errNetworkResolverSetupTest
			}
		}},
		{name: "platform", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.coordinator.platform = "unsupported"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupTestFixture(t)
			fixture.journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
				return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-resolver"}
			}
			fixture.journal.stage = func(context.Context, state.StageNetworkResolverSetupRequest) (state.OperationRecord, error) {
				t.Fatal("Start() staged invalid authority")
				return state.OperationRecord{}, nil
			}
			test.mutate(fixture)
			_, err := fixture.coordinator.Start(t.Context(), NetworkResolverSetupStartRequest{
				OperationID:       "operation-resolver",
				IntentID:          "intent-resolver",
				RequesterIdentity: "501",
			})
			if err == nil {
				t.Fatal("Start() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupStartRejectsJournalDivergence covers intent lookup, staging, and readback failures.
func TestNetworkResolverSetupStartRejectsJournalDivergence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkResolverSetupTestFixture)
	}{
		{name: "intent read", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
				return state.OperationRecord{}, errNetworkResolverSetupTest
			}
		}},
		{name: "missing intent crossed", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
				return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-other"}
			}
		}},
		{name: "staging", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.journal.stage = func(context.Context, state.StageNetworkResolverSetupRequest) (state.OperationRecord, error) {
				return state.OperationRecord{}, errNetworkResolverSetupTest
			}
		}},
		{name: "staging readback", mutate: func(fixture *networkResolverSetupTestFixture) {
			fixture.journal.stage = func(
				_ context.Context,
				request state.StageNetworkResolverSetupRequest,
			) (state.OperationRecord, error) {
				return state.OperationRecord{Operation: request.Operation, Revision: 1}, nil
			}
		}},
		{name: "replay conflict", mutate: func(fixture *networkResolverSetupTestFixture) {
			conflict := networkResolverSetupTestApprovalOperation(t, fixture.now, 3)
			conflict.Operation.Kind = domain.OperationKindNetworkSetup
			fixture.journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
				return conflict, nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupTestFixture(t)
			fixture.journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
				return state.OperationRecord{}, &state.OperationIntentNotFoundError{IntentID: "intent-resolver"}
			}
			fixture.journal.stage = func(
				_ context.Context,
				request state.StageNetworkResolverSetupRequest,
			) (state.OperationRecord, error) {
				return networkResolverSetupTestApproval(request.Operation, 3), nil
			}
			test.mutate(fixture)
			_, err := fixture.coordinator.Start(t.Context(), NetworkResolverSetupStartRequest{
				OperationID:       "operation-resolver",
				IntentID:          "intent-resolver",
				RequesterIdentity: "501",
			})
			if err == nil {
				t.Fatal("Start() error = nil")
			}
		})
	}

	fixture := newNetworkResolverSetupTestFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.coordinator.Start(canceled, NetworkResolverSetupStartRequest{
		OperationID: "operation-resolver", IntentID: "intent-resolver", RequesterIdentity: "501",
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start(canceled) error = %v", err)
	}
}

// TestNetworkResolverSetupPrepareIssuesExactCapabilityAndCloses verifies authenticated plan correlation and resource closure.
func TestNetworkResolverSetupPrepareIssuesExactCapabilityAndCloses(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	plan := fixture.resolverPlan(t, 3)
	fixture.plans.resolve = func(_ context.Context, request ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
		if request.OperationID != plan.Operation.ID {
			t.Fatalf("Resolve() operation = %q", request.OperationID)
		}
		return plan, nil
	}
	want := fixture.resolverResult(t, plan)
	issuer := &networkResolverSetupTestIssuer{issue: func(
		_ context.Context,
		requester string,
		request ticketissuer.ResolverRequest,
	) (ticketissuer.ResolverResult, error) {
		if requester != plan.TargetOwnership.OwnerIdentity || request.OperationID != plan.Operation.ID {
			t.Fatalf("Issue() authority = %q/%q", requester, request.OperationID)
		}
		return want, nil
	}}
	fixture.coordinator.issuers = func() (NetworkResolverSetupIssuer, error) { return issuer, nil }

	got, err := fixture.coordinator.Prepare(t.Context(), NetworkResolverSetupPrepareRequest{
		OperationID:               plan.Operation.ID,
		ExpectedOperationRevision: plan.OperationRevision,
		RequesterIdentity:         plan.TargetOwnership.OwnerIdentity,
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Prepare() = %#v, %v, want %#v", got, err, want)
	}
	if issuer.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", issuer.closeCalls)
	}

	issuerCalls := 0
	fixture.coordinator.issuers = func() (NetworkResolverSetupIssuer, error) {
		issuerCalls++
		return issuer, nil
	}
	_, err = fixture.coordinator.Prepare(t.Context(), NetworkResolverSetupPrepareRequest{
		OperationID:               plan.Operation.ID,
		ExpectedOperationRevision: plan.OperationRevision,
		RequesterIdentity:         "502",
	})
	if err == nil || issuerCalls != 0 {
		t.Fatalf("Prepare(wrong owner) error = %v, issuer calls = %d", err, issuerCalls)
	}
}

// TestNetworkResolverSetupPreparePreservesIndeterminateReference proves published capability identity is never replaced after uncertain cleanup.
func TestNetworkResolverSetupPreparePreservesIndeterminateReference(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	plan := fixture.resolverPlan(t, 3)
	fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
		return plan, nil
	}
	want := fixture.resolverResult(t, plan)
	issuer := &networkResolverSetupTestIssuer{
		issue: func(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error) {
			return want, nil
		},
		closeErr: errNetworkResolverSetupTest,
	}
	fixture.coordinator.issuers = func() (NetworkResolverSetupIssuer, error) { return issuer, nil }
	got, err := fixture.coordinator.Prepare(t.Context(), NetworkResolverSetupPrepareRequest{
		OperationID:               plan.Operation.ID,
		ExpectedOperationRevision: plan.OperationRevision,
		RequesterIdentity:         plan.TargetOwnership.OwnerIdentity,
	})
	if !errors.Is(err, ticketissuer.ErrResolverPublicationIndeterminate) || !reflect.DeepEqual(got, want) {
		t.Fatalf("Prepare(close indeterminate) = %#v, %v", got, err)
	}
	if issuer.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", issuer.closeCalls)
	}
}

// TestNetworkResolverSetupPrepareRejectsPublicationDivergence covers durable, issuer, and result failure boundaries.
func TestNetworkResolverSetupPrepareRejectsPublicationDivergence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkResolverSetupTestFixture, ticketissuer.ResolverPlan, *networkResolverSetupTestIssuer)
	}{
		{name: "plan read", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan, _ *networkResolverSetupTestIssuer) {
			fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
				return ticketissuer.ResolverPlan{}, errNetworkResolverSetupTest
			}
		}},
		{name: "plan revision", mutate: func(fixture *networkResolverSetupTestFixture, plan ticketissuer.ResolverPlan, _ *networkResolverSetupTestIssuer) {
			plan.OperationRevision++
			fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
				return plan, nil
			}
		}},
		{name: "issuer open", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan, _ *networkResolverSetupTestIssuer) {
			fixture.coordinator.issuers = func() (NetworkResolverSetupIssuer, error) {
				return nil, errNetworkResolverSetupTest
			}
		}},
		{name: "issuer issue", mutate: func(_ *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan, issuer *networkResolverSetupTestIssuer) {
			issuer.issue = func(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error) {
				return ticketissuer.ResolverResult{}, errNetworkResolverSetupTest
			}
		}},
		{name: "result", mutate: func(_ *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan, issuer *networkResolverSetupTestIssuer) {
			original := issuer.issue
			issuer.issue = func(ctx context.Context, requester string, request ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error) {
				result, err := original(ctx, requester, request)
				result.PolicyFingerprint = strings.Repeat("f", 64)
				return result, err
			}
		}},
		{name: "indeterminate result", mutate: func(_ *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan, issuer *networkResolverSetupTestIssuer) {
			issuer.issue = func(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error) {
				return ticketissuer.ResolverResult{}, ticketissuer.ErrResolverPublicationIndeterminate
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupTestFixture(t)
			plan := fixture.resolverPlan(t, 3)
			fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
				return plan, nil
			}
			want := fixture.resolverResult(t, plan)
			issuer := &networkResolverSetupTestIssuer{issue: func(context.Context, string, ticketissuer.ResolverRequest) (ticketissuer.ResolverResult, error) {
				return want, nil
			}}
			fixture.coordinator.issuers = func() (NetworkResolverSetupIssuer, error) { return issuer, nil }
			test.mutate(fixture, plan, issuer)
			_, err := fixture.coordinator.Prepare(t.Context(), NetworkResolverSetupPrepareRequest{
				OperationID:               plan.Operation.ID,
				ExpectedOperationRevision: plan.OperationRevision,
				RequesterIdentity:         plan.TargetOwnership.OwnerIdentity,
			})
			if err == nil {
				t.Fatal("Prepare() error = nil")
			}
			if test.name == "issuer issue" && issuer.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", issuer.closeCalls)
			}
		})
	}
}

// TestNetworkResolverSetupConfirmCompletesExactApproval verifies helper and native evidence converge before one atomic projection upgrade.
func TestNetworkResolverSetupConfirmCompletesExactApproval(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	approval := networkResolverSetupTestApprovalOperation(t, fixture.now, 3)
	fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
		return approval, nil
	}
	plan := fixture.resolverPlan(t, approval.Revision)
	fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
		return plan, nil
	}
	targetObservation := networkResolverSetupTestOwnershipObservation(t, plan.TargetOwnership)
	fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
		t.Fatal("initial Confirm() read the durable schema-one projection as though the helper upgrade were already committed")
		return ownership.Observation{}, nil
	}
	observed := networkResolverSetupTestExactObservation(t, plan.TargetOwnership, plan.Policy)
	fixture.resolver.observe = func(_ context.Context, request resolver.Request) (resolver.Observation, error) {
		if request.PolicyFingerprint() != observed.Request.PolicyFingerprint() {
			t.Fatalf("Observe() policy = %q", request.PolicyFingerprint())
		}
		return observed, nil
	}
	evidence := networkResolverSetupTestEvidence(t, targetObservation, observed)
	want := fixture.completionResult(t, approval, plan.TargetOwnership)
	var completed state.CompleteNetworkResolverSetupRequest
	fixture.store.complete = func(
		_ context.Context,
		request state.CompleteNetworkResolverSetupRequest,
	) (state.CompleteNetworkResolverSetupResult, error) {
		completed = request
		return want, nil
	}

	got, err := fixture.coordinator.Confirm(t.Context(), NetworkResolverSetupConfirmRequest{
		OperationID:               approval.Operation.ID,
		ExpectedOperationRevision: approval.Revision,
		ResolverEvidence:          evidence,
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Confirm() = %#v, %v, want %#v", got, err, want)
	}
	if completed.OperationID != approval.Operation.ID ||
		completed.ExpectedOperationRevision != approval.Revision ||
		completed.ResolverEvidence != evidence ||
		!reflect.DeepEqual(completed.ObservedResolver, observed) ||
		!completed.At.Equal(fixture.now) {
		t.Fatalf("CompleteNetworkResolverSetup() request = %#v", completed)
	}
}

// TestNetworkResolverSetupConfirmTerminalReplayReobservesAuthority proves plan retirement never weakens retry verification.
func TestNetworkResolverSetupConfirmTerminalReplayReobservesAuthority(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	approval := networkResolverSetupTestApprovalOperation(t, fixture.now, 3)
	plan := fixture.resolverPlan(t, approval.Revision)
	want := fixture.completionResult(t, approval, plan.TargetOwnership)
	want.Network.Replayed = true
	fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
		return want.Operation, nil
	}
	fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
		t.Fatal("terminal Confirm() resolved a retired plan")
		return ticketissuer.ResolverPlan{}, nil
	}
	fixture.network = want.Network.Record
	targetObservation := networkResolverSetupTestOwnershipObservation(t, plan.TargetOwnership)
	fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
		return targetObservation, nil
	}
	observed := networkResolverSetupTestExactObservation(t, plan.TargetOwnership, plan.Policy)
	fixture.resolver.observe = func(context.Context, resolver.Request) (resolver.Observation, error) {
		return observed, nil
	}
	evidence := networkResolverSetupTestEvidence(t, targetObservation, observed)
	completeCalls := 0
	fixture.store.complete = func(
		_ context.Context,
		request state.CompleteNetworkResolverSetupRequest,
	) (state.CompleteNetworkResolverSetupResult, error) {
		completeCalls++
		if !request.At.Equal(*want.Operation.Operation.FinishedAt) {
			t.Fatalf("terminal completion time = %s", request.At)
		}
		return want, nil
	}

	got, err := fixture.coordinator.Confirm(t.Context(), NetworkResolverSetupConfirmRequest{
		OperationID:               approval.Operation.ID,
		ExpectedOperationRevision: approval.Revision,
		ResolverEvidence:          evidence,
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Confirm(terminal) = %#v, %v, want %#v", got, err, want)
	}
	if completeCalls != 1 || fixture.roots.calls != 1 || fixture.ownership.calls != 1 || fixture.resolver.calls != 1 {
		t.Fatalf(
			"terminal replay calls = complete %d, roots %d, ownership %d, resolver %d",
			completeCalls,
			fixture.roots.calls,
			fixture.ownership.calls,
			fixture.resolver.calls,
		)
	}
}

// TestNetworkResolverSetupTerminalAuthoritySurvivesFullProgression proves later data-plane revisions do not erase resolver completion proof.
func TestNetworkResolverSetupTerminalAuthoritySurvivesFullProgression(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	plan := fixture.resolverPlan(t, 3)
	fixture.network.Stage = state.NetworkStageFull
	fixture.network.Revision = 11
	fixture.network.UpdatedAt = fixture.now.Add(time.Minute)
	fixture.network.Reservations.Listeners = networkResolverSetupTestListeners(plan.Policy, fixture.network.UpdatedAt)
	targetObservation := networkResolverSetupTestOwnershipObservation(t, plan.TargetOwnership)
	fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
		return targetObservation, nil
	}

	authority, err := fixture.coordinator.terminalAuthority(t.Context(), 5)
	if err != nil {
		t.Fatalf("terminalAuthority(full) error = %v", err)
	}
	if authority.network.Stage != state.NetworkStageFull ||
		authority.network.Revision != fixture.network.Revision ||
		authority.policy != plan.Policy ||
		authority.target != plan.TargetOwnership {
		t.Fatalf("terminalAuthority(full) = %#v", authority)
	}
}

// TestNetworkResolverSetupConfirmTerminalReplayAfterFullProgression proves later listener activation preserves exact approval retry.
func TestNetworkResolverSetupConfirmTerminalReplayAfterFullProgression(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	approval := networkResolverSetupTestApprovalOperation(t, fixture.now, 3)
	plan := fixture.resolverPlan(t, approval.Revision)
	want := fixture.completionResult(t, approval, plan.TargetOwnership)
	want.Network.Replayed = true
	want.Network.Record.Stage = state.NetworkStageFull
	want.Network.Record.Revision += 4
	want.Network.Record.UpdatedAt = fixture.now.Add(time.Minute)
	want.Network.Record.Reservations.Listeners = networkResolverSetupTestListeners(
		plan.Policy,
		want.Network.Record.UpdatedAt,
	)
	fixture.network = want.Network.Record
	fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
		return want.Operation, nil
	}
	fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
		t.Fatal("full-stage terminal Confirm() resolved a retired plan")
		return ticketissuer.ResolverPlan{}, nil
	}
	targetObservation := networkResolverSetupTestOwnershipObservation(t, plan.TargetOwnership)
	fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
		return targetObservation, nil
	}
	observed := networkResolverSetupTestExactObservation(t, plan.TargetOwnership, plan.Policy)
	fixture.resolver.observe = func(context.Context, resolver.Request) (resolver.Observation, error) {
		return observed, nil
	}
	evidence := networkResolverSetupTestEvidence(t, targetObservation, observed)
	fixture.store.complete = func(
		_ context.Context,
		request state.CompleteNetworkResolverSetupRequest,
	) (state.CompleteNetworkResolverSetupResult, error) {
		if request.ExpectedOperationRevision != approval.Revision {
			t.Fatalf("replay expected operation revision = %d", request.ExpectedOperationRevision)
		}
		return want, nil
	}

	got, err := fixture.coordinator.Confirm(t.Context(), NetworkResolverSetupConfirmRequest{
		OperationID:               approval.Operation.ID,
		ExpectedOperationRevision: approval.Revision,
		ResolverEvidence:          evidence,
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Confirm(full progression) = %#v, %v, want %#v", got, err, want)
	}
}

// TestNetworkResolverSetupConfirmRejectsDivergentPostconditions covers each independent confirmation boundary.
func TestNetworkResolverSetupConfirmRejectsDivergentPostconditions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkResolverSetupTestFixture, *helper.ResolverMutationEvidence)
	}{
		{name: "helper ownership", mutate: func(_ *networkResolverSetupTestFixture, evidence *helper.ResolverMutationEvidence) {
			evidence.OwnershipFingerprint = strings.Repeat("f", 64)
		}},
		{name: "helper policy", mutate: func(_ *networkResolverSetupTestFixture, evidence *helper.ResolverMutationEvidence) {
			evidence.PolicyFingerprint = strings.Repeat("f", 64)
		}},
		{name: "native absent", mutate: func(fixture *networkResolverSetupTestFixture, _ *helper.ResolverMutationEvidence) {
			fixture.resolver.observe = func(_ context.Context, request resolver.Request) (resolver.Observation, error) {
				return resolver.Observation{Request: request, Complete: true, Rules: []resolver.RuleFact{}}, nil
			}
		}},
		{name: "native fingerprint", mutate: func(_ *networkResolverSetupTestFixture, evidence *helper.ResolverMutationEvidence) {
			evidence.ObservationFingerprint = strings.Repeat("f", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupTestFixture(t)
			approval := networkResolverSetupTestApprovalOperation(t, fixture.now, 3)
			fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
				return approval, nil
			}
			plan := fixture.resolverPlan(t, approval.Revision)
			fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
				return plan, nil
			}
			targetObservation := networkResolverSetupTestOwnershipObservation(t, plan.TargetOwnership)
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return targetObservation, nil
			}
			observed := networkResolverSetupTestExactObservation(t, plan.TargetOwnership, plan.Policy)
			fixture.resolver.observe = func(context.Context, resolver.Request) (resolver.Observation, error) {
				return observed, nil
			}
			evidence := networkResolverSetupTestEvidence(t, targetObservation, observed)
			test.mutate(fixture, &evidence)
			fixture.store.complete = func(context.Context, state.CompleteNetworkResolverSetupRequest) (state.CompleteNetworkResolverSetupResult, error) {
				t.Fatal("Confirm() committed divergent postconditions")
				return state.CompleteNetworkResolverSetupResult{}, nil
			}
			_, err := fixture.coordinator.Confirm(t.Context(), NetworkResolverSetupConfirmRequest{
				OperationID:               approval.Operation.ID,
				ExpectedOperationRevision: approval.Revision,
				ResolverEvidence:          evidence,
			})
			if err == nil {
				t.Fatal("Confirm() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupConfirmRejectsDurableDivergence covers operation, plan, commit, and readback failures.
func TestNetworkResolverSetupConfirmRejectsDurableDivergence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkResolverSetupTestFixture, state.OperationRecord, ticketissuer.ResolverPlan)
	}{
		{name: "operation read", mutate: func(fixture *networkResolverSetupTestFixture, _ state.OperationRecord, _ ticketissuer.ResolverPlan) {
			fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
				return state.OperationRecord{}, errNetworkResolverSetupTest
			}
		}},
		{name: "operation crossed", mutate: func(fixture *networkResolverSetupTestFixture, approval state.OperationRecord, _ ticketissuer.ResolverPlan) {
			approval.Operation.ID = "operation-other"
			fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
				return approval, nil
			}
		}},
		{name: "operation revision", mutate: func(fixture *networkResolverSetupTestFixture, approval state.OperationRecord, _ ticketissuer.ResolverPlan) {
			approval.Revision++
			fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
				return approval, nil
			}
		}},
		{name: "operation state", mutate: func(fixture *networkResolverSetupTestFixture, approval state.OperationRecord, _ ticketissuer.ResolverPlan) {
			approval.Operation.State = domain.OperationRunning
			approval.Operation.Phase = "running"
			fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
				return approval, nil
			}
		}},
		{name: "plan read", mutate: func(fixture *networkResolverSetupTestFixture, _ state.OperationRecord, _ ticketissuer.ResolverPlan) {
			fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
				return ticketissuer.ResolverPlan{}, errNetworkResolverSetupTest
			}
		}},
		{name: "plan crossed", mutate: func(fixture *networkResolverSetupTestFixture, _ state.OperationRecord, plan ticketissuer.ResolverPlan) {
			plan.Operation.ID = "operation-other"
			fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
				return plan, nil
			}
		}},
		{name: "store", mutate: func(fixture *networkResolverSetupTestFixture, _ state.OperationRecord, _ ticketissuer.ResolverPlan) {
			fixture.store.complete = func(context.Context, state.CompleteNetworkResolverSetupRequest) (state.CompleteNetworkResolverSetupResult, error) {
				return state.CompleteNetworkResolverSetupResult{}, errNetworkResolverSetupTest
			}
		}},
		{name: "store readback", mutate: func(fixture *networkResolverSetupTestFixture, _ state.OperationRecord, _ ticketissuer.ResolverPlan) {
			fixture.store.complete = func(context.Context, state.CompleteNetworkResolverSetupRequest) (state.CompleteNetworkResolverSetupResult, error) {
				return state.CompleteNetworkResolverSetupResult{}, nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupTestFixture(t)
			approval := networkResolverSetupTestApprovalOperation(t, fixture.now, 3)
			fixture.journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
				return approval, nil
			}
			plan := fixture.resolverPlan(t, approval.Revision)
			fixture.plans.resolve = func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
				return plan, nil
			}
			targetObservation := networkResolverSetupTestOwnershipObservation(t, plan.TargetOwnership)
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return targetObservation, nil
			}
			observed := networkResolverSetupTestExactObservation(t, plan.TargetOwnership, plan.Policy)
			fixture.resolver.observe = func(context.Context, resolver.Request) (resolver.Observation, error) {
				return observed, nil
			}
			fixture.store.complete = func(context.Context, state.CompleteNetworkResolverSetupRequest) (state.CompleteNetworkResolverSetupResult, error) {
				return fixture.completionResult(t, approval, plan.TargetOwnership), nil
			}
			test.mutate(fixture, approval, plan)
			_, err := fixture.coordinator.Confirm(t.Context(), NetworkResolverSetupConfirmRequest{
				OperationID:               approval.Operation.ID,
				ExpectedOperationRevision: approval.Revision,
				ResolverEvidence:          networkResolverSetupTestEvidence(t, targetObservation, observed),
			})
			if err == nil {
				t.Fatal("Confirm() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupRequestValidation covers every caller-owned identity and evidence branch.
func TestNetworkResolverSetupRequestValidation(t *testing.T) {
	validEvidence := helper.ResolverMutationEvidence{
		PolicyFingerprint:      strings.Repeat("a", 64),
		OwnershipFingerprint:   strings.Repeat("b", 64),
		ObservationFingerprint: strings.Repeat("c", 64),
		Postcondition:          helper.ResolverPostconditionExact,
	}
	tests := []struct {
		name    string
		request interface{ Validate() error }
		valid   bool
	}{
		{
			name: "start valid",
			request: NetworkResolverSetupStartRequest{
				OperationID: "operation-resolver", IntentID: "intent-resolver", RequesterIdentity: "501",
			},
			valid: true,
		},
		{name: "start operation", request: NetworkResolverSetupStartRequest{IntentID: "intent-resolver", RequesterIdentity: "501"}},
		{name: "start intent", request: NetworkResolverSetupStartRequest{OperationID: "operation-resolver", RequesterIdentity: "501"}},
		{name: "start requester", request: NetworkResolverSetupStartRequest{OperationID: "operation-resolver", IntentID: "intent-resolver"}},
		{
			name: "prepare valid",
			request: NetworkResolverSetupPrepareRequest{
				OperationID: "operation-resolver", ExpectedOperationRevision: 3, RequesterIdentity: "501",
			},
			valid: true,
		},
		{name: "prepare operation", request: NetworkResolverSetupPrepareRequest{ExpectedOperationRevision: 3, RequesterIdentity: "501"}},
		{name: "prepare revision", request: NetworkResolverSetupPrepareRequest{OperationID: "operation-resolver", RequesterIdentity: "501"}},
		{name: "prepare requester", request: NetworkResolverSetupPrepareRequest{OperationID: "operation-resolver", ExpectedOperationRevision: 3}},
		{
			name: "confirm valid",
			request: NetworkResolverSetupConfirmRequest{
				OperationID: "operation-resolver", ExpectedOperationRevision: 3, ResolverEvidence: validEvidence,
			},
			valid: true,
		},
		{name: "confirm operation", request: NetworkResolverSetupConfirmRequest{ExpectedOperationRevision: 3, ResolverEvidence: validEvidence}},
		{name: "confirm revision", request: NetworkResolverSetupConfirmRequest{OperationID: "operation-resolver", ResolverEvidence: validEvidence}},
		{
			name: "confirm evidence",
			request: NetworkResolverSetupConfirmRequest{
				OperationID: "operation-resolver", ExpectedOperationRevision: 3,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

// TestNetworkResolverSetupValidationRejectsCrossedAuthority directly covers every strict readback boundary.
func TestNetworkResolverSetupValidationRejectsCrossedAuthority(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	approval := networkResolverSetupTestApprovalOperation(t, fixture.now, 3)
	queued, err := domain.NewOperation(
		"operation-resolver",
		"intent-resolver",
		domain.OperationKindNetworkResolverSetup,
		"",
		fixture.now,
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	wrongKind := approval
	wrongKind.Operation.Kind = domain.OperationKindNetworkSetup
	invalidOperation := approval
	invalidOperation.Operation.StartedAt = nil
	invalidRevision := approval
	invalidRevision.Revision = 0
	if err := validateExistingNetworkResolverSetupOperation(approval, approval.Operation.IntentID); err != nil {
		t.Fatalf("validateExistingNetworkResolverSetupOperation(valid) error = %v", err)
	}
	for _, test := range []struct {
		name   string
		record state.OperationRecord
		intent domain.IntentID
	}{
		{name: "intent", record: approval, intent: "intent-other"},
		{name: "kind", record: wrongKind, intent: approval.Operation.IntentID},
		{name: "operation", record: invalidOperation, intent: approval.Operation.IntentID},
		{name: "revision", record: invalidRevision, intent: approval.Operation.IntentID},
	} {
		t.Run("existing "+test.name, func(t *testing.T) {
			if err := validateExistingNetworkResolverSetupOperation(test.record, test.intent); err == nil {
				t.Fatal("validateExistingNetworkResolverSetupOperation() error = nil")
			}
		})
	}
	if err := validateStagedNetworkResolverSetupOperation(approval, approval.Operation.IntentID); err != nil {
		t.Fatalf("validateStagedNetworkResolverSetupOperation(valid) error = %v", err)
	}
	if err := validateStagedNetworkResolverSetupOperation(
		state.OperationRecord{Operation: queued, Revision: 1},
		queued.IntentID,
	); err == nil {
		t.Fatal("validateStagedNetworkResolverSetupOperation(queued) error = nil")
	}
	if err := validateStagedNetworkResolverSetupOperation(wrongKind, approval.Operation.IntentID); err == nil {
		t.Fatal("validateStagedNetworkResolverSetupOperation(wrong kind) error = nil")
	}

	if err := validateConfirmNetworkResolverSetupOperation(approval, approval.Operation.ID); err != nil {
		t.Fatalf("validateConfirmNetworkResolverSetupOperation(valid) error = %v", err)
	}
	for _, test := range []struct {
		name   string
		record state.OperationRecord
		id     domain.OperationID
	}{
		{name: "identity", record: approval, id: "operation-other"},
		{name: "kind", record: wrongKind, id: wrongKind.Operation.ID},
		{name: "operation", record: invalidOperation, id: invalidOperation.Operation.ID},
		{name: "revision", record: invalidRevision, id: invalidRevision.Operation.ID},
	} {
		t.Run("confirm "+test.name, func(t *testing.T) {
			if err := validateConfirmNetworkResolverSetupOperation(test.record, test.id); err == nil {
				t.Fatal("validateConfirmNetworkResolverSetupOperation() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupPlanAndResultValidationRejectDivergence covers durable-plan and publication correlation failures.
func TestNetworkResolverSetupPlanAndResultValidationRejectDivergence(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	plan := fixture.resolverPlan(t, 3)
	if err := validateNetworkResolverSetupPlan(plan, plan.Operation.ID, plan.OperationRevision); err != nil {
		t.Fatalf("validateNetworkResolverSetupPlan(valid) error = %v", err)
	}
	for _, test := range []struct {
		name     string
		mutate   func(*ticketissuer.ResolverPlan)
		selected domain.OperationID
		revision domain.Sequence
	}{
		{
			name: "purpose",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.Purpose = ticketissuer.ResolverPlanPurposeGlobalRelease
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "checkpoint revision",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.CheckpointRevision = 1
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "checkpoint phase",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.CheckpointPhase = ticketissuer.ResolverCheckpointPhaseGlobalRelease
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "kind",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.Operation.Kind = domain.OperationKindNetworkRelease
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "state",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.Operation.State = domain.OperationRunning
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "phase",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.Operation.Phase = "resolver"
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "mutation",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.Mutation = helper.OperationReleaseResolver
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "source fingerprint",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.ExpectedSourceOwnershipFingerprint = strings.Repeat("f", 64)
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name: "operation",
			mutate: func(candidate *ticketissuer.ResolverPlan) {
				candidate.Operation.ID = "operation-other"
			},
			selected: plan.Operation.ID,
			revision: plan.OperationRevision,
		},
		{
			name:     "revision",
			selected: plan.Operation.ID,
			revision: plan.OperationRevision + 1,
		},
	} {
		t.Run("plan "+test.name, func(t *testing.T) {
			candidate := plan
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			if err := validateNetworkResolverSetupPlan(candidate, test.selected, test.revision); err == nil {
				t.Fatal("validateNetworkResolverSetupPlan() error = nil")
			}
		})
	}

	result := fixture.resolverResult(t, plan)
	if err := validateNetworkResolverSetupResult(result, plan, fixture.now); err != nil {
		t.Fatalf("validateNetworkResolverSetupResult(valid) error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ticketissuer.ResolverResult)
	}{
		{name: "invalid", mutate: func(result *ticketissuer.ResolverResult) { result.Reference = "bad" }},
		{name: "operation", mutate: func(result *ticketissuer.ResolverResult) { result.OperationID = "operation-other" }},
		{name: "policy", mutate: func(result *ticketissuer.ResolverResult) { result.PolicyFingerprint = strings.Repeat("f", 64) }},
		{name: "ownership", mutate: func(result *ticketissuer.ResolverResult) { result.OwnershipFingerprint = strings.Repeat("f", 64) }},
	} {
		t.Run("result "+test.name, func(t *testing.T) {
			candidate := result
			test.mutate(&candidate)
			if err := validateNetworkResolverSetupResult(candidate, plan, fixture.now); err == nil {
				t.Fatal("validateNetworkResolverSetupResult() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupEvidenceAndOwnershipValidationRejectDivergence covers every independently supplied proof field.
func TestNetworkResolverSetupEvidenceAndOwnershipValidationRejectDivergence(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	validEvidence := helper.ResolverMutationEvidence{
		PolicyFingerprint:      strings.Repeat("a", 64),
		OwnershipFingerprint:   strings.Repeat("b", 64),
		ObservationFingerprint: strings.Repeat("c", 64),
		Postcondition:          helper.ResolverPostconditionExact,
	}
	if err := validateNetworkResolverSetupEvidence(validEvidence); err != nil {
		t.Fatalf("validateNetworkResolverSetupEvidence(valid) error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*helper.ResolverMutationEvidence)
	}{
		{name: "policy", mutate: func(evidence *helper.ResolverMutationEvidence) { evidence.PolicyFingerprint = "bad" }},
		{name: "ownership", mutate: func(evidence *helper.ResolverMutationEvidence) { evidence.OwnershipFingerprint = "bad" }},
		{name: "observation", mutate: func(evidence *helper.ResolverMutationEvidence) { evidence.ObservationFingerprint = "bad" }},
		{name: "postcondition", mutate: func(evidence *helper.ResolverMutationEvidence) {
			evidence.Postcondition = helper.ResolverPostconditionOwnedAbsent
		}},
	} {
		t.Run("evidence "+test.name, func(t *testing.T) {
			candidate := validEvidence
			test.mutate(&candidate)
			if err := validateNetworkResolverSetupEvidence(candidate); err == nil {
				t.Fatal("validateNetworkResolverSetupEvidence() error = nil")
			}
		})
	}

	if err := validateNetworkResolverSetupOwnership(
		fixture.source,
		ownership.IdentitySchemaVersion,
		fixture.network,
	); err != nil {
		t.Fatalf("validateNetworkResolverSetupOwnership(valid) error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ownership.Observation, *state.NetworkRecord)
	}{
		{name: "absent", mutate: func(observation *ownership.Observation, _ *state.NetworkRecord) { observation.Exists = false }},
		{name: "record", mutate: func(observation *ownership.Observation, _ *state.NetworkRecord) {
			observation.Record.InstallationID = ""
		}},
		{name: "schema", mutate: func(observation *ownership.Observation, _ *state.NetworkRecord) {
			observation.Record.SchemaVersion = ownership.NetworkPolicySchemaVersion
			observation.Record.NetworkPolicyFingerprint = strings.Repeat("a", 64)
		}},
		{name: "fingerprint", mutate: func(observation *ownership.Observation, _ *state.NetworkRecord) {
			observation.Fingerprint = strings.Repeat("f", 64)
		}},
		{name: "network", mutate: func(_ *ownership.Observation, network *state.NetworkRecord) { network.Ownership.Generation++ }},
	} {
		t.Run("ownership "+test.name, func(t *testing.T) {
			observation := fixture.source
			network := fixture.network
			test.mutate(&observation, &network)
			if err := validateNetworkResolverSetupOwnership(
				observation,
				ownership.IdentitySchemaVersion,
				network,
			); err == nil {
				t.Fatal("validateNetworkResolverSetupOwnership() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupPlatformProfiles pins the cross-platform product mapping independently of the test host.
func TestNetworkResolverSetupPlatformProfiles(t *testing.T) {
	for _, test := range []struct {
		goos string
		want networkplan.Platform
	}{
		{goos: "darwin", want: networkplan.PlatformMacOS},
		{goos: "linux", want: networkplan.PlatformUbuntu2404},
		{goos: "windows", want: networkplan.PlatformWindows11},
	} {
		t.Run(test.goos, func(t *testing.T) {
			got, err := networkResolverSetupPlatform(test.goos)
			if err != nil || got != test.want {
				t.Fatalf("networkResolverSetupPlatform(%q) = %q, %v", test.goos, got, err)
			}
		})
	}
	if _, err := networkResolverSetupPlatform("plan9"); err == nil {
		t.Fatal("networkResolverSetupPlatform(unsupported) error = nil")
	}
	if current, err := CurrentNetworkResolverSetupPlatform(); err != nil || current == "" {
		t.Fatalf("CurrentNetworkResolverSetupPlatform() = %q, %v", current, err)
	}
}

// TestNetworkResolverSetupTerminalAuthorityRejectsDivergence covers every durable reconstruction dependency.
func TestNetworkResolverSetupTerminalAuthorityRejectsDivergence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*networkResolverSetupTestFixture, ticketissuer.ResolverPlan)
	}{
		{name: "network read", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
				return state.NetworkRecord{}, false, errNetworkResolverSetupTest
			}
		}},
		{name: "network missing", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
				return state.NetworkRecord{}, false, nil
			}
		}},
		{name: "network invalid", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.network.Leases = nil
		}},
		{name: "stage", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.network.Stage = state.NetworkStageIdentity
		}},
		{name: "revision", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.network.Revision = 4
		}},
		{name: "policy", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.coordinator.platform = "unsupported"
		}},
		{name: "ownership read", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return ownership.Observation{}, errNetworkResolverSetupTest
			}
		}},
		{name: "ownership", mutate: func(fixture *networkResolverSetupTestFixture, _ ticketissuer.ResolverPlan) {
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return ownership.Observation{}, nil
			}
		}},
		{name: "ownership policy", mutate: func(fixture *networkResolverSetupTestFixture, plan ticketissuer.ResolverPlan) {
			target := plan.TargetOwnership
			target.NetworkPolicyFingerprint = strings.Repeat("f", 64)
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return networkResolverSetupTestOwnershipObservation(t, target), nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkResolverSetupTestFixture(t)
			plan := fixture.resolverPlan(t, 3)
			fixture.network.Stage = state.NetworkStageResolver
			fixture.network.Revision = 5
			fixture.ownership.observe = func(context.Context) (ownership.Observation, error) {
				return networkResolverSetupTestOwnershipObservation(t, plan.TargetOwnership), nil
			}
			test.mutate(fixture, plan)
			if _, err := fixture.coordinator.terminalAuthority(t.Context(), 5); err == nil {
				t.Fatal("terminalAuthority() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupIndependentObserversRejectDivergence covers native resolver read failures.
func TestNetworkResolverSetupIndependentObserversRejectDivergence(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	plan := fixture.resolverPlan(t, 3)
	exact := networkResolverSetupTestExactObservation(t, plan.TargetOwnership, plan.Policy)
	fixture.resolver.observe = func(context.Context, resolver.Request) (resolver.Observation, error) {
		return exact, nil
	}
	if _, err := fixture.coordinator.observeExactResolver(t.Context(), plan.TargetOwnership, plan.Policy); err != nil {
		t.Fatalf("observeExactResolver(valid) error = %v", err)
	}
	for _, test := range []struct {
		name    string
		target  ownership.Record
		observe func(context.Context, resolver.Request) (resolver.Observation, error)
	}{
		{name: "request", target: ownership.Record{}, observe: fixture.resolver.observe},
		{name: "read", target: plan.TargetOwnership, observe: func(context.Context, resolver.Request) (resolver.Observation, error) {
			return resolver.Observation{}, errNetworkResolverSetupTest
		}},
		{name: "invalid", target: plan.TargetOwnership, observe: func(context.Context, resolver.Request) (resolver.Observation, error) {
			return resolver.Observation{}, nil
		}},
		{name: "crossed", target: plan.TargetOwnership, observe: func(context.Context, resolver.Request) (resolver.Observation, error) {
			otherPolicy := plan.Policy
			otherPolicy.AuthorityFingerprint = strings.Repeat("f", 64)
			return networkResolverSetupTestExactObservation(t, plan.TargetOwnership, otherPolicy), nil
		}},
		{name: "not exact", target: plan.TargetOwnership, observe: func(_ context.Context, request resolver.Request) (resolver.Observation, error) {
			return resolver.Observation{Request: request, Complete: true, Rules: []resolver.RuleFact{}}, nil
		}},
	} {
		t.Run("resolver "+test.name, func(t *testing.T) {
			fixture.resolver.observe = test.observe
			if _, err := fixture.coordinator.observeExactResolver(t.Context(), test.target, plan.Policy); err == nil {
				t.Fatal("observeExactResolver() error = nil")
			}
		})
	}
}

// TestNetworkResolverSetupOperationTimeHonorsDurableBoundary covers clock skew canonicalization.
func TestNetworkResolverSetupOperationTimeHonorsDurableBoundary(t *testing.T) {
	fixture := newNetworkResolverSetupTestFixture(t)
	lowerBound := fixture.now.Add(time.Minute)
	if got := fixture.coordinator.operationTime(fixture.now, lowerBound); !got.Equal(lowerBound) {
		t.Fatalf("operationTime() = %s, want %s", got, lowerBound)
	}
}

// networkResolverSetupTestFixture holds one complete valid coordinator dependency graph.
type networkResolverSetupTestFixture struct {
	now           time.Time
	network       state.NetworkRecord
	source        ownership.Observation
	policy        networkpolicy.Policy
	journal       *networkResolverSetupTestJournal
	networkSource *networkResolverSetupTestNetwork
	plans         *networkResolverSetupTestPlans
	store         *networkResolverSetupTestStore
	roots         *networkResolverSetupTestRoots
	ownership     *networkResolverSetupTestOwnership
	resolver      *networkResolverSetupTestResolver
	coordinator   *NetworkResolverSetupCoordinator
}

// newNetworkResolverSetupTestFixture constructs valid identity-stage authority with unexpected side-effect defaults.
func newNetworkResolverSetupTestFixture(t *testing.T) *networkResolverSetupTestFixture {
	t.Helper()
	now := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	pool := networkResolverSetupTestPool(t, "127.91.0.8/29")
	network := state.NetworkRecord{
		Stage:       state.NetworkStageIdentity,
		Revision:    1,
		CreatedAt:   now.Add(-time.Hour),
		UpdatedAt:   now.Add(-time.Hour),
		Ownership:   identity.Ownership{InstallationID: "installation-resolver", Generation: 1},
		Pool:        pool,
		Leases:      []identity.Lease{},
		Quarantines: []identity.Quarantine{},
		Reservations: state.DataPlaneReservations{
			Endpoints:            []state.EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
	sourceRecord := ownership.Record{
		SchemaVersion:      ownership.IdentitySchemaVersion,
		InstallationID:     "installation-resolver",
		OwnerIdentity:      "501",
		Generation:         1,
		LoopbackPoolPrefix: pool.Prefix().String(),
		TicketVerifierKey:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}
	source := networkResolverSetupTestOwnershipObservation(t, sourceRecord)
	rootFingerprint := strings.Repeat("a", 64)
	policy, err := networkplan.Build(networkplan.Request{
		Platform:             networkplan.PlatformMacOS,
		InstallationID:       network.Ownership.InstallationID,
		Pool:                 pool,
		AuthorityFingerprint: rootFingerprint,
	})
	if err != nil {
		t.Fatalf("networkplan.Build() error = %v", err)
	}
	journal := &networkResolverSetupTestJournal{
		operation: func(context.Context, domain.OperationID) (state.OperationRecord, error) {
			t.Fatal("unexpected Operation()")
			return state.OperationRecord{}, nil
		},
		byIntent: func(context.Context, domain.IntentID) (state.OperationRecord, error) {
			t.Fatal("unexpected OperationByIntent()")
			return state.OperationRecord{}, nil
		},
		stage: func(context.Context, state.StageNetworkResolverSetupRequest) (state.OperationRecord, error) {
			t.Fatal("unexpected StageNetworkResolverSetup()")
			return state.OperationRecord{}, nil
		},
	}
	networkSource := &networkResolverSetupTestNetwork{}
	plans := &networkResolverSetupTestPlans{resolve: func(context.Context, ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
		t.Fatal("unexpected Resolve()")
		return ticketissuer.ResolverPlan{}, nil
	}}
	store := &networkResolverSetupTestStore{complete: func(context.Context, state.CompleteNetworkResolverSetupRequest) (state.CompleteNetworkResolverSetupResult, error) {
		t.Fatal("unexpected CompleteNetworkResolverSetup()")
		return state.CompleteNetworkResolverSetupResult{}, nil
	}}
	roots := &networkResolverSetupTestRoots{read: func() (certificates.Root, error) {
		return certificates.Root{Fingerprint: rootFingerprint}, nil
	}}
	ownershipObserver := &networkResolverSetupTestOwnership{}
	resolverObserver := &networkResolverSetupTestResolver{observe: func(context.Context, resolver.Request) (resolver.Observation, error) {
		t.Fatal("unexpected resolver Observe()")
		return resolver.Observation{}, nil
	}}
	fixture := &networkResolverSetupTestFixture{
		now:           now,
		network:       network,
		source:        source,
		journal:       journal,
		networkSource: networkSource,
		plans:         plans,
		store:         store,
		roots:         roots,
		ownership:     ownershipObserver,
		resolver:      resolverObserver,
	}
	fixture.policy = policy
	networkSource.read = func(context.Context) (state.NetworkRecord, bool, error) {
		return fixture.network, true, nil
	}
	ownershipObserver.observe = func(context.Context) (ownership.Observation, error) {
		return fixture.source, nil
	}
	fixture.coordinator = NewNetworkResolverSetupCoordinator(
		journal,
		networkSource,
		plans,
		store,
		roots,
		func() (NetworkResolverSetupIssuer, error) {
			t.Fatal("unexpected resolver issuer factory")
			return nil, nil
		},
		ownershipObserver,
		resolverObserver,
		networkplan.PlatformMacOS,
		networkResolverSetupTestClock{now: now},
	)
	return fixture
}

// resolverPlan returns the fixture's canonical schema-two approval authority.
func (fixture *networkResolverSetupTestFixture) resolverPlan(
	t *testing.T,
	revision domain.Sequence,
) ticketissuer.ResolverPlan {
	t.Helper()
	policyFingerprint, err := fixture.policy.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	target := fixture.source.Record
	target.SchemaVersion = ownership.NetworkPolicySchemaVersion
	target.NetworkPolicyFingerprint = policyFingerprint
	startedAt := fixture.now.Add(-time.Minute)
	return ticketissuer.ResolverPlan{
		Purpose: ticketissuer.ResolverPlanPurposeSetup,
		Operation: domain.Operation{
			ID:          "operation-resolver",
			IntentID:    "intent-resolver",
			Kind:        domain.OperationKindNetworkResolverSetup,
			State:       domain.OperationRequiresApproval,
			Phase:       string(ticketissuer.ResolverCheckpointPhaseSetupApproval),
			RequestedAt: fixture.now.Add(-2 * time.Minute),
			StartedAt:   &startedAt,
		},
		OperationRevision:                  revision,
		CheckpointRevision:                 0,
		CheckpointPhase:                    ticketissuer.ResolverCheckpointPhaseSetupApproval,
		Mutation:                           helper.OperationEnsureResolver,
		ExpectedSourceOwnershipFingerprint: fixture.source.Fingerprint,
		TargetOwnership:                    target,
		Policy:                             fixture.policy,
	}
}

// resolverResult returns valid launch metadata bound to one fixture plan.
func (fixture *networkResolverSetupTestFixture) resolverResult(
	t *testing.T,
	plan ticketissuer.ResolverPlan,
) ticketissuer.ResolverResult {
	t.Helper()
	policyFingerprint, err := plan.Policy.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(policy) error = %v", err)
	}
	ownershipFingerprint := networkResolverSetupTestOwnershipFingerprint(t, plan.TargetOwnership)
	return ticketissuer.ResolverResult{
		OperationID:          plan.Operation.ID,
		Reference:            helper.TicketReference(strings.Repeat("1", 64)),
		Operation:            helper.OperationEnsureResolver,
		PolicyFingerprint:    policyFingerprint,
		OwnershipFingerprint: ownershipFingerprint,
		ExpiresAt:            fixture.now.Add(time.Minute),
	}
}

// completionResult returns an exact terminal projection after unrelated global journal progress.
func (fixture *networkResolverSetupTestFixture) completionResult(
	t *testing.T,
	approval state.OperationRecord,
	target ownership.Record,
) state.CompleteNetworkResolverSetupResult {
	t.Helper()
	startedAt := *approval.Operation.StartedAt
	finishedAt := fixture.now
	operation := approval.Operation
	operation.State = domain.OperationSucceeded
	operation.Phase = "completed"
	operation.StartedAt = &startedAt
	operation.FinishedAt = &finishedAt
	network := fixture.network
	network.Stage = state.NetworkStageResolver
	network.Revision = approval.Revision + 6
	network.UpdatedAt = finishedAt
	if err := target.Validate(); err != nil {
		t.Fatalf("target Validate() error = %v", err)
	}
	return state.CompleteNetworkResolverSetupResult{
		Operation:       state.OperationRecord{Operation: operation, Revision: approval.Revision + 7},
		NetworkRevision: approval.Revision + 6,
		Network:         state.NetworkMutationResult{Record: network},
	}
}

// networkResolverSetupTestApproval converts one queued operation into the staged approval shape.
func networkResolverSetupTestApproval(operation domain.Operation, revision domain.Sequence) state.OperationRecord {
	startedAt := operation.RequestedAt
	operation.State = domain.OperationRequiresApproval
	operation.Phase = "awaiting resolver approval"
	operation.StartedAt = &startedAt
	return state.OperationRecord{Operation: operation, Revision: revision}
}

// networkResolverSetupTestApprovalOperation constructs one valid approval operation fixture.
func networkResolverSetupTestApprovalOperation(
	t *testing.T,
	at time.Time,
	revision domain.Sequence,
) state.OperationRecord {
	t.Helper()
	operation, err := domain.NewOperation(
		"operation-resolver",
		"intent-resolver",
		domain.OperationKindNetworkResolverSetup,
		"",
		at.Add(-time.Minute),
	)
	if err != nil {
		t.Fatalf("domain.NewOperation() error = %v", err)
	}
	return networkResolverSetupTestApproval(operation, revision)
}

// networkResolverSetupTestPool constructs the complete exact-eight pool for one /29 prefix.
func networkResolverSetupTestPool(t *testing.T, value string) identity.Pool {
	t.Helper()
	prefix := netip.MustParsePrefix(value)
	addresses := make([]netip.Addr, 8)
	address := prefix.Addr()
	for index := range addresses {
		addresses[index] = address
		address = address.Next()
	}
	pool, err := identity.NewPool(prefix, addresses)
	if err != nil {
		t.Fatalf("identity.NewPool() error = %v", err)
	}
	return pool
}

// networkResolverSetupTestOwnershipObservation fingerprints one protected ownership record.
func networkResolverSetupTestOwnershipObservation(
	t *testing.T,
	record ownership.Record,
) ownership.Observation {
	t.Helper()
	return ownership.Observation{
		Exists:      true,
		Record:      record,
		Fingerprint: networkResolverSetupTestOwnershipFingerprint(t, record),
	}
}

// networkResolverSetupTestOwnershipFingerprint returns one valid protected-record digest.
func networkResolverSetupTestOwnershipFingerprint(t *testing.T, record ownership.Record) string {
	t.Helper()
	fingerprint, err := record.Fingerprint()
	if err != nil {
		t.Fatalf("Record.Fingerprint() error = %v", err)
	}
	return fingerprint
}

// networkResolverSetupTestExactObservation constructs one complete exact native rule for the policy.
func networkResolverSetupTestExactObservation(
	t *testing.T,
	target ownership.Record,
	policy networkpolicy.Policy,
) resolver.Observation {
	t.Helper()
	request, err := resolver.NewRequest(target.InstallationID, policy)
	if err != nil {
		t.Fatalf("resolver.NewRequest() error = %v", err)
	}
	owner := request.OwnerMarker()
	return resolver.Observation{
		Request:  request,
		Complete: true,
		Rules: []resolver.RuleFact{{
			Mechanism:              request.Mechanism(),
			NativeID:               "resolver-rule-resolver",
			Namespace:              request.Suffix(),
			Servers:                []netip.AddrPort{request.Endpoint()},
			RouteOnly:              true,
			NativeExact:            true,
			NativeAttributesSHA256: strings.Repeat("e", 64),
			Owner:                  &owner,
		}},
	}
}

// networkResolverSetupTestListeners converts one canonical policy into full-stage durable reservations.
func networkResolverSetupTestListeners(
	policy networkpolicy.Policy,
	verifiedAt time.Time,
) state.SharedListenerReservations {
	reservation := func(listener networkpolicy.Listener) state.ListenerReservation {
		mode := state.ListenerModeRedirect
		if listener.Advertised == listener.Bind {
			mode = state.ListenerModeDirect
		}
		return state.ListenerReservation{
			Mode:       mode,
			Advertised: listener.Advertised,
			Bind:       listener.Bind,
			Generation: 1,
			VerifiedAt: verifiedAt,
		}
	}
	return state.SharedListenerReservations{
		DNS:   reservation(policy.DNS),
		HTTP:  reservation(policy.HTTP),
		HTTPS: reservation(policy.HTTPS),
	}
}

// networkResolverSetupTestEvidence derives the correlated helper evidence from independent fixture observations.
func networkResolverSetupTestEvidence(
	t *testing.T,
	ownershipObservation ownership.Observation,
	resolverObservation resolver.Observation,
) helper.ResolverMutationEvidence {
	t.Helper()
	observationFingerprint, err := resolverObservation.Fingerprint()
	if err != nil {
		t.Fatalf("Observation.Fingerprint() error = %v", err)
	}
	return helper.ResolverMutationEvidence{
		Changed:                true,
		PolicyFingerprint:      resolverObservation.Request.PolicyFingerprint(),
		OwnershipFingerprint:   ownershipObservation.Fingerprint,
		ObservationFingerprint: observationFingerprint,
		Postcondition:          helper.ResolverPostconditionExact,
	}
}
