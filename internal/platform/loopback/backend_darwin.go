//go:build darwin

package loopback

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

const (
	darwinInterfaceNameBytes     = 16
	maximumDarwinRIBBytes        = 16 << 20
	maximumDarwinRoutingMessages = 65536
	darwinRequiredInterfaceFlags = unix.IFF_UP | unix.IFF_LOOPBACK | unix.IFF_RUNNING
)

// darwinSockaddrInet4 pins the sockaddr_in layout consumed by Darwin's address ioctls.
type darwinSockaddrInet4 struct {
	Length  uint8
	Family  uint8
	Port    uint16
	Address [4]byte
	Zero    [8]byte
}

// darwinAliasRequest pins the in_aliasreq layout expected by SIOCAIFADDR.
type darwinAliasRequest struct {
	Name      [darwinInterfaceNameBytes]byte
	Address   darwinSockaddrInet4
	Broadcast darwinSockaddrInet4
	Mask      darwinSockaddrInet4
}

// darwinAddressRequest pins the ifreq address layout expected by SIOCDIFADDR.
type darwinAddressRequest struct {
	Name    [darwinInterfaceNameBytes]byte
	Address darwinSockaddrInet4
}

var (
	_ [16 - unsafe.Sizeof(darwinSockaddrInet4{})]byte
	_ [unsafe.Sizeof(darwinSockaddrInet4{}) - 16]byte
	_ [64 - unsafe.Sizeof(darwinAliasRequest{})]byte
	_ [unsafe.Sizeof(darwinAliasRequest{}) - 64]byte
	_ [32 - unsafe.Sizeof(darwinAddressRequest{})]byte
	_ [unsafe.Sizeof(darwinAddressRequest{}) - 32]byte

	_ [16 - unsafe.Offsetof(darwinAliasRequest{}.Address)]byte
	_ [unsafe.Offsetof(darwinAliasRequest{}.Address) - 16]byte
	_ [32 - unsafe.Offsetof(darwinAliasRequest{}.Broadcast)]byte
	_ [unsafe.Offsetof(darwinAliasRequest{}.Broadcast) - 32]byte
	_ [48 - unsafe.Offsetof(darwinAliasRequest{}.Mask)]byte
	_ [unsafe.Offsetof(darwinAliasRequest{}.Mask) - 48]byte
	_ [16 - unsafe.Offsetof(darwinAddressRequest{}.Address)]byte
	_ [unsafe.Offsetof(darwinAddressRequest{}.Address) - 16]byte

	_ [darwinInterfaceNameBytes - unix.IFNAMSIZ]byte
	_ [unix.IFNAMSIZ - darwinInterfaceNameBytes]byte
)

// darwinSnapshot keeps one routing RIB observation internally consistent while the adapter consumes typed facts.
type darwinSnapshot struct {
	interfaces  []InterfaceFact
	assignments []AssignmentFact
}

// darwinHost isolates native reads and effects so safety policy can be exercised without privileged ioctls.
type darwinHost struct {
	snapshot      func(context.Context, netip.Addr) (darwinSnapshot, error)
	openSocket    func() (int, error)
	closeSocket   func(int) error
	addAddress    func(int, *darwinAliasRequest) error
	deleteAddress func(int, *darwinAddressRequest) error
}

// platformBackend implements exact Darwin loopback effects without launching external processes.
type platformBackend struct {
	host darwinHost
}

// newPlatformBackend creates the Darwin adapter without acquiring privilege.
func newPlatformBackend() backend {
	return platformBackend{host: newDarwinHost()}
}

// newDarwinHost binds the adapter to Darwin's routing RIB and address ioctls.
func newDarwinHost() darwinHost {
	return darwinHost{
		snapshot:      readDarwinSnapshot,
		openSocket:    openDarwinAddressSocket,
		closeSocket:   unix.Close,
		addAddress:    addDarwinAddress,
		deleteAddress: deleteDarwinAddress,
	}
}

// openDarwinAddressSocket marks the descriptor close-on-exec even though Harbor's helper never launches child processes.
func openDarwinAddressSocket() (int, error) {
	file, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return -1, err
	}
	if _, err := unix.FcntlInt(uintptr(file), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		_ = unix.Close(file)
		return -1, fmt.Errorf("mark Darwin address socket close-on-exec: %w", err)
	}
	return file, nil
}

// interfaces returns every bounded interface fact from one routing RIB snapshot.
func (backend platformBackend) interfaces(ctx context.Context) ([]InterfaceFact, error) {
	snapshot, err := backend.host.snapshot(ctx, netip.Addr{})
	if err != nil {
		return nil, err
	}
	return append([]InterfaceFact(nil), snapshot.interfaces...), nil
}

// assignments returns every exact-target IPv4 assignment from one routing RIB snapshot.
func (backend platformBackend) assignments(ctx context.Context, target netip.Addr) ([]AssignmentFact, error) {
	snapshot, err := backend.host.snapshot(ctx, target)
	if err != nil {
		return nil, err
	}
	return append([]AssignmentFact(nil), snapshot.assignments...), nil
}

// ensure issues SIOCAIFADDR only after re-proving the exact name-based interface target.
func (backend platformBackend) ensure(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	request, err := newDarwinAliasRequest(interf, prefix)
	if err != nil {
		return err
	}
	return backend.mutate(ctx, interf, func(file int) error {
		return backend.host.addAddress(file, &request)
	})
}

// release issues SIOCDIFADDR only after re-proving the exact name-based interface target.
func (backend platformBackend) release(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	request, err := newDarwinAddressRequest(interf, prefix)
	if err != nil {
		return err
	}
	return backend.mutate(ctx, interf, func(file int) error {
		return backend.host.deleteAddress(file, &request)
	})
}

// mutate keeps socket acquisition outside the identity-to-ioctl window and reports cleanup uncertainty.
func (backend platformBackend) mutate(ctx context.Context, expected InterfaceFact, effect func(int) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := backend.host.openSocket()
	if err != nil {
		return fmt.Errorf("open Darwin address socket: %w", err)
	}
	defer func() {
		if closeErr := backend.host.closeSocket(file); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close Darwin address socket: %w", closeErr))
		}
	}()

	if err := backend.revalidateInterface(ctx, expected); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := effect(file); err != nil {
		return fmt.Errorf("apply Darwin address ioctl: %w", err)
	}
	return nil
}

// revalidateInterface closes the time-of-check gap created by Darwin's name-based address ioctls.
func (backend platformBackend) revalidateInterface(ctx context.Context, expected InterfaceFact) error {
	snapshot, err := backend.host.snapshot(ctx, netip.Addr{})
	if err != nil {
		return fmt.Errorf("revalidate Darwin loopback identity: %w", err)
	}
	observed, err := selectLoopback(snapshot.interfaces)
	if err != nil {
		return fmt.Errorf("revalidate Darwin loopback identity: %w", err)
	}
	if observed != expected {
		return fmt.Errorf("revalidate Darwin loopback identity: interface identity changed")
	}
	return nil
}

// readDarwinSnapshot converts Darwin's interface routing RIB into bounded adapter facts.
func readDarwinSnapshot(ctx context.Context, target netip.Addr) (darwinSnapshot, error) {
	return readDarwinSnapshotWith(ctx, target, route.FetchRIB, route.ParseRIB, darwinInterfaceType)
}

// readDarwinSnapshotWith makes cancellation and malformed kernel snapshots testable without privileged host effects.
func readDarwinSnapshotWith(
	ctx context.Context,
	target netip.Addr,
	fetch func(int, route.RIBType, int) ([]byte, error),
	parse func(route.RIBType, []byte) ([]route.Message, error),
	interfaceType func(*route.InterfaceMessage) (int, error),
) (darwinSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return darwinSnapshot{}, err
	}
	raw, err := fetch(unix.AF_UNSPEC, route.RIBTypeInterface, 0)
	if err != nil {
		return darwinSnapshot{}, fmt.Errorf("fetch Darwin interface RIB: %w", err)
	}
	if len(raw) > maximumDarwinRIBBytes {
		return darwinSnapshot{}, fmt.Errorf("Darwin interface RIB exceeds limit %d", maximumDarwinRIBBytes)
	}
	if err := ctx.Err(); err != nil {
		return darwinSnapshot{}, err
	}
	messages, err := parse(route.RIBTypeInterface, raw)
	if err != nil {
		return darwinSnapshot{}, fmt.Errorf("parse Darwin interface RIB: %w", err)
	}
	if len(messages) > maximumDarwinRoutingMessages {
		return darwinSnapshot{}, fmt.Errorf("Darwin routing message count exceeds limit %d", maximumDarwinRoutingMessages)
	}
	if err := ctx.Err(); err != nil {
		return darwinSnapshot{}, err
	}
	return darwinSnapshotFromMessages(messages, target, interfaceType)
}

// darwinSnapshotFromMessages maps interface identity before associating exact-target address messages by index.
func darwinSnapshotFromMessages(messages []route.Message, target netip.Addr, interfaceType func(*route.InterfaceMessage) (int, error)) (darwinSnapshot, error) {
	if len(messages) > maximumDarwinRoutingMessages {
		return darwinSnapshot{}, fmt.Errorf("Darwin routing message count exceeds limit %d", maximumDarwinRoutingMessages)
	}
	if target.IsValid() {
		if _, err := validateAddress(target); err != nil {
			return darwinSnapshot{}, err
		}
	}

	interfaces := make([]InterfaceFact, 0)
	addressMessages := make([]*route.InterfaceAddrMessage, 0)
	seenIndexes := make(map[int]struct{})
	seenNames := make(map[string]struct{})
	for _, generic := range messages {
		switch message := generic.(type) {
		case *route.InterfaceMessage:
			if message == nil {
				return darwinSnapshot{}, fmt.Errorf("Darwin interface message is nil")
			}
			if message.Type != unix.RTM_IFINFO && message.Type != unix.RTM_IFINFO2 {
				return darwinSnapshot{}, fmt.Errorf("Darwin interface message type %d is unsupported", message.Type)
			}
			kind, err := interfaceType(message)
			if err != nil {
				return darwinSnapshot{}, fmt.Errorf("read Darwin interface %d type: %w", message.Index, err)
			}
			fact, err := darwinInterfaceFact(message, kind)
			if err != nil {
				return darwinSnapshot{}, err
			}
			if _, exists := seenIndexes[fact.Index]; exists {
				return darwinSnapshot{}, fmt.Errorf("Darwin interface index %d is duplicated", fact.Index)
			}
			if _, exists := seenNames[fact.Name]; exists {
				return darwinSnapshot{}, fmt.Errorf("Darwin interface name %q is duplicated", fact.Name)
			}
			seenIndexes[fact.Index] = struct{}{}
			seenNames[fact.Name] = struct{}{}
			interfaces = append(interfaces, fact)
			if len(interfaces) > maximumInterfaceFacts {
				return darwinSnapshot{}, fmt.Errorf("interface count exceeds limit %d", maximumInterfaceFacts)
			}
		case *route.InterfaceAddrMessage:
			if message == nil {
				return darwinSnapshot{}, fmt.Errorf("Darwin interface address message is nil")
			}
			if message.Type != unix.RTM_NEWADDR {
				return darwinSnapshot{}, fmt.Errorf("Darwin interface address message type %d is unsupported", message.Type)
			}
			addressMessages = append(addressMessages, message)
		case *route.InterfaceMulticastAddrMessage:
			if message == nil {
				return darwinSnapshot{}, fmt.Errorf("Darwin multicast address message is nil")
			}
			if message.Type != unix.RTM_NEWMADDR && message.Type != unix.RTM_NEWMADDR2 {
				return darwinSnapshot{}, fmt.Errorf("Darwin multicast address message type %d is unsupported", message.Type)
			}
			// Multicast membership is part of NET_RT_IFLIST but cannot describe the requested unicast identity.
			continue
		default:
			return darwinSnapshot{}, fmt.Errorf("Darwin routing message type %T is unsupported", generic)
		}
	}

	snapshot := darwinSnapshot{interfaces: interfaces}
	if !target.IsValid() {
		return snapshot, nil
	}
	byIndex := make(map[int]InterfaceFact, len(interfaces))
	for _, interf := range interfaces {
		byIndex[interf.Index] = interf
	}
	for _, message := range addressMessages {
		assignment, matched, err := darwinAssignmentFact(message, target, byIndex)
		if err != nil {
			return darwinSnapshot{}, err
		}
		if !matched {
			continue
		}
		snapshot.assignments = append(snapshot.assignments, assignment)
		if len(snapshot.assignments) > maximumAssignmentFacts {
			return darwinSnapshot{}, fmt.Errorf("assignment count exceeds limit %d", maximumAssignmentFacts)
		}
	}
	return snapshot, nil
}

// darwinInterfaceType reads IFT_LOOP from interface metrics rather than confusing it with the routing message type.
func darwinInterfaceType(message *route.InterfaceMessage) (int, error) {
	systemFacts := message.Sys()
	if len(systemFacts) != 1 {
		return 0, fmt.Errorf("interface metrics count is %d", len(systemFacts))
	}
	metrics, ok := systemFacts[0].(*route.InterfaceMetrics)
	if !ok {
		return 0, fmt.Errorf("interface metrics have type %T", systemFacts[0])
	}
	return metrics.Type, nil
}

// darwinInterfaceFact accepts only names that can be represented losslessly by Darwin's ioctl ABI.
func darwinInterfaceFact(message *route.InterfaceMessage, interfaceType int) (InterfaceFact, error) {
	if message.Index <= 0 {
		return InterfaceFact{}, fmt.Errorf("Darwin interface index %d is invalid", message.Index)
	}
	if message.Name == "" || message.Name != strings.TrimSpace(message.Name) || strings.IndexByte(message.Name, 0) >= 0 || len(message.Name) >= darwinInterfaceNameBytes {
		return InterfaceFact{}, fmt.Errorf("Darwin interface %d name is invalid", message.Index)
	}
	native := message.Flags&darwinRequiredInterfaceFlags == darwinRequiredInterfaceFlags && interfaceType == unix.IFT_LOOP
	fact := InterfaceFact{Name: message.Name, Index: message.Index, NativeLoopback: native}
	if native {
		fact.Kind = InterfaceKindDarwinNative
	}
	return fact, nil
}

// darwinAssignmentFact retains every exact-target assignment and rejects masks or indexes that cannot be classified safely.
func darwinAssignmentFact(message *route.InterfaceAddrMessage, target netip.Addr, interfaces map[int]InterfaceFact) (AssignmentFact, bool, error) {
	if message.Index <= 0 {
		return AssignmentFact{}, false, fmt.Errorf("Darwin assignment interface index %d is invalid", message.Index)
	}
	addressFact := darwinRouteAddress(message.Addrs, unix.RTAX_IFA)
	address, ok := addressFact.(*route.Inet4Addr)
	if !ok || netip.AddrFrom4(address.IP) != target {
		return AssignmentFact{}, false, nil
	}
	interf, exists := interfaces[message.Index]
	if !exists {
		return AssignmentFact{}, false, fmt.Errorf("Darwin assignment interface %d was not observed", message.Index)
	}
	maskFact := darwinRouteAddress(message.Addrs, unix.RTAX_NETMASK)
	mask, ok := maskFact.(*route.Inet4Addr)
	if !ok {
		return AssignmentFact{}, false, fmt.Errorf("Darwin assignment %s has no IPv4 netmask", target)
	}
	prefixLength, ok := darwinIPv4MaskPrefixLength(mask.IP)
	if !ok {
		return AssignmentFact{}, false, fmt.Errorf("Darwin assignment %s has a non-contiguous netmask", target)
	}
	return AssignmentFact{
		Address:        target,
		PrefixLength:   prefixLength,
		InterfaceName:  interf.Name,
		InterfaceIndex: interf.Index,
	}, true, nil
}

// darwinRouteAddress bounds routing-address access because omitted RTAX slots produce shorter slices.
func darwinRouteAddress(addresses []route.Addr, index int) route.Addr {
	if index < 0 || index >= len(addresses) {
		return nil
	}
	return addresses[index]
}

// darwinIPv4MaskPrefixLength rejects masks whose one bits are not one contiguous prefix.
func darwinIPv4MaskPrefixLength(mask [4]byte) (int, bool) {
	prefixLength := 0
	seenZero := false
	for _, octet := range mask {
		for bit := 7; bit >= 0; bit-- {
			set := octet&(1<<uint(bit)) != 0
			if set && seenZero {
				return 0, false
			}
			if set {
				prefixLength++
			} else {
				seenZero = true
			}
		}
	}
	return prefixLength, true
}

// newDarwinAliasRequest constructs the exact in_aliasreq payload for a /32 loopback alias.
func newDarwinAliasRequest(interf InterfaceFact, prefix netip.Prefix) (darwinAliasRequest, error) {
	if err := validateDarwinMutation(interf, prefix); err != nil {
		return darwinAliasRequest{}, err
	}
	request := darwinAliasRequest{
		Name:    darwinInterfaceName(interf.Name),
		Address: darwinAddressSockaddr(prefix.Addr()),
		Mask: darwinSockaddrInet4{
			Length:  16,
			Address: [4]byte{0xff, 0xff, 0xff, 0xff},
		},
	}
	return request, nil
}

// newDarwinAddressRequest constructs the exact ifreq payload for deleting one observed address.
func newDarwinAddressRequest(interf InterfaceFact, prefix netip.Prefix) (darwinAddressRequest, error) {
	if err := validateDarwinMutation(interf, prefix); err != nil {
		return darwinAddressRequest{}, err
	}
	return darwinAddressRequest{
		Name:    darwinInterfaceName(interf.Name),
		Address: darwinAddressSockaddr(prefix.Addr()),
	}, nil
}

// validateDarwinMutation prevents platform-local callers from truncating an interface name or widening the requested prefix.
func validateDarwinMutation(interf InterfaceFact, prefix netip.Prefix) error {
	if !interf.NativeLoopback || interf.Kind != InterfaceKindDarwinNative || interf.Index <= 0 {
		return fmt.Errorf("Darwin mutation interface identity is invalid")
	}
	if interf.Name == "" || interf.Name != strings.TrimSpace(interf.Name) || strings.IndexByte(interf.Name, 0) >= 0 || len(interf.Name) >= darwinInterfaceNameBytes {
		return fmt.Errorf("Darwin mutation interface name is invalid")
	}
	address, err := validateAddress(prefix.Addr())
	if err != nil || prefix.Bits() != 32 || address != prefix.Addr() {
		return fmt.Errorf("Darwin mutation requires a canonical IPv4 loopback /32")
	}
	return nil
}

// darwinInterfaceName copies a previously validated name into Darwin's NUL-padded IFNAMSIZ field.
func darwinInterfaceName(name string) [darwinInterfaceNameBytes]byte {
	var encoded [darwinInterfaceNameBytes]byte
	copy(encoded[:], name)
	return encoded
}

// darwinAddressSockaddr encodes one canonical address in Darwin's sockaddr_in ABI.
func darwinAddressSockaddr(address netip.Addr) darwinSockaddrInet4 {
	return darwinSockaddrInet4{
		Length:  16,
		Family:  unix.AF_INET,
		Address: address.As4(),
	}
}

// addDarwinAddress confines unsafe ABI conversion to the fixed SIOCAIFADDR request.
func addDarwinAddress(file int, request *darwinAliasRequest) error {
	return darwinIoctl(file, uintptr(unix.SIOCAIFADDR), unsafe.Pointer(request))
}

// deleteDarwinAddress confines unsafe ABI conversion to the fixed SIOCDIFADDR request.
func deleteDarwinAddress(file int, request *darwinAddressRequest) error {
	return darwinIoctl(file, uintptr(unix.SIOCDIFADDR), unsafe.Pointer(request))
}

// darwinIoctl preserves errno for authority and race classification while keeping diagnostics bounded.
func darwinIoctl(file int, request uintptr, argument unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(file), request, uintptr(argument))
	runtime.KeepAlive(argument)
	if errno != 0 {
		return fmt.Errorf("Darwin ioctl %#x: %w", request, errno)
	}
	return nil
}
