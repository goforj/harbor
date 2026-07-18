package platformproof

import (
	"fmt"
	"net/netip"
	"strings"
)

// ParseAddresses parses the two comma-separated loopback identities accepted by the proof CLI.
func ParseAddresses(value string) ([]netip.Addr, error) {
	parts := strings.Split(value, ",")
	addresses := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		address, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("parse proof address %q: %w", part, err)
		}
		addresses = append(addresses, address.Unmap())
	}
	if err := validateAddresses(addresses); err != nil {
		return nil, err
	}
	return addresses, nil
}
