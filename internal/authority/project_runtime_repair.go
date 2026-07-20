package authority

import (
	"context"
	"errors"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/projectprocess"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// projectRuntimeRepairCoordinator limits authority to the caller-bound retained-runtime repair workflow.
type projectRuntimeRepairCoordinator interface {
	Inspect(context.Context, reconcile.ProjectRuntimeRepairInspectRequest) (reconcile.ProjectRuntimeRepairInspection, error)
	Confirm(context.Context, reconcile.ProjectRuntimeRepairConfirmRequest) (state.ProjectRecord, error)
}

// unsupportedProjectRuntimeRepairCoordinator keeps private authority constructors deterministic outside production assembly.
type unsupportedProjectRuntimeRepairCoordinator struct{}

// Inspect returns the explicit unsupported shape used by authority tests that do not exercise native repair.
func (unsupportedProjectRuntimeRepairCoordinator) Inspect(
	ctx context.Context,
	request reconcile.ProjectRuntimeRepairInspectRequest,
) (reconcile.ProjectRuntimeRepairInspection, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return reconcile.ProjectRuntimeRepairInspection{}, err
	}
	if err := request.Validate(); err != nil {
		return reconcile.ProjectRuntimeRepairInspection{}, err
	}
	return reconcile.ProjectRuntimeRepairInspection{
		ProjectID:   request.ProjectID,
		Disposition: reconcile.ProjectRuntimeRepairInspectionUnsupported,
	}, nil
}

// Confirm rejects confirmation because an unsupported inspection never creates a process-local plan.
func (unsupportedProjectRuntimeRepairCoordinator) Confirm(
	ctx context.Context,
	request reconcile.ProjectRuntimeRepairConfirmRequest,
) (state.ProjectRecord, error) {
	ctx = normalizeContext(ctx)
	if err := ctx.Err(); err != nil {
		return state.ProjectRecord{}, err
	}
	if err := request.Validate(); err != nil {
		return state.ProjectRecord{}, err
	}
	return state.ProjectRecord{}, &reconcile.ProjectRuntimeRepairPlanNotFoundError{}
}

// InspectProjectRuntimeRepair returns only the bounded display projection from one caller-bound daemon inspection.
func (authority *Authority) InspectProjectRuntimeRepair(
	ctx context.Context,
	caller control.Caller,
	request control.InspectProjectRuntimeRepairRequest,
) (control.ProjectRuntimeRepairInspection, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectRuntimeRepairInspection{}, control.NewProjectRuntimeRepairInvalidError(err)
	}
	inspection, err := authority.runtimeRepair.Inspect(ctx, reconcile.ProjectRuntimeRepairInspectRequest{
		Caller:    projectRuntimeRepairCaller(caller),
		ProjectID: request.ProjectID,
	})
	if err != nil {
		return control.ProjectRuntimeRepairInspection{}, classifyProjectRuntimeRepairError(err)
	}
	result := control.ProjectRuntimeRepairInspection{ProjectID: inspection.ProjectID}
	switch inspection.Disposition {
	case reconcile.ProjectRuntimeRepairInspectionUnsupported:
		result.Disposition = control.ProjectRuntimeRepairInspectionUnsupported
	case reconcile.ProjectRuntimeRepairInspectionNotActionable:
		result.Disposition = control.ProjectRuntimeRepairInspectionNotActionable
		result.Reason = control.ProjectRuntimeRepairNotActionableReason(inspection.Reason)
	case reconcile.ProjectRuntimeRepairInspectionConfirmable:
		result.Disposition = control.ProjectRuntimeRepairInspectionConfirmable
		result.Confirmable = projectRuntimeRepairConfirmable(inspection.Confirmable)
	default:
		return control.ProjectRuntimeRepairInspection{}, errors.New("project runtime repair coordinator returned an unsupported inspection disposition")
	}
	if err := result.Validate(); err != nil {
		return control.ProjectRuntimeRepairInspection{}, err
	}
	return result, nil
}

// ConfirmProjectRuntimeRepair consumes one opaque plan and returns only its atomically stopped project projection.
func (authority *Authority) ConfirmProjectRuntimeRepair(
	ctx context.Context,
	caller control.Caller,
	request control.ConfirmProjectRuntimeRepairRequest,
) (control.ProjectRuntimeRepairConfirmation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectRuntimeRepairConfirmation{}, control.NewProjectRuntimeRepairInvalidError(err)
	}
	project, err := authority.runtimeRepair.Confirm(ctx, reconcile.ProjectRuntimeRepairConfirmRequest{
		Caller:       projectRuntimeRepairCaller(caller),
		ProjectID:    request.ProjectID,
		InspectionID: reconcile.ProjectRuntimeRepairInspectionID(request.InspectionID),
		Fingerprint:  reconcile.ProjectRuntimeRepairCandidateFingerprint(request.Fingerprint),
	})
	if err != nil {
		return control.ProjectRuntimeRepairConfirmation{}, classifyProjectRuntimeRepairError(err)
	}
	result := control.ProjectRuntimeRepairConfirmation{Project: project.Project, Revision: project.Revision}
	if err := result.Validate(); err != nil {
		return control.ProjectRuntimeRepairConfirmation{}, err
	}
	return result, nil
}

// projectRuntimeRepairCaller preserves both authenticated identity layers in every process-local plan.
func projectRuntimeRepairCaller(caller control.Caller) reconcile.ProjectRuntimeRepairCaller {
	return reconcile.ProjectRuntimeRepairCaller{
		UserID:    caller.Transport.UserID,
		ProcessID: caller.Transport.ProcessID,
		Role:      caller.Session.Role,
	}
}

// projectRuntimeRepairConfirmable maps only display facts and opaque selectors, never the native candidate receipt.
func projectRuntimeRepairConfirmable(
	confirmable *reconcile.ProjectRuntimeRepairConfirmable,
) *control.ProjectRuntimeRepairConfirmable {
	if confirmable == nil {
		return nil
	}
	return &control.ProjectRuntimeRepairConfirmable{
		Candidate: control.ProjectRuntimeRepairDisplayFacts{
			Command:     confirmable.Display.Command,
			Checkout:    confirmable.Display.CheckoutRoot,
			Endpoint:    confirmable.Display.Endpoint.String(),
			RootPID:     confirmable.Display.RootPID,
			MemberCount: confirmable.Display.ProcessCount,
		},
		InspectionID:         control.ProjectRuntimeRepairInspectionID(confirmable.InspectionID),
		CandidateFingerprint: control.ProjectRuntimeRepairCandidateFingerprint(confirmable.Fingerprint),
		ExpiresAt:            confirmable.ExpiresAt,
	}
}

// classifyProjectRuntimeRepairError exposes only stable request, absence, and fresh-inspection classifications.
func classifyProjectRuntimeRepairError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var projectMissing *state.ProjectNotFoundError
	if errors.As(err, &projectMissing) {
		return control.NewProjectRuntimeRepairNotFoundError(err)
	}
	if projectRuntimeRepairConflict(err) {
		return control.NewProjectRuntimeRepairConflictError(err)
	}
	return err
}

// projectRuntimeRepairConflict recognizes every consumed, stale, bounded, or native plan outcome safe to retry by inspection.
func projectRuntimeRepairConflict(err error) bool {
	var planMissing *reconcile.ProjectRuntimeRepairPlanNotFoundError
	var planMismatch *reconcile.ProjectRuntimeRepairPlanMismatchError
	var planExpired *reconcile.ProjectRuntimeRepairPlanExpiredError
	var planCapacity *reconcile.ProjectRuntimeRepairPlanCapacityError
	var durableDrift *reconcile.ProjectRuntimeRepairDurableDriftError
	var discoveryDrift *reconcile.ProjectRuntimeRepairDiscoveryDriftError
	var nativeDrift *reconcile.ProjectRuntimeRepairNativeDriftError
	var nativeFailure *reconcile.ProjectRuntimeRepairNativeFailureError
	return errors.As(err, &planMissing) ||
		errors.As(err, &planMismatch) ||
		errors.As(err, &planExpired) ||
		errors.As(err, &planCapacity) ||
		errors.As(err, &durableDrift) ||
		errors.As(err, &discoveryDrift) ||
		errors.As(err, &nativeDrift) ||
		errors.As(err, &nativeFailure) ||
		errors.Is(err, projectprocess.ErrRuntimeRepairDrift) ||
		errors.Is(err, projectprocess.ErrRuntimeRepairNotSettled)
}
