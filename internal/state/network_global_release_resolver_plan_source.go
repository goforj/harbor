package state

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
)

// GlobalNetworkReleaseResolverPlanReader reads one validated durable global release plan.
type GlobalNetworkReleaseResolverPlanReader interface {
	// ReadGlobalNetworkReleasePlan returns the plan for the selected operation while it remains active.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error)
}

// GlobalNetworkReleaseResolverPlanSource resolves release-only resolver capability authority from the staged global release plan.
type GlobalNetworkReleaseResolverPlanSource struct {
	plans GlobalNetworkReleaseResolverPlanReader
}

// GlobalNetworkReleaseResolverPlanSource must retain the ticket issuer's narrow read contract.
var _ ticketissuer.ResolverPlanSource = (*GlobalNetworkReleaseResolverPlanSource)(nil)

// NewGlobalNetworkReleaseResolverPlanSource creates a source fenced by validated durable global release authority.
func NewGlobalNetworkReleaseResolverPlanSource(plans GlobalNetworkReleaseResolverPlanReader) *GlobalNetworkReleaseResolverPlanSource {
	if plans == nil {
		panic("state.NewGlobalNetworkReleaseResolverPlanSource requires a global network release plan reader")
	}
	return &GlobalNetworkReleaseResolverPlanSource{
		plans: plans,
	}
}

// Resolve returns only the release-resolver capability approved at the current resolver checkpoint.
func (source *GlobalNetworkReleaseResolverPlanSource) Resolve(ctx context.Context, request ticketissuer.ResolverRequest) (ticketissuer.ResolverPlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve global network release resolver plan: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.ResolverPlan{}, err
	}
	plan, found, err := source.plans.ReadGlobalNetworkReleasePlan(ctx, request.OperationID)
	if err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve global network release resolver plan %q: %w", request.OperationID, err)
	}
	if !found {
		return ticketissuer.ResolverPlan{}, errors.New("resolve global network release resolver plan: active release authority is absent")
	}
	if plan.Operation.Operation.ID != request.OperationID {
		return ticketissuer.ResolverPlan{}, errors.New("resolve global network release resolver plan: durable operation does not match requested operation")
	}
	if err := validateGlobalNetworkReleaseResolverPlanRecord(plan); err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve global network release resolver plan: invalid durable authority: %w", err)
	}
	target := plan.Authority.Projection.ConfirmedOwnership.Record
	fingerprint, err := target.Fingerprint()
	if err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve global network release resolver plan: fingerprint target ownership: %w", err)
	}
	result := ticketissuer.ResolverPlan{
		Purpose:                            ticketissuer.ResolverPlanPurposeGlobalRelease,
		Operation:                          plan.Operation.Operation,
		OperationRevision:                  plan.Operation.Revision,
		CheckpointRevision:                 plan.CheckpointRevision,
		CheckpointPhase:                    ticketissuer.ResolverCheckpointPhaseGlobalRelease,
		Mutation:                           helper.OperationReleaseResolver,
		ExpectedSourceOwnershipFingerprint: fingerprint,
		TargetOwnership:                    target,
		Policy:                             plan.Authority.Policy,
	}
	if err := result.Validate(); err != nil {
		return ticketissuer.ResolverPlan{}, fmt.Errorf("resolve global network release resolver plan: invalid durable authority: %w", err)
	}
	return result, nil
}

// validateGlobalNetworkReleaseResolverPlanRecord preserves the active global-release boundary when a narrow reader is substituted.
func validateGlobalNetworkReleaseResolverPlanRecord(plan GlobalNetworkReleasePlanRecord) error {
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
	if operation.State != domain.OperationRunning {
		return fmt.Errorf("release operation state is %q, want %q", operation.State, domain.OperationRunning)
	}
	if operation.Phase != globalNetworkReleaseRuntimeOperationPhase {
		return fmt.Errorf("release operation phase is %q, want %q", operation.Phase, globalNetworkReleaseRuntimeOperationPhase)
	}
	if plan.Operation.Revision == 0 || plan.Operation.Revision > domain.MaximumSequence {
		return errors.New("release operation revision is outside the durable sequence range")
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseResolver {
		return fmt.Errorf("durable phase is %q, want %q", plan.Phase, GlobalNetworkReleasePlanPhaseResolver)
	}
	if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
		return errors.New("release checkpoint revision is outside the durable sequence range")
	}
	if err := plan.Authority.Validate(); err != nil {
		return fmt.Errorf("release authority: %w", err)
	}
	return nil
}
