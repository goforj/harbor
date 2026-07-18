//go:build windows

package hostconflict

import (
	"context"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maximumWindowsRouteRows = 65536

// windowsRouteAuthority identifies one native route row without relying on its display name.
type windowsRouteAuthority struct {
	interfaceLUID  uint64
	interfaceIndex uint32
	destination    [4]byte
	prefixLength   uint8
	nextHop        [4]byte
}

// routes captures the selected route and the complete bounded IPv4 matching table from the current compartment.
func (api nativeWindowsHostConflictAPI) routes(ctx context.Context, request Request, interfaces windowsInterfaceSnapshot) (RouteSnapshot, error) {
	if err := api.procedureError; err != nil {
		return RouteSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return RouteSnapshot{}, err
	}
	selectedRow, err := api.bestRoute(ctx, request.Candidate())
	if err != nil {
		return RouteSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return RouteSnapshot{}, err
	}
	var table *windows.MibIpForwardTable2
	status, _, _ := windowsGetRouteTableProcedure.Call(
		uintptr(windows.AF_INET),
		uintptr(unsafe.Pointer(&table)),
	)
	if err := windowsHostConflictStatusError(status); err != nil {
		return RouteSnapshot{}, fmt.Errorf("read IP Helper IPv4 route table: %w", err)
	}
	if err := ctx.Err(); err != nil {
		if table != nil {
			windowsFreeTableProcedure.Call(uintptr(unsafe.Pointer(table)))
		}
		return RouteSnapshot{}, err
	}
	if table == nil {
		return RouteSnapshot{}, fmt.Errorf("IP Helper returned an empty route table pointer")
	}
	defer windowsFreeTableProcedure.Call(uintptr(unsafe.Pointer(table)))
	if table.NumEntries > maximumWindowsRouteRows {
		return RouteSnapshot{}, fmt.Errorf("IP Helper route table exceeds limit %d", maximumWindowsRouteRows)
	}

	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	return normalizeWindowsRouteSnapshot(ctx, request, interfaces, selectedRow, rows)
}

// normalizeWindowsRouteSnapshot joins selection to exactly one bounded native table row.
func normalizeWindowsRouteSnapshot(ctx context.Context, request Request, interfaces windowsInterfaceSnapshot, selectedRow windows.MibIpForwardRow2, rows []windows.MibIpForwardRow2) (RouteSnapshot, error) {
	if len(rows) > maximumWindowsRouteRows {
		return RouteSnapshot{}, fmt.Errorf("IP Helper route table exceeds limit %d", maximumWindowsRouteRows)
	}
	selectedFact, selectedAuthority, err := normalizeWindowsRoute(selectedRow, interfaces)
	if err != nil {
		return RouteSnapshot{}, fmt.Errorf("normalize IP Helper selected route: %w", err)
	}
	if !selectedFact.Destination.Contains(request.Candidate()) {
		return RouteSnapshot{}, fmt.Errorf("IP Helper selected route %s does not contain candidate %s", selectedFact.Destination, request.Candidate())
	}

	matching := make([]RouteFact, 0, 8)
	selectedMatches := 0
	for index, row := range rows {
		if index%64 == 0 {
			if err := ctx.Err(); err != nil {
				return RouteSnapshot{}, err
			}
		}
		destination, err := windowsRouteDestination(row)
		if err != nil {
			return RouteSnapshot{}, fmt.Errorf("read IP Helper route row %d destination: %w", index, err)
		}
		if !destination.Contains(request.Candidate()) {
			continue
		}
		fact, authority, err := normalizeWindowsRoute(row, interfaces)
		if err != nil {
			return RouteSnapshot{}, fmt.Errorf("normalize IP Helper route row %d: %w", index, err)
		}
		if len(matching) == maximumRouteFacts {
			return RouteSnapshot{}, fmt.Errorf("IP Helper matching routes exceed limit %d", maximumRouteFacts)
		}
		matching = append(matching, fact)
		if authority == selectedAuthority {
			selectedMatches++
			selectedFact = fact
		}
	}
	if err := ctx.Err(); err != nil {
		return RouteSnapshot{}, err
	}
	if selectedMatches != 1 {
		return RouteSnapshot{}, fmt.Errorf("IP Helper selected route matched %d table rows by native authority, want exactly one", selectedMatches)
	}
	return RouteSnapshot{Complete: true, Selected: &selectedFact, Matching: matching}, nil
}

// bestRoute asks IP Helper for the effective IPv4 route in the calling thread's current compartment.
func (api nativeWindowsHostConflictAPI) bestRoute(ctx context.Context, candidate netip.Addr) (windows.MibIpForwardRow2, error) {
	if err := api.procedureError; err != nil {
		return windows.MibIpForwardRow2{}, err
	}
	destination := windows.RawSockaddrInet{}
	destination4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(&destination))
	destination4.Family = windows.AF_INET
	destination4.Addr = candidate.As4()
	var row windows.MibIpForwardRow2
	var source windows.RawSockaddrInet
	status, _, _ := windowsGetBestRouteProcedure.Call(
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&destination)),
		0,
		uintptr(unsafe.Pointer(&row)),
		uintptr(unsafe.Pointer(&source)),
	)
	if err := windowsHostConflictStatusError(status); err != nil {
		return windows.MibIpForwardRow2{}, fmt.Errorf("select IP Helper route for %s: %w", candidate, err)
	}
	if err := ctx.Err(); err != nil {
		return windows.MibIpForwardRow2{}, err
	}
	if _, err := windowsIPv4Sockaddr(source, "selected source"); err != nil {
		return windows.MibIpForwardRow2{}, fmt.Errorf("select IP Helper route for %s: %w", candidate, err)
	}
	return row, nil
}

// normalizeWindowsRoute converts every authority-bearing IPv4 field and rejects malformed native rows.
func normalizeWindowsRoute(row windows.MibIpForwardRow2, interfaces windowsInterfaceSnapshot) (RouteFact, windowsRouteAuthority, error) {
	destination, err := windowsRouteDestination(row)
	if err != nil {
		return RouteFact{}, windowsRouteAuthority{}, err
	}
	nextHop, err := windowsIPv4Sockaddr(row.NextHop, "next hop")
	if err != nil {
		return RouteFact{}, windowsRouteAuthority{}, err
	}
	identity, err := interfaces.identity(row.InterfaceLuid, row.InterfaceIndex)
	if err != nil {
		return RouteFact{}, windowsRouteAuthority{}, err
	}
	if row.Loopback > 1 || row.AutoconfigureAddress > 1 || row.Publish > 1 || row.Immortal > 1 {
		return RouteFact{}, windowsRouteAuthority{}, fmt.Errorf("route contains a non-boolean native flag")
	}
	nativeLoopback := sameInterfaceAuthority(PlatformWindows, identity, interfaces.loopback.Interface)
	if (row.Loopback != 0) != nativeLoopback {
		return RouteFact{}, windowsRouteAuthority{}, fmt.Errorf("route loopback flag disagrees with native interface identity")
	}
	gateway := nextHop
	if nextHop.IsUnspecified() {
		gateway = netip.Addr{}
	}
	fact := RouteFact{
		Destination:    destination,
		Interface:      identity,
		NativeLoopback: nativeLoopback,
		Gateway:        gateway,
		Normalization:  RouteNormalizationDirect,
	}
	authority := windowsRouteAuthority{
		interfaceLUID:  row.InterfaceLuid,
		interfaceIndex: row.InterfaceIndex,
		destination:    destination.Addr().As4(),
		prefixLength:   row.DestinationPrefix.PrefixLength,
		nextHop:        nextHop.As4(),
	}
	return fact, authority, nil
}

// windowsRouteDestination validates the family and canonical prefix before relevance filtering.
func windowsRouteDestination(row windows.MibIpForwardRow2) (netip.Prefix, error) {
	if row.DestinationPrefix.PrefixLength > 32 {
		return netip.Prefix{}, fmt.Errorf("destination prefix length %d exceeds IPv4 width", row.DestinationPrefix.PrefixLength)
	}
	destinationAddress, err := windowsIPv4Sockaddr(row.DestinationPrefix.Prefix, "destination")
	if err != nil {
		return netip.Prefix{}, err
	}
	destination := netip.PrefixFrom(destinationAddress, int(row.DestinationPrefix.PrefixLength))
	if destination != destination.Masked() {
		return netip.Prefix{}, fmt.Errorf("destination %s is not canonical", destination)
	}
	return destination, nil
}

// windowsIPv4Sockaddr decodes the SOCKADDR_INET union without accepting another family or a port.
func windowsIPv4Sockaddr(raw windows.RawSockaddrInet, label string) (netip.Addr, error) {
	if raw.Family != windows.AF_INET {
		return netip.Addr{}, fmt.Errorf("%s address family %d is not IPv4", label, raw.Family)
	}
	value := (*windows.RawSockaddrInet4)(unsafe.Pointer(&raw))
	if value.Port != 0 {
		return netip.Addr{}, fmt.Errorf("%s contains an unexpected port", label)
	}
	return netip.AddrFrom4(value.Addr), nil
}
