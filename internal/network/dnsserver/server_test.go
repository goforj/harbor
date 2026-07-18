package dnsserver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// TestServerServesExactAuthorityOverUDPAndTCP verifies the public wire contract on both transports.
func TestServerServesExactAuthorityOverUDPAndTCP(t *testing.T) {
	server, address := startTestServer(t, []Record{
		{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")},
		{Name: "mysql.orders.test", Address: netip.MustParseAddr("127.77.0.10")},
		{Name: "mysql.billing.test", Address: netip.MustParseAddr("127.77.0.11")},
	}, 19*time.Second)

	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			assertAResponse(t, exchange(t, address, network, "orders.test.", dns.TypeA, dns.ClassINET), "127.0.0.1", 19)
			assertAResponse(t, exchange(t, address, network, "mysql.orders.test.", dns.TypeA, dns.ClassINET), "127.77.0.10", 19)
			assertAResponse(t, exchange(t, address, network, "MYSQL.BILLING.TEST.", dns.TypeA, dns.ClassINET), "127.77.0.11", 19)
		})
	}

	active, ok := server.Address()
	if !ok || active != address {
		t.Errorf("Address() = %s, %t, want %s, true", active, ok, address)
	}
}

// TestServerResponsePolicy verifies NXDOMAIN, NODATA, and REFUSED remain distinct outcomes.
func TestServerResponsePolicy(t *testing.T) {
	_, address := startTestServer(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	tests := []struct {
		name          string
		queryName     string
		queryType     uint16
		queryClass    uint16
		wantRcode     int
		wantAuthority bool
		wantSOA       bool
	}{
		{name: "known AAAA is NODATA", queryName: "orders.test.", queryType: dns.TypeAAAA, queryClass: dns.ClassINET, wantRcode: dns.RcodeSuccess, wantAuthority: true, wantSOA: true},
		{name: "known TXT is NODATA", queryName: "orders.test.", queryType: dns.TypeTXT, queryClass: dns.ClassINET, wantRcode: dns.RcodeSuccess, wantAuthority: true, wantSOA: true},
		{name: "unknown A is NXDOMAIN", queryName: "missing.test.", queryType: dns.TypeA, queryClass: dns.ClassINET, wantRcode: dns.RcodeNameError, wantAuthority: true, wantSOA: true},
		{name: "unknown AAAA is NXDOMAIN", queryName: "missing.test.", queryType: dns.TypeAAAA, queryClass: dns.ClassINET, wantRcode: dns.RcodeNameError, wantAuthority: true, wantSOA: true},
		{name: "zone apex is NODATA", queryName: "test.", queryType: dns.TypeA, queryClass: dns.ClassINET, wantRcode: dns.RcodeSuccess, wantAuthority: true, wantSOA: true},
		{name: "foreign name is refused", queryName: "example.com.", queryType: dns.TypeA, queryClass: dns.ClassINET, wantRcode: dns.RcodeRefused},
		{name: "foreign class is refused", queryName: "orders.test.", queryType: dns.TypeA, queryClass: dns.ClassCHAOS, wantRcode: dns.RcodeRefused},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := exchange(t, address, "udp", test.queryName, test.queryType, test.queryClass)
			if response.Rcode != test.wantRcode {
				t.Errorf("Rcode = %s, want %s", dns.RcodeToString[response.Rcode], dns.RcodeToString[test.wantRcode])
			}
			if response.Authoritative != test.wantAuthority {
				t.Errorf("Authoritative = %t, want %t", response.Authoritative, test.wantAuthority)
			}
			if len(response.Answer) != 0 {
				t.Errorf("Answer = %#v, want NODATA", response.Answer)
			}
			if gotSOA := len(response.Ns) == 1 && response.Ns[0].Header().Rrtype == dns.TypeSOA; gotSOA != test.wantSOA {
				t.Errorf("SOA authority = %t, want %t; authority = %#v", gotSOA, test.wantSOA, response.Ns)
			}
		})
	}
}

// TestServerPreservesZoneAndEmptyNonterminalExistence keeps NXDOMAIN scoped to absent names.
func TestServerPreservesZoneAndEmptyNonterminalExistence(t *testing.T) {
	_, address := startTestServer(t, []Record{{Name: "mysql.orders.test", Address: netip.MustParseAddr("127.0.0.11")}}, DefaultTTL)

	for _, name := range []string{"test.", "orders.test."} {
		response := exchange(t, address, "udp", name, dns.TypeA, dns.ClassINET)
		if response.Rcode != dns.RcodeSuccess || !response.Authoritative || len(response.Answer) != 0 {
			t.Errorf("A response for existing name %s = %#v, want authoritative NODATA", name, response)
		}
		if len(response.Ns) != 1 || response.Ns[0].Header().Rrtype != dns.TypeSOA {
			t.Errorf("authority for existing name %s = %#v, want SOA", name, response.Ns)
		}
	}

	response := exchange(t, address, "tcp", testZoneName, dns.TypeSOA, dns.ClassINET)
	if response.Rcode != dns.RcodeSuccess || !response.Authoritative || len(response.Answer) != 1 || response.Answer[0].Header().Rrtype != dns.TypeSOA {
		t.Fatalf("zone SOA response = %#v", response)
	}
	soa := response.Answer[0].(*dns.SOA)
	if soa.Hdr.Ttl != uint32(DefaultTTL/time.Second) || soa.Minttl != uint32(DefaultTTL/time.Second) {
		t.Errorf("zone SOA TTLs = %d/%d, want %d", soa.Hdr.Ttl, soa.Minttl, uint32(DefaultTTL/time.Second))
	}
}

// TestServerCompressesMaximumNameResponses keeps legacy UDP replies within their advertised limit.
func TestServerCompressesMaximumNameResponses(t *testing.T) {
	name := strings.Join([]string{
		strings.Repeat("a", 63),
		strings.Repeat("b", 63),
		strings.Repeat("c", 63),
		strings.Repeat("d", 56),
		"test",
	}, ".")
	if len(name) != 253 {
		t.Fatalf("maximum fixture name length = %d, want 253", len(name))
	}
	server, address := startTestServer(t, []Record{{Name: name, Address: netip.MustParseAddr("127.0.0.19")}}, DefaultTTL)
	response := exchange(t, address, "udp", dns.Fqdn(name), dns.TypeA, dns.ClassINET)
	assertAResponse(t, response, "127.0.0.19", uint32(DefaultTTL/time.Second))
	request := new(dns.Msg)
	request.SetQuestion(dns.Fqdn(name), dns.TypeA)
	built := server.responseFor(request)
	built.Truncate(udpPayloadSize(request))
	packed, err := built.Pack()
	if err != nil {
		t.Fatalf("response Pack() error = %v", err)
	}
	if len(packed) > dns.MinMsgSize || built.Truncated {
		t.Fatalf("maximum-name response length/truncation = %d/%t, want <= %d/false", len(packed), built.Truncated, dns.MinMsgSize)
	}
}

// TestServerAcceptsOneEDNSRecord verifies bounded clients can advertise modern UDP sizes.
func TestServerAcceptsOneEDNSRecord(t *testing.T) {
	_, address := startTestServer(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	request.SetEdns0(maxUDPRequestBytes, false)
	response := exchangeMessage(t, address, "udp", request)
	assertAResponse(t, response, "127.0.0.1", uint32(DefaultTTL/time.Second))
	if responseOPT := response.IsEdns0(); responseOPT == nil || responseOPT.UDPSize() != maxUDPRequestBytes {
		t.Fatalf("response EDNS = %#v, want UDP size %d", responseOPT, maxUDPRequestBytes)
	}

	request.IsEdns0().SetVersion(1)
	response = exchangeMessage(t, address, "udp", request)
	if response.Rcode != dns.RcodeBadVers || response.IsEdns0() == nil {
		t.Fatalf("EDNS version response = %#v, want BADVERS with OPT", response)
	}
}

// TestServerRejectsStructurallyInvalidQueries verifies invalid operations cannot mutate authority.
func TestServerRejectsStructurallyInvalidQueries(t *testing.T) {
	_, address := startTestServer(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	tests := []struct {
		name   string
		mutate func(*dns.Msg)
	}{
		{name: "notify opcode", mutate: func(message *dns.Msg) { message.Opcode = dns.OpcodeNotify }},
		{name: "nonzero rcode", mutate: func(message *dns.Msg) { message.Rcode = dns.RcodeServerFailure }},
		{name: "reserved zero bit", mutate: func(message *dns.Msg) { message.Zero = true }},
		{name: "truncated request", mutate: func(message *dns.Msg) { message.Truncated = true }},
		{name: "answer section", mutate: func(message *dns.Msg) { message.Answer = []dns.RR{testARecord("orders.test.")} }},
		{name: "authority section", mutate: func(message *dns.Msg) { message.Ns = []dns.RR{testARecord("orders.test.")} }},
		{name: "non EDNS extra", mutate: func(message *dns.Msg) { message.Extra = []dns.RR{testARecord("orders.test.")} }},
		{name: "non-root EDNS owner", mutate: func(message *dns.Msg) {
			message.SetEdns0(maxUDPRequestBytes, false)
			message.IsEdns0().Hdr.Name = "orders.test."
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := new(dns.Msg)
			request.SetQuestion("orders.test.", dns.TypeA)
			test.mutate(request)
			response := exchangeMessage(t, address, "udp", request)
			if response.Rcode != dns.RcodeFormatError {
				t.Errorf("Rcode = %s, want FORMERR", dns.RcodeToString[response.Rcode])
			}
		})
	}
}

// TestServerIgnoresMalformedAndOversizedUDP verifies hostile datagrams do not stop valid service.
func TestServerIgnoresMalformedAndOversizedUDP(t *testing.T) {
	_, address := startTestServer(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	connection, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(address))
	if err != nil {
		t.Fatalf("DialUDP() error = %v", err)
	}
	defer connection.Close()

	assertUDPTimeout(t, connection, []byte{0x01, 0x02})
	allocationProbe := make([]byte, dnsHeaderBytes)
	binary.BigEndian.PutUint16(allocationProbe[4:6], ^uint16(0))
	assertUDPTimeout(t, connection, allocationProbe)
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	packed, err := request.Pack()
	if err != nil {
		t.Fatalf("Pack() error = %v", err)
	}
	oversized := append(packed, make([]byte, maxUDPRequestBytes+1-len(packed))...)
	assertUDPTimeout(t, connection, oversized)

	assertAResponse(t, exchange(t, address, "udp", "orders.test.", dns.TypeA, dns.ClassINET), "127.0.0.1", uint32(DefaultTTL/time.Second))
}

// TestAdmissibleWireHeaderBoundsParserAllocations covers every pre-decode count and direction limit.
func TestAdmissibleWireHeaderBoundsParserAllocations(t *testing.T) {
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	packed, err := request.Pack()
	if err != nil {
		t.Fatalf("Pack() error = %v", err)
	}
	if !admissibleWireHeader(packed) {
		t.Fatal("admissibleWireHeader(valid query) = false")
	}

	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{name: "short"},
		{name: "response", mutate: func(packet []byte) { packet[2] |= 0x80 }},
		{name: "questions", mutate: func(packet []byte) { binary.BigEndian.PutUint16(packet[4:6], 3) }},
		{name: "answers", mutate: func(packet []byte) { binary.BigEndian.PutUint16(packet[6:8], 2) }},
		{name: "authorities", mutate: func(packet []byte) { binary.BigEndian.PutUint16(packet[8:10], 2) }},
		{name: "extras", mutate: func(packet []byte) { binary.BigEndian.PutUint16(packet[10:12], 3) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := append([]byte(nil), packed...)
			if test.name == "short" {
				candidate = candidate[:dnsHeaderBytes-1]
			} else {
				test.mutate(candidate)
			}
			if admissibleWireHeader(candidate) {
				t.Fatal("admissibleWireHeader() = true, want false")
			}
		})
	}
}

// TestServerRejectsOversizedAndTruncatedTCPFrames verifies length bounds apply before allocation.
func TestServerRejectsOversizedAndTruncatedTCPFrames(t *testing.T) {
	_, address := startTestServer(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)

	oversized, err := net.DialTimeout("tcp4", address.String(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout() error = %v", err)
	}
	if err := binary.Write(oversized, binary.BigEndian, uint16(maxTCPRequestBytes+1)); err != nil {
		t.Fatalf("Write(length) error = %v", err)
	}
	if err := oversized.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	if _, err := io.ReadAll(oversized); err != nil && !isNetworkClose(err) {
		t.Errorf("ReadAll(oversized) error = %v", err)
	}
	_ = oversized.Close()

	truncated, err := net.DialTimeout("tcp4", address.String(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout() error = %v", err)
	}
	frame := []byte{0, 20, 1, 2, 3}
	if _, err := truncated.Write(frame); err != nil {
		t.Fatalf("Write(truncated) error = %v", err)
	}
	_ = truncated.Close()

	assertAResponse(t, exchange(t, address, "tcp", "orders.test.", dns.TypeA, dns.ClassINET), "127.0.0.1", uint32(DefaultTTL/time.Second))
}

// TestServerReplacesSnapshotsAtomicallyWithConcurrentQueries verifies readers never observe mixed data.
func TestServerReplacesSnapshotsAtomicallyWithConcurrentQueries(t *testing.T) {
	first := mustSnapshot(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.11")}}, time.Second)
	second := mustSnapshot(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.12")}}, 2*time.Second)
	server, address := startServerWithSnapshot(t, first)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 8)
	var queries sync.WaitGroup
	for range 8 {
		queries.Add(1)
		go func() {
			defer queries.Done()
			client := &dns.Client{Net: "udp", Timeout: time.Second}
			for range 200 {
				request := new(dns.Msg)
				request.SetQuestion("orders.test.", dns.TypeA)
				response, _, err := client.ExchangeContext(ctx, request, address.String())
				if err != nil {
					errCh <- err
					return
				}
				answer, ok := response.Answer[0].(*dns.A)
				if !ok {
					errCh <- fmt.Errorf("answer = %#v, want A", response.Answer)
					return
				}
				tuple := answer.A.String() + "/" + fmt.Sprint(answer.Hdr.Ttl)
				if tuple != "127.0.0.11/1" && tuple != "127.0.0.12/2" {
					errCh <- fmt.Errorf("observed mixed snapshot tuple %s", tuple)
					return
				}
			}
		}()
	}
	for range 500 {
		if err := server.Replace(second); err != nil {
			t.Fatalf("Replace(second) error = %v", err)
		}
		if err := server.Replace(first); err != nil {
			t.Fatalf("Replace(first) error = %v", err)
		}
	}
	queries.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent query error = %v", err)
	}
}

// TestServerRejectsInvalidReplacementWithoutChangingRecords verifies validation precedes publication.
func TestServerRejectsInvalidReplacementWithoutChangingRecords(t *testing.T) {
	server, address := startTestServer(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	if err := server.Replace(Snapshot{}); err == nil {
		t.Fatal("Replace(zero snapshot) error = nil, want validation error")
	}
	assertAResponse(t, exchange(t, address, "udp", "orders.test.", dns.TypeA, dns.ClassINET), "127.0.0.1", uint32(DefaultTTL/time.Second))
}

// TestServerLifecycleSupportsCloseAndRestart verifies listeners join before an address is reused.
func TestServerLifecycleSupportsCloseAndRestart(t *testing.T) {
	snapshot := mustSnapshot(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	server, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), 0), snapshot)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if _, ok := server.Address(); ok {
		t.Fatal("Address() reported a listener before Start")
	}
	if err := server.Wait(context.Background()); err != nil {
		t.Errorf("Wait(before Start) error = %v", err)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Errorf("Close(before Start) error = %v", err)
	}

	first, err := server.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := server.Start(context.Background()); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Start() error = %v, want ErrRunning", err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer waitCancel()
	if err := server.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait(running) error = %v, want deadline", err)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, ok := server.Address(); ok {
		t.Fatal("Address() retained a listener after Close")
	}
	if err := server.Close(context.Background()); err != nil {
		t.Errorf("second Close() error = %v", err)
	}
	assertPortReleased(t, first)

	second, err := server.Start(context.Background())
	if err != nil {
		t.Fatalf("restart Start() error = %v", err)
	}
	assertAResponse(t, exchange(t, second, "tcp", "orders.test.", dns.TypeA, dns.ClassINET), "127.0.0.1", uint32(DefaultTTL/time.Second))
	if err := server.Close(context.Background()); err != nil {
		t.Fatalf("restart Close() error = %v", err)
	}
}

// TestServerParentCancellationStopsBothTransports verifies daemon cancellation cannot orphan DNS.
func TestServerParentCancellationStopsBothTransports(t *testing.T) {
	snapshot := mustSnapshot(t, nil, DefaultTTL)
	server, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), 0), snapshot)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	parent, cancel := context.WithCancel(context.Background())
	address, err := server.Start(parent)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancel()
	if err := server.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	assertPortReleased(t, address)
}

// TestServerRepeatedLifecycleJoinsGenerations exercises restart cleanup enough to expose retained loops.
func TestServerRepeatedLifecycleJoinsGenerations(t *testing.T) {
	snapshot := mustSnapshot(t, nil, DefaultTTL)
	server, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), 0), snapshot)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	for generation := range 25 {
		address, err := server.Start(context.Background())
		if err != nil {
			t.Fatalf("Start(generation %d) error = %v", generation, err)
		}
		if err := server.Close(context.Background()); err != nil {
			t.Fatalf("Close(generation %d) error = %v", generation, err)
		}
		if err := server.Wait(context.Background()); err != nil {
			t.Fatalf("Wait(generation %d) error = %v", generation, err)
		}
		assertPortReleased(t, address)
	}
}

// TestServerInputValidation covers nil, canceled, invalid config, and unavailable sockets.
func TestServerInputValidation(t *testing.T) {
	snapshot := mustSnapshot(t, nil, DefaultTTL)
	if _, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), 0), Snapshot{}); err == nil || !strings.Contains(err.Error(), "NewSnapshot") {
		t.Fatalf("NewServer(zero snapshot) error = %v, want snapshot validation", err)
	}
	configTests := []struct {
		name   string
		config Config
		want   string
	}{
		{name: "missing address", config: Config{}, want: "not IPv4 loopback"},
		{name: "public address", config: DefaultConfig(netip.MustParseAddr("192.0.2.1"), 0), want: "not IPv4 loopback"},
		{name: "IPv6 loopback", config: DefaultConfig(netip.IPv6Loopback(), 0), want: "not IPv4 loopback"},
		{name: "negative read timeout", config: Config{Address: netip.MustParseAddr("127.0.0.1"), ReadTimeout: -1}, want: "read timeout"},
		{name: "long read timeout", config: Config{Address: netip.MustParseAddr("127.0.0.1"), ReadTimeout: maxServerTimeout + time.Nanosecond}, want: "read timeout"},
		{name: "negative write timeout", config: Config{Address: netip.MustParseAddr("127.0.0.1"), WriteTimeout: -1}, want: "write timeout"},
		{name: "long shutdown timeout", config: Config{Address: netip.MustParseAddr("127.0.0.1"), ShutdownTimeout: maxServerTimeout + time.Nanosecond}, want: "shutdown timeout"},
	}
	for _, test := range configTests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.config.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
			if _, err := NewServer(test.config, snapshot); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewServer() error = %v, want containing %q", err, test.want)
			}
		})
	}

	server, err := NewServer(Config{Address: netip.MustParseAddr("127.0.0.1")}, snapshot)
	if err != nil {
		t.Fatalf("NewServer(defaulted timeouts) error = %v", err)
	}
	if _, err := server.Start(nil); err == nil {
		t.Fatal("Start(nil) error = nil")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.Start(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start(canceled) error = %v, want canceled", err)
	}
	if err := server.Close(nil); err == nil {
		t.Fatal("Close(nil) error = nil")
	}
	if err := server.Wait(nil); err == nil {
		t.Fatal("Wait(nil) error = nil")
	}

	tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenTCP() error = %v", err)
	}
	port := uint16(tcp.Addr().(*net.TCPAddr).Port)
	occupied, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), port), snapshot)
	if err != nil {
		t.Fatalf("NewServer(occupied) error = %v", err)
	}
	if _, err := occupied.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "bind TCP") {
		t.Fatalf("Start(occupied TCP) error = %v", err)
	}
	_ = tcp.Close()

	tcp, err = net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenTCP(UDP conflict probe) error = %v", err)
	}
	port = uint16(tcp.Addr().(*net.TCPAddr).Port)
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)})
	if err != nil {
		_ = tcp.Close()
		t.Fatalf("ListenUDP() error = %v", err)
	}
	if err := tcp.Close(); err != nil {
		_ = udp.Close()
		t.Fatalf("Close(TCP conflict probe) error = %v", err)
	}
	occupied, err = NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), port), snapshot)
	if err != nil {
		t.Fatalf("NewServer(occupied UDP) error = %v", err)
	}
	if _, err := occupied.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "bind UDP") {
		t.Fatalf("Start(occupied UDP) error = %v", err)
	}
	_ = udp.Close()
}

// TestAwaitStartupCleansUpListenerFailures verifies partial startup never leaks its reserved sockets.
func TestAwaitStartupCleansUpListenerFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		result protocolResult
		want   string
	}{
		{name: "listener error", result: protocolResult{protocol: "udp", err: boom}, want: "start udp listener"},
		{name: "listener clean exit", result: protocolResult{protocol: "tcp"}, want: "stopped during startup"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := unstartedRun(t)
			run.results <- test.result
			run.results <- protocolResult{protocol: "other"}
			err := awaitStartup(context.Background(), run)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("awaitStartup() error = %v, want containing %q", err, test.want)
			}
			select {
			case <-run.done:
			default:
				t.Fatal("awaitStartup() did not close run.done")
			}
			assertPortReleased(t, run.address)
		})
	}
}

// TestAwaitStartupHonorsCancellation verifies cancellation joins both startup goroutines.
func TestAwaitStartupHonorsCancellation(t *testing.T) {
	run := unstartedRun(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		run.results <- protocolResult{protocol: "udp"}
		run.results <- protocolResult{protocol: "tcp"}
	}()
	if err := awaitStartup(ctx, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitStartup() error = %v, want canceled", err)
	}
	assertPortReleased(t, run.address)
}

// TestCloseHonorsCallerCancellation verifies a caller can bound waiting on an unhealthy generation.
func TestCloseHonorsCallerCancellation(t *testing.T) {
	run := &serverRun{stop: make(chan struct{}), done: make(chan struct{})}
	server := &Server{run: run}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want canceled", err)
	}
	select {
	case <-run.stop:
	default:
		t.Fatal("Close() did not request a stop before returning")
	}
}

// TestShutdownProtocolsReportsUnstartedServers verifies dependency lifecycle errors stay visible.
func TestShutdownProtocolsReportsUnstartedServers(t *testing.T) {
	errs := shutdownProtocols(&serverRun{udp: new(dns.Server), tcp: new(dns.Server)}, time.Second)
	if len(errs) != 2 {
		t.Fatalf("shutdownProtocols() errors = %#v, want two", errs)
	}
}

// TestBoundedReaderRejectsReadFailures verifies malformed TCP frames fail without large allocations.
func TestBoundedReaderRejectsReadFailures(t *testing.T) {
	closedClient, closedServer := net.Pipe()
	_ = closedClient.Close()
	_ = closedServer.Close()
	reader := &boundedReader{stopping: new(atomic.Bool)}
	if _, err := reader.ReadTCP(closedServer, time.Second); err == nil {
		t.Fatal("ReadTCP(closed) error = nil")
	}

	client, server := net.Pipe()
	go func() {
		_, _ = client.Write([]byte{0, 8, 1, 2})
		_ = client.Close()
	}()
	if _, err := reader.ReadTCP(server, time.Second); err == nil {
		t.Fatal("ReadTCP(truncated) error = nil")
	}
	_ = server.Close()

	stopping := new(atomic.Bool)
	stopping.Store(true)
	reader = &boundedReader{stopping: stopping}
	left, right := net.Pipe()
	if _, err := reader.ReadTCP(left, time.Second); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ReadTCP(stopping) error = %v, want net.ErrClosed", err)
	}
	_ = left.Close()
	_ = right.Close()

	stopping = new(atomic.Bool)
	left, right = net.Pipe()
	hooked := &deadlineSignalConnection{Conn: left, stopping: stopping}
	reader = &boundedReader{stopping: stopping}
	if _, err := reader.ReadTCP(hooked, time.Second); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ReadTCP(shutdown race) error = %v, want net.ErrClosed", err)
	}
	_ = left.Close()
	_ = right.Close()
}

// TestLimitedListenerRejectsExcessConnections verifies connection admission remains bounded.
func TestLimitedListenerRejectsExcessConnections(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenTCP() error = %v", err)
	}
	limited := &limitedListener{Listener: listener, permits: make(chan struct{}, 1)}
	limited.permits <- struct{}{}
	accepted := make(chan error, 1)
	go func() {
		_, err := limited.Accept()
		accepted <- err
	}()

	client, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout() error = %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := client.Read(buffer); err == nil {
		t.Fatal("Read(excess connection) error = nil")
	}
	_ = client.Close()
	_ = limited.Close()
	if err := <-accepted; err == nil {
		t.Fatal("Accept(after Close) error = nil")
	}
}

// TestServerBoundsQueriesPerTCPConnection verifies retained clients cannot hold one connection forever.
func TestServerBoundsQueriesPerTCPConnection(t *testing.T) {
	_, address := startTestServer(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	raw, err := net.DialTimeout("tcp4", address.String(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout() error = %v", err)
	}
	connection := &dns.Conn{Conn: raw}
	defer connection.Close()
	for index := range maxTCPQueries {
		request := new(dns.Msg)
		request.SetQuestion("orders.test.", dns.TypeA)
		if err := connection.WriteMsg(request); err != nil {
			t.Fatalf("WriteMsg(%d) error = %v", index, err)
		}
		if _, err := connection.ReadMsg(); err != nil {
			t.Fatalf("ReadMsg(%d) error = %v", index, err)
		}
	}
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	writeErr := connection.WriteMsg(request)
	if writeErr == nil {
		if _, err := connection.ReadMsg(); err == nil {
			t.Fatal("query beyond TCP limit succeeded")
		}
	}
	assertAResponse(t, exchange(t, address, "udp", "orders.test.", dns.TypeA, dns.ClassINET), "127.0.0.1", uint32(DefaultTTL/time.Second))
}

// TestUDPProtocolBoundsAdmissionBeforeWorkers proves datagram floods cannot accumulate goroutines.
func TestUDPProtocolBoundsAdmissionBeforeWorkers(t *testing.T) {
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	defer connection.Close()
	release := make(chan struct{})
	released := sync.OnceFunc(func() { close(release) })
	defer released()
	var active atomic.Int64
	var peak atomic.Int64
	respond := func(request *dns.Msg) *dns.Msg {
		current := active.Add(1)
		defer active.Add(-1)
		for observed := peak.Load(); current > observed && !peak.CompareAndSwap(observed, current); observed = peak.Load() {
		}
		<-release
		response := new(dns.Msg)
		return response.SetReply(request)
	}
	started := make(chan struct{})
	protocol := newUDPProtocol(connection, time.Second, func() { close(started) }, respond)
	result := make(chan error, 1)
	go func() {
		result <- protocol.ActivateAndServe()
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("UDP protocol did not start")
	}
	if err := protocol.ActivateAndServe(); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("second ActivateAndServe() error = %v, want lifecycle error", err)
	}

	client, err := net.DialUDP("udp4", nil, connection.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP() error = %v", err)
	}
	defer client.Close()
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	packed, err := request.Pack()
	if err != nil {
		t.Fatalf("request Pack() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(protocol.permits) < maxUDPQueries && time.Now().Before(deadline) {
		if _, err := client.Write(packed); err != nil {
			t.Fatalf("client Write() error = %v", err)
		}
	}
	if admitted := len(protocol.permits); admitted != maxUDPQueries {
		t.Fatalf("admitted UDP queries = %d, want %d", admitted, maxUDPQueries)
	}
	for range maxUDPQueries {
		_, _ = client.Write(packed)
	}
	time.Sleep(10 * time.Millisecond)
	if gotPeak := peak.Load(); gotPeak != maxUDPQueries {
		t.Fatalf("peak UDP workers = %d, want %d", gotPeak, maxUDPQueries)
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := protocol.ShutdownContext(shutdown); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ShutdownContext(blocked workers) error = %v, want deadline", err)
	}
	canceled, cancelSecond := context.WithCancel(context.Background())
	cancelSecond()
	if err := protocol.ShutdownContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("second ShutdownContext(blocked workers) error = %v, want canceled", err)
	}
	released()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ActivateAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP protocol did not join admitted workers")
	}
	if err := protocol.ShutdownContext(context.Background()); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("ShutdownContext(stopped) error = %v, want lifecycle error", err)
	}
}

// TestUDPProtocolContainsPacketLocalFailures keeps malformed adapter output from ending admission.
func TestUDPProtocolContainsPacketLocalFailures(t *testing.T) {
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	defer connection.Close()
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	packed, err := request.Pack()
	if err != nil {
		t.Fatalf("request Pack() error = %v", err)
	}
	remote := netip.MustParseAddrPort("127.0.0.1:43101")

	protocol := newUDPProtocol(connection, time.Second, func() {}, func(*dns.Msg) *dns.Msg { return nil })
	invokeUDPHandler(t, protocol, packed, remote)

	protocol.respond = func(request *dns.Msg) *dns.Msg {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeBadVers
		return response
	}
	invokeUDPHandler(t, protocol, packed, remote)

	protocol.respond = func(request *dns.Msg) *dns.Msg {
		response := new(dns.Msg)
		return response.SetReply(request)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("connection Close() error = %v", err)
	}
	invokeUDPHandler(t, protocol, packed, remote)
}

// TestUDPProtocolReportsUnexpectedSocketClosure distinguishes failure from requested shutdown.
func TestUDPProtocolReportsUnexpectedSocketClosure(t *testing.T) {
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("connection Close() error = %v", err)
	}
	protocol := newUDPProtocol(connection, time.Second, func() {}, func(*dns.Msg) *dns.Msg { return nil })
	if err := protocol.ActivateAndServe(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ActivateAndServe(closed socket) error = %v, want net.ErrClosed", err)
	}
}

// TestValidQueryCoversStructuralBranches keeps request policy explicit independently of the DNS parser.
func TestValidQueryCoversStructuralBranches(t *testing.T) {
	base := new(dns.Msg)
	base.SetQuestion("orders.test.", dns.TypeA)
	if !validQuery(base) {
		t.Fatal("validQuery(base) = false")
	}

	tests := []struct {
		name   string
		mutate func(*dns.Msg)
	}{
		{name: "missing question", mutate: func(message *dns.Msg) { message.Question = nil }},
		{name: "response bit", mutate: func(message *dns.Msg) { message.Response = true }},
		{name: "two questions", mutate: func(message *dns.Msg) { message.Question = append(message.Question, message.Question[0]) }},
		{name: "two extras", mutate: func(message *dns.Msg) {
			message.Extra = []dns.RR{testARecord("orders.test."), testARecord("orders.test.")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := base.Copy()
			test.mutate(message)
			if validQuery(message) {
				t.Fatal("validQuery() = true, want false")
			}
		})
	}
}

// TestUDPPayloadSizeBoundsClientAdvertisements keeps response allocation policy explicit.
func TestUDPPayloadSizeBoundsClientAdvertisements(t *testing.T) {
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	if size := udpPayloadSize(request); size != dns.MinMsgSize {
		t.Fatalf("udpPayloadSize(no EDNS) = %d, want %d", size, dns.MinMsgSize)
	}
	request.SetEdns0(128, false)
	if size := udpPayloadSize(request); size != dns.MinMsgSize {
		t.Fatalf("udpPayloadSize(small EDNS) = %d, want %d", size, dns.MinMsgSize)
	}
	request.IsEdns0().SetUDPSize(4096)
	if size := udpPayloadSize(request); size != maxUDPRequestBytes {
		t.Fatalf("udpPayloadSize(large EDNS) = %d, want %d", size, maxUDPRequestBytes)
	}
}

// TestServerLoopbackClientPolicy verifies unknown and non-loopback address types are denied.
func TestServerLoopbackClientPolicy(t *testing.T) {
	tests := []struct {
		name    string
		address net.Addr
		want    bool
	}{
		{name: "UDP loopback", address: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, want: true},
		{name: "TCP loopback", address: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, want: true},
		{name: "UDP public", address: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1)}},
		{name: "TCP public", address: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1)}},
		{name: "unknown", address: testAddress("opaque")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isLoopbackClient(test.address); got != test.want {
				t.Errorf("isLoopbackClient() = %t, want %t", got, test.want)
			}
		})
	}
}

// TestServerDropsNonLoopbackWriter verifies the access check runs before any response is emitted.
func TestServerDropsNonLoopbackWriter(t *testing.T) {
	snapshot := mustSnapshot(t, []Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	server, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), 0), snapshot)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	writer := &recordingResponseWriter{remote: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1)}}
	request := new(dns.Msg)
	request.SetQuestion("orders.test.", dns.TypeA)
	server.serveDNS(writer, request)
	if writer.response != nil {
		t.Errorf("response = %#v, want none", writer.response)
	}
}

// TestTerminalErrorClassifiesExpectedAndUnexpectedStops verifies lifecycle diagnostics remain precise.
func TestTerminalErrorClassifiesExpectedAndUnexpectedStops(t *testing.T) {
	boom := errors.New("boom")
	if err := terminalError(true, []protocolResult{{protocol: "udp"}, {protocol: "tcp", err: net.ErrClosed}}, nil); err != nil {
		t.Errorf("terminalError(expected) = %v", err)
	}
	if err := terminalError(false, []protocolResult{{protocol: "udp"}}, nil); err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Errorf("terminalError(unexpected nil) = %v", err)
	}
	if err := terminalError(false, []protocolResult{{protocol: "tcp", err: boom}}, []error{boom}); err == nil || !strings.Contains(err.Error(), "dns tcp listener") || !strings.Contains(err.Error(), "dns shutdown") {
		t.Errorf("terminalError(failures) = %v", err)
	}
}

// startTestServer builds and starts a server that test cleanup always joins.
func startTestServer(t *testing.T, records []Record, ttl time.Duration) (*Server, netip.AddrPort) {
	t.Helper()
	return startServerWithSnapshot(t, mustSnapshot(t, records, ttl))
}

// startServerWithSnapshot starts a prebuilt snapshot for replacement tests.
func startServerWithSnapshot(t *testing.T, snapshot Snapshot) (*Server, netip.AddrPort) {
	t.Helper()
	server, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), 0), snapshot)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	address, err := server.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return server, address
}

// unstartedRun reserves matching sockets for direct startup-abort tests.
func unstartedRun(t *testing.T) *serverRun {
	t.Helper()
	tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenTCP() error = %v", err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		_ = tcp.Close()
		t.Fatalf("ListenUDP() error = %v", err)
	}
	return &serverRun{
		address:     netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", port)),
		udpConn:     udp,
		tcpListener: tcp,
		results:     make(chan protocolResult, 2),
		done:        make(chan struct{}),
	}
}

// mustSnapshot creates a valid snapshot or fails its caller immediately.
func mustSnapshot(t *testing.T, records []Record, ttl time.Duration) Snapshot {
	t.Helper()
	snapshot, err := NewSnapshot(records, ttl)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	return snapshot
}

// exchange submits one DNS question and requires a response.
func exchange(t *testing.T, address netip.AddrPort, network string, name string, queryType uint16, queryClass uint16) *dns.Msg {
	t.Helper()
	request := new(dns.Msg)
	request.SetQuestion(name, queryType)
	request.Question[0].Qclass = queryClass
	return exchangeMessage(t, address, network, request)
}

// exchangeMessage submits a prepared DNS message through the requested wire transport.
func exchangeMessage(t *testing.T, address netip.AddrPort, network string, request *dns.Msg) *dns.Msg {
	t.Helper()
	client := &dns.Client{Net: network, Timeout: time.Second}
	response, _, err := client.ExchangeContext(context.Background(), request, address.String())
	if err != nil {
		t.Fatalf("ExchangeContext(%s) error = %v", network, err)
	}
	return response
}

// assertAResponse verifies one authoritative IPv4 answer and its bounded TTL.
func assertAResponse(t *testing.T, response *dns.Msg, address string, ttl uint32) {
	t.Helper()
	if response.Rcode != dns.RcodeSuccess || !response.Authoritative {
		t.Fatalf("response status = %s, authoritative %t", dns.RcodeToString[response.Rcode], response.Authoritative)
	}
	if len(response.Answer) != 1 {
		t.Fatalf("Answer = %#v, want one A record", response.Answer)
	}
	answer, ok := response.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("Answer[0] = %T, want *dns.A", response.Answer[0])
	}
	if got := answer.A.String(); got != address {
		t.Errorf("A address = %s, want %s", got, address)
	}
	if answer.Hdr.Ttl != ttl {
		t.Errorf("A TTL = %d, want %d", answer.Hdr.Ttl, ttl)
	}
}

// assertUDPTimeout sends one ignored packet and requires the server to remain silent.
func assertUDPTimeout(t *testing.T, connection *net.UDPConn, packet []byte) {
	t.Helper()
	if err := connection.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	if _, err := connection.Write(packet); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	buffer := make([]byte, 512)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("Read() error = nil, want timeout")
	} else if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
		t.Fatalf("Read() error = %v, want timeout", err)
	}
}

// invokeUDPHandler supplies the ownership tokens normally established by the UDP admission loop.
func invokeUDPHandler(t *testing.T, protocol *udpProtocol, packet []byte, remote netip.AddrPort) {
	t.Helper()
	buffer := protocol.buffers.Get().([]byte)
	copy(buffer, packet)
	protocol.permits <- struct{}{}
	protocol.workers.Add(1)
	protocol.handle(buffer[:len(packet)], remote)
	protocol.workers.Wait()
	if permits := len(protocol.permits); permits != 0 {
		t.Fatalf("UDP handler permits after return = %d, want zero", permits)
	}
}

// assertPortReleased proves Close joined both listener loops rather than abandoning sockets.
func assertPortReleased(t *testing.T, address netip.AddrPort) {
	t.Helper()
	tcp, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(address))
	if err != nil {
		t.Fatalf("ListenTCP(released) error = %v", err)
	}
	_ = tcp.Close()
	udp, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(address))
	if err != nil {
		t.Fatalf("ListenUDP(released) error = %v", err)
	}
	_ = udp.Close()
}

// testARecord creates a syntactically valid record for malformed-query sections.
func testARecord(name string) *dns.A {
	return &dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(127, 0, 0, 1)}
}

// isNetworkClose recognizes platform-specific errors caused by a peer closing a rejected frame.
func isNetworkClose(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "reset by peer") || strings.Contains(err.Error(), "forcibly closed")
}

// testAddress supplies an unsupported net.Addr implementation for access-policy tests.
type testAddress string

// Network identifies the test address as intentionally opaque.
func (a testAddress) Network() string {
	return "test"
}

// String returns the opaque address payload for diagnostics.
func (a testAddress) String() string {
	return string(a)
}

// recordingResponseWriter captures handler output without opening a network socket.
type recordingResponseWriter struct {
	remote   net.Addr
	response *dns.Msg
}

// deadlineSignalConnection reproduces shutdown between a reader's deadline write and blocking read.
type deadlineSignalConnection struct {
	net.Conn
	stopping *atomic.Bool
	once     sync.Once
}

// SetReadDeadline marks shutdown on the first call before preserving the wrapped socket behavior.
func (connection *deadlineSignalConnection) SetReadDeadline(deadline time.Time) error {
	connection.once.Do(func() { connection.stopping.Store(true) })
	return connection.Conn.SetReadDeadline(deadline)
}

// LocalAddr returns the loopback listener identity used by the test handler.
func (w *recordingResponseWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

// RemoteAddr returns the caller identity under test.
func (w *recordingResponseWriter) RemoteAddr() net.Addr {
	return w.remote
}

// WriteMsg captures the response for assertions.
func (w *recordingResponseWriter) WriteMsg(message *dns.Msg) error {
	w.response = message
	return nil
}

// Write rejects raw responses because the Harbor handler always writes typed messages.
func (w *recordingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("unexpected raw response")
}

// Close has no transport effect for the recording writer.
func (w *recordingResponseWriter) Close() error {
	return nil
}

// TsigStatus reports that the test request has no transaction signature.
func (w *recordingResponseWriter) TsigStatus() error {
	return nil
}

// TsigTimersOnly has no effect because tests do not use transaction signatures.
func (w *recordingResponseWriter) TsigTimersOnly(bool) {}

// Hijack is unsupported because the Harbor handler never takes over DNS connections.
func (w *recordingResponseWriter) Hijack() {}
