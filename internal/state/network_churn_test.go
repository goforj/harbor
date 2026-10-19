package state

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/database"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/inspects"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
)

// TestNetworkAddressLifecycleSurvivesPoolChurnAndRestart exercises more removals than capacity through production state mutations.
func TestNetworkAddressLifecycleSurvivesPoolChurnAndRestart(t *testing.T) {
	store, _ := newNetworkInitializeTestHarness(t, false)
	initial := networkIdentityInitializeTestRequest()
	addresses := []string{"127.77.0.8", "127.77.0.9", "127.77.0.10", "127.77.0.11", "127.77.0.12", "127.77.0.13", "127.77.0.14", "127.77.0.15"}
	initial.Pool = recordTestPool("127.77.0.8/29", addresses...)
	if _, err := store.InitializeNetworkIdentity(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	at := initial.At.Add(time.Hour)
	lastGenerations := map[netip.Addr]uint64{}
	var retained NetworkLeaseEnsure
	for cycle := 0; cycle < 33; cycle++ {
		at = at.Add(2 * time.Hour)
		project := emptyProjectStoreMutationProject(domain.ProjectID(fmt.Sprintf("project-churn-%d", cycle)))
		project.UpdatedAt = at
		registered, err := store.RegisterProject(t.Context(), project)
		if err != nil {
			t.Fatal(err)
		}
		network, _, err := store.Network(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		candidates, err := store.ReusableNetworkAddresses(t.Context(), network.Revision, at)
		if err != nil {
			t.Fatal(err)
		}
		eligible := map[netip.Addr]uint64{}
		for _, candidate := range candidates {
			eligible[candidate.Address] = candidate.Generation
		}
		quarantines := slices.DeleteFunc(slices.Clone(network.Quarantines), func(q identity.Quarantine) bool { return eligible[q.Address] != 0 })
		key, err := identity.NewPrimaryKey(project.ID)
		if err != nil {
			t.Fatal(err)
		}
		requirements := []identity.LeaseKey{key}
		for _, lease := range network.Leases {
			requirements = append(requirements, lease.Key)
		}
		input := identity.Input{Pool: network.Pool, Ownership: network.Ownership, Requirements: requirements, Existing: network.Leases, Quarantines: network.Quarantines}
		plan, err := identity.NewPlanner().Plan(input)
		if err != nil {
			input.Quarantines = quarantines
			plan, err = identity.NewPlanner().Plan(input)
		}
		if err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if len(plan.Allocated) != 1 {
			t.Fatalf("cycle %d allocations = %v", cycle, plan.Allocated)
		}
		lease := plan.Allocated[0]
		generation := uint64(1)
		if eligible[lease.Address] != 0 {
			generation = eligible[lease.Address]
		}
		if generation <= lastGenerations[lease.Address] {
			t.Fatalf("cycle %d reused a stale address generation", cycle)
		}
		ensure := NetworkLeaseEnsure{Lease: lease, Generation: generation, LeasedAt: at, EnsureEvidence: "fixture exact alias and free port"}
		assigned, err := store.ReplaceProjectNetwork(t.Context(), ReplaceProjectNetworkRequest{ProjectID: project.ID, ExpectedProjectRevision: registered.Record.Revision, ExpectedNetworkRevision: network.Revision, Ensures: []NetworkLeaseEnsure{ensure}, Releases: []NetworkLeaseRelease{}, Endpoints: []EndpointReservation{}, At: at})
		if err != nil {
			t.Fatalf("cycle %d allocation: %v", cycle, err)
		}
		lastGenerations[lease.Address] = generation
		if cycle == 0 {
			retained = ensure
			continue
		}
		journal := projectStoreMutationJournal(store)
		operation, err := domain.NewOperation(domain.OperationID(fmt.Sprintf("operation-churn-%d", cycle)), domain.IntentID(fmt.Sprintf("intent-churn-%d", cycle)), domain.OperationKindProjectUnregister, project.ID, at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		queued, err := journal.Enqueue(t.Context(), operation)
		if err != nil {
			t.Fatal(err)
		}
		running, err := journal.Transition(t.Context(), operation.ID, queued.Revision, domain.OperationRunning, "removing project", at.Add(2*time.Second), nil)
		if err != nil {
			t.Fatal(err)
		}
		begin := BeginProjectNetworkReleaseRequest{ProjectID: project.ID, OperationID: operation.ID, ExpectedNetworkRevision: assigned.Record.Revision, ExpectedProjectRevision: registered.Record.Revision, ExpectedOperationRevision: running.Revision, BeginGeneration: generation + 1, At: at.Add(3 * time.Second)}
		begun, err := store.BeginProjectNetworkRelease(t.Context(), begin)
		if err != nil {
			t.Fatal(err)
		}
		if cycle%4 == 0 {
			store = reopenNetworkChurnStore(t, store)
		}
		complete := networkReleaseTestCompleteRequest(begin, begun.Release)
		complete.ExpectedNetworkRevision = begun.Record.Revision
		completed, err := store.CompleteProjectNetworkRelease(t.Context(), complete)
		if err != nil {
			t.Fatal(err)
		}
		if cycle%4 == 0 {
			store = reopenNetworkChurnStore(t, store)
		}
		beforeRemoval, err := store.ReusableNetworkAddresses(t.Context(), completed.Record.Revision, complete.At.Add(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range beforeRemoval {
			if candidate.Address == lease.Address {
				t.Fatal("retained project released its primary proof before unregister completed")
			}
		}
		if _, err := store.CompleteProjectUnregister(t.Context(), project.ID, operation.ID, running.Revision, "completed removal", complete.At.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if cycle%4 == 0 {
			store = reopenNetworkChurnStore(t, store)
		}
		after, _, err := store.Network(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Leases) != 1 || !reflect.DeepEqual(after.Leases[0], retained.Lease) {
			t.Fatalf("cycle %d disturbed retained project: %v", cycle, after.Leases)
		}
		cooling, err := store.ReusableNetworkAddresses(t.Context(), after.Revision, complete.At.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range cooling {
			if candidate.Address == lease.Address {
				t.Fatal("removed project bypassed the reuse delay")
			}
		}
	}
	if len(lastGenerations) != len(addresses) {
		t.Fatalf("churn did not exercise all pool addresses: %v", lastGenerations)
	}
	if lastGenerations[netip.MustParseAddr(addresses[1])] <= 1 {
		t.Fatal("exhausted pool did not recycle a released address")
	}
}

// reopenNetworkChurnStore discards in-memory writers and reopens the same SQLite database like a daemon restart.
func reopenNetworkChurnStore(t *testing.T, store *Store) *Store {
	t.Helper()
	if err := store.mutations.connections.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	connections := database.NewConnections(inspects.NewManager())
	t.Cleanup(func() {
		if err := connections.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return newStore(models.NewHarborStateRepo(connections), models.NewProjectRepo(connections), models.NewProjectSessionRepo(connections), models.NewNetworkStateRepo(connections), NewMutationCoordinator(connections), projectStoreMutationTestClock)
}
