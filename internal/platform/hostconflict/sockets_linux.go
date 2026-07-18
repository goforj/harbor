//go:build linux

package hostconflict

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/goforj/harbor/internal/platform/linuxnetlink"
	"golang.org/x/sys/unix"
)

const (
	linuxInetDiagMessageBytes = 72
	linuxInetDiagRequestBytes = 56
	linuxInetDiagSKV6Only     = 11
	linuxTCPListenState       = 10
)

// observeLinuxSockets enumerates only requested protocols while filtering relevant addresses and ports locally.
func observeLinuxSockets(ctx context.Context, client linuxNetlinkExchanger, request Request) (SocketSnapshot, error) {
	snapshot := SocketSnapshot{Complete: true}
	protocols := requestedLinuxSocketProtocols(request)
	for _, protocol := range protocols {
		for _, family := range []uint8{unix.AF_INET, unix.AF_INET6} {
			reply, err := client.Exchange(ctx, unix.SOCK_DIAG_BY_FAMILY, unix.NLM_F_DUMP, marshalLinuxInetDiagRequest(family, protocol), linuxnetlink.CompletionDump)
			if err != nil {
				return SocketSnapshot{}, err
			}
			for _, message := range reply.Messages {
				if message.Type != unix.SOCK_DIAG_BY_FAMILY {
					return SocketSnapshot{}, fmt.Errorf("host conflict Linux socket dump returned message type %d", message.Type)
				}
				fact, relevant, complete, err := parseLinuxInetDiagMessage(message.Payload, family, protocol, request)
				if err != nil {
					return SocketSnapshot{}, err
				}
				if !complete {
					snapshot.Complete = false
				}
				if !relevant {
					continue
				}
				if len(snapshot.Endpoints) >= maximumSocketFacts {
					snapshot.Complete = false
					snapshot.Truncated = true
					continue
				}
				snapshot.Endpoints = append(snapshot.Endpoints, fact)
			}
		}
	}
	if snapshot.Truncated {
		snapshot.Complete = false
	}
	return snapshot, nil
}

// requestedLinuxSocketProtocols returns each required inet_diag protocol once in stable order.
func requestedLinuxSocketProtocols(request Request) []uint8 {
	tcp := false
	udp := false
	for _, requirement := range request.Requirements() {
		switch requirement.Transport {
		case TransportTCP4:
			tcp = true
		case TransportUDP4:
			udp = true
		}
	}
	protocols := make([]uint8, 0, 2)
	if tcp {
		protocols = append(protocols, unix.IPPROTO_TCP)
	}
	if udp {
		protocols = append(protocols, unix.IPPROTO_UDP)
	}
	return protocols
}

// marshalLinuxInetDiagRequest asks for TCP listeners or every UDP endpoint without kernel bytecode filters.
func marshalLinuxInetDiagRequest(family uint8, protocol uint8) []byte {
	payload := make([]byte, linuxInetDiagRequestBytes)
	payload[0] = family
	payload[1] = protocol
	states := uint32(^uint32(0))
	if protocol == unix.IPPROTO_TCP {
		states = uint32(1) << linuxTCPListenState
	}
	binary.NativeEndian.PutUint32(payload[4:8], states)
	binary.NativeEndian.PutUint32(payload[48:52], ^uint32(0))
	binary.NativeEndian.PutUint32(payload[52:56], ^uint32(0))
	return payload
}

// parseLinuxInetDiagMessage converts only endpoints that can consume an IPv4 candidate capability.
func parseLinuxInetDiagMessage(payload []byte, expectedFamily uint8, expectedProtocol uint8, request Request) (SocketFact, bool, bool, error) {
	if len(payload) < linuxInetDiagMessageBytes {
		return SocketFact{}, false, false, fmt.Errorf("host conflict Linux inet_diag message is truncated")
	}
	family := payload[0]
	if family != expectedFamily {
		return SocketFact{}, false, false, fmt.Errorf("host conflict Linux inet_diag family %d does not match request %d", family, expectedFamily)
	}
	if expectedProtocol == unix.IPPROTO_TCP && payload[1] != linuxTCPListenState {
		return SocketFact{}, false, false, fmt.Errorf("host conflict Linux TCP diagnostic returned non-listening state %d", payload[1])
	}
	attributes, err := linuxnetlink.ParseAttributes(payload[linuxInetDiagMessageBytes:])
	if err != nil {
		return SocketFact{}, false, false, err
	}
	v6OnlyPayload, v6OnlyPresent, err := linuxnetlink.OneAttribute(attributes, linuxInetDiagSKV6Only)
	if err != nil {
		return SocketFact{}, false, false, err
	}
	if v6OnlyPresent && (len(v6OnlyPayload) != 1 || v6OnlyPayload[0] > 1) {
		return SocketFact{}, false, false, fmt.Errorf("host conflict Linux inet_diag has an invalid SKV6ONLY attribute")
	}

	port := binary.BigEndian.Uint16(payload[4:6])
	protocol := SocketProtocolTCP
	if expectedProtocol == unix.IPPROTO_UDP {
		protocol = SocketProtocolUDP
	}
	if port == 0 || !requestHasSocket(request, protocol, port) {
		return SocketFact{}, false, true, nil
	}

	address, err := parseLinuxInetDiagAddress(payload[8:24], family)
	if err != nil {
		return SocketFact{}, false, false, err
	}
	if v6OnlyPresent && address != netip.IPv6Unspecified() {
		return SocketFact{}, false, false, fmt.Errorf("host conflict Linux inet_diag has SKV6ONLY on a non-wildcard endpoint")
	}
	relevant := address == request.Candidate() || address == netip.IPv4Unspecified() || address == netip.IPv6Unspecified()
	if !relevant {
		return SocketFact{}, false, true, nil
	}
	fact := SocketFact{
		Protocol:     protocol,
		Address:      address,
		Port:         port,
		TCPAccepting: protocol == SocketProtocolTCP,
		IPv6Only:     IPv6OnlyNotApplicable,
	}
	complete := true
	if address == netip.IPv6Unspecified() {
		switch {
		case !v6OnlyPresent:
			fact.IPv6Only = IPv6OnlyUnknown
			complete = false
		case v6OnlyPayload[0] == 1:
			fact.IPv6Only = IPv6OnlyEnabled
		default:
			fact.IPv6Only = IPv6OnlyDisabled
		}
	}
	return fact, true, complete, nil
}

// parseLinuxInetDiagAddress decodes the fixed network-byte-order source address for its declared family.
func parseLinuxInetDiagAddress(payload []byte, family uint8) (netip.Addr, error) {
	if len(payload) != 16 {
		return netip.Addr{}, fmt.Errorf("host conflict Linux inet_diag source address has invalid length %d", len(payload))
	}
	switch family {
	case unix.AF_INET:
		for _, value := range payload[4:] {
			if value != 0 {
				return netip.Addr{}, fmt.Errorf("host conflict Linux IPv4 inet_diag address has nonzero padding")
			}
		}
		return netip.AddrFrom4([4]byte(payload[:4])), nil
	case unix.AF_INET6:
		address := netip.AddrFrom16([16]byte(payload))
		if address.Is4In6() {
			return netip.Addr{}, fmt.Errorf("host conflict Linux inet_diag returned an IPv4-mapped IPv6 endpoint")
		}
		return address, nil
	default:
		return netip.Addr{}, fmt.Errorf("host conflict Linux inet_diag address family %d is unsupported", family)
	}
}
