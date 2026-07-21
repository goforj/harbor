package helper

import (
	"net/netip"
	"strings"
	"time"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

// ProtocolVersion is the only helper ticket and response version this build accepts.
const ProtocolVersion uint16 = 3

// MaxTicketLifetime bounds how long a captured helper ticket can remain useful.
const MaxTicketLifetime = 5 * time.Minute

// MaxTicketRedemptionDuration bounds how long the helper waits on its fixed authenticated ticket source.
const MaxTicketRedemptionDuration = 15 * time.Second

// MaximumRequesterIdentityLength is the byte limit shared by machine ownership and helper ticket admission.
const MaximumRequesterIdentityLength = 256

const (
	minimumNonceLength                         = 32
	maximumNonceLength                         = 128
	fingerprintLength                          = 64
	ticketReferenceLength                      = 64
	loopbackPoolPrefixBits                     = 29
	loopbackPoolIdentities                     = 1 << (32 - loopbackPoolPrefixBits)
	identityOwnershipSchemaVersion      uint32 = 1
	networkPolicyOwnershipSchemaVersion uint32 = 2
)

// MaximumSocketRequirements bounds the native port capabilities one pre-assignment observation may authorize.
const MaximumSocketRequirements = 128

// Operation identifies an allowlisted privileged helper effect.
type Operation string

const (
	// OperationEnsureLoopbackIdentity admits one owned loopback identity ensure operation.
	OperationEnsureLoopbackIdentity Operation = "ensure_loopback_identity"
	// OperationEnsureLoopbackPool admits one canonical pool ensure operation containing eight exact identities.
	OperationEnsureLoopbackPool Operation = "ensure_loopback_pool"
	// OperationReleaseLoopbackIdentity admits one owned loopback identity release operation.
	OperationReleaseLoopbackIdentity Operation = "release_loopback_identity"
	// OperationEnsureResolver admits one exact policy-bound resolver ensure operation.
	OperationEnsureResolver Operation = "ensure_resolver"
	// OperationReleaseResolver admits one exact policy-bound resolver release operation.
	OperationReleaseResolver Operation = "release_resolver"
	// OperationEnsureTrust admits one exact public-CA trust ensure operation.
	OperationEnsureTrust Operation = "ensure_trust"
	// OperationReleaseTrust admits one exact public-CA trust release operation.
	OperationReleaseTrust Operation = "release_trust"
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

// SocketTransport identifies one IPv4 socket capability protected by a pre-assignment observation.
type SocketTransport string

const (
	// SocketTransportTCP4 identifies an IPv4 TCP listener requirement.
	SocketTransportTCP4 SocketTransport = "tcp4"
	// SocketTransportUDP4 identifies an IPv4 UDP bind requirement.
	SocketTransportUDP4 SocketTransport = "udp4"
)

// SocketRequirement identifies one transport and nonzero port protected before address assignment.
type SocketRequirement struct {
	Transport SocketTransport `json:"transport"`
	Port      uint16          `json:"port"`
}

// Validate rejects requirements that do not identify one concrete IPv4 socket capability.
func (r SocketRequirement) Validate() error {
	if r.Transport != SocketTransportTCP4 && r.Transport != SocketTransportUDP4 {
		return newRequestError(ErrorCodeInvalidTicket, "pre-assignment socket transport is invalid")
	}
	if r.Port == 0 {
		return newRequestError(ErrorCodeInvalidTicket, "pre-assignment socket port must be positive")
	}
	return nil
}

// ExpectedPreAssignment binds an absent-state ensure to the independently observed route and socket safety facts.
type ExpectedPreAssignment struct {
	Fingerprint  string              `json:"fingerprint"`
	Requirements []SocketRequirement `json:"requirements"`
}

// Validate verifies the fingerprint and explicit canonical transport-then-port requirement order.
func (e ExpectedPreAssignment) Validate() error {
	if !validFingerprint(e.Fingerprint) {
		return newRequestError(ErrorCodeInvalidTicket, "expected pre-assignment fingerprint is invalid")
	}
	if e.Requirements == nil {
		return newRequestError(ErrorCodeInvalidTicket, "expected pre-assignment requirements must be explicit")
	}
	if len(e.Requirements) > MaximumSocketRequirements {
		return newRequestError(ErrorCodeInvalidTicket, "expected pre-assignment requirements exceed the protocol bound")
	}
	for index, requirement := range e.Requirements {
		if err := requirement.Validate(); err != nil {
			return err
		}
		if index > 0 && compareSocketRequirements(e.Requirements[index-1], requirement) >= 0 {
			return newRequestError(ErrorCodeInvalidTicket, "expected pre-assignment requirements are not unique canonical order")
		}
	}
	return nil
}

// compareSocketRequirements orders requirements by transport and then numeric port.
func compareSocketRequirements(left SocketRequirement, right SocketRequirement) int {
	if left.Transport < right.Transport {
		return -1
	}
	if left.Transport > right.Transport {
		return 1
	}
	if left.Port < right.Port {
		return -1
	}
	if left.Port > right.Port {
		return 1
	}
	return 0
}

// ExpectedLoopbackIdentity binds one exact pool address to its independently observed assignment and pre-assignment facts.
type ExpectedLoopbackIdentity struct {
	Address               string                 `json:"address"`
	ExpectedObservation   ExpectedObservation    `json:"expected_observation"`
	ExpectedPreAssignment *ExpectedPreAssignment `json:"expected_pre_assignment,omitempty"`
}

// ExpectedLoopbackPool binds one canonical /29 to all eight exact /32 identity preconditions in address order.
type ExpectedLoopbackPool struct {
	Identities []ExpectedLoopbackIdentity `json:"identities"`
}

// ExpectedResolverObservation binds one resolver mutation to a complete native observation.
type ExpectedResolverObservation struct {
	Fingerprint string `json:"fingerprint"`
}

// Validate requires the canonical digest emitted by the resolver adapter.
func (o ExpectedResolverObservation) Validate() error {
	if !validFingerprint(o.Fingerprint) {
		return newRequestError(ErrorCodeInvalidTicket, "expected resolver observation fingerprint is invalid")
	}
	return nil
}

// ExpectedTrustObservation binds one trust mutation to a complete native observation.
type ExpectedTrustObservation struct {
	Fingerprint string `json:"fingerprint"`
}

// Validate requires the canonical digest emitted by the trust adapter.
func (o ExpectedTrustObservation) Validate() error {
	if !validFingerprint(o.Fingerprint) {
		return newRequestError(ErrorCodeInvalidTicket, "expected trust observation fingerprint is invalid")
	}
	return nil
}

// TrustRoot carries only the public CA material that a signed trust ticket may authorize.
type TrustRoot struct {
	CertificatePEM []byte    `json:"certificate_pem"`
	Fingerprint    string    `json:"fingerprint"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
}

// Validate rejects missing, private, oversized, or internally inconsistent public CA material.
func (root TrustRoot) Validate() error {
	return validateTrustRootShape(root)
}

// Validate verifies that the authority contains every exact /29 address once in canonical order with route-only absent-state evidence.
func (expected ExpectedLoopbackPool) Validate(pool netip.Prefix) error {
	if !pool.IsValid() || !pool.Addr().Is4() || !pool.Addr().IsLoopback() || pool.Bits() != loopbackPoolPrefixBits || pool != pool.Masked() {
		return newRequestError(ErrorCodeInvalidTicket, "expected loopback pool requires a canonical IPv4 loopback /29")
	}
	if len(expected.Identities) != loopbackPoolIdentities {
		return newRequestError(ErrorCodeInvalidTicket, "expected loopback pool must contain exactly eight identities")
	}

	address := pool.Addr()
	for _, identity := range expected.Identities {
		if !validApprovedAddress(identity.Address) {
			return newRequestError(ErrorCodeInvalidTicket, "expected loopback pool identity address must be canonical IPv4 loopback")
		}
		parsed, _ := netip.ParseAddr(identity.Address)
		if parsed != address {
			return newRequestError(ErrorCodeInvalidTicket, "expected loopback pool identities must enumerate the complete pool in canonical order")
		}
		if err := identity.ExpectedObservation.Validate(); err != nil {
			return err
		}
		switch identity.ExpectedObservation.State {
		case ObservationAbsent:
			if identity.ExpectedPreAssignment == nil {
				return newRequestError(ErrorCodeInvalidTicket, "absent pool identity requires an expected pre-assignment observation")
			}
			if err := identity.ExpectedPreAssignment.Validate(); err != nil {
				return err
			}
			if len(identity.ExpectedPreAssignment.Requirements) != 0 {
				return newRequestError(ErrorCodeInvalidTicket, "absent pool identity requires route-only pre-assignment observation")
			}
		case ObservationOwned:
			if identity.ExpectedPreAssignment != nil {
				return newRequestError(ErrorCodeInvalidTicket, "owned pool identity cannot contain an expected pre-assignment observation")
			}
		}
		address = address.Next()
	}
	return nil
}

// Ticket authorizes exactly one bounded privileged operation.
type Ticket struct {
	Version                     uint16                       `json:"version"`
	Operation                   Operation                    `json:"operation"`
	InstallationID              string                       `json:"installation_id"`
	RequesterIdentity           string                       `json:"requester_identity"`
	OwnershipGeneration         uint64                       `json:"ownership_generation"`
	OwnershipSchemaVersion      uint32                       `json:"ownership_schema_version"`
	NetworkPolicyFingerprint    string                       `json:"network_policy_fingerprint,omitempty"`
	NetworkPolicy               *networkpolicy.Policy        `json:"network_policy,omitempty"`
	ApprovedPool                string                       `json:"approved_pool"`
	ApprovedAddress             string                       `json:"approved_address,omitempty"`
	ExpectedObservation         ExpectedObservation          `json:"expected_observation,omitzero"`
	ExpectedPreAssignment       *ExpectedPreAssignment       `json:"expected_pre_assignment,omitempty"`
	ExpectedLoopbackPool        *ExpectedLoopbackPool        `json:"expected_loopback_pool,omitempty"`
	ExpectedResolverObservation *ExpectedResolverObservation `json:"expected_resolver_observation,omitempty"`
	TrustRoot                   *TrustRoot                   `json:"trust_root,omitempty"`
	ExpectedTrustObservation    *ExpectedTrustObservation    `json:"expected_trust_observation,omitempty"`
	Nonce                       string                       `json:"nonce"`
	ExpiresAt                   time.Time                    `json:"expires_at"`
}

// Validate verifies the ticket against the current time without touching host state.
func (t Ticket) Validate(now time.Time) error {
	if t.Version != ProtocolVersion {
		return newRequestError(ErrorCodeInvalidTicket, "ticket version is unsupported")
	}
	if !validOperation(t.Operation) {
		return newRequestError(ErrorCodeInvalidTicket, "ticket operation is not allowlisted")
	}
	if err := ValidateInstallationID(t.InstallationID); err != nil {
		return newRequestError(ErrorCodeInvalidTicket, "installation ID is invalid")
	}
	if !validToken(t.RequesterIdentity, 1, MaximumRequesterIdentityLength) {
		return newRequestError(ErrorCodeInvalidTicket, "requester identity is invalid")
	}
	if t.OwnershipGeneration == 0 {
		return newRequestError(ErrorCodeInvalidTicket, "ownership generation must be positive")
	}
	switch t.OwnershipSchemaVersion {
	case identityOwnershipSchemaVersion:
		if t.NetworkPolicyFingerprint != "" {
			return newRequestError(ErrorCodeInvalidTicket, "identity-only ownership cannot carry a network policy fingerprint")
		}
	case networkPolicyOwnershipSchemaVersion:
		if !validFingerprint(t.NetworkPolicyFingerprint) {
			return newRequestError(ErrorCodeInvalidTicket, "network-policy ownership requires a canonical policy fingerprint")
		}
	default:
		return newRequestError(ErrorCodeInvalidTicket, "ownership schema version is unsupported")
	}
	pool, err := netip.ParsePrefix(t.ApprovedPool)
	if err != nil || !pool.Addr().Is4() || !pool.Addr().IsLoopback() || pool.Bits() < 8 || pool != pool.Masked() {
		return newRequestError(ErrorCodeInvalidTicket, "approved pool must be a canonical IPv4 loopback prefix")
	}
	if t.Operation == OperationEnsureResolver || t.Operation == OperationReleaseResolver {
		if err := t.validateResolverAuthority(); err != nil {
			return err
		}
	} else if t.Operation == OperationEnsureTrust || t.Operation == OperationReleaseTrust {
		if err := t.validateTrustAuthority(); err != nil {
			return err
		}
	} else if t.Operation == OperationEnsureLoopbackPool {
		if t.NetworkPolicy != nil || t.ExpectedResolverObservation != nil || t.TrustRoot != nil || t.ExpectedTrustObservation != nil {
			return newRequestError(ErrorCodeInvalidTicket, "loopback operation cannot contain network authority")
		}
		if pool.Bits() != loopbackPoolPrefixBits || pool.String() != t.ApprovedPool {
			return newRequestError(ErrorCodeInvalidTicket, "pool ensure requires a canonical IPv4 loopback /29")
		}
		if t.ApprovedAddress != "" || t.ExpectedObservation != (ExpectedObservation{}) || t.ExpectedPreAssignment != nil {
			return newRequestError(ErrorCodeInvalidTicket, "pool ensure cannot contain legacy single-address authority")
		}
		if t.ExpectedLoopbackPool == nil {
			return newRequestError(ErrorCodeInvalidTicket, "pool ensure requires expected loopback pool authority")
		}
		if err := t.ExpectedLoopbackPool.Validate(pool); err != nil {
			return err
		}
	} else {
		if t.NetworkPolicy != nil || t.ExpectedResolverObservation != nil || t.TrustRoot != nil || t.ExpectedTrustObservation != nil {
			return newRequestError(ErrorCodeInvalidTicket, "loopback operation cannot contain network authority")
		}
		if t.ExpectedLoopbackPool != nil {
			return newRequestError(ErrorCodeInvalidTicket, "single-address operation cannot contain expected loopback pool authority")
		}
		if err := t.validateSingleAddressAuthority(pool); err != nil {
			return err
		}
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

// validOperation keeps the signed operation vocabulary explicit at one boundary.
func validOperation(operation Operation) bool {
	switch operation {
	case OperationEnsureLoopbackIdentity,
		OperationEnsureLoopbackPool,
		OperationReleaseLoopbackIdentity,
		OperationEnsureResolver,
		OperationReleaseResolver,
		OperationEnsureTrust,
		OperationReleaseTrust:
		return true
	default:
		return false
	}
}

// validateResolverAuthority binds the resolver effect to one complete canonical host-network policy.
func (t Ticket) validateResolverAuthority() error {
	if t.OwnershipSchemaVersion != networkPolicyOwnershipSchemaVersion {
		return newRequestError(ErrorCodeInvalidTicket, "resolver operation requires network-policy ownership")
	}
	if t.NetworkPolicy == nil {
		return newRequestError(ErrorCodeInvalidTicket, "resolver operation requires a host-network policy")
	}
	if err := t.NetworkPolicy.Validate(); err != nil {
		return newRequestError(ErrorCodeInvalidTicket, "resolver host-network policy is invalid")
	}
	fingerprint, err := t.NetworkPolicy.Fingerprint()
	if err != nil || fingerprint != t.NetworkPolicyFingerprint {
		return newRequestError(ErrorCodeInvalidTicket, "resolver host-network policy fingerprint does not match ownership")
	}
	if t.ApprovedAddress != "" || t.ExpectedObservation != (ExpectedObservation{}) ||
		t.ExpectedPreAssignment != nil || t.ExpectedLoopbackPool != nil || t.TrustRoot != nil || t.ExpectedTrustObservation != nil {
		return newRequestError(ErrorCodeInvalidTicket, "resolver operation cannot contain loopback or trust mutation authority")
	}
	if t.ExpectedResolverObservation == nil {
		return newRequestError(ErrorCodeInvalidTicket, "resolver operation requires an expected observation")
	}
	return t.ExpectedResolverObservation.Validate()
}

// validateTrustAuthority binds trust effects to one complete policy and public-only CA material.
func (t Ticket) validateTrustAuthority() error {
	if t.OwnershipSchemaVersion != networkPolicyOwnershipSchemaVersion {
		return newRequestError(ErrorCodeInvalidTicket, "trust operation requires network-policy ownership")
	}
	if t.NetworkPolicy == nil {
		return newRequestError(ErrorCodeInvalidTicket, "trust operation requires a host-network policy")
	}
	if err := t.NetworkPolicy.Validate(); err != nil {
		return newRequestError(ErrorCodeInvalidTicket, "trust host-network policy is invalid")
	}
	policyFingerprint, err := t.NetworkPolicy.Fingerprint()
	if err != nil || policyFingerprint != t.NetworkPolicyFingerprint {
		return newRequestError(ErrorCodeInvalidTicket, "trust host-network policy fingerprint does not match ownership")
	}
	if t.TrustRoot == nil {
		return newRequestError(ErrorCodeInvalidTicket, "trust operation requires public CA material")
	}
	if err := validateTrustRootShape(*t.TrustRoot); err != nil {
		return err
	}
	if t.TrustRoot.Fingerprint != t.NetworkPolicy.AuthorityFingerprint {
		return newRequestError(ErrorCodeInvalidTicket, "trust CA fingerprint does not match host-network policy")
	}
	if t.ExpectedTrustObservation == nil {
		return newRequestError(ErrorCodeInvalidTicket, "trust operation requires an expected observation")
	}
	if err := t.ExpectedTrustObservation.Validate(); err != nil {
		return err
	}
	if t.ApprovedAddress != "" || t.ExpectedObservation != (ExpectedObservation{}) ||
		t.ExpectedPreAssignment != nil || t.ExpectedLoopbackPool != nil || t.ExpectedResolverObservation != nil {
		return newRequestError(ErrorCodeInvalidTicket, "trust operation cannot contain loopback or resolver mutation authority")
	}
	return nil
}

// validateTrustRootShape bounds signed public CA material before the trust handler performs deep certificate validation.
func validateTrustRootShape(root TrustRoot) error {
	if len(root.CertificatePEM) == 0 || len(root.CertificatePEM) > 64<<10 {
		return newRequestError(ErrorCodeInvalidTicket, "trust CA certificate material is missing or too large")
	}
	if strings.Contains(string(root.CertificatePEM), "PRIVATE KEY") {
		return newRequestError(ErrorCodeInvalidTicket, "trust CA certificate material must not contain private key data")
	}
	if !validFingerprint(root.Fingerprint) || root.NotBefore.IsZero() || root.NotAfter.IsZero() ||
		root.NotBefore.Location() != time.UTC || root.NotAfter.Location() != time.UTC || !root.NotAfter.After(root.NotBefore) {
		return newRequestError(ErrorCodeInvalidTicket, "trust CA public identity is invalid")
	}
	return nil
}

// validateSingleAddressAuthority verifies the legacy exact-address authority used by ensure and release operations.
func (t Ticket) validateSingleAddressAuthority(pool netip.Prefix) error {
	if !validApprovedAddress(t.ApprovedAddress) {
		return newRequestError(ErrorCodeInvalidTicket, "approved address must be canonical IPv4 loopback")
	}
	address, _ := netip.ParseAddr(t.ApprovedAddress)
	if !pool.Contains(address) {
		return newRequestError(ErrorCodeInvalidTicket, "approved address must belong to the approved pool")
	}
	if err := t.ExpectedObservation.Validate(); err != nil {
		return err
	}
	if t.Operation == OperationEnsureLoopbackIdentity && t.ExpectedObservation.State == ObservationAbsent {
		if t.ExpectedPreAssignment == nil {
			return newRequestError(ErrorCodeInvalidTicket, "absent ensure requires an expected pre-assignment observation")
		}
		if err := t.ExpectedPreAssignment.Validate(); err != nil {
			return err
		}
	} else if t.ExpectedPreAssignment != nil {
		return newRequestError(ErrorCodeInvalidTicket, "expected pre-assignment observation is not allowed for this operation")
	}
	if t.Operation == OperationReleaseLoopbackIdentity && t.ExpectedObservation.State != ObservationOwned {
		return newRequestError(ErrorCodeInvalidTicket, "release requires an owned expected observation")
	}
	return nil
}

// TicketReference is an opaque single-use lookup handle understood only by the fixed ticket redeemer.
type TicketReference string

// Validate verifies the reference is the canonical lowercase encoding of 32 random bytes.
func (r TicketReference) Validate() error {
	if !validLowerHex(string(r), ticketReferenceLength) {
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
	return validLowerHex(value, fingerprintLength)
}

// validLowerHex accepts one exact-width lowercase hexadecimal representation.
func validLowerHex(value string, length int) bool {
	if len(value) != length {
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
