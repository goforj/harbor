package control

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestDecodeEmptyRequestRequiresOneEmptyObject verifies every alternate JSON shape fails closed.
func TestDecodeEmptyRequestRequiresOneEmptyObject(t *testing.T) {
	if err := decodeEmptyRequest([]byte(" \n {} \t")); err != nil {
		t.Fatalf("decode empty object: %v", err)
	}

	tooLarge := "{" + strings.Repeat(" ", maximumEmptyRequestBytes) + "}"
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "missing"},
		{name: "null", payload: "null"},
		{name: "array", payload: "[]"},
		{name: "field", payload: `{"field":true}`},
		{name: "malformed", payload: "{"},
		{name: "concatenated", payload: "{} {}"},
		{name: "trailing malformed", payload: "{} x"},
		{name: "too large", payload: tooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := decodeEmptyRequest([]byte(test.payload)); err == nil {
				t.Fatalf("decodeEmptyRequest(%q) succeeded", test.payload)
			}
		})
	}
}

// TestTransportPeerValidationBoundsAuthenticatedIdentity verifies impossible OS identity shapes are rejected.
func TestTransportPeerValidationBoundsAuthenticatedIdentity(t *testing.T) {
	if err := validateTransportPeer(local.PeerIdentity{UserID: "S-1-5-21-1000", ProcessID: 1}); err != nil {
		t.Fatalf("validate transport peer: %v", err)
	}

	for _, test := range []struct {
		name string
		peer local.PeerIdentity
	}{
		{name: "missing user", peer: local.PeerIdentity{ProcessID: 1}},
		{name: "surrounding whitespace", peer: local.PeerIdentity{UserID: " 501", ProcessID: 1}},
		{name: "long user", peer: local.PeerIdentity{UserID: strings.Repeat("1", 257), ProcessID: 1}},
		{name: "control user", peer: local.PeerIdentity{UserID: "50\n1", ProcessID: 1}},
		{name: "invalid UTF-8", peer: local.PeerIdentity{UserID: string([]byte{0xff}), ProcessID: 1}},
		{name: "missing process", peer: local.PeerIdentity{UserID: "501"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateTransportPeer(test.peer); err == nil {
				t.Fatalf("validateTransportPeer(%#v) succeeded", test.peer)
			}
		})
	}
}

// TestControlSnapshotValidationRequiresArrayCollections verifies valid domain state still has one JSON collection shape.
func TestControlSnapshotValidationRequiresArrayCollections(t *testing.T) {
	snapshot := testSnapshot()
	if err := validateControlSnapshot(snapshot); err != nil {
		t.Fatalf("validate canonical control snapshot: %v", err)
	}
	snapshot.Projects = nil
	if err := validateControlSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "collections") {
		t.Fatalf("nil collection error = %v, want initialized collection failure", err)
	}

	snapshot = testSnapshot()
	snapshot.Sequence = domain.Sequence(rpc.MaximumSequence + 1)
	if err := validateControlSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("oversized sequence error = %v, want sequence failure", err)
	}

	project := domain.ProjectSnapshot{
		ID:        "project-1",
		Name:      "Project One",
		Path:      "/workspace/project-one",
		Slug:      "project-one",
		State:     domain.ProjectStopped,
		UpdatedAt: time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC),
		Apps:      []domain.AppSnapshot{},
		Services:  []domain.ServiceSnapshot{},
		Resources: []domain.ResourceSnapshot{},
	}
	for _, test := range []struct {
		name   string
		mutate func(*domain.ProjectSnapshot)
	}{
		{name: "Apps", mutate: func(project *domain.ProjectSnapshot) { project.Apps = nil }},
		{name: "services", mutate: func(project *domain.ProjectSnapshot) { project.Services = nil }},
		{name: "resources", mutate: func(project *domain.ProjectSnapshot) { project.Resources = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := project
			test.mutate(&candidate)
			snapshot := testSnapshot()
			snapshot.Projects = []domain.ProjectSnapshot{candidate}
			if err := validateControlSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "project") {
				t.Fatalf("project collection error = %v, want initialized project collections", err)
			}
		})
	}
}

// TestControlAuthorizationRequiresRoleCapabilityAndLiveContext verifies negotiation policy is independently testable.
func TestControlAuthorizationRequiresRoleCapabilityAndLiveContext(t *testing.T) {
	valid := rpc.Hello{Role: rpc.RoleCLI, Capabilities: capabilities()}
	if err := authorizeControlHello(context.Background(), valid); err != nil {
		t.Fatalf("authorize valid control client: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := authorizeControlHello(cancelled, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authorization error = %v, want context cancellation", err)
	}
	wrongRole := valid
	wrongRole.Role = rpc.RoleGoForjSession
	if err := authorizeControlHello(context.Background(), wrongRole); err == nil {
		t.Fatal("authorization accepted GoForj session role")
	}
	missingCapability := valid
	missingCapability.Capabilities = nil
	if err := authorizeControlHello(context.Background(), missingCapability); err == nil {
		t.Fatal("authorization accepted a client without control.v1")
	}
}

// TestCallerFromRequestRequiresNegotiatedProductBoundary verifies dispatch repeats handshake invariants.
func TestCallerFromRequestRequiresNegotiatedProductBoundary(t *testing.T) {
	valid := session.Request{Peer: session.Peer{
		Role:         rpc.RoleDesktop,
		Protocol:     protocolV1,
		Capabilities: daemonCapabilities(false, false, false, false, false),
	}}
	caller, err := callerFromRequest(testClientPeer, valid)
	if err != nil || caller.Transport != testClientPeer {
		t.Fatalf("callerFromRequest() = %#v, %v", caller, err)
	}

	wrongRole := valid
	wrongRole.Peer.Role = rpc.RoleGoForjSession
	if _, err := callerFromRequest(testClientPeer, wrongRole); err == nil {
		t.Fatal("dispatch accepted GoForj session role")
	}
	wrongProtocol := valid
	wrongProtocol.Peer.Protocol.Minor++
	if _, err := callerFromRequest(testClientPeer, wrongProtocol); err == nil {
		t.Fatal("dispatch accepted another protocol")
	}
	missingCapability := valid
	missingCapability.Peer.Capabilities = nil
	if _, err := callerFromRequest(testClientPeer, missingCapability); err == nil {
		t.Fatal("dispatch accepted a missing capability")
	}
}

// TestBuildValidationUsesNegotiationTokenGrammar verifies status cannot accept metadata rejected by Hello.
func TestBuildValidationUsesNegotiationTokenGrammar(t *testing.T) {
	if err := validateBuild(Build{Version: "v1.2.3+meta"}); err != nil {
		t.Fatalf("validate SemVer build metadata: %v", err)
	}
	if err := validateBuildToken("revision", "", true); err != nil {
		t.Fatalf("validate absent optional revision: %v", err)
	}
	for _, test := range []struct {
		name  string
		build Build
	}{
		{name: "missing", build: Build{}},
		{name: "whitespace", build: Build{Version: " dev"}},
		{name: "long", build: Build{Version: strings.Repeat("a", maximumBuildToken+1)}},
		{name: "Unicode", build: Build{Version: "développement"}},
		{name: "punctuation", build: Build{Version: "v1/2"}},
		{name: "revision", build: Build{Version: "dev", Revision: "bad revision"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateBuild(test.build); err == nil {
				t.Fatalf("validateBuild(%#v) succeeded", test.build)
			}
		})
	}
	for _, value := range []string{"abc", "ABC", "123", "v1.2_3-4:5+6"} {
		if err := validateBuildToken("token", value, false); err != nil {
			t.Fatalf("validate supported token %q: %v", value, err)
		}
	}
}

// TestAuthorityErrorMapsOnlyReviewedCategories verifies local causes never define their wire message.
func TestAuthorityErrorMapsOnlyReviewedCategories(t *testing.T) {
	if !errors.Is(authorityError(context.Canceled), context.Canceled) {
		t.Fatal("authorityError did not preserve cancellation")
	}
	if !errors.Is(authorityError(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("authorityError did not preserve deadline")
	}
	want := errors.New("private state detail")
	converted := authorityError(want)
	var handlerError *session.HandlerError
	if !errors.As(converted, &handlerError) || handlerError.Code() != rpc.ErrorCodeInternal || !errors.Is(converted, want) {
		t.Fatalf("authorityError() = %#v, want internal handler error wrapping cause", converted)
	}
}

// TestNetworkSetupObservationMessageAllowsOnlyReviewedNativeDiagnostics verifies dynamic setup text fails closed at the control boundary.
func TestNetworkSetupObservationMessageAllowsOnlyReviewedNativeDiagnostics(t *testing.T) {
	address := netip.MustParseAddr("127.77.10.8")
	hostDetail := "observe Darwin host conflicts: host conflict Darwin route contains inconsistent IPv4 gateway evidence"
	wantHost := "Harbor could not inspect host conflicts for 127.77.10.8: " + hostDetail
	if got := networkSetupObservationMessage(NetworkSetupObservationHostConflicts, address, hostDetail); got != wantHost {
		t.Fatalf("host observation message = %q, want %q", got, wantHost)
	}
	assignmentDetail := "loopback observe 127.77.10.8: observe-failed"
	wantAssignment := "Harbor could not inspect loopback assignment for 127.77.10.8: " + assignmentDetail
	if got := networkSetupObservationMessage(NetworkSetupObservationAssignment, address, assignmentDetail); got != wantAssignment {
		t.Fatalf("assignment observation message = %q, want %q", got, wantAssignment)
	}

	fallback := rpc.NewWireError(rpc.ErrorCodeNetworkObservationFailed).Message
	for _, test := range []struct {
		name    string
		stage   NetworkSetupObservationStage
		address netip.Addr
		detail  string
	}{
		{name: "unsupported stage", stage: "filesystem", address: address, detail: hostDetail},
		{name: "foreign loopback", stage: NetworkSetupObservationHostConflicts, address: netip.MustParseAddr("127.78.10.8"), detail: hostDetail},
		{name: "non-loopback", stage: NetworkSetupObservationHostConflicts, address: netip.MustParseAddr("10.0.0.8"), detail: hostDetail},
		{name: "unreviewed prefix", stage: NetworkSetupObservationHostConflicts, address: address, detail: "database unavailable"},
		{name: "wrong assignment address", stage: NetworkSetupObservationAssignment, address: address, detail: "loopback observe 127.77.10.9: observe-failed"},
		{name: "secret", stage: NetworkSetupObservationHostConflicts, address: address, detail: "observe Darwin host conflicts: APP_KEY=secret-value"},
		{name: "control", stage: NetworkSetupObservationHostConflicts, address: address, detail: "observe Darwin host conflicts: failed\nforged"},
		{name: "format", stage: NetworkSetupObservationHostConflicts, address: address, detail: "observe Darwin host conflicts: failed\u2060forged"},
		{name: "padded", stage: NetworkSetupObservationHostConflicts, address: address, detail: " observe Darwin host conflicts: failed"},
		{name: "oversize", stage: NetworkSetupObservationHostConflicts, address: address, detail: "observe Darwin host conflicts: " + strings.Repeat("x", maximumNetworkObservationDetailBytes)},
		{name: "invalid UTF-8", stage: NetworkSetupObservationHostConflicts, address: address, detail: string([]byte{0xff})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := networkSetupObservationMessage(test.stage, test.address, test.detail); got != fallback {
				t.Fatalf("network setup observation message = %q, want fallback %q", got, fallback)
			}
		})
	}
}

// TestDaemonStatusValidationAllowsAdditiveCanonicalCapabilities verifies future negotiated surfaces remain additive.
func TestDaemonStatusValidationAllowsAdditiveCanonicalCapabilities(t *testing.T) {
	status := testStatus()
	status.Capabilities = []rpc.Capability{CapabilityV1, "events.v1"}
	status.Sequence = domain.Sequence(rpc.MaximumSequence)
	if err := status.Validate(); err != nil {
		t.Fatalf("validate additive daemon status: %v", err)
	}
}

// TestDaemonStatusValidationRejectsInvalidCapabilitySyntax verifies malformed advertised features fail structurally.
func TestDaemonStatusValidationRejectsInvalidCapabilitySyntax(t *testing.T) {
	status := testStatus()
	status.Capabilities = []rpc.Capability{CapabilityV1, "bad capability"}
	if err := status.Validate(); err == nil || !strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("invalid capability error = %v, want capability syntax failure", err)
	}
}

// TestContextualStatusValidationRejectsContradictions verifies status cannot disagree with server or handshake facts.
func TestContextualStatusValidationRejectsContradictions(t *testing.T) {
	status := testStatus()
	clientPeer := session.Peer{
		Role:         rpc.RoleCLI,
		BuildVersion: "client-build",
		Protocol:     protocolV1,
		Capabilities: daemonCapabilities(false, false, false, false, false),
	}
	if err := validateServingStatus(status, testBuild, clientPeer); err != nil {
		t.Fatalf("validate serving status: %v", err)
	}
	daemonPeer := session.Peer{
		Role:         rpc.RoleDaemon,
		BuildVersion: testBuild.Version,
		Protocol:     protocolV1,
		Capabilities: daemonCapabilities(false, false, false, false, false),
	}
	if err := validateReceivedStatus(status, daemonPeer); err != nil {
		t.Fatalf("validate received status: %v", err)
	}

	for _, test := range []struct {
		name  string
		build buildinfo.Info
	}{
		{name: "version", build: testBuildWithVersion("v9.0.0")},
		{name: "revision", build: buildinfo.Info{Version: testBuild.Version, Revision: "different", Modified: testBuild.Modified}},
		{name: "modified", build: buildinfo.Info{Version: testBuild.Version, Revision: testBuild.Revision, Modified: !testBuild.Modified}},
	} {
		t.Run("server "+test.name, func(t *testing.T) {
			if err := validateServingStatus(status, test.build, clientPeer); err == nil || !strings.Contains(err.Error(), "build") {
				t.Fatalf("serving status error = %v, want build contradiction", err)
			}
		})
	}

	wrongVersion := daemonPeer
	wrongVersion.BuildVersion = "v9.0.0"
	if err := validateReceivedStatus(status, wrongVersion); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("received version error = %v, want contradiction", err)
	}
	wrongProtocol := clientPeer
	wrongProtocol.Protocol.Minor++
	if err := validateServingStatus(status, testBuild, wrongProtocol); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("serving protocol error = %v, want contradiction", err)
	}
	wrongCapabilities := daemonPeer
	wrongCapabilities.Capabilities = []rpc.Capability{CapabilityV1, "events.v1"}
	if err := validateReceivedStatus(status, wrongCapabilities); err == nil || !strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("received capabilities error = %v, want contradiction", err)
	}
}
