package state

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// GlobalNetworkReleaseTrustPlanReader reads one validated durable global release plan.
type GlobalNetworkReleaseTrustPlanReader interface {
	// ReadGlobalNetworkReleasePlan returns the plan for the selected operation while it remains active.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error)
}

// GlobalNetworkReleaseTrustPlanSource resolves owned-only trust-release capability authority from the staged global release plan.
type GlobalNetworkReleaseTrustPlanSource struct {
	plans GlobalNetworkReleaseTrustPlanReader
}

// GlobalNetworkReleaseTrustPlanSource must retain the ticket issuer's narrow read contract.
var _ ticketissuer.TrustPlanSource = (*GlobalNetworkReleaseTrustPlanSource)(nil)

// NewGlobalNetworkReleaseTrustPlanSource creates a source fenced by validated durable global release authority.
func NewGlobalNetworkReleaseTrustPlanSource(plans GlobalNetworkReleaseTrustPlanReader) *GlobalNetworkReleaseTrustPlanSource {
	if plans == nil {
		panic("state.NewGlobalNetworkReleaseTrustPlanSource requires a global network release plan reader")
	}
	return &GlobalNetworkReleaseTrustPlanSource{
		plans: plans,
	}
}

// Resolve returns only the owned trust-release capability approved at the current trust checkpoint.
func (source *GlobalNetworkReleaseTrustPlanSource) Resolve(ctx context.Context, request ticketissuer.TrustRequest) (ticketissuer.TrustPlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("resolve global network release trust plan: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.TrustPlan{}, err
	}
	plan, found, err := source.plans.ReadGlobalNetworkReleasePlan(ctx, request.OperationID)
	if err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("resolve global network release trust plan %q: %w", request.OperationID, err)
	}
	if !found {
		return ticketissuer.TrustPlan{}, errors.New("resolve global network release trust plan: active release authority is absent")
	}
	if plan.Operation.Operation.ID != request.OperationID {
		return ticketissuer.TrustPlan{}, errors.New("resolve global network release trust plan: durable operation does not match requested operation")
	}
	if err := validateGlobalNetworkReleaseTrustPlanRecord(plan); err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("resolve global network release trust plan: invalid durable authority: %w", err)
	}
	result := ticketissuer.TrustPlan{
		Purpose:            ticketissuer.TrustPlanPurposeGlobalNetworkRelease,
		Operation:          plan.Operation.Operation,
		OperationRevision:  plan.Operation.Revision,
		CheckpointRevision: plan.CheckpointRevision,
		CheckpointPhase:    ticketissuer.TrustCheckpointPhaseGlobalRelease,
		Mutation:           helper.OperationReleaseTrust,
		TargetOwnership:    plan.Authority.Projection.ConfirmedOwnership.Record,
		Policy:             plan.Authority.Policy,
		Root:               cloneGlobalNetworkReleaseRoot(plan.Authority.Root),
	}
	if err := result.Validate(); err != nil {
		return ticketissuer.TrustPlan{}, fmt.Errorf("resolve global network release trust plan: invalid durable authority: %w", err)
	}
	return result, nil
}

// validateGlobalNetworkReleaseTrustPlanRecord preserves the owned active global-release boundary when a narrow reader is substituted.
func validateGlobalNetworkReleaseTrustPlanRecord(plan GlobalNetworkReleasePlanRecord) error {
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
	if plan.Phase != GlobalNetworkReleasePlanPhaseTrust {
		return fmt.Errorf("durable phase is %q, want %q", plan.Phase, GlobalNetworkReleasePlanPhaseTrust)
	}
	if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
		return errors.New("release checkpoint revision is outside the durable sequence range")
	}
	if plan.ResolverReceipt == nil {
		return errors.New("release trust phase has no resolver receipt")
	}
	if plan.LowPortReceipt == nil {
		return errors.New("release trust phase has no low-port receipt")
	}
	if err := plan.LowPortReceipt.Validate(); err != nil {
		return fmt.Errorf("release low-port receipt: %w", err)
	}
	if plan.LowPortReceipt.VerifiedAt.Before(plan.NetworkUpdatedAt) {
		return errors.New("release low-port receipt verification precedes network authority")
	}
	if plan.LowPortReceipt.SourceCheckpointRevision+1 >= plan.CheckpointRevision {
		return errors.New("release low-port receipt does not precede the trust checkpoint")
	}
	if err := plan.ResolverReceipt.Validate(); err != nil {
		return fmt.Errorf("release resolver receipt: %w", err)
	}
	if plan.ResolverReceipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
		return errors.New("release resolver receipt does not precede the trust checkpoint")
	}
	if plan.ResolverReceipt.VerifiedAt.Before(plan.LowPortReceipt.VerifiedAt) {
		return errors.New("release resolver receipt verification precedes low-port receipt")
	}
	if plan.Authority.TrustDisposition != GlobalNetworkReleaseTrustOwned {
		return errors.New("release trust authority preserves a preexisting unowned root")
	}
	if err := plan.Authority.Validate(); err != nil {
		return fmt.Errorf("release authority: %w", err)
	}
	return nil
}
