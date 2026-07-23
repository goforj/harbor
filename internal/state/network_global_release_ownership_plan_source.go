package state

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// GlobalNetworkReleaseOwnershipPlanReader reads one validated durable global release plan.
type GlobalNetworkReleaseOwnershipPlanReader interface {
	// ReadGlobalNetworkReleasePlan returns the plan for the selected operation while it remains active.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error)
}

// GlobalNetworkReleaseOwnershipPlanSource resolves the terminal ownership-release authority from the staged global release plan.
type GlobalNetworkReleaseOwnershipPlanSource struct {
	plans GlobalNetworkReleaseOwnershipPlanReader
}

// GlobalNetworkReleaseOwnershipPlanSource must retain the ticket issuer's narrow read contract.
var _ ticketissuer.OwnershipReleasePlanSource = (*GlobalNetworkReleaseOwnershipPlanSource)(nil)

// NewGlobalNetworkReleaseOwnershipPlanSource creates a source fenced by validated durable global release authority.
func NewGlobalNetworkReleaseOwnershipPlanSource(plans GlobalNetworkReleaseOwnershipPlanReader) *GlobalNetworkReleaseOwnershipPlanSource {
	if plans == nil {
		panic("state.NewGlobalNetworkReleaseOwnershipPlanSource requires a global network release plan reader")
	}
	return &GlobalNetworkReleaseOwnershipPlanSource{plans: plans}
}

// Resolve returns only the ownership-release capability approved at the terminal ownership checkpoint.
func (source *GlobalNetworkReleaseOwnershipPlanSource) Resolve(ctx context.Context, request ticketissuer.OwnershipReleaseRequest) (ticketissuer.OwnershipReleasePlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.OwnershipReleasePlan{}, fmt.Errorf("resolve global network release ownership plan: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.OwnershipReleasePlan{}, err
	}
	plan, found, err := source.plans.ReadGlobalNetworkReleasePlan(ctx, request.OperationID)
	if err != nil {
		return ticketissuer.OwnershipReleasePlan{}, fmt.Errorf("resolve global network release ownership plan %q: %w", request.OperationID, err)
	}
	if !found {
		return ticketissuer.OwnershipReleasePlan{}, errors.New("resolve global network release ownership plan: active release authority is absent")
	}
	if plan.Operation.Operation.ID != request.OperationID {
		return ticketissuer.OwnershipReleasePlan{}, errors.New("resolve global network release ownership plan: durable operation does not match requested operation")
	}
	if err := validateGlobalNetworkReleaseOwnershipPlanRecord(plan); err != nil {
		return ticketissuer.OwnershipReleasePlan{}, fmt.Errorf("resolve global network release ownership plan: invalid durable authority: %w", err)
	}
	result := ticketissuer.OwnershipReleasePlan{
		Operation:                    plan.Operation.Operation,
		OperationRevision:            plan.Operation.Revision,
		CheckpointRevision:           plan.CheckpointRevision,
		Mutation:                     helper.OperationReleaseNetworkOwnership,
		TargetOwnership:              plan.Authority.Projection.ConfirmedOwnership.Record,
		ExpectedOwnershipFingerprint: plan.Authority.ExpectedOwnershipFingerprint,
	}
	if err := result.Validate(); err != nil {
		return ticketissuer.OwnershipReleasePlan{}, fmt.Errorf("resolve global network release ownership plan: invalid durable authority: %w", err)
	}
	return result, nil
}

// validateGlobalNetworkReleaseOwnershipPlanRecord preserves the terminal release boundary when a narrow reader is substituted.
func validateGlobalNetworkReleaseOwnershipPlanRecord(plan GlobalNetworkReleasePlanRecord) error {
	operation := plan.Operation.Operation
	if err := operation.Validate(); err != nil {
		return fmt.Errorf("release operation: %w", err)
	}
	if operation.Kind != domain.OperationKindNetworkRelease {
		return fmt.Errorf("release operation kind is %q, want %q", operation.Kind, domain.OperationKindNetworkRelease)
	}
	if operation.ProjectID != "" {
		return errors.New("release operation is not global")
	}
	if operation.State != domain.OperationRunning || operation.Phase != globalNetworkReleaseRuntimeOperationPhase {
		return errors.New("release operation is not active global release authority")
	}
	if plan.Operation.Revision == 0 || plan.Operation.Revision > domain.MaximumSequence {
		return errors.New("release operation revision is outside the durable sequence range")
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseOwnership {
		return fmt.Errorf("durable phase is %q, want %q", plan.Phase, GlobalNetworkReleasePlanPhaseOwnership)
	}
	if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
		return errors.New("release checkpoint revision is outside the durable sequence range")
	}
	if err := plan.Authority.Validate(); err != nil {
		return fmt.Errorf("release authority: %w", err)
	}
	if plan.EffectsReceipt == nil {
		return errors.New("release ownership phase has no effects receipt")
	}
	if err := plan.EffectsReceipt.Validate(); err != nil {
		return fmt.Errorf("release effects receipt: %w", err)
	}
	if plan.EffectsReceipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
		return errors.New("release effects receipt does not precede the ownership checkpoint")
	}
	if plan.EffectsReceipt.OwnershipObservationFingerprint != plan.Authority.ExpectedOwnershipFingerprint {
		return errors.New("release effects receipt ownership fingerprint does not match retained authority")
	}
	if err := validateGlobalNetworkReleaseOwnershipPredecessors(plan); err != nil {
		return err
	}
	return nil
}

// validateGlobalNetworkReleaseOwnershipPredecessors requires every destructive release receipt retained before ownership release.
func validateGlobalNetworkReleaseOwnershipPredecessors(plan GlobalNetworkReleasePlanRecord) error {
	if plan.LowPortReceipt == nil || plan.ResolverReceipt == nil || plan.TrustReceipt == nil {
		return errors.New("release ownership phase has incomplete destructive-release receipts")
	}
	if err := plan.LowPortReceipt.Validate(); err != nil {
		return fmt.Errorf("release low-port receipt: %w", err)
	}
	if err := plan.ResolverReceipt.Validate(); err != nil {
		return fmt.Errorf("release resolver receipt: %w", err)
	}
	if err := plan.TrustReceipt.Validate(); err != nil {
		return fmt.Errorf("release trust receipt: %w", err)
	}
	if plan.ResolverReceipt.SourceCheckpointRevision <= plan.LowPortReceipt.SourceCheckpointRevision ||
		plan.ResolverReceipt.VerifiedAt.Before(plan.LowPortReceipt.VerifiedAt) ||
		plan.TrustReceipt.SourceCheckpointRevision <= plan.ResolverReceipt.SourceCheckpointRevision ||
		plan.TrustReceipt.VerifiedAt.Before(plan.ResolverReceipt.VerifiedAt) ||
		plan.TrustReceipt.Disposition != plan.Authority.TrustDisposition {
		return errors.New("release ownership phase destructive-release receipt order is invalid")
	}
	if plan.LoopbackReceipt == nil {
		return errors.New("release ownership phase has no loopback receipt")
	}
	if err := plan.LoopbackReceipt.Validate(); err != nil {
		return fmt.Errorf("release loopback receipt: %w", err)
	}
	if plan.EffectsReceipt.VerifiedAt.Before(plan.LoopbackReceipt.VerifiedAt) {
		return errors.New("release effects receipt verification precedes loopback receipt")
	}
	if plan.LoopbackReceipt.SourceCheckpointRevision <= plan.TrustReceipt.SourceCheckpointRevision {
		return errors.New("release loopback receipt does not follow trust receipt")
	}
	if plan.LoopbackReceipt.SourceCheckpointRevision+1 != plan.EffectsReceipt.SourceCheckpointRevision {
		return errors.New("release effects receipt does not follow loopback receipt")
	}
	return nil
}
