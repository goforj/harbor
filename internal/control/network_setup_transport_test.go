package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestControlClientRoundTripsNetworkSetup verifies both human roles preserve caller identity, revision selection, and exact helper evidence.
func TestControlClientRoundTripsNetworkSetup(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			setup := NetworkSetupOperation{
				Operation: validNetworkSetupOperation(domain.OperationRequiresApproval),
				Revision:  3,
			}
			preparation := NetworkSetupApprovalPreparation{
				OperationID:       setup.Operation.ID,
				OperationRevision: setup.Revision,
				Ticket:            validNetworkSetupApprovalTicket(),
			}
			confirmation := validNetworkSetupApprovalConfirmation()
			confirmation.NetworkRevision = setup.Revision + 2
			confirmation.Revision = setup.Revision + 3
			authority := &recordingAuthority{
				networkSetup:             setup,
				networkSetupPreparation:  preparation,
				networkSetupConfirmation: confirmation,
			}
			running := newRunningControlClient(t, role, authority, nil)
			startRequest := StartNetworkSetupRequest{IntentID: setup.Operation.IntentID}
			prepareRequest := PrepareNetworkSetupApprovalRequest{
				OperationID:               setup.Operation.ID,
				ExpectedOperationRevision: setup.Revision,
			}
			confirmRequest := ConfirmNetworkSetupApprovalRequest{
				OperationID:               setup.Operation.ID,
				ExpectedOperationRevision: setup.Revision,
				PoolEvidence:              validNetworkSetupPoolEvidence(),
			}

			gotSetup, err := running.client.StartNetworkSetup(t.Context(), startRequest)
			if err != nil {
				t.Fatalf("StartNetworkSetup() error = %v", err)
			}
			gotPreparation, err := running.client.PrepareNetworkSetupApproval(t.Context(), prepareRequest)
			if err != nil {
				t.Fatalf("PrepareNetworkSetupApproval() error = %v", err)
			}
			gotConfirmation, err := running.client.ConfirmNetworkSetupApproval(t.Context(), confirmRequest)
			if err != nil {
				t.Fatalf("ConfirmNetworkSetupApproval() error = %v", err)
			}
			if !reflect.DeepEqual(gotSetup, setup) ||
				!reflect.DeepEqual(gotPreparation, preparation) ||
				!reflect.DeepEqual(gotConfirmation, confirmation) {
				t.Fatalf("network setup results = %#v / %#v / %#v", gotSetup, gotPreparation, gotConfirmation)
			}

			authority.mu.Lock()
			startRequests := append([]StartNetworkSetupRequest(nil), authority.networkSetupRequests...)
			prepareRequests := append([]PrepareNetworkSetupApprovalRequest(nil), authority.networkSetupPrepareRequests...)
			confirmRequests := append([]ConfirmNetworkSetupApprovalRequest(nil), authority.networkSetupConfirmRequests...)
			authority.mu.Unlock()
			if !reflect.DeepEqual(startRequests, []StartNetworkSetupRequest{startRequest}) ||
				!reflect.DeepEqual(prepareRequests, []PrepareNetworkSetupApprovalRequest{prepareRequest}) ||
				!reflect.DeepEqual(confirmRequests, []ConfirmNetworkSetupApprovalRequest{confirmRequest}) {
				t.Fatalf("network setup authority requests = %#v / %#v / %#v", startRequests, prepareRequests, confirmRequests)
			}
			callers := authority.recordedCallers()
			if len(callers) != 3 {
				t.Fatalf("network setup authority callers = %d, want 3", len(callers))
			}
			for _, caller := range callers {
				if caller.Transport != testClientPeer || caller.Session.Role != role ||
					!containsCapability(caller.Session.Capabilities, CapabilityNetworkSetupV1) {
					t.Fatalf("network setup caller = %#v, want authenticated %s caller", caller, role)
				}
			}
		})
	}
}

// TestDecodeNetworkSetupRequestsRequiresExactObjects proves every setup method rejects ambiguous or expanded authority.
func TestDecodeNetworkSetupRequestsRequiresExactObjects(t *testing.T) {
	startRequest := StartNetworkSetupRequest{IntentID: "intent-network-setup"}
	prepareRequest := PrepareNetworkSetupApprovalRequest{
		OperationID:               "operation-network-setup",
		ExpectedOperationRevision: 3,
	}
	confirmRequest := ConfirmNetworkSetupApprovalRequest{
		OperationID:               prepareRequest.OperationID,
		ExpectedOperationRevision: prepareRequest.ExpectedOperationRevision,
		PoolEvidence:              validNetworkSetupPoolEvidence(),
	}
	startJSON, err := json.Marshal(startRequest)
	if err != nil {
		t.Fatalf("marshal start request: %v", err)
	}
	prepareJSON, err := json.Marshal(prepareRequest)
	if err != nil {
		t.Fatalf("marshal preparation request: %v", err)
	}
	confirmJSON, err := json.Marshal(confirmRequest)
	if err != nil {
		t.Fatalf("marshal confirmation request: %v", err)
	}
	poolJSON, err := json.Marshal(confirmRequest.PoolEvidence)
	if err != nil {
		t.Fatalf("marshal pool evidence: %v", err)
	}
	decodedStart, err := decodeStartNetworkSetupRequest(startJSON)
	if err != nil || !reflect.DeepEqual(decodedStart, startRequest) {
		t.Fatalf("decodeStartNetworkSetupRequest() = %#v, %v", decodedStart, err)
	}
	decodedPrepare, err := decodePrepareNetworkSetupApprovalRequest(prepareJSON)
	if err != nil || !reflect.DeepEqual(decodedPrepare, prepareRequest) {
		t.Fatalf("decodePrepareNetworkSetupApprovalRequest() = %#v, %v", decodedPrepare, err)
	}
	decodedConfirm, err := decodeConfirmNetworkSetupApprovalRequest(confirmJSON)
	if err != nil || !reflect.DeepEqual(decodedConfirm, confirmRequest) {
		t.Fatalf("decodeConfirmNetworkSetupApprovalRequest() = %#v, %v", decodedConfirm, err)
	}

	confirmPrefix := `{"operation_id":"operation-network-setup","expected_operation_revision":3,"pool_evidence":`
	validConfirm := confirmPrefix + string(poolJSON) + `}`
	decodeStart := func(payload []byte) error {
		_, err := decodeStartNetworkSetupRequest(payload)
		return err
	}
	decodePrepare := func(payload []byte) error {
		_, err := decodePrepareNetworkSetupApprovalRequest(payload)
		return err
	}
	decodeConfirm := func(payload []byte) error {
		_, err := decodeConfirmNetworkSetupApprovalRequest(payload)
		return err
	}
	for _, test := range []struct {
		name    string
		decode  func([]byte) error
		payload string
	}{
		{name: "start empty", decode: decodeStart},
		{name: "start non-object", decode: decodeStart, payload: `[]`},
		{name: "start missing", decode: decodeStart, payload: `{}`},
		{name: "start duplicate", decode: decodeStart, payload: `{"intent_id":"intent-network-setup","intent_id":"intent-other"}`},
		{name: "start unknown", decode: decodeStart, payload: `{"intent_id":"intent-network-setup","force":true}`},
		{name: "start trailing", decode: decodeStart, payload: string(startJSON) + `{}`},
		{name: "start oversized", decode: decodeStart, payload: strings.Repeat(" ", maximumNetworkSetupSelectionRequestBytes+1)},
		{name: "prepare duplicate", decode: decodePrepare, payload: `{"operation_id":"operation-network-setup","operation_id":"operation-other","expected_operation_revision":3}`},
		{name: "prepare unknown", decode: decodePrepare, payload: `{"operation_id":"operation-network-setup","expected_operation_revision":3,"force":true}`},
		{name: "prepare missing", decode: decodePrepare, payload: `{"operation_id":"operation-network-setup"}`},
		{name: "prepare trailing", decode: decodePrepare, payload: string(prepareJSON) + ` x`},
		{name: "confirm duplicate", decode: decodeConfirm, payload: validConfirm[:len(validConfirm)-1] + `,"pool_evidence":` + string(poolJSON) + `}`},
		{name: "confirm unknown", decode: decodeConfirm, payload: validConfirm[:len(validConfirm)-1] + `,"force":true}`},
		{name: "confirm missing", decode: decodeConfirm, payload: `{"operation_id":"operation-network-setup","expected_operation_revision":3}`},
		{name: "confirm trailing", decode: decodeConfirm, payload: validConfirm + `{}`},
		{name: "confirm oversized", decode: decodeConfirm, payload: strings.Repeat(" ", maximumNetworkSetupConfirmationRequestBytes+1)},
		{name: "nested duplicate", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"state":"owned"`, `"state":"owned","state":"owned"`, 1)},
		{name: "nested unknown", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"pool":"127.42.0.0/29"`, `"pool":"127.42.0.0/29","result":{}`, 1)},
		{name: "helper envelope", decode: decodeConfirm, payload: confirmPrefix + `{"version":2,"ok":true,"result":{}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode([]byte(test.payload)); err == nil {
				t.Fatal("decoder accepted invalid network setup request")
			}
		})
	}
}

// TestNetworkSetupRequiresNegotiatedCapability proves both typed calls and server handlers fail before dispatch without setup negotiation.
func TestNetworkSetupRequiresNegotiatedCapability(t *testing.T) {
	startRequest := StartNetworkSetupRequest{IntentID: "intent-network-setup"}
	prepareRequest := PrepareNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 3}
	confirmRequest := ConfirmNetworkSetupApprovalRequest{
		OperationID:               prepareRequest.OperationID,
		ExpectedOperationRevision: prepareRequest.ExpectedOperationRevision,
		PoolEvidence:              validNetworkSetupPoolEvidence(),
	}
	client := &Client{peer: DaemonPeer{Session: session.Peer{Capabilities: []rpc.Capability{CapabilityV1}}}}
	for _, call := range []func() error{
		func() error { _, err := client.StartNetworkSetup(t.Context(), startRequest); return err },
		func() error { _, err := client.PrepareNetworkSetupApproval(t.Context(), prepareRequest); return err },
		func() error { _, err := client.ConfirmNetworkSetupApproval(t.Context(), confirmRequest); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "does not support network setup") {
			t.Fatalf("client capability error = %v", err)
		}
	}

	authority := &recordingAuthority{}
	server := &Server{config: ServerConfig{Authority: authority}}
	peer := session.Peer{
		Role:         rpc.RoleCLI,
		Protocol:     protocolV1,
		Capabilities: []rpc.Capability{CapabilityV1},
	}
	for _, call := range []struct {
		handler session.Handler
		request any
	}{
		{handler: server.networkSetupStartHandler(testClientPeer), request: startRequest},
		{handler: server.networkSetupApprovalPrepareHandler(testClientPeer), request: prepareRequest},
		{handler: server.networkSetupApprovalConfirmHandler(testClientPeer), request: confirmRequest},
	} {
		payload, err := json.Marshal(call.request)
		if err != nil {
			t.Fatalf("marshal capability request: %v", err)
		}
		_, err = call.handler(context.Background(), session.Request{Peer: peer, Payload: payload})
		var handlerError *session.HandlerError
		if !errors.As(err, &handlerError) || handlerError.Code() != rpc.ErrorCodePermissionDenied {
			t.Fatalf("handler capability error = %#v, want permission_denied", err)
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("unnegotiated network setup reached authority %d times", len(callers))
	}
}

// TestNetworkSetupAuthorityErrorsKeepSafeWireCodes verifies classified setup failures survive authority mapping for all methods.
func TestNetworkSetupAuthorityErrorsKeepSafeWireCodes(t *testing.T) {
	cause := errors.New("network setup state changed")
	authority := &recordingAuthority{
		networkSetupErr:        NewNetworkSetupConflictError(cause),
		networkSetupPrepareErr: NewNetworkSetupNotFoundError(cause),
		networkSetupConfirmErr: NewNetworkSetupConflictError(cause),
	}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	startRequest := StartNetworkSetupRequest{IntentID: "intent-network-setup"}
	prepareRequest := PrepareNetworkSetupApprovalRequest{OperationID: "operation-network-setup", ExpectedOperationRevision: 3}
	confirmRequest := ConfirmNetworkSetupApprovalRequest{
		OperationID:               prepareRequest.OperationID,
		ExpectedOperationRevision: prepareRequest.ExpectedOperationRevision,
		PoolEvidence:              validNetworkSetupPoolEvidence(),
	}
	for _, test := range []struct {
		call func() error
		want rpc.ErrorCode
	}{
		{call: func() error { _, err := running.client.StartNetworkSetup(t.Context(), startRequest); return err }, want: rpc.ErrorCodeConflict},
		{call: func() error {
			_, err := running.client.PrepareNetworkSetupApproval(t.Context(), prepareRequest)
			return err
		}, want: rpc.ErrorCodeNotFound},
		{call: func() error {
			_, err := running.client.ConfirmNetworkSetupApproval(t.Context(), confirmRequest)
			return err
		}, want: rpc.ErrorCodeConflict},
	} {
		var wireError rpc.WireError
		if err := test.call(); !errors.As(err, &wireError) || wireError.Code != test.want {
			t.Fatalf("network setup authority error = %#v, want %q", err, test.want)
		}
	}
}

// TestNetworkSetupConfirmationCorrelationRequiresLifecycleRevisions proves completion follows the selected approval revision exactly.
func TestNetworkSetupConfirmationCorrelationRequiresLifecycleRevisions(t *testing.T) {
	request := ConfirmNetworkSetupApprovalRequest{
		OperationID:               "operation-network-setup",
		ExpectedOperationRevision: 3,
		PoolEvidence:              validNetworkSetupPoolEvidence(),
	}
	confirmation := validNetworkSetupApprovalConfirmation()
	confirmation.NetworkRevision = 5
	confirmation.Revision = 6
	if err := validateNetworkSetupApprovalConfirmationCorrelation(request, confirmation); err != nil {
		t.Fatalf("validateNetworkSetupApprovalConfirmationCorrelation() error = %v", err)
	}
	for _, mutate := range []func(*NetworkSetupApprovalConfirmation){
		func(value *NetworkSetupApprovalConfirmation) { value.Operation.ID = "operation-other" },
		func(value *NetworkSetupApprovalConfirmation) { value.NetworkRevision-- },
		func(value *NetworkSetupApprovalConfirmation) { value.Revision++ },
		func(value *NetworkSetupApprovalConfirmation) { value.Pool = "127.43.0.0/29" },
	} {
		candidate := confirmation
		mutate(&candidate)
		if err := validateNetworkSetupApprovalConfirmationCorrelation(request, candidate); err == nil {
			t.Fatalf("correlation accepted %#v", candidate)
		}
	}
}
