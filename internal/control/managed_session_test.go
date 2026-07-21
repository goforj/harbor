package control

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/managedsession"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// recordingManagedControlAuthority returns correlated responses while retaining the authenticated peer.
type recordingManagedControlAuthority struct {
	peer             local.PeerIdentity
	registerCalls    int
	publicationCalls int
	barrierCalls     int
}

// RegisterManagedSession returns the next fence for one authenticated request.
func (authority *recordingManagedControlAuthority) RegisterManagedSession(_ context.Context, peer local.PeerIdentity, request managedsession.RegisterRequest) (managedsession.RegisterResponse, error) {
	authority.peer = peer
	authority.registerCalls++
	return managedsession.RegisterResponse{
		SchemaVersion: managedsession.SchemaVersion,
		Fence: harbordruntime.ManagedPublicationFence{
			ProjectID:         request.ProjectID,
			SessionID:         request.SessionID,
			SessionGeneration: request.ExpectedSessionGeneration + 1,
		},
		AttachmentTicket: "ticket",
	}, nil
}

// ReplaceManagedPublications acknowledges one complete bounded replacement.
func (authority *recordingManagedControlAuthority) ReplaceManagedPublications(_ context.Context, peer local.PeerIdentity, request managedsession.ReplacePublicationsRequest) (managedsession.ReplacePublicationsResponse, error) {
	authority.peer = peer
	authority.publicationCalls++
	return managedsession.ReplacePublicationsResponse{
		SchemaVersion:    managedsession.SchemaVersion,
		Fence:            request.Fence,
		Accepted:         true,
		PublicationCount: uint16(len(request.Publications)),
	}, nil
}

// AcknowledgeManagedBarrier acknowledges one exact lifecycle phase.
func (authority *recordingManagedControlAuthority) AcknowledgeManagedBarrier(_ context.Context, peer local.PeerIdentity, request managedsession.BarrierRequest) (managedsession.BarrierResponse, error) {
	authority.peer = peer
	authority.barrierCalls++
	return managedsession.BarrierResponse{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         request.Fence,
		Phase:         request.Phase,
		Acknowledged:  true,
	}, nil
}

// TestControlServerRoutesManagedRoleToExclusiveHandlers proves the production endpoint can admit only the configured managed surface.
func TestControlServerRoutesManagedRoleToExclusiveHandlers(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	managedAuthority := new(recordingManagedControlAuthority)
	controlServer, err := newServer(ServerConfig{
		Authority:        &recordingAuthority{},
		RequestShutdown:  func() {},
		ManagedAuthority: managedAuthority,
	}, testBuild)
	if err != nil {
		t.Fatalf("construct control server: %v", err)
	}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(serverContext, serverConnection)
	}()
	managedClient, err := session.NewClient(context.Background(), clientConnection, session.ClientConfig{
		Role:           rpc.RoleGoForjSession,
		ClientVersion:  "forj-test",
		ProtocolRanges: protocolRanges(),
		Capabilities:   []rpc.Capability{managedsession.CapabilityLaunchContextV1, managedsession.CapabilityV1},
	})
	if err != nil {
		cancelServer()
		t.Fatalf("negotiate managed client: %v", err)
	}
	if !containsCapability(managedClient.Peer().Capabilities, managedsession.CapabilityLaunchContextV1) {
		t.Fatal("managed server did not advertise the launch-context capability")
	}
	t.Cleanup(func() {
		_ = managedClient.Close()
		cancelServer()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("managed control server did not stop")
		}
	})

	registerRequest := managedsession.RegisterRequest{
		SchemaVersion:             managedsession.SchemaVersion,
		ProjectID:                 "project-orders",
		SessionID:                 "session-orders",
		ProjectRoot:               "/workspace/orders",
		ExpectedSessionGeneration: 1,
		DescriptorDigest:          strings.Repeat("a", 64),
		ClientNonce:               "nonce",
		Owner:                     domain.SessionOwnerHarbor,
		Capabilities:              []rpc.Capability{managedsession.CapabilityLaunchContextV1, managedsession.CapabilityV1},
		ActiveApps:                []managedsession.ActiveApp{},
		LaunchTicket:              strings.Repeat("b", 64),
	}
	registerPayload, err := managedsession.MarshalRegisterRequest(registerRequest)
	if err != nil {
		t.Fatalf("marshal register request: %v", err)
	}
	registerResponsePayload, err := managedClient.Call(t.Context(), managedsession.MethodRegister, json.RawMessage(registerPayload))
	if err != nil {
		t.Fatalf("managed register call: %v", err)
	}
	registerResponse, err := managedsession.DecodeRegisterResponse(registerResponsePayload)
	if err != nil {
		t.Fatalf("decode register response: %v", err)
	}

	publicationRequest := managedsession.ReplacePublicationsRequest{
		SchemaVersion: managedsession.SchemaVersion,
		Fence:         registerResponse.Fence,
		Publications:  []harbordruntime.ManagedEndpointPublication{},
	}
	publicationPayload, err := managedsession.MarshalReplacePublicationsRequest(publicationRequest)
	if err != nil {
		t.Fatalf("marshal publication request: %v", err)
	}
	if _, err := managedClient.Call(t.Context(), managedsession.MethodReplacePublications, json.RawMessage(publicationPayload)); err != nil {
		t.Fatalf("managed publication call: %v", err)
	}

	barrierRequest := managedsession.BarrierRequest{
		SchemaVersion:           managedsession.SchemaVersion,
		Fence:                   registerResponse.Fence,
		Phase:                   managedsession.BarrierPhaseCompose,
		AcceptedProjectIdentity: "orders",
	}
	barrierPayload, err := managedsession.MarshalBarrierRequest(barrierRequest)
	if err != nil {
		t.Fatalf("marshal barrier request: %v", err)
	}
	barrierResponsePayload, err := managedClient.Call(t.Context(), managedsession.MethodBarrier, json.RawMessage(barrierPayload))
	if err != nil {
		t.Fatalf("managed barrier call: %v", err)
	}
	barrierResponse, err := managedsession.DecodeBarrierResponse(barrierResponsePayload)
	if err != nil {
		t.Fatalf("decode barrier response: %v", err)
	}
	if !barrierResponse.Acknowledged || managedAuthority.registerCalls != 1 || managedAuthority.publicationCalls != 1 || managedAuthority.barrierCalls != 1 {
		t.Fatalf("managed authority calls = register %d publication %d barrier %d response %#v", managedAuthority.registerCalls, managedAuthority.publicationCalls, managedAuthority.barrierCalls, barrierResponse)
	}
	if managedAuthority.peer != testClientPeer {
		t.Fatalf("managed authority peer = %#v, want %#v", managedAuthority.peer, testClientPeer)
	}
}
