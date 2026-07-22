package state

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/network/identity"
)

// GlobalNetworkReleaseLoopbackPlanReader reads one validated durable global release plan.
type GlobalNetworkReleaseLoopbackPlanReader interface {
	// ReadGlobalNetworkReleasePlan returns the plan for the selected operation while it remains active.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (GlobalNetworkReleasePlanRecord, bool, error)
}

// GlobalNetworkReleaseLoopbackPlanSource resolves release-only loopback-pool capability authority from the staged global release plan.
type GlobalNetworkReleaseLoopbackPlanSource struct {
	plans GlobalNetworkReleaseLoopbackPlanReader
}

// GlobalNetworkReleaseLoopbackPlanSource must retain the ticket issuer's narrow read contract.
var _ ticketissuer.PoolReleasePlanSource = (*GlobalNetworkReleaseLoopbackPlanSource)(nil)

// NewGlobalNetworkReleaseLoopbackPlanSource creates a source fenced by validated durable global release authority.
func NewGlobalNetworkReleaseLoopbackPlanSource(
	plans GlobalNetworkReleaseLoopbackPlanReader,
) *GlobalNetworkReleaseLoopbackPlanSource {
	if plans == nil {
		panic("state.NewGlobalNetworkReleaseLoopbackPlanSource requires a global network release plan reader")
	}
	return &GlobalNetworkReleaseLoopbackPlanSource{
		plans: plans,
	}
}

// Resolve returns only the release-loopback-pool capability approved at the current loopback checkpoint.
func (source *GlobalNetworkReleaseLoopbackPlanSource) Resolve(
	ctx context.Context,
	request ticketissuer.PoolReleaseRequest,
) (ticketissuer.PoolReleasePlan, error) {
	if err := request.Validate(); err != nil {
		return ticketissuer.PoolReleasePlan{}, fmt.Errorf("resolve global network release loopback plan: %w", err)
	}
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return ticketissuer.PoolReleasePlan{}, err
	}
	plan, found, err := source.plans.ReadGlobalNetworkReleasePlan(ctx, request.OperationID)
	if err != nil {
		return ticketissuer.PoolReleasePlan{}, fmt.Errorf(
			"resolve global network release loopback plan %q: %w",
			request.OperationID,
			err,
		)
	}
	if !found {
		return ticketissuer.PoolReleasePlan{}, errors.New(
			"resolve global network release loopback plan: active release authority is absent",
		)
	}
	if plan.Operation.Operation.ID != request.OperationID {
		return ticketissuer.PoolReleasePlan{}, errors.New(
			"resolve global network release loopback plan: durable operation does not match requested operation",
		)
	}
	pool, targets, err := validateGlobalNetworkReleaseLoopbackPlanRecord(plan)
	if err != nil {
		return ticketissuer.PoolReleasePlan{}, fmt.Errorf(
			"resolve global network release loopback plan: invalid durable authority: %w",
			err,
		)
	}
	result := ticketissuer.PoolReleasePlan{
		Operation:          plan.Operation.Operation,
		OperationRevision:  plan.Operation.Revision,
		CheckpointRevision: plan.CheckpointRevision,
		TargetOwnership:    plan.Authority.Projection.ConfirmedOwnership.Record,
		Pool:               pool,
		Targets:            slices.Clone(targets),
	}
	if err := result.Validate(); err != nil {
		return ticketissuer.PoolReleasePlan{}, fmt.Errorf(
			"resolve global network release loopback plan: invalid durable authority: %w",
			err,
		)
	}
	return result, nil
}

// validateGlobalNetworkReleaseLoopbackPlanRecord preserves the complete ordered release boundary when a narrow reader is substituted.
func validateGlobalNetworkReleaseLoopbackPlanRecord(
	plan GlobalNetworkReleasePlanRecord,
) (identity.Pool, []ticketissuer.PoolReleaseTarget, error) {
	operation := plan.Operation.Operation
	if err := operation.Validate(); err != nil {
		return identity.Pool{}, nil, fmt.Errorf("release operation: %w", err)
	}
	if operation.Kind != domain.OperationKindNetworkRelease {
		return identity.Pool{}, nil, fmt.Errorf(
			"release operation kind is %q, want %q",
			operation.Kind,
			domain.OperationKindNetworkRelease,
		)
	}
	if operation.ProjectID != "" {
		return identity.Pool{}, nil, errors.New("release operation is not global")
	}
	if operation.State != domain.OperationRunning || operation.Phase != globalNetworkReleaseRuntimeOperationPhase {
		return identity.Pool{}, nil, errors.New("release operation is not active global release authority")
	}
	if plan.Operation.Revision == 0 || plan.Operation.Revision > domain.MaximumSequence {
		return identity.Pool{}, nil, errors.New("release operation revision is outside the durable sequence range")
	}
	if plan.Phase != GlobalNetworkReleasePlanPhaseLoopbacks {
		return identity.Pool{}, nil, fmt.Errorf(
			"durable phase is %q, want %q",
			plan.Phase,
			GlobalNetworkReleasePlanPhaseLoopbacks,
		)
	}
	if plan.CheckpointRevision == 0 || plan.CheckpointRevision > domain.MaximumSequence {
		return identity.Pool{}, nil, errors.New("release checkpoint revision is outside the durable sequence range")
	}
	if err := plan.Authority.Validate(); err != nil {
		return identity.Pool{}, nil, fmt.Errorf("release authority: %w", err)
	}
	pool, err := networkSetupIdentityPool(plan.Authority.Projection.ConfirmedOwnership.Record.LoopbackPoolPrefix)
	if err != nil {
		return identity.Pool{}, nil, fmt.Errorf("release loopback pool: %w", err)
	}
	targets := make([]ticketissuer.PoolReleaseTarget, len(plan.Authority.LoopbackTargets))
	for index, target := range plan.Authority.LoopbackTargets {
		targets[index] = ticketissuer.PoolReleaseTarget{
			Address:                target.Address,
			ObservationFingerprint: target.ObservationFingerprint,
		}
	}
	if len(targets) != 8 {
		return identity.Pool{}, nil, errors.New("release loopback targets are not the exact retained ordered pool")
	}
	for index, candidate := range pool.Candidates() {
		if targets[index].Address != candidate {
			return identity.Pool{}, nil, errors.New("release loopback targets are not the exact retained ordered pool")
		}
	}
	if err := validateGlobalNetworkReleaseLoopbackPredecessors(plan); err != nil {
		return identity.Pool{}, nil, err
	}
	return pool, targets, nil
}

// validateGlobalNetworkReleaseLoopbackPredecessors requires every earlier destructive release to have completed in durable order.
func validateGlobalNetworkReleaseLoopbackPredecessors(plan GlobalNetworkReleasePlanRecord) error {
	if plan.LowPortReceipt == nil {
		return errors.New("release loopback phase has no low-port receipt")
	}
	if err := plan.LowPortReceipt.Validate(); err != nil {
		return fmt.Errorf("release low-port receipt: %w", err)
	}
	if plan.LowPortReceipt.VerifiedAt.Before(plan.NetworkUpdatedAt) {
		return errors.New("release low-port receipt verification precedes network authority")
	}
	if plan.ResolverReceipt == nil {
		return errors.New("release loopback phase has no resolver receipt")
	}
	if err := plan.ResolverReceipt.Validate(); err != nil {
		return fmt.Errorf("release resolver receipt: %w", err)
	}
	if plan.ResolverReceipt.SourceCheckpointRevision <= plan.LowPortReceipt.SourceCheckpointRevision ||
		plan.ResolverReceipt.VerifiedAt.Before(plan.LowPortReceipt.VerifiedAt) {
		return errors.New("release resolver receipt does not follow low-port receipt")
	}
	if plan.TrustReceipt == nil {
		return errors.New("release loopback phase has no trust receipt")
	}
	if err := plan.TrustReceipt.Validate(); err != nil {
		return fmt.Errorf("release trust receipt: %w", err)
	}
	if plan.TrustReceipt.SourceCheckpointRevision+1 != plan.CheckpointRevision {
		return errors.New("release trust receipt does not precede the loopback checkpoint")
	}
	if plan.TrustReceipt.SourceCheckpointRevision <= plan.ResolverReceipt.SourceCheckpointRevision ||
		plan.TrustReceipt.VerifiedAt.Before(plan.ResolverReceipt.VerifiedAt) {
		return errors.New("release trust receipt does not follow resolver receipt")
	}
	if plan.TrustReceipt.Disposition != plan.Authority.TrustDisposition {
		return errors.New("release trust receipt disposition does not match retained authority")
	}
	return nil
}
