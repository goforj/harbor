//go:build darwin

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// The released macOS 14 and 15 XNU headers use these identical pack(4) records; exact envelopes are the wire-version gate.
	darwinPCBABIRevision = 1

	darwinXinpgenBytes   = 24
	darwinXinpcbBytes    = 104
	darwinXsocketBytes   = 104
	darwinXsockbufBytes  = 32
	darwinXsockstatBytes = 136
	darwinXtcpcbBytes    = 204
	darwinXtcpcbStride   = 208
	darwinUDPPCBBytes    = 408
	darwinTCPPCBBytes    = 616

	darwinXSOInpcb  = 0x010
	darwinXSOSocket = 0x001
	darwinXSORcvbuf = 0x002
	darwinXSOSndbuf = 0x004
	darwinXSOStats  = 0x008
	darwinXSOTcpcb  = 0x020
	darwinINPIPv4   = 0x01
	darwinINPIPv6   = 0x02
	darwinINPV4Map  = 0x04
	darwinINPV6Only = 0x00008000
	darwinTCPListen = 1
	darwinTCPStates = 11

	maximumDarwinPCBBytes   = 64 << 20
	maximumDarwinPCBRecords = 65536
	darwinSysctlRetries     = 3
)

const (
	darwinXinpcbOffsetLocalPort = 18
	darwinXinpcbOffsetFlags     = 36
	darwinXinpcbOffsetVFlag     = 44
	darwinXinpcbOffsetProtocol  = 46
	darwinXinpcbOffsetLocal     = 64
	darwinXsocketOffsetType     = 16
	darwinXsocketOffsetProtocol = 36
	darwinXsocketOffsetFamily   = 40
	darwinXtcpcbOffsetState     = 36
)

// darwinXinpgenABI pins XNU's pack(4) pcblist_n generation record.
type darwinXinpgenABI struct {
	Length           uint32
	Count            uint32
	Generation       uint64
	SocketGeneration uint64
}

// darwinXinpcbABI pins the fields Harbor reads while retaining XNU's pack(4) layout.
type darwinXinpcbABI struct {
	Length         uint32
	Kind           uint32
	PCB            [8]byte
	ForeignPort    uint16
	LocalPort      uint16
	ProtocolPCB    [8]byte
	Generation     [8]byte
	Flags          uint32
	Flow           uint32
	VFlag          uint8
	TTL            uint8
	Protocol       uint8
	Padding        uint8
	ForeignAddress [16]byte
	LocalAddress   [16]byte
	IPv4TOS        uint8
	Depend4Padding [3]byte
	IPv6HopLimit   uint8
	Depend6Padding [3]byte
	IPv6Checksum   int32
	IPv6Interface  uint16
	IPv6Hops       int16
	FlowHash       uint32
	Flags2         uint32
}

// darwinXsocketABI pins the socket type, protocol, and family offsets in XNU's pack(4) record.
type darwinXsocketABI struct {
	Length   uint32
	Kind     uint32
	Socket   [8]byte
	Type     int16
	Padding  [2]byte
	Options  uint32
	Linger   int16
	State    int16
	PCB      [8]byte
	Protocol int32
	Family   int32
	Tail     [60]byte
}

// darwinXsockbufABI pins the fixed socket-buffer record envelope.
type darwinXsockbufABI struct {
	Length uint32
	Kind   uint32
	Tail   [24]byte
}

// darwinXsockstatABI pins the fixed per-socket statistics record envelope.
type darwinXsockstatABI struct {
	Length uint32
	Kind   uint32
	Tail   [128]byte
}

// darwinXtcpcbABI pins the TCP state offset and unrounded XNU record length.
type darwinXtcpcbABI struct {
	Length    uint32
	Kind      uint32
	SegmentQ  [8]byte
	DupACKs   int32
	Timers    [4]int32
	State     int32
	Flags     uint32
	Remaining [160]byte
}

var (
	_ [darwinXinpgenBytes - int(unsafe.Sizeof(darwinXinpgenABI{}))]byte
	_ [int(unsafe.Sizeof(darwinXinpgenABI{})) - darwinXinpgenBytes]byte
	_ [darwinXinpcbBytes - int(unsafe.Sizeof(darwinXinpcbABI{}))]byte
	_ [int(unsafe.Sizeof(darwinXinpcbABI{})) - darwinXinpcbBytes]byte
	_ [darwinXsocketBytes - int(unsafe.Sizeof(darwinXsocketABI{}))]byte
	_ [int(unsafe.Sizeof(darwinXsocketABI{})) - darwinXsocketBytes]byte
	_ [darwinXsockbufBytes - int(unsafe.Sizeof(darwinXsockbufABI{}))]byte
	_ [int(unsafe.Sizeof(darwinXsockbufABI{})) - darwinXsockbufBytes]byte
	_ [darwinXsockstatBytes - int(unsafe.Sizeof(darwinXsockstatABI{}))]byte
	_ [int(unsafe.Sizeof(darwinXsockstatABI{})) - darwinXsockstatBytes]byte
	_ [darwinXtcpcbBytes - int(unsafe.Sizeof(darwinXtcpcbABI{}))]byte
	_ [int(unsafe.Sizeof(darwinXtcpcbABI{})) - darwinXtcpcbBytes]byte

	_ [darwinXinpcbOffsetLocalPort - int(unsafe.Offsetof(darwinXinpcbABI{}.LocalPort))]byte
	_ [int(unsafe.Offsetof(darwinXinpcbABI{}.LocalPort)) - darwinXinpcbOffsetLocalPort]byte
	_ [darwinXinpcbOffsetFlags - int(unsafe.Offsetof(darwinXinpcbABI{}.Flags))]byte
	_ [int(unsafe.Offsetof(darwinXinpcbABI{}.Flags)) - darwinXinpcbOffsetFlags]byte
	_ [darwinXinpcbOffsetVFlag - int(unsafe.Offsetof(darwinXinpcbABI{}.VFlag))]byte
	_ [int(unsafe.Offsetof(darwinXinpcbABI{}.VFlag)) - darwinXinpcbOffsetVFlag]byte
	_ [darwinXinpcbOffsetProtocol - int(unsafe.Offsetof(darwinXinpcbABI{}.Protocol))]byte
	_ [int(unsafe.Offsetof(darwinXinpcbABI{}.Protocol)) - darwinXinpcbOffsetProtocol]byte
	_ [darwinXinpcbOffsetLocal - int(unsafe.Offsetof(darwinXinpcbABI{}.LocalAddress))]byte
	_ [int(unsafe.Offsetof(darwinXinpcbABI{}.LocalAddress)) - darwinXinpcbOffsetLocal]byte
	_ [darwinXsocketOffsetType - int(unsafe.Offsetof(darwinXsocketABI{}.Type))]byte
	_ [int(unsafe.Offsetof(darwinXsocketABI{}.Type)) - darwinXsocketOffsetType]byte
	_ [darwinXsocketOffsetProtocol - int(unsafe.Offsetof(darwinXsocketABI{}.Protocol))]byte
	_ [int(unsafe.Offsetof(darwinXsocketABI{}.Protocol)) - darwinXsocketOffsetProtocol]byte
	_ [darwinXsocketOffsetFamily - int(unsafe.Offsetof(darwinXsocketABI{}.Family))]byte
	_ [int(unsafe.Offsetof(darwinXsocketABI{}.Family)) - darwinXsocketOffsetFamily]byte
	_ [darwinXtcpcbOffsetState - int(unsafe.Offsetof(darwinXtcpcbABI{}.State))]byte
	_ [int(unsafe.Offsetof(darwinXtcpcbABI{}.State)) - darwinXtcpcbOffsetState]byte
)

// darwinSysctlRead supplies one native sysctl table to bounded acquisition tests.
type darwinSysctlRead func(string) ([]byte, error)

// darwinPCBProtocol describes one pinned pcblist_n table and its shared-model protocol.
type darwinPCBProtocol struct {
	name       string
	native     uint8
	socketType int32
	protocol   SocketProtocol
	itemBytes  int
}

// darwinPCBRecord contains the collision-relevant fields from one validated record group.
type darwinPCBRecord struct {
	address      netip.Addr
	port         uint16
	flags        uint32
	vflag        uint8
	tcpAccepting bool
	shadow       bool
}

// observeDarwinSockets reads only protocol tables named by the request and filters them locally.
func observeDarwinSockets(ctx context.Context, request Request) (SocketSnapshot, error) {
	return observeDarwinSocketsWith(ctx, request, func(name string) ([]byte, error) {
		return unix.SysctlRaw(name)
	})
}

// observeDarwinSocketsWith preserves protocol boundaries so one incomplete table cannot hide another.
func observeDarwinSocketsWith(ctx context.Context, request Request, read darwinSysctlRead) (SocketSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SocketSnapshot{}, err
	}
	snapshot := SocketSnapshot{Complete: true}
	for _, protocol := range requestedDarwinPCBProtocols(request) {
		raw, err := readDarwinPCBTable(ctx, protocol.name, read)
		if err != nil {
			return SocketSnapshot{}, err
		}
		table, err := parseDarwinPCBTable(raw, protocol, request)
		if err != nil {
			return SocketSnapshot{}, fmt.Errorf("host conflict Darwin parse %s PCB table: %w", protocol.protocol, err)
		}
		if !table.Complete {
			snapshot.Complete = false
		}
		if table.Truncated {
			snapshot.Truncated = true
		}
		for _, endpoint := range table.Endpoints {
			if len(snapshot.Endpoints) >= maximumSocketFacts {
				snapshot.Complete = false
				snapshot.Truncated = true
				continue
			}
			snapshot.Endpoints = append(snapshot.Endpoints, endpoint)
		}
	}
	return snapshot, nil
}

// requestedDarwinPCBProtocols returns each native table at most once in stable order.
func requestedDarwinPCBProtocols(request Request) []darwinPCBProtocol {
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
	protocols := make([]darwinPCBProtocol, 0, 2)
	if tcp {
		protocols = append(protocols, darwinPCBProtocol{
			name:       "net.inet.tcp.pcblist_n",
			native:     unix.IPPROTO_TCP,
			socketType: unix.SOCK_STREAM,
			protocol:   SocketProtocolTCP,
			itemBytes:  darwinTCPPCBBytes,
		})
	}
	if udp {
		protocols = append(protocols, darwinPCBProtocol{
			name:       "net.inet.udp.pcblist_n",
			native:     unix.IPPROTO_UDP,
			socketType: unix.SOCK_DGRAM,
			protocol:   SocketProtocolUDP,
			itemBytes:  darwinUDPPCBBytes,
		})
	}
	return protocols
}

// readDarwinPCBTable retries only the native size race and rejects an oversized result before parsing.
func readDarwinPCBTable(ctx context.Context, name string, read darwinSysctlRead) ([]byte, error) {
	for attempt := 0; attempt < darwinSysctlRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := read(name)
		if errors.Is(err, unix.ENOMEM) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("host conflict Darwin read sysctl %q: %w", name, err)
		}
		if len(raw) > maximumDarwinPCBBytes {
			return nil, fmt.Errorf("host conflict Darwin sysctl %q exceeds %d bytes", name, maximumDarwinPCBBytes)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("host conflict Darwin read sysctl %q after %d size races: %w", name, darwinSysctlRetries, unix.ENOMEM)
}

// parseDarwinPCBTable validates the complete fixed-stride table before emitting filtered endpoint facts.
func parseDarwinPCBTable(raw []byte, protocol darwinPCBProtocol, request Request) (SocketSnapshot, error) {
	if darwinPCBABIRevision != 1 {
		return SocketSnapshot{}, fmt.Errorf("unsupported pcblist_n ABI revision %d", darwinPCBABIRevision)
	}
	if err := validateDarwinPCBProtocol(protocol); err != nil {
		return SocketSnapshot{}, err
	}
	if len(raw) < darwinXinpgenBytes || len(raw) > maximumDarwinPCBBytes {
		return SocketSnapshot{}, fmt.Errorf("pcblist_n table has invalid length %d", len(raw))
	}
	header, err := parseDarwinXinpgen(raw[:darwinXinpgenBytes])
	if err != nil {
		return SocketSnapshot{}, err
	}
	if header.count > maximumDarwinPCBRecords {
		return SocketSnapshot{}, fmt.Errorf("pcblist_n count %d exceeds %d records", header.count, maximumDarwinPCBRecords)
	}
	if header.count == 0 {
		if len(raw) != darwinXinpgenBytes {
			return SocketSnapshot{}, fmt.Errorf("empty pcblist_n table contains an unexpected trailer or records")
		}
		return SocketSnapshot{Complete: true}, nil
	}
	wantBytes := darwinXinpgenBytes + int(header.count)*protocol.itemBytes + darwinXinpgenBytes
	if len(raw) != wantBytes {
		return SocketSnapshot{}, errors.Join(errDarwinPCBSnapshotChanged, fmt.Errorf("pcblist_n table length %d does not match count %d and stride %d", len(raw), header.count, protocol.itemBytes))
	}
	trailerOffset := len(raw) - darwinXinpgenBytes
	trailer, err := parseDarwinXinpgen(raw[trailerOffset:])
	if err != nil {
		return SocketSnapshot{}, fmt.Errorf("pcblist_n trailer: %w", err)
	}
	if header != trailer {
		return SocketSnapshot{}, errors.Join(errDarwinPCBSnapshotChanged, fmt.Errorf("pcblist_n generation header and trailer differ"))
	}

	snapshot := SocketSnapshot{Complete: true}
	for index := uint32(0); index < header.count; index++ {
		offset := darwinXinpgenBytes + int(index)*protocol.itemBytes
		record, err := parseDarwinPCBRecord(raw[offset:offset+protocol.itemBytes], protocol)
		if err != nil {
			return SocketSnapshot{}, fmt.Errorf("pcblist_n record %d: %w", index, err)
		}
		fact, relevant, complete, err := darwinPCBRecordFact(record, protocol, request)
		if err != nil {
			return SocketSnapshot{}, fmt.Errorf("pcblist_n record %d: %w", index, err)
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
	return snapshot, nil
}

// darwinXinpgen captures the generation fields used to reject an internally raced table.
type darwinXinpgen struct {
	count            uint32
	generation       uint64
	socketGeneration uint64
}

// parseDarwinXinpgen rejects any generation envelope other than XNU's pinned 24-byte version.
func parseDarwinXinpgen(raw []byte) (darwinXinpgen, error) {
	if len(raw) != darwinXinpgenBytes {
		return darwinXinpgen{}, fmt.Errorf("pcblist_n generation record has invalid length %d", len(raw))
	}
	if length := binary.NativeEndian.Uint32(raw[:4]); length != darwinXinpgenBytes {
		return darwinXinpgen{}, fmt.Errorf("pcblist_n generation record declares unsupported length %d", length)
	}
	return darwinXinpgen{
		count:            binary.NativeEndian.Uint32(raw[4:8]),
		generation:       binary.NativeEndian.Uint64(raw[8:16]),
		socketGeneration: binary.NativeEndian.Uint64(raw[16:24]),
	}, nil
}

// parseDarwinPCBRecord validates every subrecord kind and length before interpreting family facts.
func parseDarwinPCBRecord(raw []byte, protocol darwinPCBProtocol) (darwinPCBRecord, error) {
	if len(raw) != protocol.itemBytes {
		return darwinPCBRecord{}, fmt.Errorf("record has length %d, want %d", len(raw), protocol.itemBytes)
	}
	if err := validateDarwinPCBRecordHeader(raw[:darwinXinpcbBytes], darwinXinpcbBytes, darwinXSOInpcb, "xinpcb_n"); err != nil {
		return darwinPCBRecord{}, err
	}
	xsocketOffset := darwinXinpcbBytes
	if err := validateDarwinPCBRecordHeader(raw[xsocketOffset:xsocketOffset+darwinXsocketBytes], darwinXsocketBytes, darwinXSOSocket, "xsocket_n"); err != nil {
		return darwinPCBRecord{}, err
	}
	receiveOffset := xsocketOffset + darwinXsocketBytes
	if err := validateDarwinPCBRecordHeader(raw[receiveOffset:receiveOffset+darwinXsockbufBytes], darwinXsockbufBytes, darwinXSORcvbuf, "receive xsockbuf_n"); err != nil {
		return darwinPCBRecord{}, err
	}
	sendOffset := receiveOffset + darwinXsockbufBytes
	if err := validateDarwinPCBRecordHeader(raw[sendOffset:sendOffset+darwinXsockbufBytes], darwinXsockbufBytes, darwinXSOSndbuf, "send xsockbuf_n"); err != nil {
		return darwinPCBRecord{}, err
	}
	statsOffset := sendOffset + darwinXsockbufBytes
	if err := validateDarwinPCBRecordHeader(raw[statsOffset:statsOffset+darwinXsockstatBytes], darwinXsockstatBytes, darwinXSOStats, "xsockstat_n"); err != nil {
		return darwinPCBRecord{}, err
	}

	xinpcb := raw[:darwinXinpcbBytes]
	xsocket := raw[xsocketOffset : xsocketOffset+darwinXsocketBytes]
	if vflag := xinpcb[darwinXinpcbOffsetVFlag]; vflag == 0 || vflag&^(darwinINPIPv4|darwinINPIPv6|darwinINPV4Map) != 0 {
		return darwinPCBRecord{}, fmt.Errorf("xinpcb_n has unsupported address flags %#x", vflag)
	}
	if xinpcb[darwinXinpcbOffsetVFlag]&darwinINPV4Map != 0 {
		return darwinPCBRecord{}, fmt.Errorf("xinpcb_n contains ambiguous IPv4-mapped address facts")
	}

	family := int32(binary.NativeEndian.Uint32(xsocket[darwinXsocketOffsetFamily : darwinXsocketOffsetFamily+4]))
	socketType := int32(int16(binary.NativeEndian.Uint16(xsocket[darwinXsocketOffsetType : darwinXsocketOffsetType+2])))
	xsocketProtocol := int32(binary.NativeEndian.Uint32(xsocket[darwinXsocketOffsetProtocol : darwinXsocketOffsetProtocol+4]))
	xinpcbProtocol := xinpcb[darwinXinpcbOffsetProtocol]
	shadow := family == 0 && socketType == 0 && xinpcbProtocol == 0 && xsocketProtocol == int32(protocol.native)
	if !shadow {
		if family != unix.AF_INET && family != unix.AF_INET6 {
			return darwinPCBRecord{}, fmt.Errorf("xsocket_n has unsupported family %d", family)
		}
		if socketType != protocol.socketType {
			return darwinPCBRecord{}, fmt.Errorf("xsocket_n type %d does not match protocol", socketType)
		}
		if xsocketProtocol != int32(protocol.native) || xinpcbProtocol != 0 {
			return darwinPCBRecord{}, fmt.Errorf("PCB protocol facts %d and %d do not match %d", xinpcbProtocol, xsocketProtocol, protocol.native)
		}
	}

	vflag := xinpcb[darwinXinpcbOffsetVFlag]
	address, err := parseDarwinPCBAddress(xinpcb[darwinXinpcbOffsetLocal:darwinXinpcbOffsetLocal+16], family, vflag, shadow)
	if err != nil {
		return darwinPCBRecord{}, err
	}
	tcpAccepting := false
	if protocol.protocol == SocketProtocolTCP {
		tcpOffset := statsOffset + darwinXsockstatBytes
		if err := validateDarwinPCBRecordHeader(raw[tcpOffset:tcpOffset+darwinXtcpcbBytes], darwinXtcpcbBytes, darwinXSOTcpcb, "xtcpcb_n"); err != nil {
			return darwinPCBRecord{}, err
		}
		if !allDarwinBytesZero(raw[tcpOffset+darwinXtcpcbBytes : tcpOffset+darwinXtcpcbStride]) {
			return darwinPCBRecord{}, fmt.Errorf("xtcpcb_n has nonzero alignment padding")
		}
		state := int32(binary.NativeEndian.Uint32(raw[tcpOffset+darwinXtcpcbOffsetState : tcpOffset+darwinXtcpcbOffsetState+4]))
		if state < 0 || state >= darwinTCPStates {
			return darwinPCBRecord{}, fmt.Errorf("xtcpcb_n has unsupported TCP state %d", state)
		}
		tcpAccepting = state == darwinTCPListen
	}
	return darwinPCBRecord{
		address:      address,
		port:         binary.BigEndian.Uint16(xinpcb[darwinXinpcbOffsetLocalPort : darwinXinpcbOffsetLocalPort+2]),
		flags:        binary.NativeEndian.Uint32(xinpcb[darwinXinpcbOffsetFlags : darwinXinpcbOffsetFlags+4]),
		vflag:        vflag,
		tcpAccepting: tcpAccepting,
		shadow:       shadow,
	}, nil
}

// validateDarwinPCBRecordHeader enforces exact lengths and kinds as the pcblist_n record-version gate.
func validateDarwinPCBRecordHeader(raw []byte, wantLength int, wantKind uint32, label string) error {
	if len(raw) != wantLength {
		return fmt.Errorf("%s has invalid slice length %d", label, len(raw))
	}
	length := binary.NativeEndian.Uint32(raw[:4])
	kind := binary.NativeEndian.Uint32(raw[4:8])
	if length != uint32(wantLength) {
		return fmt.Errorf("%s declares unsupported length %d", label, length)
	}
	if kind != wantKind {
		return fmt.Errorf("%s declares unsupported kind %#x", label, kind)
	}
	return nil
}

// parseDarwinPCBAddress reconciles xsocket family and xinpcb vflag rather than trusting either alone.
func parseDarwinPCBAddress(raw []byte, family int32, vflag uint8, shadow bool) (netip.Addr, error) {
	if len(raw) != 16 {
		return netip.Addr{}, fmt.Errorf("xinpcb_n local address has invalid length %d", len(raw))
	}
	if shadow {
		switch vflag {
		case darwinINPIPv4:
			family = unix.AF_INET
		case darwinINPIPv6:
			family = unix.AF_INET6
		default:
			return netip.Addr{}, fmt.Errorf("shadow PCB has ambiguous address flags %#x", vflag)
		}
	}
	switch family {
	case unix.AF_INET:
		if vflag != darwinINPIPv4 {
			return netip.Addr{}, fmt.Errorf("AF_INET socket has contradictory address flags %#x", vflag)
		}
		if !allDarwinBytesZero(raw[:12]) {
			return netip.Addr{}, fmt.Errorf("AF_INET xinpcb_n address has nonzero IPv4 padding")
		}
		return netip.AddrFrom4([4]byte(raw[12:16])), nil
	case unix.AF_INET6:
		if vflag == darwinINPIPv4 {
			if !allDarwinBytesZero(raw[:12]) {
				return netip.Addr{}, fmt.Errorf("AF_INET6 IPv4-capable xinpcb_n address has nonzero padding")
			}
			return netip.AddrFrom4([4]byte(raw[12:16])), nil
		}
		if vflag != darwinINPIPv6 && vflag != darwinINPIPv4|darwinINPIPv6 {
			return netip.Addr{}, fmt.Errorf("AF_INET6 socket has contradictory address flags %#x", vflag)
		}
		address := netip.AddrFrom16([16]byte(raw))
		if address.Is4In6() {
			return netip.Addr{}, fmt.Errorf("AF_INET6 xinpcb_n contains an IPv4-mapped address")
		}
		if vflag&darwinINPIPv4 != 0 && !address.IsUnspecified() {
			return netip.Addr{}, fmt.Errorf("AF_INET6 non-wildcard has contradictory IPv4 capability")
		}
		return address, nil
	default:
		return netip.Addr{}, fmt.Errorf("xsocket_n has unsupported family %d", family)
	}
}

// validateDarwinPCBProtocol prevents a malformed descriptor from changing fixed record boundaries.
func validateDarwinPCBProtocol(protocol darwinPCBProtocol) error {
	switch protocol.protocol {
	case SocketProtocolTCP:
		if protocol.native != unix.IPPROTO_TCP || protocol.socketType != unix.SOCK_STREAM || protocol.itemBytes != darwinTCPPCBBytes {
			return fmt.Errorf("TCP pcblist_n descriptor does not match ABI revision %d", darwinPCBABIRevision)
		}
	case SocketProtocolUDP:
		if protocol.native != unix.IPPROTO_UDP || protocol.socketType != unix.SOCK_DGRAM || protocol.itemBytes != darwinUDPPCBBytes {
			return fmt.Errorf("UDP pcblist_n descriptor does not match ABI revision %d", darwinPCBABIRevision)
		}
	default:
		return fmt.Errorf("pcblist_n descriptor has unsupported protocol %q", protocol.protocol)
	}
	return nil
}

// darwinPCBRecordFact filters one requested collision shape and makes uncertain dual-stack state explicit.
func darwinPCBRecordFact(record darwinPCBRecord, protocol darwinPCBProtocol, request Request) (SocketFact, bool, bool, error) {
	if record.port == 0 || !requestHasSocket(request, protocol.protocol, record.port) {
		return SocketFact{}, false, true, nil
	}
	if protocol.protocol == SocketProtocolTCP && !record.tcpAccepting {
		return SocketFact{}, false, true, nil
	}
	relevant := record.address == request.Candidate() || record.address == netip.IPv4Unspecified() || record.address == netip.IPv6Unspecified()
	if !relevant {
		return SocketFact{}, false, true, nil
	}
	fact := SocketFact{
		Protocol:     protocol.protocol,
		Address:      record.address,
		Port:         record.port,
		TCPAccepting: protocol.protocol == SocketProtocolTCP,
		IPv6Only:     IPv6OnlyNotApplicable,
	}
	complete := !record.shadow
	if record.address == netip.IPv6Unspecified() {
		hasIPv4 := record.vflag&darwinINPIPv4 != 0
		v6Only := record.flags&darwinINPV6Only != 0
		switch {
		case v6Only && !hasIPv4:
			fact.IPv6Only = IPv6OnlyEnabled
		case !v6Only && hasIPv4:
			fact.IPv6Only = IPv6OnlyDisabled
		default:
			fact.IPv6Only = IPv6OnlyUnknown
			complete = false
		}
	}
	return fact, true, complete, nil
}

// allDarwinBytesZero rejects hidden address and alignment facts without allocating.
func allDarwinBytesZero(raw []byte) bool {
	for _, value := range raw {
		if value != 0 {
			return false
		}
	}
	return true
}
