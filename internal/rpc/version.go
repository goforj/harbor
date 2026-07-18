package rpc

import (
	"errors"
	"fmt"
	"sort"
)

var (
	// ErrNoCompatibleProtocol reports that two peers do not share a protocol version.
	ErrNoCompatibleProtocol = errors.New("no compatible protocol version")
)

// Version identifies one compatible revision of the Harbor IPC protocol.
type Version struct {
	Major uint16 `json:"major"`
	Minor uint16 `json:"minor"`
}

// String returns the dotted protocol version.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

// Validate rejects the zero protocol version, which is reserved for an
// unnegotiated connection.
func (v Version) Validate() error {
	if v.Major == 0 {
		return errors.New("protocol major must be greater than zero")
	}

	return nil
}

// Compare orders a version before, at, or after another version.
func (v Version) Compare(other Version) int {
	if v.Major < other.Major {
		return -1
	}
	if v.Major > other.Major {
		return 1
	}
	if v.Minor < other.Minor {
		return -1
	}
	if v.Minor > other.Minor {
		return 1
	}

	return 0
}

// VersionRange declares a contiguous set of minor versions within one major.
// Multiple ranges let a peer support migration windows across protocol majors
// without claiming support for every version between them.
type VersionRange struct {
	Min Version `json:"min"`
	Max Version `json:"max"`
}

// Validate verifies that a range is ordered and stays within one major.
func (r VersionRange) Validate() error {
	if err := r.Min.Validate(); err != nil {
		return fmt.Errorf("minimum version: %w", err)
	}
	if err := r.Max.Validate(); err != nil {
		return fmt.Errorf("maximum version: %w", err)
	}
	if r.Min.Major != r.Max.Major {
		return errors.New("a protocol range cannot span major versions")
	}
	if r.Min.Compare(r.Max) > 0 {
		return errors.New("minimum protocol version exceeds maximum")
	}

	return nil
}

// CanonicalVersionRanges validates, sorts, and merges overlapping ranges.
func CanonicalVersionRanges(ranges []VersionRange) ([]VersionRange, error) {
	if len(ranges) == 0 {
		return nil, errors.New("at least one protocol range is required")
	}

	canonical := append([]VersionRange(nil), ranges...)
	for i, candidate := range canonical {
		if err := candidate.Validate(); err != nil {
			return nil, fmt.Errorf("protocol range %d: %w", i, err)
		}
	}

	sort.Slice(canonical, func(i, j int) bool {
		comparison := canonical[i].Min.Compare(canonical[j].Min)
		if comparison != 0 {
			return comparison < 0
		}

		return canonical[i].Max.Compare(canonical[j].Max) < 0
	})

	merged := make([]VersionRange, 0, len(canonical))
	for _, candidate := range canonical {
		if len(merged) == 0 {
			merged = append(merged, candidate)
			continue
		}

		last := &merged[len(merged)-1]
		if last.Max.Major == candidate.Min.Major && uint32(candidate.Min.Minor) <= uint32(last.Max.Minor)+1 {
			if candidate.Max.Compare(last.Max) > 0 {
				last.Max = candidate.Max
			}
			continue
		}

		merged = append(merged, candidate)
	}

	return merged, nil
}

// NegotiateVersion chooses the highest version supported by both peers.
func NegotiateVersion(clientRanges []VersionRange, serverRanges []VersionRange) (Version, error) {
	client, err := CanonicalVersionRanges(clientRanges)
	if err != nil {
		return Version{}, fmt.Errorf("client protocol ranges: %w", err)
	}
	server, err := CanonicalVersionRanges(serverRanges)
	if err != nil {
		return Version{}, fmt.Errorf("server protocol ranges: %w", err)
	}

	var selected Version
	found := false
	for _, clientRange := range client {
		for _, serverRange := range server {
			candidate, compatible := intersectVersionRanges(clientRange, serverRange)
			if compatible && (!found || candidate.Compare(selected) > 0) {
				selected = candidate
				found = true
			}
		}
	}

	if !found {
		return Version{}, ErrNoCompatibleProtocol
	}

	return selected, nil
}

// intersectVersionRanges returns the highest version in a shared range.
func intersectVersionRanges(left VersionRange, right VersionRange) (Version, bool) {
	if left.Min.Major != right.Min.Major {
		return Version{}, false
	}

	lower := left.Min
	if right.Min.Compare(lower) > 0 {
		lower = right.Min
	}
	upper := left.Max
	if right.Max.Compare(upper) < 0 {
		upper = right.Max
	}
	if lower.Compare(upper) > 0 {
		return Version{}, false
	}

	return upper, true
}
