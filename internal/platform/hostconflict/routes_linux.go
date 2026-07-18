//go:build linux

package hostconflict

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"
)

var linuxOrdinaryLoopbackPrefix = netip.MustParsePrefix("127.0.0.0/8")

// linuxInterface contains the rtnetlink identity and flags needed to prove native loopback selection.
type linuxInterface struct {
	identity InterfaceIdentity
	hardware uint16
	flags    uint32
}

// linuxInterfaceSnapshot binds route and policy parsing to one bounded RTM_GETLINK dump.
type linuxInterfaceSnapshot struct {
	byIndex   map[uint32]linuxInterface
	ordered   []linuxInterface
	loopback  LoopbackIdentity
	complete  bool
	truncated bool
}

// observeLinuxInterfaces requires the namespace's single operational kernel loopback device.
func observeLinuxInterfaces(ctx context.Context, client linuxNetlinkExchanger) (linuxInterfaceSnapshot, error) {
	request := make([]byte, unix.SizeofIfInfomsg)
	request[0] = unix.AF_UNSPEC
	reply, err := client.exchange(ctx, unix.RTM_GETLINK, unix.NLM_F_DUMP, request, true)
	if err != nil {
		return linuxInterfaceSnapshot{}, err
	}
	snapshot := linuxInterfaceSnapshot{
		byIndex:   make(map[uint32]linuxInterface),
		complete:  !reply.truncated,
		truncated: reply.truncated,
	}
	seenNames := make(map[string]struct{})
	seenIndexes := make(map[uint32]struct{})
	loopbacks := make([]linuxInterface, 0, 1)
	for _, message := range reply.messages {
		if message.messageType != unix.RTM_NEWLINK {
			return linuxInterfaceSnapshot{}, fmt.Errorf("host conflict Linux link dump returned message type %d", message.messageType)
		}
		linuxInterface, err := parseLinuxInterface(message.payload)
		if err != nil {
			return linuxInterfaceSnapshot{}, err
		}
		if _, exists := seenIndexes[linuxInterface.identity.Index]; exists {
			return linuxInterfaceSnapshot{}, fmt.Errorf("host conflict Linux link dump repeats interface index %d", linuxInterface.identity.Index)
		}
		seenIndexes[linuxInterface.identity.Index] = struct{}{}
		if _, exists := seenNames[linuxInterface.identity.Name]; exists {
			return linuxInterfaceSnapshot{}, fmt.Errorf("host conflict Linux link dump repeats interface name %q", linuxInterface.identity.Name)
		}
		seenNames[linuxInterface.identity.Name] = struct{}{}

		isNativeLoopback := linuxInterface.flags&(unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK) == unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK && linuxInterface.hardware == unix.ARPHRD_LOOPBACK
		if isNativeLoopback {
			loopbacks = append(loopbacks, linuxInterface)
		}
		if len(snapshot.ordered) >= maximumPolicyInterfaces {
			snapshot.complete = false
			snapshot.truncated = true
			continue
		}
		snapshot.byIndex[linuxInterface.identity.Index] = linuxInterface
		snapshot.ordered = append(snapshot.ordered, linuxInterface)
	}
	if len(loopbacks) != 1 {
		return linuxInterfaceSnapshot{}, fmt.Errorf("host conflict Linux namespace has %d operational native loopback interfaces", len(loopbacks))
	}
	if _, retained := snapshot.byIndex[loopbacks[0].identity.Index]; !retained {
		return linuxInterfaceSnapshot{}, fmt.Errorf("host conflict Linux native loopback fell outside the interface observation bound")
	}
	snapshot.loopback = LoopbackIdentity{Interface: loopbacks[0].identity, Kind: LoopbackKindLinuxNative}
	return snapshot, nil
}

// parseLinuxInterface decodes the fixed ifinfomsg and its one canonical kernel name.
func parseLinuxInterface(payload []byte) (linuxInterface, error) {
	if len(payload) < unix.SizeofIfInfomsg {
		return linuxInterface{}, fmt.Errorf("host conflict Linux link message is truncated")
	}
	index := int32(binary.NativeEndian.Uint32(payload[4:8]))
	if index <= 0 {
		return linuxInterface{}, fmt.Errorf("host conflict Linux link message has invalid interface index %d", index)
	}
	attributes, err := parseLinuxNetlinkAttributes(payload[unix.SizeofIfInfomsg:])
	if err != nil {
		return linuxInterface{}, err
	}
	namePayload, ok, err := oneLinuxAttribute(attributes, unix.IFLA_IFNAME)
	if err != nil {
		return linuxInterface{}, err
	}
	if !ok || len(namePayload) < 2 || namePayload[len(namePayload)-1] != 0 {
		return linuxInterface{}, fmt.Errorf("host conflict Linux link message is missing a terminated interface name")
	}
	if len(namePayload)-1 > unix.IFNAMSIZ-1 {
		return linuxInterface{}, fmt.Errorf("host conflict Linux interface name exceeds %d bytes", unix.IFNAMSIZ-1)
	}
	for _, value := range namePayload[:len(namePayload)-1] {
		if value == 0 {
			return linuxInterface{}, fmt.Errorf("host conflict Linux link message contains an embedded interface-name terminator")
		}
	}
	identity := InterfaceIdentity{Name: string(namePayload[:len(namePayload)-1]), Index: uint32(index)}
	if err := identity.Validate(); err != nil {
		return linuxInterface{}, err
	}
	return linuxInterface{
		identity: identity,
		hardware: binary.NativeEndian.Uint16(payload[2:4]),
		flags:    binary.NativeEndian.Uint32(payload[8:12]),
	}, nil
}

// observeLinuxRoutes combines the all-table dump with FIB_MATCH selections for every requested flow.
func observeLinuxRoutes(ctx context.Context, client linuxNetlinkExchanger, request Request, requesterUID uint32, interfaces linuxInterfaceSnapshot) (RouteSnapshot, error) {
	reply, err := client.exchange(ctx, unix.RTM_GETROUTE, unix.NLM_F_DUMP, marshalLinuxRouteMessage(unix.AF_INET, 0, 0), true)
	if err != nil {
		return RouteSnapshot{}, err
	}
	snapshot := RouteSnapshot{
		Complete:  interfaces.complete && !reply.truncated,
		Truncated: interfaces.truncated || reply.truncated,
	}
	for _, message := range reply.messages {
		if message.messageType != unix.RTM_NEWROUTE {
			return RouteSnapshot{}, fmt.Errorf("host conflict Linux route dump returned message type %d", message.messageType)
		}
		fact, matches, representable, err := parseLinuxRoute(message.payload, request.Candidate(), interfaces)
		if err != nil {
			return RouteSnapshot{}, err
		}
		if !matches {
			continue
		}
		if !representable {
			snapshot.Complete = false
			continue
		}
		if len(snapshot.Matching) >= maximumRouteFacts {
			snapshot.Complete = false
			snapshot.Truncated = true
			continue
		}
		snapshot.Matching = append(snapshot.Matching, fact)
	}

	selected, selectedComplete, selectedTruncated, err := observeLinuxSelectedRoutes(ctx, client, request, requesterUID, interfaces)
	if err != nil {
		return RouteSnapshot{}, err
	}
	if selectedTruncated {
		snapshot.Truncated = true
	}
	if !selectedComplete {
		snapshot.Complete = false
	}
	if selected != nil && routeFactCount(snapshot.Matching, *selected) > 0 {
		snapshot.Selected = selected
	} else {
		snapshot.Complete = false
	}
	if snapshot.Truncated {
		snapshot.Complete = false
	}
	return snapshot, nil
}

// observeLinuxSelectedRoutes proves UID and every protocol-port lookup choose the same normalized FIB route.
func observeLinuxSelectedRoutes(ctx context.Context, client linuxNetlinkExchanger, request Request, requesterUID uint32, interfaces linuxInterfaceSnapshot) (*RouteFact, bool, bool, error) {
	lookups := append([]SocketRequirement{{}}, request.Requirements()...)
	var selected *RouteFact
	complete := true
	truncated := false
	for _, requirement := range lookups {
		payload := marshalLinuxRouteLookup(request.Candidate(), requesterUID, requirement)
		reply, err := client.exchange(ctx, unix.RTM_GETROUTE, 0, payload, false)
		if err != nil {
			return nil, false, false, err
		}
		if reply.truncated {
			complete = false
			truncated = true
			continue
		}
		if len(reply.messages) != 1 || reply.messages[0].messageType != unix.RTM_NEWROUTE {
			complete = false
			continue
		}
		fact, matches, representable, err := parseLinuxSelectedRoute(reply.messages[0].payload, request.Candidate(), interfaces)
		if err != nil {
			return nil, false, false, err
		}
		if !matches || !representable {
			complete = false
			continue
		}
		if selected == nil {
			selected = &fact
			continue
		}
		if *selected != fact {
			complete = false
			selected = nil
		}
	}
	if !complete {
		selected = nil
	}
	return selected, complete, truncated, nil
}

// marshalLinuxRouteMessage encodes the fixed rtmsg shared by dumps and selected lookups.
func marshalLinuxRouteMessage(family uint8, destinationBits uint8, flags uint32) []byte {
	payload := make([]byte, unix.SizeofRtMsg)
	payload[0] = family
	payload[1] = destinationBits
	binary.NativeEndian.PutUint32(payload[8:12], flags)
	return payload
}

// marshalLinuxRouteLookup binds kernel policy routing to the requesting UID and exact protected flow.
func marshalLinuxRouteLookup(candidate netip.Addr, requesterUID uint32, requirement SocketRequirement) []byte {
	payload := marshalLinuxRouteMessage(unix.AF_INET, 32, unix.RTM_F_LOOKUP_TABLE|unix.RTM_F_FIB_MATCH)
	candidateBytes := candidate.As4()
	payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_DST, candidateBytes[:])
	uid := make([]byte, 4)
	binary.NativeEndian.PutUint32(uid, requesterUID)
	payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_UID, uid)
	if requirement.Port == 0 {
		return payload
	}
	protocol := byte(unix.IPPROTO_TCP)
	if requirement.Transport == TransportUDP4 {
		protocol = unix.IPPROTO_UDP
	}
	payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_IP_PROTO, []byte{protocol})
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, requirement.Port)
	payload = marshalLinuxNetlinkAttribute(payload, unix.RTA_DPORT, port)
	return payload
}

// parseLinuxRoute identifies candidate-matching prefixes before deciding whether the model can preserve them.
func parseLinuxRoute(payload []byte, candidate netip.Addr, interfaces linuxInterfaceSnapshot) (RouteFact, bool, bool, error) {
	return parseLinuxRouteForSelection(payload, candidate, interfaces, false)
}

// parseLinuxSelectedRoute permits RTM_F_LOOKUP_TABLE to report the lookup table while preserving baseline semantics.
func parseLinuxSelectedRoute(payload []byte, candidate netip.Addr, interfaces linuxInterfaceSnapshot) (RouteFact, bool, bool, error) {
	return parseLinuxRouteForSelection(payload, candidate, interfaces, true)
}

// parseLinuxRouteForSelection keeps dump-origin metadata stricter than the kernel's selected-route table report.
func parseLinuxRouteForSelection(payload []byte, candidate netip.Addr, interfaces linuxInterfaceSnapshot, selectedLookup bool) (RouteFact, bool, bool, error) {
	if len(payload) < unix.SizeofRtMsg {
		return RouteFact{}, false, false, fmt.Errorf("host conflict Linux route message is truncated")
	}
	if payload[0] != unix.AF_INET {
		return RouteFact{}, false, false, fmt.Errorf("host conflict Linux IPv4 route query returned family %d", payload[0])
	}
	destinationBits := int(payload[1])
	if destinationBits < 0 || destinationBits > 32 {
		return RouteFact{}, false, false, fmt.Errorf("host conflict Linux route has invalid prefix length %d", destinationBits)
	}
	attributes, err := parseLinuxNetlinkAttributes(payload[unix.SizeofRtMsg:])
	if err != nil {
		return RouteFact{}, false, false, err
	}
	destinationPayload, destinationPresent, err := oneLinuxAttribute(attributes, unix.RTA_DST)
	if err != nil {
		return RouteFact{}, false, false, err
	}
	if !destinationPresent && destinationBits != 0 {
		return RouteFact{}, false, false, fmt.Errorf("host conflict Linux route omits its non-default destination")
	}
	destinationAddress := netip.IPv4Unspecified()
	if destinationPresent {
		if len(destinationPayload) != 4 {
			return RouteFact{}, false, false, fmt.Errorf("host conflict Linux route has a non-IPv4 destination")
		}
		destinationAddress = netip.AddrFrom4([4]byte(destinationPayload))
	}
	destination := netip.PrefixFrom(destinationAddress, destinationBits).Masked()
	if destination.Addr() != destinationAddress {
		return RouteFact{}, false, false, fmt.Errorf("host conflict Linux route destination is not prefix-canonical")
	}
	if !destination.Contains(candidate) {
		return RouteFact{}, false, true, nil
	}
	if payload[2] != 0 || payload[3] != 0 || binary.NativeEndian.Uint32(payload[8:12]) != 0 {
		return RouteFact{}, true, false, nil
	}

	routeType := payload[7]
	if routeType != unix.RTN_LOCAL && routeType != unix.RTN_UNICAST {
		return RouteFact{}, true, false, nil
	}
	if !linuxRouteAttributesRepresentable(attributes) {
		return RouteFact{}, true, false, nil
	}
	table, err := linuxRouteTable(payload[4], attributes)
	if err != nil {
		return RouteFact{}, false, false, err
	}
	if err := validateLinuxOptionalIPv4Attribute(attributes, unix.RTA_PREFSRC); err != nil {
		return RouteFact{}, false, false, err
	}
	if err := validateLinuxOptionalUint32Attribute(attributes, unix.RTA_PRIORITY); err != nil {
		return RouteFact{}, false, false, err
	}
	interfacePayload, interfacePresent, err := oneLinuxAttribute(attributes, unix.RTA_OIF)
	if err != nil {
		return RouteFact{}, false, false, err
	}
	if !interfacePresent || len(interfacePayload) != 4 {
		return RouteFact{}, true, false, nil
	}
	interfaceIndex := binary.NativeEndian.Uint32(interfacePayload)
	linuxInterface, exists := interfaces.byIndex[interfaceIndex]
	if !exists {
		return RouteFact{}, true, false, nil
	}
	gatewayPayload, gatewayPresent, err := oneLinuxAttribute(attributes, unix.RTA_GATEWAY)
	if err != nil {
		return RouteFact{}, false, false, err
	}
	gateway := netip.Addr{}
	if gatewayPresent {
		if len(gatewayPayload) != 4 {
			return RouteFact{}, true, false, nil
		}
		gateway = netip.AddrFrom4([4]byte(gatewayPayload))
		if gateway.IsUnspecified() {
			return RouteFact{}, true, false, nil
		}
	}
	fact := RouteFact{
		Destination:    destination,
		Interface:      linuxInterface.identity,
		NativeLoopback: linuxInterface.identity == interfaces.loopback.Interface,
		Gateway:        gateway,
		Normalization:  RouteNormalizationDirect,
	}
	if fact.Destination == linuxOrdinaryLoopbackPrefix && fact.NativeLoopback && !fact.Gateway.IsValid() {
		tableValid := table == unix.RT_TABLE_LOCAL || selectedLookup && table == unix.RT_TABLE_MAIN
		if routeType != unix.RTN_LOCAL || !tableValid || payload[5] != unix.RTPROT_KERNEL || payload[6] != unix.RT_SCOPE_HOST {
			return RouteFact{}, true, false, nil
		}
	}
	return fact, true, true, nil
}

// linuxRouteAttributesRepresentable admits only fields whose route-selection meaning is preserved or validated.
func linuxRouteAttributesRepresentable(attributes map[uint16][]linuxNetlinkAttribute) bool {
	for attributeType := range attributes {
		switch attributeType {
		case unix.RTA_DST, unix.RTA_OIF, unix.RTA_GATEWAY, unix.RTA_PRIORITY, unix.RTA_PREFSRC, unix.RTA_TABLE:
			continue
		default:
			return false
		}
	}
	return true
}

// linuxRouteTable resolves the extended table attribute without accepting contradictory encodings.
func linuxRouteTable(headerTable uint8, attributes map[uint16][]linuxNetlinkAttribute) (uint32, error) {
	tablePayload, tablePresent, err := oneLinuxAttribute(attributes, unix.RTA_TABLE)
	if err != nil {
		return 0, err
	}
	table := uint32(headerTable)
	if !tablePresent {
		return table, nil
	}
	if len(tablePayload) != 4 {
		return 0, fmt.Errorf("host conflict Linux route has an invalid extended table")
	}
	extended := binary.NativeEndian.Uint32(tablePayload)
	if table != unix.RT_TABLE_UNSPEC && table != extended {
		return 0, fmt.Errorf("host conflict Linux route has contradictory table identifiers")
	}
	return extended, nil
}

// validateLinuxOptionalIPv4Attribute rejects duplicate or non-IPv4 route preferences.
func validateLinuxOptionalIPv4Attribute(attributes map[uint16][]linuxNetlinkAttribute, attributeType uint16) error {
	payload, present, err := oneLinuxAttribute(attributes, attributeType)
	if err != nil {
		return err
	}
	if present && len(payload) != 4 {
		return fmt.Errorf("host conflict Linux route attribute %d is not IPv4", attributeType)
	}
	return nil
}

// validateLinuxOptionalUint32Attribute rejects duplicate or non-native scalar route metadata.
func validateLinuxOptionalUint32Attribute(attributes map[uint16][]linuxNetlinkAttribute, attributeType uint16) error {
	payload, present, err := oneLinuxAttribute(attributes, attributeType)
	if err != nil {
		return err
	}
	if present && len(payload) != 4 {
		return fmt.Errorf("host conflict Linux route attribute %d is not uint32", attributeType)
	}
	return nil
}

// oneLinuxAttribute rejects duplicate authority-bearing attributes instead of accepting an arbitrary value.
func oneLinuxAttribute(attributes map[uint16][]linuxNetlinkAttribute, attributeType uint16) ([]byte, bool, error) {
	values := attributes[attributeType]
	if len(values) == 0 {
		return nil, false, nil
	}
	if len(values) != 1 {
		return nil, false, fmt.Errorf("host conflict Linux netlink message repeats attribute %d", attributeType)
	}
	if values[0].flags != 0 {
		return nil, false, fmt.Errorf("host conflict Linux netlink attribute %d has unsupported encoding flags", attributeType)
	}
	return values[0].payload, true, nil
}

// routeFactCount preserves duplicate kernel routes because multiplicity is part of conflict evidence.
func routeFactCount(facts []RouteFact, expected RouteFact) int {
	count := 0
	for _, fact := range facts {
		if fact == expected {
			count++
		}
	}
	return count
}
