package control

import (
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

const networkResolverPolicyMigrationApprovalPhase = "awaiting resolver policy migration approval"

// StartNetworkResolverPolicyMigrationRequest identifies one idempotent legacy resolver-policy retirement intent.
type StartNetworkResolverPolicyMigrationRequest struct {
	// IntentID identifies one client-stable migration intent.
	IntentID domain.IntentID `json:"intent_id"`
}

// Validate reports whether the request contains one stable migration intent.
func (request StartNetworkResolverPolicyMigrationRequest) Validate() error {
	return request.IntentID.Validate()
}

// NetworkResolverPolicyMigrationOperation reports one durable global legacy resolver-policy migration operation.
type NetworkResolverPolicyMigrationOperation struct {
	// Operation is the durable daemon-owned migration operation.
	Operation domain.Operation `json:"operation"`
	// Revision is the durable operation revision.
	Revision domain.Sequence `json:"revision"`
}

// Validate reports whether the operation is at the migration approval checkpoint or its exact completed replay state.
func (operation NetworkResolverPolicyMigrationOperation) Validate() error {
	if err := operation.Operation.Validate(); err != nil {
		return err
	}
	if operation.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration || operation.Operation.ProjectID != "" {
		return errors.New("network resolver policy migration operation must be global")
	}
	switch operation.Operation.State {
	case domain.OperationRequiresApproval:
		if operation.Operation.Phase != networkResolverPolicyMigrationApprovalPhase {
			return errors.New("network resolver policy migration approval operation has an invalid phase")
		}
	case domain.OperationSucceeded:
		if operation.Operation.Phase != "completed" {
			return errors.New("network resolver policy migration succeeded operation has an invalid phase")
		}
	default:
		return errors.New("network resolver policy migration operation must be awaiting approval or completed")
	}
	return validateNetworkResolverPolicyMigrationRevision(operation.Revision)
}

// PrepareNetworkResolverPolicyMigrationApprovalRequest selects the exact migration approval revision.
type PrepareNetworkResolverPolicyMigrationApprovalRequest struct {
	// OperationID identifies the durable migration operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedOperationRevision fences preparation to one approval checkpoint.
	ExpectedOperationRevision domain.Sequence `json:"expected_operation_revision"`
}

// Validate reports whether the request identifies one bounded migration approval checkpoint.
func (request PrepareNetworkResolverPolicyMigrationApprovalRequest) Validate() error {
	return validateNetworkResolverPolicyMigrationSelection(request.OperationID, request.ExpectedOperationRevision)
}

// NetworkResolverPolicyMigrationApprovalTicket is non-secret launch metadata for one legacy resolver retirement.
type NetworkResolverPolicyMigrationApprovalTicket struct {
	// OperationID identifies the durable migration operation.
	OperationID domain.OperationID `json:"operation_id"`
	// Reference identifies the opaque helper capability.
	Reference helper.TicketReference `json:"reference"`
	// Operation fixes helper authority to legacy resolver retirement.
	Operation helper.Operation `json:"operation"`
	// PolicyFingerprint binds the ticket to the legacy policy.
	PolicyFingerprint string `json:"policy_fingerprint"`
	// PostOwnershipFingerprint binds confirmation to the exact schema-one ownership record left after retirement.
	PostOwnershipFingerprint string `json:"post_ownership_fingerprint"`
	// ExpiresAt limits the helper capability lifetime.
	ExpiresAt time.Time `json:"expires_at"`
}

// Validate reports whether ticket metadata can retire only the selected legacy resolver policy.
func (ticket NetworkResolverPolicyMigrationApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationRetireResolver {
		return fmt.Errorf("network resolver policy migration helper operation is %q, expected %q", ticket.Operation, helper.OperationRetireResolver)
	}
	if !validNetworkResolverSetupFingerprint(ticket.PolicyFingerprint) || !validNetworkResolverSetupFingerprint(ticket.PostOwnershipFingerprint) {
		return errors.New("network resolver policy migration ticket fingerprint is invalid")
	}
	if ticket.ExpiresAt.IsZero() || ticket.ExpiresAt.Location() != time.UTC {
		return errors.New("network resolver policy migration helper expiry must be a nonzero UTC time")
	}
	return nil
}

// NetworkResolverPolicyMigrationPublicationDisposition identifies whether ticket publication completed durably or must be reconciled by reference.
type NetworkResolverPolicyMigrationPublicationDisposition string

const (
	// NetworkResolverPolicyMigrationPublicationDurable means capability publication was durably confirmed.
	NetworkResolverPolicyMigrationPublicationDurable NetworkResolverPolicyMigrationPublicationDisposition = "durable"
	// NetworkResolverPolicyMigrationPublicationIndeterminate means only the returned reference may be reconciled without reissue.
	NetworkResolverPolicyMigrationPublicationIndeterminate NetworkResolverPolicyMigrationPublicationDisposition = "indeterminate"
)

// Validate reports whether the disposition preserves safe ticket retry semantics.
func (disposition NetworkResolverPolicyMigrationPublicationDisposition) Validate() error {
	switch disposition {
	case NetworkResolverPolicyMigrationPublicationDurable, NetworkResolverPolicyMigrationPublicationIndeterminate:
		return nil
	default:
		return fmt.Errorf("network resolver policy migration publication disposition %q is unsupported", disposition)
	}
}

// NetworkResolverPolicyMigrationApprovalPreparation reports one helper capability for an exact migration approval checkpoint.
type NetworkResolverPolicyMigrationApprovalPreparation struct {
	// OperationID identifies the durable migration operation.
	OperationID domain.OperationID `json:"operation_id"`
	// OperationRevision identifies the selected approval checkpoint.
	OperationRevision domain.Sequence `json:"operation_revision"`
	// PublicationDisposition tells callers whether to redeem or reconcile the returned reference.
	PublicationDisposition NetworkResolverPolicyMigrationPublicationDisposition `json:"publication_disposition"`
	// Ticket supplies the resolver retirement helper capability.
	Ticket NetworkResolverPolicyMigrationApprovalTicket `json:"ticket"`
}

// Validate reports whether preparation and ticket identify the same bounded migration operation.
func (preparation NetworkResolverPolicyMigrationApprovalPreparation) Validate() error {
	if err := validateNetworkResolverPolicyMigrationSelection(preparation.OperationID, preparation.OperationRevision); err != nil {
		return err
	}
	if err := preparation.PublicationDisposition.Validate(); err != nil {
		return err
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID {
		return errors.New("network resolver policy migration ticket belongs to another operation")
	}
	return nil
}

// ConfirmNetworkResolverPolicyMigrationApprovalRequest selects one migration checkpoint and supplies retirement evidence.
type ConfirmNetworkResolverPolicyMigrationApprovalRequest struct {
	// OperationID identifies the durable migration operation.
	OperationID domain.OperationID `json:"operation_id"`
	// ExpectedOperationRevision fences confirmation to one approval checkpoint.
	ExpectedOperationRevision domain.Sequence `json:"expected_operation_revision"`
	// ResolverEvidence proves the owned legacy resolver policy is absent.
	ResolverEvidence helper.ResolverMutationEvidence `json:"resolver_evidence"`
}

// Validate reports whether confirmation proves the legacy owned resolver policy is absent.
func (request ConfirmNetworkResolverPolicyMigrationApprovalRequest) Validate() error {
	if err := validateNetworkResolverPolicyMigrationSelection(request.OperationID, request.ExpectedOperationRevision); err != nil {
		return err
	}
	if request.ResolverEvidence.Postcondition != helper.ResolverPostconditionOwnedAbsent {
		return errors.New("network resolver policy migration evidence must prove owned absence")
	}
	if !validNetworkResolverSetupFingerprint(request.ResolverEvidence.PolicyFingerprint) ||
		!validNetworkResolverSetupFingerprint(request.ResolverEvidence.OwnershipFingerprint) ||
		!validNetworkResolverSetupFingerprint(request.ResolverEvidence.ObservationFingerprint) {
		return errors.New("network resolver policy migration evidence fingerprint is invalid")
	}
	return nil
}

// NetworkResolverPolicyMigrationApprovalConfirmation reports the succeeded retirement and its contiguous network revision.
type NetworkResolverPolicyMigrationApprovalConfirmation struct {
	// Operation is the succeeded durable migration operation.
	Operation domain.Operation `json:"operation"`
	// Revision is the terminal operation revision.
	Revision domain.Sequence `json:"revision"`
	// NetworkRevision is the contiguous identity-stage revision.
	NetworkRevision domain.Sequence `json:"network_revision"`
}

// Validate reports whether confirmation is one succeeded global retirement with contiguous revisions.
func (confirmation NetworkResolverPolicyMigrationApprovalConfirmation) Validate() error {
	if err := confirmation.Operation.Validate(); err != nil {
		return err
	}
	if confirmation.Operation.Kind != domain.OperationKindNetworkResolverPolicyMigration || confirmation.Operation.ProjectID != "" || confirmation.Operation.State != domain.OperationSucceeded || confirmation.Operation.Phase != "completed" {
		return errors.New("network resolver policy migration confirmation must contain a succeeded global migration")
	}
	if err := validateNetworkResolverPolicyMigrationRevision(confirmation.Revision); err != nil {
		return err
	}
	if err := validateNetworkResolverPolicyMigrationRevision(confirmation.NetworkRevision); err != nil {
		return err
	}
	if confirmation.Revision != confirmation.NetworkRevision+1 {
		return errors.New("network resolver policy migration operation revision must immediately follow the network revision")
	}
	return nil
}

// validateNetworkResolverPolicyMigrationSelection validates one operation and optimistic approval revision.
func validateNetworkResolverPolicyMigrationSelection(operationID domain.OperationID, revision domain.Sequence) error {
	if err := operationID.Validate(); err != nil {
		return err
	}
	return validateNetworkResolverPolicyMigrationRevision(revision)
}

// validateNetworkResolverPolicyMigrationRevision keeps migration revisions representable by every supported client.
func validateNetworkResolverPolicyMigrationRevision(revision domain.Sequence) error {
	if revision == 0 || revision > domain.MaximumSequence {
		return fmt.Errorf("network resolver policy migration revision must be between 1 and %d", domain.MaximumSequence)
	}
	return nil
}
