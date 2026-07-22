package reconcile

import (
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
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/lowport"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/state"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/localca"
)

// TestNetworkDataPlaneSetupPrepareTrustPreservesIndeterminatePublication binds requester and revision without replacing a lost result.
func TestNetworkDataPlaneSetupPrepareTrustPreservesIndeterminatePublication(t *testing.T) {
	plan, _ := networkDataPlaneSetupTestTrustPlan(t)
	policy, _ := plan.Policy.Fingerprint()
	owner, _ := plan.TargetOwnership.Fingerprint()
	result := ticketissuer.TrustResult{
		OperationID:          plan.Operation.ID,
		Reference:            helper.TicketReference(strings.Repeat("d", 64)),
		Operation:            helper.OperationEnsureTrust,
		PolicyFingerprint:    policy,
		OwnershipFingerprint: owner,
		AuthorityFingerprint: plan.Root.Fingerprint,
		Mechanism:            plan.Policy.Mechanisms.Trust,
		ExpiresAt:            time.Now().UTC().Add(time.Minute),
	}
	issuer := &networkDataPlaneSetupTestTrustIssuer{result: result, err: ticketissuer.ErrTrustPublicationIndeterminate}
	c := &NetworkDataPlaneSetupCoordinator{trustPlans: networkDataPlaneSetupTestTrustPlans{plan}, trustIssuers: func() (NetworkDataPlaneSetupTrustIssuer, error) { return issuer, nil }, clock: networkDataPlaneSetupTestClock{time.Now().UTC()}}
	got, err := c.PrepareTrust(context.Background(), NetworkDataPlaneSetupPrepareTrustRequest{
		OperationID:               plan.Operation.ID,
		ExpectedOperationRevision: plan.OperationRevision,
		RequesterIdentity:         plan.TargetOwnership.OwnerIdentity,
	})
	if !errors.Is(err, ticketissuer.ErrTrustPublicationIndeterminate) || got != result || !issuer.closed || issuer.requester != plan.TargetOwnership.OwnerIdentity {
		t.Fatalf("PrepareTrust() = %#v, %v; issuer=%#v", got, err, issuer)
	}
	_, err = c.PrepareTrust(context.Background(), NetworkDataPlaneSetupPrepareTrustRequest{
		OperationID:               plan.Operation.ID,
		ExpectedOperationRevision: plan.OperationRevision + 1,
		RequesterIdentity:         plan.TargetOwnership.OwnerIdentity,
	})
	var stale *state.StaleRevisionError
	if !errors.As(err, &stale) {
		t.Fatalf("PrepareTrust(stale) error = %v", err)
	}
}

// TestNetworkDataPlaneSetupTrustObservation admits only exact ownership or an identical unowned CA.
func TestNetworkDataPlaneSetupTrustObservation(t *testing.T) {
	plan, request := networkDataPlaneSetupTestTrustPlan(t)
	exact := networkDataPlaneSetupTrustObservation(t, request, true)
	fingerprint, err := exact.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	coordinator := &NetworkDataPlaneSetupCoordinator{trust: networkDataPlaneSetupTestTrustObserver{observation: exact}}
	evidence := helper.TrustMutationEvidence{AuthorityFingerprint: plan.Root.Fingerprint, Mechanism: plan.Policy.Mechanisms.Trust, ObservationFingerprint: fingerprint, Postcondition: helper.TrustPostconditionExact}
	if err := coordinator.observeExactTrust(context.Background(), plan, evidence); err != nil {
		t.Fatalf("observeExactTrust(exact) error = %v", err)
	}

	preexisting := networkDataPlaneSetupTrustObservation(t, request, false)
	fingerprint, err = preexisting.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(preexisting) error = %v", err)
	}
	coordinator.trust = networkDataPlaneSetupTestTrustObserver{observation: preexisting}
	evidence.ObservationFingerprint, evidence.Postcondition = fingerprint, helper.TrustPostconditionPreexisting
	if err := coordinator.observeExactTrust(context.Background(), plan, evidence); err != nil {
		t.Fatalf("observeExactTrust(preexisting) error = %v", err)
	}

	preexisting.Entries[0].NativeExact = false
	coordinator.trust = networkDataPlaneSetupTestTrustObserver{observation: preexisting}
	if err := coordinator.observeExactTrust(context.Background(), plan, evidence); err == nil {
		t.Fatal("observeExactTrust(drifted preexisting) error = nil")
	}
}

// TestNetworkDataPlaneSetupLowPortObservationRequiresExactSchemaTwoPolicy rejects policy and native drift.
func TestNetworkDataPlaneSetupLowPortObservationRequiresExactSchemaTwoPolicy(t *testing.T) {
	plan := networkDataPlaneSetupTestLowPortPlan(t)
	request := plan.NativeRequest
	observation := lowport.Observation{Request: request, Complete: true, Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("a", 64)},
		{Kind: lowport.ArtifactKindService, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("b", 64)},
	}}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	policyFingerprint, _ := plan.Policy.Fingerprint()
	ownershipFingerprint, _ := plan.TargetOwnership.Fingerprint()
	coordinator := &NetworkDataPlaneSetupCoordinator{lowPorts: networkDataPlaneSetupTestLowPortObserver{observation: observation}}
	evidence := helper.LowPortMutationEvidence{PolicyFingerprint: policyFingerprint, OwnershipFingerprint: ownershipFingerprint, ObservationFingerprint: fingerprint, Postcondition: helper.LowPortPostconditionExact}
	if err := coordinator.observeExactLowPorts(context.Background(), plan, evidence); err != nil {
		t.Fatalf("observeExactLowPorts(exact) error = %v", err)
	}
	evidence.PolicyFingerprint = strings.Repeat("f", 64)
	if err := coordinator.observeExactLowPorts(context.Background(), plan, evidence); err == nil {
		t.Fatal("observeExactLowPorts(policy drift) error = nil")
	}
}

// TestNewNetworkDataPlaneSetupCoordinatorFailsFast keeps incomplete runtime wiring from silently accepting setup work.
func TestNewNetworkDataPlaneSetupCoordinatorFailsFast(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewNetworkDataPlaneSetupCoordinator(nil...) did not panic")
		}
	}()
	NewNetworkDataPlaneSetupCoordinator(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", nil)
}

// TestNewNetworkDataPlaneSetupCoordinatorRejectsTypedNilDependency prevents hidden nil interface values from passing constructor wiring checks.
func TestNewNetworkDataPlaneSetupCoordinatorRejectsTypedNilDependency(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewNetworkDataPlaneSetupCoordinator(typed nil trust observer) did not panic")
		}
	}()
	plan, _ := networkDataPlaneSetupTestTrustPlan(t)
	var observer networkDataPlaneSetupTestNilTrustObserver
	NewNetworkDataPlaneSetupCoordinator(&networkDataPlaneSetupLifecycleJournal{}, networkDataPlaneSetupLifecycleNetwork{}, networkDataPlaneSetupLifecycleProjection{}, &networkDataPlaneSetupLifecycleStore{}, networkDataPlaneSetupLifecycleRoots{}, networkDataPlaneSetupTestTrustPlans{plan}, networkDataPlaneSetupTestLowPlans{}, func() (NetworkDataPlaneSetupTrustIssuer, error) { return &networkDataPlaneSetupTestTrustIssuer{}, nil }, func() (NetworkDataPlaneSetupLowPortIssuer, error) {
		return &networkDataPlaneSetupTestLowPortIssuer{}, nil
	}, networkDataPlaneSetupLifecycleOwnership{}, observer, networkDataPlaneSetupLifecycleLowPorts{}, networkDataPlaneSetupLifecycleRuntime{}, networkDataPlaneSetupLifecycleEndpoints{}, "darwin", networkDataPlaneSetupTestClock{time.Now()})
}

// networkDataPlaneSetupTestTrustObserver returns a scripted trust observation.
type networkDataPlaneSetupTestTrustObserver struct{ observation trust.Observation }

// Observe returns the scripted trust observation.
func (observer networkDataPlaneSetupTestTrustObserver) Observe(context.Context, trust.Request) (trust.Observation, error) {
	return observer.observation, nil
}

// networkDataPlaneSetupTestCanceledNetwork records an unexpected canceled-context network read.
type networkDataPlaneSetupTestCanceledNetwork struct{ called *bool }

// Network records the read so canceled-context tests can reject it.
func (network networkDataPlaneSetupTestCanceledNetwork) Network(context.Context) (state.NetworkRecord, bool, error) {
	*network.called = true
	return state.NetworkRecord{}, false, errors.New("unexpected network read")
}

// networkDataPlaneSetupTestNilTrustObserver models a typed-nil observer hidden in an interface.
type networkDataPlaneSetupTestNilTrustObserver map[string]string

// Observe panics if constructor validation lets the typed-nil observer escape.
func (networkDataPlaneSetupTestNilTrustObserver) Observe(context.Context, trust.Request) (trust.Observation, error) {
	panic("typed nil trust observer was invoked")
}

// networkDataPlaneSetupTestLowPortObserver returns a scripted low-port observation.
type networkDataPlaneSetupTestLowPortObserver struct{ observation lowport.Observation }

// Observe returns the scripted low-port observation.
func (observer networkDataPlaneSetupTestLowPortObserver) Observe(context.Context, lowport.Request) (lowport.Observation, error) {
	return observer.observation, nil
}

// networkDataPlaneSetupTestTrustPlans returns one immutable trust plan.
type networkDataPlaneSetupTestTrustPlans struct{ plan ticketissuer.TrustPlan }

// Resolve returns the fixture trust plan.
func (source networkDataPlaneSetupTestTrustPlans) Resolve(context.Context, ticketissuer.TrustRequest) (ticketissuer.TrustPlan, error) {
	return source.plan, nil
}

// networkDataPlaneSetupTestTrustIssuer records one trust publication.
type networkDataPlaneSetupTestTrustIssuer struct {
	result    ticketissuer.TrustResult
	err       error
	requester string
	closed    bool
}

// Issue records the requester and returns the scripted result.
func (issuer *networkDataPlaneSetupTestTrustIssuer) Issue(_ context.Context, requester string, _ ticketissuer.TrustRequest) (ticketissuer.TrustResult, error) {
	issuer.requester = requester
	return issuer.result, issuer.err
}

// Close records the release of the fixture issuer.
func (issuer *networkDataPlaneSetupTestTrustIssuer) Close() error { issuer.closed = true; return nil }

// networkDataPlaneSetupTestClock supplies one deterministic instant.
type networkDataPlaneSetupTestClock struct{ now time.Time }

// Now returns the fixture instant.
func (clock networkDataPlaneSetupTestClock) Now() time.Time { return clock.now }

// networkDataPlaneSetupTestTrustPlan constructs matching trust authority and observation input.
func networkDataPlaneSetupTestTrustPlan(t *testing.T) (ticketissuer.TrustPlan, trust.Request) {
	t.Helper()
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	authority, err := localca.New(localca.Config{CAValidity: 24 * time.Hour, LeafValidity: time.Hour, Backdate: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	material := authority.Material()
	root := certificates.Root{CertificatePEM: material.CertificatePEM, Fingerprint: material.Fingerprint, NotBefore: material.NotBefore, NotAfter: material.NotAfter}
	policy, err := networkpolicy.New(root.Fingerprint, networkpolicy.MacOSMechanisms(), networkpolicy.Listener{Advertised: mustAddrPort("127.0.0.1:21000"), Bind: mustAddrPort("127.0.0.1:21000")}, networkpolicy.Listener{Advertised: mustAddrPort("127.0.0.1:80"), Bind: mustAddrPort("127.0.0.1:21001")}, networkpolicy.Listener{Advertised: mustAddrPort("127.0.0.1:443"), Bind: mustAddrPort("127.0.0.1:21002")})
	if err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(nil)
	digest, _ := policy.Fingerprint()
	target := ownership.Record{SchemaVersion: ownership.NetworkPolicySchemaVersion, InstallationID: "installation-data-plane", OwnerIdentity: "501", Generation: 7, LoopbackPoolPrefix: "127.44.0.0/29", NetworkPolicyFingerprint: digest, TicketVerifierKey: base64.StdEncoding.EncodeToString(public)}
	request, err := trust.NewRequestForRequester(target.InstallationID, target.OwnerIdentity, policy.Mechanisms.Trust, root)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := domain.NewOperation(
		"operation-data-plane",
		"intent-data-plane",
		domain.OperationKindNetworkDataPlaneSetup,
		"",
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	running, err := queued.Transition(
		domain.OperationRunning,
		"preparing trust",
		now.Add(time.Second),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := running.Transition(
		domain.OperationRequiresApproval,
		"awaiting trust approval",
		now.Add(2*time.Second),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return ticketissuer.TrustPlan{
		Purpose:            ticketissuer.TrustPlanPurposeDataPlaneSetup,
		Operation:          operation,
		OperationRevision:  41,
		CheckpointRevision: 0,
		CheckpointPhase:    ticketissuer.TrustCheckpointPhaseSetupApproval,
		Mutation:           helper.OperationEnsureTrust,
		TargetOwnership:    target,
		Policy:             policy,
		Root:               root,
	}, request
}

// networkDataPlaneSetupTrustObservation constructs an exact owned or unowned trust observation.
func networkDataPlaneSetupTrustObservation(t *testing.T, request trust.Request, owned bool) trust.Observation {
	t.Helper()
	entry := trust.Entry{Mechanism: request.Mechanism(), NativeID: "entry", CertificateFingerprint: request.AuthorityFingerprint(), NativeExact: true, NativeAttributesSHA256: strings.Repeat("c", 64)}
	if owned {
		owner := request.OwnerMarker()
		entry.Owner = &owner
	}
	return trust.Observation{Request: request, Complete: true, Entries: []trust.Entry{entry}}
}

// networkDataPlaneSetupLowPortObservation constructs an exact paired low-port observation.
func networkDataPlaneSetupLowPortObservation(request lowport.Request) lowport.Observation {
	return lowport.Observation{Request: request, Complete: true, Artifacts: []lowport.Artifact{
		{Kind: lowport.ArtifactKindPlist, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("a", 64)},
		{Kind: lowport.ArtifactKindService, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("b", 64)},
	}}
}

// networkDataPlaneSetupTestLowPortPlan constructs one low-port approval plan.
func networkDataPlaneSetupTestLowPortPlan(t *testing.T) ticketissuer.LowPortPlan {
	t.Helper()
	trust, _ := networkDataPlaneSetupTestTrustPlan(t)
	request, err := lowport.NewRequest(trust.TargetOwnership, trust.Policy)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	return ticketissuer.LowPortPlan{
		Purpose: ticketissuer.LowPortPlanPurposeDataPlaneSetup,
		Operation: domain.Operation{
			ID:          "operation-data-plane",
			IntentID:    "intent-data-plane",
			Kind:        domain.OperationKindNetworkDataPlaneSetup,
			State:       domain.OperationRequiresApproval,
			Phase:       networkDataPlaneSetupLowPortApprovalPhase,
			RequestedAt: started,
			StartedAt:   &started,
		},
		OperationRevision:  41,
		CheckpointRevision: 0,
		CheckpointPhase:    ticketissuer.LowPortCheckpointPhaseSetupApproval,
		Mutation:           helper.OperationEnsureLowPorts,
		TargetOwnership:    trust.TargetOwnership,
		Policy:             trust.Policy,
		NativeRequest:      request,
	}
}

// mustAddrPort parses a fixture listener address.
func mustAddrPort(value string) netip.AddrPort { return netip.MustParseAddrPort(value) }

// TestNetworkDataPlaneSetupConfirmLowPortsStopsAtEachDurableBoundary proves a
// retry never performs a later irreversible effect after any earlier boundary fails.
func TestNetworkDataPlaneSetupConfirmLowPortsStopsAtEachDurableBoundary(t *testing.T) {
	for _, test := range []struct {
		name string
		fail string
		want []string
	}{
		{name: "activation stage", fail: "stage", want: []string{"low-plan", "low-observe", "projection", "ownership", "root", "trust-observe", "low-observe", "stage"}},
		{name: "durable activation", fail: "store", want: []string{"low-plan", "low-observe", "projection", "ownership", "root", "trust-observe", "low-observe", "stage", "store"}},
		{name: "activation verification", fail: "verify", want: []string{"low-plan", "low-observe", "projection", "ownership", "root", "trust-observe", "low-observe", "stage", "store", "verify"}},
		{name: "first runtime activation", fail: "runtime", want: []string{"low-plan", "low-observe", "projection", "ownership", "root", "trust-observe", "low-observe", "stage", "store", "verify", "runtime"}},
		{name: "endpoint backfill", fail: "endpoints", want: []string{"low-plan", "low-observe", "projection", "ownership", "root", "trust-observe", "low-observe", "stage", "store", "verify", "runtime", "endpoints"}},
		{name: "terminal acknowledgement", fail: "complete", want: []string{"low-plan", "low-observe", "projection", "ownership", "root", "trust-observe", "low-observe", "stage", "store", "verify", "runtime", "endpoints", "runtime", "complete"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNetworkDataPlaneSetupLifecycleFixture(t, test.fail)
			_, err := fixture.coordinator.ConfirmLowPorts(t.Context(), fixture.request)
			if err == nil {
				t.Fatal("ConfirmLowPorts() error = nil")
			}
			if got := strings.Join(fixture.calls, ","); got != strings.Join(test.want, ",") {
				t.Fatalf("call order = %s, want %s", got, strings.Join(test.want, ","))
			}
		})
	}
}

// TestNetworkDataPlaneSetupConfirmLowPortsAcceptsIdenticalPreexistingTrust keeps
// the activation-time drift check aligned with the earlier authenticated proof.
func TestNetworkDataPlaneSetupConfirmLowPortsAcceptsIdenticalPreexistingTrust(t *testing.T) {
	fixture := newNetworkDataPlaneSetupLifecycleFixture(t, "preexisting")
	result, err := fixture.coordinator.ConfirmLowPorts(t.Context(), fixture.request)
	if err != nil || result.Operation.Operation.State != domain.OperationSucceeded || strings.Join(fixture.calls, ",") != "low-plan,low-observe,projection,ownership,root,trust-observe,low-observe,stage,store,verify,runtime,endpoints,runtime,complete" {
		t.Fatalf("ConfirmLowPorts(preexisting) = %#v, %v; calls=%v", result, err, fixture.calls)
	}

	fixture = newNetworkDataPlaneSetupLifecycleFixture(t, "trust-drift")
	_, err = fixture.coordinator.ConfirmLowPorts(t.Context(), fixture.request)
	if err == nil || strings.Join(fixture.calls, ",") != "low-plan,low-observe,projection,ownership,root,trust-observe" {
		t.Fatalf("ConfirmLowPorts(preexisting drift) error=%v; calls=%v", err, fixture.calls)
	}
}

// TestNetworkDataPlaneSetupPrepareLowPortsPreservesIndeterminatePublication ensures
// an uncertain helper response is returned verbatim, so callers can reconcile it.
func TestNetworkDataPlaneSetupPrepareLowPortsPreservesIndeterminatePublication(t *testing.T) {
	plan := networkDataPlaneSetupTestLowPortPlan(t)
	policy, _ := plan.Policy.Fingerprint()
	owner, _ := plan.TargetOwnership.Fingerprint()
	result := ticketissuer.LowPortResult{OperationID: plan.Operation.ID, Reference: helper.TicketReference(strings.Repeat("e", 64)), Operation: helper.OperationEnsureLowPorts, PolicyFingerprint: policy, OwnershipFingerprint: owner, ExpiresAt: time.Now().UTC().Add(time.Minute)}
	issuer := &networkDataPlaneSetupTestLowPortIssuer{result: result, err: ticketissuer.ErrLowPortPublicationIndeterminate}
	coordinator := &NetworkDataPlaneSetupCoordinator{lowPortPlans: networkDataPlaneSetupTestLowPlans{plan: plan}, lowPortIssuers: func() (NetworkDataPlaneSetupLowPortIssuer, error) { return issuer, nil }, clock: networkDataPlaneSetupTestClock{time.Now().UTC()}}
	got, err := coordinator.PrepareLowPorts(t.Context(), NetworkDataPlaneSetupPrepareLowPortsRequest{OperationID: plan.Operation.ID, ExpectedOperationRevision: plan.OperationRevision, RequesterIdentity: plan.TargetOwnership.OwnerIdentity})
	if !errors.Is(err, ticketissuer.ErrLowPortPublicationIndeterminate) || got != result || issuer.requester != plan.TargetOwnership.OwnerIdentity || !issuer.closed {
		t.Fatalf("PrepareLowPorts() = %#v, %v; issuer=%#v", got, err, issuer)
	}
}

// TestNetworkDataPlaneSetupRejectsNilIssuers prevents factories from turning malformed wiring into panics.
func TestNetworkDataPlaneSetupRejectsNilIssuers(t *testing.T) {
	trustPlan, _ := networkDataPlaneSetupTestTrustPlan(t)
	lowPlan := networkDataPlaneSetupTestLowPortPlan(t)
	var nilTrust *networkDataPlaneSetupNilTrustIssuer
	trust := &NetworkDataPlaneSetupCoordinator{trustPlans: networkDataPlaneSetupTestTrustPlans{trustPlan}, trustIssuers: func() (NetworkDataPlaneSetupTrustIssuer, error) { return nilTrust, nil }, clock: networkDataPlaneSetupTestClock{time.Now().UTC()}}
	if _, err := trust.PrepareTrust(t.Context(), NetworkDataPlaneSetupPrepareTrustRequest{
		OperationID:               trustPlan.Operation.ID,
		ExpectedOperationRevision: trustPlan.OperationRevision,
		RequesterIdentity:         trustPlan.TargetOwnership.OwnerIdentity,
	}); err == nil || !strings.Contains(err.Error(), "issuer is nil") {
		t.Fatalf("PrepareTrust(typed nil) error = %v", err)
	}
	var nilLow *networkDataPlaneSetupNilLowPortIssuer
	low := &NetworkDataPlaneSetupCoordinator{lowPortPlans: networkDataPlaneSetupTestLowPlans{lowPlan}, lowPortIssuers: func() (NetworkDataPlaneSetupLowPortIssuer, error) { return nilLow, nil }, clock: networkDataPlaneSetupTestClock{time.Now().UTC()}}
	if _, err := low.PrepareLowPorts(t.Context(), NetworkDataPlaneSetupPrepareLowPortsRequest{OperationID: lowPlan.Operation.ID, ExpectedOperationRevision: lowPlan.OperationRevision, RequesterIdentity: lowPlan.TargetOwnership.OwnerIdentity}); err == nil || !strings.Contains(err.Error(), "issuer is nil") {
		t.Fatalf("PrepareLowPorts(typed nil) error = %v", err)
	}
}

// TestNetworkDataPlaneSetupConfirmTrustRejectsDifferentSupportedMechanism rejects evidence for another supported trust store before observation or advancement.
func TestNetworkDataPlaneSetupConfirmTrustRejectsDifferentSupportedMechanism(t *testing.T) {
	plan, request := networkDataPlaneSetupTestTrustPlan(t)
	observation := networkDataPlaneSetupTrustObservation(t, request, true)
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	observed, advanced := false, false
	coordinator := &NetworkDataPlaneSetupCoordinator{
		trustPlans: networkDataPlaneSetupTestTrustPlans{plan},
		trust: networkDataPlaneSetupLifecycleTrust{observe: func(context.Context, trust.Request) (trust.Observation, error) {
			observed = true
			return observation, nil
		}},
		projections: networkDataPlaneSetupLifecycleProjection{resolve: func(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
			t.Fatal("ConfirmTrust() resolved projection for mismatched mechanism")
			return state.NetworkDataPlaneSetupProjection{}, nil
		}},
		operations: &networkDataPlaneSetupLifecycleJournal{advance: func(context.Context, state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error) {
			advanced = true
			return state.OperationRecord{}, nil
		}},
	}
	_, err = coordinator.ConfirmTrust(t.Context(), NetworkDataPlaneSetupConfirmTrustRequest{
		OperationID:               plan.Operation.ID,
		ExpectedOperationRevision: plan.OperationRevision,
		RequesterIdentity:         plan.TargetOwnership.OwnerIdentity,
		TrustEvidence: helper.TrustMutationEvidence{
			AuthorityFingerprint:   plan.Root.Fingerprint,
			Mechanism:              networkpolicy.UbuntuSystemTrust,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.TrustPostconditionExact,
		},
	})
	if err == nil || observed || advanced {
		t.Fatalf("ConfirmTrust(different mechanism) error=%v, observed=%t, advanced=%t", err, observed, advanced)
	}
}

// TestNetworkDataPlaneSetupObservationRequestMismatchRejectsCrossAuthorityFacts rejects valid observations bound to a different trust or low-port request.
func TestNetworkDataPlaneSetupObservationRequestMismatchRejectsCrossAuthorityFacts(t *testing.T) {
	trustPlan, _ := networkDataPlaneSetupTestTrustPlan(t)
	otherTrustRequest, err := trust.NewRequestForRequester(trustPlan.TargetOwnership.InstallationID, "502", trustPlan.Policy.Mechanisms.Trust, trustPlan.Root)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	trustObservation := networkDataPlaneSetupTrustObservation(t, otherTrustRequest, true)
	trustFingerprint, err := trustObservation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(trust) error = %v", err)
	}
	trustAdvanced := false
	trustCoordinator := &NetworkDataPlaneSetupCoordinator{trustPlans: networkDataPlaneSetupTestTrustPlans{trustPlan}, trust: networkDataPlaneSetupTestTrustObserver{observation: trustObservation}, projections: networkDataPlaneSetupLifecycleProjection{resolve: func(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
		t.Fatal("ConfirmTrust() resolved projection for mismatched observation request")
		return state.NetworkDataPlaneSetupProjection{}, nil
	}}, operations: &networkDataPlaneSetupLifecycleJournal{advance: func(context.Context, state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error) {
		trustAdvanced = true
		return state.OperationRecord{}, nil
	}}}
	_, err = trustCoordinator.ConfirmTrust(t.Context(), NetworkDataPlaneSetupConfirmTrustRequest{
		OperationID:               trustPlan.Operation.ID,
		ExpectedOperationRevision: trustPlan.OperationRevision,
		RequesterIdentity:         trustPlan.TargetOwnership.OwnerIdentity,
		TrustEvidence: helper.TrustMutationEvidence{
			AuthorityFingerprint:   trustPlan.Root.Fingerprint,
			Mechanism:              trustPlan.Policy.Mechanisms.Trust,
			ObservationFingerprint: trustFingerprint,
			Postcondition:          helper.TrustPostconditionExact,
		},
	})
	if err == nil || trustAdvanced {
		t.Fatalf("ConfirmTrust(mismatched observation request) error=%v, advanced=%t", err, trustAdvanced)
	}

	lowPlan := networkDataPlaneSetupTestLowPortPlan(t)
	otherOwnership := lowPlan.TargetOwnership
	otherOwnership.OwnerIdentity = "502"
	otherLowRequest, err := lowport.NewRequest(otherOwnership, lowPlan.Policy)
	if err != nil {
		t.Fatalf("NewRequest(low ports) error = %v", err)
	}
	lowObservation := networkDataPlaneSetupLowPortObservation(otherLowRequest)
	lowFingerprint, err := lowObservation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(low ports) error = %v", err)
	}
	lowAdvanced := false
	lowCoordinator := &NetworkDataPlaneSetupCoordinator{lowPortPlans: networkDataPlaneSetupTestLowPlans{lowPlan}, lowPorts: networkDataPlaneSetupTestLowPortObserver{observation: lowObservation}, projections: networkDataPlaneSetupLifecycleProjection{resolve: func(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
		t.Fatal("ConfirmLowPorts() resolved projection for mismatched observation request")
		return state.NetworkDataPlaneSetupProjection{}, nil
	}}, operations: &networkDataPlaneSetupLifecycleJournal{stage: func(context.Context, state.StageNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
		lowAdvanced = true
		return state.NetworkDataPlaneSetupActivationResult{}, nil
	}}}
	policyFingerprint, _ := lowPlan.Policy.Fingerprint()
	ownershipFingerprint, _ := lowPlan.TargetOwnership.Fingerprint()
	_, err = lowCoordinator.ConfirmLowPorts(t.Context(), NetworkDataPlaneSetupConfirmLowPortsRequest{OperationID: lowPlan.Operation.ID, ExpectedOperationRevision: lowPlan.OperationRevision, RequesterIdentity: lowPlan.TargetOwnership.OwnerIdentity, LowPortEvidence: helper.LowPortMutationEvidence{PolicyFingerprint: policyFingerprint, OwnershipFingerprint: ownershipFingerprint, ObservationFingerprint: lowFingerprint, Postcondition: helper.LowPortPostconditionExact}})
	if err == nil || lowAdvanced {
		t.Fatalf("ConfirmLowPorts(mismatched observation request) error=%v, advanced=%t", err, lowAdvanced)
	}
}

// TestNetworkDataPlaneSetupTrustObservationRejectsRootValidityMetadataMismatch rejects a trust observation whose otherwise identical request has different root validity metadata.
func TestNetworkDataPlaneSetupTrustObservationRejectsRootValidityMetadataMismatch(t *testing.T) {
	plan, _ := networkDataPlaneSetupTestTrustPlan(t)
	otherRoot := plan.Root
	otherRoot.NotAfter = otherRoot.NotAfter.Add(time.Minute)
	otherRequest, err := trust.NewRequestForRequester(plan.TargetOwnership.InstallationID, plan.TargetOwnership.OwnerIdentity, plan.Policy.Mechanisms.Trust, otherRoot)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	observation := networkDataPlaneSetupTrustObservation(t, otherRequest, true)
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	advanced := false
	coordinator := &NetworkDataPlaneSetupCoordinator{trustPlans: networkDataPlaneSetupTestTrustPlans{plan}, trust: networkDataPlaneSetupTestTrustObserver{observation: observation}, projections: networkDataPlaneSetupLifecycleProjection{resolve: func(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
		t.Fatal("ConfirmTrust() resolved projection for mismatched root validity metadata")
		return state.NetworkDataPlaneSetupProjection{}, nil
	}}, operations: &networkDataPlaneSetupLifecycleJournal{advance: func(context.Context, state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error) {
		advanced = true
		return state.OperationRecord{}, nil
	}}}
	_, err = coordinator.ConfirmTrust(t.Context(), NetworkDataPlaneSetupConfirmTrustRequest{
		OperationID:               plan.Operation.ID,
		ExpectedOperationRevision: plan.OperationRevision,
		RequesterIdentity:         plan.TargetOwnership.OwnerIdentity,
		TrustEvidence: helper.TrustMutationEvidence{
			AuthorityFingerprint:   plan.Root.Fingerprint,
			Mechanism:              plan.Policy.Mechanisms.Trust,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.TrustPostconditionExact,
		},
	})
	if err == nil || advanced {
		t.Fatalf("ConfirmTrust(mismatched root validity metadata) error=%v, advanced=%t", err, advanced)
	}
}

// TestNetworkDataPlaneSetupCanceledContextsHaveNoEffects rejects already-canceled calls before publishing, observing, or mutating durable and runtime state.
func TestNetworkDataPlaneSetupCanceledContextsHaveNoEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	trustPlan, _ := networkDataPlaneSetupTestTrustPlan(t)
	lowPlan := networkDataPlaneSetupTestLowPortPlan(t)

	t.Run("Start avoids intent and network reads", func(t *testing.T) {
		networkRead := false
		coordinator := &NetworkDataPlaneSetupCoordinator{operations: &networkDataPlaneSetupLifecycleJournal{byIntent: func(context.Context, domain.IntentID) (state.OperationRecord, error) {
			t.Fatal("Start() read intent for canceled context")
			return state.OperationRecord{}, nil
		}}, network: networkDataPlaneSetupTestCanceledNetwork{called: &networkRead}}
		_, err := coordinator.Start(ctx, NetworkDataPlaneSetupStartRequest{OperationID: "operation-canceled", IntentID: "intent-canceled", RequesterIdentity: trustPlan.TargetOwnership.OwnerIdentity})
		if !errors.Is(err, context.Canceled) || networkRead {
			t.Fatalf("Start(canceled) error=%v, networkRead=%t", err, networkRead)
		}
	})

	t.Run("PrepareTrust avoids issuer", func(t *testing.T) {
		coordinator := &NetworkDataPlaneSetupCoordinator{trustPlans: networkDataPlaneSetupTestTrustPlans{trustPlan}, trustIssuers: func() (NetworkDataPlaneSetupTrustIssuer, error) {
			t.Fatal("PrepareTrust() opened issuer for canceled context")
			return nil, nil
		}, clock: networkDataPlaneSetupTestClock{time.Now()}}
		_, err := coordinator.PrepareTrust(ctx, NetworkDataPlaneSetupPrepareTrustRequest{
			OperationID:               trustPlan.Operation.ID,
			ExpectedOperationRevision: trustPlan.OperationRevision,
			RequesterIdentity:         trustPlan.TargetOwnership.OwnerIdentity,
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PrepareTrust(canceled) error = %v", err)
		}
	})

	t.Run("PrepareLowPorts avoids plan and issuer", func(t *testing.T) {
		coordinator := &NetworkDataPlaneSetupCoordinator{lowPortPlans: networkDataPlaneSetupLifecycleLowPlans{resolve: func(context.Context, ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
			t.Fatal("PrepareLowPorts() resolved plan for canceled context")
			return ticketissuer.LowPortPlan{}, nil
		}}, lowPortIssuers: func() (NetworkDataPlaneSetupLowPortIssuer, error) {
			t.Fatal("PrepareLowPorts() opened issuer for canceled context")
			return nil, nil
		}}
		_, err := coordinator.PrepareLowPorts(ctx, NetworkDataPlaneSetupPrepareLowPortsRequest{OperationID: lowPlan.Operation.ID, ExpectedOperationRevision: lowPlan.OperationRevision, RequesterIdentity: lowPlan.TargetOwnership.OwnerIdentity})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PrepareLowPorts(canceled) error = %v", err)
		}
	})

	t.Run("ConfirmTrust avoids observer and durable advancement", func(t *testing.T) {
		coordinator := &NetworkDataPlaneSetupCoordinator{trustPlans: networkDataPlaneSetupTestTrustPlans{trustPlan}, trust: networkDataPlaneSetupLifecycleTrust{observe: func(context.Context, trust.Request) (trust.Observation, error) {
			t.Fatal("ConfirmTrust() observed trust for canceled context")
			return trust.Observation{}, nil
		}}, operations: &networkDataPlaneSetupLifecycleJournal{advance: func(context.Context, state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error) {
			t.Fatal("ConfirmTrust() advanced durable state for canceled context")
			return state.OperationRecord{}, nil
		}}}
		_, err := coordinator.ConfirmTrust(ctx, NetworkDataPlaneSetupConfirmTrustRequest{
			OperationID:               trustPlan.Operation.ID,
			ExpectedOperationRevision: trustPlan.OperationRevision,
			RequesterIdentity:         trustPlan.TargetOwnership.OwnerIdentity,
			TrustEvidence: helper.TrustMutationEvidence{
				AuthorityFingerprint:   trustPlan.Root.Fingerprint,
				Mechanism:              trustPlan.Policy.Mechanisms.Trust,
				ObservationFingerprint: strings.Repeat("a", 64),
				Postcondition:          helper.TrustPostconditionExact,
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ConfirmTrust(canceled) error = %v", err)
		}
	})

	t.Run("ConfirmLowPorts avoids observer store runtime and durable mutation", func(t *testing.T) {
		coordinator := &NetworkDataPlaneSetupCoordinator{lowPortPlans: networkDataPlaneSetupTestLowPlans{lowPlan}, lowPorts: networkDataPlaneSetupLifecycleLowPorts{observe: func(context.Context, lowport.Request) (lowport.Observation, error) {
			t.Fatal("ConfirmLowPorts() observed low ports for canceled context")
			return lowport.Observation{}, nil
		}}, store: &networkDataPlaneSetupLifecycleStore{activate: func(context.Context, state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error) {
			t.Fatal("ConfirmLowPorts() mutated store for canceled context")
			return state.NetworkMutationResult{}, nil
		}}, runtime: networkDataPlaneSetupLifecycleRuntime{activate: func(context.Context, domain.Sequence) error {
			t.Fatal("ConfirmLowPorts() activated runtime for canceled context")
			return nil
		}}, operations: &networkDataPlaneSetupLifecycleJournal{stage: func(context.Context, state.StageNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
			t.Fatal("ConfirmLowPorts() staged durable state for canceled context")
			return state.NetworkDataPlaneSetupActivationResult{}, nil
		}}}
		_, err := coordinator.ConfirmLowPorts(ctx, NetworkDataPlaneSetupConfirmLowPortsRequest{OperationID: lowPlan.Operation.ID, ExpectedOperationRevision: lowPlan.OperationRevision, RequesterIdentity: lowPlan.TargetOwnership.OwnerIdentity, LowPortEvidence: helper.LowPortMutationEvidence{PolicyFingerprint: strings.Repeat("a", 64), OwnershipFingerprint: strings.Repeat("b", 64), ObservationFingerprint: strings.Repeat("c", 64), Postcondition: helper.LowPortPostconditionExact}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ConfirmLowPorts(canceled) error = %v", err)
		}
	})

	t.Run("Recover avoids operation store and runtime", func(t *testing.T) {
		coordinator := &NetworkDataPlaneSetupCoordinator{operations: &networkDataPlaneSetupLifecycleJournal{operation: func(context.Context, domain.OperationID) (state.OperationRecord, error) {
			t.Fatal("Recover() read operation for canceled context")
			return state.OperationRecord{}, nil
		}}, store: &networkDataPlaneSetupLifecycleStore{activate: func(context.Context, state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error) {
			t.Fatal("Recover() mutated store for canceled context")
			return state.NetworkMutationResult{}, nil
		}}, runtime: networkDataPlaneSetupLifecycleRuntime{activate: func(context.Context, domain.Sequence) error {
			t.Fatal("Recover() activated runtime for canceled context")
			return nil
		}}}
		_, err := coordinator.Recover(ctx, lowPlan.Operation.ID)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recover(canceled) error = %v", err)
		}
	})
}

// TestNetworkDataPlaneSetupStartReplayAndConfirmTrustBoundary keeps intent replay
// side-effect free and binds the trust advancement to the resolved approval.
func TestNetworkDataPlaneSetupStartReplayAndConfirmTrustBoundary(t *testing.T) {
	trustPlan, trustRequest := networkDataPlaneSetupTestTrustPlan(t)
	startedAt := time.Now().UTC()
	replayed := state.OperationRecord{
		Operation: domain.Operation{
			ID:          trustPlan.Operation.ID,
			IntentID:    "intent-data-plane",
			Kind:        domain.OperationKindNetworkDataPlaneSetup,
			State:       domain.OperationRequiresApproval,
			Phase:       networkDataPlaneSetupTrustApprovalPhase,
			RequestedAt: startedAt,
			StartedAt:   &startedAt,
		},
		Revision: trustPlan.OperationRevision,
	}
	journal := &networkDataPlaneSetupLifecycleJournal{byIntent: func(_ context.Context, id domain.IntentID) (state.OperationRecord, error) {
		if id != replayed.Operation.IntentID {
			t.Fatalf("intent = %q", id)
		}
		return replayed, nil
	}}
	coordinator := NewNetworkDataPlaneSetupCoordinator(journal, networkDataPlaneSetupLifecycleNetwork{}, networkDataPlaneSetupLifecycleProjection{}, &networkDataPlaneSetupLifecycleStore{}, networkDataPlaneSetupLifecycleRoots{}, networkDataPlaneSetupTestTrustPlans{trustPlan}, networkDataPlaneSetupTestLowPlans{}, func() (NetworkDataPlaneSetupTrustIssuer, error) { t.Fatal("replay opened issuer"); return nil, nil }, func() (NetworkDataPlaneSetupLowPortIssuer, error) { return nil, nil }, networkDataPlaneSetupLifecycleOwnership{}, networkDataPlaneSetupLifecycleTrust{}, networkDataPlaneSetupLifecycleLowPorts{}, networkDataPlaneSetupLifecycleRuntime{}, networkDataPlaneSetupLifecycleEndpoints{}, "darwin", networkDataPlaneSetupTestClock{startedAt})
	got, err := coordinator.Start(t.Context(), NetworkDataPlaneSetupStartRequest{OperationID: "another-operation", IntentID: replayed.Operation.IntentID, RequesterIdentity: trustPlan.TargetOwnership.OwnerIdentity})
	if err != nil || got != replayed {
		t.Fatalf("Start(replay) = %#v, %v", got, err)
	}

	observation := networkDataPlaneSetupTrustObservation(t, trustRequest, true)
	fingerprint, _ := observation.Fingerprint()
	var advanced state.AdvanceNetworkDataPlaneSetupTrustRequest
	journal.advance = func(_ context.Context, request state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error) {
		advanced = request
		return replayed, nil
	}
	coordinator.projections = networkDataPlaneSetupLifecycleProjection{resolve: func(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
		return state.NetworkDataPlaneSetupProjection{}, nil
	}}
	coordinator.trust = networkDataPlaneSetupLifecycleTrust{observe: func(context.Context, trust.Request) (trust.Observation, error) {
		return observation, nil
	}}
	_, err = coordinator.ConfirmTrust(t.Context(), NetworkDataPlaneSetupConfirmTrustRequest{
		OperationID:               trustPlan.Operation.ID,
		ExpectedOperationRevision: trustPlan.OperationRevision,
		RequesterIdentity:         trustPlan.TargetOwnership.OwnerIdentity,
		TrustEvidence: helper.TrustMutationEvidence{
			AuthorityFingerprint:   trustPlan.Root.Fingerprint,
			Mechanism:              trustPlan.Policy.Mechanisms.Trust,
			ObservationFingerprint: fingerprint,
			Postcondition:          helper.TrustPostconditionExact,
		},
	})
	if err != nil {
		t.Fatalf("ConfirmTrust() error = %v", err)
	}
	if advanced.OperationID != trustPlan.Operation.ID || advanced.ExpectedOperationRevision != trustPlan.OperationRevision || advanced.RequesterIdentity != trustPlan.TargetOwnership.OwnerIdentity {
		t.Fatalf("AdvanceNetworkDataPlaneSetupTrust request = %#v", advanced)
	}
}

// TestNetworkDataPlaneSetupRecoverOnlyReplaysDurableActivation proves approval
// boundaries never reissue helper authority, while activation recovery resumes
// only the persisted post-helper receipt.
func TestNetworkDataPlaneSetupRecoverOnlyReplaysDurableActivation(t *testing.T) {
	plan := networkDataPlaneSetupTestLowPortPlan(t)
	operation := state.OperationRecord{Operation: plan.Operation, Revision: 91}
	journal := &networkDataPlaneSetupLifecycleJournal{}
	var calls []string
	journal.operation = func(context.Context, domain.OperationID) (state.OperationRecord, error) {
		calls = append(calls, "operation")
		return operation, nil
	}
	journal.readPlan = func(context.Context, domain.OperationID) (state.NetworkDataPlaneSetupPlanRecord, bool, error) {
		t.Fatal("approval recovery read activation plan")
		return state.NetworkDataPlaneSetupPlanRecord{}, false, nil
	}
	coordinator := NewNetworkDataPlaneSetupCoordinator(journal, networkDataPlaneSetupLifecycleNetwork{}, networkDataPlaneSetupLifecycleProjection{}, &networkDataPlaneSetupLifecycleStore{}, networkDataPlaneSetupLifecycleRoots{}, networkDataPlaneSetupTestTrustPlans{}, networkDataPlaneSetupTestLowPlans{}, func() (NetworkDataPlaneSetupTrustIssuer, error) {
		t.Fatal("approval recovery opened trust issuer")
		return nil, nil
	}, func() (NetworkDataPlaneSetupLowPortIssuer, error) {
		t.Fatal("approval recovery opened low-port issuer")
		return nil, nil
	}, networkDataPlaneSetupLifecycleOwnership{}, networkDataPlaneSetupLifecycleTrust{}, networkDataPlaneSetupLifecycleLowPorts{}, networkDataPlaneSetupLifecycleRuntime{}, networkDataPlaneSetupLifecycleEndpoints{}, "darwin", networkDataPlaneSetupTestClock{time.Now()})
	got, err := coordinator.Recover(t.Context(), plan.Operation.ID)
	if err != nil || got != operation || strings.Join(calls, ",") != "operation" {
		t.Fatalf("Recover(approval) = %#v, %v; calls=%v", got, err, calls)
	}

	operation.Operation.State, operation.Operation.Phase = domain.OperationRunning, networkDataPlaneSetupActivationPhase
	activation := state.ActivateNetworkDataPlaneRequest{}
	journal.readPlan = func(context.Context, domain.OperationID) (state.NetworkDataPlaneSetupPlanRecord, bool, error) {
		calls = append(calls, "plan")
		return state.NetworkDataPlaneSetupPlanRecord{Projection: state.NetworkDataPlaneSetupProjection{ConfirmedOwnership: ownership.Observation{Record: plan.TargetOwnership}}, Activation: &activation}, true, nil
	}
	store := &networkDataPlaneSetupLifecycleStore{activate: func(context.Context, state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error) {
		calls = append(calls, "store")
		return state.NetworkMutationResult{Record: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 301}}, nil
	}}
	journal.verify = func(_ context.Context, request state.CompleteNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
		calls = append(calls, "verify")
		if request.ExpectedOperationRevision != 91 || request.RequesterIdentity != plan.TargetOwnership.OwnerIdentity {
			t.Fatalf("recovery verification request = %#v", request)
		}
		return state.NetworkDataPlaneSetupActivationResult{}, nil
	}
	journal.complete = func(_ context.Context, request state.CompleteNetworkDataPlaneSetupRequest) (state.OperationRecord, error) {
		calls = append(calls, "complete")
		if request.ExpectedOperationRevision != 91 || request.RequesterIdentity != plan.TargetOwnership.OwnerIdentity {
			t.Fatalf("recovery completion request = %#v", request)
		}
		return operation, nil
	}
	coordinator.store = store
	coordinator.runtime = networkDataPlaneSetupLifecycleRuntime{activate: func(context.Context, domain.Sequence) error { calls = append(calls, "runtime"); return nil }}
	coordinator.endpoints = networkDataPlaneSetupLifecycleEndpoints{reconcile: func(context.Context) (state.NetworkRecord, error) {
		calls = append(calls, "endpoints")
		return state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 302}, nil
	}}
	calls = nil
	got, err = coordinator.Recover(t.Context(), plan.Operation.ID)
	if err != nil || got != operation || strings.Join(calls, ",") != "operation,plan,store,verify,runtime,endpoints,runtime,complete" {
		t.Fatalf("Recover(activation) = %#v, %v; calls=%v", got, err, calls)
	}
}

// TestNetworkDataPlaneSetupStartResumesActivationReceipt proves an intent retry
// resumes only the durable post-helper activation receipt.
func TestNetworkDataPlaneSetupStartResumesActivationReceipt(t *testing.T) {
	plan := networkDataPlaneSetupTestLowPortPlan(t)
	operation := state.OperationRecord{Operation: plan.Operation, Revision: 91}
	operation.Operation.State, operation.Operation.Phase = domain.OperationRunning, networkDataPlaneSetupActivationPhase
	activation := state.ActivateNetworkDataPlaneRequest{}
	var calls []string
	journal := &networkDataPlaneSetupLifecycleJournal{}
	journal.byIntent = func(context.Context, domain.IntentID) (state.OperationRecord, error) {
		calls = append(calls, "intent")
		return operation, nil
	}
	journal.readPlan = func(context.Context, domain.OperationID) (state.NetworkDataPlaneSetupPlanRecord, bool, error) {
		calls = append(calls, "plan")
		return state.NetworkDataPlaneSetupPlanRecord{Projection: state.NetworkDataPlaneSetupProjection{ConfirmedOwnership: ownership.Observation{Record: plan.TargetOwnership}}, Activation: &activation}, true, nil
	}
	journal.verify = func(context.Context, state.CompleteNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
		calls = append(calls, "verify")
		return state.NetworkDataPlaneSetupActivationResult{}, nil
	}
	journal.complete = func(context.Context, state.CompleteNetworkDataPlaneSetupRequest) (state.OperationRecord, error) {
		calls = append(calls, "complete")
		return operation, nil
	}
	coordinator := NewNetworkDataPlaneSetupCoordinator(journal, networkDataPlaneSetupLifecycleNetwork{}, networkDataPlaneSetupLifecycleProjection{}, &networkDataPlaneSetupLifecycleStore{activate: func(context.Context, state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error) {
		calls = append(calls, "store")
		return state.NetworkMutationResult{Record: state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 101}}, nil
	}}, networkDataPlaneSetupLifecycleRoots{}, networkDataPlaneSetupTestTrustPlans{}, networkDataPlaneSetupTestLowPlans{}, func() (NetworkDataPlaneSetupTrustIssuer, error) {
		t.Fatal("Start resume opened trust issuer")
		return nil, nil
	}, func() (NetworkDataPlaneSetupLowPortIssuer, error) {
		t.Fatal("Start resume opened low-port issuer")
		return nil, nil
	}, networkDataPlaneSetupLifecycleOwnership{}, networkDataPlaneSetupLifecycleTrust{}, networkDataPlaneSetupLifecycleLowPorts{}, networkDataPlaneSetupLifecycleRuntime{activate: func(context.Context, domain.Sequence) error { calls = append(calls, "runtime"); return nil }}, networkDataPlaneSetupLifecycleEndpoints{reconcile: func(context.Context) (state.NetworkRecord, error) {
		calls = append(calls, "endpoints")
		return state.NetworkRecord{Stage: state.NetworkStageFull, Revision: 102}, nil
	}}, "darwin", networkDataPlaneSetupTestClock{time.Now()})
	got, err := coordinator.Start(t.Context(), NetworkDataPlaneSetupStartRequest{OperationID: "new-operation", IntentID: plan.Operation.IntentID, RequesterIdentity: plan.TargetOwnership.OwnerIdentity})
	if err != nil || got != operation || strings.Join(calls, ",") != "intent,plan,store,verify,runtime,endpoints,runtime,complete" {
		t.Fatalf("Start(resume) = %#v, %v; calls=%v", got, err, calls)
	}
	calls = nil
	_, err = coordinator.Start(t.Context(), NetworkDataPlaneSetupStartRequest{OperationID: "new-operation", IntentID: plan.Operation.IntentID, RequesterIdentity: "wrong-owner"})
	if err == nil || strings.Join(calls, ",") != "intent,plan" {
		t.Fatalf("Start(wrong requester) error=%v; calls=%v", err, calls)
	}
}

// TestNetworkDataPlaneSetupRejectsTypedNilIssuers keeps factory wiring failures
// from becoming panics after an approval has been resolved.
func TestNetworkDataPlaneSetupRejectsTypedNilIssuers(t *testing.T) {
	trustPlan, _ := networkDataPlaneSetupTestTrustPlan(t)
	lowPlan := networkDataPlaneSetupTestLowPortPlan(t)
	var trustIssuer networkDataPlaneSetupTestNilTrustIssuer
	trustCoordinator := &NetworkDataPlaneSetupCoordinator{trustPlans: networkDataPlaneSetupTestTrustPlans{trustPlan}, trustIssuers: func() (NetworkDataPlaneSetupTrustIssuer, error) { return trustIssuer, nil }, clock: networkDataPlaneSetupTestClock{time.Now()}}
	if _, err := trustCoordinator.PrepareTrust(t.Context(), NetworkDataPlaneSetupPrepareTrustRequest{
		OperationID:               trustPlan.Operation.ID,
		ExpectedOperationRevision: trustPlan.OperationRevision,
		RequesterIdentity:         trustPlan.TargetOwnership.OwnerIdentity,
	}); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("PrepareTrust(typed nil) error = %v", err)
	}
	var lowIssuer networkDataPlaneSetupTestNilLowPortIssuer
	lowCoordinator := &NetworkDataPlaneSetupCoordinator{lowPortPlans: networkDataPlaneSetupTestLowPlans{lowPlan}, lowPortIssuers: func() (NetworkDataPlaneSetupLowPortIssuer, error) { return lowIssuer, nil }, clock: networkDataPlaneSetupTestClock{time.Now()}}
	if _, err := lowCoordinator.PrepareLowPorts(t.Context(), NetworkDataPlaneSetupPrepareLowPortsRequest{OperationID: lowPlan.Operation.ID, ExpectedOperationRevision: lowPlan.OperationRevision, RequesterIdentity: lowPlan.TargetOwnership.OwnerIdentity}); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("PrepareLowPorts(typed nil) error = %v", err)
	}
}

// networkDataPlaneSetupTestNilTrustIssuer models a typed-nil map issuer.
type networkDataPlaneSetupTestNilTrustIssuer map[string]string

// Issue panics when the guard fails.
func (networkDataPlaneSetupTestNilTrustIssuer) Issue(context.Context, string, ticketissuer.TrustRequest) (ticketissuer.TrustResult, error) {
	panic("typed nil trust issuer was invoked")
}

// Close panics when the typed-nil guard fails.
func (networkDataPlaneSetupTestNilTrustIssuer) Close() error {
	panic("typed nil trust issuer was closed")
}

// networkDataPlaneSetupTestNilLowPortIssuer models a typed-nil function issuer.
type networkDataPlaneSetupTestNilLowPortIssuer func()

// Issue panics when the guard fails.
func (networkDataPlaneSetupTestNilLowPortIssuer) Issue(context.Context, string, ticketissuer.LowPortRequest) (ticketissuer.LowPortResult, error) {
	panic("typed nil low-port issuer was invoked")
}

// Close panics when the typed-nil guard fails.
func (networkDataPlaneSetupTestNilLowPortIssuer) Close() error {
	panic("typed nil low-port issuer was closed")
}

// networkDataPlaneSetupTestLowPlans returns one immutable low-port plan.
type networkDataPlaneSetupTestLowPlans struct{ plan ticketissuer.LowPortPlan }

// Resolve returns the fixture low-port plan.
func (plans networkDataPlaneSetupTestLowPlans) Resolve(context.Context, ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
	return plans.plan, nil
}

// networkDataPlaneSetupTestLowPortIssuer records one low-port publication.
type networkDataPlaneSetupTestLowPortIssuer struct {
	result    ticketissuer.LowPortResult
	err       error
	requester string
	closed    bool
}

// Issue records the requester and returns the scripted result.
func (issuer *networkDataPlaneSetupTestLowPortIssuer) Issue(_ context.Context, requester string, _ ticketissuer.LowPortRequest) (ticketissuer.LowPortResult, error) {
	issuer.requester = requester
	return issuer.result, issuer.err
}

// Close records the fixture issuer closure.
func (issuer *networkDataPlaneSetupTestLowPortIssuer) Close() error { issuer.closed = true; return nil }

// networkDataPlaneSetupNilTrustIssuer panics if a malformed typed-nil factory value is invoked.
type networkDataPlaneSetupNilTrustIssuer struct{}

// Issue panics if the typed-nil trust guard fails.
func (*networkDataPlaneSetupNilTrustIssuer) Issue(context.Context, string, ticketissuer.TrustRequest) (ticketissuer.TrustResult, error) {
	panic("nil trust issuer called")
}

// Close panics if typed-nil trust issuance reaches cleanup.
func (*networkDataPlaneSetupNilTrustIssuer) Close() error { panic("nil trust issuer closed") }

// networkDataPlaneSetupNilLowPortIssuer panics if a malformed typed-nil factory value is invoked.
type networkDataPlaneSetupNilLowPortIssuer struct{}

// Issue panics if the typed-nil low-port guard fails.
func (*networkDataPlaneSetupNilLowPortIssuer) Issue(context.Context, string, ticketissuer.LowPortRequest) (ticketissuer.LowPortResult, error) {
	panic("nil low-port issuer called")
}

// Close panics if typed-nil low-port issuance reaches cleanup.
func (*networkDataPlaneSetupNilLowPortIssuer) Close() error { panic("nil low-port issuer closed") }

// networkDataPlaneSetupLifecycleFixture retains scripted coordinator inputs and calls.
type networkDataPlaneSetupLifecycleFixture struct {
	coordinator *NetworkDataPlaneSetupCoordinator
	request     NetworkDataPlaneSetupConfirmLowPortsRequest
	calls       []string
}

// newNetworkDataPlaneSetupLifecycleFixture creates a scripted activation lifecycle.
func newNetworkDataPlaneSetupLifecycleFixture(t *testing.T, fail string) *networkDataPlaneSetupLifecycleFixture {
	t.Helper()
	plan := networkDataPlaneSetupTestLowPortPlan(t)
	trustPlan, trustRequest := networkDataPlaneSetupTestTrustPlan(t)
	lowObservation := lowport.Observation{Request: plan.NativeRequest, Complete: true, Artifacts: []lowport.Artifact{{Kind: lowport.ArtifactKindPlist, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("a", 64)}, {Kind: lowport.ArtifactKindService, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("b", 64)}}}
	lowFingerprint, _ := lowObservation.Fingerprint()
	policyFingerprint, _ := plan.Policy.Fingerprint()
	ownershipFingerprint, _ := plan.TargetOwnership.Fingerprint()
	trustObservation := networkDataPlaneSetupTrustObservation(t, trustRequest, true)
	if fail == "preexisting" || fail == "trust-drift" {
		trustObservation = networkDataPlaneSetupTrustObservation(t, trustRequest, false)
	}
	if fail == "trust-drift" {
		trustObservation.Entries[0].NativeExact = false
	}
	trustFingerprint, _ := trustObservation.Fingerprint()
	projection := state.NetworkDataPlaneSetupProjection{Stage: state.NetworkStageResolver, NetworkRevision: 70, NetworkUpdatedAt: time.Now().UTC(), ResolverProof: state.NetworkSetupProof{Component: state.NetworkSetupComponentResolver, Evidence: strings.Repeat("c", 64), Generation: plan.TargetOwnership.Generation, VerifiedAt: time.Now().UTC()}, ConfirmedOwnership: ownership.Observation{Exists: true, Record: plan.TargetOwnership, Fingerprint: ownershipFingerprint}}
	fixture := &networkDataPlaneSetupLifecycleFixture{}
	call := func(name string) { fixture.calls = append(fixture.calls, name) }
	failErr := errors.New("injected " + fail)
	journal := &networkDataPlaneSetupLifecycleJournal{}
	journal.stage = func(_ context.Context, request state.StageNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
		call("stage")
		if fail == "stage" {
			return state.NetworkDataPlaneSetupActivationResult{}, failErr
		}
		return state.NetworkDataPlaneSetupActivationResult{Operation: state.OperationRecord{Operation: plan.Operation, Revision: 91}, Activation: request.Activation}, nil
	}
	journal.verify = func(_ context.Context, request state.CompleteNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
		call("verify")
		if request.ExpectedOperationRevision != 91 || request.RequesterIdentity != plan.TargetOwnership.OwnerIdentity {
			t.Fatalf("activation verification request = %#v", request)
		}
		if fail == "verify" {
			return state.NetworkDataPlaneSetupActivationResult{}, failErr
		}
		return state.NetworkDataPlaneSetupActivationResult{}, nil
	}
	journal.complete = func(_ context.Context, request state.CompleteNetworkDataPlaneSetupRequest) (state.OperationRecord, error) {
		call("complete")
		if fail == "preexisting" {
			finished := projection.NetworkUpdatedAt
			completed := plan.Operation
			completed.State, completed.Phase, completed.FinishedAt = domain.OperationSucceeded, networkDataPlaneSetupCompletedPhase, &finished
			return state.OperationRecord{Operation: completed, Revision: 103}, nil
		}
		if request.ExpectedOperationRevision != 91 || request.RequesterIdentity != plan.TargetOwnership.OwnerIdentity {
			t.Fatalf("terminal completion request = %#v", request)
		}
		if fail == "complete" {
			return state.OperationRecord{}, failErr
		}
		return state.OperationRecord{}, errors.New("unexpected terminal success")
	}
	fullRecord := networkDataPlaneSetupLifecycleFullRecord(t, plan, projection.NetworkUpdatedAt, 102)
	store := &networkDataPlaneSetupLifecycleStore{activate: func(context.Context, state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error) {
		call("store")
		if fail == "store" {
			return state.NetworkMutationResult{}, failErr
		}
		return state.NetworkMutationResult{Record: fullRecord}, nil
	}}
	fixture.coordinator = NewNetworkDataPlaneSetupCoordinator(journal, networkDataPlaneSetupLifecycleNetwork{}, networkDataPlaneSetupLifecycleProjection{resolve: func(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
		call("projection")
		return projection, nil
	}}, store, networkDataPlaneSetupLifecycleRoots{root: trustPlan.Root, call: func() { call("root") }}, networkDataPlaneSetupTestTrustPlans{trustPlan}, networkDataPlaneSetupLifecycleLowPlans{resolve: func(context.Context, ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
		call("low-plan")
		return plan, nil
	}}, func() (NetworkDataPlaneSetupTrustIssuer, error) { return nil, nil }, func() (NetworkDataPlaneSetupLowPortIssuer, error) { return nil, nil }, networkDataPlaneSetupLifecycleOwnership{observe: func(context.Context) (ownership.Observation, error) {
		call("ownership")
		return projection.ConfirmedOwnership, nil
	}}, networkDataPlaneSetupLifecycleTrust{observe: func(context.Context, trust.Request) (trust.Observation, error) {
		call("trust-observe")
		return trustObservation, nil
	}}, networkDataPlaneSetupLifecycleLowPorts{observe: func(context.Context, lowport.Request) (lowport.Observation, error) {
		call("low-observe")
		return lowObservation, nil
	}}, networkDataPlaneSetupLifecycleRuntime{activate: func(context.Context, domain.Sequence) error {
		call("runtime")
		if fail == "runtime" {
			return failErr
		}
		return nil
	}}, networkDataPlaneSetupLifecycleEndpoints{reconcile: func(context.Context) (state.NetworkRecord, error) {
		call("endpoints")
		if fail == "endpoints" {
			return state.NetworkRecord{}, failErr
		}
		return fullRecord, nil
	}}, "darwin", networkDataPlaneSetupTestClock{time.Now().UTC()})
	fixture.request = NetworkDataPlaneSetupConfirmLowPortsRequest{OperationID: plan.Operation.ID, ExpectedOperationRevision: plan.OperationRevision, RequesterIdentity: plan.TargetOwnership.OwnerIdentity, LowPortEvidence: helper.LowPortMutationEvidence{PolicyFingerprint: policyFingerprint, OwnershipFingerprint: ownershipFingerprint, ObservationFingerprint: lowFingerprint, Postcondition: helper.LowPortPostconditionExact}}
	_ = trustFingerprint
	return fixture
}

// networkDataPlaneSetupLifecycleFullRecord constructs the smallest valid terminal aggregate.
func networkDataPlaneSetupLifecycleFullRecord(t *testing.T, plan ticketissuer.LowPortPlan, at time.Time, revision domain.Sequence) state.NetworkRecord {
	t.Helper()
	pool := networkResolverSetupTestPool(t, "127.91.0.8/29")
	return state.NetworkRecord{Stage: state.NetworkStageFull, Revision: revision, CreatedAt: at, UpdatedAt: at, Ownership: identity.Ownership{InstallationID: identity.InstallationID(plan.TargetOwnership.InstallationID), Generation: plan.TargetOwnership.Generation}, Pool: pool, Leases: []identity.Lease{}, Quarantines: []identity.Quarantine{}, Reservations: state.DataPlaneReservations{Listeners: listenersForPolicy(plan.Policy, plan.TargetOwnership.Generation, at), Endpoints: []state.EndpointReservation{}, SuppressedProjectIDs: []domain.ProjectID{}}}
}

// networkDataPlaneSetupLifecycleJournal scripts durable lifecycle boundaries.
type networkDataPlaneSetupLifecycleJournal struct {
	operation func(context.Context, domain.OperationID) (state.OperationRecord, error)
	readPlan  func(context.Context, domain.OperationID) (state.NetworkDataPlaneSetupPlanRecord, bool, error)
	byIntent  func(context.Context, domain.IntentID) (state.OperationRecord, error)
	advance   func(context.Context, state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error)
	stage     func(context.Context, state.StageNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error)
	verify    func(context.Context, state.CompleteNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error)
	complete  func(context.Context, state.CompleteNetworkDataPlaneSetupRequest) (state.OperationRecord, error)
}

// Operation delegates an operation read.
func (journal *networkDataPlaneSetupLifecycleJournal) Operation(ctx context.Context, id domain.OperationID) (state.OperationRecord, error) {
	return journal.operation(ctx, id)
}

// OperationByIntent delegates an intent read.
func (journal *networkDataPlaneSetupLifecycleJournal) OperationByIntent(ctx context.Context, id domain.IntentID) (state.OperationRecord, error) {
	return journal.byIntent(ctx, id)
}

// StageNetworkDataPlaneSetup rejects unexpected setup staging.
func (journal *networkDataPlaneSetupLifecycleJournal) StageNetworkDataPlaneSetup(context.Context, state.StageNetworkDataPlaneSetupRequest) (state.OperationRecord, error) {
	return state.OperationRecord{}, errors.New("unexpected setup stage")
}

// AdvanceNetworkDataPlaneSetupTrust delegates trust advancement.
func (journal *networkDataPlaneSetupLifecycleJournal) AdvanceNetworkDataPlaneSetupTrust(ctx context.Context, request state.AdvanceNetworkDataPlaneSetupTrustRequest) (state.OperationRecord, error) {
	return journal.advance(ctx, request)
}

// StageNetworkDataPlaneActivation delegates activation staging.
func (journal *networkDataPlaneSetupLifecycleJournal) StageNetworkDataPlaneActivation(ctx context.Context, request state.StageNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
	return journal.stage(ctx, request)
}

// CompleteNetworkDataPlaneActivation delegates activation verification.
func (journal *networkDataPlaneSetupLifecycleJournal) CompleteNetworkDataPlaneActivation(ctx context.Context, request state.CompleteNetworkDataPlaneActivationRequest) (state.NetworkDataPlaneSetupActivationResult, error) {
	return journal.verify(ctx, request)
}

// CompleteNetworkDataPlaneSetup delegates terminal completion.
func (journal *networkDataPlaneSetupLifecycleJournal) CompleteNetworkDataPlaneSetup(ctx context.Context, request state.CompleteNetworkDataPlaneSetupRequest) (state.OperationRecord, error) {
	return journal.complete(ctx, request)
}

// ReadNetworkDataPlaneSetupPlan delegates persisted receipt reads.
func (journal *networkDataPlaneSetupLifecycleJournal) ReadNetworkDataPlaneSetupPlan(ctx context.Context, id domain.OperationID) (state.NetworkDataPlaneSetupPlanRecord, bool, error) {
	return journal.readPlan(ctx, id)
}

// networkDataPlaneSetupLifecycleNetwork rejects unexpected network authority reads.
type networkDataPlaneSetupLifecycleNetwork struct{}

// Network rejects unexpected authority reads.
func (networkDataPlaneSetupLifecycleNetwork) Network(context.Context) (state.NetworkRecord, bool, error) {
	return state.NetworkRecord{}, false, errors.New("unexpected network read")
}

// networkDataPlaneSetupLifecycleLowPlans scripts low-port plan resolution.
type networkDataPlaneSetupLifecycleLowPlans struct {
	resolve func(context.Context, ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error)
}

// Resolve delegates low-port plan resolution.
func (plans networkDataPlaneSetupLifecycleLowPlans) Resolve(ctx context.Context, request ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
	return plans.resolve(ctx, request)
}

// networkDataPlaneSetupLifecycleProjection scripts projection resolution.
type networkDataPlaneSetupLifecycleProjection struct {
	resolve func(context.Context, networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error)
}

// Resolve delegates projection resolution.
func (source networkDataPlaneSetupLifecycleProjection) Resolve(ctx context.Context, policy networkpolicy.Policy) (state.NetworkDataPlaneSetupProjection, error) {
	return source.resolve(ctx, policy)
}

// networkDataPlaneSetupLifecycleStore scripts durable activation.
type networkDataPlaneSetupLifecycleStore struct {
	activate func(context.Context, state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error)
}

// ActivateNetworkDataPlane delegates durable activation.
func (store *networkDataPlaneSetupLifecycleStore) ActivateNetworkDataPlane(ctx context.Context, request state.ActivateNetworkDataPlaneRequest) (state.NetworkMutationResult, error) {
	return store.activate(ctx, request)
}

// networkDataPlaneSetupLifecycleRoots scripts public-root reads.
type networkDataPlaneSetupLifecycleRoots struct {
	root certificates.Root
	call func()
}

// PublicRoot returns the scripted certificate authority.
func (roots networkDataPlaneSetupLifecycleRoots) PublicRoot() (certificates.Root, error) {
	roots.call()
	return roots.root, nil
}

// networkDataPlaneSetupLifecycleOwnership scripts ownership observation.
type networkDataPlaneSetupLifecycleOwnership struct {
	observe func(context.Context) (ownership.Observation, error)
}

// Observe delegates ownership observation.
func (observer networkDataPlaneSetupLifecycleOwnership) Observe(ctx context.Context) (ownership.Observation, error) {
	return observer.observe(ctx)
}

// networkDataPlaneSetupLifecycleTrust scripts trust observation.
type networkDataPlaneSetupLifecycleTrust struct {
	observe func(context.Context, trust.Request) (trust.Observation, error)
}

// Observe delegates trust observation.
func (observer networkDataPlaneSetupLifecycleTrust) Observe(ctx context.Context, request trust.Request) (trust.Observation, error) {
	return observer.observe(ctx, request)
}

// networkDataPlaneSetupLifecycleLowPorts scripts low-port observation.
type networkDataPlaneSetupLifecycleLowPorts struct {
	observe func(context.Context, lowport.Request) (lowport.Observation, error)
}

// Observe delegates low-port observation.
func (observer networkDataPlaneSetupLifecycleLowPorts) Observe(ctx context.Context, request lowport.Request) (lowport.Observation, error) {
	return observer.observe(ctx, request)
}

// networkDataPlaneSetupLifecycleRuntime scripts runtime activation.
type networkDataPlaneSetupLifecycleRuntime struct {
	activate func(context.Context, domain.Sequence) error
}

// ActivateNetwork delegates runtime activation.
func (runtime networkDataPlaneSetupLifecycleRuntime) ActivateNetwork(ctx context.Context, revision domain.Sequence) error {
	return runtime.activate(ctx, revision)
}

// networkDataPlaneSetupLifecycleEndpoints scripts endpoint backfill.
type networkDataPlaneSetupLifecycleEndpoints struct {
	reconcile func(context.Context) (state.NetworkRecord, error)
}

// ReconcileFullStageDefaultHTTPEndpoints delegates endpoint backfill.
func (endpoints networkDataPlaneSetupLifecycleEndpoints) ReconcileFullStageDefaultHTTPEndpoints(ctx context.Context) (state.NetworkRecord, error) {
	return endpoints.reconcile(ctx)
}
