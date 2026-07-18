package dnsserver

import (
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

// FuzzUDPWireResponse keeps arbitrary parseable datagrams inside Harbor's response budget.
func FuzzUDPWireResponse(f *testing.F) {
	snapshot, err := NewSnapshot([]Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	if err != nil {
		f.Fatalf("NewSnapshot() error = %v", err)
	}
	server, err := NewServer(DefaultConfig(netip.MustParseAddr("127.0.0.1"), 0), snapshot)
	if err != nil {
		f.Fatalf("NewServer() error = %v", err)
	}
	for _, request := range []*dns.Msg{
		new(dns.Msg).SetQuestion("orders.test.", dns.TypeA),
		new(dns.Msg).SetQuestion("missing.test.", dns.TypeAAAA),
	} {
		packed, packErr := request.Pack()
		if packErr != nil {
			f.Fatalf("seed Pack() error = %v", packErr)
		}
		f.Add(packed)
	}
	f.Add([]byte{0x01, 0x02})

	f.Fuzz(func(t *testing.T, packet []byte) {
		if len(packet) > maxUDPRequestBytes || !admissibleWireHeader(packet) {
			return
		}
		request := new(dns.Msg)
		if err := request.Unpack(packet); err != nil {
			return
		}
		response := server.responseFor(request)
		if response == nil {
			return
		}
		limit := udpPayloadSize(request)
		response.Truncate(limit)
		packed, err := response.Pack()
		if err != nil {
			t.Fatalf("response Pack() error = %v", err)
		}
		if len(packed) > limit {
			t.Fatalf("response length = %d, want <= %d", len(packed), limit)
		}
	})
}
