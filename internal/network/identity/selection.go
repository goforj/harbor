package identity

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

const (
	poolSelectionPrefixBits   = 29
	poolSelectionAddressCount = 1 << (32 - poolSelectionPrefixBits)

	// A /16 worth of /29 candidates permits broad coordinator policy while bounding normalization work.
	maximumPoolCandidateObservations = 8192
	// Selection evidence shares the durable network bound so later proof composition cannot inherit oversized host facts.
	maximumPoolSelectionEvidenceLength = 16 * 1024
)

// PoolAssignmentState describes whether one exact address is already assigned.
type PoolAssignmentState string

const (
	// PoolAssignmentAbsent means the caller proved that the exact address is not assigned.
	PoolAssignmentAbsent PoolAssignmentState = "absent"
	// PoolAssignmentPresent means the caller observed an existing assignment at the exact address.
	PoolAssignmentPresent PoolAssignmentState = "present"
	// PoolAssignmentIndeterminate means the caller could not prove assignment presence or absence.
	PoolAssignmentIndeterminate PoolAssignmentState = "indeterminate"
)

// PoolHostConflictState describes whether route, socket, and policy facts admit one address.
type PoolHostConflictState string

const (
	// PoolHostConflictSafe means the caller proved that no host conflict blocks the address.
	PoolHostConflictSafe PoolHostConflictState = "safe"
	// PoolHostConflictPresent means the caller observed a definite host conflict at the address.
	PoolHostConflictPresent PoolHostConflictState = "conflict"
	// PoolHostConflictIndeterminate means the caller could not prove the address free of host conflicts.
	PoolHostConflictIndeterminate PoolHostConflictState = "indeterminate"
)

// PoolAddressObservation contains the independently produced facts for one exact candidate address.
type PoolAddressObservation struct {
	Address              netip.Addr
	AssignmentState      PoolAssignmentState
	AssignmentEvidence   string
	HostConflictState    PoolHostConflictState
	HostConflictEvidence string
}

// PoolCandidateObservation contains one complete canonical /29 and all eight address observations.
type PoolCandidateObservation struct {
	Prefix    netip.Prefix
	Addresses []PoolAddressObservation
}

// PoolSelectionRequest supplies bounded candidate pools and a deterministic canonical starting offset.
type PoolSelectionRequest struct {
	Candidates  []PoolCandidateObservation
	StartOffset uint64
}

// PoolSelection contains the selected pool and its address evidence in canonical order.
type PoolSelection struct {
	Pool     Pool
	Evidence []PoolAddressObservation
}

// PoolSelectionExhaustionError explains why no complete candidate pool was safe to select.
// One candidate may contribute to more than one blocked count when its observations contain multiple failures.
type PoolSelectionExhaustionError struct {
	CandidatePools           int
	StartIndex               int
	AssignmentBlockedPools   int
	HostConflictBlockedPools int
	IndeterminatePools       int
}

// Error reports the canonical search start and each actionable class that blocked selection.
func (err *PoolSelectionExhaustionError) Error() string {
	return fmt.Sprintf(
		"loopback /29 pool selection exhausted after inspecting %d candidate pools from canonical index %d: %d assignment-blocked, %d host-conflict-blocked, %d indeterminate",
		err.CandidatePools,
		err.StartIndex,
		err.AssignmentBlockedPools,
		err.HostConflictBlockedPools,
		err.IndeterminatePools,
	)
}

// SelectPool chooses the first fully absent and conflict-safe /29 using deterministic canonical wraparound.
func SelectPool(request PoolSelectionRequest) (PoolSelection, error) {
	candidates, err := normalizePoolSelectionCandidates(request.Candidates)
	if err != nil {
		return PoolSelection{}, err
	}

	start := int(request.StartOffset % uint64(len(candidates)))
	exhaustion := &PoolSelectionExhaustionError{
		CandidatePools: len(candidates),
		StartIndex:     start,
	}
	for offset := 0; offset < len(candidates); offset++ {
		candidate := candidates[(start+offset)%len(candidates)]
		eligible, assignmentBlocked, conflictBlocked, indeterminate := poolCandidateEligibility(candidate)
		if assignmentBlocked {
			exhaustion.AssignmentBlockedPools++
		}
		if conflictBlocked {
			exhaustion.HostConflictBlockedPools++
		}
		if indeterminate {
			exhaustion.IndeterminatePools++
		}
		if !eligible {
			continue
		}

		addresses := make([]netip.Addr, len(candidate.Addresses))
		for index, observation := range candidate.Addresses {
			addresses[index] = observation.Address
		}
		pool, err := NewPool(candidate.Prefix, addresses)
		if err != nil {
			return PoolSelection{}, fmt.Errorf("select loopback /29 pool: construct selected pool: %w", err)
		}
		return PoolSelection{
			Pool:     pool,
			Evidence: slices.Clone(candidate.Addresses),
		}, nil
	}

	return PoolSelection{}, exhaustion
}

// normalizePoolSelectionCandidates validates complete facts before ordering them independently of caller enumeration.
func normalizePoolSelectionCandidates(candidates []PoolCandidateObservation) ([]PoolCandidateObservation, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("select loopback /29 pool: at least one candidate pool is required")
	}
	if len(candidates) > maximumPoolCandidateObservations {
		return nil, fmt.Errorf(
			"select loopback /29 pool: candidate pool count %d exceeds %d",
			len(candidates),
			maximumPoolCandidateObservations,
		)
	}

	normalized := make([]PoolCandidateObservation, len(candidates))
	seenPrefixes := make(map[netip.Prefix]struct{}, len(candidates))
	for index, candidate := range candidates {
		validated, err := normalizePoolCandidate(candidate)
		if err != nil {
			return nil, fmt.Errorf("select loopback /29 pool: candidate %d: %w", index, err)
		}
		if _, duplicate := seenPrefixes[validated.Prefix]; duplicate {
			return nil, fmt.Errorf("select loopback /29 pool: duplicate candidate prefix %s", validated.Prefix)
		}
		seenPrefixes[validated.Prefix] = struct{}{}
		normalized[index] = validated
	}
	slices.SortFunc(normalized, comparePoolCandidateObservations)
	return normalized, nil
}

// normalizePoolCandidate requires one canonical /29 and exactly one observation for each of its eight addresses.
func normalizePoolCandidate(candidate PoolCandidateObservation) (PoolCandidateObservation, error) {
	if err := validateSelectionPrefix(candidate.Prefix); err != nil {
		return PoolCandidateObservation{}, err
	}
	if len(candidate.Addresses) != poolSelectionAddressCount {
		return PoolCandidateObservation{}, fmt.Errorf(
			"candidate %s contains %d address observations, want %d",
			candidate.Prefix,
			len(candidate.Addresses),
			poolSelectionAddressCount,
		)
	}

	addresses := slices.Clone(candidate.Addresses)
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for index, observation := range addresses {
		if err := validatePoolAddressObservation(candidate.Prefix, observation); err != nil {
			return PoolCandidateObservation{}, fmt.Errorf("address observation %d: %w", index, err)
		}
		if _, duplicate := seen[observation.Address]; duplicate {
			return PoolCandidateObservation{}, fmt.Errorf("candidate %s repeats address %s", candidate.Prefix, observation.Address)
		}
		seen[observation.Address] = struct{}{}
	}
	slices.SortFunc(addresses, comparePoolAddressObservations)

	expected := candidate.Prefix.Addr()
	for index, observation := range addresses {
		if observation.Address != expected {
			return PoolCandidateObservation{}, fmt.Errorf(
				"candidate %s address %d is %s, want %s",
				candidate.Prefix,
				index,
				observation.Address,
				expected,
			)
		}
		expected = expected.Next()
	}
	return PoolCandidateObservation{Prefix: candidate.Prefix, Addresses: addresses}, nil
}

// validateSelectionPrefix prevents a caller from presenting a partial, overlapping, or non-loopback pool boundary.
func validateSelectionPrefix(prefix netip.Prefix) error {
	if !prefix.IsValid() {
		return fmt.Errorf("candidate prefix is invalid")
	}
	if !prefix.Addr().Is4() || prefix.Addr() != prefix.Addr().Unmap() {
		return fmt.Errorf("candidate prefix %s is not canonical IPv4", prefix)
	}
	if !prefix.Addr().IsLoopback() || prefix.Bits() != poolSelectionPrefixBits {
		return fmt.Errorf("candidate prefix %s is not an IPv4-loopback /29", prefix)
	}
	if prefix != prefix.Masked() {
		return fmt.Errorf("candidate prefix %s is not canonical", prefix)
	}
	return nil
}

// validatePoolAddressObservation rejects facts that cannot prove one exact address's complete admission state.
func validatePoolAddressObservation(prefix netip.Prefix, observation PoolAddressObservation) error {
	address := observation.Address
	if !address.IsValid() || !address.Is4() || address != address.Unmap() || !address.IsLoopback() {
		return fmt.Errorf("address %s is not canonical IPv4 loopback", address)
	}
	if !prefix.Contains(address) {
		return fmt.Errorf("address %s is outside candidate %s", address, prefix)
	}
	switch observation.AssignmentState {
	case PoolAssignmentAbsent, PoolAssignmentPresent, PoolAssignmentIndeterminate:
	default:
		return fmt.Errorf("address %s assignment state %q is unsupported", address, observation.AssignmentState)
	}
	if err := validatePoolSelectionEvidence("assignment evidence", observation.AssignmentEvidence); err != nil {
		return fmt.Errorf("address %s: %w", address, err)
	}
	switch observation.HostConflictState {
	case PoolHostConflictSafe, PoolHostConflictPresent, PoolHostConflictIndeterminate:
	default:
		return fmt.Errorf("address %s host conflict state %q is unsupported", address, observation.HostConflictState)
	}
	if err := validatePoolSelectionEvidence("host conflict evidence", observation.HostConflictEvidence); err != nil {
		return fmt.Errorf("address %s: %w", address, err)
	}
	return nil
}

// validatePoolSelectionEvidence keeps opaque host facts nonempty and within the durable evidence byte bound.
func validatePoolSelectionEvidence(name string, evidence string) error {
	if strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(evidence) > maximumPoolSelectionEvidenceLength {
		return fmt.Errorf("%s exceeds %d bytes", name, maximumPoolSelectionEvidenceLength)
	}
	return nil
}

// poolCandidateEligibility reports every rejection class so exhaustion remains useful without changing selection order.
func poolCandidateEligibility(candidate PoolCandidateObservation) (eligible bool, assignmentBlocked bool, conflictBlocked bool, indeterminate bool) {
	eligible = true
	for _, observation := range candidate.Addresses {
		switch observation.AssignmentState {
		case PoolAssignmentAbsent:
		case PoolAssignmentPresent:
			assignmentBlocked = true
			eligible = false
		case PoolAssignmentIndeterminate:
			indeterminate = true
			eligible = false
		}
		switch observation.HostConflictState {
		case PoolHostConflictSafe:
		case PoolHostConflictPresent:
			conflictBlocked = true
			eligible = false
		case PoolHostConflictIndeterminate:
			indeterminate = true
			eligible = false
		}
	}
	return eligible, assignmentBlocked, conflictBlocked, indeterminate
}

// comparePoolCandidateObservations orders disjoint /29 values by their canonical first address.
func comparePoolCandidateObservations(left PoolCandidateObservation, right PoolCandidateObservation) int {
	return left.Prefix.Addr().Compare(right.Prefix.Addr())
}

// comparePoolAddressObservations orders evidence by exact canonical address.
func comparePoolAddressObservations(left PoolAddressObservation, right PoolAddressObservation) int {
	return left.Address.Compare(right.Address)
}
