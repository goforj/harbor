package reconcile

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/hostconflict"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/state"
)

// projectUnregisterTestClock keeps ticket and lifecycle assertions on one canonical instant.
type projectUnregisterTestClock struct {
	now time.Time
}

// Now returns the fixture's stable UTC instant.
func (clock projectUnregisterTestClock) Now() time.Time {
	return clock.now
}

// projectUnregisterTestPlans supplies an explicit canonical plan set and records source usage.
type projectUnregisterTestPlans struct {
	mutex         sync.Mutex
	requests      []ticketissuer.Request
	plans         map[identity.LeaseKey]ticketissuer.Plan
	requestCalls  int
	resolveCalls  []ticketissuer.Request
	requestsError error
	resolveError  error
}

// RequestsForOperation returns a detached copy of the fixture's complete request set.
func (plans *projectUnregisterTestPlans) RequestsForOperation(
	_ context.Context,
	operationID domain.OperationID,
) ([]ticketissuer.Request, error) {
	plans.mutex.Lock()
	defer plans.mutex.Unlock()
	plans.requestCalls++
	if plans.requestsError != nil {
		return nil, plans.requestsError
	}
	requests := slices.Clone(plans.requests)
	for _, request := range requests {
		if request.OperationID != operationID {
			return requests, nil
		}
	}
	return requests, nil
}

// Resolve returns the exact fixture plan selected by one lease key.
func (plans *projectUnregisterTestPlans) Resolve(
	_ context.Context,
	request ticketissuer.Request,
) (ticketissuer.Plan, error) {
	plans.mutex.Lock()
	defer plans.mutex.Unlock()
	plans.resolveCalls = append(plans.resolveCalls, request)
	if plans.resolveError != nil {
		return ticketissuer.Plan{}, plans.resolveError
	}
	plan, exists := plans.plans[request.LeaseKey]
	if !exists {
		return ticketissuer.Plan{}, errors.New("plan not found")
	}
	plan.Requirements = slices.Clone(plan.Requirements)
	return plan, nil
}

// projectUnregisterTestObserver returns independently configurable native facts for each address.
type projectUnregisterTestObserver struct {
	mutex        sync.Mutex
	facts        map[netip.Addr]loopback.Observation
	errors       map[netip.Addr]error
	calls        []netip.Addr
	active       int
	maximum      int
	blockFirst   chan struct{}
	firstEntered chan struct{}
}

// Observe records concurrency and returns the configured fact without mutating host state.
func (observer *projectUnregisterTestObserver) Observe(
	ctx context.Context,
	address netip.Addr,
) (loopback.Observation, error) {
	observer.mutex.Lock()
	observer.calls = append(observer.calls, address)
	observer.active++
	if observer.active > observer.maximum {
		observer.maximum = observer.active
	}
	block := observer.blockFirst != nil && len(observer.calls) == 1
	entered := observer.firstEntered
	fact := observer.facts[address]
	err := observer.errors[address]
	observer.mutex.Unlock()

	if block {
		close(entered)
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-observer.blockFirst:
		}
	}

	observer.mutex.Lock()
	observer.active--
	observer.mutex.Unlock()
	return fact, err
}

// callSnapshot returns detached observer diagnostics for assertions.
func (observer *projectUnregisterTestObserver) callSnapshot() ([]netip.Addr, int) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	return slices.Clone(observer.calls), observer.maximum
}

// projectUnregisterTestWithdrawal records every durable revision checked against the live data plane.
type projectUnregisterTestWithdrawal struct {
	mutex  sync.Mutex
	calls  []projectUnregisterWithdrawalCall
	err    error
	failAt int
}

// projectUnregisterWithdrawalCall binds one project to the durable network revision supplied to the gate.
type projectUnregisterWithdrawalCall struct {
	projectID domain.ProjectID
	revision  domain.Sequence
}

// VerifyProjectWithdrawn records the gate request and returns its configured result.
func (withdrawal *projectUnregisterTestWithdrawal) VerifyProjectWithdrawn(
	_ context.Context,
	projectID domain.ProjectID,
	revision domain.Sequence,
) error {
	withdrawal.mutex.Lock()
	defer withdrawal.mutex.Unlock()
	withdrawal.calls = append(withdrawal.calls, projectUnregisterWithdrawalCall{projectID: projectID, revision: revision})
	if withdrawal.failAt > 0 && len(withdrawal.calls) != withdrawal.failAt {
		return nil
	}
	return withdrawal.err
}

// callSnapshot returns detached withdrawal diagnostics for assertions.
func (withdrawal *projectUnregisterTestWithdrawal) callSnapshot() []projectUnregisterWithdrawalCall {
	withdrawal.mutex.Lock()
	defer withdrawal.mutex.Unlock()
	return slices.Clone(withdrawal.calls)
}

// projectUnregisterTestIssuerFactory records lazy issuer use and produces bounded valid results.
type projectUnregisterTestIssuerFactory struct {
	mutex          sync.Mutex
	now            time.Time
	addresses      map[identity.LeaseKey]netip.Addr
	openCalls      int
	issueCalls     []projectUnregisterIssueCall
	closeCalls     int
	openError      error
	issueError     error
	closeError     error
	resultOverride *ticketissuer.Result
}

// projectUnregisterIssueCall records the trusted requester only inside the test double.
type projectUnregisterIssueCall struct {
	requester string
	request   ticketissuer.Request
}

// Open lazily constructs one fixture issuer.
func (factory *projectUnregisterTestIssuerFactory) Open() (TicketIssuer, error) {
	factory.mutex.Lock()
	defer factory.mutex.Unlock()
	factory.openCalls++
	if factory.openError != nil {
		return nil, factory.openError
	}
	return &projectUnregisterTestIssuer{factory: factory}, nil
}

// snapshot returns detached issuer diagnostics for assertions.
func (factory *projectUnregisterTestIssuerFactory) snapshot() (int, []projectUnregisterIssueCall, int) {
	factory.mutex.Lock()
	defer factory.mutex.Unlock()
	return factory.openCalls, slices.Clone(factory.issueCalls), factory.closeCalls
}

// projectUnregisterTestIssuer delegates result construction and diagnostics to its factory.
type projectUnregisterTestIssuer struct {
	factory *projectUnregisterTestIssuerFactory
}

// Issue returns one valid opaque capability for the requested fixture lease.
func (issuer *projectUnregisterTestIssuer) Issue(
	_ context.Context,
	requester string,
	request ticketissuer.Request,
) (ticketissuer.Result, error) {
	issuer.factory.mutex.Lock()
	defer issuer.factory.mutex.Unlock()
	issuer.factory.issueCalls = append(issuer.factory.issueCalls, projectUnregisterIssueCall{
		requester: requester,
		request:   request,
	})
	if issuer.factory.issueError != nil {
		return ticketissuer.Result{}, issuer.factory.issueError
	}
	if issuer.factory.resultOverride != nil {
		return *issuer.factory.resultOverride, nil
	}
	return ticketissuer.Result{
		OperationID: request.OperationID,
		LeaseKey:    request.LeaseKey,
		Reference:   helper.TicketReference(strings.Repeat("a", 64)),
		Operation:   helper.OperationReleaseLoopbackIdentity,
		Address:     issuer.factory.addresses[request.LeaseKey],
		ExpiresAt:   issuer.factory.now.Add(time.Minute),
	}, nil
}

// Close records release of the fixture issuer.
func (issuer *projectUnregisterTestIssuer) Close() error {
	issuer.factory.mutex.Lock()
	defer issuer.factory.mutex.Unlock()
	issuer.factory.closeCalls++
	return issuer.factory.closeError
}

// projectUnregisterTestState models forward-only durable transitions across coordinator restarts.
type projectUnregisterTestState struct {
	mutex                   sync.Mutex
	runtime                 state.RuntimeState
	project                 state.ProjectRecord
	releases                map[domain.OperationID]state.ProjectNetworkReleaseRecord
	active                  []state.OperationRecord
	runtimeCalls            int
	projectCalls            int
	releaseCalls            int
	activeCalls             int
	stageCalls              []state.StageProjectNetworkReleaseApprovalRequest
	resumeCalls             []state.ResumeProjectNetworkReleaseApprovalRequest
	completeNetworkCalls    []state.CompleteProjectNetworkReleaseRequest
	completeProjectCalls    []projectUnregisterCompleteProjectCall
	stageError              error
	resumeError             error
	completeNetworkError    error
	completeNetworkFailures int
	completeProjectError    error
	runtimeError            error
	projectError            error
	releaseError            error
	activeError             error
}

// projectUnregisterCompleteProjectCall records the final atomic deletion boundary.
type projectUnregisterCompleteProjectCall struct {
	projectID        domain.ProjectID
	operationID      domain.OperationID
	expectedRevision domain.Sequence
	phase            string
	at               time.Time
}

// RuntimeState returns the fixture's current durable network revision.
func (projectState *projectUnregisterTestState) RuntimeState(_ context.Context) (state.RuntimeState, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.runtimeCalls++
	if projectState.runtimeError != nil {
		return state.RuntimeState{}, projectState.runtimeError
	}
	return projectState.runtime, nil
}

// Project returns the fixture project and optimistic revision.
func (projectState *projectUnregisterTestState) Project(
	_ context.Context,
	projectID domain.ProjectID,
) (state.ProjectRecord, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.projectCalls++
	if projectState.projectError != nil {
		return state.ProjectRecord{}, projectState.projectError
	}
	if projectState.project.Project.ID != projectID {
		return state.ProjectRecord{}, errors.New("project not found")
	}
	return projectState.project, nil
}

// ProjectNetworkRelease returns one detached durable recovery marker.
func (projectState *projectUnregisterTestState) ProjectNetworkRelease(
	_ context.Context,
	operationID domain.OperationID,
) (state.ProjectNetworkReleaseRecord, bool, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.releaseCalls++
	if projectState.releaseError != nil {
		return state.ProjectNetworkReleaseRecord{}, false, projectState.releaseError
	}
	release, found := projectState.releases[operationID]
	return cloneProjectUnregisterTestRelease(release), found, nil
}

// StageProjectNetworkReleaseApproval records restart recovery and transitions the fixture operation to approval.
func (projectState *projectUnregisterTestState) StageProjectNetworkReleaseApproval(
	_ context.Context,
	request state.StageProjectNetworkReleaseApprovalRequest,
) (state.ProjectNetworkReleaseApprovalResult, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.stageCalls = append(projectState.stageCalls, request)
	if projectState.stageError != nil {
		return state.ProjectNetworkReleaseApprovalResult{}, projectState.stageError
	}
	record, index, err := projectState.operation(request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		return state.ProjectNetworkReleaseApprovalResult{}, err
	}
	record.Operation.State = domain.OperationRequiresApproval
	record.Operation.Phase = request.Phase
	record.Revision++
	projectState.active[index] = record
	return state.ProjectNetworkReleaseApprovalResult{Operation: record}, nil
}

// ResumeProjectNetworkReleaseApproval records exact plan retirement and transitions the fixture operation to running.
func (projectState *projectUnregisterTestState) ResumeProjectNetworkReleaseApproval(
	_ context.Context,
	request state.ResumeProjectNetworkReleaseApprovalRequest,
) (state.OperationRecord, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.resumeCalls = append(projectState.resumeCalls, request)
	if projectState.resumeError != nil {
		return state.OperationRecord{}, projectState.resumeError
	}
	record, index, err := projectState.operation(request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		return state.OperationRecord{}, err
	}
	record.Operation.State = domain.OperationRunning
	record.Operation.Phase = request.Phase
	record.Revision++
	projectState.active[index] = record
	return record, nil
}

// CompleteProjectNetworkRelease records exact evidence and advances the fixture marker to completed.
func (projectState *projectUnregisterTestState) CompleteProjectNetworkRelease(
	_ context.Context,
	request state.CompleteProjectNetworkReleaseRequest,
) (state.ProjectNetworkReleaseMutationResult, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.completeNetworkCalls = append(projectState.completeNetworkCalls, request)
	if projectState.completeNetworkFailures > 0 {
		projectState.completeNetworkFailures--
		return state.ProjectNetworkReleaseMutationResult{}, projectState.completeNetworkError
	}
	if projectState.completeNetworkError != nil {
		return state.ProjectNetworkReleaseMutationResult{}, projectState.completeNetworkError
	}
	if err := request.Validate(); err != nil {
		return state.ProjectNetworkReleaseMutationResult{}, err
	}
	release, found := projectState.releases[request.OperationID]
	if !found {
		return state.ProjectNetworkReleaseMutationResult{}, errors.New("release not found")
	}
	release.State = state.ProjectNetworkReleaseCompleted
	release.ActiveLeases = []state.NetworkLeaseEnsure{}
	release.Endpoints = []state.EndpointReservation{}
	release.Completion = &state.ProjectNetworkReleaseCompletion{
		Generation:  request.CompletionGeneration,
		CompletedAt: request.At,
		Evidence:    request.ReleaseEvidence,
	}
	projectState.releases[request.OperationID] = release
	projectState.runtime.Network.Revision++
	projectState.runtime.Network.UpdatedAt = request.At
	return state.ProjectNetworkReleaseMutationResult{Record: projectState.runtime.Network, Release: release}, nil
}

// CompleteProjectUnregister records final deletion and removes the fixture operation from active recovery.
func (projectState *projectUnregisterTestState) CompleteProjectUnregister(
	_ context.Context,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	expectedRevision domain.Sequence,
	phase string,
	at time.Time,
) (state.OperationRecord, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.completeProjectCalls = append(projectState.completeProjectCalls, projectUnregisterCompleteProjectCall{
		projectID:        projectID,
		operationID:      operationID,
		expectedRevision: expectedRevision,
		phase:            phase,
		at:               at,
	})
	if projectState.completeProjectError != nil {
		return state.OperationRecord{}, projectState.completeProjectError
	}
	record, index, err := projectState.operation(operationID, expectedRevision)
	if err != nil {
		return state.OperationRecord{}, err
	}
	finished := at
	record.Operation.State = domain.OperationSucceeded
	record.Operation.Phase = phase
	record.Operation.FinishedAt = &finished
	record.Revision++
	projectState.active = append(projectState.active[:index], projectState.active[index+1:]...)
	return record, nil
}

// ActiveOperations returns a detached copy of every fixture operation still needing recovery.
func (projectState *projectUnregisterTestState) ActiveOperations(_ context.Context) ([]state.OperationRecord, error) {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	projectState.activeCalls++
	if projectState.activeError != nil {
		return nil, projectState.activeError
	}
	return slices.Clone(projectState.active), nil
}

// operation selects one exact fixture operation revision while the caller holds the state lock.
func (projectState *projectUnregisterTestState) operation(
	operationID domain.OperationID,
	expectedRevision domain.Sequence,
) (state.OperationRecord, int, error) {
	for index, record := range projectState.active {
		if record.Operation.ID != operationID {
			continue
		}
		if record.Revision != expectedRevision {
			return state.OperationRecord{}, -1, &state.StaleRevisionError{
				OperationID: operationID,
				Expected:    expectedRevision,
				Actual:      record.Revision,
			}
		}
		return record, index, nil
	}
	return state.OperationRecord{}, -1, errors.New("operation not found")
}

// snapshot returns detached mutation diagnostics for assertions.
func (projectState *projectUnregisterTestState) snapshot() projectUnregisterStateSnapshot {
	projectState.mutex.Lock()
	defer projectState.mutex.Unlock()
	return projectUnregisterStateSnapshot{
		runtimeCalls:         projectState.runtimeCalls,
		projectCalls:         projectState.projectCalls,
		releaseCalls:         projectState.releaseCalls,
		activeCalls:          projectState.activeCalls,
		stageCalls:           slices.Clone(projectState.stageCalls),
		resumeCalls:          slices.Clone(projectState.resumeCalls),
		completeNetworkCalls: slices.Clone(projectState.completeNetworkCalls),
		completeProjectCalls: slices.Clone(projectState.completeProjectCalls),
		active:               slices.Clone(projectState.active),
	}
}

// projectUnregisterStateSnapshot contains detached fake-state diagnostics.
type projectUnregisterStateSnapshot struct {
	runtimeCalls         int
	projectCalls         int
	releaseCalls         int
	activeCalls          int
	stageCalls           []state.StageProjectNetworkReleaseApprovalRequest
	resumeCalls          []state.ResumeProjectNetworkReleaseApprovalRequest
	completeNetworkCalls []state.CompleteProjectNetworkReleaseRequest
	completeProjectCalls []projectUnregisterCompleteProjectCall
	active               []state.OperationRecord
}

// cloneProjectUnregisterTestRelease isolates retained lease slices from coordinator code.
func cloneProjectUnregisterTestRelease(release state.ProjectNetworkReleaseRecord) state.ProjectNetworkReleaseRecord {
	release.ActiveLeases = slices.Clone(release.ActiveLeases)
	release.Endpoints = slices.Clone(release.Endpoints)
	if release.Completion != nil {
		completion := *release.Completion
		release.Completion = &completion
	}
	return release
}

// projectUnregisterFixture owns one complete approval and restart-recovery scenario.
type projectUnregisterFixture struct {
	now         time.Time
	projectID   domain.ProjectID
	operationID domain.OperationID
	revision    domain.Sequence
	leases      []identity.Lease
	plans       *projectUnregisterTestPlans
	observer    *projectUnregisterTestObserver
	withdrawal  *projectUnregisterTestWithdrawal
	issuers     *projectUnregisterTestIssuerFactory
	state       *projectUnregisterTestState
	coordinator *ProjectUnregisterCoordinator
}

// newProjectUnregisterFixture creates two active leases under one staged approval revision.
func newProjectUnregisterFixture(t *testing.T) *projectUnregisterFixture {
	t.Helper()
	now := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	projectID := domain.ProjectID("project-alpha")
	operationID := domain.OperationID("operation-unregister-alpha")
	revision := domain.Sequence(40)
	ownership, err := identity.NewOwnership("installation-alpha", 7)
	if err != nil {
		t.Fatalf("NewOwnership() error = %v", err)
	}
	primaryKey, err := identity.NewPrimaryKey(projectID)
	if err != nil {
		t.Fatalf("NewPrimaryKey() error = %v", err)
	}
	secondaryKey, err := identity.NewSecondaryKey(projectID, "mysql")
	if err != nil {
		t.Fatalf("NewSecondaryKey() error = %v", err)
	}
	leasing := []identity.Lease{
		{Key: primaryKey, Address: netip.MustParseAddr("127.77.0.10"), Ownership: ownership},
		{Key: secondaryKey, Address: netip.MustParseAddr("127.77.0.11"), Ownership: ownership},
	}
	requests := make([]ticketissuer.Request, 0, len(leasing))
	approvalPlans := make(map[identity.LeaseKey]ticketissuer.Plan, len(leasing))
	addresses := make(map[identity.LeaseKey]netip.Addr, len(leasing))
	for _, lease := range leasing {
		request := ticketissuer.Request{OperationID: operationID, LeaseKey: lease.Key}
		requests = append(requests, request)
		approvalPlans[lease.Key] = ticketissuer.Plan{
			OperationID:       operationID,
			OperationRevision: revision,
			OperationState:    domain.OperationRequiresApproval,
			Mutation:          helper.OperationReleaseLoopbackIdentity,
			Lease:             lease,
			LeaseState:        ticketissuer.LeaseActive,
			Requirements:      []hostconflict.SocketRequirement{},
		}
		addresses[lease.Key] = lease.Address
	}

	operation := projectUnregisterTestOperation(now, projectID, operationID, domain.OperationRequiresApproval, revision)
	project := domain.ProjectSnapshot{
		ID:        projectID,
		Name:      "Project Alpha",
		Path:      "/tmp/project-alpha",
		Slug:      "project-alpha",
		State:     domain.ProjectStopped,
		UpdatedAt: now.Add(-time.Hour),
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
	release := state.ProjectNetworkReleaseRecord{
		ProjectID:       projectID,
		OperationID:     operationID,
		State:           state.ProjectNetworkReleaseReleasing,
		BeginGeneration: 20,
		BeganAt:         now.Add(-10 * time.Minute),
		ActiveLeases: []state.NetworkLeaseEnsure{
			{Lease: leasing[0], Generation: 10, EnsureEvidence: "primary ensured", LeasedAt: now.Add(-30 * time.Minute)},
			{Lease: leasing[1], Generation: 11, EnsureEvidence: "secondary ensured", LeasedAt: now.Add(-25 * time.Minute)},
		},
		Endpoints: []state.EndpointReservation{},
	}
	projectState := &projectUnregisterTestState{
		runtime: state.RuntimeState{
			NetworkInitialized: true,
			Network: state.NetworkRecord{
				Revision:  30,
				UpdatedAt: now.Add(-5 * time.Minute),
				Ownership: ownership,
			},
		},
		project:  state.ProjectRecord{Project: project, Revision: 31},
		releases: map[domain.OperationID]state.ProjectNetworkReleaseRecord{operationID: release},
		active:   []state.OperationRecord{operation},
	}
	plans := &projectUnregisterTestPlans{requests: requests, plans: approvalPlans}
	observer := &projectUnregisterTestObserver{
		facts:  make(map[netip.Addr]loopback.Observation, len(leasing)),
		errors: make(map[netip.Addr]error),
	}
	for _, lease := range leasing {
		observer.facts[lease.Address] = projectUnregisterExactObservation(lease.Address)
	}
	withdrawal := &projectUnregisterTestWithdrawal{}
	issuers := &projectUnregisterTestIssuerFactory{now: now, addresses: addresses}
	coordinator := NewProjectUnregisterCoordinator(
		projectState,
		projectState,
		plans,
		observer,
		withdrawal,
		issuers.Open,
		projectUnregisterTestClock{now: now},
	)
	return &projectUnregisterFixture{
		now:         now,
		projectID:   projectID,
		operationID: operationID,
		revision:    revision,
		leases:      leasing,
		plans:       plans,
		observer:    observer,
		withdrawal:  withdrawal,
		issuers:     issuers,
		state:       projectState,
		coordinator: coordinator,
	}
}

// projectUnregisterTestOperation creates one valid active unregister record.
func projectUnregisterTestOperation(
	now time.Time,
	projectID domain.ProjectID,
	operationID domain.OperationID,
	operationState domain.OperationState,
	revision domain.Sequence,
) state.OperationRecord {
	started := now.Add(-20 * time.Minute)
	return state.OperationRecord{
		Operation: domain.Operation{
			ID:          operationID,
			IntentID:    "intent-unregister-alpha",
			Kind:        domain.OperationKindProjectUnregister,
			ProjectID:   projectID,
			State:       operationState,
			Phase:       string(operationState),
			RequestedAt: now.Add(-30 * time.Minute),
			StartedAt:   &started,
		},
		Revision: revision,
	}
}

// projectUnregisterAbsentObservation creates one valid empty Linux loopback observation.
func projectUnregisterAbsentObservation(address netip.Addr) loopback.Observation {
	return loopback.Observation{
		Address:     address,
		Loopback:    projectUnregisterLoopbackFact(),
		State:       loopback.StateAbsent,
		Assignments: []loopback.AssignmentFact{},
	}
}

// projectUnregisterExactObservation creates one valid Harbor-shaped Linux /32 observation.
func projectUnregisterExactObservation(address netip.Addr) loopback.Observation {
	loopbackFact := projectUnregisterLoopbackFact()
	return loopback.Observation{
		Address:  address,
		Loopback: loopbackFact,
		State:    loopback.StateExact,
		Assignments: []loopback.AssignmentFact{{
			Address:        address,
			PrefixLength:   32,
			InterfaceName:  loopbackFact.Name,
			InterfaceIndex: loopbackFact.Index,
			NativeLoopback: true,
			InterfaceKind:  loopback.InterfaceKindLinuxNative,
			Linux: &loopback.LinuxAssignmentFact{
				Scope:                    loopback.LinuxAddressScopeHost,
				Flags:                    1 << 7,
				Label:                    loopbackFact.Name,
				AddressMatchesLocal:      true,
				CacheInfoPresent:         true,
				ValidLifetimeSeconds:     ^uint32(0),
				PreferredLifetimeSeconds: ^uint32(0),
			},
		}},
	}
}

// projectUnregisterForeignObservation creates one valid conflicting assignment on an ordinary interface.
func projectUnregisterForeignObservation(address netip.Addr) loopback.Observation {
	return loopback.Observation{
		Address:  address,
		Loopback: projectUnregisterLoopbackFact(),
		State:    loopback.StateForeign,
		Assignments: []loopback.AssignmentFact{{
			Address:        address,
			PrefixLength:   32,
			InterfaceName:  "eth0",
			InterfaceIndex: 2,
			Linux: &loopback.LinuxAssignmentFact{
				Scope:                    loopback.LinuxAddressScopeUniverse,
				Flags:                    0,
				Label:                    "eth0",
				AddressMatchesLocal:      true,
				CacheInfoPresent:         true,
				ValidLifetimeSeconds:     ^uint32(0),
				PreferredLifetimeSeconds: ^uint32(0),
			},
		}},
	}
}

// projectUnregisterLoopbackFact returns the native Linux identity used by fingerprint validation.
func projectUnregisterLoopbackFact() loopback.InterfaceFact {
	return loopback.InterfaceFact{
		Name:           "lo",
		Index:          1,
		Kind:           loopback.InterfaceKindLinuxNative,
		NativeLoopback: true,
	}
}

// TestProjectUnregisterPrepareObservesEveryPlanAndIssuesOneTicket proves batching cannot mint multiple expiring capabilities.
func TestProjectUnregisterPrepareObservesEveryPlanAndIssuesOneTicket(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	result, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
		RequesterIdentity:         "501",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.OperationID != fixture.operationID || result.OperationRevision != fixture.revision || result.ProjectID != fixture.projectID {
		t.Fatalf("Prepare() identity = %#v", result)
	}
	if result.TotalLeases != 2 || result.ReleasedLeases != 0 || result.PendingLeases != 2 || result.Ticket == nil {
		t.Fatalf("Prepare() progress = %#v", result)
	}
	if result.Ticket.LeaseKey != fixture.leases[0].Key {
		t.Fatalf("Prepare() ticket lease = %#v, want first canonical lease %#v", result.Ticket.LeaseKey, fixture.leases[0].Key)
	}
	calls, maximum := fixture.observer.callSnapshot()
	if !slices.Equal(calls, []netip.Addr{fixture.leases[0].Address, fixture.leases[1].Address}) || maximum != 1 {
		t.Fatalf("Observe() calls = %v, maximum concurrency %d", calls, maximum)
	}
	openCalls, issueCalls, closeCalls := fixture.issuers.snapshot()
	if openCalls != 1 || closeCalls != 1 || len(issueCalls) != 1 {
		t.Fatalf("issuer calls = open %d, issue %#v, close %d", openCalls, issueCalls, closeCalls)
	}
	if issueCalls[0].requester != "501" || issueCalls[0].request.LeaseKey != fixture.leases[0].Key {
		t.Fatalf("Issue() call = %#v", issueCalls[0])
	}
	withdrawals := fixture.withdrawal.callSnapshot()
	if len(withdrawals) != 2 || withdrawals[0].revision != 30 || withdrawals[1].revision != 30 {
		t.Fatalf("VerifyProjectWithdrawn() calls = %#v", withdrawals)
	}
}

// TestProjectUnregisterPrepareSkipsAbsentLeases proves a partial prior helper run resumes at the first remaining effect.
func TestProjectUnregisterPrepareSkipsAbsentLeases(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.observer.facts[fixture.leases[0].Address] = projectUnregisterAbsentObservation(fixture.leases[0].Address)

	result, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
		RequesterIdentity:         "501",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.ReleasedLeases != 1 || result.PendingLeases != 1 || result.Ticket == nil {
		t.Fatalf("Prepare() progress = %#v", result)
	}
	if result.Ticket.LeaseKey != fixture.leases[1].Key {
		t.Fatalf("Prepare() ticket lease = %#v, want %#v", result.Ticket.LeaseKey, fixture.leases[1].Key)
	}
	_, issueCalls, _ := fixture.issuers.snapshot()
	if len(issueCalls) != 1 || issueCalls[0].request.LeaseKey != fixture.leases[1].Key {
		t.Fatalf("Issue() calls = %#v", issueCalls)
	}
}

// TestProjectUnregisterPrepareReturnsReadyWithoutOpeningIssuer proves already-absent releases need no machine-global ticket authority.
func TestProjectUnregisterPrepareReturnsReadyWithoutOpeningIssuer(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setAllObservations(loopback.StateAbsent)

	result, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
		RequesterIdentity:         "501",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.ReleasedLeases != 2 || result.PendingLeases != 0 || result.Ticket != nil {
		t.Fatalf("Prepare() progress = %#v", result)
	}
	openCalls, issueCalls, closeCalls := fixture.issuers.snapshot()
	if openCalls != 0 || len(issueCalls) != 0 || closeCalls != 0 {
		t.Fatalf("issuer unexpectedly used = open %d, issue %d, close %d", openCalls, len(issueCalls), closeCalls)
	}
}

// TestProjectUnregisterPrepareRejectsConflictAfterObservingCompleteSet proves one foreign fact blocks all ticket publication.
func TestProjectUnregisterPrepareRejectsConflictAfterObservingCompleteSet(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.observer.facts[fixture.leases[0].Address] = projectUnregisterForeignObservation(fixture.leases[0].Address)

	_, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
		RequesterIdentity:         "501",
	})
	var conflict *HostStateConflictError
	if !errors.As(err, &conflict) || conflict.Address != fixture.leases[0].Address || conflict.State != loopback.StateForeign {
		t.Fatalf("Prepare() error = %v, want foreign HostStateConflictError", err)
	}
	calls, _ := fixture.observer.callSnapshot()
	if len(calls) != len(fixture.leases) {
		t.Fatalf("Observe() calls = %v, want every lease", calls)
	}
	openCalls, issueCalls, closeCalls := fixture.issuers.snapshot()
	if openCalls != 0 || len(issueCalls) != 0 || closeCalls != 0 {
		t.Fatalf("issuer unexpectedly used = open %d, issue %d, close %d", openCalls, len(issueCalls), closeCalls)
	}
}

// TestProjectUnregisterPrepareRejectsStaleRevisionBeforeHostAccess proves UI retries cannot act on replacement authority.
func TestProjectUnregisterPrepareRejectsStaleRevisionBeforeHostAccess(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	_, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision - 1,
		RequesterIdentity:         "501",
	})
	var stale *state.StaleRevisionError
	if !errors.As(err, &stale) || stale.Actual != fixture.revision || stale.Expected != fixture.revision-1 {
		t.Fatalf("Prepare() error = %v, want StaleRevisionError", err)
	}
	calls, _ := fixture.observer.callSnapshot()
	if len(calls) != 0 || len(fixture.withdrawal.callSnapshot()) != 0 {
		t.Fatalf("stale Prepare touched host: observations %v, withdrawal %#v", calls, fixture.withdrawal.callSnapshot())
	}
	openCalls, _, _ := fixture.issuers.snapshot()
	if openCalls != 0 {
		t.Fatalf("stale Prepare opened %d issuers", openCalls)
	}
}

// TestProjectUnregisterPrepareFailsClosedAtWithdrawalGate proves live routes block observation and capability creation.
func TestProjectUnregisterPrepareFailsClosedAtWithdrawalGate(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.withdrawal.err = errors.New("live route remains")

	_, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
		RequesterIdentity:         "501",
	})
	if err == nil || !strings.Contains(err.Error(), "live route remains") {
		t.Fatalf("Prepare() error = %v", err)
	}
	calls, _ := fixture.observer.callSnapshot()
	openCalls, _, _ := fixture.issuers.snapshot()
	if len(calls) != 0 || openCalls != 0 {
		t.Fatalf("withdrawal failure touched host facts %v or opened %d issuers", calls, openCalls)
	}
}

// TestProjectUnregisterPrepareDoesNotEchoRequesterIdentity proves validation failures keep authenticated identity data out of diagnostics.
func TestProjectUnregisterPrepareDoesNotEchoRequesterIdentity(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	secret := strings.Repeat("sensitive-requester-", 32)
	_, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
		RequesterIdentity:         secret,
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Prepare() error = %q", err)
	}
}

// TestProjectUnregisterConfirmRequiresEveryLeaseAbsent proves confirmation cannot retire authority after a partial helper run.
func TestProjectUnregisterConfirmRequiresEveryLeaseAbsent(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.observer.facts[fixture.leases[0].Address] = projectUnregisterAbsentObservation(fixture.leases[0].Address)

	_, err := fixture.coordinator.Confirm(context.Background(), ConfirmRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
	})
	var incomplete *ReleaseIncompleteError
	if !errors.As(err, &incomplete) || incomplete.Remaining != 1 {
		t.Fatalf("Confirm() error = %v, want one pending release", err)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.resumeCalls) != 0 || len(snapshot.completeNetworkCalls) != 0 || len(snapshot.completeProjectCalls) != 0 {
		t.Fatalf("incomplete Confirm mutated state = %#v", snapshot)
	}
}

// TestProjectUnregisterConfirmResumesAndCompletesSynchronously proves successful postconditions reach project deletion before returning.
func TestProjectUnregisterConfirmResumesAndCompletesSynchronously(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setAllObservations(loopback.StateAbsent)

	completed, err := fixture.coordinator.Confirm(context.Background(), ConfirmRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
	})
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if completed.Operation.State != domain.OperationSucceeded || completed.Operation.ProjectID != fixture.projectID {
		t.Fatalf("Confirm() operation = %#v", completed)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.resumeCalls) != 1 || len(snapshot.completeNetworkCalls) != 1 || len(snapshot.completeProjectCalls) != 1 {
		t.Fatalf("Confirm() mutations = %#v", snapshot)
	}
	resume := snapshot.resumeCalls[0]
	if resume.ExpectedOperationRevision != fixture.revision || resume.Phase != projectUnregisterResumePhase {
		t.Fatalf("Resume request = %#v", resume)
	}
	network := snapshot.completeNetworkCalls[0]
	if err := network.Validate(); err != nil {
		t.Fatalf("completion request Validate() error = %v", err)
	}
	if network.ExpectedNetworkRevision != 30 || network.ExpectedProjectRevision != 31 || network.ExpectedOperationRevision != fixture.revision+1 {
		t.Fatalf("completion revisions = network %d, project %d, operation %d", network.ExpectedNetworkRevision, network.ExpectedProjectRevision, network.ExpectedOperationRevision)
	}
	if network.CompletionGeneration != 21 || len(network.Releases) != 2 {
		t.Fatalf("completion generation/releases = %d/%#v", network.CompletionGeneration, network.Releases)
	}
	for _, release := range network.Releases {
		if release.ReleaseGeneration != 21 || !strings.HasPrefix(release.ReleaseEvidence, "loopback-absent-sha256:") || release.ReuseAfter.Sub(release.QuarantinedAt) != time.Hour {
			t.Fatalf("release evidence = %#v", release)
		}
	}
	project := snapshot.completeProjectCalls[0]
	if project.expectedRevision != fixture.revision+1 || project.phase != projectUnregisterCompletePhase {
		t.Fatalf("CompleteProjectUnregister() call = %#v", project)
	}
	if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
		t.Fatalf("Confirm() opened %d issuers", openCalls)
	}
	if withdrawals := fixture.withdrawal.callSnapshot(); len(withdrawals) != 3 {
		t.Fatalf("Confirm() withdrawal checks = %#v, want 3", withdrawals)
	}
}

// TestProjectUnregisterConfirmRejectsConflictWithoutRetiringPlans proves foreign reassignment keeps approval durable.
func TestProjectUnregisterConfirmRejectsConflictWithoutRetiringPlans(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setAllObservations(loopback.StateAbsent)
	fixture.observer.facts[fixture.leases[1].Address] = projectUnregisterForeignObservation(fixture.leases[1].Address)

	_, err := fixture.coordinator.Confirm(context.Background(), ConfirmRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
	})
	var conflict *HostStateConflictError
	if !errors.As(err, &conflict) || conflict.Address != fixture.leases[1].Address {
		t.Fatalf("Confirm() error = %v, want HostStateConflictError", err)
	}
	if len(fixture.state.snapshot().resumeCalls) != 0 {
		t.Fatal("conflicting Confirm retired approval plans")
	}
}

// TestProjectUnregisterRecoverLeavesApprovalAndQueuedOperationsInert proves startup never surprises the user with privilege activity.
func TestProjectUnregisterRecoverLeavesApprovalAndQueuedOperationsInert(t *testing.T) {
	for _, operationState := range []domain.OperationState{domain.OperationRequiresApproval, domain.OperationQueued} {
		t.Run(string(operationState), func(t *testing.T) {
			fixture := newProjectUnregisterFixture(t)
			fixture.state.active[0] = projectUnregisterTestOperation(
				fixture.now,
				fixture.projectID,
				fixture.operationID,
				operationState,
				fixture.revision,
			)
			if err := fixture.coordinator.Recover(context.Background()); err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			snapshot := fixture.state.snapshot()
			if snapshot.activeCalls != 1 || snapshot.runtimeCalls != 0 || snapshot.releaseCalls != 0 || len(snapshot.stageCalls) != 0 {
				t.Fatalf("inert Recover() state calls = %#v", snapshot)
			}
			if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
				t.Fatalf("inert Recover() opened %d issuers", openCalls)
			}
		})
	}
}

// TestProjectUnregisterRecoverRestagesExactEffectsWithoutIssuer proves a post-resume crash restores durable consent authority only.
func TestProjectUnregisterRecoverRestagesExactEffectsWithoutIssuer(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setRunningOperation()
	fixture.observer.facts[fixture.leases[0].Address] = projectUnregisterAbsentObservation(fixture.leases[0].Address)

	if err := fixture.coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.stageCalls) != 1 || snapshot.stageCalls[0].ExpectedOperationRevision != fixture.revision {
		t.Fatalf("Recover() stage calls = %#v", snapshot.stageCalls)
	}
	if len(snapshot.completeNetworkCalls) != 0 || len(snapshot.completeProjectCalls) != 0 {
		t.Fatalf("Recover() unexpectedly completed = %#v", snapshot)
	}
	if len(snapshot.active) != 1 || snapshot.active[0].Operation.State != domain.OperationRequiresApproval {
		t.Fatalf("Recover() operation = %#v", snapshot.active)
	}
	if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
		t.Fatalf("Recover() opened %d issuers", openCalls)
	}
}

// TestProjectUnregisterRecoverCompletesAlreadyAbsentRelease proves restart recovery advances without user interaction when effects landed.
func TestProjectUnregisterRecoverCompletesAlreadyAbsentRelease(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setRunningOperation()
	fixture.setAllObservations(loopback.StateAbsent)

	if err := fixture.coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.stageCalls) != 0 || len(snapshot.completeNetworkCalls) != 1 || len(snapshot.completeProjectCalls) != 1 || len(snapshot.active) != 0 {
		t.Fatalf("Recover() mutations = %#v", snapshot)
	}
	if openCalls, issueCalls, closeCalls := fixture.issuers.snapshot(); openCalls != 0 || len(issueCalls) != 0 || closeCalls != 0 {
		t.Fatalf("Recover() used issuer = open %d, issue %d, close %d", openCalls, len(issueCalls), closeCalls)
	}
}

// TestProjectUnregisterRecoverCompletesDurableCompletedMarker proves a crash between network completion and deletion needs no host mutation.
func TestProjectUnregisterRecoverCompletesDurableCompletedMarker(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setRunningOperation()
	release := fixture.state.releases[fixture.operationID]
	release.State = state.ProjectNetworkReleaseCompleted
	release.ActiveLeases = []state.NetworkLeaseEnsure{}
	release.Endpoints = []state.EndpointReservation{}
	release.Completion = &state.ProjectNetworkReleaseCompletion{
		Generation:  21,
		CompletedAt: fixture.now.Add(-time.Minute),
		Evidence:    "verified host release",
	}
	fixture.state.releases[fixture.operationID] = release

	if err := fixture.coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	snapshot := fixture.state.snapshot()
	if len(snapshot.completeNetworkCalls) != 0 || len(snapshot.completeProjectCalls) != 1 || len(snapshot.active) != 0 {
		t.Fatalf("Recover() mutations = %#v", snapshot)
	}
	calls, _ := fixture.observer.callSnapshot()
	if len(calls) != 0 {
		t.Fatalf("completed marker triggered host observations %v", calls)
	}
}

// TestProjectUnregisterRecoverIgnoresPreBeginRunningOperation proves unrelated workflow ownership remains with its normal executor.
func TestProjectUnregisterRecoverIgnoresPreBeginRunningOperation(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setRunningOperation()
	delete(fixture.state.releases, fixture.operationID)

	if err := fixture.coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	snapshot := fixture.state.snapshot()
	if snapshot.releaseCalls != 1 || snapshot.runtimeCalls != 0 || len(snapshot.stageCalls) != 0 || len(snapshot.completeProjectCalls) != 0 {
		t.Fatalf("Recover() state calls = %#v", snapshot)
	}
}

// TestProjectUnregisterRecoverFinishesAfterConfirmCrash proves durable release facts survive a failed synchronous completion boundary.
func TestProjectUnregisterRecoverFinishesAfterConfirmCrash(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.setAllObservations(loopback.StateAbsent)
	fixture.state.completeNetworkError = errors.New("temporary database failure")
	fixture.state.completeNetworkFailures = 1

	_, err := fixture.coordinator.Confirm(context.Background(), ConfirmRequest{
		OperationID:               fixture.operationID,
		ExpectedOperationRevision: fixture.revision,
	})
	if err == nil || !strings.Contains(err.Error(), "temporary database failure") {
		t.Fatalf("Confirm() error = %v", err)
	}
	firstSnapshot := fixture.state.snapshot()
	if len(firstSnapshot.resumeCalls) != 1 || len(firstSnapshot.active) != 1 || firstSnapshot.active[0].Operation.State != domain.OperationRunning {
		t.Fatalf("failed Confirm() durable boundary = %#v", firstSnapshot)
	}

	fixture.state.completeNetworkError = nil
	restarted := NewProjectUnregisterCoordinator(
		fixture.state,
		fixture.state,
		fixture.plans,
		fixture.observer,
		fixture.withdrawal,
		fixture.issuers.Open,
		projectUnregisterTestClock{now: fixture.now.Add(time.Minute)},
	)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatalf("restarted Recover() error = %v", err)
	}
	finalSnapshot := fixture.state.snapshot()
	if len(finalSnapshot.completeNetworkCalls) != 2 || len(finalSnapshot.completeProjectCalls) != 1 || len(finalSnapshot.active) != 0 {
		t.Fatalf("restarted recovery mutations = %#v", finalSnapshot)
	}
	if openCalls, _, _ := fixture.issuers.snapshot(); openCalls != 0 {
		t.Fatalf("restart recovery opened %d issuers", openCalls)
	}
}

// TestProjectUnregisterCoordinatorSerializesPublicMethods proves concurrent callers cannot interleave plan or host authority.
func TestProjectUnregisterCoordinatorSerializesPublicMethods(t *testing.T) {
	fixture := newProjectUnregisterFixture(t)
	fixture.observer.blockFirst = make(chan struct{})
	fixture.observer.firstEntered = make(chan struct{})

	firstDone := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
			OperationID:               fixture.operationID,
			ExpectedOperationRevision: fixture.revision,
			RequesterIdentity:         "501",
		})
		firstDone <- err
	}()
	select {
	case <-fixture.observer.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Prepare() did not reach observer")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := fixture.coordinator.Prepare(context.Background(), PrepareRequest{
			OperationID:               fixture.operationID,
			ExpectedOperationRevision: fixture.revision,
			RequesterIdentity:         "501",
		})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second Prepare() escaped serialization with %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(fixture.observer.blockFirst)
	for index, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Prepare() %d error = %v", index+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Prepare() %d did not finish", index+1)
		}
	}
	_, maximum := fixture.observer.callSnapshot()
	if maximum != 1 {
		t.Fatalf("maximum concurrent Observe() calls = %d, want 1", maximum)
	}
}

// TestNextReleaseGenerationUsesNativePersistenceBound proves generation arithmetic cannot wrap its generated int model.
func TestNextReleaseGenerationUsesNativePersistenceBound(t *testing.T) {
	_, err := nextReleaseGeneration(
		state.NetworkRecord{Ownership: identity.Ownership{Generation: uint64(^uint(0) >> 1)}},
		state.ProjectNetworkReleaseRecord{BeginGeneration: 1},
	)
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("nextReleaseGeneration() error = %v", err)
	}
}

// setAllObservations changes every fixture lease to one valid accepted state.
func (fixture *projectUnregisterFixture) setAllObservations(observationState loopback.State) {
	for _, lease := range fixture.leases {
		switch observationState {
		case loopback.StateAbsent:
			fixture.observer.facts[lease.Address] = projectUnregisterAbsentObservation(lease.Address)
		case loopback.StateExact:
			fixture.observer.facts[lease.Address] = projectUnregisterExactObservation(lease.Address)
		default:
			panic("unsupported fixture observation state")
		}
	}
}

// setRunningOperation moves the fixture to the post-resume restart boundary without changing its marker.
func (fixture *projectUnregisterFixture) setRunningOperation() {
	fixture.state.active[0] = projectUnregisterTestOperation(
		fixture.now,
		fixture.projectID,
		fixture.operationID,
		domain.OperationRunning,
		fixture.revision,
	)
}
