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
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/loopback"
)

// scriptedPoolReleasePlanSource returns one configured plan per durable read.
type scriptedPoolReleasePlanSource struct {
	plans  []PoolReleasePlan
	errors []error
	calls  int
}

// Resolve returns the next retained plan.
func (source *scriptedPoolReleasePlanSource) Resolve(context.Context, PoolReleaseRequest) (PoolReleasePlan, error) {
	index := source.calls
	source.calls++
	if index < len(source.errors) && source.errors[index] != nil {
		return PoolReleasePlan{}, source.errors[index]
	}
	if index >= len(source.plans) {
		index = len(source.plans) - 1
	}
	return source.plans[index], nil
}

// TestPoolReleasePlanValidate rejects every release-only admission boundary.
func TestPoolReleasePlanValidate(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	cases := []struct {
		name   string
		mutate func(*PoolReleasePlan)
	}{
		{
			name: "operation kind",
			mutate: func(plan *PoolReleasePlan) {
				plan.Operation.Kind = domain.OperationKindNetworkDataPlaneSetup
			},
		},
		{
			name: "project",
			mutate: func(plan *PoolReleasePlan) {
				plan.Operation.ProjectID = "project"
			},
		},
		{
			name: "state",
			mutate: func(plan *PoolReleasePlan) {
				plan.Operation.State = domain.OperationRequiresApproval
			},
		},
		{
			name: "phase",
			mutate: func(plan *PoolReleasePlan) {
				plan.Operation.Phase = "trust"
			},
		},
		{
			name: "operation revision",
			mutate: func(plan *PoolReleasePlan) {
				plan.OperationRevision = 0
			},
		},
		{
			name: "operation ID",
			mutate: func(plan *PoolReleasePlan) {
				plan.Operation.ID = ""
			},
		},
		{
			name: "checkpoint revision",
			mutate: func(plan *PoolReleasePlan) {
				plan.CheckpointRevision = plan.OperationRevision
			},
		},
		{
			name: "ownership schema",
			mutate: func(plan *PoolReleasePlan) {
				plan.TargetOwnership.SchemaVersion++
			},
		},
		{
			name: "ownership key",
			mutate: func(plan *PoolReleasePlan) {
				plan.TargetOwnership.TicketVerifierKey = "bad"
			},
		},
		{
			name: "pool bounds",
			mutate: func(plan *PoolReleasePlan) {
				plan.Pool = mustIdentityPool(t, "127.77.0.0/30", 2)
			},
		},
		{
			name: "ownership pool",
			mutate: func(plan *PoolReleasePlan) {
				plan.TargetOwnership.LoopbackPoolPrefix = "127.77.0.8/29"
			},
		},
		{
			name: "target count",
			mutate: func(plan *PoolReleasePlan) {
				plan.Targets = plan.Targets[:7]
			},
		},
		{
			name: "target order",
			mutate: func(plan *PoolReleasePlan) {
				plan.Targets[0], plan.Targets[1] = plan.Targets[1], plan.Targets[0]
			},
		},
		{
			name: "target fingerprint",
			mutate: func(plan *PoolReleasePlan) {
				plan.Targets[0].ObservationFingerprint = strings.Repeat("A", 64)
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan, err := clonePoolReleasePlan(fixture.plan)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

// TestPoolReleaseRequestValidate rejects an empty durable selector.
func TestPoolReleaseRequestValidate(t *testing.T) {
	if err := (PoolReleaseRequest{}).Validate(); err == nil {
		t.Fatal("Validate() error = nil")
	}
}

// TestPoolReleaseServiceFencesEveryDurablePlanField proves the second read is a full authority compare-and-swap.
func TestPoolReleaseServiceFencesEveryDurablePlanField(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*PoolReleasePlan)
	}{
		{
			name: "operation",
			mutate: func(plan *PoolReleasePlan) {
				plan.Operation.IntentID = "other-intent"
			},
		},
		{
			name: "operation revision",
			mutate: func(plan *PoolReleasePlan) {
				plan.OperationRevision++
			},
		},
		{
			name: "checkpoint",
			mutate: func(plan *PoolReleasePlan) {
				plan.CheckpointRevision++
			},
		},
		{
			name: "ownership",
			mutate: func(plan *PoolReleasePlan) {
				plan.TargetOwnership.Generation++
			},
		},
		{
			name: "pool candidates",
			mutate: func(plan *PoolReleasePlan) {
				pool := mustIdentityPool(t, "127.78.0.0/29", 8)
				plan.Pool = pool
				plan.TargetOwnership.LoopbackPoolPrefix = pool.Prefix().String()
				for index, address := range pool.Candidates() {
					plan.Targets[index].Address = address
				}
			},
		},
		{
			name: "targets",
			mutate: func(plan *PoolReleasePlan) {
				plan.Targets[0].ObservationFingerprint = strings.Repeat("b", 64)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPoolReleaseFixture(t)
			changed, err := clonePoolReleasePlan(fixture.plan)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&changed)
			fixture.plans.plans[1] = changed
			result, err := fixture.service.Issue(t.Context(), fixture.requester, fixture.request)
			if err == nil || result != (PoolResult{}) || fixture.publisher.calls != 0 {
				t.Fatalf("Issue() result/error/calls = %#v/%v/%d", result, err, fixture.publisher.calls)
			}
		})
	}
}

// TestPoolReleaseServiceRejectsFirstAndSecondPassAuthorityDrift proves both independent observations are mandatory.
func TestPoolReleaseServiceRejectsFirstAndSecondPassAuthorityDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*poolReleaseFixture, int)
	}{
		{
			name: "ownership observe",
			mutate: func(fixture *poolReleaseFixture, index int) {
				fixture.ownership.errors = []error{nil, nil}
				fixture.ownership.errors[index] = errors.New("ownership")
			},
		},
		{
			name: "ownership absent",
			mutate: func(fixture *poolReleaseFixture, index int) {
				fixture.ownership.observations[index].Exists = false
			},
		},
		{
			name: "ownership record",
			mutate: func(fixture *poolReleaseFixture, index int) {
				fixture.ownership.observations[index].Record.Generation++
			},
		},
		{
			name: "ownership fingerprint",
			mutate: func(fixture *poolReleaseFixture, index int) {
				fixture.ownership.observations[index].Fingerprint = strings.Repeat("b", 64)
			},
		},
	} {
		for _, index := range []int{0, 1} {
			t.Run(test.name+" pass", func(t *testing.T) {
				fixture := newPoolReleaseFixture(t)
				test.mutate(fixture, index)
				result, err := fixture.service.Issue(t.Context(), fixture.requester, fixture.request)
				if err == nil || result != (PoolResult{}) || fixture.publisher.calls != 0 {
					t.Fatalf("Issue() = %#v, %v", result, err)
				}
			})
		}
	}
}

// TestPoolReleaseServiceIssueBindsEveryExactRetainedObservation proves a release ticket cannot carry setup pre-assignment authority.
func TestPoolReleaseServiceIssueBindsEveryExactRetainedObservation(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation != helper.OperationReleaseLoopbackPool ||
		fixture.publisher.calls != 1 ||
		len(fixture.loopback.calls) != 16 {
		t.Fatalf("result/calls = %#v/%d/%d", result, fixture.publisher.calls, len(fixture.loopback.calls))
	}
	ticket := fixture.publisher.ticket
	if ticket.Operation != helper.OperationReleaseLoopbackPool ||
		ticket.ExpectedLoopbackPool == nil ||
		len(ticket.ExpectedLoopbackPool.Identities) != 8 {
		t.Fatalf("ticket = %#v", ticket)
	}
	for index, identity := range ticket.ExpectedLoopbackPool.Identities {
		if identity.Address != fixture.plan.Targets[index].Address.String() ||
			identity.ExpectedObservation.State != helper.ObservationOwned ||
			identity.ExpectedObservation.Fingerprint != fixture.plan.Targets[index].ObservationFingerprint ||
			identity.ExpectedPreAssignment != nil {
			t.Fatalf("identity %d = %#v", index, identity)
		}
	}
}

// TestPoolReleaseServiceIssueRejectsObservationChangeBeforePublication proves retained evidence is re-read after ticket construction.
func TestPoolReleaseServiceIssueRejectsObservationChangeBeforePublication(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	fixture.service.loopback = &secondPassPoolLoopbackObserver{
		base: fixture.loopback,
		change: func(address netip.Addr) loopback.Observation {
			return poolLoopbackObservation(address, loopback.StateAbsent)
		},
	}
	_, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if err == nil || fixture.publisher.calls != 0 {
		t.Fatalf("Issue() error/calls = %v/%d", err, fixture.publisher.calls)
	}
}

// secondPassPoolLoopbackObserver preserves the first full scan and changes only the revalidation scan.
type secondPassPoolLoopbackObserver struct {
	base   *poolLoopbackObserver
	calls  int
	change func(netip.Addr) loopback.Observation
}

// Observe returns retained facts for the first eight reads and changed facts thereafter.
func (observer *secondPassPoolLoopbackObserver) Observe(ctx context.Context, address netip.Addr) (loopback.Observation, error) {
	observer.calls++
	if observer.calls > 8 && address == observer.base.calls[0] {
		return observer.change(address), nil
	}
	return observer.base.Observe(ctx, address)
}

// TestPoolReleaseServiceIssueReturnsOnlyValidUncertainResult proves a malformed durability-uncertain reference is never returned.
func TestPoolReleaseServiceIssueReturnsOnlyValidUncertainResult(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	fixture.publisher.err = ticketspool.ErrDurabilityUncertain
	result, err := fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if !errors.Is(err, ErrPoolPublicationIndeterminate) || result.Reference == "" {
		t.Fatalf("Issue() result/error = %#v/%v", result, err)
	}
	fixture = newPoolReleaseFixture(t)
	fixture.publisher.err = ticketspool.ErrDurabilityUncertain
	fixture.publisher.reference = "bad"
	result, err = fixture.service.Issue(t.Context(), fixture.plan.TargetOwnership.OwnerIdentity, fixture.request)
	if !errors.Is(err, ErrPoolPublicationIndeterminate) || result != (PoolResult{}) {
		t.Fatalf("malformed Issue() result/error = %#v/%v", result, err)
	}
}

// TestPoolReleaseServiceIssueRejectsAuthorityFailures proves every failed authority boundary returns no capability.
func TestPoolReleaseServiceIssueRejectsAuthorityFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*poolReleaseFixture)
	}{
		{
			name: "invalid request",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.request.OperationID = ""
			},
		},
		{
			name: "source",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.plans.errors = []error{errors.New("source unavailable")}
			},
		},
		{
			name: "requester",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.requester = "other"
			},
		},
		{
			name: "ownership absent",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.ownership.observations[0].Exists = false
			},
		},
		{
			name: "ownership fingerprint",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.ownership.observations[0].Fingerprint = strings.Repeat("b", 64)
			},
		},
		{
			name: "key",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.keys.err = errors.New("key unavailable")
			},
		},
		{
			name: "loopback error",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.loopback.errors[fixture.plan.Targets[0].Address] = errors.New("observer unavailable")
			},
		},
		{
			name: "loopback absent",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.loopback.observations[fixture.plan.Targets[0].Address] = poolLoopbackObservation(
					fixture.plan.Targets[0].Address,
					loopback.StateAbsent,
				)
			},
		},
		{
			name: "publish",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.publisher.err = errors.New("publish unavailable")
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPoolReleaseFixture(t)
			test.mutate(fixture)
			result, err := fixture.service.Issue(t.Context(), fixture.requester, fixture.request)
			if err == nil || result != (PoolResult{}) {
				t.Fatalf("Issue() result/error = %#v/%v", result, err)
			}
		})
	}
}

// TestPoolReleaseServiceIssueRejectsChangedPlan proves the final durable read fences every retained plan dimension.
func TestPoolReleaseServiceIssueRejectsChangedPlan(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	changed, err := clonePoolReleasePlan(fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	changed.CheckpointRevision++
	fixture.plans.plans[1] = changed
	result, err := fixture.service.Issue(t.Context(), fixture.requester, fixture.request)
	if err == nil || result != (PoolResult{}) || fixture.publisher.calls != 0 {
		t.Fatalf("Issue() result/error/calls = %#v/%v/%d", result, err, fixture.publisher.calls)
	}
}

// TestPoolReleaseServiceIssueRejectsKeyEntropyAndLoopbackAuthorityFailures proves no ordinary failure yields a capability.
func TestPoolReleaseServiceIssueRejectsKeyEntropyAndLoopbackAuthorityFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*poolReleaseFixture)
	}{
		{
			name: "key pin",
			mutate: func(fixture *poolReleaseFixture) {
				for index := range fixture.plans.plans {
					fixture.plans.plans[index].TargetOwnership.TicketVerifierKey = base64.StdEncoding.EncodeToString(
						bytes.Repeat([]byte{1}, ed25519.PublicKeySize),
					)
				}
			},
		},
		{
			name: "entropy",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.service.entropy = strings.NewReader("short")
			},
		},
		{
			name: "address",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.loopback.observations[fixture.plan.Targets[0].Address] = poolLoopbackObservation(
					fixture.plan.Targets[1].Address,
					loopback.StateExact,
				)
			},
		},
		{
			name: "foreign",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.loopback.observations[fixture.plan.Targets[0].Address] = poolLoopbackObservation(
					fixture.plan.Targets[0].Address,
					loopback.StateForeign,
				)
			},
		},
		{
			name: "fingerprint",
			mutate: func(fixture *poolReleaseFixture) {
				fixture.plan.Targets[0].ObservationFingerprint = strings.Repeat("b", 64)
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPoolReleaseFixture(t)
			test.mutate(fixture)
			result, err := fixture.service.Issue(t.Context(), fixture.requester, fixture.request)
			if err == nil || result != (PoolResult{}) || fixture.publisher.calls != 0 {
				t.Fatalf("Issue() = %#v, %v", result, err)
			}
		})
	}
}

// TestPoolReleaseServiceCancellationAndClose proves cancellation gates issuance and Close is idempotent.
func TestPoolReleaseServiceCancellationAndClose(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.service.Issue(ctx, fixture.requester, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := fixture.service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Issue(t.Context(), fixture.requester, fixture.request); err == nil {
		t.Fatal("Issue() error = nil")
	}
}

// TestNewPoolReleaseServiceRejectsEveryNilDependency proves issuer construction fails closed for each authority boundary.
func TestNewPoolReleaseServiceRejectsEveryNilDependency(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	for _, test := range []struct {
		name string
		call func()
	}{
		{
			name: "plans",
			call: func() {
				NewPoolReleaseService(
					nil,
					fixture.ownership,
					fixture.keys,
					fixture.publisher,
					fixture.loopback,
					fixture.service.clock,
					fixture.service.entropy,
				)
			},
		},
		{
			name: "ownership",
			call: func() {
				NewPoolReleaseService(
					fixture.plans,
					nil,
					fixture.keys,
					fixture.publisher,
					fixture.loopback,
					fixture.service.clock,
					fixture.service.entropy,
				)
			},
		},
		{
			name: "keys",
			call: func() {
				NewPoolReleaseService(
					fixture.plans,
					fixture.ownership,
					nil,
					fixture.publisher,
					fixture.loopback,
					fixture.service.clock,
					fixture.service.entropy,
				)
			},
		},
		{
			name: "publisher",
			call: func() {
				NewPoolReleaseService(
					fixture.plans,
					fixture.ownership,
					fixture.keys,
					nil,
					fixture.loopback,
					fixture.service.clock,
					fixture.service.entropy,
				)
			},
		},
		{
			name: "loopback",
			call: func() {
				NewPoolReleaseService(
					fixture.plans,
					fixture.ownership,
					fixture.keys,
					fixture.publisher,
					nil,
					fixture.service.clock,
					fixture.service.entropy,
				)
			},
		},
		{
			name: "clock",
			call: func() {
				NewPoolReleaseService(
					fixture.plans,
					fixture.ownership,
					fixture.keys,
					fixture.publisher,
					fixture.loopback,
					nil,
					fixture.service.entropy,
				)
			},
		},
		{
			name: "entropy",
			call: func() {
				NewPoolReleaseService(
					fixture.plans,
					fixture.ownership,
					fixture.keys,
					fixture.publisher,
					fixture.loopback,
					fixture.service.clock,
					nil,
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewPoolReleaseService() did not panic")
				}
			}()
			test.call()
		})
	}
}

// TestOpenDefaultPoolReleaseServiceOwnsBothStores proves default opening cleans partial state and closes successful stores once.
func TestOpenDefaultPoolReleaseServiceOwnsBothStores(t *testing.T) {
	fixture := newPoolReleaseFixture(t)
	if _, err := OpenDefaultPoolReleaseService(nil, fixture.ownership); err == nil {
		t.Fatal("nil plans error = nil")
	}
	if _, err := OpenDefaultPoolReleaseService(fixture.plans, nil); err == nil {
		t.Fatal("nil ownership error = nil")
	}
	if _, err := openDefaultPoolReleaseService(fixture.plans, fixture.ownership, poolReleaseDefaultOpeners{}); err == nil {
		t.Fatal("incomplete openers error = nil")
	}
	key := &closingKeyLoader{KeyLoader: fixture.keys}
	publisher := &closingPublisher{Publisher: fixture.publisher}
	keyFailureOpeners := poolReleaseDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return nil, errors.New("key")
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return publisher, nil
		},
	}
	if _, err := openDefaultPoolReleaseService(
		fixture.plans,
		fixture.ownership,
		keyFailureOpeners,
	); err == nil {
		t.Fatal("key opener error = nil")
	}
	nilKeyOpeners := poolReleaseDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return nil, nil
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return publisher, nil
		},
	}
	if _, err := openDefaultPoolReleaseService(
		fixture.plans,
		fixture.ownership,
		nilKeyOpeners,
	); err == nil {
		t.Fatal("nil key error = nil")
	}
	publisherFailureOpeners := poolReleaseDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return key, nil
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return nil, errors.New("publisher")
		},
	}
	if _, err := openDefaultPoolReleaseService(
		fixture.plans,
		fixture.ownership,
		publisherFailureOpeners,
	); err == nil || key.closeCalls != 1 {
		t.Fatalf("publisher error/key closes = %v/%d", err, key.closeCalls)
	}
	key = &closingKeyLoader{KeyLoader: fixture.keys}
	nilPublisherOpeners := poolReleaseDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return key, nil
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return nil, nil
		},
	}
	if _, err := openDefaultPoolReleaseService(
		fixture.plans,
		fixture.ownership,
		nilPublisherOpeners,
	); err == nil || key.closeCalls != 1 {
		t.Fatalf("nil publisher/key closes = %v/%d", err, key.closeCalls)
	}
	key = &closingKeyLoader{KeyLoader: fixture.keys}
	publisher = &closingPublisher{Publisher: fixture.publisher}
	successOpeners := poolReleaseDefaultOpeners{
		openKeys: func() (defaultKeyStoreCloser, error) {
			return key, nil
		},
		openPublisher: func() (defaultPublisherCloser, error) {
			return publisher, nil
		},
	}
	service, err := openDefaultPoolReleaseService(
		fixture.plans,
		fixture.ownership,
		successOpeners,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if key.closeCalls != 1 || publisher.closeCalls != 1 {
		t.Fatalf("close calls = %d/%d", key.closeCalls, publisher.closeCalls)
	}
}

// poolReleaseFixture contains a valid plan and all independent issuance boundaries.
type poolReleaseFixture struct {
	plan      PoolReleasePlan
	request   PoolReleaseRequest
	requester string
	plans     *scriptedPoolReleasePlanSource
	ownership *scriptedOwnershipObserver
	keys      *staticKeyLoader
	publisher *capturingPublisher
	loopback  *poolLoopbackObserver
	service   *PoolReleaseService
}

// newPoolReleaseFixture constructs one fully retained global pool-release plan.
func newPoolReleaseFixture(t *testing.T) *poolReleaseFixture {
	t.Helper()
	now := time.Date(2026, time.July, 22, 16, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pool := mustIdentityPool(t, "127.77.0.0/29", 8)
	target := ownership.Record{
		SchemaVersion:            ownership.NetworkPolicySchemaVersion,
		InstallationID:           "harbor-pool-release-test",
		OwnerIdentity:            "501",
		Generation:               7,
		LoopbackPoolPrefix:       pool.Prefix().String(),
		NetworkPolicyFingerprint: strings.Repeat("a", 64),
		TicketVerifierKey:        base64.StdEncoding.EncodeToString(public),
	}
	requestedAt := now.Add(-2 * time.Minute)
	startedAt := now.Add(-time.Minute)
	plan := PoolReleasePlan{
		Operation: domain.Operation{
			ID:          "operation-pool-release",
			IntentID:    "intent-pool-release",
			Kind:        domain.OperationKindNetworkRelease,
			State:       domain.OperationRunning,
			Phase:       "releasing network runtime",
			RequestedAt: requestedAt,
			StartedAt:   &startedAt,
		},
		OperationRevision:  11,
		CheckpointRevision: 12,
		TargetOwnership:    target,
		Pool:               pool,
	}
	loopbackObserver := &poolLoopbackObserver{
		observations: make(map[netip.Addr]loopback.Observation),
		errors:       make(map[netip.Addr]error),
	}
	for _, address := range pool.Candidates() {
		observation := poolLoopbackObservation(address, loopback.StateExact)
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		plan.Targets = append(plan.Targets, PoolReleaseTarget{
			Address:                address,
			ObservationFingerprint: fingerprint,
		})
		loopbackObserver.observations[address] = observation
	}
	ownedFingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	ownershipObserver := &scriptedOwnershipObserver{
		observations: []ownership.Observation{
			{
				Exists:      true,
				Record:      target,
				Fingerprint: ownedFingerprint,
			},
			{
				Exists:      true,
				Record:      target,
				Fingerprint: ownedFingerprint,
			},
		},
	}
	publisher := &capturingPublisher{reference: helper.TicketReference(strings.Repeat("a", 64))}
	plans := &scriptedPoolReleasePlanSource{plans: []PoolReleasePlan{plan, plan}}
	keys := &staticKeyLoader{key: private}
	service := NewPoolReleaseService(
		plans,
		ownershipObserver,
		keys,
		publisher,
		loopbackObserver,
		fixedClock{now: now},
		bytes.NewReader(bytes.Repeat([]byte{0x5a}, ticketNonceBytes)),
	)
	return &poolReleaseFixture{
		plan:      plan,
		request:   PoolReleaseRequest{OperationID: plan.Operation.ID},
		requester: target.OwnerIdentity,
		plans:     plans,
		ownership: ownershipObserver,
		keys:      keys,
		publisher: publisher,
		loopback:  loopbackObserver,
		service:   service,
	}
}
