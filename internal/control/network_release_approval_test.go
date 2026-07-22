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

	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// recordingNetworkReleaseApprovalAuthority records the narrow low-port approval calls made through a connected server.
type recordingNetworkReleaseApprovalAuthority struct {
	mu            sync.Mutex
	preparation   NetworkReleaseApprovalPreparation
	release       NetworkReleaseOperation
	err           error
	callers       []Caller
	prepares      []PrepareNetworkReleaseApprovalRequest
	confirmations []ConfirmNetworkReleaseApprovalRequest
}

// PrepareNetworkReleaseApproval records the authenticated caller and returns the configured preparation.
func (authority *recordingNetworkReleaseApprovalAuthority) PrepareNetworkReleaseApproval(
	_ context.Context,
	caller Caller,
	request PrepareNetworkReleaseApprovalRequest,
) (NetworkReleaseApprovalPreparation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.prepares = append(authority.prepares, request)
	return authority.preparation, authority.err
}

// ConfirmNetworkReleaseApproval records the authenticated caller and returns the configured release projection.
func (authority *recordingNetworkReleaseApprovalAuthority) ConfirmNetworkReleaseApproval(
	_ context.Context,
	caller Caller,
	request ConfirmNetworkReleaseApprovalRequest,
) (NetworkReleaseOperation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	authority.confirmations = append(authority.confirmations, request)
	return authority.release, authority.err
}

// TestNetworkReleaseApprovalStableProtocolNames fixes the reviewed capability and method identities.
func TestNetworkReleaseApprovalStableProtocolNames(t *testing.T) {
	if CapabilityNetworkReleaseApprovalV1 != "control.network-release-approval.v1" {
		t.Fatalf("capability = %q", CapabilityNetworkReleaseApprovalV1)
	}
	if methodNetworkReleaseLowPortPrepare != "control.v1.network.release.low-port.prepare" ||
		methodNetworkReleaseLowPortConfirm != "control.v1.network.release.low-port.confirm" {
		t.Fatalf("methods = %q, %q", methodNetworkReleaseLowPortPrepare, methodNetworkReleaseLowPortConfirm)
	}
}

// TestNetworkReleaseApprovalValidation confines every public value to release-low-ports and owned-absent evidence.
func TestNetworkReleaseApprovalValidation(t *testing.T) {
	preparation := validNetworkReleaseApprovalPreparation()
	confirmation := validNetworkReleaseApprovalConfirmation()
	for _, test := range []struct {
		name     string
		validate func() error
	}{
		{
			name: "prepare request",
			validate: func() error {
				return (PrepareNetworkReleaseApprovalRequest{
					OperationID:                preparation.OperationID,
					ExpectedCheckpointRevision: preparation.CheckpointRevision,
				}).Validate()
			},
		},
		{
			name:     "ticket",
			validate: preparation.Ticket.Validate,
		},
		{
			name:     "preparation",
			validate: preparation.Validate,
		},
		{
			name:     "confirmation",
			validate: confirmation.Validate,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
	for _, test := range []struct {
		name     string
		validate func() error
	}{
		{
			name: "empty operation",
			validate: func() error {
				return (PrepareNetworkReleaseApprovalRequest{
					ExpectedCheckpointRevision: 6,
				}).Validate()
			},
		},
		{
			name: "zero checkpoint",
			validate: func() error {
				return (PrepareNetworkReleaseApprovalRequest{
					OperationID: preparation.OperationID,
				}).Validate()
			},
		},
		{
			name: "wrong ticket operation",
			validate: func() error {
				value := preparation.Ticket
				value.Operation = helper.OperationEnsureLowPorts
				return value.Validate()
			},
		},
		{
			name: "ticket non UTC expiry",
			validate: func() error {
				value := preparation.Ticket
				value.ExpiresAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("local", 3600))
				return value.Validate()
			},
		},
		{
			name: "ticket operation mismatch",
			validate: func() error {
				value := preparation
				value.Ticket.OperationID = "other"
				return value.Validate()
			},
		},
		{
			name: "exact evidence",
			validate: func() error {
				value := confirmation
				value.LowPortEvidence.Postcondition = helper.LowPortPostconditionExact
				return value.Validate()
			},
		},
		{
			name: "bad evidence fingerprint",
			validate: func() error {
				value := confirmation
				value.LowPortEvidence.PolicyFingerprint = "bad"
				return value.Validate()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err == nil {
				t.Fatal("Validate() accepted invalid approval data")
			}
		})
	}
}

// TestNetworkReleaseApprovalCapabilityIsIndependent keeps approval negotiation independent from start/read and rejects typed nil authorities.
func TestNetworkReleaseApprovalCapabilityIsIndependent(t *testing.T) {
	if containsCapability(daemonCapabilities(false, true, false), CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("release start/read enabled approval")
	}
	if !containsCapability(daemonCapabilities(false, false, true), CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("approval was not independently enabled")
	}
	for _, authority := range []NetworkReleaseApprovalAuthority{
		nil,
		(*recordingNetworkReleaseApprovalAuthority)(nil),
	} {
		if !networkReleaseApprovalAuthorityIsNil(authority) {
			t.Fatalf("nil approval authority %T was enabled", authority)
		}
	}
}

// TestNetworkReleaseApprovalConnectedCalls proves exact caller propagation and selection correlation on both approval methods.
func TestNetworkReleaseApprovalConnectedCalls(t *testing.T) {
	preparation := validNetworkReleaseApprovalPreparation()
	release := validNetworkReleaseApprovalRelease(t)
	authority := &recordingNetworkReleaseApprovalAuthority{
		preparation: preparation,
		release:     release,
	}
	running := newNetworkReleaseApprovalRunningClient(t, authority)
	if !containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("approval capability was not negotiated")
	}
	prepared, err := running.client.PrepareNetworkReleaseApproval(
		t.Context(),
		PrepareNetworkReleaseApprovalRequest{
			OperationID:                preparation.OperationID,
			ExpectedCheckpointRevision: preparation.CheckpointRevision,
		},
	)
	if err != nil {
		t.Fatalf("PrepareNetworkReleaseApproval() error = %v", err)
	}
	confirmed, err := running.client.ConfirmNetworkReleaseApproval(
		t.Context(),
		validNetworkReleaseApprovalConfirmation(),
	)
	if err != nil {
		t.Fatalf("ConfirmNetworkReleaseApproval() error = %v", err)
	}
	if prepared != preparation ||
		confirmed.Operation.ID != release.Operation.ID ||
		confirmed.Phase != NetworkReleasePhaseResolver ||
		confirmed.CheckpointRevision != release.CheckpointRevision {
		t.Fatalf("results = %#v %#v", prepared, confirmed)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if len(authority.prepares) != 1 || len(authority.confirmations) != 1 || len(authority.callers) != 2 {
		t.Fatalf("calls = %#v %#v %#v", authority.prepares, authority.confirmations, authority.callers)
	}
	for _, caller := range authority.callers {
		if caller.Transport.UserID != testClientPeer.UserID {
			t.Fatalf("caller = %#v", caller)
		}
	}
}

// TestNetworkReleaseApprovalClientRejectsUnnegotiatedCapability proves no approval authority request is dispatched to old daemons.
func TestNetworkReleaseApprovalClientRejectsUnnegotiatedCapability(t *testing.T) {
	running := newNetworkReleaseApprovalRunningClient(t, nil)
	if containsCapability(running.client.Peer().Session.Capabilities, CapabilityNetworkReleaseApprovalV1) {
		t.Fatal("absent approval authority negotiated capability")
	}
	if _, err := running.client.PrepareNetworkReleaseApproval(
		t.Context(),
		PrepareNetworkReleaseApprovalRequest{
			OperationID:                "operation-release",
			ExpectedCheckpointRevision: 6,
		},
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("prepare error = %v", err)
	}
	if _, err := running.client.ConfirmNetworkReleaseApproval(
		t.Context(),
		validNetworkReleaseApprovalConfirmation(),
	); err == nil || !strings.Contains(err.Error(), "does not support network release approval") {
		t.Fatalf("confirm error = %v", err)
	}
}

// TestNetworkReleaseApprovalDecodersRejectAmbiguousJSON covers outer, nested evidence, response, and bounded request decoding.
func TestNetworkReleaseApprovalDecodersRejectAmbiguousJSON(t *testing.T) {
	evidence, err := json.Marshal(validNetworkReleaseApprovalEvidence())
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := json.Marshal(validNetworkReleaseApprovalPreparation())
	if err != nil {
		t.Fatal(err)
	}
	release, err := json.Marshal(networkReleaseResponse{
		Release: validNetworkReleaseOperation(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		decode  func([]byte) error
		payload string
	}{
		{
			name:    "prepare unknown",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"extra":true}`,
		},
		{
			name:    "prepare duplicate",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release","operation_id":"other","expected_checkpoint_revision":6}`,
		},
		{
			name:    "prepare missing",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release"}`,
		},
		{
			name:    "prepare trailing",
			decode:  decodePrepareApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6} null`,
		},
		{
			name:   "confirm unknown",
			decode: decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
				string(evidence) + `,"extra":true}`,
		},
		{
			name:   "confirm duplicate",
			decode: decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
				string(evidence) + `,"low_port_evidence":` + string(evidence) + `}`,
		},
		{
			name:    "confirm missing",
			decode:  decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6}`,
		},
		{
			name:   "confirm trailing",
			decode: decodeConfirmApprovalError,
			payload: `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
				string(evidence) + `} []`,
		},
		{
			name:   "nested evidence unknown",
			decode: decodeConfirmApprovalError,
			payload: strings.Replace(
				`{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":`+string(evidence)+`}`,
				`"postcondition":"owned_absent"`,
				`"postcondition":"owned_absent","extra":true`,
				1,
			),
		},
		{
			name:    "response unknown",
			decode:  decodeApprovalPreparationResponseError,
			payload: `{"extra":true}`,
		},
		{
			name:   "response duplicate",
			decode: decodeApprovalPreparationResponseError,
			payload: `{"preparation":` + string(preparation) + `,"preparation":` +
				string(preparation) + `}`,
		},
		{
			name:    "response missing",
			decode:  decodeApprovalPreparationResponseError,
			payload: `{}`,
		},
		{
			name:    "response trailing",
			decode:  decodeApprovalPreparationResponseError,
			payload: `{"preparation":` + string(preparation) + `} null`,
		},
		{
			name:   "response nested duplicate",
			decode: decodeApprovalPreparationResponseError,
			payload: strings.Replace(
				`{"preparation":`+string(preparation)+`}`,
				`"operation_id":"operation-release"`,
				`"operation_id":"operation-release","operation_id":"other"`,
				1,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode([]byte(test.payload)); err == nil {
				t.Fatal("decoder accepted ambiguous JSON")
			}
		})
	}
	oversized := `{"operation_id":"operation-release","expected_checkpoint_revision":6,"low_port_evidence":` +
		string(evidence) + strings.Repeat(" ", maximumNetworkReleaseApprovalConfirmationRequestBytes) + `}`
	if _, err := decodeConfirmNetworkReleaseApprovalRequest([]byte(oversized)); err == nil {
		t.Fatal("confirmation decoder accepted oversized request")
	}
	oversizedResponse := `{"preparation":` + string(preparation) + `}` +
		strings.Repeat(" ", helper.MaxResponseBytes)
	if err := decodeApprovalPreparationResponseError([]byte(oversizedResponse)); err == nil {
		t.Fatal("preparation response decoder accepted an oversized response")
	}
	oversizedReleaseResponse := string(release) + strings.Repeat(" ", maximumNetworkReleaseResponseBytes)
	if err := decodeNetworkReleaseResponse(
		[]byte(oversizedReleaseResponse),
		&networkReleaseResponse{},
	); err == nil {
		t.Fatal("release response decoder accepted an oversized response")
	}
}

// TestNetworkReleaseApprovalCorrelationsRejectWrongOperationCheckpointAndPhase preserves caller-owned optimistic boundaries.
func TestNetworkReleaseApprovalCorrelationsRejectWrongOperationCheckpointAndPhase(t *testing.T) {
	prepare := PrepareNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
	}
	if err := validateNetworkReleaseApprovalPreparationCorrelation(
		prepare,
		validNetworkReleaseApprovalPreparation(),
	); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*NetworkReleaseApprovalPreparation){
		func(value *NetworkReleaseApprovalPreparation) {
			value.OperationID = "other"
		},
		func(value *NetworkReleaseApprovalPreparation) {
			value.CheckpointRevision++
		},
	} {
		value := validNetworkReleaseApprovalPreparation()
		mutate(&value)
		if err := validateNetworkReleaseApprovalPreparationCorrelation(prepare, value); err == nil {
			t.Fatal("preparation correlation accepted wrong boundary")
		}
	}
	confirm := validNetworkReleaseApprovalConfirmation()
	for _, mutate := range []func(*NetworkReleaseOperation){
		func(value *NetworkReleaseOperation) {
			value.Operation.ID = "other"
		},
		func(value *NetworkReleaseOperation) {
			value.CheckpointRevision = confirm.ExpectedCheckpointRevision
		},
		func(value *NetworkReleaseOperation) {
			value.Phase = NetworkReleasePhaseTrust
		},
	} {
		value := validNetworkReleaseApprovalRelease(t)
		mutate(&value)
		if err := validateNetworkReleaseApprovalConfirmationCorrelation(confirm, value); err == nil {
			t.Fatal("confirmation correlation accepted wrong release boundary")
		}
	}
}

// TestNetworkReleaseApprovalHandlerRejectsUnnegotiatedAndAuthorityFailures preserves wire error categories.
func TestNetworkReleaseApprovalHandlerRejectsUnnegotiatedAndAuthorityFailures(t *testing.T) {
	server := &Server{
		config: ServerConfig{
			NetworkReleaseApprovalAuthority: &recordingNetworkReleaseApprovalAuthority{
				err: errors.New("authority failure"),
			},
		},
	}
	handler := server.networkReleaseLowPortPrepareHandler(testClientPeer)
	payload, err := json.Marshal(PrepareNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := session.Peer{
		Role:     rpc.RoleCLI,
		Protocol: protocolV1,
		Capabilities: []rpc.Capability{
			CapabilityV1,
		},
	}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodePermissionDenied)
	peer.Capabilities = []rpc.Capability{
		CapabilityV1,
		CapabilityNetworkReleaseApprovalV1,
	}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodeInternal)
	server.config.NetworkReleaseApprovalAuthority = &recordingNetworkReleaseApprovalAuthority{}
	_, err = handler(t.Context(), session.Request{
		Peer:    peer,
		Payload: payload,
	})
	assertNetworkReleaseHandlerCode(t, err, rpc.ErrorCodeInternal)
}

// validNetworkReleaseApprovalPreparation returns one canonical ticket preparation fixture.
func validNetworkReleaseApprovalPreparation() NetworkReleaseApprovalPreparation {
	return NetworkReleaseApprovalPreparation{
		OperationID:        "operation-release",
		CheckpointRevision: 6,
		Ticket: NetworkReleaseApprovalTicket{
			OperationID:                "operation-release",
			Reference:                  helper.TicketReference(strings.Repeat("a", 64)),
			Operation:                  helper.OperationReleaseLowPorts,
			PolicyFingerprint:          strings.Repeat("b", 64),
			TargetOwnershipFingerprint: strings.Repeat("c", 64),
			ObservationFingerprint:     strings.Repeat("d", 64),
			ExpiresAt:                  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
	}
}

// validNetworkReleaseApprovalEvidence returns canonical helper evidence for the release-only owned-absent postcondition.
func validNetworkReleaseApprovalEvidence() helper.LowPortMutationEvidence {
	return helper.LowPortMutationEvidence{
		PolicyFingerprint:      strings.Repeat("a", 64),
		OwnershipFingerprint:   strings.Repeat("b", 64),
		ObservationFingerprint: strings.Repeat("c", 64),
		Postcondition:          helper.LowPortPostconditionOwnedAbsent,
	}
}

// validNetworkReleaseApprovalConfirmation returns a canonical confirmation at the retained low-port checkpoint.
func validNetworkReleaseApprovalConfirmation() ConfirmNetworkReleaseApprovalRequest {
	return ConfirmNetworkReleaseApprovalRequest{
		OperationID:                "operation-release",
		ExpectedCheckpointRevision: 6,
		LowPortEvidence:            validNetworkReleaseApprovalEvidence(),
	}
}

// validNetworkReleaseApprovalRelease returns the resolver checkpoint that must follow a successful low-port confirmation.
func validNetworkReleaseApprovalRelease(t *testing.T) NetworkReleaseOperation {
	release := validNetworkReleaseOperation(t)
	release.Phase = NetworkReleasePhaseResolver
	release.CheckpointRevision = 7
	return release
}

// newNetworkReleaseApprovalRunningClient connects a real local client and server around the optional approval authority.
func newNetworkReleaseApprovalRunningClient(
	t *testing.T,
	authority NetworkReleaseApprovalAuthority,
) runningControlClient {
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
		Authority:                       &recordingAuthority{},
		NetworkReleaseApprovalAuthority: authority,
		RequestShutdown:                 func() {},
	}, testBuild)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, serverConnection)
	}()
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
	t.Cleanup(func() {
		running.close(t)
	})
	return running
}

// decodePrepareApprovalError adapts the typed decoder to the strict JSON test table.
func decodePrepareApprovalError(payload []byte) error {
	_, err := decodePrepareNetworkReleaseApprovalRequest(payload)
	return err
}

// decodeConfirmApprovalError adapts the typed decoder to the strict JSON test table.
func decodeConfirmApprovalError(payload []byte) error {
	_, err := decodeConfirmNetworkReleaseApprovalRequest(payload)
	return err
}

// decodeApprovalPreparationResponseError adapts the typed response decoder to the strict JSON test table.
func decodeApprovalPreparationResponseError(payload []byte) error {
	return decodeNetworkReleaseApprovalPreparationResponse(payload, &networkReleaseApprovalPreparationResponse{})
}
