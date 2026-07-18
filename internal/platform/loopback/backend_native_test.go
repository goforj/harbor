//go:build linux || darwin

package loopback

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"testing"
)

// TestPlatformCommandRejectsAmbientProcessState proves fixed native tools cannot inherit project configuration or cwd.
func TestPlatformCommandRejectsAmbientProcessState(t *testing.T) {
	command := newPlatformCommand(context.Background(), "/fixed/tool", "one", "two")
	if command.Path != "/fixed/tool" || !reflect.DeepEqual(command.Args, []string{"/fixed/tool", "one", "two"}) {
		t.Fatalf("newPlatformCommand() target = %q %#v", command.Path, command.Args)
	}
	if command.Dir != "/" || command.Env == nil || len(command.Env) != 0 {
		t.Fatalf("newPlatformCommand() process state = dir %q, env %#v", command.Dir, command.Env)
	}
}

// TestNativeAddressFactPreservesTheAssignedHost proves non-/32 conflicts retain their local address.
func TestNativeAddressFactPreservesTheAssignedHost(t *testing.T) {
	address, prefixLength, ok := nativeAddressFact(&net.IPNet{
		IP:   net.ParseIP("127.77.0.10").To4(),
		Mask: net.CIDRMask(8, 32),
	})
	if !ok || address != netip.MustParseAddr("127.77.0.10") || prefixLength != 8 {
		t.Fatalf("nativeAddressFact() = %s/%d, %t", address, prefixLength, ok)
	}

	address, prefixLength, ok = nativeAddressFact(&net.IPAddr{IP: net.ParseIP("127.77.0.10")})
	if !ok || address != netip.MustParseAddr("127.77.0.10") || prefixLength != 32 {
		t.Fatalf("nativeAddressFact(IPAddr) = %s/%d, %t", address, prefixLength, ok)
	}

	if _, _, ok := nativeAddressFact(testNetworkAddress("not-an-ip")); ok {
		t.Fatal("nativeAddressFact() accepted an unsupported network address")
	}
	if _, _, ok := nativeAddressFact(&net.IPNet{IP: net.IP{1, 2, 3}, Mask: net.CIDRMask(8, 32)}); ok {
		t.Fatal("nativeAddressFact() accepted an invalid IP")
	}
	if _, _, ok := nativeAddressFact(&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)}); ok {
		t.Fatal("nativeAddressFact() accepted IPv6")
	}
	if _, _, ok := nativeAddressFact(&net.IPAddr{IP: net.ParseIP("::1")}); ok {
		t.Fatal("nativeAddressFact() accepted an IPv6 IPAddr")
	}
}

// TestNativeFactsHonorCancellation avoids beginning host enumeration after cancellation.
func TestNativeFactsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := nativeInterfaceFacts(ctx, "lo", InterfaceKindLinuxNative, func() (bool, error) { return true, nil }); err == nil {
		t.Fatal("nativeInterfaceFacts() accepted a canceled context")
	}
	if _, err := nativeAssignmentFacts(ctx, testAddress); err == nil {
		t.Fatal("nativeAssignmentFacts() accepted a canceled context")
	}
}

// TestNativeBackendObservesWithoutMutation exercises native address enumeration on Unix CI hosts.
func TestNativeBackendObservesWithoutMutation(t *testing.T) {
	adapter := New()
	observation, err := adapter.Observe(context.Background(), netip.MustParseAddr("127.254.254.254"))
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.State != StateAbsent {
		t.Skipf("host already assigns the reserved test identity: %+v", observation)
	}
}

// TestNativeBackendHonorsCancellationBeforeMutation proves canceled requests cannot invoke platform tools.
func TestNativeBackendHonorsCancellationBeforeMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prefix := netip.PrefixFrom(testAddress, 32)
	platform := platformBackend{}
	if err := platform.ensure(ctx, testLoopback, prefix); err == nil {
		t.Fatal("ensure() accepted a canceled context")
	}
	if err := platform.release(ctx, testLoopback, prefix); err == nil {
		t.Fatal("release() accepted a canceled context")
	}
}

// testNetworkAddress supplies an unsupported net.Addr implementation.
type testNetworkAddress string

// Network identifies the fake address family used by the rejection test.
func (testNetworkAddress) Network() string {
	return "test"
}

// String returns the intentionally unparseable test value.
func (address testNetworkAddress) String() string {
	return string(address)
}
