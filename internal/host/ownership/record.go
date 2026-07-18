package ownership

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/goforj/harbor/internal/helper"
)

const (
	// CurrentSchemaVersion is the only machine-ownership record schema understood by this package.
	CurrentSchemaVersion uint32 = 1

	maximumOwnerIdentityLength = 256
	maximumSIDSubauthorities   = 15
	maximumSIDAuthority        = 1<<48 - 1
)

// Record is the canonical machine-global claim checked before Harbor mutates protected host state.
type Record struct {
	SchemaVersion      uint32 `json:"schema_version"`
	InstallationID     string `json:"installation_id"`
	OwnerIdentity      string `json:"owner_identity"`
	Generation         uint64 `json:"generation"`
	LoopbackPoolPrefix string `json:"loopback_pool_prefix"`
	TicketVerifierKey  string `json:"ticket_verifier_key"`
}

// Validate rejects records whose identity cannot be compared canonically across daemon and helper processes.
func (record Record) Validate() error {
	if record.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("machine ownership schema version is %d, want %d", record.SchemaVersion, CurrentSchemaVersion)
	}
	if err := helper.ValidateInstallationID(record.InstallationID); err != nil {
		return fmt.Errorf("machine ownership: %w", err)
	}
	if err := validateOwnerIdentity(record.OwnerIdentity); err != nil {
		return err
	}
	if record.Generation == 0 {
		return fmt.Errorf("machine ownership generation must be greater than zero")
	}
	if err := validateLoopbackPoolPrefix(record.LoopbackPoolPrefix); err != nil {
		return err
	}
	if err := validateTicketVerifierKey(record.TicketVerifierKey); err != nil {
		return err
	}
	return nil
}

// Fingerprint returns the lowercase SHA-256 digest of the validated canonical JSON record.
func (record Record) Fingerprint() (string, error) {
	if err := record.Validate(); err != nil {
		return "", fmt.Errorf("fingerprint machine ownership record: %w", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode machine ownership record fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// validateOwnerIdentity accepts the canonical unsigned UID or Windows SID emitted by local peer authentication.
func validateOwnerIdentity(value string) error {
	if value == "" {
		return fmt.Errorf("machine ownership owner identity is required")
	}
	if len(value) > maximumOwnerIdentityLength {
		return fmt.Errorf("machine ownership owner identity exceeds %d bytes", maximumOwnerIdentityLength)
	}
	if value[0] == 'S' {
		return validateSID(value)
	}
	return validateUID(value)
}

// validateUID rejects signs, whitespace, and leading zeros so peer identities compare byte-for-byte.
func validateUID(value string) error {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return fmt.Errorf("machine ownership owner identity %q is not a canonical unsigned UID", value)
	}
	return nil
}

// validateSID enforces the canonical decimal SID spelling returned by Windows peer authentication.
func validateSID(value string) error {
	parts := strings.Split(value, "-")
	if len(parts) < 4 || len(parts) > 3+maximumSIDSubauthorities || parts[0] != "S" || parts[1] != "1" {
		return fmt.Errorf("machine ownership owner identity %q is not a canonical Windows SID", value)
	}
	for index, part := range parts[1:] {
		bitSize := 32
		if index == 1 {
			bitSize = 48
		}
		parsed, err := strconv.ParseUint(part, 10, bitSize)
		if err != nil || strconv.FormatUint(parsed, 10) != part {
			return fmt.Errorf("machine ownership owner identity %q is not a canonical Windows SID", value)
		}
		if index == 1 && parsed > maximumSIDAuthority {
			return fmt.Errorf("machine ownership owner identity %q is not a canonical Windows SID", value)
		}
	}
	return nil
}

// validateLoopbackPoolPrefix prevents equivalent or non-loopback address pools from receiving different spellings.
func validateLoopbackPoolPrefix(value string) error {
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return fmt.Errorf("machine ownership loopback pool prefix %q is invalid: %w", value, err)
	}
	if !prefix.Addr().Is4() || !prefix.Addr().IsLoopback() {
		return fmt.Errorf("machine ownership loopback pool prefix %q is not IPv4 loopback", value)
	}
	if prefix.Bits() < 8 {
		return fmt.Errorf("machine ownership loopback pool prefix %q extends outside IPv4 loopback", value)
	}
	if prefix != prefix.Masked() || prefix.String() != value {
		return fmt.Errorf("machine ownership loopback pool prefix %q is not canonical", value)
	}
	return nil
}

// validateTicketVerifierKey pins exactly one canonical Ed25519 public key for helper ticket admission.
func validateTicketVerifierKey(value string) error {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return fmt.Errorf("machine ownership ticket verifier key is not canonical base64: %w", err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		return fmt.Errorf("machine ownership ticket verifier key contains %d bytes, want %d", len(decoded), ed25519.PublicKeySize)
	}
	if base64.StdEncoding.EncodeToString(decoded) != value {
		return fmt.Errorf("machine ownership ticket verifier key is not canonical padded base64")
	}
	return nil
}
