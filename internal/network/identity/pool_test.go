package identity

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// TestNewPoolCanonicalizesCandidates verifies stable ordering and defensive copies.
func TestNewPoolCanonicalizesCandidates(t *testing.T) {
	candidates := []netip.Addr{
		mustAddress(t, "127.77.0.13"),
		mustAddress(t, "127.77.0.10"),
		mustAddress(t, "127.77.0.12"),
	}
	original := append([]netip.Addr(nil), candidates...)

	pool, err := NewPool(mustPrefix(t, "127.77.0.99/24"), candidates)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}

	if got, want := pool.Prefix().String(), "127.77.0.0/24"; got != want {
		t.Fatalf("Prefix() = %q, want %q", got, want)
	}
	if got, want := pool.Candidates(), []netip.Addr{
		mustAddress(t, "127.77.0.10"),
		mustAddress(t, "127.77.0.12"),
		mustAddress(t, "127.77.0.13"),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Candidates() = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(candidates, original) {
		t.Fatalf("NewPool() mutated candidates: got %v, want %v", candidates, original)
	}

	copyOfCandidates := pool.Candidates()
	copyOfCandidates[0] = mustAddress(t, "127.77.0.99")
	if got := pool.Candidates()[0].String(); got != "127.77.0.10" {
		t.Fatalf("Candidates() exposed internal slice, first = %s", got)
	}
	if !pool.Contains(mustAddress(t, "::ffff:127.77.0.12")) {
		t.Fatal("Contains() did not normalize an IPv4-mapped candidate")
	}
	if pool.Contains(mustAddress(t, "127.77.0.11")) {
		t.Fatal("Contains() reported an address absent from the candidate set")
	}
	if got, want := pool.Capacity(), 3; got != want {
		t.Fatalf("Capacity() = %d, want %d", got, want)
	}
	if err := pool.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestNewPoolRejectsInvalidRangesAndCandidates verifies that allocation cannot escape its bounded loopback pool.
func TestNewPoolRejectsInvalidRangesAndCandidates(t *testing.T) {
	tests := []struct {
		name       string
		prefix     netip.Prefix
		candidates []netip.Addr
		contains   string
	}{
		{name: "invalid prefix", prefix: netip.Prefix{}, candidates: []netip.Addr{mustAddress(t, "127.77.0.10")}, contains: "prefix is invalid"},
		{name: "IPv6 prefix", prefix: mustPrefix(t, "::1/128"), candidates: []netip.Addr{mustAddress(t, "127.77.0.10")}, contains: "is not IPv4"},
		{name: "non-loopback prefix", prefix: mustPrefix(t, "10.0.0.0/24"), candidates: []netip.Addr{mustAddress(t, "10.0.0.10")}, contains: "not contained by IPv4 loopback"},
		{name: "prefix wider than loopback", prefix: mustPrefix(t, "127.0.0.0/7"), candidates: []netip.Addr{mustAddress(t, "127.77.0.10")}, contains: "not contained by IPv4 loopback"},
		{name: "empty candidates", prefix: mustPrefix(t, "127.77.0.0/24"), contains: "at least one candidate"},
		{name: "invalid candidate", prefix: mustPrefix(t, "127.77.0.0/24"), candidates: []netip.Addr{{}}, contains: "candidate address is invalid"},
		{name: "non-loopback candidate", prefix: mustPrefix(t, "127.77.0.0/24"), candidates: []netip.Addr{mustAddress(t, "10.0.0.10")}, contains: "not IPv4 loopback"},
		{name: "candidate outside prefix", prefix: mustPrefix(t, "127.77.0.0/24"), candidates: []netip.Addr{mustAddress(t, "127.78.0.10")}, contains: "outside"},
		{name: "duplicate candidate", prefix: mustPrefix(t, "127.77.0.0/24"), candidates: []netip.Addr{mustAddress(t, "127.77.0.10"), mustAddress(t, "::ffff:127.77.0.10")}, contains: "duplicate candidate"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPool(test.prefix, test.candidates)
			if err == nil {
				t.Fatal("NewPool() error = nil")
			}
			if !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("NewPool() error = %q, want substring %q", err, test.contains)
			}
		})
	}
}

// TestPoolValidateRejectsForgedValues verifies that callers cannot bypass NewPool with a zero or malformed value.
func TestPoolValidateRejectsForgedValues(t *testing.T) {
	if err := (Pool{}).Validate(); err == nil {
		t.Fatal("zero Pool.Validate() error = nil")
	}

	pool := Pool{
		prefix: mustPrefix(t, "127.77.0.0/24"),
		candidates: []netip.Addr{
			mustAddress(t, "127.77.0.12"),
			mustAddress(t, "127.77.0.10"),
		},
	}
	if err := pool.Validate(); err == nil || !strings.Contains(err.Error(), "unique and sorted") {
		t.Fatalf("forged Pool.Validate() error = %v, want sorted error", err)
	}

	forged := []struct {
		name     string
		pool     Pool
		contains string
	}{
		{
			name:     "noncanonical prefix",
			pool:     Pool{prefix: mustPrefix(t, "127.77.0.99/24"), candidates: []netip.Addr{mustAddress(t, "127.77.0.10")}},
			contains: "prefix must be canonical",
		},
		{
			name:     "empty candidates",
			pool:     Pool{prefix: mustPrefix(t, "127.77.0.0/24")},
			contains: "at least one candidate",
		},
		{
			name:     "outside candidate",
			pool:     Pool{prefix: mustPrefix(t, "127.77.0.0/24"), candidates: []netip.Addr{mustAddress(t, "127.78.0.10")}},
			contains: "outside",
		},
	}
	for _, test := range forged {
		t.Run(test.name, func(t *testing.T) {
			err := test.pool.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.contains)
			}
		})
	}
}
