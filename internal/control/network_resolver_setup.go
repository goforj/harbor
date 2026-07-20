package control

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

const networkResolverSetupFingerprintBytes = 32

// StartNetworkResolverSetupRequest identifies one idempotent machine-global resolver setup intent.
type StartNetworkResolverSetupRequest struct {
	IntentID domain.IntentID `json:"intent_id"`
}

// Validate reports whether the request contains a stable resolver setup intent identity.
func (request StartNetworkResolverSetupRequest) Validate() error {
	return request.IntentID.Validate()
}

// NetworkResolverSetupOperation reports one durable machine-global resolver setup operation revision.
type NetworkResolverSetupOperation struct {
	Operation domain.Operation `json:"operation"`
	Revision  domain.Sequence  `json:"revision"`
}

// Validate reports whether the operation is a valid global resolver setup snapshot at a bounded revision.
func (snapshot NetworkResolverSetupOperation) Validate() error {
	if err := snapshot.Operation.Validate(); err != nil {
		return err
	}
	if snapshot.Operation.Kind != domain.OperationKindNetworkResolverSetup || snapshot.Operation.ProjectID != "" {
		return errors.New("network resolver setup operation must be machine-global")
	}
	return validateNetworkResolverSetupRevision(snapshot.Revision)
}

// PrepareNetworkResolverSetupApprovalRequest selects one exact operation revision for helper authorization.
type PrepareNetworkResolverSetupApprovalRequest struct {
	OperationID               domain.OperationID `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence    `json:"expected_operation_revision"`
}

// Validate reports whether the preparation request identifies one bounded current operation revision.
func (request PrepareNetworkResolverSetupApprovalRequest) Validate() error {
	return validateNetworkResolverSetupApprovalSelection(request.OperationID, request.ExpectedOperationRevision)
}

// NetworkResolverSetupApprovalTicket is the non-secret launch metadata for one resolver ensure.
type NetworkResolverSetupApprovalTicket struct {
	OperationID                domain.OperationID     `json:"operation_id"`
	Reference                  helper.TicketReference `json:"reference"`
	Operation                  helper.Operation       `json:"operation"`
	PolicyFingerprint          string                 `json:"policy_fingerprint"`
	TargetOwnershipFingerprint string                 `json:"target_ownership_fingerprint"`
	ExpiresAt                  time.Time              `json:"expires_at"`
}

// Validate reports whether helper metadata is canonical and limited to one resolver ensure.
func (ticket NetworkResolverSetupApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationEnsureResolver {
		return fmt.Errorf(
			"network resolver setup helper operation is %q, expected %q",
			ticket.Operation,
			helper.OperationEnsureResolver,
		)
	}
	if !validNetworkResolverSetupFingerprint(ticket.PolicyFingerprint) {
		return errors.New("network resolver setup policy fingerprint is invalid")
	}
	if !validNetworkResolverSetupFingerprint(ticket.TargetOwnershipFingerprint) {
		return errors.New("network resolver setup target ownership fingerprint is invalid")
	}
	if ticket.ExpiresAt.IsZero() || ticket.ExpiresAt.Location() != time.UTC {
		return errors.New("network resolver setup helper expiry must be a nonzero UTC time")
	}
	return nil
}

// NetworkResolverSetupApprovalPreparation reports one helper capability for an exact resolver setup operation revision.
type NetworkResolverSetupApprovalPreparation struct {
	OperationID       domain.OperationID                 `json:"operation_id"`
	OperationRevision domain.Sequence                    `json:"operation_revision"`
	Ticket            NetworkResolverSetupApprovalTicket `json:"ticket"`
}

// Validate reports whether the preparation and helper ticket identify the same bounded operation.
func (preparation NetworkResolverSetupApprovalPreparation) Validate() error {
	if err := validateNetworkResolverSetupApprovalSelection(preparation.OperationID, preparation.OperationRevision); err != nil {
		return err
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID {
		return errors.New("network resolver setup helper ticket belongs to another operation")
	}
	return nil
}

// ConfirmNetworkResolverSetupApprovalRequest selects one operation revision and supplies its resolver postcondition.
type ConfirmNetworkResolverSetupApprovalRequest struct {
	OperationID               domain.OperationID              `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence                 `json:"expected_operation_revision"`
	ResolverEvidence          helper.ResolverMutationEvidence `json:"resolver_evidence"`
}

// Validate reports whether the confirmation identifies one revision and one exact resolver ensure postcondition.
func (request ConfirmNetworkResolverSetupApprovalRequest) Validate() error {
	if err := validateNetworkResolverSetupApprovalSelection(request.OperationID, request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkResolverSetupEvidence(request.ResolverEvidence)
}

// NetworkResolverSetupApprovalConfirmation reports the succeeded operation and contiguous resolver network revision.
type NetworkResolverSetupApprovalConfirmation struct {
	Operation       domain.Operation `json:"operation"`
	Revision        domain.Sequence  `json:"revision"`
	NetworkRevision domain.Sequence  `json:"network_revision"`
}

// Validate reports whether confirmation contains one succeeded global resolver setup and its contiguous network revision.
func (confirmation NetworkResolverSetupApprovalConfirmation) Validate() error {
	if err := confirmation.Operation.Validate(); err != nil {
		return err
	}
	if confirmation.Operation.Kind != domain.OperationKindNetworkResolverSetup ||
		confirmation.Operation.ProjectID != "" ||
		confirmation.Operation.State != domain.OperationSucceeded {
		return errors.New("approval confirmation must contain a succeeded global network resolver setup operation")
	}
	if err := validateNetworkResolverSetupRevision(confirmation.Revision); err != nil {
		return err
	}
	if err := validateNetworkResolverSetupRevision(confirmation.NetworkRevision); err != nil {
		return err
	}
	if confirmation.Revision != confirmation.NetworkRevision+1 {
		return errors.New("network resolver setup operation revision must immediately follow the resolver network revision")
	}
	return nil
}

// validateNetworkResolverSetupApprovalSelection validates the shared operation and optimistic revision request shape.
func validateNetworkResolverSetupApprovalSelection(operationID domain.OperationID, revision domain.Sequence) error {
	if err := operationID.Validate(); err != nil {
		return err
	}
	return validateNetworkResolverSetupRevision(revision)
}

// validateNetworkResolverSetupRevision keeps control revisions exactly representable by every supported client.
func validateNetworkResolverSetupRevision(revision domain.Sequence) error {
	if revision == 0 || revision > domain.MaximumSequence {
		return fmt.Errorf("network resolver setup revision must be between 1 and %d", domain.MaximumSequence)
	}
	return nil
}

// validateNetworkResolverSetupEvidence confines confirmation to one exact policy-bound resolver ensure result.
func validateNetworkResolverSetupEvidence(evidence helper.ResolverMutationEvidence) error {
	if !validNetworkResolverSetupFingerprint(evidence.PolicyFingerprint) {
		return errors.New("network resolver setup evidence policy fingerprint is invalid")
	}
	if !validNetworkResolverSetupFingerprint(evidence.OwnershipFingerprint) {
		return errors.New("network resolver setup evidence ownership fingerprint is invalid")
	}
	if !validNetworkResolverSetupFingerprint(evidence.ObservationFingerprint) {
		return errors.New("network resolver setup evidence observation fingerprint is invalid")
	}
	if evidence.Postcondition != helper.ResolverPostconditionExact {
		return errors.New("network resolver setup evidence must prove the exact resolver policy")
	}
	return nil
}

// validNetworkResolverSetupFingerprint accepts the canonical lowercase SHA-256 representation used across helper boundaries.
func validNetworkResolverSetupFingerprint(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil &&
		len(decoded) == networkResolverSetupFingerprintBytes &&
		hex.EncodeToString(decoded) == value
}
