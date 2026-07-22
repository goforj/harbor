package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// primaryLeaseTestState keeps optimistic project and network revisions observable without replacing persistence semantics.
type primaryLeaseTestState struct {
	project      state.ProjectRecord
	projects     map[domain.ProjectID]state.ProjectRecord
	network      state.NetworkRecord
	initialized  bool
	projectErr   error
	networkErr   error
	replaceCalls []state.ReplaceProjectNetworkRequest
	replace      func(state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error)
}

// Project returns the fixture's exact registered project revision.
func (fixture *primaryLeaseTestState) Project(_ context.Context, projectID domain.ProjectID) (state.ProjectRecord, error) {
	if fixture.projectErr != nil {
		return state.ProjectRecord{}, fixture.projectErr
	}
	if fixture.projects != nil {
		project, found := fixture.projects[projectID]
		if !found {
			return state.ProjectRecord{}, &state.ProjectNotFoundError{ProjectID: projectID}
		}
		return project, nil
	}
	return fixture.project, nil
}

// Network returns the fixture's complete network authority at its configured stage.
func (fixture *primaryLeaseTestState) Network(context.Context) (state.NetworkRecord, bool, error) {
	if fixture.networkErr != nil {
		return state.NetworkRecord{}, false, fixture.networkErr
	}
	return fixture.network, fixture.initialized, nil
}

// ReplaceProjectNetwork applies one complete project delta while enforcing production's optimistic revisions.
func (fixture *primaryLeaseTestState) ReplaceProjectNetwork(
	_ context.Context,
	request state.ReplaceProjectNetworkRequest,
) (state.NetworkMutationResult, error) {
	fixture.replaceCalls = append(fixture.replaceCalls, request)
	if fixture.replace != nil {
		return fixture.replace(request)
	}
	if err := request.Validate(); err != nil {
		return state.NetworkMutationResult{}, err
	}
	if request.ExpectedNetworkRevision != fixture.network.Revision {
		return state.NetworkMutationResult{}, &state.NetworkRevisionConflictError{
			Expected: request.ExpectedNetworkRevision,
			Actual:   fixture.network.Revision,
		}
	}
	project := fixture.project
	if fixture.projects != nil {
		project = fixture.projects[request.ProjectID]
	}
	if request.ExpectedProjectRevision != project.Revision {
		return state.NetworkMutationResult{}, &state.ProjectRevisionConflictError{
			ProjectID: request.ProjectID,
			Expected:  request.ExpectedProjectRevision,
			Actual:    project.Revision,
		}
	}
	for _, ensure := range request.Ensures {
		fixture.network.Leases = append(fixture.network.Leases, ensure.Lease)
	}
	slices.SortFunc(fixture.network.Leases, primaryLeaseTestLeaseCompare)
	endpoints := make([]state.EndpointReservation, 0, len(fixture.network.Reservations.Endpoints)+len(request.Endpoints))
	for _, endpoint := range fixture.network.Reservations.Endpoints {
		if endpoint.Key.ProjectID != request.ProjectID {
			endpoints = append(endpoints, endpoint)
		}
	}
	endpoints = append(endpoints, request.Endpoints...)
	slices.SortFunc(endpoints, primaryLeaseEndpointCompare)
	fixture.network.Reservations.Endpoints = endpoints
	fixture.network.Revision++
	fixture.network.UpdatedAt = request.At
	if err := fixture.network.Validate(); err != nil {
		return state.NetworkMutationResult{}, err
	}
	return state.NetworkMutationResult{Record: fixture.network}, nil
}

// primaryLeaseTestDiscoverer records the exact address whose project App port was admitted.
type primaryLeaseTestDiscoverer struct {
	port  uint16
	err   error
	calls []netip.Addr
}

// DiscoverDefaultRuntimeAtAddress constructs the same literal-address runtime target as production discovery.
func (discoverer *primaryLeaseTestDiscoverer) DiscoverDefaultRuntimeAtAddress(
	_ context.Context,
	_ string,
	address netip.Addr,
) (projectdiscovery.RuntimeTarget, error) {
	discoverer.calls = append(discoverer.calls, address)
	if discoverer.err != nil {
		return projectdiscovery.RuntimeTarget{}, discoverer.err
	}
	return projectdiscovery.NewRuntimeTarget("app", "App", address, discoverer.port)
}

// primaryLeaseTestLoopbackObserver returns one independently fingerprintable assignment fact per candidate.
type primaryLeaseTestLoopbackObserver struct {
	facts map[netip.Addr]loopback.Observation
	errs  map[netip.Addr]error
	calls []netip.Addr
}

// Observe returns the configured exact-address host snapshot.
func (observer *primaryLeaseTestLoopbackObserver) Observe(_ context.Context, address netip.Addr) (loopback.Observation, error) {
	observer.calls = append(observer.calls, address)
	if err := observer.errs[address]; err != nil {
		return loopback.Observation{}, err
	}
	return observer.facts[address], nil
}

// primaryLeaseTestPortProber returns deterministic exact-address listener evidence.
type primaryLeaseTestPortProber struct {
	results map[netip.Addr]identity.ProbeResult
	errs    map[netip.Addr]error
	calls   []identity.ProbeRequest
}

// primaryLeaseTestRuntimeRepairer models the reviewed same-user cleanup boundary without native process effects.
type primaryLeaseTestRuntimeRepairer struct {
	inspection     projectprocess.UnattributedRuntimeInspection
	inspections    []projectprocess.UnattributedRuntimeInspection
	confirmation   projectprocess.RuntimeRepairConfirmation
	confirmations  []projectprocess.RuntimeRepairConfirmation
	inspectErr     error
	confirmErr     error
	inspectTargets []projectprocess.RuntimeRepairTarget
	candidates     []projectprocess.UnattributedRuntimeCandidate
	onInspect      func()
	onConfirm      func()
	inspectIndex   int
	confirmIndex   int
}

// Inspect returns one configured exact-listener classification for automatic retained-port recovery.
func (repairer *primaryLeaseTestRuntimeRepairer) Inspect(_ context.Context, target projectprocess.RuntimeRepairTarget) (projectprocess.UnattributedRuntimeInspection, error) {
	repairer.inspectTargets = append(repairer.inspectTargets, target)
	if repairer.onInspect != nil {
		repairer.onInspect()
	}
	if repairer.inspectErr != nil {
		return projectprocess.UnattributedRuntimeInspection{}, repairer.inspectErr
	}
	if repairer.inspectIndex < len(repairer.inspections) {
		inspection := repairer.inspections[repairer.inspectIndex]
		repairer.inspectIndex++
		return inspection, nil
	}
	return repairer.inspection, nil
}

// Confirm records one exact candidate and returns the configured bounded settlement result.
func (repairer *primaryLeaseTestRuntimeRepairer) Confirm(_ context.Context, candidate projectprocess.UnattributedRuntimeCandidate) (projectprocess.RuntimeRepairConfirmation, error) {
	repairer.candidates = append(repairer.candidates, candidate)
	if repairer.onConfirm != nil {
		repairer.onConfirm()
	}
	if repairer.confirmErr != nil {
		return projectprocess.RuntimeRepairConfirmation{}, repairer.confirmErr
	}
	if repairer.confirmIndex < len(repairer.confirmations) {
		confirmation := repairer.confirmations[repairer.confirmIndex]
		repairer.confirmIndex++
		return confirmation, nil
	}
	return repairer.confirmation, nil
}

// Probe returns configured facts or an available result for every requested port.
func (prober *primaryLeaseTestPortProber) Probe(_ context.Context, request identity.ProbeRequest) (identity.ProbeResult, error) {
	prober.calls = append(prober.calls, request)
	if err := prober.errs[request.Address]; err != nil {
		return identity.ProbeResult{}, err
	}
	if result, exists := prober.results[request.Address]; exists {
		return result, nil
	}
	result := identity.ProbeResult{Address: request.Address, Ports: make([]identity.PortProbe, 0, len(request.Ports))}
	for _, port := range request.Ports {
		result.Ports = append(result.Ports, identity.PortProbe{
			Port:      port,
			Available: true,
			Evidence:  "fixture exact-address port available",
		})
	}
	return result, nil
}

// primaryLeaseTestFixture collects the independent state, discovery, assignment, and socket boundaries.
type primaryLeaseTestFixture struct {
	state       *primaryLeaseTestState
	discoverer  *primaryLeaseTestDiscoverer
	loopback    *primaryLeaseTestLoopbackObserver
	ports       *primaryLeaseTestPortProber
	coordinator *projectPrimaryLeaseCoordinator
	now         time.Time
}

// newPrimaryLeaseTestFixture creates a valid identity-stage pool with every candidate pre-provisioned exactly.
func newPrimaryLeaseTestFixture(t *testing.T, candidates ...netip.Addr) *primaryLeaseTestFixture {
	t.Helper()
	pool, err := identity.NewPool(netip.MustParsePrefix("127.77.0.0/24"), candidates)
	if err != nil {
		t.Fatalf("create primary lease pool: %v", err)
	}
	createdAt := time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC)
	now := createdAt.Add(time.Hour)
	project := state.ProjectRecord{
		Project: domain.ProjectSnapshot{
			ID:        "project-orders",
			Name:      "Orders",
			Path:      "/test/orders",
			Slug:      "orders",
			State:     domain.ProjectStopped,
			UpdatedAt: createdAt,
			Apps:      []domain.AppSnapshot{},
			Services:  []domain.ServiceSnapshot{},
			Resources: []domain.ResourceSnapshot{},
		},
		Revision: 10,
	}
	network := state.NetworkRecord{
		Stage:       state.NetworkStageIdentity,
		Revision:    20,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt.Add(time.Minute),
		Ownership:   identity.Ownership{InstallationID: "harbor-test-installation", Generation: 7},
		Pool:        pool,
		Leases:      []identity.Lease{},
		Quarantines: []identity.Quarantine{},
		Reservations: state.DataPlaneReservations{
			Endpoints:            []state.EndpointReservation{},
			SuppressedProjectIDs: []domain.ProjectID{},
		},
	}
	observer := &primaryLeaseTestLoopbackObserver{
		facts: make(map[netip.Addr]loopback.Observation, len(candidates)),
		errs:  make(map[netip.Addr]error),
	}
	for _, address := range candidates {
		observer.facts[address] = primaryLeaseTestExactObservation(address)
	}
	discoverer := &primaryLeaseTestDiscoverer{port: 3000}
	ports := &primaryLeaseTestPortProber{
		results: make(map[netip.Addr]identity.ProbeResult),
		errs:    make(map[netip.Addr]error),
	}
	fixture := &primaryLeaseTestFixture{
		state:      &primaryLeaseTestState{project: project, network: network, initialized: true},
		discoverer: discoverer,
		loopback:   observer,
		ports:      ports,
		now:        now,
	}
	fixture.coordinator = newProjectPrimaryLeaseCoordinator(fixture.state, discoverer, observer, ports, func() time.Time { return now })
	return fixture
}

// TestProjectPrimaryLeaseCoordinatorAllocatesAndPersistsOneObservedDelta proves the complete unprivileged allocation path.
func TestProjectPrimaryLeaseCoordinatorAllocatesAndPersistsOneObservedDelta(t *testing.T) {
	address11 := netip.MustParseAddr("127.77.0.11")
	address12 := netip.MustParseAddr("127.77.0.12")
	address13 := netip.MustParseAddr("127.77.0.13")
	fixture := newPrimaryLeaseTestFixture(t, address13, address11, address12)
	fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, "project-billing", address11, fixture.state.network.Ownership)}
	fixture.state.network.Quarantines = []identity.Quarantine{{Address: address12, Reason: "recent release"}}

	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if admission.Lease.Address != address13 || admission.Target.Address != address13 || admission.Target.Port != 3000 ||
		admission.Project.Revision != fixture.state.project.Revision || admission.Project.Project.Path != fixture.state.project.Project.Path {
		t.Fatalf("Ensure() admission = %#v", admission)
	}
	if !slices.Equal(fixture.discoverer.calls, []netip.Addr{address13}) || admission.NetworkUpdatedAt != fixture.now {
		t.Fatalf("admission target/revision boundary = calls %v, updated %s", fixture.discoverer.calls, admission.NetworkUpdatedAt)
	}
	if !slices.Equal(fixture.loopback.calls, []netip.Addr{address13}) || len(fixture.ports.calls) != 1 || fixture.ports.calls[0].Address != address13 {
		t.Fatalf("host observations = loopback %v, ports %#v", fixture.loopback.calls, fixture.ports.calls)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("ReplaceProjectNetwork() calls = %d, want 1", len(fixture.state.replaceCalls))
	}
	request := fixture.state.replaceCalls[0]
	if request.ExpectedNetworkRevision != 20 || request.ExpectedProjectRevision != 10 ||
		len(request.Ensures) != 1 || len(request.Releases) != 0 || len(request.Endpoints) != 0 {
		t.Fatalf("replacement request = %#v", request)
	}
	ensure := request.Ensures[0]
	if ensure.Lease != admission.Lease || ensure.Generation != primaryLeaseInitialGeneration ||
		ensure.LeasedAt != fixture.now || !strings.HasPrefix(ensure.EnsureEvidence, "project-primary-lease-sha256:") {
		t.Fatalf("lease ensure = %#v", ensure)
	}
	if len(fixture.state.network.Leases) != 2 || fixture.state.network.Leases[0].Key.ProjectID != "project-billing" {
		t.Fatalf("persisted leases = %#v", fixture.state.network.Leases)
	}
}

// TestProjectPrimaryLeaseCoordinatorReplansAroundObservedCandidateConflicts proves unsafe addresses never become durable.
func TestProjectPrimaryLeaseCoordinatorReplansAroundObservedCandidateConflicts(t *testing.T) {
	address11 := netip.MustParseAddr("127.77.0.11")
	address12 := netip.MustParseAddr("127.77.0.12")
	tests := []struct {
		name       string
		configure  func(*primaryLeaseTestFixture)
		wantProbes []netip.Addr
	}{
		{
			name: "setup drift",
			configure: func(fixture *primaryLeaseTestFixture) {
				fixture.loopback.facts[address11] = primaryLeaseTestAbsentObservation(address11)
			},
			wantProbes: []netip.Addr{address12},
		},
		{
			name: "occupied App port",
			configure: func(fixture *primaryLeaseTestFixture) {
				fixture.ports.results[address11] = primaryLeaseTestProbeResult(address11, 3000, false)
			},
			wantProbes: []netip.Addr{address11, address12},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address11, address12)
			test.configure(fixture)
			admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
			if err != nil || admission.Lease.Address != address12 {
				t.Fatalf("Ensure() = %#v, %v, want %s", admission, err, address12)
			}
			gotProbes := make([]netip.Addr, 0, len(fixture.ports.calls))
			for _, request := range fixture.ports.calls {
				gotProbes = append(gotProbes, request.Address)
			}
			if !slices.Equal(gotProbes, test.wantProbes) {
				t.Fatalf("port probes = %v, want %v", gotProbes, test.wantProbes)
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorSettlesAnOwnedFreshCandidateBeforeReplanning proves a stale project listener does not force a new address.
func TestProjectPrimaryLeaseCoordinatorSettlesAnOwnedFreshCandidateBeforeReplanning(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, false)
	repairer := &primaryLeaseTestRuntimeRepairer{
		inspection: projectprocess.UnattributedRuntimeInspection{
			State:     projectprocess.RuntimeRepairInspectionActionable,
			Candidate: &projectprocess.UnattributedRuntimeCandidate{},
		},
		confirmation: projectprocess.RuntimeRepairConfirmation{
			State:    projectprocess.RuntimeRepairConfirmationSettled,
			Signaled: true,
		},
		onConfirm: func() {
			fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, true)
		},
	}
	fixture.coordinator.runtimeRepairer = repairer

	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease.Address != address {
		t.Fatalf("Ensure() = %#v, %v, want the repaired candidate address", admission, err)
	}
	if len(repairer.inspectTargets) != 1 || len(repairer.candidates) != 1 {
		t.Fatalf("fresh candidate cleanup calls = inspections %d candidates %d, want one each", len(repairer.inspectTargets), len(repairer.candidates))
	}
	if repairer.inspectTargets[0].CheckoutRoot != fixture.state.project.Project.Path ||
		repairer.inspectTargets[0].Endpoint != netip.MustParseAddrPort("127.77.0.11:3000") {
		t.Fatalf("fresh candidate repair target = %#v, want project checkout and exact listener", repairer.inspectTargets[0])
	}
	if len(fixture.ports.calls) != 2 {
		t.Fatalf("fresh candidate probes = %d, want initial and post-settlement probes", len(fixture.ports.calls))
	}
}

// TestProjectPrimaryLeaseCoordinatorReturnsRetainedLeaseWithoutHostMutation proves allocation never churns durable identity.
func TestProjectPrimaryLeaseCoordinatorReturnsRetainedLeaseWithoutHostMutation(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	retained := primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)
	fixture.state.network.Leases = []identity.Lease{retained}

	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease != retained || admission.Target.Address != address ||
		admission.Project.Revision != fixture.state.project.Revision || admission.Project.Project.Path != fixture.state.project.Project.Path {
		t.Fatalf("Ensure() = %#v, %v", admission, err)
	}
	if !slices.Equal(fixture.loopback.calls, []netip.Addr{address}) || len(fixture.ports.calls) != 1 || len(fixture.state.replaceCalls) != 0 {
		t.Fatalf("retained lease effects = loopback %v, probes %v, writes %v", fixture.loopback.calls, fixture.ports.calls, fixture.state.replaceCalls)
	}
}

// TestProjectPrimaryLeaseCoordinatorRejectsUnsafeRetainedLeaseWithoutReallocation keeps logical identity stable under host drift.
func TestProjectPrimaryLeaseCoordinatorRejectsUnsafeRetainedLeaseWithoutReallocation(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	tests := []struct {
		name      string
		configure func(*primaryLeaseTestFixture)
		want      string
		wantCode  domain.ProblemCode
	}{
		{
			name: "exact assignment missing",
			configure: func(fixture *primaryLeaseTestFixture) {
				fixture.loopback.facts[address] = primaryLeaseTestAbsentObservation(address)
			},
			want:     "host state \"absent\"",
			wantCode: "project.network.identity_unavailable",
		},
		{
			name: "App port occupied",
			configure: func(fixture *primaryLeaseTestFixture) {
				fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, false)
			},
			want:     "App port 3000 is unavailable",
			wantCode: "project.network.port_unavailable",
		},
		{
			name: "host observation fails",
			configure: func(fixture *primaryLeaseTestFixture) {
				fixture.loopback.errs[address] = errors.New("fixture retained observation failed")
			},
			want: "observe pre-provisioned",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			fixture.state.network.Leases = []identity.Lease{
				primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
			}
			test.configure(fixture)
			_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ensure() error = %v, want containing %q", err, test.want)
			}
			assertPrimaryLeaseTestRejection(t, err, test.wantCode)
			if len(fixture.state.replaceCalls) != 0 || len(fixture.loopback.calls) != 1 {
				t.Fatalf("unsafe retained lease effects = writes %d, observations %v", len(fixture.state.replaceCalls), fixture.loopback.calls)
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorSettlesAProjectOwnedListenerBeforeRejecting proves a same-project stale listener is an automatic retry edge.
func TestProjectPrimaryLeaseCoordinatorSettlesAProjectOwnedListenerBeforeRejecting(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{
		primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
	}
	fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, false)
	fixture.coordinator.runtimeRepairer = &primaryLeaseTestRuntimeRepairer{
		inspection: projectprocess.UnattributedRuntimeInspection{State: projectprocess.RuntimeRepairInspectionMissing},
		onInspect: func() {
			fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, true)
		},
	}

	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease.Address != address {
		t.Fatalf("Ensure() = %#v, %v, want retained lease after automatic settlement", admission, err)
	}
	if len(fixture.coordinator.runtimeRepairer.(*primaryLeaseTestRuntimeRepairer).inspectTargets) != 1 || len(fixture.ports.calls) != 2 {
		t.Fatalf("automatic cleanup observations = %#v, probes %d, want one inspection and two probes", fixture.coordinator.runtimeRepairer, len(fixture.ports.calls))
	}
}

// TestProjectPrimaryLeaseCoordinatorConfirmsAnExactProjectListener proves actionable same-user scope is signaled before retrying admission.
func TestProjectPrimaryLeaseCoordinatorConfirmsAnExactProjectListener(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{
		primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
	}
	fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, false)
	repairer := &primaryLeaseTestRuntimeRepairer{
		inspection: projectprocess.UnattributedRuntimeInspection{
			State:     projectprocess.RuntimeRepairInspectionActionable,
			Candidate: &projectprocess.UnattributedRuntimeCandidate{},
		},
		confirmation: projectprocess.RuntimeRepairConfirmation{
			State:    projectprocess.RuntimeRepairConfirmationSettled,
			Signaled: true,
		},
		onConfirm: func() {
			fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, true)
		},
	}
	fixture.coordinator.runtimeRepairer = repairer

	if _, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID); err != nil {
		t.Fatalf("Ensure() error = %v, want automatic exact-scope cleanup", err)
	}
	if len(repairer.inspectTargets) != 1 || repairer.inspectTargets[0].Endpoint != netip.MustParseAddrPort("127.77.0.11:3000") || len(repairer.candidates) != 1 {
		t.Fatalf("automatic cleanup calls = targets %#v candidates %d, want one exact target and candidate", repairer.inspectTargets, len(repairer.candidates))
	}
}

// TestProjectPrimaryLeaseCoordinatorRetriesTransientListenerDrift keeps a process race from surfacing as a user-visible port conflict.
func TestProjectPrimaryLeaseCoordinatorRetriesTransientListenerDrift(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{
		primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
	}
	fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, false)
	var repairer *primaryLeaseTestRuntimeRepairer
	repairer = &primaryLeaseTestRuntimeRepairer{
		inspections: []projectprocess.UnattributedRuntimeInspection{
			{
				State:     projectprocess.RuntimeRepairInspectionActionable,
				Candidate: &projectprocess.UnattributedRuntimeCandidate{},
			},
			{
				State:     projectprocess.RuntimeRepairInspectionActionable,
				Candidate: &projectprocess.UnattributedRuntimeCandidate{},
			},
		},
		confirmations: []projectprocess.RuntimeRepairConfirmation{
			{State: projectprocess.RuntimeRepairConfirmationDrifted},
			{State: projectprocess.RuntimeRepairConfirmationSettled, Signaled: true},
		},
		onConfirm: func() {
			if len(repairer.candidates) == 2 {
				fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, true)
			}
		},
	}
	fixture.coordinator.runtimeRepairer = repairer

	if _, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID); err != nil {
		t.Fatalf("Ensure() error = %v, want retry after transient listener drift", err)
	}
	if len(repairer.inspectTargets) != 2 || len(repairer.candidates) != 2 {
		t.Fatalf("automatic cleanup retries = inspections %d candidates %d, want two each", len(repairer.inspectTargets), len(repairer.candidates))
	}
}

// TestProjectPrimaryLeaseCoordinatorRequiresSignalProofForProcessBackedRecovery prevents a vanished listener from hiding an unresolved process scope.
func TestProjectPrimaryLeaseCoordinatorRequiresSignalProofForProcessBackedRecovery(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	repairer := &primaryLeaseTestRuntimeRepairer{
		inspection: projectprocess.UnattributedRuntimeInspection{
			State: projectprocess.RuntimeRepairInspectionMissing,
		},
	}
	fixture.coordinator.runtimeRepairer = repairer

	resolved, err := fixture.coordinator.repairAppPortConflict(t.Context(), "/test/orders", address, 3000, true)
	if err != nil {
		t.Fatalf("repairAppPortConflict() error = %v", err)
	}
	if resolved {
		t.Fatal("repairAppPortConflict() reported a vanished listener as signal-backed settlement")
	}
	if len(repairer.candidates) != 0 {
		t.Fatalf("repairAppPortConflict() signaled %d candidates after listener disappearance", len(repairer.candidates))
	}
}

// TestProjectPrimaryLeaseCoordinatorPreservesForeignListenerFailure proves ownership of an address never authorizes killing an unrelated process.
func TestProjectPrimaryLeaseCoordinatorPreservesForeignListenerFailure(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{
		primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
	}
	fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3000, false)
	repairer := &primaryLeaseTestRuntimeRepairer{
		inspection: projectprocess.UnattributedRuntimeInspection{State: projectprocess.RuntimeRepairInspectionForeign},
	}
	fixture.coordinator.runtimeRepairer = repairer

	_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err == nil || !strings.Contains(err.Error(), "App port 3000 is unavailable") {
		t.Fatalf("Ensure() error = %v, want retained foreign-listener rejection", err)
	}
	if len(repairer.candidates) != 0 {
		t.Fatalf("foreign listener cleanup signaled %d candidates", len(repairer.candidates))
	}
}

// TestProjectPrimaryLeaseCoordinatorBoundsOptimisticRevisionRaces proves persistent writer churn cannot loop forever.
func TestProjectPrimaryLeaseCoordinatorBoundsOptimisticRevisionRaces(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.replace = func(request state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
		return state.NetworkMutationResult{}, &state.NetworkRevisionConflictError{
			Expected: request.ExpectedNetworkRevision,
			Actual:   request.ExpectedNetworkRevision + 1,
		}
	}
	_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err == nil || !strings.Contains(err.Error(), "did not converge after 4 revisions") {
		t.Fatalf("Ensure() error = %v, want bounded convergence failure", err)
	}
	if len(fixture.state.replaceCalls) != primaryLeasePersistenceAttempts {
		t.Fatalf("replacement attempts = %d, want %d", len(fixture.state.replaceCalls), primaryLeasePersistenceAttempts)
	}
}

// TestProjectPrimaryLeaseCoordinatorRejectsPersistenceReadbackDrift proves storage cannot weaken admitted host facts.
func TestProjectPrimaryLeaseCoordinatorRejectsPersistenceReadbackDrift(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	cause := errors.New("fixture persistence failed")
	tests := []struct {
		name    string
		replace func(*primaryLeaseTestFixture, state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error)
		want    string
	}{
		{
			name: "write error",
			replace: func(_ *primaryLeaseTestFixture, _ state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
				return state.NetworkMutationResult{}, cause
			},
			want: cause.Error(),
		},
		{
			name: "invalid readback",
			replace: func(_ *primaryLeaseTestFixture, _ state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
				return state.NetworkMutationResult{}, nil
			},
			want: "validate persisted primary lease",
		},
		{
			name: "missing admitted lease",
			replace: func(fixture *primaryLeaseTestFixture, request state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
				record := fixture.state.network
				record.Revision++
				record.UpdatedAt = request.At
				return state.NetworkMutationResult{Record: record}, nil
			},
			want: "differs from its admitted identity",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			fixture.state.replace = func(request state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
				return test.replace(fixture, request)
			}
			_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ensure() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorUsesDurableTimeLowerBounds keeps lease facts causal when wall clocks lag.
func TestProjectPrimaryLeaseCoordinatorUsesDurableTimeLowerBounds(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	tests := []struct {
		name           string
		projectUpdated time.Time
		networkUpdated time.Time
		want           time.Time
	}{
		{
			name:           "project",
			projectUpdated: time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC),
			networkUpdated: time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC),
			want:           time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC),
		},
		{
			name:           "network",
			projectUpdated: time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC),
			networkUpdated: time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC),
			want:           time.Date(2026, time.July, 19, 11, 0, 0, 0, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			fixture.state.project.Project.UpdatedAt = test.projectUpdated
			fixture.state.network.UpdatedAt = test.networkUpdated
			fixture.coordinator.now = func() time.Time {
				return time.Date(2026, time.July, 19, 7, 0, 0, 0, time.UTC)
			}
			if _, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID); err != nil {
				t.Fatalf("Ensure() error = %v", err)
			}
			if got := fixture.state.replaceCalls[0].At; got != test.want {
				t.Fatalf("replacement time = %s, want %s", got, test.want)
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorCreatesDefaultHTTPForNewFullStageLease proves lease and route authority are committed together.
func TestProjectPrimaryLeaseCoordinatorCreatesDefaultHTTPForNewFullStageLease(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	primaryLeaseTestEnableFullStage(t, fixture)

	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease.Address != address || fixture.state.network.Stage != state.NetworkStageFull {
		t.Fatalf("Ensure(full stage) = %#v, %v, network %#v", admission, err, fixture.state.network)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("full-stage replacement = %#v", fixture.state.replaceCalls)
	}
	want := primaryLeaseTestDefaultHTTPEndpoint(fixture, primaryLeaseDefaultHTTPEndpointInitialGeneration)
	request := fixture.state.replaceCalls[0]
	if len(request.Ensures) != 1 || len(request.Releases) != 0 || !slices.Equal(request.Endpoints, []state.EndpointReservation{want}) {
		t.Fatalf("full-stage replacement = %#v, want one lease ensure and endpoint %#v", request, want)
	}
	if !slices.Equal(fixture.state.network.Reservations.Endpoints, []state.EndpointReservation{want}) {
		t.Fatalf("persisted endpoints = %#v, want %#v", fixture.state.network.Reservations.Endpoints, want)
	}
}

// TestProjectPrimaryLeaseCoordinatorAddsAndRetainsDefaultHTTPForRetainedLease proves activation is endpoint-only and idempotent.
func TestProjectPrimaryLeaseCoordinatorAddsAndRetainsDefaultHTTPForRetainedLease(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	retained := primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)
	fixture.state.network.Leases = []identity.Lease{retained}
	primaryLeaseTestEnableFullStage(t, fixture)
	custom := state.EndpointReservation{
		Key: state.EndpointReservationKey{
			ProjectID:  fixture.state.project.Project.ID,
			EndpointID: "status",
		},
		Protocol:   state.EndpointProtocolHTTP,
		Host:       "status.orders.test",
		Public:     fixture.state.network.Reservations.Listeners.HTTPS.Advertised,
		Generation: 4,
	}
	fixture.state.network.Reservations.Endpoints = []state.EndpointReservation{custom}
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("retained full-stage fixture Validate() error = %v", err)
	}

	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease != retained || admission.NetworkUpdatedAt != fixture.now {
		t.Fatalf("Ensure(retained full stage) = %#v, %v", admission, err)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("ReplaceProjectNetwork() calls = %d, want 1", len(fixture.state.replaceCalls))
	}
	wantDefault := primaryLeaseTestDefaultHTTPEndpoint(fixture, primaryLeaseDefaultHTTPEndpointInitialGeneration)
	wantEndpoints := []state.EndpointReservation{wantDefault, custom}
	request := fixture.state.replaceCalls[0]
	if len(request.Ensures) != 0 || len(request.Releases) != 0 || !slices.Equal(request.Endpoints, wantEndpoints) {
		t.Fatalf("retained replacement = %#v, want endpoint-only write %#v", request, wantEndpoints)
	}
	if !slices.Equal(fixture.state.network.Reservations.Endpoints, wantEndpoints) {
		t.Fatalf("persisted endpoints = %#v, want %#v", fixture.state.network.Reservations.Endpoints, wantEndpoints)
	}

	restarted := newProjectPrimaryLeaseCoordinator(
		fixture.state,
		fixture.discoverer,
		fixture.loopback,
		fixture.ports,
		func() time.Time { return fixture.now.Add(time.Minute) },
	)
	if _, err := restarted.Ensure(t.Context(), fixture.state.project.Project.ID); err != nil {
		t.Fatalf("Ensure(after restart) error = %v", err)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("ReplaceProjectNetwork() calls after restart = %d, want 1", len(fixture.state.replaceCalls))
	}
	if !slices.Equal(fixture.state.network.Reservations.Endpoints, wantEndpoints) {
		t.Fatalf("endpoints after restart = %#v, want stable generations %#v", fixture.state.network.Reservations.Endpoints, wantEndpoints)
	}
}

// TestProjectPrimaryLeaseCoordinatorReconcilesThreeDefaultHTTPReservationsWithoutHostObservation proves promotion is endpoint-only and idempotent.
func TestProjectPrimaryLeaseCoordinatorReconcilesThreeDefaultHTTPReservationsWithoutHostObservation(t *testing.T) {
	addresses := []netip.Addr{
		netip.MustParseAddr("127.77.0.11"),
		netip.MustParseAddr("127.77.0.12"),
		netip.MustParseAddr("127.77.0.13"),
	}
	fixture := newPrimaryLeaseTestFixture(t, addresses...)
	projects := []state.ProjectRecord{
		{Project: fixture.state.project.Project, Revision: 10},
		{Project: fixture.state.project.Project, Revision: 11},
		{Project: fixture.state.project.Project, Revision: 12},
	}
	identities := []struct {
		id   domain.ProjectID
		name string
		slug string
		path string
	}{
		{id: "project-alpha", name: "Alpha", slug: "alpha", path: "/test/alpha"},
		{id: "project-bravo", name: "Bravo", slug: "bravo", path: "/test/bravo"},
		{id: "project-charlie", name: "Charlie", slug: "charlie", path: "/test/charlie"},
	}
	fixture.state.projects = make(map[domain.ProjectID]state.ProjectRecord, len(projects))
	fixture.state.network.Leases = make([]identity.Lease, 0, len(projects))
	for index := range projects {
		projects[index].Project.ID = identities[index].id
		projects[index].Project.Name = identities[index].name
		projects[index].Project.Slug = identities[index].slug
		projects[index].Project.Path = identities[index].path
		fixture.state.projects[identities[index].id] = projects[index]
		fixture.state.network.Leases = append(
			fixture.state.network.Leases,
			primaryLeaseTestLease(t, identities[index].id, addresses[index], fixture.state.network.Ownership),
		)
	}
	slices.SortFunc(fixture.state.network.Leases, primaryLeaseTestLeaseCompare)
	primaryLeaseTestEnableFullStage(t, fixture)

	tcpEndpoints := make([]state.EndpointReservation, 0, len(projects))
	for index, lease := range fixture.state.network.Leases {
		leaseKey := lease.Key
		tcpEndpoints = append(tcpEndpoints, state.EndpointReservation{
			Key: state.EndpointReservationKey{
				ProjectID:  lease.Key.ProjectID,
				EndpointID: primaryLeaseServiceEndpointIDPrefix + "mysql",
			},
			Protocol:   state.EndpointProtocolTCP,
			Host:       "mysql." + projects[index].Project.Slug + ".test",
			Public:     netip.AddrPortFrom(lease.Address, 3306),
			Identity:   &leaseKey,
			Generation: uint64(index + 4),
		})
	}
	slices.SortFunc(tcpEndpoints, primaryLeaseEndpointCompare)
	fixture.state.network.Reservations.Endpoints = slices.Clone(tcpEndpoints)
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("three-project full-stage fixture Validate() error = %v", err)
	}

	observationFailure := errors.New("endpoint-only reconciliation reached a host observation")
	fixture.discoverer.err = observationFailure
	for _, address := range addresses {
		fixture.loopback.errs[address] = observationFailure
		fixture.ports.errs[address] = observationFailure
	}
	repairer := &primaryLeaseTestRuntimeRepairer{inspectErr: observationFailure, confirmErr: observationFailure}
	fixture.coordinator.runtimeRepairer = repairer
	lifecycle := &ProjectLifecycleCoordinator{primaryLeases: fixture.coordinator}

	final, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(t.Context())
	if err != nil {
		t.Fatalf("ReconcileFullStageDefaultHTTPEndpoints() error = %v", err)
	}
	if final.Revision != 23 || fixture.state.network.Revision != 23 {
		t.Fatalf("final network revisions = returned %d, durable %d; want 23", final.Revision, fixture.state.network.Revision)
	}
	if len(fixture.state.replaceCalls) != len(projects) {
		t.Fatalf("endpoint replacement calls = %d, want %d", len(fixture.state.replaceCalls), len(projects))
	}
	if len(final.Reservations.Endpoints) != len(projects)*2 {
		t.Fatalf("final endpoints = %#v, want one TCP and one HTTP reservation per project", final.Reservations.Endpoints)
	}
	for index, project := range projects {
		request := fixture.state.replaceCalls[index]
		if request.ProjectID != project.Project.ID || request.ExpectedNetworkRevision != domain.Sequence(20+index) ||
			len(request.Ensures) != 0 || len(request.Releases) != 0 {
			t.Fatalf("endpoint-only replacement %d = %#v", index, request)
		}
		var gotTCP *state.EndpointReservation
		var gotHTTP *state.EndpointReservation
		for endpointIndex := range final.Reservations.Endpoints {
			endpoint := &final.Reservations.Endpoints[endpointIndex]
			if endpoint.Key.ProjectID != project.Project.ID {
				continue
			}
			switch endpoint.Key.EndpointID {
			case primaryLeaseServiceEndpointIDPrefix + "mysql":
				gotTCP = endpoint
			case primaryLeaseDefaultHTTPEndpointID:
				gotHTTP = endpoint
			}
		}
		wantTCP := tcpEndpoints[index]
		if gotTCP == nil || !reflect.DeepEqual(*gotTCP, wantTCP) {
			t.Fatalf("project %q TCP endpoint = %#v, want unchanged %#v", project.Project.ID, gotTCP, wantTCP)
		}
		if gotHTTP == nil || gotHTTP.Protocol != state.EndpointProtocolHTTP ||
			gotHTTP.Host != project.Project.Slug+".test" ||
			gotHTTP.Public != fixture.state.network.Reservations.Listeners.HTTPS.Advertised ||
			gotHTTP.Generation != primaryLeaseDefaultHTTPEndpointInitialGeneration {
			t.Fatalf("project %q default HTTP endpoint = %#v", project.Project.ID, gotHTTP)
		}
	}

	replayed, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(t.Context())
	if err != nil {
		t.Fatalf("replayed ReconcileFullStageDefaultHTTPEndpoints() error = %v", err)
	}
	if replayed.Revision != final.Revision || len(fixture.state.replaceCalls) != len(projects) {
		t.Fatalf("replayed reconciliation = revision %d and %d writes, want revision %d and %d writes", replayed.Revision, len(fixture.state.replaceCalls), final.Revision, len(projects))
	}
	if len(fixture.discoverer.calls) != 0 || len(fixture.loopback.calls) != 0 || len(fixture.ports.calls) != 0 ||
		len(repairer.inspectTargets) != 0 || len(repairer.candidates) != 0 {
		t.Fatalf(
			"endpoint reconciliation observations = discovery %v, loopback %v, ports %#v, repair inspect %#v, repair confirm %#v",
			fixture.discoverer.calls,
			fixture.loopback.calls,
			fixture.ports.calls,
			repairer.inspectTargets,
			repairer.candidates,
		)
	}
}

// TestProjectPrimaryLeaseCoordinatorDefaultHTTPReconciliationSkipsNonPublishableProjects covers pre-full and teardown boundaries.
func TestProjectPrimaryLeaseCoordinatorDefaultHTTPReconciliationSkipsNonPublishableProjects(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{
		primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
	}
	lifecycle := &ProjectLifecycleCoordinator{primaryLeases: fixture.coordinator}

	identityRecord, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(t.Context())
	if err != nil || identityRecord.Revision != fixture.state.network.Revision || len(fixture.state.replaceCalls) != 0 {
		t.Fatalf("identity-stage reconciliation = %#v, %v, writes %d", identityRecord, err, len(fixture.state.replaceCalls))
	}

	primaryLeaseTestEnableFullStage(t, fixture)
	fixture.state.network.Reservations.SuppressedProjectIDs = []domain.ProjectID{fixture.state.project.Project.ID}
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("suppressed full-stage fixture Validate() error = %v", err)
	}
	suppressedRecord, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(t.Context())
	if err != nil || suppressedRecord.Revision != fixture.state.network.Revision || len(fixture.state.replaceCalls) != 0 {
		t.Fatalf("suppressed reconciliation = %#v, %v, writes %d", suppressedRecord, err, len(fixture.state.replaceCalls))
	}
}

// TestProjectPrimaryLeaseCoordinatorBoundsDefaultHTTPReconciliationRaces proves endpoint-only retries cannot spin indefinitely.
func TestProjectPrimaryLeaseCoordinatorBoundsDefaultHTTPReconciliationRaces(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{
		primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
	}
	primaryLeaseTestEnableFullStage(t, fixture)
	fixture.state.replace = func(request state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
		return state.NetworkMutationResult{}, &state.NetworkRevisionConflictError{
			Expected: request.ExpectedNetworkRevision,
			Actual:   request.ExpectedNetworkRevision + 1,
		}
	}
	lifecycle := &ProjectLifecycleCoordinator{primaryLeases: fixture.coordinator}

	_, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(t.Context())
	if err == nil || !strings.Contains(err.Error(), "did not converge after 4 revisions") {
		t.Fatalf("revision-raced reconciliation error = %v", err)
	}
	if len(fixture.state.replaceCalls) != primaryLeasePersistenceAttempts {
		t.Fatalf("revision-raced replacement calls = %d, want %d", len(fixture.state.replaceCalls), primaryLeasePersistenceAttempts)
	}
	if len(fixture.discoverer.calls) != 0 || len(fixture.loopback.calls) != 0 || len(fixture.ports.calls) != 0 {
		t.Fatalf("revision-raced host observations = discovery %v, loopback %v, ports %#v", fixture.discoverer.calls, fixture.loopback.calls, fixture.ports.calls)
	}
}

// TestProjectPrimaryLeaseCoordinatorAssignsDescriptorResources proves ready resource facts become exact loopback HTTP reservations.
func TestProjectPrimaryLeaseCoordinatorAssignsDescriptorResources(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.project.Project.State = domain.ProjectStarting
	fixture.state.project.Project.Apps = []domain.AppSnapshot{{
		ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true,
	}}
	fixture.state.project.Project.Services = []domain.ServiceSnapshot{{
		ID: "mailpit", Name: "Mailpit", Kind: "compose", State: domain.EntityReady,
		Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
	}}
	fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
	primaryLeaseTestEnableFullStage(t, fixture)
	fixture.state.network.Reservations.Endpoints = []state.EndpointReservation{
		{
			Key:      state.EndpointReservationKey{ProjectID: fixture.state.project.Project.ID, EndpointID: "api-reference"},
			Protocol: state.EndpointProtocolHTTP, Host: "api-reference.orders.test",
			Public: fixture.state.network.Reservations.Listeners.HTTPS.Advertised, Generation: 5,
		},
		{
			Key:      state.EndpointReservationKey{ProjectID: fixture.state.project.Project.ID, EndpointID: "legacy"},
			Protocol: state.EndpointProtocolHTTP, Host: "legacy.orders.test",
			Public: fixture.state.network.Reservations.Listeners.HTTPS.Advertised, Generation: 2,
		},
	}
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("assignment fixture Validate() error = %v", err)
	}
	resources := []domain.ResourceSnapshot{
		{ID: "app-http", Name: "App", Kind: "application", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000"},
		{ID: "api-reference", Name: "API Reference", Kind: "docs", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000/swagger"},
		{ID: "mailpit", Name: "Mailpit", Kind: "mail", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByService, ServiceID: "mailpit"}, URL: "http://127.77.0.11:8025/"},
	}
	if err := fixture.coordinator.assignHTTPResourceEndpoints(t.Context(), fixture.state.project.Project.ID, resources); err != nil {
		t.Fatalf("assignHTTPResourceEndpoints() error = %v", err)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("endpoint assignment writes = %d, want 1", len(fixture.state.replaceCalls))
	}
	endpoints := fixture.state.network.Reservations.Endpoints
	if len(endpoints) != 3 {
		t.Fatalf("assigned endpoints = %#v, want app-http and two descriptor resources", endpoints)
	}
	byID := make(map[string]state.EndpointReservation, len(endpoints))
	for _, endpoint := range endpoints {
		byID[endpoint.Key.EndpointID] = endpoint
	}
	if byID["app-http"].Host != "orders.test" || byID["api-reference"].Host != "api-reference.orders.test" || byID["mailpit"].Host != "mailpit.orders.test" {
		t.Fatalf("assigned endpoint hosts = %#v", byID)
	}
	if byID["api-reference"].Generation != 5 || byID["mailpit"].Generation != 1 {
		t.Fatalf("assigned endpoint generations = %#v", byID)
	}
	if _, exists := byID["legacy"]; exists {
		t.Fatalf("stale optional endpoint survived assignment: %#v", endpoints)
	}
}

// TestProjectPrimaryLeaseCoordinatorAssignsSelectedNativeServiceEndpoints proves static service intent becomes stable public authority without a relay.
func TestProjectPrimaryLeaseCoordinatorAssignsSelectedNativeServiceEndpoints(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	primary := primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)
	fixture.state.network.Leases = []identity.Lease{primary}
	primaryLeaseTestEnableFullStage(t, fixture)
	fixture.state.network.Reservations.Endpoints = []state.EndpointReservation{
		primaryLeaseTestDefaultHTTPEndpoint(fixture, primaryLeaseDefaultHTTPEndpointInitialGeneration),
		{
			Key:      state.EndpointReservationKey{ProjectID: fixture.state.project.Project.ID, EndpointID: "service:stale"},
			Protocol: state.EndpointProtocolTCP,
			Host:     "stale.orders.test", Public: netip.AddrPortFrom(address, 43100), Identity: &primary.Key, Generation: 4,
		},
		{
			Key:      state.EndpointReservationKey{ProjectID: fixture.state.project.Project.ID, EndpointID: "custom"},
			Protocol: state.EndpointProtocolHTTP,
			Host:     "custom.orders.test", Public: fixture.state.network.Reservations.Listeners.HTTPS.Advertised, Generation: 2,
		},
	}
	slices.SortFunc(fixture.state.network.Reservations.Endpoints, primaryLeaseEndpointCompare)
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("native endpoint fixture Validate() error = %v", err)
	}
	requirements := []goforj.ServiceRequirement{
		{
			ID: "requirement.database.primary", ServiceKey: "mysql", Owner: goforj.ServiceRequirementOwnerCompose, Lifecycle: goforj.ServiceRequirementLifecycleProject,
			Endpoints: []goforj.ServiceEndpoint{{ID: "endpoint.database.primary.tcp", Protocol: goforj.ServiceEndpointProtocolTCP, NativePort: 3306, Visibility: goforj.ServiceEndpointVisibilityHost}},
		},
		{
			ID: "requirement.cache.primary", ServiceKey: "redis", Owner: goforj.ServiceRequirementOwnerCompose, Lifecycle: goforj.ServiceRequirementLifecycleProject,
			Endpoints: []goforj.ServiceEndpoint{{ID: "endpoint.cache.primary.tcp", Protocol: goforj.ServiceEndpointProtocolTCP, NativePort: 6379, Visibility: goforj.ServiceEndpointVisibilityHost}},
		},
	}
	if err := fixture.coordinator.assignServiceEndpointReservations(t.Context(), fixture.state.project.Project.ID, requirements); err != nil {
		t.Fatalf("assignServiceEndpointReservations() error = %v", err)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("service endpoint assignment writes = %d, want 1", len(fixture.state.replaceCalls))
	}
	byID := make(map[string]state.EndpointReservation, len(fixture.state.network.Reservations.Endpoints))
	for _, endpoint := range fixture.state.network.Reservations.Endpoints {
		byID[endpoint.Key.EndpointID] = endpoint
	}
	for _, want := range []struct {
		id     string
		host   string
		public netip.AddrPort
		gen    uint64
	}{
		{id: primaryLeaseServiceEndpointIDPrefix + "endpoint.database.primary.tcp", host: "mysql.orders.test", public: netip.AddrPortFrom(address, 3306), gen: 1},
		{id: primaryLeaseServiceEndpointIDPrefix + "endpoint.cache.primary.tcp", host: "redis.orders.test", public: netip.AddrPortFrom(address, 6379), gen: 1},
	} {
		got, exists := byID[want.id]
		if !exists || got.Protocol != state.EndpointProtocolTCP || got.Host != want.host || got.Public != want.public || got.Identity == nil || *got.Identity != primary.Key || got.Generation != want.gen {
			t.Fatalf("service endpoint %q = %#v, want host %q public %s generation %d", want.id, got, want.host, want.public, want.gen)
		}
	}
	if _, exists := byID["service:stale"]; exists {
		t.Fatalf("stale Harbor service endpoint survived assignment: %#v", byID)
	}
	if _, exists := byID["custom"]; !exists {
		t.Fatalf("unmanaged endpoint was removed: %#v", byID)
	}

	if err := fixture.coordinator.assignServiceEndpointReservations(t.Context(), fixture.state.project.Project.ID, requirements); err != nil {
		t.Fatalf("repeat assignServiceEndpointReservations() error = %v", err)
	}
	if len(fixture.state.replaceCalls) != 1 {
		t.Fatalf("repeat service endpoint assignment writes = %d, want idempotent 1", len(fixture.state.replaceCalls))
	}
}

// TestProjectPrimaryLeaseCoordinatorSkipsPrivateAndAvailableServiceEndpoints keeps non-host intent from becoming public authority.
func TestProjectPrimaryLeaseCoordinatorSkipsPrivateAndAvailableServiceEndpoints(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
	primaryLeaseTestEnableFullStage(t, fixture)
	requirements := []goforj.ServiceRequirement{
		{ID: "available", ServiceKey: "mysql", Owner: goforj.ServiceRequirementOwnerAvailable, Lifecycle: goforj.ServiceRequirementLifecycleProject, Endpoints: []goforj.ServiceEndpoint{{ID: "available.endpoint", Protocol: goforj.ServiceEndpointProtocolTCP, NativePort: 3306, Visibility: goforj.ServiceEndpointVisibilityHost}}},
		{ID: "private", ServiceKey: "redis", Owner: goforj.ServiceRequirementOwnerCompose, Lifecycle: goforj.ServiceRequirementLifecycleProject, Endpoints: []goforj.ServiceEndpoint{{ID: "private.endpoint", Protocol: goforj.ServiceEndpointProtocolTCP, NativePort: 6379, Visibility: goforj.ServiceEndpointVisibilityPrivate}}},
	}
	if err := fixture.coordinator.assignServiceEndpointReservations(t.Context(), fixture.state.project.Project.ID, requirements); err != nil {
		t.Fatalf("assignServiceEndpointReservations() error = %v", err)
	}
	if len(fixture.state.replaceCalls) != 0 || len(fixture.state.network.Reservations.Endpoints) != 0 {
		t.Fatalf("non-host service intent changed durable authority: writes %d endpoints %#v", len(fixture.state.replaceCalls), fixture.state.network.Reservations.Endpoints)
	}
}

// TestProjectPrimaryLeaseCoordinatorRejectsUnsafeNativeServiceEndpointIntent keeps unsupported publication shapes fail-closed.
func TestProjectPrimaryLeaseCoordinatorRejectsUnsafeNativeServiceEndpointIntent(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	tests := []struct {
		name       string
		serviceKey string
		protocol   goforj.ServiceEndpointProtocol
		port       int
		want       string
	}{
		{name: "invalid service host label", serviceKey: "MySQL", protocol: goforj.ServiceEndpointProtocolTCP, port: 3306, want: "lowercase DNS label"},
		{name: "host HTTP before managed session", serviceKey: "mailpit", protocol: goforj.ServiceEndpointProtocolHTTP, port: 8025, want: "cannot be host-published"},
		{name: "native port out of range", serviceKey: "mysql", protocol: goforj.ServiceEndpointProtocolTCP, port: 65536, want: "outside 1-65535"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
			primaryLeaseTestEnableFullStage(t, fixture)
			requirements := []goforj.ServiceRequirement{{
				ID: "requirement", ServiceKey: test.serviceKey, Owner: goforj.ServiceRequirementOwnerCompose, Lifecycle: goforj.ServiceRequirementLifecycleProject,
				Endpoints: []goforj.ServiceEndpoint{{ID: "endpoint", Protocol: test.protocol, NativePort: test.port, Visibility: goforj.ServiceEndpointVisibilityHost}},
			}}
			if err := fixture.coordinator.assignServiceEndpointReservations(t.Context(), fixture.state.project.Project.ID, requirements); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("assignServiceEndpointReservations() error = %v, want containing %q", err, test.want)
			}
			if len(fixture.state.replaceCalls) != 0 {
				t.Fatalf("unsafe service endpoint intent wrote durable state: %#v", fixture.state.replaceCalls)
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorRejectsNativeServiceHostCollision keeps one DNS name tied to one endpoint owner.
func TestProjectPrimaryLeaseCoordinatorRejectsNativeServiceHostCollision(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
	primaryLeaseTestEnableFullStage(t, fixture)
	requirements := []goforj.ServiceRequirement{
		{ID: "first", ServiceKey: "mysql", Owner: goforj.ServiceRequirementOwnerCompose, Lifecycle: goforj.ServiceRequirementLifecycleProject, Endpoints: []goforj.ServiceEndpoint{{ID: "first.endpoint", Protocol: goforj.ServiceEndpointProtocolTCP, NativePort: 3306, Visibility: goforj.ServiceEndpointVisibilityHost}}},
		{ID: "second", ServiceKey: "mysql", Owner: goforj.ServiceRequirementOwnerExternal, Lifecycle: goforj.ServiceRequirementLifecycleProject, Endpoints: []goforj.ServiceEndpoint{{ID: "second.endpoint", Protocol: goforj.ServiceEndpointProtocolTCP, NativePort: 3307, Visibility: goforj.ServiceEndpointVisibilityHost}}},
	}
	if err := fixture.coordinator.assignServiceEndpointReservations(t.Context(), fixture.state.project.Project.ID, requirements); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("assignServiceEndpointReservations() error = %v, want host collision", err)
	}
	if len(fixture.state.replaceCalls) != 0 {
		t.Fatalf("host collision wrote durable state: %#v", fixture.state.replaceCalls)
	}
}

// TestProjectPrimaryLeaseCoordinatorRejectsDescriptorResourceKeyCollision keeps HTTP intent from replacing native endpoint authority.
func TestProjectPrimaryLeaseCoordinatorRejectsDescriptorResourceKeyCollision(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.project.Project.State = domain.ProjectStarting
	fixture.state.project.Project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true}}
	primary := primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)
	fixture.state.network.Leases = []identity.Lease{primary}
	primaryLeaseTestEnableFullStage(t, fixture)
	fixture.state.network.Reservations.Endpoints = []state.EndpointReservation{{
		Key:      state.EndpointReservationKey{ProjectID: fixture.state.project.Project.ID, EndpointID: "api-reference"},
		Protocol: state.EndpointProtocolTCP, Host: "api-reference.orders.test",
		Public: netip.AddrPortFrom(address, 3306), Identity: &primary.Key, Generation: 1,
	}}
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("key collision fixture Validate() error = %v", err)
	}
	resources := []domain.ResourceSnapshot{
		{ID: "app-http", Name: "App", Kind: "application", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000"},
		{ID: "api-reference", Name: "API Reference", Kind: "docs", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000/swagger"},
	}
	if err := fixture.coordinator.assignHTTPResourceEndpoints(t.Context(), fixture.state.project.Project.ID, resources); err == nil || !strings.Contains(err.Error(), "conflicts with preserved TCP endpoint") {
		t.Fatalf("assignHTTPResourceEndpoints() error = %v, want TCP key collision", err)
	}
	if len(fixture.state.replaceCalls) != 0 {
		t.Fatalf("key collision wrote durable state: %#v", fixture.state.replaceCalls)
	}
}

// TestProjectPrimaryLeaseCoordinatorRejectsUnsafeDescriptorResourceEndpoint keeps endpoint assignment fail-closed.
func TestProjectPrimaryLeaseCoordinatorRejectsUnsafeDescriptorResourceEndpoint(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	baseResources := func() []domain.ResourceSnapshot {
		return []domain.ResourceSnapshot{
			{ID: "app-http", Name: "App", Kind: "application", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000"},
			{ID: "api-reference", Name: "API Reference", Kind: "docs", Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000/swagger"},
		}
	}
	tests := []struct {
		name   string
		mutate func([]domain.ResourceSnapshot) []domain.ResourceSnapshot
		want   string
	}{
		{name: "missing app", mutate: func(resources []domain.ResourceSnapshot) []domain.ResourceSnapshot { return resources[1:] }, want: "required \"app-http\" resource"},
		{name: "foreign address", mutate: func(resources []domain.ResourceSnapshot) []domain.ResourceSnapshot {
			resources[1].URL = "http://127.77.0.12:3000/swagger"
			return resources
		}, want: "assigned IPv4 loopback address"},
		{name: "mapped address", mutate: func(resources []domain.ResourceSnapshot) []domain.ResourceSnapshot {
			resources[1].URL = "http://[::ffff:127.77.0.11]:3000/swagger"
			return resources
		}, want: "assigned IPv4 loopback address"},
		{name: "non-DNS ID", mutate: func(resources []domain.ResourceSnapshot) []domain.ResourceSnapshot {
			resources[1].ID = "api.reference"
			return resources
		}, want: "lowercase DNS label"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			fixture.state.project.Project.State = domain.ProjectStarting
			fixture.state.project.Project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true}}
			fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
			primaryLeaseTestEnableFullStage(t, fixture)
			if err := fixture.coordinator.assignHTTPResourceEndpoints(t.Context(), fixture.state.project.Project.ID, test.mutate(baseResources())); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("assignHTTPResourceEndpoints() error = %v, want containing %q", err, test.want)
			}
			if len(fixture.state.replaceCalls) != 0 {
				t.Fatalf("unsafe endpoint assignment wrote durable state: %#v", fixture.state.replaceCalls)
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorDefersDescriptorEndpointsBeforeFullStage preserves the non-publishable network boundary.
func TestProjectPrimaryLeaseCoordinatorDefersDescriptorEndpointsBeforeFullStage(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	resources := []domain.ResourceSnapshot{{
		ID: "app-http", Name: "App", Kind: "application",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000",
	}}
	if err := fixture.coordinator.assignHTTPResourceEndpoints(t.Context(), fixture.state.project.Project.ID, resources); err != nil {
		t.Fatalf("assignHTTPResourceEndpoints(identity stage) error = %v", err)
	}
	if len(fixture.state.replaceCalls) != 0 || len(fixture.state.network.Reservations.Endpoints) != 0 {
		t.Fatalf("identity-stage endpoint effects = writes %d, endpoints %#v", len(fixture.state.replaceCalls), fixture.state.network.Reservations.Endpoints)
	}
}

// TestProjectPrimaryLeaseCoordinatorBoundsDescriptorEndpointRevisionRaces prevents endpoint assignment from looping on hostile writer churn.
func TestProjectPrimaryLeaseCoordinatorBoundsDescriptorEndpointRevisionRaces(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.project.Project.State = domain.ProjectStarting
	fixture.state.project.Project.Apps = []domain.AppSnapshot{{ID: "app", Name: "App", State: domain.EntityReady, Active: true, Required: true}}
	fixture.state.network.Leases = []identity.Lease{primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership)}
	primaryLeaseTestEnableFullStage(t, fixture)
	fixture.state.replace = func(request state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
		return state.NetworkMutationResult{}, &state.NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: request.ExpectedNetworkRevision + 1}
	}
	resources := []domain.ResourceSnapshot{{
		ID: "app-http", Name: "App", Kind: "application",
		Owner: domain.ResourceOwner{Kind: domain.ResourceOwnedByApp, AppID: "app"}, URL: "http://127.77.0.11:3000",
	}}
	err := fixture.coordinator.assignHTTPResourceEndpoints(t.Context(), fixture.state.project.Project.ID, resources)
	if err == nil || !strings.Contains(err.Error(), "did not converge after 4 revisions") {
		t.Fatalf("AssignHTTPResourceEndpoints() error = %v, want bounded convergence failure", err)
	}
	if len(fixture.state.replaceCalls) != primaryLeasePersistenceAttempts {
		t.Fatalf("endpoint assignment attempts = %d, want %d", len(fixture.state.replaceCalls), primaryLeasePersistenceAttempts)
	}
}

// TestProjectPrimaryLeaseCoordinatorLeavesIdentityStageEndpointFree proves endpoint activation waits for ingress authority.
func TestProjectPrimaryLeaseCoordinatorLeavesIdentityStageEndpointFree(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	for _, retained := range []bool{false, true} {
		name := "new lease"
		if retained {
			name = "retained lease"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			if retained {
				fixture.state.network.Leases = []identity.Lease{
					primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
				}
			}

			if _, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID); err != nil {
				t.Fatalf("Ensure(identity stage) error = %v", err)
			}
			if len(fixture.state.network.Reservations.Endpoints) != 0 {
				t.Fatalf("identity-stage endpoints = %#v, want none", fixture.state.network.Reservations.Endpoints)
			}
			if retained {
				if len(fixture.state.replaceCalls) != 0 {
					t.Fatalf("retained identity-stage writes = %d, want none", len(fixture.state.replaceCalls))
				}
				return
			}
			if len(fixture.state.replaceCalls) != 1 || len(fixture.state.replaceCalls[0].Endpoints) != 0 {
				t.Fatalf("new identity-stage replacement = %#v, want no endpoints", fixture.state.replaceCalls)
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorRejectsConflictingDefaultHTTPAuthority proves activation never replaces an established route shape.
func TestProjectPrimaryLeaseCoordinatorRejectsConflictingDefaultHTTPAuthority(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	fixture.state.network.Leases = []identity.Lease{
		primaryLeaseTestLease(t, fixture.state.project.Project.ID, address, fixture.state.network.Ownership),
	}
	primaryLeaseTestEnableFullStage(t, fixture)
	fixture.state.network.Reservations.Endpoints = []state.EndpointReservation{{
		Key: state.EndpointReservationKey{
			ProjectID:  fixture.state.project.Project.ID,
			EndpointID: primaryLeaseDefaultHTTPEndpointID,
		},
		Protocol:   state.EndpointProtocolHTTP,
		Host:       "legacy.orders.test",
		Public:     fixture.state.network.Reservations.Listeners.HTTPS.Advertised,
		Generation: 7,
	}}
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("conflicting full-stage fixture Validate() error = %v", err)
	}

	_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	var conflict *state.NetworkProjectReplacementConflictError
	if !errors.As(err, &conflict) || conflict.ProjectID != fixture.state.project.Project.ID ||
		!strings.Contains(conflict.Difference, "default HTTP endpoint") {
		t.Fatalf("Ensure(conflicting endpoint) error = %v, want typed project replacement conflict", err)
	}
	if len(fixture.state.replaceCalls) != 0 || len(fixture.loopback.calls) != 0 || len(fixture.ports.calls) != 0 {
		t.Fatalf("conflicting endpoint effects = writes %d, loopback %v, ports %v", len(fixture.state.replaceCalls), fixture.loopback.calls, fixture.ports.calls)
	}
}

// TestProjectPrimaryLeaseCoordinatorRetriesOptimisticRevisionRaces proves a losing candidate is replanned from fresh authority.
func TestProjectPrimaryLeaseCoordinatorRetriesOptimisticRevisionRaces(t *testing.T) {
	address11 := netip.MustParseAddr("127.77.0.11")
	address12 := netip.MustParseAddr("127.77.0.12")
	fixture := newPrimaryLeaseTestFixture(t, address11, address12)
	first := true
	fixture.state.replace = func(request state.ReplaceProjectNetworkRequest) (state.NetworkMutationResult, error) {
		if first {
			first = false
			fixture.state.network.Leases = append(fixture.state.network.Leases, primaryLeaseTestLease(t, "project-billing", address11, fixture.state.network.Ownership))
			fixture.state.network.Revision++
			return state.NetworkMutationResult{}, &state.NetworkRevisionConflictError{Expected: request.ExpectedNetworkRevision, Actual: fixture.state.network.Revision}
		}
		fixture.state.replace = nil
		return fixture.state.ReplaceProjectNetwork(t.Context(), request)
	}

	admission, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	if err != nil || admission.Lease.Address != address12 {
		t.Fatalf("Ensure() = %#v, %v, want replanned %s", admission, err, address12)
	}
	if len(fixture.state.replaceCalls) != 3 {
		// The successful callback re-enters the fixture method, so the second attempt records both the callback boundary and the applied write.
		t.Fatalf("replacement observations = %d, want 3", len(fixture.state.replaceCalls))
	}
}

// TestProjectPrimaryLeaseCoordinatorFailsClosedOnIncompleteAuthority covers every external observation boundary.
func TestProjectPrimaryLeaseCoordinatorFailsClosedOnIncompleteAuthority(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	cause := errors.New("fixture observation failed")
	tests := []struct {
		name      string
		configure func(*primaryLeaseTestFixture)
		want      string
		wantCode  domain.ProblemCode
	}{
		{name: "network absent", configure: func(fixture *primaryLeaseTestFixture) { fixture.state.initialized = false }, want: "not initialized", wantCode: "project.network.setup_required"},
		{name: "project read", configure: func(fixture *primaryLeaseTestFixture) { fixture.state.projectErr = cause }, want: "read project"},
		{name: "network read", configure: func(fixture *primaryLeaseTestFixture) { fixture.state.networkErr = cause }, want: "read network"},
		{name: "network invalid", configure: func(fixture *primaryLeaseTestFixture) { fixture.state.network.Leases = nil }, want: "invalid network authority"},
		{name: "discovery", configure: func(fixture *primaryLeaseTestFixture) { fixture.discoverer.err = cause }, want: "discover primary runtime at"},
		{name: "render update", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.discoverer.err = &projectdiscovery.RenderUpdateRequiredError{}
		}, want: "run forj render", wantCode: "project.render.update_required"},
		{name: "loopback read", configure: func(fixture *primaryLeaseTestFixture) { fixture.loopback.errs[address] = cause }, want: "observe pre-provisioned"},
		{name: "loopback address", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.loopback.facts[address] = primaryLeaseTestExactObservation(netip.MustParseAddr("127.77.0.12"))
		}, want: "observation address differs"},
		{name: "loopback facts", configure: func(fixture *primaryLeaseTestFixture) {
			observation := primaryLeaseTestExactObservation(address)
			observation.State = loopback.StateAbsent
			fixture.loopback.facts[address] = observation
		}, want: "validate primary identity observation"},
		{name: "port read", configure: func(fixture *primaryLeaseTestFixture) { fixture.ports.errs[address] = cause }, want: "probe primary identity"},
		{name: "port address", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.ports.results[address] = primaryLeaseTestProbeResult(netip.MustParseAddr("127.77.0.12"), 3000, true)
		}, want: "probe address differs"},
		{name: "port count", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.ports.results[address] = identity.ProbeResult{Address: address, Ports: []identity.PortProbe{}}
		}, want: "returned 0 ports"},
		{name: "port identity", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.ports.results[address] = primaryLeaseTestProbeResult(address, 3001, true)
		}, want: "expected 3000"},
		{name: "port evidence empty", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.ports.results[address] = identity.ProbeResult{Address: address, Ports: []identity.PortProbe{{Port: 3000, Available: true}}}
		}, want: "evidence"},
		{name: "port evidence unbounded", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.ports.results[address] = identity.ProbeResult{Address: address, Ports: []identity.PortProbe{{Port: 3000, Available: true, Evidence: strings.Repeat("x", maximumPrimaryLeaseProbeEvidenceBytes+1)}}}
		}, want: "evidence"},
		{name: "pool exhausted", configure: func(fixture *primaryLeaseTestFixture) {
			fixture.loopback.facts[address] = primaryLeaseTestAbsentObservation(address)
		}, want: "pool exhausted", wantCode: "project.network.capacity_exhausted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPrimaryLeaseTestFixture(t, address)
			test.configure(fixture)
			_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ensure() error = %v, want containing %q", err, test.want)
			}
			assertPrimaryLeaseTestRejection(t, err, test.wantCode)
			if len(fixture.state.replaceCalls) != 0 {
				t.Fatalf("failed admission wrote %d replacements", len(fixture.state.replaceCalls))
			}
		})
	}
}

// TestProjectPrimaryLeaseCoordinatorClassifiesInvalidRuntimeConfiguration keeps correctable checkout errors client-visible.
func TestProjectPrimaryLeaseCoordinatorClassifiesInvalidRuntimeConfiguration(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	_, invalidErr := projectdiscovery.NewDiscoverer().DiscoverDefaultRuntimeAtAddress(t.Context(), t.TempDir(), address)
	var invalid *projectdiscovery.InvalidProjectError
	if !errors.As(invalidErr, &invalid) {
		t.Fatalf("runtime discovery fixture error = %T / %v", invalidErr, invalidErr)
	}
	fixture.discoverer.err = invalidErr

	_, err := fixture.coordinator.Ensure(t.Context(), fixture.state.project.Project.ID)
	assertPrimaryLeaseTestRejection(t, err, "project.runtime.invalid")
	if !errors.As(err, &invalid) {
		t.Fatalf("classified runtime error lost InvalidProjectError: %v", err)
	}
}

// TestProjectPrimaryLeaseCoordinatorValidatesCallsAndDependencies covers caller-controlled inputs before effects.
func TestProjectPrimaryLeaseCoordinatorValidatesCallsAndDependencies(t *testing.T) {
	address := netip.MustParseAddr("127.77.0.11")
	fixture := newPrimaryLeaseTestFixture(t, address)
	lifecycle := &ProjectLifecycleCoordinator{primaryLeases: fixture.coordinator}
	if _, err := fixture.coordinator.Ensure(t.Context(), " bad "); err == nil {
		t.Fatal("Ensure() accepted invalid project ID")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.coordinator.Ensure(ctx, fixture.state.project.Project.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure(cancelled) error = %v", err)
	}
	if _, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileFullStageDefaultHTTPEndpoints(cancelled) error = %v", err)
	}
	fixture.state.initialized = false
	uninitialized, err := lifecycle.ReconcileFullStageDefaultHTTPEndpoints(t.Context())
	if err != nil || uninitialized.Revision != 0 {
		t.Fatalf("ReconcileFullStageDefaultHTTPEndpoints(uninitialized) = %#v, %v", uninitialized, err)
	}
	assertPrimaryLeaseTestPanic(t, func() {
		var coordinator *projectPrimaryLeaseCoordinator
		_, _ = coordinator.Ensure(t.Context(), "project-orders")
	})
	assertPrimaryLeaseTestPanic(t, func() {
		var coordinator *ProjectLifecycleCoordinator
		_, _ = coordinator.ReconcileFullStageDefaultHTTPEndpoints(t.Context())
	})
	assertPrimaryLeaseTestPanic(t, func() {
		newProjectPrimaryLeaseCoordinator(nil, fixture.discoverer, fixture.loopback, fixture.ports, time.Now)
	})
	assertPrimaryLeaseTestPanic(t, func() { newSystemProjectPrimaryLeaseCoordinator(nil, fixture.discoverer) })
}

// TestPrimaryLeasePortAndPlannerValidation covers internal invariants that injected dependencies cannot bypass.
func TestPrimaryLeasePortAndPlannerValidation(t *testing.T) {
	if got, err := canonicalPrimaryLeasePorts([]uint16{3001, 3000}); err != nil || !slices.Equal(got, []uint16{3000, 3001}) {
		t.Fatalf("canonicalPrimaryLeasePorts() = %v, %v", got, err)
	}
	for _, ports := range [][]uint16{nil, {0}, {3000, 3000}, make([]uint16, maximumPrimaryLeasePorts+1)} {
		if _, err := canonicalPrimaryLeasePorts(ports); err == nil {
			t.Fatalf("canonicalPrimaryLeasePorts(%v) accepted invalid input", ports)
		}
	}
	key, err := identity.NewPrimaryKey("project-orders")
	if err != nil {
		t.Fatalf("create primary key: %v", err)
	}
	if _, err := primaryLeaseAllocation(identity.Plan{}, key); err == nil {
		t.Fatal("primaryLeaseAllocation() accepted missing allocation")
	}
	if _, err := primaryLeaseAllocation(identity.Plan{Allocated: []identity.Lease{{Key: identity.LeaseKey{ProjectID: "project-other"}}}}, key); err == nil {
		t.Fatal("primaryLeaseAllocation() accepted another key")
	}
	if !primaryLeaseRevisionConflict(&state.NetworkRevisionConflictError{}) || !primaryLeaseRevisionConflict(&state.ProjectRevisionConflictError{}) || primaryLeaseRevisionConflict(errors.New("other")) {
		t.Fatal("primaryLeaseRevisionConflict() classification drifted")
	}
	fixture := newPrimaryLeaseTestFixture(t, netip.MustParseAddr("127.77.0.11"))
	if _, _, err := fixture.coordinator.probePrimaryPorts(t.Context(), fixture.state.network.Pool, netip.MustParseAddr("127.77.0.11"), nil); err == nil {
		t.Fatal("probePrimaryPorts() accepted an empty port set")
	}
	alpha := state.EndpointReservation{Key: state.EndpointReservationKey{ProjectID: "project-orders", EndpointID: "alpha"}}
	beta := state.EndpointReservation{Key: state.EndpointReservationKey{ProjectID: "project-other", EndpointID: "beta"}}
	fixture.state.network.Reservations.Endpoints = []state.EndpointReservation{beta, alpha}
	got, changed, err := primaryLeaseProjectEndpoints(fixture.state.network, fixture.state.project.Project)
	if err != nil || changed || !slices.Equal(got, []state.EndpointReservation{alpha}) {
		t.Fatalf("primaryLeaseProjectEndpoints() = %#v, %t, %v, want alpha only and unchanged", got, changed, err)
	}
	lease := primaryLeaseTestLease(t, fixture.state.project.Project.ID, netip.MustParseAddr("127.77.0.11"), fixture.state.network.Ownership)
	available := primaryLeaseTestProbeResult(lease.Address, 3000, true)
	unavailable := primaryLeaseTestProbeResult(lease.Address, 3000, false)
	if primaryLeaseEvidence(lease, "same-loopback", available) == primaryLeaseEvidence(lease, "same-loopback", unavailable) {
		t.Fatal("primaryLeaseEvidence() did not bind port availability")
	}
}

// assertPrimaryLeaseTestRejection verifies only expected admission conditions carry a client-facing problem.
func assertPrimaryLeaseTestRejection(t *testing.T, err error, wantCode domain.ProblemCode) {
	t.Helper()
	var rejection *projectPrimaryLeaseRejection
	if wantCode == "" {
		if errors.As(err, &rejection) {
			t.Fatalf("error %v was classified as unexpected admission rejection %#v", err, rejection.Problem())
		}
		return
	}
	if !errors.As(err, &rejection) {
		t.Fatalf("error %v is not a projectPrimaryLeaseRejection", err)
	}
	problem := rejection.Problem()
	if problem.Code != wantCode || !problem.Retryable || problem.Validate() != nil {
		t.Fatalf("admission rejection problem = %#v, want valid retryable code %q", problem, wantCode)
	}
}

// primaryLeaseTestLease creates one validated primary lease owned by the fixture installation.
func primaryLeaseTestLease(
	t *testing.T,
	projectID domain.ProjectID,
	address netip.Addr,
	ownership identity.Ownership,
) identity.Lease {
	t.Helper()
	key, err := identity.NewPrimaryKey(projectID)
	if err != nil {
		t.Fatalf("create fixture primary key: %v", err)
	}
	return identity.Lease{Key: key, Address: address, Ownership: ownership}
}

// primaryLeaseTestLeaseCompare orders fixture leases by their durable project identity.
func primaryLeaseTestLeaseCompare(left identity.Lease, right identity.Lease) int {
	if left.Key.ProjectID < right.Key.ProjectID {
		return -1
	}
	if left.Key.ProjectID > right.Key.ProjectID {
		return 1
	}
	return strings.Compare(left.Key.SecondaryID, right.Key.SecondaryID)
}

// primaryLeaseTestLoopbackFact returns the exact Linux interface identity required by fingerprint validation.
func primaryLeaseTestLoopbackFact() loopback.InterfaceFact {
	return loopback.InterfaceFact{Name: "lo", Index: 1, Kind: loopback.InterfaceKindLinuxNative, NativeLoopback: true}
}

// primaryLeaseTestExactObservation creates one valid pre-provisioned /32 assignment.
func primaryLeaseTestExactObservation(address netip.Addr) loopback.Observation {
	interf := primaryLeaseTestLoopbackFact()
	return loopback.Observation{
		Address:  address,
		Loopback: interf,
		State:    loopback.StateExact,
		Assignments: []loopback.AssignmentFact{{
			Address:        address,
			PrefixLength:   32,
			InterfaceName:  interf.Name,
			InterfaceIndex: interf.Index,
			NativeLoopback: true,
			InterfaceKind:  interf.Kind,
			Linux: &loopback.LinuxAssignmentFact{
				Scope:                    loopback.LinuxAddressScopeHost,
				Flags:                    1 << 7,
				Label:                    interf.Name,
				AddressMatchesLocal:      true,
				CacheInfoPresent:         true,
				ValidLifetimeSeconds:     ^uint32(0),
				PreferredLifetimeSeconds: ^uint32(0),
			},
		}},
	}
}

// primaryLeaseTestAbsentObservation creates one valid pool assignment-drift observation.
func primaryLeaseTestAbsentObservation(address netip.Addr) loopback.Observation {
	return loopback.Observation{
		Address:     address,
		Loopback:    primaryLeaseTestLoopbackFact(),
		State:       loopback.StateAbsent,
		Assignments: []loopback.AssignmentFact{},
	}
}

// primaryLeaseTestProbeResult creates one exact-address socket observation.
func primaryLeaseTestProbeResult(address netip.Addr, port uint16, available bool) identity.ProbeResult {
	return identity.ProbeResult{
		Address: address,
		Ports: []identity.PortProbe{{
			Port:      port,
			Available: available,
			Evidence:  "fixture exact-address probe evidence",
		}},
	}
}

// primaryLeaseTestEnableFullStage installs valid durable listeners before endpoint activation is exercised.
func primaryLeaseTestEnableFullStage(t *testing.T, fixture *primaryLeaseTestFixture) {
	t.Helper()
	fixture.state.network.Stage = state.NetworkStageFull
	fixture.state.network.Reservations.Listeners = primaryLeaseTestListeners(fixture.state.network.UpdatedAt)
	if err := fixture.state.network.Validate(); err != nil {
		t.Fatalf("full-stage fixture Validate() error = %v", err)
	}
}

// primaryLeaseTestDefaultHTTPEndpoint returns the exact durable default route expected for the fixture project.
func primaryLeaseTestDefaultHTTPEndpoint(fixture *primaryLeaseTestFixture, generation uint64) state.EndpointReservation {
	return state.EndpointReservation{
		Key: state.EndpointReservationKey{
			ProjectID:  fixture.state.project.Project.ID,
			EndpointID: primaryLeaseDefaultHTTPEndpointID,
		},
		Protocol:   state.EndpointProtocolHTTP,
		Host:       fixture.state.project.Project.Slug + ".test",
		Public:     fixture.state.network.Reservations.Listeners.HTTPS.Advertised,
		Generation: generation,
	}
}

// primaryLeaseTestListeners creates a valid full-stage ingress boundary outside the project identity pool.
func primaryLeaseTestListeners(verifiedAt time.Time) state.SharedListenerReservations {
	address := netip.MustParseAddr("127.88.0.1")
	reservation := func(port uint16) state.ListenerReservation {
		endpoint := netip.AddrPortFrom(address, port)
		return state.ListenerReservation{
			Mode:       state.ListenerModeDirect,
			Advertised: endpoint,
			Bind:       endpoint,
			Generation: 1,
			VerifiedAt: verifiedAt,
		}
	}
	return state.SharedListenerReservations{
		DNS:   reservation(53),
		HTTP:  reservation(80),
		HTTPS: reservation(443),
	}
}

// assertPrimaryLeaseTestPanic requires invalid dependency wiring to fail before delayed lifecycle work.
func assertPrimaryLeaseTestPanic(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("call did not panic")
		}
	}()
	call()
}
