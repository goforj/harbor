package identity

import (
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestSelectPoolUsesCanonicalWraparound verifies selection is independent of caller ordering and wraps deterministically.
func TestSelectPoolUsesCanonicalWraparound(t *testing.T) {
	first := poolSelectionTestCandidate(t, "127.77.0.0/29", PoolAssignmentAbsent, PoolHostConflictSafe)
	second := poolSelectionTestCandidate(t, "127.77.0.8/29", PoolAssignmentPresent, PoolHostConflictSafe)
	third := poolSelectionTestCandidate(t, "127.77.0.16/29", PoolAssignmentAbsent, PoolHostConflictPresent)
	slices.Reverse(first.Addresses)
	input := []PoolCandidateObservation{third, first, second}
	originalFirstAddresses := slices.Clone(first.Addresses)

	selected, err := SelectPool(PoolSelectionRequest{Candidates: input, StartOffset: 1})
	if err != nil {
		t.Fatalf("SelectPool() error = %v", err)
	}
	if got, want := selected.Pool.Prefix(), mustPrefix(t, "127.77.0.0/29"); got != want {
		t.Fatalf("selected prefix = %s, want %s", got, want)
	}
	if got, want := selected.Pool.Capacity(), poolSelectionAddressCount; got != want {
		t.Fatalf("selected capacity = %d, want %d", got, want)
	}
	wantAddresses := poolSelectionTestAddresses(mustPrefix(t, "127.77.0.0/29"))
	if got := selected.Pool.Candidates(); !reflect.DeepEqual(got, wantAddresses) {
		t.Fatalf("selected addresses = %v, want %v", got, wantAddresses)
	}
	if len(selected.Evidence) != poolSelectionAddressCount {
		t.Fatalf("selected evidence count = %d, want %d", len(selected.Evidence), poolSelectionAddressCount)
	}
	for index, evidence := range selected.Evidence {
		if evidence.Address != wantAddresses[index] || evidence.AssignmentState != PoolAssignmentAbsent || evidence.HostConflictState != PoolHostConflictSafe {
			t.Fatalf("selected evidence %d = %#v", index, evidence)
		}
	}
	if !reflect.DeepEqual(first.Addresses, originalFirstAddresses) {
		t.Fatalf("SelectPool() mutated caller addresses: got %#v, want %#v", first.Addresses, originalFirstAddresses)
	}

	seeded, err := SelectPool(PoolSelectionRequest{
		Candidates: []PoolCandidateObservation{
			poolSelectionTestCandidate(t, "127.77.0.0/29", PoolAssignmentAbsent, PoolHostConflictSafe),
			poolSelectionTestCandidate(t, "127.77.0.8/29", PoolAssignmentAbsent, PoolHostConflictSafe),
			poolSelectionTestCandidate(t, "127.77.0.16/29", PoolAssignmentAbsent, PoolHostConflictSafe),
		},
		StartOffset: 5,
	})
	if err != nil {
		t.Fatalf("SelectPool(seed) error = %v", err)
	}
	if got, want := seeded.Pool.Prefix(), mustPrefix(t, "127.77.0.16/29"); got != want {
		t.Fatalf("seeded prefix = %s, want %s", got, want)
	}
}

// TestSelectPoolReportsActionableExhaustion verifies definite and indeterminate blockers remain distinguishable.
func TestSelectPoolReportsActionableExhaustion(t *testing.T) {
	assigned := poolSelectionTestCandidate(t, "127.77.0.0/29", PoolAssignmentAbsent, PoolHostConflictSafe)
	assigned.Addresses[3].AssignmentState = PoolAssignmentPresent
	conflicted := poolSelectionTestCandidate(t, "127.77.0.8/29", PoolAssignmentAbsent, PoolHostConflictSafe)
	conflicted.Addresses[2].HostConflictState = PoolHostConflictPresent
	indeterminate := poolSelectionTestCandidate(t, "127.77.0.16/29", PoolAssignmentAbsent, PoolHostConflictSafe)
	indeterminate.Addresses[1].AssignmentState = PoolAssignmentIndeterminate
	indeterminate.Addresses[4].HostConflictState = PoolHostConflictIndeterminate

	_, err := SelectPool(PoolSelectionRequest{
		Candidates:  []PoolCandidateObservation{indeterminate, assigned, conflicted},
		StartOffset: 4,
	})
	var exhaustion *PoolSelectionExhaustionError
	if !errors.As(err, &exhaustion) {
		t.Fatalf("SelectPool() error = %v, want PoolSelectionExhaustionError", err)
	}
	want := PoolSelectionExhaustionError{
		CandidatePools:           3,
		StartIndex:               1,
		AssignmentBlockedPools:   1,
		HostConflictBlockedPools: 1,
		IndeterminatePools:       1,
	}
	if got := *exhaustion; got != want {
		t.Fatalf("exhaustion = %#v, want %#v", got, want)
	}
	for _, fragment := range []string{"3 candidate pools", "canonical index 1", "1 assignment-blocked", "1 host-conflict-blocked", "1 indeterminate"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("exhaustion error = %q, want %q", err, fragment)
		}
	}
}

// TestSelectPoolRejectsMalformedOrIncompleteFacts verifies no partial candidate can influence selection.
func TestSelectPoolRejectsMalformedOrIncompleteFacts(t *testing.T) {
	valid := poolSelectionTestCandidate(t, "127.77.0.0/29", PoolAssignmentAbsent, PoolHostConflictSafe)
	tests := []struct {
		name     string
		request  func() PoolSelectionRequest
		contains string
	}{
		{name: "empty candidates", request: func() PoolSelectionRequest { return PoolSelectionRequest{} }, contains: "at least one candidate"},
		{name: "too many candidates", request: func() PoolSelectionRequest {
			return PoolSelectionRequest{Candidates: make([]PoolCandidateObservation, maximumPoolCandidateObservations+1)}
		}, contains: "exceeds"},
		{name: "invalid prefix", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Prefix = netip.Prefix{}
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "prefix is invalid"},
		{name: "IPv6 prefix", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Prefix = mustPrefix(t, "::1/128")
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "not canonical IPv4"},
		{name: "non-loopback prefix", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Prefix = mustPrefix(t, "10.0.0.0/29")
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "not an IPv4-loopback /29"},
		{name: "wrong prefix size", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Prefix = mustPrefix(t, "127.77.0.0/28")
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "not an IPv4-loopback /29"},
		{name: "noncanonical prefix", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Prefix = mustPrefix(t, "127.77.0.3/29")
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "not canonical"},
		{name: "duplicate prefix", request: func() PoolSelectionRequest {
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{valid, valid}}
		}, contains: "duplicate candidate prefix"},
		{name: "incomplete addresses", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = candidate.Addresses[:7]
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "want 8"},
		{name: "duplicate address", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[7] = candidate.Addresses[6]
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "repeats address"},
		{name: "outside address", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[7].Address = mustAddress(t, "127.77.0.8")
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "outside candidate"},
		{name: "mapped address", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[0].Address = mustAddress(t, "::ffff:127.77.0.0")
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "not canonical IPv4 loopback"},
		{name: "unknown assignment", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[0].AssignmentState = ""
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "assignment state"},
		{name: "missing assignment evidence", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[0].AssignmentEvidence = " \t"
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "assignment evidence is required"},
		{name: "oversized assignment evidence", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[0].AssignmentEvidence = strings.Repeat("a", maximumPoolSelectionEvidenceLength+1)
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "assignment evidence exceeds"},
		{name: "unknown host conflict", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[0].HostConflictState = ""
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "host conflict state"},
		{name: "missing host conflict evidence", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[0].HostConflictEvidence = ""
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "host conflict evidence is required"},
		{name: "oversized host conflict evidence", request: func() PoolSelectionRequest {
			candidate := valid
			candidate.Addresses = slices.Clone(candidate.Addresses)
			candidate.Addresses[0].HostConflictEvidence = strings.Repeat("h", maximumPoolSelectionEvidenceLength+1)
			return PoolSelectionRequest{Candidates: []PoolCandidateObservation{candidate}}
		}, contains: "host conflict evidence exceeds"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := SelectPool(test.request())
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("SelectPool() error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

// poolSelectionTestCandidate creates one complete candidate with uniform semantic observations.
func poolSelectionTestCandidate(
	t *testing.T,
	prefixText string,
	assignment PoolAssignmentState,
	conflict PoolHostConflictState,
) PoolCandidateObservation {
	t.Helper()
	prefix := mustPrefix(t, prefixText)
	addresses := poolSelectionTestAddresses(prefix)
	observations := make([]PoolAddressObservation, len(addresses))
	for index, address := range addresses {
		observations[index] = PoolAddressObservation{
			Address:              address,
			AssignmentState:      assignment,
			AssignmentEvidence:   "assignment=" + address.String(),
			HostConflictState:    conflict,
			HostConflictEvidence: "host-conflict=" + address.String(),
		}
	}
	return PoolCandidateObservation{Prefix: prefix, Addresses: observations}
}

// poolSelectionTestAddresses enumerates every address in one test prefix without applying production selection logic.
func poolSelectionTestAddresses(prefix netip.Prefix) []netip.Addr {
	addresses := make([]netip.Addr, poolSelectionAddressCount)
	address := prefix.Addr()
	for index := range addresses {
		addresses[index] = address
		address = address.Next()
	}
	return addresses
}
