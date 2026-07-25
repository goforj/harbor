//go:build windows

package dataplane

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestWindowsMediumIntegrityDirectLowPortLifecycle proves the production runtime owns and releases DNS, HTTP, and HTTPS without a broker.
func TestWindowsMediumIntegrityDirectLowPortLifecycle(t *testing.T) {
	if os.Getenv("HARBOR_WINDOWS_DIRECT_LOW_PORT_TEST") != "1" {
		t.Skip("set HARBOR_WINDOWS_DIRECT_LOW_PORT_TEST=1 to bind the Windows product low ports")
	}
	requireWindowsMediumIntegrityToken(t)
	listeners := ListenerPlan{
		DNS:   netip.MustParseAddrPort("127.0.0.2:53"),
		HTTP:  netip.MustParseAddrPort("127.0.0.1:80"),
		HTTPS: netip.MustParseAddrPort("127.0.0.1:443"),
	}
	runtime := mustRuntime(t, Config{
		Desired:             mustDesiredState(t, listeners, nil, nil),
		CertificateProvider: inertCertificateProvider(),
		StartupTimeout:      5 * time.Second,
		ShutdownTimeout:     5 * time.Second,
	})
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	if err := runtime.Start(parent); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		if err := runtime.Close(closeContext); err != nil {
			t.Errorf("cleanup Close() error = %v", err)
		}
	})
	snapshot := runtime.Snapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Snapshot().Validate() error = %v", err)
	}
	if snapshot.State != StateReady || snapshot.DNS.Address != listeners.DNS ||
		snapshot.Ingress.HTTPAddress != listeners.HTTP || snapshot.Ingress.HTTPSAddress != listeners.HTTPS {
		t.Fatalf("ready snapshot = %#v", snapshot)
	}

	closeContext, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	if err := runtime.Close(closeContext); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertDNSRebindable(t, listeners.DNS)
	assertTCPRebindable(t, listeners.HTTP)
	assertTCPRebindable(t, listeners.HTTPS)
}

// requireWindowsMediumIntegrityToken prevents an elevated runner from substituting for the shipping daemon identity.
func requireWindowsMediumIntegrityToken(t *testing.T) {
	t.Helper()
	token := windows.GetCurrentProcessToken()
	if token.IsElevated() {
		t.Fatal("direct low-port proof requires a non-elevated Windows token")
	}
	var size uint32
	err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &size)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size < uint32(unsafe.Sizeof(windows.Tokenmandatorylabel{})) {
		t.Fatalf("read Windows token integrity size: size = %d, error = %v", size, err)
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buffer[0], size, &size); err != nil {
		t.Fatalf("read Windows token integrity: %v", err)
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buffer[0]))
	sid := label.Label.Sid
	if sid == nil || sid.SubAuthorityCount() == 0 {
		t.Fatal("Windows token integrity SID is unavailable")
	}
	const mediumIntegrityRID = 8192
	if actual := sid.SubAuthority(uint32(sid.SubAuthorityCount() - 1)); actual != mediumIntegrityRID {
		t.Fatalf("Windows token integrity RID = %d, want %d", actual, mediumIntegrityRID)
	}
}
