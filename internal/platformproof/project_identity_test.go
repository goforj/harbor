package platformproof

import (
	"context"
	"net/netip"
	"testing"
)

// TestProveProjectIdentitiesRejectsInvalidRequests exercises every request boundary before host mutation.
func TestProveProjectIdentitiesRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request ProjectIdentityRequest
	}{
		{name: "missing addresses", request: ProjectIdentityRequest{Port: 3306}},
		{name: "one address", request: ProjectIdentityRequest{Addresses: []netip.Addr{netip.MustParseAddr("127.77.0.10")}, Port: 3306}},
		{name: "three addresses", request: ProjectIdentityRequest{Addresses: []netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.11"), netip.MustParseAddr("127.77.0.12")}, Port: 3306}},
		{name: "duplicate addresses", request: ProjectIdentityRequest{Addresses: []netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.10")}, Port: 3306}},
		{name: "non-loopback", request: ProjectIdentityRequest{Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("127.77.0.11")}, Port: 3306}},
		{name: "ipv6 loopback", request: ProjectIdentityRequest{Addresses: []netip.Addr{netip.IPv6Loopback(), netip.MustParseAddr("127.77.0.11")}, Port: 3306}},
		{name: "zero port", request: ProjectIdentityRequest{Addresses: []netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.11")}}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ProveProjectIdentities(context.Background(), test.request); err == nil {
				t.Fatal("expected invalid proof request to fail")
			}
		})
	}
}

// TestProveIdentitiesAbsentRejectsInvalidAddresses keeps cleanup from accepting a weaker identity set.
func TestProveIdentitiesAbsentRejectsInvalidAddresses(t *testing.T) {
	t.Parallel()

	_, err := ProveIdentitiesAbsent(ProjectIdentityRequest{
		Addresses: []netip.Addr{netip.MustParseAddr("127.77.0.10")},
	})
	if err == nil {
		t.Fatal("expected incomplete cleanup identity set to fail")
	}
}

// TestSelectExplicitIdentityAssignments accepts exact /32 addresses on a verified loopback interface.
func TestSelectExplicitIdentityAssignments(t *testing.T) {
	t.Parallel()

	addresses := []netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.11")}
	observed := []interfaceAssignment{
		{address: addresses[0], name: "lo", index: 1, loopback: true, prefixLength: 32},
		{address: addresses[1], name: "lo", index: 1, loopback: true, prefixLength: 32},
	}
	selected, err := selectExplicitIdentityAssignments(addresses, observed)
	if err != nil {
		t.Fatalf("select assignments: %v", err)
	}
	if len(selected) != 2 || selected[1].address != addresses[1] {
		t.Fatalf("unexpected assignments: %+v", selected)
	}
}

// TestSelectExplicitIdentityAssignmentsRejectsWeakObservations exercises every assignment invariant.
func TestSelectExplicitIdentityAssignmentsRejectsWeakObservations(t *testing.T) {
	t.Parallel()

	addresses := []netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.11")}
	valid := []interfaceAssignment{
		{address: addresses[0], name: "lo", index: 1, loopback: true, prefixLength: 32},
		{address: addresses[1], name: "lo", index: 1, loopback: true, prefixLength: 32},
	}
	tests := []struct {
		name   string
		mutate func([]interfaceAssignment) []interfaceAssignment
	}{
		{name: "missing", mutate: func(assignments []interfaceAssignment) []interfaceAssignment { return assignments[:1] }},
		{name: "ambiguous", mutate: func(assignments []interfaceAssignment) []interfaceAssignment {
			return append(assignments, assignments[0])
		}},
		{name: "missing name", mutate: func(assignments []interfaceAssignment) []interfaceAssignment {
			assignments[0].name = ""
			return assignments
		}},
		{name: "missing index", mutate: func(assignments []interfaceAssignment) []interfaceAssignment {
			assignments[0].index = 0
			return assignments
		}},
		{name: "non-loopback", mutate: func(assignments []interfaceAssignment) []interfaceAssignment {
			assignments[0].loopback = false
			return assignments
		}},
		{name: "broad prefix", mutate: func(assignments []interfaceAssignment) []interfaceAssignment {
			assignments[0].prefixLength = 8
			return assignments
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observed := append([]interfaceAssignment(nil), valid...)
			if _, err := selectExplicitIdentityAssignments(addresses, test.mutate(observed)); err == nil {
				t.Fatal("expected weak interface observation to fail")
			}
		})
	}
}

// TestConfirmIdentityAssignments rejects count and interface changes across the socket proof.
func TestConfirmIdentityAssignments(t *testing.T) {
	t.Parallel()

	addresses := []netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.11")}
	initial := []interfaceAssignment{
		{address: addresses[0], name: "lo", index: 1, loopback: true, prefixLength: 32},
		{address: addresses[1], name: "lo", index: 1, loopback: true, prefixLength: 32},
	}
	if err := confirmIdentityAssignments(addresses, initial, initial); err != nil {
		t.Fatalf("confirm stable assignments: %v", err)
	}
	if err := confirmIdentityAssignments(addresses, initial[:1], initial); err == nil {
		t.Fatal("expected changed assignment count to fail")
	}
	changed := append([]interfaceAssignment(nil), initial...)
	changed[1].index = 2
	if err := confirmIdentityAssignments(addresses, initial, changed); err == nil {
		t.Fatal("expected changed interface identity to fail")
	}
}

// TestObserveInterfaceAssignments sees the host loopback identity through the same API used by proofs.
func TestObserveInterfaceAssignments(t *testing.T) {
	t.Parallel()

	observed, err := observeInterfaceAssignments()
	if err != nil {
		t.Fatalf("observe interface assignments: %v", err)
	}
	foundLoopback := false
	for _, assignment := range observed {
		if assignment.address.Is4() && assignment.address.IsLoopback() && assignment.loopback && assignment.name != "" && assignment.index > 0 {
			foundLoopback = true
			break
		}
	}
	if !foundLoopback {
		t.Fatal("expected an explicit IPv4 loopback interface assignment")
	}
}
