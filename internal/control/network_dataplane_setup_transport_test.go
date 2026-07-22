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
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
)

// recordingDataPlaneAuthority makes the optional server surface observable without granting tests any production authority.
type recordingDataPlaneAuthority struct {
	mu      sync.Mutex
	setup   NetworkDataPlaneSetupOperation
	callers []Caller
}

// StartNetworkDataPlaneSetup records the authenticated caller so the connected transport test can prove identity propagation.
func (authority *recordingDataPlaneAuthority) StartNetworkDataPlaneSetup(_ context.Context, caller Caller, request StartNetworkDataPlaneSetupRequest) (NetworkDataPlaneSetupOperation, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.callers = append(authority.callers, caller)
	result := authority.setup
	result.Operation.IntentID = request.IntentID
	return result, nil
}

// ReadNetworkDataPlaneSetup rejects calls outside the connected start-method test contract.
func (authority *recordingDataPlaneAuthority) ReadNetworkDataPlaneSetup(context.Context, Caller, ReadNetworkDataPlaneSetupRequest) (NetworkDataPlaneSetupOperation, error) {
	return NetworkDataPlaneSetupOperation{}, errors.New("unexpected read")
}

// PrepareNetworkDataPlaneTrustApproval rejects calls outside the connected start-method test contract.
func (authority *recordingDataPlaneAuthority) PrepareNetworkDataPlaneTrustApproval(context.Context, Caller, PrepareNetworkDataPlaneTrustApprovalRequest) (NetworkDataPlaneTrustApprovalPreparation, error) {
	return NetworkDataPlaneTrustApprovalPreparation{}, errors.New("unexpected trust prepare")
}

// ConfirmNetworkDataPlaneTrustApproval rejects calls outside the connected start-method test contract.
func (authority *recordingDataPlaneAuthority) ConfirmNetworkDataPlaneTrustApproval(context.Context, Caller, ConfirmNetworkDataPlaneTrustApprovalRequest) (NetworkDataPlaneSetupOperation, error) {
	return NetworkDataPlaneSetupOperation{}, errors.New("unexpected trust confirm")
}

// PrepareNetworkDataPlaneLowPortApproval rejects calls outside the connected start-method test contract.
func (authority *recordingDataPlaneAuthority) PrepareNetworkDataPlaneLowPortApproval(context.Context, Caller, PrepareNetworkDataPlaneLowPortApprovalRequest) (NetworkDataPlaneLowPortApprovalPreparation, error) {
	return NetworkDataPlaneLowPortApprovalPreparation{}, errors.New("unexpected low-port prepare")
}

// ConfirmNetworkDataPlaneLowPortApproval rejects calls outside the connected start-method test contract.
func (authority *recordingDataPlaneAuthority) ConfirmNetworkDataPlaneLowPortApproval(context.Context, Caller, ConfirmNetworkDataPlaneLowPortApprovalRequest) (NetworkDataPlaneSetupConfirmation, error) {
	return NetworkDataPlaneSetupConfirmation{}, errors.New("unexpected low-port confirm")
}

// TestNetworkDataPlaneSetupNegotiatesOnlyWithLiveAuthority proves an authenticated local handshake exposes the optional method only when wired.
func TestNetworkDataPlaneSetupNegotiatesOnlyWithLiveAuthority(t *testing.T) {
	setup := validNetworkDataPlaneSetupOperation(t, domain.OperationRunning, networkDataPlaneSetupTrustApprovalPhase)
	authority := &recordingDataPlaneAuthority{setup: setup}
	running := newDataPlaneRunningClient(t, authority)
	if !containsCapability(running.client.peer.Session.Capabilities, CapabilityNetworkDataPlaneSetupV1) {
		t.Fatal("configured authority did not negotiate network data-plane setup")
	}
	got, err := running.client.StartNetworkDataPlaneSetup(t.Context(), StartNetworkDataPlaneSetupRequest{IntentID: "intent-data-plane"})
	if err != nil {
		t.Fatalf("StartNetworkDataPlaneSetup() error = %v", err)
	}
	if got.Operation.IntentID != "intent-data-plane" {
		t.Fatalf("start result = %#v", got)
	}
	authority.mu.Lock()
	callers := append([]Caller(nil), authority.callers...)
	authority.mu.Unlock()
	if len(callers) != 1 || callers[0].Transport.UserID != testClientPeer.UserID {
		t.Fatalf("handler callers = %#v", callers)
	}

	for _, candidate := range []NetworkDataPlaneSetupAuthority{nil, (*recordingDataPlaneAuthority)(nil)} {
		if !networkDataPlaneSetupAuthorityIsNil(candidate) {
			t.Fatalf("nil authority %T considered enabled", candidate)
		}
	}
}

// TestDecodeNetworkDataPlaneSetupRequestsRequiresExactObjects covers every authority-bearing envelope, including nested helper evidence.
func TestDecodeNetworkDataPlaneSetupRequestsRequiresExactObjects(t *testing.T) {
	trust := validNetworkDataPlaneTrustEvidence()
	lowPort := validNetworkDataPlaneLowPortEvidence()
	trustJSON, err := json.Marshal(trust)
	if err != nil {
		t.Fatal(err)
	}
	lowPortJSON, err := json.Marshal(lowPort)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		decode  func([]byte) error
		payload string
	}{
		{"start unknown", func(b []byte) error { _, e := decodeStartNetworkDataPlaneSetupRequest(b); return e }, `{"intent_id":"intent-data-plane","forged":true}`},
		{"start duplicate", func(b []byte) error { _, e := decodeStartNetworkDataPlaneSetupRequest(b); return e }, `{"intent_id":"intent-data-plane","intent_id":"other"}`},
		{"start missing", func(b []byte) error { _, e := decodeStartNetworkDataPlaneSetupRequest(b); return e }, `{}`},
		{"start trailing", func(b []byte) error { _, e := decodeStartNetworkDataPlaneSetupRequest(b); return e }, `{"intent_id":"intent-data-plane"} null`},
		{"trust unknown", func(b []byte) error { _, e := decodeConfirmNetworkDataPlaneTrustApprovalRequest(b); return e }, `{"operation_id":"operation-data-plane","expected_operation_revision":7,"trust_evidence":` + string(trustJSON) + `,"forged":true}`},
		{"trust duplicate", func(b []byte) error { _, e := decodeConfirmNetworkDataPlaneTrustApprovalRequest(b); return e }, `{"operation_id":"operation-data-plane","expected_operation_revision":7,"expected_operation_revision":8,"trust_evidence":` + string(trustJSON) + `}`},
		{"trust missing evidence", func(b []byte) error { _, e := decodeConfirmNetworkDataPlaneTrustApprovalRequest(b); return e }, `{"operation_id":"operation-data-plane","expected_operation_revision":7}`},
		{"trust trailing", func(b []byte) error { _, e := decodeConfirmNetworkDataPlaneTrustApprovalRequest(b); return e }, `{"operation_id":"operation-data-plane","expected_operation_revision":7,"trust_evidence":` + string(trustJSON) + `}{}`},
		{"trust evidence envelope", func(b []byte) error { _, e := decodeConfirmNetworkDataPlaneTrustApprovalRequest(b); return e }, `{"operation_id":"operation-data-plane","expected_operation_revision":7,"trust_evidence":{"version":2,"ok":true,"result":{}}}`},
		{"low-port duplicate evidence", func(b []byte) error { _, e := decodeConfirmNetworkDataPlaneLowPortApprovalRequest(b); return e }, `{"operation_id":"operation-data-plane","expected_operation_revision":7,"low_port_evidence":` + string(lowPortJSON) + `,"low_port_evidence":` + string(lowPortJSON) + `}`},
		{"low-port nested unknown", func(b []byte) error { _, e := decodeConfirmNetworkDataPlaneLowPortApprovalRequest(b); return e }, strings.Replace(`{"operation_id":"operation-data-plane","expected_operation_revision":7,"low_port_evidence":`+string(lowPortJSON)+`}`, `"postcondition":"exact"`, `"postcondition":"exact","forged":true`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode([]byte(test.payload)); err == nil {
				t.Fatal("decoder accepted an ambiguous authority request")
			}
		})
	}
}

// TestNetworkDataPlaneSetupCorrelationRejectsStaleAndMismatchedResponses keeps optimistic revisions and operation identity client-owned.
func TestNetworkDataPlaneSetupCorrelationRejectsStaleAndMismatchedResponses(t *testing.T) {
	setup := validNetworkDataPlaneSetupOperation(t, domain.OperationRequiresApproval, networkDataPlaneSetupLowPortApprovalPhase)
	trustRequest := ConfirmNetworkDataPlaneTrustApprovalRequest{OperationID: setup.Operation.ID, ExpectedOperationRevision: setup.Revision, TrustEvidence: validNetworkDataPlaneTrustEvidence()}
	if err := validateNetworkDataPlaneTrustConfirmationCorrelation(trustRequest, setup); err == nil {
		t.Fatal("trust confirmation accepted a stale response revision")
	}
	setup.Revision++
	setup.Operation.ID = "operation-other"
	if err := validateNetworkDataPlaneTrustConfirmationCorrelation(trustRequest, setup); err == nil {
		t.Fatal("trust confirmation accepted another operation")
	}
	completed := validNetworkDataPlaneSetupOperation(t, domain.OperationSucceeded, networkDataPlaneSetupCompletedPhase)
	confirmation := NetworkDataPlaneSetupConfirmation{Operation: completed.Operation, Revision: 8, NetworkRevision: 7}
	lowPortRequest := ConfirmNetworkDataPlaneLowPortApprovalRequest{OperationID: completed.Operation.ID, ExpectedOperationRevision: 8, LowPortEvidence: validNetworkDataPlaneLowPortEvidence()}
	if err := validateNetworkDataPlaneLowPortConfirmationCorrelation(lowPortRequest, confirmation); err == nil {
		t.Fatal("low-port confirmation accepted a stale response revision")
	}
	confirmation.Revision++
	confirmation.Operation.ID = "operation-other"
	if err := validateNetworkDataPlaneLowPortConfirmationCorrelation(lowPortRequest, confirmation); err == nil {
		t.Fatal("low-port confirmation accepted another operation")
	}
}

// validNetworkDataPlaneSetupOperation constructs one globally scoped operation fixture at the requested lifecycle edge.
func validNetworkDataPlaneSetupOperation(t *testing.T, state domain.OperationState, phase string) NetworkDataPlaneSetupOperation {
	t.Helper()
	at := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	operation, err := domain.NewOperation("operation-data-plane", "intent-data-plane", domain.OperationKindNetworkDataPlaneSetup, "", at)
	if err != nil {
		t.Fatal(err)
	}
	if state == domain.OperationRequiresApproval || state == domain.OperationSucceeded {
		operation, err = operation.Transition(domain.OperationRunning, "preparing", at, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if state != domain.OperationQueued && state != domain.OperationRunning {
		operation, err = operation.Transition(state, phase, at, nil)
		if err != nil {
			t.Fatal(err)
		}
	} else if state == domain.OperationRunning {
		operation, err = operation.Transition(state, phase, at, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	return NetworkDataPlaneSetupOperation{Operation: operation, Revision: 7}
}

// validNetworkDataPlaneTrustEvidence returns one canonical exact-trust confirmation fixture.
func validNetworkDataPlaneTrustEvidence() helper.TrustMutationEvidence {
	return helper.TrustMutationEvidence{AuthorityFingerprint: strings.Repeat("a", 64), ObservationFingerprint: strings.Repeat("b", 64), Mechanism: networkpolicy.DarwinCurrentUserTrust, Postcondition: helper.TrustPostconditionExact}
}

// validNetworkDataPlaneLowPortEvidence returns one canonical exact-low-port confirmation fixture.
func validNetworkDataPlaneLowPortEvidence() helper.LowPortMutationEvidence {
	return helper.LowPortMutationEvidence{PolicyFingerprint: strings.Repeat("a", 64), OwnershipFingerprint: strings.Repeat("b", 64), ObservationFingerprint: strings.Repeat("c", 64), Postcondition: helper.LowPortPostconditionExact}
}

// newDataPlaneRunningClient connects one real local client and server around the optional authority.
func newDataPlaneRunningClient(t *testing.T, dataPlane NetworkDataPlaneSetupAuthority) runningControlClient {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	server, err := newServer(ServerConfig{Authority: &recordingAuthority{}, NetworkDataPlaneSetupAuthority: dataPlane, RequestShutdown: func() {}}, testBuild)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, serverConnection) }()
	client, err := newClient(context.Background(), ClientConfig{Role: rpc.RoleCLI, Dial: func(context.Context) (local.Conn, error) { return clientConnection, nil }}, testBuild)
	if err != nil {
		cancel()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatal(err)
	}
	running := runningControlClient{client: client, cancel: cancel, serverDone: done}
	t.Cleanup(func() { running.close(t) })
	return running
}
