package identity

import (
	"fmt"
	"net/netip"
	"testing"
)

// TestDefaultPoolPrefixesCoversProductionNamespace verifies exact ordered coverage without overlap or gaps.
func TestDefaultPoolPrefixesCoversProductionNamespace(t *testing.T) {
	prefixes := DefaultPoolPrefixes()
	if len(prefixes) != 256 {
		t.Fatalf("DefaultPoolPrefixes() count = %d, want 256", len(prefixes))
	}
	if prefixes[0] != netip.MustParsePrefix("127.77.0.0/24") {
		t.Fatalf("first prefix = %s, want 127.77.0.0/24", prefixes[0])
	}
	if prefixes[len(prefixes)-1] != netip.MustParsePrefix("127.77.255.0/24") {
		t.Fatalf("last prefix = %s, want 127.77.255.0/24", prefixes[len(prefixes)-1])
	}

	namespace := netip.MustParsePrefix(defaultPoolNamespaceText)
	expectedAddress := namespace.Addr()
	for index, prefix := range prefixes {
		if prefix.Bits() != defaultPoolPrefixBits || prefix != prefix.Masked() || prefix.Addr() != expectedAddress {
			t.Fatalf("prefix %d = %s, want canonical /24 at %s", index, prefix, expectedAddress)
		}
		if !namespace.Contains(prefix.Addr()) {
			t.Fatalf("prefix %d address %s is outside %s", index, prefix.Addr(), namespace)
		}
		for range 1 << (32 - defaultPoolPrefixBits) {
			expectedAddress = expectedAddress.Next()
		}
	}
	if expectedAddress != netip.MustParseAddr("127.78.0.0") {
		t.Fatalf("coverage ended at %s, want 127.78.0.0", expectedAddress)
	}
}

// TestDefaultPoolPrefixesReturnsDefensiveSlice proves callers cannot alter later candidate enumeration.
func TestDefaultPoolPrefixesReturnsDefensiveSlice(t *testing.T) {
	first := DefaultPoolPrefixes()
	first[0] = netip.MustParsePrefix("127.99.0.0/29")
	second := DefaultPoolPrefixes()
	if second[0] != netip.MustParsePrefix("127.77.0.0/24") {
		t.Fatalf("second first prefix = %s, want defensive 127.77.0.0/24", second[0])
	}
}

// TestDefaultPoolStartOffsetValidatesAndHashesInstallationID covers rejection and one stable cross-architecture vector.
func TestDefaultPoolStartOffsetValidatesAndHashesInstallationID(t *testing.T) {
	for _, installationID := range []InstallationID{"", " bad ", ".harbor", "harbor/other"} {
		if offset, err := DefaultPoolStartOffset(installationID); err == nil || offset != 0 {
			t.Fatalf("DefaultPoolStartOffset(%q) = %d, %v, want zero and a validation error", installationID, offset, err)
		}
	}

	offset, err := DefaultPoolStartOffset("harbor-test-installation")
	if err != nil {
		t.Fatalf("DefaultPoolStartOffset() error = %v", err)
	}
	const want uint64 = 0xe1405d74ca258ab2
	if offset != want {
		t.Fatalf("DefaultPoolStartOffset() = %#x, want %#x", offset, want)
	}
}

// TestDefaultPoolStartOffsetDistributesDeterministicSamples guards against accidentally collapsing the digest to one candidate.
func TestDefaultPoolStartOffsetDistributesDeterministicSamples(t *testing.T) {
	seen := make(map[uint64]struct{})
	for index := range 4096 {
		installationID := InstallationID(fmt.Sprintf("harbor-installation-%03d", index))
		offset, err := DefaultPoolStartOffset(installationID)
		if err != nil {
			t.Fatalf("DefaultPoolStartOffset(%q) error = %v", installationID, err)
		}
		seen[offset%defaultPoolPrefixCount] = struct{}{}
	}
	if len(seen) < 250 {
		t.Fatalf("4096 deterministic samples reached %d candidates, want at least 250", len(seen))
	}
}
