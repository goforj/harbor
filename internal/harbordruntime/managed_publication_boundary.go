package harbordruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/state"
)

// ManagedPublicationStateSource supplies the durable aggregate and active session used to authorize one publication plan.
type ManagedPublicationStateSource interface {
	RuntimeState(context.Context) (state.RuntimeState, error)
	ActiveProjectSession(context.Context, domain.ProjectID) (domain.ProjectSession, error)
}

// ManagedNativeRoutePlanRequest selects one exact attached session and its observed private endpoint publications.
type ManagedNativeRoutePlanRequest struct {
	Fence        ManagedPublicationFence
	Publications []ManagedEndpointPublication
	// AllowProjectStarting is reserved for the authenticated Compose barrier, before App readiness is complete.
	AllowProjectStarting bool
}

// Validate reports whether a managed native route request contains a complete fence and bounded observations.
func (request ManagedNativeRoutePlanRequest) Validate() error {
	return (ManagedPublicationPlanInput{
		Fence:        request.Fence,
		Publications: request.Publications,
	}).Validate()
}

// PlanVerifiedManagedNativeRoutes plans routes only after Harbor-owned network and session state is revalidated.
//
// The function is deliberately pure: it does not persist private upstreams, mutate the data plane, or claim a
// caller-supplied reservation. A second durable read fences the returned plan against authority changing while it
// is being assembled; a later runtime owner can use the result as an input to an authenticated activation step.
func PlanVerifiedManagedNativeRoutes(
	ctx context.Context,
	source ManagedPublicationStateSource,
	request ManagedNativeRoutePlanRequest,
) ([]dataplane.NativeRoute, error) {
	ctx = normalizeManagedPublicationContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("managed publication state source is required")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}

	first, err := readVerifiedManagedPublicationAuthority(ctx, source, request.Fence, request.AllowProjectStarting)
	if err != nil {
		return nil, err
	}
	firstRoutes, err := PlanManagedNativeRoutes(ManagedPublicationPlanInput{
		Fence:        request.Fence,
		Reservations: first.Reservations,
		Publications: request.Publications,
	})
	if err != nil {
		return nil, err
	}

	second, err := readVerifiedManagedPublicationAuthority(ctx, source, request.Fence, request.AllowProjectStarting)
	if err != nil {
		return nil, errors.Join(managedPublicationAuthorityChanged, err)
	}
	secondRoutes, err := PlanManagedNativeRoutes(ManagedPublicationPlanInput{
		Fence:        request.Fence,
		Reservations: second.Reservations,
		Publications: request.Publications,
	})
	if err != nil {
		return nil, errors.Join(managedPublicationAuthorityChanged, err)
	}
	if !reflect.DeepEqual(first.Session, second.Session) ||
		!reflect.DeepEqual(first.Reservations, second.Reservations) ||
		!slices.Equal(firstRoutes, secondRoutes) {
		return nil, managedPublicationAuthorityChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return firstRoutes, nil
}

// managedPublicationAuthorityChanged marks a durable fence that moved while a pure plan was being assembled.
var managedPublicationAuthorityChanged = errors.New("managed publication durable authority changed during planning")

// managedPublicationAuthority is the validated, Harbor-owned input captured for one planner pass.
type managedPublicationAuthority struct {
	Session      domain.ProjectSession
	Reservations []state.EndpointReservation
}

// readVerifiedManagedPublicationAuthority reads resolver-or-full network state for the requested attached session.
func readVerifiedManagedPublicationAuthority(
	ctx context.Context,
	source ManagedPublicationStateSource,
	fence ManagedPublicationFence,
	allowProjectStarting bool,
) (managedPublicationAuthority, error) {
	runtimeState, err := source.RuntimeState(ctx)
	if err != nil {
		return managedPublicationAuthority{}, fmt.Errorf("read managed publication runtime state: %w", err)
	}
	if err := runtimeState.Validate(); err != nil {
		return managedPublicationAuthority{}, fmt.Errorf("validate managed publication runtime state: %w", err)
	}
	if !runtimeState.NetworkInitialized {
		return managedPublicationAuthority{}, fmt.Errorf("managed publication requires Harbor network resolver authority")
	}
	if runtimeState.Network.Stage != state.NetworkStageResolver && runtimeState.Network.Stage != state.NetworkStageFull {
		return managedPublicationAuthority{}, fmt.Errorf("managed publication requires resolver or full Harbor network ownership; current stage is %q", runtimeState.Network.Stage)
	}

	project, found := managedPublicationProject(runtimeState.Snapshot.Projects, fence.ProjectID)
	if !found {
		return managedPublicationAuthority{}, &state.ProjectNotFoundError{ProjectID: fence.ProjectID}
	}
	if project.State != domain.ProjectReady && !(allowProjectStarting && project.State == domain.ProjectStarting) {
		return managedPublicationAuthority{}, fmt.Errorf("managed publication project %q is %q, not ready", fence.ProjectID, project.State)
	}

	session, err := source.ActiveProjectSession(ctx, fence.ProjectID)
	if err != nil {
		return managedPublicationAuthority{}, fmt.Errorf("read managed publication session for project %q: %w", fence.ProjectID, err)
	}
	if err := session.Validate(); err != nil {
		return managedPublicationAuthority{}, fmt.Errorf("validate managed publication session: %w", err)
	}
	if session.ProjectID != fence.ProjectID {
		return managedPublicationAuthority{}, fmt.Errorf("managed publication session %q belongs to project %q, not %q", session.ID, session.ProjectID, fence.ProjectID)
	}
	if session.ID != fence.SessionID {
		return managedPublicationAuthority{}, &state.ProjectSessionNotFoundError{ProjectID: fence.ProjectID, SessionID: fence.SessionID}
	}
	if session.Generation != fence.SessionGeneration {
		return managedPublicationAuthority{}, &state.StaleSessionGenerationError{
			ProjectID: fence.ProjectID,
			SessionID: fence.SessionID,
			Expected:  fence.SessionGeneration,
			Actual:    session.Generation,
		}
	}
	if session.State != domain.SessionAttached {
		return managedPublicationAuthority{}, fmt.Errorf("managed publication session %q is %q, not attached", session.ID, session.State)
	}

	reservations := make([]state.EndpointReservation, 0, len(runtimeState.Network.Reservations.Endpoints))
	for _, reservation := range runtimeState.Network.Reservations.Endpoints {
		if reservation.Key.ProjectID != fence.ProjectID {
			continue
		}
		reservations = append(reservations, cloneManagedPublicationReservation(reservation))
	}
	return managedPublicationAuthority{Session: session, Reservations: reservations}, nil
}

// managedPublicationProject returns one exact project snapshot without allowing a caller to select another project.
func managedPublicationProject(projects []domain.ProjectSnapshot, projectID domain.ProjectID) (domain.ProjectSnapshot, bool) {
	for _, project := range projects {
		if project.ID == projectID {
			return project, true
		}
	}
	return domain.ProjectSnapshot{}, false
}

// cloneManagedPublicationReservation protects a planner pass from pointer-backed identity facts being reused by a source.
func cloneManagedPublicationReservation(reservation state.EndpointReservation) state.EndpointReservation {
	if reservation.Identity != nil {
		identityCopy := *reservation.Identity
		reservation.Identity = &identityCopy
	}
	return reservation
}

// normalizeManagedPublicationContext gives nil callers cancellation-free semantics at this read-only boundary.
func normalizeManagedPublicationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
