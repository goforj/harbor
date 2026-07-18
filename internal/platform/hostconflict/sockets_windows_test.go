//go:build windows

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestWindowsEndpointRowABIMatchesIPHelper fixes every parser stride to Microsoft's native layouts.
func TestWindowsEndpointRowABIMatchesIPHelper(t *testing.T) {
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "TCP4", got: unsafe.Sizeof(windowsTCP4OwnerPIDRow{}), want: windowsTCP4RowSize},
		{name: "TCP6", got: unsafe.Sizeof(windowsTCP6OwnerPIDRow{}), want: windowsTCP6RowSize},
		{name: "UDP4", got: unsafe.Sizeof(windowsUDP4OwnerPIDRow{}), want: windowsUDP4RowSize},
		{name: "UDP6", got: unsafe.Sizeof(windowsUDP6OwnerPIDRow{}), want: windowsUDP6RowSize},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("%s row size = %d, want %d", test.name, test.got, test.want)
		}
	}
}

// TestReadWindowsEndpointTableUsesBoundedGrowth covers the native two-call and resize contracts.
func TestReadWindowsEndpointTableUsesBoundedGrowth(t *testing.T) {
	calls := 0
	data, err := readWindowsEndpointTable(context.Background(), func(buffer []byte, size *uint32) error {
		calls++
		if buffer == nil {
			*size = 4
			return windows.ERROR_INSUFFICIENT_BUFFER
		}
		binary.LittleEndian.PutUint32(buffer, 0)
		*size = 4
		return nil
	})
	if err != nil || calls != 2 || len(data) != 4 {
		t.Fatalf("readWindowsEndpointTable() = %v, calls %d, error %v", data, calls, err)
	}

	calls = 0
	data, err = readWindowsEndpointTable(context.Background(), func(buffer []byte, size *uint32) error {
		calls++
		switch calls {
		case 1:
			*size = 4
			return windows.ERROR_INSUFFICIENT_BUFFER
		case 2:
			*size = 8
			return windows.ERROR_INSUFFICIENT_BUFFER
		default:
			binary.LittleEndian.PutUint32(buffer, 0)
			*size = 4
			return nil
		}
	})
	if err != nil || calls != 3 || len(data) != 4 {
		t.Fatalf("readWindowsEndpointTable(resize) = %v, calls %d, error %v", data, calls, err)
	}
}

// TestReadWindowsEndpointTableRejectsMalformedSizesAndStatuses exercises every allocation boundary.
func TestReadWindowsEndpointTableRejectsMalformedSizesAndStatuses(t *testing.T) {
	sentinel := errors.New("fixture failure")
	tests := []struct {
		name string
		call windowsEndpointTableCall
		want string
	}{
		{
			name: "unexpected size success",
			call: func([]byte, *uint32) error { return nil },
			want: "unexpectedly succeeded",
		},
		{
			name: "size query failure",
			call: func([]byte, *uint32) error { return sentinel },
			want: sentinel.Error(),
		},
		{
			name: "undersized",
			call: func(_ []byte, size *uint32) error { *size = 3; return windows.ERROR_INSUFFICIENT_BUFFER },
			want: "smaller than",
		},
		{
			name: "oversized",
			call: func(_ []byte, size *uint32) error {
				*size = maximumWindowsEndpointTableSize + 1
				return windows.ERROR_INSUFFICIENT_BUFFER
			},
			want: "exceeds limit",
		},
		{
			name: "non-growing resize",
			call: func(buffer []byte, size *uint32) error { *size = 4; return windows.ERROR_INSUFFICIENT_BUFFER },
			want: "did not grow",
		},
		{
			name: "reported beyond buffer",
			call: func(buffer []byte, size *uint32) error {
				if buffer == nil {
					*size = 4
					return windows.ERROR_INSUFFICIENT_BUFFER
				}
				*size = 5
				return nil
			},
			want: "invalid endpoint table size",
		},
		{
			name: "reported short header",
			call: func(buffer []byte, size *uint32) error {
				if buffer == nil {
					*size = 4
					return windows.ERROR_INSUFFICIENT_BUFFER
				}
				*size = 3
				return nil
			},
			want: "invalid endpoint table size",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if data, err := readWindowsEndpointTable(context.Background(), test.call); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readWindowsEndpointTable() = %v, error %v, want containing %q", data, err, test.want)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if data, err := readWindowsEndpointTable(canceled, func([]byte, *uint32) error {
		t.Fatal("canceled read invoked native call")
		return nil
	}); data != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("readWindowsEndpointTable(canceled) = %v, %v", data, err)
	}
}

// TestWindowsEndpointTableRowsRejectsOverflowTruncationAndTrailingData protects all row slicing.
func TestWindowsEndpointTableRowsRejectsOverflowTruncationAndTrailingData(t *testing.T) {
	valid := windowsTestEndpointTable(t, windowsUDP4RowSize, make([]byte, windowsUDP4RowSize))
	if count, rows, err := windowsEndpointTableRows(valid, windowsUDP4RowSize); err != nil || count != 1 || len(rows) != windowsUDP4RowSize {
		t.Fatalf("windowsEndpointTableRows() = %d, %d, %v", count, len(rows), err)
	}
	tests := [][]byte{
		{0, 0, 0},
		func() []byte { data := make([]byte, 4); binary.LittleEndian.PutUint32(data, ^uint32(0)); return data }(),
		func() []byte {
			data := make([]byte, 4+windowsUDP4RowSize)
			binary.LittleEndian.PutUint32(data, 2)
			return data
		}(),
		func() []byte { data := make([]byte, 5); binary.LittleEndian.PutUint32(data, 0); return data }(),
	}
	for index, data := range tests {
		if count, rows, err := windowsEndpointTableRows(data, windowsUDP4RowSize); err == nil {
			t.Errorf("fixture %d windowsEndpointTableRows() = %d, %v, want error", index, count, rows)
		}
	}
	if count, rows, err := windowsEndpointTableRows(make([]byte, 4), 0); err == nil {
		t.Fatalf("windowsEndpointTableRows(zero stride) = %d, %v, want error", count, rows)
	}
}

// TestParseWindowsEndpointTablesRetainsRequestedConflictShapes covers both protocols and families.
func TestParseWindowsEndpointTablesRetainsRequestedConflictShapes(t *testing.T) {
	request := mustRequest(t)
	tcp4Rows := [][]byte{
		windowsTestTCP4Row(request.Candidate(), 443),
		windowsTestTCP4Row(netip.IPv4Unspecified(), 53),
		windowsTestTCP4Row(netip.MustParseAddr("127.77.0.99"), 443),
		windowsTestTCP4Row(request.Candidate(), 8443),
	}
	tcp4, err := parseWindowsTCP4Table(context.Background(), windowsTestEndpointTable(t, windowsTCP4RowSize, tcp4Rows...), request)
	if err != nil || len(tcp4) != 2 || !tcp4[0].TCPAccepting || tcp4[0].IPv6Only != IPv6OnlyNotApplicable {
		t.Fatalf("parseWindowsTCP4Table() = %#v, %v", tcp4, err)
	}
	tcp6, err := parseWindowsTCP6Table(context.Background(), windowsTestEndpointTable(t, windowsTCP6RowSize, windowsTestTCP6Row(netip.IPv6Unspecified(), 443)), request)
	if err != nil || len(tcp6) != 1 || tcp6[0].IPv6Only != IPv6OnlyUnknown || !tcp6[0].TCPAccepting {
		t.Fatalf("parseWindowsTCP6Table() = %#v, %v", tcp6, err)
	}
	udp4Rows := [][]byte{
		windowsTestUDP4Row(request.Candidate(), 53),
		windowsTestUDP4Row(netip.IPv4Unspecified(), 53),
		windowsTestUDP4Row(netip.MustParseAddr("127.77.0.99"), 53),
	}
	udp4, err := parseWindowsUDP4Table(context.Background(), windowsTestEndpointTable(t, windowsUDP4RowSize, udp4Rows...), request)
	if err != nil || len(udp4) != 2 || udp4[0].TCPAccepting {
		t.Fatalf("parseWindowsUDP4Table() = %#v, %v", udp4, err)
	}
	udp6, err := parseWindowsUDP6Table(context.Background(), windowsTestEndpointTable(t, windowsUDP6RowSize, windowsTestUDP6Row(netip.IPv6Unspecified(), 53)), request)
	if err != nil || len(udp6) != 1 || udp6[0].IPv6Only != IPv6OnlyUnknown {
		t.Fatalf("parseWindowsUDP6Table() = %#v, %v", udp6, err)
	}

	observation := safeWindowsObservation(t)
	observation.Sockets.Endpoints = tcp6
	assessment, err := observation.Classify()
	if err != nil || assessment.Sockets != StateConflict {
		t.Fatalf("Classify(unknown IPv6 wildcard) = %#v, %v", assessment, err)
	}
}

// TestParseWindowsEndpointTablesRejectsMalformedRows covers state, ports, remote endpoints, mapping, and scopes.
func TestParseWindowsEndpointTablesRejectsMalformedRows(t *testing.T) {
	request := mustRequest(t)
	tcp4 := windowsTestTCP4Row(request.Candidate(), 443)
	tcp6 := windowsTestTCP6Row(netip.IPv6Unspecified(), 443)
	udp6 := windowsTestUDP6Row(netip.IPv6Unspecified(), 53)
	tests := []struct {
		name string
		call func([]byte) error
		row  []byte
	}{
		{name: "TCP4 state", row: func() []byte {
			row := append([]byte(nil), tcp4...)
			binary.LittleEndian.PutUint32(row[0:4], 1)
			return row
		}(), call: func(row []byte) error {
			_, err := parseWindowsTCP4Table(context.Background(), windowsTestEndpointTable(t, windowsTCP4RowSize, row), request)
			return err
		}},
		{name: "TCP4 reserved port", row: func() []byte { row := append([]byte(nil), tcp4...); row[10] = 1; return row }(), call: func(row []byte) error {
			_, err := parseWindowsTCP4Table(context.Background(), windowsTestEndpointTable(t, windowsTCP4RowSize, row), request)
			return err
		}},
		{name: "TCP4 remote", row: func() []byte { row := append([]byte(nil), tcp4...); row[12] = 1; return row }(), call: func(row []byte) error {
			_, err := parseWindowsTCP4Table(context.Background(), windowsTestEndpointTable(t, windowsTCP4RowSize, row), request)
			return err
		}},
		{name: "TCP6 state", row: func() []byte {
			row := append([]byte(nil), tcp6...)
			binary.LittleEndian.PutUint32(row[48:52], 1)
			return row
		}(), call: func(row []byte) error {
			_, err := parseWindowsTCP6Table(context.Background(), windowsTestEndpointTable(t, windowsTCP6RowSize, row), request)
			return err
		}},
		{name: "TCP6 remote", row: func() []byte { row := append([]byte(nil), tcp6...); row[24] = 1; return row }(), call: func(row []byte) error {
			_, err := parseWindowsTCP6Table(context.Background(), windowsTestEndpointTable(t, windowsTCP6RowSize, row), request)
			return err
		}},
		{name: "TCP6 mapped", row: windowsTestTCP6Row(netip.MustParseAddr("::ffff:127.77.0.10"), 443), call: func(row []byte) error {
			_, err := parseWindowsTCP6Table(context.Background(), windowsTestEndpointTable(t, windowsTCP6RowSize, row), request)
			return err
		}},
		{name: "TCP6 wildcard scope", row: func() []byte {
			row := append([]byte(nil), tcp6...)
			binary.LittleEndian.PutUint32(row[16:20], 1)
			return row
		}(), call: func(row []byte) error {
			_, err := parseWindowsTCP6Table(context.Background(), windowsTestEndpointTable(t, windowsTCP6RowSize, row), request)
			return err
		}},
		{name: "UDP6 mapped", row: windowsTestUDP6Row(netip.MustParseAddr("::ffff:127.77.0.10"), 53), call: func(row []byte) error {
			_, err := parseWindowsUDP6Table(context.Background(), windowsTestEndpointTable(t, windowsUDP6RowSize, row), request)
			return err
		}},
		{name: "UDP6 wildcard scope", row: func() []byte {
			row := append([]byte(nil), udp6...)
			binary.LittleEndian.PutUint32(row[16:20], 1)
			return row
		}(), call: func(row []byte) error {
			_, err := parseWindowsUDP6Table(context.Background(), windowsTestEndpointTable(t, windowsUDP6RowSize, row), request)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(test.row); err == nil {
				t.Fatal("parser error = nil")
			}
		})
	}
}

// TestParseWindowsEndpointTableHonorsCancellation bounds large native decoding work.
func TestParseWindowsEndpointTableHonorsCancellation(t *testing.T) {
	request := mustRequest(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	data := windowsTestEndpointTable(t, windowsTCP4RowSize, windowsTestTCP4Row(request.Candidate(), 443))
	if facts, err := parseWindowsTCP4Table(canceled, data, request); facts != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("parseWindowsTCP4Table(canceled) = %#v, %v", facts, err)
	}
}

// windowsTestEndpointTable builds one exact DWORD-counted native table fixture.
func windowsTestEndpointTable(t *testing.T, rowSize int, rows ...[]byte) []byte {
	t.Helper()
	data := make([]byte, 4+rowSize*len(rows))
	binary.LittleEndian.PutUint32(data[:4], uint32(len(rows)))
	for index, row := range rows {
		if len(row) != rowSize {
			t.Fatalf("row %d size = %d, want %d", index, len(row), rowSize)
		}
		copy(data[4+index*rowSize:], row)
	}
	return data
}

// windowsTestTCP4Row builds one listening owner-PID row with a non-authoritative PID.
func windowsTestTCP4Row(address netip.Addr, port uint16) []byte {
	row := make([]byte, windowsTCP4RowSize)
	binary.LittleEndian.PutUint32(row[0:4], windowsTCPStateListen)
	value := address.As4()
	copy(row[4:8], value[:])
	windowsSetTestEndpointPort(row[8:12], port)
	binary.LittleEndian.PutUint32(row[20:24], 4242)
	return row
}

// windowsTestTCP6Row builds one listening IPv6 owner-PID row.
func windowsTestTCP6Row(address netip.Addr, port uint16) []byte {
	row := make([]byte, windowsTCP6RowSize)
	value := address.As16()
	copy(row[0:16], value[:])
	windowsSetTestEndpointPort(row[20:24], port)
	binary.LittleEndian.PutUint32(row[48:52], windowsTCPStateListen)
	binary.LittleEndian.PutUint32(row[52:56], 4242)
	return row
}

// windowsTestUDP4Row builds one IPv4 owner-PID endpoint row.
func windowsTestUDP4Row(address netip.Addr, port uint16) []byte {
	row := make([]byte, windowsUDP4RowSize)
	value := address.As4()
	copy(row[0:4], value[:])
	windowsSetTestEndpointPort(row[4:8], port)
	binary.LittleEndian.PutUint32(row[8:12], 4242)
	return row
}

// windowsTestUDP6Row builds one IPv6 owner-PID endpoint row.
func windowsTestUDP6Row(address netip.Addr, port uint16) []byte {
	row := make([]byte, windowsUDP6RowSize)
	value := address.As16()
	copy(row[0:16], value[:])
	windowsSetTestEndpointPort(row[20:24], port)
	binary.LittleEndian.PutUint32(row[24:28], 4242)
	return row
}

// windowsSetTestEndpointPort writes the API's network-order low word representation.
func windowsSetTestEndpointPort(destination []byte, port uint16) {
	binary.BigEndian.PutUint16(destination[:2], port)
}
