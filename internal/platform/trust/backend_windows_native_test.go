//go:build windows

package trust

import (
	"context"
	"crypto/x509"
	"os"
	"testing"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/trust/certificates"
	"github.com/goforj/harbor/internal/trust/localca"
	"golang.org/x/sys/windows"
)

// TestWindowsCurrentUserRootLifecycle proves the production CryptoAPI boundary adds, marks, observes, and removes one generated authority.
func TestWindowsCurrentUserRootLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_WINDOWS_TRUST_TEST") != "1" {
		t.Skip("set HARBOR_WINDOWS_TRUST_TEST=1 to mutate CurrentUser Root")
	}
	runWindowsRootLifecycle(t, networkpolicy.WindowsCurrentUserTrust, New)
}

// TestWindowsMachineRootLifecycle proves the elevated production boundary establishes system trust and cleans up exactly.
func TestWindowsMachineRootLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_WINDOWS_MACHINE_TRUST_TEST") != "1" {
		t.Skip("set HARBOR_WINDOWS_MACHINE_TRUST_TEST=1 from an elevated process to mutate LocalMachine Root")
	}
	runWindowsRootLifecycle(t, networkpolicy.WindowsMachineTrust, NewMachine)
}

// runWindowsRootLifecycle proves one exact Windows trust scope through ownership, system verification, and release.
func runWindowsRootLifecycle(
	t *testing.T,
	mechanism networkpolicy.TrustMechanism,
	newAdapter func() (*Adapter, error),
) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser() error = %v", err)
	}
	request, authority := windowsNativeTrustFixture(t, user.User.Sid.String(), mechanism)
	adapter, err := newAdapter()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Log("observing initial CurrentUser Root state")
	before, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("Observe(before) error = %v", err)
	}
	beforeAssessment, err := before.Classify()
	if err != nil {
		t.Fatalf("Classify(before) error = %v", err)
	}
	if beforeAssessment.State != StateAbsent || beforeAssessment.Owned != OwnedStateAbsent {
		t.Fatalf("before assessment = %#v, want absent", beforeAssessment)
	}
	beforeFingerprint, err := before.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(before) error = %v", err)
	}
	t.Cleanup(func() {
		observation, observeErr := adapter.Observe(context.Background(), request)
		if observeErr != nil {
			t.Errorf("cleanup Observe() error = %v", observeErr)
			return
		}
		assessment, classifyErr := observation.Classify()
		if classifyErr != nil {
			t.Errorf("cleanup Classify() error = %v", classifyErr)
			return
		}
		if assessment.Owned == OwnedStateAbsent {
			return
		}
		fingerprint, fingerprintErr := observation.Fingerprint()
		if fingerprintErr != nil {
			t.Errorf("cleanup Fingerprint() error = %v", fingerprintErr)
			return
		}
		if _, releaseErr := adapter.ReleaseIfObserved(context.Background(), request, fingerprint); releaseErr != nil {
			t.Errorf("cleanup ReleaseIfObserved() error = %v", releaseErr)
		}
	})
	t.Log("ensuring marked CurrentUser Root certificate")
	ensured, err := adapter.EnsureIfObserved(t.Context(), request, beforeFingerprint)
	if err != nil {
		t.Fatalf("EnsureIfObserved() error = %v", err)
	}
	ensuredAssessment, err := ensured.After.Classify()
	if err != nil {
		t.Fatalf("Classify(ensured) error = %v", err)
	}
	if !ensured.Attempted || !ensured.Changed ||
		ensuredAssessment.State != StateExact || ensuredAssessment.Owned != OwnedStateExact {
		t.Fatalf("ensure change = %#v, assessment = %#v", ensured, ensuredAssessment)
	}
	t.Log("verifying issued leaf through the Windows system trust engine")
	leaf, err := authority.Issue(t.Context(), []string{"harbor-native.test"})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	leafCertificate, err := x509.ParseCertificate(leaf.TLSCertificate.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	if _, err := leafCertificate.Verify(x509.VerifyOptions{DNSName: "harbor-native.test"}); err != nil {
		t.Fatalf("Verify(system roots) error = %v", err)
	}

	ensuredFingerprint, err := ensured.After.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint(ensured) error = %v", err)
	}
	t.Log("releasing marked CurrentUser Root certificate")
	released, err := adapter.ReleaseIfObserved(t.Context(), request, ensuredFingerprint)
	if err != nil {
		t.Fatalf("ReleaseIfObserved() error = %v", err)
	}
	releasedAssessment, err := released.After.Classify()
	if err != nil {
		t.Fatalf("Classify(released) error = %v", err)
	}
	if !released.Attempted || !released.Changed ||
		releasedAssessment.State != StateAbsent || releasedAssessment.Owned != OwnedStateAbsent {
		t.Fatalf("release change = %#v, assessment = %#v", released, releasedAssessment)
	}
}

// windowsNativeTrustFixture retains the signer so the lifecycle can prove Windows system-chain trust.
func windowsNativeTrustFixture(
	t *testing.T,
	requesterIdentity string,
	mechanism networkpolicy.TrustMechanism,
) (Request, *localca.Authority) {
	t.Helper()
	authority, err := localca.New(localca.Config{})
	if err != nil {
		t.Fatalf("localca.New() error = %v", err)
	}
	material := authority.Material()
	root := certificates.Root{
		CertificatePEM: material.CertificatePEM,
		Fingerprint:    material.Fingerprint,
		NotBefore:      material.NotBefore,
		NotAfter:       material.NotAfter,
	}
	request, err := NewRequestForRequester(
		"windows-native-trust-test",
		requesterIdentity,
		mechanism,
		root,
	)
	if err != nil {
		t.Fatalf("NewRequestForRequester() error = %v", err)
	}
	return request, authority
}
