package authority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"sync"
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
	mutex        sync.Mutex
	replacements [][]dataplane.NativeRoute
	live         bool
	reconcileErr error
	liveErr      error
	callback     func()
}

// ReconcileManagedNativeRoutes records only actual relay generation changes.
func (routes *recordingManagedNativeRoutes) ReconcileManagedNativeRoutes(ctx context.Context, replacement []dataplane.NativeRoute) error {
	if routes.callback != nil {
		routes.callback()
	}
	routes.mutex.Lock()
	defer routes.mutex.Unlock()
	if routes.reconcileErr != nil {
		return routes.reconcileErr
	}
	if len(replacement) == 0 && len(routes.replacements) == 0 {
		routes.live = true
		return nil
	}
	if routes.live && len(routes.replacements) > 0 && reflect.DeepEqual(routes.replacements[len(routes.replacements)-1], replacement) {
		return nil
	}
	routes.replacements = append(routes.replacements, append([]dataplane.NativeRoute(nil), replacement...))
	routes.live = true
	return nil
}

// recordingManagedPublicationObserver returns the Harbor-owned replacement used by barrier activation assertions.
type recordingManagedPublicationObserver struct {
	publications         []harbordruntime.ManagedEndpointPublication
	calls                int
	lastFence            harbordruntime.ManagedPublicationFence
	allowProjectStarting bool
}

// ObserveManagedPublications records the exact fence and returns a fresh Harbor-owned publication set.
func (observer *recordingManagedPublicationObserver) ObserveManagedPublications(_ context.Context, _ domain.ProjectID, _ domain.SessionID, fence harbordruntime.ManagedPublicationFence) ([]harbordruntime.ManagedEndpointPublication, error) {
	return observer.observe(fence, false)
}

// ObserveManagedPublicationsForPhase records whether the barrier requested pre-ready Compose observation.
func (observer *recordingManagedPublicationObserver) ObserveManagedPublicationsForPhase(_ context.Context, _ domain.ProjectID, _ domain.SessionID, fence harbordruntime.ManagedPublicationFence, allowProjectStarting bool) ([]harbordruntime.ManagedEndpointPublication, error) {
	return observer.observe(fence, allowProjectStarting)
}

// observe records one publication observation without changing the returned topology.
func (observer *recordingManagedPublicationObserver) observe(fence harbordruntime.ManagedPublicationFence, allowProjectStarting bool) ([]harbordruntime.ManagedEndpointPublication, error) {
	observer.calls++
	observer.lastFence = fence
	observer.allowProjectStarting = allowProjectStarting
	return append([]harbordruntime.ManagedEndpointPublication(nil), observer.publications...), nil
}

// ReplaceManagedNativeRoutes records one complete route replacement for barrier assertions.
func (routes *recordingManagedNativeRoutes) ReplaceManagedNativeRoutes(_ context.Context, replacement []dataplane.NativeRoute) error {
	routes.mutex.Lock()
	defer routes.mutex.Unlock()
	routes.replacements = append(routes.replacements, append([]dataplane.NativeRoute(nil), replacement...))
	routes.live = true
	return nil
}

// ManagedNativeRoutesLive reports the configured route publication postcondition.
func (routes *recordingManagedNativeRoutes) ManagedNativeRoutesLive(context.Context, []dataplane.NativeRoute) error {
	routes.mutex.Lock()
	defer routes.mutex.Unlock()
	if routes.liveErr != nil {
		return routes.liveErr
	}
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

// multiManagedSessionAuthorityStore provides independent attached sessions for barrier-generation tests.
type multiManagedSessionAuthorityStore struct {
	*recordingStore
	sessions map[domain.ProjectID]domain.ProjectSession
}

// Project returns the aggregate project selected by one attachment request.
func (store *multiManagedSessionAuthorityStore) Project(_ context.Context, projectID domain.ProjectID) (state.ProjectRecord, error) {
	for _, project := range store.runtimeState.Snapshot.Projects {
		if project.ID == projectID {
			return state.ProjectRecord{Project: project}, nil
		}
	}
	return state.ProjectRecord{}, &state.ProjectNotFoundError{ProjectID: projectID}
}

// ActiveProjectSession returns the exact session selected by a barrier fence.
func (store *multiManagedSessionAuthorityStore) ActiveProjectSession(_ context.Context, projectID domain.ProjectID) (domain.ProjectSession, error) {
	session, found := store.sessions[projectID]
	if !found {
		return domain.ProjectSession{}, &state.ProjectSessionNotFoundError{ProjectID: projectID}
	}
	return session, nil
}

// CompleteManagedSessionAttachment is unreachable because these tests install already-attached fences.
func (store *multiManagedSessionAuthorityStore) CompleteManagedSessionAttachment(context.Context, state.CompleteManagedSessionAttachmentRequest) (domain.ProjectSession, error) {
	return domain.ProjectSession{}, errors.New("barrier test session attachment is already complete")
}

// fenceManagedPublicationObserver returns Harbor-owned publications for only the observed fence.
type fenceManagedPublicationObserver struct {
	publications map[harbordruntime.ManagedPublicationFence][]harbordruntime.ManagedEndpointPublication
	errs         map[harbordruntime.ManagedPublicationFence]error
	blockFence   harbordruntime.ManagedPublicationFence
	entered      chan struct{}
	release      chan struct{}
	once         sync.Once
	callback     func()
}

// ObserveManagedPublications observes one exact attached session without pre-ready access.
func (observer *fenceManagedPublicationObserver) ObserveManagedPublications(_ context.Context, _ domain.ProjectID, _ domain.SessionID, fence harbordruntime.ManagedPublicationFence) ([]harbordruntime.ManagedEndpointPublication, error) {
	return observer.ObserveManagedPublicationsForPhase(context.Background(), "", "", fence, false)
}

// ObserveManagedPublicationsForPhase returns only Harbor-owned facts associated with the requested fence.
func (observer *fenceManagedPublicationObserver) ObserveManagedPublicationsForPhase(_ context.Context, _ domain.ProjectID, _ domain.SessionID, fence harbordruntime.ManagedPublicationFence, _ bool) ([]harbordruntime.ManagedEndpointPublication, error) {
	if observer.callback != nil {
		observer.callback()
	}
	if fence == observer.blockFence && observer.entered != nil {
		observer.once.Do(func() { close(observer.entered) })
		<-observer.release
	}
	if err := observer.errs[fence]; err != nil {
		return nil, err
	}
	return append([]harbordruntime.ManagedEndpointPublication(nil), observer.publications[fence]...), nil
}

// TestAuthorityManagedBarrierRejectsReentrantBarrierWithoutDeadlock keeps injected observations outside mutex ownership.
func TestAuthorityManagedBarrierRejectsReentrantBarrierWithoutDeadlock(t *testing.T) {
	authority, store, peer, orders, payments, routes, observer := managedBarrierTestAuthority(t)
	store.runtimeState.Snapshot.Projects[0].State = domain.ProjectStarting
	store.runtimeState.Snapshot.Projects[1].State = domain.ProjectStarting
	observer.publications[orders] = []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(orders, "127.0.0.1:43007")}
	reentrant := make(chan error, 1)
	observer.callback = func() {
		_, err := authority.AcknowledgeManagedBarrier(context.Background(), peer, managedBarrierRequest(payments))
		reentrant <- err
	}
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(orders)); err != nil {
		t.Fatalf("outer barrier error = %v", err)
	}
	if err := <-reentrant; !errors.Is(err, managedsession.ErrManagedSessionNotReady) {
		t.Fatalf("reentrant barrier error = %v, want retryable not-ready", err)
	}
	observer.callback = nil
	reentrant = make(chan error, 1)
	routes.callback = func() {
		_, err := authority.AcknowledgeManagedBarrier(context.Background(), peer, managedBarrierRequest(payments))
		reentrant <- err
	}
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(orders)); err != nil {
		t.Fatalf("outer route-activation barrier error = %v", err)
	}
	if err := <-reentrant; !errors.Is(err, managedsession.ErrManagedSessionNotReady) {
		t.Fatalf("reentrant route-activation barrier error = %v, want retryable not-ready", err)
	}
}

// managedBarrierTestAuthority constructs two exact attached fences and a valid full Harbor aggregate.
func managedBarrierTestAuthority(t *testing.T) (*Authority, *multiManagedSessionAuthorityStore, local.PeerIdentity, harbordruntime.ManagedPublicationFence, harbordruntime.ManagedPublicationFence, *recordingManagedNativeRoutes, *fenceManagedPublicationObserver) {
	t.Helper()
	runtimeState := authorityRouteRuntimeState()
	runtimeState.Network.Revision = runtimeState.Snapshot.Sequence
	runtimeState.Network.CreatedAt = runtimeState.Snapshot.CapturedAt
	runtimeState.Network.UpdatedAt = runtimeState.Snapshot.CapturedAt
	runtimeState.Network.Ownership = identity.Ownership{InstallationID: "harbor-installation", Generation: 1}
	runtimeState.Network.Pool, _ = identity.NewPool(netip.MustParsePrefix("127.77.0.0/24"), []netip.Addr{netip.MustParseAddr("127.77.0.10"), netip.MustParseAddr("127.77.0.11")})
	runtimeState.Network.Leases = []identity.Lease{{Key: identity.LeaseKey{ProjectID: "project-orders"}, Address: netip.MustParseAddr("127.77.0.10"), Ownership: runtimeState.Network.Ownership}, {Key: identity.LeaseKey{ProjectID: "project-payments"}, Address: netip.MustParseAddr("127.77.0.11"), Ownership: runtimeState.Network.Ownership}}
	runtimeState.Network.Quarantines = []identity.Quarantine{}
	runtimeState.Network.Reservations.Listeners = state.SharedListenerReservations{
		DNS:   state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:53"), Bind: netip.MustParseAddrPort("127.0.0.2:53"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
		HTTP:  state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:80"), Bind: netip.MustParseAddrPort("127.0.0.2:80"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
		HTTPS: state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:443"), Bind: netip.MustParseAddrPort("127.0.0.2:443"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
	}
	runtimeState.Network.Reservations.Endpoints[0].Public = netip.MustParseAddrPort("127.0.0.2:443")
	runtimeState.Network.Reservations.SuppressedProjectIDs = []domain.ProjectID{}
	payments := runtimeState.Snapshot.Projects[0]
	payments.ID = "project-payments"
	payments.Name = "Payments"
	payments.Path = "/workspace/payments"
	payments.Slug = "payments"
	runtimeState.Snapshot.Projects = append(runtimeState.Snapshot.Projects, payments)
	runtimeState.Network.Reservations.Endpoints = append([]state.EndpointReservation{{Key: state.EndpointReservationKey{ProjectID: "project-payments", EndpointID: "service:mysql"}, Protocol: state.EndpointProtocolTCP, Host: "mysql.payments.test", Public: netip.MustParseAddrPort("127.77.0.11:3306"), Identity: &identity.LeaseKey{ProjectID: "project-payments"}, Generation: 1}}, runtimeState.Network.Reservations.Endpoints...)
	runtimeState.Network.Reservations.Endpoints = append(runtimeState.Network.Reservations.Endpoints, state.EndpointReservation{Key: state.EndpointReservationKey{ProjectID: "project-orders", EndpointID: "service:mysql"}, Protocol: state.EndpointProtocolTCP, Host: "mysql.orders.test", Public: netip.MustParseAddrPort("127.77.0.10:3306"), Identity: &identity.LeaseKey{ProjectID: "project-orders"}, Generation: 1})
	sort.Slice(runtimeState.Network.Reservations.Endpoints, func(left, right int) bool {
		return runtimeState.Network.Reservations.Endpoints[left].Host < runtimeState.Network.Reservations.Endpoints[right].Host
	})
	store := &multiManagedSessionAuthorityStore{recordingStore: &recordingStore{runtimeState: runtimeState}, sessions: make(map[domain.ProjectID]domain.ProjectSession)}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	routes := new(recordingManagedNativeRoutes)
	observer := &fenceManagedPublicationObserver{publications: make(map[harbordruntime.ManagedPublicationFence][]harbordruntime.ManagedEndpointPublication), errs: make(map[harbordruntime.ManagedPublicationFence]error)}
	authority.managedRoutes = routes
	authority.managedObserver = observer
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	orders := managedBarrierTestAttachment(t, authority, store, peer, "project-orders", "session-orders")
	paymentsFence := managedBarrierTestAttachment(t, authority, store, peer, "project-payments", "session-payments")
	return authority, store, peer, orders, paymentsFence, routes, observer
}

// managedBarrierTestAttachment installs one durable attached session and matching process-local registry fence.
func managedBarrierTestAttachment(t *testing.T, authority *Authority, store *multiManagedSessionAuthorityStore, peer local.PeerIdentity, projectID domain.ProjectID, sessionID domain.SessionID) harbordruntime.ManagedPublicationFence {
	t.Helper()
	session := managedSessionAuthorityFixture().session
	session.ID = sessionID
	session.ProjectID = projectID
	session.State = domain.SessionAttached
	session.Generation = 3
	store.sessions[projectID] = session
	fence, err := authority.managedRegistry.Open(session)
	if err != nil {
		t.Fatalf("open %q registry fence: %v", projectID, err)
	}
	authority.managedSessions[projectID] = managedSessionAttachment{response: managedsession.RegisterResponse{Fence: fence}, peer: peer}
	return fence
}

// managedBarrierRequest returns the only currently supported Compose barrier request.
func managedBarrierRequest(fence harbordruntime.ManagedPublicationFence) managedsession.BarrierRequest {
	return managedsession.BarrierRequest{SchemaVersion: managedsession.SchemaVersion, Fence: fence, Phase: managedsession.BarrierPhaseCompose, AcceptedProjectIdentity: "project"}
}

// managedBarrierPublication returns one exact service observation for the selected reservation.
func managedBarrierPublication(fence harbordruntime.ManagedPublicationFence, upstream string) harbordruntime.ManagedEndpointPublication {
	return harbordruntime.ManagedEndpointPublication{Fence: fence, EndpointID: "service:mysql", ReservationGeneration: 1, Upstream: netip.MustParseAddrPort(upstream)}
}

// TestAuthorityManagedBarrierSkipsUnacknowledgedAttachments keeps one project from publishing another process's registry facts.
func TestAuthorityManagedBarrierSkipsUnacknowledgedAttachments(t *testing.T) {
	authority, store, peer, orders, payments, routes, observer := managedBarrierTestAuthority(t)
	store.runtimeState.Snapshot.Projects[1].State = domain.ProjectStarting
	observer.publications[orders] = []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(orders, "127.0.0.1:43007")}
	if err := authority.managedRegistry.Replace(payments, []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(payments, "127.0.0.1:43008")}); err != nil {
		t.Fatalf("seed unacknowledged starting publication: %v", err)
	}
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(orders)); err != nil {
		t.Fatalf("acknowledge ready orders barrier: %v", err)
	}
	if len(routes.replacements) != 1 || len(routes.replacements[0]) != 1 {
		t.Fatalf("replacements = %#v, want only the observed orders route", routes.replacements)
	}

	store.runtimeState.Snapshot.Projects[1].State = domain.ProjectReady
	if _, err := authority.ReplaceManagedPublications(t.Context(), peer, managedsession.ReplacePublicationsRequest{SchemaVersion: managedsession.SchemaVersion, Fence: payments, Publications: []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(payments, "127.0.0.1:43008")}}); err != nil {
		t.Fatalf("replace unacknowledged ready publication: %v", err)
	}
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(orders)); err != nil {
		t.Fatalf("acknowledge orders after ready replacement: %v", err)
	}
	if len(routes.replacements[len(routes.replacements)-1]) != 1 {
		t.Fatalf("latest replacement = %#v, want unacknowledged ready payments withheld", routes.replacements[len(routes.replacements)-1])
	}
}

// TestAuthorityManagedBarrierSerializesStartingProjects keeps each acknowledged Starting fence in later generations.
func TestAuthorityManagedBarrierSerializesStartingProjects(t *testing.T) {
	authority, store, peer, orders, payments, routes, observer := managedBarrierTestAuthority(t)
	store.runtimeState.Snapshot.Projects[0].State = domain.ProjectStarting
	store.runtimeState.Snapshot.Projects[1].State = domain.ProjectStarting
	observer.publications[orders] = []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(orders, "127.0.0.1:43007")}
	observer.publications[payments] = []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(payments, "127.0.0.1:43008")}
	observer.blockFence = orders
	observer.entered = make(chan struct{})
	observer.release = make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		_, err := authority.AcknowledgeManagedBarrier(context.Background(), peer, managedBarrierRequest(orders))
		completed <- err
	}()
	<-observer.entered
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(payments)); !errors.Is(err, managedsession.ErrManagedSessionNotReady) {
		t.Fatalf("concurrent payments barrier error = %v, want retryable not-ready", err)
	}
	if _, err := authority.ReplaceManagedPublications(t.Context(), peer, managedsession.ReplacePublicationsRequest{SchemaVersion: managedsession.SchemaVersion, Fence: payments, Publications: []harbordruntime.ManagedEndpointPublication{}}); !errors.Is(err, managedsession.ErrManagedSessionNotReady) {
		t.Fatalf("replace during orders barrier error = %v, want retryable not-ready", err)
	}
	close(observer.release)
	if err := <-completed; err != nil {
		t.Fatalf("acknowledge orders barrier: %v", err)
	}
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(payments)); err != nil {
		t.Fatalf("retry payments barrier: %v", err)
	}
	if !authority.managedSessions[orders.ProjectID].composeAcknowledged || !authority.managedSessions[payments.ProjectID].composeAcknowledged {
		t.Fatalf("attachments = %#v, want both exact Compose fences acknowledged", authority.managedSessions)
	}
	if got := len(routes.replacements[len(routes.replacements)-1]); got != 2 {
		t.Fatalf("latest replacement route count = %d, want both acknowledged Starting routes", got)
	}
}

// TestAuthorityManagedBarrierWithdrawsFailedReobservationsAndTerminalAttachments prevents stale trust from surviving either path.
func TestAuthorityManagedBarrierWithdrawsFailedReobservationsAndTerminalAttachments(t *testing.T) {
	authority, store, peer, orders, payments, routes, observer := managedBarrierTestAuthority(t)
	store.runtimeState.Snapshot.Projects[0].State = domain.ProjectStarting
	store.runtimeState.Snapshot.Projects[1].State = domain.ProjectStarting
	observer.publications[orders] = []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(orders, "127.0.0.1:43007")}
	observer.publications[payments] = []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(payments, "127.0.0.1:43008")}
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(orders)); err != nil {
		t.Fatalf("initial orders barrier: %v", err)
	}
	routes.reconcileErr = errors.New("native route replace failed")
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(orders)); err == nil {
		t.Fatal("re-observation barrier succeeded after reconcile failure")
	}
	if authority.managedSessions[orders.ProjectID].composeAcknowledged {
		t.Fatal("failed re-observation retained prior orders acknowledgement")
	}
	routes.reconcileErr = nil
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(payments)); err != nil {
		t.Fatalf("payments barrier after orders failure: %v", err)
	}
	if got := len(routes.replacements[len(routes.replacements)-1]); got != 1 {
		t.Fatalf("replacement after failed orders re-observation has %d routes, want payments only", got)
	}
	store.runtimeState.Snapshot.Projects[1].State = domain.ProjectFailed
	observer.publications[orders] = []harbordruntime.ManagedEndpointPublication{managedBarrierPublication(orders, "127.0.0.1:43007")}
	if _, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(orders)); err != nil {
		t.Fatalf("healthy orders barrier with failed payments attachment: %v", err)
	}
	if got := len(routes.replacements[len(routes.replacements)-1]); got != 1 {
		t.Fatalf("replacement with failed payments attachment has %d routes, want orders only", got)
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

// TestAuthorityManagedSessionReplaysDurableAttachmentAfterRestart proves a fresh authority can rebuild only its
// ephemeral publication stream from the exact attached process evidence already persisted by the prior daemon.
func TestAuthorityManagedSessionReplaysDurableAttachmentAfterRestart(t *testing.T) {
	store := managedSessionAuthorityFixture()
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	request := managedSessionAuthorityRequest()
	first := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	response, err := first.RegisterManagedSession(t.Context(), peer, request)
	if err != nil {
		t.Fatalf("initial RegisterManagedSession() error = %v", err)
	}
	if response.Fence.SessionGeneration != 3 {
		t.Fatalf("initial fence generation = %d, want 3", response.Fence.SessionGeneration)
	}

	restarted := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	request.ExpectedSessionGeneration = 2
	replayed, err := restarted.RegisterManagedSession(t.Context(), peer, request)
	if err != nil {
		t.Fatalf("replayed RegisterManagedSession() error = %v", err)
	}
	if replayed.Fence != response.Fence || replayed.AttachmentTicket == "" {
		t.Fatalf("replayed response = %#v, want the durable fence and a fresh ephemeral ticket", replayed)
	}
	if store.attachmentCalls != 1 || store.session.State != domain.SessionAttached || store.session.Generation != 3 {
		t.Fatalf("durable replay mutated attachment state: calls=%d state=%q generation=%d", store.attachmentCalls, store.session.State, store.session.Generation)
	}
	if restarted.managedSessions[replayed.Fence.ProjectID].composeAcknowledged {
		t.Fatal("replayed attachment retained a prior process-local Compose acknowledgement")
	}
	if _, err := restarted.ReplaceManagedPublications(t.Context(), peer, managedsession.ReplacePublicationsRequest{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         replayed.Fence,
		Publications:  []harbordruntime.ManagedEndpointPublication{},
	}); err != nil {
		t.Fatalf("publication after replay = %v", err)
	}
}

// TestAuthorityManagedSessionReplayRejectsIdentityDrift keeps a restarted daemon from rebuilding a fence for a
// different generation, descriptor, or operating-system process.
func TestAuthorityManagedSessionReplayRejectsIdentityDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*managedsession.RegisterRequest, *local.PeerIdentity)
		want   string
	}{
		{name: "generation", mutate: func(request *managedsession.RegisterRequest, _ *local.PeerIdentity) {
			request.ExpectedSessionGeneration = 1
		}, want: "generation"},
		{name: "descriptor", mutate: func(request *managedsession.RegisterRequest, _ *local.PeerIdentity) {
			request.DescriptorDigest = strings.Repeat("d", 64)
		}, want: "descriptor"},
		{name: "peer", mutate: func(_ *managedsession.RegisterRequest, peer *local.PeerIdentity) { peer.ProcessID++ }, want: "peer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := managedSessionAuthorityFixture()
			request := managedSessionAuthorityRequest()
			peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
			first := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
			if _, err := first.RegisterManagedSession(t.Context(), peer, request); err != nil {
				t.Fatalf("initial RegisterManagedSession() error = %v", err)
			}
			restarted := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
			test.mutate(&request, &peer)
			if _, err := restarted.RegisterManagedSession(t.Context(), peer, request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("replayed RegisterManagedSession() error = %v, want %q rejection", err, test.want)
			}
		})
	}
}

// TestAuthorityManagedSessionReplayRequiresFreshComposeObservation keeps a restarted daemon from activating caller-supplied Starting publications.
func TestAuthorityManagedSessionReplayRequiresFreshComposeObservation(t *testing.T) {
	_, store, peer, orders, _, routes, observer := managedBarrierTestAuthority(t)
	store.runtimeState.Snapshot.Projects[0].State = domain.ProjectStarting
	restarted := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	restarted.managedRoutes = routes
	request := managedSessionAuthorityRequest()
	replayed, err := restarted.RegisterManagedSession(t.Context(), peer, request)
	if err != nil {
		t.Fatalf("replay managed session: %v", err)
	}
	publication := managedBarrierPublication(replayed.Fence, "127.0.0.1:43007")
	if _, err := restarted.ReplaceManagedPublications(t.Context(), peer, managedsession.ReplacePublicationsRequest{SchemaVersion: managedsession.SchemaVersion, Fence: replayed.Fence, Publications: []harbordruntime.ManagedEndpointPublication{publication}}); err != nil {
		t.Fatalf("replace replayed publication: %v", err)
	}
	if _, err := restarted.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(replayed.Fence)); !errors.Is(err, managedsession.ErrManagedSessionNotReady) {
		t.Fatalf("replayed caller-only barrier error = %v, want fresh-observation rejection", err)
	}
	if len(routes.replacements) != 0 {
		t.Fatalf("caller-only replay replacement activated routes = %#v", routes.replacements)
	}
	observer.publications[replayed.Fence] = []harbordruntime.ManagedEndpointPublication{publication}
	restarted.managedObserver = observer
	if _, err := restarted.AcknowledgeManagedBarrier(t.Context(), peer, managedBarrierRequest(replayed.Fence)); err != nil {
		t.Fatalf("freshly observed replay barrier: %v", err)
	}
	if len(routes.replacements) != 1 || len(routes.replacements[0]) != 1 || routes.replacements[0][0].ID != string(orders.ProjectID)+":service:mysql" {
		t.Fatalf("fresh replay activation routes = %#v, want one orders route", routes.replacements)
	}
}

// TestAuthorityManagedBarrierRequiresLiveRouteActivator proves the typed barrier becomes positive only after the controller accepts the complete route set.
func TestAuthorityManagedBarrierRequiresLiveRouteActivator(t *testing.T) {
	store := managedSessionAuthorityFixture()
	store.recordingStore.runtimeState = authorityRouteRuntimeState()
	runtimeState := &store.recordingStore.runtimeState
	runtimeState.Snapshot.Projects[0].State = domain.ProjectStarting
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
	if !observer.allowProjectStarting {
		t.Fatal("publication observer did not receive the Compose pre-ready allowance")
	}
}

// TestAuthorityManagedBarrierAcceptsDirectNativePublication proves a Compose-owned exact public bind satisfies the
// barrier without creating the self-relay that would otherwise connect the listener back to itself.
func TestAuthorityManagedBarrierAcceptsDirectNativePublication(t *testing.T) {
	store := managedSessionAuthorityFixture()
	store.recordingStore.runtimeState = authorityRouteRuntimeState()
	runtimeState := &store.recordingStore.runtimeState
	runtimeState.Network.Revision = runtimeState.Snapshot.Sequence
	runtimeState.Network.CreatedAt = runtimeState.Snapshot.CapturedAt
	runtimeState.Network.UpdatedAt = runtimeState.Snapshot.CapturedAt
	runtimeState.Network.Ownership = identity.Ownership{InstallationID: "harbor-installation", Generation: 1}
	runtimeState.Network.Pool, _ = identity.NewPool(netip.MustParsePrefix("127.77.0.0/24"), []netip.Addr{netip.MustParseAddr("127.77.0.10")})
	runtimeState.Network.Leases = []identity.Lease{{Key: identity.LeaseKey{ProjectID: "project-orders"}, Address: netip.MustParseAddr("127.77.0.10"), Ownership: identity.Ownership{InstallationID: "harbor-installation", Generation: 1}}}
	runtimeState.Network.Quarantines = []identity.Quarantine{}
	runtimeState.Network.Reservations.Listeners = state.SharedListenerReservations{
		DNS:   state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:53"), Bind: netip.MustParseAddrPort("127.0.0.2:53"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
		HTTP:  state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:80"), Bind: netip.MustParseAddrPort("127.0.0.2:80"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
		HTTPS: state.ListenerReservation{Mode: state.ListenerModeDirect, Advertised: netip.MustParseAddrPort("127.0.0.2:443"), Bind: netip.MustParseAddrPort("127.0.0.2:443"), Generation: 1, VerifiedAt: runtimeState.Snapshot.CapturedAt},
	}
	runtimeState.Network.Reservations.Endpoints[0].Public = netip.MustParseAddrPort("127.0.0.2:443")
	runtimeState.Network.Reservations.Endpoints = append([]state.EndpointReservation{{Key: state.EndpointReservationKey{ProjectID: "project-orders", EndpointID: "service:mysql"}, Protocol: state.EndpointProtocolTCP, Host: "mysql.orders.test", Public: netip.MustParseAddrPort("127.77.0.10:3306"), Identity: &identity.LeaseKey{ProjectID: "project-orders"}, Generation: 1}}, runtimeState.Network.Reservations.Endpoints...)
	runtimeState.Network.Reservations.SuppressedProjectIDs = []domain.ProjectID{}
	activator := new(recordingManagedNativeRoutes)
	observer := &recordingManagedPublicationObserver{publications: []harbordruntime.ManagedEndpointPublication{{
		Fence:                 harbordruntime.ManagedPublicationFence{ProjectID: "project-orders", SessionID: "session-orders", SessionGeneration: 3},
		EndpointID:            "service:mysql",
		ReservationGeneration: 1,
		Upstream:              netip.MustParseAddrPort("127.77.0.10:3306"),
	}}}
	authority := newAuthority(store, testProjectUnregisterApprovals(), buildinfo.Info{Version: "dev"}, testProjectLifecycles(), testNetworkSetups(), testNetworkResolverSetups(), testHTTPRoutes())
	authority.managedRoutes = activator
	authority.managedObserver = observer
	peer := local.PeerIdentity{UserID: "501", ProcessID: 321}
	response, err := authority.RegisterManagedSession(t.Context(), peer, managedSessionAuthorityRequest())
	if err != nil {
		t.Fatalf("RegisterManagedSession() error = %v", err)
	}
	barrier, err := authority.AcknowledgeManagedBarrier(t.Context(), peer, managedsession.BarrierRequest{SchemaVersion: managedsession.SchemaVersion, Fence: response.Fence, Phase: managedsession.BarrierPhaseCompose, AcceptedProjectIdentity: "orders"})
	if err != nil {
		t.Fatalf("AcknowledgeManagedBarrier() error = %v", err)
	}
	if !barrier.Acknowledged || len(activator.replacements) != 1 || !activator.replacements[0][0].Direct {
		t.Fatalf("barrier = %#v, publications = %#v; want acknowledged direct DNS publication", barrier, activator.replacements)
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
