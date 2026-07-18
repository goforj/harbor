package identity

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
)

const maxIdentityTokenLength = 255

// InstallationID identifies the Harbor installation that owns a host projection.
type InstallationID string

// Ownership binds a lease to one installation ownership generation.
type Ownership struct {
	InstallationID InstallationID
	Generation     uint64
}

// NewOwnership validates an installation identifier and its nonzero generation.
func NewOwnership(installationID InstallationID, generation uint64) (Ownership, error) {
	ownership := Ownership{InstallationID: installationID, Generation: generation}
	if err := ownership.Validate(); err != nil {
		return Ownership{}, err
	}
	return ownership, nil
}

// Validate rejects ownership values that cannot safely mark a host projection.
func (o Ownership) Validate() error {
	if err := validateIdentityToken("installation ID", string(o.InstallationID)); err != nil {
		return err
	}
	if o.Generation == 0 {
		return fmt.Errorf("identity ownership: generation must be greater than zero")
	}
	return nil
}

// LeaseKind describes whether an address is a project's primary or additional identity.
type LeaseKind string

const (
	// LeaseKindPrimary is the stable default identity for a project.
	LeaseKindPrimary LeaseKind = "primary"
	// LeaseKindSecondary is an additional identity for an intra-project same-port collision.
	LeaseKindSecondary LeaseKind = "secondary"
)

// LeaseKey is the stable logical identity that receives one loopback address.
type LeaseKey struct {
	ProjectID   domain.ProjectID
	SecondaryID string
}

// NewPrimaryKey creates the stable primary identity key for a project.
func NewPrimaryKey(projectID domain.ProjectID) (LeaseKey, error) {
	key := LeaseKey{ProjectID: projectID}
	if err := key.Validate(); err != nil {
		return LeaseKey{}, err
	}
	return key, nil
}

// NewSecondaryKey creates a stable additional identity key for one same-port requirement.
func NewSecondaryKey(projectID domain.ProjectID, secondaryID string) (LeaseKey, error) {
	key := LeaseKey{ProjectID: projectID, SecondaryID: secondaryID}
	if err := key.Validate(); err != nil {
		return LeaseKey{}, err
	}
	if key.SecondaryID == "" {
		return LeaseKey{}, fmt.Errorf("identity lease key: secondary ID is required")
	}
	return key, nil
}

// Kind returns whether the key represents the primary or an additional identity.
func (k LeaseKey) Kind() LeaseKind {
	if k.SecondaryID == "" {
		return LeaseKindPrimary
	}
	return LeaseKindSecondary
}

// Validate rejects keys that cannot remain stable across reconciliations.
func (k LeaseKey) Validate() error {
	if err := k.ProjectID.Validate(); err != nil {
		return fmt.Errorf("identity lease key: %w", err)
	}
	if k.SecondaryID == "" {
		return nil
	}
	if err := validateIdentityToken("secondary ID", k.SecondaryID); err != nil {
		return fmt.Errorf("identity lease key: %w", err)
	}
	return nil
}

// Lease binds one stable logical identity to an exact loopback address and owner.
type Lease struct {
	Key       LeaseKey
	Address   netip.Addr
	Ownership Ownership
}

// Validate rejects malformed leases before they influence allocation or cleanup.
func (l Lease) Validate() error {
	if err := l.Key.Validate(); err != nil {
		return err
	}
	address := l.Address.Unmap()
	if !address.IsValid() || !address.Is4() || !address.IsLoopback() {
		return fmt.Errorf("identity lease: address %s is not IPv4 loopback", l.Address)
	}
	if err := l.Ownership.Validate(); err != nil {
		return err
	}
	return nil
}

// Quarantine temporarily prevents reuse of a recently released or suspect address.
type Quarantine struct {
	Address netip.Addr
	Reason  string
}

// Validate rejects quarantines that are not tied to an exact pool candidate.
func (q Quarantine) Validate(pool Pool) error {
	address := q.Address.Unmap()
	if !pool.Contains(address) {
		return fmt.Errorf("identity quarantine: address %s is not a pool candidate", q.Address)
	}
	if strings.TrimSpace(q.Reason) == "" {
		return fmt.Errorf("identity quarantine: reason is required for %s", address)
	}
	return nil
}

// ConflictKind identifies the observed host fact that makes an address unavailable.
type ConflictKind string

const (
	// ConflictKindAddress means the address is already present without matching Harbor ownership.
	ConflictKindAddress ConflictKind = "address"
	// ConflictKindListener means a required port is already occupied by another listener.
	ConflictKindListener ConflictKind = "listener"
	// ConflictKindResolver means another resolver already maps a required name to the address.
	ConflictKindResolver ConflictKind = "resolver"
	// ConflictKindOwnership means another Harbor installation or generation owns the address.
	ConflictKindOwnership ConflictKind = "ownership"
)

// Conflict records an observed reason that a candidate cannot be allocated.
type Conflict struct {
	Address netip.Addr
	Kind    ConflictKind
	Port    uint16
	Detail  string
}

// Validate rejects conflicts that do not identify a supported candidate fact.
func (c Conflict) Validate(pool Pool) error {
	address := c.Address.Unmap()
	if !pool.Contains(address) {
		return fmt.Errorf("identity conflict: address %s is not a pool candidate", c.Address)
	}
	switch c.Kind {
	case ConflictKindAddress, ConflictKindListener, ConflictKindResolver, ConflictKindOwnership:
	default:
		return fmt.Errorf("identity conflict: kind %q is unsupported", c.Kind)
	}
	if c.Kind != ConflictKindListener && c.Port != 0 {
		return fmt.Errorf("identity conflict: port is only valid for listener conflicts")
	}
	if c.Kind == ConflictKindListener && c.Port == 0 {
		return fmt.Errorf("identity conflict: listener port is required")
	}
	return nil
}

// validateIdentityToken bounds ownership and lease keys without assigning filesystem semantics to them.
func validateIdentityToken(label string, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not have surrounding whitespace", label)
	}
	if len(value) > maxIdentityTokenLength {
		return fmt.Errorf("%s exceeds %d bytes", label, maxIdentityTokenLength)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return fmt.Errorf("%s must not contain whitespace or control characters", label)
		}
	}
	return nil
}

// sameOwnership compares the installation and generation as one indivisible authority marker.
func sameOwnership(left Ownership, right Ownership) bool {
	return left == right
}

// keyLess gives project identities a canonical primary-before-secondary order.
func keyLess(left LeaseKey, right LeaseKey) bool {
	if left.ProjectID != right.ProjectID {
		return string(left.ProjectID) < string(right.ProjectID)
	}
	if left.Kind() != right.Kind() {
		return left.Kind() == LeaseKindPrimary
	}
	return left.SecondaryID < right.SecondaryID
}

// leaseLess applies canonical key ordering after duplicate lease keys have been rejected.
func leaseLess(left Lease, right Lease) bool {
	return keyLess(left.Key, right.Key)
}
