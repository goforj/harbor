//go:build linux

package loopback

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/goforj/harbor/internal/platform/linuxnetlink"
	"golang.org/x/sys/unix"
)

const linuxInfiniteLifetime = ^uint32(0)

// linuxRouteClient exposes only the sequenced route transactions needed by loopback ownership.
type linuxRouteClient interface {
	Exchange(context.Context, uint16, uint16, []byte, linuxnetlink.Completion) (linuxnetlink.Reply, error)
	Close() error
}

// linuxOpenRoute creates one isolated kernel port for a bounded observation or mutation.
type linuxOpenRoute func() (linuxRouteClient, error)

// platformBackend implements Linux loopback effects without child processes or network clients.
type platformBackend struct {
	openRoute linuxOpenRoute
}

// linuxLink contains the bounded identity needed to select and revalidate a native loopback.
type linuxLink struct {
	fact InterfaceFact
}

// linuxAddress contains one exact local-address match and its kernel attributes.
type linuxAddress struct {
	fact              AssignmentFact
	scope             uint8
	flags             uint32
	validLifetime     uint32
	preferredLifetime uint32
	cacheInfoPresent  bool
	addressMatches    bool
	label             string
	additional        string
}

// newPlatformBackend creates the Linux adapter without acquiring privilege.
func newPlatformBackend() backend {
	return &platformBackend{openRoute: func() (linuxRouteClient, error) {
		return linuxnetlink.OpenRoute()
	}}
}

// interfaces returns a complete link snapshot with kernel-native loopback evidence.
func (backend *platformBackend) interfaces(ctx context.Context) ([]InterfaceFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := backend.openRoute()
	if err != nil {
		return nil, fmt.Errorf("open Linux route observation: %w", err)
	}
	links, observationErr := observeLinuxLinks(ctx, client)
	closeErr := client.Close()
	if observationErr != nil || closeErr != nil {
		return nil, errors.Join(observationErr, closeErr)
	}
	facts := make([]InterfaceFact, len(links))
	for index, link := range links {
		facts[index] = link.fact
	}
	return facts, nil
}

// assignments returns every exact local-address match from a complete native address dump.
func (backend *platformBackend) assignments(ctx context.Context, address netip.Addr) ([]AssignmentFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := backend.openRoute()
	if err != nil {
		return nil, fmt.Errorf("open Linux address observation: %w", err)
	}
	links, err := observeLinuxLinks(ctx, client)
	if err != nil {
		return nil, errors.Join(err, client.Close())
	}
	addresses, observationErr := observeLinuxAddresses(ctx, client, address, links)
	closeErr := client.Close()
	if observationErr != nil || closeErr != nil {
		return nil, errors.Join(observationErr, closeErr)
	}
	facts := make([]AssignmentFact, len(addresses))
	for index, assignment := range addresses {
		fact := assignment.fact
		fact.Linux = &LinuxAssignmentFact{
			Scope:                      linuxAddressScope(assignment.scope),
			Flags:                      assignment.flags,
			Label:                      assignment.label,
			AddressMatchesLocal:        assignment.addressMatches,
			CacheInfoPresent:           assignment.cacheInfoPresent,
			ValidLifetimeSeconds:       assignment.validLifetime,
			PreferredLifetimeSeconds:   assignment.preferredLifetime,
			AdditionalAttributesSHA256: assignment.additional,
		}
		facts[index] = fact
	}
	return facts, nil
}

// ensure creates one exact /32 through an exclusive, acknowledged RTM_NEWADDR request.
func (backend *platformBackend) ensure(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateLinuxMutation(interf, prefix); err != nil {
		return err
	}
	client, err := backend.openRoute()
	if err != nil {
		return fmt.Errorf("open Linux address mutation: %w", err)
	}
	if err := revalidateLinuxLoopback(ctx, client, interf); err != nil {
		return errors.Join(err, client.Close())
	}
	payload, err := marshalLinuxAddressMutation(interf, prefix, true)
	if err != nil {
		return errors.Join(err, client.Close())
	}
	_, mutationErr := client.Exchange(ctx, unix.RTM_NEWADDR, unix.NLM_F_CREATE|unix.NLM_F_EXCL, payload, linuxnetlink.CompletionAck)
	closeErr := client.Close()
	if mutationErr != nil {
		return errors.Join(fmt.Errorf("create Linux loopback address: %w", mutationErr), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// release removes only the observed interface-index and exact /32 tuple through RTM_DELADDR.
func (backend *platformBackend) release(ctx context.Context, interf InterfaceFact, prefix netip.Prefix) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateLinuxMutation(interf, prefix); err != nil {
		return err
	}
	client, err := backend.openRoute()
	if err != nil {
		return fmt.Errorf("open Linux address mutation: %w", err)
	}
	if err := revalidateLinuxLoopback(ctx, client, interf); err != nil {
		return errors.Join(err, client.Close())
	}
	if err := revalidateLinuxReleaseAddress(ctx, client, interf, prefix.Addr()); err != nil {
		return errors.Join(err, client.Close())
	}
	payload, err := marshalLinuxAddressMutation(interf, prefix, false)
	if err != nil {
		return errors.Join(err, client.Close())
	}
	_, mutationErr := client.Exchange(ctx, unix.RTM_DELADDR, 0, payload, linuxnetlink.CompletionAck)
	if errors.Is(mutationErr, unix.ENOENT) || errors.Is(mutationErr, unix.EADDRNOTAVAIL) {
		// A concurrent exact delete is decided by Adapter's mandatory fresh observation.
		mutationErr = nil
	}
	closeErr := client.Close()
	if mutationErr != nil {
		return errors.Join(fmt.Errorf("delete Linux loopback address: %w", mutationErr), closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// observeLinuxLinks requires a complete, unique RTM_GETLINK snapshot before returning authority facts.
func observeLinuxLinks(ctx context.Context, client linuxRouteClient) ([]linuxLink, error) {
	request := make([]byte, unix.SizeofIfInfomsg)
	request[0] = unix.AF_UNSPEC
	reply, err := client.Exchange(ctx, unix.RTM_GETLINK, unix.NLM_F_DUMP, request, linuxnetlink.CompletionDump)
	if err != nil {
		return nil, fmt.Errorf("read Linux interface table: %w", err)
	}
	if len(reply.Messages) > maximumInterfaceFacts {
		return nil, fmt.Errorf("interface count exceeds limit %d", maximumInterfaceFacts)
	}
	links := make([]linuxLink, 0, len(reply.Messages))
	seenIndexes := make(map[int]struct{}, len(reply.Messages))
	seenNames := make(map[string]struct{}, len(reply.Messages))
	for _, message := range reply.Messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if message.Type != unix.RTM_NEWLINK {
			return nil, fmt.Errorf("Linux link dump returned message type %d", message.Type)
		}
		link, err := parseLinuxLink(message.Payload)
		if err != nil {
			return nil, err
		}
		if _, exists := seenIndexes[link.fact.Index]; exists {
			return nil, fmt.Errorf("Linux link dump repeats interface index %d", link.fact.Index)
		}
		if _, exists := seenNames[link.fact.Name]; exists {
			return nil, fmt.Errorf("Linux link dump repeats interface name %q", link.fact.Name)
		}
		seenIndexes[link.fact.Index] = struct{}{}
		seenNames[link.fact.Name] = struct{}{}
		links = append(links, link)
	}
	return links, nil
}

// parseLinuxLink decodes one fixed ifinfomsg and its single canonical interface name.
func parseLinuxLink(payload []byte) (linuxLink, error) {
	if len(payload) < unix.SizeofIfInfomsg {
		return linuxLink{}, fmt.Errorf("Linux link message is truncated")
	}
	if payload[0] != unix.AF_UNSPEC {
		return linuxLink{}, fmt.Errorf("Linux link dump returned family %d", payload[0])
	}
	index := int32(binary.NativeEndian.Uint32(payload[4:8]))
	if index <= 0 {
		return linuxLink{}, fmt.Errorf("Linux link message has invalid interface index %d", index)
	}
	attributes, err := linuxnetlink.ParseAttributes(payload[unix.SizeofIfInfomsg:])
	if err != nil {
		return linuxLink{}, err
	}
	namePayload, present, err := linuxnetlink.OneAttribute(attributes, unix.IFLA_IFNAME)
	if err != nil {
		return linuxLink{}, err
	}
	if !present || len(namePayload) < 2 || namePayload[len(namePayload)-1] != 0 {
		return linuxLink{}, fmt.Errorf("Linux link message is missing a terminated interface name")
	}
	if len(namePayload)-1 > unix.IFNAMSIZ-1 {
		return linuxLink{}, fmt.Errorf("Linux interface name exceeds %d bytes", unix.IFNAMSIZ-1)
	}
	for _, value := range namePayload[:len(namePayload)-1] {
		if value == 0 {
			return linuxLink{}, fmt.Errorf("Linux link message contains an embedded interface-name terminator")
		}
	}
	flags := binary.NativeEndian.Uint32(payload[8:12])
	hardware := binary.NativeEndian.Uint16(payload[2:4])
	isNative := flags&(unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK) == unix.IFF_UP|unix.IFF_RUNNING|unix.IFF_LOOPBACK && hardware == unix.ARPHRD_LOOPBACK
	fact := InterfaceFact{Name: string(namePayload[:len(namePayload)-1]), Index: int(index), NativeLoopback: isNative}
	if isNative {
		fact.Kind = InterfaceKindLinuxNative
	}
	return linuxLink{fact: fact}, nil
}

// observeLinuxAddresses returns every matching local IPv4 address with its exact kernel attributes.
func observeLinuxAddresses(ctx context.Context, client linuxRouteClient, target netip.Addr, links []linuxLink) ([]linuxAddress, error) {
	request := make([]byte, unix.SizeofIfAddrmsg)
	request[0] = unix.AF_INET
	reply, err := client.Exchange(ctx, unix.RTM_GETADDR, unix.NLM_F_DUMP, request, linuxnetlink.CompletionDump)
	if err != nil {
		return nil, fmt.Errorf("read Linux address table: %w", err)
	}
	names := make(map[int]string, len(links))
	for _, link := range links {
		names[link.fact.Index] = link.fact.Name
	}
	addresses := make([]linuxAddress, 0, 1)
	for _, message := range reply.Messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if message.Type != unix.RTM_NEWADDR {
			return nil, fmt.Errorf("Linux address dump returned message type %d", message.Type)
		}
		assignment, matches, err := parseLinuxAddress(message.Payload, target, names)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}
		addresses = append(addresses, assignment)
		if len(addresses) > maximumAssignmentFacts {
			return nil, fmt.Errorf("assignment count exceeds limit %d", maximumAssignmentFacts)
		}
	}
	return addresses, nil
}

// parseLinuxAddress decodes one local IPv4 assignment without hiding conflicting attributes.
func parseLinuxAddress(payload []byte, target netip.Addr, names map[int]string) (linuxAddress, bool, error) {
	if len(payload) < unix.SizeofIfAddrmsg {
		return linuxAddress{}, false, fmt.Errorf("Linux address message is truncated")
	}
	if payload[0] != unix.AF_INET {
		return linuxAddress{}, false, fmt.Errorf("Linux IPv4 address dump returned family %d", payload[0])
	}
	interfaceIndex := int(binary.NativeEndian.Uint32(payload[4:8]))
	if interfaceIndex <= 0 {
		return linuxAddress{}, false, fmt.Errorf("Linux address message has invalid interface index %d", interfaceIndex)
	}
	attributes, err := linuxnetlink.ParseAttributes(payload[unix.SizeofIfAddrmsg:])
	if err != nil {
		return linuxAddress{}, false, err
	}
	localPayload, localPresent, err := linuxnetlink.OneAttribute(attributes, unix.IFA_LOCAL)
	if err != nil {
		return linuxAddress{}, false, err
	}
	addressPayload, addressPresent, err := linuxnetlink.OneAttribute(attributes, unix.IFA_ADDRESS)
	if err != nil {
		return linuxAddress{}, false, err
	}
	local, localValid := parseLinuxIPv4Attribute(localPayload, localPresent)
	if localPresent && !localValid {
		return linuxAddress{}, false, fmt.Errorf("Linux address message has an invalid local IPv4 address")
	}
	address, addressValid := parseLinuxIPv4Attribute(addressPayload, addressPresent)
	if addressPresent && !addressValid {
		return linuxAddress{}, false, fmt.Errorf("Linux address message has an invalid peer IPv4 address")
	}
	if !localPresent && !addressPresent {
		return linuxAddress{}, false, fmt.Errorf("Linux address message is missing local and peer IPv4 addresses")
	}
	localMatches := localPresent && local == target
	addressMatches := addressPresent && address == target
	if !localMatches && !addressMatches {
		return linuxAddress{}, false, nil
	}
	interfaceName, exists := names[interfaceIndex]
	if !exists {
		return linuxAddress{}, false, fmt.Errorf("Linux address references unobserved interface %d", interfaceIndex)
	}
	labelPayload, labelPresent, err := linuxnetlink.OneAttribute(attributes, unix.IFA_LABEL)
	if err != nil {
		return linuxAddress{}, false, err
	}
	label, err := parseLinuxAddressLabel(labelPayload, labelPresent)
	if err != nil {
		return linuxAddress{}, false, err
	}

	flags := uint32(payload[2])
	flagPayload, flagsPresent, err := linuxnetlink.OneAttribute(attributes, unix.IFA_FLAGS)
	if err != nil {
		return linuxAddress{}, false, err
	}
	if flagsPresent {
		if len(flagPayload) != 4 {
			return linuxAddress{}, false, fmt.Errorf("Linux address flags have length %d", len(flagPayload))
		}
		flags = binary.NativeEndian.Uint32(flagPayload)
	}
	cachePayload, cachePresent, err := linuxnetlink.OneAttribute(attributes, unix.IFA_CACHEINFO)
	if err != nil {
		return linuxAddress{}, false, err
	}
	validLifetime := uint32(0)
	preferredLifetime := uint32(0)
	if cachePresent {
		if len(cachePayload) != 16 {
			return linuxAddress{}, false, fmt.Errorf("Linux address cache info has length %d", len(cachePayload))
		}
		preferredLifetime = binary.NativeEndian.Uint32(cachePayload[0:4])
		validLifetime = binary.NativeEndian.Uint32(cachePayload[4:8])
	}
	additional := linuxAdditionalAttributesSHA256(attributes)
	return linuxAddress{
		fact: AssignmentFact{
			Address:        target,
			PrefixLength:   int(payload[1]),
			InterfaceName:  interfaceName,
			InterfaceIndex: interfaceIndex,
		},
		scope:             payload[3],
		flags:             flags,
		validLifetime:     validLifetime,
		preferredLifetime: preferredLifetime,
		cacheInfoPresent:  cachePresent,
		addressMatches:    localMatches && addressMatches,
		label:             label,
		additional:        additional,
	}, true, nil
}

// parseLinuxIPv4Attribute recognizes only an exact four-byte IPv4 payload.
func parseLinuxIPv4Attribute(payload []byte, present bool) (netip.Addr, bool) {
	if !present || len(payload) != 4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte(payload)), true
}

// parseLinuxAddressLabel retains valid aliases while keeping malformed kernel strings out of facts.
func parseLinuxAddressLabel(payload []byte, present bool) (string, error) {
	if !present {
		return "", nil
	}
	if len(payload) < 2 || len(payload) > unix.IFNAMSIZ || payload[len(payload)-1] != 0 {
		return "", fmt.Errorf("Linux address label is not a bounded terminated name")
	}
	for _, value := range payload[:len(payload)-1] {
		if value == 0 {
			return "", fmt.Errorf("Linux address label contains an embedded terminator")
		}
	}
	return string(payload[:len(payload)-1]), nil
}

// linuxAdditionalAttributesSHA256 binds every attribute outside Harbor's exact address allowlist.
func linuxAdditionalAttributesSHA256(attributes map[uint16][]linuxnetlink.Attribute) string {
	types := make([]int, 0, len(attributes))
	for attributeType := range attributes {
		switch attributeType {
		case unix.IFA_ADDRESS, unix.IFA_LOCAL, unix.IFA_LABEL, unix.IFA_CACHEINFO, unix.IFA_FLAGS:
			continue
		default:
			types = append(types, int(attributeType))
		}
	}
	if len(types) == 0 {
		return ""
	}
	sort.Ints(types)
	payload := []byte("goforj.harbor.linux-address-additional-attributes.v1\x00")
	for _, rawType := range types {
		attributeType := uint16(rawType)
		for _, attribute := range attributes[attributeType] {
			payload = binary.AppendUvarint(payload, uint64(attributeType))
			payload = binary.AppendUvarint(payload, uint64(attribute.Flags))
			payload = binary.AppendUvarint(payload, uint64(len(attribute.Payload)))
			payload = append(payload, attribute.Payload...)
		}
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// linuxAddressScope converts every documented routing scope into a bounded fact value.
func linuxAddressScope(scope uint8) LinuxAddressScope {
	switch scope {
	case unix.RT_SCOPE_UNIVERSE:
		return LinuxAddressScopeUniverse
	case unix.RT_SCOPE_SITE:
		return LinuxAddressScopeSite
	case unix.RT_SCOPE_LINK:
		return LinuxAddressScopeLink
	case unix.RT_SCOPE_HOST:
		return LinuxAddressScopeHost
	case unix.RT_SCOPE_NOWHERE:
		return LinuxAddressScopeNowhere
	default:
		return LinuxAddressScopeUnknown
	}
}

// validateLinuxMutation confines the backend to a previously verified native loopback and exact /32.
func validateLinuxMutation(interf InterfaceFact, prefix netip.Prefix) error {
	if !interf.NativeLoopback ||
		interf.Kind != InterfaceKindLinuxNative ||
		interf.Index <= 0 ||
		interf.Name == "" ||
		len(interf.Name) > maximumLinuxLabel ||
		strings.ContainsRune(interf.Name, 0) {
		return fmt.Errorf("observed Linux loopback identity is incomplete")
	}
	if !prefix.IsValid() || !prefix.Addr().Is4() || !prefix.Addr().IsLoopback() || prefix.Bits() != 32 || prefix != prefix.Masked() {
		return fmt.Errorf("Linux loopback mutation requires a canonical IPv4 /32")
	}
	return nil
}

// revalidateLinuxLoopback binds the numeric mutation target to the same native identity immediately before use.
func revalidateLinuxLoopback(ctx context.Context, client linuxRouteClient, expected InterfaceFact) error {
	request := make([]byte, unix.SizeofIfInfomsg)
	request[0] = unix.AF_UNSPEC
	binary.NativeEndian.PutUint32(request[4:8], uint32(expected.Index))
	reply, err := client.Exchange(ctx, unix.RTM_GETLINK, 0, request, linuxnetlink.CompletionData)
	if err != nil {
		return fmt.Errorf("revalidate Linux loopback interface: %w", err)
	}
	if len(reply.Messages) != 1 || reply.Messages[0].Type != unix.RTM_NEWLINK {
		return fmt.Errorf("revalidate Linux loopback returned %d interface records", len(reply.Messages))
	}
	observed, err := parseLinuxLink(reply.Messages[0].Payload)
	if err != nil {
		return fmt.Errorf("revalidate Linux loopback interface: %w", err)
	}
	if observed.fact != expected {
		return fmt.Errorf("observed Linux loopback identity changed")
	}
	return nil
}

// revalidateLinuxReleaseAddress narrows deletion to the same exact assignment admitted by the adapter.
func revalidateLinuxReleaseAddress(ctx context.Context, client linuxRouteClient, expected InterfaceFact, target netip.Addr) error {
	addresses, err := observeLinuxAddresses(ctx, client, target, []linuxLink{{fact: expected}})
	if err != nil {
		return fmt.Errorf("revalidate Linux loopback address: %w", err)
	}
	if len(addresses) != 1 {
		return fmt.Errorf("revalidate Linux loopback address returned %d matching records", len(addresses))
	}
	assignment := addresses[0]
	if assignment.fact.InterfaceIndex != expected.Index ||
		assignment.fact.InterfaceName != expected.Name ||
		assignment.fact.PrefixLength != 32 ||
		assignment.scope != unix.RT_SCOPE_HOST ||
		assignment.flags != unix.IFA_F_PERMANENT ||
		assignment.label != expected.Name ||
		!assignment.addressMatches ||
		!assignment.cacheInfoPresent ||
		assignment.validLifetime != linuxInfiniteLifetime ||
		assignment.preferredLifetime != linuxInfiniteLifetime ||
		assignment.additional != "" {
		return fmt.Errorf("observed Linux loopback assignment changed before deletion")
	}
	return nil
}

// marshalLinuxAddressMutation encodes the exact index, /32, host scope, and infinite lifetime tuple.
func marshalLinuxAddressMutation(interf InterfaceFact, prefix netip.Prefix, includeLifetime bool) ([]byte, error) {
	payload := make([]byte, unix.SizeofIfAddrmsg)
	payload[0] = unix.AF_INET
	payload[1] = uint8(prefix.Bits())
	payload[3] = unix.RT_SCOPE_HOST
	binary.NativeEndian.PutUint32(payload[4:8], uint32(interf.Index))
	address := prefix.Addr().As4()
	var err error
	payload, err = linuxnetlink.MarshalAttribute(payload, unix.IFA_LOCAL, address[:])
	if err != nil {
		return nil, err
	}
	payload, err = linuxnetlink.MarshalAttribute(payload, unix.IFA_ADDRESS, address[:])
	if err != nil {
		return nil, err
	}
	payload, err = linuxnetlink.MarshalAttribute(payload, unix.IFA_LABEL, append([]byte(interf.Name), 0))
	if err != nil {
		return nil, err
	}
	if includeLifetime {
		cacheInfo := make([]byte, 16)
		binary.NativeEndian.PutUint32(cacheInfo[0:4], linuxInfiniteLifetime)
		binary.NativeEndian.PutUint32(cacheInfo[4:8], linuxInfiniteLifetime)
		payload, err = linuxnetlink.MarshalAttribute(payload, unix.IFA_CACHEINFO, cacheInfo)
		if err != nil {
			return nil, err
		}
	}
	return payload, nil
}
