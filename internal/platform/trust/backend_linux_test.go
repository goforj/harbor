//go:build linux

package trust

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUbuntuBundleMatchCountingBindsDERAndBoundsDuplicates covers the active system-store parser.
func TestUbuntuBundleMatchCountingBindsDERAndBoundsDuplicates(t *testing.T) {
	request := ubuntuTrustTestRequest(t)
	root := request.Root()
	matches, err := countUbuntuBundleMatches(root.CertificatePEM, request.AuthorityFingerprint())
	if err != nil || matches != 1 {
		t.Fatalf("countUbuntuBundleMatches() = (%d, %v)", matches, err)
	}
	block, _ := pem.Decode(root.CertificatePEM)
	other := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: append([]byte(nil), block.Bytes...)})
	other[len(other)-2] ^= 1
	if _, err := countUbuntuBundleMatches(other, request.AuthorityFingerprint()); err == nil {
		t.Fatal("countUbuntuBundleMatches() accepted malformed PEM")
	}
	tooMany := bytes.Repeat(root.CertificatePEM, maximumUbuntuActiveRootMatches+1)
	if _, err := countUbuntuBundleMatches(tooMany, request.AuthorityFingerprint()); err == nil {
		t.Fatal("countUbuntuBundleMatches() accepted excessive matching roots")
	}
	if got := ubuntuCertificateFingerprint([]byte("malformed")); len(got) != canonicalFingerprintLength || strings.Trim(got, "0123456789abcdef") != "" {
		t.Fatalf("ubuntuCertificateFingerprint(malformed) = %q", got)
	}
}

// TestUbuntuLimitedBufferDrainsWhileRetainingOnlyItsBound proves child output cannot exceed evidence bounds.
func TestUbuntuLimitedBufferDrainsWhileRetainingOnlyItsBound(t *testing.T) {
	buffer := &ubuntuLimitedBuffer{maximum: 4}
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil || written != 6 || buffer.String() != "abcd" {
		t.Fatalf("Write() = (%d, %v), output = %q", written, err, buffer.String())
	}
}

// TestPrivilegedUbuntuTrustAdapterLifecycle exercises the production system bundle when explicitly opted in.
func TestPrivilegedUbuntuTrustAdapterLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_PRIVILEGED_TRUST_TEST") != "1" {
		t.Skip("set HARBOR_PRIVILEGED_TRUST_TEST=1 on a disposable Ubuntu host")
	}
	if os.Geteuid() != 0 {
		t.Fatal("privileged Ubuntu trust lifecycle requires root")
	}
	for _, path := range []string{
		filepath.Join(ubuntuTrustMarkerDirectory, ubuntuTrustMarkerName),
		filepath.Join(ubuntuTrustSourceDirectory, ubuntuTrustSourceName),
		filepath.Join(ubuntuTrustSourceDirectory, ubuntuTrustPendingName),
	} {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			t.Fatalf("fixed Harbor trust path %q must be absent before lifecycle test: %v", path, err)
		}
	}

	request := ubuntuTrustTestRequest(t)
	store := ubuntuSystemTrust{}
	if _, err := store.snapshot(t.Context(), request); err != nil {
		t.Fatalf("native Ubuntu trust snapshot error = %v", err)
	}
	adapter := newAdapter(newUbuntuTrustBackend(store))
	t.Cleanup(func() {
		observation, observeErr := adapter.Observe(context.Background(), request)
		if observeErr != nil {
			t.Errorf("cleanup Observe() error = %v: %v", observeErr, errors.Unwrap(observeErr))
			return
		}
		assessment, classifyErr := observation.Classify()
		if classifyErr != nil || assessment.Owned == OwnedStateAbsent {
			return
		}
		fingerprint, fingerprintErr := observation.Fingerprint()
		if fingerprintErr != nil {
			t.Errorf("cleanup Fingerprint() error = %v", fingerprintErr)
			return
		}
		if _, releaseErr := adapter.ReleaseIfObserved(context.Background(), request, fingerprint); releaseErr != nil {
			t.Errorf("cleanup ReleaseIfObserved() error = %v: %v", releaseErr, errors.Unwrap(releaseErr))
		}
	})
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe() before error = %v: %v", err, errors.Unwrap(err))
	}
	beforeAssessment, err := before.Classify()
	if err != nil || beforeAssessment.State != StateAbsent {
		t.Fatalf("Classify() before = %#v, %v", beforeAssessment, err)
	}
	beforeFingerprint, err := before.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() before error = %v", err)
	}
	ensured, err := adapter.EnsureIfObserved(t.Context(), request, beforeFingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v: %v", err, errors.Unwrap(err))
	}
	ensuredAssessment, err := ensured.After.Classify()
	if err != nil || ensuredAssessment.State != StateExact {
		t.Fatalf("Classify() ensured = %#v, %v", ensuredAssessment, err)
	}
	ensuredFingerprint, err := ensured.After.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() ensured error = %v", err)
	}
	released, err := adapter.ReleaseIfObserved(t.Context(), request, ensuredFingerprint)
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v: %v", err, errors.Unwrap(err))
	}
	releasedAssessment, err := released.After.Classify()
	if err != nil || releasedAssessment.State != StateAbsent {
		t.Fatalf("Classify() released = %#v, %v", releasedAssessment, err)
	}
}
