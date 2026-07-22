package control

import (
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// PrepareNetworkReleaseApprovalRequest selects the exact release checkpoint that may issue low-port authority.
type PrepareNetworkReleaseApprovalRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedCheckpointRevision fences preparation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence `json:"expected_checkpoint_revision"`
}

// Validate reports whether the request identifies one bounded release checkpoint.
func (request PrepareNetworkReleaseApprovalRequest) Validate() error {
	return validateNetworkReleaseApprovalSelection(request.OperationID, request.ExpectedCheckpointRevision)
}

// NetworkReleaseApprovalTicket is non-secret launch metadata for one low-port release.
type NetworkReleaseApprovalTicket struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// Reference identifies the opaque helper capability.
	Reference helper.TicketReference `json:"reference"`
	// Operation fixes the helper action to low-port release.
	Operation helper.Operation `json:"operation"`
	// PolicyFingerprint binds the ticket to the retained network policy.
	PolicyFingerprint string `json:"policy_fingerprint"`
	// TargetOwnershipFingerprint binds the ticket to the retained ownership target.
	TargetOwnershipFingerprint string `json:"target_ownership_fingerprint"`
	// ObservationFingerprint binds the ticket to the observed low-port state.
	ObservationFingerprint string `json:"observation_fingerprint"`
	// ExpiresAt limits the lifetime of the helper capability.
	ExpiresAt time.Time `json:"expires_at"`
}

// Validate reports whether ticket metadata can launch only the selected low-port release.
func (ticket NetworkReleaseApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationReleaseLowPorts {
		return fmt.Errorf("network release low-port operation is %q, expected %q", ticket.Operation, helper.OperationReleaseLowPorts)
	}
	for name, fingerprint := range map[string]string{
		"policy":           ticket.PolicyFingerprint,
		"target ownership": ticket.TargetOwnershipFingerprint,
		"observation":      ticket.ObservationFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network release low-port %s fingerprint is invalid", name)
		}
	}
	if ticket.ExpiresAt.IsZero() || ticket.ExpiresAt.Location() != time.UTC {
		return errors.New("network release low-port expiry must be a nonzero UTC time")
	}
	return nil
}

// NetworkReleaseApprovalPreparation reports one helper capability for an exact release checkpoint.
type NetworkReleaseApprovalPreparation struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// CheckpointRevision identifies the retained release checkpoint.
	CheckpointRevision domain.Sequence `json:"checkpoint_revision"`
	// Ticket supplies the low-port release helper capability.
	Ticket NetworkReleaseApprovalTicket `json:"ticket"`
}

// Validate reports whether preparation and ticket identify the same selected release operation.
func (preparation NetworkReleaseApprovalPreparation) Validate() error {
	if err := validateNetworkReleaseApprovalSelection(preparation.OperationID, preparation.CheckpointRevision); err != nil {
		return err
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID {
		return errors.New("network release low-port ticket belongs to another operation")
	}
	return nil
}

// ConfirmNetworkReleaseApprovalRequest selects one release checkpoint and supplies its low-port release evidence.
type ConfirmNetworkReleaseApprovalRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedCheckpointRevision fences confirmation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence `json:"expected_checkpoint_revision"`
	// LowPortEvidence proves the owned low-port state is absent.
	LowPortEvidence helper.LowPortMutationEvidence `json:"low_port_evidence"`
}

// Validate reports whether confirmation carries one owned-absent low-port release postcondition.
func (request ConfirmNetworkReleaseApprovalRequest) Validate() error {
	if err := validateNetworkReleaseApprovalSelection(request.OperationID, request.ExpectedCheckpointRevision); err != nil {
		return err
	}
	return validateNetworkReleaseLowPortEvidence(request.LowPortEvidence)
}

// validateNetworkReleaseApprovalSelection bounds the optimistic release checkpoint selector.
func validateNetworkReleaseApprovalSelection(operationID domain.OperationID, revision domain.Sequence) error {
	if err := operationID.Validate(); err != nil {
		return err
	}
	return validateNetworkReleaseRevision("checkpoint", revision)
}

// validateNetworkReleaseLowPortEvidence confines confirmation to a completed owned-absent low-port release.
func validateNetworkReleaseLowPortEvidence(evidence helper.LowPortMutationEvidence) error {
	if evidence.Postcondition != helper.LowPortPostconditionOwnedAbsent {
		return errors.New("network release low-port evidence must prove owned absence")
	}
	for name, fingerprint := range map[string]string{
		"policy":      evidence.PolicyFingerprint,
		"ownership":   evidence.OwnershipFingerprint,
		"observation": evidence.ObservationFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network release low-port evidence %s fingerprint is invalid", name)
		}
	}
	return nil
}

// PrepareNetworkReleaseResolverApprovalRequest selects the exact release checkpoint that may issue resolver authority.
type PrepareNetworkReleaseResolverApprovalRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedCheckpointRevision fences preparation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence `json:"expected_checkpoint_revision"`
}

// Validate reports whether the request identifies one bounded release checkpoint.
func (request PrepareNetworkReleaseResolverApprovalRequest) Validate() error {
	return validateNetworkReleaseApprovalSelection(request.OperationID, request.ExpectedCheckpointRevision)
}

// NetworkReleaseResolverApprovalTicket is non-secret launch metadata for one resolver release.
type NetworkReleaseResolverApprovalTicket struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// Reference identifies the opaque helper capability.
	Reference helper.TicketReference `json:"reference"`
	// Operation fixes the helper action to resolver release.
	Operation helper.Operation `json:"operation"`
	// PolicyFingerprint binds the ticket to the retained network policy.
	PolicyFingerprint string `json:"policy_fingerprint"`
	// TargetOwnershipFingerprint binds the ticket to the retained ownership target.
	TargetOwnershipFingerprint string `json:"target_ownership_fingerprint"`
	// ExpiresAt limits the lifetime of the helper capability.
	ExpiresAt time.Time `json:"expires_at"`
}

// NetworkReleaseResolverPublicationDisposition identifies whether ticket publication completed durably or needs reference-based reconciliation.
type NetworkReleaseResolverPublicationDisposition string

const (
	// NetworkReleaseResolverPublicationDurable means the capability publisher confirmed durable storage.
	NetworkReleaseResolverPublicationDurable NetworkReleaseResolverPublicationDisposition = "durable"
	// NetworkReleaseResolverPublicationIndeterminate means the returned reference is the only capability that may be reconciled.
	NetworkReleaseResolverPublicationIndeterminate NetworkReleaseResolverPublicationDisposition = "indeterminate"
)

// Validate rejects publication states that would let callers infer unsafe retry behavior.
func (disposition NetworkReleaseResolverPublicationDisposition) Validate() error {
	switch disposition {
	case NetworkReleaseResolverPublicationDurable, NetworkReleaseResolverPublicationIndeterminate:
		return nil
	default:
		return fmt.Errorf("network release resolver publication disposition %q is unsupported", disposition)
	}
}

// Validate reports whether ticket metadata can launch only the selected resolver release.
func (ticket NetworkReleaseResolverApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationReleaseResolver {
		return fmt.Errorf("network release resolver operation is %q, expected %q", ticket.Operation, helper.OperationReleaseResolver)
	}
	for name, fingerprint := range map[string]string{
		"policy":           ticket.PolicyFingerprint,
		"target ownership": ticket.TargetOwnershipFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network release resolver %s fingerprint is invalid", name)
		}
	}
	if ticket.ExpiresAt.IsZero() || ticket.ExpiresAt.Location() != time.UTC {
		return errors.New("network release resolver expiry must be a nonzero UTC time")
	}
	return nil
}

// NetworkReleaseResolverApprovalPreparation reports one helper capability for an exact release checkpoint.
type NetworkReleaseResolverApprovalPreparation struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// CheckpointRevision identifies the retained release checkpoint.
	CheckpointRevision domain.Sequence `json:"checkpoint_revision"`
	// PublicationDisposition tells the caller whether to redeem or reconcile the one returned reference without reissuing.
	PublicationDisposition NetworkReleaseResolverPublicationDisposition `json:"publication_disposition"`
	// Ticket supplies the resolver release helper capability.
	Ticket NetworkReleaseResolverApprovalTicket `json:"ticket"`
}

// Validate reports whether preparation and ticket identify the same selected release operation.
func (preparation NetworkReleaseResolverApprovalPreparation) Validate() error {
	if err := validateNetworkReleaseApprovalSelection(preparation.OperationID, preparation.CheckpointRevision); err != nil {
		return err
	}
	if err := preparation.PublicationDisposition.Validate(); err != nil {
		return err
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID {
		return errors.New("network release resolver ticket belongs to another operation")
	}
	return nil
}

// ConfirmNetworkReleaseResolverApprovalRequest selects one release checkpoint and supplies resolver release evidence.
type ConfirmNetworkReleaseResolverApprovalRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedCheckpointRevision fences confirmation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence `json:"expected_checkpoint_revision"`
	// ResolverEvidence proves the owned resolver state is absent.
	ResolverEvidence helper.ResolverMutationEvidence `json:"resolver_evidence"`
}

// Validate reports whether confirmation carries one owned-absent resolver release postcondition.
func (request ConfirmNetworkReleaseResolverApprovalRequest) Validate() error {
	if err := validateNetworkReleaseApprovalSelection(request.OperationID, request.ExpectedCheckpointRevision); err != nil {
		return err
	}
	return validateNetworkReleaseResolverEvidence(request.ResolverEvidence)
}

// validateNetworkReleaseResolverEvidence confines confirmation to a completed owned-absent resolver release.
func validateNetworkReleaseResolverEvidence(evidence helper.ResolverMutationEvidence) error {
	if evidence.Postcondition != helper.ResolverPostconditionOwnedAbsent {
		return errors.New("network release resolver evidence must prove owned absence")
	}
	for name, fingerprint := range map[string]string{
		"policy":      evidence.PolicyFingerprint,
		"ownership":   evidence.OwnershipFingerprint,
		"observation": evidence.ObservationFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network release resolver evidence %s fingerprint is invalid", name)
		}
	}
	return nil
}

// PrepareNetworkReleaseTrustApprovalRequest selects the exact release checkpoint that may issue trust-release authority.
type PrepareNetworkReleaseTrustApprovalRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedCheckpointRevision fences preparation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence `json:"expected_checkpoint_revision"`
}

// Validate reports whether the request identifies one bounded release checkpoint.
func (request PrepareNetworkReleaseTrustApprovalRequest) Validate() error {
	return validateNetworkReleaseApprovalSelection(request.OperationID, request.ExpectedCheckpointRevision)
}

// NetworkReleaseTrustDisposition identifies whether Harbor owns the trust entry being released.
type NetworkReleaseTrustDisposition string

const (
	// NetworkReleaseTrustOwned means Harbor owns the trust entry and must remove it.
	NetworkReleaseTrustOwned NetworkReleaseTrustDisposition = "owned"
	// NetworkReleaseTrustPreexistingUnowned means a matching unowned trust entry must be preserved.
	NetworkReleaseTrustPreexistingUnowned NetworkReleaseTrustDisposition = "preexisting_unowned"
)

// Validate rejects trust ownership states that could blur removal with preservation.
func (disposition NetworkReleaseTrustDisposition) Validate() error {
	switch disposition {
	case NetworkReleaseTrustOwned, NetworkReleaseTrustPreexistingUnowned:
		return nil
	default:
		return fmt.Errorf("network release trust disposition %q is unsupported", disposition)
	}
}

// NetworkReleaseTrustPublicationDisposition identifies whether trust capability publication completed durably.
type NetworkReleaseTrustPublicationDisposition string

const (
	// NetworkReleaseTrustPublicationNotRequired means preservation does not issue a helper capability.
	NetworkReleaseTrustPublicationNotRequired NetworkReleaseTrustPublicationDisposition = "not_required"
	// NetworkReleaseTrustPublicationDurable means the capability publisher confirmed durable storage.
	NetworkReleaseTrustPublicationDurable NetworkReleaseTrustPublicationDisposition = "durable"
	// NetworkReleaseTrustPublicationIndeterminate means the returned reference is the only capability that may be reconciled.
	NetworkReleaseTrustPublicationIndeterminate NetworkReleaseTrustPublicationDisposition = "indeterminate"
)

// Validate rejects publication states that would let callers infer unsafe retry behavior.
func (disposition NetworkReleaseTrustPublicationDisposition) Validate() error {
	switch disposition {
	case NetworkReleaseTrustPublicationNotRequired,
		NetworkReleaseTrustPublicationDurable,
		NetworkReleaseTrustPublicationIndeterminate:
		return nil
	default:
		return fmt.Errorf("network release trust publication disposition %q is unsupported", disposition)
	}
}

// NetworkReleaseTrustApprovalTicket is non-secret launch metadata for one trust release.
type NetworkReleaseTrustApprovalTicket struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// Reference identifies the opaque helper capability.
	Reference helper.TicketReference `json:"reference"`
	// Operation fixes the helper action to trust release.
	Operation helper.Operation `json:"operation"`
	// PolicyFingerprint binds the ticket to the retained network policy.
	PolicyFingerprint string `json:"policy_fingerprint"`
	// TargetOwnershipFingerprint binds the ticket to the retained ownership target.
	TargetOwnershipFingerprint string `json:"target_ownership_fingerprint"`
	// AuthorityFingerprint binds the ticket to the public trust authority being released.
	AuthorityFingerprint string `json:"authority_fingerprint"`
	// Mechanism identifies the supported platform trust mechanism.
	Mechanism networkpolicy.TrustMechanism `json:"mechanism"`
	// ExpiresAt limits the lifetime of the helper capability.
	ExpiresAt time.Time `json:"expires_at"`
}

// Validate reports whether ticket metadata can launch only the selected trust release.
func (ticket NetworkReleaseTrustApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationReleaseTrust {
		return fmt.Errorf("network release trust operation is %q, expected %q", ticket.Operation, helper.OperationReleaseTrust)
	}
	for name, fingerprint := range map[string]string{
		"authority":        ticket.AuthorityFingerprint,
		"policy":           ticket.PolicyFingerprint,
		"target ownership": ticket.TargetOwnershipFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network release trust %s fingerprint is invalid", name)
		}
	}
	if !validNetworkDataPlaneTrustMechanism(ticket.Mechanism) {
		return fmt.Errorf("network release trust mechanism %q is unsupported", ticket.Mechanism)
	}
	return validateNetworkReleaseTrustExpiry(ticket.ExpiresAt)
}

// validateNetworkReleaseTrustExpiry rejects ambiguous helper ticket lifetimes before callers redeem them.
func validateNetworkReleaseTrustExpiry(expiresAt time.Time) error {
	if expiresAt.IsZero() || expiresAt.Location() != time.UTC {
		return errors.New("network release trust expiry must be a nonzero UTC time")
	}
	return nil
}

// NetworkReleaseTrustApprovalPreparation reports whether one trust capability is required for an exact release checkpoint.
type NetworkReleaseTrustApprovalPreparation struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// CheckpointRevision identifies the retained release checkpoint.
	CheckpointRevision domain.Sequence `json:"checkpoint_revision"`
	// Disposition identifies whether Harbor owns the trust entry.
	Disposition NetworkReleaseTrustDisposition `json:"disposition"`
	// PublicationDisposition tells the caller whether to redeem or reconcile the returned reference without reissuing.
	PublicationDisposition NetworkReleaseTrustPublicationDisposition `json:"publication_disposition"`
	// Ticket supplies trust-release helper capability only for owned trust entries.
	Ticket *NetworkReleaseTrustApprovalTicket `json:"ticket"`
}

// Validate reports whether preparation honors the retained ownership and publication invariants.
func (preparation NetworkReleaseTrustApprovalPreparation) Validate() error {
	if err := validateNetworkReleaseApprovalSelection(preparation.OperationID, preparation.CheckpointRevision); err != nil {
		return err
	}
	if err := preparation.Disposition.Validate(); err != nil {
		return err
	}
	if err := preparation.PublicationDisposition.Validate(); err != nil {
		return err
	}
	switch preparation.Disposition {
	case NetworkReleaseTrustOwned:
		if preparation.PublicationDisposition != NetworkReleaseTrustPublicationDurable &&
			preparation.PublicationDisposition != NetworkReleaseTrustPublicationIndeterminate {
			return errors.New("owned network release trust preparation must publish a ticket")
		}
		if preparation.Ticket == nil {
			return errors.New("owned network release trust preparation has no ticket")
		}
		if err := preparation.Ticket.Validate(); err != nil {
			return err
		}
		if preparation.Ticket.OperationID != preparation.OperationID {
			return errors.New("network release trust ticket belongs to another operation")
		}
	case NetworkReleaseTrustPreexistingUnowned:
		if preparation.PublicationDisposition != NetworkReleaseTrustPublicationNotRequired {
			return errors.New("preexisting network release trust preparation must not publish a ticket")
		}
		if preparation.Ticket != nil {
			return errors.New("preexisting network release trust preparation has a ticket")
		}
	}
	return nil
}

// ConfirmNetworkReleaseTrustApprovalRequest selects one release checkpoint and optionally supplies trust-release evidence.
type ConfirmNetworkReleaseTrustApprovalRequest struct {
	// OperationID identifies the durable global release operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedCheckpointRevision fences confirmation to one retained release checkpoint.
	ExpectedCheckpointRevision domain.Sequence `json:"expected_checkpoint_revision"`
	// TrustEvidence proves owned trust absence when a helper mutation was required.
	TrustEvidence *helper.TrustMutationEvidence `json:"trust_evidence"`
}

// Validate reports whether confirmation carries an optional owned-absent trust-release postcondition.
func (request ConfirmNetworkReleaseTrustApprovalRequest) Validate() error {
	if err := validateNetworkReleaseApprovalSelection(request.OperationID, request.ExpectedCheckpointRevision); err != nil {
		return err
	}
	if request.TrustEvidence == nil {
		return nil
	}
	return validateNetworkReleaseTrustEvidence(*request.TrustEvidence)
}

// validateNetworkReleaseTrustEvidence confines confirmation to a completed owned-absent trust release.
func validateNetworkReleaseTrustEvidence(evidence helper.TrustMutationEvidence) error {
	for name, fingerprint := range map[string]string{
		"authority":   evidence.AuthorityFingerprint,
		"observation": evidence.ObservationFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network release trust evidence %s fingerprint is invalid", name)
		}
	}
	if !validNetworkDataPlaneTrustMechanism(evidence.Mechanism) {
		return fmt.Errorf("network release trust evidence mechanism %q is unsupported", evidence.Mechanism)
	}
	if evidence.Postcondition != helper.TrustPostconditionOwnedAbsent {
		return errors.New("network release trust evidence must prove owned absence")
	}
	return nil
}
