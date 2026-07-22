package state

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/platform/lowport"
)

// GlobalNetworkReleaseLowPortPlanReader reads one validated durable global release plan.
type GlobalNetworkReleaseLowPortPlanReader interface {
	// ReadGlobalNetworkReleasePlan returns the plan for the selected operation while it remains active.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error)
}

// GlobalNetworkReleaseLowPortPlanSource resolves release-only low-port capability authority from the staged global release plan.
type GlobalNetworkReleaseLowPortPlanSource struct {
	plans GlobalNetworkReleaseLowPortPlanReader
}

// GlobalNetworkReleaseLowPortPlanSource must retain the ticket issuer's narrow read contract.
var _ ticketissuer.LowPortPlanSource = (*GlobalNetworkReleaseLowPortPlanSource)(nil)

// NewGlobalNetworkReleaseLowPortPlanSource creates a source fenced by validated durable global release authority.
func NewGlobalNetworkReleaseLowPortPlanSource(plans GlobalNetworkReleaseLowPortPlanReader) *GlobalNetworkReleaseLowPortPlanSource {
	if plans == nil {
		panic("state.NewGlobalNetworkReleaseLowPortPlanSource requires a global network release plan reader")
	}
	return &GlobalNetworkReleaseLowPortPlanSource{
		plans: plans,
	}
}

// Resolve returns only the release-low-ports capability approved at the current low-port checkpoint.
func (source *GlobalNetworkReleaseLowPortPlanSource) Resolve(ctx context.Context, request ticketissuer.LowPortRequest) (ticketissuer.LowPortPlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("resolve global network release low-port plan: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.LowPortPlan{}, err
	}
	plan, found, err := source.plans.ReadGlobalNetworkReleasePlan(ctx, request.OperationID)
	if err != nil {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("resolve global network release low-port plan %q: %w", request.OperationID, err)
	}
	if !found {
		return ticketissuer.LowPortPlan{}, errors.New("resolve global network release low-port plan: active release authority is absent")
	}
	if plan.Operation.Operation.ID != request.OperationID {
		return ticketissuer.LowPortPlan{}, errors.New("resolve global network release low-port plan: durable operation does not match requested operation")
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseLowPorts {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("resolve global network release low-port plan: durable phase is %q, want %q", plan.Phase, GlobalNetworkReleasePlanPhaseLowPorts)
	}
	native, err := lowport.NewRequest(plan.Authority.Projection.ConfirmedOwnership.Record, plan.Authority.Policy)
	if err != nil {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("resolve global network release low-port plan: derive native request: %w", err)
	}
	result := ticketissuer.LowPortPlan{
		Purpose:            ticketissuer.LowPortPlanPurposeGlobalNetworkRelease,
		Operation:          plan.Operation.Operation,
		OperationRevision:  plan.Operation.Revision,
		CheckpointRevision: plan.CheckpointRevision,
		CheckpointPhase:    ticketissuer.LowPortCheckpointPhaseGlobalRelease,
		Mutation:           helper.OperationReleaseLowPorts,
		TargetOwnership:    plan.Authority.Projection.ConfirmedOwnership.Record,
		Policy:             plan.Authority.Policy,
		NativeRequest:      native,
	}
	if err := result.Validate(); err != nil {
		return ticketissuer.LowPortPlan{}, fmt.Errorf("resolve global network release low-port plan: invalid durable authority: %w", err)
	}
	return result, nil
}
