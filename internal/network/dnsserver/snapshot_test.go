package dnsserver

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

// TestNewSnapshotCopiesAndOrdersRecords verifies the public snapshot is immutable and canonical.
func TestNewSnapshotCopiesAndOrdersRecords(t *testing.T) {
	records := []Record{
		{Name: "redis.orders.test", Address: netip.MustParseAddr("127.77.0.10")},
		{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")},
	}

	snapshot, err := NewSnapshot(records, DefaultTTL)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	records[0] = Record{Name: "changed.test", Address: netip.MustParseAddr("127.0.0.9")}

	got := snapshot.Records()
	want := []Record{
		{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")},
		{Name: "redis.orders.test", Address: netip.MustParseAddr("127.77.0.10")},
	}
	if len(got) != len(want) {
		t.Fatalf("Records() length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("Records()[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
	got[0] = Record{Name: "mutated.test", Address: netip.MustParseAddr("127.0.0.8")}
	if snapshot.Records()[0] != want[0] {
		t.Fatal("Records() exposed mutable snapshot storage")
	}
	if gotTTL := snapshot.TTL(); gotTTL != DefaultTTL {
		t.Errorf("TTL() = %s, want %s", gotTTL, DefaultTTL)
	}
	if err := snapshot.validate(); err != nil {
		t.Errorf("validate() error = %v", err)
	}
}

// TestNewSnapshotAcceptsEmptyRecordSet verifies empty authority can be published deliberately.
func TestNewSnapshotAcceptsEmptyRecordSet(t *testing.T) {
	snapshot, err := NewSnapshot(nil, MinTTL)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	if records := snapshot.Records(); len(records) != 0 {
		t.Errorf("Records() = %#v, want empty", records)
	}
}

// TestNewSnapshotRejectsInvalidTTL exercises every TTL boundary and precision branch.
func TestNewSnapshotRejectsInvalidTTL(t *testing.T) {
	tests := []struct {
		name string
		ttl  time.Duration
		want string
	}{
		{name: "zero", ttl: 0, want: "between"},
		{name: "below minimum", ttl: MinTTL - time.Nanosecond, want: "between"},
		{name: "above maximum", ttl: MaxTTL + time.Second, want: "between"},
		{name: "fractional second", ttl: MinTTL + time.Nanosecond, want: "whole seconds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSnapshot(nil, test.ttl)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewSnapshot() error = %v, want containing %q", err, test.want)
			}
		})
	}

	for _, ttl := range []time.Duration{MinTTL, MaxTTL} {
		if _, err := NewSnapshot(nil, ttl); err != nil {
			t.Errorf("NewSnapshot(ttl %s) error = %v", ttl, err)
		}
	}
}

// TestNewSnapshotRejectsInvalidRecords covers canonical-name and address validation failures.
func TestNewSnapshotRejectsInvalidRecords(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	longName := strings.Repeat("a", 250) + ".test"
	longLabel := strings.Repeat("a", 64) + ".test"
	tests := []struct {
		name   string
		record Record
		want   string
	}{
		{name: "missing name", record: Record{Address: loopback}, want: "name is required"},
		{name: "long name", record: Record{Name: longName, Address: loopback}, want: "exceeds 253"},
		{name: "uppercase", record: Record{Name: "Orders.test", Address: loopback}, want: "lowercase"},
		{name: "trailing dot", record: Record{Name: "orders.test.", Address: loopback}, want: "trailing dot"},
		{name: "foreign zone", record: Record{Name: "orders.example", Address: loopback}, want: "outside"},
		{name: "zone apex", record: Record{Name: ".test", Address: loopback}, want: "between 1 and 63"},
		{name: "empty label", record: Record{Name: "api..orders.test", Address: loopback}, want: "between 1 and 63"},
		{name: "long label", record: Record{Name: longLabel, Address: loopback}, want: "between 1 and 63"},
		{name: "underscore", record: Record{Name: "api_v2.orders.test", Address: loopback}, want: "unsupported character"},
		{name: "wildcard", record: Record{Name: "*.orders.test", Address: loopback}, want: "unsupported character"},
		{name: "leading hyphen", record: Record{Name: "-api.orders.test", Address: loopback}, want: "must not start"},
		{name: "trailing hyphen", record: Record{Name: "api-.orders.test", Address: loopback}, want: "must not start"},
		{name: "missing address", record: Record{Name: "orders.test"}, want: "not IPv4 loopback"},
		{name: "IPv6", record: Record{Name: "orders.test", Address: netip.IPv6Loopback()}, want: "not IPv4 loopback"},
		{name: "public IPv4", record: Record{Name: "orders.test", Address: netip.MustParseAddr("192.0.2.1")}, want: "not IPv4 loopback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSnapshot([]Record{test.record}, DefaultTTL)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewSnapshot() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestNewSnapshotRejectsDuplicateNames ensures address equality cannot hide publication collisions.
func TestNewSnapshotRejectsDuplicateNames(t *testing.T) {
	_, err := NewSnapshot([]Record{
		{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")},
		{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.2")},
	}, DefaultTTL)
	if err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Fatalf("NewSnapshot() error = %v, want duplicate name", err)
	}
}

// TestSnapshotValidateRejectsCorruption protects publication against invalid package-local values.
func TestSnapshotValidateRejectsCorruption(t *testing.T) {
	valid, err := NewSnapshot([]Record{{Name: "orders.test", Address: netip.MustParseAddr("127.0.0.1")}}, DefaultTTL)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	tests := []struct {
		name     string
		snapshot Snapshot
		want     string
	}{
		{name: "zero value", snapshot: Snapshot{}, want: "NewSnapshot"},
		{name: "bad TTL", snapshot: Snapshot{valid: true}, want: "TTL"},
		{name: "index length", snapshot: Snapshot{valid: true, ttl: valid.ttl, records: map[string]netip.Addr{"orders.test": netip.MustParseAddr("127.0.0.1")}}, want: "indexes are inconsistent"},
		{name: "invalid ordered record", snapshot: Snapshot{valid: true, ttl: valid.ttl, records: map[string]netip.Addr{"Orders.test": netip.MustParseAddr("127.0.0.1")}, ordered: []Record{{Name: "Orders.test", Address: netip.MustParseAddr("127.0.0.1")}}}, want: "lowercase"},
		{name: "missing index", snapshot: Snapshot{valid: true, ttl: valid.ttl, records: map[string]netip.Addr{"other.test": netip.MustParseAddr("127.0.0.1")}, ordered: valid.ordered}, want: "record index"},
		{name: "different index address", snapshot: Snapshot{valid: true, ttl: valid.ttl, records: map[string]netip.Addr{"orders.test": netip.MustParseAddr("127.0.0.2")}, ordered: valid.ordered}, want: "record index"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.snapshot.validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
