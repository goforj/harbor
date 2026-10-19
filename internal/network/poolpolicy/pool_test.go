package poolpolicy

import (
	"net/netip"
	"testing"
)

// TestPoolPolicyBounds protects both legacy upgrades and the maximum privileged mutation scope.
func TestPoolPolicyBounds(t *testing.T) {
	for value, want := range map[string]int{"127.77.0.0/24": 256, "127.77.0.8/29": 8} {
		prefix := netip.MustParsePrefix(value)
		addresses, err := Addresses(prefix)
		if err != nil || len(addresses) != want || addresses[0] != prefix.Addr() {
			t.Fatalf("Addresses(%s) = %d, %v", value, len(addresses), err)
		}
		for i, a := range addresses {
			if !prefix.Contains(a) || i > 0 && a != addresses[i-1].Next() {
				t.Fatal("noncanonical enumeration")
			}
		}
	}
	for _, value := range []string{"192.0.2.0/24", "127.77.0.0/23", "127.77.0.0/28", "127.77.0.1/24", "127.77.0.1/32", "::1/128"} {
		if _, err := Addresses(netip.MustParsePrefix(value)); err == nil {
			t.Fatalf("unbounded or noncanonical pool %s admitted", value)
		}
	}
	if err := Expansion(netip.MustParsePrefix("127.77.0.8/29"), netip.MustParsePrefix("127.77.0.0/24")); err != nil {
		t.Fatal(err)
	}
	if err := Expansion(netip.MustParsePrefix("127.77.0.8/29"), netip.MustParsePrefix("127.77.1.0/24")); err == nil {
		t.Fatal("relocation admitted")
	}
}
