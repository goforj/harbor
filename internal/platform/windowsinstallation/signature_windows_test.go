//go:build windows

package windowsinstallation

import (
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestNativeProviderCertificatePrefix rejects unreadable or incomplete native state without an unchecked dereference.
func TestNativeProviderCertificatePrefix(t *testing.T) {
	if _, err := readProviderCertificatePrefix(0); err == nil {
		t.Fatal("accepted an unreadable native provider address")
	}
	address, err := windows.VirtualAlloc(0, unsafe.Sizeof(providerCertificatePrefix{}), windows.MEM_RESERVE|windows.MEM_COMMIT, windows.PAGE_READWRITE)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.VirtualFree(address, 0, windows.MEM_RELEASE); err != nil {
			t.Error(err)
		}
	}()
	certificate := &windows.CertContext{}
	for _, test := range []struct {
		name     string
		prefix   providerCertificatePrefix
		accepted bool
	}{
		{"exact", providerCertificatePrefix{uint32(unsafe.Sizeof(providerCertificatePrefix{})), certificate}, true},
		{"short prefix", providerCertificatePrefix{1, certificate}, false},
		{"missing certificate", providerCertificatePrefix{uint32(unsafe.Sizeof(providerCertificatePrefix{})), nil}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var written uintptr
			if err := windows.WriteProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&test.prefix)), unsafe.Sizeof(test.prefix), &written); err != nil || written != unsafe.Sizeof(test.prefix) {
				t.Fatalf("write native provider fixture: bytes=%d error=%v", written, err)
			}
			actual, err := readProviderCertificatePrefix(address)
			if (err == nil) != test.accepted {
				t.Fatalf("native provider prefix read = %v, want accepted %v", err, test.accepted)
			}
			if test.accepted && actual != certificate {
				t.Fatal("provider certificate identity changed")
			}
		})
	}
	runtime.KeepAlive(certificate)
}
