// Package identity plans stable loopback identities without mutating host state.
package identity

import (
	"fmt"
	"net/netip"
	"slices"
)

// Pool is a validated, deterministic set of IPv4 loopback candidates.
type Pool struct {
	prefix     netip.Prefix
	candidates []netip.Addr
}

// NewPool validates and canonicalizes a bounded set of loopback candidates.
func NewPool(prefix netip.Prefix, candidates []netip.Addr) (Pool, error) {
	if err := validatePrefix(prefix); err != nil {
		return Pool{}, err
	}
	if len(candidates) == 0 {
		return Pool{}, fmt.Errorf("identity pool: at least one candidate is required")
	}

	canonicalPrefix := prefix.Masked()
	canonicalCandidates := make([]netip.Addr, 0, len(candidates))
	seen := make(map[netip.Addr]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = candidate.Unmap()
		if err := validateCandidate(canonicalPrefix, candidate); err != nil {
			return Pool{}, err
		}
		if _, exists := seen[candidate]; exists {
			return Pool{}, fmt.Errorf("identity pool: duplicate candidate %s", candidate)
		}
		seen[candidate] = struct{}{}
		canonicalCandidates = append(canonicalCandidates, candidate)
	}

	slices.SortFunc(canonicalCandidates, netip.Addr.Compare)
	return Pool{prefix: canonicalPrefix, candidates: canonicalCandidates}, nil
}

// Prefix returns the canonical loopback prefix owned by the pool.
func (p Pool) Prefix() netip.Prefix {
	return p.prefix
}

// Candidates returns a copy of the pool candidates in deterministic address order.
func (p Pool) Candidates() []netip.Addr {
	return slices.Clone(p.candidates)
}

// Capacity returns the total number of candidate identities in the pool.
func (p Pool) Capacity() int {
	return len(p.candidates)
}

// Contains reports whether the pool contains the exact candidate address.
func (p Pool) Contains(address netip.Addr) bool {
	address = address.Unmap()
	_, found := slices.BinarySearchFunc(p.candidates, address, netip.Addr.Compare)
	return found
}

// Validate confirms that the pool still satisfies the invariants established by NewPool.
func (p Pool) Validate() error {
	if err := validatePrefix(p.prefix); err != nil {
		return err
	}
	if p.prefix != p.prefix.Masked() {
		return fmt.Errorf("identity pool: prefix must be canonical")
	}
	if len(p.candidates) == 0 {
		return fmt.Errorf("identity pool: at least one candidate is required")
	}
	for index, candidate := range p.candidates {
		if err := validateCandidate(p.prefix, candidate); err != nil {
			return err
		}
		if index > 0 && p.candidates[index-1].Compare(candidate) >= 0 {
			return fmt.Errorf("identity pool: candidates must be unique and sorted")
		}
	}
	return nil
}

// validatePrefix ensures the selected range cannot escape the IPv4 loopback block.
func validatePrefix(prefix netip.Prefix) error {
	if !prefix.IsValid() {
		return fmt.Errorf("identity pool: prefix is invalid")
	}
	if !prefix.Addr().Is4() {
		return fmt.Errorf("identity pool: prefix %s is not IPv4", prefix)
	}
	if prefix.Bits() < 8 || !prefix.Masked().Addr().IsLoopback() {
		return fmt.Errorf("identity pool: prefix %s is not contained by IPv4 loopback", prefix)
	}
	return nil
}

// validateCandidate keeps allocation inputs inside the explicitly selected pool.
func validateCandidate(prefix netip.Prefix, candidate netip.Addr) error {
	if !candidate.IsValid() {
		return fmt.Errorf("identity pool: candidate address is invalid")
	}
	if !candidate.Is4() || !candidate.IsLoopback() {
		return fmt.Errorf("identity pool: candidate %s is not IPv4 loopback", candidate)
	}
	if !prefix.Contains(candidate) {
		return fmt.Errorf("identity pool: candidate %s is outside %s", candidate, prefix)
	}
	return nil
}
