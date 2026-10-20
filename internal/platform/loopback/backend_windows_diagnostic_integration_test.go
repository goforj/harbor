//go:build windows && platformmutation

package loopback

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/goforj/harbor/internal/network/poolpolicy"
	"github.com/goforj/harbor/internal/platformproof"
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
	helper.logAddressTable(identity, -1)
	for sample := 0; sample < 10; sample++ {
		time.Sleep(100 * time.Millisecond)
		later, laterErr := helper.windowsIPHelper.address(identity)
		helper.t.Logf("read-only sample %d: row=%+v err=%v", sample, later, laterErr)
		helper.logAddressTable(identity, sample)
	}
	return observed, err
}

// logAddressTable cross-checks the direct query against the independently copied native table.
func (helper windowsReadbackDiagnostic) logAddressTable(identity windows.MibUnicastIpAddressRow, sample int) {
	target, ok := windowsIPv4Address(&identity.Address)
	if !ok {
		helper.t.Fatal("diagnostic requested an invalid native IPv4 identity")
	}
	rows, err := helper.windowsIPHelper.addressRows()
	if err != nil {
		helper.t.Logf("table sample %d: err=%v", sample, err)
		return
	}
	count := 0
	for _, row := range rows {
		address, ipv4 := windowsIPv4Address(&row.Address)
		if ipv4 && address == target {
			helper.t.Logf("table sample %d: row=%+v", sample, row)
			count++
		}
	}
	helper.t.Logf("table sample %d: matching_rows=%d", sample, count)
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
	sequence := append(append([]netip.Addr(nil), addresses[8:40]...), addresses...)
	for _, address := range sequence {
		expected := initial[address]
		if retained, ok := created[address]; ok {
			expected = retained
		}
		change, err := adapter.EnsureIfObserved(t.Context(), address, expected)
		if err != nil || change.After.State != StateExact {
			t.Fatalf("ensure %s: %+v err=%+v", address, change, err)
		}
		created[address], err = change.After.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := platformproof.ProveProjectIdentities(t.Context(), platformproof.ProjectIdentityRequest{Addresses: addresses[:100], Port: 3306}); err != nil {
		t.Fatal(err)
	}

}

// TestWindowsNativeSkipAsSourceTransition tests Set only for absent rows created in this isolated transaction.
func TestWindowsNativeSkipAsSourceTransition(t *testing.T) {
	value := os.Getenv("HARBOR_WINDOWS_DIAGNOSTIC_POOL")
	if value == "" {
		t.Skip("HARBOR_WINDOWS_DIAGNOSTIC_POOL explicitly selects an isolated native proof range")
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || prefix.String() != value || prefix.Bits() != poolpolicy.PrefixBits {
		t.Fatal("diagnostic requires an explicit canonical loopback /24")
	}
	addresses, err := poolpolicy.Addresses(prefix)
	if err != nil {
		t.Fatal(err)
	}
	platform := newPlatformBackend().(*platformBackend)
	interfaces, err := platform.interfaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var candidates []InterfaceFact
	for _, interf := range interfaces {
		if interf.NativeLoopback {
			candidates = append(candidates, interf)
		}
	}
	if len(candidates) != 1 {
		t.Fatalf("require exactly one native loopback: %+v", candidates)
	}
	setter := ipHelperDLL.NewProc("SetUnicastIpAddressEntry")
	if err := setter.Find(); err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses[:100] {
		row, err := platform.windowsAddressRow(candidates[0], netip.PrefixFrom(address, 32))
		if err != nil {
			t.Fatal(err)
		}
		facts, err := platform.assignments(t.Context(), address)
		if err != nil || len(facts) != 0 {
			t.Fatalf("require absent address %s: %+v err=%v", address, facts, err)
		}
		if _, err := platform.ipHelper.address(row); !errors.Is(err, windows.ERROR_NOT_FOUND) {
			t.Fatalf("require absent direct row %s: %v", address, err)
		}
		if err := platform.revalidateInterface(t.Context(), candidates[0]); err != nil {
			t.Fatal(err)
		}
		var before, after windows.Filetime
		windows.GetSystemTimeAsFileTime(&before)
		if err := platform.ipHelper.createAddress(&row); err != nil {
			t.Fatal(err)
		}
		windows.GetSystemTimeAsFileTime(&after)
		observed, err := platform.ipHelper.address(row)
		if err != nil {
			t.Fatal(err)
		}
		birth := observed.CreationTimeStamp
		fresh := birth.Nanoseconds() >= before.Nanoseconds() && birth.Nanoseconds() <= after.Nanoseconds()
		// Cleanup accepts only the old/new SkipAsSource values on this freshly born exact row.
		t.Cleanup(func() {
			current, err := platform.ipHelper.address(row)
			if errors.Is(err, windows.ERROR_NOT_FOUND) {
				return
			}
			if err != nil || !fresh || !diagnosticCreatedWindowsRow(current, row, birth) {
				t.Errorf("preserve changed diagnostic row %s: %+v err=%v", address, current, err)
				return
			}
			if err := diagnosticWindowsTableMatches(platform.ipHelper, current); err != nil {
				t.Error(err)
				return
			}
			if err := platform.ipHelper.deleteAddress(&current); err != nil {
				t.Error(err)
				return
			}
			if err := platform.reconcileAddress(context.Background(), row, false); err != nil {
				t.Error(err)
			}
		})
		if !fresh || !diagnosticCreatedWindowsRow(observed, row, birth) {
			t.Fatalf("created row is not an exact fresh candidate: before=%+v after=%+v row=%+v", before, after, observed)
		}
		if err := diagnosticWindowsTableMatches(platform.ipHelper, observed); err != nil {
			t.Fatal(err)
		}
		t.Logf("fresh before Set: %+v", observed)
		updated := observed
		updated.SkipAsSource = 1
		// Reobserve immediately before Set so no previously existing or replaced assignment is repaired.
		current, err := platform.ipHelper.address(row)
		if err != nil || current != observed {
			t.Fatalf("row changed before Set: %+v err=%v", current, err)
		}
		status, _, _ := setter.Call(uintptr(unsafe.Pointer(&updated)))
		if err := windowsStatusError(status); err != nil {
			t.Fatal(err)
		}
		verified, err := platform.ipHelper.address(row)
		if err != nil || !diagnosticCreatedWindowsRow(verified, row, birth) || verified.SkipAsSource != 1 {
			t.Fatalf("Set readback: %+v err=%v", verified, err)
		}
		expected := observed
		expected.SkipAsSource = 1
		if verified != expected {
			t.Fatalf("Set changed another native field: before=%+v after=%+v", observed, verified)
		}
		if err := diagnosticWindowsTableMatches(platform.ipHelper, verified); err != nil {
			t.Fatal(err)
		}
		t.Logf("fresh after Set: %+v", verified)
	}
	if _, err := platformproof.ProveProjectIdentities(t.Context(), platformproof.ProjectIdentityRequest{Addresses: addresses[:100], Port: 3306}); err != nil {
		t.Fatal(err)
	}
}

// diagnosticCreatedWindowsRow confines the test transition to the exact new birth and every owned attribute.
func diagnosticCreatedWindowsRow(observed, expected windows.MibUnicastIpAddressRow, birth windows.Filetime) bool {
	if birth == (windows.Filetime{}) || observed.CreationTimeStamp != birth || observed.SkipAsSource > 1 {
		return false
	}
	observed.SkipAsSource = 1
	return exactWindowsAddressRow(observed, expected) && observed.Address == expected.Address && observed.ScopeId == expected.ScopeId
}

// diagnosticWindowsTableMatches requires the independent table to contain exactly the direct native observation.
func diagnosticWindowsTableMatches(helper windowsIPHelper, expected windows.MibUnicastIpAddressRow) error {
	rows, err := helper.addressRows()
	if err != nil {
		return err
	}
	target, _ := windowsIPv4Address(&expected.Address)
	count := 0
	for _, row := range rows {
		address, ipv4 := windowsIPv4Address(&row.Address)
		if !ipv4 || address != target {
			continue
		}
		count++
		if row != expected {
			return fmt.Errorf("independent table differs: direct=%+v table=%+v", expected, row)
		}
	}
	if count != 1 {
		return fmt.Errorf("independent table has %d entries for %s", count, target)
	}
	return nil
}

// TestWindowsDiagnosticCreatedRowRejectsDrift protects every transition and cleanup admission field.
func TestWindowsDiagnosticCreatedRowRejectsDrift(t *testing.T) {
	backend, _ := newWindowsTestBackend(windowsTestInterface())
	row, err := backend.windowsAddressRow(windowsTestInterface(), netip.PrefixFrom(testAddress, 32))
	if err != nil {
		t.Fatal(err)
	}
	row.DadState = windows.IpDadStatePreferred
	row.CreationTimeStamp = windows.Filetime{LowDateTime: 42}
	birth := row.CreationTimeStamp
	tests := []struct {
		name   string
		mutate func(*windows.MibUnicastIpAddressRow)
	}{
		{"birth", func(r *windows.MibUnicastIpAddressRow) { r.CreationTimeStamp.LowDateTime++ }},
		{"address", func(r *windows.MibUnicastIpAddressRow) { r.Address.Port++ }},
		{"luid", func(r *windows.MibUnicastIpAddressRow) { r.InterfaceLuid++ }},
		{"index", func(r *windows.MibUnicastIpAddressRow) { r.InterfaceIndex++ }},
		{"prefix", func(r *windows.MibUnicastIpAddressRow) { r.OnLinkPrefixLength-- }},
		{"prefix origin", func(r *windows.MibUnicastIpAddressRow) { r.PrefixOrigin++ }},
		{"suffix origin", func(r *windows.MibUnicastIpAddressRow) { r.SuffixOrigin++ }},
		{"valid lifetime", func(r *windows.MibUnicastIpAddressRow) { r.ValidLifetime-- }},
		{"preferred lifetime", func(r *windows.MibUnicastIpAddressRow) { r.PreferredLifetime-- }},
		{"DAD", func(r *windows.MibUnicastIpAddressRow) { r.DadState = windows.IpDadStateTentative }},
		{"scope", func(r *windows.MibUnicastIpAddressRow) { r.ScopeId++ }},
		{"skip enum", func(r *windows.MibUnicastIpAddressRow) { r.SkipAsSource = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := row
			test.mutate(&changed)
			if diagnosticCreatedWindowsRow(changed, row, birth) {
				t.Fatal("admitted changed row")
			}
		})
	}
	for _, skip := range []uint8{0, 1} {
		current := row
		current.SkipAsSource = skip
		if !diagnosticCreatedWindowsRow(current, row, birth) {
			t.Fatalf("rejected controlled SkipAsSource=%d", skip)
		}
	}
	if diagnosticCreatedWindowsRow(row, row, windows.Filetime{}) {
		t.Fatal("admitted unknown birth")
	}
}
