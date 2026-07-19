package control

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/network/identity"
)

const maximumProjectUnregisterApprovalLeases = 1<<16 - 1

// PrepareProjectUnregisterApprovalRequest selects one exact operation revision for interactive helper authorization.
type PrepareProjectUnregisterApprovalRequest struct {
	OperationID               domain.OperationID `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence    `json:"expected_operation_revision"`
}

// Validate reports whether the preparation request identifies one bounded current operation revision.
func (request PrepareProjectUnregisterApprovalRequest) Validate() error {
	return validateProjectUnregisterApprovalSelection(
		request.OperationID,
		request.ExpectedOperationRevision,
	)
}

// ConfirmProjectUnregisterApprovalRequest selects the operation revision whose host effects must be independently verified.
type ConfirmProjectUnregisterApprovalRequest struct {
	OperationID               domain.OperationID `json:"operation_id"`
	ExpectedOperationRevision domain.Sequence    `json:"expected_operation_revision"`
}

// Validate reports whether the confirmation request identifies one bounded current operation revision.
func (request ConfirmProjectUnregisterApprovalRequest) Validate() error {
	return validateProjectUnregisterApprovalSelection(
		request.OperationID,
		request.ExpectedOperationRevision,
	)
}

// HelperApprovalLeaseKey identifies the exact project assignment selected by one opaque helper capability.
type HelperApprovalLeaseKey struct {
	ProjectID   domain.ProjectID `json:"project_id"`
	SecondaryID string           `json:"secondary_id,omitempty"`
}

// Validate reports whether the helper lease key can select one stable primary or secondary assignment.
func (key HelperApprovalLeaseKey) Validate() error {
	return identity.LeaseKey{ProjectID: key.ProjectID, SecondaryID: key.SecondaryID}.Validate()
}

// HelperApprovalTicket is the non-secret launch metadata returned for one short-lived privileged effect.
type HelperApprovalTicket struct {
	OperationID domain.OperationID     `json:"operation_id"`
	LeaseKey    HelperApprovalLeaseKey `json:"lease_key"`
	Reference   helper.TicketReference `json:"reference"`
	Operation   helper.Operation       `json:"operation"`
	Address     string                 `json:"address"`
	ExpiresAt   time.Time              `json:"expires_at"`
}

// Validate reports whether helper metadata is canonical and limited to project-unregister release authority.
func (ticket HelperApprovalTicket) Validate() error {
	if err := ticket.OperationID.Validate(); err != nil {
		return err
	}
	if err := ticket.LeaseKey.Validate(); err != nil {
		return err
	}
	if err := ticket.Reference.Validate(); err != nil {
		return err
	}
	if ticket.Operation != helper.OperationReleaseLoopbackIdentity {
		return fmt.Errorf("project unregister helper operation is %q, expected %q", ticket.Operation, helper.OperationReleaseLoopbackIdentity)
	}
	address, err := netip.ParseAddr(ticket.Address)
	if err != nil || !address.Is4() || !address.IsLoopback() || address != address.Unmap() || ticket.Address != address.String() {
		return errors.New("project unregister helper address must be canonical IPv4 loopback")
	}
	if ticket.ExpiresAt.IsZero() || ticket.ExpiresAt.Location() != time.UTC {
		return errors.New("project unregister helper expiry must be a nonzero UTC time")
	}
	return nil
}

// ProjectUnregisterApprovalPreparation reports exact release progress and at most one interactive helper capability.
type ProjectUnregisterApprovalPreparation struct {
	OperationID       domain.OperationID    `json:"operation_id"`
	OperationRevision domain.Sequence       `json:"operation_revision"`
	ProjectID         domain.ProjectID      `json:"project_id"`
	TotalLeases       int                   `json:"total_leases"`
	ReleasedLeases    int                   `json:"released_leases"`
	PendingLeases     int                   `json:"pending_leases"`
	Ticket            *HelperApprovalTicket `json:"ticket,omitempty"`
}

// Validate reports whether release counts and optional launch authority describe one coherent operation revision.
func (preparation ProjectUnregisterApprovalPreparation) Validate() error {
	if err := validateProjectUnregisterApprovalSelection(preparation.OperationID, preparation.OperationRevision); err != nil {
		return err
	}
	if err := preparation.ProjectID.Validate(); err != nil {
		return err
	}
	if preparation.TotalLeases <= 0 || preparation.TotalLeases > maximumProjectUnregisterApprovalLeases {
		return fmt.Errorf("project unregister approval lease count must be between 1 and %d", maximumProjectUnregisterApprovalLeases)
	}
	if preparation.ReleasedLeases < 0 || preparation.PendingLeases < 0 ||
		preparation.ReleasedLeases+preparation.PendingLeases != preparation.TotalLeases {
		return errors.New("project unregister approval lease counts are inconsistent")
	}
	if preparation.PendingLeases == 0 {
		if preparation.Ticket != nil {
			return errors.New("completed project unregister approval must not contain a helper ticket")
		}
		return nil
	}
	if preparation.Ticket == nil {
		return errors.New("pending project unregister approval requires one helper ticket")
	}
	if err := preparation.Ticket.Validate(); err != nil {
		return err
	}
	if preparation.Ticket.OperationID != preparation.OperationID ||
		preparation.Ticket.LeaseKey.ProjectID != preparation.ProjectID {
		return errors.New("project unregister helper ticket belongs to another operation or project")
	}
	return nil
}

// ProjectUnregisterApprovalConfirmation reports the succeeded durable operation after daemon-side host verification.
type ProjectUnregisterApprovalConfirmation struct {
	Operation domain.Operation `json:"operation"`
	Revision  domain.Sequence  `json:"revision"`
}

// Validate reports whether confirmation contains one succeeded project-unregister operation and bounded revision.
func (confirmation ProjectUnregisterApprovalConfirmation) Validate() error {
	if err := confirmation.Operation.Validate(); err != nil {
		return err
	}
	if confirmation.Operation.Kind != domain.OperationKindProjectUnregister ||
		confirmation.Operation.State != domain.OperationSucceeded {
		return errors.New("approval confirmation must contain a succeeded project unregister operation")
	}
	return validateProjectUnregisterApprovalRevision(confirmation.Revision)
}

// validateProjectUnregisterApprovalSelection validates the shared operation and optimistic revision request shape.
func validateProjectUnregisterApprovalSelection(operationID domain.OperationID, revision domain.Sequence) error {
	if err := operationID.Validate(); err != nil {
		return err
	}
	return validateProjectUnregisterApprovalRevision(revision)
}

// validateProjectUnregisterApprovalRevision keeps control revisions exactly representable by every supported client.
func validateProjectUnregisterApprovalRevision(revision domain.Sequence) error {
	if revision == 0 || revision > domain.MaximumSequence {
		return fmt.Errorf("project unregister approval revision must be between 1 and %d", domain.MaximumSequence)
	}
	return nil
}
