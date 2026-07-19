package launcher

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/network/identity"
)

// LaunchTicket is the immutable non-secret metadata needed to launch one opaque helper capability.
type LaunchTicket struct {
	operationID domain.OperationID
	leaseKey    identity.LeaseKey
	reference   helper.TicketReference
	operation   helper.Operation
	address     netip.Addr
	expiresAt   time.Time
}

// NewLaunchTicket validates and captures metadata from an already authenticated approval response.
// Launcher.Invoke independently applies the trusted-clock lifetime checks immediately before native consent.
func NewLaunchTicket(
	operationID domain.OperationID,
	leaseKey identity.LeaseKey,
	reference helper.TicketReference,
	operation helper.Operation,
	address string,
	expiresAt time.Time,
) (LaunchTicket, error) {
	parsedAddress, err := netip.ParseAddr(address)
	if err != nil {
		return LaunchTicket{}, fmt.Errorf("launch ticket address is not canonical IPv4 loopback")
	}

	ticket := LaunchTicket{
		operationID: operationID,
		leaseKey:    leaseKey,
		reference:   reference,
		operation:   operation,
		address:     parsedAddress,
		expiresAt:   expiresAt,
	}
	if err := ticket.validateStructure(address); err != nil {
		return LaunchTicket{}, err
	}
	return ticket, nil
}

// validateAt rejects stale, excessively long-lived, or internally malformed launch metadata.
func (ticket LaunchTicket) validateAt(now time.Time) error {
	if err := ticket.validateStructure(ticket.address.String()); err != nil {
		return err
	}
	if !ticket.expiresAt.After(now) {
		return fmt.Errorf("launch ticket expiry is not in the future")
	}
	if ticket.expiresAt.After(now.Add(helper.MaxTicketLifetime)) {
		return fmt.Errorf("launch ticket expiry exceeds the protocol bound")
	}
	return nil
}

// validateStructure keeps the client-visible metadata canonical without interpreting the signed ticket contents.
func (ticket LaunchTicket) validateStructure(address string) error {
	if err := ticket.operationID.Validate(); err != nil {
		return fmt.Errorf("launch ticket operation ID: %w", err)
	}
	if err := ticket.leaseKey.Validate(); err != nil {
		return fmt.Errorf("launch ticket lease key: %w", err)
	}
	if err := ticket.reference.Validate(); err != nil {
		return fmt.Errorf("launch ticket reference: %w", err)
	}
	if ticket.operation != helper.OperationEnsureLoopbackIdentity && ticket.operation != helper.OperationReleaseLoopbackIdentity {
		return fmt.Errorf("launch ticket helper operation %q is unsupported", ticket.operation)
	}
	if !ticket.address.Is4() || !ticket.address.IsLoopback() || ticket.address != ticket.address.Unmap() || address != ticket.address.String() {
		return fmt.Errorf("launch ticket address is not canonical IPv4 loopback")
	}
	if ticket.expiresAt.IsZero() || ticket.expiresAt.Location() != time.UTC {
		return fmt.Errorf("launch ticket expiry must be a nonzero UTC time")
	}
	return nil
}
