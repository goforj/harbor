//go:build windows

package hostconflict

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsObservationRetries      = 3
	windowsInterfaceAccessLoopback = 1
	maximumWindowsInterfaceRows    = 65536
)

var (
	windowsIPHelperDLL                    = windows.NewLazySystemDLL("iphlpapi.dll")
	windowsGetCurrentCompartmentProcedure = windowsIPHelperDLL.NewProc("GetCurrentThreadCompartmentId")
	windowsGetInterfaceTableProcedure     = windowsIPHelperDLL.NewProc("GetIfTable2Ex")
	windowsGetRouteTableProcedure         = windowsIPHelperDLL.NewProc("GetIpForwardTable2")
	windowsGetBestRouteProcedure          = windowsIPHelperDLL.NewProc("GetBestRoute2")
	windowsGetTCPTableProcedure           = windowsIPHelperDLL.NewProc("GetExtendedTcpTable")
	windowsGetUDPTableProcedure           = windowsIPHelperDLL.NewProc("GetExtendedUdpTable")
	windowsFreeTableProcedure             = windowsIPHelperDLL.NewProc("FreeMibTable")
	windowsHostConflictProcedureOnce      sync.Once
	windowsHostConflictProcedureError     error
)

// windowsObservationPass supplies one complete IP Helper pass for stability tests.
type windowsObservationPass func(context.Context, Request) (Observation, error)

// windowsPassOperations isolates compartment and table orchestration from native codecs.
type windowsPassOperations struct {
	compartment func() (NetworkScope, error)
	interfaces  func(context.Context) (windowsInterfaceSnapshot, error)
	routes      func(context.Context, Request, windowsInterfaceSnapshot) (RouteSnapshot, error)
	sockets     func(context.Context, Request) (SocketSnapshot, error)
}

// windowsInterfaceSnapshot binds reusable indexes to stable LUIDs before routes are interpreted.
type windowsInterfaceSnapshot struct {
	loopback LoopbackIdentity
	byLUID   map[uint64]InterfaceIdentity
	byIndex  map[uint32]InterfaceIdentity
}

// nativeWindowsHostConflictAPI implements the bounded native IP Helper boundary.
type nativeWindowsHostConflictAPI struct {
	procedureError error
}

// ObserveWindows returns two consecutive matching, complete observations from the caller's active compartment.
func ObserveWindows(ctx context.Context, request Request) (Observation, error) {
	if err := request.Validate(); err != nil {
		return Observation{}, fmt.Errorf("observe Windows host conflicts: %w", err)
	}
	ctx = normalizeWindowsObservationContext(ctx)
	if err := ctx.Err(); err != nil {
		return Observation{}, fmt.Errorf("observe Windows host conflicts: %w", err)
	}

	// Windows networking compartments are thread-scoped. Every native query and
	// both stability passes must therefore remain on the same operating-system thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	api := nativeWindowsHostConflictAPI{procedureError: resolveWindowsHostConflictProcedures()}
	initialScope, err := api.compartment()
	if err != nil {
		return Observation{}, fmt.Errorf("observe Windows host conflicts: %w", err)
	}
	observation, err := observeStableWindows(ctx, request, func(ctx context.Context, request Request) (Observation, error) {
		return observeWindowsPassWith(ctx, request, windowsPassOperations{
			compartment: api.compartment,
			interfaces:  api.interfaces,
			routes:      api.routes,
			sockets:     api.sockets,
		})
	})
	if err != nil {
		return Observation{}, err
	}
	finalScope, err := api.compartment()
	if err != nil {
		return Observation{}, fmt.Errorf("observe Windows host conflicts: %w", err)
	}
	if !sameWindowsScope(initialScope, finalScope) || !sameWindowsScope(initialScope, observation.Scope) {
		return Observation{}, fmt.Errorf("observe Windows host conflicts: network compartment changed across stable observation")
	}
	return observation, nil
}

// observeStableWindows requires consecutive complete equality so an A-B-A race cannot authorize either state.
func observeStableWindows(ctx context.Context, request Request, observe windowsObservationPass) (Observation, error) {
	previousFingerprint := ""
	for attempt := 0; attempt <= windowsObservationRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return Observation{}, fmt.Errorf("observe Windows host conflicts: %w", err)
		}
		observation, err := observe(ctx, request)
		if err != nil {
			return Observation{}, fmt.Errorf("observe Windows host conflicts: %w", err)
		}
		if !observation.Routes.Complete || observation.Routes.Truncated || !observation.Sockets.Complete || observation.Sockets.Truncated {
			return Observation{}, fmt.Errorf("observe Windows host conflicts: native observation is incomplete")
		}
		fingerprint, err := observation.Fingerprint()
		if err != nil {
			return Observation{}, fmt.Errorf("observe Windows host conflicts: invalid native facts: %w", err)
		}
		if previousFingerprint != "" && fingerprint == previousFingerprint {
			return observation, nil
		}
		previousFingerprint = fingerprint
	}
	return Observation{}, fmt.Errorf("observe Windows host conflicts: IP Helper facts did not stabilize after %d passes", windowsObservationRetries+1)
}

// observeWindowsPassWith rejects a compartment transition around every bounded fact collection.
func observeWindowsPassWith(ctx context.Context, request Request, operations windowsPassOperations) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	scopeBefore, err := operations.compartment()
	if err != nil {
		return Observation{}, err
	}
	// IP Helper resolves these tables through the calling thread's current
	// compartment; the before-and-after identity bracket makes that scope explicit.
	interfaces, err := operations.interfaces(ctx)
	if err != nil {
		return Observation{}, err
	}
	routes, err := operations.routes(ctx, request, interfaces)
	if err != nil {
		return Observation{}, err
	}
	sockets := SocketSnapshot{Complete: true}
	if len(request.Requirements()) > 0 {
		sockets, err = operations.sockets(ctx, request)
		if err != nil {
			return Observation{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	scopeAfter, err := operations.compartment()
	if err != nil {
		return Observation{}, err
	}
	if !sameWindowsScope(scopeBefore, scopeAfter) {
		return Observation{}, fmt.Errorf("host conflict Windows network compartment changed during observation")
	}

	observation := Observation{
		Request:  request,
		Scope:    scopeBefore,
		Loopback: interfaces.loopback,
		Routes:   routes,
		Sockets:  sockets,
	}
	if err := observation.Validate(); err != nil {
		return Observation{}, fmt.Errorf("host conflict Windows native observation: %w", err)
	}
	return observation, nil
}

// compartment captures the calling thread's current IP Helper compartment identity.
func (api nativeWindowsHostConflictAPI) compartment() (NetworkScope, error) {
	if api.procedureError != nil {
		return NetworkScope{}, api.procedureError
	}
	identifier, _, _ := windowsGetCurrentCompartmentProcedure.Call()
	if identifier == 0 || identifier > uintptr(^uint32(0)) {
		return NetworkScope{}, fmt.Errorf("IP Helper returned an invalid current compartment identifier")
	}
	scope, err := NewWindowsScope(uint32(identifier))
	if err != nil {
		return NetworkScope{}, err
	}
	return scope, nil
}

// interfaces proves one active native software loopback and indexes every route-resolvable identity.
func (api nativeWindowsHostConflictAPI) interfaces(ctx context.Context) (windowsInterfaceSnapshot, error) {
	if err := api.procedureError; err != nil {
		return windowsInterfaceSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return windowsInterfaceSnapshot{}, err
	}
	var table *windows.MibIfTable2
	status, _, _ := windowsGetInterfaceTableProcedure.Call(
		uintptr(windows.MibIfTableNormalWithoutStatistics),
		uintptr(unsafe.Pointer(&table)),
	)
	if err := windowsHostConflictStatusError(status); err != nil {
		return windowsInterfaceSnapshot{}, fmt.Errorf("read IP Helper interface table: %w", err)
	}
	if err := ctx.Err(); err != nil {
		if table != nil {
			windowsFreeTableProcedure.Call(uintptr(unsafe.Pointer(table)))
		}
		return windowsInterfaceSnapshot{}, err
	}
	if table == nil {
		return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper returned an empty interface table pointer")
	}
	defer windowsFreeTableProcedure.Call(uintptr(unsafe.Pointer(table)))
	if table.NumEntries > maximumWindowsInterfaceRows {
		return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper interface table exceeds limit %d", maximumWindowsInterfaceRows)
	}
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	snapshot, err := normalizeWindowsInterfaces(ctx, rows)
	if err != nil {
		return windowsInterfaceSnapshot{}, err
	}
	return snapshot, nil
}

// normalizeWindowsInterfaces validates table identity before selecting the sole native loopback.
func normalizeWindowsInterfaces(ctx context.Context, rows []windows.MibIfRow2) (windowsInterfaceSnapshot, error) {
	if len(rows) > maximumWindowsInterfaceRows {
		return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper interface table exceeds limit %d", maximumWindowsInterfaceRows)
	}
	snapshot := windowsInterfaceSnapshot{
		byLUID:  make(map[uint64]InterfaceIdentity, len(rows)),
		byIndex: make(map[uint32]InterfaceIdentity, len(rows)),
	}
	loopbackCount := 0
	for index, row := range rows {
		if index%64 == 0 {
			if err := ctx.Err(); err != nil {
				return windowsInterfaceSnapshot{}, err
			}
		}
		if row.InterfaceLuid == 0 || row.InterfaceIndex == 0 {
			return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper interface row %d has incomplete native identity", index)
		}
		name, err := decodeWindowsInterfaceAlias(row.Alias[:])
		if err != nil {
			return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper interface row %d: %w", index, err)
		}
		identity := InterfaceIdentity{Name: name, Index: row.InterfaceIndex, WindowsLUID: row.InterfaceLuid}
		if err := identity.validateForPlatform(PlatformWindows); err != nil {
			return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper interface row %d: %w", index, err)
		}
		if _, exists := snapshot.byLUID[identity.WindowsLUID]; exists {
			return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper interface table repeats LUID %d", identity.WindowsLUID)
		}
		if _, exists := snapshot.byIndex[identity.Index]; exists {
			return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper interface table repeats index %d", identity.Index)
		}
		snapshot.byLUID[identity.WindowsLUID] = identity
		snapshot.byIndex[identity.Index] = identity
		if exactWindowsHostConflictLoopback(row) {
			loopbackCount++
			snapshot.loopback = LoopbackIdentity{Interface: identity, Kind: LoopbackKindWindowsSoftware}
		}
	}
	if loopbackCount != 1 {
		return windowsInterfaceSnapshot{}, fmt.Errorf("IP Helper reported %d active native software loopbacks, want exactly one", loopbackCount)
	}
	return snapshot, nil
}

// identity resolves a route's LUID and index only when both native identifiers still agree.
func (snapshot windowsInterfaceSnapshot) identity(luid uint64, index uint32) (InterfaceIdentity, error) {
	if luid == 0 || index == 0 {
		return InterfaceIdentity{}, fmt.Errorf("route interface identity is incomplete")
	}
	byLUID, foundLUID := snapshot.byLUID[luid]
	byIndex, foundIndex := snapshot.byIndex[index]
	if !foundLUID || !foundIndex || !sameInterfaceAuthority(PlatformWindows, byLUID, byIndex) {
		return InterfaceIdentity{}, fmt.Errorf("route interface LUID %d and index %d do not resolve to one interface", luid, index)
	}
	return byLUID, nil
}

// exactWindowsHostConflictLoopback requires IP Helper's type, access, and operational facts to agree.
func exactWindowsHostConflictLoopback(row windows.MibIfRow2) bool {
	return row.InterfaceLuid != 0 &&
		row.InterfaceIndex != 0 &&
		row.Type == windows.IF_TYPE_SOFTWARE_LOOPBACK &&
		row.AccessType == windowsInterfaceAccessLoopback &&
		row.OperStatus == windows.IfOperStatusUp
}

// decodeWindowsInterfaceAlias rejects unterminated or malformed UTF-16 display evidence.
func decodeWindowsInterfaceAlias(value []uint16) (string, error) {
	terminator := -1
	for index, codeUnit := range value {
		if codeUnit == 0 {
			terminator = index
			break
		}
	}
	if terminator < 0 {
		return "", fmt.Errorf("interface alias is not null terminated")
	}
	value = value[:terminator]
	for index := 0; index < len(value); index++ {
		codeUnit := value[index]
		switch {
		case codeUnit >= 0xd800 && codeUnit <= 0xdbff:
			if index+1 >= len(value) || value[index+1] < 0xdc00 || value[index+1] > 0xdfff {
				return "", fmt.Errorf("interface alias contains malformed UTF-16")
			}
			index++
		case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
			return "", fmt.Errorf("interface alias contains malformed UTF-16")
		}
	}
	return string(utf16.Decode(value)), nil
}

// sameWindowsScope compares validated active-compartment identities without pointer equality.
func sameWindowsScope(left NetworkScope, right NetworkScope) bool {
	return left.Platform == PlatformWindows &&
		right.Platform == PlatformWindows &&
		left.WindowsCompartment != nil &&
		right.WindowsCompartment != nil &&
		left.WindowsCompartment.ID != 0 &&
		left.WindowsCompartment.ID == right.WindowsCompartment.ID
}

// normalizeWindowsObservationContext makes nil contexts follow the same bounded cancellation path.
func normalizeWindowsObservationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// windowsHostConflictStatusError preserves the Win32 status returned directly by IP Helper procedures.
func windowsHostConflictStatusError(status uintptr) error {
	if status == 0 {
		return nil
	}
	return windows.Errno(status)
}

// resolveWindowsHostConflictProcedures resolves every raw entry point once so a missing symbol cannot resemble success.
func resolveWindowsHostConflictProcedures() error {
	windowsHostConflictProcedureOnce.Do(func() {
		windowsHostConflictProcedureError = errors.Join(
			findWindowsHostConflictProcedure("GetCurrentThreadCompartmentId", windowsGetCurrentCompartmentProcedure),
			findWindowsHostConflictProcedure("GetIfTable2Ex", windowsGetInterfaceTableProcedure),
			findWindowsHostConflictProcedure("GetIpForwardTable2", windowsGetRouteTableProcedure),
			findWindowsHostConflictProcedure("GetBestRoute2", windowsGetBestRouteProcedure),
			findWindowsHostConflictProcedure("GetExtendedTcpTable", windowsGetTCPTableProcedure),
			findWindowsHostConflictProcedure("GetExtendedUdpTable", windowsGetUDPTableProcedure),
			findWindowsHostConflictProcedure("FreeMibTable", windowsFreeTableProcedure),
		)
	})
	return windowsHostConflictProcedureError
}

// findWindowsHostConflictProcedure adds stable operation context to an exact DLL entry-point lookup.
func findWindowsHostConflictProcedure(name string, procedure *windows.LazyProc) error {
	if err := procedure.Find(); err != nil {
		return fmt.Errorf("resolve IP Helper procedure %s: %w", name, err)
	}
	return nil
}
