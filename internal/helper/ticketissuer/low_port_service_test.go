package ticketissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/launcher"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/host/ownership"
	lowport "github.com/goforj/harbor/internal/platform/lowport"
)

// scriptedLowPortPlanSource returns one plan or failure per serialized durable read.
type scriptedLowPortPlanSource struct {
	plans        []LowPortPlan
	errors       []error
	requests     []LowPortRequest
	beforeReturn func(int)
}

// Resolve records the selected operation and returns the next scripted plan.
func (source *scriptedLowPortPlanSource) Resolve(_ context.Context, request LowPortRequest) (LowPortPlan, error) {
	index := len(source.requests)
	source.requests = append(source.requests, request)
	if source.beforeReturn != nil {
		source.beforeReturn(index)
	}
	if index < len(source.errors) && source.errors[index] != nil {
		return LowPortPlan{}, source.errors[index]
	}
	if len(source.plans) == 0 {
		return LowPortPlan{}, errors.New("low-port plan script is empty")
	}
	if index >= len(source.plans) {
		index = len(source.plans) - 1
	}
	return source.plans[index], nil
}

// scriptedLowPortObserver returns one native observation or failure per serialized read.
type scriptedLowPortObserver struct {
	observations []lowport.Observation
	errors       []error
	requests     []lowport.Request
	beforeReturn func(int)
}

// Observe records immutable native authority and returns the next scripted observation.
func (observer *scriptedLowPortObserver) Observe(_ context.Context, request lowport.Request) (lowport.Observation, error) {
	index := len(observer.requests)
	observer.requests = append(observer.requests, request)
	if observer.beforeReturn != nil {
		observer.beforeReturn(index)
	}
	if index < len(observer.errors) && observer.errors[index] != nil {
		return lowport.Observation{}, observer.errors[index]
	}
	if len(observer.observations) == 0 {
		return lowport.Observation{}, errors.New("low-port observation script is empty")
	}
	if index >= len(observer.observations) {
		index = len(observer.observations) - 1
	}
	return observer.observations[index], nil
}

// lowPortIssuerFixture contains one valid global approval and every replaceable authority boundary.
type lowPortIssuerFixture struct {
	now         time.Time
	request     LowPortRequest
	plan        LowPortPlan
	private     ed25519.PrivateKey
	owned       ownership.Observation
	observation lowport.Observation
	plans       *scriptedLowPortPlanSource
	ownership   *scriptedOwnershipObserver
	keys        *staticKeyLoader
	publisher   *capturingPublisher
	observer    *scriptedLowPortObserver
	service     *LowPortService
}

// TestLowPortServiceIssueBindsEveryAuthority proves the result and signed ticket contain only approved low-port authority.
func TestLowPortServiceIssueBindsEveryAuthority(t *testing.T) {
	for _, mutation := range []helper.Operation{helper.OperationEnsureLowPorts, helper.OperationReleaseLowPorts} {
		states := []lowport.State{lowport.StateAbsent, lowport.StateExact}
		if mutation == helper.OperationEnsureLowPorts {
			states = append(states, lowport.StateOwnedDrifted)
		}
		for _, state := range states {
			t.Run(string(mutation)+"/"+string(state), func(t *testing.T) {
				fixture := newLowPortIssuerFixture(t, mutation)
				fixture.observation = lowPortObservationForState(t, fixture.plan.NativeRequest, state)
				fixture.observer.observations = []lowport.Observation{fixture.observation, reverseLowPortObservation(fixture.observation)}

				result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
				if err != nil {
					t.Fatalf("Issue() error = %v", err)
				}
				if err := result.Validate(fixture.now); err != nil {
					t.Fatalf("LowPortResult.Validate() error = %v", err)
				}
				ownershipFingerprint, err := fixture.plan.TargetOwnership.Fingerprint()
				if err != nil {
					t.Fatal(err)
				}
				observationFingerprint, err := fixture.observation.Fingerprint()
				if err != nil {
					t.Fatal(err)
				}
				if result.OperationID != fixture.plan.Operation.ID || result.Reference != fixture.publisher.reference ||
					result.Operation != mutation || result.PolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint ||
					result.OwnershipFingerprint != ownershipFingerprint || result.ObservationFingerprint != observationFingerprint ||
					result.ExpiresAt != fixture.now.Add(ticketLifetime) {
					t.Fatalf("Issue() result = %#v", result)
				}
				if _, err := launcher.NewLowPortLaunchTicket(
					result.OperationID,
					result.Reference,
					result.Operation,
					result.PolicyFingerprint,
					result.OwnershipFingerprint,
					result.ObservationFingerprint,
					result.ExpiresAt,
				); err != nil {
					t.Fatalf("launcher.NewLowPortLaunchTicket() error = %v", err)
				}
				if len(fixture.plans.requests) != 2 || fixture.ownership.calls != 2 || fixture.keys.calls != 1 ||
					len(fixture.observer.requests) != 2 || fixture.publisher.calls != 1 {
					t.Fatalf("calls = plans %d ownership %d keys %d native %d publish %d", len(fixture.plans.requests), fixture.ownership.calls, fixture.keys.calls, len(fixture.observer.requests), fixture.publisher.calls)
				}
				for index, request := range fixture.observer.requests {
					if request != fixture.plan.NativeRequest {
						t.Fatalf("native request %d = %#v", index, request)
					}
				}

				ticket := fixture.publisher.ticket
				if ticket.Operation != mutation || ticket.InstallationID != fixture.plan.TargetOwnership.InstallationID ||
					ticket.RequesterIdentity != fixture.plan.TargetOwnership.OwnerIdentity ||
					ticket.OwnershipGeneration != fixture.plan.TargetOwnership.Generation ||
					ticket.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion ||
					ticket.NetworkPolicyFingerprint != fixture.plan.TargetOwnership.NetworkPolicyFingerprint ||
					ticket.ApprovedPool != fixture.plan.TargetOwnership.LoopbackPoolPrefix ||
					ticket.NetworkPolicy == nil || *ticket.NetworkPolicy != fixture.plan.Policy ||
					ticket.ExpectedLowPortObservation == nil || ticket.ExpectedLowPortObservation.Fingerprint != observationFingerprint {
					t.Fatalf("published low-port ticket = %#v", ticket)
				}
				if ticket.ApprovedAddress != "" || ticket.ExpectedObservation != (helper.ExpectedObservation{}) ||
					ticket.ExpectedPreAssignment != nil || ticket.ExpectedLoopbackPool != nil ||
					ticket.ExpectedResolverObservation != nil || ticket.TrustRoot != nil || ticket.ExpectedTrustObservation != nil {
					t.Fatalf("published ticket contains mixed authority: %#v", ticket)
				}
				if ticket.Nonce != strings.Repeat("5a", ticketNonceBytes) || ticket.ExpiresAt != fixture.now.Add(ticketLifetime) ||
					!bytes.Equal(fixture.publisher.key, fixture.private) {
					t.Fatalf("ticket correlation = nonce %q expiry %s key match %t", ticket.Nonce, ticket.ExpiresAt, bytes.Equal(fixture.publisher.key, fixture.private))
				}
			})
		}
	}
}

// TestLowPortServiceRejectsReleaseOfOwnedDriftedState matches the production adapter's release contract.
func TestLowPortServiceRejectsReleaseOfOwnedDriftedState(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationReleaseLowPorts)
	fixture.observer.observations = []lowport.Observation{
		lowPortObservationForState(t, fixture.plan.NativeRequest, lowport.StateOwnedDrifted),
	}

	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err == nil || !strings.Contains(err.Error(), "owned-drifted native state") || result != (LowPortResult{}) || fixture.publisher.calls != 0 {
		t.Fatalf("Issue() = (%#v, %v), publish %d", result, err, fixture.publisher.calls)
	}
}

// TestLowPortPlanValidateRejectsEveryAuthorityDimension covers the complete public plan contract.
func TestLowPortPlanValidateRejectsEveryAuthorityDimension(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	tests := []struct {
		name   string
		want   string
		mutate func(*LowPortPlan)
	}{
		{name: "operation", want: "operation", mutate: func(plan *LowPortPlan) { plan.Operation.ID = "" }},
		{name: "kind", want: "operation kind", mutate: func(plan *LowPortPlan) { plan.Operation.Kind = domain.OperationKindNetworkResolverSetup }},
		{name: "project scope", want: "must not identify a project", mutate: func(plan *LowPortPlan) { plan.Operation.ProjectID = "project-low-port" }},
		{name: "state", want: "operation state", mutate: func(plan *LowPortPlan) { plan.Operation.State = domain.OperationRunning }},
		{name: "revision zero", want: "operation revision", mutate: func(plan *LowPortPlan) { plan.OperationRevision = 0 }},
		{name: "revision overflow", want: "operation revision", mutate: func(plan *LowPortPlan) { plan.OperationRevision = domain.MaximumSequence + 1 }},
		{name: "mutation", want: "not allowlisted", mutate: func(plan *LowPortPlan) { plan.Mutation = helper.OperationEnsureResolver }},
		{name: "ownership", want: "target ownership", mutate: func(plan *LowPortPlan) { plan.TargetOwnership.InstallationID = "" }},
		{name: "ownership schema", want: "schema is 1", mutate: func(plan *LowPortPlan) {
			plan.TargetOwnership.SchemaVersion = ownership.IdentitySchemaVersion
			plan.TargetOwnership.NetworkPolicyFingerprint = ""
		}},
		{name: "policy", want: "approval policy", mutate: func(plan *LowPortPlan) { plan.Policy.Suffix = ".invalid" }},
		{name: "policy ownership", want: "does not match target ownership", mutate: func(plan *LowPortPlan) {
			plan.Policy = alternateLowPortPolicy(t, plan.Policy)
		}},
		{name: "native request", want: "native request", mutate: func(plan *LowPortPlan) { plan.NativeRequest = lowport.Request{} }},
		{name: "native mismatch", want: "does not match policy-bound ownership", mutate: func(plan *LowPortPlan) {
			other := plan.TargetOwnership
			other.InstallationID = "installation-low-port-other"
			request, err := lowport.NewRequest(other, plan.Policy)
			if err != nil {
				t.Fatal(err)
			}
			plan.NativeRequest = request
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := cloneLowPortPlan(fixture.plan)
			test.mutate(&plan)
			if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LowPortPlan.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
	if err := fixture.plan.Validate(); err != nil {
		t.Fatalf("valid LowPortPlan.Validate() error = %v", err)
	}
}

// TestLowPortResultValidateRejectsEveryField covers launcher metadata validation independently from issuance.
func TestLowPortResultValidateRejectsEveryField(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		want   string
		mutate func(*LowPortResult)
	}{
		{name: "operation ID", want: "operation", mutate: func(value *LowPortResult) { value.OperationID = "" }},
		{name: "reference", want: "reference", mutate: func(value *LowPortResult) { value.Reference = "bad" }},
		{name: "operation", want: "unsupported", mutate: func(value *LowPortResult) { value.Operation = helper.OperationEnsureResolver }},
		{name: "policy", want: "policy fingerprint", mutate: func(value *LowPortResult) { value.PolicyFingerprint = "bad" }},
		{name: "ownership", want: "ownership fingerprint", mutate: func(value *LowPortResult) { value.OwnershipFingerprint = strings.Repeat("A", 64) }},
		{name: "observation", want: "observation fingerprint", mutate: func(value *LowPortResult) { value.ObservationFingerprint = "bad" }},
		{name: "expiry zero", want: "expiry", mutate: func(value *LowPortResult) { value.ExpiresAt = time.Time{} }},
		{name: "expiry past", want: "expiry", mutate: func(value *LowPortResult) { value.ExpiresAt = fixture.now }},
		{name: "expiry non-UTC", want: "expiry", mutate: func(value *LowPortResult) { value.ExpiresAt = value.ExpiresAt.In(time.FixedZone("zero", 0)) }},
		{name: "expiry excessive", want: "protocol bound", mutate: func(value *LowPortResult) {
			value.ExpiresAt = fixture.now.Add(helper.MaxTicketLifetime + time.Nanosecond)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := result
			test.mutate(&candidate)
			if err := candidate.Validate(fixture.now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LowPortResult.Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
	for _, operation := range []helper.Operation{helper.OperationEnsureLowPorts, helper.OperationReleaseLowPorts} {
		candidate := result
		candidate.Operation = operation
		if err := candidate.Validate(fixture.now); err != nil {
			t.Fatalf("LowPortResult.Validate(%q) error = %v", operation, err)
		}
	}
}

// TestSameLowPortPlanPinsEveryField verifies revalidation cannot omit any durable authority dimension.
func TestSameLowPortPlanPinsEveryField(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	if !sameLowPortPlan(fixture.plan, cloneLowPortPlan(fixture.plan)) {
		t.Fatal("equivalent plans differ")
	}
	mutations := []func(*LowPortPlan){
		func(plan *LowPortPlan) { plan.Operation.ID = "operation-other" },
		func(plan *LowPortPlan) { plan.Operation.IntentID = "intent-other" },
		func(plan *LowPortPlan) { plan.Operation.Kind = domain.OperationKindNetworkResolverSetup },
		func(plan *LowPortPlan) { plan.Operation.ProjectID = "project-other" },
		func(plan *LowPortPlan) { plan.Operation.State = domain.OperationRunning },
		func(plan *LowPortPlan) { plan.Operation.Phase = "other phase" },
		func(plan *LowPortPlan) { plan.Operation.RequestedAt = plan.Operation.RequestedAt.Add(time.Second) },
		func(plan *LowPortPlan) {
			changed := plan.Operation.StartedAt.Add(time.Second)
			plan.Operation.StartedAt = &changed
		},
		func(plan *LowPortPlan) { changed := fixture.now; plan.Operation.FinishedAt = &changed },
		func(plan *LowPortPlan) {
			plan.Operation.Problem = &domain.Problem{Code: "other", Message: "other", Retryable: true}
		},
		func(plan *LowPortPlan) { plan.OperationRevision++ },
		func(plan *LowPortPlan) { plan.Mutation = helper.OperationReleaseLowPorts },
		func(plan *LowPortPlan) { plan.TargetOwnership.Generation++ },
		func(plan *LowPortPlan) { plan.Policy.AuthorityFingerprint = strings.Repeat("b", 64) },
		func(plan *LowPortPlan) { plan.NativeRequest = lowport.Request{} },
	}
	for index, mutate := range mutations {
		candidate := cloneLowPortPlan(fixture.plan)
		mutate(&candidate)
		if sameLowPortPlan(fixture.plan, candidate) {
			t.Fatalf("plan mutation %d was ignored", index)
		}
	}

	finished := fixture.now
	problem := &domain.Problem{Code: "problem", Message: "problem", Retryable: true}
	operation := fixture.plan.Operation
	operation.FinishedAt = &finished
	operation.Problem = problem
	cloned := cloneLowPortOperation(operation)
	*operation.StartedAt = operation.StartedAt.Add(time.Hour)
	*operation.FinishedAt = operation.FinishedAt.Add(time.Hour)
	operation.Problem.Message = "changed"
	if cloned.StartedAt.Equal(*operation.StartedAt) || cloned.FinishedAt.Equal(*operation.FinishedAt) || cloned.Problem.Message == operation.Problem.Message {
		t.Fatal("cloneLowPortOperation retained source-owned pointer storage")
	}
}

// TestLowPortServiceRejectsInvalidSelectionAndOwnership prevents caller input from becoming host authority.
func TestLowPortServiceRejectsInvalidSelectionAndOwnership(t *testing.T) {
	planFailure := errors.New("plan failed")
	tests := []struct {
		name      string
		want      string
		requester string
		mutate    func(*lowPortIssuerFixture)
	}{
		{name: "plan source", want: planFailure.Error(), mutate: func(fixture *lowPortIssuerFixture) { fixture.plans.errors = []error{planFailure} }},
		{name: "invalid plan", want: "invalid approval plan", mutate: func(fixture *lowPortIssuerFixture) { fixture.plans.plans[0].OperationRevision = 0 }},
		{name: "wrong selected operation", want: "does not match requested operation", mutate: func(fixture *lowPortIssuerFixture) { fixture.request.OperationID = "operation-low-port-other" }},
		{name: "ownership observer", want: "ownership failed", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.ownership.errors = []error{errors.New("ownership failed")}
		}},
		{name: "ownership absent", want: "projection is absent", mutate: func(fixture *lowPortIssuerFixture) { fixture.ownership.observations = []ownership.Observation{{}} }},
		{name: "ownership invalid", want: "invalid ownership projection", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.ownership.observations = []ownership.Observation{{Exists: true}}
		}},
		{name: "ownership differs", want: "differs from the approved target", mutate: func(fixture *lowPortIssuerFixture) {
			other := fixture.plan.TargetOwnership
			other.Generation++
			fingerprint, err := other.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			fixture.ownership.observations = []ownership.Observation{{Exists: true, Record: other, Fingerprint: fingerprint}}
		}},
		{name: "requester", want: "authenticated requester", requester: "502"},
		{name: "ownership fingerprint", want: "fingerprint does not match", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.ownership.observations = []ownership.Observation{{Exists: true, Record: fixture.plan.TargetOwnership, Fingerprint: strings.Repeat("f", 64)}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
			requester := fixture.plan.TargetOwnership.OwnerIdentity
			if test.requester != "" {
				requester = test.requester
			}
			if test.mutate != nil {
				test.mutate(fixture)
			}
			result, err := fixture.service.Issue(t.Context(), requester, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) || result != (LowPortResult{}) {
				t.Fatalf("Issue() = (%#v, %v), want zero result/error containing %q", result, err, test.want)
			}
			if fixture.publisher.calls != 0 {
				t.Fatalf("publisher calls = %d", fixture.publisher.calls)
			}
		})
	}
}

// TestLowPortServiceRejectsUnsafeOrUnstableNativeState covers every fail-closed observation conclusion.
func TestLowPortServiceRejectsUnsafeOrUnstableNativeState(t *testing.T) {
	nativeFailure := errors.New("native failed")
	tests := []struct {
		name   string
		want   string
		mutate func(*lowPortIssuerFixture)
	}{
		{name: "observer", want: nativeFailure.Error(), mutate: func(fixture *lowPortIssuerFixture) { fixture.observer.errors = []error{nativeFailure} }},
		{name: "wrong request", want: "belongs to another request", mutate: func(fixture *lowPortIssuerFixture) {
			other := fixture.plan.TargetOwnership
			other.InstallationID = "installation-low-port-other"
			request, err := lowport.NewRequest(other, fixture.plan.Policy)
			if err != nil {
				t.Fatal(err)
			}
			fixture.observer.observations = []lowport.Observation{lowPortObservationForState(t, request, lowport.StateAbsent)}
		}},
		{name: "malformed", want: "invalid native observation", mutate: func(fixture *lowPortIssuerFixture) {
			observation := fixture.observation
			observation.Artifacts[0].Fingerprint = "bad"
			fixture.observer.observations = []lowport.Observation{observation}
		}},
		{name: "foreign", want: "foreign native state", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.observer.observations = []lowport.Observation{lowPortObservationForState(t, fixture.plan.NativeRequest, lowport.StateForeign)}
		}},
		{name: "ambiguous", want: "ambiguous native state", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.observer.observations = []lowport.Observation{lowPortObservationForState(t, fixture.plan.NativeRequest, lowport.StateAmbiguous)}
		}},
		{name: "indeterminate", want: "indeterminate native state", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.observer.observations = []lowport.Observation{lowPortObservationForState(t, fixture.plan.NativeRequest, lowport.StateIndeterminate)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
			test.mutate(fixture)
			result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) || result != (LowPortResult{}) {
				t.Fatalf("Issue() = (%#v, %v), want zero result/error containing %q", result, err, test.want)
			}
			if fixture.publisher.calls != 0 {
				t.Fatalf("publisher calls = %d", fixture.publisher.calls)
			}
		})
	}
}

// TestLowPortServiceRequiresPinnedKeyAndFreshEntropy rejects replacement signing authority and incomplete tickets.
func TestLowPortServiceRequiresPinnedKeyAndFreshEntropy(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*lowPortIssuerFixture)
	}{
		{name: "key load", want: "load failed", mutate: func(fixture *lowPortIssuerFixture) { fixture.keys.err = errors.New("load failed") }},
		{name: "key shape", want: "signing key is invalid", mutate: func(fixture *lowPortIssuerFixture) { fixture.keys.key = ed25519.PrivateKey{1} }},
		{name: "key mismatch", want: "does not match machine ownership", mutate: func(fixture *lowPortIssuerFixture) {
			_, other, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			fixture.keys.key = other
		}},
		{name: "entropy", want: "entropy failed", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.service.entropy = errorReader{err: errors.New("entropy failed")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
			test.mutate(fixture)
			result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) || result != (LowPortResult{}) || fixture.publisher.calls != 0 {
				t.Fatalf("Issue() = (%#v, %v), publish %d; want %q", result, err, fixture.publisher.calls, test.want)
			}
		})
	}
}

// TestLowPortServiceRevalidatesCompleteAuthority prevents publication across any durable or native drift.
func TestLowPortServiceRevalidatesCompleteAuthority(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*lowPortIssuerFixture)
	}{
		{name: "plan read", want: "second plan failed", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.plans.errors = []error{nil, errors.New("second plan failed")}
		}},
		{name: "plan changed", want: "approval plan changed", mutate: func(fixture *lowPortIssuerFixture) {
			changed := cloneLowPortPlan(fixture.plan)
			changed.Operation.Phase = "different approval phase"
			fixture.plans.plans = []LowPortPlan{fixture.plan, changed}
		}},
		{name: "ownership read", want: "second ownership failed", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.ownership.errors = []error{nil, errors.New("second ownership failed")}
		}},
		{name: "ownership changed", want: "revalidate ownership", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.ownership.observations = []ownership.Observation{fixture.owned, {Exists: true, Record: fixture.plan.TargetOwnership, Fingerprint: strings.Repeat("f", 64)}}
		}},
		{name: "native read", want: "second native failed", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.observer.errors = []error{nil, errors.New("second native failed")}
		}},
		{name: "native changed", want: "native observation changed", mutate: func(fixture *lowPortIssuerFixture) {
			fixture.observer.observations = []lowport.Observation{
				lowPortObservationForState(t, fixture.plan.NativeRequest, lowport.StateAbsent),
				lowPortObservationForState(t, fixture.plan.NativeRequest, lowport.StateExact),
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
			test.mutate(fixture)
			result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) || result != (LowPortResult{}) || fixture.publisher.calls != 0 {
				t.Fatalf("Issue() = (%#v, %v), publish %d; want %q", result, err, fixture.publisher.calls, test.want)
			}
		})
	}
}

// TestLowPortServicePublicationOutcomes preserve the only reconcilable reference after uncertain durability.
func TestLowPortServicePublicationOutcomes(t *testing.T) {
	ordinary := errors.New("publication rejected")
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	fixture.publisher.err = ordinary
	if result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); !errors.Is(err, ordinary) || errors.Is(err, ErrLowPortPublicationIndeterminate) || result != (LowPortResult{}) {
		t.Fatalf("ordinary publication = (%#v, %v)", result, err)
	}

	fixture = newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	durabilityCause := errors.New("directory sync failed")
	fixture.publisher.err = errors.Join(ticketspool.ErrDurabilityUncertain, durabilityCause)
	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if !errors.Is(err, ErrLowPortPublicationIndeterminate) || !errors.Is(err, ticketspool.ErrDurabilityUncertain) || !errors.Is(err, durabilityCause) || result.Reference != fixture.publisher.reference {
		t.Fatalf("uncertain publication = (%#v, %v)", result, err)
	}
	if validateErr := result.Validate(fixture.now); validateErr != nil {
		t.Fatalf("uncertain result Validate() error = %v", validateErr)
	}

	for _, uncertain := range []bool{false, true} {
		fixture = newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
		fixture.publisher.reference = "bad"
		if uncertain {
			fixture.publisher.err = ticketspool.ErrDurabilityUncertain
		}
		result, err = fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
		if !errors.Is(err, ErrLowPortPublicationIndeterminate) || result.Reference != "bad" || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("invalid published result uncertain=%t = (%#v, %v)", uncertain, result, err)
		}
	}
}

// TestLowPortServiceRevalidatesTicketLifetimeAtPublication prevents slow authority checks from publishing an expired capability.
func TestLowPortServiceRevalidatesTicketLifetimeAtPublication(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	clock := &advancingLowPortClock{now: fixture.now}
	fixture.service.clock = clock
	fixture.observer.beforeReturn = func(index int) {
		if index == 1 {
			clock.Set(fixture.now.Add(ticketLifetime))
		}
	}

	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err == nil || !strings.Contains(err.Error(), "expired before publication") || result != (LowPortResult{}) || fixture.publisher.calls != 0 {
		t.Fatalf("Issue() = (%#v, %v), publish %d", result, err, fixture.publisher.calls)
	}
}

// TestLowPortServiceBindsPublicationDeadlineAndReportsExpiryAfterPublication preserves the only reconcilable reference after an expired publication.
func TestLowPortServiceBindsPublicationDeadlineAndReportsExpiryAfterPublication(t *testing.T) {
	for _, durabilityIndeterminate := range []bool{false, true} {
		t.Run(fmt.Sprintf("durability-indeterminate=%t", durabilityIndeterminate), func(t *testing.T) {
			fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
			clock := &advancingLowPortClock{now: fixture.now}
			publisher := &deadlineLowPortPublisher{
				reference: fixture.publisher.reference,
				afterPublish: func() {
					clock.Set(fixture.now.Add(ticketLifetime))
				},
			}
			if durabilityIndeterminate {
				publisher.err = ticketspool.ErrDurabilityUncertain
			}
			fixture.service.clock = clock
			fixture.service.publisher = publisher

			result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if !errors.Is(err, ErrLowPortPublicationIndeterminate) || result.Reference != publisher.reference || publisher.calls != 1 {
				t.Fatalf("Issue() = (%#v, %v), publish %d", result, err, publisher.calls)
			}
			if durabilityIndeterminate && !errors.Is(err, ticketspool.ErrDurabilityUncertain) {
				t.Fatalf("Issue() error = %v, want durability uncertainty", err)
			}
			if !publisher.deadline.Equal(result.ExpiresAt) {
				t.Fatalf("publication deadline = %s, expiry = %s", publisher.deadline, result.ExpiresAt)
			}
			if validateErr := result.Validate(clock.Now()); validateErr == nil {
				t.Fatal("published expired result Validate() error = nil")
			}
		})
	}
}

// TestLowPortServiceCancellationAndClosure keep abandoned callers outside durable publication.
func TestLowPortServiceCancellationAndClosure(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := fixture.service.Issue(canceled, fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); !errors.Is(err, context.Canceled) || result != (LowPortResult{}) {
		t.Fatalf("Issue(canceled) = (%#v, %v)", result, err)
	}
	if result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, LowPortRequest{}); err == nil || result != (LowPortResult{}) {
		t.Fatalf("Issue(invalid) = (%#v, %v)", result, err)
	}
	if len(fixture.plans.requests) != 0 {
		t.Fatalf("preflight failures resolved %d plans", len(fixture.plans.requests))
	}

	closeFailure := errors.New("close failed")
	closeCalls := 0
	fixture.service.closeStore = func() error { closeCalls++; return closeFailure }
	if err := fixture.service.Close(); !errors.Is(err, closeFailure) || closeCalls != 1 {
		t.Fatalf("Close() error/count = %v/%d", err, closeCalls)
	}
	if err := fixture.service.Close(); err != nil || closeCalls != 1 {
		t.Fatalf("second Close() error/count = %v/%d", err, closeCalls)
	}
	if result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request); err == nil || !strings.Contains(err.Error(), "closed") || result != (LowPortResult{}) {
		t.Fatalf("Issue(closed) = (%#v, %v)", result, err)
	}

	fresh := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	if result, err := fresh.service.Issue(nil, fresh.plan.TargetOwnership.OwnerIdentity, fresh.request); err != nil || result.OperationID != fresh.plan.Operation.ID {
		t.Fatalf("Issue(nil context) = (%#v, %v)", result, err)
	}
}

// TestLowPortServiceRechecksCancellationAfterTicketConstruction prevents a serialized abandoned capability from publishing.
func TestLowPortServiceRechecksCancellationAfterTicketConstruction(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	ctx, cancel := context.WithCancel(t.Context())
	fixture.service.entropy = &cancelingLowPortReader{cancel: cancel}
	result, err := fixture.service.Issue(ctx, fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if !errors.Is(err, context.Canceled) || result != (LowPortResult{}) || fixture.publisher.calls != 0 || len(fixture.plans.requests) != 1 {
		t.Fatalf("Issue() = (%#v, %v), plans %d publish %d", result, err, len(fixture.plans.requests), fixture.publisher.calls)
	}
}

// TestLowPortServiceRechecksCancellationAfterNativeRevalidation prevents publication after a late abandoned observation.
func TestLowPortServiceRechecksCancellationAfterNativeRevalidation(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	ctx, cancel := context.WithCancel(t.Context())
	fixture.observer.beforeReturn = func(index int) {
		if index == 1 {
			cancel()
		}
	}
	result, err := fixture.service.Issue(ctx, fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if !errors.Is(err, context.Canceled) || result != (LowPortResult{}) || fixture.publisher.calls != 0 || len(fixture.observer.requests) != 2 {
		t.Fatalf("Issue() = (%#v, %v), observations %d publish %d", result, err, len(fixture.observer.requests), fixture.publisher.calls)
	}
}

// TestLowPortServiceRechecksCancellationAfterSerializationWait avoids authority reads for an abandoned queued issue.
func TestLowPortServiceRechecksCancellationAfterSerializationWait(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	blocking := &blockingLowPortPublisher{reference: fixture.publisher.reference, entered: make(chan struct{}), release: make(chan struct{})}
	fixture.service.publisher = blocking
	first := make(chan error, 1)
	go func() {
		_, err := fixture.service.Issue(context.Background(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
		first <- err
	}()
	waitLowPortSignal(t, blocking.entered, "publisher entry")

	queuedBase, cancel := context.WithCancel(context.Background())
	queuedContext := &signaledLowPortContext{Context: queuedBase, checked: make(chan struct{})}
	queued := make(chan error, 1)
	go func() {
		_, err := fixture.service.Issue(queuedContext, fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
		queued <- err
	}()
	waitLowPortSignal(t, queuedContext.checked, "queued pre-lock check")
	cancel()
	close(blocking.release)
	if err := waitLowPortError(t, first, "first issuance"); err != nil {
		t.Fatalf("first Issue() error = %v", err)
	}
	if err := waitLowPortError(t, queued, "queued issuance"); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Issue() error = %v", err)
	}
	if len(fixture.plans.requests) != 2 || blocking.calls.Load() != 1 {
		t.Fatalf("authority calls = plans %d publish %d", len(fixture.plans.requests), blocking.calls.Load())
	}
}

// TestLowPortServiceDefensivelyCopiesPlanObservationAndKey proves source-owned mutable storage cannot alter publication.
func TestLowPortServiceDefensivelyCopiesPlanObservationAndKey(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	stablePlan := cloneLowPortPlan(fixture.plan)
	fixture.plans.plans = []LowPortPlan{fixture.plan, stablePlan}
	fixture.plans.beforeReturn = func(index int) {
		if index == 1 {
			*fixture.plans.plans[0].Operation.StartedAt = fixture.now.Add(10 * time.Minute)
		}
	}
	stableObservation := cloneLowPortObservation(fixture.observation)
	fixture.observer.observations = []lowport.Observation{fixture.observation, stableObservation}
	fixture.observer.beforeReturn = func(index int) {
		if index == 1 {
			fixture.observer.observations[0].Artifacts[0].Fingerprint = strings.Repeat("e", 64)
		}
	}
	borrowed := append(ed25519.PrivateKey(nil), fixture.private...)
	fixture.service.keys = &borrowedLowPortKey{key: borrowed}
	fixture.observer.beforeReturn = func(index int) {
		if index == 1 {
			fixture.observer.observations[0].Artifacts[0].Fingerprint = strings.Repeat("e", 64)
			for keyIndex := range borrowed {
				borrowed[keyIndex] = 0
			}
		}
	}

	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if result.OperationID != fixture.plan.Operation.ID || !bytes.Equal(fixture.publisher.key, fixture.private) {
		t.Fatalf("result/key = %#v / copied %t", result, bytes.Equal(fixture.publisher.key, fixture.private))
	}
}

// TestLowPortDefaultServiceOpeningOwnsPartialCleanup validates fixed-store acquisition and reverse release.
func TestLowPortDefaultServiceOpeningOwnsPartialCleanup(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	keyCloseFailure := errors.New("key close failed")
	publisherCloseFailure := errors.New("publisher close failed")
	var closeLog []string
	keyStore := &lowPortClosingKeyStore{KeyLoader: fixture.keys, closeErr: keyCloseFailure, log: &closeLog}
	publisherStore := &lowPortClosingPublisher{Publisher: fixture.publisher, closeErr: publisherCloseFailure, log: &closeLog}
	service, err := openDefaultLowPortService(fixture.plans, fixture.ownership, fixture.observer, lowPortDefaultOpeners{
		openKeys:      func() (defaultKeyStoreCloser, error) { return keyStore, nil },
		openPublisher: func() (defaultPublisherCloser, error) { return publisherStore, nil },
	})
	if err != nil || service == nil {
		t.Fatalf("openDefaultLowPortService() = (%#v, %v)", service, err)
	}
	if err := service.Close(); !errors.Is(err, keyCloseFailure) || !errors.Is(err, publisherCloseFailure) {
		t.Fatalf("Close() error = %v", err)
	}
	if strings.Join(closeLog, ",") != "publisher,key" {
		t.Fatalf("close order = %v", closeLog)
	}
	if err := service.Close(); err != nil || keyStore.closeCalls != 1 || publisherStore.closeCalls != 1 {
		t.Fatalf("second Close() = %v, calls %d/%d", err, publisherStore.closeCalls, keyStore.closeCalls)
	}

	openFailure := errors.New("open failed")
	if opened, err := openDefaultLowPortService(fixture.plans, fixture.ownership, fixture.observer, lowPortDefaultOpeners{openKeys: func() (defaultKeyStoreCloser, error) { return nil, openFailure }, openPublisher: func() (defaultPublisherCloser, error) { return publisherStore, nil }}); opened != nil || !errors.Is(err, openFailure) {
		t.Fatalf("key open failure = (%#v, %v)", opened, err)
	}
	if opened, err := openDefaultLowPortService(fixture.plans, fixture.ownership, fixture.observer, lowPortDefaultOpeners{openKeys: func() (defaultKeyStoreCloser, error) { return nil, nil }, openPublisher: func() (defaultPublisherCloser, error) { return publisherStore, nil }}); opened != nil || err == nil || !strings.Contains(err.Error(), "opener returned nil") {
		t.Fatalf("nil key opener = (%#v, %v)", opened, err)
	}

	partialKey := &lowPortClosingKeyStore{KeyLoader: fixture.keys, closeErr: keyCloseFailure}
	if opened, err := openDefaultLowPortService(fixture.plans, fixture.ownership, fixture.observer, lowPortDefaultOpeners{openKeys: func() (defaultKeyStoreCloser, error) { return partialKey, nil }, openPublisher: func() (defaultPublisherCloser, error) { return nil, openFailure }}); opened != nil || !errors.Is(err, openFailure) || !errors.Is(err, keyCloseFailure) || partialKey.closeCalls != 1 {
		t.Fatalf("publisher open failure = (%#v, %v), key closes %d", opened, err, partialKey.closeCalls)
	}
	partialKey = &lowPortClosingKeyStore{KeyLoader: fixture.keys, closeErr: keyCloseFailure}
	if opened, err := openDefaultLowPortService(fixture.plans, fixture.ownership, fixture.observer, lowPortDefaultOpeners{openKeys: func() (defaultKeyStoreCloser, error) { return partialKey, nil }, openPublisher: func() (defaultPublisherCloser, error) { return nil, nil }}); opened != nil || err == nil || !errors.Is(err, keyCloseFailure) || partialKey.closeCalls != 1 {
		t.Fatalf("nil publisher opener = (%#v, %v), key closes %d", opened, err, partialKey.closeCalls)
	}
}

// TestLowPortServiceConstructorsFailFast covers every missing explicit authority and default opener.
func TestLowPortServiceConstructorsFailFast(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	defaultCases := []struct {
		name      string
		plans     LowPortPlanSource
		ownership OwnershipObserver
		observer  LowPortObserver
		want      string
	}{
		{name: "plans", ownership: fixture.ownership, observer: fixture.observer, want: "durable plan source"},
		{name: "ownership", plans: fixture.plans, observer: fixture.observer, want: "ownership observer"},
		{name: "observer", plans: fixture.plans, ownership: fixture.ownership, want: "low-port observer"},
	}
	for _, test := range defaultCases {
		t.Run("default "+test.name, func(t *testing.T) {
			service, err := OpenDefaultLowPortService(test.plans, test.ownership, test.observer)
			if service != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenDefaultLowPortService() = (%#v, %v)", service, err)
			}
		})
	}
	if service, err := openDefaultLowPortService(fixture.plans, fixture.ownership, fixture.observer, lowPortDefaultOpeners{}); service != nil || err == nil || !strings.Contains(err.Error(), "openers are incomplete") {
		t.Fatalf("incomplete openers = (%#v, %v)", service, err)
	}

	constructors := []func(){
		func() {
			NewLowPortService(nil, fixture.ownership, fixture.keys, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewLowPortService(fixture.plans, nil, fixture.keys, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewLowPortService(fixture.plans, fixture.ownership, nil, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewLowPortService(fixture.plans, fixture.ownership, fixture.keys, nil, fixture.observer, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewLowPortService(fixture.plans, fixture.ownership, fixture.keys, fixture.publisher, nil, fixedClock{now: fixture.now}, bytes.NewReader(nil))
		},
		func() {
			NewLowPortService(fixture.plans, fixture.ownership, fixture.keys, fixture.publisher, fixture.observer, nil, bytes.NewReader(nil))
		},
		func() {
			NewLowPortService(fixture.plans, fixture.ownership, fixture.keys, fixture.publisher, fixture.observer, fixedClock{now: fixture.now}, nil)
		},
	}
	for index, constructor := range constructors {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewLowPortService() did not panic")
				}
			}()
			constructor()
		})
	}
}

// TestLowPortServiceCloseWaitsForIssueAndConcurrentCloseRunsOnce proves serialized lifecycle ownership.
func TestLowPortServiceCloseWaitsForIssueAndConcurrentCloseRunsOnce(t *testing.T) {
	t.Run("in-flight issue", func(t *testing.T) {
		fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
		publisher := &blockingLowPortPublisher{reference: fixture.publisher.reference, entered: make(chan struct{}), release: make(chan struct{})}
		fixture.service.publisher = publisher
		storeClosed := make(chan struct{})
		fixture.service.closeStore = func() error { close(storeClosed); return nil }
		issueResult := make(chan error, 1)
		go func() {
			_, err := fixture.service.Issue(context.Background(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			issueResult <- err
		}()
		waitLowPortSignal(t, publisher.entered, "publisher entry")
		closeResult := make(chan error, 1)
		go func() { closeResult <- fixture.service.Close() }()
		select {
		case <-storeClosed:
			t.Fatal("Close() crossed an in-flight issuance")
		case <-time.After(25 * time.Millisecond):
		}
		close(publisher.release)
		if err := waitLowPortError(t, issueResult, "issue"); err != nil {
			t.Fatal(err)
		}
		if err := waitLowPortError(t, closeResult, "close"); err != nil {
			t.Fatal(err)
		}
		waitLowPortSignal(t, storeClosed, "store close")
	})

	t.Run("concurrent close", func(t *testing.T) {
		fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
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
			go func() { <-start; results <- fixture.service.Close() }()
		}
		close(start)
		waitLowPortSignal(t, entered, "first close")
		close(release)
		for range callers {
			if err := waitLowPortError(t, results, "concurrent close"); err != nil {
				t.Fatal(err)
			}
		}
		if closeCalls.Load() != 1 {
			t.Fatalf("closeStore calls = %d", closeCalls.Load())
		}
	})
}

// TestLowPortServiceConcurrentIssueAndCloseStress exercises the serialized boundary under race-detector pressure.
func TestLowPortServiceConcurrentIssueAndCloseStress(t *testing.T) {
	fixture := newLowPortIssuerFixture(t, helper.OperationEnsureLowPorts)
	fixture.service.entropy = repeatingLowPortReader{}
	var closeCalls atomic.Int64
	fixture.service.closeStore = func() error { closeCalls.Add(1); return nil }
	const issueCalls = 64
	const closeCallsCount = 16
	start := make(chan struct{})
	errorsChannel := make(chan error, issueCalls+closeCallsCount)
	var group sync.WaitGroup
	for range issueCalls {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := fixture.service.Issue(context.Background(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
			if err != nil && !strings.Contains(err.Error(), "closed") {
				errorsChannel <- err
			}
		}()
	}
	for range closeCallsCount {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if err := fixture.service.Close(); err != nil {
				errorsChannel <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent operation error = %v", err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("closeStore calls = %d", closeCalls.Load())
	}
}

// newLowPortIssuerFixture constructs one valid schema-two macOS approval with a stable absent native state.
func newLowPortIssuerFixture(t *testing.T, mutation helper.Operation) *lowPortIssuerFixture {
	t.Helper()
	now := time.Date(2026, time.July, 22, 15, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := networkpolicy.New(
		strings.Repeat("a", 64),
		networkpolicy.MacOSMechanisms(),
		networkpolicy.Listener{Advertised: netip.MustParseAddrPort("127.0.0.1:21000"), Bind: netip.MustParseAddrPort("127.0.0.1:21000")},
		networkpolicy.Listener{Advertised: netip.MustParseAddrPort("127.0.0.1:80"), Bind: netip.MustParseAddrPort("127.0.0.1:21001")},
		networkpolicy.Listener{Advertised: netip.MustParseAddrPort("127.0.0.1:443"), Bind: netip.MustParseAddrPort("127.0.0.1:21002")},
	)
	if err != nil {
		t.Fatal(err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	target := ownership.Record{
		SchemaVersion:            ownership.NetworkPolicySchemaVersion,
		InstallationID:           "installation-low-port-test",
		OwnerIdentity:            "501",
		Generation:               7,
		LoopbackPoolPrefix:       "127.77.0.0/29",
		NetworkPolicyFingerprint: policyFingerprint,
		TicketVerifierKey:        base64.StdEncoding.EncodeToString(public),
	}
	nativeRequest, err := lowport.NewRequest(target, policy)
	if err != nil {
		t.Fatal(err)
	}
	requestedAt := now.Add(-2 * time.Minute)
	startedAt := now.Add(-time.Minute)
	operation := domain.Operation{
		ID:          "operation-low-port-setup",
		IntentID:    "intent-low-port-setup",
		Kind:        domain.OperationKindNetworkDataPlaneSetup,
		State:       domain.OperationRequiresApproval,
		Phase:       "awaiting low-port approval",
		RequestedAt: requestedAt,
		StartedAt:   &startedAt,
	}
	plan := LowPortPlan{Operation: operation, OperationRevision: 11, Mutation: mutation, TargetOwnership: target, Policy: policy, NativeRequest: nativeRequest}
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid plan error = %v", err)
	}
	ownershipFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	owned := ownership.Observation{Exists: true, Record: target, Fingerprint: ownershipFingerprint}
	observation := lowPortObservationForState(t, nativeRequest, lowport.StateAbsent)
	plans := &scriptedLowPortPlanSource{plans: []LowPortPlan{plan, plan}}
	ownershipObserver := &scriptedOwnershipObserver{observations: []ownership.Observation{owned, owned}}
	keys := &staticKeyLoader{key: private}
	publisher := &capturingPublisher{reference: helper.TicketReference(strings.Repeat("a", 64))}
	observer := &scriptedLowPortObserver{observations: []lowport.Observation{observation, observation}}
	service := NewLowPortService(plans, ownershipObserver, keys, publisher, observer, fixedClock{now: now}, bytes.NewReader(bytes.Repeat([]byte{0x5a}, ticketNonceBytes*8)))
	return &lowPortIssuerFixture{
		now: now, request: LowPortRequest{OperationID: operation.ID}, plan: cloneLowPortPlan(plan), private: private,
		owned: owned, observation: cloneLowPortObservation(observation), plans: plans, ownership: ownershipObserver,
		keys: keys, publisher: publisher, observer: observer, service: service,
	}
}

// alternateLowPortPolicy creates a distinct valid policy for mismatch tests.
func alternateLowPortPolicy(t *testing.T, policy networkpolicy.Policy) networkpolicy.Policy {
	t.Helper()
	other, err := networkpolicy.New(strings.Repeat("b", 64), policy.Mechanisms, policy.DNS, policy.HTTP, policy.HTTPS)
	if err != nil {
		t.Fatal(err)
	}
	return other
}

// lowPortObservationForState builds one validated bounded native classification fixture.
func lowPortObservationForState(t *testing.T, request lowport.Request, state lowport.State) lowport.Observation {
	t.Helper()
	plistAbsent := lowport.Artifact{Kind: lowport.ArtifactKindPlist, Fingerprint: strings.Repeat("1", 64)}
	serviceAbsent := lowport.Artifact{Kind: lowport.ArtifactKindService, Fingerprint: strings.Repeat("2", 64)}
	plistOwned := lowport.Artifact{Kind: lowport.ArtifactKindPlist, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("3", 64)}
	serviceOwned := lowport.Artifact{Kind: lowport.ArtifactKindService, Present: true, Owned: true, Exact: true, Fingerprint: strings.Repeat("4", 64)}
	observation := lowport.Observation{Request: request, Complete: true, Artifacts: []lowport.Artifact{plistAbsent, serviceAbsent}}
	switch state {
	case lowport.StateAbsent:
	case lowport.StateExact:
		observation.Artifacts = []lowport.Artifact{plistOwned, serviceOwned}
	case lowport.StateOwnedDrifted:
		serviceOwned.Exact = false
		observation.Artifacts = []lowport.Artifact{plistOwned, serviceOwned}
	case lowport.StateForeign:
		plistOwned.Owned = false
		plistOwned.Exact = false
		observation.Artifacts = []lowport.Artifact{plistOwned, serviceAbsent}
	case lowport.StateAmbiguous:
		plistOwned.Owned = false
		plistOwned.Exact = false
		plistOwned.Ambiguous = true
		observation.Artifacts = []lowport.Artifact{plistOwned, serviceAbsent}
	case lowport.StateIndeterminate:
		observation.Complete = false
	default:
		t.Fatalf("unsupported state %q", state)
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("observation for %q error = %v", state, err)
	}
	classified, err := observation.State()
	if err != nil {
		t.Fatalf("classify observation for %q error = %v", state, err)
	}
	if classified != state {
		t.Fatalf("observation state = %q, want %q", classified, state)
	}
	return observation
}

// reverseLowPortObservation reorders complete native facts without changing their canonical meaning.
func reverseLowPortObservation(observation lowport.Observation) lowport.Observation {
	reversed := cloneLowPortObservation(observation)
	for left, right := 0, len(reversed.Artifacts)-1; left < right; left, right = left+1, right-1 {
		reversed.Artifacts[left], reversed.Artifacts[right] = reversed.Artifacts[right], reversed.Artifacts[left]
	}
	return reversed
}

// cancelingLowPortReader cancels its caller after filling one ticket nonce.
type cancelingLowPortReader struct{ cancel context.CancelFunc }

// Read fills deterministic entropy and cancels after construction has consumed it.
func (reader *cancelingLowPortReader) Read(value []byte) (int, error) {
	for index := range value {
		value[index] = 0x5a
	}
	reader.cancel()
	return len(value), nil
}

// repeatingLowPortReader supplies deterministic unbounded entropy under the service serialization lock.
type repeatingLowPortReader struct{}

// Read fills every requested byte.
func (repeatingLowPortReader) Read(value []byte) (int, error) {
	for index := range value {
		value[index] = 0x5a
	}
	return len(value), nil
}

// advancingLowPortClock exposes a deterministic instant that test collaborators may advance.
type advancingLowPortClock struct {
	mutex sync.Mutex
	now   time.Time
}

// Now returns the current test instant.
func (clock *advancingLowPortClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

// Set advances the current test instant at one authority-boundary checkpoint.
func (clock *advancingLowPortClock) Set(now time.Time) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = now
}

// deadlineLowPortPublisher records the context deadline passed to durable publication.
type deadlineLowPortPublisher struct {
	reference    helper.TicketReference
	err          error
	afterPublish func()
	deadline     time.Time
	calls        int
}

// Publish records the expiry-bound publication context before returning its scripted outcome.
func (publisher *deadlineLowPortPublisher) Publish(ctx context.Context, _ helper.Ticket, _ ed25519.PrivateKey) (helper.TicketReference, error) {
	publisher.calls++
	publisher.deadline, _ = ctx.Deadline()
	if publisher.afterPublish != nil {
		publisher.afterPublish()
	}
	return publisher.reference, publisher.err
}

// borrowedLowPortKey returns storage still owned by the test to prove the service copies it.
type borrowedLowPortKey struct{ key ed25519.PrivateKey }

// Load returns the exact borrowed key storage.
func (loader *borrowedLowPortKey) Load(context.Context) (ed25519.PrivateKey, error) {
	return loader.key, nil
}

// blockingLowPortPublisher holds publication until lifecycle tests release it.
type blockingLowPortPublisher struct {
	reference helper.TicketReference
	entered   chan struct{}
	release   chan struct{}
	calls     atomic.Int64
}

// Publish records entry and waits for explicit release.
func (publisher *blockingLowPortPublisher) Publish(context.Context, helper.Ticket, ed25519.PrivateKey) (helper.TicketReference, error) {
	if publisher.calls.Add(1) == 1 {
		close(publisher.entered)
	}
	<-publisher.release
	return publisher.reference, nil
}

// signaledLowPortContext reports the pre-lock cancellation check once.
type signaledLowPortContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

// Err reports the check before delegating to the wrapped context.
func (ctx *signaledLowPortContext) Err() error {
	ctx.once.Do(func() { close(ctx.checked) })
	return ctx.Context.Err()
}

// lowPortClosingKeyStore records close ordering and failure propagation.
type lowPortClosingKeyStore struct {
	KeyLoader
	closeErr   error
	log        *[]string
	closeCalls int
}

// Close records one key-store release.
func (store *lowPortClosingKeyStore) Close() error {
	store.closeCalls++
	if store.log != nil {
		*store.log = append(*store.log, "key")
	}
	return store.closeErr
}

// lowPortClosingPublisher records close ordering and failure propagation.
type lowPortClosingPublisher struct {
	Publisher
	closeErr   error
	log        *[]string
	closeCalls int
}

// Close records one publisher release.
func (publisher *lowPortClosingPublisher) Close() error {
	publisher.closeCalls++
	if publisher.log != nil {
		*publisher.log = append(*publisher.log, "publisher")
	}
	return publisher.closeErr
}

// waitLowPortSignal waits for one bounded concurrency checkpoint.
func waitLowPortSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// waitLowPortError waits for one bounded goroutine result.
func waitLowPortError(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}
