package resolver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/network/identity"
)

const (
	ownerMarkerVersion         = uint16(1)
	maximumRuleFacts           = 256
	maximumServersPerRule      = 16
	maximumNativeIDLength      = 512
	maximumNamespaceLength     = 253
	maximumDNSLabelLength      = 63
	maximumAddressZoneLength   = 128
	maximumMarkerTextLength    = 128
	canonicalFingerprintLength = sha256.Size * 2
)

// Request is the immutable resolver authority derived from one validated host-network policy.
type Request struct {
	policy            networkpolicy.Policy
	installationID    identity.InstallationID
	policyFingerprint string
}

// NewRequest validates one installation and policy before deriving its exact resolver authority.
func NewRequest(installationID identity.InstallationID, policy networkpolicy.Policy) (Request, error) {
	if err := installationID.Validate(); err != nil {
		return Request{}, fmt.Errorf("resolver request installation ID: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return Request{}, fmt.Errorf("resolver request policy: %w", err)
	}
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return Request{}, fmt.Errorf("resolver request fingerprint policy: %w", err)
	}
	request := Request{
		policy:            policy,
		installationID:    installationID,
		policyFingerprint: policyFingerprint,
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Validate rejects zero, corrupt, or internally inconsistent resolver requests.
func (r Request) Validate() error {
	if err := r.installationID.Validate(); err != nil {
		return fmt.Errorf("resolver request installation ID: %w", err)
	}
	if err := r.policy.Validate(); err != nil {
		return fmt.Errorf("resolver request policy: %w", err)
	}
	wantFingerprint, err := r.policy.Fingerprint()
	if err != nil {
		return fmt.Errorf("resolver request fingerprint policy: %w", err)
	}
	if r.policyFingerprint != wantFingerprint {
		return fmt.Errorf("resolver request policy fingerprint is inconsistent")
	}
	return nil
}

// Policy returns the validated value policy from which this resolver request was derived.
func (r Request) Policy() networkpolicy.Policy {
	return r.policy
}

// InstallationID returns the validated installation identity carried by the ownership marker.
func (r Request) InstallationID() identity.InstallationID {
	return r.installationID
}

// PolicyFingerprint returns the canonical host-network policy digest carried by the ownership marker.
func (r Request) PolicyFingerprint() string {
	return r.policyFingerprint
}

// Mechanism returns the only resolver mechanism admitted by the validated policy.
func (r Request) Mechanism() networkpolicy.ResolverMechanism {
	return r.policy.Mechanisms.Resolver
}

// Suffix returns the only resolver namespace Harbor may claim.
func (r Request) Suffix() string {
	return r.policy.Suffix
}

// Endpoint returns the exact DNS socket the operating-system resolver must use.
func (r Request) Endpoint() netip.AddrPort {
	return r.policy.DNS.Advertised
}

// OwnerMarker returns the canonical native marker for this installation and policy.
func (r Request) OwnerMarker() OwnerMarker {
	return OwnerMarker{
		Version:           ownerMarkerVersion,
		InstallationID:    string(r.installationID),
		PolicyFingerprint: r.policyFingerprint,
	}
}

// OwnerMarker is bounded ownership evidence parsed from one native resolver artifact.
type OwnerMarker struct {
	Version           uint16
	InstallationID    string
	PolicyFingerprint string
}

// Validate rejects markers that cannot be compared canonically across helper processes.
func (m OwnerMarker) Validate() error {
	if m.Version == 0 {
		return fmt.Errorf("resolver owner marker version must be greater than zero")
	}
	if len(m.InstallationID) > maximumMarkerTextLength {
		return fmt.Errorf("resolver owner marker installation ID exceeds %d bytes", maximumMarkerTextLength)
	}
	if err := identity.InstallationID(m.InstallationID).Validate(); err != nil {
		return fmt.Errorf("resolver owner marker installation ID: %w", err)
	}
	if err := validateFingerprintText("resolver owner marker policy fingerprint", m.PolicyFingerprint); err != nil {
		return err
	}
	return nil
}

// RuleFact is one complete, bounded resolver rule observed through a native platform facility.
type RuleFact struct {
	Mechanism networkpolicy.ResolverMechanism
	// NativeID is emitted by the backend and can identify only a rule from this observation.
	NativeID  string
	Namespace string
	// Servers is a set because native enumeration order cannot grant or revoke ownership.
	Servers   []netip.AddrPort
	RouteOnly bool
	// NativeExact proves every mechanism-specific attribute has the reviewed desired shape.
	NativeExact bool
	// NativeAttributesSHA256 binds all safety-relevant raw fields, including drift not exposed above.
	NativeAttributesSHA256 string
	Owner                  *OwnerMarker
}

// Validate rejects rule facts that are malformed, unbounded, or unrelated to the requested suffix.
func (f RuleFact) Validate(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if !validMechanism(f.Mechanism) {
		return fmt.Errorf("resolver rule mechanism %q is unsupported", f.Mechanism)
	}
	if err := validateBoundedText("resolver rule native ID", f.NativeID, maximumNativeIDLength); err != nil {
		return err
	}
	if err := validateNamespace(f.Namespace); err != nil {
		return err
	}
	if !namespaceClaimsSuffix(f.Namespace, request.Suffix()) {
		return fmt.Errorf("resolver rule namespace %q does not claim %q", f.Namespace, request.Suffix())
	}
	if len(f.Servers) > maximumServersPerRule {
		return fmt.Errorf("resolver rule servers exceed limit %d", maximumServersPerRule)
	}
	seenServers := make(map[netip.AddrPort]struct{}, len(f.Servers))
	for index, server := range f.Servers {
		if err := validateServer(server); err != nil {
			return fmt.Errorf("resolver rule server %d: %w", index, err)
		}
		if _, exists := seenServers[server]; exists {
			return fmt.Errorf("resolver rule repeats server %s", server)
		}
		seenServers[server] = struct{}{}
	}
	if err := validateFingerprintText("resolver rule native attribute fingerprint", f.NativeAttributesSHA256); err != nil {
		return err
	}
	if f.Owner != nil {
		if err := f.Owner.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Observation contains the complete bounded rule set relevant to one resolver request.
type Observation struct {
	Request   Request
	Complete  bool
	Truncated bool
	Rules     []RuleFact
}

// Validate rejects observations that could not safely drive classification or mutation.
func (o Observation) Validate() error {
	if err := o.Request.Validate(); err != nil {
		return err
	}
	if o.Complete && o.Truncated {
		return fmt.Errorf("resolver observation cannot be complete and truncated")
	}
	if len(o.Rules) > maximumRuleFacts {
		return fmt.Errorf("resolver observation rules exceed limit %d", maximumRuleFacts)
	}
	for index, rule := range o.Rules {
		if err := rule.Validate(o.Request); err != nil {
			return fmt.Errorf("resolver observation rule %d: %w", index, err)
		}
	}
	return nil
}

// Change reports the observations surrounding one conditional resolver mutation.
type Change struct {
	Attempted     bool
	Changed       bool
	Indeterminate bool
	Before        Observation
	After         Observation
}

// cloneObservation prevents backend-owned slices and marker pointers from crossing the adapter boundary.
func cloneObservation(observation Observation) Observation {
	cloned := observation
	cloned.Rules = make([]RuleFact, len(observation.Rules))
	for index, rule := range observation.Rules {
		cloned.Rules[index] = rule
		cloned.Rules[index].Servers = slices.Clone(rule.Servers)
		if rule.Owner != nil {
			owner := *rule.Owner
			cloned.Rules[index].Owner = &owner
		}
	}
	return cloned
}

// sameRequest compares the private canonical authority without exposing mutable construction fields.
func sameRequest(left Request, right Request) bool {
	return left.policy == right.policy &&
		left.installationID == right.installationID &&
		left.policyFingerprint == right.policyFingerprint
}

// validMechanism confines facts to the three resolver facilities admitted by networkpolicy.Policy.
func validMechanism(mechanism networkpolicy.ResolverMechanism) bool {
	switch mechanism {
	case networkpolicy.DarwinResolverFile,
		networkpolicy.UbuntuSystemdResolved,
		networkpolicy.WindowsNRPT:
		return true
	default:
		return false
	}
}

// validateNamespace requires one canonical lowercase suffix namespace with DNS label bounds.
func validateNamespace(namespace string) error {
	if len(namespace) < 2 || len(namespace) > maximumNamespaceLength || namespace[0] != '.' {
		return fmt.Errorf("resolver rule namespace %q is not a bounded suffix", namespace)
	}
	if namespace != strings.ToLower(namespace) || !utf8.ValidString(namespace) {
		return fmt.Errorf("resolver rule namespace %q is not canonical lowercase ASCII", namespace)
	}
	labels := strings.Split(namespace[1:], ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > maximumDNSLabelLength {
			return fmt.Errorf("resolver rule namespace %q contains an invalid label length", namespace)
		}
		for index, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
				continue
			}
			if character == '-' && index > 0 && index < len(label)-1 {
				continue
			}
			return fmt.Errorf("resolver rule namespace %q contains a noncanonical label", namespace)
		}
	}
	return nil
}

// namespaceClaimsSuffix retains exact and more-specific claims that can affect Harbor names.
func namespaceClaimsSuffix(namespace string, suffix string) bool {
	return namespace == suffix || strings.HasSuffix(namespace, suffix)
}

// validateServer requires a canonical address, explicit port, and bounded IPv6 scope identity.
func validateServer(server netip.AddrPort) error {
	address := server.Addr()
	if !server.IsValid() || address != address.Unmap() || server.Port() == 0 {
		return fmt.Errorf("server %s is not a canonical address with a nonzero port", server)
	}
	if address.Is4() && address.Zone() != "" {
		return fmt.Errorf("server %s has an invalid IPv4 zone", server)
	}
	if address.Zone() != "" {
		if err := validateBoundedText("resolver rule server zone", address.Zone(), maximumAddressZoneLength); err != nil {
			return err
		}
	}
	return nil
}

// validateBoundedText excludes controls and ambiguous boundaries from native identifiers.
func validateBoundedText(label string, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not have surrounding whitespace", label)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s must not contain control characters", label)
		}
	}
	return nil
}

// validateFingerprintText keeps fingerprints in the canonical lowercase SHA-256 namespace.
func validateFingerprintText(label string, value string) error {
	if len(value) != canonicalFingerprintLength {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", label, canonicalFingerprintLength)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", label, canonicalFingerprintLength)
	}
	return nil
}
