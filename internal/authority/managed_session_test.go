package authority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/managedsession"
	"github.com/goforj/harbor/internal/network/dataplane"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/state"
)

// managedSessionAuthorityStore is a deterministic durable boundary for attachment tests.
type managedSessionAuthorityStore struct {
	*recordingStore
	project         state.ProjectRecord
	session         domain.ProjectSession
	attachmentCalls int
}

// recordingManagedNativeRoutes records the complete route set handed to the live data-plane boundary.
type recordingManagedNativeRoutes struct {
	replacements [][]dataplane.NativeRoute
	live         bool
}

// recordingManagedPublicationObserver returns the Harbor-owned replacement used by barrier activation assertions.
type recordingManagedPublicationObserver struct {
	publications []harbordruntime.ManagedEndpointPublication
	calls        int
	lastFence    harbordruntime.ManagedPublicationFence
}

// ObserveManagedPublications records the exact fence and returns a fresh Harbor-owned publication set.
func (observer *recordingManagedPublicationObserver) ObserveManagedPublications(_ context.Context, _ domain.ProjectID, _ domain.SessionID, fence harbordruntime.ManagedPublicationFence) ([]harbordruntime.ManagedEndpointPublication, error) {
	observer.calls++
	observer.lastFence = fence
	return append([]harbordruntime.ManagedEndpointPublication(nil), observer.publications...), nil
}

// ReplaceManagedNativeRoutes records one complete route replacement for barrier assertions.
func (routes *recordingManagedNativeRoutes) ReplaceManagedNativeRoutes(_ context.Context, replacement []dataplane.NativeRoute) error {
	routes.replacements = append(routes.replacements, append([]dataplane.NativeRoute(nil), replacement...))
	routes.live = true
	return nil
}

// ManagedNativeRoutesLive reports the configured route publication postcondition.
func (routes *recordingManagedNativeRoutes) ManagedNativeRoutesLive(context.Context, []dataplane.NativeRoute) error {
	if !routes.live {
		return errors.New("managed native routes are not live")
	}
	return nil
}

// Project returns the one registered project selected by the test request.
func (store *managedSessionAuthorityStore) Project(_ context.Context, projectID domain.ProjectID) (state.ProjectRecord, error) {
	if projectID != store.project.Project.ID {
		return state.ProjectRecord{}, &state.ProjectNotFoundError{ProjectID: projectID}
	}
	return store.project, nil
}

// ActiveProjectSession returns the current process-backed session selected by the test request.
func (store *managedSessionAuthorityStore) ActiveProjectSession(_ context.Context, projectID domain.ProjectID) (domain.ProjectSession, error) {
	if projectID != store.session.ProjectID {
		return domain.ProjectSession{}, &state.ProjectSessionNotFoundError{ProjectID: projectID}
	}
	return store.session, nil
}

// CompleteManagedSessionAttachment advances the fixture's awaiting session exactly once.
func (store *managedSessionAuthorityStore) CompleteManagedSessionAttachment(_ context.Context, request state.CompleteManagedSessionAttachmentRequest) (domain.ProjectSession, error) {
	if store.session.State == domain.SessionAttached && store.session.Generation == request.ExpectedSessionGeneration+1 {
		return store.session, nil
	}
	if store.session.State != domain.SessionAwaitingAttach || store.session.Generation != request.ExpectedSessionGeneration {
		return domain.ProjectSession{}, errors.New("managed attachment fence is stale")
	}
	if store.session.Process == nil || *store.session.Process != request.Process {
		return domain.ProjectSession{}, errors.New("managed attachment process differs")
	}
	store.session.State = domain.SessionAttached
	store.session.Generation++
	store.session.UpdatedAt = request.At
	store.attachmentCalls++
	return store.session, nil
}

// managedSessionAuthorityFixture constructs one awaiting Harbor-owned session and its registered project.
func managedSessionAuthorityFixture() *managedSessionAuthorityStore {
	at := time.Date(2026, time.July, 21, 5, 0, 0, 0, time.UTC)
	project := domain.ProjectSnapshot{
		ID:        "project-orders",
		Name:      "Orders",
		Path:      "/workspace/orders",
		Slug:      "orders",
		State:     domain.ProjectReady,
		UpdatedAt: at,
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
	process := &domain.ProcessEvidence{
		PID:                321,
		BirthToken:         "birth-321",
		ExecutableIdentity: "/workspace/bin/forj",
		ArgumentDigest:     strings.Repeat("c", 64),
	}
	return &managedSessionAuthorityStore{
		recordingStore: &recordingStore{},
		project:        state.ProjectRecord{Project: project, Revision: 4},
		session: domain.ProjectSession{
			ID:               "session-orders",
			ProjectID:        project.ID,
			Owner:            domain.SessionOwnerHarbor,
			State:            domain.SessionAwaitingAttach,
			DescriptorDigest: strings.Repeat("a", 64),
			CredentialDigest: strings.Repeat("b", 64),
			Generation:       2,
			Process:          process,
			CreatedAt:        at.Add(-time.Minute),
			UpdatedAt:        at,
		},
	}
}

// managedSessionAuthorityRequest returns one exact attachment identity for the fixture.
func managedSessionAuthorityRequest() managedsession.RegisterRequest {
	return managedsession.RegisterRequest{
		SchemaVersion:             managedsession.SchemaVersion,
		ProjectID:                 "project-orders",
		SessionID:                 "session-orders",
		ProjectRoot:               "/workspace/orders",
		ExpectedSessionGeneration: 2,
		DescriptorDigest:          strings.Repeat("a", 64),
		ClientNonce:               "nonce-1",
		Owner:                     domain.SessionOwnerHarbor,
		Capabilities:              []rpc.Capability{managedsession.CapabilityV1},
		ActiveApps:                []managedsession.ActiveApp{},
	}
}

// TestAuthorityManagedSessionRoundTripBindsPeerAndFence verifies registration, observation replacement, and barrier readiness.
func TestAuthorityManagedSessionRoundTripBindsPeerAndFence(t *testing.T) {
	store := managedSessionAuthorityFixture()
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	request := managedSessionAuthorityRequest()

	response, err := authority.RegisterManagedSession(t.Context(), peer, request)
	if err != nil {
		t.Fatalf("RegisterManagedSession() error = %v", err)
	}
	if response.Fence.SessionGeneration != 3 || response.AttachmentTicket == "" {
		t.Fatalf("RegisterManagedSession() response = %#v, want generation 3 and ticket", response)
	}
	if store.attachmentCalls != 1 || store.session.State != domain.SessionAttached {
		t.Fatalf("durable attachment = calls %d state %q, want one attached transition", store.attachmentCalls, store.session.State)
	}

	publicationRequest := managedsession.ReplacePublicationsRequest{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         response.Fence,
		Publications: []harbordruntime.ManagedEndpointPublication{{
			Fence:                 response.Fence,
			EndpointID:            "mysql",
			ReservationGeneration: 1,
			Upstream:              netip.MustParseAddrPort("127.0.0.1:3307"),
		}},
	}
	publicationResponse, err := authority.ReplaceManagedPublications(t.Context(), peer, publicationRequest)
	if err != nil {
		t.Fatalf("ReplaceManagedPublications() error = %v", err)
	}
	if !publicationResponse.Accepted || publicationResponse.PublicationCount != 1 {
		t.Fatalf("ReplaceManagedPublications() response = %#v, want accepted one publication", publicationResponse)
	}

	barrierResponse, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedsession.BarrierRequest{
		SchemaVersion:           managedsession.SchemaVersion,
		Fence:                   response.Fence,
		Phase:                   managedsession.BarrierPhaseCompose,
		AcceptedProjectIdentity: "orders",
	})
	if err != nil {
		t.Fatalf("AcknowledgeManagedBarrier() error = %v", err)
	}
	if barrierResponse.Acknowledged {
		t.Fatal("AcknowledgeManagedBarrier() claimed native route activation before it was wired")
	}

	replayed, err := authority.RegisterManagedSession(t.Context(), peer, request)
	if err != nil {
		t.Fatalf("replayed RegisterManagedSession() error = %v", err)
	}
	if replayed != response || store.attachmentCalls != 1 {
		t.Fatalf("replayed registration = %#v, calls = %d, want exact response and no second transition", replayed, store.attachmentCalls)
	}
}

// TestAuthorityManagedBarrierRequiresLiveRouteActivator proves the typed barrier becomes positive only after the controller accepts the complete route set.
func TestAuthorityManagedBarrierRequiresLiveRouteActivator(t *testing.T) {
	store := managedSessionAuthorityFixture()
	store.recordingStore.runtimeState = authorityRouteRuntimeState()
	runtimeState := &store.recordingStore.runtimeState
	runtimeState.Network.Revision = runtimeState.Snapshot.Sequence
	runtimeState.Network.CreatedAt = runtimeState.Snapshot.CapturedAt
	runtimeState.Network.UpdatedAt = runtimeState.Snapshot.CapturedAt
	runtimeState.Network.Ownership = identity.Ownership{InstallationID: "harbor-installation", Generation: 1}
	runtimeState.Network.Pool, _ = identity.NewPool(netip.MustParsePrefix("127.77.0.0/24"), []netip.Addr{netip.MustParseAddr("127.77.0.10")})
	runtimeState.Network.Leases = []identity.Lease{{
		Key:       identity.LeaseKey{ProjectID: "project-orders"},
		Address:   netip.MustParseAddr("127.77.0.10"),
		Ownership: identity.Ownership{InstallationID: "harbor-installation", Generation: 1},
	}}
	runtimeState.Network.Quarantines = []identity.Quarantine{}
	runtimeState.Network.Reservations.Listeners = state.SharedListenerReservations{
		DNS:   state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:53"), Bind: netip.MustParseAddrPort("127.0.0.2:53"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
		HTTP:  state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:80"), Bind: netip.MustParseAddrPort("127.0.0.2:80"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
		HTTPS: state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:443"), Bind: netip.MustParseAddrPort("127.0.0.2:443"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
	}
	runtimeState.Network.Reservations.Endpoints[0].Public = netip.MustParseAddrPort("127.0.0.2:443")
	serviceReservation := state.EndpointReservation{
		Key:        state.EndpointReservationKey{ProjectID: "project-orders", EndpointID: "service:mysql"},
		Protocol:   state.EndpointProtocolTCP,
		Host:       "mysql.orders.test",
		Public:     netip.MustParseAddrPort("127.77.0.10:3306"),
		Identity:   &identity.LeaseKey{ProjectID: "project-orders"},
		Generation: 1,
	}
	runtimeState.Network.Reservations.Endpoints = append([]state.EndpointReservation{serviceReservation}, runtimeState.Network.Reservations.Endpoints...)
	runtimeState.Network.Reservations.SuppressedProjectIDs = []domain.ProjectID{}
	activator := new(recordingManagedNativeRoutes)
	observer := &recordingManagedPublicationObserver{publications: []harbordruntime.ManagedEndpointPublication{{
		Fence:                 harbordruntime.ManagedPublicationFence{ProjectID: "project-orders", SessionID: "session-orders", SessionGeneration: 3},
		EndpointID:            "service:mysql",
		ReservationGeneration: 1,
		Upstream:              netip.MustParseAddrPort("127.0.0.1:43007"),
	}}}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	authority.managedRoutes = activator
	authority.managedObserver = observer
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	response, err := authority.RegisterManagedSession(t.Context(), peer, managedSessionAuthorityRequest())
	if err != nil {
		t.Fatalf("RegisterManagedSession() error = %v", err)
	}
	if _, err := authority.ReplaceManagedPublications(t.Context(), peer, managedsession.ReplacePublicationsRequest{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         response.Fence,
		Publications: []harbordruntime.ManagedEndpointPublication{{
			Fence:                 response.Fence,
			EndpointID:            "service:mysql",
			ReservationGeneration: 1,
			Upstream:              netip.MustParseAddrPort("127.0.0.1:43007"),
		}},
	}); err != nil {
		t.Fatalf("ReplaceManagedPublications() error = %v", err)
	}
	barrier, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedsession.BarrierRequest{
		SchemaVersion:           managedsession.SchemaVersion,
		Fence:                   response.Fence,
		Phase:                   managedsession.BarrierPhaseCompose,
		AcceptedProjectIdentity: "orders",
	})
	if err != nil {
		t.Fatalf("AcknowledgeManagedBarrier() error = %v", err)
	}
	if !barrier.Acknowledged || len(activator.replacements) != 1 || len(activator.replacements[0]) != 1 || activator.replacements[0][0].ID != "project-orders:service:mysql" {
		t.Fatalf("barrier = %#v, replacements = %#v; want acknowledged mysql route", barrier, activator.replacements)
	}
	if observer.calls != 1 || observer.lastFence != response.Fence {
		t.Fatalf("publication observer calls = %d fence = %#v, want one call for %#v", observer.calls, observer.lastFence, response.Fence)
	}
}

// TestAuthorityManagedSessionRejectsPeerDrift keeps another local process from inheriting a live fence.
func TestAuthorityManagedSessionRejectsPeerDrift(t *testing.T) {
	store := managedSessionAuthorityFixture()
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	response, err := authority.RegisterManagedSession(t.Context(), peer, managedSessionAuthorityRequest())
	if err != nil {
		t.Fatalf("RegisterManagedSession() error = %v", err)
	}
	_, err = authority.ReplaceManagedPublications(t.Context(), local.PeerIdentity{UserID: "501", ProcessID: 322}, managedsession.ReplacePublicationsRequest{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         response.Fence,
		Publications:  []harbordruntime.ManagedEndpointPublication{},
	})
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Fatalf("peer drift error = %v, want peer rejection", err)
	}
}

// TestAuthorityManagedSessionVerifiesNegotiatedLaunchTicket binds the inherited credential to durable session authority.
func TestAuthorityManagedSessionVerifiesNegotiatedLaunchTicket(t *testing.T) {
	store := managedSessionAuthorityFixture()
	ticket := strings.Repeat("launch-ticket-", 8)
	digest := sha256.Sum256([]byte(ticket))
	store.session.CredentialDigest = hex.EncodeToString(digest[:])
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	request := managedSessionAuthorityRequest()
	request.Capabilities = []rpc.Capability{managedsession.CapabilityLaunchContextV1, managedsession.CapabilityV1}
	request.LaunchTicket = ticket
	if _, err := authority.RegisterManagedSession(t.Context(), peer, request); err != nil {
		t.Fatalf("RegisterManagedSession(valid launch ticket) error = %v", err)
	}
	attachment := authority.managedSessions[request.ProjectID]
	if attachment.request.LaunchTicket != "" || attachment.launchTicketDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("managed replay identity retained launch proof: request=%#v digest=%q", attachment.request, attachment.launchTicketDigest)
	}

	store = managedSessionAuthorityFixture()
	authority = newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	request.LaunchTicket = "wrong-launch-ticket"
	if _, err := authority.RegisterManagedSession(t.Context(), peer, request); err == nil || !strings.Contains(err.Error(), "launch ticket") {
		t.Fatalf("RegisterManagedSession(wrong launch ticket) error = %v, want launch-ticket rejection", err)
	}
}

// TestAuthorityManagedSessionReportsPlannedStartupAsRetryable isolates the child-before-process-evidence race.
func TestAuthorityManagedSessionReportsPlannedStartupAsRetryable(t *testing.T) {
	store := managedSessionAuthorityFixture()
	store.session.State = domain.SessionPlanned
	store.session.Generation = 1
	store.session.Process = nil
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	request := managedSessionAuthorityRequest()
	request.ExpectedSessionGeneration = 1
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	_, err := authority.RegisterManagedSession(t.Context(), peer, request)
	if !errors.Is(err, managedsession.ErrManagedSessionAwaitingAttach) {
		t.Fatalf("RegisterManagedSession() error = %v, want awaiting-attach sentinel", err)
	}
}
