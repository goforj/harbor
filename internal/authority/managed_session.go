package authority

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/managedsession"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/state"
)

// managedSessionAttachment retains the authenticated peer and response needed to replay one registration.
type managedSessionAttachment struct {
	request            managedsession.RegisterRequest
	launchTicketDigest string
	response           managedsession.RegisterResponse
	peer               local.PeerIdentity
}

// RegisterManagedSession authenticates one Harbor-launched process against its awaiting durable session.
func (authority *Authority) RegisterManagedSession(
	ctx context.Context,
	peer local.PeerIdentity,
	request managedsession.RegisterRequest,
) (managedsession.RegisterResponse, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return managedsession.RegisterResponse{}, err
	}
	if authority.managedStore == nil || authority.managedRegistry == nil {
		return managedsession.RegisterResponse{}, errors.New("managed session authority is unavailable")
	}
	if peer.ProcessID == 0 {
		return managedsession.RegisterResponse{}, errors.New("managed session peer process identity is invalid")
	}
	if err := ctx.Err(); err != nil {
		return managedsession.RegisterResponse{}, err
	}

	authority.managedMu.Lock()
	defer authority.managedMu.Unlock()
	if authority.managedSessions == nil {
		authority.managedSessions = make(map[domain.ProjectID]managedSessionAttachment)
	}
	replayRequest := request
	replayRequest.LaunchTicket = ""
	launchTicketDigest := managedLaunchTicketDigest(request.LaunchTicket)
	if existing, found := authority.managedSessions[request.ProjectID]; found {
		active, err := authority.managedStore.ActiveProjectSession(ctx, request.ProjectID)
		if err != nil {
			var missing *state.ProjectSessionNotFoundError
			if !errors.As(err, &missing) {
				return managedsession.RegisterResponse{}, fmt.Errorf("read existing managed session: %w", err)
			}
			if closeErr := authority.closeManagedAttachment(existing.response.Fence); closeErr != nil {
				return managedsession.RegisterResponse{}, closeErr
			}
			delete(authority.managedSessions, request.ProjectID)
		} else if active.ID == existing.response.Fence.SessionID &&
			active.Generation == existing.response.Fence.SessionGeneration &&
			active.State == domain.SessionAttached &&
			active.Process != nil && active.Process.PID == int64(existing.peer.ProcessID) {
			if existing.peer == peer && reflect.DeepEqual(existing.request, replayRequest) && secureStringEqual(existing.launchTicketDigest, launchTicketDigest) {
				return existing.response, nil
			}
			return managedsession.RegisterResponse{}, fmt.Errorf("project %q already has an attached managed session", request.ProjectID)
		} else {
			if closeErr := authority.closeManagedAttachment(existing.response.Fence); closeErr != nil {
				return managedsession.RegisterResponse{}, closeErr
			}
			delete(authority.managedSessions, request.ProjectID)
		}
	}

	project, err := authority.managedStore.Project(ctx, request.ProjectID)
	if err != nil {
		return managedsession.RegisterResponse{}, fmt.Errorf("read managed project %q: %w", request.ProjectID, err)
	}
	if project.Project.Path != request.ProjectRoot {
		return managedsession.RegisterResponse{}, fmt.Errorf("managed session project root does not match registered checkout")
	}
	active, err := authority.managedStore.ActiveProjectSession(ctx, request.ProjectID)
	if err != nil {
		return managedsession.RegisterResponse{}, fmt.Errorf("read managed project session %q: %w", request.ProjectID, err)
	}
	if active.ID != request.SessionID {
		return managedsession.RegisterResponse{}, fmt.Errorf("managed session %q is not the active project session", request.SessionID)
	}
	if active.State == domain.SessionAttached {
		return authority.replayAttachedManagedSession(peer, request, active)
	}
	if active.State != domain.SessionAwaitingAttach {
		if active.State == domain.SessionPlanned {
			return managedsession.RegisterResponse{}, fmt.Errorf("%w: session %q is still planned", managedsession.ErrManagedSessionAwaitingAttach, request.SessionID)
		}
		return managedsession.RegisterResponse{}, fmt.Errorf("managed session %q is %q, not awaiting attachment", request.SessionID, active.State)
	}
	if active.Generation != request.ExpectedSessionGeneration {
		return managedsession.RegisterResponse{}, fmt.Errorf("managed session %q generation does not match the requested attachment fence", request.SessionID)
	}
	if active.Owner != request.Owner {
		return managedsession.RegisterResponse{}, fmt.Errorf("managed session owner %q does not match the request", active.Owner)
	}
	if active.DescriptorDigest != request.DescriptorDigest {
		return managedsession.RegisterResponse{}, errors.New("managed session descriptor digest does not match the admitted project")
	}
	if request.LaunchTicket != "" {
		if err := verifyManagedLaunchTicket(request.LaunchTicket, active.CredentialDigest); err != nil {
			return managedsession.RegisterResponse{}, err
		}
	}
	if active.Process == nil || active.Process.PID != int64(peer.ProcessID) {
		return managedsession.RegisterResponse{}, errors.New("managed session peer is not the admitted process")
	}

	at := authority.now().UTC().Round(0)
	if at.Before(active.UpdatedAt) {
		at = active.UpdatedAt
	}
	prospective := active
	prospective.State = domain.SessionAttached
	prospective.Generation++
	prospective.UpdatedAt = at
	if err := prospective.Validate(); err != nil {
		return managedsession.RegisterResponse{}, fmt.Errorf("validate managed attachment session: %w", err)
	}
	ticket, err := newManagedAttachmentTicket()
	if err != nil {
		return managedsession.RegisterResponse{}, fmt.Errorf("issue managed session attachment ticket: %w", err)
	}
	response := managedsession.RegisterResponse{
		SchemaVersion:    managedsession.SchemaVersion,
		Fence:            harbordruntime.ManagedPublicationFence{ProjectID: prospective.ProjectID, SessionID: prospective.ID, SessionGeneration: prospective.Generation},
		AttachmentTicket: ticket,
	}
	if err := managedsession.ValidateRegisterCorrelation(request, response); err != nil {
		return managedsession.RegisterResponse{}, err
	}
	fence, err := authority.managedRegistry.Open(prospective)
	if err != nil {
		return managedsession.RegisterResponse{}, err
	}
	closeRegistry := true
	defer func() {
		if closeRegistry {
			_ = authority.managedRegistry.Close(fence)
		}
	}()
	attached, err := authority.managedStore.CompleteManagedSessionAttachment(ctx, state.CompleteManagedSessionAttachmentRequest{
		ProjectID:                 request.ProjectID,
		SessionID:                 request.SessionID,
		ExpectedSessionGeneration: request.ExpectedSessionGeneration,
		Process:                   *active.Process,
		At:                        at,
	})
	if err != nil {
		return managedsession.RegisterResponse{}, err
	}
	if attached.ID != fence.SessionID || attached.Generation != fence.SessionGeneration || attached.State != domain.SessionAttached {
		return managedsession.RegisterResponse{}, errors.New("managed session attachment returned an unexpected durable fence")
	}
	if response.Fence != fence {
		return managedsession.RegisterResponse{}, errors.New("managed session registry returned an unexpected fence")
	}
	authority.managedSessions[request.ProjectID] = managedSessionAttachment{
		request:            replayRequest,
		launchTicketDigest: launchTicketDigest,
		response:           response,
		peer:               peer,
	}
	closeRegistry = false
	return response, nil
}

// replayAttachedManagedSession reconstructs process-local authority after Harbor restarts without mutating durable state.
//
// The durable attached session is sufficient to prove the process boundary: the reconnecting peer must have the
// same operating-system PID, project root, session generation, descriptor digest, and inherited launch credential.
// Reopening the ephemeral registry here lets the next publication/barrier call rebuild native routes while the
// existing GoForj process remains the source of truth for its running watchers.
func (authority *Authority) replayAttachedManagedSession(
	peer local.PeerIdentity,
	request managedsession.RegisterRequest,
	active domain.ProjectSession,
) (managedsession.RegisterResponse, error) {
	if active.Generation == 0 || request.ExpectedSessionGeneration == rpc.MaximumSequence ||
		active.Generation != request.ExpectedSessionGeneration+1 {
		return managedsession.RegisterResponse{}, fmt.Errorf("managed session %q generation does not match the requested replay fence", request.SessionID)
	}
	if active.Owner != request.Owner {
		return managedsession.RegisterResponse{}, fmt.Errorf("managed session owner %q does not match the replay request", active.Owner)
	}
	if active.DescriptorDigest != request.DescriptorDigest {
		return managedsession.RegisterResponse{}, errors.New("managed session descriptor digest does not match the durable replay session")
	}
	if request.LaunchTicket != "" {
		if err := verifyManagedLaunchTicket(request.LaunchTicket, active.CredentialDigest); err != nil {
			return managedsession.RegisterResponse{}, err
		}
	}
	if active.Process == nil || active.Process.PID != int64(peer.ProcessID) {
		return managedsession.RegisterResponse{}, errors.New("managed session replay peer is not the attached process")
	}

	ticket, err := newManagedAttachmentTicket()
	if err != nil {
		return managedsession.RegisterResponse{}, fmt.Errorf("issue managed session replay attachment ticket: %w", err)
	}
	response := managedsession.RegisterResponse{
		SchemaVersion: managedsession.SchemaVersion,
		Fence: harbordruntime.ManagedPublicationFence{
			ProjectID:         active.ProjectID,
			SessionID:         active.ID,
			SessionGeneration: active.Generation,
		},
		AttachmentTicket: ticket,
	}
	if err := managedsession.ValidateRegisterCorrelation(request, response); err != nil {
		return managedsession.RegisterResponse{}, err
	}
	fence, err := authority.managedRegistry.Open(active)
	if err != nil {
		return managedsession.RegisterResponse{}, fmt.Errorf("reopen managed publication stream: %w", err)
	}
	closeRegistry := true
	defer func() {
		if closeRegistry {
			_ = authority.managedRegistry.Close(fence)
		}
	}()
	if response.Fence != fence {
		return managedsession.RegisterResponse{}, errors.New("managed session replay registry returned an unexpected fence")
	}
	replayRequest := request
	replayRequest.LaunchTicket = ""
	authority.managedSessions[request.ProjectID] = managedSessionAttachment{
		request:            replayRequest,
		launchTicketDigest: managedLaunchTicketDigest(request.LaunchTicket),
		response:           response,
		peer:               peer,
	}
	closeRegistry = false
	return response, nil
}

// ReplaceManagedPublications accepts a complete private observation only from its registered process peer.
func (authority *Authority) ReplaceManagedPublications(
	ctx context.Context,
	peer local.PeerIdentity,
	request managedsession.ReplacePublicationsRequest,
) (managedsession.ReplacePublicationsResponse, error) {
	if err := request.Validate(); err != nil {
		return managedsession.ReplacePublicationsResponse{}, err
	}
	if _, err := authority.authorizeManagedFence(ctx, peer, request.Fence); err != nil {
		return managedsession.ReplacePublicationsResponse{}, err
	}
	if err := authority.managedRegistry.Replace(request.Fence, request.Publications); err != nil {
		return managedsession.ReplacePublicationsResponse{}, err
	}
	response := managedsession.ReplacePublicationsResponse{
		SchemaVersion:    managedsession.SchemaVersion,
		Fence:            request.Fence,
		Accepted:         true,
		PublicationCount: uint16(len(request.Publications)),
	}
	if err := managedsession.ValidateReplacePublicationsCorrelation(request, response); err != nil {
		return managedsession.ReplacePublicationsResponse{}, err
	}
	return response, nil
}

// PlanManagedRuntime returns one exact assignment plan after reauthenticating the attached process fence.
func (authority *Authority) PlanManagedRuntime(
	ctx context.Context,
	peer local.PeerIdentity,
	request managedsession.RuntimePlanRequest,
) (managedsession.RuntimePlanResponse, error) {
	if err := request.Validate(); err != nil {
		return managedsession.RuntimePlanResponse{}, err
	}
	if _, err := authority.authorizeManagedFence(ctx, peer, request.Fence); err != nil {
		return managedsession.RuntimePlanResponse{}, err
	}
	planner, ok := authority.lifecycle.(managedRuntimePlanObserver)
	if !ok {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("%w: managed runtime-plan authority is unavailable", managedsession.ErrManagedSessionNotReady)
	}
	response, err := planner.PlanManagedRuntime(normalizeContext(ctx), request)
	if err != nil {
		return managedsession.RuntimePlanResponse{}, err
	}
	if err := managedsession.ValidateRuntimePlanCorrelation(request, response); err != nil {
		return managedsession.RuntimePlanResponse{}, fmt.Errorf("validate managed runtime plan authority: %w", err)
	}
	return response, nil
}

// managedPublicationStateSource joins the control aggregate and exact session reads for native route planning.
type managedPublicationStateSource struct {
	runtime  controlState
	sessions managedSessionState
}

// RuntimeState returns the Harbor-owned network and project aggregate used by the publication boundary.
func (source managedPublicationStateSource) RuntimeState(ctx context.Context) (state.RuntimeState, error) {
	return source.runtime.RuntimeState(ctx)
}

// ActiveProjectSession returns one exact durable session for publication planning.
func (source managedPublicationStateSource) ActiveProjectSession(ctx context.Context, projectID domain.ProjectID) (domain.ProjectSession, error) {
	return source.sessions.ActiveProjectSession(ctx, projectID)
}

// currentManagedNativeRoutes plans every attached managed session from its latest complete observation.
func (authority *Authority) currentManagedNativeRoutes(ctx context.Context, allowProjectStarting bool, startingFence harbordruntime.ManagedPublicationFence) ([]dataplane.NativeRoute, error) {
	if authority.managedStore == nil || authority.managedRegistry == nil {
		return nil, errors.New("managed session route authority is unavailable")
	}
	authority.managedMu.Lock()
	attachments := make([]managedSessionAttachment, 0, len(authority.managedSessions))
	for _, attachment := range authority.managedSessions {
		attachments = append(attachments, attachment)
	}
	authority.managedMu.Unlock()
	routes := make([]dataplane.NativeRoute, 0)
	source := managedPublicationStateSource{runtime: authority.store, sessions: authority.managedStore}
	for _, attachment := range attachments {
		fence := attachment.response.Fence
		allowStartingForAttachment := allowProjectStarting && fence == startingFence
		publications, err := authority.managedRegistry.Snapshot(fence)
		if err != nil {
			return nil, err
		}
		planned, err := harbordruntime.PlanVerifiedManagedNativeRoutes(ctx, source, harbordruntime.ManagedNativeRoutePlanRequest{
			Fence:                fence,
			Publications:         publications,
			AllowProjectStarting: allowStartingForAttachment,
		})
		if err != nil {
			return nil, fmt.Errorf("plan managed native routes for project %q: %w", fence.ProjectID, err)
		}
		routes = append(routes, planned...)
	}
	slices.SortFunc(routes, func(left, right dataplane.NativeRoute) int {
		if left.Host < right.Host {
			return -1
		}
		if left.Host > right.Host {
			return 1
		}
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	return routes, nil
}

// AcknowledgeManagedBarrier reports the route barrier only after a future native activation owner proves it.
func (authority *Authority) AcknowledgeManagedBarrier(
	ctx context.Context,
	peer local.PeerIdentity,
	request managedsession.BarrierRequest,
) (managedsession.BarrierResponse, error) {
	if err := request.Validate(); err != nil {
		return managedsession.BarrierResponse{}, err
	}
	if _, err := authority.authorizeManagedFence(ctx, peer, request.Fence); err != nil {
		return managedsession.BarrierResponse{}, err
	}
	acknowledged := false
	if authority.managedRoutes != nil {
		allowProjectStarting := request.Phase == managedsession.BarrierPhaseCompose
		if authority.managedObserver != nil {
			var observed []harbordruntime.ManagedEndpointPublication
			var err error
			if phaseObserver, ok := authority.managedObserver.(managedPublicationPhaseObserver); ok {
				observed, err = phaseObserver.ObserveManagedPublicationsForPhase(
					normalizeContext(ctx),
					request.Fence.ProjectID,
					request.Fence.SessionID,
					request.Fence,
					allowProjectStarting,
				)
			} else {
				observed, err = authority.managedObserver.ObserveManagedPublications(
					normalizeContext(ctx),
					request.Fence.ProjectID,
					request.Fence.SessionID,
					request.Fence,
				)
			}
			if err != nil {
				return managedsession.BarrierResponse{}, err
			}
			if err := authority.managedRegistry.Replace(request.Fence, observed); err != nil {
				return managedsession.BarrierResponse{}, err
			}
		}
		routes, err := authority.currentManagedNativeRoutes(normalizeContext(ctx), allowProjectStarting, request.Fence)
		if err != nil {
			return managedsession.BarrierResponse{}, fmt.Errorf("%w: plan managed native routes: %w", managedsession.ErrManagedSessionNotReady, err)
		}
		if err := authority.managedRoutes.ReplaceManagedNativeRoutes(normalizeContext(ctx), routes); err != nil {
			return managedsession.BarrierResponse{}, fmt.Errorf("%w: replace managed native routes: %w", managedsession.ErrManagedSessionNotReady, err)
		}
		if err := authority.managedRoutes.ManagedNativeRoutesLive(normalizeContext(ctx), routes); err != nil {
			return managedsession.BarrierResponse{}, fmt.Errorf("%w: verify managed native routes: %w", managedsession.ErrManagedSessionNotReady, err)
		}
		acknowledged = true
	}
	response := managedsession.BarrierResponse{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         request.Fence,
		Phase:         request.Phase,
		Acknowledged:  acknowledged,
	}
	if err := managedsession.ValidateBarrierCorrelation(request, response); err != nil {
		return managedsession.BarrierResponse{}, err
	}
	return response, nil
}

// authorizeManagedFence revalidates the durable session and operating-system peer before each observation.
func (authority *Authority) authorizeManagedFence(
	ctx context.Context,
	peer local.PeerIdentity,
	fence harbordruntime.ManagedPublicationFence,
) (managedSessionAttachment, error) {
	if authority.managedStore == nil || authority.managedRegistry == nil {
		return managedSessionAttachment{}, errors.New("managed session authority is unavailable")
	}
	if err := fence.Validate(); err != nil {
		return managedSessionAttachment{}, err
	}
	authority.managedMu.Lock()
	attachment, found := authority.managedSessions[fence.ProjectID]
	authority.managedMu.Unlock()
	if !found || attachment.response.Fence != fence {
		return managedSessionAttachment{}, errors.New("managed session fence is not attached")
	}
	if attachment.peer != peer {
		return managedSessionAttachment{}, errors.New("managed session peer does not match the attached process")
	}
	active, err := authority.managedStore.ActiveProjectSession(normalizeContext(ctx), fence.ProjectID)
	if err != nil {
		return managedSessionAttachment{}, err
	}
	if active.ID != fence.SessionID || active.Generation != fence.SessionGeneration || active.State != domain.SessionAttached || active.Process == nil || active.Process.PID != int64(peer.ProcessID) {
		return managedSessionAttachment{}, errors.New("managed session durable attachment no longer matches the request")
	}
	return attachment, nil
}

// closeManagedAttachment removes one process-local publication stream while tolerating an already-retired stream.
func (authority *Authority) closeManagedAttachment(fence harbordruntime.ManagedPublicationFence) error {
	err := authority.managedRegistry.Close(fence)
	if errors.Is(err, harbordruntime.ErrManagedPublicationFenceNotFound) {
		return nil
	}
	return err
}

// newManagedAttachmentTicket creates one bounded ephemeral credential for a successful registration response.
func newManagedAttachmentTicket() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// verifyManagedLaunchTicket compares the inherited one-use proof with the durable digest without exposing either value.
func verifyManagedLaunchTicket(ticket, expectedDigest string) error {
	if !secureStringEqual(managedLaunchTicketDigest(ticket), expectedDigest) {
		return errors.New("managed session launch ticket does not match the admitted session")
	}
	return nil
}

// managedLaunchTicketDigest hashes one inherited ticket before it enters any process-local replay identity.
func managedLaunchTicketDigest(ticket string) string {
	if ticket == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(digest[:])
}

// secureStringEqual compares replay digests without making ticket material observable through timing.
func secureStringEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
