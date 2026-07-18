//go:build windows

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsTCPTableOwnerPIDListener = 3
	windowsUDPTableOwnerPID         = 1
	windowsTCPStateListen           = 2
	windowsTCP4RowSize              = 24
	windowsTCP6RowSize              = 56
	windowsUDP4RowSize              = 12
	windowsUDP6RowSize              = 28
	maximumWindowsEndpointRows      = 65536
	maximumWindowsEndpointTableSize = 16 << 20
	windowsEndpointResizeAttempts   = 3
)

// windowsTCP4OwnerPIDRow fixes the documented MIB_TCPTABLE_OWNER_PID row ABI.
type windowsTCP4OwnerPIDRow struct {
	state      uint32
	localAddr  uint32
	localPort  uint32
	remoteAddr uint32
	remotePort uint32
	owningPID  uint32
}

// windowsTCP6OwnerPIDRow fixes the documented MIB_TCP6TABLE_OWNER_PID row ABI.
type windowsTCP6OwnerPIDRow struct {
	localAddr     [16]byte
	localScopeID  uint32
	localPort     uint32
	remoteAddr    [16]byte
	remoteScopeID uint32
	remotePort    uint32
	state         uint32
	owningPID     uint32
}

// windowsUDP4OwnerPIDRow fixes the documented MIB_UDPTABLE_OWNER_PID row ABI.
type windowsUDP4OwnerPIDRow struct {
	localAddr uint32
	localPort uint32
	owningPID uint32
}

// windowsUDP6OwnerPIDRow fixes the documented MIB_UDP6TABLE_OWNER_PID row ABI.
type windowsUDP6OwnerPIDRow struct {
	localAddr    [16]byte
	localScopeID uint32
	localPort    uint32
	owningPID    uint32
}

var (
	_ [windowsTCP4RowSize - int(unsafe.Sizeof(windowsTCP4OwnerPIDRow{}))]byte
	_ [int(unsafe.Sizeof(windowsTCP4OwnerPIDRow{})) - windowsTCP4RowSize]byte
	_ [windowsTCP6RowSize - int(unsafe.Sizeof(windowsTCP6OwnerPIDRow{}))]byte
	_ [int(unsafe.Sizeof(windowsTCP6OwnerPIDRow{})) - windowsTCP6RowSize]byte
	_ [windowsUDP4RowSize - int(unsafe.Sizeof(windowsUDP4OwnerPIDRow{}))]byte
	_ [int(unsafe.Sizeof(windowsUDP4OwnerPIDRow{})) - windowsUDP4RowSize]byte
	_ [windowsUDP6RowSize - int(unsafe.Sizeof(windowsUDP6OwnerPIDRow{}))]byte
	_ [int(unsafe.Sizeof(windowsUDP6OwnerPIDRow{})) - windowsUDP6RowSize]byte
)

// windowsEndpointTableCall fills one native table buffer and returns the procedure's status.
type windowsEndpointTableCall func([]byte, *uint32) error

// sockets enumerates both address families for every requested transport in the current compartment.
func (api nativeWindowsHostConflictAPI) sockets(ctx context.Context, request Request) (SocketSnapshot, error) {
	if err := api.procedureError; err != nil {
		return SocketSnapshot{}, err
	}
	needsTCP := false
	needsUDP := false
	for _, requirement := range request.Requirements() {
		if requirement.Transport == TransportTCP4 {
			needsTCP = true
		} else {
			needsUDP = true
		}
	}

	endpoints := make([]SocketFact, 0, 8)
	if needsTCP {
		for _, family := range []uint32{windows.AF_INET, windows.AF_INET6} {
			data, err := readWindowsEndpointTable(ctx, api.tcpTableCall(family))
			if err != nil {
				return SocketSnapshot{}, fmt.Errorf("read IP Helper TCP%d listener table: %w", windowsAddressFamilyBits(family), err)
			}
			var facts []SocketFact
			if family == windows.AF_INET {
				facts, err = parseWindowsTCP4Table(ctx, data, request)
			} else {
				facts, err = parseWindowsTCP6Table(ctx, data, request)
			}
			if err != nil {
				return SocketSnapshot{}, fmt.Errorf("parse IP Helper TCP%d listener table: %w", windowsAddressFamilyBits(family), err)
			}
			endpoints = append(endpoints, facts...)
			if len(endpoints) > maximumSocketFacts {
				return SocketSnapshot{}, fmt.Errorf("IP Helper relevant endpoints exceed limit %d", maximumSocketFacts)
			}
		}
	}
	if needsUDP {
		for _, family := range []uint32{windows.AF_INET, windows.AF_INET6} {
			data, err := readWindowsEndpointTable(ctx, api.udpTableCall(family))
			if err != nil {
				return SocketSnapshot{}, fmt.Errorf("read IP Helper UDP%d endpoint table: %w", windowsAddressFamilyBits(family), err)
			}
			var facts []SocketFact
			if family == windows.AF_INET {
				facts, err = parseWindowsUDP4Table(ctx, data, request)
			} else {
				facts, err = parseWindowsUDP6Table(ctx, data, request)
			}
			if err != nil {
				return SocketSnapshot{}, fmt.Errorf("parse IP Helper UDP%d endpoint table: %w", windowsAddressFamilyBits(family), err)
			}
			endpoints = append(endpoints, facts...)
			if len(endpoints) > maximumSocketFacts {
				return SocketSnapshot{}, fmt.Errorf("IP Helper relevant endpoints exceed limit %d", maximumSocketFacts)
			}
		}
	}
	return SocketSnapshot{Complete: true, Endpoints: endpoints}, nil
}

// tcpTableCall binds GetExtendedTcpTable to the listener-only owner-PID class for one family.
func (api nativeWindowsHostConflictAPI) tcpTableCall(family uint32) windowsEndpointTableCall {
	return func(buffer []byte, size *uint32) error {
		if api.procedureError != nil {
			return api.procedureError
		}
		pointer := uintptr(0)
		if len(buffer) > 0 {
			pointer = uintptr(unsafe.Pointer(&buffer[0]))
		}
		status, _, _ := windowsGetTCPTableProcedure.Call(
			pointer,
			uintptr(unsafe.Pointer(size)),
			0,
			uintptr(family),
			windowsTCPTableOwnerPIDListener,
			0,
		)
		return windowsHostConflictStatusError(status)
	}
}

// udpTableCall binds GetExtendedUdpTable to the owner-PID class for one family.
func (api nativeWindowsHostConflictAPI) udpTableCall(family uint32) windowsEndpointTableCall {
	return func(buffer []byte, size *uint32) error {
		if api.procedureError != nil {
			return api.procedureError
		}
		pointer := uintptr(0)
		if len(buffer) > 0 {
			pointer = uintptr(unsafe.Pointer(&buffer[0]))
		}
		status, _, _ := windowsGetUDPTableProcedure.Call(
			pointer,
			uintptr(unsafe.Pointer(size)),
			0,
			uintptr(family),
			windowsUDPTableOwnerPID,
			0,
		)
		return windowsHostConflictStatusError(status)
	}
}

// readWindowsEndpointTable performs the documented size query with strict growth and memory bounds.
func readWindowsEndpointTable(ctx context.Context, call windowsEndpointTableCall) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := uint32(0)
	err := call(nil, &size)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		if err == nil {
			return nil, fmt.Errorf("IP Helper size query unexpectedly succeeded without a buffer")
		}
		return nil, err
	}
	if err := validateWindowsEndpointTableSize(size); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < windowsEndpointResizeAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		buffer := make([]byte, int(size))
		reported := size
		err := call(buffer, &reported)
		if err == nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if reported < 4 || reported > uint32(len(buffer)) {
				return nil, fmt.Errorf("IP Helper reported invalid endpoint table size %d for buffer %d", reported, len(buffer))
			}
			return buffer[:reported], nil
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil, err
		}
		if reported <= size {
			return nil, fmt.Errorf("IP Helper endpoint table resize did not grow beyond %d bytes", size)
		}
		if err := validateWindowsEndpointTableSize(reported); err != nil {
			return nil, err
		}
		size = reported
	}
	return nil, fmt.Errorf("IP Helper endpoint table did not stabilize after %d resize attempts", windowsEndpointResizeAttempts)
}

// validateWindowsEndpointTableSize rejects buffers that cannot contain a count or exceed the memory budget.
func validateWindowsEndpointTableSize(size uint32) error {
	if size < 4 {
		return fmt.Errorf("IP Helper endpoint table size %d is smaller than its header", size)
	}
	if size > maximumWindowsEndpointTableSize {
		return fmt.Errorf("IP Helper endpoint table size %d exceeds limit %d", size, maximumWindowsEndpointTableSize)
	}
	return nil
}

// parseWindowsTCP4Table retains requested exact and wildcard listeners after validating the native ABI.
func parseWindowsTCP4Table(ctx context.Context, data []byte, request Request) ([]SocketFact, error) {
	count, rows, err := windowsEndpointTableRows(data, windowsTCP4RowSize)
	if err != nil {
		return nil, err
	}
	facts := make([]SocketFact, 0)
	for index := 0; index < count; index++ {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		row := rows[index*windowsTCP4RowSize : (index+1)*windowsTCP4RowSize]
		if binary.LittleEndian.Uint32(row[0:4]) != windowsTCPStateListen {
			return nil, fmt.Errorf("TCP4 listener row %d has non-listening state", index)
		}
		port, err := windowsEndpointPort(row[8:12])
		if err != nil {
			return nil, fmt.Errorf("TCP4 listener row %d: %w", index, err)
		}
		if !allWindowsBytesZero(row[12:20]) {
			return nil, fmt.Errorf("TCP4 listener row %d has a remote endpoint", index)
		}
		if !requestHasSocket(request, SocketProtocolTCP, port) {
			continue
		}
		address := netip.AddrFrom4([4]byte(row[4:8]))
		if address != request.Candidate() && address != netip.IPv4Unspecified() {
			continue
		}
		facts = append(facts, SocketFact{Protocol: SocketProtocolTCP, Address: address, Port: port, TCPAccepting: true, IPv6Only: IPv6OnlyNotApplicable})
	}
	return facts, nil
}

// parseWindowsTCP6Table retains requested wildcard listeners with unknown dual-stack behavior.
func parseWindowsTCP6Table(ctx context.Context, data []byte, request Request) ([]SocketFact, error) {
	count, rows, err := windowsEndpointTableRows(data, windowsTCP6RowSize)
	if err != nil {
		return nil, err
	}
	facts := make([]SocketFact, 0)
	for index := 0; index < count; index++ {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		row := rows[index*windowsTCP6RowSize : (index+1)*windowsTCP6RowSize]
		if binary.LittleEndian.Uint32(row[48:52]) != windowsTCPStateListen {
			return nil, fmt.Errorf("TCP6 listener row %d has non-listening state", index)
		}
		port, err := windowsEndpointPort(row[20:24])
		if err != nil {
			return nil, fmt.Errorf("TCP6 listener row %d: %w", index, err)
		}
		if !allWindowsBytesZero(row[24:48]) {
			return nil, fmt.Errorf("TCP6 listener row %d has a remote endpoint", index)
		}
		if !requestHasSocket(request, SocketProtocolTCP, port) {
			continue
		}
		address := netip.AddrFrom16([16]byte(row[0:16]))
		if address.Is4In6() {
			return nil, fmt.Errorf("TCP6 listener row %d uses an IPv4-mapped address", index)
		}
		if address != netip.IPv6Unspecified() {
			continue
		}
		if binary.LittleEndian.Uint32(row[16:20]) != 0 {
			return nil, fmt.Errorf("TCP6 wildcard row %d has a scope identifier", index)
		}
		facts = append(facts, SocketFact{Protocol: SocketProtocolTCP, Address: address, Port: port, TCPAccepting: true, IPv6Only: IPv6OnlyUnknown})
	}
	return facts, nil
}

// parseWindowsUDP4Table retains requested exact and wildcard endpoints after validating the native ABI.
func parseWindowsUDP4Table(ctx context.Context, data []byte, request Request) ([]SocketFact, error) {
	count, rows, err := windowsEndpointTableRows(data, windowsUDP4RowSize)
	if err != nil {
		return nil, err
	}
	facts := make([]SocketFact, 0)
	for index := 0; index < count; index++ {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		row := rows[index*windowsUDP4RowSize : (index+1)*windowsUDP4RowSize]
		port, err := windowsEndpointPort(row[4:8])
		if err != nil {
			return nil, fmt.Errorf("UDP4 endpoint row %d: %w", index, err)
		}
		if !requestHasSocket(request, SocketProtocolUDP, port) {
			continue
		}
		address := netip.AddrFrom4([4]byte(row[0:4]))
		if address != request.Candidate() && address != netip.IPv4Unspecified() {
			continue
		}
		facts = append(facts, SocketFact{Protocol: SocketProtocolUDP, Address: address, Port: port, IPv6Only: IPv6OnlyNotApplicable})
	}
	return facts, nil
}

// parseWindowsUDP6Table retains requested wildcard endpoints with unknown dual-stack behavior.
func parseWindowsUDP6Table(ctx context.Context, data []byte, request Request) ([]SocketFact, error) {
	count, rows, err := windowsEndpointTableRows(data, windowsUDP6RowSize)
	if err != nil {
		return nil, err
	}
	facts := make([]SocketFact, 0)
	for index := 0; index < count; index++ {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		row := rows[index*windowsUDP6RowSize : (index+1)*windowsUDP6RowSize]
		port, err := windowsEndpointPort(row[20:24])
		if err != nil {
			return nil, fmt.Errorf("UDP6 endpoint row %d: %w", index, err)
		}
		if !requestHasSocket(request, SocketProtocolUDP, port) {
			continue
		}
		address := netip.AddrFrom16([16]byte(row[0:16]))
		if address.Is4In6() {
			return nil, fmt.Errorf("UDP6 endpoint row %d uses an IPv4-mapped address", index)
		}
		if address != netip.IPv6Unspecified() {
			continue
		}
		if binary.LittleEndian.Uint32(row[16:20]) != 0 {
			return nil, fmt.Errorf("UDP6 wildcard row %d has a scope identifier", index)
		}
		facts = append(facts, SocketFact{Protocol: SocketProtocolUDP, Address: address, Port: port, IPv6Only: IPv6OnlyUnknown})
	}
	return facts, nil
}

// windowsEndpointTableRows validates count arithmetic and exact table length before any row slicing.
func windowsEndpointTableRows(data []byte, rowSize int) (int, []byte, error) {
	if rowSize <= 0 {
		return 0, nil, fmt.Errorf("endpoint table row size must be greater than zero")
	}
	if len(data) < 4 {
		return 0, nil, fmt.Errorf("endpoint table is smaller than its header")
	}
	count := uint64(binary.LittleEndian.Uint32(data[:4]))
	if count > maximumWindowsEndpointRows {
		return 0, nil, fmt.Errorf("endpoint table row count %d exceeds limit %d", count, maximumWindowsEndpointRows)
	}
	available := uint64(len(data) - 4)
	if count > available/uint64(rowSize) {
		return 0, nil, fmt.Errorf("endpoint table row count %d exceeds its %d-byte payload", count, available)
	}
	want := count * uint64(rowSize)
	if want != available {
		return 0, nil, fmt.Errorf("endpoint table has %d trailing bytes beyond %d rows", available-want, count)
	}
	return int(count), data[4:], nil
}

// windowsEndpointPort decodes the low network-order word and rejects nonzero reserved bytes.
func windowsEndpointPort(value []byte) (uint16, error) {
	if len(value) != 4 || value[2] != 0 || value[3] != 0 {
		return 0, fmt.Errorf("endpoint port has malformed reserved bytes")
	}
	port := binary.BigEndian.Uint16(value[:2])
	if port == 0 {
		return 0, fmt.Errorf("endpoint port is zero")
	}
	return port, nil
}

// allWindowsBytesZero verifies listener remote endpoints without integer alignment assumptions.
func allWindowsBytesZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

// windowsAddressFamilyBits provides bounded diagnostic labels for the only requested families.
func windowsAddressFamilyBits(family uint32) int {
	if family == windows.AF_INET6 {
		return 6
	}
	return 4
}
