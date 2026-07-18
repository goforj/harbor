//go:build darwin

package loopback

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// TestDarwinSnapshotRetainsExactTargetAssignments proves message order and unrelated address families cannot hide conflicts.
func TestDarwinSnapshotRetainsExactTargetAssignments(t *testing.T) {
	target := netip.MustParseAddr("127.77.0.10")
	loopbackMessage := &route.InterfaceMessage{
		Type:  unix.RTM_IFINFO,
		Flags: darwinRequiredInterfaceFlags,
		Index: 1,
		Name:  "lo0",
	}
	ordinaryMessage := &route.InterfaceMessage{
		Type:  unix.RTM_IFINFO2,
		Flags: unix.IFF_UP | unix.IFF_RUNNING,
		Index: 4,
		Name:  "en0",
	}
	foreignAssignment := darwinTestAddressMessage(4, target.As4(), [4]byte{0xff, 0xff, 0xff, 0})
	loopbackAssignment := darwinTestAddressMessage(1, target.As4(), [4]byte{0xff, 0xff, 0xff, 0xff})
	unrelatedAssignment := darwinTestAddressMessage(1, [4]byte{127, 77, 0, 11}, [4]byte{0xff, 0xff, 0xff, 0xff})
	ipv6Assignment := &route.InterfaceAddrMessage{
		Type:  unix.RTM_NEWADDR,
		Index: 1,
		Addrs: darwinTestRoutingAddresses(unix.RTAX_IFA, &route.Inet6Addr{IP: [16]byte{15: 1}}),
	}
	multicast := &route.InterfaceMulticastAddrMessage{Type: unix.RTM_NEWMADDR}

	types := map[*route.InterfaceMessage]int{
		loopbackMessage: unix.IFT_LOOP,
		ordinaryMessage: unix.IFT_ETHER,
	}
	snapshot, err := darwinSnapshotFromMessages([]route.Message{
		foreignAssignment,
		loopbackMessage,
		unrelatedAssignment,
		multicast,
		ordinaryMessage,
		ipv6Assignment,
		loopbackAssignment,
	}, target, func(message *route.InterfaceMessage) (int, error) {
		return types[message], nil
	})
	if err != nil {
		t.Fatalf("darwinSnapshotFromMessages() error = %v", err)
	}
	wantInterfaces := []InterfaceFact{
		{Name: "lo0", Index: 1, Kind: InterfaceKindDarwinNative, NativeLoopback: true},
		{Name: "en0", Index: 4},
	}
	if !reflect.DeepEqual(snapshot.interfaces, wantInterfaces) {
		t.Fatalf("interfaces = %+v, want %+v", snapshot.interfaces, wantInterfaces)
	}
	wantAssignments := []AssignmentFact{
		{Address: target, PrefixLength: 24, InterfaceName: "en0", InterfaceIndex: 4},
		{Address: target, PrefixLength: 32, InterfaceName: "lo0", InterfaceIndex: 1},
	}
	if !reflect.DeepEqual(snapshot.assignments, wantAssignments) {
		t.Fatalf("assignments = %+v, want %+v", snapshot.assignments, wantAssignments)
	}
}

// TestReadDarwinSnapshotWithBoundsAndCancelsNativeReads proves no partial RIB becomes trusted host evidence.
func TestReadDarwinSnapshotWithBoundsAndCancelsNativeReads(t *testing.T) {
	resolver := func(*route.InterfaceMessage) (int, error) { return unix.IFT_LOOP, nil }
	parseEmpty := func(route.RIBType, []byte) ([]route.Message, error) { return nil, nil }
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fetched := false
		_, err := readDarwinSnapshotWith(ctx, netip.Addr{}, func(int, route.RIBType, int) ([]byte, error) {
			fetched = true
			return nil, nil
		}, parseEmpty, resolver)
		if !errors.Is(err, context.Canceled) || fetched {
			t.Fatalf("readDarwinSnapshotWith() error = %v, fetched = %t", err, fetched)
		}
	})
	t.Run("fetch failed", func(t *testing.T) {
		_, err := readDarwinSnapshotWith(context.Background(), netip.Addr{}, func(addressFamily int, ribType route.RIBType, arg int) ([]byte, error) {
			if addressFamily != unix.AF_UNSPEC || ribType != route.RIBTypeInterface || arg != 0 {
				t.Fatalf("fetch arguments = %d, %d, %d", addressFamily, ribType, arg)
			}
			return nil, unix.EIO
		}, parseEmpty, resolver)
		if !errors.Is(err, unix.EIO) {
			t.Fatalf("readDarwinSnapshotWith() error = %v, want EIO", err)
		}
	})
	t.Run("RIB too large", func(t *testing.T) {
		parsed := false
		_, err := readDarwinSnapshotWith(context.Background(), netip.Addr{}, func(int, route.RIBType, int) ([]byte, error) {
			return make([]byte, maximumDarwinRIBBytes+1), nil
		}, func(route.RIBType, []byte) ([]route.Message, error) {
			parsed = true
			return nil, nil
		}, resolver)
		if err == nil || parsed {
			t.Fatalf("readDarwinSnapshotWith() error = %v, parsed = %t", err, parsed)
		}
	})
	t.Run("canceled after fetch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		parsed := false
		_, err := readDarwinSnapshotWith(ctx, netip.Addr{}, func(int, route.RIBType, int) ([]byte, error) {
			cancel()
			return []byte{1}, nil
		}, func(route.RIBType, []byte) ([]route.Message, error) {
			parsed = true
			return nil, nil
		}, resolver)
		if !errors.Is(err, context.Canceled) || parsed {
			t.Fatalf("readDarwinSnapshotWith() error = %v, parsed = %t", err, parsed)
		}
	})
	t.Run("parse failed", func(t *testing.T) {
		_, err := readDarwinSnapshotWith(context.Background(), netip.Addr{}, func(int, route.RIBType, int) ([]byte, error) {
			return []byte{1}, nil
		}, func(ribType route.RIBType, raw []byte) ([]route.Message, error) {
			if ribType != route.RIBTypeInterface || !reflect.DeepEqual(raw, []byte{1}) {
				t.Fatalf("parse arguments = %d, %v", ribType, raw)
			}
			return nil, unix.EINVAL
		}, resolver)
		if !errors.Is(err, unix.EINVAL) {
			t.Fatalf("readDarwinSnapshotWith() error = %v, want EINVAL", err)
		}
	})
	t.Run("message count too large", func(t *testing.T) {
		_, err := readDarwinSnapshotWith(context.Background(), netip.Addr{}, func(int, route.RIBType, int) ([]byte, error) {
			return nil, nil
		}, func(route.RIBType, []byte) ([]route.Message, error) {
			return make([]route.Message, maximumDarwinRoutingMessages+1), nil
		}, resolver)
		if err == nil {
			t.Fatal("readDarwinSnapshotWith() accepted too many parsed messages")
		}
	})
	t.Run("canceled after parse", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := readDarwinSnapshotWith(ctx, netip.Addr{}, func(int, route.RIBType, int) ([]byte, error) {
			return nil, nil
		}, func(route.RIBType, []byte) ([]route.Message, error) {
			cancel()
			return nil, nil
		}, resolver)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("readDarwinSnapshotWith() error = %v, want context.Canceled", err)
		}
	})
}

// TestDarwinInterfaceFactRequiresKernelLoopbackEvidence pins every identity fact used before a name-based ioctl.
func TestDarwinInterfaceFactRequiresKernelLoopbackEvidence(t *testing.T) {
	tests := []struct {
		name          string
		flags         int
		interfaceType int
		wantNative    bool
	}{
		{name: "all evidence", flags: darwinRequiredInterfaceFlags, interfaceType: unix.IFT_LOOP, wantNative: true},
		{name: "missing up", flags: unix.IFF_LOOPBACK | unix.IFF_RUNNING, interfaceType: unix.IFT_LOOP},
		{name: "missing loopback", flags: unix.IFF_UP | unix.IFF_RUNNING, interfaceType: unix.IFT_LOOP},
		{name: "missing running", flags: unix.IFF_UP | unix.IFF_LOOPBACK, interfaceType: unix.IFT_LOOP},
		{name: "wrong interface type", flags: darwinRequiredInterfaceFlags, interfaceType: unix.IFT_ETHER},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fact, err := darwinInterfaceFact(&route.InterfaceMessage{Name: "lo0", Index: 1, Flags: test.flags}, test.interfaceType)
			if err != nil {
				t.Fatalf("darwinInterfaceFact() error = %v", err)
			}
			if fact.NativeLoopback != test.wantNative {
				t.Fatalf("NativeLoopback = %t, want %t", fact.NativeLoopback, test.wantNative)
			}
			if test.wantNative && fact.Kind != InterfaceKindDarwinNative {
				t.Fatalf("Kind = %q, want %q", fact.Kind, InterfaceKindDarwinNative)
			}
			if !test.wantNative && fact.Kind != "" {
				t.Fatalf("ordinary Kind = %q, want empty", fact.Kind)
			}
		})
	}
}

// TestDarwinSnapshotRejectsMalformedInterfaceFacts prevents lossy ioctl names and ambiguous indexes from entering observations.
func TestDarwinSnapshotRejectsMalformedInterfaceFacts(t *testing.T) {
	tests := []struct {
		name     string
		messages func() []route.Message
		resolver func(*route.InterfaceMessage) (int, error)
	}{
		{
			name: "invalid index",
			messages: func() []route.Message {
				return []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 0, Name: "lo0"}}
			},
		},
		{
			name: "empty name",
			messages: func() []route.Message {
				return []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1}}
			},
		},
		{
			name: "whitespace name",
			messages: func() []route.Message {
				return []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1, Name: " lo0"}}
			},
		},
		{
			name: "embedded NUL",
			messages: func() []route.Message {
				return []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1, Name: "lo\x000"}}
			},
		},
		{
			name: "name cannot fit ioctl field",
			messages: func() []route.Message {
				return []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1, Name: strings.Repeat("x", darwinInterfaceNameBytes)}}
			},
		},
		{
			name: "duplicate index",
			messages: func() []route.Message {
				return []route.Message{
					&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1, Name: "lo0"},
					&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1, Name: "en0"},
				}
			},
		},
		{
			name: "duplicate name",
			messages: func() []route.Message {
				return []route.Message{
					&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1, Name: "lo0"},
					&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 2, Name: "lo0"},
				}
			},
		},
		{
			name: "unsupported interface message",
			messages: func() []route.Message {
				return []route.Message{&route.InterfaceMessage{Type: unix.RTM_ADD, Index: 1, Name: "lo0"}}
			},
		},
		{
			name: "missing metrics",
			messages: func() []route.Message {
				return []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO, Index: 1, Name: "lo0"}}
			},
			resolver: func(*route.InterfaceMessage) (int, error) {
				return 0, errors.New("metrics unavailable")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := test.resolver
			if resolver == nil {
				resolver = func(*route.InterfaceMessage) (int, error) { return unix.IFT_LOOP, nil }
			}
			if _, err := darwinSnapshotFromMessages(test.messages(), netip.Addr{}, resolver); err == nil {
				t.Fatal("darwinSnapshotFromMessages() accepted malformed interface facts")
			}
		})
	}
}

// TestDarwinSnapshotRejectsUnclassifiableAssignments proves exact-target evidence can never disappear through malformed RIB data.
func TestDarwinSnapshotRejectsUnclassifiableAssignments(t *testing.T) {
	target := netip.MustParseAddr("127.77.0.10")
	loopback := &route.InterfaceMessage{Type: unix.RTM_IFINFO, Flags: darwinRequiredInterfaceFlags, Index: 1, Name: "lo0"}
	tests := []struct {
		name       string
		assignment *route.InterfaceAddrMessage
	}{
		{
			name:       "invalid index",
			assignment: darwinTestAddressMessage(0, target.As4(), [4]byte{0xff, 0xff, 0xff, 0xff}),
		},
		{
			name:       "unknown interface",
			assignment: darwinTestAddressMessage(2, target.As4(), [4]byte{0xff, 0xff, 0xff, 0xff}),
		},
		{
			name: "missing mask",
			assignment: &route.InterfaceAddrMessage{
				Type:  unix.RTM_NEWADDR,
				Index: 1,
				Addrs: darwinTestRoutingAddresses(unix.RTAX_IFA, &route.Inet4Addr{IP: target.As4()}),
			},
		},
		{
			name:       "non-contiguous mask",
			assignment: darwinTestAddressMessage(1, target.As4(), [4]byte{0xff, 0, 0xff, 0}),
		},
		{
			name:       "unsupported message type",
			assignment: darwinTestAddressMessage(1, target.As4(), [4]byte{0xff, 0xff, 0xff, 0xff}),
		},
	}
	tests[len(tests)-1].assignment.Type = unix.RTM_DELADDR
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := darwinSnapshotFromMessages(
				[]route.Message{loopback, test.assignment},
				target,
				func(*route.InterfaceMessage) (int, error) { return unix.IFT_LOOP, nil },
			)
			if err == nil {
				t.Fatal("darwinSnapshotFromMessages() accepted unclassifiable assignment facts")
			}
		})
	}
}

// TestDarwinSnapshotRejectsUnboundedOrUnexpectedMessages pins the RIB parser to a finite known message vocabulary.
func TestDarwinSnapshotRejectsUnboundedOrUnexpectedMessages(t *testing.T) {
	tooMany := make([]route.Message, maximumDarwinRoutingMessages+1)
	if _, err := darwinSnapshotFromMessages(tooMany, netip.Addr{}, func(*route.InterfaceMessage) (int, error) { return 0, nil }); err == nil {
		t.Fatal("darwinSnapshotFromMessages() accepted an unbounded message set")
	}
	if _, err := darwinSnapshotFromMessages(
		[]route.Message{&route.RouteMessage{}},
		netip.Addr{},
		func(*route.InterfaceMessage) (int, error) { return 0, nil },
	); err == nil {
		t.Fatal("darwinSnapshotFromMessages() accepted an unexpected message type")
	}
	if _, err := darwinSnapshotFromMessages(
		[]route.Message{&route.InterfaceMulticastAddrMessage{Type: unix.RTM_DELMADDR}},
		netip.Addr{},
		func(*route.InterfaceMessage) (int, error) { return 0, nil },
	); err == nil {
		t.Fatal("darwinSnapshotFromMessages() accepted an unexpected multicast message type")
	}
	var nilInterface *route.InterfaceMessage
	var nilAddress *route.InterfaceAddrMessage
	var nilMulticast *route.InterfaceMulticastAddrMessage
	for _, message := range []route.Message{nilInterface, nilAddress, nilMulticast} {
		if _, err := darwinSnapshotFromMessages(
			[]route.Message{message},
			netip.Addr{},
			func(*route.InterfaceMessage) (int, error) { return 0, nil },
		); err == nil {
			t.Fatalf("darwinSnapshotFromMessages() accepted nil message %T", message)
		}
	}
	if _, err := darwinSnapshotFromMessages(
		nil,
		netip.MustParseAddr("192.0.2.1"),
		func(*route.InterfaceMessage) (int, error) { return 0, nil },
	); err == nil {
		t.Fatal("darwinSnapshotFromMessages() accepted a non-loopback target")
	}
}

// TestDarwinSnapshotBoundsExactAssignments rejects a kernel snapshot larger than the adapter can classify.
func TestDarwinSnapshotBoundsExactAssignments(t *testing.T) {
	target := netip.MustParseAddr("127.77.0.10")
	messages := []route.Message{
		&route.InterfaceMessage{Type: unix.RTM_IFINFO, Flags: darwinRequiredInterfaceFlags, Index: 1, Name: "lo0"},
	}
	for range maximumAssignmentFacts + 1 {
		messages = append(messages, darwinTestAddressMessage(1, target.As4(), [4]byte{0xff, 0xff, 0xff, 0xff}))
	}
	if _, err := darwinSnapshotFromMessages(messages, target, func(*route.InterfaceMessage) (int, error) { return unix.IFT_LOOP, nil }); err == nil {
		t.Fatal("darwinSnapshotFromMessages() accepted too many exact assignments")
	}
}

// TestDarwinIPv4MaskPrefixLength accepts every contiguous edge while rejecting disjoint one bits.
func TestDarwinIPv4MaskPrefixLength(t *testing.T) {
	tests := []struct {
		mask [4]byte
		want int
		ok   bool
	}{
		{mask: [4]byte{}, want: 0, ok: true},
		{mask: [4]byte{0x80}, want: 1, ok: true},
		{mask: [4]byte{0xff}, want: 8, ok: true},
		{mask: [4]byte{0xff, 0xff, 0xff}, want: 24, ok: true},
		{mask: [4]byte{0xff, 0xff, 0xff, 0xff}, want: 32, ok: true},
		{mask: [4]byte{0x7f}, ok: false},
		{mask: [4]byte{0xff, 0xfe, 1}, ok: false},
	}
	for _, test := range tests {
		got, ok := darwinIPv4MaskPrefixLength(test.mask)
		if got != test.want || ok != test.ok {
			t.Errorf("darwinIPv4MaskPrefixLength(%v) = %d, %t; want %d, %t", test.mask, got, ok, test.want, test.ok)
		}
	}
}

// TestDarwinRequestsMatchNativeABI pins every byte-bearing field used by the privileged ioctl boundary.
func TestDarwinRequestsMatchNativeABI(t *testing.T) {
	interf := darwinTestLoopback()
	prefix := netip.MustParsePrefix("127.77.0.10/32")
	alias, err := newDarwinAliasRequest(interf, prefix)
	if err != nil {
		t.Fatalf("newDarwinAliasRequest() error = %v", err)
	}
	remove, err := newDarwinAddressRequest(interf, prefix)
	if err != nil {
		t.Fatalf("newDarwinAddressRequest() error = %v", err)
	}
	wantName := [darwinInterfaceNameBytes]byte{'l', 'o', '0'}
	wantAddress := darwinSockaddrInet4{Length: 16, Family: unix.AF_INET, Address: [4]byte{127, 77, 0, 10}}
	wantMask := darwinSockaddrInet4{Length: 16, Address: [4]byte{0xff, 0xff, 0xff, 0xff}}
	if alias.Name != wantName || alias.Address != wantAddress || alias.Broadcast != (darwinSockaddrInet4{}) || alias.Mask != wantMask {
		t.Fatalf("alias request = %+v", alias)
	}
	if remove.Name != wantName || remove.Address != wantAddress {
		t.Fatalf("remove request = %+v", remove)
	}
	if unsafe.Sizeof(alias) != 64 || unsafe.Offsetof(alias.Address) != 16 || unsafe.Offsetof(alias.Broadcast) != 32 || unsafe.Offsetof(alias.Mask) != 48 {
		t.Fatalf("alias ABI = size %d offsets %d/%d/%d", unsafe.Sizeof(alias), unsafe.Offsetof(alias.Address), unsafe.Offsetof(alias.Broadcast), unsafe.Offsetof(alias.Mask))
	}
	if unsafe.Sizeof(remove) != 32 || unsafe.Offsetof(remove.Address) != 16 {
		t.Fatalf("remove ABI = size %d address offset %d", unsafe.Sizeof(remove), unsafe.Offsetof(remove.Address))
	}
}

// TestDarwinMutationRevalidatesIdentityImmediatelyBeforeIoctl proves no ticket-supplied name reaches the kernel unchecked.
func TestDarwinMutationRevalidatesIdentityImmediatelyBeforeIoctl(t *testing.T) {
	interf := darwinTestLoopback()
	prefix := netip.MustParsePrefix("127.77.0.10/32")
	events := make([]string, 0, 4)
	backend := platformBackend{host: darwinHost{
		snapshot: func(context.Context, netip.Addr) (darwinSnapshot, error) {
			events = append(events, "snapshot")
			return darwinSnapshot{interfaces: []InterfaceFact{interf}}, nil
		},
		openSocket: func() (int, error) {
			events = append(events, "open")
			return 73, nil
		},
		closeSocket: func(file int) error {
			events = append(events, fmt.Sprintf("close:%d", file))
			return nil
		},
		addAddress: func(file int, request *darwinAliasRequest) error {
			events = append(events, fmt.Sprintf("add:%d:%s", file, string(request.Name[:3])))
			return nil
		},
		deleteAddress: func(file int, request *darwinAddressRequest) error {
			events = append(events, fmt.Sprintf("delete:%d:%s", file, string(request.Name[:3])))
			return nil
		},
	}}
	if err := backend.ensure(context.Background(), interf, prefix); err != nil {
		t.Fatalf("ensure() error = %v", err)
	}
	wantEnsure := []string{"open", "snapshot", "add:73:lo0", "close:73"}
	if !reflect.DeepEqual(events, wantEnsure) {
		t.Fatalf("ensure events = %v, want %v", events, wantEnsure)
	}
	events = events[:0]
	if err := backend.release(context.Background(), interf, prefix); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	wantRelease := []string{"open", "snapshot", "delete:73:lo0", "close:73"}
	if !reflect.DeepEqual(events, wantRelease) {
		t.Fatalf("release events = %v, want %v", events, wantRelease)
	}
}

// TestDarwinMutationRejectsIdentityDrift closes the socket without issuing an ioctl when any identity fact changes.
func TestDarwinMutationRejectsIdentityDrift(t *testing.T) {
	expected := darwinTestLoopback()
	prefix := netip.MustParsePrefix("127.77.0.10/32")
	tests := []struct {
		name       string
		interfaces []InterfaceFact
	}{
		{name: "missing", interfaces: []InterfaceFact{{Name: "en0", Index: 4}}},
		{name: "index changed", interfaces: []InterfaceFact{{Name: "lo0", Index: 9, Kind: InterfaceKindDarwinNative, NativeLoopback: true}}},
		{name: "name changed", interfaces: []InterfaceFact{{Name: "lo1", Index: 1, Kind: InterfaceKindDarwinNative, NativeLoopback: true}}},
		{name: "identity ambiguous", interfaces: []InterfaceFact{expected, {Name: "lo1", Index: 9, Kind: InterfaceKindDarwinNative, NativeLoopback: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ioctlCalled := false
			closed := false
			backend := platformBackend{host: darwinHost{
				snapshot: func(context.Context, netip.Addr) (darwinSnapshot, error) {
					return darwinSnapshot{interfaces: test.interfaces}, nil
				},
				openSocket: func() (int, error) { return 73, nil },
				closeSocket: func(int) error {
					closed = true
					return nil
				},
				addAddress: func(int, *darwinAliasRequest) error {
					ioctlCalled = true
					return nil
				},
			}}
			if err := backend.ensure(context.Background(), expected, prefix); err == nil {
				t.Fatal("ensure() accepted changed interface identity")
			}
			if ioctlCalled || !closed {
				t.Fatalf("ioctlCalled = %t, closed = %t", ioctlCalled, closed)
			}
		})
	}
}

// TestDarwinMutationHonorsCancellationAroundRevalidation prevents cancellation from crossing the privileged syscall boundary.
func TestDarwinMutationHonorsCancellationAroundRevalidation(t *testing.T) {
	interf := darwinTestLoopback()
	prefix := netip.MustParsePrefix("127.77.0.10/32")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opened := false
	backend := platformBackend{host: darwinHost{
		openSocket: func() (int, error) {
			opened = true
			return 73, nil
		},
	}}
	if err := backend.ensure(ctx, interf, prefix); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensure() error = %v, want context.Canceled", err)
	}
	if opened {
		t.Fatal("ensure() opened a socket after cancellation")
	}

	ctx, cancel = context.WithCancel(context.Background())
	ioctlCalled := false
	closed := false
	backend = platformBackend{host: darwinHost{
		snapshot: func(context.Context, netip.Addr) (darwinSnapshot, error) {
			cancel()
			return darwinSnapshot{interfaces: []InterfaceFact{interf}}, nil
		},
		openSocket: func() (int, error) { return 73, nil },
		closeSocket: func(int) error {
			closed = true
			return nil
		},
		addAddress: func(int, *darwinAliasRequest) error {
			ioctlCalled = true
			return nil
		},
	}}
	if err := backend.ensure(ctx, interf, prefix); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensure() error = %v, want context.Canceled", err)
	}
	if ioctlCalled || !closed {
		t.Fatalf("ioctlCalled = %t, closed = %t", ioctlCalled, closed)
	}
}

// TestDarwinMutationPreservesNativeFailures keeps authority, observation, ioctl, and cleanup errors inspectable.
func TestDarwinMutationPreservesNativeFailures(t *testing.T) {
	interf := darwinTestLoopback()
	prefix := netip.MustParsePrefix("127.77.0.10/32")
	tests := []struct {
		name string
		host darwinHost
		want error
	}{
		{
			name: "socket",
			host: darwinHost{openSocket: func() (int, error) { return -1, unix.EPERM }},
			want: unix.EPERM,
		},
		{
			name: "snapshot",
			host: darwinHost{
				openSocket:  func() (int, error) { return 73, nil },
				closeSocket: func(int) error { return nil },
				snapshot: func(context.Context, netip.Addr) (darwinSnapshot, error) {
					return darwinSnapshot{}, unix.EIO
				},
			},
			want: unix.EIO,
		},
		{
			name: "ioctl",
			host: darwinHost{
				openSocket:  func() (int, error) { return 73, nil },
				closeSocket: func(int) error { return nil },
				snapshot: func(context.Context, netip.Addr) (darwinSnapshot, error) {
					return darwinSnapshot{interfaces: []InterfaceFact{interf}}, nil
				},
				addAddress: func(int, *darwinAliasRequest) error { return unix.EACCES },
			},
			want: unix.EACCES,
		},
		{
			name: "close",
			host: darwinHost{
				openSocket:  func() (int, error) { return 73, nil },
				closeSocket: func(int) error { return unix.EBADF },
				snapshot: func(context.Context, netip.Addr) (darwinSnapshot, error) {
					return darwinSnapshot{interfaces: []InterfaceFact{interf}}, nil
				},
				addAddress: func(int, *darwinAliasRequest) error { return nil },
			},
			want: unix.EBADF,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := platformBackend{host: test.host}
			err := backend.ensure(context.Background(), interf, prefix)
			if !errors.Is(err, test.want) {
				t.Fatalf("ensure() error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

// TestDarwinMutationRejectsMalformedRequests prevents direct backend callers from widening or redirecting effects.
func TestDarwinMutationRejectsMalformedRequests(t *testing.T) {
	valid := darwinTestLoopback()
	tests := []struct {
		name   string
		interf InterfaceFact
		prefix netip.Prefix
	}{
		{name: "non-host prefix", interf: valid, prefix: netip.MustParsePrefix("127.77.0.10/24")},
		{name: "non-loopback", interf: valid, prefix: netip.MustParsePrefix("192.0.2.1/32")},
		{name: "IPv6", interf: valid, prefix: netip.MustParsePrefix("::1/128")},
		{name: "wrong kind", interf: InterfaceFact{Name: "lo0", Index: 1, Kind: InterfaceKindLinuxNative, NativeLoopback: true}, prefix: netip.MustParsePrefix("127.77.0.10/32")},
		{name: "not native", interf: InterfaceFact{Name: "lo0", Index: 1, Kind: InterfaceKindDarwinNative}, prefix: netip.MustParsePrefix("127.77.0.10/32")},
		{name: "invalid index", interf: InterfaceFact{Name: "lo0", Kind: InterfaceKindDarwinNative, NativeLoopback: true}, prefix: netip.MustParsePrefix("127.77.0.10/32")},
		{name: "long name", interf: InterfaceFact{Name: strings.Repeat("x", darwinInterfaceNameBytes), Index: 1, Kind: InterfaceKindDarwinNative, NativeLoopback: true}, prefix: netip.MustParsePrefix("127.77.0.10/32")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := platformBackend{}
			if err := backend.ensure(context.Background(), test.interf, test.prefix); err == nil {
				t.Fatal("ensure() accepted a malformed request")
			}
			if err := backend.release(context.Background(), test.interf, test.prefix); err == nil {
				t.Fatal("release() accepted a malformed request")
			}
		})
	}
}

// TestDarwinBackendCopiesSnapshotFacts prevents a caller from mutating retained host evidence.
func TestDarwinBackendCopiesSnapshotFacts(t *testing.T) {
	interf := darwinTestLoopback()
	assignment := AssignmentFact{Address: netip.MustParseAddr("127.77.0.10"), PrefixLength: 32, InterfaceName: "lo0", InterfaceIndex: 1}
	backend := platformBackend{host: darwinHost{
		snapshot: func(context.Context, netip.Addr) (darwinSnapshot, error) {
			return darwinSnapshot{interfaces: []InterfaceFact{interf}, assignments: []AssignmentFact{assignment}}, nil
		},
	}}
	interfaces, err := backend.interfaces(context.Background())
	if err != nil {
		t.Fatalf("interfaces() error = %v", err)
	}
	assignments, err := backend.assignments(context.Background(), assignment.Address)
	if err != nil {
		t.Fatalf("assignments() error = %v", err)
	}
	interfaces[0].Name = "changed"
	assignments[0].InterfaceName = "changed"
	if interf.Name != "lo0" || assignment.InterfaceName != "lo0" {
		t.Fatal("backend returned retained snapshot slices")
	}
}

// TestDarwinNativeBackendObservesWithoutMutation exercises routing-RIB parsing on a real Darwin CI host.
func TestDarwinNativeBackendObservesWithoutMutation(t *testing.T) {
	observation, err := New().Observe(context.Background(), netip.MustParseAddr("127.254.254.254"))
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.State != StateAbsent {
		t.Skipf("host already assigns the reserved test identity: %+v", observation)
	}
}

// darwinTestLoopback returns the exact identity shape admitted by the Darwin backend.
func darwinTestLoopback() InterfaceFact {
	return InterfaceFact{Name: "lo0", Index: 1, Kind: InterfaceKindDarwinNative, NativeLoopback: true}
}

// darwinTestAddressMessage creates one address message with independently controlled local address and mask.
func darwinTestAddressMessage(index int, address [4]byte, mask [4]byte) *route.InterfaceAddrMessage {
	addresses := make([]route.Addr, unix.RTAX_MAX)
	addresses[unix.RTAX_IFA] = &route.Inet4Addr{IP: address}
	addresses[unix.RTAX_NETMASK] = &route.Inet4Addr{IP: mask}
	return &route.InterfaceAddrMessage{Type: unix.RTM_NEWADDR, Index: index, Addrs: addresses}
}

// darwinTestRoutingAddresses creates a sparse RTAX slice without relying on message marshaling internals.
func darwinTestRoutingAddresses(index int, address route.Addr) []route.Addr {
	addresses := make([]route.Addr, unix.RTAX_MAX)
	addresses[index] = address
	return addresses
}
