package trust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/trust/certroot"
)

const (
	ownerMarkerVersion         = uint16(1)
	maximumTrustEntries        = 256
	maximumNativeIDLength      = 512
	maximumMarkerTextLength    = 128
	maximumNativeAttributes    = 512
	maximumCertificatePEMBytes = 64 << 10
	canonicalFingerprintLength = sha256.Size * 2
)

const observationFingerprintDomain = "goforj.harbor.trust-observation.v1\x00"

// Request is the immutable trust authority derived from one installation and public CA.
type Request struct {
	installationID    string
	requesterIdentity string
	mechanism         networkpolicy.TrustMechanism
	root              certroot.Root
}

// NewRequest validates one installation, trust scope, and public CA before deriving its exact trust authority.
func NewRequest(installationID string, mechanism networkpolicy.TrustMechanism, root certroot.Root) (Request, error) {
	return newRequest(installationID, "", mechanism, root)
}

// NewRequestForRequester binds trust authority to the interactive identity whose current-user store may be changed.
func NewRequestForRequester(
	installationID string,
	requesterIdentity string,
	mechanism networkpolicy.TrustMechanism,
	root certroot.Root,
) (Request, error) {
	return newRequest(installationID, requesterIdentity, mechanism, root)
}

// newRequest validates one trust authority while preserving the legacy constructor for platform-neutral callers.
func newRequest(
	installationID string,
	requesterIdentity string,
	mechanism networkpolicy.TrustMechanism,
	root certroot.Root,
) (Request, error) {
	if err := helper.ValidateInstallationID(installationID); err != nil {
		return Request{}, fmt.Errorf("trust request installation ID: %w", err)
	}
	if requesterIdentity != "" {
		if err := helper.ValidateRequesterIdentity(requesterIdentity); err != nil {
			return Request{}, fmt.Errorf("trust request requester identity: %w", err)
		}
	}
	if err := validateMechanism(mechanism); err != nil {
		return Request{}, err
	}
	root = normalizeRoot(root)
	if err := validateRoot(root); err != nil {
		return Request{}, fmt.Errorf("trust request root: %w", err)
	}
	request := Request{
		installationID:    installationID,
		requesterIdentity: requesterIdentity,
		mechanism:         mechanism,
		root:              root,
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Validate rejects zero, corrupt, or internally inconsistent trust requests.
func (request Request) Validate() error {
	if err := helper.ValidateInstallationID(request.installationID); err != nil {
		return fmt.Errorf("trust request installation ID: %w", err)
	}
	if request.requesterIdentity != "" {
		if err := helper.ValidateRequesterIdentity(request.requesterIdentity); err != nil {
			return fmt.Errorf("trust request requester identity: %w", err)
		}
	}
	if err := validateMechanism(request.mechanism); err != nil {
		return err
	}
	if err := validateRoot(request.root); err != nil {
		return fmt.Errorf("trust request root: %w", err)
	}
	return nil
}

// InstallationID returns the installation identity carried by the ownership marker.
func (request Request) InstallationID() string {
	return request.installationID
}

// RequesterIdentity returns the optional interactive identity bound to a current-user trust store.
func (request Request) RequesterIdentity() string {
	return request.requesterIdentity
}

// Mechanism returns the supported operating-system trust scope selected for this request.
func (request Request) Mechanism() networkpolicy.TrustMechanism {
	return request.mechanism
}

// Root returns a defensive public-only copy of the authority bound to this request.
func (request Request) Root() certroot.Root {
	return cloneRoot(request.root)
}

// AuthorityFingerprint returns the exact CA certificate fingerprint carried by this request.
func (request Request) AuthorityFingerprint() string {
	return request.root.Fingerprint
}

// OwnerMarker returns the versioned ownership evidence required before Harbor may remove a trust entry.
func (request Request) OwnerMarker() OwnerMarker {
	return OwnerMarker{
		Version:              ownerMarkerVersion,
		InstallationID:       request.installationID,
		RequesterIdentity:    request.requesterIdentity,
		Mechanism:            request.mechanism,
		AuthorityFingerprint: request.root.Fingerprint,
	}
}

// OwnerMarker is bounded ownership evidence parsed from one native trust entry.
type OwnerMarker struct {
	Version              uint16
	InstallationID       string
	RequesterIdentity    string
	Mechanism            networkpolicy.TrustMechanism
	AuthorityFingerprint string
}

// Validate rejects markers that cannot be compared canonically across helper processes.
func (marker OwnerMarker) Validate() error {
	if marker.Version == 0 {
		return fmt.Errorf("trust owner marker version must be greater than zero")
	}
	if len(marker.InstallationID) > maximumMarkerTextLength {
		return fmt.Errorf("trust owner marker installation ID exceeds %d bytes", maximumMarkerTextLength)
	}
	if err := helper.ValidateInstallationID(marker.InstallationID); err != nil {
		return fmt.Errorf("trust owner marker installation ID: %w", err)
	}
	if marker.RequesterIdentity != "" {
		if err := helper.ValidateRequesterIdentity(marker.RequesterIdentity); err != nil {
			return fmt.Errorf("trust owner marker requester identity: %w", err)
		}
	}
	if err := validateMechanism(marker.Mechanism); err != nil {
		return fmt.Errorf("trust owner marker mechanism: %w", err)
	}
	if err := validateFingerprintText("trust owner marker authority fingerprint", marker.AuthorityFingerprint); err != nil {
		return err
	}
	return nil
}

// Entry is one complete, bounded trust artifact observed through a native platform facility.
type Entry struct {
	// Mechanism identifies the native store scope that produced this entry.
	Mechanism networkpolicy.TrustMechanism
	// NativeID is emitted by the backend and identifies only an entry from this observation.
	NativeID string
	// CertificateFingerprint identifies the public certificate represented by the entry.
	CertificateFingerprint string
	// NativeExact proves every mechanism-specific attribute has the reviewed desired shape.
	NativeExact bool
	// NativeAttributesSHA256 binds safety-relevant native fields not exposed above.
	NativeAttributesSHA256 string
	// Owner is explicit Harbor ownership evidence; nil entries remain foreign.
	Owner *OwnerMarker
}

// Validate rejects trust facts that are malformed, unbounded, or unrelated to the request.
func (entry Entry) Validate(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if entry.Mechanism != request.Mechanism() {
		return fmt.Errorf("trust entry mechanism %q does not match request mechanism %q", entry.Mechanism, request.Mechanism())
	}
	if err := validateBoundedText("trust entry native ID", entry.NativeID, maximumNativeIDLength); err != nil {
		return err
	}
	if err := validateFingerprintText("trust entry certificate fingerprint", entry.CertificateFingerprint); err != nil {
		return err
	}
	if err := validateFingerprintText("trust entry native attribute fingerprint", entry.NativeAttributesSHA256); err != nil {
		return err
	}
	if entry.Owner != nil {
		if err := entry.Owner.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Observation contains the complete bounded trust entries relevant to one request.
type Observation struct {
	Request   Request
	Complete  bool
	Truncated bool
	Entries   []Entry
}

// Validate rejects observations that cannot safely drive classification or mutation.
func (observation Observation) Validate() error {
	if err := observation.Request.Validate(); err != nil {
		return err
	}
	if observation.Complete && observation.Truncated {
		return fmt.Errorf("trust observation cannot be complete and truncated")
	}
	if len(observation.Entries) > maximumTrustEntries {
		return fmt.Errorf("trust observation entries exceed limit %d", maximumTrustEntries)
	}
	seenIDs := make(map[string]struct{}, len(observation.Entries))
	for index, entry := range observation.Entries {
		if err := entry.Validate(observation.Request); err != nil {
			return fmt.Errorf("trust observation entry %d: %w", index, err)
		}
		if _, duplicate := seenIDs[entry.NativeID]; duplicate {
			return fmt.Errorf("trust observation entry native ID %q is duplicated", entry.NativeID)
		}
		seenIDs[entry.NativeID] = struct{}{}
	}
	return nil
}

// Change reports the observations surrounding one conditional trust mutation.
type Change struct {
	Attempted     bool
	Changed       bool
	Indeterminate bool
	Before        Observation
	After         Observation
}

// cloneRoot gives each request and observation independent ownership of public certificate bytes.
func cloneRoot(root certroot.Root) certroot.Root {
	root.CertificatePEM = append([]byte(nil), root.CertificatePEM...)
	return root
}

// normalizeRoot canonicalizes certificate instants before they become part of immutable request identity.
func normalizeRoot(root certroot.Root) certroot.Root {
	root = cloneRoot(root)
	root.NotBefore = root.NotBefore.UTC().Round(0)
	root.NotAfter = root.NotAfter.UTC().Round(0)
	return root
}

// cloneRequest gives backend boundaries independent ownership of public request material.
func cloneRequest(request Request) Request {
	request.root = cloneRoot(request.root)
	return request
}

// cloneEntry gives adapter callers independent ownership of native fact metadata and markers.
func cloneEntry(entry Entry) Entry {
	cloned := entry
	if entry.Owner != nil {
		owner := *entry.Owner
		cloned.Owner = &owner
	}
	return cloned
}

// cloneObservation prevents backend-owned slices and marker pointers crossing the adapter boundary.
func cloneObservation(observation Observation) Observation {
	cloned := observation
	cloned.Request = cloneRequest(observation.Request)
	cloned.Entries = make([]Entry, len(observation.Entries))
	for index, entry := range observation.Entries {
		cloned.Entries[index] = cloneEntry(entry)
	}
	return cloned
}

// sameRequest compares private canonical authority without exposing mutable construction fields.
func sameRequest(left Request, right Request) bool {
	return left.installationID == right.installationID &&
		left.requesterIdentity == right.requesterIdentity &&
		left.mechanism == right.mechanism &&
		left.root.Fingerprint == right.root.Fingerprint &&
		left.root.NotBefore.Equal(right.root.NotBefore) &&
		left.root.NotAfter.Equal(right.root.NotAfter) &&
		bytes.Equal(left.root.CertificatePEM, right.root.CertificatePEM)
}

// validateMechanism confines requests and facts to the trust scopes declared by the host policy.
func validateMechanism(mechanism networkpolicy.TrustMechanism) error {
	switch mechanism {
	case networkpolicy.DarwinCurrentUserTrust,
		networkpolicy.UbuntuSystemTrust,
		networkpolicy.WindowsCurrentUserTrust:
		return nil
	default:
		return fmt.Errorf("trust mechanism %q is unsupported", mechanism)
	}
}

// validateRoot validates the canonical public CA shape before a native adapter mutates its store.
//
// Certificate semantics are deliberately not parsed here: the helper must not
// gain a network-capable dependency through crypto/x509, and the selected
// native trust API validates the DER certificate at the mutation boundary.
func validateRoot(root certroot.Root) error {
	if len(root.CertificatePEM) == 0 {
		return fmt.Errorf("root certificate PEM is required")
	}
	if len(root.CertificatePEM) > maximumCertificatePEMBytes {
		return fmt.Errorf("root certificate PEM exceeds %d bytes", maximumCertificatePEMBytes)
	}
	if bytes.Contains(root.CertificatePEM, []byte("PRIVATE KEY")) {
		return fmt.Errorf("root certificate PEM must not contain private key material")
	}
	if err := validateFingerprintText("root certificate fingerprint", root.Fingerprint); err != nil {
		return err
	}
	if root.NotBefore.IsZero() || root.NotAfter.IsZero() {
		return fmt.Errorf("root certificate validity must be nonzero")
	}
	if root.NotBefore.Location() != time.UTC || root.NotAfter.Location() != time.UTC {
		return fmt.Errorf("root certificate validity must use UTC")
	}
	if !root.NotAfter.After(root.NotBefore) {
		return fmt.Errorf("root certificate validity must have a positive lifetime")
	}

	block, rest := pem.Decode(root.CertificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("root certificate PEM must contain one CERTIFICATE block")
	}
	consumed := root.CertificatePEM[:len(root.CertificatePEM)-len(rest)]
	if bytes.LastIndex(consumed, []byte("-----BEGIN CERTIFICATE-----")) != 0 {
		return fmt.Errorf("root certificate PEM contains leading data")
	}
	if len(block.Headers) != 0 {
		return fmt.Errorf("root certificate PEM headers are not supported")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("root certificate PEM contains trailing data")
	}
	canonicalPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})
	if !bytes.Equal(canonicalPEM, root.CertificatePEM) {
		return fmt.Errorf("root certificate PEM is not canonical")
	}
	digest := sha256.Sum256(block.Bytes)
	if hex.EncodeToString(digest[:]) != root.Fingerprint {
		return fmt.Errorf("root certificate fingerprint does not match certificate material")
	}
	return nil
}

// validateBoundedText rejects malformed native identity text before it enters a fingerprint or mutation guard.
func validateBoundedText(label, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have surrounding whitespace", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

// validateFingerprintText requires the lowercase hexadecimal spelling used by trust CAS evidence.
func validateFingerprintText(label, value string) error {
	if len(value) != canonicalFingerprintLength {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", label, canonicalFingerprintLength)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", label, canonicalFingerprintLength)
	}
	return nil
}
