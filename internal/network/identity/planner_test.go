package identity

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
)

// TestPlannerAllocatesPrimaryAndSecondaryLeasesDeterministically verifies canonical key and address ordering.
func TestPlannerAllocatesPrimaryAndSecondaryLeasesDeterministically(t *testing.T) {
	pool := mustPool(t,
		"127.77.0.13",
		"127.77.0.10",
		"127.77.0.12",
		"127.77.0.11",
	)
	ownership := mustOwnership(t, "installation-a", 4)
	requirements := []LeaseKey{
		mustPrimary(t, "beta"),
		mustSecondary(t, "alpha", "mail.smtp"),
		mustPrimary(t, "alpha"),
		mustSecondary(t, "alpha", "database.primary"),
	}
	originalRequirements := append([]LeaseKey(nil), requirements...)

	plan, err := NewPlanner().Plan(Input{
		Pool: pool, Ownership: ownership, Requirements: requirements,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	if got, want := leaseKeys(plan.Leases), []string{
		"alpha/primary",
		"alpha/secondary/database.primary",
		"alpha/secondary/mail.smtp",
		"beta/primary",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lease keys = %v, want %v", got, want)
	}
	if got, want := leaseAddresses(plan.Leases), []string{
		"127.77.0.10",
		"127.77.0.11",
		"127.77.0.12",
		"127.77.0.13",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lease addresses = %v, want %v", got, want)
	}
	for _, lease := range plan.Leases {
		if lease.Ownership != ownership {
			t.Fatalf("lease ownership = %#v, want %#v", lease.Ownership, ownership)
		}
	}
	if len(plan.Retained) != 0 || len(plan.Released) != 0 {
		t.Fatalf("new plan retained %d and released %d leases", len(plan.Retained), len(plan.Released))
	}
	if got, want := plan.Capacity, (Capacity{
		Total: 4, Desired: 4, Needed: 4, Allocatable: 4,
	}); got != want {
		t.Fatalf("capacity = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(requirements, originalRequirements) {
		t.Fatalf("Plan() mutated requirements: got %v, want %v", requirements, originalRequirements)
	}

	reversed := append([]Lease(nil), plan.Leases...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reconciled, err := NewPlanner().Plan(Input{
		Pool: pool, Ownership: ownership, Requirements: requirements, Existing: reversed,
	})
	if err != nil {
		t.Fatalf("second Plan() error = %v", err)
	}
	if !reflect.DeepEqual(reconciled.Leases, plan.Leases) {
		t.Fatalf("stable leases = %#v, want %#v", reconciled.Leases, plan.Leases)
	}
	if len(reconciled.Allocated) != 0 || len(reconciled.Released) != 0 || len(reconciled.Retained) != 4 {
		t.Fatalf("second plan allocated=%d retained=%d released=%d", len(reconciled.Allocated), len(reconciled.Retained), len(reconciled.Released))
	}
}

// TestPlannerRetainsStableLeasesBeforeAllocating verifies existing identity stability across new requirements.
func TestPlannerRetainsStableLeasesBeforeAllocating(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11", "127.77.0.12")
	ownership := mustOwnership(t, "installation-a", 2)
	alpha := mustPrimary(t, "alpha")
	existing := Lease{Key: alpha, Address: mustAddress(t, "127.77.0.12"), Ownership: ownership}

	plan, err := NewPlanner().Plan(Input{
		Pool:      pool,
		Ownership: ownership,
		Requirements: []LeaseKey{
			mustPrimary(t, "beta"),
			mustSecondary(t, "alpha", "database.primary"),
			alpha,
		},
		Existing: []Lease{existing},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	if got, want := plan.Retained, []Lease{existing}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retained = %#v, want %#v", got, want)
	}
	if got, want := leaseKeys(plan.Allocated), []string{"alpha/secondary/database.primary", "beta/primary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allocated keys = %v, want %v", got, want)
	}
	if got, want := leaseAddresses(plan.Allocated), []string{"127.77.0.10", "127.77.0.11"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allocated addresses = %v, want %v", got, want)
	}
	if got, want := plan.Capacity, (Capacity{
		Total: 3, Desired: 3, Existing: 1, Retained: 1, Needed: 2,
		Blocked: 1, Allocatable: 2,
	}); got != want {
		t.Fatalf("capacity = %#v, want %#v", got, want)
	}
}

// TestPlannerDoesNotReuseReleasedAddressesInOnePass verifies that stale clients cannot be redirected during a transition.
func TestPlannerDoesNotReuseReleasedAddressesInOnePass(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11")
	ownership := mustOwnership(t, "installation-a", 1)
	existing := Lease{
		Key: mustPrimary(t, "old-project"), Address: mustAddress(t, "127.77.0.10"), Ownership: ownership,
	}

	plan, err := NewPlanner().Plan(Input{
		Pool:         pool,
		Ownership:    ownership,
		Requirements: []LeaseKey{mustPrimary(t, "new-project")},
		Existing:     []Lease{existing},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := leaseAddresses(plan.Allocated), []string{"127.77.0.11"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allocated addresses = %v, want %v", got, want)
	}
	if got, want := plan.Released, []Lease{existing}; !reflect.DeepEqual(got, want) {
		t.Fatalf("released = %#v, want %#v", got, want)
	}
}

// TestPlannerAccountsForQuarantinesConflictsAndOverlaps verifies exact union and diagnostic capacity counts.
func TestPlannerAccountsForQuarantinesConflictsAndOverlaps(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11", "127.77.0.12", "127.77.0.13")
	ownership := mustOwnership(t, "installation-a", 1)

	plan, err := NewPlanner().Plan(Input{
		Pool:         pool,
		Ownership:    ownership,
		Requirements: []LeaseKey{mustPrimary(t, "beta"), mustPrimary(t, "alpha")},
		Quarantines: []Quarantine{
			{Address: mustAddress(t, "127.77.0.10"), Reason: "recently released"},
		},
		Conflicts: []Conflict{
			{Address: mustAddress(t, "127.77.0.10"), Kind: ConflictKindResolver, Detail: "foreign answer"},
			{Address: mustAddress(t, "127.77.0.10"), Kind: ConflictKindOwnership, Detail: "foreign owner"},
			{Address: mustAddress(t, "127.77.0.11"), Kind: ConflictKindListener, Port: 3306, Detail: "foreign listener"},
		},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	if got, want := leaseAddresses(plan.Leases), []string{"127.77.0.12", "127.77.0.13"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lease addresses = %v, want %v", got, want)
	}
	if got, want := plan.Capacity, (Capacity{
		Total: 4, Desired: 2, Needed: 2, Quarantined: 1, Conflicted: 2,
		Blocked: 2, Allocatable: 2,
	}); got != want {
		t.Fatalf("capacity = %#v, want %#v", got, want)
	}
}

// TestPlannerRelocatesCurrentOwnedLeaseAfterConflict verifies safe replacement without immediate address reuse.
func TestPlannerRelocatesCurrentOwnedLeaseAfterConflict(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11")
	ownership := mustOwnership(t, "installation-a", 3)
	key := mustPrimary(t, "alpha")
	existing := Lease{Key: key, Address: mustAddress(t, "127.77.0.10"), Ownership: ownership}

	plan, err := NewPlanner().Plan(Input{
		Pool: pool, Ownership: ownership, Requirements: []LeaseKey{key}, Existing: []Lease{existing},
		Conflicts: []Conflict{{
			Address: mustAddress(t, "127.77.0.10"), Kind: ConflictKindListener, Port: 3306,
		}},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := plan.Released, []Lease{existing}; !reflect.DeepEqual(got, want) {
		t.Fatalf("released = %#v, want %#v", got, want)
	}
	if got, want := leaseAddresses(plan.Allocated), []string{"127.77.0.11"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allocated addresses = %v, want %v", got, want)
	}
	if got, want := plan.Capacity, (Capacity{
		Total: 2, Desired: 1, Existing: 1, Needed: 1, Conflicted: 1,
		Blocked: 1, Allocatable: 1,
	}); got != want {
		t.Fatalf("capacity = %#v, want %#v", got, want)
	}
}

// TestPlannerPreservesForeignLeases verifies that another owner blocks capacity but is never released.
func TestPlannerPreservesForeignLeases(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11")
	current := mustOwnership(t, "installation-a", 2)
	foreign := mustOwnership(t, "installation-b", 7)
	existing := Lease{
		Key: mustPrimary(t, "foreign-project"), Address: mustAddress(t, "127.77.0.10"), Ownership: foreign,
	}

	plan, err := NewPlanner().Plan(Input{
		Pool: pool, Ownership: current, Requirements: []LeaseKey{mustPrimary(t, "alpha")}, Existing: []Lease{existing},
		Quarantines: []Quarantine{{Address: mustAddress(t, "127.77.0.10"), Reason: "ownership review"}},
		Conflicts:   []Conflict{{Address: mustAddress(t, "127.77.0.10"), Kind: ConflictKindOwnership}},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Released) != 0 {
		t.Fatalf("released foreign leases = %#v", plan.Released)
	}
	if got, want := leaseAddresses(plan.Allocated), []string{"127.77.0.11"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allocated addresses = %v, want %v", got, want)
	}
	if got, want := plan.Capacity, (Capacity{
		Total: 2, Desired: 1, Existing: 1, Needed: 1, Quarantined: 1,
		Conflicted: 1, Blocked: 1, Allocatable: 1,
	}); got != want {
		t.Fatalf("capacity = %#v, want %#v", got, want)
	}
}

// TestPlannerRejectsDesiredLeaseFromAnotherOwnershipGeneration verifies compare-and-swap ownership behavior.
func TestPlannerRejectsDesiredLeaseFromAnotherOwnershipGeneration(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11")
	current := mustOwnership(t, "installation-a", 3)
	stale := mustOwnership(t, "installation-a", 2)
	key := mustPrimary(t, "alpha")

	_, err := NewPlanner().Plan(Input{
		Pool: pool, Ownership: current, Requirements: []LeaseKey{key},
		Existing: []Lease{{Key: key, Address: mustAddress(t, "127.77.0.10"), Ownership: stale}},
	})
	var ownershipError *OwnershipConflictError
	if !errors.As(err, &ownershipError) {
		t.Fatalf("Plan() error = %v, want OwnershipConflictError", err)
	}
	if ownershipError.Key != key || ownershipError.Expected != current || ownershipError.Actual != stale {
		t.Fatalf("OwnershipConflictError = %#v", ownershipError)
	}
	if got, want := err.Error(), "loopback identity alpha/primary has ownership installation-a/2, expected installation-a/3"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestPlannerReportsExactExhaustion verifies actionable capacity fields and wording.
func TestPlannerReportsExactExhaustion(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11", "127.77.0.12", "127.77.0.13")
	current := mustOwnership(t, "installation-a", 1)
	foreign := mustOwnership(t, "installation-b", 1)

	plan, err := NewPlanner().Plan(Input{
		Pool:         pool,
		Ownership:    current,
		Requirements: []LeaseKey{mustPrimary(t, "alpha"), mustPrimary(t, "beta")},
		Existing: []Lease{{
			Key: mustPrimary(t, "foreign"), Address: mustAddress(t, "127.77.0.10"), Ownership: foreign,
		}},
		Quarantines: []Quarantine{{Address: mustAddress(t, "127.77.0.11"), Reason: "cooldown"}},
		Conflicts:   []Conflict{{Address: mustAddress(t, "127.77.0.12"), Kind: ConflictKindAddress}},
	})
	var exhaustion *ExhaustionError
	if !errors.As(err, &exhaustion) {
		t.Fatalf("Plan() error = %v, want ExhaustionError", err)
	}
	if got, want := *exhaustion, (ExhaustionError{
		Required: 2, Available: 1, Missing: 1,
		Capacity: Capacity{
			Total: 4, Desired: 2, Existing: 1, Needed: 2, Quarantined: 1,
			Conflicted: 1, Blocked: 3, Allocatable: 1,
		},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExhaustionError = %#v, want %#v", got, want)
	}
	if plan.Capacity != exhaustion.Capacity {
		t.Fatalf("returned capacity = %#v, error capacity = %#v", plan.Capacity, exhaustion.Capacity)
	}
	if len(plan.Leases) != 0 || len(plan.Retained) != 0 || len(plan.Allocated) != 0 || len(plan.Released) != 0 {
		t.Fatalf("exhausted plan exposed partial actions: %#v", plan)
	}
	if got, want := err.Error(), "loopback identity pool exhausted: 2 addresses required, 1 available, 1 missing (4 total, 3 blocked, 1 existing, 1 quarantined, 1 conflicted)"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestPlannerRejectsInconsistentInput verifies planner invariants before any transition is returned.
func TestPlannerRejectsInconsistentInput(t *testing.T) {
	pool := mustPool(t, "127.77.0.10", "127.77.0.11", "127.77.0.12")
	ownership := mustOwnership(t, "installation-a", 1)
	alpha := mustPrimary(t, "alpha")
	beta := mustPrimary(t, "beta")

	tests := []struct {
		name     string
		input    Input
		contains string
	}{
		{
			name:     "invalid pool",
			input:    Input{Ownership: ownership},
			contains: "prefix is invalid",
		},
		{
			name:     "invalid ownership",
			input:    Input{Pool: pool, Ownership: Ownership{InstallationID: "installation-a"}},
			contains: "generation",
		},
		{
			name:     "duplicate requirement",
			input:    Input{Pool: pool, Ownership: ownership, Requirements: []LeaseKey{alpha, alpha}},
			contains: "duplicate requirement alpha/primary",
		},
		{
			name:     "secondary without primary",
			input:    Input{Pool: pool, Ownership: ownership, Requirements: []LeaseKey{mustSecondary(t, "alpha", "mysql.secondary")}},
			contains: "requires a primary identity",
		},
		{
			name:     "invalid project ID",
			input:    Input{Pool: pool, Ownership: ownership, Requirements: []LeaseKey{{ProjectID: domain.ProjectID(" bad ")}}},
			contains: "project ID",
		},
		{
			name:     "existing address outside pool",
			input:    Input{Pool: pool, Ownership: ownership, Existing: []Lease{{Key: alpha, Address: mustAddress(t, "127.78.0.10"), Ownership: ownership}}},
			contains: "not a pool candidate",
		},
		{
			name: "duplicate existing key",
			input: Input{Pool: pool, Ownership: ownership, Existing: []Lease{
				{Key: alpha, Address: mustAddress(t, "127.77.0.10"), Ownership: ownership},
				{Key: alpha, Address: mustAddress(t, "127.77.0.11"), Ownership: ownership},
			}},
			contains: "duplicate existing lease key",
		},
		{
			name: "duplicate existing address",
			input: Input{Pool: pool, Ownership: ownership, Existing: []Lease{
				{Key: alpha, Address: mustAddress(t, "127.77.0.10"), Ownership: ownership},
				{Key: beta, Address: mustAddress(t, "127.77.0.10"), Ownership: ownership},
			}},
			contains: "duplicate existing lease address",
		},
		{
			name: "duplicate quarantine",
			input: Input{Pool: pool, Ownership: ownership, Quarantines: []Quarantine{
				{Address: mustAddress(t, "127.77.0.10"), Reason: "first"},
				{Address: mustAddress(t, "127.77.0.10"), Reason: "second"},
			}},
			contains: "duplicate quarantine",
		},
		{
			name:     "unsupported conflict",
			input:    Input{Pool: pool, Ownership: ownership, Conflicts: []Conflict{{Address: mustAddress(t, "127.77.0.10"), Kind: "unknown"}}},
			contains: "unsupported",
		},
		{
			name:     "port on non-listener conflict",
			input:    Input{Pool: pool, Ownership: ownership, Conflicts: []Conflict{{Address: mustAddress(t, "127.77.0.10"), Kind: ConflictKindAddress, Port: 80}}},
			contains: "port is only valid",
		},
		{
			name:     "listener conflict without port",
			input:    Input{Pool: pool, Ownership: ownership, Conflicts: []Conflict{{Address: mustAddress(t, "127.77.0.10"), Kind: ConflictKindListener}}},
			contains: "listener port is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPlanner().Plan(test.input)
			if err == nil {
				t.Fatal("Plan() error = nil")
			}
			if !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Plan() error = %q, want substring %q", err, test.contains)
			}
		})
	}
}
