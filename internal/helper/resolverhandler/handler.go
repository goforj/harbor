package resolverhandler

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/machinepaths"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// conditionalAdapter is the exact observation-bound resolver surface available to the privileged handler.
type conditionalAdapter interface {
	Observe(context.Context, resolver.Request) (resolver.Observation, error)
	EnsureIfObserved(context.Context, resolver.Request, string) (resolver.Change, error)
	ReleaseIfObserved(context.Context, resolver.Request, string) (resolver.Change, error)
}

// ownershipStore is the protected ownership mutation surface available to the resolver handler.
type ownershipStore interface {
	Upgrade(context.Context, string, ownership.Record) (ownership.Observation, error)
	Downgrade(context.Context, string, ownership.Record) (ownership.Observation, error)
	Close() error
}

// Handler turns one admitted policy-bound ticket into an exact resolver effect.
type Handler struct {
	adapter   conditionalAdapter
	ownership ownershipStore
}

var _ helper.ResolverHandler = (*Handler)(nil)

// OpenDefault opens a handler against Harbor's fixed protected ownership record and reviewed resolver adapter.
func OpenDefault(adapter *resolver.Adapter) (*Handler, error) {
	if adapter == nil {
		panic("resolverhandler.OpenDefault requires a non-nil resolver adapter")
	}
	paths, err := machinepaths.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve protected resolver ownership path: %w", err)
	}
	store, err := ownership.NewStore(paths.OwnershipPath)
	if err != nil {
		return nil, fmt.Errorf("open protected resolver ownership: %w", err)
	}
	return newHandler(adapter, store), nil
}

// newHandler keeps native effects replaceable in tests without expanding the production constructor.
func newHandler(adapter conditionalAdapter, ownershipStore ownershipStore) *Handler {
	if adapter == nil {
		panic("resolverhandler.newHandler requires a non-nil resolver adapter")
	}
	if ownershipStore == nil {
		panic("resolverhandler.newHandler requires a non-nil ownership store")
	}
	return &Handler{adapter: adapter, ownership: ownershipStore}
}

// Close releases the retained protected ownership boundary without changing its record.
func (handler *Handler) Close() error {
	return handler.ownership.Close()
}

// EnsureResolver ensures only the resolver rule described by the ticket's complete signed policy.
func (handler *Handler) EnsureResolver(
	ctx context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (helper.ResolverMutationEvidence, error) {
	request, expected, err := resolverRequestFromTicket(ticket, helper.OperationEnsureResolver)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	ownershipFingerprint, err := handler.ensureOwnership(ctx, ticket, admission)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	change, err := handler.adapter.EnsureIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, ownershipFingerprint, request, change)
}

// ReleaseResolver removes only the uniquely owned rule described by the ticket's complete signed policy.
func (handler *Handler) ReleaseResolver(
	ctx context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (helper.ResolverMutationEvidence, error) {
	request, expected, err := resolverRequestFromTicket(ticket, helper.OperationReleaseResolver)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	_, ownershipFingerprint, err := currentOwnershipTarget(ticket, admission)
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("admit resolver release ownership: %w", err)
	}
	change, err := handler.adapter.ReleaseIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, ownershipFingerprint, request, change)
}

// RetireResolver removes one exact owned resolver rule and returns schema-one post-state evidence on an idempotent replay.
func (handler *Handler) RetireResolver(
	ctx context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (helper.ResolverMutationEvidence, error) {
	request, expected, err := resolverRequestFromTicket(ticket, helper.OperationRetireResolver)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	source, sourceFingerprint, target, targetFingerprint, err := retirementOwnership(ticket, admission)
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("admit resolver retirement ownership: %w", err)
	}
	if admission.OwnershipState == helper.OwnershipAdmissionAlreadyRetired {
		observed, err := handler.adapter.Observe(ctx, request)
		if err != nil {
			return helper.ResolverMutationEvidence{}, fmt.Errorf("observe retired resolver: %w", err)
		}
		change := resolver.Change{
			Before: observed,
			After:  observed,
		}
		evidence, err := evidenceFromChange(ticket.Operation, sourceFingerprint, request, change)
		if err != nil {
			return helper.ResolverMutationEvidence{}, err
		}
		downgraded, err := handler.ownership.Downgrade(ctx, targetFingerprint, target)
		if err != nil {
			return helper.ResolverMutationEvidence{}, fmt.Errorf("confirm retired resolver ownership: %w", err)
		}
		if !downgraded.Exists || downgraded.Record != source || downgraded.Fingerprint != sourceFingerprint {
			return helper.ResolverMutationEvidence{}, fmt.Errorf("confirm retired resolver ownership: protected store returned a different schema-1 source")
		}
		return evidence, nil
	}
	change, err := handler.adapter.ReleaseIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	evidence, err := evidenceFromChange(ticket.Operation, sourceFingerprint, request, change)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	downgraded, err := handler.ownership.Downgrade(ctx, targetFingerprint, target)
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("downgrade resolver ownership: %w", err)
	}
	if !downgraded.Exists || downgraded.Record != source || downgraded.Fingerprint != sourceFingerprint {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("downgrade resolver ownership: protected store returned a different schema-1 source")
	}
	return evidence, nil
}

// ensureOwnership performs the one admitted schema transition before any resolver mutation.
func (handler *Handler) ensureOwnership(
	ctx context.Context,
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (string, error) {
	target, targetFingerprint, err := ownershipTarget(ticket, admission)
	if err != nil {
		return "", fmt.Errorf("admit resolver ensure ownership: %w", err)
	}
	source := target
	source.SchemaVersion = ownership.IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	sourceFingerprint, err := source.Fingerprint()
	if err != nil {
		return "", fmt.Errorf("derive resolver ownership transition source: %w", err)
	}
	switch admission.OwnershipState {
	case helper.OwnershipAdmissionAlreadyCurrent:
		if admission.OwnershipFingerprint != targetFingerprint || admission.PostOwnershipFingerprint != targetFingerprint {
			return "", fmt.Errorf("admit resolver ensure ownership: protected ownership is not the signed schema-2 target")
		}
	case helper.OwnershipAdmissionSchema1To2:
		if admission.OwnershipFingerprint != sourceFingerprint || admission.PostOwnershipFingerprint != targetFingerprint {
			return "", fmt.Errorf("admit resolver ownership transition: protected schema-1 source is not derived from the signed target")
		}
	default:
		return "", fmt.Errorf("admit resolver ensure ownership: ownership admission state %q is unsupported", admission.OwnershipState)
	}
	upgraded, err := handler.ownership.Upgrade(ctx, sourceFingerprint, target)
	if err != nil {
		return "", fmt.Errorf("upgrade resolver ownership: %w", err)
	}
	if !upgraded.Exists || upgraded.Record != target || upgraded.Fingerprint != targetFingerprint {
		return "", fmt.Errorf("upgrade resolver ownership: protected store returned a different schema-2 target")
	}
	return upgraded.Fingerprint, nil
}

// currentOwnershipTarget rejects release and current-state paths unless protected ownership already equals schema 2.
func currentOwnershipTarget(ticket helper.Ticket, admission helper.TicketAdmission) (ownership.Record, string, error) {
	target, targetFingerprint, err := ownershipTarget(ticket, admission)
	if err != nil {
		return ownership.Record{}, "", err
	}
	if admission.OwnershipState != helper.OwnershipAdmissionAlreadyCurrent {
		return ownership.Record{}, "", fmt.Errorf("resolver release requires ownership to be already current")
	}
	if admission.OwnershipFingerprint != targetFingerprint || admission.PostOwnershipFingerprint != targetFingerprint {
		return ownership.Record{}, "", fmt.Errorf("protected ownership is not the signed schema-2 target")
	}
	return target, targetFingerprint, nil
}

// retirementOwnership verifies the protected schema-2 target and its ticket-derived schema-1 successor.
func retirementOwnership(ticket helper.Ticket, admission helper.TicketAdmission) (ownership.Record, string, ownership.Record, string, error) {
	target, targetFingerprint, err := ownershipTarget(ticket, admission)
	if err != nil {
		return ownership.Record{}, "", ownership.Record{}, "", err
	}
	source := target
	source.SchemaVersion = ownership.IdentitySchemaVersion
	source.NetworkPolicyFingerprint = ""
	sourceFingerprint, err := source.Fingerprint()
	if err != nil {
		return ownership.Record{}, "", ownership.Record{}, "", fmt.Errorf("fingerprint resolver retirement schema-1 source: %w", err)
	}
	if admission.PostOwnershipFingerprint != sourceFingerprint {
		return ownership.Record{}, "", ownership.Record{}, "", fmt.Errorf("resolver retirement post-ownership fingerprint does not match the schema-1 source")
	}
	switch admission.OwnershipState {
	case helper.OwnershipAdmissionSchema2To1:
		if admission.OwnershipFingerprint != targetFingerprint {
			return ownership.Record{}, "", ownership.Record{}, "", fmt.Errorf("protected ownership is not the signed schema-2 target")
		}
	case helper.OwnershipAdmissionAlreadyRetired:
		if admission.OwnershipFingerprint != sourceFingerprint {
			return ownership.Record{}, "", ownership.Record{}, "", fmt.Errorf("protected ownership is not the derived schema-1 source")
		}
	default:
		return ownership.Record{}, "", ownership.Record{}, "", fmt.Errorf("resolver retirement ownership admission is unsupported")
	}
	return source, sourceFingerprint, target, targetFingerprint, nil
}

// ownershipTarget reconstructs the exact schema-2 record from signed dimensions and independently admitted key material.
func ownershipTarget(
	ticket helper.Ticket,
	admission helper.TicketAdmission,
) (ownership.Record, string, error) {
	if admission.RequesterIdentity != ticket.RequesterIdentity ||
		admission.InstallationID != ticket.InstallationID ||
		admission.OwnershipGeneration != ticket.OwnershipGeneration ||
		admission.OwnershipSchemaVersion != ticket.OwnershipSchemaVersion ||
		admission.NetworkPolicyFingerprint != ticket.NetworkPolicyFingerprint ||
		admission.ApprovedPool != ticket.ApprovedPool {
		return ownership.Record{}, "", fmt.Errorf("ownership admission does not match the signed resolver target")
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
		return ownership.Record{}, "", fmt.Errorf("resolver ownership target is not schema 2")
	}
	targetFingerprint, err := target.Fingerprint()
	if err != nil {
		return ownership.Record{}, "", fmt.Errorf("fingerprint resolver ownership target: %w", err)
	}
	if admission.TargetOwnershipFingerprint != targetFingerprint {
		return ownership.Record{}, "", fmt.Errorf("ownership admission target fingerprint does not match the signed resolver target")
	}
	return target, targetFingerprint, nil
}

// resolverRequestFromTicket reconstructs private resolver authority only from the complete signed policy.
func resolverRequestFromTicket(
	ticket helper.Ticket,
	operation helper.Operation,
) (resolver.Request, helper.ExpectedResolverObservation, error) {
	if ticket.Operation != operation {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, fmt.Errorf(
			"resolver ticket operation %q does not match handler operation %q",
			ticket.Operation,
			operation,
		)
	}
	if ticket.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, fmt.Errorf("resolver ticket is not bound to network-policy ownership")
	}
	if ticket.NetworkPolicy == nil {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, fmt.Errorf("resolver ticket is missing its host-network policy")
	}
	policy := *ticket.NetworkPolicy
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, fmt.Errorf("resolver ticket host-network policy: %w", err)
	}
	if policyFingerprint != ticket.NetworkPolicyFingerprint {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, fmt.Errorf("resolver ticket host-network policy does not match machine ownership")
	}
	if ticket.ExpectedResolverObservation == nil {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, fmt.Errorf("resolver ticket is missing its expected observation")
	}
	expected := *ticket.ExpectedResolverObservation
	if err := expected.Validate(); err != nil {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, err
	}
	if ticket.ApprovedAddress != "" || ticket.ExpectedObservation != (helper.ExpectedObservation{}) ||
		ticket.ExpectedPreAssignment != nil || ticket.ExpectedLoopbackPool != nil {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, fmt.Errorf("resolver ticket contains loopback mutation authority")
	}
	request, err := resolver.NewRequest(ticket.InstallationID, policy)
	if err != nil {
		return resolver.Request{}, helper.ExpectedResolverObservation{}, err
	}
	return request, expected, nil
}

// evidenceFromChange reduces native resolver facts to the postcondition needed by the unprivileged caller.
func evidenceFromChange(
	operation helper.Operation,
	ownershipFingerprint string,
	request resolver.Request,
	change resolver.Change,
) (helper.ResolverMutationEvidence, error) {
	if change.Indeterminate {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("resolver mutation postcondition is indeterminate")
	}
	after := change.After
	if err := after.Validate(); err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("validate resolver mutation postcondition: %w", err)
	}
	if after.Request.InstallationID() != request.InstallationID() ||
		after.Request.PolicyFingerprint() != request.PolicyFingerprint() {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("resolver mutation postcondition belongs to another policy")
	}
	assessment, err := after.Classify()
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("classify resolver mutation postcondition: %w", err)
	}
	postcondition := helper.ResolverPostconditionExact
	switch operation {
	case helper.OperationEnsureResolver:
		if assessment.State != resolver.StateExact {
			return helper.ResolverMutationEvidence{}, fmt.Errorf("resolver ensure did not produce an exact owned rule")
		}
	case helper.OperationReleaseResolver, helper.OperationRetireResolver:
		if assessment.State == resolver.StateIndeterminate || assessment.Owned != resolver.OwnedStateAbsent {
			return helper.ResolverMutationEvidence{}, fmt.Errorf("resolver release did not remove its owned rule")
		}
		postcondition = helper.ResolverPostconditionOwnedAbsent
	default:
		return helper.ResolverMutationEvidence{}, fmt.Errorf("resolver evidence operation %q is unsupported", operation)
	}
	fingerprint, err := after.Fingerprint()
	if err != nil {
		return helper.ResolverMutationEvidence{}, fmt.Errorf("fingerprint resolver mutation postcondition: %w", err)
	}
	return helper.ResolverMutationEvidence{
		Changed:                change.Changed,
		PolicyFingerprint:      request.PolicyFingerprint(),
		OwnershipFingerprint:   ownershipFingerprint,
		ObservationFingerprint: fingerprint,
		Postcondition:          postcondition,
	}, nil
}
