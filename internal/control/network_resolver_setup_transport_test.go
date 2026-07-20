package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestControlClientRoundTripsNetworkResolverSetup verifies both human roles preserve caller identity, revision selection, and exact helper evidence.
func TestControlClientRoundTripsNetworkResolverSetup(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			setup := NetworkResolverSetupOperation{
				Operation: validNetworkResolverSetupOperation(domain.OperationRequiresApproval),
				Revision:  7,
			}
			preparation := NetworkResolverSetupApprovalPreparation{
				OperationID:       setup.Operation.ID,
				OperationRevision: setup.Revision,
				Ticket:            validNetworkResolverSetupApprovalTicket(),
			}
			confirmation := validNetworkResolverSetupApprovalConfirmation()
			confirmation.NetworkRevision = setup.Revision + 7
			confirmation.Revision = setup.Revision + 8
			authority := &recordingAuthority{
				networkResolverSetup:             setup,
				networkResolverSetupPreparation:  preparation,
				networkResolverSetupConfirmation: confirmation,
			}
			running := newRunningControlClient(t, role, authority, nil)
			startRequest := StartNetworkResolverSetupRequest{IntentID: setup.Operation.IntentID}
			prepareRequest := PrepareNetworkResolverSetupApprovalRequest{
				OperationID:               setup.Operation.ID,
				ExpectedOperationRevision: setup.Revision,
			}
			confirmRequest := ConfirmNetworkResolverSetupApprovalRequest{
				OperationID:               setup.Operation.ID,
				ExpectedOperationRevision: setup.Revision,
				ResolverEvidence:          validNetworkResolverSetupEvidence(),
			}

			gotSetup, err := running.client.StartNetworkResolverSetup(t.Context(), startRequest)
			if err != nil {
				t.Fatalf("StartNetworkResolverSetup() error = %v", err)
			}
			gotPreparation, err := running.client.PrepareNetworkResolverSetupApproval(t.Context(), prepareRequest)
			if err != nil {
				t.Fatalf("PrepareNetworkResolverSetupApproval() error = %v", err)
			}
			gotConfirmation, err := running.client.ConfirmNetworkResolverSetupApproval(t.Context(), confirmRequest)
			if err != nil {
				t.Fatalf("ConfirmNetworkResolverSetupApproval() error = %v", err)
			}
			if !reflect.DeepEqual(gotSetup, setup) ||
				!reflect.DeepEqual(gotPreparation, preparation) ||
				!reflect.DeepEqual(gotConfirmation, confirmation) {
				t.Fatalf("network resolver setup results = %#v / %#v / %#v", gotSetup, gotPreparation, gotConfirmation)
			}

			authority.mu.Lock()
			startRequests := append([]StartNetworkResolverSetupRequest(nil), authority.networkResolverSetupRequests...)
			prepareRequests := append([]PrepareNetworkResolverSetupApprovalRequest(nil), authority.networkResolverSetupPrepareRequests...)
			confirmRequests := append([]ConfirmNetworkResolverSetupApprovalRequest(nil), authority.networkResolverSetupConfirmRequests...)
			authority.mu.Unlock()
			if !reflect.DeepEqual(startRequests, []StartNetworkResolverSetupRequest{startRequest}) ||
				!reflect.DeepEqual(prepareRequests, []PrepareNetworkResolverSetupApprovalRequest{prepareRequest}) ||
				!reflect.DeepEqual(confirmRequests, []ConfirmNetworkResolverSetupApprovalRequest{confirmRequest}) {
				t.Fatalf("network resolver setup authority requests = %#v / %#v / %#v", startRequests, prepareRequests, confirmRequests)
			}
			callers := authority.recordedCallers()
			if len(callers) != 3 {
				t.Fatalf("network resolver setup authority callers = %d, want 3", len(callers))
			}
			for _, caller := range callers {
				if caller.Transport != testClientPeer || caller.Session.Role != role ||
					!containsCapability(caller.Session.Capabilities, CapabilityNetworkResolverSetupV1) {
					t.Fatalf("network resolver setup caller = %#v, want authenticated %s caller", caller, role)
				}
			}
		})
	}
}

// TestDecodeNetworkResolverSetupRequestsRequiresExactObjects proves every resolver setup method rejects ambiguous or expanded authority.
func TestDecodeNetworkResolverSetupRequestsRequiresExactObjects(t *testing.T) {
	startRequest := StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"}
	prepareRequest := PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               "operation-network-resolver-setup",
		ExpectedOperationRevision: 7,
	}
	confirmRequest := ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               prepareRequest.OperationID,
		ExpectedOperationRevision: prepareRequest.ExpectedOperationRevision,
		ResolverEvidence:          validNetworkResolverSetupEvidence(),
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
	evidenceJSON, err := json.Marshal(confirmRequest.ResolverEvidence)
	if err != nil {
		t.Fatalf("marshal resolver evidence: %v", err)
	}
	decodedStart, err := decodeStartNetworkResolverSetupRequest(startJSON)
	if err != nil || !reflect.DeepEqual(decodedStart, startRequest) {
		t.Fatalf("decodeStartNetworkResolverSetupRequest() = %#v, %v", decodedStart, err)
	}
	decodedPrepare, err := decodePrepareNetworkResolverSetupApprovalRequest(prepareJSON)
	if err != nil || !reflect.DeepEqual(decodedPrepare, prepareRequest) {
		t.Fatalf("decodePrepareNetworkResolverSetupApprovalRequest() = %#v, %v", decodedPrepare, err)
	}
	decodedConfirm, err := decodeConfirmNetworkResolverSetupApprovalRequest(confirmJSON)
	if err != nil || !reflect.DeepEqual(decodedConfirm, confirmRequest) {
		t.Fatalf("decodeConfirmNetworkResolverSetupApprovalRequest() = %#v, %v", decodedConfirm, err)
	}

	confirmPrefix := `{"operation_id":"operation-network-resolver-setup","expected_operation_revision":7,"resolver_evidence":`
	validConfirm := confirmPrefix + string(evidenceJSON) + `}`
	decodeStart := func(payload []byte) error {
		_, err := decodeStartNetworkResolverSetupRequest(payload)
		return err
	}
	decodePrepare := func(payload []byte) error {
		_, err := decodePrepareNetworkResolverSetupApprovalRequest(payload)
		return err
	}
	decodeConfirm := func(payload []byte) error {
		_, err := decodeConfirmNetworkResolverSetupApprovalRequest(payload)
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
		{name: "start duplicate", decode: decodeStart, payload: `{"intent_id":"intent-network-resolver-setup","intent_id":"intent-other"}`},
		{name: "start unknown", decode: decodeStart, payload: `{"intent_id":"intent-network-resolver-setup","force":true}`},
		{name: "start malformed field", decode: decodeStart, payload: `{"intent_id":"intent-network-resolver-setup",`},
		{name: "start wrong type", decode: decodeStart, payload: `{"intent_id":7}`},
		{name: "start invalid value", decode: decodeStart, payload: `{"intent_id":""}`},
		{name: "start trailing", decode: decodeStart, payload: string(startJSON) + `{}`},
		{name: "start oversized", decode: decodeStart, payload: strings.Repeat(" ", maximumNetworkResolverSetupSelectionRequestBytes+1)},
		{name: "prepare empty", decode: decodePrepare},
		{name: "prepare non-object", decode: decodePrepare, payload: `[]`},
		{name: "prepare duplicate", decode: decodePrepare, payload: `{"operation_id":"operation-network-resolver-setup","operation_id":"operation-other","expected_operation_revision":7}`},
		{name: "prepare duplicate revision", decode: decodePrepare, payload: `{"operation_id":"operation-network-resolver-setup","expected_operation_revision":7,"expected_operation_revision":8}`},
		{name: "prepare unknown", decode: decodePrepare, payload: `{"operation_id":"operation-network-resolver-setup","expected_operation_revision":7,"force":true}`},
		{name: "prepare missing", decode: decodePrepare, payload: `{"operation_id":"operation-network-resolver-setup"}`},
		{name: "prepare malformed field", decode: decodePrepare, payload: `{"operation_id":"operation-network-resolver-setup",`},
		{name: "prepare operation wrong type", decode: decodePrepare, payload: `{"operation_id":7,"expected_operation_revision":7}`},
		{name: "prepare revision wrong type", decode: decodePrepare, payload: `{"operation_id":"operation-network-resolver-setup","expected_operation_revision":"7"}`},
		{name: "prepare invalid operation", decode: decodePrepare, payload: `{"operation_id":"","expected_operation_revision":7}`},
		{name: "prepare invalid revision", decode: decodePrepare, payload: `{"operation_id":"operation-network-resolver-setup","expected_operation_revision":0}`},
		{name: "prepare trailing", decode: decodePrepare, payload: string(prepareJSON) + ` x`},
		{name: "prepare oversized", decode: decodePrepare, payload: strings.Repeat(" ", maximumNetworkResolverSetupSelectionRequestBytes+1)},
		{name: "confirm duplicate", decode: decodeConfirm, payload: validConfirm[:len(validConfirm)-1] + `,"resolver_evidence":` + string(evidenceJSON) + `}`},
		{name: "confirm duplicate operation", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"operation_id":"operation-network-resolver-setup"`, `"operation_id":"operation-network-resolver-setup","operation_id":"operation-other"`, 1)},
		{name: "confirm duplicate revision", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"expected_operation_revision":7`, `"expected_operation_revision":7,"expected_operation_revision":8`, 1)},
		{name: "confirm unknown", decode: decodeConfirm, payload: validConfirm[:len(validConfirm)-1] + `,"force":true}`},
		{name: "confirm missing", decode: decodeConfirm, payload: `{"operation_id":"operation-network-resolver-setup","expected_operation_revision":7}`},
		{name: "confirm malformed field", decode: decodeConfirm, payload: `{"operation_id":"operation-network-resolver-setup",`},
		{name: "confirm operation wrong type", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"operation_id":"operation-network-resolver-setup"`, `"operation_id":7`, 1)},
		{name: "confirm revision wrong type", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"expected_operation_revision":7`, `"expected_operation_revision":"7"`, 1)},
		{name: "confirm evidence truncated", decode: decodeConfirm, payload: confirmPrefix},
		{name: "confirm invalid operation", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"operation_id":"operation-network-resolver-setup"`, `"operation_id":""`, 1)},
		{name: "confirm invalid revision", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"expected_operation_revision":7`, `"expected_operation_revision":0`, 1)},
		{name: "confirm trailing", decode: decodeConfirm, payload: validConfirm + `{}`},
		{name: "confirm oversized", decode: decodeConfirm, payload: strings.Repeat(" ", maximumNetworkResolverSetupConfirmationRequestBytes+1)},
		{name: "nested duplicate", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"postcondition":"exact"`, `"postcondition":"exact","postcondition":"exact"`, 1)},
		{name: "nested unknown", decode: decodeConfirm, payload: strings.Replace(validConfirm, `"postcondition":"exact"`, `"postcondition":"exact","result":{}`, 1)},
		{name: "helper envelope", decode: decodeConfirm, payload: confirmPrefix + `{"version":2,"ok":true,"result":{}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode([]byte(test.payload)); err == nil {
				t.Fatal("decoder accepted invalid network resolver setup request")
			}
		})
	}
}

// TestNetworkResolverSetupRequestBoundsAreExact proves each hard limit accepts its final byte and rejects one more.
func TestNetworkResolverSetupRequestBoundsAreExact(t *testing.T) {
	startJSON, err := json.Marshal(StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"})
	if err != nil {
		t.Fatalf("marshal start request: %v", err)
	}
	confirmJSON, err := json.Marshal(ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               "operation-network-resolver-setup",
		ExpectedOperationRevision: 7,
		ResolverEvidence:          validNetworkResolverSetupEvidence(),
	})
	if err != nil {
		t.Fatalf("marshal confirmation request: %v", err)
	}

	boundedStart := append(
		append([]byte(nil), startJSON...),
		[]byte(strings.Repeat(" ", maximumNetworkResolverSetupSelectionRequestBytes-len(startJSON)))...,
	)
	if _, err := decodeStartNetworkResolverSetupRequest(boundedStart); err != nil {
		t.Fatalf("decode start at request bound: %v", err)
	}
	if _, err := decodeStartNetworkResolverSetupRequest(append(boundedStart, ' ')); err == nil {
		t.Fatal("start decoder accepted one byte beyond its request bound")
	}

	boundedConfirmation := append(
		append([]byte(nil), confirmJSON...),
		[]byte(strings.Repeat(" ", maximumNetworkResolverSetupConfirmationRequestBytes-len(confirmJSON)))...,
	)
	if _, err := decodeConfirmNetworkResolverSetupApprovalRequest(boundedConfirmation); err != nil {
		t.Fatalf("decode confirmation at request bound: %v", err)
	}
	if _, err := decodeConfirmNetworkResolverSetupApprovalRequest(append(boundedConfirmation, ' ')); err == nil {
		t.Fatal("confirmation decoder accepted one byte beyond its request bound")
	}
}

// TestNetworkResolverSetupRequiresNegotiatedCapability proves typed calls and handlers fail before dispatch without resolver negotiation.
func TestNetworkResolverSetupRequiresNegotiatedCapability(t *testing.T) {
	startRequest := StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"}
	prepareRequest := PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               "operation-network-resolver-setup",
		ExpectedOperationRevision: 7,
	}
	confirmRequest := ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               prepareRequest.OperationID,
		ExpectedOperationRevision: prepareRequest.ExpectedOperationRevision,
		ResolverEvidence:          validNetworkResolverSetupEvidence(),
	}
	client := &Client{peer: DaemonPeer{Session: session.Peer{Capabilities: []rpc.Capability{CapabilityV1}}}}
	for _, call := range []func() error{
		func() error { _, err := client.StartNetworkResolverSetup(t.Context(), startRequest); return err },
		func() error {
			_, err := client.PrepareNetworkResolverSetupApproval(t.Context(), prepareRequest)
			return err
		},
		func() error {
			_, err := client.ConfirmNetworkResolverSetupApproval(t.Context(), confirmRequest)
			return err
		},
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "does not support network resolver setup") {
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
		{handler: server.networkResolverSetupStartHandler(testClientPeer), request: startRequest},
		{handler: server.networkResolverSetupApprovalPrepareHandler(testClientPeer), request: prepareRequest},
		{handler: server.networkResolverSetupApprovalConfirmHandler(testClientPeer), request: confirmRequest},
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
		t.Fatalf("unnegotiated network resolver setup reached authority %d times", len(callers))
	}
}

// TestNetworkResolverSetupClientsRejectInvalidInputsBeforeDispatch verifies local validation precedes every session call.
func TestNetworkResolverSetupClientsRejectInvalidInputsBeforeDispatch(t *testing.T) {
	client := &Client{peer: DaemonPeer{Session: session.Peer{Capabilities: []rpc.Capability{CapabilityNetworkResolverSetupV1}}}}
	for _, call := range []func() error{
		func() error {
			_, err := client.StartNetworkResolverSetup(t.Context(), StartNetworkResolverSetupRequest{})
			return err
		},
		func() error {
			_, err := client.PrepareNetworkResolverSetupApproval(t.Context(), PrepareNetworkResolverSetupApprovalRequest{})
			return err
		},
		func() error {
			_, err := client.ConfirmNetworkResolverSetupApproval(t.Context(), ConfirmNetworkResolverSetupApprovalRequest{})
			return err
		},
	} {
		if err := call(); err == nil {
			t.Fatal("client accepted an invalid resolver setup request")
		}
	}
}

// TestNetworkResolverSetupClientsRejectUntrustedResponses proves decoding, contract validation, and request correlation remain client-owned.
func TestNetworkResolverSetupClientsRejectUntrustedResponses(t *testing.T) {
	validSetup := NetworkResolverSetupOperation{
		Operation: validNetworkResolverSetupOperation(domain.OperationRequiresApproval),
		Revision:  7,
	}
	validPreparation := NetworkResolverSetupApprovalPreparation{
		OperationID:       validSetup.Operation.ID,
		OperationRevision: validSetup.Revision,
		Ticket:            validNetworkResolverSetupApprovalTicket(),
	}
	validConfirmation := validNetworkResolverSetupApprovalConfirmation()
	validConfirmation.NetworkRevision = validSetup.Revision + 2
	validConfirmation.Revision = validSetup.Revision + 3
	startRequest := StartNetworkResolverSetupRequest{IntentID: validSetup.Operation.IntentID}
	prepareRequest := PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               validSetup.Operation.ID,
		ExpectedOperationRevision: validSetup.Revision,
	}
	confirmRequest := ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               validSetup.Operation.ID,
		ExpectedOperationRevision: validSetup.Revision,
		ResolverEvidence:          validNetworkResolverSetupEvidence(),
	}

	invalidSetup := validSetup
	invalidSetup.Revision = 0
	mismatchedSetup := validSetup
	mismatchedSetup.Operation.IntentID = "intent-other"
	invalidPreparation := validPreparation
	invalidPreparation.OperationRevision = 0
	mismatchedPreparation := validPreparation
	mismatchedPreparation.OperationID = "operation-other"
	mismatchedPreparation.Ticket.OperationID = "operation-other"
	invalidConfirmation := validConfirmation
	invalidConfirmation.Operation.State = domain.OperationFailed
	mismatchedConfirmation := validConfirmation
	mismatchedConfirmation.Operation.ID = "operation-other"

	for _, test := range []struct {
		name     string
		method   string
		response any
		call     func(*Client) error
		message  string
	}{
		{
			name:     "start decode",
			method:   methodNetworkResolverSetupStart,
			response: map[string]any{"setup": "invalid"},
			call: func(client *Client) error {
				_, err := client.StartNetworkResolverSetup(t.Context(), startRequest)
				return err
			},
			message: "decode network resolver setup response",
		},
		{
			name:     "start validation",
			method:   methodNetworkResolverSetupStart,
			response: networkResolverSetupResponse{Setup: invalidSetup},
			call: func(client *Client) error {
				_, err := client.StartNetworkResolverSetup(t.Context(), startRequest)
				return err
			},
			message: "validate network resolver setup response",
		},
		{
			name:     "start correlation",
			method:   methodNetworkResolverSetupStart,
			response: networkResolverSetupResponse{Setup: mismatchedSetup},
			call: func(client *Client) error {
				_, err := client.StartNetworkResolverSetup(t.Context(), startRequest)
				return err
			},
			message: "does not match the requested intent",
		},
		{
			name:     "prepare decode",
			method:   methodNetworkResolverSetupApprovalPrepare,
			response: map[string]any{"preparation": "invalid"},
			call: func(client *Client) error {
				_, err := client.PrepareNetworkResolverSetupApproval(t.Context(), prepareRequest)
				return err
			},
			message: "decode network resolver setup approval preparation response",
		},
		{
			name:     "prepare validation",
			method:   methodNetworkResolverSetupApprovalPrepare,
			response: networkResolverSetupApprovalPreparationResponse{Preparation: invalidPreparation},
			call: func(client *Client) error {
				_, err := client.PrepareNetworkResolverSetupApproval(t.Context(), prepareRequest)
				return err
			},
			message: "validate network resolver setup approval preparation response",
		},
		{
			name:     "prepare correlation",
			method:   methodNetworkResolverSetupApprovalPrepare,
			response: networkResolverSetupApprovalPreparationResponse{Preparation: mismatchedPreparation},
			call: func(client *Client) error {
				_, err := client.PrepareNetworkResolverSetupApproval(t.Context(), prepareRequest)
				return err
			},
			message: "does not match the requested operation revision",
		},
		{
			name:     "confirm decode",
			method:   methodNetworkResolverSetupApprovalConfirm,
			response: map[string]any{"confirmation": "invalid"},
			call: func(client *Client) error {
				_, err := client.ConfirmNetworkResolverSetupApproval(t.Context(), confirmRequest)
				return err
			},
			message: "decode network resolver setup approval confirmation response",
		},
		{
			name:     "confirm validation",
			method:   methodNetworkResolverSetupApprovalConfirm,
			response: networkResolverSetupApprovalConfirmationResponse{Confirmation: invalidConfirmation},
			call: func(client *Client) error {
				_, err := client.ConfirmNetworkResolverSetupApproval(t.Context(), confirmRequest)
				return err
			},
			message: "validate network resolver setup approval confirmation response",
		},
		{
			name:     "confirm correlation",
			method:   methodNetworkResolverSetupApprovalConfirm,
			response: networkResolverSetupApprovalConfirmationResponse{Confirmation: mismatchedConfirmation},
			call: func(client *Client) error {
				_, err := client.ConfirmNetworkResolverSetupApproval(t.Context(), confirmRequest)
				return err
			},
			message: "does not match the requested operation revision",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			running := newConfiguredNetworkResolverSetupClient(t, test.method, test.response)
			if err := test.call(running.client); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("resolver setup client error = %v, want %q", err, test.message)
			}
		})
	}
}

// TestNetworkResolverSetupHandlersRejectInvalidRequestsAndAuthorityResults proves daemon dispatch fails closed at every method boundary.
func TestNetworkResolverSetupHandlersRejectInvalidRequestsAndAuthorityResults(t *testing.T) {
	peer := session.Peer{
		Role:         rpc.RoleCLI,
		BuildVersion: testBuild.Version,
		Protocol:     protocolV1,
		Capabilities: capabilities(),
	}
	startRequest := StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"}
	prepareRequest := PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               "operation-network-resolver-setup",
		ExpectedOperationRevision: 7,
	}
	confirmRequest := ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               prepareRequest.OperationID,
		ExpectedOperationRevision: prepareRequest.ExpectedOperationRevision,
		ResolverEvidence:          validNetworkResolverSetupEvidence(),
	}
	startPayload, err := json.Marshal(startRequest)
	if err != nil {
		t.Fatalf("marshal start request: %v", err)
	}
	preparePayload, err := json.Marshal(prepareRequest)
	if err != nil {
		t.Fatalf("marshal prepare request: %v", err)
	}
	confirmPayload, err := json.Marshal(confirmRequest)
	if err != nil {
		t.Fatalf("marshal confirm request: %v", err)
	}

	validSetup := NetworkResolverSetupOperation{
		Operation: validNetworkResolverSetupOperation(domain.OperationRequiresApproval),
		Revision:  7,
	}
	mismatchedSetup := validSetup
	mismatchedSetup.Operation.IntentID = "intent-other"
	validPreparation := NetworkResolverSetupApprovalPreparation{
		OperationID:       prepareRequest.OperationID,
		OperationRevision: prepareRequest.ExpectedOperationRevision,
		Ticket:            validNetworkResolverSetupApprovalTicket(),
	}
	mismatchedPreparation := validPreparation
	mismatchedPreparation.OperationID = "operation-other"
	mismatchedPreparation.Ticket.OperationID = "operation-other"
	validConfirmation := validNetworkResolverSetupApprovalConfirmation()
	validConfirmation.NetworkRevision = confirmRequest.ExpectedOperationRevision + 2
	validConfirmation.Revision = confirmRequest.ExpectedOperationRevision + 3
	mismatchedConfirmation := validConfirmation
	mismatchedConfirmation.Operation.ID = "operation-other"

	for _, test := range []struct {
		name      string
		authority *recordingAuthority
		handler   func(*Server) session.Handler
		request   session.Request
		want      rpc.ErrorCode
	}{
		{
			name:      "start caller",
			authority: &recordingAuthority{},
			handler:   func(server *Server) session.Handler { return server.networkResolverSetupStartHandler(testClientPeer) },
			request:   session.Request{Payload: startPayload},
			want:      rpc.ErrorCodePermissionDenied,
		},
		{
			name:      "prepare caller",
			authority: &recordingAuthority{},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalPrepareHandler(testClientPeer)
			},
			request: session.Request{Payload: preparePayload},
			want:    rpc.ErrorCodePermissionDenied,
		},
		{
			name:      "confirm caller",
			authority: &recordingAuthority{},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalConfirmHandler(testClientPeer)
			},
			request: session.Request{Payload: confirmPayload},
			want:    rpc.ErrorCodePermissionDenied,
		},
		{
			name:      "start request",
			authority: &recordingAuthority{},
			handler:   func(server *Server) session.Handler { return server.networkResolverSetupStartHandler(testClientPeer) },
			request:   session.Request{Peer: peer, Payload: []byte(`{"force":true}`)},
			want:      rpc.ErrorCodeInvalidRequest,
		},
		{
			name:      "prepare request",
			authority: &recordingAuthority{},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalPrepareHandler(testClientPeer)
			},
			request: session.Request{Peer: peer, Payload: []byte(`{"force":true}`)},
			want:    rpc.ErrorCodeInvalidRequest,
		},
		{
			name:      "confirm request",
			authority: &recordingAuthority{},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalConfirmHandler(testClientPeer)
			},
			request: session.Request{Peer: peer, Payload: []byte(`{"force":true}`)},
			want:    rpc.ErrorCodeInvalidRequest,
		},
		{
			name:      "start result validation",
			authority: &recordingAuthority{},
			handler:   func(server *Server) session.Handler { return server.networkResolverSetupStartHandler(testClientPeer) },
			request:   session.Request{Peer: peer, Payload: startPayload},
			want:      rpc.ErrorCodeInternal,
		},
		{
			name:      "prepare result validation",
			authority: &recordingAuthority{},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalPrepareHandler(testClientPeer)
			},
			request: session.Request{Peer: peer, Payload: preparePayload},
			want:    rpc.ErrorCodeInternal,
		},
		{
			name:      "confirm result validation",
			authority: &recordingAuthority{},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalConfirmHandler(testClientPeer)
			},
			request: session.Request{Peer: peer, Payload: confirmPayload},
			want:    rpc.ErrorCodeInternal,
		},
		{
			name:      "start result correlation",
			authority: &recordingAuthority{networkResolverSetup: mismatchedSetup},
			handler:   func(server *Server) session.Handler { return server.networkResolverSetupStartHandler(testClientPeer) },
			request:   session.Request{Peer: peer, Payload: startPayload},
			want:      rpc.ErrorCodeInternal,
		},
		{
			name:      "prepare result correlation",
			authority: &recordingAuthority{networkResolverSetupPreparation: mismatchedPreparation},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalPrepareHandler(testClientPeer)
			},
			request: session.Request{Peer: peer, Payload: preparePayload},
			want:    rpc.ErrorCodeInternal,
		},
		{
			name:      "confirm result correlation",
			authority: &recordingAuthority{networkResolverSetupConfirmation: mismatchedConfirmation},
			handler: func(server *Server) session.Handler {
				return server.networkResolverSetupApprovalConfirmHandler(testClientPeer)
			},
			request: session.Request{Peer: peer, Payload: confirmPayload},
			want:    rpc.ErrorCodeInternal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{config: ServerConfig{Authority: test.authority}}
			_, err := test.handler(server)(t.Context(), test.request)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.want {
				t.Fatalf("handler error = %#v, want %q", err, test.want)
			}
		})
	}
}

// newConfiguredNetworkResolverSetupClient starts a typed client against one deliberately malformed synthetic daemon response.
func newConfiguredNetworkResolverSetupClient(t *testing.T, method string, response any) *configuredStopClient {
	t.Helper()
	clientStream, serverStream := net.Pipe()
	clientConnection := &testLocalConn{Conn: clientStream, peer: testDaemonPeer}
	serverConnection := &testLocalConn{Conn: serverStream, peer: testClientPeer}
	controlServer, err := session.NewServer(session.ServerConfig{
		DaemonVersion:  testBuild.Version,
		ProtocolRanges: protocolRanges(),
		Capabilities:   capabilities(),
		Authorize:      authorizeControlHello,
		Handlers: map[string]session.Handler{
			method: func(context.Context, session.Request) (any, error) {
				return response, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("construct synthetic resolver setup server: %v", err)
	}
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(serverContext, serverConnection)
	}()

	client, err := newClient(context.Background(), ClientConfig{
		Role: rpc.RoleCLI,
		Dial: func(context.Context) (local.Conn, error) {
			return clientConnection, nil
		},
	}, testBuild)
	if err != nil {
		cancelServer()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
		t.Fatalf("construct configured resolver setup client: %v", err)
	}
	running := &configuredStopClient{client: client, cancel: cancelServer, serverDone: serverDone}
	t.Cleanup(func() { running.finish(t) })
	return running
}

// TestNetworkResolverSetupAuthorityErrorsKeepSafeWireCodes verifies classified resolver failures survive authority mapping for all methods.
func TestNetworkResolverSetupAuthorityErrorsKeepSafeWireCodes(t *testing.T) {
	cause := errors.New("network resolver setup state changed")
	authority := &recordingAuthority{
		networkResolverSetupErr:        NewNetworkResolverSetupConflictError(cause),
		networkResolverSetupPrepareErr: NewNetworkResolverSetupNotFoundError(cause),
		networkResolverSetupConfirmErr: NewNetworkResolverSetupConflictError(cause),
	}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	startRequest := StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"}
	prepareRequest := PrepareNetworkResolverSetupApprovalRequest{
		OperationID:               "operation-network-resolver-setup",
		ExpectedOperationRevision: 7,
	}
	confirmRequest := ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               prepareRequest.OperationID,
		ExpectedOperationRevision: prepareRequest.ExpectedOperationRevision,
		ResolverEvidence:          validNetworkResolverSetupEvidence(),
	}
	for _, test := range []struct {
		call func() error
		want rpc.ErrorCode
	}{
		{call: func() error {
			_, err := running.client.StartNetworkResolverSetup(t.Context(), startRequest)
			return err
		}, want: rpc.ErrorCodeConflict},
		{call: func() error {
			_, err := running.client.PrepareNetworkResolverSetupApproval(t.Context(), prepareRequest)
			return err
		}, want: rpc.ErrorCodeNotFound},
		{call: func() error {
			_, err := running.client.ConfirmNetworkResolverSetupApproval(t.Context(), confirmRequest)
			return err
		}, want: rpc.ErrorCodeConflict},
	} {
		var wireError rpc.WireError
		if err := test.call(); !errors.As(err, &wireError) || wireError.Code != test.want {
			t.Fatalf("network resolver setup authority error = %#v, want %q", err, test.want)
		}
	}
}

// TestNetworkResolverSetupHelperFailuresRemainRedacted verifies fixed helper guidance crosses IPC without daemon-local path details.
func TestNetworkResolverSetupHelperFailuresRemainRedacted(t *testing.T) {
	const secret = "/Users/person/private APP_KEY=secret"
	request := StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"}
	for _, test := range []struct {
		name         string
		authorityErr error
		wantCode     rpc.ErrorCode
	}{
		{
			name:         "privileged helper required",
			authorityErr: NewNetworkResolverSetupPrivilegedHelperRequiredError(errors.New(secret)),
			wantCode:     rpc.ErrorCodePrivilegedHelperRequired,
		},
		{
			name:         "privileged helper unsafe",
			authorityErr: NewNetworkResolverSetupPrivilegedHelperUnsafeError(errors.New(secret)),
			wantCode:     rpc.ErrorCodePrivilegedHelperUnsafe,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := &recordingAuthority{networkResolverSetupErr: test.authorityErr}
			running := newRunningControlClient(t, rpc.RoleDesktop, authority, nil)
			_, callErr := running.client.StartNetworkResolverSetup(t.Context(), request)
			var wireError rpc.WireError
			wantMessage := rpc.NewWireError(test.wantCode).Message
			if !errors.As(callErr, &wireError) || wireError.Code != test.wantCode || wireError.Message != wantMessage {
				t.Fatalf("StartNetworkResolverSetup() error = %#v, want %q %q", callErr, test.wantCode, wantMessage)
			}
			if strings.Contains(callErr.Error(), secret) || strings.Contains(callErr.Error(), "APP_KEY") {
				t.Fatalf("StartNetworkResolverSetup() leaked daemon-local cause: %v", callErr)
			}
		})
	}
}

// TestNetworkResolverSetupInternalFailuresStayDaemonLocal verifies unclassified resolver causes are observed but never cross IPC.
func TestNetworkResolverSetupInternalFailuresStayDaemonLocal(t *testing.T) {
	secretFailure := errors.New("resolver failed near /Users/person/private APP_KEY=secret")
	authority := &recordingAuthority{networkResolverSetupErr: secretFailure}
	observed := make(chan error, 1)
	running := newRunningControlClient(t, rpc.RoleDesktop, authority, func(caller Caller, method string, err error) {
		if caller.Transport == testClientPeer && method == methodNetworkResolverSetupStart {
			observed <- err
		}
	})

	_, err := running.client.StartNetworkResolverSetup(
		t.Context(),
		StartNetworkResolverSetupRequest{IntentID: "intent-network-resolver-setup"},
	)
	var wireError rpc.WireError
	if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
		t.Fatalf("resolver authority wire error = %#v, want internal", err)
	}
	if strings.Contains(err.Error(), "APP_KEY") || strings.Contains(err.Error(), "/Users/person/private") {
		t.Fatalf("resolver authority cause crossed the wire: %v", err)
	}
	select {
	case diagnostic := <-observed:
		if !errors.Is(diagnostic, secretFailure) {
			t.Fatalf("observed resolver error = %v, want local authority cause", diagnostic)
		}
	case <-time.After(time.Second):
		t.Fatal("resolver authority failure was not observed locally")
	}
}

// TestNetworkResolverSetupConfirmationCorrelationRequiresLifecycleRevisions proves completion follows the selected approval revision exactly.
func TestNetworkResolverSetupConfirmationCorrelationRequiresLifecycleRevisions(t *testing.T) {
	request := ConfirmNetworkResolverSetupApprovalRequest{
		OperationID:               "operation-network-resolver-setup",
		ExpectedOperationRevision: 7,
		ResolverEvidence:          validNetworkResolverSetupEvidence(),
	}
	confirmation := validNetworkResolverSetupApprovalConfirmation()
	confirmation.NetworkRevision = 9
	confirmation.Revision = 10
	if err := validateNetworkResolverSetupApprovalConfirmationCorrelation(request, confirmation); err != nil {
		t.Fatalf("validateNetworkResolverSetupApprovalConfirmationCorrelation() error = %v", err)
	}
	for _, mutate := range []func(*NetworkResolverSetupApprovalConfirmation){
		func(value *NetworkResolverSetupApprovalConfirmation) { value.Operation.ID = "operation-other" },
		func(value *NetworkResolverSetupApprovalConfirmation) { value.NetworkRevision-- },
		func(value *NetworkResolverSetupApprovalConfirmation) { value.Revision++ },
	} {
		candidate := confirmation
		mutate(&candidate)
		if err := validateNetworkResolverSetupApprovalConfirmationCorrelation(request, candidate); err == nil {
			t.Fatalf("correlation accepted %#v", candidate)
		}
	}
}

// TestNetworkResolverSetupProtocolNamesAndCapabilityRemainStable protects the additive wire surface from accidental renaming.
func TestNetworkResolverSetupProtocolNamesAndCapabilityRemainStable(t *testing.T) {
	if CapabilityNetworkResolverSetupV1 != "control.network-resolver-setup.v1" {
		t.Fatalf("CapabilityNetworkResolverSetupV1 = %q", CapabilityNetworkResolverSetupV1)
	}
	if methodNetworkResolverSetupStart != "control.v1.network.resolver.setup.start" ||
		methodNetworkResolverSetupApprovalPrepare != "control.v1.network.resolver.setup.approval.prepare" ||
		methodNetworkResolverSetupApprovalConfirm != "control.v1.network.resolver.setup.approval.confirm" {
		t.Fatalf(
			"network resolver setup methods = %q / %q / %q",
			methodNetworkResolverSetupStart,
			methodNetworkResolverSetupApprovalPrepare,
			methodNetworkResolverSetupApprovalConfirm,
		)
	}
}
