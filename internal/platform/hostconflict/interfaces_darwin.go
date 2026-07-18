//go:build darwin

package hostconflict

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

const darwinRequiredLoopbackFlags = unix.IFF_UP | unix.IFF_LOOPBACK | unix.IFF_RUNNING

// darwinInterfaceSnapshot retains every stable interface identity needed to interpret route facts.
type darwinInterfaceSnapshot struct {
	loopback LoopbackIdentity
	byIndex  map[uint32]InterfaceIdentity
}

// darwinInterfaceType resolves the kernel link type carried in x/net/route's platform metrics.
type darwinInterfaceType func(*route.InterfaceMessage) (int, error)

// observeDarwinInterfaces reads one bounded interface RIB without using name-based network APIs.
func observeDarwinInterfaces(ctx context.Context) (darwinInterfaceSnapshot, error) {
	return readDarwinInterfacesWith(ctx, route.FetchRIB, route.ParseRIB, darwinRouteInterfaceType)
}

// readDarwinInterfacesWith keeps raw RIB acquisition and parsing deterministic in codec tests.
func readDarwinInterfacesWith(
	ctx context.Context,
	fetch func(int, route.RIBType, int) ([]byte, error),
	parse func(route.RIBType, []byte) ([]route.Message, error),
	interfaceType darwinInterfaceType,
) (darwinInterfaceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return darwinInterfaceSnapshot{}, err
	}
	raw, err := fetch(unix.AF_UNSPEC, route.RIBTypeInterface, 0)
	if err != nil {
		return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin fetch interface RIB: %w", err)
	}
	allowedTypes := map[uint8]struct{}{
		unix.RTM_IFINFO:    {},
		unix.RTM_IFINFO2:   {},
		unix.RTM_NEWADDR:   {},
		unix.RTM_NEWMADDR:  {},
		unix.RTM_NEWMADDR2: {},
	}
	messageCount, err := validateDarwinRIBFrames(raw, maximumDarwinInterfaceRIB, allowedTypes)
	if err != nil {
		return darwinInterfaceSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return darwinInterfaceSnapshot{}, err
	}
	messages, err := parse(route.RIBTypeInterface, raw)
	if err != nil {
		return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin parse interface RIB: %w", err)
	}
	if len(messages) != messageCount {
		return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface parser omitted %d messages", messageCount-len(messages))
	}
	if err := ctx.Err(); err != nil {
		return darwinInterfaceSnapshot{}, err
	}
	return darwinInterfacesFromMessages(messages, interfaceType)
}

// darwinInterfacesFromMessages selects exactly one native loopback and rejects ambiguous interface identities.
func darwinInterfacesFromMessages(messages []route.Message, interfaceType darwinInterfaceType) (darwinInterfaceSnapshot, error) {
	if len(messages) > maximumDarwinRIBMessages {
		return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface facts exceed %d messages", maximumDarwinRIBMessages)
	}
	snapshot := darwinInterfaceSnapshot{byIndex: make(map[uint32]InterfaceIdentity)}
	names := make(map[string]struct{})
	referencedIndexes := make([]uint32, 0)
	loopbacks := 0
	for _, rawMessage := range messages {
		switch message := rawMessage.(type) {
		case *route.InterfaceMessage:
			if message == nil {
				return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface RIB contains a nil interface message")
			}
			if message.Type != unix.RTM_IFINFO && message.Type != unix.RTM_IFINFO2 {
				return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface RIB contains type %d as interface facts", message.Type)
			}
			identity, native, err := darwinInterfaceIdentity(message, interfaceType)
			if err != nil {
				return darwinInterfaceSnapshot{}, err
			}
			if _, exists := snapshot.byIndex[identity.Index]; exists {
				return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface index %d is duplicated", identity.Index)
			}
			if _, exists := names[identity.Name]; exists {
				return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface name %q is duplicated", identity.Name)
			}
			snapshot.byIndex[identity.Index] = identity
			names[identity.Name] = struct{}{}
			if native {
				loopbacks++
				snapshot.loopback = LoopbackIdentity{Interface: identity, Kind: LoopbackKindMacOSNative}
			}
		case *route.InterfaceAddrMessage:
			if message == nil || message.Type != unix.RTM_NEWADDR || message.Index <= 0 || uint64(message.Index) > uint64(^uint32(0)) {
				return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface RIB contains invalid address facts")
			}
			referencedIndexes = append(referencedIndexes, uint32(message.Index))
		case *route.InterfaceMulticastAddrMessage:
			if message == nil || (message.Type != unix.RTM_NEWMADDR && message.Type != unix.RTM_NEWMADDR2) || message.Index <= 0 || uint64(message.Index) > uint64(^uint32(0)) {
				return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface RIB contains invalid multicast facts")
			}
			referencedIndexes = append(referencedIndexes, uint32(message.Index))
		default:
			return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface RIB contains unexpected message %T", rawMessage)
		}
	}
	for _, index := range referencedIndexes {
		if _, exists := snapshot.byIndex[index]; !exists {
			return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin interface RIB references unknown index %d", index)
		}
	}
	if loopbacks != 1 {
		return darwinInterfaceSnapshot{}, fmt.Errorf("host conflict Darwin native loopback count is %d, want 1", loopbacks)
	}
	return snapshot, nil
}

// darwinInterfaceIdentity preserves index as authority while retaining the bounded kernel name as evidence.
func darwinInterfaceIdentity(message *route.InterfaceMessage, interfaceType darwinInterfaceType) (InterfaceIdentity, bool, error) {
	if message.Index <= 0 || uint64(message.Index) > uint64(^uint32(0)) {
		return InterfaceIdentity{}, false, fmt.Errorf("host conflict Darwin interface index %d is invalid", message.Index)
	}
	if message.Name == "" || len(message.Name) >= unix.IFNAMSIZ || strings.ContainsRune(message.Name, '\x00') {
		return InterfaceIdentity{}, false, fmt.Errorf("host conflict Darwin interface name %q cannot identify a native interface", message.Name)
	}
	identity := InterfaceIdentity{Name: message.Name, Index: uint32(message.Index)}
	if err := identity.validateForPlatform(PlatformMacOS); err != nil {
		return InterfaceIdentity{}, false, err
	}
	kind, err := interfaceType(message)
	if err != nil {
		return InterfaceIdentity{}, false, fmt.Errorf("host conflict Darwin resolve interface type for %q: %w", message.Name, err)
	}
	native := message.Flags&darwinRequiredLoopbackFlags == darwinRequiredLoopbackFlags && kind == unix.IFT_LOOP
	return identity, native, nil
}

// darwinRouteInterfaceType extracts the one metrics record supplied by the Darwin route parser.
func darwinRouteInterfaceType(message *route.InterfaceMessage) (int, error) {
	metrics := message.Sys()
	if len(metrics) != 1 {
		return 0, fmt.Errorf("interface metrics count is %d", len(metrics))
	}
	interfaceMetrics, ok := metrics[0].(*route.InterfaceMetrics)
	if !ok || interfaceMetrics == nil {
		return 0, fmt.Errorf("interface metrics have unexpected type %T", metrics[0])
	}
	return interfaceMetrics.Type, nil
}
