//go:build windows

package windowsinstallation

import (
	"errors"
	"runtime"
	"testing"
	"unsafe"

	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/platform/trust"
	"github.com/goforj/harbor/internal/trust/certroot"
	"github.com/goforj/harbor/internal/trust/localca"
	"golang.org/x/sys/windows"
)

// TestNativeRestrictedMachineTrustObservation compares registry-store access under the actual product runtime token without certificate mutations.
func TestNativeRestrictedMachineTrustObservation(t *testing.T) {
	source := windows.GetCurrentProcessToken()
	if !source.IsElevated() {
		t.Skip("disposable high native controller is required")
	}
	var opened windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &opened); err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	restricted, err := restrictedRuntimeToken(opened)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, impersonation); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			t.Fatalf("restore native controller: %v", err)
		}
	}()
	var actual windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &actual); err != nil {
		t.Fatal(err)
	}
	facts, err := inspectRuntimeToken(actual)
	actual.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual restricted machine trust observer: %+v", facts)
	name := windows.StringToUTF16Ptr("ROOT")
	writable, writableErr := windows.CertOpenStore(uintptr(windows.CERT_STORE_PROV_SYSTEM_REGISTRY), 0, 0, uint32(windows.CERT_SYSTEM_STORE_LOCAL_MACHINE), uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if writableErr == nil {
		windows.CertCloseStore(writable, 0)
		t.Fatal("existing machine trust observer unexpectedly obtained write-capable store")
	}
	t.Logf("existing write-capable machine ROOT open: %v", writableErr)
	if !errors.Is(writableErr, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("write-capable open failed for unrelated reason: %v", writableErr)
	}
	readonly, err := windows.CertOpenStore(uintptr(windows.CERT_STORE_PROV_SYSTEM_REGISTRY), 0, 0, uint32(windows.CERT_SYSTEM_STORE_LOCAL_MACHINE|windows.CERT_STORE_READONLY_FLAG|windows.CERT_STORE_OPEN_EXISTING_FLAG), uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	if err != nil {
		t.Fatalf("read-only machine ROOT observation: %v", err)
	}
	defer windows.CertCloseStore(readonly, 0)
	certificate, err := windows.CertEnumCertificatesInStore(readonly, nil)
	if err != nil {
		t.Fatalf("enumerate read-only machine ROOT: %v", err)
	}
	if err := windows.CertFreeCertificateContext(certificate); err != nil {
		t.Fatal(err)
	}
	t.Log("read-only existing machine ROOT enumeration succeeded without certificate mutations")
	authority, err := localca.New(localca.Config{})
	if err != nil {
		t.Fatal(err)
	}
	material := authority.Material()
	request, err := trust.NewRequestForRequester("windows-low-observer-native", facts.UserSID, networkpolicy.WindowsMachineTrust, certroot.Root{
		CertificatePEM: material.CertificatePEM, Fingerprint: material.Fingerprint, NotBefore: material.NotBefore, NotAfter: material.NotAfter,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := trust.NewMachine()
	if err != nil {
		t.Fatal(err)
	}
	observation, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatalf("production machine trust observer under actual E0 token: %v", err)
	}
	assessment, err := observation.Classify()
	if err != nil {
		t.Fatal(err)
	}
	if assessment.State != trust.StateAbsent || assessment.Owned != trust.OwnedStateAbsent {
		t.Fatalf("generated uninstalled public root was not absent: %+v", assessment)
	}
	before, err := observation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.EnsureIfObserved(t.Context(), request, before); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("actual E0 trust mutation did not preserve machine write refusal: %v", err)
	}
	after, err := adapter.Observe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := after.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != before {
		t.Fatal("refused E0 mutation changed relevant machine trust contents")
	}
	t.Log("production Adapter.Observe succeeded under actual restricted token; EnsureIfObserved denied before certificate write, public-root contents unchanged")
}
