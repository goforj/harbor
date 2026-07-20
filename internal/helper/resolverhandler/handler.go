package resolverhandler

import (
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/resolver"
)

// conditionalAdapter is the exact observation-bound resolver surface available to the privileged handler.
type conditionalAdapter interface {
	Observe(context.Context, resolver.Request) (resolver.Observation, error)
	EnsureIfObserved(context.Context, resolver.Request, string) (resolver.Change, error)
	ReleaseIfObserved(context.Context, resolver.Request, string) (resolver.Change, error)
}

// Handler turns one admitted policy-bound ticket into an exact resolver effect.
type Handler struct {
	adapter conditionalAdapter
}

var _ helper.ResolverHandler = (*Handler)(nil)

// New creates a handler backed by one reviewed platform resolver adapter.
func New(adapter *resolver.Adapter) *Handler {
	if adapter == nil {
		panic("resolverhandler.New requires a non-nil resolver adapter")
	}
	return newHandler(adapter)
}

// newHandler keeps native effects replaceable in tests without expanding the production constructor.
func newHandler(adapter conditionalAdapter) *Handler {
	return &Handler{adapter: adapter}
}

// EnsureResolver ensures only the resolver rule described by the ticket's complete signed policy.
func (handler *Handler) EnsureResolver(ctx context.Context, ticket helper.Ticket) (helper.ResolverMutationEvidence, error) {
	request, expected, err := resolverRequestFromTicket(ticket, helper.OperationEnsureResolver)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	change, err := handler.adapter.EnsureIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, request, change)
}

// ReleaseResolver removes only the uniquely owned rule described by the ticket's complete signed policy.
func (handler *Handler) ReleaseResolver(ctx context.Context, ticket helper.Ticket) (helper.ResolverMutationEvidence, error) {
	request, expected, err := resolverRequestFromTicket(ticket, helper.OperationReleaseResolver)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	change, err := handler.adapter.ReleaseIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.ResolverMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, request, change)
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
	case helper.OperationReleaseResolver:
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
		ObservationFingerprint: fingerprint,
		Postcondition:          postcondition,
	}, nil
}
