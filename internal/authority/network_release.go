package authority

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// networkReleasePlanReader reads one exact durable global release plan.
type networkReleasePlanReader interface {
	// ReadGlobalNetworkReleasePlan returns the plan owned by one daemon operation, if it remains active.
	ReadGlobalNetworkReleasePlan(context.Context, domain.OperationID) (state.GlobalNetworkReleasePlanRecord, bool, error)
}

// networkReleaseCoordinator starts or resumes one authenticated global release operation.
type networkReleaseCoordinator interface {
	// Start stages or resumes one caller-bound global network release.
	Start(context.Context, reconcile.GlobalNetworkReleaseStartRequest) (state.OperationRecord, error)
}

// NetworkReleaseAuthority adapts the durable global release lifecycle to its optional control surface.
type NetworkReleaseAuthority struct {
	plans          networkReleasePlanReader
	coordinator    networkReleaseCoordinator
	newOperationID func() (domain.OperationID, error)
}

// _ confirms NetworkReleaseAuthority exposes the optional control boundary.
var _ control.NetworkReleaseAuthority = (*NetworkReleaseAuthority)(nil)

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
	result, err := authority.readNetworkRelease(ctx, started.Operation.ID)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != started.Operation.ID ||
		result.Operation.IntentID != request.IntentID ||
		result.Revision != started.Revision ||
		!reflect.DeepEqual(result.Operation, started.Operation) {
		return control.NetworkReleaseOperation{}, errors.New("network release result differs from its returned operation or requested intent")
	}
	return result, nil
}

// ReadNetworkRelease returns only the requested daemon-owned global release operation.
func (authority *NetworkReleaseAuthority) ReadNetworkRelease(ctx context.Context, _ control.Caller, request control.ReadNetworkReleaseRequest) (control.NetworkReleaseOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	return authority.readNetworkRelease(ctx, request.OperationID)
}

// readNetworkRelease reads and projects the exact durable plan without returning retained host authority.
func (authority *NetworkReleaseAuthority) readNetworkRelease(ctx context.Context, operationID domain.OperationID) (control.NetworkReleaseOperation, error) {
	plan, found, err := authority.plans.ReadGlobalNetworkReleasePlan(ctx, operationID)
	if err != nil {
		return control.NetworkReleaseOperation{}, classifyNetworkReleaseError(err)
	}
	if !found {
		return control.NetworkReleaseOperation{}, control.NewNetworkReleaseNotFoundError(errors.New("network release operation was not found"))
	}
	result, err := networkReleaseOperation(plan)
	if err != nil {
		return control.NetworkReleaseOperation{}, err
	}
	if result.Operation.ID != operationID {
		return control.NetworkReleaseOperation{}, errors.New("network release plan differs from its requested operation")
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
	if errors.As(err, &intentConflict) || errors.As(err, &operationConflict) || errors.As(err, &active) || errors.As(err, &stale) {
		return control.NewNetworkReleaseConflictError(err)
	}
	return err
}
