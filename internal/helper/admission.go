package helper

import "context"

// OwnershipAdmissionState identifies how protected ownership relates to the signed ticket target.
type OwnershipAdmissionState string

const (
	// OwnershipAdmissionAlreadyCurrent means protected ownership already equals the signed ticket target.
	OwnershipAdmissionAlreadyCurrent OwnershipAdmissionState = "already_current"
	// OwnershipAdmissionSchema1To2 means an ensure may transition the exact target-derived schema-1 claim to schema 2.
	OwnershipAdmissionSchema1To2 OwnershipAdmissionState = "schema_1_to_2"
	// OwnershipAdmissionSchema2To1 means a resolver retirement may transition the exact schema-2 target to its schema-1 form.
	OwnershipAdmissionSchema2To1 OwnershipAdmissionState = "schema_2_to_1"
	// OwnershipAdmissionAlreadyRetired means a resolver retirement already reached its exact schema-one successor.
	OwnershipAdmissionAlreadyRetired OwnershipAdmissionState = "already_retired"
	// OwnershipAdmissionAlreadyReleased means the exact signed schema-two target has already been removed.
	OwnershipAdmissionAlreadyReleased OwnershipAdmissionState = "already_released"
)

// TicketAdmission carries bindings established independently from the untrusted wire request.
type TicketAdmission struct {
	TicketReference            TicketReference
	RequesterIdentity          string
	InstallationID             string
	OwnershipGeneration        uint64
	OwnershipSchemaVersion     uint32
	NetworkPolicyFingerprint   string
	ApprovedPool               string
	OwnershipState             OwnershipAdmissionState
	OwnershipFingerprint       string
	TargetOwnershipFingerprint string
	PostOwnershipFingerprint   string
	TicketVerifierKey          string
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
		admission.OwnershipSchemaVersion != r.Ticket.OwnershipSchemaVersion ||
		admission.NetworkPolicyFingerprint != r.Ticket.NetworkPolicyFingerprint ||
		admission.ApprovedPool != r.Ticket.ApprovedPool {
		return ErrTicketRedemptionFailed
	}
	if !validFingerprint(admission.OwnershipFingerprint) ||
		!validFingerprint(admission.TargetOwnershipFingerprint) ||
		!validFingerprint(admission.PostOwnershipFingerprint) ||
		admission.TicketVerifierKey == "" {
		return ErrTicketRedemptionFailed
	}
	switch admission.OwnershipState {
	case OwnershipAdmissionAlreadyCurrent:
		if r.Ticket.Operation == OperationReleaseNetworkOwnership &&
			(r.Ticket.ExpectedOwnershipFingerprint != admission.TargetOwnershipFingerprint ||
				admission.OwnershipFingerprint != admission.TargetOwnershipFingerprint ||
				admission.PostOwnershipFingerprint != admission.TargetOwnershipFingerprint) {
			return ErrTicketRedemptionFailed
		}
	case OwnershipAdmissionSchema1To2:
		if r.Ticket.Operation != OperationEnsureResolver ||
			r.Ticket.OwnershipSchemaVersion != networkPolicyOwnershipSchemaVersion {
			return ErrTicketRedemptionFailed
		}
	case OwnershipAdmissionSchema2To1:
		if r.Ticket.Operation != OperationRetireResolver ||
			r.Ticket.OwnershipSchemaVersion != networkPolicyOwnershipSchemaVersion {
			return ErrTicketRedemptionFailed
		}
	case OwnershipAdmissionAlreadyRetired:
		if r.Ticket.Operation != OperationRetireResolver ||
			r.Ticket.OwnershipSchemaVersion != networkPolicyOwnershipSchemaVersion {
			return ErrTicketRedemptionFailed
		}
	case OwnershipAdmissionAlreadyReleased:
		if r.Ticket.Operation != OperationReleaseNetworkOwnership ||
			r.Ticket.OwnershipSchemaVersion != networkPolicyOwnershipSchemaVersion ||
			admission.OwnershipFingerprint != admission.TargetOwnershipFingerprint ||
			admission.PostOwnershipFingerprint != admission.TargetOwnershipFingerprint {
			return ErrTicketRedemptionFailed
		}
	default:
		return ErrTicketRedemptionFailed
	}
	return nil
}
