//go:build darwin

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

const (
	darwinRouteLookupTimeout   = 2 * time.Second
	maximumDarwinRouteDatagram = 64 << 10
	maximumDarwinRouteReads    = 64
	maximumDarwinRouteSequence = uint32(1<<31 - 1)
	darwinRouteResponseNoise   = unix.RTF_DONE
	darwinRouteErrnoOffset     = 24
)

var darwinRouteLookupSequence atomic.Uint32

// darwinRouteAuthorityKey joins one RTM_GET selection to a unique RIB fact without response-only flags.
type darwinRouteAuthorityKey struct {
	destination   netip.Prefix
	interfaceID   uint32
	gateway       netip.Addr
	normalization RouteNormalization
}

// observeDarwinRoutes combines an exact kernel lookup with the complete candidate-matching route RIB.
func observeDarwinRoutes(ctx context.Context, request Request, interfaces darwinInterfaceSnapshot) (RouteSnapshot, error) {
	return observeDarwinRoutesWith(ctx, request, interfaces, lookupDarwinSelectedRoute, route.FetchRIB, route.ParseRIB)
}

// observeDarwinRoutesWith keeps route selection and dump races explicit for deterministic tests.
func observeDarwinRoutesWith(
	ctx context.Context,
	request Request,
	interfaces darwinInterfaceSnapshot,
	lookup func(context.Context, netip.Addr) (*route.RouteMessage, error),
	fetch func(int, route.RIBType, int) ([]byte, error),
	parse func(route.RIBType, []byte) ([]route.Message, error),
) (RouteSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return RouteSnapshot{}, err
	}
	selectedMessage, err := lookup(ctx, request.Candidate())
	if err != nil {
		return RouteSnapshot{}, err
	}
	raw, err := fetch(unix.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return RouteSnapshot{}, fmt.Errorf("host conflict Darwin fetch route RIB: %w", err)
	}
	allowedTypes := map[uint8]struct{}{unix.RTM_GET: {}, unix.RTM_GET2: {}}
	messageCount, err := validateDarwinRIBFrames(raw, maximumDarwinRouteRIB, allowedTypes)
	if err != nil {
		return RouteSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return RouteSnapshot{}, err
	}
	messages, err := parse(route.RIBTypeRoute, raw)
	if err != nil {
		return RouteSnapshot{}, fmt.Errorf("host conflict Darwin parse route RIB: %w", err)
	}
	if len(messages) != messageCount {
		return RouteSnapshot{}, fmt.Errorf("host conflict Darwin route parser omitted %d messages", messageCount-len(messages))
	}
	if err := normalizeDarwinRouteStatuses(raw, messages); err != nil {
		return RouteSnapshot{}, fmt.Errorf("host conflict Darwin normalize route RIB status: %w", err)
	}
	return darwinRoutesFromMessages(messages, selectedMessage, request.Candidate(), interfaces)
}

// darwinRoutesFromMessages retains every candidate match and joins the selected lookup to exactly one RIB authority.
func darwinRoutesFromMessages(messages []route.Message, selectedMessage *route.RouteMessage, candidate netip.Addr, interfaces darwinInterfaceSnapshot) (RouteSnapshot, error) {
	if selectedMessage == nil {
		return RouteSnapshot{}, fmt.Errorf("host conflict Darwin selected route is nil")
	}
	if selectedMessage.Version != unix.RTM_VERSION || selectedMessage.Type != unix.RTM_GET {
		return RouteSnapshot{}, fmt.Errorf("host conflict Darwin selected route has unsupported version %d or type %d", selectedMessage.Version, selectedMessage.Type)
	}
	selected, relevant, err := darwinRouteFact(selectedMessage, candidate, interfaces, true)
	if err != nil {
		return RouteSnapshot{}, err
	}
	if !relevant {
		return RouteSnapshot{}, fmt.Errorf("host conflict Darwin selected route does not match candidate %s", candidate)
	}
	selectedKey := darwinRouteFactAuthorityKey(selected)
	snapshot := RouteSnapshot{Complete: true}
	for _, rawMessage := range messages {
		message, ok := rawMessage.(*route.RouteMessage)
		if !ok || message == nil {
			return RouteSnapshot{}, fmt.Errorf("host conflict Darwin route RIB contains unexpected message %T", rawMessage)
		}
		if message.Type != unix.RTM_GET && message.Type != unix.RTM_GET2 {
			return RouteSnapshot{}, fmt.Errorf("host conflict Darwin route RIB contains unsupported route type %d", message.Type)
		}
		fact, relevant, err := darwinRouteFact(message, candidate, interfaces, false)
		if err != nil {
			return RouteSnapshot{}, err
		}
		if !relevant {
			continue
		}
		if len(snapshot.Matching) >= maximumRouteFacts {
			return RouteSnapshot{}, fmt.Errorf("host conflict Darwin matching routes exceed %d facts", maximumRouteFacts)
		}
		snapshot.Matching = append(snapshot.Matching, fact)
	}
	matches := 0
	var selectedRIBFact RouteFact
	for _, fact := range snapshot.Matching {
		if darwinRouteFactAuthorityKey(fact) == selectedKey {
			matches++
			selectedRIBFact = fact
		}
	}
	if matches != 1 {
		return RouteSnapshot{}, errors.Join(errDarwinRouteSnapshotChanged, fmt.Errorf("selected route authority matched %d following RIB facts, want 1", matches))
	}
	snapshot.Selected = &selectedRIBFact
	return snapshot, nil
}

// darwinRouteFact converts one route without discarding flags that could change candidate selection.
func darwinRouteFact(message *route.RouteMessage, candidate netip.Addr, interfaces darwinInterfaceSnapshot, selectedLookup bool) (RouteFact, bool, error) {
	if message.Err != nil {
		return RouteFact{}, false, fmt.Errorf("host conflict Darwin route message status: %w", message.Err)
	}
	if message.Flags < 0 {
		return RouteFact{}, false, fmt.Errorf("host conflict Darwin route flags are negative")
	}
	flags := uint32(message.Flags)
	destination, err := darwinRouteDestination(message, selectedLookup)
	if err != nil {
		return RouteFact{}, false, err
	}
	if !destination.Contains(candidate) {
		return RouteFact{}, false, nil
	}
	if flags&^darwinKnownRouteFlags() != 0 {
		return RouteFact{}, true, fmt.Errorf("host conflict Darwin route contains unknown flags %#x", flags&^darwinKnownRouteFlags())
	}
	if err := validateDarwinRouteAddresses(message.Addrs); err != nil {
		return RouteFact{}, true, err
	}
	if flags&unix.RTF_UP == 0 {
		return RouteFact{}, true, fmt.Errorf("host conflict Darwin matching route is not up")
	}
	if unsafe := flags & darwinUnsafeRouteFlags(); unsafe != 0 {
		return RouteFact{}, true, fmt.Errorf("host conflict Darwin matching route contains unsupported semantics %#x", unsafe)
	}
	identity, err := darwinRouteInterface(message, interfaces)
	if err != nil {
		return RouteFact{}, true, err
	}
	gateway, err := darwinRouteGateway(message, flags, identity)
	if err != nil {
		return RouteFact{}, true, err
	}
	native := sameInterfaceAuthority(PlatformMacOS, identity, interfaces.loopback.Interface)
	normalization := RouteNormalizationDirect
	if flags&unix.RTF_WASCLONED != 0 {
		if destination.Bits() == 32 && native && !gateway.IsValid() {
			normalization = RouteNormalizationMacOSCloneUnresolved
		} else if destination.Bits() <= 8 {
			return RouteFact{}, true, fmt.Errorf("host conflict Darwin cloned route has an invalid parent-shaped destination %s", destination)
		}
	}
	if flags&unix.RTF_LLINFO != 0 && flags&unix.RTF_WASCLONED == 0 {
		return RouteFact{}, true, fmt.Errorf("host conflict Darwin link-layer route lacks cloned-route evidence")
	}
	return RouteFact{
		Destination:    destination,
		Interface:      identity,
		NativeLoopback: native,
		Gateway:        gateway,
		Normalization:  normalization,
		NativeFlags:    uint64(canonicalDarwinRouteFlags(flags)),
	}, true, nil
}

// darwinRouteFactAuthorityKey keeps native flag drift visible while joining equivalent lookup and dump encodings.
func darwinRouteFactAuthorityKey(fact RouteFact) darwinRouteAuthorityKey {
	return darwinRouteAuthorityKey{
		destination:   fact.Destination,
		interfaceID:   fact.Interface.Index,
		gateway:       fact.Gateway,
		normalization: fact.Normalization,
	}
}

// canonicalDarwinRouteFlags removes only RTF_DONE, which XNU adds when confirming a routing-socket message.
func canonicalDarwinRouteFlags(flags uint32) uint32 {
	return flags &^ darwinRouteResponseNoise
}

// darwinRouteDestination normalizes an RTM_GET echo while requiring dump destinations to be prefix-canonical.
func darwinRouteDestination(message *route.RouteMessage, selectedLookup bool) (netip.Prefix, error) {
	destinationAddress, ok := darwinRouteIPv4Address(message.Addrs, unix.RTAX_DST)
	if !ok {
		return netip.Prefix{}, fmt.Errorf("host conflict Darwin route omits an IPv4 destination")
	}
	bits := 0
	maskAddress, maskPresent := darwinRouteIPv4Address(message.Addrs, unix.RTAX_NETMASK)
	if message.Flags&unix.RTF_HOST != 0 {
		bits = 32
		if maskPresent {
			maskBits, valid := darwinIPv4MaskPrefixLength(maskAddress.As4())
			if !valid || maskBits != 32 {
				return netip.Prefix{}, fmt.Errorf("host conflict Darwin host route has a non-host netmask")
			}
		}
	} else if maskPresent {
		var valid bool
		bits, valid = darwinIPv4MaskPrefixLength(maskAddress.As4())
		if !valid {
			return netip.Prefix{}, fmt.Errorf("host conflict Darwin route netmask is not contiguous")
		}
	} else if !destinationAddress.IsUnspecified() {
		return netip.Prefix{}, fmt.Errorf("host conflict Darwin non-default route omits its netmask")
	}
	prefix := netip.PrefixFrom(destinationAddress, bits).Masked()
	if !selectedLookup && prefix.Addr() != destinationAddress {
		return netip.Prefix{}, fmt.Errorf("host conflict Darwin route destination %s is not prefix-canonical", destinationAddress)
	}
	return prefix, nil
}

// darwinRouteInterface reconciles every available native interface index and rejects stale RIB identities.
func darwinRouteInterface(message *route.RouteMessage, interfaces darwinInterfaceSnapshot) (InterfaceIdentity, error) {
	indexes := make([]uint32, 0, 3)
	if message.Index > 0 && uint64(message.Index) <= uint64(^uint32(0)) {
		indexes = append(indexes, uint32(message.Index))
	} else if message.Index != 0 {
		return InterfaceIdentity{}, fmt.Errorf("host conflict Darwin route interface index %d is invalid", message.Index)
	}
	for _, position := range []int{unix.RTAX_IFP, unix.RTAX_GATEWAY} {
		if position >= len(message.Addrs) || message.Addrs[position] == nil {
			continue
		}
		link, ok := message.Addrs[position].(*route.LinkAddr)
		if !ok {
			continue
		}
		if link.Index > 0 && uint64(link.Index) <= uint64(^uint32(0)) {
			indexes = append(indexes, uint32(link.Index))
		} else if link.Index != 0 {
			return InterfaceIdentity{}, fmt.Errorf("host conflict Darwin route link index %d is invalid", link.Index)
		}
	}
	if len(indexes) == 0 {
		return InterfaceIdentity{}, fmt.Errorf("host conflict Darwin route omits its interface index")
	}
	index := indexes[0]
	for _, observed := range indexes[1:] {
		if observed != index {
			return InterfaceIdentity{}, fmt.Errorf("host conflict Darwin route contains inconsistent interface indexes %d and %d", index, observed)
		}
	}
	identity, exists := interfaces.byIndex[index]
	if !exists {
		return InterfaceIdentity{}, errors.Join(errDarwinRouteSnapshotChanged, fmt.Errorf("route references unknown interface index %d", index))
	}
	for _, address := range message.Addrs {
		link, ok := address.(*route.LinkAddr)
		if !ok || link.Name == "" {
			continue
		}
		if link.Name != identity.Name {
			return InterfaceIdentity{}, errors.Join(errDarwinRouteSnapshotChanged, fmt.Errorf("route interface name %q does not match index %d name %q", link.Name, index, identity.Name))
		}
	}
	return identity, nil
}

// darwinRouteGateway preserves an IPv4 next hop while treating a direct link address as absence.
func darwinRouteGateway(message *route.RouteMessage, flags uint32, identity InterfaceIdentity) (netip.Addr, error) {
	if unix.RTAX_GATEWAY >= len(message.Addrs) || message.Addrs[unix.RTAX_GATEWAY] == nil {
		if flags&unix.RTF_GATEWAY != 0 {
			return netip.Addr{}, fmt.Errorf("host conflict Darwin gateway route omits its next hop")
		}
		return netip.Addr{}, nil
	}
	switch gateway := message.Addrs[unix.RTAX_GATEWAY].(type) {
	case *route.Inet4Addr:
		if gateway == nil || flags&unix.RTF_GATEWAY == 0 {
			return netip.Addr{}, fmt.Errorf("host conflict Darwin route contains inconsistent IPv4 gateway evidence")
		}
		address := netip.AddrFrom4(gateway.IP)
		if address.IsUnspecified() {
			return netip.Addr{}, fmt.Errorf("host conflict Darwin route gateway is unspecified")
		}
		return address, nil
	case *route.LinkAddr:
		if gateway == nil || flags&unix.RTF_GATEWAY != 0 {
			return netip.Addr{}, fmt.Errorf("host conflict Darwin route contains inconsistent link gateway evidence")
		}
		if gateway.Index < 0 {
			return netip.Addr{}, fmt.Errorf("host conflict Darwin direct gateway index is negative")
		}
		if gateway.Index != 0 && uint32(gateway.Index) != identity.Index {
			return netip.Addr{}, fmt.Errorf("host conflict Darwin direct gateway index does not match route interface")
		}
		return netip.Addr{}, nil
	default:
		return netip.Addr{}, fmt.Errorf("host conflict Darwin route gateway has unsupported type %T", message.Addrs[unix.RTAX_GATEWAY])
	}
}

// validateDarwinRouteAddresses rejects family aliases in every address slot that affects an IPv4 route.
func validateDarwinRouteAddresses(addresses []route.Addr) error {
	if len(addresses) > unix.RTAX_MAX {
		return fmt.Errorf("host conflict Darwin route contains %d address slots, want at most %d", len(addresses), unix.RTAX_MAX)
	}
	for position, address := range addresses {
		if address == nil {
			continue
		}
		valid := false
		switch position {
		case unix.RTAX_DST, unix.RTAX_NETMASK, unix.RTAX_GENMASK, unix.RTAX_IFA, unix.RTAX_AUTHOR, unix.RTAX_BRD:
			value, ok := address.(*route.Inet4Addr)
			valid = ok && value != nil
		case unix.RTAX_GATEWAY:
			switch value := address.(type) {
			case *route.Inet4Addr:
				valid = value != nil
			case *route.LinkAddr:
				valid = value != nil
			}
		case unix.RTAX_IFP:
			value, ok := address.(*route.LinkAddr)
			valid = ok && value != nil
		}
		if !valid {
			return fmt.Errorf("host conflict Darwin route address slot %d has unsupported type %T", position, address)
		}
	}
	return nil
}

// darwinRouteIPv4Address returns one typed IPv4 routing address without accepting family aliases.
func darwinRouteIPv4Address(addresses []route.Addr, position int) (netip.Addr, bool) {
	if position >= len(addresses) || addresses[position] == nil {
		return netip.Addr{}, false
	}
	address, ok := addresses[position].(*route.Inet4Addr)
	if !ok || address == nil {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4(address.IP), true
}

// darwinIPv4MaskPrefixLength accepts only one contiguous most-significant-bit mask.
func darwinIPv4MaskPrefixLength(mask [4]byte) (int, bool) {
	bits := 0
	zeroSeen := false
	for _, value := range mask {
		for shift := 7; shift >= 0; shift-- {
			set := value&(1<<shift) != 0
			if set && zeroSeen {
				return 0, false
			}
			if set {
				bits++
			} else {
				zeroSeen = true
			}
		}
	}
	return bits, true
}

// darwinKnownRouteFlags enumerates every flag in the pinned XNU route ABI so future bits fail closed.
func darwinKnownRouteFlags() uint32 {
	return unix.RTF_UP | unix.RTF_GATEWAY | unix.RTF_HOST | unix.RTF_REJECT |
		unix.RTF_DYNAMIC | unix.RTF_MODIFIED | unix.RTF_DONE | unix.RTF_DELCLONE |
		unix.RTF_CLONING | unix.RTF_XRESOLVE | unix.RTF_LLINFO | unix.RTF_STATIC |
		unix.RTF_BLACKHOLE | unix.RTF_NOIFREF | unix.RTF_PROTO2 | unix.RTF_PROTO1 |
		unix.RTF_PRCLONING | unix.RTF_WASCLONED | unix.RTF_PROTO3 | unix.RTF_PINNED |
		unix.RTF_LOCAL | unix.RTF_BROADCAST | unix.RTF_MULTICAST | unix.RTF_IFSCOPE |
		unix.RTF_CONDEMNED | unix.RTF_IFREF | unix.RTF_PROXY | unix.RTF_ROUTER |
		unix.RTF_DEAD | unix.RTF_GLOBAL
}

// darwinUnsafeRouteFlags identifies semantics the shared route model cannot preserve losslessly.
func darwinUnsafeRouteFlags() uint32 {
	return unix.RTF_REJECT | unix.RTF_DELCLONE | unix.RTF_XRESOLVE | unix.RTF_BLACKHOLE |
		unix.RTF_PROTO2 | unix.RTF_PROTO1 | unix.RTF_PROTO3 | unix.RTF_CONDEMNED |
		unix.RTF_DEAD
}

// lookupDarwinSelectedRoute asks the routing socket for the process-global route selected for one exact address.
func lookupDarwinSelectedRoute(ctx context.Context, candidate netip.Addr) (selected *route.RouteMessage, lookupErr error) {
	fileDescriptor, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("host conflict Darwin open route socket: %w", err)
	}
	defer func() {
		if err := unix.Close(fileDescriptor); err != nil {
			lookupErr = errors.Join(lookupErr, fmt.Errorf("host conflict Darwin close route socket: %w", err))
		}
	}()
	if _, err := unix.FcntlInt(uintptr(fileDescriptor), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return nil, fmt.Errorf("host conflict Darwin mark route socket close-on-exec: %w", err)
	}
	sequence := nextDarwinRouteSequence()
	processID := uintptr(unix.Getpid())
	query, err := newDarwinSelectedRouteQuery(candidate, processID, sequence)
	if err != nil {
		return nil, err
	}
	encoded, err := query.Marshal()
	if err != nil {
		return nil, fmt.Errorf("host conflict Darwin encode selected-route query: %w", err)
	}
	if len(encoded) > maximumDarwinRouteDatagram {
		return nil, fmt.Errorf("host conflict Darwin selected-route query exceeds its bound")
	}
	if err := writeDarwinRouteQuery(fileDescriptor, encoded); err != nil {
		return nil, err
	}
	lookupContext, cancel := context.WithTimeout(normalizeDarwinObservationContext(ctx), darwinRouteLookupTimeout)
	defer cancel()
	return receiveDarwinSelectedRoute(lookupContext, fileDescriptor, processID, sequence)
}

// newDarwinSelectedRouteQuery builds the same host lookup shape as Darwin's native route utility.
func newDarwinSelectedRouteQuery(candidate netip.Addr, processID uintptr, sequence int) (*route.RouteMessage, error) {
	if !candidate.Is4() {
		return nil, fmt.Errorf("host conflict Darwin selected-route candidate %s is not IPv4", candidate)
	}
	if processID == 0 || uint64(processID) > math.MaxUint32 {
		return nil, fmt.Errorf("host conflict Darwin selected-route process ID %d is invalid", processID)
	}
	if sequence <= 0 || uint64(sequence) > uint64(maximumDarwinRouteSequence) {
		return nil, fmt.Errorf("host conflict Darwin selected-route sequence %d is invalid", sequence)
	}
	addresses := make([]route.Addr, unix.RTAX_MAX)
	addresses[unix.RTAX_DST] = &route.Inet4Addr{IP: candidate.As4()}
	addresses[unix.RTAX_IFP] = &route.LinkAddr{}
	return &route.RouteMessage{
		Version: unix.RTM_VERSION,
		Type:    unix.RTM_GET,
		Flags:   unix.RTF_UP | unix.RTF_HOST | unix.RTF_GATEWAY,
		ID:      processID,
		Seq:     sequence,
		Addrs:   addresses,
	}, nil
}

// nextDarwinRouteSequence allocates a process-global nonzero signed sequence for broadcast AF_ROUTE replies.
func nextDarwinRouteSequence() int {
	for {
		current := darwinRouteLookupSequence.Load()
		next := current + 1
		if next == 0 || next > maximumDarwinRouteSequence {
			next = 1
		}
		if darwinRouteLookupSequence.CompareAndSwap(current, next) {
			return int(next)
		}
	}
}

// writeDarwinRouteQuery publishes exactly one atomic routing record without hiding the original errno.
func writeDarwinRouteQuery(fileDescriptor int, encoded []byte) error {
	return writeDarwinRouteQueryWith(fileDescriptor, encoded, unix.Write)
}

// writeDarwinRouteQueryWith treats one routing message as an atomic record and retries only interruption.
func writeDarwinRouteQueryWith(fileDescriptor int, encoded []byte, write func(int, []byte) (int, error)) error {
	for {
		written, err := write(fileDescriptor, encoded)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("host conflict Darwin write selected-route query: %w", err)
		}
		if written != len(encoded) {
			return fmt.Errorf("host conflict Darwin selected-route query made non-atomic write %d of %d bytes", written, len(encoded))
		}
		return nil
	}
}

// receiveDarwinSelectedRoute filters bounded route events until the exact process and sequence response arrives.
func receiveDarwinSelectedRoute(ctx context.Context, fileDescriptor int, processID uintptr, sequence int) (*route.RouteMessage, error) {
	buffer := make([]byte, maximumDarwinRouteDatagram)
	for reads := 0; reads < maximumDarwinRouteReads; reads++ {
		if err := waitDarwinRouteReadable(ctx, fileDescriptor); err != nil {
			return nil, err
		}
		length, _, flags, _, err := unix.Recvmsg(fileDescriptor, buffer, nil, 0)
		if errors.Is(err, unix.EINTR) {
			reads--
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("host conflict Darwin read selected-route response: %w", err)
		}
		if flags&unix.MSG_TRUNC != 0 || length <= 0 || length > len(buffer) {
			return nil, fmt.Errorf("host conflict Darwin selected-route response was truncated")
		}
		allowedTypes := darwinRouteSocketMessageTypes()
		messageCount, err := validateDarwinRIBFrames(buffer[:length], maximumDarwinRouteDatagram, allowedTypes)
		if err != nil {
			return nil, err
		}
		messages, err := route.ParseRIB(route.RIBTypeRoute, buffer[:length])
		if err != nil {
			return nil, fmt.Errorf("host conflict Darwin parse selected-route response: %w", err)
		}
		if len(messages) != messageCount {
			return nil, fmt.Errorf("host conflict Darwin route socket parser omitted %d messages", messageCount-len(messages))
		}
		if err := normalizeDarwinRouteStatuses(buffer[:length], messages); err != nil {
			return nil, fmt.Errorf("host conflict Darwin normalize route socket status: %w", err)
		}
		var selected *route.RouteMessage
		for _, rawMessage := range messages {
			message, ok := rawMessage.(*route.RouteMessage)
			if !ok || message == nil || message.Type != unix.RTM_GET || message.ID != processID || message.Seq != sequence {
				continue
			}
			if selected != nil {
				return nil, fmt.Errorf("host conflict Darwin route socket returned duplicate selected-route responses")
			}
			selected = message
		}
		if selected != nil {
			if selected.Err != nil {
				return nil, fmt.Errorf("host conflict Darwin selected-route query: %w", selected.Err)
			}
			return selected, nil
		}
	}
	return nil, fmt.Errorf("host conflict Darwin selected-route response exceeded %d reads", maximumDarwinRouteReads)
}

// normalizeDarwinRouteStatuses replaces x/net/route's BSD-generic status with Darwin's native rtm_errno field.
func normalizeDarwinRouteStatuses(raw []byte, messages []route.Message) error {
	for index, message := range messages {
		if len(raw) < 4 {
			return fmt.Errorf("route message %d has a truncated native header", index)
		}
		length := int(binary.NativeEndian.Uint16(raw[:2]))
		if length < 4 || length > len(raw) {
			return fmt.Errorf("route message %d has invalid native length %d", index, length)
		}
		routeMessage, ok := message.(*route.RouteMessage)
		if ok && routeMessage != nil && (routeMessage.Type == unix.RTM_GET || routeMessage.Type == unix.RTM_GET2) {
			if int(raw[3]) != routeMessage.Type {
				return fmt.Errorf("route message %d native type %d does not match parsed type %d", index, raw[3], routeMessage.Type)
			}
			if length < unix.SizeofRtMsghdr {
				return fmt.Errorf("route message %d native header has length %d, want at least %d", index, length, unix.SizeofRtMsghdr)
			}
			routeMessage.Err = nil
			if routeMessage.Type == unix.RTM_GET {
				status := syscall.Errno(binary.NativeEndian.Uint32(raw[darwinRouteErrnoOffset : darwinRouteErrnoOffset+4]))
				if status != 0 {
					routeMessage.Err = status
				}
			}
		}
		raw = raw[length:]
	}
	if len(raw) != 0 {
		return fmt.Errorf("native route frames and parsed messages differ by %d bytes", len(raw))
	}
	return nil
}

// waitDarwinRouteReadable bounds a blocking route socket independently from caller deadline choices.
func waitDarwinRouteReadable(ctx context.Context, fileDescriptor int) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("host conflict Darwin wait for selected-route response: %w", err)
		}
		timeout := 50
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return fmt.Errorf("host conflict Darwin wait for selected-route response: %w", context.DeadlineExceeded)
			}
			if remaining < 50*time.Millisecond {
				timeout = max(1, int(remaining/time.Millisecond))
			}
		}
		poll := []unix.PollFd{{Fd: int32(fileDescriptor), Events: unix.POLLIN}}
		ready, err := unix.Poll(poll, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("host conflict Darwin poll selected-route response: %w", err)
		}
		if ready == 0 {
			continue
		}
		if poll[0].Revents&unix.POLLIN != 0 {
			return nil
		}
		return fmt.Errorf("host conflict Darwin route socket returned poll events %#x", poll[0].Revents)
	}
}

// darwinRouteSocketMessageTypes enumerates every route event x/net/route can decode on current Darwin.
func darwinRouteSocketMessageTypes() map[uint8]struct{} {
	return map[uint8]struct{}{
		unix.RTM_ADD: {}, unix.RTM_DELETE: {}, unix.RTM_CHANGE: {}, unix.RTM_GET: {},
		unix.RTM_LOSING: {}, unix.RTM_REDIRECT: {}, unix.RTM_MISS: {}, unix.RTM_LOCK: {},
		unix.RTM_RESOLVE: {}, unix.RTM_NEWADDR: {}, unix.RTM_DELADDR: {}, unix.RTM_IFINFO: {},
		unix.RTM_NEWMADDR: {}, unix.RTM_DELMADDR: {}, unix.RTM_IFINFO2: {}, unix.RTM_NEWMADDR2: {},
		unix.RTM_GET2: {},
	}
}
