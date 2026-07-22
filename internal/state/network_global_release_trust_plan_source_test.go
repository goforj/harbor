package state

import (
	"context"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// scriptedGlobalNetworkReleaseTrustPlanReader returns one bounded durable release read for source admission tests.
type scriptedGlobalNetworkReleaseTrustPlanReader struct {
	plan  GlobalNetworkReleasePlanRecord
	found bool
}

// ReadGlobalNetworkReleasePlan returns the configured release authority.
func (reader *scriptedGlobalNetworkReleaseTrustPlanReader) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error) {
	if operationID != reader.plan.Operation.Operation.ID {
		return GlobalNetworkReleasePlanRecord{}, false, nil
	}
	return reader.plan.Clone(), reader.found, nil
}

// TestGlobalNetworkReleaseTrustPlanSourceResolvesOwnedReleaseAuthority proves only a validated owned trust checkpoint grants destructive capability authority.
func TestGlobalNetworkReleaseTrustPlanSourceResolvesOwnedReleaseAuthority(t *testing.T) {
	release := validGlobalNetworkReleaseTrustPlanRecord(t)
	plan, err := NewGlobalNetworkReleaseTrustPlanSource(&scriptedGlobalNetworkReleaseTrustPlanReader{
		plan:  release,
		found: true,
	}).Resolve(t.Context(), ticketissuer.TrustRequest{OperationID: release.Operation.Operation.ID})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if plan.Purpose != ticketissuer.TrustPlanPurposeGlobalNetworkRelease ||
		plan.Operation != release.Operation.Operation ||
		plan.OperationRevision != release.Operation.Revision ||
		plan.CheckpointRevision != release.CheckpointRevision ||
		plan.CheckpointPhase != ticketissuer.TrustCheckpointPhaseGlobalRelease ||
		plan.Mutation != helper.OperationReleaseTrust ||
		plan.TargetOwnership != release.Authority.Projection.ConfirmedOwnership.Record ||
		plan.Policy != release.Authority.Policy ||
		plan.Root.Fingerprint != release.Authority.Root.Fingerprint {
		t.Fatalf("Resolve() plan = %#v", plan)
	}
	plan.Root.CertificatePEM[0] ^= 1
	if release.Authority.Root.CertificatePEM[0] == plan.Root.CertificatePEM[0] {
		t.Fatal("Resolve() returned mutable retained root")
	}
}

// TestGlobalNetworkReleaseTrustPlanSourceRejectsCorruptOrUnownedReceipts proves a substitutable reader cannot manufacture trust-release authority.
func TestGlobalNetworkReleaseTrustPlanSourceRejectsCorruptOrUnownedReceipts(t *testing.T) {
	base := validGlobalNetworkReleaseTrustPlanRecord(t)
	for _, test := range []struct {
		name   string
		mutate func(*GlobalNetworkReleasePlanRecord)
	}{
		{
			name: "missing resolver receipt",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.ResolverReceipt = nil
			},
		},
		{
			name: "resolver source",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.ResolverReceipt.SourceCheckpointRevision--
			},
		},
		{
			name: "resolver before low port",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.ResolverReceipt.VerifiedAt = plan.LowPortReceipt.VerifiedAt.Add(-1)
			},
		},
		{
			name: "low port source",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.LowPortReceipt.SourceCheckpointRevision = plan.CheckpointRevision - 1
			},
		},
		{
			name: "preexisting unowned",
			mutate: func(plan *GlobalNetworkReleasePlanRecord) {
				plan.Authority.TrustDisposition = GlobalNetworkReleaseTrustPreexistingUnowned
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := base.Clone()
			test.mutate(&plan)
			_, err := NewGlobalNetworkReleaseTrustPlanSource(&scriptedGlobalNetworkReleaseTrustPlanReader{
				plan:  plan,
				found: true,
			}).Resolve(t.Context(), ticketissuer.TrustRequest{OperationID: plan.Operation.Operation.ID})
			if err == nil {
				t.Fatal("Resolve() error = nil")
			}
		})
	}
}

// validGlobalNetworkReleaseTrustPlanRecord creates a trusted checkpoint with ordered low-port and resolver receipts.
func validGlobalNetworkReleaseTrustPlanRecord(t *testing.T) GlobalNetworkReleasePlanRecord {
	t.Helper()
	plan := validGlobalNetworkReleaseResolverPlanRecord(t)
	plan.Phase = GlobalNetworkReleasePlanPhaseTrust
	plan.CheckpointRevision++
	plan.ResolverReceipt = &GlobalNetworkReleaseResolverReceipt{
		SourceCheckpointRevision:          plan.CheckpointRevision - 1,
		ResolverEvidenceDigest:            strings.Repeat("c", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("d", 64),
		VerifiedAt:                        plan.LowPortReceipt.VerifiedAt,
	}
	return plan
}

// _ confirms the source reader fixture keeps the narrow release reader contract.
var _ GlobalNetworkReleaseTrustPlanReader = (*scriptedGlobalNetworkReleaseTrustPlanReader)(nil)
