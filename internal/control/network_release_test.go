package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// recordingNetworkReleaseAuthority records calls to the optional release boundary.
type recordingNetworkReleaseAuthority struct {
	mu      sync.Mutex
	release NetworkReleaseOperation
	err     error
	callers []Caller
	starts  []StartNetworkReleaseRequest
	reads   []ReadNetworkReleaseRequest
}

// StartNetworkRelease records the authenticated caller and returns the configured release projection.
func (authority *recordingNetworkReleaseAuthority) StartNetworkRelease(_ context.Context, caller Caller, request StartNetworkReleaseRequest) (NetworkReleaseOperation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.starts = append(authority.starts, request)
	authority.release.Operation.IntentID = request.IntentID
	return authority.release, authority.err
}

// ReadNetworkRelease records the authenticated caller and returns the configured release projection.
func (authority *recordingNetworkReleaseAuthority) ReadNetworkRelease(_ context.Context, caller Caller, request ReadNetworkReleaseRequest) (NetworkReleaseOperation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.reads = append(authority.reads, request)
	return authority.release, authority.err
}

// TestNetworkReleaseNegotiationAndCalls proves normal clients request the optional capability and complete both methods.
func TestNetworkReleaseNegotiationAndCalls(t *testing.T) {
	authority := &recordingNetworkReleaseAuthority{release: validNetworkReleaseOperation(t)}
	running := newNetworkReleaseRunningClient(t, authority)
	if !containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseV1) {
		t.Fatal("configured release authority did not negotiate its capability")
	}
	started, err := running.client.StartNetworkRelease(t.Context(), StartNetworkReleaseRequest{
		IntentID: "intent-started",
	})
	if err != nil {
		t.Fatalf("StartNetworkRelease() error = %v", err)
	}
	read, err := running.client.ReadNetworkRelease(t.Context(), ReadNetworkReleaseRequest{
		OperationID: started.Operation.ID,
	})
	if err != nil {
		t.Fatalf("ReadNetworkRelease() error = %v", err)
	}
	if read.Operation.ID != started.Operation.ID || read.Operation.IntentID != started.Operation.IntentID ||
		read.Revision != started.Revision || read.Phase != started.Phase ||
		read.CheckpointRevision != started.CheckpointRevision || read.NetworkRevision != started.NetworkRevision {
		t.Fatalf("read = %#v, want projection %#v", read, started)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if len(authority.starts) != 1 || len(authority.reads) != 1 || len(authority.callers) != 2 {
		t.Fatalf("authority calls = starts %#v reads %#v callers %#v", authority.starts, authority.reads, authority.callers)
	}
	if authority.callers[0].Transport.UserID != testClientPeer.UserID {
		t.Fatalf("caller = %#v", authority.callers[0])
	}
}

// TestNetworkReleaseClientRejectsUnsupportedDaemon proves unsupported daemons receive no authority-bearing request.
func TestNetworkReleaseClientRejectsUnsupportedDaemon(t *testing.T) {
	running := newNetworkReleaseRunningClient(t, nil)
	if containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseV1) {
		t.Fatal("absent release authority negotiated its capability")
	}
	_, err := running.client.StartNetworkRelease(t.Context(), StartNetworkReleaseRequest{
		IntentID: "intent-release",
	})
	if err == nil || !strings.Contains(err.Error(), "does not support network release") {
		t.Fatalf("StartNetworkRelease() error = %v", err)
	}
}

// TestNetworkReleaseClientValidatesBeforeSending proves malformed selectors cannot reach a session or authority boundary.
func TestNetworkReleaseClientValidatesBeforeSending(t *testing.T) {
	client := &Client{}
	if _, err := client.StartNetworkRelease(t.Context(), StartNetworkReleaseRequest{}); err == nil {
		t.Fatal("StartNetworkRelease accepted an invalid request before session dispatch")
	}
	if _, err := client.ReadNetworkRelease(t.Context(), ReadNetworkReleaseRequest{}); err == nil {
		t.Fatal("ReadNetworkRelease accepted an invalid request before session dispatch")
	}
}

// TestNetworkReleaseCapabilityAdvertisementAndTypedNilHandling keeps optional authority negotiation explicit.
func TestNetworkReleaseCapabilityAdvertisementAndTypedNilHandling(t *testing.T) {
	if containsCapability(daemonCapabilities(false, false, false, false), CapabilityNetworkReleaseV1) {
		t.Fatal("disabled release capability was advertised")
	}
	if !containsCapability(daemonCapabilities(false, true, false, false), CapabilityNetworkReleaseV1) {
		t.Fatal("enabled release capability was not advertised")
	}
	for _, authority := range []NetworkReleaseAuthority{
		nil,
		(*recordingNetworkReleaseAuthority)(nil),
	} {
		if !networkReleaseAuthorityIsNil(authority) {
			t.Fatalf("nil authority %T was considered enabled", authority)
		}
	}
}

// TestNetworkReleaseOperationValidationRejectsInvalidRetainedPlanProjections covers every public lifecycle boundary.
func TestNetworkReleaseOperationValidationRejectsInvalidRetainedPlanProjections(t *testing.T) {
	valid := validNetworkReleaseOperation(t)
	for _, test := range []struct {
		name   string
		mutate func(*NetworkReleaseOperation)
	}{
		{
			name: "invalid operation",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Operation.ID = ""
			},
		},
		{
			name: "kind",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Operation.Kind = domain.OperationKindNetworkSetup
			},
		},
		{
			name: "project",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Operation.ProjectID = "project-alpha"
			},
		},
		{
			name: "state",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Operation.State = domain.OperationQueued
				operation.Operation.Phase = string(domain.OperationQueued)
				operation.Operation.StartedAt = nil
			},
		},
		{
			name: "operation phase",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Operation.Phase = "other"
			},
		},
		{
			name: "plan phase",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Phase = "foreign"
			},
		},
		{
			name: "operation revision",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Revision = 0
			},
		},
		{
			name: "operation revision overflow",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.Revision = domain.MaximumSequence + 1
			},
		},
		{
			name: "checkpoint revision",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.CheckpointRevision = 0
			},
		},
		{
			name: "checkpoint revision overflow",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.CheckpointRevision = domain.MaximumSequence + 1
			},
		},
		{
			name: "network revision",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.NetworkRevision = 0
			},
		},
		{
			name: "network revision overflow",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.NetworkRevision = domain.MaximumSequence + 1
			},
		},
		{
			name: "checkpoint order",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.CheckpointRevision = operation.Revision - 1
			},
		},
		{
			name: "network order",
			mutate: func(operation *NetworkReleaseOperation) {
				operation.NetworkRevision = operation.Revision
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation := valid
			test.mutate(&operation)
			if err := operation.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly accepted an invalid release projection")
			}
		})
	}
}

// TestNetworkReleaseOperationValidationAcceptsEveryRetainedPlanPhase keeps the wire enum aligned with future plan checkpoints.
func TestNetworkReleaseOperationValidationAcceptsEveryRetainedPlanPhase(t *testing.T) {
	for _, phase := range []NetworkReleasePhase{
		NetworkReleasePhaseRuntimeRelease,
		NetworkReleasePhaseLowPorts,
		NetworkReleasePhaseResolver,
		NetworkReleasePhaseTrust,
		NetworkReleasePhaseLoopbacks,
		NetworkReleasePhaseVerifyEffects,
		NetworkReleasePhaseOwnership,
		NetworkReleasePhaseProjection,
	} {
		t.Run(string(phase), func(t *testing.T) {
			operation := validNetworkReleaseOperation(t)
			operation.Phase = phase
			if err := operation.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

// TestNetworkReleaseCorrelationRequiresTheRequestedBoundary prevents responses from crossing caller-owned selectors.
func TestNetworkReleaseCorrelationRequiresTheRequestedBoundary(t *testing.T) {
	operation := validNetworkReleaseOperation(t)
	start := StartNetworkReleaseRequest{IntentID: operation.Operation.IntentID}
	read := ReadNetworkReleaseRequest{OperationID: operation.Operation.ID}
	if err := validateNetworkReleaseStartCorrelation(start, operation); err != nil {
		t.Fatalf("validateNetworkReleaseStartCorrelation() error = %v", err)
	}
	if err := validateNetworkReleaseReadCorrelation(read, operation); err != nil {
		t.Fatalf("validateNetworkReleaseReadCorrelation() error = %v", err)
	}
	operation.Operation.IntentID = "intent-other"
	if err := validateNetworkReleaseStartCorrelation(start, operation); err == nil {
		t.Fatal("start correlation accepted another intent")
	}
	operation = validNetworkReleaseOperation(t)
	operation.Operation.ID = "operation-other"
	if err := validateNetworkReleaseReadCorrelation(read, operation); err == nil {
		t.Fatal("read correlation accepted another operation")
	}
}

// TestDecodeNetworkReleaseRequestsRequiresExactObjects rejects malformed and surplus authority-bearing JSON.
func TestDecodeNetworkReleaseRequestsRequiresExactObjects(t *testing.T) {
	oversized := `{"intent_id":"` + strings.Repeat("a", maximumNetworkReleaseRequestBytes) + `"}`
	for _, test := range []struct {
		name    string
		payload string
		decode  func([]byte) error
	}{
		{
			name:    "valid start",
			payload: `{"intent_id":"intent-release"}`,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "valid read",
			payload: `{"operation_id":"operation-release"}`,
			decode:  decodeReadNetworkReleaseError,
		},
		{
			name:    "empty",
			payload: ``,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "non-object",
			payload: `[]`,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "oversized",
			payload: oversized,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "malformed",
			payload: `{"intent_id":`,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "trailing",
			payload: `{"intent_id":"intent-release"} null`,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "unknown",
			payload: `{"intent_id":"intent-release","forged":true}`,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "duplicate",
			payload: `{"intent_id":"intent-release","intent_id":"intent-other"}`,
			decode:  decodeStartNetworkReleaseError,
		},
		{
			name:    "missing",
			payload: `{}`,
			decode:  decodeStartNetworkReleaseError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.decode([]byte(test.payload))
			if strings.HasPrefix(test.name, "valid") {
				if err != nil {
					t.Fatalf("decoder error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("decoder accepted an invalid release request")
			}
		})
	}
}

// TestDecodeReadNetworkReleaseRequestRejectsInvalidSelectors verifies the read-only selector has the same strict envelope.
func TestDecodeReadNetworkReleaseRequestRejectsInvalidSelectors(t *testing.T) {
	for _, payload := range []string{
		``,
		`null`,
		`[]`,
		`{"operation_id":`,
		`{"operation_id":"operation-release"} null`,
		`{"operation_id":"operation-release","forged":true}`,
		`{"operation_id":"operation-release","operation_id":"operation-other"}`,
		`{}`,
		`{"operation_id":" bad "}`,
	} {
		if _, err := decodeReadNetworkReleaseRequest([]byte(payload)); err == nil {
			t.Fatalf("decodeReadNetworkReleaseRequest(%q) unexpectedly succeeded", payload)
		}
	}
}

// TestDecodeNetworkReleaseResponseRejectsAmbiguousAndInvalidResults keeps client results bounded before correlation.
func TestDecodeNetworkReleaseResponseRejectsAmbiguousAndInvalidResults(t *testing.T) {
	valid := validNetworkReleaseOperation(t)
	payload, err := json.Marshal(networkReleaseResponse{Release: valid})
	if err != nil {
		t.Fatal(err)
	}
	duplicateRevision := []byte(strings.Replace(
		string(payload),
		`"revision":5`,
		`"revision":5,"revision":5`,
		1,
	))
	duplicateOperationID := []byte(strings.Replace(
		string(payload),
		`"id":"operation-release"`,
		`"id":"operation-release","id":"operation-release"`,
		1,
	))
	if string(duplicateRevision) == string(payload) || string(duplicateOperationID) == string(payload) {
		t.Fatal("duplicate-field fixtures did not modify the canonical response")
	}
	for _, test := range []struct {
		name    string
		payload []byte
		valid   bool
	}{
		{
			name:    "valid",
			payload: payload,
			valid:   true,
		},
		{
			name:    "malformed",
			payload: []byte(`{"release":`),
		},
		{
			name:    "trailing",
			payload: append(payload, []byte(` null`)...),
		},
		{
			name:    "unknown",
			payload: []byte(`{"release":{},"forged":true}`),
		},
		{
			name:    "nested unknown",
			payload: []byte(`{"release":{"operation":{"forged":true}}}`),
		},
		{
			name:    "duplicate",
			payload: []byte(`{"release":{},"release":{}}`),
		},
		{
			name:    "duplicate release field",
			payload: duplicateRevision,
		},
		{
			name:    "duplicate operation field",
			payload: duplicateOperationID,
		},
		{
			name:    "invalid",
			payload: []byte(`{"release":{}}`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response networkReleaseResponse
			err := decodeNetworkReleaseResponse(test.payload, &response)
			if test.valid {
				if err != nil || response.Release.Validate() != nil {
					t.Fatalf("decodeNetworkReleaseResponse() = %#v, %v", response, err)
				}
				return
			}
			if err == nil && response.Release.Validate() == nil {
				t.Fatal("response decoder accepted an invalid result")
			}
		})
	}
}

// TestNetworkReleaseHandlerRejectsAuthorityErrorsAndUnnegotiatedCalls preserves reviewed wire classifications.
func TestNetworkReleaseHandlerRejectsAuthorityErrorsAndUnnegotiatedCalls(t *testing.T) {
	authority := &recordingNetworkReleaseAuthority{
		release: validNetworkReleaseOperation(t),
		err:     errors.New("authority failure"),
	}
	server := &Server{config: ServerConfig{NetworkReleaseAuthority: authority}}
	peer := session.Peer{
		Role:         rpc.RoleCLI,
		Protocol:     protocolV1,
		Capabilities: []rpc.Capability{CapabilityV1, CapabilityNetworkReleaseV1},
	}
	payload := []byte(`{"intent_id":"intent-release"}`)
	_, err := server.networkReleaseStartHandler(testClientPeer)(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	if err == nil {
		t.Fatal("handler accepted an authority failure")
	}
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodeInternal)
	peer.Capabilities = []rpc.Capability{CapabilityV1}
	_, err = server.networkReleaseStartHandler(testClientPeer)(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	if err == nil {
		t.Fatal("handler accepted an unnegotiated release call")
	}
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodePermissionDenied)
}

// assertNetworkReleaseHandlerCode verifies transport failures preserve the stable reviewed wire category.
func assertNetworkReleaseHandlerCode(t *testing.T, err error, want rpc.ErrorCode) {
	t.Helper()
	var handlerError *session.HandlerError
	if !errors.As(err, &handlerError) || handlerError.Code() != want {
		t.Fatalf("handler error = %#v, want code %q", err, want)
	}
}

// decodeStartNetworkReleaseError adapts the typed start decoder to the shared test table.
func decodeStartNetworkReleaseError(payload []byte) error {
	_, err := decodeStartNetworkReleaseRequest(payload)
	return err
}

// decodeReadNetworkReleaseError adapts the typed read decoder to the shared test table.
func decodeReadNetworkReleaseError(payload []byte) error {
	_, err := decodeReadNetworkReleaseRequest(payload)
	return err
}

// newNetworkReleaseRunningClient connects one real local client and server around the optional authority.
func newNetworkReleaseRunningClient(t *testing.T, release NetworkReleaseAuthority) runningControlClient {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{
		Conn: clientStream,
		peer: testDaemonPeer,
	}
	serverConnection := &testLocalConn{
		Conn: serverStream,
		peer: testClientPeer,
	}
	server, err := newServer(ServerConfig{
		Authority:               &recordingAuthority{},
		NetworkReleaseAuthority: release,
		RequestShutdown:         func() {},
	}, testBuild)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, serverConnection) }()
	client, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) {
			return clientConnection, nil
		},
	}, testBuild)
	if err != nil {
		cancel()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatal(err)
	}
	running := runningControlClient{
		client:     client,
		cancel:     cancel,
		serverDone: done,
	}
	t.Cleanup(func() { running.close(t) })
	return running
}

// validNetworkReleaseOperation creates one running release with a retained checkpoint following the full network revision.
func validNetworkReleaseOperation(t *testing.T) NetworkReleaseOperation {
	t.Helper()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	operation, err := domain.NewOperation("operation-release", "intent-release", domain.OperationKindNetworkRelease, "", now)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = operation.Transition(domain.OperationRunning, networkReleaseRuntimeOperationPhase, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NetworkReleaseOperation{
		Operation:          operation,
		Revision:           5,
		Phase:              NetworkReleasePhaseRuntimeRelease,
		CheckpointRevision: 6,
		NetworkRevision:    4,
	}
}
