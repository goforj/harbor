package state

import (
	"context"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// scriptedGlobalNetworkReleaseOwnershipPlanReader returns one bounded durable release read for source admission tests.
type scriptedGlobalNetworkReleaseOwnershipPlanReader struct {
	plan  GlobalNetworkReleasePlanRecord
	found bool
}

// ReadGlobalNetworkReleasePlan returns the configured release authority.
func (reader *scriptedGlobalNetworkReleaseOwnershipPlanReader) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error) {
	if operationID != reader.plan.Operation.Operation.ID {
		return GlobalNetworkReleasePlanRecord{}, false, nil
	}
	return reader.plan.Clone(), reader.found, nil
}

// TestGlobalNetworkReleaseOwnershipPlanSourceResolvesTerminalAuthority proves ownership release is admitted only from its terminal checkpoint.
func TestGlobalNetworkReleaseOwnershipPlanSourceResolvesTerminalAuthority(t *testing.T) {
	release := validGlobalNetworkReleaseOwnershipPlanRecord(t)
	plan, err := NewGlobalNetworkReleaseOwnershipPlanSource(
		&scriptedGlobalNetworkReleaseOwnershipPlanReader{
			plan:  release,
			found: true,
		},
	).Resolve(
		t.Context(),
		ticketissuer.OwnershipReleaseRequest{
			OperationID: release.Operation.Operation.ID,
		},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if plan.Operation != release.Operation.Operation || plan.OperationRevision != release.Operation.Revision || plan.CheckpointRevision != release.CheckpointRevision || plan.Mutation != helper.OperationReleaseNetworkOwnership || plan.TargetOwnership != release.Authority.Projection.ConfirmedOwnership.Record || plan.ExpectedOwnershipFingerprint != release.Authority.ExpectedOwnershipFingerprint {
		t.Fatalf("Resolve() plan = %#v", plan)
	}
}

// TestGlobalNetworkReleaseOwnershipPlanSourceRejectsEveryTerminalFence proves substituted readers cannot manufacture terminal release authority.
func TestGlobalNetworkReleaseOwnershipPlanSourceRejectsEveryTerminalFence(t *testing.T) {
	base := validGlobalNetworkReleaseOwnershipPlanRecord(t)
	for _, test := range []struct {
		name   string
		mutate func(*GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "wrong phase",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.Phase = GlobalNetworkReleasePlanPhaseProjection
			},
		},
		{
			name: "missing effects receipt",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.EffectsReceipt = nil
			},
		},
		{
			name: "effects checkpoint",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.EffectsReceipt.SourceCheckpointRevision--
			},
		},
		{
			name: "effects fingerprint",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.EffectsReceipt.OwnershipObservationFingerprint = strings.Repeat("f", 64)
			},
		},
		{
			name: "missing loopback receipt",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.LoopbackReceipt = nil
			},
		},
		{
			name: "wrong operation",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.Operation.Operation.ProjectID = "project"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := base.Clone()
			test.mutate(&plan)
			_, err := NewGlobalNetworkReleaseOwnershipPlanSource(
				&scriptedGlobalNetworkReleaseOwnershipPlanReader{
					plan:  plan,
					found: true,
				},
			).Resolve(
				t.Context(),
				ticketissuer.OwnershipReleaseRequest{
					OperationID: plan.Operation.Operation.ID,
				},
			)
			if err == nil {
				t.Fatal("Resolve() error = nil")
			}
		})
	}
}

// validGlobalNetworkReleaseOwnershipPlanRecord creates the ordered effects-complete terminal ownership checkpoint.
func validGlobalNetworkReleaseOwnershipPlanRecord(t *testing.T) GlobalNetworkReleasePlanRecord {
	t.Helper()
	plan := validGlobalNetworkReleaseLoopbackPlanRecord(t)
	plan.Phase = GlobalNetworkReleasePlanPhaseOwnership
	plan.CheckpointRevision += 2
	plan.LoopbackReceipt = &GlobalNetworkReleaseLoopbackReceipt{
		SourceCheckpointRevision:     plan.CheckpointRevision - 2,
		LoopbackEvidenceDigest:       strings.Repeat("9", 64),
		OwnedAbsentObservationDigest: strings.Repeat("8", 64),
		VerifiedAt:                   plan.TrustReceipt.VerifiedAt,
	}
	plan.EffectsReceipt = &GlobalNetworkReleaseEffectsReceipt{
		SourceCheckpointRevision:        plan.CheckpointRevision - 1,
		RuntimeObservationDigest:        strings.Repeat("a", 64),
		OwnershipObservationFingerprint: plan.Authority.ExpectedOwnershipFingerprint,
		LowPortObservationFingerprint:   strings.Repeat("b", 64),
		ResolverObservationFingerprint:  strings.Repeat("c", 64),
		TrustObservationFingerprint:     strings.Repeat("d", 64),
		LoopbackObservationDigest:       strings.Repeat("e", 64),
		VerifiedAt:                      plan.LoopbackReceipt.VerifiedAt,
	}
	return plan
}

// _ confirms the source reader fixture keeps the narrow release reader contract.
var _ GlobalNetworkReleaseOwnershipPlanReader = (*scriptedGlobalNetworkReleaseOwnershipPlanReader)(nil)
