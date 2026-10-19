// Package poolpolicy defines the bounded loopback capacity shared by setup, helper approval, and cleanup.
package poolpolicy

import (
	"fmt"
	"net/netip"
)

const (
	// PrefixBits reserves enough identities for 100 projects and secondary same-port endpoints.
	PrefixBits = 24
	// Capacity bounds both host mutations and signed aggregate evidence.
	Capacity = 1 << (32 - PrefixBits)
	// LegacyPrefixBits preserves existing eight-address installations until explicitly expanded.
	LegacyPrefixBits = 29
)

// Size rejects partial or unbounded aggregate approval ranges while retaining legacy installations.
func Size(prefix netip.Prefix) (int, error) {
	if !prefix.IsValid() || !prefix.Addr().Is4() || !prefix.Addr().IsLoopback() || prefix != prefix.Masked() || (prefix.Bits() != PrefixBits && prefix.Bits() != LegacyPrefixBits) {
		return 0, fmt.Errorf("pool must be a canonical IPv4 loopback /24 or legacy /29")
	}
	return 1 << (32 - prefix.Bits()), nil
}

// Addresses enumerates the entire approved range once in canonical order.
func Addresses(prefix netip.Prefix) ([]netip.Addr, error) {
	size, err := Size(prefix)
	if err != nil {
		return nil, err
	}
	addresses := make([]netip.Addr, size)
	address := prefix.Addr()
	for index := range addresses {
		addresses[index] = address
		address = address.Next()
	}
	return addresses, nil
}

// Expansion validates the only supported ownership growth without permitting relocation or shrinkage.
func Expansion(previous, target netip.Prefix) error {
	if _, err := Size(previous); err != nil {
		return err
	}
	if _, err := Size(target); err != nil {
		return err
	}
	if previous.Bits() != LegacyPrefixBits || target.Bits() != PrefixBits || !target.Contains(previous.Addr()) {
		return fmt.Errorf("pool expansion must retain the current /29 inside its containing /24")
	}
	return nil
}
