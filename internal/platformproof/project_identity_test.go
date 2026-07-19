package platformproof

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
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

// TestValidateDuplicateListenerRejection accepts only portable address-in-use error chains.
func TestValidateDuplicateListenerRejection(t *testing.T) {
	t.Parallel()

	addressInUse := platformAddressInUseError()
	wrappedAddressInUse := &net.OpError{
		Op:   "listen",
		Net:  "tcp4",
		Addr: &net.TCPAddr{IP: net.ParseIP("127.77.0.10"), Port: 3306},
		Err:  &os.SyscallError{Syscall: "bind", Err: addressInUse},
	}
	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "raw address in use", err: addressInUse},
		{name: "formatted address in use", err: fmt.Errorf("open listener: %w", addressInUse)},
		{name: "syscall address in use", err: &os.SyscallError{Syscall: "bind", Err: addressInUse}},
		{name: "network operation address in use", err: wrappedAddressInUse},
		{name: "listener unexpectedly succeeds", wantErr: true},
		{name: "permission denied", err: &os.SyscallError{Syscall: "bind", Err: os.ErrPermission}, wantErr: true},
		{name: "cancelled listen", err: context.Canceled, wantErr: true},
		{name: "matching text without sentinel", err: errors.New("address already in use"), wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateDuplicateListenerRejection("127.77.0.10:3306", test.err)
			if test.wantErr && err == nil {
				t.Fatal("expected duplicate-listener result to fail")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("expected address conflict to pass: %v", err)
			}
		})
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
