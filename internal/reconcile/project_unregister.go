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
	projectUnregisterStartPhase    = "releasing project network"
	projectUnregisterApprovalPhase = "awaiting host network release approval"
	projectUnregisterResumePhase   = "host network release verified"
	projectUnregisterCompletePhase = "project unregistered"
	projectUnregisterQuarantine    = time.Hour

	// SQLite persists network generations as signed 64-bit integers on every supported Harbor host.
	maximumPersistedNetworkGeneration uint64 = 1<<63 - 1
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
	// BeginProjectNetworkRelease atomically suppresses routes and retains exact host-release recovery facts.
	BeginProjectNetworkRelease(context.Context, state.BeginProjectNetworkReleaseRequest) (state.ProjectNetworkReleaseMutationResult, error)
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

// ProjectUnregisterOperationJournal owns idempotent initiation and every recoverable operation transition.
type ProjectUnregisterOperationJournal interface {
	ActiveOperationSource

	// Enqueue durably records one queued operation or replays its matching intent.
	Enqueue(context.Context, domain.Operation) (state.OperationRecord, error)
	// OperationByIntent returns the durable operation already owning one idempotency key.
	OperationByIntent(context.Context, domain.IntentID) (state.OperationRecord, error)
	// Transition advances one exact operation revision through its durable lifecycle.
	Transition(context.Context, domain.OperationID, domain.Sequence, domain.OperationState, string, time.Time, *domain.Problem) (state.OperationRecord, error)
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

// StartRequest identifies one idempotent project unregister intent and its daemon-assigned operation identity.
type StartRequest struct {
	ProjectID   domain.ProjectID
	OperationID domain.OperationID
	IntentID    domain.IntentID
}

// Validate rejects identities that cannot own one stable unregister operation.
func (request StartRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.OperationID.Validate(); err != nil {
		return err
	}
	return request.IntentID.Validate()
}

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
	operations ProjectUnregisterOperationJournal
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
	operations ProjectUnregisterOperationJournal,
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

// Start durably initiates or resumes one idempotent project unregister intent through its first stable boundary.
func (coordinator *ProjectUnregisterCoordinator) Start(
	ctx context.Context,
	request StartRequest,
) (state.OperationRecord, error) {
	if err := request.Validate(); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start project unregister: %w", err)
	}
	ctx = normalizeContext(ctx)

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return state.OperationRecord{}, err
	}
	requestedAt := coordinator.operationTime(coordinator.clock.Now(), time.Time{})
	operation, err := domain.NewOperation(
		request.OperationID,
		request.IntentID,
		domain.OperationKindProjectUnregister,
		request.ProjectID,
		requestedAt,
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start project unregister: create operation: %w", err)
	}

	existing, err := coordinator.operations.OperationByIntent(ctx, request.IntentID)
	expectedEnqueuedOperation := operation
	var expectedEnqueuedRevision *domain.Sequence
	if err == nil {
		if existing.Operation.IntentID != request.IntentID {
			return state.OperationRecord{}, fmt.Errorf("start project unregister: operation intent readback differs from request")
		}
		if existing.Operation.Kind != operation.Kind || existing.Operation.ProjectID != operation.ProjectID {
			return state.OperationRecord{}, &state.IntentConflictError{
				IntentID:            request.IntentID,
				ExistingOperationID: existing.Operation.ID,
				ExistingKind:        existing.Operation.Kind,
				ExistingProjectID:   existing.Operation.ProjectID,
				RequestedKind:       operation.Kind,
				RequestedProjectID:  operation.ProjectID,
			}
		}
		expectedEnqueuedOperation = existing.Operation
		expectedEnqueuedRevision = &existing.Revision
	} else {
		var corruptState *state.CorruptStateError
		if errors.As(err, &corruptState) {
			return state.OperationRecord{}, fmt.Errorf("start project unregister: read operation intent: %w", err)
		}
		var intentMissing *state.OperationIntentNotFoundError
		if !errors.As(err, &intentMissing) {
			return state.OperationRecord{}, fmt.Errorf("start project unregister: read operation intent: %w", err)
		}
		project, err := coordinator.state.Project(ctx, request.ProjectID)
		if err != nil {
			return state.OperationRecord{}, fmt.Errorf("start project unregister: read project: %w", err)
		}
		if project.Project.ID != request.ProjectID {
			return state.OperationRecord{}, fmt.Errorf("start project unregister: project readback differs from request")
		}
		active, err := coordinator.operations.ActiveOperations(ctx)
		if err != nil {
			return state.OperationRecord{}, fmt.Errorf("start project unregister: read active operations: %w", err)
		}
		operationIDs := make([]domain.OperationID, 0)
		for _, activeOperation := range active {
			if activeOperation.Operation.ProjectID == request.ProjectID {
				operationIDs = append(operationIDs, activeOperation.Operation.ID)
			}
		}
		if len(operationIDs) != 0 {
			slices.Sort(operationIDs)
			return state.OperationRecord{}, &state.ProjectBusyError{
				ProjectID:    request.ProjectID,
				OperationIDs: operationIDs,
			}
		}
	}
	enqueued, err := coordinator.operations.Enqueue(ctx, operation)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start project unregister: enqueue operation: %w", err)
	}
	if err := validateProjectUnregisterOperationReadback(
		enqueued,
		expectedEnqueuedOperation,
		expectedEnqueuedRevision,
	); err != nil {
		return state.OperationRecord{}, fmt.Errorf("start project unregister: enqueue readback: %w", err)
	}
	advanced, err := coordinator.advanceOperation(ctx, enqueued)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start project unregister: %w", err)
	}
	return advanced, nil
}

// validateProjectUnregisterOperationReadback binds a journal result to the exact new candidate or previously observed replay record.
func validateProjectUnregisterOperationReadback(
	record state.OperationRecord,
	expectedOperation domain.Operation,
	expectedRevision *domain.Sequence,
) error {
	if !reflect.DeepEqual(record.Operation, expectedOperation) {
		return fmt.Errorf("operation differs from the durable unregister intent")
	}
	if expectedRevision != nil && record.Revision != *expectedRevision {
		return fmt.Errorf("operation revision differs from the durable unregister intent")
	}
	if err := record.Operation.Validate(); err != nil {
		return fmt.Errorf("operation is invalid: %w", err)
	}
	if err := validateOperationRevision(record.Revision); err != nil {
		return fmt.Errorf("operation revision is invalid: %w", err)
	}
	return nil
}

// advanceOperation resumes one unregister record without replaying terminal work or interactive approval.
func (coordinator *ProjectUnregisterCoordinator) advanceOperation(
	ctx context.Context,
	operation state.OperationRecord,
) (state.OperationRecord, error) {
	if operation.Operation.Kind != domain.OperationKindProjectUnregister {
		return state.OperationRecord{}, fmt.Errorf("operation %q kind is %q, not %q", operation.Operation.ID, operation.Operation.Kind, domain.OperationKindProjectUnregister)
	}
	switch operation.Operation.State {
	case domain.OperationQueued:
		return coordinator.advanceQueued(ctx, operation)
	case domain.OperationRunning:
		return coordinator.advanceRunning(ctx, operation)
	case domain.OperationRequiresApproval,
		domain.OperationSucceeded,
		domain.OperationFailed,
		domain.OperationCancelled:
		return operation, nil
	default:
		return state.OperationRecord{}, fmt.Errorf("operation %q has unsupported state %q", operation.Operation.ID, operation.Operation.State)
	}
}

// advanceQueued proves the project still exists before making its unregister operation recoverably running.
func (coordinator *ProjectUnregisterCoordinator) advanceQueued(
	ctx context.Context,
	operation state.OperationRecord,
) (state.OperationRecord, error) {
	project, err := coordinator.state.Project(ctx, operation.Operation.ProjectID)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("read project before unregister: %w", err)
	}
	if project.Project.ID != operation.Operation.ProjectID {
		return state.OperationRecord{}, fmt.Errorf("project readback differs from unregister operation")
	}
	at := coordinator.operationTime(coordinator.clock.Now(), operation.Operation.RequestedAt)
	expectedRunning, err := operation.Operation.Transition(
		domain.OperationRunning,
		projectUnregisterStartPhase,
		at,
		nil,
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("derive queued operation transition: %w", err)
	}
	running, err := coordinator.operations.Transition(
		ctx,
		operation.Operation.ID,
		operation.Revision,
		domain.OperationRunning,
		projectUnregisterStartPhase,
		at,
		nil,
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("start queued operation: %w", err)
	}
	if !reflect.DeepEqual(running.Operation, expectedRunning) ||
		running.Revision <= operation.Revision ||
		running.Revision > domain.MaximumSequence {
		return state.OperationRecord{}, fmt.Errorf("started operation readback differs from the requested transition")
	}
	return coordinator.advanceRunning(ctx, running)
}

// advanceRunning creates a missing release boundary or resumes the exact one already committed before a crash.
func (coordinator *ProjectUnregisterCoordinator) advanceRunning(
	ctx context.Context,
	operation state.OperationRecord,
) (state.OperationRecord, error) {
	release, found, err := coordinator.state.ProjectNetworkRelease(ctx, operation.Operation.ID)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("read network release: %w", err)
	}
	if found {
		return coordinator.advanceRelease(ctx, operation, release)
	}
	return coordinator.beginRunning(ctx, operation)
}

// beginRunning chooses direct pending deletion or stages the first route-suppression boundary from fresh durable revisions.
func (coordinator *ProjectUnregisterCoordinator) beginRunning(
	ctx context.Context,
	operation state.OperationRecord,
) (state.OperationRecord, error) {
	runtimeState, err := coordinator.state.RuntimeState(ctx)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("read runtime state: %w", err)
	}
	project, err := coordinator.state.Project(ctx, operation.Operation.ProjectID)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("read project release owner: %w", err)
	}
	if project.Project.ID != operation.Operation.ProjectID {
		return state.OperationRecord{}, fmt.Errorf("project readback differs from unregister operation")
	}
	if !runtimeState.NetworkInitialized {
		return coordinator.completePending(ctx, project, operation, time.Time{})
	}
	if !projectHasRuntimeNetworkClaims(runtimeState.Network, operation.Operation.ProjectID) {
		return coordinator.completePending(ctx, project, operation, runtimeState.Network.UpdatedAt)
	}

	generation, err := nextProjectUnregisterBeginGeneration(runtimeState.Network)
	if err != nil {
		return state.OperationRecord{}, err
	}
	lowerBound := runtimeState.Network.UpdatedAt
	if operation.Operation.StartedAt != nil && operation.Operation.StartedAt.After(lowerBound) {
		lowerBound = *operation.Operation.StartedAt
	}
	began, err := coordinator.state.BeginProjectNetworkRelease(
		ctx,
		state.BeginProjectNetworkReleaseRequest{
			ProjectID:                 operation.Operation.ProjectID,
			OperationID:               operation.Operation.ID,
			ExpectedNetworkRevision:   runtimeState.Network.Revision,
			ExpectedProjectRevision:   project.Revision,
			ExpectedOperationRevision: operation.Revision,
			BeginGeneration:           generation,
			At:                        coordinator.operationTime(coordinator.clock.Now(), lowerBound),
		},
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("begin project network release: %w", err)
	}
	if began.Release.ProjectID != operation.Operation.ProjectID || began.Release.OperationID != operation.Operation.ID {
		return state.OperationRecord{}, fmt.Errorf("begun network release differs from unregister operation")
	}
	return coordinator.advanceRelease(ctx, operation, began.Release)
}

// completePending atomically removes a project whose durable network state proves no teardown boundary is needed.
func (coordinator *ProjectUnregisterCoordinator) completePending(
	ctx context.Context,
	project state.ProjectRecord,
	operation state.OperationRecord,
	lowerBound time.Time,
) (state.OperationRecord, error) {
	if project.Project.UpdatedAt.After(lowerBound) {
		lowerBound = project.Project.UpdatedAt
	}
	if operation.Operation.StartedAt != nil && operation.Operation.StartedAt.After(lowerBound) {
		lowerBound = *operation.Operation.StartedAt
	}
	completed, err := coordinator.state.CompleteProjectUnregister(
		ctx,
		project.Project.ID,
		operation.Operation.ID,
		operation.Revision,
		projectUnregisterCompletePhase,
		coordinator.operationTime(coordinator.clock.Now(), lowerBound),
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("complete pending project unregister: %w", err)
	}
	return completed, nil
}

// projectHasRuntimeNetworkClaims reports whether a project owns any identity, route, or suppression boundary.
func projectHasRuntimeNetworkClaims(record state.NetworkRecord, projectID domain.ProjectID) bool {
	for _, lease := range record.Leases {
		if lease.Key.ProjectID == projectID {
			return true
		}
	}
	for _, endpoint := range record.Reservations.Endpoints {
		if endpoint.Key.ProjectID == projectID {
			return true
		}
	}
	for _, suppressedProjectID := range record.Reservations.SuppressedProjectIDs {
		if suppressedProjectID == projectID {
			return true
		}
	}
	return false
}

// nextProjectUnregisterBeginGeneration advances beyond every visible network authority generation.
func nextProjectUnregisterBeginGeneration(record state.NetworkRecord) (uint64, error) {
	maximum := record.Ownership.Generation
	for _, lease := range record.Leases {
		if lease.Ownership.Generation > maximum {
			maximum = lease.Ownership.Generation
		}
	}
	for _, listener := range []state.ListenerReservation{
		record.Reservations.Listeners.DNS,
		record.Reservations.Listeners.HTTP,
		record.Reservations.Listeners.HTTPS,
	} {
		if listener.Generation > maximum {
			maximum = listener.Generation
		}
	}
	for _, endpoint := range record.Reservations.Endpoints {
		if endpoint.Generation > maximum {
			maximum = endpoint.Generation
		}
	}
	if maximum >= maximumPersistedNetworkGeneration {
		return 0, fmt.Errorf("project network release generation is exhausted")
	}
	return maximum + 1, nil
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
		if operation.Operation.State != domain.OperationQueued && operation.Operation.State != domain.OperationRunning {
			recoveryErrors = append(recoveryErrors, fmt.Errorf(
				"operation %q has unsupported active state %q",
				operation.Operation.ID,
				operation.Operation.State,
			))
			continue
		}
		if _, err := coordinator.advanceOperation(ctx, operation); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("operation %q: %w", operation.Operation.ID, err))
		}
	}
	return errors.Join(recoveryErrors...)
}

// advanceRelease finishes one exact durable release or restores approval when a host effect remains.
func (coordinator *ProjectUnregisterCoordinator) advanceRelease(
	ctx context.Context,
	operation state.OperationRecord,
	release state.ProjectNetworkReleaseRecord,
) (state.OperationRecord, error) {
	if release.ProjectID != operation.Operation.ProjectID || release.OperationID != operation.Operation.ID {
		return state.OperationRecord{}, fmt.Errorf("network release owner differs from its unregister operation")
	}
	if release.State == state.ProjectNetworkReleaseCompleted {
		return coordinator.finishRunning(ctx, operation)
	}
	if release.State != state.ProjectNetworkReleaseReleasing {
		return state.OperationRecord{}, fmt.Errorf("network release state %q is unsupported", release.State)
	}
	if _, err := coordinator.verifyWithdrawal(ctx, release.ProjectID); err != nil {
		return state.OperationRecord{}, err
	}
	observations, err := coordinator.observeRelease(ctx, release)
	if err != nil {
		return state.OperationRecord{}, err
	}
	if countExactObservations(observations) == 0 {
		return coordinator.finishRunning(ctx, operation)
	}

	at := coordinator.operationTime(coordinator.clock.Now(), release.BeganAt)
	staged, err := coordinator.state.StageProjectNetworkReleaseApproval(
		ctx,
		state.StageProjectNetworkReleaseApprovalRequest{
			OperationID:               operation.Operation.ID,
			ExpectedOperationRevision: operation.Revision,
			Phase:                     projectUnregisterApprovalPhase,
			At:                        at,
		},
	)
	if err != nil {
		return state.OperationRecord{}, fmt.Errorf("restore project network release approval: %w", err)
	}
	return staged.Operation, nil
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
	if maximum >= maximumPersistedNetworkGeneration {
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
