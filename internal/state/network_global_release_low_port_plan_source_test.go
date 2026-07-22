package state

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/platform/lowport"
)

// scriptedGlobalNetworkReleaseLowPortPlanReader returns one bounded durable release read for source admission tests.
type scriptedGlobalNetworkReleaseLowPortPlanReader struct {
	plan     GlobalNetworkReleasePlanRecord
	found    bool
	err      error
	requests []domain.OperationID
}

// ReadGlobalNetworkReleasePlan records the selected operation before returning its scripted durable result.
func (reader *scriptedGlobalNetworkReleaseLowPortPlanReader) ReadGlobalNetworkReleasePlan(_ context.Context, operationID domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error) {
	reader.requests = append(reader.requests, operationID)
	return reader.plan.Clone(), reader.found, reader.err
}

// TestGlobalNetworkReleaseLowPortPlanSourceResolvesStagedReleaseAuthority proves only the durable low-port checkpoint grants release capability authority.
func TestGlobalNetworkReleaseLowPortPlanSourceResolvesStagedReleaseAuthority(t *testing.T) {
	release := validGlobalNetworkReleaseLowPortPlanRecord(t)
	reader := &scriptedGlobalNetworkReleaseLowPortPlanReader{
		plan:  release,
		found: true,
	}
	source := NewGlobalNetworkReleaseLowPortPlanSource(reader)
	plan, err := source.Resolve(t.Context(), ticketissuer.LowPortRequest{OperationID: release.Operation.Operation.ID})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantNative, err := lowport.NewRequest(release.Authority.Projection.ConfirmedOwnership.Record, release.Authority.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Purpose != ticketissuer.LowPortPlanPurposeGlobalNetworkRelease ||
		plan.Operation != release.Operation.Operation ||
		plan.OperationRevision != release.Operation.Revision ||
		plan.CheckpointRevision != release.CheckpointRevision ||
		plan.CheckpointPhase != ticketissuer.LowPortCheckpointPhaseGlobalRelease ||
		plan.Mutation != helper.OperationReleaseLowPorts ||
		plan.TargetOwnership != release.Authority.Projection.ConfirmedOwnership.Record ||
		plan.Policy != release.Authority.Policy ||
		plan.NativeRequest != wantNative {
		t.Fatalf("Resolve() plan = %#v", plan)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Resolve() plan validation = %v", err)
	}
}

// TestGlobalNetworkReleaseLowPortPlanSourceRejectsEveryReleaseFence proves callers cannot repurpose setup or stale release authority.
func TestGlobalNetworkReleaseLowPortPlanSourceRejectsEveryReleaseFence(t *testing.T) {
	base := validGlobalNetworkReleaseLowPortPlanRecord(t)
	tests := []struct {
		name   string
		mutate func(*scriptedGlobalNetworkReleaseLowPortPlanReader, *ticketissuer.LowPortRequest)
		want   string
	}{
		{
			name: "reader failure",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.err = errors.New("read failed")
			},
			want: "read failed",
		},
		{
			name: "absent",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.found = false
			},
			want: "authority is absent",
		},
		{
			name: "operation mismatch",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.plan.Operation.Operation.ID = "operation-other"
			},
			want: "does not match requested",
		},
		{
			name: "runtime phase",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.plan.Phase = GlobalNetworkReleasePlanPhaseRuntimeRelease
			},
			want: "durable phase",
		},
		{
			name: "operation kind",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.plan.Operation.Operation.Kind = domain.OperationKindNetworkDataPlaneSetup
			},
			want: "release operation kind",
		},
		{
			name: "operation state",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.plan.Operation.Operation.State = domain.OperationRequiresApproval
			},
			want: "release operation state",
		},
		{
			name: "operation phase",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.plan.Operation.Operation.Phase = "other"
			},
			want: "release operation phase",
		},
		{
			name: "checkpoint",
			mutate: func(reader *scriptedGlobalNetworkReleaseLowPortPlanReader, _ *ticketissuer.LowPortRequest) {
				reader.plan.CheckpointRevision = 0
			},
			want: "release checkpoint revision",
		},
		{
			name: "caller request",
			mutate: func(_ *scriptedGlobalNetworkReleaseLowPortPlanReader, request *ticketissuer.LowPortRequest) {
				request.OperationID = "operation-other"
			},
			want: "does not match requested",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &scriptedGlobalNetworkReleaseLowPortPlanReader{
				plan:  base,
				found: true,
			}
			request := ticketissuer.LowPortRequest{OperationID: base.Operation.Operation.ID}
			test.mutate(reader, &request)
			_, err := NewGlobalNetworkReleaseLowPortPlanSource(reader).Resolve(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNewGlobalNetworkReleaseLowPortPlanSourceRequiresReader proves release authority cannot fall back to unbound state reads.
func TestNewGlobalNetworkReleaseLowPortPlanSourceRequiresReader(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewGlobalNetworkReleaseLowPortPlanSource(nil) did not panic")
		}
	}()
	_ = NewGlobalNetworkReleaseLowPortPlanSource(nil)
}

// validGlobalNetworkReleaseLowPortPlanRecord creates an already-validated low-port checkpoint without mutable state dependencies.
func validGlobalNetworkReleaseLowPortPlanRecord(t *testing.T) GlobalNetworkReleasePlanRecord {
	t.Helper()
	_, _, stage := newGlobalNetworkReleaseStageFixture(t)
	authority := stage.Authority.Clone()
	authority.Policy.Mechanisms = networkpolicy.MacOSMechanisms()
	policyFingerprint, err := authority.Policy.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	authority.Projection.ConfirmedOwnership.Record.NetworkPolicyFingerprint = policyFingerprint
	ownershipFingerprint, err := authority.Projection.ConfirmedOwnership.Record.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	authority.Projection.ConfirmedOwnership.Fingerprint = ownershipFingerprint
	authority.ExpectedOwnershipFingerprint = ownershipFingerprint
	operation := stage.Operation
	operation.State = domain.OperationRunning
	operation.Phase = globalNetworkReleaseRuntimeOperationPhase
	startedAt := operation.RequestedAt
	operation.StartedAt = &startedAt
	return GlobalNetworkReleasePlanRecord{
		Operation: OperationRecord{
			Operation: operation,
			Revision:  5,
		},
		Phase:              GlobalNetworkReleasePlanPhaseLowPorts,
		CheckpointRevision: 6,
		NetworkRevision:    authority.Projection.NetworkRevision,
		NetworkUpdatedAt:   authority.Projection.NetworkUpdatedAt,
		Authority:          authority,
	}
}

var _ GlobalNetworkReleaseLowPortPlanReader = (*scriptedGlobalNetworkReleaseLowPortPlanReader)(nil)
