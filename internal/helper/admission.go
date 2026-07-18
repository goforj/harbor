package helper

import "context"

// TicketAdmission carries bindings established independently from the untrusted wire request.
type TicketAdmission struct {
	TicketReference     TicketReference
	DaemonIdentity      string
	PeerIdentity        string
	InstallationID      string
	OwnershipGeneration uint64
	AuthorizedTicket    Ticket
}

// TicketRedemption carries one canonical ticket and its independently authenticated bindings.
type TicketRedemption struct {
	Ticket    Ticket
	Admission TicketAdmission
}

// TicketRedeemer authenticates and consumes opaque references through the adapter fixed by process composition.
type TicketRedeemer interface {
	// Redeem atomically resolves one reference into a canonical ticket and independent admission bindings.
	Redeem(context.Context, TicketReference) (TicketRedemption, error)
}

// UnavailableTicketRedeemer fails closed until a platform-authenticated redemption adapter is installed.
type UnavailableTicketRedeemer struct{}

// Redeem rejects references because this seed cannot authenticate or consume them.
func (UnavailableTicketRedeemer) Redeem(context.Context, TicketReference) (TicketRedemption, error) {
	return TicketRedemption{}, ErrTicketRedemptionUnavailable
}

// validate proves the redemption is bound to this exact reference, ticket, daemon, peer, and ownership generation.
func (r TicketRedemption) validate(reference TicketReference) error {
	admission := r.Admission
	if admission.TicketReference != reference ||
		admission.DaemonIdentity != r.Ticket.DaemonIdentity ||
		admission.PeerIdentity != r.Ticket.RequesterIdentity ||
		admission.InstallationID != r.Ticket.InstallationID ||
		admission.OwnershipGeneration != r.Ticket.OwnershipGeneration ||
		!ticketsEqual(admission.AuthorizedTicket, r.Ticket) {
		return ErrTicketRedemptionFailed
	}
	return nil
}

// ticketsEqual compares every authorized field without relying on time.Time's internal representation.
func ticketsEqual(left Ticket, right Ticket) bool {
	return left.Version == right.Version &&
		left.Operation == right.Operation &&
		left.DaemonIdentity == right.DaemonIdentity &&
		left.InstallationID == right.InstallationID &&
		left.RequesterIdentity == right.RequesterIdentity &&
		left.OwnershipGeneration == right.OwnershipGeneration &&
		left.ApprovedAddress == right.ApprovedAddress &&
		left.ExpectedObservation == right.ExpectedObservation &&
		left.Nonce == right.Nonce &&
		left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.ExpiresAt.Location() == right.ExpiresAt.Location()
}
