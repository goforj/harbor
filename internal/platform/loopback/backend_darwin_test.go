//go:build darwin

package loopback

import (
	"net/netip"
	"reflect"
	"testing"
)

// TestDarwinEnsureArgumentsRequireAnExplicitHostMask prevents classful loopback inference from weakening a /32 request.
func TestDarwinEnsureArgumentsRequireAnExplicitHostMask(t *testing.T) {
	interf := InterfaceFact{Name: "lo0", Index: 1, Kind: InterfaceKindDarwinNative, NativeLoopback: true}
	prefix := netip.MustParsePrefix("127.77.0.10/32")
	want := []string{"lo0", "inet", "127.77.0.10/32", "alias"}
	if got := darwinEnsureArguments(interf, prefix); !reflect.DeepEqual(got, want) {
		t.Fatalf("darwinEnsureArguments() = %#v, want %#v", got, want)
	}
}
