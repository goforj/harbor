package lowporthandler

import (
	"context"
	"errors"
	"fmt"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/lowport"
)

// conditionalAdapter is the only observation-bound low-port surface available to the privileged handler.
type conditionalAdapter interface {
	EnsureIfObserved(context.Context, lowport.Request, string) (lowport.Change, error)
	ReleaseIfObserved(context.Context, lowport.Request, string) (lowport.Change, error)
}

// Handler turns one admitted schema-2 policy ticket into an exact low-port service effect.
type Handler struct {
	adapter conditionalAdapter
}

// New creates a handler around an injected reviewed low-port adapter.
func New(adapter *lowport.Adapter) *Handler {
	if adapter == nil {
		panic("lowporthandler.New requires a non-nil low-port adapter")
	}
	return newHandler(adapter)
}

// newHandler keeps native effects replaceable in tests without expanding the production constructor.
func newHandler(adapter conditionalAdapter) *Handler {
	if adapter == nil {
		panic("lowporthandler.newHandler requires a non-nil low-port adapter")
	}
	return &Handler{adapter: adapter}
}

// Close releases no resources because the low-port adapter owns only per-call native handles.
func (handler *Handler) Close() error {
	if handler == nil {
		return nil
	}
	return nil
}

// EnsureLowPorts ensures only the fixed low-port service described by current schema-2 ownership and policy.
func (handler *Handler) EnsureLowPorts(
	ctx context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (helper.LowPortMutationEvidence, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	request, ownershipFingerprint, expected, err := lowPortRequestFromTicket(
		ticket,
		admission,
		helper.OperationEnsureLowPorts,
	)
	if err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	change, err := handler.adapter.EnsureIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, ownershipFingerprint, request, expected.Fingerprint, change)
}

// ReleaseLowPorts removes only the fixed service owned by the current schema-2 policy.
func (handler *Handler) ReleaseLowPorts(
	ctx context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (helper.LowPortMutationEvidence, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	request, ownershipFingerprint, expected, err := lowPortRequestFromTicket(
		ticket,
		admission,
		helper.OperationReleaseLowPorts,
	)
	if err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	change, err := handler.adapter.ReleaseIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, ownershipFingerprint, request, expected.Fingerprint, change)
}

// lowPortRequestFromTicket reconstructs exact native authority from authenticated ticket and ownership dimensions.
func lowPortRequestFromTicket(
	ticket helper.Ticket,
	admission helper.TicketAdmission,
	operation helper.Operation,
) (lowport.Request, string, helper.ExpectedLowPortObservation, error) {
	if ticket.Operation != operation {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, fmt.Errorf(
			"low-port ticket operation %q does not match handler operation %q",
			ticket.Operation,
			operation,
		)
	}
	if ticket.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, errors.New(
			"low-port ticket is not bound to network-policy ownership",
		)
	}
	if ticket.NetworkPolicy == nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, errors.New(
			"low-port ticket is missing its host-network policy",
		)
	}
	policy := *ticket.NetworkPolicy
	if err := policy.Validate(); err != nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, fmt.Errorf(
			"low-port ticket host-network policy: %w",
			err,
		)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, fmt.Errorf(
			"low-port ticket host-network policy: %w",
			err,
		)
	}
	if policyFingerprint != ticket.NetworkPolicyFingerprint {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, errors.New(
			"low-port ticket host-network policy does not match machine ownership",
		)
	}
	if ticket.ExpectedLowPortObservation == nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, errors.New(
			"low-port ticket is missing its expected observation",
		)
	}
	expected := *ticket.ExpectedLowPortObservation
	if err := expected.Validate(); err != nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, err
	}
	if ticket.ApprovedAddress != "" ||
		ticket.ExpectedObservation != (helper.ExpectedObservation{}) ||
		ticket.ExpectedPreAssignment != nil ||
		ticket.ExpectedLoopbackPool != nil ||
		ticket.ExpectedResolverObservation != nil ||
		ticket.TrustRoot != nil ||
		ticket.ExpectedTrustObservation != nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, errors.New(
			"low-port ticket contains loopback, resolver, or trust mutation authority",
		)
	}
	target, ownershipFingerprint, err := currentOwnershipTarget(ticket, admission)
	if err != nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, err
	}
	request, err := lowport.NewRequest(target, policy)
	if err != nil {
		return lowport.Request{}, "", helper.ExpectedLowPortObservation{}, fmt.Errorf(
			"construct low-port request: %w",
			err,
		)
	}
	return request, ownershipFingerprint, expected, nil
}

// currentOwnershipTarget requires protected ownership to equal the signed schema-2 target before host mutation.
func currentOwnershipTarget(
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (ownership.Record, string, error) {
	if admission.RequesterIdentity != ticket.RequesterIdentity ||
		admission.InstallationID != ticket.InstallationID ||
		admission.OwnershipGeneration != ticket.OwnershipGeneration ||
		admission.OwnershipSchemaVersion != ticket.OwnershipSchemaVersion ||
		admission.NetworkPolicyFingerprint != ticket.NetworkPolicyFingerprint ||
		admission.ApprovedPool != ticket.ApprovedPool {
		return ownership.Record{}, "", errors.New(
			"low-port ownership admission does not match the signed target",
		)
	}
	if admission.OwnershipState != helper.OwnershipAdmissionAlreadyCurrent {
		return ownership.Record{}, "", errors.New(
			"low-port mutation requires ownership to be already current",
		)
	}
	target := ownership.Record{
		SchemaVersion:            ticket.OwnershipSchemaVersion,
		InstallationID:           ticket.InstallationID,
		OwnerIdentity:            ticket.RequesterIdentity,
		Generation:               ticket.OwnershipGeneration,
		LoopbackPoolPrefix:       ticket.ApprovedPool,
		NetworkPolicyFingerprint: ticket.NetworkPolicyFingerprint,
		TicketVerifierKey:        admission.TicketVerifierKey,
	}
	if target.SchemaVersion != ownership.NetworkPolicySchemaVersion {
		return ownership.Record{}, "", errors.New("low-port ownership target is not schema 2")
	}
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		return ownership.Record{}, "", fmt.Errorf("fingerprint low-port ownership target: %w", err)
	}
	if admission.TargetOwnershipFingerprint != targetFingerprint {
		return ownership.Record{}, "", errors.New(
			"low-port ownership admission target fingerprint does not match the signed target",
		)
	}
	if admission.OwnershipFingerprint != targetFingerprint {
		return ownership.Record{}, "", errors.New(
			"protected ownership is not the signed schema-2 low-port target",
		)
	}
	return target, targetFingerprint, nil
}

// evidenceFromChange independently verifies adapter compare-and-swap and postcondition evidence.
func evidenceFromChange(
	operation helper.Operation,
	ownershipFingerprint string,
	request lowport.Request,
	expectedFingerprint string,
	change lowport.Change,
) (helper.LowPortMutationEvidence, error) {
	if change.Indeterminate {
		return helper.LowPortMutationEvidence{}, errors.New("low-port mutation postcondition is indeterminate")
	}
	beforeFingerprint, err := validateCorrelatedObservation("precondition", change.Before, request)
	if err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	beforeState, err := change.Before.State()
	if err != nil {
		return helper.LowPortMutationEvidence{}, fmt.Errorf(
			"classify low-port mutation precondition: %w",
			err,
		)
	}
	if beforeState != lowport.StateAbsent &&
		beforeState != lowport.StateExact &&
		beforeState != lowport.StateOwnedDrifted {
		return helper.LowPortMutationEvidence{}, fmt.Errorf(
			"low-port mutation precondition %q cannot be safely changed",
			beforeState,
		)
	}
	if beforeFingerprint != expectedFingerprint {
		return helper.LowPortMutationEvidence{}, errors.New(
			"low-port mutation precondition does not match the signed observation",
		)
	}
	afterFingerprint, err := validateCorrelatedObservation("postcondition", change.After, request)
	if err != nil {
		return helper.LowPortMutationEvidence{}, err
	}
	observedChanged := beforeFingerprint != afterFingerprint
	if change.Changed != observedChanged || change.Changed && !change.Attempted {
		return helper.LowPortMutationEvidence{}, errors.New(
			"low-port mutation change flags do not match its observations",
		)
	}
	state, err := change.After.State()
	if err != nil {
		return helper.LowPortMutationEvidence{}, fmt.Errorf(
			"classify low-port mutation postcondition: %w",
			err,
		)
	}
	postcondition := helper.LowPortPostconditionExact
	switch operation {
	case helper.OperationEnsureLowPorts:
		if state != lowport.StateExact {
			return helper.LowPortMutationEvidence{}, fmt.Errorf(
				"low-port ensure postcondition is %q, want %q",
				state,
				lowport.StateExact,
			)
		}
	case helper.OperationReleaseLowPorts:
		if state != lowport.StateAbsent {
			return helper.LowPortMutationEvidence{}, fmt.Errorf(
				"low-port release postcondition is %q, want %q",
				state,
				lowport.StateAbsent,
			)
		}
		postcondition = helper.LowPortPostconditionOwnedAbsent
	default:
		return helper.LowPortMutationEvidence{}, fmt.Errorf(
			"low-port evidence operation %q is unsupported",
			operation,
		)
	}
	return helper.LowPortMutationEvidence{
		Changed:                change.Changed,
		PolicyFingerprint:      request.PolicyFingerprint(),
		OwnershipFingerprint:   ownershipFingerprint,
		ObservationFingerprint: afterFingerprint,
		Postcondition:          postcondition,
	}, nil
}

// validateCorrelatedObservation proves one complete canonical observation belongs to the signed request.
func validateCorrelatedObservation(
	name string,
	observation lowport.Observation,
	request lowport.Request,
) (string, error) {
	if err := observation.Validate(); err != nil {
		return "", fmt.Errorf("validate low-port mutation %s: %w", name, err)
	}
	if observation.Request != request {
		return "", fmt.Errorf("low-port mutation %s belongs to another request", name)
	}
	state, err := observation.State()
	if err != nil {
		return "", fmt.Errorf("validate low-port mutation %s artifacts: %w", name, err)
	}
	if state == lowport.StateIndeterminate {
		return "", fmt.Errorf("low-port mutation %s is incomplete", name)
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return "", fmt.Errorf("fingerprint low-port mutation %s: %w", name, err)
	}
	return fingerprint, nil
}

// normalizeContext gives direct handler callers the same nil-context behavior as native adapters.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ helper.LowPortHandler = (*Handler)(nil)
var _ conditionalAdapter = (*lowport.Adapter)(nil)
