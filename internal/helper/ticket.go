package helper

import (
	"net/netip"
	"strings"
	"time"
)

// ProtocolVersion is the only helper ticket and response version this build accepts.
const ProtocolVersion uint16 = 1

// MaxTicketLifetime bounds how long a captured helper ticket can remain useful.
const MaxTicketLifetime = 5 * time.Minute

// MaxTicketRedemptionDuration bounds how long the helper waits on its fixed authenticated ticket source.
const MaxTicketRedemptionDuration = 15 * time.Second

const (
	minimumNonceLength     = 32
	maximumNonceLength     = 128
	maximumIDLength        = 128
	fingerprintLength      = 64
	minimumReferenceLength = 32
	maximumReferenceLength = 128
)

// Operation identifies an allowlisted privileged helper effect.
type Operation string

const (
	// OperationEnsureLoopbackIdentity admits one owned loopback identity ensure operation.
	OperationEnsureLoopbackIdentity Operation = "ensure_loopback_identity"
	// OperationReleaseLoopbackIdentity admits one owned loopback identity release operation.
	OperationReleaseLoopbackIdentity Operation = "release_loopback_identity"
)

// ObservationState identifies the expected pre-mutation state of an approved address.
type ObservationState string

const (
	// ObservationAbsent means the approved address was not present in the guarded observation.
	ObservationAbsent ObservationState = "absent"
	// ObservationOwned means the approved address matched this installation's ownership evidence.
	ObservationOwned ObservationState = "owned"
)

// ExpectedObservation binds a ticket to a fingerprinted pre-mutation host observation.
type ExpectedObservation struct {
	State       ObservationState `json:"state"`
	Fingerprint string           `json:"fingerprint"`
}

// Validate verifies that the observation is explicit and canonically fingerprinted.
func (o ExpectedObservation) Validate() error {
	if o.State != ObservationAbsent && o.State != ObservationOwned {
		return newRequestError(ErrorCodeInvalidTicket, "expected observation state is invalid")
	}
	if !validFingerprint(o.Fingerprint) {
		return newRequestError(ErrorCodeInvalidTicket, "expected observation fingerprint is invalid")
	}
	return nil
}

// Ticket authorizes exactly one bounded loopback identity operation.
type Ticket struct {
	Version             uint16              `json:"version"`
	Operation           Operation           `json:"operation"`
	DaemonIdentity      string              `json:"daemon_identity"`
	InstallationID      string              `json:"installation_id"`
	RequesterIdentity   string              `json:"requester_identity"`
	OwnershipGeneration uint64              `json:"ownership_generation"`
	ApprovedAddress     string              `json:"approved_address"`
	ExpectedObservation ExpectedObservation `json:"expected_observation"`
	Nonce               string              `json:"nonce"`
	ExpiresAt           time.Time           `json:"expires_at"`
}

// Validate verifies the ticket against the current time without touching host state.
func (t Ticket) Validate(now time.Time) error {
	if t.Version != ProtocolVersion {
		return newRequestError(ErrorCodeInvalidTicket, "ticket version is unsupported")
	}
	if t.Operation != OperationEnsureLoopbackIdentity && t.Operation != OperationReleaseLoopbackIdentity {
		return newRequestError(ErrorCodeInvalidTicket, "ticket operation is not allowlisted")
	}
	if !validToken(t.DaemonIdentity, 1, maximumIDLength) {
		return newRequestError(ErrorCodeInvalidTicket, "daemon identity is invalid")
	}
	if err := ValidateInstallationID(t.InstallationID); err != nil {
		return newRequestError(ErrorCodeInvalidTicket, "installation ID is invalid")
	}
	if !validToken(t.RequesterIdentity, 1, maximumIDLength) {
		return newRequestError(ErrorCodeInvalidTicket, "requester identity is invalid")
	}
	if t.OwnershipGeneration == 0 {
		return newRequestError(ErrorCodeInvalidTicket, "ownership generation must be positive")
	}
	if !validApprovedAddress(t.ApprovedAddress) {
		return newRequestError(ErrorCodeInvalidTicket, "approved address must be canonical IPv4 loopback")
	}
	if err := t.ExpectedObservation.Validate(); err != nil {
		return err
	}
	if t.Operation == OperationReleaseLoopbackIdentity && t.ExpectedObservation.State != ObservationOwned {
		return newRequestError(ErrorCodeInvalidTicket, "release requires an owned expected observation")
	}
	if !validToken(t.Nonce, minimumNonceLength, maximumNonceLength) {
		return newRequestError(ErrorCodeInvalidTicket, "ticket nonce is invalid")
	}
	if t.ExpiresAt.IsZero() || !t.ExpiresAt.After(now) {
		return newRequestError(ErrorCodeInvalidTicket, "ticket is expired")
	}
	if t.ExpiresAt.Location() != time.UTC {
		return newRequestError(ErrorCodeInvalidTicket, "ticket expiry must use UTC")
	}
	if t.ExpiresAt.After(now.Add(MaxTicketLifetime)) {
		return newRequestError(ErrorCodeInvalidTicket, "ticket expiry exceeds the maximum lifetime")
	}
	return nil
}

// TicketReference is an opaque single-use lookup handle understood only by the fixed ticket redeemer.
type TicketReference string

// Validate verifies only the reference's canonical wire bounds without interpreting its contents.
func (r TicketReference) Validate() error {
	if !validToken(string(r), minimumReferenceLength, maximumReferenceLength) {
		return newRequestError(ErrorCodeInvalidTicket, "ticket reference is invalid")
	}
	return nil
}

// Request is the strict top-level helper request envelope containing no mutation authority.
type Request struct {
	Version         uint16          `json:"version"`
	TicketReference TicketReference `json:"ticket_reference"`
}

// Validate verifies the protocol version and opaque reference before trusted redemption.
func (r Request) Validate() error {
	if r.Version != ProtocolVersion {
		return newRequestError(ErrorCodeInvalidTicket, "request version is unsupported")
	}
	return r.TicketReference.Validate()
}

// validApprovedAddress accepts only canonical IPv4 loopback text so equivalent spellings cannot bypass comparisons.
func validApprovedAddress(value string) bool {
	address, err := netip.ParseAddr(value)
	if err != nil || !address.Is4() || !address.IsLoopback() {
		return false
	}
	return value == address.String()
}

// validFingerprint accepts the canonical lowercase SHA-256 representation used by host observations.
func validFingerprint(value string) bool {
	if len(value) != fingerprintLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// validToken limits identifiers to an unambiguous path-free ASCII alphabet.
func validToken(value string, minimumLength int, maximumLength int) bool {
	if len(value) < minimumLength || len(value) > maximumLength || strings.TrimSpace(value) != value {
		return false
	}
	if !tokenAlphanumeric(value[0]) || !tokenAlphanumeric(value[len(value)-1]) {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

// tokenAlphanumeric keeps identifier boundaries independent from path-like punctuation.
func tokenAlphanumeric(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}
