package trust

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/goforj/harbor/internal/host/networkpolicy"
)

const (
	ubuntuTrustNativeID            = "ubuntu-system-trust-root"
	ubuntuForeignTrustNativeID     = "ubuntu-system-trust-foreign-root"
	ubuntuTrustOwnerPrefix         = "goforj.harbor.ubuntu-system-trust.v1|"
	ubuntuTrustAttributesDomain    = "goforj.harbor.ubuntu-system-trust.attributes.v1\x00"
	maximumUbuntuActiveRootMatches = 16
)

// ubuntuTrustSnapshot contains the bounded fixed-path and active-bundle facts needed for one trust decision.
type ubuntuTrustSnapshot struct {
	MarkerPresent      bool
	MarkerExact        bool
	Marker             *OwnerMarker
	SourcePresent      bool
	SourceExact        bool
	SourceFingerprint  string
	PendingPresent     bool
	PendingExact       bool
	PendingFingerprint string
	ActiveMatches      int
}

// ubuntuTrustNative confines native effects to Ubuntu's fixed local-CA source and system bundle.
type ubuntuTrustNative interface {
	snapshot(context.Context, Request) (ubuntuTrustSnapshot, error)
	ensure(context.Context, Request) error
	release(context.Context, Request) error
}

// ubuntuTrustBackend translates bounded Ubuntu system-store facts into the portable trust model.
type ubuntuTrustBackend struct {
	native ubuntuTrustNative
}

// newUbuntuTrustBackend injects the native boundary for portable ownership and recovery tests.
func newUbuntuTrustBackend(native ubuntuTrustNative) backend {
	if native == nil {
		panic("trust.newUbuntuTrustBackend requires a non-nil native store")
	}
	return &ubuntuTrustBackend{native: native}
}

// observe converts one complete Ubuntu trust snapshot into canonical facts.
func (backend *ubuntuTrustBackend) observe(ctx context.Context, request Request) (Observation, error) {
	if err := validateUbuntuTrustRequest(request); err != nil {
		return Observation{}, err
	}
	snapshot, err := backend.native.snapshot(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	if snapshot.ActiveMatches < 0 || snapshot.ActiveMatches > maximumUbuntuActiveRootMatches {
		return Observation{}, fmt.Errorf("Ubuntu trust bundle returned %d matching roots, limit is %d", snapshot.ActiveMatches, maximumUbuntuActiveRootMatches)
	}
	observation := Observation{Request: request, Complete: true}
	representedActiveRoot := false
	if snapshot.Marker != nil {
		fingerprint := snapshot.Marker.AuthorityFingerprint
		switch {
		case snapshot.SourceFingerprint != "":
			fingerprint = snapshot.SourceFingerprint
		case snapshot.PendingFingerprint != "":
			fingerprint = snapshot.PendingFingerprint
		}
		entry := Entry{
			Mechanism:              request.Mechanism(),
			NativeID:               ubuntuTrustNativeID,
			CertificateFingerprint: fingerprint,
			NativeExact:            snapshot.MarkerExact && snapshot.SourcePresent && snapshot.SourceExact && !snapshot.PendingPresent && snapshot.ActiveMatches == 1,
			NativeAttributesSHA256: ubuntuTrustAttributesFingerprint(snapshot),
			Owner:                  snapshot.Marker,
		}
		observation.Entries = append(observation.Entries, entry)
		representedActiveRoot = fingerprint == request.AuthorityFingerprint() && snapshot.ActiveMatches == 1
	} else if snapshot.SourcePresent && snapshot.SourceFingerprint == request.AuthorityFingerprint() {
		observation.Entries = append(observation.Entries, Entry{
			Mechanism:              request.Mechanism(),
			NativeID:               ubuntuForeignTrustNativeID,
			CertificateFingerprint: snapshot.SourceFingerprint,
			NativeExact:            snapshot.SourceExact && snapshot.ActiveMatches > 0,
			NativeAttributesSHA256: ubuntuTrustAttributesFingerprint(snapshot),
		})
		representedActiveRoot = snapshot.ActiveMatches > 0
	}
	if snapshot.ActiveMatches > 0 && !representedActiveRoot {
		observation.Entries = append(observation.Entries, Entry{
			Mechanism:              request.Mechanism(),
			NativeID:               ubuntuForeignTrustNativeID + "-bundle",
			CertificateFingerprint: request.AuthorityFingerprint(),
			NativeExact:            true,
			NativeAttributesSHA256: ubuntuTrustAttributesFingerprint(snapshot),
		})
	}
	return observation, nil
}

// ensure installs or repairs only the request's exact owned Ubuntu root.
func (backend *ubuntuTrustBackend) ensure(ctx context.Context, request Request, before Observation) error {
	if err := validateUbuntuTrustRequest(request); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.State != StateAbsent && assessment.State != StateOwnedDrifted {
		return fmt.Errorf("Ubuntu trust ensure requires absent or recoverable owned state: %w", errNativeMutationConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.ensure(ctx, request)
}

// release removes only an exact root or a uniquely marked interrupted release.
func (backend *ubuntuTrustBackend) release(ctx context.Context, request Request, before Observation) error {
	if err := validateUbuntuTrustRequest(request); err != nil {
		return err
	}
	if err := before.Validate(); err != nil {
		return err
	}
	assessment := classifyValidated(before)
	if assessment.Owned != OwnedStateExact && assessment.Owned != OwnedStateDrifted {
		return fmt.Errorf("Ubuntu trust release requires one exact or recoverable owned root: %w", errNativeMutationConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.native.release(ctx, request)
}

// validateUbuntuTrustRequest confines the backend to one canonical non-root UID and the system-store mechanism.
func validateUbuntuTrustRequest(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Mechanism() != networkpolicy.UbuntuSystemTrust {
		return fmt.Errorf("Ubuntu trust backend rejected mechanism %q", request.Mechanism())
	}
	identity := request.RequesterIdentity()
	if identity == "" || (len(identity) > 1 && identity[0] == '0') {
		return fmt.Errorf("Ubuntu trust requester identity is not one canonical non-root UID")
	}
	uid, err := strconv.ParseUint(identity, 10, 32)
	if err != nil || uid == 0 {
		return fmt.Errorf("Ubuntu trust requester identity is not one canonical non-root UID")
	}
	if err := request.OwnerMarker().Validate(); err != nil {
		return fmt.Errorf("Ubuntu trust owner marker: %w", err)
	}
	return nil
}

// ubuntuTrustOwnerText encodes the complete owner marker in one fixed protected sidecar.
func ubuntuTrustOwnerText(request Request) string {
	marker := request.OwnerMarker()
	return ubuntuTrustOwnerPrefix +
		marker.InstallationID + "|" +
		marker.RequesterIdentity + "|" +
		marker.AuthorityFingerprint + "\n"
}

// parseUbuntuTrustOwner accepts only the exact bounded marker shape written for Ubuntu system trust.
func parseUbuntuTrustOwner(value string) (OwnerMarker, bool) {
	if !strings.HasPrefix(value, ubuntuTrustOwnerPrefix) || len(value) > maximumNativeIDLength || !strings.HasSuffix(value, "\n") {
		return OwnerMarker{}, false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(value, ubuntuTrustOwnerPrefix), "\n"), "|")
	if len(parts) != 3 {
		return OwnerMarker{}, false
	}
	marker := OwnerMarker{
		Version:              ownerMarkerVersion,
		InstallationID:       parts[0],
		RequesterIdentity:    parts[1],
		Mechanism:            networkpolicy.UbuntuSystemTrust,
		AuthorityFingerprint: parts[2],
	}
	requester := marker.RequesterIdentity
	uid, err := strconv.ParseUint(requester, 10, 32)
	if err != nil || uid == 0 || (len(requester) > 1 && requester[0] == '0') || marker.Validate() != nil {
		return OwnerMarker{}, false
	}
	return marker, true
}

// ubuntuTrustAttributesFingerprint binds every fixed-path and active-bundle fact used by compare-and-swap.
func ubuntuTrustAttributesFingerprint(snapshot ubuntuTrustSnapshot) string {
	owner := ""
	if snapshot.Marker != nil {
		owner = fmt.Sprintf("%d|%s|%s|%s|%s",
			snapshot.Marker.Version,
			snapshot.Marker.InstallationID,
			snapshot.Marker.RequesterIdentity,
			snapshot.Marker.Mechanism,
			snapshot.Marker.AuthorityFingerprint,
		)
	}
	payload := fmt.Sprintf(
		"%s%t|%t|%s|%t|%t|%s|%t|%t|%s|%d",
		ubuntuTrustAttributesDomain,
		snapshot.MarkerPresent,
		snapshot.MarkerExact,
		owner,
		snapshot.SourcePresent,
		snapshot.SourceExact,
		snapshot.SourceFingerprint,
		snapshot.PendingPresent,
		snapshot.PendingExact,
		snapshot.PendingFingerprint,
		snapshot.ActiveMatches,
	)
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}
