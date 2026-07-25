package trust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	windowsCurrentUserTrustNativeIDPrefix = "windows-current-user-root-"
	windowsMachineTrustNativeIDPrefix     = "windows-machine-root-"
	windowsCurrentUserTrustOwnerPrefix    = "goforj.harbor.windows-current-user-root.v1|"
	windowsMachineTrustOwnerPrefix        = "goforj.harbor.windows-machine-root.v1|"
)

// windowsTrustEntry is one bounded certificate and ownership-property fact from an exact Windows Root store.
type windowsTrustEntry struct {
	CertificateDER []byte
	FriendlyName   string
}

// windowsTrustNative confines native effects to the current user's ROOT certificate store.
type windowsTrustNative interface {
	snapshot(context.Context, Request) ([]windowsTrustEntry, error)
	ensure(context.Context, Request) error
	release(context.Context, Request) error
}

// windowsTrustBackend translates bounded Windows Root facts into the portable trust model.
type windowsTrustBackend struct {
	mechanism networkpolicy.TrustMechanism
	native    windowsTrustNative
}

// newWindowsTrustBackend injects the native boundary for portable ownership and CAS tests.
func newWindowsTrustBackend(mechanism networkpolicy.TrustMechanism, native windowsTrustNative) backend {
	if native == nil {
		panic("trust.newWindowsTrustBackend requires a non-nil native store")
	}
	if mechanism != networkpolicy.WindowsCurrentUserTrust && mechanism != networkpolicy.WindowsMachineTrust {
		panic("trust.newWindowsTrustBackend requires a Windows trust mechanism")
	}
	return &windowsTrustBackend{mechanism: mechanism, native: native}
}

// observe converts one complete Windows Root snapshot into canonical facts.
func (backend *windowsTrustBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateWindowsTrustRequest(request, backend.mechanism); err != nil {
		return Observation{}, err
	}
	entries, err := backend.native.snapshot(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	if len(entries) > maximumTrustEntries {
		return Observation{}, fmt.Errorf("Windows trust store returned %d entries, limit is %d", len(entries), maximumTrustEntries)
	}
	observation := Observation{Request: request, Complete: true, Entries: make([]Entry, 0, len(entries))}
	for index, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		certificate, err := parseWindowsTrustCertificate(entry.CertificateDER)
		if err != nil {
			return Observation{}, fmt.Errorf("parse Windows trusted certificate %d: %w", index, err)
		}
		fingerprint := sha256.Sum256(certificate)
		fingerprintText := hex.EncodeToString(fingerprint[:])
		fact := Entry{
			Mechanism:              request.Mechanism(),
			NativeID:               windowsTrustNativeIDPrefix(request.Mechanism()) + fingerprintText + "-" + strconv.Itoa(index),
			CertificateFingerprint: fingerprintText,
			NativeExact:            true,
			NativeAttributesSHA256: windowsTrustAttributesFingerprint(request.Mechanism(), entry.FriendlyName),
		}
		if marker, ok := parseWindowsTrustOwner(entry.FriendlyName, request.Mechanism()); ok {
			fact.Owner = &marker
		}
		observation.Entries = append(observation.Entries, fact)
	}
	return observation, nil
}

// ensure adds one owned root only while the selected Root store remains absent for this authority.
func (backend *windowsTrustBackend) ensure(ctx context.Context, request Request, before Observation) error {
	if err := validateWindowsTrustRequest(request, backend.mechanism); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.State != StateAbsent || assessment.Owned != OwnedStateAbsent {
		return fmt.Errorf("Windows trust ensure requires an absent authority: %w", errNativeMutationConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.ensure(ctx, request)
}

// release removes only one exact certificate carrying the request's complete owner marker.
func (backend *windowsTrustBackend) release(ctx context.Context, request Request, before Observation) error {
	if err := validateWindowsTrustRequest(request, backend.mechanism); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.State != StateExact || assessment.Owned != OwnedStateExact {
		return fmt.Errorf("Windows trust release requires one exact owned root: %w", errNativeMutationConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.release(ctx, request)
}

// validateWindowsTrustRequest confines this backend to one canonical requester SID and exact store mechanism.
func validateWindowsTrustRequest(request Request, mechanism networkpolicy.TrustMechanism) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mechanism() != mechanism {
		return fmt.Errorf("Windows trust backend rejected mechanism %q", request.Mechanism())
	}
	if !canonicalWindowsTrustRequester(request.RequesterIdentity()) {
		return fmt.Errorf("Windows trust requester identity is not one canonical local-user SID")
	}
	if err := request.OwnerMarker().Validate(); err != nil {
		return fmt.Errorf("Windows trust owner marker: %w", err)
	}
	if len(windowsTrustOwnerName(request)) > maximumNativeIDLength {
		return fmt.Errorf("Windows trust owner marker exceeds %d bytes", maximumNativeIDLength)
	}
	return nil
}

// canonicalWindowsTrustRequester accepts the local/domain account SID shape used by the product profile.
func canonicalWindowsTrustRequester(value string) bool {
	if !strings.HasPrefix(value, "S-1-5-21-") || len(value) > maximumMarkerTextLength {
		return false
	}
	parts := strings.Split(value, "-")
	if len(parts) != 8 || parts[0] != "S" || parts[1] != "1" || parts[2] != "5" || parts[3] != "21" {
		return false
	}
	for _, part := range parts[4:] {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

// windowsRootDER converts the public PEM boundary into the DER bytes required by CryptoAPI.
func windowsRootDER(certificatePEM []byte) ([]byte, error) {
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(rest) != 0 || len(block.Bytes) == 0 {
		return nil, fmt.Errorf("Windows trust root is not one canonical CERTIFICATE PEM block")
	}
	return block.Bytes, nil
}

// parseWindowsTrustCertificate bounds native certificate bytes before hashing the CryptoAPI representation.
func parseWindowsTrustCertificate(der []byte) ([]byte, error) {
	if len(der) == 0 || len(der) > maximumCertificatePEMBytes {
		return nil, fmt.Errorf("certificate DER has invalid size %d", len(der))
	}
	return der, nil
}

// windowsTrustOwnerName encodes the complete owner marker in the certificate's friendly-name property.
func windowsTrustOwnerName(request Request) string {
	marker := request.OwnerMarker()
	return windowsTrustOwnerPrefix(request.Mechanism()) +
		marker.InstallationID + "|" +
		marker.RequesterIdentity + "|" +
		marker.AuthorityFingerprint
}

// parseWindowsTrustOwner accepts only the exact bounded marker shape written for one Windows trust scope.
func parseWindowsTrustOwner(value string, mechanism networkpolicy.TrustMechanism) (OwnerMarker, bool) {
	prefix := windowsTrustOwnerPrefix(mechanism)
	if prefix == "" || !strings.HasPrefix(value, prefix) || len(value) > maximumNativeIDLength {
		return OwnerMarker{}, false
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), "|")
	if len(parts) != 3 {
		return OwnerMarker{}, false
	}
	marker := OwnerMarker{
		Version:              ownerMarkerVersion,
		InstallationID:       parts[0],
		RequesterIdentity:    parts[1],
		Mechanism:            mechanism,
		AuthorityFingerprint: parts[2],
	}
	if !canonicalWindowsTrustRequester(marker.RequesterIdentity) || marker.Validate() != nil {
		return OwnerMarker{}, false
	}
	return marker, true
}

// windowsTrustNativeIDPrefix returns the stable native identity namespace for one Windows trust scope.
func windowsTrustNativeIDPrefix(mechanism networkpolicy.TrustMechanism) string {
	if mechanism == networkpolicy.WindowsMachineTrust {
		return windowsMachineTrustNativeIDPrefix
	}
	return windowsCurrentUserTrustNativeIDPrefix
}

// windowsTrustOwnerPrefix returns the stable ownership namespace for one Windows trust scope.
func windowsTrustOwnerPrefix(mechanism networkpolicy.TrustMechanism) string {
	switch mechanism {
	case networkpolicy.WindowsCurrentUserTrust:
		return windowsCurrentUserTrustOwnerPrefix
	case networkpolicy.WindowsMachineTrust:
		return windowsMachineTrustOwnerPrefix
	default:
		return ""
	}
}

// windowsTrustAttributesFingerprint binds CAS evidence to the mechanism and complete friendly-name property.
func windowsTrustAttributesFingerprint(mechanism networkpolicy.TrustMechanism, friendlyName string) string {
	namespace := "goforj.harbor.windows-current-user-root.attributes.v1\x00"
	if mechanism == networkpolicy.WindowsMachineTrust {
		namespace = "goforj.harbor.windows-machine-root.attributes.v1\x00"
	}
	digest := sha256.Sum256([]byte(namespace + friendlyName))
	return hex.EncodeToString(digest[:])
}
