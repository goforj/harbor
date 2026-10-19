package state

import (
	"fmt"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/models"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/migrations"
)

// TestStoreExpandsFullNetworkAndAdmitsOneHundredProjects proves upgrade, rollback, restart, and lease preservation through real persistence.
func TestStoreExpandsFullNetworkAndAdmitsOneHundredProjects(t *testing.T) {
	fixture, _ := newFullNetworkDataPlaneSetupProjectionFixture(t)
	for _, migration := range migrations.GetMigrations() {
		if migration.Name() == "2026_10_19_010000_expand_loopback_capacity" {
			if err := migration.Up(fixture.database); err != nil {
				t.Fatal(err)
			}
		}
	}
	store := fixture.store
	before, _, err := store.Network(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	anchor := emptyProjectStoreMutationProject("project-capacity-anchor")
	anchor.UpdatedAt = before.UpdatedAt.Add(time.Second)
	registered, err := store.RegisterProject(t.Context(), anchor)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := identity.NewPrimaryKey(anchor.ID)
	lease := identity.Lease{Key: key, Address: before.Pool.Candidates()[0], Ownership: before.Ownership}
	endpoint := EndpointReservation{Key: EndpointReservationKey{ProjectID: anchor.ID, EndpointID: "app-http"}, Protocol: EndpointProtocolHTTP, Host: anchor.Slug + ".test", Public: before.Reservations.Listeners.HTTPS.Advertised, Generation: 1}
	assigned, err := store.ReplaceProjectNetwork(t.Context(), ReplaceProjectNetworkRequest{ProjectID: anchor.ID, ExpectedProjectRevision: registered.Record.Revision, ExpectedNetworkRevision: before.Revision, Ensures: []NetworkLeaseEnsure{{Lease: lease, Generation: 1, EnsureEvidence: "fixture owned identity", LeasedAt: anchor.UpdatedAt}}, Releases: []NetworkLeaseRelease{}, Endpoints: []EndpointReservation{endpoint}, At: anchor.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	before = assigned.Record
	projected, _, err := readMachineOwnershipProjectionInTransaction(fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	target := projected.Record
	target.LoopbackPoolPrefix = netip.PrefixFrom(before.Pool.Prefix().Addr(), 24).Masked().String()
	operation, err := domain.NewOperation("operation-capacity-upgrade", "intent-capacity-upgrade", domain.OperationKindNetworkSetup, "", before.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approval, err := projectStoreMutationJournal(store).StageNetworkSetup(t.Context(), StageNetworkSetupRequest{Operation: operation, Ownership: target, Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	interleaved := emptyProjectStoreMutationProject("project-during-approval")
	interleaved.UpdatedAt = operation.RequestedAt.Add(time.Second)
	if _, err := store.RegisterProject(t.Context(), interleaved); err != nil {
		t.Fatal(err)
	}
	store = reopenNetworkChurnStore(t, store)
	db, err := store.mutations.connections.GetHarbord()
	if err != nil {
		t.Fatal(err)
	}
	source := NewNetworkSetupPlanSource(models.NewNetworkSetupPlanRepo(store.mutations.connections))
	plan, err := source.Resolve(t.Context(), ticketissuer.PoolRequest{OperationID: operation.ID})
	if err != nil || plan.PreviousPool != projected.Record.LoopbackPoolPrefix || plan.Pool.Capacity() != 256 {
		t.Fatalf("restart plan = %v, %v", plan.Pool.Prefix(), err)
	}
	request := networkSetupCompletionRequest(t, approval, target)
	request.At = operation.RequestedAt.Add(time.Minute)
	beforeRows := networkReplaceTestRows(t, db)
	if err := db.Exec("CREATE TRIGGER fail_capacity_expansion BEFORE UPDATE OF pool_network ON network_state BEGIN SELECT RAISE(ABORT, 'injected capacity expansion failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteNetworkSetup(t.Context(), request); err == nil {
		t.Fatal("injected write failure ignored")
	}
	if after := networkReplaceTestRows(t, db); !reflect.DeepEqual(beforeRows, after) {
		t.Fatal("failed expansion changed network authority")
	}
	if _, err := source.Resolve(t.Context(), ticketissuer.PoolRequest{OperationID: operation.ID}); err != nil {
		t.Fatal("failed commit lost retryable approval", err)
	}
	if err := db.Exec("DROP TRIGGER fail_capacity_expansion").Error; err != nil {
		t.Fatal(err)
	}
	expanded, err := store.CompleteNetworkSetup(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !expanded.Expanded || expanded.Network.Record.Pool.Capacity() != 256 || expanded.Network.Record.Stage != NetworkStageFull || !reflect.DeepEqual(before.Leases, expanded.Network.Record.Leases) || !reflect.DeepEqual(before.Reservations, expanded.Network.Record.Reservations) {
		t.Fatal("expansion disturbed retained project or shared routes")
	}
	store = reopenNetworkChurnStore(t, store)
	if _, err := store.CompleteNetworkSetup(t.Context(), request); err != nil {
		t.Fatal("lost completion response did not replay", err)
	}
	for index := range 100 {
		project := emptyProjectStoreMutationProject(domain.ProjectID(fmt.Sprintf("project-large-%03d", index)))
		project.UpdatedAt = request.At.Add(time.Minute)
		registered, err := store.RegisterProject(t.Context(), project)
		if err != nil {
			t.Fatal(err)
		}
		network, _, err := store.Network(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		key, _ := identity.NewPrimaryKey(project.ID)
		requirements := []identity.LeaseKey{key}
		for _, retained := range network.Leases {
			requirements = append(requirements, retained.Key)
		}
		plan, err := identity.NewPlanner().Plan(identity.Input{Pool: network.Pool, Ownership: network.Ownership, Requirements: requirements, Existing: network.Leases, Quarantines: network.Quarantines})
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.ReplaceProjectNetwork(t.Context(), ReplaceProjectNetworkRequest{ProjectID: project.ID, ExpectedProjectRevision: registered.Record.Revision, ExpectedNetworkRevision: network.Revision, Ensures: []NetworkLeaseEnsure{{Lease: plan.Allocated[0], Generation: 1, EnsureEvidence: "fixture free identity", LeasedAt: project.UpdatedAt}}, Releases: []NetworkLeaseRelease{}, Endpoints: []EndpointReservation{}, At: project.UpdatedAt})
		if err != nil {
			t.Fatalf("project %d: %v", index, err)
		}
	}
	network, _, err := store.Network(t.Context())
	if err != nil || len(network.Leases) != 101 {
		t.Fatalf("large installation = %d leases, %v", len(network.Leases), err)
	}
	found := false
	for _, retained := range network.Leases {
		if retained == lease {
			found = true
		}
	}
	if !found {
		t.Fatal("original project address changed")
	}
	if _, err := store.CompleteNetworkSetup(t.Context(), request); err != nil {
		t.Fatal("terminal expansion replay after later allocations failed", err)
	}
}
