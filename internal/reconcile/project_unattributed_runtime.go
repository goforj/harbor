package reconcile

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"reflect"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/state"
)

// UnattributedProjectRuntimeInspectionDisposition identifies the fixed result shapes for an already-retired listener.
type UnattributedProjectRuntimeInspectionDisposition string

const (
	// UnattributedProjectRuntimeInspectionConfirmable means one exact candidate is retained for explicit confirmation.
	UnattributedProjectRuntimeInspectionConfirmable UnattributedProjectRuntimeInspectionDisposition = "confirmable"
	// UnattributedProjectRuntimeInspectionNotActionable means native evidence did not isolate one safe scope.
	UnattributedProjectRuntimeInspectionNotActionable UnattributedProjectRuntimeInspectionDisposition = "not_actionable"
	// UnattributedProjectRuntimeInspectionUnsupported means the current platform has no reviewed backend.
	UnattributedProjectRuntimeInspectionUnsupported UnattributedProjectRuntimeInspectionDisposition = "unsupported"
)

// UnattributedProjectRuntimeInspectRequest selects one project while leaving all host derivation to the daemon.
type UnattributedProjectRuntimeInspectRequest struct {
	// Caller binds the process-local plan to the authenticated client.
	Caller ProjectRuntimeRepairCaller
	// ProjectID identifies the route-free project with no durable session.
	ProjectID domain.ProjectID
}

// Validate reports whether one caller and project identify a valid inspection request.
func (request UnattributedProjectRuntimeInspectRequest) Validate() error {
	if err := request.Caller.Validate(); err != nil {
		return err
	}
	return request.ProjectID.Validate()
}

// UnattributedProjectRuntimeConfirmRequest selects only the opaque result of one prior inspection.
type UnattributedProjectRuntimeConfirmRequest struct {
	// Caller must exactly match the inspection caller.
	Caller ProjectRuntimeRepairCaller
	// ProjectID must exactly match the inspected project.
	ProjectID domain.ProjectID
	// InspectionID selects one one-use daemon plan.
	InspectionID ProjectRuntimeRepairInspectionID
	// Fingerprint binds confirmation to the displayed native scope.
	Fingerprint ProjectRuntimeRepairCandidateFingerprint
}

// Validate reports whether the confirmation request contains only bounded opaque selection data.
func (request UnattributedProjectRuntimeConfirmRequest) Validate() error {
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

// UnattributedProjectRuntimeInspection is a receipt-free result for an already-retired listener.
type UnattributedProjectRuntimeInspection struct {
	// ProjectID identifies the project whose durable boundary was inspected.
	ProjectID domain.ProjectID
	// Disposition identifies the exact result shape.
	Disposition UnattributedProjectRuntimeInspectionDisposition
	// Confirmable contains only bounded display facts and opaque selectors.
	Confirmable *ProjectRuntimeRepairConfirmable
	// Reason explains a supported non-actionable native result.
	Reason ProjectRuntimeRepairNotActionableReason
}

// Validate reports whether the inspection contains exactly the fields allowed by its disposition.
func (inspection UnattributedProjectRuntimeInspection) Validate() error {
	if err := inspection.ProjectID.Validate(); err != nil {
		return err
	}
	switch inspection.Disposition {
	case UnattributedProjectRuntimeInspectionConfirmable:
		if inspection.Confirmable == nil || inspection.Reason != "" {
			return errors.New("confirmable unattributed runtime inspection requires only candidate details")
		}
		return inspection.Confirmable.Validate()
	case UnattributedProjectRuntimeInspectionNotActionable:
		if inspection.Confirmable != nil {
			return errors.New("non-actionable unattributed runtime inspection must not contain candidate details")
		}
		return inspection.Reason.Validate()
	case UnattributedProjectRuntimeInspectionUnsupported:
		if inspection.Confirmable != nil || inspection.Reason != "" {
			return errors.New("unsupported unattributed runtime inspection must not contain candidate details or a reason")
		}
		return nil
	default:
		return fmt.Errorf("unknown unattributed runtime inspection disposition %q", inspection.Disposition)
	}
}

// UnattributedProjectRuntimeConfirmation returns the unchanged durable project after native scope settlement.
type UnattributedProjectRuntimeConfirmation struct {
	// Project is the final read-only durable projection; confirmation never mutates it.
	Project state.ProjectRecord
}

// Validate reports whether confirmation returns one still-retryable route-free project projection.
func (confirmation UnattributedProjectRuntimeConfirmation) Validate() error {
	if err := confirmation.Project.Validate(); err != nil {
		return err
	}
	return validateUnattributedProject(confirmation.Project)
}

// unattributedProjectRuntimeStore reads the durable no-session boundary without exposing mutation authority.
type unattributedProjectRuntimeStore interface {
	UnattributedProjectRuntimeInspectionBoundary(context.Context, domain.ProjectID) (state.UnattributedProjectRuntimeInspectionBoundary, error)
}

// unattributedProjectRuntimePlan retains every fence needed between inspect and confirm.
type unattributedProjectRuntimePlan struct {
	caller      ProjectRuntimeRepairCaller
	projectID   domain.ProjectID
	boundary    state.UnattributedProjectRuntimeInspectionBoundary
	target      projectprocess.RuntimeRepairTarget
	candidate   projectprocess.UnattributedRuntimeCandidate
	fingerprint ProjectRuntimeRepairCandidateFingerprint
	expiresAt   time.Time
}

// UnattributedProjectRuntimeCoordinator owns caller-bound one-use plans without durable completion mutation.
type UnattributedProjectRuntimeCoordinator struct {
	store           unattributedProjectRuntimeStore
	discoverer      projectRuntimeRepairDiscoverer
	repairer        projectprocess.UnattributedRuntimeRepairer
	now             func() time.Time
	random          io.Reader
	planTTL         time.Duration
	maximumPlans    int
	inspectionMutex sync.Mutex
	plansMutex      sync.Mutex
	plans           map[ProjectRuntimeRepairInspectionID]unattributedProjectRuntimePlan
}

// NewUnattributedProjectRuntimeCoordinator creates the production no-session inspection authority.
func NewUnattributedProjectRuntimeCoordinator(store *state.Store) *UnattributedProjectRuntimeCoordinator {
	if store == nil {
		panic("reconcile.NewUnattributedProjectRuntimeCoordinator requires non-nil state")
	}
	return newUnattributedProjectRuntimeCoordinator(
		store,
		projectdiscovery.NewDiscoverer(),
		projectprocess.NewUnattributedRuntimeRepairer(),
		time.Now,
		rand.Reader,
		defaultProjectRuntimeRepairPlanTTL,
		defaultProjectRuntimeRepairMaximumPlans,
	)
}

// newUnattributedProjectRuntimeCoordinator injects durable, discovery, native, time, and entropy seams for tests.
func newUnattributedProjectRuntimeCoordinator(
	store unattributedProjectRuntimeStore,
	discoverer projectRuntimeRepairDiscoverer,
	repairer projectprocess.UnattributedRuntimeRepairer,
	now func() time.Time,
	random io.Reader,
	planTTL time.Duration,
	maximumPlans int,
) *UnattributedProjectRuntimeCoordinator {
	if nilDependency(store) || nilDependency(discoverer) || nilDependency(repairer) || nilDependency(now) || nilDependency(random) {
		panic("reconcile.newUnattributedProjectRuntimeCoordinator requires every dependency")
	}
	if planTTL <= 0 || planTTL > defaultProjectRuntimeRepairPlanTTL {
		panic("reconcile.newUnattributedProjectRuntimeCoordinator requires a positive bounded plan TTL")
	}
	if maximumPlans <= 0 || maximumPlans > defaultProjectRuntimeRepairMaximumPlans {
		panic("reconcile.newUnattributedProjectRuntimeCoordinator requires a positive bounded plan capacity")
	}
	return &UnattributedProjectRuntimeCoordinator{
		store: store, discoverer: discoverer, repairer: repairer, now: now, random: random,
		planTTL: planTTL, maximumPlans: maximumPlans,
		plans: make(map[ProjectRuntimeRepairInspectionID]unattributedProjectRuntimePlan),
	}
}

// Inspect invalidates prior plans and derives one fresh durable and native no-session inspection.
func (coordinator *UnattributedProjectRuntimeCoordinator) Inspect(
	ctx context.Context,
	request UnattributedProjectRuntimeInspectRequest,
) (UnattributedProjectRuntimeInspection, error) {
	if coordinator == nil {
		panic("reconcile.UnattributedProjectRuntimeCoordinator.Inspect requires a non-nil receiver")
	}
	ctx = normalizeProjectRuntimeRepairContext(ctx)
	if err := request.Validate(); err != nil {
		return UnattributedProjectRuntimeInspection{}, err
	}
	if err := ctx.Err(); err != nil {
		return UnattributedProjectRuntimeInspection{}, err
	}
	coordinator.inspectionMutex.Lock()
	defer coordinator.inspectionMutex.Unlock()
	coordinator.invalidatePlans(request.ProjectID, coordinator.now().UTC().Round(0))

	boundary, err := coordinator.store.UnattributedProjectRuntimeInspectionBoundary(ctx, request.ProjectID)
	if err != nil {
		return UnattributedProjectRuntimeInspection{}, fmt.Errorf("inspect unattributed project runtime boundary: %w", err)
	}
	if err := boundary.Validate(); err != nil {
		return UnattributedProjectRuntimeInspection{}, fmt.Errorf("inspect unattributed project runtime boundary is invalid: %w", err)
	}
	if boundary.Project.Project.ID != request.ProjectID {
		return UnattributedProjectRuntimeInspection{}, errors.New("unattributed runtime boundary belongs to another project")
	}
	target, err := coordinator.discoverTarget(ctx, boundary)
	if err != nil {
		return UnattributedProjectRuntimeInspection{}, err
	}
	native, err := coordinator.repairer.Inspect(ctx, target)
	if err != nil {
		return UnattributedProjectRuntimeInspection{}, fmt.Errorf("inspect native unattributed runtime: %w", err)
	}
	if err := validateUnattributedNativeInspection(native, target); err != nil {
		return UnattributedProjectRuntimeInspection{}, fmt.Errorf("inspect native unattributed runtime result: %w", err)
	}
	return coordinator.projectInspection(request, boundary, target, native)
}

// Confirm consumes one plan, revalidates every fence, settles the exact scope, and performs no Harbor state mutation.
func (coordinator *UnattributedProjectRuntimeCoordinator) Confirm(
	ctx context.Context,
	request UnattributedProjectRuntimeConfirmRequest,
) (UnattributedProjectRuntimeConfirmation, error) {
	if coordinator == nil {
		panic("reconcile.UnattributedProjectRuntimeCoordinator.Confirm requires a non-nil receiver")
	}
	ctx = normalizeProjectRuntimeRepairContext(ctx)
	if err := validateUnattributedProjectRuntimePlanSelection(request); err != nil {
		return UnattributedProjectRuntimeConfirmation{}, err
	}
	if err := ctx.Err(); err != nil {
		return UnattributedProjectRuntimeConfirmation{}, err
	}
	plan, found := coordinator.takePlan(request.InspectionID)
	if !found {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairPlanNotFoundError{}
	}
	if request.Fingerprint.Validate() != nil || plan.caller != request.Caller || plan.projectID != request.ProjectID || subtle.ConstantTimeCompare([]byte(plan.fingerprint), []byte(request.Fingerprint)) != 1 {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairPlanMismatchError{}
	}
	if !coordinator.now().UTC().Round(0).Before(plan.expiresAt) {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairPlanExpiredError{}
	}

	boundary, err := coordinator.store.UnattributedProjectRuntimeInspectionBoundary(ctx, request.ProjectID)
	if err != nil {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
	}
	if err := boundary.Validate(); err != nil {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
	}
	if !reflect.DeepEqual(boundary, plan.boundary) {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDurableDriftError{cause: errors.New("unattributed runtime durable boundary changed after inspection")}
	}
	target, err := coordinator.discoverTarget(ctx, boundary)
	if err != nil {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDiscoveryDriftError{cause: err}
	}
	if target != plan.target {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDiscoveryDriftError{}
	}
	confirmation, confirmErr := coordinator.repairer.Confirm(ctx, plan.candidate.Clone())
	if err := confirmation.Validate(); err != nil {
		return UnattributedProjectRuntimeConfirmation{}, newProjectRuntimeRepairNativeFailure(confirmation.Signaled, errors.Join(confirmErr, err))
	}
	if confirmErr != nil {
		return UnattributedProjectRuntimeConfirmation{}, newProjectRuntimeRepairNativeFailure(confirmation.Signaled, confirmErr)
	}
	switch confirmation.State {
	case projectprocess.RuntimeRepairConfirmationDrifted:
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairNativeDriftError{}
	case projectprocess.RuntimeRepairConfirmationFailed:
		return UnattributedProjectRuntimeConfirmation{}, newProjectRuntimeRepairNativeFailure(confirmation.Signaled, errors.New("native unattributed runtime confirmation returned a failed state"))
	case projectprocess.RuntimeRepairConfirmationSettled:
	default:
		return UnattributedProjectRuntimeConfirmation{}, newProjectRuntimeRepairNativeFailure(confirmation.Signaled, fmt.Errorf("native unattributed runtime confirmation returned unknown state %q", confirmation.State))
	}

	finalBoundary, err := coordinator.store.UnattributedProjectRuntimeInspectionBoundary(ctx, request.ProjectID)
	if err != nil {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
	}
	if err := finalBoundary.Validate(); err != nil {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDurableDriftError{cause: err}
	}
	if !reflect.DeepEqual(finalBoundary, plan.boundary) {
		return UnattributedProjectRuntimeConfirmation{}, &ProjectRuntimeRepairDurableDriftError{cause: errors.New("unattributed runtime durable boundary changed after settlement")}
	}
	result := UnattributedProjectRuntimeConfirmation{Project: finalBoundary.Project}
	if err := result.Validate(); err != nil {
		return UnattributedProjectRuntimeConfirmation{}, fmt.Errorf("validate unattributed runtime confirmation: %w", err)
	}
	return result, nil
}

// discoverTarget derives the exact App endpoint from the retained primary lease.
func (coordinator *UnattributedProjectRuntimeCoordinator) discoverTarget(
	ctx context.Context,
	boundary state.UnattributedProjectRuntimeInspectionBoundary,
) (projectprocess.RuntimeRepairTarget, error) {
	discovered, err := coordinator.discoverer.DiscoverDefaultRuntimeAtAddress(ctx, boundary.Project.Project.Path, boundary.PrimaryLease.Address)
	if err != nil {
		return projectprocess.RuntimeRepairTarget{}, fmt.Errorf("discover unattributed project runtime target: %w", err)
	}
	if err := discovered.Validate(); err != nil {
		return projectprocess.RuntimeRepairTarget{}, fmt.Errorf("discovered unattributed project runtime target is invalid: %w", err)
	}
	if discovered.Address != boundary.PrimaryLease.Address {
		return projectprocess.RuntimeRepairTarget{}, errors.New("discovered unattributed project runtime target differs from the durable primary lease")
	}
	target := projectprocess.RuntimeRepairTarget{CheckoutRoot: boundary.Project.Project.Path, Endpoint: netip.AddrPortFrom(discovered.Address, discovered.Port)}
	if err := target.Validate(); err != nil {
		return projectprocess.RuntimeRepairTarget{}, fmt.Errorf("derive unattributed project runtime target: %w", err)
	}
	return target, nil
}

// projectInspection maps native state into a receipt-free, caller-bound result.
func (coordinator *UnattributedProjectRuntimeCoordinator) projectInspection(
	request UnattributedProjectRuntimeInspectRequest,
	boundary state.UnattributedProjectRuntimeInspectionBoundary,
	target projectprocess.RuntimeRepairTarget,
	native projectprocess.UnattributedRuntimeInspection,
) (UnattributedProjectRuntimeInspection, error) {
	result := UnattributedProjectRuntimeInspection{ProjectID: request.ProjectID}
	switch native.State {
	case projectprocess.RuntimeRepairInspectionMissing:
		result.Disposition, result.Reason = UnattributedProjectRuntimeInspectionNotActionable, ProjectRuntimeRepairReasonNone
	case projectprocess.RuntimeRepairInspectionAmbiguous:
		result.Disposition, result.Reason = UnattributedProjectRuntimeInspectionNotActionable, ProjectRuntimeRepairReasonAmbiguous
	case projectprocess.RuntimeRepairInspectionForeign:
		result.Disposition, result.Reason = UnattributedProjectRuntimeInspectionNotActionable, ProjectRuntimeRepairReasonForeign
	case projectprocess.RuntimeRepairInspectionUnreadable:
		result.Disposition, result.Reason = UnattributedProjectRuntimeInspectionNotActionable, ProjectRuntimeRepairReasonUnreadable
	case projectprocess.RuntimeRepairInspectionUnsupported:
		result.Disposition = UnattributedProjectRuntimeInspectionUnsupported
	case projectprocess.RuntimeRepairInspectionActionable:
		candidate := native.Candidate.Clone()
		display, err := safeProjectRuntimeRepairDisplay(candidate.Display)
		if err != nil {
			return UnattributedProjectRuntimeInspection{}, err
		}
		expiresAt := coordinator.now().UTC().Round(0).Add(coordinator.planTTL)
		plan := unattributedProjectRuntimePlan{caller: request.Caller, projectID: request.ProjectID, boundary: boundary, target: target, candidate: candidate, fingerprint: ProjectRuntimeRepairCandidateFingerprint(candidate.Fingerprint), expiresAt: expiresAt}
		inspectionID, err := coordinator.storePlan(plan)
		if err != nil {
			return UnattributedProjectRuntimeInspection{}, err
		}
		result.Disposition = UnattributedProjectRuntimeInspectionConfirmable
		result.Confirmable = &ProjectRuntimeRepairConfirmable{Display: display, InspectionID: inspectionID, Fingerprint: plan.fingerprint, ExpiresAt: expiresAt}
	default:
		return UnattributedProjectRuntimeInspection{}, fmt.Errorf("cannot map native unattributed runtime inspection state %q", native.State)
	}
	if err := result.Validate(); err != nil {
		return UnattributedProjectRuntimeInspection{}, fmt.Errorf("unattributed runtime inspection projection: %w", err)
	}
	return result, nil
}

// invalidatePlans removes expired plans and every older plan for the selected project.
func (coordinator *UnattributedProjectRuntimeCoordinator) invalidatePlans(projectID domain.ProjectID, now time.Time) {
	coordinator.plansMutex.Lock()
	defer coordinator.plansMutex.Unlock()
	for id, plan := range coordinator.plans {
		if plan.projectID == projectID || !now.Before(plan.expiresAt) {
			delete(coordinator.plans, id)
		}
	}
}

// storePlan inserts one bounded plan without evicting a valid plan for another project.
func (coordinator *UnattributedProjectRuntimeCoordinator) storePlan(plan unattributedProjectRuntimePlan) (ProjectRuntimeRepairInspectionID, error) {
	for attempt := 0; attempt < projectRuntimeRepairIDAttempts; attempt++ {
		id, err := newProjectRuntimeRepairInspectionID(coordinator.random)
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
		if _, exists := coordinator.plans[id]; exists {
			coordinator.plansMutex.Unlock()
			continue
		}
		coordinator.plans[id] = plan
		coordinator.plansMutex.Unlock()
		return id, nil
	}
	return "", errors.New("generate unique unattributed runtime inspection ID")
}

// takePlan atomically removes one plan so confirmation is one-use even when later fences fail.
func (coordinator *UnattributedProjectRuntimeCoordinator) takePlan(id ProjectRuntimeRepairInspectionID) (unattributedProjectRuntimePlan, bool) {
	coordinator.plansMutex.Lock()
	defer coordinator.plansMutex.Unlock()
	plan, found := coordinator.plans[id]
	if found {
		delete(coordinator.plans, id)
	}
	return plan, found
}

// hasPlan reports whether the no-session map currently owns an inspection ID before fallback dispatch.
func (coordinator *UnattributedProjectRuntimeCoordinator) hasPlan(id ProjectRuntimeRepairInspectionID) bool {
	coordinator.plansMutex.Lock()
	defer coordinator.plansMutex.Unlock()
	_, found := coordinator.plans[id]
	return found
}

// validateUnattributedNativeInspection checks the receipt-free native shape and target binding.
func validateUnattributedNativeInspection(inspection projectprocess.UnattributedRuntimeInspection, target projectprocess.RuntimeRepairTarget) error {
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
		return errors.New("native unattributed runtime inspection diagnostic does not match its state")
	}
	if inspection.State != projectprocess.RuntimeRepairInspectionActionable {
		if inspection.Candidate != nil {
			return errors.New("non-actionable unattributed runtime inspection contains candidate authority")
		}
		return nil
	}
	if inspection.Candidate == nil {
		return errors.New("actionable unattributed runtime inspection is missing its candidate")
	}
	if err := validateProjectRuntimeRepairOpaqueHex("native unattributed runtime candidate fingerprint", inspection.Candidate.Fingerprint); err != nil {
		return err
	}
	if err := inspection.Candidate.Display.Validate(); err != nil {
		return err
	}
	if inspection.Candidate.Display.CheckoutRoot != target.CheckoutRoot || inspection.Candidate.Display.Endpoint != target.Endpoint {
		return errors.New("native unattributed runtime candidate display differs from the daemon-derived target")
	}
	return nil
}

// validateUnattributedProjectRuntimePlanSelection validates identity before atomically consuming a plan.
func validateUnattributedProjectRuntimePlanSelection(request UnattributedProjectRuntimeConfirmRequest) error {
	if err := request.Caller.Validate(); err != nil {
		return err
	}
	if err := request.ProjectID.Validate(); err != nil {
		return err
	}
	return request.InspectionID.Validate()
}

// validateUnattributedProject keeps confirmation results route-free and retryable without changing their durable state.
func validateUnattributedProject(project state.ProjectRecord) error {
	snapshot := project.Project
	if snapshot.State != domain.ProjectStopped && snapshot.State != domain.ProjectFailed && snapshot.State != domain.ProjectUnavailable {
		return fmt.Errorf("unattributed runtime project must remain retryable, got %q", snapshot.State)
	}
	if len(snapshot.Resources) != 0 {
		return errors.New("unattributed runtime project must remain route-free")
	}
	for _, app := range snapshot.Apps {
		if app.State != domain.EntityStopped || app.Active {
			return errors.New("unattributed runtime project retains an active App")
		}
	}
	for _, service := range snapshot.Services {
		if service.State != domain.EntityStopped {
			return errors.New("unattributed runtime project retains an active service")
		}
	}
	return nil
}
