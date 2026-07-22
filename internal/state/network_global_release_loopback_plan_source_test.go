package state

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// scriptedGlobalNetworkReleaseLoopbackPlanReader returns one bounded durable release read for source admission tests.
type scriptedGlobalNetworkReleaseLoopbackPlanReader struct {
	plan  GlobalNetworkReleasePlanRecord
	found bool
	err   error
}

// ReadGlobalNetworkReleasePlan returns the configured release authority.
func (reader *scriptedGlobalNetworkReleaseLoopbackPlanReader) ReadGlobalNetworkReleasePlan(
	_ context.Context,
	_ domain.OperationID,
) (GlobalNetworkReleasePlanRecord, bool, error) {
	return reader.plan.Clone(), reader.found, reader.err
}

// TestGlobalNetworkReleaseLoopbackPlanSourceResolvesReleaseAuthority proves the full ordered loopback checkpoint grants only its retained authority.
func TestGlobalNetworkReleaseLoopbackPlanSourceResolvesReleaseAuthority(t *testing.T) {
	release := validGlobalNetworkReleaseLoopbackPlanRecord(t)
	plan, err := NewGlobalNetworkReleaseLoopbackPlanSource(&scriptedGlobalNetworkReleaseLoopbackPlanReader{
		plan:  release,
		found: true,
	}).Resolve(
		t.Context(),
		ticketissuer.PoolReleaseRequest{OperationID: release.Operation.Operation.ID},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if plan.Operation != release.Operation.Operation ||
		plan.OperationRevision != release.Operation.Revision ||
		plan.CheckpointRevision != release.CheckpointRevision ||
		plan.TargetOwnership != release.Authority.Projection.ConfirmedOwnership.Record ||
		plan.Pool.Prefix().String() != release.Authority.Projection.ConfirmedOwnership.Record.LoopbackPoolPrefix ||
		len(plan.Targets) != 8 {
		t.Fatalf("Resolve() plan = %#v", plan)
	}
	for index, target := range release.Authority.LoopbackTargets {
		if plan.Targets[index].Address != target.Address ||
			plan.Targets[index].ObservationFingerprint != target.ObservationFingerprint {
			t.Fatalf("Resolve() targets = %#v", plan.Targets)
		}
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Resolve() plan validation = %v", err)
	}
}

// TestGlobalNetworkReleaseLoopbackPlanSourceRejectsEveryReleaseFence proves incomplete or substituted authority cannot release the loopback pool.
func TestGlobalNetworkReleaseLoopbackPlanSourceRejectsEveryReleaseFence(t *testing.T) {
	base := validGlobalNetworkReleaseLoopbackPlanRecord(t)
	wrongPhases := []GlobalNetworkReleasePlanPhase{
		GlobalNetworkReleasePlanPhaseRuntimeRelease,
		GlobalNetworkReleasePlanPhaseLowPorts,
		GlobalNetworkReleasePlanPhaseResolver,
		GlobalNetworkReleasePlanPhaseTrust,
		GlobalNetworkReleasePlanPhaseVerifyEffects,
		GlobalNetworkReleasePlanPhaseOwnership,
		GlobalNetworkReleasePlanPhaseProjection,
	}
	tests := []struct {
		name   string
		mutate func(*scriptedGlobalNetworkReleaseLoopbackPlanReader, *ticketissuer.PoolReleaseRequest)
	}{
		{
			name: "request",
			mutate: func(_ *scriptedGlobalNetworkReleaseLoopbackPlanReader, request *ticketissuer.PoolReleaseRequest) {
				request.OperationID = ""
			},
		},
		{
			name: "reader",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.err = errors.New("read failed")
			},
		},
		{
			name: "missing operation",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.found = false
			},
		},
		{
			name: "other operation",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Operation.Operation.ID = "other"
			},
		},
		{
			name: "revision",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Operation.Revision = 0
			},
		},
		{
			name: "state",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Operation.Operation.State = domain.OperationRequiresApproval
			},
		},
		{
			name: "runtime operation phase",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Operation.Operation.Phase = "other"
			},
		},
		{
			name: "checkpoint before operation revision",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.CheckpointRevision = reader.plan.Operation.Revision
			},
		},
		{
			name: "kind",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Operation.Operation.Kind = domain.OperationKindNetworkDataPlaneSetup
			},
		},
		{
			name: "project",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Operation.Operation.ProjectID = "project"
			},
		},
		{
			name: "checkpoint",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.CheckpointRevision = 0
			},
		},
		{
			name: "missing low port receipt",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.LowPortReceipt = nil
			},
		},
		{
			name: "corrupt low port receipt",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.LowPortReceipt.LowPortEvidenceDigest = "bad"
			},
		},
		{
			name: "missing resolver receipt",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.ResolverReceipt = nil
			},
		},
		{
			name: "corrupt resolver receipt",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.ResolverReceipt.ResolverEvidenceDigest = "bad"
			},
		},
		{
			name: "missing trust receipt",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.TrustReceipt = nil
			},
		},
		{
			name: "corrupt trust receipt",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.TrustReceipt.ConfirmationDigest = "bad"
			},
		},
		{
			name: "targets",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Authority.LoopbackTargets[0],
					reader.plan.Authority.LoopbackTargets[1] = reader.plan.Authority.LoopbackTargets[1],
					reader.plan.Authority.LoopbackTargets[0]
			},
		},
		{
			name: "pool",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Authority.Projection.ConfirmedOwnership.Record.LoopbackPoolPrefix = "127.0.0.8/29"
			},
		},
		{
			name: "ownership",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Authority.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = strings.Repeat("f", 64)
			},
		},
		{
			name: "target fingerprint",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Authority.LoopbackTargets[0].ObservationFingerprint = "bad"
			},
		},
		{
			name: "resolver source ordering",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.ResolverReceipt.SourceCheckpointRevision = reader.plan.LowPortReceipt.SourceCheckpointRevision
			},
		},
		{
			name: "trust time ordering",
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.TrustReceipt.VerifiedAt = reader.plan.ResolverReceipt.VerifiedAt.Add(-1)
			},
		},
	}
	for _, phase := range wrongPhases {
		phase := phase
		tests = append(tests, struct {
			name   string
			mutate func(*scriptedGlobalNetworkReleaseLoopbackPlanReader, *ticketissuer.PoolReleaseRequest)
		}{
			name: "phase " + string(phase),
			mutate: func(reader *scriptedGlobalNetworkReleaseLoopbackPlanReader, _ *ticketissuer.PoolReleaseRequest) {
				reader.plan.Phase = phase
			},
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &scriptedGlobalNetworkReleaseLoopbackPlanReader{
				plan:  base,
				found: true,
			}
			request := ticketissuer.PoolReleaseRequest{OperationID: base.Operation.Operation.ID}
			test.mutate(reader, &request)
			if _, err := NewGlobalNetworkReleaseLoopbackPlanSource(reader).Resolve(t.Context(), request); err == nil {
				t.Fatal("Resolve() error = nil")
			}
		})
	}
}

// TestGlobalNetworkReleaseLoopbackPlanSourceDefensivelyCopiesTargets proves issuer callers cannot alter retained release authority.
func TestGlobalNetworkReleaseLoopbackPlanSourceDefensivelyCopiesTargets(t *testing.T) {
	release := validGlobalNetworkReleaseLoopbackPlanRecord(t)
	reader := &scriptedGlobalNetworkReleaseLoopbackPlanReader{
		plan:  release,
		found: true,
	}
	source := NewGlobalNetworkReleaseLoopbackPlanSource(reader)
	first, err := source.Resolve(
		t.Context(),
		ticketissuer.PoolReleaseRequest{OperationID: release.Operation.Operation.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	first.Targets[0].ObservationFingerprint = strings.Repeat("f", 64)
	second, err := source.Resolve(
		t.Context(),
		ticketissuer.PoolReleaseRequest{OperationID: release.Operation.Operation.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(first.Targets, second.Targets) ||
		second.Targets[0].ObservationFingerprint != release.Authority.LoopbackTargets[0].ObservationFingerprint {
		t.Fatalf("Resolve() targets leaked retained authority: %#v", second.Targets)
	}
}

// validGlobalNetworkReleaseLoopbackPlanRecord creates a loopback checkpoint with each required earlier release receipt.
func validGlobalNetworkReleaseLoopbackPlanRecord(t *testing.T) GlobalNetworkReleasePlanRecord {
	t.Helper()
	plan := validGlobalNetworkReleaseTrustPlanRecord(t)
	plan.Phase = GlobalNetworkReleasePlanPhaseLoopbacks
	plan.CheckpointRevision++
	plan.TrustReceipt = &GlobalNetworkReleaseTrustReceipt{
		SourceCheckpointRevision: plan.CheckpointRevision - 1,
		Disposition:              plan.Authority.TrustDisposition,
		ConfirmationDigest:       strings.Repeat("e", 64),
		ObservationFingerprint:   strings.Repeat("f", 64),
		VerifiedAt:               plan.ResolverReceipt.VerifiedAt,
	}
	return plan
}

// _ confirms the source reader fixture keeps the narrow release reader contract.
var _ GlobalNetworkReleaseLoopbackPlanReader = (*scriptedGlobalNetworkReleaseLoopbackPlanReader)(nil)
