package authority

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/state"
)

// recordingNetworkResolverPolicyMigrationCoordinator captures only authority-layer migration requests.
type recordingNetworkResolverPolicyMigrationCoordinator struct {
	startResult     state.OperationRecord
	startErr        error
	prepareResult   ticketissuer.ResolverResult
	prepareErr      error
	confirmErr      error
	startRequests   []reconcile.NetworkResolverPolicyMigrationStartRequest
	prepareRequests []reconcile.NetworkResolverPolicyMigrationPrepareRequest
	confirmRequests []reconcile.NetworkResolverPolicyMigrationConfirmRequest
}

// Start records the daemon-generated identity and authenticated requester.
func (coordinator *recordingNetworkResolverPolicyMigrationCoordinator) Start(_ context.Context, request reconcile.NetworkResolverPolicyMigrationStartRequest) (state.OperationRecord, error) {
	coordinator.startRequests = append(coordinator.startRequests, request)
	return coordinator.startResult, coordinator.startErr
}

// Prepare records the authenticated requester and exact approval selection.
func (coordinator *recordingNetworkResolverPolicyMigrationCoordinator) Prepare(_ context.Context, request reconcile.NetworkResolverPolicyMigrationPrepareRequest) (ticketissuer.ResolverResult, error) {
	coordinator.prepareRequests = append(coordinator.prepareRequests, request)
	return coordinator.prepareResult, coordinator.prepareErr
}

// Confirm records the requester selected for one exact retirement confirmation.
func (coordinator *recordingNetworkResolverPolicyMigrationCoordinator) Confirm(_ context.Context, request reconcile.NetworkResolverPolicyMigrationConfirmRequest) (state.CompleteNetworkResolverPolicyMigrationResult, error) {
	coordinator.confirmRequests = append(coordinator.confirmRequests, request)
	return state.CompleteNetworkResolverPolicyMigrationResult{}, coordinator.confirmErr
}

// TestNetworkResolverPolicyMigrationAuthorityBindsConfirmationToCaller verifies a different transport user cannot substitute the approved owner.
func TestNetworkResolverPolicyMigrationAuthorityBindsConfirmationToCaller(t *testing.T) {
	rejected := errors.New("authenticated requester does not match the approved machine owner")
	coordinator := &recordingNetworkResolverPolicyMigrationCoordinator{confirmErr: rejected}
	authority := newNetworkResolverPolicyMigrationAuthorityForTest(coordinator, time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC))
	request := control.ConfirmNetworkResolverPolicyMigrationApprovalRequest{
		OperationID:               "operation-policy-migration",
		ExpectedOperationRevision: 3,
		ResolverEvidence: helper.ResolverMutationEvidence{
			PolicyFingerprint:      strings.Repeat("a", 64),
			OwnershipFingerprint:   strings.Repeat("b", 64),
			ObservationFingerprint: strings.Repeat("c", 64),
			Postcondition:          helper.ResolverPostconditionOwnedAbsent,
		},
	}
	_, err := authority.ConfirmNetworkResolverPolicyMigrationApproval(t.Context(), control.Caller{Transport: local.PeerIdentity{UserID: "different-user"}}, request)
	if !errors.Is(err, rejected) {
		t.Fatalf("ConfirmNetworkResolverPolicyMigrationApproval() error = %v, want %v", err, rejected)
	}
	want := []reconcile.NetworkResolverPolicyMigrationConfirmRequest{{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         "different-user",
		ResolverEvidence:          request.ResolverEvidence,
	}}
	if !reflect.DeepEqual(coordinator.confirmRequests, want) {
		t.Fatalf("Confirm requests = %#v, want %#v", coordinator.confirmRequests, want)
	}
}

// newNetworkResolverPolicyMigrationAuthorityForTest builds a migration authority with deterministic daemon identity.
func newNetworkResolverPolicyMigrationAuthorityForTest(coordinator networkResolverPolicyMigrationCoordinator, now time.Time) *NetworkResolverPolicyMigrationAuthority {
	return newNetworkResolverPolicyMigrationAuthority(
		coordinator,
		func() time.Time { return now },
		func() (domain.OperationID, error) { return "operation-policy-migration", nil },
	)
}

// authorityNetworkResolverPolicyMigrationOperation constructs the sole valid migration approval checkpoint.
func authorityNetworkResolverPolicyMigrationOperation(t *testing.T, at time.Time) state.OperationRecord {
	t.Helper()
	queued, err := domain.NewOperation("operation-policy-migration", "intent-policy-migration", domain.OperationKindNetworkResolverPolicyMigration, "", at)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	running, err := queued.Transition(domain.OperationRunning, "preparing resolver policy migration", at, nil)
	if err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	approval, err := running.Transition(domain.OperationRequiresApproval, "awaiting resolver policy migration approval", at, nil)
	if err != nil {
		t.Fatalf("Transition(requires approval) error = %v", err)
	}
	return state.OperationRecord{
		Operation: approval,
		Revision:  3,
	}
}

// TestAuthorityNetworkResolverPolicyMigrationBindsIdentityAndPreservesIndeterminatePublication verifies the retirement boundary owns identity and retry classification.
func TestAuthorityNetworkResolverPolicyMigrationBindsIdentityAndPreservesIndeterminatePublication(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	coordinator := &recordingNetworkResolverPolicyMigrationCoordinator{
		startResult: authorityNetworkResolverPolicyMigrationOperation(t, now),
		prepareResult: ticketissuer.ResolverResult{
			OperationID:          "operation-policy-migration",
			Reference:            helper.TicketReference(strings.Repeat("a", 64)),
			Operation:            helper.OperationRetireResolver,
			PolicyFingerprint:    strings.Repeat("b", 64),
			OwnershipFingerprint: strings.Repeat("c", 64),
			ExpiresAt:            now.Add(time.Minute),
		},
		prepareErr: ticketissuer.ErrResolverPublicationIndeterminate,
	}
	authority := newNetworkResolverPolicyMigrationAuthorityForTest(coordinator, now)
	caller := control.Caller{Transport: local.PeerIdentity{UserID: "machine-owner"}}

	started, err := authority.StartNetworkResolverPolicyMigration(t.Context(), caller, control.StartNetworkResolverPolicyMigrationRequest{IntentID: "intent-policy-migration"})
	if err != nil {
		t.Fatalf("StartNetworkResolverPolicyMigration() error = %v", err)
	}
	expectedStart := control.NetworkResolverPolicyMigrationOperation{
		Operation: coordinator.startResult.Operation,
		Revision:  coordinator.startResult.Revision,
	}
	if started != expectedStart {
		t.Fatalf("StartNetworkResolverPolicyMigration() = %#v", started)
	}
	preparation, err := authority.PrepareNetworkResolverPolicyMigrationApproval(
		t.Context(),
		caller,
		control.PrepareNetworkResolverPolicyMigrationApprovalRequest{
			OperationID:               started.Operation.ID,
			ExpectedOperationRevision: started.Revision,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkResolverPolicyMigrationApproval() error = %v", err)
	}
	if preparation.PublicationDisposition != control.NetworkResolverPolicyMigrationPublicationIndeterminate {
		t.Fatalf("PublicationDisposition = %q, want indeterminate", preparation.PublicationDisposition)
	}
	if preparation.Ticket.PostOwnershipFingerprint != coordinator.prepareResult.OwnershipFingerprint {
		t.Fatalf("ticket ownership fingerprint = %q, want post-schema-1 fingerprint %q", preparation.Ticket.PostOwnershipFingerprint, coordinator.prepareResult.OwnershipFingerprint)
	}
	expectedStartRequests := []reconcile.NetworkResolverPolicyMigrationStartRequest{
		{
			OperationID:       "operation-policy-migration",
			IntentID:          "intent-policy-migration",
			RequesterIdentity: "machine-owner",
		},
	}
	if !reflect.DeepEqual(coordinator.startRequests, expectedStartRequests) {
		t.Fatalf("Start requests = %#v", coordinator.startRequests)
	}
	expectedPrepareRequests := []reconcile.NetworkResolverPolicyMigrationPrepareRequest{
		{
			OperationID:               "operation-policy-migration",
			ExpectedOperationRevision: 3,
			RequesterIdentity:         "machine-owner",
		},
	}
	if !reflect.DeepEqual(coordinator.prepareRequests, expectedPrepareRequests) {
		t.Fatalf("Prepare requests = %#v", coordinator.prepareRequests)
	}
}
