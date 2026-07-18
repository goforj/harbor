//go:build darwin

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// darwinPCBTestRecord describes one synthetic fixed-stride pcblist_n item.
type darwinPCBTestRecord struct {
	address  netip.Addr
	port     uint16
	family   int32
	vflag    uint8
	flags    uint32
	tcpState int32
	shadow   bool
	protocol darwinPCBProtocol
}

// TestDarwinPCBABIIsPinned proves both supported architectures compile the exact XNU pack(4) contract.
func TestDarwinPCBABIIsPinned(t *testing.T) {
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "xinpgen size", got: unsafe.Sizeof(darwinXinpgenABI{}), want: darwinXinpgenBytes},
		{name: "xinpcb size", got: unsafe.Sizeof(darwinXinpcbABI{}), want: darwinXinpcbBytes},
		{name: "xsocket size", got: unsafe.Sizeof(darwinXsocketABI{}), want: darwinXsocketBytes},
		{name: "xsockbuf size", got: unsafe.Sizeof(darwinXsockbufABI{}), want: darwinXsockbufBytes},
		{name: "xsockstat size", got: unsafe.Sizeof(darwinXsockstatABI{}), want: darwinXsockstatBytes},
		{name: "xtcpcb size", got: unsafe.Sizeof(darwinXtcpcbABI{}), want: darwinXtcpcbBytes},
		{name: "local port offset", got: unsafe.Offsetof(darwinXinpcbABI{}.LocalPort), want: darwinXinpcbOffsetLocalPort},
		{name: "flags offset", got: unsafe.Offsetof(darwinXinpcbABI{}.Flags), want: darwinXinpcbOffsetFlags},
		{name: "vflag offset", got: unsafe.Offsetof(darwinXinpcbABI{}.VFlag), want: darwinXinpcbOffsetVFlag},
		{name: "local address offset", got: unsafe.Offsetof(darwinXinpcbABI{}.LocalAddress), want: darwinXinpcbOffsetLocal},
		{name: "socket protocol offset", got: unsafe.Offsetof(darwinXsocketABI{}.Protocol), want: darwinXsocketOffsetProtocol},
		{name: "socket family offset", got: unsafe.Offsetof(darwinXsocketABI{}.Family), want: darwinXsocketOffsetFamily},
		{name: "TCP state offset", got: unsafe.Offsetof(darwinXtcpcbABI{}.State), want: darwinXtcpcbOffsetState},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("ABI value = %d, want %d", test.got, test.want)
			}
		})
	}
}

// TestParseDarwinPCBTableFindsRequestedCollisionShapes covers exact, wildcard, dual-stack, and TCP-state filtering.
func TestParseDarwinPCBTableFindsRequestedCollisionShapes(t *testing.T) {
	request := darwinTestRequest(t)
	protocol := requestedDarwinPCBProtocols(request)[0]
	records := []darwinPCBTestRecord{
		darwinTCPTestRecord(protocol, testCandidate, 443, unix.AF_INET, darwinINPIPv4, 0, darwinTCPListen),
		darwinTCPTestRecord(protocol, netip.IPv4Unspecified(), 53, unix.AF_INET, darwinINPIPv4, 0, darwinTCPListen),
		darwinTCPTestRecord(protocol, netip.IPv6Unspecified(), 53, unix.AF_INET6, darwinINPIPv6, darwinINPV6Only, darwinTCPListen),
		darwinTCPTestRecord(protocol, netip.IPv6Unspecified(), 443, unix.AF_INET6, darwinINPIPv4|darwinINPIPv6, 0, darwinTCPListen),
		darwinTCPTestRecord(protocol, testCandidate, 53, unix.AF_INET, darwinINPIPv4, 0, 4),
		darwinTCPTestRecord(protocol, netip.MustParseAddr("127.77.0.11"), 53, unix.AF_INET, darwinINPIPv4, 0, darwinTCPListen),
	}
	snapshot, err := parseDarwinPCBTable(darwinPCBTestTable(protocol, records, 12, 31), protocol, request)
	if err != nil {
		t.Fatalf("parseDarwinPCBTable() error = %v", err)
	}
	if !snapshot.Complete || snapshot.Truncated || len(snapshot.Endpoints) != 4 {
		t.Fatalf("parseDarwinPCBTable() = %#v", snapshot)
	}
	if snapshot.Endpoints[2].IPv6Only != IPv6OnlyEnabled || snapshot.Endpoints[3].IPv6Only != IPv6OnlyDisabled {
		t.Fatalf("IPv6-only facts = %q, %q", snapshot.Endpoints[2].IPv6Only, snapshot.Endpoints[3].IPv6Only)
	}
	for _, endpoint := range snapshot.Endpoints {
		if !endpoint.TCPAccepting {
			t.Fatalf("TCP endpoint is not accepting: %#v", endpoint)
		}
	}
}

// TestParseDarwinUDPPCBTablePreservesDuplicateFacts proves the table remains a multiset.
func TestParseDarwinUDPPCBTablePreservesDuplicateFacts(t *testing.T) {
	request := darwinTestRequest(t)
	protocol := requestedDarwinPCBProtocols(request)[1]
	record := darwinPCBTestRecord{
		address:  testCandidate,
		port:     53,
		family:   unix.AF_INET,
		vflag:    darwinINPIPv4,
		protocol: protocol,
	}
	snapshot, err := parseDarwinPCBTable(darwinPCBTestTable(protocol, []darwinPCBTestRecord{record, record}, 2, 3), protocol, request)
	if err != nil {
		t.Fatalf("parseDarwinPCBTable() error = %v", err)
	}
	if !snapshot.Complete || len(snapshot.Endpoints) != 2 {
		t.Fatalf("parseDarwinPCBTable() = %#v", snapshot)
	}
	if snapshot.Endpoints[0].TCPAccepting || snapshot.Endpoints[0].IPv6Only != IPv6OnlyNotApplicable {
		t.Fatalf("UDP endpoint = %#v", snapshot.Endpoints[0])
	}
}

// TestParseDarwinPCBTableMakesUncertainDualStackStateExplicit prevents an unknown wildcard from becoming absence.
func TestParseDarwinPCBTableMakesUncertainDualStackStateExplicit(t *testing.T) {
	request := darwinTestRequest(t)
	protocol := requestedDarwinPCBProtocols(request)[1]
	record := darwinPCBTestRecord{
		address:  netip.IPv6Unspecified(),
		port:     53,
		family:   unix.AF_INET6,
		vflag:    darwinINPIPv6,
		protocol: protocol,
	}
	snapshot, err := parseDarwinPCBTable(darwinPCBTestTable(protocol, []darwinPCBTestRecord{record}, 7, 8), protocol, request)
	if err != nil {
		t.Fatalf("parseDarwinPCBTable() error = %v", err)
	}
	if snapshot.Complete || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].IPv6Only != IPv6OnlyUnknown {
		t.Fatalf("parseDarwinPCBTable() = %#v", snapshot)
	}
}

// TestParseDarwinPCBTableHandlesUserlandShadowConservatively covers XNU's intentionally sparse Skywalk records.
func TestParseDarwinPCBTableHandlesUserlandShadowConservatively(t *testing.T) {
	request := darwinTestRequest(t)
	protocol := requestedDarwinPCBProtocols(request)[1]
	record := darwinPCBTestRecord{
		address:  testCandidate,
		port:     53,
		family:   0,
		vflag:    darwinINPIPv4,
		shadow:   true,
		protocol: protocol,
	}
	snapshot, err := parseDarwinPCBTable(darwinPCBTestTable(protocol, []darwinPCBTestRecord{record}, 1, 1), protocol, request)
	if err != nil {
		t.Fatalf("parseDarwinPCBTable() error = %v", err)
	}
	if snapshot.Complete || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].Address != testCandidate {
		t.Fatalf("parseDarwinPCBTable() = %#v", snapshot)
	}
	record.vflag = darwinINPIPv4 | darwinINPIPv6
	if _, err := parseDarwinPCBTable(darwinPCBTestTable(protocol, []darwinPCBTestRecord{record}, 1, 1), protocol, request); err == nil || !strings.Contains(err.Error(), "ambiguous address flags") {
		t.Fatalf("ambiguous shadow parse error = %v", err)
	}
}

// TestParseDarwinPCBTableRejectsMalformedABIAndGeneration exercises fail-closed table boundaries.
func TestParseDarwinPCBTableRejectsMalformedABIAndGeneration(t *testing.T) {
	request := darwinTestRequest(t)
	protocol := requestedDarwinPCBProtocols(request)[0]
	valid := darwinPCBTestTable(protocol, []darwinPCBTestRecord{
		darwinTCPTestRecord(protocol, testCandidate, 443, unix.AF_INET, darwinINPIPv4, 0, darwinTCPListen),
	}, 4, 9)
	tests := []struct {
		name       string
		mutate     func([]byte) []byte
		contains   string
		generation bool
	}{
		{name: "truncated", mutate: func(raw []byte) []byte { return raw[:len(raw)-1] }, contains: "length"},
		{name: "header length", mutate: func(raw []byte) []byte { binary.NativeEndian.PutUint32(raw[:4], 25); return raw }, contains: "unsupported length"},
		{name: "header count", mutate: func(raw []byte) []byte { binary.NativeEndian.PutUint32(raw[4:8], 2); return raw }, contains: "does not match count", generation: true},
		{name: "trailer generation", mutate: func(raw []byte) []byte { binary.NativeEndian.PutUint64(raw[len(raw)-16:len(raw)-8], 5); return raw }, contains: "differ", generation: true},
		{name: "xinpcb length", mutate: func(raw []byte) []byte {
			binary.NativeEndian.PutUint32(raw[darwinXinpgenBytes:darwinXinpgenBytes+4], 100)
			return raw
		}, contains: "unsupported length"},
		{name: "xsocket kind", mutate: func(raw []byte) []byte {
			offset := darwinXinpgenBytes + darwinXinpcbBytes
			binary.NativeEndian.PutUint32(raw[offset+4:offset+8], 0xff)
			return raw
		}, contains: "unsupported kind"},
		{name: "TCP padding", mutate: func(raw []byte) []byte { raw[darwinXinpgenBytes+darwinTCPPCBBytes-1] = 1; return raw }, contains: "alignment padding"},
		{name: "unknown TCP state", mutate: func(raw []byte) []byte {
			offset := darwinXinpgenBytes + darwinUDPPCBBytes + darwinXtcpcbOffsetState
			binary.NativeEndian.PutUint32(raw[offset:offset+4], darwinTCPStates)
			return raw
		}, contains: "unsupported TCP state"},
		{name: "mapped flag", mutate: func(raw []byte) []byte { raw[darwinXinpgenBytes+darwinXinpcbOffsetVFlag] |= darwinINPV4Map; return raw }, contains: "IPv4-mapped"},
		{name: "contradictory PCB protocol", mutate: func(raw []byte) []byte { raw[darwinXinpgenBytes+darwinXinpcbOffsetProtocol] = 99; return raw }, contains: "protocol facts"},
		{name: "family contradiction", mutate: func(raw []byte) []byte {
			offset := darwinXinpgenBytes + darwinXinpcbBytes + darwinXsocketOffsetFamily
			binary.NativeEndian.PutUint32(raw[offset:offset+4], unix.AF_INET6)
			return raw
		}, contains: "contradictory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := append([]byte(nil), valid...)
			_, err := parseDarwinPCBTable(test.mutate(raw), protocol, request)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("parseDarwinPCBTable() error = %v, want %q", err, test.contains)
			}
			if test.generation && !errors.Is(err, errDarwinPCBSnapshotChanged) {
				t.Fatalf("parseDarwinPCBTable() error = %v, want generation sentinel", err)
			}
		})
	}
}

// TestParseDarwinPCBTableAcceptsOnlyHeaderForEmptySnapshot pins XNU's zero-count special case.
func TestParseDarwinPCBTableAcceptsOnlyHeaderForEmptySnapshot(t *testing.T) {
	request := darwinTestRequest(t)
	protocol := requestedDarwinPCBProtocols(request)[0]
	header := darwinPCBTestGeneration(0, 9, 11)
	snapshot, err := parseDarwinPCBTable(header, protocol, request)
	if err != nil || !snapshot.Complete || len(snapshot.Endpoints) != 0 {
		t.Fatalf("parseDarwinPCBTable() = %#v, %v", snapshot, err)
	}
	if _, err := parseDarwinPCBTable(append(header, header...), protocol, request); err == nil || !strings.Contains(err.Error(), "unexpected trailer") {
		t.Fatalf("zero-count trailer error = %v", err)
	}
}

// TestReadDarwinPCBTableRetriesOnlyENOMEMAndPreservesErrno verifies bounded native acquisition policy.
func TestReadDarwinPCBTableRetriesOnlyENOMEMAndPreservesErrno(t *testing.T) {
	calls := 0
	raw, err := readDarwinPCBTable(context.Background(), "test.pcblist_n", func(string) ([]byte, error) {
		calls++
		if calls < 3 {
			return nil, unix.ENOMEM
		}
		return []byte{1, 2, 3}, nil
	})
	if err != nil || calls != 3 || len(raw) != 3 {
		t.Fatalf("readDarwinPCBTable() = %v, %v after %d calls", raw, err, calls)
	}
	_, err = readDarwinPCBTable(context.Background(), "test.pcblist_n", func(string) ([]byte, error) {
		return nil, unix.EPERM
	})
	if !errors.Is(err, unix.EPERM) {
		t.Fatalf("readDarwinPCBTable() error = %v, want EPERM", err)
	}
	_, err = readDarwinPCBTable(context.Background(), "test.pcblist_n", func(string) ([]byte, error) {
		return nil, unix.ENOMEM
	})
	if !errors.Is(err, unix.ENOMEM) {
		t.Fatalf("readDarwinPCBTable() error = %v, want ENOMEM", err)
	}
}

// TestRequestedDarwinPCBProtocolsReadsOnlyRequestedTables proves request filtering happens before sysctl access.
func TestRequestedDarwinPCBProtocolsReadsOnlyRequestedTables(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{{Transport: TransportUDP4, Port: 53}})
	if err != nil {
		t.Fatal(err)
	}
	protocols := requestedDarwinPCBProtocols(request)
	if len(protocols) != 1 || protocols[0].protocol != SocketProtocolUDP {
		t.Fatalf("requestedDarwinPCBProtocols() = %#v", protocols)
	}
}

// darwinTestRequest returns the shared candidate with TCP and UDP requirements.
func darwinTestRequest(t *testing.T) Request {
	t.Helper()
	return mustRequest(t)
}

// darwinTCPTestRecord creates one TCP fixture record.
func darwinTCPTestRecord(protocol darwinPCBProtocol, address netip.Addr, port uint16, family int32, vflag uint8, flags uint32, state int32) darwinPCBTestRecord {
	return darwinPCBTestRecord{
		address: address, port: port, family: family, vflag: vflag, flags: flags,
		tcpState: state, protocol: protocol,
	}
}

// darwinPCBTestTable wraps fixed-stride records in matching generation envelopes.
func darwinPCBTestTable(protocol darwinPCBProtocol, records []darwinPCBTestRecord, generation uint64, socketGeneration uint64) []byte {
	header := darwinPCBTestGeneration(uint32(len(records)), generation, socketGeneration)
	raw := append([]byte(nil), header...)
	for _, record := range records {
		raw = append(raw, darwinPCBTestItem(record)...)
	}
	if len(records) > 0 {
		raw = append(raw, header...)
	}
	return raw
}

// darwinPCBTestGeneration encodes one native-endian xinpgen fixture.
func darwinPCBTestGeneration(count uint32, generation uint64, socketGeneration uint64) []byte {
	raw := make([]byte, darwinXinpgenBytes)
	binary.NativeEndian.PutUint32(raw[:4], darwinXinpgenBytes)
	binary.NativeEndian.PutUint32(raw[4:8], count)
	binary.NativeEndian.PutUint64(raw[8:16], generation)
	binary.NativeEndian.PutUint64(raw[16:24], socketGeneration)
	return raw
}

// darwinPCBTestItem encodes one exact ABI fixture without using unsafe memory projection.
func darwinPCBTestItem(record darwinPCBTestRecord) []byte {
	raw := make([]byte, record.protocol.itemBytes)
	darwinPutPCBRecordHeader(raw[:darwinXinpcbBytes], darwinXinpcbBytes, darwinXSOInpcb)
	xsocketOffset := darwinXinpcbBytes
	darwinPutPCBRecordHeader(raw[xsocketOffset:xsocketOffset+darwinXsocketBytes], darwinXsocketBytes, darwinXSOSocket)
	receiveOffset := xsocketOffset + darwinXsocketBytes
	darwinPutPCBRecordHeader(raw[receiveOffset:receiveOffset+darwinXsockbufBytes], darwinXsockbufBytes, darwinXSORcvbuf)
	sendOffset := receiveOffset + darwinXsockbufBytes
	darwinPutPCBRecordHeader(raw[sendOffset:sendOffset+darwinXsockbufBytes], darwinXsockbufBytes, darwinXSOSndbuf)
	statsOffset := sendOffset + darwinXsockbufBytes
	darwinPutPCBRecordHeader(raw[statsOffset:statsOffset+darwinXsockstatBytes], darwinXsockstatBytes, darwinXSOStats)

	binary.BigEndian.PutUint16(raw[darwinXinpcbOffsetLocalPort:darwinXinpcbOffsetLocalPort+2], record.port)
	binary.NativeEndian.PutUint32(raw[darwinXinpcbOffsetFlags:darwinXinpcbOffsetFlags+4], record.flags)
	raw[darwinXinpcbOffsetVFlag] = record.vflag
	darwinPutPCBAddress(raw[darwinXinpcbOffsetLocal:darwinXinpcbOffsetLocal+16], record.address)
	if !record.shadow {
		binary.NativeEndian.PutUint16(raw[xsocketOffset+darwinXsocketOffsetType:xsocketOffset+darwinXsocketOffsetType+2], uint16(record.protocol.socketType))
		binary.NativeEndian.PutUint32(raw[xsocketOffset+darwinXsocketOffsetFamily:xsocketOffset+darwinXsocketOffsetFamily+4], uint32(record.family))
	}
	binary.NativeEndian.PutUint32(raw[xsocketOffset+darwinXsocketOffsetProtocol:xsocketOffset+darwinXsocketOffsetProtocol+4], uint32(record.protocol.native))
	if record.protocol.protocol == SocketProtocolTCP {
		tcpOffset := statsOffset + darwinXsockstatBytes
		darwinPutPCBRecordHeader(raw[tcpOffset:tcpOffset+darwinXtcpcbBytes], darwinXtcpcbBytes, darwinXSOTcpcb)
		binary.NativeEndian.PutUint32(raw[tcpOffset+darwinXtcpcbOffsetState:tcpOffset+darwinXtcpcbOffsetState+4], uint32(record.tcpState))
	}
	return raw
}

// darwinPutPCBRecordHeader writes one fixed kind-and-length envelope.
func darwinPutPCBRecordHeader(raw []byte, length int, kind uint32) {
	binary.NativeEndian.PutUint32(raw[:4], uint32(length))
	binary.NativeEndian.PutUint32(raw[4:8], kind)
}

// darwinPutPCBAddress writes XNU's 16-byte local-address union.
func darwinPutPCBAddress(raw []byte, address netip.Addr) {
	if address.Is4() {
		value := address.As4()
		copy(raw[12:], value[:])
		return
	}
	value := address.As16()
	copy(raw, value[:])
}
