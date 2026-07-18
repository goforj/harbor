package dnsserver

import (
	"net/netip"
	"testing"
)

// FuzzNewSnapshotNames verifies arbitrary record names are either rejected or remain safely publishable.
func FuzzNewSnapshotNames(f *testing.F) {
	for _, seed := range []string{"orders.test", "mysql.orders.test", "Orders.test", "*.test", "orders.example", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		snapshot, err := NewSnapshot([]Record{{Name: name, Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
		if err != nil {
			return
		}
		if err := snapshot.validate(); err != nil {
			t.Fatalf("accepted snapshot validate() error = %v", err)
		}
	})
}
