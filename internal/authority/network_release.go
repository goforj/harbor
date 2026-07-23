package authority

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// networkReleasePlanReader reads one exact durable global release plan.
type networkReleasePlanReader interface {
	// ReadGlobalNetworkReleasePlan returns the plan owned by one daemon operation, if it remains active.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error)
	// ReadGlobalNetworkReleaseTerminal returns one completed release replay fence.
	ReadGlobalNetworkReleaseTerminal(context.Context, domain.OperationID) (state.GlobalNetworkReleaseTerminalRecord, bool, error)
}

// networkReleaseCoordinator starts or resumes one authenticated global release operation.
type networkReleaseCoordinator interface {
	// Start stages or resumes one caller-bound global network release.
	Start(context.Context, reconcile.GlobalNetworkReleaseStartRequest) (state.OperationRecord, error)
	// PrepareLowPorts publishes one bounded low-port release capability.
	PrepareLowPorts(context.Context, reconcile.GlobalNetworkReleasePrepareLowPortsRequest) (ticketissuer.LowPortResult, error)
	// ConfirmLowPorts verifies low-port removal and advances the retained release plan.
	ConfirmLowPorts(context.Context, reconcile.GlobalNetworkReleaseConfirmLowPortsRequest) (state.GlobalNetworkReleasePlanRecord, error)
	// PrepareResolver publishes one bounded resolver release capability.
	PrepareResolver(context.Context, reconcile.GlobalNetworkReleasePrepareResolverRequest) (ticketissuer.ResolverResult, error)
	// ConfirmResolver verifies resolver removal and advances the retained release plan.
	ConfirmResolver(context.Context, reconcile.GlobalNetworkReleaseConfirmResolverRequest) (state.GlobalNetworkReleasePlanRecord, error)
	// PrepareTrust publishes or preserves one bounded trust release capability.
	PrepareTrust(context.Context, reconcile.GlobalNetworkReleasePrepareTrustRequest) (reconcile.GlobalNetworkReleaseTrustPreparation, error)
	// ConfirmTrust verifies trust removal or preservation and advances the retained release plan.
	ConfirmTrust(context.Context, reconcile.GlobalNetworkReleaseConfirmTrustRequest) (state.GlobalNetworkReleasePlanRecord, error)
	// PrepareLoopbacks publishes one bounded loopback-pool release capability.
	PrepareLoopbacks(context.Context, reconcile.GlobalNetworkReleasePrepareLoopbacksRequest) (ticketissuer.PoolResult, error)
	// ConfirmLoopbacks verifies complete loopback-pool removal and advances the retained release plan.
	ConfirmLoopbacks(context.Context, reconcile.GlobalNetworkReleaseConfirmLoopbacksRequest) (state.GlobalNetworkReleasePlanRecord, error)
	// ConfirmOwnership independently verifies ownership absence and returns the completed terminal fence.
	ConfirmOwnership(context.Context, reconcile.GlobalNetworkReleaseConfirmOwnershipRequest) (state.GlobalNetworkReleaseTerminalRecord, error)
}

// NetworkReleaseAuthority adapts the durable global release lifecycle to its optional control surface.
type NetworkReleaseAuthority struct {
	plans          networkReleasePlanReader
	coordinator    networkReleaseCoordinator
	newOperationID func() (domain.OperationID, error)
}

// _ confirms NetworkReleaseAuthority exposes the optional control boundary.
var _ control.NetworkReleaseAuthority = (*NetworkReleaseAuthority)(nil)

// _ confirms NetworkReleaseAuthority exposes the optional low-port approval boundary.
var _ control.NetworkReleaseApprovalAuthority = (*NetworkReleaseAuthority)(nil)

// NewNetworkReleaseAuthority creates an optional global network release authority with all required narrow dependencies.
func NewNetworkReleaseAuthority(plans networkReleasePlanReader, coordinator networkReleaseCoordinator) *NetworkReleaseAuthority {
	return newNetworkReleaseAuthority(plans, coordinator, newOpaqueOperationID)
}

// newNetworkReleaseAuthority keeps daemon-owned operation identity generation deterministic in boundary tests.
func newNetworkReleaseAuthority(plans networkReleasePlanReader, coordinator networkReleaseCoordinator, newOperationID func() (domain.OperationID, error)) *NetworkReleaseAuthority {
	if nilAuthorityDependency(plans) || nilAuthorityDependency(coordinator) || nilAuthorityDependency(newOperationID) {
		panic("authority.newNetworkReleaseAuthority requires every dependency")
	}
	return &NetworkReleaseAuthority{
		plans:          plans,
		coordinator:    coordinator,
		newOperationID: newOperationID,
	}
}

// StartNetworkRelease assigns one daemon-owned operation ID after admitting the client intent and projects its exact durable plan.
func (authority *NetworkReleaseAuthority) StartNetworkRelease(ctx context.Context, caller control.Caller, request control.StartNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.NetworkReleaseOperation{}, fmt.Errorf("generate network release operation identity: %w", err)
	}
	if err := operationID.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, fmt.Errorf("generated network release operation identity is invalid: %w", err)
	}
	coordinatorRequest := reconcile.GlobalNetworkReleaseStartRequest{
		OperationID:       operationID,
		IntentID:          request.IntentID,
		RequesterIdentity: caller.Transport.UserID,
	}
	if err := coordinatorRequest.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, fmt.Errorf("network release coordinator request: %w", err)
	}
	started, err := authority.coordinator.Start(ctx, coordinatorRequest)
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	if err := validateNetworkReleaseStartedOperation(started, request.IntentID); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	result, err := authority.readNetworkRelease(ctx, caller.Transport.UserID, started.Operation.ID)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != started.Operation.ID ||
		result.Operation.IntentID != request.IntentID ||
		result.Revision != started.Revision ||
		!sameNetworkReleaseOperation(result.Operation, started.Operation) {
		return control.NetworkReleaseOperation{}, errors.New("network release result differs from its returned operation or requested intent")
	}
	return result, nil
}

// ReadNetworkRelease returns only the requested daemon-owned global release operation.
func (authority *NetworkReleaseAuthority) ReadNetworkRelease(ctx context.Context, caller control.Caller, request control.ReadNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	return authority.readNetworkRelease(ctx, caller.Transport.UserID, request.OperationID)
}

// PrepareNetworkReleaseApproval binds release-low-ports helper authority to the authenticated transport user.
func (authority *NetworkReleaseAuthority) PrepareNetworkReleaseApproval(ctx context.Context, caller control.Caller, request control.PrepareNetworkReleaseApprovalRequest) (control.NetworkReleaseApprovalPreparation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseApprovalPreparation{}, err
	}
	result, err := authority.coordinator.PrepareLowPorts(ctx, reconcile.GlobalNetworkReleasePrepareLowPortsRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
	})
	if err != nil {
		return control.NetworkReleaseApprovalPreparation{}, classifyNetworkReleaseError(err)
	}
	if err := result.Validate(time.Now().UTC()); err != nil {
		return control.NetworkReleaseApprovalPreparation{}, fmt.Errorf("network release low-port preparation result: %w", err)
	}
	preparation := control.NetworkReleaseApprovalPreparation{
		OperationID:        request.OperationID,
		CheckpointRevision: request.ExpectedCheckpointRevision,
		Ticket: control.NetworkReleaseApprovalTicket{
			OperationID:                result.OperationID,
			Reference:                  result.Reference,
			Operation:                  result.Operation,
			PolicyFingerprint:          result.PolicyFingerprint,
			TargetOwnershipFingerprint: result.OwnershipFingerprint,
			ObservationFingerprint:     result.ObservationFingerprint,
			ExpiresAt:                  result.ExpiresAt,
		},
	}
	if err := preparation.Validate(); err != nil {
		return control.NetworkReleaseApprovalPreparation{}, fmt.Errorf("network release low-port preparation: %w", err)
	}
	if preparation.Ticket.OperationID != request.OperationID {
		return control.NetworkReleaseApprovalPreparation{}, errors.New("network release low-port preparation ticket differs from the requested operation")
	}
	return preparation, nil
}

// ConfirmNetworkReleaseApproval independently verifies low-port removal and advances the release to resolver retirement.
func (authority *NetworkReleaseAuthority) ConfirmNetworkReleaseApproval(ctx context.Context, caller control.Caller, request control.ConfirmNetworkReleaseApprovalRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	plan, err := authority.coordinator.ConfirmLowPorts(ctx, reconcile.GlobalNetworkReleaseConfirmLowPortsRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
		LowPortEvidence:            request.LowPortEvidence,
	})
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	result, err := networkReleaseOperation(plan)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != request.OperationID ||
		result.CheckpointRevision <= request.ExpectedCheckpointRevision ||
		result.Phase != control.NetworkReleasePhaseResolver {
		return control.NetworkReleaseOperation{}, errors.New("network release low-port confirmation did not advance the requested checkpoint to resolver release")
	}
	return result, nil
}

// PrepareNetworkReleaseResolverApproval binds release-resolver helper authority to the authenticated transport user.
func (authority *NetworkReleaseAuthority) PrepareNetworkReleaseResolverApproval(ctx context.Context, caller control.Caller, request control.PrepareNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseResolverApprovalPreparation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseResolverApprovalPreparation{}, err
	}
	result, err := authority.coordinator.PrepareResolver(ctx, reconcile.GlobalNetworkReleasePrepareResolverRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
	})
	disposition := control.NetworkReleaseResolverPublicationDurable
	if errors.Is(err, ticketissuer.ErrResolverPublicationIndeterminate) {
		disposition = control.NetworkReleaseResolverPublicationIndeterminate
	} else if err != nil {
		return control.NetworkReleaseResolverApprovalPreparation{}, classifyNetworkReleaseError(err)
	}
	if err := result.Validate(time.Now().UTC()); err != nil {
		return control.NetworkReleaseResolverApprovalPreparation{}, fmt.Errorf("network release resolver preparation result: %w", err)
	}
	preparation := control.NetworkReleaseResolverApprovalPreparation{
		OperationID:            request.OperationID,
		CheckpointRevision:     request.ExpectedCheckpointRevision,
		PublicationDisposition: disposition,
		Ticket: control.NetworkReleaseResolverApprovalTicket{
			OperationID:                result.OperationID,
			Reference:                  result.Reference,
			Operation:                  result.Operation,
			PolicyFingerprint:          result.PolicyFingerprint,
			TargetOwnershipFingerprint: result.OwnershipFingerprint,
			ExpiresAt:                  result.ExpiresAt,
		},
	}
	if err := preparation.Validate(); err != nil {
		return control.NetworkReleaseResolverApprovalPreparation{}, fmt.Errorf("network release resolver preparation: %w", err)
	}
	if preparation.Ticket.OperationID != request.OperationID {
		return control.NetworkReleaseResolverApprovalPreparation{}, errors.New("network release resolver preparation ticket differs from the requested operation")
	}
	return preparation, nil
}

// ConfirmNetworkReleaseResolverApproval independently verifies resolver removal and advances the release to trust retirement.
func (authority *NetworkReleaseAuthority) ConfirmNetworkReleaseResolverApproval(ctx context.Context, caller control.Caller, request control.ConfirmNetworkReleaseResolverApprovalRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	plan, err := authority.coordinator.ConfirmResolver(ctx, reconcile.GlobalNetworkReleaseConfirmResolverRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
		ResolverEvidence:           request.ResolverEvidence,
	})
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	result, err := networkReleaseOperation(plan)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != request.OperationID ||
		result.CheckpointRevision <= request.ExpectedCheckpointRevision ||
		result.Phase != control.NetworkReleasePhaseTrust {
		return control.NetworkReleaseOperation{}, errors.New("network release resolver confirmation did not advance the requested checkpoint to trust release")
	}
	return result, nil
}

// PrepareNetworkReleaseTrustApproval binds release-trust helper authority to the authenticated transport user.
func (authority *NetworkReleaseAuthority) PrepareNetworkReleaseTrustApproval(ctx context.Context, caller control.Caller, request control.PrepareNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseTrustApprovalPreparation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseTrustApprovalPreparation{}, err
	}
	result, err := authority.coordinator.PrepareTrust(ctx, reconcile.GlobalNetworkReleasePrepareTrustRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
	})
	publicationDisposition := control.NetworkReleaseTrustPublicationDurable
	if errors.Is(err, ticketissuer.ErrTrustPublicationIndeterminate) {
		publicationDisposition = control.NetworkReleaseTrustPublicationIndeterminate
	} else if err != nil {
		return control.NetworkReleaseTrustApprovalPreparation{}, classifyNetworkReleaseError(err)
	}
	if err := result.Validate(time.Now().UTC()); err != nil {
		return control.NetworkReleaseTrustApprovalPreparation{}, fmt.Errorf("network release trust preparation result: %w", err)
	}
	preparation, err := networkReleaseTrustApprovalPreparation(request, result, publicationDisposition)
	if err != nil {
		return control.NetworkReleaseTrustApprovalPreparation{}, err
	}
	return preparation, nil
}

// ConfirmNetworkReleaseTrustApproval independently verifies trust removal or preservation and advances the release to loopback retirement.
func (authority *NetworkReleaseAuthority) ConfirmNetworkReleaseTrustApproval(ctx context.Context, caller control.Caller, request control.ConfirmNetworkReleaseTrustApprovalRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	evidence := helper.TrustMutationEvidence{}
	if request.TrustEvidence != nil {
		evidence = *request.TrustEvidence
	}
	plan, err := authority.coordinator.ConfirmTrust(ctx, reconcile.GlobalNetworkReleaseConfirmTrustRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
		TrustEvidence:              evidence,
	})
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	result, err := networkReleaseOperation(plan)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != request.OperationID ||
		result.CheckpointRevision <= request.ExpectedCheckpointRevision ||
		result.Phase != control.NetworkReleasePhaseLoopbacks {
		return control.NetworkReleaseOperation{}, errors.New("network release trust confirmation did not advance the requested checkpoint to loopback release")
	}
	return result, nil
}

// PrepareNetworkReleaseLoopbackApproval binds release-loopback-pool helper authority to the authenticated transport user.
func (authority *NetworkReleaseAuthority) PrepareNetworkReleaseLoopbackApproval(ctx context.Context, caller control.Caller, request control.PrepareNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseLoopbackApprovalPreparation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseLoopbackApprovalPreparation{}, err
	}
	result, err := authority.coordinator.PrepareLoopbacks(ctx, reconcile.GlobalNetworkReleasePrepareLoopbacksRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
	})
	publicationDisposition := control.NetworkReleaseLoopbackPublicationDurable
	if errors.Is(err, ticketissuer.ErrPoolPublicationIndeterminate) {
		publicationDisposition = control.NetworkReleaseLoopbackPublicationIndeterminate
	} else if err != nil {
		return control.NetworkReleaseLoopbackApprovalPreparation{}, classifyNetworkReleaseError(err)
	}
	if err := result.Validate(time.Now().UTC()); err != nil {
		resultErr := fmt.Errorf("network release loopback preparation result: %w", err)
		if publicationDisposition == control.NetworkReleaseLoopbackPublicationIndeterminate {
			return control.NetworkReleaseLoopbackApprovalPreparation{}, classifyNetworkReleaseError(
				errors.Join(ticketissuer.ErrPoolPublicationIndeterminate, resultErr),
			)
		}
		return control.NetworkReleaseLoopbackApprovalPreparation{}, resultErr
	}
	preparation := control.NetworkReleaseLoopbackApprovalPreparation{
		OperationID:            request.OperationID,
		CheckpointRevision:     request.ExpectedCheckpointRevision,
		PublicationDisposition: publicationDisposition,
		Ticket: control.NetworkReleaseLoopbackApprovalTicket{
			OperationID: result.OperationID,
			Reference:   result.Reference,
			Operation:   result.Operation,
			Pool:        result.Pool.String(),
			ExpiresAt:   result.ExpiresAt,
		},
	}
	if err := preparation.Validate(); err != nil {
		preparationErr := fmt.Errorf("network release loopback preparation: %w", err)
		if publicationDisposition == control.NetworkReleaseLoopbackPublicationIndeterminate {
			return control.NetworkReleaseLoopbackApprovalPreparation{}, classifyNetworkReleaseError(
				errors.Join(ticketissuer.ErrPoolPublicationIndeterminate, preparationErr),
			)
		}
		return control.NetworkReleaseLoopbackApprovalPreparation{}, preparationErr
	}
	if preparation.OperationID != request.OperationID ||
		preparation.CheckpointRevision != request.ExpectedCheckpointRevision ||
		preparation.Ticket.OperationID != request.OperationID {
		correlationErr := errors.New("network release loopback preparation differs from the requested checkpoint")
		if publicationDisposition == control.NetworkReleaseLoopbackPublicationIndeterminate {
			return control.NetworkReleaseLoopbackApprovalPreparation{}, classifyNetworkReleaseError(
				errors.Join(ticketissuer.ErrPoolPublicationIndeterminate, correlationErr),
			)
		}
		return control.NetworkReleaseLoopbackApprovalPreparation{}, correlationErr
	}
	return preparation, nil
}

// ConfirmNetworkReleaseLoopbackApproval independently verifies complete loopback-pool removal,
// verifies its effects, and advances the release to ownership.
func (authority *NetworkReleaseAuthority) ConfirmNetworkReleaseLoopbackApproval(ctx context.Context, caller control.Caller, request control.ConfirmNetworkReleaseLoopbackApprovalRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	plan, err := authority.coordinator.ConfirmLoopbacks(ctx, reconcile.GlobalNetworkReleaseConfirmLoopbacksRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
		LoopbackEvidence:           request.LoopbackEvidence,
	})
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	result, err := networkReleaseOperation(plan)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != request.OperationID ||
		result.CheckpointRevision <= request.ExpectedCheckpointRevision ||
		result.Phase != control.NetworkReleasePhaseOwnership {
		return control.NetworkReleaseOperation{}, errors.New(
			"network release loopback confirmation did not advance the requested checkpoint through effect verification to ownership",
		)
	}
	return result, nil
}

// ConfirmNetworkReleaseOwnership binds independent ownership absence confirmation to the authenticated transport user.
func (authority *NetworkReleaseAuthority) ConfirmNetworkReleaseOwnership(ctx context.Context, caller control.Caller, request control.ConfirmNetworkReleaseOwnershipRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	terminal, err := authority.coordinator.ConfirmOwnership(ctx, reconcile.GlobalNetworkReleaseConfirmOwnershipRequest{
		OperationID:                request.OperationID,
		ExpectedCheckpointRevision: request.ExpectedCheckpointRevision,
		RequesterIdentity:          caller.Transport.UserID,
	})
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	result, err := networkReleaseTerminalOperation(terminal)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != request.OperationID ||
		result.Operation.State != domain.OperationSucceeded ||
		result.Phase != control.NetworkReleasePhaseProjection ||
		result.CheckpointRevision != request.ExpectedCheckpointRevision {
		return control.NetworkReleaseOperation{}, errors.New("network release ownership confirmation did not complete the requested terminal release")
	}
	return result, nil
}

// sameNetworkReleaseOperation compares operation facts by timestamp value so durable time locations do not affect replay correlation.
func sameNetworkReleaseOperation(left domain.Operation, right domain.Operation) bool {
	return left.ID == right.ID &&
		left.IntentID == right.IntentID &&
		left.Kind == right.Kind &&
		left.ProjectID == right.ProjectID &&
		left.State == right.State &&
		left.Phase == right.Phase &&
		left.RequestedAt.Equal(right.RequestedAt) &&
		sameNetworkReleaseTime(left.StartedAt, right.StartedAt) &&
		sameNetworkReleaseTime(left.FinishedAt, right.FinishedAt) &&
		sameNetworkReleaseProblem(left.Problem, right.Problem)
}

// sameNetworkReleaseTime compares optional operation timestamps by instant.
func sameNetworkReleaseTime(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

// sameNetworkReleaseProblem compares optional immutable operation problems by value.
func sameNetworkReleaseProblem(left *domain.Problem, right *domain.Problem) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// networkReleaseTrustApprovalPreparation projects trusted issuer metadata while preserving ownership and publication invariants.
func networkReleaseTrustApprovalPreparation(request control.PrepareNetworkReleaseTrustApprovalRequest, result reconcile.GlobalNetworkReleaseTrustPreparation, publicationDisposition control.NetworkReleaseTrustPublicationDisposition) (control.NetworkReleaseTrustApprovalPreparation, error) {
	preparation := control.NetworkReleaseTrustApprovalPreparation{
		OperationID:            request.OperationID,
		CheckpointRevision:     request.ExpectedCheckpointRevision,
		PublicationDisposition: publicationDisposition,
	}
	switch result.Disposition {
	case state.GlobalNetworkReleaseTrustOwned:
		if result.Ticket == nil {
			return control.NetworkReleaseTrustApprovalPreparation{}, errors.New("owned network release trust preparation has no ticket")
		}
		preparation.Disposition = control.NetworkReleaseTrustOwned
		preparation.Ticket = &control.NetworkReleaseTrustApprovalTicket{
			OperationID:                result.Ticket.OperationID,
			Reference:                  result.Ticket.Reference,
			Operation:                  result.Ticket.Operation,
			PolicyFingerprint:          result.Ticket.PolicyFingerprint,
			TargetOwnershipFingerprint: result.Ticket.OwnershipFingerprint,
			AuthorityFingerprint:       result.Ticket.AuthorityFingerprint,
			Mechanism:                  result.Ticket.Mechanism,
			ExpiresAt:                  result.Ticket.ExpiresAt,
		}
	case state.GlobalNetworkReleaseTrustPreexistingUnowned:
		if publicationDisposition != control.NetworkReleaseTrustPublicationDurable {
			return control.NetworkReleaseTrustApprovalPreparation{}, errors.New("preexisting network release trust preparation cannot have indeterminate publication")
		}
		preparation.Disposition = control.NetworkReleaseTrustPreexistingUnowned
		preparation.PublicationDisposition = control.NetworkReleaseTrustPublicationNotRequired
	default:
		return control.NetworkReleaseTrustApprovalPreparation{}, fmt.Errorf("network release trust disposition %q is unsupported", result.Disposition)
	}
	if err := preparation.Validate(); err != nil {
		return control.NetworkReleaseTrustApprovalPreparation{}, fmt.Errorf("network release trust preparation: %w", err)
	}
	if preparation.OperationID != request.OperationID || preparation.CheckpointRevision != request.ExpectedCheckpointRevision {
		return control.NetworkReleaseTrustApprovalPreparation{}, errors.New("network release trust preparation differs from the requested checkpoint")
	}
	return preparation, nil
}

// readNetworkRelease reads and projects the exact durable plan without returning retained host authority.
func (authority *NetworkReleaseAuthority) readNetworkRelease(ctx context.Context, requesterIdentity string, operationID domain.OperationID) (control.NetworkReleaseOperation, error) {
	plan, found, err := authority.plans.ReadGlobalNetworkReleasePlan(ctx, operationID)
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	if found {
		if plan.Authority.Projection.ConfirmedOwnership.Record.OwnerIdentity != requesterIdentity {
			return control.NetworkReleaseOperation{}, control.NewNetworkReleaseNotFoundError(errors.New("network release operation was not found"))
		}
		result, projectionErr := networkReleaseOperation(plan)
		if projectionErr != nil {
			return control.NetworkReleaseOperation{}, projectionErr
		}
		if result.Operation.ID != operationID {
			return control.NetworkReleaseOperation{}, errors.New("network release plan differs from its requested operation")
		}
		return result, nil
	}
	terminal, terminalFound, terminalErr := authority.plans.ReadGlobalNetworkReleaseTerminal(ctx, operationID)
	if terminalErr != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(terminalErr)
	}
	if !terminalFound || terminal.OwnerIdentity != requesterIdentity {
		return control.NetworkReleaseOperation{}, control.NewNetworkReleaseNotFoundError(errors.New("network release operation was not found"))
	}
	result, err := networkReleaseTerminalOperation(terminal)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != operationID {
		return control.NetworkReleaseOperation{}, errors.New("network release plan differs from its requested operation")
	}
	return result, nil
}

// networkReleaseTerminalOperation projects only the compact replay fence retained after the active plan is deleted.
func networkReleaseTerminalOperation(terminal state.GlobalNetworkReleaseTerminalRecord) (control.NetworkReleaseOperation, error) {
	result := control.NetworkReleaseOperation{
		Operation:          terminal.Operation.Operation,
		Revision:           terminal.Operation.Revision,
		Phase:              control.NetworkReleasePhaseProjection,
		CheckpointRevision: terminal.SourceCheckpointRevision,
		NetworkRevision:    terminal.NetworkRevision,
	}
	if err := result.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, fmt.Errorf("network release terminal: %w", err)
	}
	return result, nil
}

// validateNetworkReleaseStartedOperation rejects coordinator responses that cross the requested intent before their durable plan is read.
func validateNetworkReleaseStartedOperation(record state.OperationRecord, intentID domain.IntentID) error {
	if err := record.Operation.Validate(); err != nil {
		return fmt.Errorf("network release started operation: %w", err)
	}
	if record.Revision == 0 || record.Operation.Kind != domain.OperationKindNetworkRelease || record.Operation.ProjectID != "" || record.Operation.IntentID != intentID {
		return errors.New("network release started operation differs from its requested intent")
	}
	return nil
}

// networkReleaseOperation projects only client-safe progress fields from one exact durable release plan.
func networkReleaseOperation(plan state.GlobalNetworkReleasePlanRecord) (control.NetworkReleaseOperation, error) {
	phase, err := networkReleasePhase(plan.Phase)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	result := control.NetworkReleaseOperation{
		Operation:          plan.Operation.Operation,
		Revision:           plan.Operation.Revision,
		Phase:              phase,
		CheckpointRevision: plan.CheckpointRevision,
		NetworkRevision:    plan.NetworkRevision,
	}
	if err := result.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, fmt.Errorf("network release plan: %w", err)
	}
	return result, nil
}

// networkReleasePhase maps only the fixed durable release phases into their public typed equivalents.
func networkReleasePhase(phase state.GlobalNetworkReleasePlanPhase) (control.NetworkReleasePhase, error) {
	switch phase {
	case state.GlobalNetworkReleasePlanPhaseRuntimeRelease:
		return control.NetworkReleasePhaseRuntimeRelease, nil
	case state.GlobalNetworkReleasePlanPhaseLowPorts:
		return control.NetworkReleasePhaseLowPorts, nil
	case state.GlobalNetworkReleasePlanPhaseResolver:
		return control.NetworkReleasePhaseResolver, nil
	case state.GlobalNetworkReleasePlanPhaseTrust:
		return control.NetworkReleasePhaseTrust, nil
	case state.GlobalNetworkReleasePlanPhaseLoopbacks:
		return control.NetworkReleasePhaseLoopbacks, nil
	case state.GlobalNetworkReleasePlanPhaseVerifyEffects:
		return control.NetworkReleasePhaseVerifyEffects, nil
	case state.GlobalNetworkReleasePlanPhaseOwnership:
		return control.NetworkReleasePhaseOwnership, nil
	case state.GlobalNetworkReleasePlanPhaseProjection:
		return control.NetworkReleasePhaseProjection, nil
	default:
		return "", fmt.Errorf("network release plan phase %q is unsupported", phase)
	}
}

// classifyNetworkReleaseError maps reviewed durable absence and contention into stable control categories.
func classifyNetworkReleaseError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var operationMissing *state.OperationNotFoundError
	var intentMissing *state.OperationIntentNotFoundError
	if errors.As(err, &operationMissing) || errors.As(err, &intentMissing) {
		return control.NewNetworkReleaseNotFoundError(err)
	}
	var intentConflict *state.IntentConflictError
	var operationConflict *state.OperationIDConflictError
	var active *state.GlobalNetworkReleaseActiveError
	var stale *state.StaleRevisionError
	if errors.As(err, &intentConflict) ||
		errors.As(err, &operationConflict) ||
		errors.As(err, &active) ||
		errors.As(err, &stale) ||
		errors.Is(err, ticketissuer.ErrResolverPublicationIndeterminate) ||
		errors.Is(err, ticketissuer.ErrTrustPublicationIndeterminate) ||
		errors.Is(err, ticketissuer.ErrPoolPublicationIndeterminate) {
		return control.NewNetworkReleaseConflictError(err)
	}
	return err
}
