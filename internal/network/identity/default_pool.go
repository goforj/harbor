package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
)

const (
	defaultPoolNamespaceText = "127.77.0.0/16"
	defaultPoolPrefixBits    = 29
	defaultPoolPrefixCount   = 1 << (defaultPoolPrefixBits - 16)
)

// DefaultPoolPrefixes returns every disjoint /29 in Harbor's production candidate namespace in canonical address order.
func DefaultPoolPrefixes() []netip.Prefix {
	prefixes := make([]netip.Prefix, defaultPoolPrefixCount)
	address := netip.MustParsePrefix(defaultPoolNamespaceText).Addr()
	for index := range prefixes {
		prefixes[index] = netip.PrefixFrom(address, defaultPoolPrefixBits)
		for range 1 << (32 - defaultPoolPrefixBits) {
			address = address.Next()
		}
	}
	return prefixes
}

// DefaultPoolStartOffset validates the installation ID and interprets the first eight SHA-256 digest bytes as a big-endian uint64.
func DefaultPoolStartOffset(installationID InstallationID) (uint64, error) {
	if err := installationID.Validate(); err != nil {
		return 0, err
	}
	digest := sha256.Sum256([]byte(installationID))
	return binary.BigEndian.Uint64(digest[:8]), nil
}
