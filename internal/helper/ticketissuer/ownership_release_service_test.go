package ticketissuer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/host/ownership"
)

// TestOwnershipReleaseServiceIssuesFromDurableProjection proves terminal release does not require protected-host ownership to remain present.
func TestOwnershipReleaseServiceIssuesFromDurableProjection(t *testing.T) {
	fixture := newOwnershipReleaseFixture(t)
	result, err := fixture.service.Issue(t.Context(), fixture.target.OwnerIdentity, fixture.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if result.Operation != helper.OperationReleaseNetworkOwnership ||
		result.OperationRevision != fixture.plan.OperationRevision ||
		result.CheckpointRevision != fixture.plan.CheckpointRevision ||
		result.OwnershipFingerprint != fixture.plan.ExpectedOwnershipFingerprint ||
		fixture.publisher.ticket.Operation != helper.OperationReleaseNetworkOwnership ||
		fixture.publisher.ticket.ReleaseOperationID != string(fixture.plan.Operation.ID) ||
		fixture.publisher.ticket.ReleaseOperationRevision != uint64(fixture.plan.OperationRevision) ||
		fixture.publisher.ticket.ReleaseCheckpointRevision != uint64(fixture.plan.CheckpointRevision) ||
		fixture.publisher.ticket.ExpectedOwnershipFingerprint != fixture.plan.ExpectedOwnershipFingerprint {
		t.Fatalf("Issue() result/ticket = %#v / %#v", result, fixture.publisher.ticket)
	}
}

// TestOwnershipReleaseServiceRejectsPublicationRaces proves the second durable or projection read fences publication.
func TestOwnershipReleaseServiceRejectsPublicationRaces(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ownershipReleaseFixture)
	}{
		{
			name: "plan",
			mutate: func(fixture *ownershipReleaseFixture) {
				changed := fixture.plan
				changed.CheckpointRevision++
				fixture.plans.plans = []OwnershipReleasePlan{
					fixture.plan,
					changed,
				}
			},
		},
		{
			name: "projection",
			mutate: func(fixture *ownershipReleaseFixture) {
				fixture.ownership.observations[1].Fingerprint = strings.Repeat("f", 64)
			},
		},
		{
			name: "requester",
			mutate: func(fixture *ownershipReleaseFixture) {
				fixture.requester = "502"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newOwnershipReleaseFixture(t)
			test.mutate(fixture)
			if _, err := fixture.service.Issue(t.Context(), fixture.requester, fixture.request); err == nil {
				t.Fatal("Issue() error = nil")
			}
			if fixture.publisher.calls != 0 {
				t.Fatalf("Publish() calls = %d, want 0", fixture.publisher.calls)
			}
		})
	}
}

// TestOwnershipReleaseServiceReturnsDurableReferenceOnPublicationAmbiguity proves retries retain the only possible publication reference.
func TestOwnershipReleaseServiceReturnsDurableReferenceOnPublicationAmbiguity(t *testing.T) {
	fixture := newOwnershipReleaseFixture(t)
	fixture.publisher.err = ticketspool.ErrDurabilityUncertain
	result, err := fixture.service.Issue(t.Context(), fixture.target.OwnerIdentity, fixture.request)
	if !errors.Is(err, ErrOwnershipReleasePublicationIndeterminate) || result.Reference != fixture.publisher.reference {
		t.Fatalf("Issue() result/error = %#v / %v", result, err)
	}
}

// ownershipReleaseFixture contains one complete terminal release authority and its independent issuer boundaries.
type ownershipReleaseFixture struct {
	plan      OwnershipReleasePlan
	target    ownership.Record
	request   OwnershipReleaseRequest
	requester string
	plans     *scriptedOwnershipReleasePlanSource
	ownership *scriptedOwnershipObserver
	publisher *capturingPublisher
	service   *OwnershipReleaseService
}

// newOwnershipReleaseFixture constructs one retained terminal ownership-release plan.
func newOwnershipReleaseFixture(t *testing.T) *ownershipReleaseFixture {
	t.Helper()
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	target := ownership.Record{
		SchemaVersion:            ownership.NetworkPolicySchemaVersion,
		InstallationID:           "harbor-ownership-release-test",
		OwnerIdentity:            "501",
		Generation:               7,
		LoopbackPoolPrefix:       "127.77.0.0/29",
		NetworkPolicyFingerprint: strings.Repeat("a", 64),
		TicketVerifierKey:        base64.StdEncoding.EncodeToString(public),
	}
	fingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	started := now.Add(-time.Minute)
	plan := OwnershipReleasePlan{
		Operation: domain.Operation{
			ID:          "operation-ownership-release",
			IntentID:    "intent-ownership-release",
			Kind:        domain.OperationKindNetworkRelease,
			State:       domain.OperationRunning,
			Phase:       "releasing network runtime",
			RequestedAt: now.Add(-2 * time.Minute),
			StartedAt:   &started,
		},
		OperationRevision:            11,
		CheckpointRevision:           12,
		Mutation:                     helper.OperationReleaseNetworkOwnership,
		TargetOwnership:              target,
		ExpectedOwnershipFingerprint: fingerprint,
	}
	plans := &scriptedOwnershipReleasePlanSource{
		plans: []OwnershipReleasePlan{
			plan,
			plan,
		},
	}
	observer := &scriptedOwnershipObserver{
		observations: []ownership.Observation{
			{
				Exists:      true,
				Record:      target,
				Fingerprint: fingerprint,
			},
			{
				Exists:      true,
				Record:      target,
				Fingerprint: fingerprint,
			},
		},
	}
	publisher := &capturingPublisher{
		reference: helper.TicketReference(strings.Repeat("a", 64)),
	}
	return &ownershipReleaseFixture{
		plan:   plan,
		target: target,
		request: OwnershipReleaseRequest{
			OperationID: plan.Operation.ID,
		},
		requester: target.OwnerIdentity,
		plans:     plans,
		ownership: observer,
		publisher: publisher,
		service: NewOwnershipReleaseService(
			plans,
			observer,
			&staticKeyLoader{
				key: private,
			},
			publisher,
			fixedClock{
				now: now,
			},
			bytes.NewReader(bytes.Repeat([]byte{0x5a}, ticketNonceBytes)),
		),
	}
}

// scriptedOwnershipReleasePlanSource returns each configured plan in order.
type scriptedOwnershipReleasePlanSource struct {
	plans []OwnershipReleasePlan
	calls int
}

// Resolve returns the next configured durable authority.
func (source *scriptedOwnershipReleasePlanSource) Resolve(_ context.Context, request OwnershipReleaseRequest) (OwnershipReleasePlan, error) {
	if len(source.plans) == 0 {
		return OwnershipReleasePlan{}, errors.New("unexpected resolve")
	}
	index := source.calls
	source.calls++
	if index >= len(source.plans) {
		index = len(source.plans) - 1
	}
	plan := source.plans[index]
	if request.OperationID != plan.Operation.ID {
		return OwnershipReleasePlan{}, errors.New("wrong operation")
	}
	return plan, nil
}

// _ confirms the fixture retains the issuer's narrow durable-reader contract.
var _ OwnershipReleasePlanSource = (*scriptedOwnershipReleasePlanSource)(nil)
