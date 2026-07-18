//go:build windows

package loopback

import (
	"context"
	"net/netip"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWindowsAddressRowUsesInfiniteManualSkipAsSourceDefaults verifies the exact CreateUnicastIpAddressEntry input.
func TestWindowsAddressRowUsesInfiniteManualSkipAsSourceDefaults(t *testing.T) {
	interf := InterfaceFact{Name: "Loopback Pseudo-Interface 1", Index: 1, Kind: InterfaceKindWindowsSoftware, NativeLoopback: true}
	prefix := netip.PrefixFrom(testAddress, 32)
	row := windowsAddressRow(interf, prefix)
	address, ok := windowsIPv4Address(&row.Address)
	if !ok || address != testAddress || row.InterfaceIndex != 1 || row.OnLinkPrefixLength != 32 {
		t.Fatalf("windowsAddressRow() target = %s/%d on %d", address, row.OnLinkPrefixLength, row.InterfaceIndex)
	}
	if row.SkipAsSource == 0 || row.PrefixOrigin != windows.IpPrefixOriginManual || row.SuffixOrigin != windows.IpSuffixOriginManual {
		t.Fatalf("windowsAddressRow() attributes = skip %d, prefix %d, suffix %d", row.SkipAsSource, row.PrefixOrigin, row.SuffixOrigin)
	}
	if row.ValidLifetime != ^uint32(0) || row.PreferredLifetime != ^uint32(0) {
		t.Fatalf("windowsAddressRow() lifetimes = valid %d, preferred %d", row.ValidLifetime, row.PreferredLifetime)
	}
}

// TestWindowsBackendObservesWithoutMutation exercises both IP Helper fact tables on Windows CI.
func TestWindowsBackendObservesWithoutMutation(t *testing.T) {
	adapter := New()
	observation, err := adapter.Observe(context.Background(), netip.MustParseAddr("127.254.254.254"))
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.State != StateAbsent {
		t.Skipf("host already assigns the reserved test identity: %+v", observation)
	}
}

// TestWindowsOriginsRemainBounded covers known and unknown IP Helper origin values.
func TestWindowsOriginsRemainBounded(t *testing.T) {
	prefixes := map[uint32]AddressOrigin{
		windows.IpPrefixOriginOther:               AddressOriginOther,
		windows.IpPrefixOriginManual:              AddressOriginManual,
		windows.IpPrefixOriginWellKnown:           AddressOriginWellKnown,
		windows.IpPrefixOriginDhcp:                AddressOriginDHCP,
		windows.IpPrefixOriginRouterAdvertisement: AddressOriginRouterAdvertisement,
		windows.IpPrefixOriginUnchanged:           AddressOriginUnchanged,
		99:                                        AddressOriginUnknown,
	}
	for input, want := range prefixes {
		if got := windowsPrefixOrigin(input); got != want {
			t.Fatalf("windowsPrefixOrigin(%d) = %q, want %q", input, got, want)
		}
	}
	suffixes := map[uint32]AddressOrigin{
		windows.IpSuffixOriginOther:            AddressOriginOther,
		windows.IpSuffixOriginManual:           AddressOriginManual,
		windows.IpSuffixOriginWellKnown:        AddressOriginWellKnown,
		windows.IpSuffixOriginDhcp:             AddressOriginDHCP,
		windows.IpSuffixOriginLinkLayerAddress: AddressOriginLinkLayer,
		windows.IpSuffixOriginRandom:           AddressOriginRandom,
		windows.IpSuffixOriginUnchanged:        AddressOriginUnchanged,
		99:                                     AddressOriginUnknown,
	}
	for input, want := range suffixes {
		if got := windowsSuffixOrigin(input); got != want {
			t.Fatalf("windowsSuffixOrigin(%d) = %q, want %q", input, got, want)
		}
	}
	states := map[uint32]AddressState{
		windows.IpDadStateInvalid:    AddressStateInvalid,
		windows.IpDadStateTentative:  AddressStateTentative,
		windows.IpDadStateDuplicate:  AddressStateDuplicate,
		windows.IpDadStateDeprecated: AddressStateDeprecated,
		windows.IpDadStatePreferred:  AddressStatePreferred,
		99:                           AddressStateUnknown,
	}
	for input, want := range states {
		if got := windowsDADState(input); got != want {
			t.Fatalf("windowsDADState(%d) = %q, want %q", input, got, want)
		}
	}
}
