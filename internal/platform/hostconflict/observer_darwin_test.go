//go:build darwin

package hostconflict

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// TestObserveStableDarwinRequiresConsecutiveFingerprints covers stability, churn, and generation-race retries.
func TestObserveStableDarwinRequiresConsecutiveFingerprints(t *testing.T) {
	request := mustRequest(t)
	first := safeMacOSObservation(t)
	second := cloneObservation(first)
	second.Sockets.Endpoints = []SocketFact{{
		Protocol: SocketProtocolTCP, Address: testCandidate, Port: 443,
		TCPAccepting: true, IPv6Only: IPv6OnlyNotApplicable,
	}}
	tests := []struct {
		name      string
		sequence  []darwinObservationResult
		wantCalls int
		wantErr   string
	}{
		{name: "immediate stability", sequence: []darwinObservationResult{{observation: first}, {observation: first}}, wantCalls: 2},
		{name: "change then stability", sequence: []darwinObservationResult{{observation: first}, {observation: second}, {observation: second}}, wantCalls: 3},
		{name: "transient reset", sequence: []darwinObservationResult{{err: errDarwinPCBSnapshotChanged}, {observation: first}, {observation: first}}, wantCalls: 3},
		{name: "route transient reset", sequence: []darwinObservationResult{{err: errDarwinRouteSnapshotChanged}, {observation: first}, {observation: first}}, wantCalls: 3},
		{name: "never stable", sequence: []darwinObservationResult{{observation: first}, {observation: second}, {observation: first}, {observation: second}, {observation: first}, {observation: second}, {observation: first}, {observation: second}}, wantCalls: darwinObservationRetries + 1, wantErr: "did not stabilize"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			observation, err := observeStableDarwin(context.Background(), request, func(context.Context, Request) (Observation, error) {
				result := test.sequence[calls]
				calls++
				return result.observation, result.err
			})
			if calls != test.wantCalls {
				t.Fatalf("observe calls = %d, want %d", calls, test.wantCalls)
			}
			if test.wantErr == "" {
				if err != nil || observation.Scope.Platform != PlatformMacOS {
					t.Fatalf("observeStableDarwin() = %#v, %v", observation, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("observeStableDarwin() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

// darwinObservationResult supplies one stability-test result.
type darwinObservationResult struct {
	observation Observation
	err         error
}

// TestObserveStableDarwinPreservesErrorsAndCancellation proves only native generation races are retried.
func TestObserveStableDarwinPreservesErrorsAndCancellation(t *testing.T) {
	sentinel := unix.EPERM
	_, err := observeStableDarwin(context.Background(), mustRequest(t), func(context.Context, Request) (Observation, error) {
		return Observation{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("observeStableDarwin() error = %v, want EPERM", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = observeStableDarwin(ctx, mustRequest(t), func(context.Context, Request) (Observation, error) {
		t.Fatal("observer ran after cancellation")
		return Observation{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("observeStableDarwin() error = %v, want canceled", err)
	}
}

// TestWaitDarwinObservationRetryHonorsCancellation verifies a transient-race pause cannot keep a canceled caller alive.
func TestWaitDarwinObservationRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitDarwinObservationRetry(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitDarwinObservationRetry() error = %v, want canceled", err)
	}
}

// TestWaitDarwinObservationRetryIsBounded verifies transient-race settlement does not become an unbounded retry policy.
func TestWaitDarwinObservationRetryIsBounded(t *testing.T) {
	started := time.Now()
	if err := waitDarwinObservationRetry(context.Background()); err != nil {
		t.Fatalf("waitDarwinObservationRetry() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < darwinObservationRetryDelay || elapsed > time.Second {
		t.Fatalf("waitDarwinObservationRetry() elapsed = %s, want bounded delay", elapsed)
	}
}

// TestObserveDarwinPassComposesProcessGlobalFacts verifies scope and route-only socket elision.
func TestObserveDarwinPassComposesProcessGlobalFacts(t *testing.T) {
	request, err := NewPreAssignmentRequest(testCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	loopback := InterfaceIdentity{Name: "lo0", Index: 1}
	baseline := baselineRoute(loopback)
	baseline.NativeFlags = unix.RTF_UP
	socketCalls := 0
	observation, err := observeDarwinPassWith(context.Background(), request, darwinPassOperations{
		interfaces: func(context.Context) (darwinInterfaceSnapshot, error) {
			return darwinInterfaceSnapshot{
				loopback: LoopbackIdentity{Interface: loopback, Kind: LoopbackKindMacOSNative},
				byIndex:  map[uint32]InterfaceIdentity{1: loopback},
			}, nil
		},
		routes: func(context.Context, Request, darwinInterfaceSnapshot) (RouteSnapshot, error) {
			return RouteSnapshot{Complete: true, Selected: &baseline, Matching: []RouteFact{baseline}}, nil
		},
		sockets: func(context.Context, Request) (SocketSnapshot, error) {
			socketCalls++
			return SocketSnapshot{}, nil
		},
	})
	if err != nil {
		t.Fatalf("observeDarwinPassWith() error = %v", err)
	}
	if socketCalls != 0 || observation.Scope != NewMacOSScope() || observation.Loopback.Interface.WindowsLUID != 0 || !observation.Sockets.Complete {
		t.Fatalf("observeDarwinPassWith() = %#v after %d socket calls", observation, socketCalls)
	}
}

// TestValidateDarwinRIBFramesRejectsUnknownAndMalformedMessages closes x/net/route's silent-skip gap.
func TestValidateDarwinRIBFramesRejectsUnknownAndMalformedMessages(t *testing.T) {
	allowed := map[uint8]struct{}{unix.RTM_GET: {}}
	valid := append(darwinTestRIBFrame(unix.RTM_GET), darwinTestRIBFrame(unix.RTM_GET)...)
	count, err := validateDarwinRIBFrames(valid, 1024, allowed)
	if err != nil || count != 2 {
		t.Fatalf("validateDarwinRIBFrames() = %d, %v", count, err)
	}
	tests := []struct {
		name     string
		raw      []byte
		contains string
	}{
		{name: "truncated header", raw: []byte{1}, contains: "truncated"},
		{name: "zero length", raw: []byte{0, 0, darwinRoutingMessageVersion, unix.RTM_GET}, contains: "invalid length"},
		{name: "unaligned length", raw: []byte{5, 0, darwinRoutingMessageVersion, unix.RTM_GET, 0}, contains: "invalid length"},
		{name: "unknown version", raw: []byte{8, 0, 9, unix.RTM_GET, 0, 0, 0, 0}, contains: "unsupported version"},
		{name: "unknown type", raw: []byte{8, 0, darwinRoutingMessageVersion, 0xff, 0, 0, 0, 0}, contains: "unsupported type"},
		{name: "trailing bytes", raw: append(darwinTestRIBFrame(unix.RTM_GET), 1), contains: "truncated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateDarwinRIBFrames(test.raw, 1024, allowed); err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("validateDarwinRIBFrames() error = %v, want %q", err, test.contains)
			}
		})
	}
}

// TestDarwinInterfacesFromMessagesFindsOneNativeLoopback covers stable identity and RIB references.
func TestDarwinInterfacesFromMessagesFindsOneNativeLoopback(t *testing.T) {
	messages := []route.Message{
		&route.InterfaceMessage{Version: unix.RTM_VERSION, Type: unix.RTM_IFINFO2, Flags: darwinRequiredLoopbackFlags, Index: 1, Name: "lo0"},
		&route.InterfaceMessage{Version: unix.RTM_VERSION, Type: unix.RTM_IFINFO2, Flags: unix.IFF_UP, Index: 7, Name: "en0"},
		&route.InterfaceAddrMessage{Version: unix.RTM_VERSION, Type: unix.RTM_NEWADDR, Index: 1},
		&route.InterfaceMulticastAddrMessage{Version: unix.RTM_VERSION, Type: unix.RTM_NEWMADDR2, Index: 7},
	}
	snapshot, err := darwinInterfacesFromMessages(messages, func(message *route.InterfaceMessage) (int, error) {
		if message.Index == 1 {
			return unix.IFT_LOOP, nil
		}
		return 6, nil
	})
	if err != nil {
		t.Fatalf("darwinInterfacesFromMessages() error = %v", err)
	}
	if snapshot.loopback.Interface != (InterfaceIdentity{Name: "lo0", Index: 1}) || len(snapshot.byIndex) != 2 {
		t.Fatalf("darwinInterfacesFromMessages() = %#v", snapshot)
	}
}

// TestDarwinInterfacesFromMessagesRejectsAmbiguousIdentity covers duplicates, references, and loopback count.
func TestDarwinInterfacesFromMessagesRejectsAmbiguousIdentity(t *testing.T) {
	loopback := &route.InterfaceMessage{Type: unix.RTM_IFINFO2, Flags: darwinRequiredLoopbackFlags, Index: 1, Name: "lo0"}
	tests := []struct {
		name     string
		messages []route.Message
		contains string
	}{
		{name: "no loopback", messages: []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO2, Flags: unix.IFF_UP, Index: 2, Name: "en0"}}, contains: "count is 0"},
		{name: "duplicate index", messages: []route.Message{loopback, &route.InterfaceMessage{Type: unix.RTM_IFINFO2, Index: 1, Name: "other"}}, contains: "index 1 is duplicated"},
		{name: "duplicate name", messages: []route.Message{loopback, &route.InterfaceMessage{Type: unix.RTM_IFINFO2, Index: 2, Name: "lo0"}}, contains: "name"},
		{name: "unknown reference", messages: []route.Message{loopback, &route.InterfaceAddrMessage{Type: unix.RTM_NEWADDR, Index: 9}}, contains: "unknown index"},
		{name: "invalid name", messages: []route.Message{&route.InterfaceMessage{Type: unix.RTM_IFINFO2, Flags: darwinRequiredLoopbackFlags, Index: 1, Name: "bad\x00name"}}, contains: "cannot identify"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := darwinInterfacesFromMessages(test.messages, func(*route.InterfaceMessage) (int, error) { return unix.IFT_LOOP, nil })
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("darwinInterfacesFromMessages() error = %v, want %q", err, test.contains)
			}
		})
	}
}

// TestDarwinRoutesFromMessagesNormalizesSelectionAndClones covers every matching route and cloned host semantics.
func TestDarwinRoutesFromMessagesNormalizesSelectionAndClones(t *testing.T) {
	interfaces := darwinTestInterfaces()
	selected := darwinTestRoute(testCandidate, netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP|unix.RTF_DONE)
	selected.Addrs[unix.RTAX_GATEWAY] = &route.Inet4Addr{IP: netip.MustParseAddr("127.0.0.1").As4()}
	baseline := darwinTestRoute(netip.MustParseAddr("127.0.0.0"), netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP|unix.RTF_CLONING)
	defaultRoute := darwinTestRoute(netip.IPv4Unspecified(), netip.IPv4Unspecified(), 2, unix.RTF_UP|unix.RTF_GATEWAY)
	defaultRoute.Addrs[unix.RTAX_GATEWAY] = &route.Inet4Addr{IP: netip.MustParseAddr("192.0.2.1").As4()}
	snapshot, err := darwinRoutesFromMessages([]route.Message{baseline, defaultRoute}, selected, testCandidate, interfaces)
	if err != nil {
		t.Fatalf("darwinRoutesFromMessages() error = %v", err)
	}
	if !snapshot.Complete || len(snapshot.Matching) != 1 || snapshot.Selected == nil || snapshot.Selected.Destination.String() != "127.0.0.0/8" {
		t.Fatalf("darwinRoutesFromMessages() = %#v", snapshot)
	}
	if snapshot.Selected.NativeFlags != unix.RTF_UP|unix.RTF_CLONING {
		t.Fatalf("selected native flags = %#x, want RIB flags", snapshot.Selected.NativeFlags)
	}

	clone := darwinTestRoute(testCandidate, netip.MustParseAddr("255.255.255.255"), 1, unix.RTF_UP|unix.RTF_HOST|unix.RTF_WASCLONED|unix.RTF_LLINFO)
	cloneSnapshot, err := darwinRoutesFromMessages([]route.Message{clone}, clone, testCandidate, interfaces)
	if err != nil {
		t.Fatalf("darwinRoutesFromMessages(clone) error = %v", err)
	}
	if cloneSnapshot.Selected == nil || cloneSnapshot.Selected.Normalization != RouteNormalizationMacOSCloneUnresolved {
		t.Fatalf("darwinRoutesFromMessages(clone) = %#v", cloneSnapshot)
	}
}

// TestDarwinRoutesFromMessagesIgnoresOnlyLessSpecificDumpNoise keeps foreign default routes outside loopback admission authority.
func TestDarwinRoutesFromMessagesIgnoresOnlyLessSpecificDumpNoise(t *testing.T) {
	interfaces := darwinTestInterfaces()
	selected := darwinTestRoute(testCandidate, netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP|unix.RTF_DONE)
	baseline := darwinTestRoute(netip.MustParseAddr("127.0.0.0"), netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP)
	unknownFlag := int(uint32(0x80000000))
	hostileDefault := darwinTestRoute(netip.IPv4Unspecified(), netip.IPv4Unspecified(), 2, unix.RTF_UP|unknownFlag)
	hostileDefault.Err = unix.EPERM
	hostileDefault.Addrs[unix.RTAX_GATEWAY] = &route.Inet6Addr{}

	snapshot, err := darwinRoutesFromMessages([]route.Message{baseline, hostileDefault}, selected, testCandidate, interfaces)
	if err != nil || len(snapshot.Matching) != 1 || snapshot.Selected == nil || snapshot.Selected.Destination.Bits() != 8 {
		t.Fatalf("darwinRoutesFromMessages(default dump) = %#v, error %v", snapshot, err)
	}

	selectedDefault := *hostileDefault
	selectedDefault.Err = nil
	selectedDefault.Flags = unix.RTF_UP | unix.RTF_GATEWAY
	selectedDefault.Addrs = append([]route.Addr(nil), hostileDefault.Addrs...)
	selectedDefault.Addrs[unix.RTAX_DST] = &route.Inet4Addr{IP: testCandidate.As4()}
	if _, err := darwinRoutesFromMessages([]route.Message{baseline}, &selectedDefault, testCandidate, interfaces); err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("darwinRoutesFromMessages(selected default) error = %v, want unsupported address type", err)
	}
}

// TestDarwinRoutesFromMessagesValidatesEveryLoopbackSpecificDumpRoute keeps candidate conflicts fail-closed.
func TestDarwinRoutesFromMessagesValidatesEveryLoopbackSpecificDumpRoute(t *testing.T) {
	interfaces := darwinTestInterfaces()
	selected := darwinTestRoute(testCandidate, netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP|unix.RTF_DONE)
	baseline := darwinTestRoute(netip.MustParseAddr("127.0.0.0"), netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP)
	unknownFlag := int(uint32(0x80000000))
	tests := []struct {
		name     string
		message  *route.RouteMessage
		contains string
	}{
		{
			name:     "loopback baseline",
			message:  darwinTestRoute(netip.MustParseAddr("127.0.0.0"), netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP|unknownFlag),
			contains: "unknown flags",
		},
		{
			name: "candidate host conflict",
			message: func() *route.RouteMessage {
				message := darwinTestRoute(testCandidate, netip.MustParseAddr("255.255.255.255"), 1, unix.RTF_UP|unix.RTF_HOST)
				message.Err = unix.EPERM
				return message
			}(),
			contains: "message status",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := darwinRoutesFromMessages([]route.Message{baseline, test.message}, selected, testCandidate, interfaces); err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("darwinRoutesFromMessages() error = %v, want %q", err, test.contains)
			}
		})
	}
}

// TestDarwinRouteGatewayNormalizesOnlyNativeBaselineMetadata binds XNU's direct-loopback response shape narrowly.
func TestDarwinRouteGatewayNormalizesOnlyNativeBaselineMetadata(t *testing.T) {
	interfaces := darwinTestInterfaces()
	loopback := interfaces.loopback.Interface
	ordinary := interfaces.byIndex[2]
	loopbackGateway := netip.MustParseAddr("127.0.0.1")
	tests := []struct {
		name           string
		destination    netip.Prefix
		identity       InterfaceIdentity
		nativeLoopback bool
		flags          uint32
		gateway        netip.Addr
		wantGateway    netip.Addr
		wantErr        string
	}{
		{
			name:           "native baseline metadata",
			destination:    darwinOrdinaryLoopbackPrefix,
			identity:       loopback,
			nativeLoopback: true,
			gateway:        loopbackGateway,
		},
		{
			name:           "native host route metadata",
			destination:    netip.PrefixFrom(loopbackGateway, 32),
			identity:       loopback,
			nativeLoopback: true,
			flags:          unix.RTF_HOST,
			gateway:        loopbackGateway,
		},
		{
			name:        "ordinary interface",
			destination: darwinOrdinaryLoopbackPrefix,
			identity:    ordinary,
			gateway:     loopbackGateway,
			wantErr:     "inconsistent IPv4 gateway evidence",
		},
		{
			name:           "more specific route",
			destination:    netip.PrefixFrom(testCandidate, 32),
			identity:       loopback,
			nativeLoopback: true,
			gateway:        loopbackGateway,
			wantErr:        "inconsistent IPv4 gateway evidence",
		},
		{
			name:           "foreign address",
			destination:    darwinOrdinaryLoopbackPrefix,
			identity:       loopback,
			nativeLoopback: true,
			gateway:        netip.MustParseAddr("192.0.2.1"),
			wantErr:        "inconsistent IPv4 gateway evidence",
		},
		{
			name:           "actual gateway route",
			destination:    darwinOrdinaryLoopbackPrefix,
			identity:       loopback,
			nativeLoopback: true,
			flags:          unix.RTF_GATEWAY,
			gateway:        loopbackGateway,
			wantGateway:    loopbackGateway,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := darwinTestRoute(test.destination.Addr(), netip.MustParseAddr("255.0.0.0"), int(test.identity.Index), int(test.flags))
			message.Addrs[unix.RTAX_GATEWAY] = &route.Inet4Addr{IP: test.gateway.As4()}
			gateway, err := darwinRouteGateway(message, test.flags, test.identity, test.destination, test.nativeLoopback)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("darwinRouteGateway() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || gateway != test.wantGateway {
				t.Fatalf("darwinRouteGateway() = %s, %v, want %s", gateway, err, test.wantGateway)
			}
		})
	}
}

// TestDarwinRoutesFromMessagesRejectsRacesAndUnknownSemantics exercises fail-closed route normalization.
func TestDarwinRoutesFromMessagesRejectsRacesAndUnknownSemantics(t *testing.T) {
	interfaces := darwinTestInterfaces()
	selected := darwinTestRoute(testCandidate, netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP)
	baseline := darwinTestRoute(netip.MustParseAddr("127.0.0.0"), netip.MustParseAddr("255.0.0.0"), 1, unix.RTF_UP)
	if _, err := darwinRoutesFromMessages(nil, selected, testCandidate, interfaces); !errors.Is(err, errDarwinRouteSnapshotChanged) {
		t.Fatalf("absent selected error = %v", err)
	}
	duplicate := *baseline
	duplicate.Flags |= unix.RTF_STATIC
	if _, err := darwinRoutesFromMessages([]route.Message{baseline, &duplicate}, selected, testCandidate, interfaces); !errors.Is(err, errDarwinRouteSnapshotChanged) {
		t.Fatalf("ambiguous selected authority error = %v", err)
	}
	tests := []struct {
		name     string
		mutate   func(*route.RouteMessage)
		contains string
	}{
		{name: "unknown flags", mutate: func(message *route.RouteMessage) { message.Flags |= int(uint32(0x80000000)) }, contains: "unknown flags"},
		{name: "not up", mutate: func(message *route.RouteMessage) { message.Flags &^= unix.RTF_UP }, contains: "not up"},
		{name: "noncontiguous mask", mutate: func(message *route.RouteMessage) {
			message.Addrs[unix.RTAX_NETMASK] = &route.Inet4Addr{IP: [4]byte{255, 0, 255, 0}}
		}, contains: "not contiguous"},
		{name: "unknown interface", mutate: func(message *route.RouteMessage) {
			message.Index = 99
			message.Addrs[unix.RTAX_IFP] = &route.LinkAddr{Index: 99, Name: "lost"}
		}, contains: "unknown interface"},
		{name: "name mismatch", mutate: func(message *route.RouteMessage) {
			message.Addrs[unix.RTAX_IFP] = &route.LinkAddr{Index: 1, Name: "wrong"}
		}, contains: "does not match"},
		{name: "IPv6 slot", mutate: func(message *route.RouteMessage) { message.Addrs[unix.RTAX_IFA] = &route.Inet6Addr{} }, contains: "unsupported type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := *baseline
			message.Addrs = append([]route.Addr(nil), baseline.Addrs...)
			test.mutate(&message)
			_, _, err := darwinRouteFact(&message, testCandidate, interfaces, false)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("darwinRouteFact() error = %v, want %q", err, test.contains)
			}
		})
	}
}

// TestCanonicalDarwinRouteFlagsDropsOnlyResponseConfirmation binds every persistent native flag.
func TestCanonicalDarwinRouteFlagsDropsOnlyResponseConfirmation(t *testing.T) {
	persistent := uint32(unix.RTF_UP | unix.RTF_CLONING | unix.RTF_PRCLONING | unix.RTF_IFSCOPE | unix.RTF_PROXY | unix.RTF_ROUTER | unix.RTF_GLOBAL)
	if got := canonicalDarwinRouteFlags(persistent | unix.RTF_DONE); got != persistent {
		t.Fatalf("canonicalDarwinRouteFlags() = %#x, want %#x", got, persistent)
	}
	if got := canonicalDarwinRouteFlags(unix.RTF_DONE); got != 0 {
		t.Fatalf("canonicalDarwinRouteFlags(RTF_DONE) = %#x, want zero", got)
	}
}

// TestNextDarwinRouteSequenceIsConcurrentAndNonzero prevents broadcast replies from crossing simultaneous lookups.
func TestNextDarwinRouteSequenceIsConcurrentAndNonzero(t *testing.T) {
	const allocations = 512
	sequences := make(chan int, allocations)
	var wait sync.WaitGroup
	for range allocations {
		wait.Add(1)
		go func() {
			defer wait.Done()
			sequences <- nextDarwinRouteSequence()
		}()
	}
	wait.Wait()
	close(sequences)
	seen := make(map[int]struct{}, allocations)
	for sequence := range sequences {
		if sequence <= 0 {
			t.Fatalf("nextDarwinRouteSequence() = %d, want positive", sequence)
		}
		if _, exists := seen[sequence]; exists {
			t.Fatalf("nextDarwinRouteSequence() duplicated %d", sequence)
		}
		seen[sequence] = struct{}{}
	}
}

// TestNewDarwinSelectedRouteQueryRequestsIPv4InterfaceEvidence binds the outbound RTM_GET contract.
func TestNewDarwinSelectedRouteQueryRequestsIPv4InterfaceEvidence(t *testing.T) {
	query, err := newDarwinSelectedRouteQuery(testCandidate, 731, 19)
	if err != nil {
		t.Fatalf("newDarwinSelectedRouteQuery() error = %v", err)
	}
	wantFlags := unix.RTF_UP | unix.RTF_HOST | unix.RTF_GATEWAY
	if query.Version != unix.RTM_VERSION || query.Type != unix.RTM_GET || query.Flags != wantFlags || query.ID != 731 || query.Seq != 19 {
		t.Fatalf("newDarwinSelectedRouteQuery() = %#v", query)
	}
	if len(query.Addrs) != unix.RTAX_MAX {
		t.Fatalf("query addresses = %d, want %d", len(query.Addrs), unix.RTAX_MAX)
	}
	destination, ok := query.Addrs[unix.RTAX_DST].(*route.Inet4Addr)
	if !ok || destination.IP != testCandidate.As4() {
		t.Fatalf("query destination = %#v", query.Addrs[unix.RTAX_DST])
	}
	if interfaceAddress, ok := query.Addrs[unix.RTAX_IFP].(*route.LinkAddr); !ok || interfaceAddress == nil {
		t.Fatalf("query interface request = %#v", query.Addrs[unix.RTAX_IFP])
	}
	encoded, err := query.Marshal()
	if err != nil {
		t.Fatalf("RouteMessage.Marshal() error = %v", err)
	}
	if len(encoded) > maximumDarwinRouteDatagram || binary.NativeEndian.Uint16(encoded[:2]) != uint16(len(encoded)) {
		t.Fatalf("encoded query length = %d, header = %d", len(encoded), binary.NativeEndian.Uint16(encoded[:2]))
	}
	wantAddresses := uint32(unix.RTA_DST | unix.RTA_IFP)
	if got := binary.NativeEndian.Uint32(encoded[12:16]); got != wantAddresses {
		t.Fatalf("encoded query address mask = %#x, want %#x", got, wantAddresses)
	}
	destinationOffset := unix.SizeofRtMsghdr
	if encoded[destinationOffset] != unix.SizeofSockaddrInet4 || encoded[destinationOffset+1] != unix.AF_INET {
		t.Fatalf("encoded destination sockaddr = %v", encoded[destinationOffset:destinationOffset+unix.SizeofSockaddrInet4])
	}
}

// TestNewDarwinSelectedRouteQueryRejectsInvalidIdentity keeps native integer truncation out of route requests.
func TestNewDarwinSelectedRouteQueryRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		name      string
		candidate netip.Addr
		processID uintptr
		sequence  int
	}{
		{name: "IPv6", candidate: netip.IPv6Loopback(), processID: 1, sequence: 1},
		{name: "zero process", candidate: testCandidate, processID: 0, sequence: 1},
		{name: "wide process", candidate: testCandidate, processID: uintptr(uint64(1) << 32), sequence: 1},
		{name: "zero sequence", candidate: testCandidate, processID: 1, sequence: 0},
		{name: "wide sequence", candidate: testCandidate, processID: 1, sequence: int(maximumDarwinRouteSequence) + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newDarwinSelectedRouteQuery(test.candidate, test.processID, test.sequence); err == nil {
				t.Fatal("newDarwinSelectedRouteQuery() error = nil")
			}
		})
	}
}

// TestNormalizeDarwinRouteStatusesReadsRTMErrno proves route-use counters cannot impersonate Darwin errors.
func TestNormalizeDarwinRouteStatusesReadsRTMErrno(t *testing.T) {
	tests := []struct {
		name        string
		messageType uint8
		status      syscall.Errno
		use         uint32
		want        error
	}{
		{name: "successful lookup with errno-shaped use", messageType: unix.RTM_GET, use: uint32(unix.EAFNOSUPPORT)},
		{name: "failed lookup with zero use", messageType: unix.RTM_GET, status: unix.EPERM, want: unix.EPERM},
		{name: "RIB record has no operation status", messageType: unix.RTM_GET2, status: unix.EPERM, use: uint32(unix.EAFNOSUPPORT)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := darwinTestRouteStatusFrame(test.messageType, test.status, test.use)
			messages, err := route.ParseRIB(route.RIBTypeRoute, raw)
			if err != nil || len(messages) != 1 {
				t.Fatalf("route.ParseRIB() = %#v, %v", messages, err)
			}
			if err := normalizeDarwinRouteStatuses(raw, messages); err != nil {
				t.Fatalf("normalizeDarwinRouteStatuses() error = %v", err)
			}
			message, ok := messages[0].(*route.RouteMessage)
			if !ok {
				t.Fatalf("route.ParseRIB() message = %T", messages[0])
			}
			if !errors.Is(message.Err, test.want) || (test.want == nil && message.Err != nil) {
				t.Fatalf("normalized route error = %v, want %v", message.Err, test.want)
			}
		})
	}
}

// TestWriteDarwinRouteQueryRequiresOneAtomicRecord covers interruption, short writes, and errno fidelity.
func TestWriteDarwinRouteQueryRequiresOneAtomicRecord(t *testing.T) {
	encoded := []byte{1, 2, 3, 4}
	calls := 0
	err := writeDarwinRouteQueryWith(7, encoded, func(fileDescriptor int, payload []byte) (int, error) {
		calls++
		if fileDescriptor != 7 || len(payload) != len(encoded) {
			t.Fatalf("write arguments = %d, %v", fileDescriptor, payload)
		}
		if calls == 1 {
			return 0, unix.EINTR
		}
		return len(payload), nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("writeDarwinRouteQueryWith() error = %v after %d calls", err, calls)
	}
	if err := writeDarwinRouteQueryWith(7, encoded, func(int, []byte) (int, error) { return 2, nil }); err == nil || !strings.Contains(err.Error(), "non-atomic") {
		t.Fatalf("short write error = %v", err)
	}
	if err := writeDarwinRouteQueryWith(7, encoded, func(int, []byte) (int, error) { return 0, unix.EPERM }); !errors.Is(err, unix.EPERM) {
		t.Fatalf("write errno = %v, want EPERM", err)
	}
}

// TestReceiveDarwinSelectedRouteReadsRawFramesWithoutSenderAddress proves AF_ROUTE metadata cannot discard a valid kernel reply.
func TestReceiveDarwinSelectedRouteReadsRawFramesWithoutSenderAddress(t *testing.T) {
	raw := darwinTestRouteStatusFrame(unix.RTM_GET, 0, uint32(unix.EAFNOSUPPORT))
	waits := 0
	reads := 0
	selected, err := receiveDarwinSelectedRouteWith(
		context.Background(),
		7,
		731,
		19,
		func(ctx context.Context, fileDescriptor int) error {
			waits++
			if ctx == nil || fileDescriptor != 7 {
				t.Fatalf("wait arguments = %#v, %d", ctx, fileDescriptor)
			}
			return nil
		},
		func(fileDescriptor int, buffer []byte) (int, error) {
			reads++
			if fileDescriptor != 7 || len(buffer) != maximumDarwinRouteDatagram {
				t.Fatalf("read arguments = %d, %d bytes", fileDescriptor, len(buffer))
			}
			return copy(buffer, raw), nil
		},
	)
	if err != nil || selected == nil || selected.ID != 731 || selected.Seq != 19 || selected.Err != nil {
		t.Fatalf("receiveDarwinSelectedRouteWith() = %#v, %v", selected, err)
	}
	if waits != 1 || reads != 1 {
		t.Fatalf("receive calls = %d waits, %d reads", waits, reads)
	}
}

// TestReceiveDarwinSelectedRouteRejectsBoundSizedReads preserves fail-closed truncation detection without recvmsg flags.
func TestReceiveDarwinSelectedRouteRejectsBoundSizedReads(t *testing.T) {
	_, err := receiveDarwinSelectedRouteWith(
		context.Background(),
		7,
		731,
		19,
		func(context.Context, int) error { return nil },
		func(_ int, buffer []byte) (int, error) { return len(buffer), nil },
	)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("receiveDarwinSelectedRouteWith() error = %v, want truncated", err)
	}
}

// TestDarwinNativeObserverSeesLiveUnprivilegedListener exercises sysctl, routing socket, and process-global scope on macOS.
func TestDarwinNativeObserverSeesLiveUnprivilegedListener(t *testing.T) {
	fileDescriptor, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, unix.IPPROTO_TCP)
	if err != nil {
		t.Fatalf("unix.Socket() error = %v", err)
	}
	defer unix.Close(fileDescriptor)
	if err := unix.Bind(fileDescriptor, &unix.SockaddrInet4{}); err != nil {
		t.Fatalf("unix.Bind() error = %v", err)
	}
	if err := unix.Listen(fileDescriptor, 1); err != nil {
		t.Fatalf("unix.Listen() error = %v", err)
	}
	bound, err := unix.Getsockname(fileDescriptor)
	if err != nil {
		t.Fatalf("unix.Getsockname() error = %v", err)
	}
	address, ok := bound.(*unix.SockaddrInet4)
	if !ok || address.Port <= 0 || address.Port > 65535 {
		t.Fatalf("unix.Getsockname() = %#v", bound)
	}
	request, err := NewPreAssignmentRequest(testCandidate, []SocketRequirement{{Transport: TransportTCP4, Port: uint16(address.Port)}})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := ObserveDarwin(context.Background(), request)
	if err != nil {
		t.Fatalf("ObserveDarwin() error = %v", err)
	}
	assessment, err := observation.Classify()
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if observation.Scope != NewMacOSScope() || observation.Loopback.Interface.WindowsLUID != 0 || assessment.Sockets != StateConflict {
		t.Fatalf("ObserveDarwin() = %#v, assessment %#v", observation, assessment)
	}
}

// TestDarwinNativeObserverProvesFreshRouteCandidateSafe gates absent-assignment admission on a real macOS RIB.
func TestDarwinNativeObserverProvesFreshRouteCandidateSafe(t *testing.T) {
	candidate := netip.MustParseAddr("127.77.254.250")
	request, err := NewPreAssignmentRequest(candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := ObserveDarwin(context.Background(), request)
	if err != nil {
		t.Fatalf("ObserveDarwin() error = %v", err)
	}
	assessment, err := observation.Classify()
	if err != nil {
		t.Fatalf("Classify() error = %v", err)
	}
	if assessment.Routes != StateSafe || assessment.State != StateSafe {
		t.Fatalf("fresh route-only assessment = %#v; observation = %#v", assessment, observation)
	}
}

// darwinTestRIBFrame returns the smallest aligned routing-message envelope accepted by the raw pre-scan.
func darwinTestRIBFrame(messageType uint8) []byte {
	raw := make([]byte, 8)
	binary.NativeEndian.PutUint16(raw[:2], uint16(len(raw)))
	raw[2] = darwinRoutingMessageVersion
	raw[3] = messageType
	return raw
}

// darwinTestRouteStatusFrame creates one parseable XNU route header with independently controlled errno and use fields.
func darwinTestRouteStatusFrame(messageType uint8, status syscall.Errno, use uint32) []byte {
	raw := make([]byte, unix.SizeofRtMsghdr+unix.SizeofSockaddrInet4)
	binary.NativeEndian.PutUint16(raw[:2], uint16(len(raw)))
	raw[2] = darwinRoutingMessageVersion
	raw[3] = messageType
	binary.NativeEndian.PutUint32(raw[8:12], unix.RTF_UP)
	binary.NativeEndian.PutUint32(raw[12:16], unix.RTA_DST)
	binary.NativeEndian.PutUint32(raw[16:20], 731)
	binary.NativeEndian.PutUint32(raw[20:24], 19)
	binary.NativeEndian.PutUint32(raw[darwinRouteErrnoOffset:darwinRouteErrnoOffset+4], uint32(status))
	binary.NativeEndian.PutUint32(raw[28:32], use)
	destinationOffset := unix.SizeofRtMsghdr
	raw[destinationOffset] = unix.SizeofSockaddrInet4
	raw[destinationOffset+1] = unix.AF_INET
	copy(raw[destinationOffset+4:destinationOffset+8], testCandidate.AsSlice())
	return raw
}

// darwinTestInterfaces returns native loopback and ordinary interface identities.
func darwinTestInterfaces() darwinInterfaceSnapshot {
	loopback := InterfaceIdentity{Name: "lo0", Index: 1}
	ordinary := InterfaceIdentity{Name: "en0", Index: 2}
	return darwinInterfaceSnapshot{
		loopback: LoopbackIdentity{Interface: loopback, Kind: LoopbackKindMacOSNative},
		byIndex:  map[uint32]InterfaceIdentity{1: loopback, 2: ordinary},
	}
}

// darwinTestRoute creates one IPv4 route with canonical interface evidence.
func darwinTestRoute(destination netip.Addr, mask netip.Addr, index int, flags int) *route.RouteMessage {
	name := "lo0"
	if index == 2 {
		name = "en0"
	}
	addresses := make([]route.Addr, unix.RTAX_MAX)
	addresses[unix.RTAX_DST] = &route.Inet4Addr{IP: destination.As4()}
	addresses[unix.RTAX_NETMASK] = &route.Inet4Addr{IP: mask.As4()}
	addresses[unix.RTAX_IFP] = &route.LinkAddr{Index: index, Name: name}
	return &route.RouteMessage{
		Version: unix.RTM_VERSION,
		Type:    unix.RTM_GET,
		Flags:   flags,
		Index:   index,
		Addrs:   addresses,
	}
}
