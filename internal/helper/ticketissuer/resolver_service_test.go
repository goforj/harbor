package ticketissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// scriptedResolverPlanSource returns one immutable plan per issuance read.
type scriptedResolverPlanSource struct {
	plans    []ResolverPlan
	errors   []error
	requests []ResolverRequest
}

// Resolve records the selected operation and returns the next scripted result.
func (source *scriptedResolverPlanSource) Resolve(_ context.Context, request ResolverRequest) (ResolverPlan, error) {
	index := len(source.requests)
	source.requests = append(source.requests, request)
	if index < len(source.errors) && source.errors[index] != nil {
		return ResolverPlan{}, source.errors[index]
	}
	if len(source.plans) == 0 {
		return ResolverPlan{}, errors.New("resolver plan script is empty")
	}
	if index >= len(source.plans) {
		index = len(source.plans) - 1
	}
	return source.plans[index], nil
}

// scriptedResolverObserver returns one native observation per issuance boundary.
type scriptedResolverObserver struct {
	observations []resolver.Observation
	errors       []error
	requests     []resolver.Request
}

// Observe records the exact request and returns the next scripted native result.
func (observer *scriptedResolverObserver) Observe(_ context.Context, request resolver.Request) (resolver.Observation, error) {
	index := len(observer.requests)
	observer.requests = append(observer.requests, request)
	if index < len(observer.errors) && observer.errors[index] != nil {
		return resolver.Observation{}, observer.errors[index]
	}
	if len(observer.observations) == 0 {
		return resolver.Observation{}, errors.New("resolver observation script is empty")
	}
	if index >= len(observer.observations) {
		index = len(observer.observations) - 1
	}
	return observer.observations[index], nil
}

// resolverIssuerFixture contains one valid transition and every replaceable authority boundary.
type resolverIssuerFixture struct {
	now         time.Time
	request     ResolverRequest
	plan        ResolverPlan
	private     ed25519.PrivateKey
	plans       *scriptedResolverPlanSource
	ownership   *scriptedOwnershipObserver
	keys        *staticKeyLoader
	publisher   *capturingPublisher
	resolver    *scriptedResolverObserver
	service     *ResolverService
	observation resolver.Observation
}

// TestResolverServiceIssueBindsTargetOwnershipAndNativeObservation proves publication contains only one exact transition.
func TestResolverServiceIssueBindsTargetOwnershipAndNativeObservation(t *testing.T) {
	fixture := newResolverIssuerFixture(t)
	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := result.Validate(fixture.now); err != nil {
		t.Fatalf("ResolverResult.Validate() error = %v", err)
	}
	if result.OperationID != fixture.plan.OperationID ||
		result.Operation != helper.OperationEnsureResolver ||
		result.PolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint {
		t.Fatalf("Issue() result = %#v", result)
	}
	if len(fixture.plans.requests) != 2 || fixture.ownership.calls != 2 || fixture.keys.calls != 1 ||
		len(fixture.resolver.requests) != 2 || fixture.publisher.calls != 1 {
		t.Fatalf(
			"Issue() calls = plans %d ownership %d keys %d resolver %d publish %d",
			len(fixture.plans.requests),
			fixture.ownership.calls,
			fixture.keys.calls,
			len(fixture.resolver.requests),
			fixture.publisher.calls,
		)
	}

	ticket := fixture.publisher.ticket
	if ticket.Operation != helper.OperationEnsureResolver ||
		ticket.InstallationID != fixture.plan.TargetOwnership.InstallationID ||
		ticket.RequesterIdentity != fixture.plan.TargetOwnership.OwnerIdentity ||
		ticket.OwnershipGeneration != fixture.plan.TargetOwnership.Generation ||
		ticket.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion ||
		ticket.NetworkPolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint ||
		ticket.ApprovedPool != fixture.plan.TargetOwnership.LoopbackPoolPrefix {
		t.Fatalf("published resolver ownership = %#v", ticket)
	}
	if ticket.NetworkPolicy == nil || *ticket.NetworkPolicy != fixture.plan.Policy ||
		ticket.ExpectedResolverObservation == nil || ticket.ApprovedAddress != "" ||
		ticket.ExpectedObservation != (helper.ExpectedObservation{}) || ticket.ExpectedPreAssignment != nil ||
		ticket.ExpectedLoopbackPool != nil {
		t.Fatalf("published ticket mixed resolver and loopback authority: %#v", ticket)
	}
	wantObservationFingerprint, err := fixture.observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if ticket.ExpectedResolverObservation.Fingerprint != wantObservationFingerprint ||
		ticket.Nonce != strings.Repeat("5a", ticketNonceBytes) ||
		ticket.ExpiresAt != fixture.now.Add(ticketLifetime) ||
		!bytes.Equal(fixture.publisher.key, fixture.private) {
		t.Fatalf("published resolver correlation = %#v", ticket)
	}
}

// TestResolverServiceIssueRevalidatesEveryAuthority prevents publication across durable or native drift.
func TestResolverServiceIssueRevalidatesEveryAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resolverIssuerFixture)
		want   string
	}{
		{
			name: "plan",
			mutate: func(fixture *resolverIssuerFixture) {
				changed := fixture.plan
				changed.OperationRevision++
				fixture.plans.plans = []ResolverPlan{fixture.plan, changed}
			},
			want: "plan changed",
		},
		{
			name: "ownership",
			mutate: func(fixture *resolverIssuerFixture) {
				fixture.ownership.observations = append(fixture.ownership.observations, ownership.Observation{})
			},
			want: "ownership projection is absent",
		},
		{
			name: "resolver",
			mutate: func(fixture *resolverIssuerFixture) {
				changed := fixture.observation
				changed.Complete = false
				fixture.resolver.observations = []resolver.Observation{fixture.observation, changed}
			},
			want: "cannot be safely ensured",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverIssuerFixture(t)
			test.mutate(fixture)
			_, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Issue() error = %v, want containing %q", err, test.want)
			}
			if fixture.publisher.calls != 0 {
				t.Fatalf("publisher calls = %d, want 0", fixture.publisher.calls)
			}
		})
	}
}

// TestResolverServiceRejectsInvalidAuthorityBeforePublication covers requester, source, observation, and signing boundaries.
func TestResolverServiceRejectsInvalidAuthorityBeforePublication(t *testing.T) {
	tests := []struct {
		name      string
		requester string
		mutate    func(*resolverIssuerFixture)
		want      string
	}{
		{name: "requester", requester: "502", want: "does not own"},
		{name: "source fingerprint", mutate: func(fixture *resolverIssuerFixture) {
			fixture.plan.ExpectedSourceOwnershipFingerprint = strings.Repeat("b", 64)
			fixture.plans.plans = []ResolverPlan{fixture.plan}
		}, want: "source ownership fingerprint"},
		{name: "resolver incomplete", mutate: func(fixture *resolverIssuerFixture) {
			fixture.observation.Complete = false
			fixture.resolver.observations = []resolver.Observation{fixture.observation}
		}, want: "cannot be safely ensured"},
		{name: "signing key", mutate: func(fixture *resolverIssuerFixture) {
			_, other, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatalf("GenerateKey() error = %v", err)
			}
			fixture.keys.key = other
		}, want: "does not match machine ownership"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverIssuerFixture(t)
			if test.mutate != nil {
				test.mutate(fixture)
			}
			requester := test.requester
			if requester == "" {
				requester = fixture.plan.TargetOwnership.OwnerIdentity
			}
			_, err := fixture.service.Issue(t.Context(), requester, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Issue() error = %v, want containing %q", err, test.want)
			}
			if fixture.publisher.calls != 0 {
				t.Fatalf("publisher calls = %d, want 0", fixture.publisher.calls)
			}
		})
	}
}

// TestResolverPlanValidationPinsOneSchemaTransition exercises every policy-specific plan boundary.
func TestResolverPlanValidationPinsOneSchemaTransition(t *testing.T) {
	fixture := newResolverIssuerFixture(t)
	tests := []struct {
		name   string
		mutate func(*ResolverPlan)
	}{
		{name: "operation", mutate: func(plan *ResolverPlan) { plan.OperationID = "" }},
		{name: "revision", mutate: func(plan *ResolverPlan) { plan.OperationRevision = 0 }},
		{name: "state", mutate: func(plan *ResolverPlan) { plan.OperationState = domain.OperationRunning }},
		{name: "mutation", mutate: func(plan *ResolverPlan) { plan.Mutation = helper.OperationReleaseResolver }},
		{name: "target schema", mutate: func(plan *ResolverPlan) {
			plan.TargetOwnership.SchemaVersion = ownership.IdentitySchemaVersion
			plan.TargetOwnership.NetworkPolicyFingerprint = ""
		}},
		{name: "target record", mutate: func(plan *ResolverPlan) { plan.TargetOwnership.InstallationID = "" }},
		{name: "policy", mutate: func(plan *ResolverPlan) { plan.Policy.Suffix = ".invalid" }},
		{name: "target policy", mutate: func(plan *ResolverPlan) { plan.TargetOwnership.NetworkPolicyFingerprint = strings.Repeat("b", 64) }},
		{name: "source", mutate: func(plan *ResolverPlan) { plan.ExpectedSourceOwnershipFingerprint = strings.Repeat("b", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := fixture.plan
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("ResolverPlan.Validate() error = nil")
			}
		})
	}
}

// TestResolverResultValidationRejectsUncorrelatedMetadata covers every client-visible launch boundary.
func TestResolverResultValidationRejectsUncorrelatedMetadata(t *testing.T) {
	fixture := newResolverIssuerFixture(t)
	valid := ResolverResult{
		OperationID:       fixture.plan.OperationID,
		Reference:         helper.TicketReference(strings.Repeat("a", 64)),
		Operation:         helper.OperationEnsureResolver,
		PolicyFingerprint: fixture.plan.TargetOwnership.NetworkPolicyFingerprint,
		ExpiresAt:         fixture.now.Add(time.Minute),
	}
	if err := valid.Validate(fixture.now); err != nil {
		t.Fatalf("valid ResolverResult error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ResolverResult)
	}{
		{name: "operation ID", mutate: func(result *ResolverResult) { result.OperationID = "" }},
		{name: "reference", mutate: func(result *ResolverResult) { result.Reference = "bad" }},
		{name: "operation", mutate: func(result *ResolverResult) { result.Operation = helper.OperationReleaseResolver }},
		{name: "fingerprint length", mutate: func(result *ResolverResult) { result.PolicyFingerprint = "bad" }},
		{name: "fingerprint case", mutate: func(result *ResolverResult) { result.PolicyFingerprint = strings.Repeat("A", 64) }},
		{name: "expiry zero", mutate: func(result *ResolverResult) { result.ExpiresAt = time.Time{} }},
		{name: "expiry local", mutate: func(result *ResolverResult) {
			result.ExpiresAt = fixture.now.In(time.FixedZone("test", 60)).Add(time.Minute)
		}},
		{name: "expiry past", mutate: func(result *ResolverResult) { result.ExpiresAt = fixture.now }},
		{name: "expiry excessive", mutate: func(result *ResolverResult) {
			result.ExpiresAt = fixture.now.Add(helper.MaxTicketLifetime + time.Nanosecond)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid
			test.mutate(&result)
			if err := result.Validate(fixture.now); err == nil {
				t.Fatal("ResolverResult.Validate() error = nil")
			}
		})
	}
}

// TestResolverServicePropagatesAuthorityFailures keeps publication behind every read and signing boundary.
func TestResolverServicePropagatesAuthorityFailures(t *testing.T) {
	cause := errors.New("scripted authority failure")
	tests := []struct {
		name   string
		mutate func(*resolverIssuerFixture)
	}{
		{name: "plan", mutate: func(fixture *resolverIssuerFixture) { fixture.plans.errors = []error{cause} }},
		{name: "ownership", mutate: func(fixture *resolverIssuerFixture) { fixture.ownership.errors = []error{cause} }},
		{name: "key", mutate: func(fixture *resolverIssuerFixture) { fixture.keys.err = cause }},
		{name: "resolver", mutate: func(fixture *resolverIssuerFixture) { fixture.resolver.errors = []error{cause} }},
		{name: "publisher", mutate: func(fixture *resolverIssuerFixture) { fixture.publisher.err = cause }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResolverIssuerFixture(t)
			test.mutate(fixture)
			_, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if !errors.Is(err, cause) {
				t.Fatalf("Issue() error = %v, want %v", err, cause)
			}
		})
	}

	fixture := newResolverIssuerFixture(t)
	fixture.service.entropy = bytes.NewReader(nil)
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err == nil || !strings.Contains(err.Error(), "generate nonce") {
		t.Fatalf("Issue(short entropy) error = %v", err)
	}
}

// TestOpenDefaultResolverServiceOwnsBothStores verifies partial-open cleanup and idempotent success closure.
func TestOpenDefaultResolverServiceOwnsBothStores(t *testing.T) {
	fixture := newResolverIssuerFixture(t)
	keyStore := &closingKeyLoader{KeyLoader: fixture.keys}
	publisher := &closingPublisher{Publisher: fixture.publisher}
	openers := resolverDefaultOpeners{
		openKeys:      func() (defaultKeyStoreCloser, error) { return keyStore, nil },
		openPublisher: func() (defaultPublisherCloser, error) { return publisher, nil },
	}
	service, err := openDefaultResolverService(fixture.plans, fixture.ownership, fixture.resolver, openers)
	if err != nil {
		t.Fatalf("openDefaultResolverService() error = %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if keyStore.closeCalls != 1 || publisher.closeCalls != 1 {
		t.Fatalf("close calls = keys %d publisher %d, want 1/1", keyStore.closeCalls, publisher.closeCalls)
	}

	keyFailure := errors.New("key open failed")
	if _, err := openDefaultResolverService(
		fixture.plans,
		fixture.ownership,
		fixture.resolver,
		resolverDefaultOpeners{
			openKeys:      func() (defaultKeyStoreCloser, error) { return nil, keyFailure },
			openPublisher: func() (defaultPublisherCloser, error) { return publisher, nil },
		},
	); !errors.Is(err, keyFailure) {
		t.Fatalf("key open error = %v", err)
	}

	keyStore.closeCalls = 0
	publisherFailure := errors.New("publisher open failed")
	if _, err := openDefaultResolverService(
		fixture.plans,
		fixture.ownership,
		fixture.resolver,
		resolverDefaultOpeners{
			openKeys:      func() (defaultKeyStoreCloser, error) { return keyStore, nil },
			openPublisher: func() (defaultPublisherCloser, error) { return nil, publisherFailure },
		},
	); !errors.Is(err, publisherFailure) || keyStore.closeCalls != 1 {
		t.Fatalf("publisher open error/cleanup = %v / %d", err, keyStore.closeCalls)
	}

	nilCases := []struct {
		name      string
		plans     ResolverPlanSource
		ownership OwnershipObserver
		resolver  ResolverObserver
		openers   resolverDefaultOpeners
	}{
		{name: "plans", ownership: fixture.ownership, resolver: fixture.resolver, openers: openers},
		{name: "ownership", plans: fixture.plans, resolver: fixture.resolver, openers: openers},
		{name: "resolver", plans: fixture.plans, ownership: fixture.ownership, openers: openers},
		{name: "openers", plans: fixture.plans, ownership: fixture.ownership, resolver: fixture.resolver},
	}
	for _, test := range nilCases {
		t.Run(test.name, func(t *testing.T) {
			if service, err := openDefaultResolverService(test.plans, test.ownership, test.resolver, test.openers); err == nil || service != nil {
				t.Fatalf("openDefaultResolverService() = (%#v, %v), want nil error result", service, err)
			}
		})
	}
}

// TestResolverServiceConstructorsFailFast covers public default routing and required dependency admission.
func TestResolverServiceConstructorsFailFast(t *testing.T) {
	fixture := newResolverIssuerFixture(t)
	if service, err := OpenDefaultResolverService(nil, fixture.ownership, fixture.resolver); err == nil || service != nil {
		t.Fatalf("OpenDefaultResolverService(nil) = (%#v, %v)", service, err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("NewResolverService(nil) did not panic")
		}
	}()
	NewResolverService(
		nil,
		fixture.ownership,
		fixture.keys,
		fixture.publisher,
		fixture.resolver,
		fixedClock{now: fixture.now},
		bytes.NewReader(nil),
	)
}

// TestResolverServiceLifecycleAndInputValidation keeps cancellation, closure, and malformed requests fail-closed.
func TestResolverServiceLifecycleAndInputValidation(t *testing.T) {
	fixture := newResolverIssuerFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.service.Issue(canceled, fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue(canceled) error = %v", err)
	}
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, ResolverRequest{}); err == nil {
		t.Fatal("Issue(invalid request) error = nil")
	}
	if err := fixture.service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := fixture.service.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Issue(closed) error = %v", err)
	}
}

// newResolverIssuerFixture creates one valid macOS policy upgrade from an exact schema-1 projection.
func newResolverIssuerFixture(t *testing.T) *resolverIssuerFixture {
	t.Helper()
	now := time.Date(2026, time.July, 20, 1, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	policy, err := networkpolicy.New(
		strings.Repeat("a", 64),
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
	target := ownership.Record{
		SchemaVersion:            ownership.NetworkPolicySchemaVersion,
		InstallationID:           "installation-resolver-test",
		OwnerIdentity:            "501",
		Generation:               1,
		LoopbackPoolPrefix:       "127.77.0.0/29",
		NetworkPolicyFingerprint: policyFingerprint,
		TicketVerifierKey:        base64.StdEncoding.EncodeToString(public),
	}
	source, sourceFingerprint, err := resolverPlanSourceOwnership(target)
	if err != nil {
		t.Fatalf("resolverPlanSourceOwnership() error = %v", err)
	}
	plan := ResolverPlan{
		OperationID:                        "operation-resolver-setup",
		OperationRevision:                  4,
		OperationState:                     domain.OperationRequiresApproval,
		Mutation:                           helper.OperationEnsureResolver,
		ExpectedSourceOwnershipFingerprint: sourceFingerprint,
		TargetOwnership:                    target,
		Policy:                             policy,
	}
	resolverRequest, err := resolver.NewRequest(target.InstallationID, policy)
	if err != nil {
		t.Fatalf("resolver.NewRequest() error = %v", err)
	}
	observation := resolver.Observation{Request: resolverRequest, Complete: true, Rules: []resolver.RuleFact{}}
	if err := observation.Validate(); err != nil {
		t.Fatalf("resolver observation error = %v", err)
	}
	projected := ownership.Observation{Exists: true, Record: source, Fingerprint: sourceFingerprint}
	plans := &scriptedResolverPlanSource{plans: []ResolverPlan{plan}}
	ownershipObserver := &scriptedOwnershipObserver{observations: []ownership.Observation{projected}}
	keys := &staticKeyLoader{key: private}
	publisher := &capturingPublisher{reference: helper.TicketReference(strings.Repeat("a", 64))}
	resolverObserver := &scriptedResolverObserver{observations: []resolver.Observation{observation}}
	service := NewResolverService(
		plans,
		ownershipObserver,
		keys,
		publisher,
		resolverObserver,
		fixedClock{now: now},
		bytes.NewReader(bytes.Repeat([]byte{0x5a}, ticketNonceBytes*4)),
	)
	return &resolverIssuerFixture{
		now:         now,
		request:     ResolverRequest{OperationID: plan.OperationID},
		plan:        plan,
		private:     private,
		plans:       plans,
		ownership:   ownershipObserver,
		keys:        keys,
		publisher:   publisher,
		resolver:    resolverObserver,
		service:     service,
		observation: observation,
	}
}
