package httpingress

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// TestNewSnapshotCanonicalizesAndCopies verifies routing input cannot mutate an installed candidate.
func TestNewSnapshotCanonicalizesAndCopies(t *testing.T) {
	t.Parallel()
	routes := []Route{
		{Host: "Admin.Orders.TEST.", Upstream: netip.MustParseAddrPort("127.0.0.1:43102")},
		{Host: "orders.test", Upstream: netip.MustParseAddrPort("127.0.0.1:43101")},
	}
	snapshot, err := NewSnapshot(routes)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	routes[0].Host = "changed.test"

	wantHosts := []string{"admin.orders.test", "orders.test"}
	if got := snapshot.Hosts(); !reflect.DeepEqual(got, wantHosts) {
		t.Fatalf("Hosts() = %#v, want %#v", got, wantHosts)
	}
	hosts := snapshot.Hosts()
	hosts[0] = "mutated.test"
	if got := snapshot.Hosts(); !reflect.DeepEqual(got, wantHosts) {
		t.Fatalf("Hosts() after caller mutation = %#v, want %#v", got, wantHosts)
	}
	route, found := snapshot.Route("ADMIN.ORDERS.TEST.")
	if !found {
		t.Fatal("Route() did not find canonical equivalent")
	}
	if route.Host != "admin.orders.test" || route.Upstream != netip.MustParseAddrPort("127.0.0.1:43102") {
		t.Fatalf("Route() = %#v", route)
	}
	if _, found := snapshot.Route("admin.orders.test:443"); found {
		t.Fatal("Route() accepted an authority with a port")
	}
}

// TestNewSnapshotCanonicalizesMappedLoopbackUpstream keeps every accepted route on the IPv4 dial path.
func TestNewSnapshotCanonicalizesMappedLoopbackUpstream(t *testing.T) {
	t.Parallel()
	snapshot, err := NewSnapshot([]Route{{
		Host:     "orders.test",
		Upstream: netip.MustParseAddrPort("[::ffff:127.0.0.1]:43101"),
	}})
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	route, found := snapshot.Route("orders.test")
	if !found || route.Upstream != netip.MustParseAddrPort("127.0.0.1:43101") {
		t.Fatalf("Route() = %#v, found %t", route, found)
	}
}

// TestNewSnapshotRejectsInvalidRoutes exercises every public candidate validation branch.
func TestNewSnapshotRejectsInvalidRoutes(t *testing.T) {
	t.Parallel()
	validUpstream := netip.MustParseAddrPort("127.0.0.1:43101")
	longLabel := strings.Repeat("a", 64) + ".test"
	longDomain := strings.Repeat("a.", 125) + "aaa.test"
	tests := []struct {
		name    string
		routes  []Route
		message string
	}{
		{name: "empty host", routes: []Route{{Upstream: validUpstream}}, message: "must not be empty"},
		{name: "whitespace", routes: []Route{{Host: " orders.test", Upstream: validUpstream}}, message: "surrounding whitespace"},
		{name: "zone root", routes: []Route{{Host: ".test", Upstream: validUpstream}}, message: "beneath .test"},
		{name: "outside zone", routes: []Route{{Host: "orders.local", Upstream: validUpstream}}, message: "beneath .test"},
		{name: "empty label", routes: []Route{{Host: "admin..orders.test", Upstream: validUpstream}}, message: "labels must not be empty"},
		{name: "long label", routes: []Route{{Host: longLabel, Upstream: validUpstream}}, message: "must not exceed 63 bytes"},
		{name: "long domain", routes: []Route{{Host: longDomain, Upstream: validUpstream}}, message: "must not exceed 253 bytes"},
		{name: "leading hyphen", routes: []Route{{Host: "-orders.test", Upstream: validUpstream}}, message: "start and end"},
		{name: "trailing hyphen", routes: []Route{{Host: "orders-.test", Upstream: validUpstream}}, message: "start and end"},
		{name: "wildcard", routes: []Route{{Host: "*.orders.test", Upstream: validUpstream}}, message: "only ASCII"},
		{name: "unicode", routes: []Route{{Host: "ordérs.test", Upstream: validUpstream}}, message: "only ASCII"},
		{name: "invalid upstream", routes: []Route{{Host: "orders.test"}}, message: "valid address and port"},
		{name: "non-loopback upstream", routes: []Route{{Host: "orders.test", Upstream: netip.MustParseAddrPort("192.0.2.1:43101")}}, message: "IPv4 loopback"},
		{name: "IPv6 upstream", routes: []Route{{Host: "orders.test", Upstream: netip.MustParseAddrPort("[::1]:43101")}}, message: "IPv4 loopback"},
		{name: "zero upstream port", routes: []Route{{Host: "orders.test", Upstream: netip.MustParseAddrPort("127.0.0.1:0")}}, message: "must not be zero"},
		{
			name: "canonical duplicate",
			routes: []Route{
				{Host: "orders.test", Upstream: validUpstream},
				{Host: "ORDERS.TEST.", Upstream: netip.MustParseAddrPort("127.0.0.1:43102")},
			},
			message: "is duplicated",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewSnapshot(test.routes); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("NewSnapshot() error = %v, want containing %q", err, test.message)
			}
		})
	}
}

// TestEmptySnapshotIsValid verifies startup can publish an authoritative table with no projects.
func TestEmptySnapshotIsValid(t *testing.T) {
	t.Parallel()
	snapshot, err := NewSnapshot(nil)
	if err != nil {
		t.Fatalf("NewSnapshot(nil) error = %v", err)
	}
	if len(snapshot.Hosts()) != 0 {
		t.Fatalf("Hosts() = %#v, want empty", snapshot.Hosts())
	}
}

// TestHostFromAuthorityRejectsAmbiguity verifies malformed ports cannot collapse into another route name.
func TestHostFromAuthorityRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		authority string
		want      string
		valid     bool
	}{
		{authority: "orders.test", want: "orders.test", valid: true},
		{authority: "orders.test:443", want: "orders.test", valid: true},
		{authority: "", valid: false},
		{authority: "orders.test:", valid: false},
		{authority: "orders.test:not-a-port", valid: false},
		{authority: "orders.test:65536", valid: false},
		{authority: "orders.test:443:extra", valid: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.authority, func(t *testing.T) {
			t.Parallel()
			got, err := hostFromAuthority(test.authority)
			if (err == nil) != test.valid {
				t.Fatalf("hostFromAuthority(%q) error = %v, valid = %v", test.authority, err, test.valid)
			}
			if got != test.want {
				t.Fatalf("hostFromAuthority(%q) = %q, want %q", test.authority, got, test.want)
			}
		})
	}
}
