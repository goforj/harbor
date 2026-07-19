package control

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
)

const (
	networkSetupPoolPrefixBits = 29
	networkSetupPoolAddresses  = 1 << (32 - networkSetupPoolPrefixBits)
)

// StartNetworkSetupRequest identifies one idempotent machine-global network setup intent.
type StartNetworkSetupRequest struct {
	IntentID domain.IntentID `json:"intent_id"`
}

// Validate reports whether the request contains a stable network setup intent identity.
func (request StartNetworkSetupRequest) Validate() error {
	return request.IntentID.Validate()
}

// NetworkSetupOperation reports one durable machine-global network setup operation revision.
type NetworkSetupOperation struct {
	Operation domain.Operation `json:"operation"`
	Revision  domain.Sequence  `json:"revision"`
}

// Validate reports whether the operation is a valid global network setup snapshot at a bounded revision.
func (snapshot NetworkSetupOperation) Validate() error {
	if err := snapshot.Operation.Validate(); err != nil {
		return err
	}
	if snapshot.Operation.Kind != domain.OperationKindNetworkSetup || snapshot.Operation.ProjectID != "" {
		return errors.New("network setup operation must be machine-global")
	}
	return validateNetworkSetupRevision(snapshot.Revision)
}

// PrepareNetworkSetupApprovalRequest selects one exact operation revision for helper authorization.
type PrepareNetworkSetupApprovalRequest struct {
	OperationID               domain.OperationID `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence    `json:"expected_operation_revision"`
}

// Validate reports whether the preparation request identifies one bounded current operation revision.
func (request PrepareNetworkSetupApprovalRequest) Validate() error {
	return validateNetworkSetupApprovalSelection(request.OperationID, request.ExpectedOperationRevision)
}

// NetworkSetupApprovalTicket is the non-secret launch metadata for one complete loopback pool ensure.
type NetworkSetupApprovalTicket struct {
	OperationID domain.OperationID     `json:"operation_id"`
	Reference   helper.TicketReference `json:"reference"`
	Operation   helper.Operation       `json:"operation"`
	Pool        string                 `json:"pool"`
	ExpiresAt   time.Time              `json:"expires_at"`
}

// Validate reports whether helper metadata is canonical and limited to complete loopback pool setup authority.
func (ticket NetworkSetupApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationEnsureLoopbackPool {
		return fmt.Errorf("network setup helper operation is %q, expected %q", ticket.Operation, helper.OperationEnsureLoopbackPool)
	}
	if _, err := parseCanonicalNetworkSetupPool(ticket.Pool); err != nil {
		return err
	}
	if ticket.ExpiresAt.IsZero() || ticket.ExpiresAt.Location() != time.UTC {
		return errors.New("network setup helper expiry must be a nonzero UTC time")
	}
	return nil
}

// NetworkSetupApprovalPreparation reports one helper capability for an exact network setup operation revision.
type NetworkSetupApprovalPreparation struct {
	OperationID       domain.OperationID         `json:"operation_id"`
	OperationRevision domain.Sequence            `json:"operation_revision"`
	Ticket            NetworkSetupApprovalTicket `json:"ticket"`
}

// Validate reports whether the preparation and helper ticket identify the same bounded operation.
func (preparation NetworkSetupApprovalPreparation) Validate() error {
	if err := validateNetworkSetupApprovalSelection(preparation.OperationID, preparation.OperationRevision); err != nil {
		return err
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID {
		return errors.New("network setup helper ticket belongs to another operation")
	}
	return nil
}

// ConfirmNetworkSetupApprovalRequest selects one operation revision and supplies independently observed pool postconditions.
type ConfirmNetworkSetupApprovalRequest struct {
	OperationID               domain.OperationID          `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence             `json:"expected_operation_revision"`
	PoolEvidence              helper.PoolMutationEvidence `json:"pool_evidence"`
}

// Validate reports whether the confirmation identifies one revision and complete owned canonical pool evidence.
func (request ConfirmNetworkSetupApprovalRequest) Validate() error {
	if err := validateNetworkSetupApprovalSelection(request.OperationID, request.ExpectedOperationRevision); err != nil {
		return err
	}
	return validateNetworkSetupPoolEvidence(request.PoolEvidence)
}

// NetworkSetupApprovalConfirmation reports the succeeded operation and contiguous network state revision.
type NetworkSetupApprovalConfirmation struct {
	Operation       domain.Operation `json:"operation"`
	Revision        domain.Sequence  `json:"revision"`
	NetworkRevision domain.Sequence  `json:"network_revision"`
	Pool            string           `json:"pool"`
}

// Validate reports whether confirmation contains one succeeded global setup and its contiguous network revision.
func (confirmation NetworkSetupApprovalConfirmation) Validate() error {
	if err := confirmation.Operation.Validate(); err != nil {
		return err
	}
	if confirmation.Operation.Kind != domain.OperationKindNetworkSetup ||
		confirmation.Operation.ProjectID != "" ||
		confirmation.Operation.State != domain.OperationSucceeded {
		return errors.New("approval confirmation must contain a succeeded global network setup operation")
	}
	if err := validateNetworkSetupRevision(confirmation.Revision); err != nil {
		return err
	}
	if err := validateNetworkSetupRevision(confirmation.NetworkRevision); err != nil {
		return err
	}
	if confirmation.Revision != confirmation.NetworkRevision+1 {
		return errors.New("network setup operation revision must immediately follow the network revision")
	}
	_, err := parseCanonicalNetworkSetupPool(confirmation.Pool)
	return err
}

// validateNetworkSetupApprovalSelection validates the shared operation and optimistic revision request shape.
func validateNetworkSetupApprovalSelection(operationID domain.OperationID, revision domain.Sequence) error {
	if err := operationID.Validate(); err != nil {
		return err
	}
	return validateNetworkSetupRevision(revision)
}

// validateNetworkSetupRevision keeps control revisions exactly representable by every supported client.
func validateNetworkSetupRevision(revision domain.Sequence) error {
	if revision == 0 || revision > domain.MaximumSequence {
		return fmt.Errorf("network setup revision must be between 1 and %d", domain.MaximumSequence)
	}
	return nil
}

// parseCanonicalNetworkSetupPool accepts only one text representation of a complete IPv4 loopback /29.
func parseCanonicalNetworkSetupPool(value string) (netip.Prefix, error) {
	pool, err := netip.ParsePrefix(value)
	if err != nil || !pool.Addr().Is4() || !pool.Addr().IsLoopback() ||
		pool.Bits() != networkSetupPoolPrefixBits || pool != pool.Masked() || pool.String() != value {
		return netip.Prefix{}, errors.New("network setup pool must be a canonical IPv4 loopback /29")
	}
	return pool, nil
}

// validateNetworkSetupPoolEvidence verifies the complete ordered postcondition without expanding helper authority.
func validateNetworkSetupPoolEvidence(evidence helper.PoolMutationEvidence) error {
	pool, err := parseCanonicalNetworkSetupPool(evidence.Pool)
	if err != nil {
		return err
	}
	if len(evidence.Identities) != networkSetupPoolAddresses {
		return fmt.Errorf("network setup pool evidence must contain exactly %d identities", networkSetupPoolAddresses)
	}

	address := pool.Addr()
	for _, identity := range evidence.Identities {
		if identity.Address != address.String() {
			return errors.New("network setup pool evidence identities must enumerate the complete pool in canonical order")
		}
		if err := identity.Observation.Validate(); err != nil {
			return errors.New("network setup pool evidence observation is invalid")
		}
		if identity.Observation.State != helper.ObservationOwned {
			return errors.New("network setup pool evidence observations must be owned")
		}
		address = address.Next()
	}
	return nil
}
