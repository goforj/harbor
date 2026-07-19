package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/platform/loopback"
	"github.com/goforj/harbor/internal/state"
)

const (
	projectUnregisterApprovalPhase = "awaiting host network release approval"
	projectUnregisterResumePhase   = "host network release verified"
	projectUnregisterCompletePhase = "project unregistered"
	projectUnregisterQuarantine    = time.Hour
)

const projectUnregisterQuarantineReason = "project unregister pending safe reuse"

// ApprovalPlanSource exposes the complete revision-bound helper authority for one operation.
type ApprovalPlanSource interface {
	ticketissuer.PlanSource

	// RequestsForOperation returns every canonical request owned by one approval operation.
	RequestsForOperation(context.Context, domain.OperationID) ([]ticketissuer.Request, error)
}

// ProjectUnregisterState owns every durable mutation used by unregister approval recovery.
type ProjectUnregisterState interface {
	// RuntimeState returns the current client and network projection from one durable instant.
	RuntimeState(context.Context) (state.RuntimeState, error)
	// Project returns one project and its optimistic revision.
	Project(context.Context, domain.ProjectID) (state.ProjectRecord, error)
	// ProjectNetworkRelease returns the durable host-release recovery boundary.
	ProjectNetworkRelease(context.Context, domain.OperationID) (state.ProjectNetworkReleaseRecord, bool, error)
	// StageProjectNetworkReleaseApproval restores durable approval authority for effects still present after a restart.
	StageProjectNetworkReleaseApproval(context.Context, state.StageProjectNetworkReleaseApprovalRequest) (state.ProjectNetworkReleaseApprovalResult, error)
	// ResumeProjectNetworkReleaseApproval retires an exact approval set and resumes its operation atomically.
	ResumeProjectNetworkReleaseApproval(context.Context, state.ResumeProjectNetworkReleaseApprovalRequest) (state.OperationRecord, error)
	// CompleteProjectNetworkRelease commits independently observed host-release facts.
	CompleteProjectNetworkRelease(context.Context, state.CompleteProjectNetworkReleaseRequest) (state.ProjectNetworkReleaseMutationResult, error)
	// CompleteProjectUnregister deletes the project only after its durable network release is complete.
	CompleteProjectUnregister(context.Context, domain.ProjectID, domain.OperationID, domain.Sequence, string, time.Time) (state.OperationRecord, error)
}

// ActiveOperationSource reads revision-bearing operations that may need restart recovery.
type ActiveOperationSource interface {
	// ActiveOperations returns queued and in-progress operations in durable revision order.
	ActiveOperations(context.Context) ([]state.OperationRecord, error)
}

// LoopbackObserver independently classifies one exact loopback address without mutating it.
type LoopbackObserver interface {
	// Observe returns bounded native assignment facts for one address.
	Observe(context.Context, netip.Addr) (loopback.Observation, error)
}

// WithdrawalVerifier proves the live data plane no longer publishes a project's durable network revision.
type WithdrawalVerifier interface {
	// VerifyProjectWithdrawn fails closed unless the selected durable revision has no live project routes.
	VerifyProjectWithdrawn(context.Context, domain.ProjectID, domain.Sequence) error
}

// TicketIssuer publishes one short-lived helper capability derived from durable authority.
type TicketIssuer interface {
	// Issue publishes one capability for the caller-authenticated requester.
	Issue(context.Context, string, ticketissuer.Request) (ticketissuer.Result, error)
	// Close releases the issuer's machine-global stores.
	Close() error
}

// IssuerFactory lazily opens machine-global helper authority only for an interactive Prepare call.
type IssuerFactory func() (TicketIssuer, error)

// PrepareRequest selects one exact approval revision on behalf of an authenticated local requester.
type PrepareRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
	RequesterIdentity         string
}

// Validate rejects stale-shaped Prepare input without echoing the authenticated identity.
func (request PrepareRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	if err := validateOperationRevision(request.ExpectedOperationRevision); err != nil {
		return err
	}
	if request.RequesterIdentity == "" || len(request.RequesterIdentity) > helper.MaximumRequesterIdentityLength {
		return fmt.Errorf("authenticated requester identity is invalid")
	}
	// UID/SID canonicalization belongs to the native issuer because it is coupled to the authenticated transport.
	return nil
}

// PrepareResult reports release progress and at most one short-lived helper capability.
type PrepareResult struct {
	OperationID       domain.OperationID
	OperationRevision domain.Sequence
	ProjectID         domain.ProjectID
	TotalLeases       int
	ReleasedLeases    int
	PendingLeases     int
	Ticket            *ticketissuer.Result
}

// ConfirmRequest selects the exact approval revision whose complete host release must be verified.
type ConfirmRequest struct {
	OperationID               domain.OperationID
	ExpectedOperationRevision domain.Sequence
}

// Validate rejects malformed or revision-free confirmation input.
func (request ConfirmRequest) Validate() error {
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	return validateOperationRevision(request.ExpectedOperationRevision)
}

// HostStateConflictError reports an address Harbor cannot safely release or treat as already released.
type HostStateConflictError struct {
	Address netip.Addr
	State   loopback.State
}

// Error describes the bounded host classification that blocked reconciliation.
func (err *HostStateConflictError) Error() string {
	return fmt.Sprintf("loopback address %s has conflicting state %q", err.Address, err.State)
}

// ReleaseIncompleteError reports that confirmation still requires one or more helper effects.
type ReleaseIncompleteError struct {
	OperationID domain.OperationID
	Remaining   int
}

// Error describes how many exact assignments remain without exposing helper authority.
func (err *ReleaseIncompleteError) Error() string {
	return fmt.Sprintf("project unregister operation %q still has %d loopback release(s) pending", err.OperationID, err.Remaining)
}

// ProjectUnregisterCoordinator serializes approval, confirmation, and restart recovery around durable authority.
type ProjectUnregisterCoordinator struct {
	state      ProjectUnregisterState
	operations ActiveOperationSource
	plans      ApprovalPlanSource
	loopback   LoopbackObserver
	withdrawal WithdrawalVerifier
	issuers    IssuerFactory
	clock      helper.Clock
	mutex      sync.Mutex
}

// NewProjectUnregisterCoordinator constructs one fail-closed unregister recovery authority.
func NewProjectUnregisterCoordinator(
	projectState ProjectUnregisterState,
	operations ActiveOperationSource,
	plans ApprovalPlanSource,
	loopbackObserver LoopbackObserver,
	withdrawal WithdrawalVerifier,
	issuers IssuerFactory,
	clock helper.Clock,
) *ProjectUnregisterCoordinator {
	if nilDependency(projectState) ||
		nilDependency(operations) ||
		nilDependency(plans) ||
		nilDependency(loopbackObserver) ||
		nilDependency(withdrawal) ||
		nilDependency(issuers) ||
		nilDependency(clock) {
		panic("reconcile.NewProjectUnregisterCoordinator requires every authority dependency")
	}
	return &ProjectUnregisterCoordinator{
		state:      projectState,
		operations: operations,
		plans:      plans,
		loopback:   loopbackObserver,
		withdrawal: withdrawal,
		issuers:    issuers,
		clock:      clock,
	}
}

// Prepare proves the complete release set is safe and publishes at most one helper capability.
func (coordinator *ProjectUnregisterCoordinator) Prepare(ctx context.Context, request PrepareRequest) (PrepareResult, error) {
	if err := request.Validate(); err != nil {
		return PrepareResult{}, fmt.Errorf("prepare project unregister approval: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return PrepareResult{}, err
	}

	plans, err := coordinator.resolvePlans(ctx, request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		return PrepareResult{}, fmt.Errorf("prepare project unregister approval: %w", err)
	}
	projectID := plans[0].Lease.Key.ProjectID
	if _, err := coordinator.verifyWithdrawal(ctx, projectID); err != nil {
		return PrepareResult{}, fmt.Errorf("prepare project unregister approval: %w", err)
	}
	observations, err := coordinator.observePlans(ctx, plans)
	if err != nil {
		return PrepareResult{}, fmt.Errorf("prepare project unregister approval: %w", err)
	}

	result := PrepareResult{
		OperationID:       request.OperationID,
		OperationRevision: request.ExpectedOperationRevision,
		ProjectID:         projectID,
		TotalLeases:       len(observations),
	}
	var next *plannedObservation
	for index := range observations {
		observation := observations[index]
		if observation.observation.State == loopback.StateAbsent {
			result.ReleasedLeases++
			continue
		}
		result.PendingLeases++
		if next == nil {
			next = &observations[index]
		}
	}
	if next == nil {
		return result, nil
	}

	// A second live check narrows the gap before the issuer revalidates durable and host authority.
	if _, err := coordinator.verifyWithdrawal(ctx, projectID); err != nil {
		return PrepareResult{}, fmt.Errorf("prepare project unregister approval: %w", err)
	}
	ticket, issueErr := coordinator.issue(ctx, request.RequesterIdentity, next.request, next.lease.Address)
	if issueErr != nil {
		return PrepareResult{}, fmt.Errorf("prepare project unregister approval: %w", issueErr)
	}
	result.Ticket = &ticket
	return result, nil
}

// Confirm independently proves every planned assignment absent before retiring authority and completing unregister.
func (coordinator *ProjectUnregisterCoordinator) Confirm(ctx context.Context, request ConfirmRequest) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm project unregister approval: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}

	plans, err := coordinator.resolvePlans(ctx, request.OperationID, request.ExpectedOperationRevision)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm project unregister approval: %w", err)
	}
	projectID := plans[0].Lease.Key.ProjectID
	if _, err := coordinator.verifyWithdrawal(ctx, projectID); err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm project unregister approval: %w", err)
	}
	observations, err := coordinator.observePlans(ctx, plans)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm project unregister approval: %w", err)
	}
	remaining := countExactObservations(observations)
	if remaining != 0 {
		return state.OperationRecord{}, &ReleaseIncompleteError{OperationID: request.OperationID, Remaining: remaining}
	}
	if _, err := coordinator.verifyWithdrawal(ctx, projectID); err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm project unregister approval: %w", err)
	}

	at := coordinator.operationTime(coordinator.clock.Now(), time.Time{})
	running, err := coordinator.state.ResumeProjectNetworkReleaseApproval(
		ctx,
		state.ResumeProjectNetworkReleaseApprovalRequest{
			OperationID:               request.OperationID,
			ExpectedOperationRevision: request.ExpectedOperationRevision,
			Phase:                     projectUnregisterResumePhase,
			At:                        at,
		},
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm project unregister approval: resume operation: %w", err)
	}
	completed, err := coordinator.finishRunning(ctx, running)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("confirm project unregister approval: %w", err)
	}
	return completed, nil
}

// Recover reconciles restart-safe unregister boundaries without ever opening or issuing helper authority.
func (coordinator *ProjectUnregisterCoordinator) Recover(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	operations, err := coordinator.operations.ActiveOperations(ctx)
	if err != nil {
		return fmt.Errorf("recover project unregister operations: %w", err)
	}
	var recoveryErrors []error
	for _, operation := range operations {
		if operation.Operation.Kind != domain.OperationKindProjectUnregister {
			continue
		}
		if operation.Operation.State == domain.OperationRequiresApproval {
			if _, err := coordinator.resolvePlans(ctx, operation.Operation.ID, operation.Revision); err != nil {
				recoveryErrors = append(recoveryErrors, fmt.Errorf("operation %q: validate approval plans: %w", operation.Operation.ID, err))
			}
			continue
		}
		if operation.Operation.State == domain.OperationQueued {
			continue
		}
		if operation.Operation.State != domain.OperationRunning {
			recoveryErrors = append(recoveryErrors, fmt.Errorf(
				"operation %q has unsupported active state %q",
				operation.Operation.ID,
				operation.Operation.State,
			))
			continue
		}
		if err := coordinator.recoverRunning(ctx, operation); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("operation %q: %w", operation.Operation.ID, err))
		}
	}
	return errors.Join(recoveryErrors...)
}

// recoverRunning advances only post-begin unregister boundaries and restores approval when an exact effect remains.
func (coordinator *ProjectUnregisterCoordinator) recoverRunning(ctx context.Context, operation state.OperationRecord) error {
	release, found, err := coordinator.state.ProjectNetworkRelease(ctx, operation.Operation.ID)
	if err != nil {
		return fmt.Errorf("read network release: %w", err)
	}
	if !found {
		return nil
	}
	if release.ProjectID != operation.Operation.ProjectID || release.OperationID != operation.Operation.ID {
		return fmt.Errorf("network release owner differs from its unregister operation")
	}
	if release.State == state.ProjectNetworkReleaseCompleted {
		_, err := coordinator.finishRunning(ctx, operation)
		return err
	}
	if release.State != state.ProjectNetworkReleaseReleasing {
		return fmt.Errorf("network release state %q is unsupported", release.State)
	}
	if _, err := coordinator.verifyWithdrawal(ctx, release.ProjectID); err != nil {
		return err
	}
	observations, err := coordinator.observeRelease(ctx, release)
	if err != nil {
		return err
	}
	if countExactObservations(observations) == 0 {
		_, err := coordinator.finishRunning(ctx, operation)
		return err
	}

	at := coordinator.operationTime(coordinator.clock.Now(), release.BeganAt)
	_, err = coordinator.state.StageProjectNetworkReleaseApproval(
		ctx,
		state.StageProjectNetworkReleaseApprovalRequest{
			OperationID:               operation.Operation.ID,
			ExpectedOperationRevision: operation.Revision,
			Phase:                     projectUnregisterApprovalPhase,
			At:                        at,
		},
	)
	if err != nil {
		return fmt.Errorf("restore project network release approval: %w", err)
	}
	return nil
}

// finishRunning re-observes a running release boundary before committing teardown and deleting its project.
func (coordinator *ProjectUnregisterCoordinator) finishRunning(
	ctx context.Context,
	operation state.OperationRecord,
) (state.OperationRecord, error) {
	release, found, err := coordinator.state.ProjectNetworkRelease(ctx, operation.Operation.ID)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("read network release: %w", err)
	}
	if !found {
		return state.OperationRecord{}, fmt.Errorf("network release was not found")
	}
	if release.ProjectID != operation.Operation.ProjectID || release.OperationID != operation.Operation.ID {
		return state.OperationRecord{}, fmt.Errorf("network release owner differs from its unregister operation")
	}

	runtimeState, err := coordinator.verifyWithdrawal(ctx, release.ProjectID)
	if err != nil {
		return state.OperationRecord{}, err
	}
	project, err := coordinator.state.Project(ctx, release.ProjectID)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("read project release owner: %w", err)
	}

	if release.State == state.ProjectNetworkReleaseReleasing {
		observations, err := coordinator.observeRelease(ctx, release)
		if err != nil {
			return state.OperationRecord{}, err
		}
		remaining := countExactObservations(observations)
		if remaining != 0 {
			return state.OperationRecord{}, &ReleaseIncompleteError{OperationID: operation.Operation.ID, Remaining: remaining}
		}
		request, err := coordinator.completionRequest(runtimeState, project, operation, release, observations)
		if err != nil {
			return state.OperationRecord{}, err
		}
		completed, err := coordinator.state.CompleteProjectNetworkRelease(ctx, request)
		if err != nil {
			return state.OperationRecord{}, fmt.Errorf("complete project network release: %w", err)
		}
		release = completed.Release
	}
	if release.State != state.ProjectNetworkReleaseCompleted {
		return state.OperationRecord{}, fmt.Errorf("network release state %q is not complete", release.State)
	}

	at := coordinator.operationTime(coordinator.clock.Now(), release.Completion.CompletedAt)
	completed, err := coordinator.state.CompleteProjectUnregister(
		ctx,
		release.ProjectID,
		operation.Operation.ID,
		operation.Revision,
		projectUnregisterCompletePhase,
		at,
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("complete project unregister: %w", err)
	}
	return completed, nil
}

// resolvePlans loads and validates the complete release authority before any ticket source can be opened.
func (coordinator *ProjectUnregisterCoordinator) resolvePlans(
	ctx context.Context,
	operationID domain.OperationID,
	expectedRevision domain.Sequence,
) ([]ticketissuer.Plan, error) {
	requests, err := coordinator.plans.RequestsForOperation(ctx, operationID)
	if err != nil {
		return nil, fmt.Errorf("enumerate approval plans: %w", err)
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("approval plan set is empty")
	}
	plans := make([]ticketissuer.Plan, 0, len(requests))
	seenKeys := make(map[identity.LeaseKey]struct{}, len(requests))
	seenAddresses := make(map[netip.Addr]struct{}, len(requests))
	var projectID domain.ProjectID
	for _, request := range requests {
		if request.OperationID != operationID {
			return nil, fmt.Errorf("approval request belongs to another operation")
		}
		if _, duplicate := seenKeys[request.LeaseKey]; duplicate {
			return nil, fmt.Errorf("approval plan set contains duplicate lease key")
		}
		plan, err := coordinator.plans.Resolve(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("resolve approval plan: %w", err)
		}
		if err := plan.Validate(); err != nil {
			return nil, fmt.Errorf("invalid approval plan: %w", err)
		}
		if plan.OperationID != operationID || plan.Lease.Key != request.LeaseKey {
			return nil, fmt.Errorf("approval plan differs from its request")
		}
		if plan.OperationRevision != expectedRevision {
			return nil, &state.StaleRevisionError{
				OperationID: operationID,
				Expected:    expectedRevision,
				Actual:      plan.OperationRevision,
			}
		}
		if plan.Mutation != helper.OperationReleaseLoopbackIdentity || plan.LeaseState != ticketissuer.LeaseActive {
			return nil, fmt.Errorf("approval plan is not an active loopback release")
		}
		if projectID == "" {
			projectID = plan.Lease.Key.ProjectID
		}
		if plan.Lease.Key.ProjectID != projectID {
			return nil, fmt.Errorf("approval plan set spans multiple projects")
		}
		if _, duplicate := seenAddresses[plan.Lease.Address]; duplicate {
			return nil, fmt.Errorf("approval plan set contains duplicate address")
		}
		seenKeys[request.LeaseKey] = struct{}{}
		seenAddresses[plan.Lease.Address] = struct{}{}
		plan.Requirements = slices.Clone(plan.Requirements)
		plans = append(plans, plan)
	}
	return plans, nil
}

// plannedObservation keeps one independently classified host fact bound to its durable request.
type plannedObservation struct {
	request     ticketissuer.Request
	lease       identity.Lease
	observation loopback.Observation
	fingerprint string
}

// observePlans classifies every plan before a single ticket may be issued.
func (coordinator *ProjectUnregisterCoordinator) observePlans(
	ctx context.Context,
	plans []ticketissuer.Plan,
) ([]plannedObservation, error) {
	observations := make([]plannedObservation, 0, len(plans))
	var observationErrors []error
	for _, plan := range plans {
		observation, fingerprint, err := coordinator.observeAddress(ctx, plan.Lease.Address)
		if err != nil {
			observationErrors = append(observationErrors, err)
			continue
		}
		observations = append(observations, plannedObservation{
			request:     ticketissuer.Request{OperationID: plan.OperationID, LeaseKey: plan.Lease.Key},
			lease:       plan.Lease,
			observation: observation,
			fingerprint: fingerprint,
		})
	}
	if err := errors.Join(observationErrors...); err != nil {
		return nil, err
	}
	return observations, nil
}

// observeRelease classifies every retained recovery lease without consulting retired approval rows.
func (coordinator *ProjectUnregisterCoordinator) observeRelease(
	ctx context.Context,
	release state.ProjectNetworkReleaseRecord,
) ([]plannedObservation, error) {
	observations := make([]plannedObservation, 0, len(release.ActiveLeases))
	var observationErrors []error
	for _, ensure := range release.ActiveLeases {
		observation, fingerprint, err := coordinator.observeAddress(ctx, ensure.Lease.Address)
		if err != nil {
			observationErrors = append(observationErrors, err)
			continue
		}
		observations = append(observations, plannedObservation{
			request: ticketissuer.Request{
				OperationID: release.OperationID,
				LeaseKey:    ensure.Lease.Key,
			},
			lease:       ensure.Lease,
			observation: observation,
			fingerprint: fingerprint,
		})
	}
	if err := errors.Join(observationErrors...); err != nil {
		return nil, err
	}
	if len(observations) == 0 {
		return nil, fmt.Errorf("releasing network boundary has no retained leases")
	}
	return observations, nil
}

// observeAddress validates native facts and rejects every state other than exact ownership or absence.
func (coordinator *ProjectUnregisterCoordinator) observeAddress(
	ctx context.Context,
	address netip.Addr,
) (loopback.Observation, string, error) {
	observation, err := coordinator.loopback.Observe(ctx, address)
	if err != nil {
		return loopback.Observation{}, "", fmt.Errorf("observe loopback address %s: %w", address, err)
	}
	if observation.Address != address {
		return loopback.Observation{}, "", fmt.Errorf("loopback observation address differs from request")
	}
	fingerprint, err := observation.Fingerprint()
	if err != nil {
		return loopback.Observation{}, "", fmt.Errorf("validate loopback observation %s: %w", address, err)
	}
	switch observation.State {
	case loopback.StateAbsent, loopback.StateExact:
		return observation, fingerprint, nil
	case loopback.StateForeign,
		loopback.StateNonHostPrefix,
		loopback.StateAttributeConflict,
		loopback.StateAmbiguous:
		return loopback.Observation{}, "", &HostStateConflictError{Address: address, State: observation.State}
	default:
		return loopback.Observation{}, "", fmt.Errorf("loopback observation state %q is unsupported", observation.State)
	}
}

// countExactObservations reports how many release effects still require the privileged helper.
func countExactObservations(observations []plannedObservation) int {
	remaining := 0
	for _, observation := range observations {
		if observation.observation.State == loopback.StateExact {
			remaining++
		}
	}
	return remaining
}

// verifyWithdrawal binds a fresh durable network revision to the live data-plane safety gate.
func (coordinator *ProjectUnregisterCoordinator) verifyWithdrawal(
	ctx context.Context,
	projectID domain.ProjectID,
) (state.RuntimeState, error) {
	runtimeState, err := coordinator.state.RuntimeState(ctx)
	if err != nil {
		return state.RuntimeState{}, fmt.Errorf("read runtime state: %w", err)
	}
	if !runtimeState.NetworkInitialized {
		return state.RuntimeState{}, fmt.Errorf("network state is not initialized")
	}
	if err := coordinator.withdrawal.VerifyProjectWithdrawn(ctx, projectID, runtimeState.Network.Revision); err != nil {
		return state.RuntimeState{}, fmt.Errorf("verify project routes withdrawn: %w", err)
	}
	return runtimeState, nil
}

// issue opens machine-global authority only after every plan and live withdrawal check succeeds.
func (coordinator *ProjectUnregisterCoordinator) issue(
	ctx context.Context,
	requesterIdentity string,
	request ticketissuer.Request,
	expectedAddress netip.Addr,
) (ticketissuer.Result, error) {
	issuer, err := coordinator.issuers()
	if err != nil {
		return ticketissuer.Result{}, fmt.Errorf("open helper ticket issuer: %w", err)
	}
	if nilTicketIssuer(issuer) {
		return ticketissuer.Result{}, fmt.Errorf("open helper ticket issuer: factory returned no issuer")
	}
	result, issueErr := issuer.Issue(ctx, requesterIdentity, request)
	closeErr := issuer.Close()
	if issueErr != nil || closeErr != nil {
		return ticketissuer.Result{}, errors.Join(issueErr, closeErr)
	}
	if err := result.Validate(coordinator.clock.Now().UTC()); err != nil {
		return ticketissuer.Result{}, fmt.Errorf("helper ticket result is invalid: %w", err)
	}
	if result.OperationID != request.OperationID ||
		result.LeaseKey != request.LeaseKey ||
		result.Operation != helper.OperationReleaseLoopbackIdentity ||
		result.Address != expectedAddress {
		return ticketissuer.Result{}, fmt.Errorf("helper ticket result differs from requested release")
	}
	return result, nil
}

// nilTicketIssuer rejects both nil interfaces and typed-nil factory results at the external store boundary.
func nilTicketIssuer(issuer TicketIssuer) bool {
	return nilDependency(issuer)
}

// nilDependency rejects nil values hidden inside an interface before they become delayed runtime panics.
func nilDependency(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// completionRequest derives bounded durable evidence solely from independently observed absent states.
func (coordinator *ProjectUnregisterCoordinator) completionRequest(
	runtimeState state.RuntimeState,
	project state.ProjectRecord,
	operation state.OperationRecord,
	release state.ProjectNetworkReleaseRecord,
	observations []plannedObservation,
) (state.CompleteProjectNetworkReleaseRequest, error) {
	generation, err := nextReleaseGeneration(runtimeState.Network, release)
	if err != nil {
		return state.CompleteProjectNetworkReleaseRequest{}, err
	}
	byKey := make(map[identity.LeaseKey]plannedObservation, len(observations))
	for _, observation := range observations {
		if observation.observation.State != loopback.StateAbsent {
			return state.CompleteProjectNetworkReleaseRequest{}, &ReleaseIncompleteError{
				OperationID: operation.Operation.ID,
				Remaining:   countExactObservations(observations),
			}
		}
		byKey[observation.lease.Key] = observation
	}
	at := coordinator.operationTime(coordinator.clock.Now(), runtimeState.Network.UpdatedAt)
	if at.Before(release.BeganAt) {
		at = release.BeganAt
	}
	releases := make([]state.NetworkLeaseRelease, 0, len(release.ActiveLeases))
	orderedEvidence := make([]plannedObservation, 0, len(release.ActiveLeases))
	for _, ensure := range release.ActiveLeases {
		observation, exists := byKey[ensure.Lease.Key]
		if !exists || observation.lease != ensure.Lease {
			return state.CompleteProjectNetworkReleaseRequest{}, fmt.Errorf("observed release set differs from retained leases")
		}
		if at.Before(ensure.LeasedAt) {
			at = ensure.LeasedAt
		}
		orderedEvidence = append(orderedEvidence, observation)
	}
	for index, ensure := range release.ActiveLeases {
		observation := orderedEvidence[index]
		releases = append(releases, state.NetworkLeaseRelease{
			Lease:             ensure.Lease,
			ReleaseGeneration: generation,
			ReleaseEvidence:   "loopback-absent-sha256:" + observation.fingerprint,
			ReleasedAt:        at,
			QuarantinedAt:     at,
			ReuseAfter:        at.Add(projectUnregisterQuarantine),
			QuarantineReason:  projectUnregisterQuarantineReason,
		})
	}
	request := state.CompleteProjectNetworkReleaseRequest{
		ProjectID:                 release.ProjectID,
		OperationID:               release.OperationID,
		ExpectedNetworkRevision:   runtimeState.Network.Revision,
		ExpectedProjectRevision:   project.Revision,
		ExpectedOperationRevision: operation.Revision,
		ExpectedBeginGeneration:   release.BeginGeneration,
		CompletionGeneration:      generation,
		Releases:                  releases,
		ReleaseEvidence:           aggregateReleaseEvidence(orderedEvidence),
		At:                        at,
	}
	if err := request.Validate(); err != nil {
		return state.CompleteProjectNetworkReleaseRequest{}, fmt.Errorf("derive project network release completion: %w", err)
	}
	return request, nil
}

// nextReleaseGeneration advances beyond every durable generation represented by the release boundary.
func nextReleaseGeneration(
	network state.NetworkRecord,
	release state.ProjectNetworkReleaseRecord,
) (uint64, error) {
	maximum := release.BeginGeneration
	if network.Ownership.Generation > maximum {
		maximum = network.Ownership.Generation
	}
	for _, ensure := range release.ActiveLeases {
		if ensure.Generation > maximum {
			maximum = ensure.Generation
		}
	}
	if maximum >= uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("project network release generation is exhausted")
	}
	return maximum + 1, nil
}

// aggregateReleaseEvidence binds the complete canonical address and observation set to one durable digest.
func aggregateReleaseEvidence(observations []plannedObservation) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("goforj.harbor.project-unregister-release.v1\x00"))
	for _, observation := range observations {
		_, _ = digest.Write([]byte(observation.lease.Address.String()))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(observation.fingerprint))
		_, _ = digest.Write([]byte{0})
	}
	return "project-loopback-release-sha256:" + hex.EncodeToString(digest.Sum(nil))
}

// operationTime returns a canonical transition instant no earlier than the supplied durable boundary.
func (coordinator *ProjectUnregisterCoordinator) operationTime(
	now time.Time,
	lowerBound time.Time,
) time.Time {
	at := now.UTC().Round(0)
	if !lowerBound.IsZero() && at.Before(lowerBound) {
		return lowerBound.UTC().Round(0)
	}
	return at
}

// validateOperationRevision enforces the shared exact-integer revision range before persistence is consulted.
func validateOperationRevision(revision domain.Sequence) error {
	if revision == 0 || revision > domain.MaximumSequence {
		return fmt.Errorf("expected operation revision must be between 1 and %d", domain.MaximumSequence)
	}
	return nil
}

// normalizeContext keeps optional cancellation scopes on the same recovery path as live daemon calls.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
