package state

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// scriptedGlobalNetworkReleaseResolverPlanReader returns one bounded durable release read for source admission tests.
type scriptedGlobalNetworkReleaseResolverPlanReader struct {
	plan     GlobalNetworkReleasePlanRecord
	found    bool
	err      error
	requests []domain.OperationID
}

// ReadGlobalNetworkReleasePlan records the selected operation before returning its scripted durable result.
func (reader *scriptedGlobalNetworkReleaseResolverPlanReader) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error) {
	reader.requests = append(reader.requests, operationID)
	return reader.plan.Clone(), reader.found, reader.err
}

// TestGlobalNetworkReleaseResolverPlanSourceResolvesReleaseAuthority proves only the durable resolver checkpoint grants release capability authority.
func TestGlobalNetworkReleaseResolverPlanSourceResolvesReleaseAuthority(t *testing.T) {
	release := validGlobalNetworkReleaseResolverPlanRecord(t)
	reader := &scriptedGlobalNetworkReleaseResolverPlanReader{
		plan:  release,
		found: true,
	}
	plan, err := NewGlobalNetworkReleaseResolverPlanSource(reader).Resolve(t.Context(), ticketissuer.ResolverRequest{
		OperationID: release.Operation.Operation.ID,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantFingerprint, err := release.Authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Purpose != ticketissuer.ResolverPlanPurposeGlobalRelease ||
		plan.Operation != release.Operation.Operation ||
		plan.OperationRevision != release.Operation.Revision ||
		plan.CheckpointRevision != release.CheckpointRevision ||
		plan.CheckpointPhase != ticketissuer.ResolverCheckpointPhaseGlobalRelease ||
		plan.Mutation != helper.OperationReleaseResolver ||
		plan.ExpectedSourceOwnershipFingerprint != wantFingerprint ||
		plan.TargetOwnership != release.Authority.Projection.ConfirmedOwnership.Record ||
		plan.Policy != release.Authority.Policy {
		t.Fatalf("Resolve() plan = %#v", plan)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Resolve() plan validation = %v", err)
	}
}

// TestGlobalNetworkReleaseResolverPlanSourceRejectsEveryReleaseFence proves callers cannot repurpose stale or incomplete release authority.
func TestGlobalNetworkReleaseResolverPlanSourceRejectsEveryReleaseFence(t *testing.T) {
	base := validGlobalNetworkReleaseResolverPlanRecord(t)
	tests := []struct {
		name   string
		mutate func(*scriptedGlobalNetworkReleaseResolverPlanReader, *ticketissuer.ResolverRequest)
		want   string
	}{
		{
			name: "request",
			mutate: func(_ *scriptedGlobalNetworkReleaseResolverPlanReader, request *ticketissuer.ResolverRequest) {
				request.OperationID = ""
			},
			want: "operation ID",
		},
		{
			name: "reader failure",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.err = errors.New("read failed")
			},
			want: "read failed",
		},
		{
			name: "absent",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.found = false
			},
			want: "authority is absent",
		},
		{
			name: "operation mismatch",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.Operation.Operation.ID = "operation-other"
			},
			want: "does not match requested",
		},
		{
			name: "operation kind",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.Operation.Operation.Kind = domain.OperationKindNetworkDataPlaneSetup
			},
			want: "release operation kind",
		},
		{
			name: "operation state",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.Operation.Operation.State = domain.OperationRequiresApproval
			},
			want: "release operation state",
		},
		{
			name: "operation phase",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.Operation.Operation.Phase = "other"
			},
			want: "release operation phase",
		},
		{
			name: "durable phase",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.Phase = GlobalNetworkReleasePlanPhaseLowPorts
			},
			want: "durable phase",
		},
		{
			name: "checkpoint",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.CheckpointRevision = 0
			},
			want: "release checkpoint revision",
		},
		{
			name: "target mismatch",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.Authority.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = strings.Repeat("b", 64)
			},
			want: "projection fingerprint does not match its record",
		},
		{
			name: "policy mismatch",
			mutate: func(reader *scriptedGlobalNetworkReleaseResolverPlanReader, _ *ticketissuer.ResolverRequest) {
				reader.plan.Authority.Policy.AuthorityFingerprint = strings.Repeat("f", 64)
			},
			want: "policy fingerprint does not match confirmed ownership",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &scriptedGlobalNetworkReleaseResolverPlanReader{
				plan:  base,
				found: true,
			}
			request := ticketissuer.ResolverRequest{
				OperationID: base.Operation.Operation.ID,
			}
			test.mutate(reader, &request)
			_, err := NewGlobalNetworkReleaseResolverPlanSource(reader).Resolve(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
			if test.name == "request" && len(reader.requests) != 0 {
				t.Fatalf("ReadGlobalNetworkReleasePlan() requests = %#v, want none", reader.requests)
			}
		})
	}
}

// TestNewGlobalNetworkReleaseResolverPlanSourceRequiresReader proves release authority cannot fall back to unbound state reads.
func TestNewGlobalNetworkReleaseResolverPlanSourceRequiresReader(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewGlobalNetworkReleaseResolverPlanSource(nil) did not panic")
		}
	}()
	_ = NewGlobalNetworkReleaseResolverPlanSource(nil)
}

// validGlobalNetworkReleaseResolverPlanRecord creates an already-validated resolver checkpoint with its required prior low-port receipt.
func validGlobalNetworkReleaseResolverPlanRecord(t *testing.T) GlobalNetworkReleasePlanRecord {
	t.Helper()
	plan := validGlobalNetworkReleaseLowPortPlanRecord(t)
	plan.Phase = GlobalNetworkReleasePlanPhaseResolver
	plan.LowPortReceipt = &GlobalNetworkReleaseLowPortReceipt{
		SourceCheckpointRevision:          plan.CheckpointRevision - 1,
		LowPortEvidenceDigest:             strings.Repeat("a", 64),
		OwnedAbsentObservationFingerprint: strings.Repeat("b", 64),
		VerifiedAt:                        plan.Authority.Projection.NetworkUpdatedAt,
	}
	return plan
}

var _ GlobalNetworkReleaseResolverPlanReader = (*scriptedGlobalNetworkReleaseResolverPlanReader)(nil)
