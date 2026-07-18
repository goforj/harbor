//go:build windows

package hostconflict

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestResolveWindowsHostConflictProceduresRequiresEveryNativeSymbol catches unsupported IP Helper surfaces early.
func TestResolveWindowsHostConflictProceduresRequiresEveryNativeSymbol(t *testing.T) {
	if err := resolveWindowsHostConflictProcedures(); err != nil {
		t.Fatalf("resolveWindowsHostConflictProcedures() error = %v", err)
	}
	missing := windows.NewLazySystemDLL("iphlpapi.dll").NewProc("HarborDefinitelyMissingProcedure")
	if err := findWindowsHostConflictProcedure("HarborDefinitelyMissingProcedure", missing); err == nil {
		t.Fatal("findWindowsHostConflictProcedure() error = nil")
	}
}

// TestObserveStableWindowsRequiresConsecutiveCompletePasses proves the pair and retry bounds.
func TestObserveStableWindowsRequiresConsecutiveCompletePasses(t *testing.T) {
	reference := safeWindowsObservation(t)
	changed := safeWindowsObservation(t)
	changed.Scope.WindowsCompartment.ID++
	tests := []struct {
		name      string
		sequence  []Observation
		wantCalls int
		wantError string
	}{
		{name: "first pair", sequence: []Observation{reference, reference}, wantCalls: 2},
		{name: "later pair", sequence: []Observation{reference, changed, changed}, wantCalls: 3},
		{name: "alternating", sequence: []Observation{reference, changed, reference, changed}, wantCalls: 4, wantError: "did not stabilize"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			observation, err := observeStableWindows(context.Background(), reference.Request, func(context.Context, Request) (Observation, error) {
				result := cloneObservation(test.sequence[calls])
				calls++
				return result, nil
			})
			if test.wantError == "" && err != nil {
				t.Fatalf("observeStableWindows() error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("observeStableWindows() error = %v, want containing %q", err, test.wantError)
			}
			if test.wantError == "" && observation.Scope.Platform != PlatformWindows {
				t.Fatalf("observeStableWindows() = %#v", observation)
			}
			if calls != test.wantCalls {
				t.Fatalf("observeStableWindows() calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

// TestObserveStableWindowsRejectsCancellationErrorsAndIncompleteFacts covers every pre-authority failure.
func TestObserveStableWindowsRejectsCancellationErrorsAndIncompleteFacts(t *testing.T) {
	reference := safeWindowsObservation(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observeStableWindows(canceled, reference.Request, func(context.Context, Request) (Observation, error) {
		t.Fatal("canceled observation invoked pass")
		return Observation{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("observeStableWindows(canceled) error = %v", err)
	}
	sentinel := errors.New("fixture failure")
	if _, err := observeStableWindows(context.Background(), reference.Request, func(context.Context, Request) (Observation, error) {
		return Observation{}, sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("observeStableWindows(failure) error = %v", err)
	}
	incomplete := cloneObservation(reference)
	incomplete.Sockets.Complete = false
	if _, err := observeStableWindows(context.Background(), reference.Request, func(context.Context, Request) (Observation, error) {
		return incomplete, nil
	}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("observeStableWindows(incomplete) error = %v", err)
	}
	invalid := cloneObservation(reference)
	invalid.Loopback.Interface.WindowsLUID = 0
	if _, err := observeStableWindows(context.Background(), reference.Request, func(context.Context, Request) (Observation, error) {
		return invalid, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid native facts") {
		t.Fatalf("observeStableWindows(invalid) error = %v", err)
	}
}

// TestObserveWindowsPassWithBindsCompartmentAndSkipsUnrequestedSockets covers native pass orchestration.
func TestObserveWindowsPassWithBindsCompartmentAndSkipsUnrequestedSockets(t *testing.T) {
	reference := safeWindowsObservation(t)
	interfaces := windowsTestInterfaceSnapshot()
	compartmentCalls := 0
	socketCalls := 0
	operations := windowsPassOperations{
		compartment: func() (NetworkScope, error) {
			compartmentCalls++
			return reference.Scope, nil
		},
		interfaces: func(context.Context) (windowsInterfaceSnapshot, error) { return interfaces, nil },
		routes: func(context.Context, Request, windowsInterfaceSnapshot) (RouteSnapshot, error) {
			return reference.Routes, nil
		},
		sockets: func(context.Context, Request) (SocketSnapshot, error) {
			socketCalls++
			return reference.Sockets, nil
		},
	}
	observation, err := observeWindowsPassWith(context.Background(), reference.Request, operations)
	if err != nil {
		t.Fatalf("observeWindowsPassWith() error = %v", err)
	}
	if compartmentCalls != 2 || socketCalls != 1 || observation.Scope.Platform != PlatformWindows {
		t.Fatalf("observeWindowsPassWith() calls = compartment %d, sockets %d; observation %#v", compartmentCalls, socketCalls, observation)
	}

	routeOnly, err := NewPreAssignmentRequest(reference.Request.Candidate(), nil)
	if err != nil {
		t.Fatalf("NewPreAssignmentRequest() error = %v", err)
	}
	socketCalls = 0
	observation, err = observeWindowsPassWith(context.Background(), routeOnly, operations)
	if err != nil {
		t.Fatalf("observeWindowsPassWith(route only) error = %v", err)
	}
	if socketCalls != 0 || !observation.Sockets.Complete {
		t.Fatalf("observeWindowsPassWith(route only) socket calls = %d, snapshot %#v", socketCalls, observation.Sockets)
	}
}

// TestObserveWindowsPassWithRejectsCompartmentChangesAndInvalidFacts prevents cross-scope joins.
func TestObserveWindowsPassWithRejectsCompartmentChangesAndInvalidFacts(t *testing.T) {
	reference := safeWindowsObservation(t)
	interfaces := windowsTestInterfaceSnapshot()
	calls := 0
	changedScope, err := NewWindowsScope(reference.Scope.WindowsCompartment.ID + 1)
	if err != nil {
		t.Fatalf("NewWindowsScope() error = %v", err)
	}
	operations := windowsPassOperations{
		compartment: func() (NetworkScope, error) {
			calls++
			if calls == 2 {
				return changedScope, nil
			}
			return reference.Scope, nil
		},
		interfaces: func(context.Context) (windowsInterfaceSnapshot, error) { return interfaces, nil },
		routes: func(context.Context, Request, windowsInterfaceSnapshot) (RouteSnapshot, error) {
			return reference.Routes, nil
		},
		sockets: func(context.Context, Request) (SocketSnapshot, error) { return reference.Sockets, nil },
	}
	if _, err := observeWindowsPassWith(context.Background(), reference.Request, operations); err == nil || !strings.Contains(err.Error(), "compartment changed") {
		t.Fatalf("observeWindowsPassWith(compartment change) error = %v", err)
	}

	operations.compartment = func() (NetworkScope, error) { return reference.Scope, nil }
	operations.routes = func(context.Context, Request, windowsInterfaceSnapshot) (RouteSnapshot, error) {
		return RouteSnapshot{Complete: true}, nil
	}
	if _, err := observeWindowsPassWith(context.Background(), reference.Request, operations); err == nil || !strings.Contains(err.Error(), "native observation") {
		t.Fatalf("observeWindowsPassWith(invalid facts) error = %v", err)
	}
}

// TestNormalizeWindowsInterfacesRequiresOneStableNativeLoopback covers table identity and candidate rules.
func TestNormalizeWindowsInterfacesRequiresOneStableNativeLoopback(t *testing.T) {
	loopback := windowsTestInterfaceRow("Loopback Pseudo-Interface 1", 48, 12, true)
	ethernet := windowsTestInterfaceRow("Ethernet", 96, 4, false)
	snapshot, err := normalizeWindowsInterfaces(context.Background(), []windows.MibIfRow2{loopback, ethernet})
	if err != nil {
		t.Fatalf("normalizeWindowsInterfaces() error = %v", err)
	}
	if snapshot.loopback.Interface.WindowsLUID != 48 || snapshot.loopback.Interface.Index != 12 {
		t.Fatalf("normalizeWindowsInterfaces() loopback = %#v", snapshot.loopback)
	}

	tests := []struct {
		name string
		rows []windows.MibIfRow2
	}{
		{name: "none", rows: []windows.MibIfRow2{ethernet}},
		{name: "multiple", rows: []windows.MibIfRow2{loopback, windowsTestInterfaceRow("Other Loopback", 49, 13, true)}},
		{name: "zero LUID", rows: []windows.MibIfRow2{func() windows.MibIfRow2 { row := loopback; row.InterfaceLuid = 0; return row }()}},
		{name: "duplicate LUID", rows: []windows.MibIfRow2{loopback, windowsTestInterfaceRow("Ethernet", 48, 4, false)}},
		{name: "duplicate index", rows: []windows.MibIfRow2{loopback, windowsTestInterfaceRow("Ethernet", 96, 12, false)}},
		{name: "down", rows: []windows.MibIfRow2{func() windows.MibIfRow2 { row := loopback; row.OperStatus = windows.IfOperStatusDown; return row }()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if snapshot, err := normalizeWindowsInterfaces(context.Background(), test.rows); err == nil {
				t.Fatalf("normalizeWindowsInterfaces() = %#v, want error", snapshot)
			}
		})
	}
}

// TestWindowsInterfaceIdentityUsesLUIDAndIndexInsteadOfAlias proves display changes cannot redirect a route.
func TestWindowsInterfaceIdentityUsesLUIDAndIndexInsteadOfAlias(t *testing.T) {
	snapshot := windowsTestInterfaceSnapshot()
	renamed := snapshot.byIndex[12]
	renamed.Name = "Renamed for display"
	snapshot.byIndex[12] = renamed
	identity, err := snapshot.identity(48, 12)
	if err != nil {
		t.Fatalf("identity() error = %v", err)
	}
	if identity.Name != "Loopback Pseudo-Interface 1" {
		t.Fatalf("identity() = %#v", identity)
	}
	if _, err := snapshot.identity(48, 4); err == nil {
		t.Fatal("identity() accepted mismatched LUID and index")
	}
}

// TestDecodeWindowsInterfaceAliasRejectsMalformedUTF16 protects display evidence boundaries.
func TestDecodeWindowsInterfaceAliasRejectsMalformedUTF16(t *testing.T) {
	value := windows.StringToUTF16("Loopback ⚓")
	decoded, err := decodeWindowsInterfaceAlias(value)
	if err != nil || decoded != "Loopback ⚓" {
		t.Fatalf("decodeWindowsInterfaceAlias() = %q, %v", decoded, err)
	}
	if _, err := decodeWindowsInterfaceAlias([]uint16{'x'}); err == nil {
		t.Fatal("decodeWindowsInterfaceAlias() accepted unterminated input")
	}
	if _, err := decodeWindowsInterfaceAlias([]uint16{0xd800, 0}); err == nil {
		t.Fatal("decodeWindowsInterfaceAlias() accepted malformed surrogate")
	}
}

// TestNormalizeWindowsRouteSnapshotJoinsOneNativeAuthorityRow proves bounded selection membership.
func TestNormalizeWindowsRouteSnapshotJoinsOneNativeAuthorityRow(t *testing.T) {
	reference := safeWindowsObservation(t)
	interfaces := windowsTestInterfaceSnapshot()
	baseline := windowsTestRouteRow(interfaces.loopback.Interface, netip.MustParsePrefix("127.0.0.0/8"), netip.IPv4Unspecified(), true)
	ethernet := interfaces.byIndex[4]
	defaultRoute := windowsTestRouteRow(ethernet, netip.MustParsePrefix("0.0.0.0/0"), netip.MustParseAddr("192.0.2.1"), false)
	snapshot, err := normalizeWindowsRouteSnapshot(context.Background(), reference.Request, interfaces, baseline, []windows.MibIpForwardRow2{defaultRoute, baseline})
	if err != nil {
		t.Fatalf("normalizeWindowsRouteSnapshot() error = %v", err)
	}
	if snapshot.Selected == nil || snapshot.Selected.Interface.WindowsLUID != 48 || len(snapshot.Matching) != 2 {
		t.Fatalf("normalizeWindowsRouteSnapshot() = %#v", snapshot)
	}
	if _, err := normalizeWindowsRouteSnapshot(context.Background(), reference.Request, interfaces, baseline, []windows.MibIpForwardRow2{defaultRoute}); err == nil || !strings.Contains(err.Error(), "matched 0") {
		t.Fatalf("normalizeWindowsRouteSnapshot(missing) error = %v", err)
	}
	if _, err := normalizeWindowsRouteSnapshot(context.Background(), reference.Request, interfaces, baseline, []windows.MibIpForwardRow2{baseline, baseline}); err == nil || !strings.Contains(err.Error(), "matched 2") {
		t.Fatalf("normalizeWindowsRouteSnapshot(duplicate) error = %v", err)
	}
	rows := make([]windows.MibIpForwardRow2, 0, maximumRouteFacts+2)
	rows = append(rows, baseline)
	for index := 0; index <= maximumRouteFacts; index++ {
		prefix := netip.PrefixFrom(reference.Request.Candidate(), 32)
		row := windowsTestRouteRow(interfaces.loopback.Interface, prefix, netip.IPv4Unspecified(), true)
		row.Metric = uint32(index + 1)
		rows = append(rows, row)
	}
	if _, err := normalizeWindowsRouteSnapshot(context.Background(), reference.Request, interfaces, baseline, rows); err == nil || !strings.Contains(err.Error(), "exceed limit") {
		t.Fatalf("normalizeWindowsRouteSnapshot(oversized matching) error = %v", err)
	}
}

// TestNormalizeWindowsRouteRejectsMalformedNativeFacts exercises family, prefix, flags, and identity.
func TestNormalizeWindowsRouteRejectsMalformedNativeFacts(t *testing.T) {
	interfaces := windowsTestInterfaceSnapshot()
	reference := windowsTestRouteRow(interfaces.loopback.Interface, netip.MustParsePrefix("127.0.0.0/8"), netip.IPv4Unspecified(), true)
	tests := []struct {
		name   string
		mutate func(*windows.MibIpForwardRow2)
	}{
		{name: "prefix width", mutate: func(row *windows.MibIpForwardRow2) { row.DestinationPrefix.PrefixLength = 33 }},
		{name: "unmasked", mutate: func(row *windows.MibIpForwardRow2) {
			windowsSetTestSockaddr4(&row.DestinationPrefix.Prefix, netip.MustParseAddr("127.0.0.1"))
		}},
		{name: "destination family", mutate: func(row *windows.MibIpForwardRow2) { row.DestinationPrefix.Prefix.Family = windows.AF_INET6 }},
		{name: "next hop family", mutate: func(row *windows.MibIpForwardRow2) { row.NextHop.Family = windows.AF_INET6 }},
		{name: "interface mismatch", mutate: func(row *windows.MibIpForwardRow2) { row.InterfaceIndex = 4 }},
		{name: "loopback disagreement", mutate: func(row *windows.MibIpForwardRow2) { row.Loopback = 0 }},
		{name: "invalid boolean", mutate: func(row *windows.MibIpForwardRow2) { row.Publish = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := reference
			test.mutate(&row)
			if fact, authority, err := normalizeWindowsRoute(row, interfaces); err == nil {
				t.Fatalf("normalizeWindowsRoute() = %#v, %#v, want error", fact, authority)
			}
		})
	}
}

// windowsTestInterfaceSnapshot returns one native loopback and one ordinary interface.
func windowsTestInterfaceSnapshot() windowsInterfaceSnapshot {
	loopback := InterfaceIdentity{Name: "Loopback Pseudo-Interface 1", Index: 12, WindowsLUID: 48}
	ethernet := InterfaceIdentity{Name: "Ethernet", Index: 4, WindowsLUID: 96}
	return windowsInterfaceSnapshot{
		loopback: LoopbackIdentity{Interface: loopback, Kind: LoopbackKindWindowsSoftware},
		byLUID:   map[uint64]InterfaceIdentity{48: loopback, 96: ethernet},
		byIndex:  map[uint32]InterfaceIdentity{12: loopback, 4: ethernet},
	}
}

// windowsTestInterfaceRow creates one bounded MIB interface fixture.
func windowsTestInterfaceRow(name string, luid uint64, index uint32, nativeLoopback bool) windows.MibIfRow2 {
	row := windows.MibIfRow2{InterfaceLuid: luid, InterfaceIndex: index, OperStatus: windows.IfOperStatusUp}
	copy(row.Alias[:], windows.StringToUTF16(name))
	if nativeLoopback {
		row.Type = windows.IF_TYPE_SOFTWARE_LOOPBACK
		row.AccessType = windowsInterfaceAccessLoopback
	}
	return row
}

// windowsTestRouteRow creates one canonical IPv4 route fixture.
func windowsTestRouteRow(identity InterfaceIdentity, destination netip.Prefix, nextHop netip.Addr, loopback bool) windows.MibIpForwardRow2 {
	row := windows.MibIpForwardRow2{
		InterfaceLuid:     identity.WindowsLUID,
		InterfaceIndex:    identity.Index,
		DestinationPrefix: windows.IpAddressPrefix{PrefixLength: uint8(destination.Bits())},
	}
	windowsSetTestSockaddr4(&row.DestinationPrefix.Prefix, destination.Addr())
	windowsSetTestSockaddr4(&row.NextHop, nextHop)
	if loopback {
		row.Loopback = 1
	}
	return row
}

// windowsSetTestSockaddr4 writes one IPv4 address into a SOCKADDR_INET fixture.
func windowsSetTestSockaddr4(destination *windows.RawSockaddrInet, address netip.Addr) {
	value := (*windows.RawSockaddrInet4)(unsafe.Pointer(destination))
	value.Family = windows.AF_INET
	value.Addr = address.As4()
}
