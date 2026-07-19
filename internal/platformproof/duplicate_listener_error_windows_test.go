//go:build windows

package platformproof

import (
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// platformAddressInUseError returns the native Winsock bind-conflict sentinel used by shared tests.
func platformAddressInUseError() error {
	return windows.WSAEADDRINUSE
}

// TestIsAddressInUseErrorUsesWinsockSentinel rejects the incompatible os errno value on Windows.
func TestIsAddressInUseErrorUsesWinsockSentinel(t *testing.T) {
	t.Parallel()

	if !isAddressInUseError(windows.WSAEADDRINUSE) {
		t.Fatal("expected Winsock address-in-use sentinel to pass")
	}
	if isAddressInUseError(syscall.EADDRINUSE) {
		t.Fatal("expected syscall address-in-use compatibility value to fail on Windows")
	}
}
