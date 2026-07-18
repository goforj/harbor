package identity

import (
	"fmt"
	"net/netip"
	"slices"
)

// Input is the complete desired and observed state considered by one planning pass.
type Input struct {
	Pool         Pool
	Ownership    Ownership
	Requirements []LeaseKey
	Existing     []Lease
	Quarantines  []Quarantine
	Conflicts    []Conflict
}

// Capacity explains exactly how the selected pool was consumed during planning.
type Capacity struct {
	Total       int
	Desired     int
	Existing    int
	Retained    int
	Needed      int
	Quarantined int
	Conflicted  int
	Blocked     int
	Allocatable int
	Remaining   int
}

// Plan is a deterministic transition from current leases to desired leases.
type Plan struct {
	Leases    []Lease
	Retained  []Lease
	Allocated []Lease
	Released  []Lease
	Capacity  Capacity
}

// ExhaustionError reports the exact requirement and capacity that prevented allocation.
type ExhaustionError struct {
	Required  int
	Available int
	Missing   int
	Capacity  Capacity
}

// Error describes pool exhaustion without hiding the capacity needed by callers.
func (e *ExhaustionError) Error() string {
	return fmt.Sprintf(
		"loopback identity pool exhausted: %d addresses required, %d available, %d missing (%d total, %d blocked, %d existing, %d quarantined, %d conflicted)",
		e.Required,
		e.Available,
		e.Missing,
		e.Capacity.Total,
		e.Capacity.Blocked,
		e.Capacity.Existing,
		e.Capacity.Quarantined,
		e.Capacity.Conflicted,
	)
}

// OwnershipConflictError prevents a stale or foreign generation from being silently adopted.
type OwnershipConflictError struct {
	Key      LeaseKey
	Expected Ownership
	Actual   Ownership
}

// Error describes the exact logical identity and ownership mismatch.
func (e *OwnershipConflictError) Error() string {
	return fmt.Sprintf(
		"loopback identity %s has ownership %s/%d, expected %s/%d",
		formatKey(e.Key),
		e.Actual.InstallationID,
		e.Actual.Generation,
		e.Expected.InstallationID,
		e.Expected.Generation,
	)
}

// Planner computes leases without reading or mutating operating-system state.
type Planner struct{}

// NewPlanner creates a stateless identity planner.
func NewPlanner() Planner {
	return Planner{}
}

// Plan retains safe leases, releases only current-owned leases, and allocates the remaining keys.
func (Planner) Plan(input Input) (Plan, error) {
	normalized, err := normalizeInput(input)
	if err != nil {
		return Plan{}, err
	}

	desired := make(map[LeaseKey]struct{}, len(normalized.Requirements))
	for _, requirement := range normalized.Requirements {
		desired[requirement] = struct{}{}
	}

	existingAddresses := make(map[netip.Addr]struct{}, len(normalized.Existing))
	existingByKey := make(map[LeaseKey]Lease, len(normalized.Existing))
	for _, lease := range normalized.Existing {
		existingAddresses[lease.Address] = struct{}{}
		existingByKey[lease.Key] = lease
	}

	quarantined := make(map[netip.Addr]struct{}, len(normalized.Quarantines))
	for _, quarantine := range normalized.Quarantines {
		quarantined[quarantine.Address] = struct{}{}
	}
	conflicted := make(map[netip.Addr]struct{}, len(normalized.Conflicts))
	for _, conflict := range normalized.Conflicts {
		conflicted[conflict.Address] = struct{}{}
	}

	plan := Plan{}
	needed := make([]LeaseKey, 0, len(normalized.Requirements))
	for _, requirement := range normalized.Requirements {
		existing, found := existingByKey[requirement]
		if !found {
			needed = append(needed, requirement)
			continue
		}
		if !sameOwnership(existing.Ownership, normalized.Ownership) {
			return Plan{}, &OwnershipConflictError{
				Key: requirement, Expected: normalized.Ownership, Actual: existing.Ownership,
			}
		}
		_, isQuarantined := quarantined[existing.Address]
		_, hasConflict := conflicted[existing.Address]
		if isQuarantined || hasConflict {
			plan.Released = append(plan.Released, existing)
			needed = append(needed, requirement)
			continue
		}
		plan.Retained = append(plan.Retained, existing)
	}

	for _, existing := range normalized.Existing {
		if _, remainsDesired := desired[existing.Key]; remainsDesired {
			continue
		}
		if sameOwnership(existing.Ownership, normalized.Ownership) {
			plan.Released = append(plan.Released, existing)
		}
	}

	blocked := make(map[netip.Addr]struct{}, len(existingAddresses)+len(quarantined)+len(conflicted))
	for address := range existingAddresses {
		blocked[address] = struct{}{}
	}
	for address := range quarantined {
		blocked[address] = struct{}{}
	}
	for address := range conflicted {
		blocked[address] = struct{}{}
	}

	available := make([]netip.Addr, 0, normalized.Pool.Capacity()-len(blocked))
	for _, candidate := range normalized.Pool.candidates {
		if _, unavailable := blocked[candidate]; !unavailable {
			available = append(available, candidate)
		}
	}

	plan.Capacity = Capacity{
		Total:       normalized.Pool.Capacity(),
		Desired:     len(normalized.Requirements),
		Existing:    len(existingAddresses),
		Retained:    len(plan.Retained),
		Needed:      len(needed),
		Quarantined: len(quarantined),
		Conflicted:  len(conflicted),
		Blocked:     len(blocked),
		Allocatable: len(available),
	}
	if len(needed) > len(available) {
		return Plan{Capacity: plan.Capacity}, &ExhaustionError{
			Required:  len(needed),
			Available: len(available),
			Missing:   len(needed) - len(available),
			Capacity:  plan.Capacity,
		}
	}

	for index, requirement := range needed {
		plan.Allocated = append(plan.Allocated, Lease{
			Key: requirement, Address: available[index], Ownership: normalized.Ownership,
		})
	}
	plan.Capacity.Remaining = len(available) - len(needed)
	plan.Leases = append(plan.Leases, plan.Retained...)
	plan.Leases = append(plan.Leases, plan.Allocated...)
	slices.SortFunc(plan.Leases, compareLeases)
	slices.SortFunc(plan.Retained, compareLeases)
	slices.SortFunc(plan.Allocated, compareLeases)
	slices.SortFunc(plan.Released, compareLeases)
	return plan, nil
}

// normalizeInput validates the complete state and canonicalizes address and slice ordering.
func normalizeInput(input Input) (Input, error) {
	if err := input.Pool.Validate(); err != nil {
		return Input{}, err
	}
	if err := input.Ownership.Validate(); err != nil {
		return Input{}, err
	}

	normalized := Input{Pool: input.Pool, Ownership: input.Ownership}
	normalized.Requirements = slices.Clone(input.Requirements)
	normalized.Existing = slices.Clone(input.Existing)
	normalized.Quarantines = slices.Clone(input.Quarantines)
	normalized.Conflicts = slices.Clone(input.Conflicts)

	requirements := make(map[LeaseKey]struct{}, len(normalized.Requirements))
	primaries := make(map[string]struct{}, len(normalized.Requirements))
	for _, requirement := range normalized.Requirements {
		if err := requirement.Validate(); err != nil {
			return Input{}, err
		}
		if _, duplicate := requirements[requirement]; duplicate {
			return Input{}, fmt.Errorf("identity plan: duplicate requirement %s", formatKey(requirement))
		}
		requirements[requirement] = struct{}{}
		if requirement.Kind() == LeaseKindPrimary {
			primaries[string(requirement.ProjectID)] = struct{}{}
		}
	}
	for _, requirement := range normalized.Requirements {
		if requirement.Kind() != LeaseKindSecondary {
			continue
		}
		if _, found := primaries[string(requirement.ProjectID)]; !found {
			return Input{}, fmt.Errorf("identity plan: secondary %s requires a primary identity", formatKey(requirement))
		}
	}
	slices.SortFunc(normalized.Requirements, compareKeys)

	leaseKeys := make(map[LeaseKey]struct{}, len(normalized.Existing))
	leaseAddresses := make(map[netip.Addr]struct{}, len(normalized.Existing))
	for index := range normalized.Existing {
		lease := &normalized.Existing[index]
		lease.Address = lease.Address.Unmap()
		if err := lease.Validate(); err != nil {
			return Input{}, err
		}
		if !normalized.Pool.Contains(lease.Address) {
			return Input{}, fmt.Errorf("identity plan: existing lease address %s is not a pool candidate", lease.Address)
		}
		if _, duplicate := leaseKeys[lease.Key]; duplicate {
			return Input{}, fmt.Errorf("identity plan: duplicate existing lease key %s", formatKey(lease.Key))
		}
		if _, duplicate := leaseAddresses[lease.Address]; duplicate {
			return Input{}, fmt.Errorf("identity plan: duplicate existing lease address %s", lease.Address)
		}
		leaseKeys[lease.Key] = struct{}{}
		leaseAddresses[lease.Address] = struct{}{}
	}
	slices.SortFunc(normalized.Existing, compareLeases)

	quarantines := make(map[netip.Addr]struct{}, len(normalized.Quarantines))
	for index := range normalized.Quarantines {
		quarantine := &normalized.Quarantines[index]
		quarantine.Address = quarantine.Address.Unmap()
		if err := quarantine.Validate(normalized.Pool); err != nil {
			return Input{}, err
		}
		if _, duplicate := quarantines[quarantine.Address]; duplicate {
			return Input{}, fmt.Errorf("identity plan: duplicate quarantine %s", quarantine.Address)
		}
		quarantines[quarantine.Address] = struct{}{}
	}
	for index := range normalized.Conflicts {
		conflict := &normalized.Conflicts[index]
		conflict.Address = conflict.Address.Unmap()
		if err := conflict.Validate(normalized.Pool); err != nil {
			return Input{}, err
		}
	}
	return normalized, nil
}

// compareKeys adapts canonical key ordering to slices.SortFunc.
func compareKeys(left LeaseKey, right LeaseKey) int {
	if keyLess(left, right) {
		return -1
	}
	if keyLess(right, left) {
		return 1
	}
	return 0
}

// compareLeases adapts canonical lease ordering to slices.SortFunc.
func compareLeases(left Lease, right Lease) int {
	if leaseLess(left, right) {
		return -1
	}
	if leaseLess(right, left) {
		return 1
	}
	return 0
}

// formatKey renders a stable identity without presenting its address as its identity.
func formatKey(key LeaseKey) string {
	if key.SecondaryID == "" {
		return string(key.ProjectID) + "/primary"
	}
	return string(key.ProjectID) + "/secondary/" + key.SecondaryID
}
