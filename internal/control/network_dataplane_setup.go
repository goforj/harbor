package control

import (
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	// networkDataPlaneSetupTrustApprovalPhase is the durable phase that permits trust-ticket issuance.
	networkDataPlaneSetupTrustApprovalPhase = "awaiting trust approval"
	// networkDataPlaneSetupLowPortApprovalPhase is the durable phase that permits paired low-port-ticket issuance.
	networkDataPlaneSetupLowPortApprovalPhase = "awaiting low-port approval"
	// networkDataPlaneSetupCompletedPhase is the terminal phase returned only after trusted ingress activation.
	networkDataPlaneSetupCompletedPhase = "completed"
)

// StartNetworkDataPlaneSetupRequest identifies one idempotent machine-global trusted-ingress setup intent.
type StartNetworkDataPlaneSetupRequest struct {
	IntentID domain.IntentID `json:"intent_id"`
}

// Validate reports whether the request contains one stable data-plane setup intent.
func (request StartNetworkDataPlaneSetupRequest) Validate() error {
	return request.IntentID.Validate()
}

// ReadNetworkDataPlaneSetupRequest selects one daemon-owned data-plane setup operation.
type ReadNetworkDataPlaneSetupRequest struct {
	OperationID domain.OperationID `json:"operation_id"`
}

// Validate reports whether the read identifies one durable operation.
func (request ReadNetworkDataPlaneSetupRequest) Validate() error {
	return request.OperationID.Validate()
}

// NetworkDataPlaneSetupOperation reports one durable machine-global trusted-ingress operation revision.
type NetworkDataPlaneSetupOperation struct {
	Operation domain.Operation `json:"operation"`
	Revision  domain.Sequence  `json:"revision"`
}

// RequiresNetworkDataPlaneLowPortApproval reports whether an operation is in the only state that may authorize low-port setup.
func RequiresNetworkDataPlaneLowPortApproval(snapshot NetworkDataPlaneSetupOperation) bool {
	return snapshot.Operation.State == domain.OperationRequiresApproval &&
		snapshot.Operation.Phase == networkDataPlaneSetupLowPortApprovalPhase
}

// Validate reports whether the snapshot is one valid global data-plane setup operation at a bounded revision.
func (snapshot NetworkDataPlaneSetupOperation) Validate() error {
	if err := snapshot.Operation.Validate(); err != nil {
		return err
	}
	if snapshot.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup || snapshot.Operation.ProjectID != "" {
		return errors.New("network data-plane setup operation must be machine-global")
	}
	return validateNetworkDataPlaneSetupRevision(snapshot.Revision)
}

// PrepareNetworkDataPlaneTrustApprovalRequest selects the exact trust-approval revision to authorize.
type PrepareNetworkDataPlaneTrustApprovalRequest struct {
	OperationID               domain.OperationID `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence    `json:"expected_operation_revision"`
}

// Validate reports whether the preparation selects one bounded current operation revision.
func (request PrepareNetworkDataPlaneTrustApprovalRequest) Validate() error {
	return validateNetworkDataPlaneSetupSelection(request.OperationID, request.ExpectedOperationRevision)
}

// NetworkDataPlaneTrustApprovalTicket is the non-secret launch metadata for one exact public-root trust ensure.
type NetworkDataPlaneTrustApprovalTicket struct {
	OperationID                domain.OperationID           `json:"operation_id"`
	Reference                  helper.TicketReference       `json:"reference"`
	Operation                  helper.Operation             `json:"operation"`
	PolicyFingerprint          string                       `json:"policy_fingerprint"`
	TargetOwnershipFingerprint string                       `json:"target_ownership_fingerprint"`
	AuthorityFingerprint       string                       `json:"authority_fingerprint"`
	Mechanism                  networkpolicy.TrustMechanism `json:"mechanism"`
	ExpiresAt                  time.Time                    `json:"expires_at"`
}

// Validate reports whether the ticket metadata can launch only the selected policy-bound trust ensure.
func (ticket NetworkDataPlaneTrustApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationEnsureTrust {
		return fmt.Errorf("network data-plane trust operation is %q, expected %q", ticket.Operation, helper.OperationEnsureTrust)
	}
	for name, fingerprint := range map[string]string{
		"authority":        ticket.AuthorityFingerprint,
		"policy":           ticket.PolicyFingerprint,
		"target ownership": ticket.TargetOwnershipFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network data-plane trust %s fingerprint is invalid", name)
		}
	}
	if !validNetworkDataPlaneTrustMechanism(ticket.Mechanism) {
		return fmt.Errorf("network data-plane trust mechanism %q is unsupported", ticket.Mechanism)
	}
	return validateNetworkDataPlaneSetupExpiry(ticket.ExpiresAt)
}

// NetworkDataPlaneTrustApprovalPreparation reports one trust capability for an exact operation revision.
type NetworkDataPlaneTrustApprovalPreparation struct {
	OperationID       domain.OperationID                  `json:"operation_id"`
	OperationRevision domain.Sequence                     `json:"operation_revision"`
	Ticket            NetworkDataPlaneTrustApprovalTicket `json:"ticket"`
}

// Validate reports whether the preparation and ticket identify the same exact operation revision.
func (preparation NetworkDataPlaneTrustApprovalPreparation) Validate() error {
	if err := validateNetworkDataPlaneSetupSelection(preparation.OperationID, preparation.OperationRevision); err != nil {
		return err
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID {
		return errors.New("network data-plane trust ticket belongs to another operation")
	}
	return nil
}

// ConfirmNetworkDataPlaneTrustApprovalRequest supplies the exact trust postcondition for one operation revision.
type ConfirmNetworkDataPlaneTrustApprovalRequest struct {
	OperationID               domain.OperationID           `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence              `json:"expected_operation_revision"`
	TrustEvidence             helper.TrustMutationEvidence `json:"trust_evidence"`
}

// Validate reports whether the confirmation carries one exact or safely preexisting trust postcondition.
func (request ConfirmNetworkDataPlaneTrustApprovalRequest) Validate() error {
	if err := validateNetworkDataPlaneSetupSelection(request.OperationID, request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkDataPlaneTrustEvidence(request.TrustEvidence)
}

// PrepareNetworkDataPlaneLowPortApprovalRequest selects the exact low-port approval revision to authorize.
type PrepareNetworkDataPlaneLowPortApprovalRequest struct {
	OperationID               domain.OperationID `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence    `json:"expected_operation_revision"`
}

// Validate reports whether the preparation selects one bounded current operation revision.
func (request PrepareNetworkDataPlaneLowPortApprovalRequest) Validate() error {
	return validateNetworkDataPlaneSetupSelection(request.OperationID, request.ExpectedOperationRevision)
}

// NetworkDataPlaneLowPortApprovalTicket is the non-secret launch metadata for the paired low-port ensure.
type NetworkDataPlaneLowPortApprovalTicket struct {
	OperationID                domain.OperationID     `json:"operation_id"`
	Reference                  helper.TicketReference `json:"reference"`
	Operation                  helper.Operation       `json:"operation"`
	PolicyFingerprint          string                 `json:"policy_fingerprint"`
	TargetOwnershipFingerprint string                 `json:"target_ownership_fingerprint"`
	ObservationFingerprint     string                 `json:"observation_fingerprint"`
	ExpiresAt                  time.Time              `json:"expires_at"`
}

// Validate reports whether the ticket metadata can launch only the selected observation-bound low-port ensure.
func (ticket NetworkDataPlaneLowPortApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationEnsureLowPorts {
		return fmt.Errorf("network data-plane low-port operation is %q, expected %q", ticket.Operation, helper.OperationEnsureLowPorts)
	}
	for name, fingerprint := range map[string]string{
		"observation":      ticket.ObservationFingerprint,
		"policy":           ticket.PolicyFingerprint,
		"target ownership": ticket.TargetOwnershipFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network data-plane low-port %s fingerprint is invalid", name)
		}
	}
	return validateNetworkDataPlaneSetupExpiry(ticket.ExpiresAt)
}

// NetworkDataPlaneLowPortApprovalPreparation reports one low-port capability for an exact operation revision.
type NetworkDataPlaneLowPortApprovalPreparation struct {
	OperationID       domain.OperationID                    `json:"operation_id"`
	OperationRevision domain.Sequence                       `json:"operation_revision"`
	Ticket            NetworkDataPlaneLowPortApprovalTicket `json:"ticket"`
}

// Validate reports whether the preparation and ticket identify the same exact operation revision.
func (preparation NetworkDataPlaneLowPortApprovalPreparation) Validate() error {
	if err := validateNetworkDataPlaneSetupSelection(preparation.OperationID, preparation.OperationRevision); err != nil {
		return err
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID {
		return errors.New("network data-plane low-port ticket belongs to another operation")
	}
	return nil
}

// ConfirmNetworkDataPlaneLowPortApprovalRequest supplies the exact paired-listener postcondition for one operation revision.
type ConfirmNetworkDataPlaneLowPortApprovalRequest struct {
	OperationID               domain.OperationID             `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence                `json:"expected_operation_revision"`
	LowPortEvidence           helper.LowPortMutationEvidence `json:"low_port_evidence"`
}

// Validate reports whether the confirmation carries one exact paired low-port postcondition.
func (request ConfirmNetworkDataPlaneLowPortApprovalRequest) Validate() error {
	if err := validateNetworkDataPlaneSetupSelection(request.OperationID, request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkDataPlaneLowPortEvidence(request.LowPortEvidence)
}

// NetworkDataPlaneSetupConfirmation reports terminal trusted-ingress activation and its preceding full-network revision.
type NetworkDataPlaneSetupConfirmation struct {
	Operation       domain.Operation `json:"operation"`
	Revision        domain.Sequence  `json:"revision"`
	NetworkRevision domain.Sequence  `json:"network_revision"`
}

// Validate reports whether confirmation contains one succeeded global setup ordered after its full-network revision.
func (confirmation NetworkDataPlaneSetupConfirmation) Validate() error {
	if err := confirmation.Operation.Validate(); err != nil {
		return err
	}
	if confirmation.Operation.Kind != domain.OperationKindNetworkDataPlaneSetup ||
		confirmation.Operation.ProjectID != "" ||
		confirmation.Operation.State != domain.OperationSucceeded ||
		confirmation.Operation.Phase != networkDataPlaneSetupCompletedPhase {
		return errors.New("network data-plane confirmation must contain a completed global setup operation")
	}
	if err := validateNetworkDataPlaneSetupRevision(confirmation.Revision); err != nil {
		return err
	}
	if err := validateNetworkDataPlaneSetupRevision(confirmation.NetworkRevision); err != nil {
		return err
	}
	if confirmation.Revision <= confirmation.NetworkRevision {
		return errors.New("network data-plane operation revision must follow the full network revision")
	}
	return nil
}

// validateNetworkDataPlaneSetupSelection validates the shared operation and optimistic revision request shape.
func validateNetworkDataPlaneSetupSelection(operationID domain.OperationID, revision domain.Sequence) error {
	if err := operationID.Validate(); err != nil {
		return err
	}
	return validateNetworkDataPlaneSetupRevision(revision)
}

// validateNetworkDataPlaneSetupRevision keeps control revisions exactly representable by every supported client.
func validateNetworkDataPlaneSetupRevision(revision domain.Sequence) error {
	if revision == 0 || revision > domain.MaximumSequence {
		return fmt.Errorf("network data-plane setup revision must be between 1 and %d", domain.MaximumSequence)
	}
	return nil
}

// validateNetworkDataPlaneSetupExpiry rejects ambiguous or noncanonical helper ticket lifetimes.
func validateNetworkDataPlaneSetupExpiry(expiresAt time.Time) error {
	if expiresAt.IsZero() || expiresAt.Location() != time.UTC {
		return errors.New("network data-plane helper expiry must be a nonzero UTC time")
	}
	return nil
}

// validateNetworkDataPlaneTrustEvidence confines confirmation to one public-root ensure result.
func validateNetworkDataPlaneTrustEvidence(evidence helper.TrustMutationEvidence) error {
	if !validNetworkDataPlaneSetupFingerprint(evidence.AuthorityFingerprint) {
		return errors.New("network data-plane trust authority fingerprint is invalid")
	}
	if !validNetworkDataPlaneSetupFingerprint(evidence.ObservationFingerprint) {
		return errors.New("network data-plane trust observation fingerprint is invalid")
	}
	if !validNetworkDataPlaneTrustMechanism(evidence.Mechanism) {
		return fmt.Errorf("network data-plane trust mechanism %q is unsupported", evidence.Mechanism)
	}
	if evidence.Postcondition != helper.TrustPostconditionExact && evidence.Postcondition != helper.TrustPostconditionPreexisting {
		return errors.New("network data-plane trust evidence must prove an exact or safely preexisting public root")
	}
	return nil
}

// validateNetworkDataPlaneLowPortEvidence confines confirmation to one exact policy-bound paired listener result.
func validateNetworkDataPlaneLowPortEvidence(evidence helper.LowPortMutationEvidence) error {
	for name, fingerprint := range map[string]string{
		"observation": evidence.ObservationFingerprint,
		"ownership":   evidence.OwnershipFingerprint,
		"policy":      evidence.PolicyFingerprint,
	} {
		if !validNetworkDataPlaneSetupFingerprint(fingerprint) {
			return fmt.Errorf("network data-plane low-port %s fingerprint is invalid", name)
		}
	}
	if evidence.Postcondition != helper.LowPortPostconditionExact {
		return errors.New("network data-plane low-port evidence must prove the exact paired listener policy")
	}
	return nil
}

// validNetworkDataPlaneTrustMechanism accepts only complete supported platform trust profiles.
func validNetworkDataPlaneTrustMechanism(mechanism networkpolicy.TrustMechanism) bool {
	switch mechanism {
	case networkpolicy.DarwinCurrentUserTrust,
		networkpolicy.UbuntuSystemTrust,
		networkpolicy.WindowsCurrentUserTrust:
		return true
	default:
		return false
	}
}

// validNetworkDataPlaneSetupFingerprint accepts the canonical lowercase SHA-256 form shared with helper transport.
func validNetworkDataPlaneSetupFingerprint(value string) bool {
	return validNetworkResolverSetupFingerprint(value)
}
