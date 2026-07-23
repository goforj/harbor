//go:build darwin

package trust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

var darwinAdministratorTrustMutationMutex sync.Mutex

const (
	darwinUserTrustNativeIDPrefix      = "darwin-user-trust-"
	darwinAdminTrustNativeIDPrefix     = "darwin-admin-trust-"
	darwinTrustOwnerPrefix             = "v1|"
	darwinAdministratorRootLabelPrefix = "com.goforj.harbor.admin-root.v1|"
)

// darwinTrustEntry is the bounded native certificate fact used by the portable trust adapter.
type darwinTrustEntry struct {
	CertificateDER []byte
	NativeExact    bool
}

// darwinTrustNative confines Security.framework effects to the selected trust domain.
type darwinTrustNative interface {
	snapshot(context.Context, Request) ([]darwinTrustEntry, error)
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

// observe converts one complete Darwin trust snapshot into canonical facts.
func (backend *darwinTrustBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateDarwinTrustRequest(request); err != nil {
		return Observation{}, err
	}
	entries, err := backend.native.snapshot(ctx, request)
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
		nativeID := darwinTrustNativeIDPrefix(request.Mechanism()) + fingerprintText + "-" + strconv.Itoa(index)
		fact := Entry{
			Mechanism:              request.Mechanism(),
			NativeID:               nativeID,
			CertificateFingerprint: fingerprintText,
			NativeExact:            entry.NativeExact,
			NativeAttributesSHA256: darwinTrustAttributesFingerprint(request.Mechanism(), entry.NativeExact),
		}
		if owned && fingerprintText == request.AuthorityFingerprint() {
			marker := request.OwnerMarker()
			fact.Owner = &marker
		}
		observation.Entries = append(observation.Entries, fact)
	}
	return observation, nil
}

// ensure applies one exact Darwin trust projection after the portable adapter admits the native observation.
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
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		if !darwinAdministratorTrustMutationMutex.TryLock() {
			return fmt.Errorf("administrator trust mutation is busy; retry the one-shot helper")
		}
		defer darwinAdministratorTrustMutationMutex.Unlock()
	}
	return backend.native.ensure(ctx, request)
}

// release removes only the exact Darwin trust projection admitted by the portable adapter.
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
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		if !darwinAdministratorTrustMutationMutex.TryLock() {
			return fmt.Errorf("administrator trust mutation is busy; retry the one-shot helper")
		}
		defer darwinAdministratorTrustMutationMutex.Unlock()
	}
	return backend.native.release(ctx, request)
}

// validateDarwinTrustRequest confines this backend to the macOS trust mechanisms.
func validateDarwinTrustRequest(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	switch request.Mechanism() {
	case networkpolicy.DarwinCurrentUserTrust, networkpolicy.DarwinAdministratorTrust:
		return nil
	default:
		return fmt.Errorf("Darwin trust backend rejected mechanism %q", request.Mechanism())
	}
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
func darwinTrustAttributesFingerprint(mechanism networkpolicy.TrustMechanism, exact bool) string {
	shape := "drifted"
	if exact {
		shape = "exact"
	}
	namespace := "goforj.harbor.darwin-user-trust.v1|"
	if mechanism == networkpolicy.DarwinAdministratorTrust {
		namespace = "goforj.harbor.darwin-admin-trust.v1|"
	}
	digest := sha256.Sum256([]byte(namespace + shape))
	return hex.EncodeToString(digest[:])
}

// darwinTrustNativeIDPrefix keeps native facts from distinct Security.framework domains disjoint.
func darwinTrustNativeIDPrefix(mechanism networkpolicy.TrustMechanism) string {
	if mechanism == networkpolicy.DarwinAdministratorTrust {
		return darwinAdminTrustNativeIDPrefix
	}
	return darwinUserTrustNativeIDPrefix
}

// darwinTrustOwnerAccount derives a bounded generic-keychain account without allowing caller-selected service names.
func darwinTrustOwnerAccount(request Request) string {
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		return darwinAdministratorTrustOwnerAccount(request)
	}
	return darwinTrustOwnerPrefix + request.InstallationID() + "|" + request.RequesterIdentity() + "|" + string(request.Mechanism()) + "|" + request.AuthorityFingerprint()
}

// darwinAdministratorTrustOwnerAccount makes one administrator marker unique to an authority instead of a requester.
func darwinAdministratorTrustOwnerAccount(request Request) string {
	return darwinTrustOwnerPrefix + string(request.Mechanism()) + "|" + request.AuthorityFingerprint()
}

// darwinAdministratorTrustOwnerAttribute preserves the complete canonical owner marker in an unencrypted keychain attribute.
func darwinAdministratorTrustOwnerAttribute(request Request) string {
	marker := request.OwnerMarker()
	return strconv.FormatUint(uint64(marker.Version), 10) + "|" + marker.InstallationID + "|" + marker.RequesterIdentity + "|" + string(marker.Mechanism) + "|" + marker.AuthorityFingerprint
}

// darwinAdministratorRootLabel reserves ownership metadata inside the root-only System.keychain boundary.
func darwinAdministratorRootLabel(request Request) string {
	return darwinAdministratorRootLabelPrefix + request.AuthorityFingerprint()
}

// validateDarwinTrustOwnerAccount keeps the marker identity within the native keychain API's bounded text shape.
func validateDarwinTrustOwnerAccount(request Request) error {
	account := darwinTrustOwnerAccount(request)
	if len(account) == 0 || len(account) > maximumNativeIDLength || strings.TrimSpace(account) != account || !utf8.ValidString(account) {
		return fmt.Errorf("Darwin trust owner account is invalid")
	}
	return nil
}

// validateDarwinAdministratorTrustOwnerAttribute keeps the administrator owner claim bounded before it reaches Security.framework.
func validateDarwinAdministratorTrustOwnerAttribute(request Request) error {
	attribute := darwinAdministratorTrustOwnerAttribute(request)
	if len(attribute) == 0 || len(attribute) > maximumNativeIDLength || strings.TrimSpace(attribute) != attribute || !utf8.ValidString(attribute) {
		return fmt.Errorf("Darwin administrator trust owner attribute is invalid")
	}
	return nil
}

// validateDarwinAdministratorRootLabel keeps certificate-item ownership evidence bounded before it reaches Security.framework.
func validateDarwinAdministratorRootLabel(request Request) error {
	label := darwinAdministratorRootLabel(request)
	if len(label) <= len(darwinAdministratorRootLabelPrefix) ||
		len(label) > maximumNativeIDLength ||
		strings.TrimSpace(label) != label ||
		!utf8.ValidString(label) {
		return fmt.Errorf("Darwin administrator root label is invalid")
	}
	if !strings.HasSuffix(label, request.AuthorityFingerprint()) || len(request.AuthorityFingerprint()) != canonicalFingerprintLength {
		return fmt.Errorf("Darwin administrator root label is not canonical")
	}
	return nil
}

// darwinAdministratorRollbackArtifact identifies one administrator-trust effect that can be safely undone.
type darwinAdministratorRollbackArtifact uint8

const (
	darwinAdministratorRollbackTrustMarker darwinAdministratorRollbackArtifact = iota + 1
	darwinAdministratorRollbackRootCertificate
)

// darwinAdministratorRollbackArtifacts identifies only effects made by the current administrator-trust invocation.
type darwinAdministratorRollbackArtifacts struct {
	TrustMarker     bool
	RootCertificate bool
}

// darwinAdministratorRootStoreState classifies the one exact Harbor-labelled certificate lookup.
type darwinAdministratorRootStoreState uint8

const (
	darwinAdministratorRootStoreAbsent darwinAdministratorRootStoreState = iota
	darwinAdministratorRootStoreOwned
)

// canAddCertificate reports whether the exact label is available for one atomic certificate add.
func (state darwinAdministratorRootStoreState) canAddCertificate() bool {
	return state == darwinAdministratorRootStoreAbsent
}

// rollbackOrder keeps ownership evidence until the artifact it protects has been removed.
func (created darwinAdministratorRollbackArtifacts) rollbackOrder() []darwinAdministratorRollbackArtifact {
	operations := make([]darwinAdministratorRollbackArtifact, 0, 3)
	if created.RootCertificate {
		operations = append(operations, darwinAdministratorRollbackRootCertificate)
	}
	if created.TrustMarker {
		operations = append(operations, darwinAdministratorRollbackTrustMarker)
	}
	return operations
}

// rollbackDarwinAdministratorArtifacts stops at the first failure so later ownership evidence remains available for retry.
func rollbackDarwinAdministratorArtifacts(created darwinAdministratorRollbackArtifacts, remove func(darwinAdministratorRollbackArtifact) error) error {
	for _, artifact := range created.rollbackOrder() {
		if err := remove(artifact); err != nil {
			return err
		}
	}
	return nil
}

// joinDarwinAdministratorRollbackError preserves the failed operation while exposing cleanup failures to callers.
func joinDarwinAdministratorRollbackError(operationErr error, cleanupErr error) error {
	return errors.Join(operationErr, cleanupErr)
}

// currentDarwinRequesterUID returns the process identity used by the current-user trust domain.
func currentDarwinRequesterUID() string {
	return strconv.Itoa(os.Getuid())
}

// validateDarwinTrustRequester prevents an elevated helper from silently mutating the wrong user's keychain.
func validateDarwinTrustRequester(request Request) error {
	if request.Mechanism() == networkpolicy.DarwinAdministratorTrust {
		return nil
	}
	if request.RequesterIdentity() == "" {
		return nil
	}
	if request.RequesterIdentity() != currentDarwinRequesterUID() {
		return fmt.Errorf("Darwin trust request targets UID %q, current process is UID %q", request.RequesterIdentity(), currentDarwinRequesterUID())
	}
	return nil
}
