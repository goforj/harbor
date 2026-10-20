//go:build windows && platformmutation

package loopback

import (
	"context"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/network/poolpolicy"
	"golang.org/x/sys/windows"
)

// windowsReadbackDiagnostic retains successive native facts without changing the result returned to reconciliation.
type windowsReadbackDiagnostic struct {
	windowsIPHelper
	t *testing.T
}

// address records conflicting create readbacks while preserving the original ownership decision.
func (helper windowsReadbackDiagnostic) address(identity windows.MibUnicastIpAddressRow) (windows.MibUnicastIpAddressRow, error) {
	observed, err := helper.windowsIPHelper.address(identity)
	if err != nil || ownedWindowsAddressRow(observed, identity) {
		return observed, err
	}
	helper.t.Logf("conflicting native readback: request=%+v observed=%+v", identity, observed)
	for sample := 0; sample < 10; sample++ {
		time.Sleep(100 * time.Millisecond)
		later, laterErr := helper.windowsIPHelper.address(identity)
		helper.t.Logf("read-only sample %d: row=%+v err=%v", sample, later, laterErr)
	}
	return observed, err
}

// createAddress records whether the native create API modifies its input despite its const-pointer contract.
func (helper windowsReadbackDiagnostic) createAddress(row *windows.MibUnicastIpAddressRow) error {
	before := *row
	err := helper.windowsIPHelper.createAddress(row)
	if before != *row {
		helper.t.Logf("native create changed input: before=%+v after=%+v err=%v", before, *row, err)
	}
	return err
}

// TestWindowsNativeCapacityReadback isolates repeated native assignment and cleanup with conflict diagnostics.
func TestWindowsNativeCapacityReadback(t *testing.T) {
	value := os.Getenv("HARBOR_WINDOWS_DIAGNOSTIC_POOL")
	if value == "" {
		t.Skip("HARBOR_WINDOWS_DIAGNOSTIC_POOL explicitly selects an isolated native proof range")
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.String() != value || prefix.Bits() != poolpolicy.PrefixBits {
		t.Fatal("diagnostic proof requires an explicit canonical IPv4 loopback /24")
	}
	addresses, err := poolpolicy.Addresses(prefix)
	if err != nil {
		t.Fatal(err)
	}
	platform := newPlatformBackend().(*platformBackend)
	platform.ipHelper = windowsReadbackDiagnostic{windowsIPHelper: platform.ipHelper, t: t}
	adapter := newAdapter(platform)
	initial := make(map[netip.Addr]string, len(addresses))
	for _, address := range addresses {
		observation, err := adapter.Observe(t.Context(), address)
		if err != nil || observation.State != StateAbsent {
			t.Fatalf("identity %s must initially be absent: %+v err=%v", address, observation, err)
		}
		initial[address], err = observation.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
	}
	created := make(map[netip.Addr]string, len(addresses))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for index := len(addresses) - 1; index >= 0; index-- {
			address := addresses[index]
			fingerprint, ok := created[address]
			if !ok {
				continue
			}
			if _, err := adapter.ReleaseIfObserved(ctx, address, fingerprint); err != nil {
				t.Errorf("release %s: %v", address, err)
			}
		}
	})
	for _, address := range addresses {
		change, err := adapter.EnsureIfObserved(t.Context(), address, initial[address])
		if err != nil || change.After.State != StateExact {
			t.Fatalf("ensure %s: %+v err=%+v", address, change, err)
		}
		created[address], err = change.After.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
	}
}
