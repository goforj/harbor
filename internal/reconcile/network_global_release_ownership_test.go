package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// TestGlobalNetworkReleasePrepareOwnershipPublishesOnlyTheDurableTerminalAuthority verifies the exact requester and checkpoint cross the issuer boundary.
func TestGlobalNetworkReleasePrepareOwnershipPublishesOnlyTheDurableTerminalAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 23, 4, 0, 0, 0, time.UTC)
	plan := globalNetworkReleaseOwnershipTestPlan(t)
	result := globalNetworkReleaseOwnershipTestResult(plan, now)
	source := &globalNetworkReleaseOwnershipPlanSource{plan: plan}
	issuer := &globalNetworkReleaseOwnershipIssuer{result: result}
	coordinator := &GlobalNetworkReleaseCoordinator{
		ownershipPlans: source,
		ownershipIssuers: func() (GlobalNetworkReleaseOwnershipIssuer, error) {
			return issuer, nil
		},
		clock: &globalNetworkReleaseClock{now: now},
	}
	request := GlobalNetworkReleasePrepareOwnershipRequest{
		OperationID:                plan.Operation.ID,
		ExpectedCheckpointRevision: plan.CheckpointRevision,
		RequesterIdentity:          plan.TargetOwnership.OwnerIdentity,
	}

	prepared, err := coordinator.PrepareOwnership(t.Context(), request)
	if err != nil {
		t.Fatalf("PrepareOwnership() error = %v", err)
	}
	if prepared != result ||
		source.request.OperationID != request.OperationID ||
		issuer.request.OperationID != request.OperationID ||
		issuer.requester != request.RequesterIdentity ||
		issuer.calls != 1 ||
		issuer.closeCalls != 1 {
		t.Fatalf(
			"PrepareOwnership() = %#v; source request = %#v; issuer = %#v",
			prepared,
			source.request,
			issuer,
		)
	}
}

// TestGlobalNetworkReleasePrepareOwnershipPreservesIndeterminatePublication prevents a possibly published capability from being replaced.
func TestGlobalNetworkReleasePrepareOwnershipPreservesIndeterminatePublication(t *testing.T) {
	now := time.Date(2026, time.July, 23, 4, 0, 0, 0, time.UTC)
	plan := globalNetworkReleaseOwnershipTestPlan(t)
	result := globalNetworkReleaseOwnershipTestResult(plan, now)
	for _, test := range []struct {
		name     string
		issueErr error
		closeErr error
	}{
		{
			name:     "issue",
			issueErr: ticketissuer.ErrOwnershipReleasePublicationIndeterminate,
		},
		{
			name:     "close",
			closeErr: errors.New("close ownership issuer"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			issuer := &globalNetworkReleaseOwnershipIssuer{
				result:   result,
				issueErr: test.issueErr,
				closeErr: test.closeErr,
			}
			coordinator := &GlobalNetworkReleaseCoordinator{
				ownershipPlans: &globalNetworkReleaseOwnershipPlanSource{plan: plan},
				ownershipIssuers: func() (GlobalNetworkReleaseOwnershipIssuer, error) {
					return issuer, nil
				},
				clock: &globalNetworkReleaseClock{now: now},
			}
			prepared, err := coordinator.PrepareOwnership(
				t.Context(),
				GlobalNetworkReleasePrepareOwnershipRequest{
					OperationID:                plan.Operation.ID,
					ExpectedCheckpointRevision: plan.CheckpointRevision,
					RequesterIdentity:          plan.TargetOwnership.OwnerIdentity,
				},
			)
			if prepared != result || !errors.Is(err, ticketissuer.ErrOwnershipReleasePublicationIndeterminate) {
				t.Fatalf("PrepareOwnership() = (%#v, %v), want result and indeterminate publication", prepared, err)
			}
		})
	}
}

// TestGlobalNetworkReleasePrepareOwnershipRejectsDriftBeforePublication ensures no helper authority opens for another owner or checkpoint.
func TestGlobalNetworkReleasePrepareOwnershipRejectsDriftBeforePublication(t *testing.T) {
	now := time.Date(2026, time.July, 23, 4, 0, 0, 0, time.UTC)
	plan := globalNetworkReleaseOwnershipTestPlan(t)
	for _, test := range []struct {
		name   string
		mutate func(*GlobalNetworkReleasePrepareOwnershipRequest)
	}{
		{
			name: "checkpoint",
			mutate: func(request *GlobalNetworkReleasePrepareOwnershipRequest) {
				request.ExpectedCheckpointRevision++
			},
		},
		{
			name: "requester",
			mutate: func(request *GlobalNetworkReleasePrepareOwnershipRequest) {
				request.RequesterIdentity = "different-owner"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			issuerCalls := 0
			coordinator := &GlobalNetworkReleaseCoordinator{
				ownershipPlans: &globalNetworkReleaseOwnershipPlanSource{plan: plan},
				ownershipIssuers: func() (GlobalNetworkReleaseOwnershipIssuer, error) {
					issuerCalls++
					return &globalNetworkReleaseOwnershipIssuer{}, nil
				},
				clock: &globalNetworkReleaseClock{now: now},
			}
			request := GlobalNetworkReleasePrepareOwnershipRequest{
				OperationID:                plan.Operation.ID,
				ExpectedCheckpointRevision: plan.CheckpointRevision,
				RequesterIdentity:          plan.TargetOwnership.OwnerIdentity,
			}
			test.mutate(&request)
			if _, err := coordinator.PrepareOwnership(t.Context(), request); err == nil {
				t.Fatal("PrepareOwnership() error = nil")
			}
			if issuerCalls != 0 {
				t.Fatalf("ownership issuer factory calls = %d, want 0", issuerCalls)
			}
		})
	}
}

// globalNetworkReleaseOwnershipPlanSource records the exact durable operation selection.
type globalNetworkReleaseOwnershipPlanSource struct {
	request ticketissuer.OwnershipReleaseRequest
	plan    ticketissuer.OwnershipReleasePlan
	err     error
}

// Resolve returns the configured terminal ownership-release plan.
func (source *globalNetworkReleaseOwnershipPlanSource) Resolve(
	_ context.Context,
	request ticketissuer.OwnershipReleaseRequest,
) (ticketissuer.OwnershipReleasePlan, error) {
	source.request = request
	return source.plan, source.err
}

// globalNetworkReleaseOwnershipIssuer records the authenticated publication request and lifecycle.
type globalNetworkReleaseOwnershipIssuer struct {
	request    ticketissuer.OwnershipReleaseRequest
	requester  string
	result     ticketissuer.OwnershipReleaseResult
	issueErr   error
	closeErr   error
	calls      int
	closeCalls int
}

// Issue returns the configured ownership-release capability result.
func (issuer *globalNetworkReleaseOwnershipIssuer) Issue(
	_ context.Context,
	requester string,
	request ticketissuer.OwnershipReleaseRequest,
) (ticketissuer.OwnershipReleaseResult, error) {
	issuer.calls++
	issuer.requester = requester
	issuer.request = request
	return issuer.result, issuer.issueErr
}

// Close records the issuer lifecycle boundary.
func (issuer *globalNetworkReleaseOwnershipIssuer) Close() error {
	issuer.closeCalls++
	return issuer.closeErr
}

// globalNetworkReleaseOwnershipTestPlan constructs one complete terminal ownership authority.
func globalNetworkReleaseOwnershipTestPlan(t *testing.T) ticketissuer.OwnershipReleasePlan {
	t.Helper()
	operation := testGlobalNetworkReleaseOperation(t)
	trustPlan, _ := networkDataPlaneSetupTestTrustPlan(t)
	target := trustPlan.TargetOwnership
	target.OwnerIdentity = "501"
	fingerprint, err := target.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return ticketissuer.OwnershipReleasePlan{
		Operation:                    operation.Operation,
		OperationRevision:            operation.Revision,
		CheckpointRevision:           operation.Revision + 1,
		Mutation:                     helper.OperationReleaseNetworkOwnership,
		TargetOwnership:              target,
		ExpectedOwnershipFingerprint: fingerprint,
	}
}

// globalNetworkReleaseOwnershipTestResult constructs exact opaque launch metadata for one plan.
func globalNetworkReleaseOwnershipTestResult(
	plan ticketissuer.OwnershipReleasePlan,
	now time.Time,
) ticketissuer.OwnershipReleaseResult {
	return ticketissuer.OwnershipReleaseResult{
		OperationID:          plan.Operation.ID,
		OperationRevision:    plan.OperationRevision,
		CheckpointRevision:   plan.CheckpointRevision,
		Reference:            helper.TicketReference(strings.Repeat("a", 64)),
		Operation:            helper.OperationReleaseNetworkOwnership,
		OwnershipFingerprint: plan.ExpectedOwnershipFingerprint,
		ExpiresAt:            now.Add(time.Minute),
	}
}
