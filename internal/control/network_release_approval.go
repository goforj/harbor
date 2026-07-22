package control

import (
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
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
