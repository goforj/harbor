package authority

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/managedsession"
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
