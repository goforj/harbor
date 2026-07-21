package reconcile

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/state"
)

const (
	defaultProjectRuntimeRepairPlanTTL      = 2 * time.Minute
	defaultProjectRuntimeRepairMaximumPlans = 128
	projectRuntimeRepairOpaqueHexLength     = 64
	projectRuntimeRepairIDAttempts          = 8
	projectRuntimeRepairMaximumUserIDBytes  = 4096
)

// ProjectRuntimeRepairInspectionDisposition identifies the fixed inspection result shapes exposed above reconciliation.
type ProjectRuntimeRepairInspectionDisposition string

const (
	// ProjectRuntimeRepairInspectionConfirmable means one exact candidate is retained for explicit confirmation.
	ProjectRuntimeRepairInspectionConfirmable ProjectRuntimeRepairInspectionDisposition = "confirmable"
	// ProjectRuntimeRepairInspectionNotActionable means native inspection did not isolate one safe candidate.
	ProjectRuntimeRepairInspectionNotActionable ProjectRuntimeRepairInspectionDisposition = "not_actionable"
	// ProjectRuntimeRepairInspectionUnsupported means no reviewed native repair backend exists on this platform.
	ProjectRuntimeRepairInspectionUnsupported ProjectRuntimeRepairInspectionDisposition = "unsupported"
)

// ProjectRuntimeRepairNotActionableReason is the bounded explanation for a non-confirmable inspection.
type ProjectRuntimeRepairNotActionableReason string

const (
	// ProjectRuntimeRepairReasonNone means the exact listener is absent.
	ProjectRuntimeRepairReasonNone ProjectRuntimeRepairNotActionableReason = "none"
	// ProjectRuntimeRepairReasonAmbiguous means native evidence identified more than one possible scope.
	ProjectRuntimeRepairReasonAmbiguous ProjectRuntimeRepairNotActionableReason = "ambiguous"
	// ProjectRuntimeRepairReasonForeign means the correlated listener is owned by another user.
	ProjectRuntimeRepairReasonForeign ProjectRuntimeRepairNotActionableReason = "foreign"
	// ProjectRuntimeRepairReasonUnreadable means required native evidence could not be read completely.
	ProjectRuntimeRepairReasonUnreadable ProjectRuntimeRepairNotActionableReason = "unreadable"
)

// ProjectRuntimeRepairInspectionID is the opaque identity of one process-local, one-use plan.
type ProjectRuntimeRepairInspectionID string

// Validate reports whether the inspection ID is the canonical lowercase encoding of 32 random bytes.
func (id ProjectRuntimeRepairInspectionID) Validate() error {
	return validateProjectRuntimeRepairOpaqueHex("project runtime repair inspection ID", string(id))
}

// ProjectRuntimeRepairCandidateFingerprint selects the exact candidate retained by one inspection.
type ProjectRuntimeRepairCandidateFingerprint string

// Validate reports whether the fingerprint is one canonical lowercase SHA-256 value.
func (fingerprint ProjectRuntimeRepairCandidateFingerprint) Validate() error {
	return validateProjectRuntimeRepairOpaqueHex("project runtime repair candidate fingerprint", string(fingerprint))
}

// ProjectRuntimeRepairCaller binds a repair plan to both authenticated transport identity and negotiated RPC role.
type ProjectRuntimeRepairCaller struct {
	// UserID is the platform-native user identity authenticated by local transport.
	UserID string
	// ProcessID is the client process authenticated by local transport.
	ProcessID uint32
	// Role is the client role established during RPC negotiation.
	Role rpc.Role
}

// Validate rejects caller identities that cannot safely bind a process-local repair plan.
func (caller ProjectRuntimeRepairCaller) Validate() error {
	if caller.UserID == "" || len(caller.UserID) > projectRuntimeRepairMaximumUserIDBytes ||
		!utf8.ValidString(caller.UserID) || strings.TrimSpace(caller.UserID) != caller.UserID {
		return errors.New("project runtime repair caller user ID must be non-empty, bounded, valid UTF-8 without surrounding whitespace")
	}
	for _, character := range caller.UserID {
		if unicode.IsControl(character) {
			return errors.New("project runtime repair caller user ID must not contain control characters")
		}
	}
	if caller.ProcessID == 0 {
		return errors.New("project runtime repair caller process ID must be positive")
	}
	return caller.Role.ValidateClient()
}

// ProjectRuntimeRepairInspectRequest selects one project for daemon-derived durable and native inspection.
type ProjectRuntimeRepairInspectRequest struct {
	// Caller is the authenticated identity that alone may confirm the resulting plan.
	Caller ProjectRuntimeRepairCaller
	// ProjectID identifies the quarantined durable project.
	ProjectID domain.ProjectID
}

// Validate rejects inspection requests without complete caller and project identity.
func (request ProjectRuntimeRepairInspectRequest) Validate() error {
	if err := request.Caller.Validate(); err != nil {
		return err
	}
	return request.ProjectID.Validate()
}

// ProjectRuntimeRepairConfirmRequest echoes only the caller, project, and opaque candidate selection.
type ProjectRuntimeRepairConfirmRequest struct {
	// Caller must exactly match the identity that created the plan.
	Caller ProjectRuntimeRepairCaller
	// ProjectID must exactly match the inspected durable project.
	ProjectID domain.ProjectID
	// InspectionID selects one process-local plan without reconstructing its authority.
	InspectionID ProjectRuntimeRepairInspectionID
	// Fingerprint must exactly match the candidate shown during inspection.
	Fingerprint ProjectRuntimeRepairCandidateFingerprint
}

// Validate rejects confirmation requests that do not select one canonical process-local plan.
func (request ProjectRuntimeRepairConfirmRequest) Validate() error {
	if err := request.Caller.Validate(); err != nil {
		return err
	}
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	if err := request.InspectionID.Validate(); err != nil {
		return err
	}
	return request.Fingerprint.Validate()
}

// ProjectRuntimeRepairDisplay contains only bounded facts safe for explicit user confirmation.
type ProjectRuntimeRepairDisplay struct {
	// RootPID identifies the displayed root without carrying reusable birth authority.
	RootPID uint32
	// Command is one fixed sanitized process-shape label.
	Command string
	// CheckoutRoot is the durable canonical checkout displayed to the caller.
	CheckoutRoot string
	// Endpoint is the exact assigned loopback listener displayed to the caller.
	Endpoint netip.AddrPort
	// ProcessCount is the bounded number of processes in the correlated scope.
	ProcessCount uint32
}

// Validate reports whether the display can be represented by the reviewed native display contract.
func (display ProjectRuntimeRepairDisplay) Validate() error {
	return (projectprocess.RuntimeRepairDisplay{
		RootPID:      int64(display.RootPID),
		Command:      display.Command,
		CheckoutRoot: display.CheckoutRoot,
		Endpoint:     display.Endpoint,
		ProcessCount: int(display.ProcessCount),
	}).Validate()
}

// ProjectRuntimeRepairConfirmable is one safe display projection and its opaque one-use selection.
type ProjectRuntimeRepairConfirmable struct {
	// Display contains no native birth receipt or environment evidence.
	Display ProjectRuntimeRepairDisplay
	// InspectionID selects the retained process-local plan.
	InspectionID ProjectRuntimeRepairInspectionID
	// Fingerprint binds confirmation to the candidate displayed during inspection.
	Fingerprint ProjectRuntimeRepairCandidateFingerprint
	// ExpiresAt is the UTC deadline after which the plan cannot cause an effect.
	ExpiresAt time.Time
}

// Validate reports whether a confirmable inspection contains one complete safe selection.
func (confirmable ProjectRuntimeRepairConfirmable) Validate() error {
	if err := confirmable.Display.Validate(); err != nil {
		return err
	}
	if err := confirmable.InspectionID.Validate(); err != nil {
		return err
	}
	if err := confirmable.Fingerprint.Validate(); err != nil {
		return err
	}
	_, offset := confirmable.ExpiresAt.Zone()
	if confirmable.ExpiresAt.IsZero() || offset != 0 {
		return errors.New("project runtime repair expiry must be a nonzero UTC time")
	}
	return nil
}

// ProjectRuntimeRepairInspection is the fixed, receipt-free result of inspecting one retained runtime.
type ProjectRuntimeRepairInspection struct {
	// ProjectID identifies the durable project inspected by the daemon.
	ProjectID domain.ProjectID
	// Disposition identifies the only valid result shape.
	Disposition ProjectRuntimeRepairInspectionDisposition
	// Confirmable is present only when one process-local plan was retained.
	Confirmable *ProjectRuntimeRepairConfirmable
	// Reason is present only for a supported but non-actionable inspection.
	Reason ProjectRuntimeRepairNotActionableReason
}

// Validate reports whether the inspection contains exactly the fields permitted by its disposition.
func (inspection ProjectRuntimeRepairInspection) Validate() error {
	if err := inspection.ProjectID.Validate(); err != nil {
		return err
	}
	switch inspection.Disposition {
	case ProjectRuntimeRepairInspectionConfirmable:
		if inspection.Confirmable == nil || inspection.Reason != "" {
			return errors.New("confirmable project runtime repair inspection requires only candidate details")
		}
		return inspection.Confirmable.Validate()
	case ProjectRuntimeRepairInspectionNotActionable:
		if inspection.Confirmable != nil {
			return errors.New("non-actionable project runtime repair inspection must not contain candidate details")
		}
		return inspection.Reason.Validate()
	case ProjectRuntimeRepairInspectionUnsupported:
		if inspection.Confirmable != nil || inspection.Reason != "" {
			return errors.New("unsupported project runtime repair inspection must not contain candidate details or a reason")
		}
		return nil
	default:
		return fmt.Errorf("unknown project runtime repair inspection disposition %q", inspection.Disposition)
	}
}

// Validate reports whether the reason belongs to the fixed non-actionable vocabulary.
func (reason ProjectRuntimeRepairNotActionableReason) Validate() error {
	switch reason {
	case ProjectRuntimeRepairReasonNone,
		ProjectRuntimeRepairReasonAmbiguous,
		ProjectRuntimeRepairReasonForeign,
		ProjectRuntimeRepairReasonUnreadable:
		return nil
	default:
		return fmt.Errorf("unknown project runtime repair non-actionable reason %q", reason)
	}
}

// ProjectRuntimeRepairPlanNotFoundError means no unused process-local plan exists for an inspection ID.
type ProjectRuntimeRepairPlanNotFoundError struct{}

// Error reports the fixed missing-plan diagnostic without exposing retained authority.
func (*ProjectRuntimeRepairPlanNotFoundError) Error() string {
	return "project runtime repair inspection plan was not found"
}

// ProjectRuntimeRepairPlanMismatchError means caller, project, or fingerprint did not match the consumed plan.
type ProjectRuntimeRepairPlanMismatchError struct{}

// Error reports the fixed plan-binding diagnostic without identifying which binding differed.
func (*ProjectRuntimeRepairPlanMismatchError) Error() string {
	return "project runtime repair confirmation does not match its inspection plan"
}

// ProjectRuntimeRepairPlanExpiredError means the consumed plan reached its UTC deadline.
type ProjectRuntimeRepairPlanExpiredError struct{}

// Error reports that a fresh inspection is required after plan expiry.
func (*ProjectRuntimeRepairPlanExpiredError) Error() string {
	return "project runtime repair inspection plan expired"
}

// ProjectRuntimeRepairPlanCapacityError means the bounded process-local plan store is full.
type ProjectRuntimeRepairPlanCapacityError struct{}

// Error reports bounded plan exhaustion without evicting a still-valid confirmation.
func (*ProjectRuntimeRepairPlanCapacityError) Error() string {
	return "project runtime repair inspection plan capacity is exhausted"
}

// ProjectRuntimeRepairDurableDriftError means durable repair authority changed or could no longer be proved after inspection.
type ProjectRuntimeRepairDurableDriftError struct {
	cause error
}

// Error reports that every durable fence must be inspected again.
func (*ProjectRuntimeRepairDurableDriftError) Error() string {
	return "project runtime repair durable boundary drifted"
}

// Unwrap preserves the durable read or validation cause for daemon-local classification.
func (err *ProjectRuntimeRepairDurableDriftError) Unwrap() error {
	return err.cause
}

// ProjectRuntimeRepairDiscoveryDriftError means runtime discovery changed or could no longer prove the inspected target.
type ProjectRuntimeRepairDiscoveryDriftError struct {
	cause error
}

// Error reports that the checkout-derived target must be inspected again.
func (*ProjectRuntimeRepairDiscoveryDriftError) Error() string {
	return "project runtime repair discovery target drifted"
}

// Unwrap preserves the discovery cause for daemon-local classification.
func (err *ProjectRuntimeRepairDiscoveryDriftError) Unwrap() error {
	return err.cause
}

// ProjectRuntimeRepairNativeDriftError means native revalidation emitted no signal because candidate evidence changed.
type ProjectRuntimeRepairNativeDriftError struct{}

// Error reports that native candidate drift requires a fresh inspection.
func (*ProjectRuntimeRepairNativeDriftError) Error() string {
	return "project runtime repair native candidate drifted"
}

// ProjectRuntimeRepairNativeFailureError means native confirmation failed before or after its graceful signal boundary.
type ProjectRuntimeRepairNativeFailureError struct {
	cause    error
	signaled bool
}

// Error reports the daemon-local native confirmation phase without exposing receipt details.
func (err *ProjectRuntimeRepairNativeFailureError) Error() string {
	if err.signaled {
		return "project runtime repair native confirmation failed after signaling"
	}
	return "project runtime repair native confirmation failed before signaling"
}

// Unwrap returns the backend diagnostic for daemon-local logging and cancellation classification.
func (err *ProjectRuntimeRepairNativeFailureError) Unwrap() error {
	return err.cause
}

// projectRuntimeRepairStore owns the durable inspection boundary and its exact terminal mutation.
type projectRuntimeRepairStore interface {
	RetainedProjectRuntimeRepairBoundary(context.Context, domain.ProjectID) (state.RetainedProjectRuntimeRepairBoundary, error)
	CompleteRetainedProjectRuntimeRepair(context.Context, state.CompleteRetainedProjectRuntimeRepairRequest) (state.ProjectRecord, error)
}

// processBackedProjectRuntimeRepairStore owns the native-confirmed completion path for exact retained process receipts.
type processBackedProjectRuntimeRepairStore interface {
	ProcessBackedProjectRuntimeRepairBoundary(context.Context, domain.ProjectID) (state.ProcessBackedProjectRuntimeRepairBoundary, error)
	CompleteProcessBackedProjectRuntimeRepair(context.Context, state.CompleteProcessBackedProjectRuntimeRepairRequest) (state.ProjectRecord, error)
}

// projectRuntimeRepairDiscoverer derives the exact default listener from one durable checkout and assigned address.
type projectRuntimeRepairDiscoverer interface {
	DiscoverDefaultRuntimeAtAddress(context.Context, string, netip.Addr) (projectdiscovery.RuntimeTarget, error)
}

// projectRuntimeRepairPlan retains all authority that must remain process-local between inspect and confirm.
type projectRuntimeRepairPlan struct {
	caller          ProjectRuntimeRepairCaller
	projectID       domain.ProjectID
	boundary        state.RetainedProjectRuntimeRepairBoundary
	processBoundary *state.ProcessBackedProjectRuntimeRepairBoundary
	target          projectprocess.RuntimeRepairTarget
	candidate       projectprocess.RuntimeRepairCandidate
	fingerprint     ProjectRuntimeRepairCandidateFingerprint
	expiresAt       time.Time
}

// ProjectRuntimeRepairCoordinator owns bounded, caller-bound plans for explicit legacy-runtime repair.
type ProjectRuntimeRepairCoordinator struct {
	store           projectRuntimeRepairStore
	discoverer      projectRuntimeRepairDiscoverer
	repairer        projectprocess.RuntimeRepairer
	unattributed    *UnattributedProjectRuntimeCoordinator
	now             func() time.Time
	random          io.Reader
	planTTL         time.Duration
	maximumPlans    int
	inspectionMutex sync.Mutex
	plansMutex      sync.Mutex
	plans           map[ProjectRuntimeRepairInspectionID]projectRuntimeRepairPlan
}

// NewProjectRuntimeRepairCoordinator creates the production retained-runtime repair authority.
func NewProjectRuntimeRepairCoordinator(store *state.Store) *ProjectRuntimeRepairCoordinator {
	if store == nil {
		panic("reconcile.NewProjectRuntimeRepairCoordinator requires non-nil state")
	}
	coordinator := newProjectRuntimeRepairCoordinator(
		store,
		projectdiscovery.NewDiscoverer(),
		projectprocess.NewRuntimeRepairer(),
		time.Now,
		rand.Reader,
		defaultProjectRuntimeRepairPlanTTL,
		defaultProjectRuntimeRepairMaximumPlans,
	)
	coordinator.unattributed = NewUnattributedProjectRuntimeCoordinator(store)
	return coordinator
}

// newProjectRuntimeRepairCoordinator injects every durable, discovery, native, time, and entropy boundary for tests.
func newProjectRuntimeRepairCoordinator(
	store projectRuntimeRepairStore,
	discoverer projectRuntimeRepairDiscoverer,
	repairer projectprocess.RuntimeRepairer,
	now func() time.Time,
	random io.Reader,
	planTTL time.Duration,
	maximumPlans int,
) *ProjectRuntimeRepairCoordinator {
	if nilDependency(store) || nilDependency(discoverer) || nilDependency(repairer) ||
		nilDependency(now) || nilDependency(random) {
		panic("reconcile.newProjectRuntimeRepairCoordinator requires every dependency")
	}
	if planTTL <= 0 || planTTL > defaultProjectRuntimeRepairPlanTTL {
		panic("reconcile.newProjectRuntimeRepairCoordinator requires a positive plan TTL no longer than two minutes")
	}
	if maximumPlans <= 0 || maximumPlans > defaultProjectRuntimeRepairMaximumPlans {
		panic("reconcile.newProjectRuntimeRepairCoordinator requires a positive bounded plan capacity")
	}
	return &ProjectRuntimeRepairCoordinator{
		store:        store,
		discoverer:   discoverer,
		repairer:     repairer,
		now:          now,
		random:       random,
		planTTL:      planTTL,
		maximumPlans: maximumPlans,
		plans:        make(map[ProjectRuntimeRepairInspectionID]projectRuntimeRepairPlan),
	}
}

// Inspect invalidates an older project plan before deriving and classifying fresh durable and native evidence.
func (coordinator *ProjectRuntimeRepairCoordinator) Inspect(
	ctx context.Context,
	request ProjectRuntimeRepairInspectRequest,
) (ProjectRuntimeRepairInspection, error) {
	if coordinator == nil {
		panic("reconcile.ProjectRuntimeRepairCoordinator.Inspect requires a non-nil receiver")
	}
	ctx = normalizeProjectRuntimeRepairContext(ctx)
	if err := request.Validate(); err != nil {
		return ProjectRuntimeRepairInspection{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectRuntimeRepairInspection{}, err
	}
	if coordinator.unattributed != nil {
		unattributed, err := coordinator.unattributed.Inspect(ctx, UnattributedProjectRuntimeInspectRequest{
			Caller:    request.Caller,
			ProjectID: request.ProjectID,
		})
		if err == nil {
			return projectRuntimeRepairInspectionFromUnattributed(unattributed)
		}
		var active *state.ProjectSessionActiveError
		if !errors.As(err, &active) {
			return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect unattributed project runtime: %w", err)
		}
	}

	coordinator.inspectionMutex.Lock()
	defer coordinator.inspectionMutex.Unlock()
	coordinator.invalidateProjectPlans(request.ProjectID, coordinator.now().UTC().Round(0))
	if processStore, ok := coordinator.store.(processBackedProjectRuntimeRepairStore); ok {
		processBoundary, err := processStore.ProcessBackedProjectRuntimeRepairBoundary(ctx, request.ProjectID)
		if err == nil {
			return coordinator.inspectProcessBackedProjectRuntime(ctx, request, processBoundary)
		}
		if !isMissingProcessBackedRuntimeBoundary(err) {
			return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect process-backed project runtime boundary: %w", err)
		}
	}

	boundary, err := coordinator.store.RetainedProjectRuntimeRepairBoundary(ctx, request.ProjectID)
	if err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect retained project runtime boundary: %w", err)
	}
	if err := boundary.Validate(); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect retained project runtime boundary is invalid: %w", err)
	}
	if boundary.Project.Project.ID != request.ProjectID {
		return ProjectRuntimeRepairInspection{}, errors.New("retained project runtime boundary belongs to another project")
	}
	target, err := coordinator.discoverTarget(ctx, boundary)
	if err != nil {
		return ProjectRuntimeRepairInspection{}, err
	}
	inspection, err := coordinator.repairer.Inspect(ctx, target)
	if err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect native project runtime: %w", err)
	}
	if err := validateProjectRuntimeRepairNativeInspection(inspection, target); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect native project runtime result: %w", err)
	}
	return coordinator.projectInspection(request, boundary, nil, target, inspection)
}

// inspectProcessBackedProjectRuntime performs native inspection after the durable process-backed fence is read.
func (coordinator *ProjectRuntimeRepairCoordinator) inspectProcessBackedProjectRuntime(
	ctx context.Context,
	request ProjectRuntimeRepairInspectRequest,
	boundary state.ProcessBackedProjectRuntimeRepairBoundary,
) (ProjectRuntimeRepairInspection, error) {
	if err := boundary.Validate(); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect process-backed project runtime boundary is invalid: %w", err)
	}
	if boundary.Project.Project.ID != request.ProjectID {
		return ProjectRuntimeRepairInspection{}, errors.New("process-backed project runtime boundary belongs to another project")
	}
	target, err := coordinator.discoverProcessBackedTarget(ctx, boundary)
	if err != nil {
		return ProjectRuntimeRepairInspection{}, err
	}
	inspection, err := coordinator.repairer.Inspect(ctx, target)
	if err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect native process-backed project runtime: %w", err)
	}
	if err := validateProjectRuntimeRepairNativeInspection(inspection, target); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("inspect native process-backed project runtime result: %w", err)
	}
	base := retainedProjectRuntimeRepairBoundaryFromProcessBacked(boundary)
	return coordinator.projectInspection(request, base, &boundary, target, inspection)
}

// Confirm consumes one plan before revalidating durable, discovery, and native postconditions in order.
func (coordinator *ProjectRuntimeRepairCoordinator) Confirm(
	ctx context.Context,
	request ProjectRuntimeRepairConfirmRequest,
) (state.ProjectRecord, error) {
	if coordinator == nil {
		panic("reconcile.ProjectRuntimeRepairCoordinator.Confirm requires a non-nil receiver")
	}
	ctx = normalizeProjectRuntimeRepairContext(ctx)
	if err := validateProjectRuntimeRepairPlanSelection(request); err != nil {
		return state.ProjectRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return state.ProjectRecord{}, err
	}
	if coordinator.unattributed != nil && !coordinator.hasPlan(request.InspectionID) && coordinator.unattributed.hasPlan(request.InspectionID) {
		confirmation, err := coordinator.unattributed.Confirm(ctx, UnattributedProjectRuntimeConfirmRequest{
			Caller:       request.Caller,
			ProjectID:    request.ProjectID,
			InspectionID: request.InspectionID,
			Fingerprint:  request.Fingerprint,
		})
		if err != nil {
			return state.ProjectRecord{}, err
		}
		return confirmation.Project, nil
	}

	plan, found := coordinator.takePlan(request.InspectionID)
	if !found {
		return state.ProjectRecord{}, &ProjectRuntimeRepairPlanNotFoundError{}
	}
	if request.Fingerprint.Validate() != nil || plan.caller != request.Caller || plan.projectID != request.ProjectID ||
		subtle.ConstantTimeCompare([]byte(plan.fingerprint), []byte(request.Fingerprint)) != 1 {
		return state.ProjectRecord{}, &ProjectRuntimeRepairPlanMismatchError{}
	}
	now := coordinator.now().UTC().Round(0)
	if !now.Before(plan.expiresAt) {
		return state.ProjectRecord{}, &ProjectRuntimeRepairPlanExpiredError{}
	}

	boundary := plan.boundary
	var processBoundary *state.ProcessBackedProjectRuntimeRepairBoundary
	if plan.processBoundary != nil {
		processStore, ok := coordinator.store.(processBackedProjectRuntimeRepairStore)
		if !ok {
			return state.ProjectRecord{}, &ProjectRuntimeRepairDurableDriftError{}
		}
		current, err := processStore.ProcessBackedProjectRuntimeRepairBoundary(ctx, request.ProjectID)
		if err != nil {
			if isProjectRuntimeRepairContextError(ctx, err) {
				return state.ProjectRecord{}, err
			}
			return state.ProjectRecord{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
		}
		if err := current.Validate(); err != nil {
			return state.ProjectRecord{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
		}
		if !projectRuntimeRepairProcessBoundariesEqual(current, *plan.processBoundary) {
			return state.ProjectRecord{}, &ProjectRuntimeRepairDurableDriftError{}
		}
		processBoundary = &current
		boundary = retainedProjectRuntimeRepairBoundaryFromProcessBacked(current)
	} else {
		current, err := coordinator.store.RetainedProjectRuntimeRepairBoundary(ctx, request.ProjectID)
		if err != nil {
			if isProjectRuntimeRepairContextError(ctx, err) {
				return state.ProjectRecord{}, err
			}
			return state.ProjectRecord{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
		}
		if err := current.Validate(); err != nil {
			return state.ProjectRecord{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
		}
		if !projectRuntimeRepairBoundariesEqual(current, plan.boundary) {
			return state.ProjectRecord{}, &ProjectRuntimeRepairDurableDriftError{}
		}
		boundary = current
	}
	target, err := coordinator.discoverTarget(ctx, boundary)
	if err != nil {
		if isProjectRuntimeRepairContextError(ctx, err) {
			return state.ProjectRecord{}, err
		}
		return state.ProjectRecord{}, &ProjectRuntimeRepairDiscoveryDriftError{cause: err}
	}
	if target != plan.target {
		return state.ProjectRecord{}, &ProjectRuntimeRepairDiscoveryDriftError{}
	}
	if err := ctx.Err(); err != nil {
		return state.ProjectRecord{}, err
	}

	confirmation, confirmErr := coordinator.repairer.Confirm(ctx, plan.candidate.Clone())
	if err := confirmation.Validate(); err != nil {
		return state.ProjectRecord{}, newProjectRuntimeRepairNativeFailure(confirmation.Signaled, errors.Join(confirmErr, err))
	}
	if confirmErr != nil {
		return state.ProjectRecord{}, newProjectRuntimeRepairNativeFailure(confirmation.Signaled, confirmErr)
	}
	switch confirmation.State {
	case projectprocess.RuntimeRepairConfirmationDrifted:
		return state.ProjectRecord{}, &ProjectRuntimeRepairNativeDriftError{}
	case projectprocess.RuntimeRepairConfirmationFailed:
		return state.ProjectRecord{}, newProjectRuntimeRepairNativeFailure(
			confirmation.Signaled,
			errors.New("native runtime repair confirmation returned a failed state"),
		)
	case projectprocess.RuntimeRepairConfirmationSettled:
	default:
		return state.ProjectRecord{}, newProjectRuntimeRepairNativeFailure(
			confirmation.Signaled,
			fmt.Errorf("native runtime repair confirmation returned unknown state %q", confirmation.State),
		)
	}

	var completed state.ProjectRecord
	if processBoundary != nil {
		processStore := coordinator.store.(processBackedProjectRuntimeRepairStore)
		completed, err = processStore.CompleteProcessBackedProjectRuntimeRepair(
			ctx,
			processBackedProjectRuntimeRepairCompletionRequest(*processBoundary, completionTimeForProjectRuntimeRepair(boundary, coordinator.now())),
		)
	} else {
		completed, err = coordinator.store.CompleteRetainedProjectRuntimeRepair(
			ctx,
			projectRuntimeRepairCompletionRequest(boundary, completionTimeForProjectRuntimeRepair(boundary, coordinator.now())),
		)
	}
	if err != nil {
		return state.ProjectRecord{}, fmt.Errorf("complete project runtime repair: %w", err)
	}
	if err := completed.Validate(); err != nil {
		return state.ProjectRecord{}, fmt.Errorf("completed retained project runtime repair is invalid: %w", err)
	}
	if completed.Project.ID != request.ProjectID || completed.Project.State != domain.ProjectStopped {
		return state.ProjectRecord{}, errors.New("completed retained project runtime repair returned a different or non-stopped project")
	}
	return completed, nil
}

// projectInspection maps one validated native state into the fixed receipt-free reconciliation vocabulary.
func (coordinator *ProjectRuntimeRepairCoordinator) projectInspection(
	request ProjectRuntimeRepairInspectRequest,
	boundary state.RetainedProjectRuntimeRepairBoundary,
	processBoundary *state.ProcessBackedProjectRuntimeRepairBoundary,
	target projectprocess.RuntimeRepairTarget,
	inspection projectprocess.RuntimeRepairInspection,
) (ProjectRuntimeRepairInspection, error) {
	result := ProjectRuntimeRepairInspection{ProjectID: request.ProjectID}
	switch inspection.State {
	case projectprocess.RuntimeRepairInspectionMissing:
		result.Disposition = ProjectRuntimeRepairInspectionNotActionable
		result.Reason = ProjectRuntimeRepairReasonNone
	case projectprocess.RuntimeRepairInspectionAmbiguous:
		result.Disposition = ProjectRuntimeRepairInspectionNotActionable
		result.Reason = ProjectRuntimeRepairReasonAmbiguous
	case projectprocess.RuntimeRepairInspectionForeign:
		result.Disposition = ProjectRuntimeRepairInspectionNotActionable
		result.Reason = ProjectRuntimeRepairReasonForeign
	case projectprocess.RuntimeRepairInspectionUnreadable:
		result.Disposition = ProjectRuntimeRepairInspectionNotActionable
		result.Reason = ProjectRuntimeRepairReasonUnreadable
	case projectprocess.RuntimeRepairInspectionUnsupported:
		result.Disposition = ProjectRuntimeRepairInspectionUnsupported
	case projectprocess.RuntimeRepairInspectionActionable:
		candidate := inspection.Candidate.Clone()
		display, err := safeProjectRuntimeRepairDisplay(candidate.Display)
		if err != nil {
			return ProjectRuntimeRepairInspection{}, err
		}
		expiresAt := coordinator.now().UTC().Round(0).Add(coordinator.planTTL)
		plan := projectRuntimeRepairPlan{
			caller:          request.Caller,
			projectID:       request.ProjectID,
			boundary:        boundary,
			processBoundary: cloneProcessBackedProjectRuntimeRepairBoundary(processBoundary),
			target:          target,
			candidate:       candidate,
			fingerprint:     ProjectRuntimeRepairCandidateFingerprint(candidate.Fingerprint),
			expiresAt:       expiresAt,
		}
		inspectionID, err := coordinator.storePlan(plan)
		if err != nil {
			return ProjectRuntimeRepairInspection{}, err
		}
		result.Disposition = ProjectRuntimeRepairInspectionConfirmable
		result.Confirmable = &ProjectRuntimeRepairConfirmable{
			Display:      display,
			InspectionID: inspectionID,
			Fingerprint:  plan.fingerprint,
			ExpiresAt:    expiresAt,
		}
	default:
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("cannot map native runtime repair inspection state %q", inspection.State)
	}
	if err := result.Validate(); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("project runtime repair inspection projection: %w", err)
	}
	return result, nil
}

// discoverTarget derives and validates the only repair target permitted by the durable checkout and primary lease.
func (coordinator *ProjectRuntimeRepairCoordinator) discoverTarget(
	ctx context.Context,
	boundary state.RetainedProjectRuntimeRepairBoundary,
) (projectprocess.RuntimeRepairTarget, error) {
	return coordinator.discoverTargetAtProject(ctx, boundary.Project.Project.Path, boundary.PrimaryLease.Address)
}

// discoverProcessBackedTarget derives the exact native target from process-backed durable identity and lease fences.
func (coordinator *ProjectRuntimeRepairCoordinator) discoverProcessBackedTarget(
	ctx context.Context,
	boundary state.ProcessBackedProjectRuntimeRepairBoundary,
) (projectprocess.RuntimeRepairTarget, error) {
	return coordinator.discoverTargetAtProject(ctx, boundary.Project.Project.Path, boundary.PrimaryLease.Address)
}

// discoverTargetAtProject derives and validates one exact default listener from a checkout and primary address.
func (coordinator *ProjectRuntimeRepairCoordinator) discoverTargetAtProject(
	ctx context.Context,
	checkoutRoot string,
	address netip.Addr,
) (projectprocess.RuntimeRepairTarget, error) {
	discovered, err := coordinator.discoverer.DiscoverDefaultRuntimeAtAddress(ctx, checkoutRoot, address)
	if err != nil {
		return projectprocess.RuntimeRepairTarget{}, fmt.Errorf("discover retained project runtime target: %w", err)
	}
	if err := discovered.Validate(); err != nil {
		return projectprocess.RuntimeRepairTarget{}, fmt.Errorf("discovered retained project runtime target is invalid: %w", err)
	}
	if discovered.Address != address {
		return projectprocess.RuntimeRepairTarget{}, errors.New("discovered retained project runtime target differs from the durable primary lease")
	}
	target := projectprocess.RuntimeRepairTarget{
		CheckoutRoot: checkoutRoot,
		Endpoint:     netip.AddrPortFrom(discovered.Address, discovered.Port),
	}
	if err := target.Validate(); err != nil {
		return projectprocess.RuntimeRepairTarget{}, fmt.Errorf("derive retained project runtime target: %w", err)
	}
	return target, nil
}

// invalidateProjectPlans removes both expired plans and every prior plan for the selected project.
func (coordinator *ProjectRuntimeRepairCoordinator) invalidateProjectPlans(projectID domain.ProjectID, now time.Time) {
	coordinator.plansMutex.Lock()
	defer coordinator.plansMutex.Unlock()
	for inspectionID, plan := range coordinator.plans {
		if plan.projectID == projectID || !now.Before(plan.expiresAt) {
			delete(coordinator.plans, inspectionID)
		}
	}
}

// storePlan inserts one bounded plan under fresh random identity without evicting a valid plan for another project.
func (coordinator *ProjectRuntimeRepairCoordinator) storePlan(plan projectRuntimeRepairPlan) (ProjectRuntimeRepairInspectionID, error) {
	for attempt := 0; attempt < projectRuntimeRepairIDAttempts; attempt++ {
		inspectionID, err := newProjectRuntimeRepairInspectionID(coordinator.random)
		if err != nil {
			return "", err
		}
		coordinator.plansMutex.Lock()
		for existingID, existing := range coordinator.plans {
			if !plan.expiresAt.Add(-coordinator.planTTL).Before(existing.expiresAt) {
				delete(coordinator.plans, existingID)
			}
		}
		if len(coordinator.plans) >= coordinator.maximumPlans {
			coordinator.plansMutex.Unlock()
			return "", &ProjectRuntimeRepairPlanCapacityError{}
		}
		if _, exists := coordinator.plans[inspectionID]; exists {
			coordinator.plansMutex.Unlock()
			continue
		}
		coordinator.plans[inspectionID] = plan
		coordinator.plansMutex.Unlock()
		return inspectionID, nil
	}
	return "", errors.New("generate unique project runtime repair inspection ID")
}

// takePlan atomically removes one plan so every confirmation attempt is one-use even when later validation fails.
func (coordinator *ProjectRuntimeRepairCoordinator) takePlan(inspectionID ProjectRuntimeRepairInspectionID) (projectRuntimeRepairPlan, bool) {
	coordinator.plansMutex.Lock()
	defer coordinator.plansMutex.Unlock()
	plan, found := coordinator.plans[inspectionID]
	if found {
		delete(coordinator.plans, inspectionID)
	}
	return plan, found
}

// hasPlan reports whether the retained-runtime map currently owns an inspection ID before fallback dispatch.
func (coordinator *ProjectRuntimeRepairCoordinator) hasPlan(inspectionID ProjectRuntimeRepairInspectionID) bool {
	coordinator.plansMutex.Lock()
	defer coordinator.plansMutex.Unlock()
	_, found := coordinator.plans[inspectionID]
	return found
}

// projectRuntimeRepairInspectionFromUnattributed maps the no-session coordinator into the existing client repair contract.
func projectRuntimeRepairInspectionFromUnattributed(
	inspection UnattributedProjectRuntimeInspection,
) (ProjectRuntimeRepairInspection, error) {
	result := ProjectRuntimeRepairInspection{
		ProjectID:   inspection.ProjectID,
		Disposition: ProjectRuntimeRepairInspectionDisposition(inspection.Disposition),
		Reason:      ProjectRuntimeRepairNotActionableReason(inspection.Reason),
	}
	if inspection.Confirmable != nil {
		result.Confirmable = &ProjectRuntimeRepairConfirmable{
			Display:      inspection.Confirmable.Display,
			InspectionID: inspection.Confirmable.InspectionID,
			Fingerprint:  inspection.Confirmable.Fingerprint,
			ExpiresAt:    inspection.Confirmable.ExpiresAt,
		}
	}
	if err := result.Validate(); err != nil {
		return ProjectRuntimeRepairInspection{}, fmt.Errorf("project runtime unattributed inspection projection: %w", err)
	}
	return result, nil
}

// validateProjectRuntimeRepairNativeInspection checks the fixed public shape without requiring access to its private native receipt.
func validateProjectRuntimeRepairNativeInspection(
	inspection projectprocess.RuntimeRepairInspection,
	target projectprocess.RuntimeRepairTarget,
) error {
	if err := inspection.State.Validate(); err != nil {
		return err
	}
	if err := inspection.Diagnostic.Validate(); err != nil {
		return err
	}
	expectedDiagnostic := map[projectprocess.RuntimeRepairInspectionState]projectprocess.RuntimeRepairDiagnostic{
		projectprocess.RuntimeRepairInspectionMissing:     projectprocess.RuntimeRepairDiagnosticListenerMissing,
		projectprocess.RuntimeRepairInspectionAmbiguous:   projectprocess.RuntimeRepairDiagnosticCandidateAmbiguous,
		projectprocess.RuntimeRepairInspectionForeign:     projectprocess.RuntimeRepairDiagnosticForeignOwner,
		projectprocess.RuntimeRepairInspectionUnreadable:  projectprocess.RuntimeRepairDiagnosticNativeUnreadable,
		projectprocess.RuntimeRepairInspectionUnsupported: projectprocess.RuntimeRepairDiagnosticPlatformUnsupported,
		projectprocess.RuntimeRepairInspectionActionable:  projectprocess.RuntimeRepairDiagnosticCandidateExact,
	}[inspection.State]
	if inspection.Diagnostic != expectedDiagnostic {
		return errors.New("native runtime repair inspection diagnostic does not match its state")
	}
	if inspection.State != projectprocess.RuntimeRepairInspectionActionable {
		if inspection.Candidate != nil {
			return errors.New("non-actionable native runtime repair inspection contains candidate authority")
		}
		return nil
	}
	if inspection.Candidate == nil {
		return errors.New("actionable native runtime repair inspection is missing its candidate")
	}
	if inspection.Candidate.Fingerprint == "" {
		return errors.New("actionable native runtime repair candidate fingerprint is empty")
	}
	if err := validateProjectRuntimeRepairOpaqueHex("native runtime repair candidate fingerprint", inspection.Candidate.Fingerprint); err != nil {
		return err
	}
	if err := inspection.Candidate.Display.Validate(); err != nil {
		return err
	}
	if inspection.Candidate.Display.CheckoutRoot != target.CheckoutRoot || inspection.Candidate.Display.Endpoint != target.Endpoint {
		return errors.New("native runtime repair candidate display differs from the daemon-derived target")
	}
	return nil
}

// safeProjectRuntimeRepairDisplay narrows native display integers before returning only receipt-free facts.
func safeProjectRuntimeRepairDisplay(display projectprocess.RuntimeRepairDisplay) (ProjectRuntimeRepairDisplay, error) {
	if display.RootPID <= 0 || display.RootPID > math.MaxUint32 || display.ProcessCount <= 0 || uint64(display.ProcessCount) > math.MaxUint32 {
		return ProjectRuntimeRepairDisplay{}, errors.New("native runtime repair display exceeds the safe projection range")
	}
	projected := ProjectRuntimeRepairDisplay{
		RootPID:      uint32(display.RootPID),
		Command:      display.Command,
		CheckoutRoot: display.CheckoutRoot,
		Endpoint:     display.Endpoint,
		ProcessCount: uint32(display.ProcessCount),
	}
	if err := projected.Validate(); err != nil {
		return ProjectRuntimeRepairDisplay{}, err
	}
	return projected, nil
}

// projectRuntimeRepairBoundariesEqual requires every durable project, session, marker, network, and lease fact to remain exact.
func projectRuntimeRepairBoundariesEqual(
	current state.RetainedProjectRuntimeRepairBoundary,
	inspected state.RetainedProjectRuntimeRepairBoundary,
) bool {
	return reflect.DeepEqual(current, inspected)
}

// projectRuntimeRepairProcessBoundariesEqual requires every process-backed durable fence to remain exact.
func projectRuntimeRepairProcessBoundariesEqual(
	current state.ProcessBackedProjectRuntimeRepairBoundary,
	inspected state.ProcessBackedProjectRuntimeRepairBoundary,
) bool {
	return reflect.DeepEqual(current, inspected)
}

// retainedProjectRuntimeRepairBoundaryFromProcessBacked maps shared durable fences without treating the receipt as runtime health.
func retainedProjectRuntimeRepairBoundaryFromProcessBacked(
	boundary state.ProcessBackedProjectRuntimeRepairBoundary,
) state.RetainedProjectRuntimeRepairBoundary {
	return state.RetainedProjectRuntimeRepairBoundary{
		Project: boundary.Project, SessionID: boundary.SessionID, SessionGeneration: boundary.SessionGeneration,
		SessionUpdatedAt: boundary.SessionUpdatedAt, RecoveryOperation: boundary.RecoveryOperation,
		NetworkRevision: boundary.NetworkRevision, NetworkUpdatedAt: boundary.NetworkUpdatedAt,
		PrimaryLease: boundary.PrimaryLease, PrimaryLeaseGeneration: boundary.PrimaryLeaseGeneration,
	}
}

// cloneProcessBackedProjectRuntimeRepairBoundary isolates the exact process receipt held by a process-local plan.
func cloneProcessBackedProjectRuntimeRepairBoundary(
	boundary *state.ProcessBackedProjectRuntimeRepairBoundary,
) *state.ProcessBackedProjectRuntimeRepairBoundary {
	if boundary == nil {
		return nil
	}
	clone := *boundary
	clone.Project.Project.Apps = make([]domain.AppSnapshot, len(boundary.Project.Project.Apps))
	copy(clone.Project.Project.Apps, boundary.Project.Project.Apps)
	clone.Project.Project.Services = make([]domain.ServiceSnapshot, len(boundary.Project.Project.Services))
	copy(clone.Project.Project.Services, boundary.Project.Project.Services)
	clone.Project.Project.Resources = make([]domain.ResourceSnapshot, len(boundary.Project.Project.Resources))
	copy(clone.Project.Project.Resources, boundary.Project.Project.Resources)
	if clone.RecoveryOperation.Operation.StartedAt != nil {
		startedAt := *clone.RecoveryOperation.Operation.StartedAt
		clone.RecoveryOperation.Operation.StartedAt = &startedAt
	}
	if clone.RecoveryOperation.Operation.FinishedAt != nil {
		finishedAt := *clone.RecoveryOperation.Operation.FinishedAt
		clone.RecoveryOperation.Operation.FinishedAt = &finishedAt
	}
	if clone.RecoveryOperation.Operation.Problem != nil {
		problem := *clone.RecoveryOperation.Operation.Problem
		clone.RecoveryOperation.Operation.Problem = &problem
	}
	return &clone
}

// isMissingProcessBackedRuntimeBoundary selects the legacy receipt-free path only when no complete process receipt exists.
func isMissingProcessBackedRuntimeBoundary(err error) bool {
	var missing *state.ProjectSessionProcessEvidenceMissingError
	var notFound *state.ProjectSessionNotFoundError
	return errors.As(err, &missing) || errors.As(err, &notFound)
}

// projectRuntimeRepairCompletionRequest copies every finalization fence from the process-local inspected boundary.
func projectRuntimeRepairCompletionRequest(
	boundary state.RetainedProjectRuntimeRepairBoundary,
	at time.Time,
) state.CompleteRetainedProjectRuntimeRepairRequest {
	return state.CompleteRetainedProjectRuntimeRepairRequest{
		ProjectID:                         boundary.Project.Project.ID,
		ExpectedProjectRevision:           boundary.Project.Revision,
		SessionID:                         boundary.SessionID,
		ExpectedSessionGeneration:         boundary.SessionGeneration,
		ExpectedSessionUpdatedAt:          boundary.SessionUpdatedAt,
		ExpectedRecoveryOperationID:       boundary.RecoveryOperation.Operation.ID,
		ExpectedRecoveryOperationRevision: boundary.RecoveryOperation.Revision,
		ExpectedNetworkRevision:           boundary.NetworkRevision,
		ExpectedNetworkUpdatedAt:          boundary.NetworkUpdatedAt,
		ExpectedPrimaryLease:              boundary.PrimaryLease,
		ExpectedPrimaryLeaseGeneration:    boundary.PrimaryLeaseGeneration,
		At:                                at,
	}
}

// processBackedProjectRuntimeRepairCompletionRequest copies every process-backed durable fence into one completion request.
func processBackedProjectRuntimeRepairCompletionRequest(
	boundary state.ProcessBackedProjectRuntimeRepairBoundary,
	at time.Time,
) state.CompleteProcessBackedProjectRuntimeRepairRequest {
	return state.CompleteProcessBackedProjectRuntimeRepairRequest{
		CompleteRetainedProjectRuntimeRepairRequest: projectRuntimeRepairCompletionRequest(
			retainedProjectRuntimeRepairBoundaryFromProcessBacked(boundary), at,
		),
		ExpectedProcess: boundary.Process,
	}
}

// completionTimeForProjectRuntimeRepair clamps the UTC completion time to every timestamp retained as a plan fence.
func completionTimeForProjectRuntimeRepair(
	boundary state.RetainedProjectRuntimeRepairBoundary,
	now time.Time,
) time.Time {
	at := now.UTC().Round(0)
	fences := []time.Time{
		boundary.Project.Project.UpdatedAt,
		boundary.SessionUpdatedAt,
		boundary.NetworkUpdatedAt,
		boundary.RecoveryOperation.Operation.RequestedAt,
	}
	if boundary.RecoveryOperation.Operation.StartedAt != nil {
		fences = append(fences, *boundary.RecoveryOperation.Operation.StartedAt)
	}
	if boundary.RecoveryOperation.Operation.FinishedAt != nil {
		fences = append(fences, *boundary.RecoveryOperation.Operation.FinishedAt)
	}
	for _, fence := range fences {
		if fence.After(at) {
			at = fence.UTC().Round(0)
		}
	}
	return at.UTC().Round(0)
}

// newProjectRuntimeRepairInspectionID reads exactly 32 random bytes and returns their canonical lowercase encoding.
func newProjectRuntimeRepairInspectionID(random io.Reader) (ProjectRuntimeRepairInspectionID, error) {
	bytes := make([]byte, projectRuntimeRepairOpaqueHexLength/2)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", fmt.Errorf("read project runtime repair inspection entropy: %w", err)
	}
	return ProjectRuntimeRepairInspectionID(hex.EncodeToString(bytes)), nil
}

// validateProjectRuntimeRepairOpaqueHex rejects truncated, padded, uppercase, or non-hex plan selectors.
func validateProjectRuntimeRepairOpaqueHex(name string, value string) error {
	if len(value) != projectRuntimeRepairOpaqueHexLength {
		return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", name, projectRuntimeRepairOpaqueHexLength)
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("%s must contain %d lowercase hexadecimal characters", name, projectRuntimeRepairOpaqueHexLength)
		}
	}
	return nil
}

// validateProjectRuntimeRepairPlanSelection validates enough identity to atomically consume a plan before comparing its fingerprint.
func validateProjectRuntimeRepairPlanSelection(request ProjectRuntimeRepairConfirmRequest) error {
	if err := request.Caller.Validate(); err != nil {
		return err
	}
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	return request.InspectionID.Validate()
}

// newProjectRuntimeRepairNativeFailure preserves backend diagnostics and signal phase only inside daemon reconciliation.
func newProjectRuntimeRepairNativeFailure(signaled bool, cause error) error {
	if cause == nil {
		cause = errors.New("native runtime repair confirmation failed without a diagnostic cause")
	}
	return &ProjectRuntimeRepairNativeFailureError{cause: cause, signaled: signaled}
}

// isProjectRuntimeRepairContextError keeps cancellation distinct from fresh-inspection drift semantics.
func isProjectRuntimeRepairContextError(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// normalizeProjectRuntimeRepairContext permits nil contexts without weakening cancellation on real requests.
func normalizeProjectRuntimeRepairContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
