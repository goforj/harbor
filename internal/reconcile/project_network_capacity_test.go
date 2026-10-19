package reconcile

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectruntime"
)

// TestProjectPrimaryLeaseCapacityDiagnostics proves exhausted starts report the observed cause without writing a lease.
func TestProjectPrimaryLeaseCapacityDiagnostics(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	cases := []struct {
		name      string
		configure func(*primaryLeaseTestFixture)
		code      string
		message   string
	}{
		{"missing alias", func(f *primaryLeaseTestFixture) {
			f.loopback.facts[address] = primaryLeaseTestAbsentObservation(address)
		}, "project.network.repair_required", "approve restoring"},
		{"occupied port", func(f *primaryLeaseTestFixture) {
			f.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, false)
		}, "project.network.port_unavailable", "Stop the conflicting listeners"},
		{"quarantined", func(f *primaryLeaseTestFixture) {
			f.state.network.Quarantines = []identity.Quarantine{{Address: address, Reason: "removal pending"}}
		}, "project.network.addresses_quarantined", "removal and the reuse delay"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			test.configure(fixture)
			_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
			var rejection *projectPrimaryLeaseRejection
			if !errors.As(err, &rejection) || string(rejection.Problem().Code) != test.code || !strings.Contains(rejection.Problem().Message, test.message) {
				t.Fatalf("Ensure() = %v, want %s containing %q", err, test.code, test.message)
			}
			if !strings.Contains(rejection.Problem().Message, "1 loopback addresses") || len(fixture.state.replaceCalls) != 0 {
				t.Fatal("failure hid capacity or persisted an unsafe lease")
			}
		})
	}
}

// TestProjectPrimaryLeaseRepairAliasDriftIsBounded proves host drift during listener repair cannot loop on the same candidate forever.
func TestProjectPrimaryLeaseRepairAliasDriftIsBounded(t *testing.T) {
	first := netip.MustParseAddr("127.77.0.11")
	second := netip.MustParseAddr("127.77.0.12")
	fixture := newPrimaryLeaseTestFixture(t, first, second)
	fixture.ports.results[first] = primaryLeaseTestProbeResult(first, 3000, false)
	repairer := &primaryLeaseTestRuntimeRepairer{result: projectruntime.ListenerRepairResult{Settled: true}, onRepair: func() { fixture.loopback.facts[first] = primaryLeaseTestAbsentObservation(first) }}
	fixture.coordinator.runtimeRepairer = repairer
	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease.Address != second || len(repairer.requests) != 1 || len(fixture.loopback.calls) != 3 {
		t.Fatalf("Ensure() = %v, %v; repair calls = %d, observations = %v", admission.Lease, err, len(repairer.requests), fixture.loopback.calls)
	}
}

// TestProjectPrimaryLeaseRepairRestoresReleasedCapacity proves a retry uses a restored alias without clearing quarantine early.
func TestProjectPrimaryLeaseRepairRestoresReleasedCapacity(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.loopback.facts[address] = primaryLeaseTestAbsentObservation(address)
	if _, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID); err == nil {
		t.Fatal("missing alias admitted")
	}
	// The approved setup coordinator owns this host effect; allocation only observes its outcome.
	fixture.loopback.facts[address] = primaryLeaseTestExactObservation(address)
	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease.Address != address || len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("retry after repair = %#v, %v", admission.Lease, err)
	}
	if fixture.loopback.facts[address].State != loopback.StateExact {
		t.Fatal("allocation mutated host state")
	}
}

// TestPrimaryLeaseCapacityProblemPreservesReservedAndForeignAuthority covers causes without a safe candidate to probe.
func TestPrimaryLeaseCapacityProblemPreservesReservedAndForeignAuthority(t *testing.T) {
	reserved := primaryLeaseCapacityProblem(identity.Capacity{Total: 8, Existing: 8}, 0, 0, 0)
	if reserved.Code != "project.network.capacity_exhausted" || !strings.Contains(reserved.Message, "8 leased") || !strings.Contains(reserved.Message, "stopping it does not") {
		t.Fatalf("reserved problem = %#v", reserved)
	}
	conflict := primaryLeaseCapacityProblem(identity.Capacity{Total: 8, Conflicted: 8}, 0, 8, 0)
	if conflict.Code != "project.network.host_conflict" || !strings.Contains(conflict.Message, "foreign host state") {
		t.Fatalf("conflict problem = %#v", conflict)
	}
}
