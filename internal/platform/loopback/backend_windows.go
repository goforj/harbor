//go:build windows

package loopback

import (
	"context"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ipHelperDLL                   = windows.NewLazySystemDLL("iphlpapi.dll")
	initializeUnicastAddressEntry = ipHelperDLL.NewProc("InitializeUnicastIpAddressEntry")
	createUnicastAddressEntry     = ipHelperDLL.NewProc("CreateUnicastIpAddressEntry")
	deleteUnicastAddressEntry     = ipHelperDLL.NewProc("DeleteUnicastIpAddressEntry")
)

// platformBackend implements exact Windows loopback effects through IP Helper.
type platformBackend struct{}

// newPlatformBackend creates the Windows adapter without acquiring privilege.
func newPlatformBackend() backend {
	return platformBackend{}
}

// interfaces verifies loopback identity from IP Helper's IF_TYPE_SOFTWARE_LOOPBACK fact.
func (platformBackend) interfaces(ctx context.Context) ([]InterfaceFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormalWithoutStatistics, &table); err != nil {
		return nil, fmt.Errorf("read IP Helper interface table: %w", err)
	}
	if table == nil {
		return nil, fmt.Errorf("IP Helper returned an empty interface table pointer")
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	if table.NumEntries > maximumInterfaceFacts {
		return nil, fmt.Errorf("interface count exceeds limit %d", maximumInterfaceFacts)
	}
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	facts := make([]InterfaceFact, 0, len(rows))
	for _, row := range rows {
		isNative := row.Type == windows.IF_TYPE_SOFTWARE_LOOPBACK
		fact := InterfaceFact{
			Name:           windows.UTF16ToString(row.Alias[:]),
			Index:          int(row.InterfaceIndex),
			NativeLoopback: isNative,
		}
		if isNative {
			fact.Kind = InterfaceKindWindowsSoftware
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// assignments reads exact IPv4 address facts from IP Helper's unicast table.
func (platformBackend) assignments(ctx context.Context, target netip.Addr) ([]AssignmentFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var table *windows.MibUnicastIpAddressTable
	if err := windows.GetUnicastIpAddressTable(windows.AF_INET, &table); err != nil {
		return nil, fmt.Errorf("read IP Helper address table: %w", err)
	}
	if table == nil {
		return nil, fmt.Errorf("IP Helper returned an empty address table pointer")
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	if table.NumEntries > 65536 {
		return nil, fmt.Errorf("unicast address table exceeds safety limit")
	}
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	facts := make([]AssignmentFact, 0, 1)
	for _, row := range rows {
		address, ok := windowsIPv4Address(&row.Address)
		if !ok || address != target {
			continue
		}
		facts = append(facts, AssignmentFact{
			Address:        address,
			PrefixLength:   int(row.OnLinkPrefixLength),
			InterfaceIndex: int(row.InterfaceIndex),
			Windows: &WindowsAssignmentFact{
				SkipAsSource:             row.SkipAsSource != 0,
				PrefixOrigin:             windowsPrefixOrigin(row.PrefixOrigin),
				SuffixOrigin:             windowsSuffixOrigin(row.SuffixOrigin),
				ValidLifetimeSeconds:     row.ValidLifetime,
				PreferredLifetimeSeconds: row.PreferredLifetime,
				DADState:                 windowsDADState(row.DadState),
			},
		})
		if len(facts) > maximumAssignmentFacts {
			return nil, fmt.Errorf("assignment count exceeds limit %d", maximumAssignmentFacts)
		}
	}
	return facts, nil
}

// ensure creates one manual, skip-as-source /32 with IP Helper.
func (platformBackend) ensure(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	row := windowsAddressRow(interf, prefix)
	status, _, _ := createUnicastAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	if status != 0 {
		return fmt.Errorf("CreateUnicastIpAddressEntry status %d", status)
	}
	return nil
}

// release deletes only the exact interface-and-address entry supplied to IP Helper.
func (platformBackend) release(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	row := windowsAddressRow(interf, prefix)
	status, _, _ := deleteUnicastAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	if status != 0 {
		return fmt.Errorf("DeleteUnicastIpAddressEntry status %d", status)
	}
	return nil
}

// windowsAddressRow initializes the exact IP Helper row required for a /32 effect.
func windowsAddressRow(interf InterfaceFact, prefix netip.Prefix) windows.MibUnicastIpAddressRow {
	var row windows.MibUnicastIpAddressRow
	initializeUnicastAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.Address))
	raw.Family = windows.AF_INET
	raw.Addr = prefix.Addr().As4()
	row.InterfaceIndex = uint32(interf.Index)
	row.OnLinkPrefixLength = uint8(prefix.Bits())
	row.PrefixOrigin = windows.IpPrefixOriginManual
	row.SuffixOrigin = windows.IpSuffixOriginManual
	row.ValidLifetime = ^uint32(0)
	row.PreferredLifetime = ^uint32(0)
	row.SkipAsSource = 1
	return row
}

// windowsIPv4Address decodes the IPv4 member of IP Helper's SOCKADDR_INET union.
func windowsIPv4Address(address *windows.RawSockaddrInet6) (netip.Addr, bool) {
	raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(address))
	if raw.Family != windows.AF_INET {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4(raw.Addr), true
}

// windowsPrefixOrigin converts IP Helper's prefix enum into a bounded public fact.
func windowsPrefixOrigin(origin uint32) AddressOrigin {
	switch origin {
	case windows.IpPrefixOriginOther:
		return AddressOriginOther
	case windows.IpPrefixOriginManual:
		return AddressOriginManual
	case windows.IpPrefixOriginWellKnown:
		return AddressOriginWellKnown
	case windows.IpPrefixOriginDhcp:
		return AddressOriginDHCP
	case windows.IpPrefixOriginRouterAdvertisement:
		return AddressOriginRouterAdvertisement
	case windows.IpPrefixOriginUnchanged:
		return AddressOriginUnchanged
	default:
		return AddressOriginUnknown
	}
}

// windowsSuffixOrigin converts IP Helper's suffix enum into a bounded public fact.
func windowsSuffixOrigin(origin uint32) AddressOrigin {
	switch origin {
	case windows.IpSuffixOriginOther:
		return AddressOriginOther
	case windows.IpSuffixOriginManual:
		return AddressOriginManual
	case windows.IpSuffixOriginWellKnown:
		return AddressOriginWellKnown
	case windows.IpSuffixOriginDhcp:
		return AddressOriginDHCP
	case windows.IpSuffixOriginLinkLayerAddress:
		return AddressOriginLinkLayer
	case windows.IpSuffixOriginRandom:
		return AddressOriginRandom
	case windows.IpSuffixOriginUnchanged:
		return AddressOriginUnchanged
	default:
		return AddressOriginUnknown
	}
}

// windowsDADState converts IP Helper's duplicate-address-detection enum into a bounded public fact.
func windowsDADState(state uint32) AddressState {
	switch state {
	case windows.IpDadStateInvalid:
		return AddressStateInvalid
	case windows.IpDadStateTentative:
		return AddressStateTentative
	case windows.IpDadStateDuplicate:
		return AddressStateDuplicate
	case windows.IpDadStateDeprecated:
		return AddressStateDeprecated
	case windows.IpDadStatePreferred:
		return AddressStatePreferred
	default:
		return AddressStateUnknown
	}
}
