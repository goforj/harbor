//go:build darwin

package trust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	darwinTrustNativeIDPrefix = "darwin-user-trust-"
	darwinTrustOwnerPrefix    = "v1|"
)

// darwinTrustEntry is the bounded native certificate fact used by the portable trust adapter.
type darwinTrustEntry struct {
	CertificateDER []byte
	NativeExact    bool
}

// darwinTrustNative confines Security.framework effects to the current user's trust domain.
type darwinTrustNative interface {
	snapshot(context.Context) ([]darwinTrustEntry, error)
	ensure(context.Context, Request) error
	release(context.Context, Request) error
	ownerExists(context.Context, Request) (bool, error)
}

// darwinTrustBackend translates bounded Security.framework facts into the portable trust model.
type darwinTrustBackend struct {
	native darwinTrustNative
}

// newDarwinTrustBackend injects the native boundary so classification and CAS behavior remain testable without a keychain.
func newDarwinTrustBackend(native darwinTrustNative) backend {
	if native == nil {
		panic("trust.newDarwinTrustBackend requires a non-nil native store")
	}
	return &darwinTrustBackend{native: native}
}

// observe converts one complete current-user trust snapshot into canonical facts.
func (backend *darwinTrustBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateDarwinTrustRequest(request); err != nil {
		return Observation{}, err
	}
	entries, err := backend.native.snapshot(ctx)
	if err != nil {
		return Observation{}, err
	}
	if len(entries) > maximumTrustEntries {
		return Observation{}, fmt.Errorf("Darwin trust store returned %d entries, limit is %d", len(entries), maximumTrustEntries)
	}
	owned, err := backend.native.ownerExists(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	observation := Observation{Request: request, Complete: true, Entries: make([]Entry, 0, len(entries))}
	for index, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		certificate, err := parseDarwinTrustCertificate(entry.CertificateDER)
		if err != nil {
			return Observation{}, fmt.Errorf("parse Darwin trusted certificate %d: %w", index, err)
		}
		fingerprint := sha256.Sum256(certificate)
		fingerprintText := hex.EncodeToString(fingerprint[:])
		nativeID := darwinTrustNativeIDPrefix + fingerprintText + "-" + strconv.Itoa(index)
		fact := Entry{
			Mechanism:              request.Mechanism(),
			NativeID:               nativeID,
			CertificateFingerprint: fingerprintText,
			NativeExact:            entry.NativeExact,
			NativeAttributesSHA256: darwinTrustAttributesFingerprint(entry.NativeExact),
		}
		if owned && fingerprintText == request.AuthorityFingerprint() {
			marker := request.OwnerMarker()
			fact.Owner = &marker
		}
		observation.Entries = append(observation.Entries, fact)
	}
	return observation, nil
}

// ensure applies one exact current-user trust projection after the portable adapter admits the native observation.
func (backend *darwinTrustBackend) ensure(ctx context.Context, request Request, before Observation) error {
	if err := validateDarwinTrustRequest(request); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.ensure(ctx, request)
}

// release removes only the exact current-user trust projection admitted by the portable adapter.
func (backend *darwinTrustBackend) release(ctx context.Context, request Request, before Observation) error {
	if err := validateDarwinTrustRequest(request); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.State != StateExact || assessment.Owned != OwnedStateExact {
		return fmt.Errorf(
			"Darwin trust release requires one exact owned entry: %w",
			errNativeMutationConflict,
		)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.release(ctx, request)
}

// validateDarwinTrustRequest keeps this backend on the user-scoped macOS trust mechanism.
func validateDarwinTrustRequest(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mechanism() != networkpolicy.DarwinCurrentUserTrust {
		return fmt.Errorf("Darwin trust backend rejected mechanism %q", request.Mechanism())
	}
	return nil
}

// parseDarwinTrustCertificate bounds native certificate bytes before hashing the Security.framework representation.
func parseDarwinTrustCertificate(der []byte) ([]byte, error) {
	if len(der) == 0 || len(der) > maximumCertificatePEMBytes {
		return nil, fmt.Errorf("certificate DER has invalid size %d", len(der))
	}
	return der, nil
}

// darwinRootDER converts the public PEM boundary into the DER bytes required by Security.framework.
func darwinRootDER(certificatePEM []byte) ([]byte, error) {
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(rest) != 0 || len(block.Bytes) == 0 {
		return nil, fmt.Errorf("Darwin trust root is not one canonical CERTIFICATE PEM block")
	}
	return block.Bytes, nil
}

// darwinTrustAttributesFingerprint binds exactness to the reviewed Security.framework trust-settings shape.
func darwinTrustAttributesFingerprint(exact bool) string {
	shape := "drifted"
	if exact {
		shape = "exact"
	}
	digest := sha256.Sum256([]byte("goforj.harbor.darwin-user-trust.v1|" + shape))
	return hex.EncodeToString(digest[:])
}

// darwinTrustOwnerAccount derives a bounded generic-keychain account without allowing caller-selected service names.
func darwinTrustOwnerAccount(request Request) string {
	return darwinTrustOwnerPrefix + request.InstallationID() + "|" + request.RequesterIdentity() + "|" + string(request.Mechanism()) + "|" + request.AuthorityFingerprint()
}

// validateDarwinTrustOwnerAccount keeps the marker identity within the native keychain API's bounded text shape.
func validateDarwinTrustOwnerAccount(request Request) error {
	account := darwinTrustOwnerAccount(request)
	if len(account) == 0 || len(account) > maximumNativeIDLength || strings.TrimSpace(account) != account || !utf8.ValidString(account) {
		return fmt.Errorf("Darwin trust owner account is invalid")
	}
	return nil
}

// currentDarwinRequesterUID returns the process identity used by the current-user trust domain.
func currentDarwinRequesterUID() string {
	return strconv.Itoa(os.Getuid())
}

// validateDarwinTrustRequester prevents an elevated helper from silently mutating the wrong user's keychain.
func validateDarwinTrustRequester(request Request) error {
	if request.RequesterIdentity() == "" {
		return nil
	}
	if request.RequesterIdentity() != currentDarwinRequesterUID() {
		return fmt.Errorf("Darwin trust request targets UID %q, current process is UID %q", request.RequesterIdentity(), currentDarwinRequesterUID())
	}
	return nil
}
