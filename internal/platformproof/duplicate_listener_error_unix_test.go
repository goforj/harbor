//go:build !windows

package platformproof

import (
	"errors"
	"syscall"
	"testing"
)

// platformAddressInUseError returns the native Unix bind-conflict sentinel used by shared tests.
func platformAddressInUseError() error {
	return syscall.EADDRINUSE
}

// TestIsAddressInUseErrorUsesUnixSentinel keeps Unix conflict classification tied to errno.
func TestIsAddressInUseErrorUsesUnixSentinel(t *testing.T) {
	t.Parallel()

	if !isAddressInUseError(syscall.EADDRINUSE) {
		t.Fatal("expected Unix address-in-use sentinel to pass")
	}
	if isAddressInUseError(errors.New("address already in use")) {
		t.Fatal("expected matching text without the Unix sentinel to fail")
	}
}
