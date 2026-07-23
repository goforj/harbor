package trusthandler

import (
	"bytes"
	"context"
	"fmt"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/ownership"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/trust/certroot"
)

// conditionalAdapter is the only observation-bound trust surface available to the privileged handler.
type conditionalAdapter interface {
	Observe(context.Context, trust.Request) (trust.Observation, error)
	EnsureIfObserved(context.Context, trust.Request, string) (trust.Change, error)
	ReleaseIfObserved(context.Context, trust.Request, string) (trust.Change, error)
}

// Handler turns one admitted public-CA ticket into an exact trust-store effect.
type Handler struct {
	adapter conditionalAdapter
}

// New creates a handler around an injected trust adapter.
//
// Native platform adapter selection is intentionally outside this package. A
// platform without a reviewed backend must not construct a handler for
// production use; tests can inject a conditional adapter through newHandler.
func New(adapter *trust.Adapter) *Handler {
	if adapter == nil {
		panic("trusthandler.New requires a non-nil trust adapter")
	}
	return newHandler(adapter)
}

// newHandler keeps native effects replaceable in tests without expanding the production constructor.
func newHandler(adapter conditionalAdapter) *Handler {
	if adapter == nil {
		panic("trusthandler.newHandler requires a non-nil trust adapter")
	}
	return &Handler{adapter: adapter}
}

// Close releases no resources because the trust adapter owns only per-call native handles.
func (handler *Handler) Close() error {
	if handler == nil {
		return nil
	}
	return nil
}

// EnsureTrust ensures only the public CA and trust scope described by the ticket's complete signed policy.
func (handler *Handler) EnsureTrust(
	ctx context.Context,
	ticket helper.Ticket,
) (helper.TrustMutationEvidence, error) {
	request, expected, err := trustRequestFromTicket(ticket, helper.OperationEnsureTrust)
	if err != nil {
		return helper.TrustMutationEvidence{}, err
	}
	change, err := handler.adapter.EnsureIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.TrustMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, request, expected.Fingerprint, change)
}

// ReleaseTrust removes only the uniquely owned public CA described by the ticket's complete signed policy.
func (handler *Handler) ReleaseTrust(
	ctx context.Context,
	ticket helper.Ticket,
) (helper.TrustMutationEvidence, error) {
	request, expected, err := trustRequestFromTicket(ticket, helper.OperationReleaseTrust)
	if err != nil {
		return helper.TrustMutationEvidence{}, err
	}
	change, err := handler.adapter.ReleaseIfObserved(ctx, request, expected.Fingerprint)
	if err != nil {
		return helper.TrustMutationEvidence{}, err
	}
	return evidenceFromChange(ticket.Operation, request, expected.Fingerprint, change)
}

// trustRequestFromTicket reconstructs private trust authority only from the complete signed policy and public root.
func trustRequestFromTicket(
	ticket helper.Ticket,
	operation helper.Operation,
) (trust.Request, helper.ExpectedTrustObservation, error) {
	if ticket.Operation != operation {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf(
			"trust ticket operation %q does not match handler operation %q",
			ticket.Operation,
			operation,
		)
	}
	if ticket.OwnershipSchemaVersion != ownership.NetworkPolicySchemaVersion {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket is not bound to network-policy ownership")
	}
	if ticket.NetworkPolicy == nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket is missing its host-network policy")
	}
	policy := *ticket.NetworkPolicy
	if err := policy.Validate(); err != nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket host-network policy: %w", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket host-network policy: %w", err)
	}
	if policyFingerprint != ticket.NetworkPolicyFingerprint {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket host-network policy does not match machine ownership")
	}
	if ticket.TrustRoot == nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket is missing its public CA material")
	}
	if ticket.TrustRoot.Fingerprint != policy.AuthorityFingerprint {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket public CA does not match host-network policy")
	}
	if ticket.ExpectedTrustObservation == nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket is missing its expected observation")
	}
	expected := *ticket.ExpectedTrustObservation
	if err := expected.Validate(); err != nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, err
	}
	if ticket.ApprovedAddress != "" || ticket.ExpectedObservation != (helper.ExpectedObservation{}) ||
		ticket.ExpectedPreAssignment != nil || ticket.ExpectedLoopbackPool != nil || ticket.ExpectedResolverObservation != nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, fmt.Errorf("trust ticket contains loopback or resolver mutation authority")
	}
	root := certroot.Root{
		CertificatePEM: append([]byte(nil), ticket.TrustRoot.CertificatePEM...),
		Fingerprint:    ticket.TrustRoot.Fingerprint,
		NotBefore:      ticket.TrustRoot.NotBefore,
		NotAfter:       ticket.TrustRoot.NotAfter,
	}
	request, err := trust.NewRequestForRequester(
		ticket.InstallationID,
		ticket.RequesterIdentity,
		policy.Mechanisms.Trust,
		root,
	)
	if err != nil {
		return trust.Request{}, helper.ExpectedTrustObservation{}, err
	}
	return request, expected, nil
}

// evidenceFromChange reduces native trust facts to the postcondition needed by the unprivileged caller.
func evidenceFromChange(
	operation helper.Operation,
	request trust.Request,
	expectedFingerprint string,
	change trust.Change,
) (helper.TrustMutationEvidence, error) {
	if change.Indeterminate {
		return helper.TrustMutationEvidence{}, fmt.Errorf("trust mutation postcondition is indeterminate")
	}
	after := change.After
	if err := after.Validate(); err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("validate trust mutation postcondition: %w", err)
	}
	if !sameRequest(after.Request, request) {
		return helper.TrustMutationEvidence{}, fmt.Errorf("trust mutation postcondition belongs to another authority")
	}
	assessment, err := after.Classify()
	if err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("classify trust mutation postcondition: %w", err)
	}
	fingerprint, err := after.Fingerprint()
	if err != nil {
		return helper.TrustMutationEvidence{}, fmt.Errorf("fingerprint trust mutation postcondition: %w", err)
	}
	postcondition, err := trustPostcondition(operation, request, expectedFingerprint, change, assessment)
	if err != nil {
		return helper.TrustMutationEvidence{}, err
	}
	return helper.TrustMutationEvidence{
		Changed:                change.Changed,
		AuthorityFingerprint:   request.AuthorityFingerprint(),
		Mechanism:              request.Mechanism(),
		ObservationFingerprint: fingerprint,
		Postcondition:          postcondition,
	}, nil
}

// trustPostcondition accepts only verified states, including a safe unowned pre-existing root that remains untouched.
func trustPostcondition(
	operation helper.Operation,
	request trust.Request,
	expectedFingerprint string,
	change trust.Change,
	assessment trust.Assessment,
) (helper.TrustPostcondition, error) {
	switch operation {
	case helper.OperationEnsureTrust:
		switch assessment.State {
		case trust.StateExact:
			return helper.TrustPostconditionExact, nil
		case trust.StateForeign:
			if assessment.Owned == trust.OwnedStateAbsent && !change.Changed &&
				trustObservationHasOnlyPreexistingIdenticalRoots(change.After, request) {
				if fingerprint, err := change.After.Fingerprint(); err != nil || fingerprint != expectedFingerprint {
					return "", fmt.Errorf("trust ensure pre-existing root observation changed unexpectedly")
				}
				return helper.TrustPostconditionPreexisting, nil
			}
		}
		return "", fmt.Errorf("trust ensure did not produce a trusted postcondition")
	case helper.OperationReleaseTrust:
		if assessment.State != trust.StateAbsent || assessment.Owned != trust.OwnedStateAbsent {
			return "", fmt.Errorf("trust release did not remove its owned root")
		}
		return helper.TrustPostconditionOwnedAbsent, nil
	default:
		return "", fmt.Errorf("trust evidence operation %q is unsupported", operation)
	}
}

// trustObservationHasOnlyPreexistingIdenticalRoots identifies an unowned root Harbor may reuse but never remove.
func trustObservationHasOnlyPreexistingIdenticalRoots(observation trust.Observation, request trust.Request) bool {
	found := false
	for _, entry := range observation.Entries {
		if entry.Owner != nil {
			return false
		}
		if entry.CertificateFingerprint != request.AuthorityFingerprint() {
			continue
		}
		if entry.Mechanism != request.Mechanism() || !entry.NativeExact {
			return false
		}
		found = true
	}
	return found
}

// sameRequest compares the complete public authority without exposing trust request internals.
func sameRequest(left trust.Request, right trust.Request) bool {
	leftRoot := left.Root()
	rightRoot := right.Root()
	return left.InstallationID() == right.InstallationID() &&
		left.RequesterIdentity() == right.RequesterIdentity() &&
		left.Mechanism() == right.Mechanism() &&
		left.AuthorityFingerprint() == right.AuthorityFingerprint() &&
		leftRoot.NotBefore.Equal(rightRoot.NotBefore) &&
		leftRoot.NotAfter.Equal(rightRoot.NotAfter) &&
		bytes.Equal(leftRoot.CertificatePEM, rightRoot.CertificatePEM)
}

var _ conditionalAdapter = (*trust.Adapter)(nil)
