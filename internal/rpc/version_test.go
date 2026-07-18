package rpc

import (
	"errors"
	"reflect"
	"testing"
)

// TestCanonicalVersionRanges verifies migration-window ranges remain per-major
// and serialize in a deterministic order.
func TestCanonicalVersionRanges(t *testing.T) {
	ranges, err := CanonicalVersionRanges([]VersionRange{
		{Min: Version{Major: 2, Minor: 0}, Max: Version{Major: 2, Minor: 1}},
		{Min: Version{Major: 1, Minor: 2}, Max: Version{Major: 1, Minor: 4}},
		{Min: Version{Major: 1, Minor: 0}, Max: Version{Major: 1, Minor: 2}},
		{Min: Version{Major: 1, Minor: 4}, Max: Version{Major: 1, Minor: 4}},
	})
	if err != nil {
		t.Fatalf("canonicalize ranges: %v", err)
	}
	want := []VersionRange{
		{Min: Version{Major: 1, Minor: 0}, Max: Version{Major: 1, Minor: 4}},
		{Min: Version{Major: 2, Minor: 0}, Max: Version{Major: 2, Minor: 1}},
	}
	if !reflect.DeepEqual(ranges, want) {
		t.Fatalf("ranges = %#v, want %#v", ranges, want)
	}

	maximum, err := CanonicalVersionRanges([]VersionRange{{
		Min: Version{Major: 1, Minor: ^uint16(0)},
		Max: Version{Major: 1, Minor: ^uint16(0)},
	}})
	if err != nil || len(maximum) != 1 {
		t.Fatalf("canonicalize maximum minor: %#v, %v", maximum, err)
	}
}

// TestCanonicalVersionRangesRejectsInvalidRanges verifies a range cannot imply
// compatibility across a protocol-major semantic break.
func TestCanonicalVersionRangesRejectsInvalidRanges(t *testing.T) {
	for _, ranges := range [][]VersionRange{
		nil,
		{{Min: Version{}, Max: Version{Major: 1}}},
		{{Min: Version{Major: 1}, Max: Version{Major: 2}}},
		{{Min: Version{Major: 1, Minor: 2}, Max: Version{Major: 1, Minor: 1}}},
	} {
		if _, err := CanonicalVersionRanges(ranges); err == nil {
			t.Fatalf("CanonicalVersionRanges(%#v) succeeded", ranges)
		}
	}
}

// TestNegotiateVersionChoosesHighestSharedVersion verifies negotiation remains
// deterministic when peers advertise multiple major-version migration ranges.
func TestNegotiateVersionChoosesHighestSharedVersion(t *testing.T) {
	selected, err := NegotiateVersion(
		[]VersionRange{
			{Min: Version{Major: 1, Minor: 1}, Max: Version{Major: 1, Minor: 5}},
			{Min: Version{Major: 2, Minor: 0}, Max: Version{Major: 2, Minor: 1}},
		},
		[]VersionRange{
			{Min: Version{Major: 1, Minor: 3}, Max: Version{Major: 1, Minor: 7}},
			{Min: Version{Major: 2, Minor: 0}, Max: Version{Major: 2, Minor: 0}},
		},
	)
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if want := (Version{Major: 2, Minor: 0}); selected != want {
		t.Fatalf("selected = %v, want %v", selected, want)
	}
}

// TestNegotiateVersionRejectsDisjointMajors verifies callers can distinguish
// an upgrade-required mismatch from malformed range input.
func TestNegotiateVersionRejectsDisjointMajors(t *testing.T) {
	_, err := NegotiateVersion(
		[]VersionRange{{Min: Version{Major: 1}, Max: Version{Major: 1, Minor: 2}}},
		[]VersionRange{{Min: Version{Major: 2}, Max: Version{Major: 2, Minor: 2}}},
	)
	if !errors.Is(err, ErrNoCompatibleProtocol) {
		t.Fatalf("error = %v, want ErrNoCompatibleProtocol", err)
	}
}
