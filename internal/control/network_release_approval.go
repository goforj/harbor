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
