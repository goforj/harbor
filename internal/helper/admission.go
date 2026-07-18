package helper

import "context"

// TicketAdmission carries bindings established independently from the untrusted wire request.
type TicketAdmission struct {
	TicketReference     TicketReference
	RequesterIdentity   string
	InstallationID      string
	OwnershipGeneration uint64
	ApprovedPool        string
}

// TicketRedemption carries one signature-authenticated ticket and its independently authenticated bindings.
type TicketRedemption struct {
	Ticket    Ticket
	Admission TicketAdmission
}

// TicketRedeemer authenticates and consumes opaque references through the adapter fixed by process composition.
type TicketRedeemer interface {
	// Redeem atomically resolves one reference, verifies its signed ticket, and establishes independent admission bindings.
	Redeem(context.Context, TicketReference) (TicketRedemption, error)
}

// UnavailableTicketRedeemer fails closed until a platform-authenticated redemption adapter is installed.
type UnavailableTicketRedeemer struct{}

// Redeem rejects references because this seed cannot authenticate or consume them.
func (UnavailableTicketRedeemer) Redeem(context.Context, TicketReference) (TicketRedemption, error) {
	return TicketRedemption{}, ErrTicketRedemptionUnavailable
}

// validate proves signed authority remains bound to the independently authenticated request and machine ownership.
func (r TicketRedemption) validate(reference TicketReference) error {
	admission := r.Admission
	if admission.TicketReference != reference ||
		admission.RequesterIdentity != r.Ticket.RequesterIdentity ||
		admission.InstallationID != r.Ticket.InstallationID ||
		admission.OwnershipGeneration != r.Ticket.OwnershipGeneration ||
		admission.ApprovedPool != r.Ticket.ApprovedPool {
		return ErrTicketRedemptionFailed
	}
	return nil
}
