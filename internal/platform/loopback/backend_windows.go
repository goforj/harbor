//go:build windows

package loopback

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// windowsInterfaceAccessLoopback mirrors NET_IF_ACCESS_LOOPBACK, which x/sys does not currently export.
	windowsInterfaceAccessLoopback = 1
	windowsReconciliationAttempts  = 201
	windowsReconciliationInterval  = 50 * time.Millisecond
)

var (
	ipHelperDLL                   = windows.NewLazySystemDLL("iphlpapi.dll")
	initializeUnicastAddressEntry = ipHelperDLL.NewProc("InitializeUnicastIpAddressEntry")
	createUnicastAddressEntry     = ipHelperDLL.NewProc("CreateUnicastIpAddressEntry")
	deleteUnicastAddressEntry     = ipHelperDLL.NewProc("DeleteUnicastIpAddressEntry")
	windowsProcedureResolution    sync.Once
	windowsProcedureResolutionErr error
)

// windowsIPHelper confines the Windows backend to the exact IP Helper calls required by loopback ownership.
type windowsIPHelper interface {
	interfaceRows() ([]windows.MibIfRow2, error)
	addressRows() ([]windows.MibUnicastIpAddressRow, error)
	interfaceByLUID(uint64) (windows.MibIfRow2, error)
	interfaceByIndex(uint32) (windows.MibIfRow2, error)
	initializeAddress(*windows.MibUnicastIpAddressRow) error
	address(windows.MibUnicastIpAddressRow) (windows.MibUnicastIpAddressRow, error)
	createAddress(*windows.MibUnicastIpAddressRow) error
	deleteAddress(*windows.MibUnicastIpAddressRow) error
}

// nativeWindowsIPHelper implements the narrow IP Helper boundary without network clients or child processes.
type nativeWindowsIPHelper struct {
	procedureErr error
}

// platformBackend implements exact Windows loopback effects through IP Helper.
type platformBackend struct {
	ipHelper               windowsIPHelper
	reconciliationAttempts int
	reconciliationInterval time.Duration
}

// newPlatformBackend creates the Windows adapter without acquiring privilege.
func newPlatformBackend() backend {
	return &platformBackend{
		ipHelper:               nativeWindowsIPHelper{procedureErr: resolveWindowsProcedures()},
		reconciliationAttempts: windowsReconciliationAttempts,
		reconciliationInterval: windowsReconciliationInterval,
	}
}

// interfaces verifies loopback identity from IP Helper's stable LUID and IF_TYPE_SOFTWARE_LOOPBACK facts.
func (backend *platformBackend) interfaces(ctx context.Context) ([]InterfaceFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := backend.ipHelper.interfaceRows()
	if err != nil {
		return nil, fmt.Errorf("read IP Helper interface table: %w", err)
	}
	if len(rows) > maximumInterfaceFacts {
		return nil, fmt.Errorf("interface count exceeds limit %d", maximumInterfaceFacts)
	}
	facts := make([]InterfaceFact, 0, len(rows))
	for _, row := range rows {
		isNative := exactWindowsLoopbackCandidate(row)
		fact := InterfaceFact{
			Name:           windows.UTF16ToString(row.Alias[:]),
			Index:          int(row.InterfaceIndex),
			NativeLoopback: isNative,
		}
		if isNative {
			fact.Kind = InterfaceKindWindowsSoftware
			fact.WindowsLUID = row.InterfaceLuid
		}
		facts = append(facts, fact)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}

// assignments reads exact IPv4 address facts from IP Helper's unicast table.
func (backend *platformBackend) assignments(ctx context.Context, target netip.Addr) ([]AssignmentFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := backend.ipHelper.addressRows()
	if err != nil {
		return nil, fmt.Errorf("read IP Helper address table: %w", err)
	}
	facts := make([]AssignmentFact, 0, 1)
	for _, row := range rows {
		address, ok := windowsIPv4Address(&row.Address)
		if !ok || address != target {
			continue
		}
		facts = append(facts, windowsAssignmentFact(row, address))
		if len(facts) > maximumAssignmentFacts {
			return nil, fmt.Errorf("assignment count exceeds limit %d", maximumAssignmentFacts)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}

// ensure creates one manual, skip-as-source /32 and returns only after Windows reports it Preferred.
func (backend *platformBackend) ensure(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := backend.revalidateInterface(ctx, interf); err != nil {
		return fmt.Errorf("revalidate Windows loopback interface: %w", err)
	}
	row, err := backend.windowsAddressRow(interf, prefix)
	if err != nil {
		return err
	}
	if err := backend.ipHelper.createAddress(&row); err != nil {
		return fmt.Errorf("create IP Helper unicast address: %w", err)
	}

	if err := backend.reconcileAddress(context.Background(), row, true); err != nil {
		rollbackErr := backend.rollbackCreatedAddress(row)
		return errors.Join(fmt.Errorf("verify created IP Helper unicast address: %w", err), rollbackErr)
	}
	return nil
}

// release deletes only the observed LUID, index, and address tuple and verifies disappearance boundedly.
func (backend *platformBackend) release(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := backend.revalidateInterface(ctx, interf); err != nil {
		return fmt.Errorf("revalidate Windows loopback interface: %w", err)
	}
	row, err := backend.windowsAddressRow(interf, prefix)
	if err != nil {
		return err
	}
	if err := backend.ipHelper.deleteAddress(&row); err != nil {
		return fmt.Errorf("delete IP Helper unicast address: %w", err)
	}
	if err := backend.reconcileAddress(context.Background(), row, false); err != nil {
		return fmt.Errorf("verify deleted IP Helper unicast address: %w", err)
	}
	return nil
}

// revalidateInterface prevents an observed Windows index from authorizing a later interface after index reuse.
func (backend *platformBackend) revalidateInterface(ctx context.Context, interf InterfaceFact) error {
	if interf.Kind != InterfaceKindWindowsSoftware || !interf.NativeLoopback || interf.WindowsLUID == 0 || interf.Index <= 0 {
		return fmt.Errorf("observed Windows loopback identity is incomplete")
	}
	byLUID, err := backend.ipHelper.interfaceByLUID(interf.WindowsLUID)
	if err != nil {
		return fmt.Errorf("resolve interface LUID: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	byIndex, err := backend.ipHelper.interfaceByIndex(uint32(interf.Index))
	if err != nil {
		return fmt.Errorf("resolve interface index: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !exactWindowsInterfaceRow(byLUID, interf) || !exactWindowsInterfaceRow(byIndex, interf) {
		return fmt.Errorf("observed Windows loopback identity changed")
	}
	return nil
}

// exactWindowsInterfaceRow binds both IP Helper identifiers and native operational facts to the observed loopback.
func exactWindowsInterfaceRow(row windows.MibIfRow2, interf InterfaceFact) bool {
	return row.InterfaceLuid != 0 &&
		row.InterfaceLuid == interf.WindowsLUID &&
		row.InterfaceIndex == uint32(interf.Index) &&
		row.Type == windows.IF_TYPE_SOFTWARE_LOOPBACK &&
		row.AccessType == windowsInterfaceAccessLoopback &&
		row.OperStatus == windows.IfOperStatusUp
}

// exactWindowsLoopbackCandidate requires IP Helper's type, access, and operational facts to agree.
func exactWindowsLoopbackCandidate(row windows.MibIfRow2) bool {
	return row.InterfaceLuid != 0 &&
		row.InterfaceIndex != 0 &&
		row.Type == windows.IF_TYPE_SOFTWARE_LOOPBACK &&
		row.AccessType == windowsInterfaceAccessLoopback &&
		row.OperStatus == windows.IfOperStatusUp
}

// windowsAddressRow initializes and binds the exact IP Helper row required for one /32 effect.
func (backend *platformBackend) windowsAddressRow(interf InterfaceFact, prefix netip.Prefix) (windows.MibUnicastIpAddressRow, error) {
	var row windows.MibUnicastIpAddressRow
	if err := backend.ipHelper.initializeAddress(&row); err != nil {
		return windows.MibUnicastIpAddressRow{}, fmt.Errorf("initialize IP Helper unicast address: %w", err)
	}
	configureWindowsAddressRow(&row, interf, prefix)
	return row, nil
}

// configureWindowsAddressRow overwrites every field that defines Harbor's exact active assignment.
func configureWindowsAddressRow(row *windows.MibUnicastIpAddressRow, interf InterfaceFact, prefix netip.Prefix) {
	raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.Address))
	raw.Family = windows.AF_INET
	raw.Addr = prefix.Addr().As4()
	row.InterfaceLuid = interf.WindowsLUID
	row.InterfaceIndex = uint32(interf.Index)
	row.OnLinkPrefixLength = uint8(prefix.Bits())
	row.PrefixOrigin = windows.IpPrefixOriginManual
	row.SuffixOrigin = windows.IpSuffixOriginManual
	row.ValidLifetime = ^uint32(0)
	row.PreferredLifetime = ^uint32(0)
	row.SkipAsSource = 1
	row.DadState = windows.IpDadStateTentative
}

// reconcileAddress bounds Windows' asynchronous DAD and deletion visibility before reporting a completed effect.
func (backend *platformBackend) reconcileAddress(ctx context.Context, identity windows.MibUnicastIpAddressRow, wantPresent bool) error {
	for attempt := 0; attempt < backend.reconciliationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		observed, err := backend.ipHelper.address(identity)
		if wantPresent {
			switch {
			case err == nil && exactWindowsAddressRow(observed, identity):
				return nil
			case err == nil && pendingWindowsAddressRow(observed, identity):
			case errors.Is(err, windows.ERROR_NOT_FOUND):
			case err != nil:
				return fmt.Errorf("read created address: %w", err)
			default:
				return fmt.Errorf("created address has conflicting attributes or DAD state")
			}
		} else {
			switch {
			case errors.Is(err, windows.ERROR_NOT_FOUND):
				return nil
			case err != nil:
				return fmt.Errorf("read deleted address: %w", err)
			case !sameWindowsAddressIdentity(observed, identity):
				return fmt.Errorf("deleted address lookup returned a different identity")
			}
		}
		if attempt+1 < backend.reconciliationAttempts {
			if err := waitForWindowsReconciliation(ctx, backend.reconciliationInterval); err != nil {
				return err
			}
		}
	}
	if wantPresent {
		return fmt.Errorf("created address did not become preferred within the reconciliation bound")
	}
	return fmt.Errorf("deleted address remained present beyond the reconciliation bound")
}

// rollbackCreatedAddress removes only an address whose Create call succeeded in this operation.
func (backend *platformBackend) rollbackCreatedAddress(row windows.MibUnicastIpAddressRow) error {
	observed, err := backend.ipHelper.address(row)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect created IP Helper unicast address for rollback: %w", err)
	}
	if !ownedWindowsAddressRow(observed, row) {
		return fmt.Errorf("created IP Helper unicast address changed before rollback")
	}
	if err := backend.ipHelper.deleteAddress(&row); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
		return fmt.Errorf("rollback created IP Helper unicast address: %w", err)
	}
	if err := backend.reconcileAddress(context.Background(), row, false); err != nil {
		return fmt.Errorf("verify created-address rollback: %w", err)
	}
	return nil
}

// waitForWindowsReconciliation keeps each asynchronous IP Helper transition cancellable between observations.
func waitForWindowsReconciliation(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// windowsAssignmentFact converts one matching IP Helper row into bounded public evidence.
func windowsAssignmentFact(row windows.MibUnicastIpAddressRow, address netip.Addr) AssignmentFact {
	return AssignmentFact{
		Address:        address,
		PrefixLength:   int(row.OnLinkPrefixLength),
		InterfaceIndex: int(row.InterfaceIndex),
		Windows: &WindowsAssignmentFact{
			InterfaceLUID:            row.InterfaceLuid,
			SkipAsSource:             row.SkipAsSource != 0,
			PrefixOrigin:             windowsPrefixOrigin(row.PrefixOrigin),
			SuffixOrigin:             windowsSuffixOrigin(row.SuffixOrigin),
			ValidLifetimeSeconds:     row.ValidLifetime,
			PreferredLifetimeSeconds: row.PreferredLifetime,
			DADState:                 windowsDADState(row.DadState),
		},
	}
}

// sameWindowsAddressIdentity proves IP Helper returned the exact address, LUID, index, and prefix requested.
func sameWindowsAddressIdentity(observed windows.MibUnicastIpAddressRow, expected windows.MibUnicastIpAddressRow) bool {
	observedAddress, observedIPv4 := windowsIPv4Address(&observed.Address)
	expectedAddress, expectedIPv4 := windowsIPv4Address(&expected.Address)
	return observedIPv4 && expectedIPv4 &&
		observedAddress == expectedAddress &&
		observed.InterfaceLuid == expected.InterfaceLuid &&
		observed.InterfaceIndex == expected.InterfaceIndex &&
		observed.OnLinkPrefixLength == expected.OnLinkPrefixLength
}

// pendingWindowsAddressRow permits only Harbor's exact row while Windows is still performing DAD.
func pendingWindowsAddressRow(observed windows.MibUnicastIpAddressRow, expected windows.MibUnicastIpAddressRow) bool {
	return ownedWindowsAddressRow(observed, expected) &&
		observed.DadState == windows.IpDadStateTentative
}

// exactWindowsAddressRow requires the created row to be fully usable rather than merely present.
func exactWindowsAddressRow(observed windows.MibUnicastIpAddressRow, expected windows.MibUnicastIpAddressRow) bool {
	return ownedWindowsAddressRow(observed, expected) &&
		observed.DadState == windows.IpDadStatePreferred
}

// ownedWindowsAddressRow proves a rollback candidate retains Harbor's identity and every non-DAD attribute.
func ownedWindowsAddressRow(observed windows.MibUnicastIpAddressRow, expected windows.MibUnicastIpAddressRow) bool {
	return sameWindowsAddressIdentity(observed, expected) &&
		observed.SkipAsSource != 0 &&
		observed.PrefixOrigin == windows.IpPrefixOriginManual &&
		observed.SuffixOrigin == windows.IpSuffixOriginManual &&
		observed.ValidLifetime == ^uint32(0) &&
		observed.PreferredLifetime == ^uint32(0)
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

// interfaceRows copies IP Helper's allocation before releasing the native table.
func (nativeWindowsIPHelper) interfaceRows() ([]windows.MibIfRow2, error) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormalWithoutStatistics, &table); err != nil {
		return nil, err
	}
	if table == nil {
		return nil, fmt.Errorf("IP Helper returned an empty interface table pointer")
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	if table.NumEntries > maximumInterfaceFacts {
		return nil, fmt.Errorf("interface count exceeds limit %d", maximumInterfaceFacts)
	}
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	return append([]windows.MibIfRow2(nil), rows...), nil
}

// addressRows copies IP Helper's allocation before releasing the native table.
func (nativeWindowsIPHelper) addressRows() ([]windows.MibUnicastIpAddressRow, error) {
	var table *windows.MibUnicastIpAddressTable
	if err := windows.GetUnicastIpAddressTable(windows.AF_INET, &table); err != nil {
		return nil, err
	}
	if table == nil {
		return nil, fmt.Errorf("IP Helper returned an empty address table pointer")
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	if table.NumEntries > 65536 {
		return nil, fmt.Errorf("unicast address table exceeds safety limit")
	}
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	return append([]windows.MibUnicastIpAddressRow(nil), rows...), nil
}

// interfaceByLUID resolves the stable half of a Windows interface identity.
func (nativeWindowsIPHelper) interfaceByLUID(luid uint64) (windows.MibIfRow2, error) {
	row := windows.MibIfRow2{InterfaceLuid: luid}
	err := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row)
	return row, err
}

// interfaceByIndex resolves the reusable half of a Windows interface identity for comparison with its LUID.
func (nativeWindowsIPHelper) interfaceByIndex(index uint32) (windows.MibIfRow2, error) {
	row := windows.MibIfRow2{InterfaceIndex: index}
	err := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row)
	return row, err
}

// initializeAddress applies Microsoft's documented row defaults before Harbor supplies its exact fields.
func (helper nativeWindowsIPHelper) initializeAddress(row *windows.MibUnicastIpAddressRow) error {
	if helper.procedureErr != nil {
		return helper.procedureErr
	}
	initializeUnicastAddressEntry.Call(uintptr(unsafe.Pointer(row)))
	return nil
}

// address retrieves one exact IP Helper address row by its interface-bound identity.
func (nativeWindowsIPHelper) address(identity windows.MibUnicastIpAddressRow) (windows.MibUnicastIpAddressRow, error) {
	err := windows.GetUnicastIpAddressEntry(&identity)
	return identity, err
}

// createAddress returns the IP Helper status as a Windows errno so callers can inspect the actual cause.
func (helper nativeWindowsIPHelper) createAddress(row *windows.MibUnicastIpAddressRow) error {
	if helper.procedureErr != nil {
		return helper.procedureErr
	}
	status, _, _ := createUnicastAddressEntry.Call(uintptr(unsafe.Pointer(row)))
	return windowsStatusError(status)
}

// deleteAddress returns the IP Helper status as a Windows errno so callers can inspect the actual cause.
func (helper nativeWindowsIPHelper) deleteAddress(row *windows.MibUnicastIpAddressRow) error {
	if helper.procedureErr != nil {
		return helper.procedureErr
	}
	status, _, _ := deleteUnicastAddressEntry.Call(uintptr(unsafe.Pointer(row)))
	return windowsStatusError(status)
}

// windowsStatusError preserves the documented Win32 status returned directly by IP Helper procedures.
func windowsStatusError(status uintptr) error {
	if status == 0 {
		return nil
	}
	return windows.Errno(status)
}

// resolveWindowsProcedures resolves every raw IP Helper entry point once so missing symbols cannot resemble success.
func resolveWindowsProcedures() error {
	windowsProcedureResolution.Do(func() {
		windowsProcedureResolutionErr = errors.Join(
			findWindowsProcedure("InitializeUnicastIpAddressEntry", initializeUnicastAddressEntry),
			findWindowsProcedure("CreateUnicastIpAddressEntry", createUnicastAddressEntry),
			findWindowsProcedure("DeleteUnicastIpAddressEntry", deleteUnicastAddressEntry),
		)
	})
	return windowsProcedureResolutionErr
}

// findWindowsProcedure adds stable operation context to a failed DLL entry-point lookup.
func findWindowsProcedure(name string, procedure *windows.LazyProc) error {
	if err := procedure.Find(); err != nil {
		return fmt.Errorf("resolve IP Helper procedure %s: %w", name, err)
	}
	return nil
}
