package control

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/session"
)

// TestControlClientRoundTripsProjectRuntimeRepairForHumanRoles verifies both human clients preserve caller identity and opaque selections.
func TestControlClientRoundTripsProjectRuntimeRepairForHumanRoles(t *testing.T) {
	for _, role := range []rpc.Role{rpc.RoleCLI, rpc.RoleDesktop} {
		t.Run(string(role), func(t *testing.T) {
			confirmable := runtimeRepairContractTestConfirmable(t)
			inspection := ProjectRuntimeRepairInspection{
				ProjectID:   "project-orders",
				Disposition: ProjectRuntimeRepairInspectionConfirmable,
				Confirmable: &confirmable,
			}
			confirmation := runtimeRepairContractTestConfirmation(t)
			authority := &recordingAuthority{
				projectRuntimeRepairInspection:   inspection,
				projectRuntimeRepairConfirmation: confirmation,
			}
			running := newRunningControlClient(t, role, authority, nil)
			inspectRequest := InspectProjectRuntimeRepairRequest{ProjectID: inspection.ProjectID}
			confirmRequest := ConfirmProjectRuntimeRepairRequest{
				ProjectID:    inspection.ProjectID,
				InspectionID: confirmable.InspectionID,
				Fingerprint:  confirmable.CandidateFingerprint,
			}

			gotInspection, err := running.client.InspectProjectRuntimeRepair(t.Context(), inspectRequest)
			if err != nil || !reflect.DeepEqual(gotInspection, inspection) {
				t.Fatalf("InspectProjectRuntimeRepair() = %#v, %v, want %#v", gotInspection, err, inspection)
			}
			gotConfirmation, err := running.client.ConfirmProjectRuntimeRepair(t.Context(), confirmRequest)
			if err != nil || !reflect.DeepEqual(gotConfirmation, confirmation) {
				t.Fatalf("ConfirmProjectRuntimeRepair() = %#v, %v, want %#v", gotConfirmation, err, confirmation)
			}

			authority.mu.Lock()
			inspectRequests := append(
				[]InspectProjectRuntimeRepairRequest(nil),
				authority.projectRuntimeRepairInspectRequests...,
			)
			confirmRequests := append(
				[]ConfirmProjectRuntimeRepairRequest(nil),
				authority.projectRuntimeRepairConfirmRequests...,
			)
			authority.mu.Unlock()
			if !reflect.DeepEqual(inspectRequests, []InspectProjectRuntimeRepairRequest{inspectRequest}) ||
				!reflect.DeepEqual(confirmRequests, []ConfirmProjectRuntimeRepairRequest{confirmRequest}) {
				t.Fatalf("project runtime repair authority requests = %#v / %#v", inspectRequests, confirmRequests)
			}
			callers := authority.recordedCallers()
			if len(callers) != 2 {
				t.Fatalf("project runtime repair authority callers = %d, want 2", len(callers))
			}
			for _, caller := range callers {
				if caller.Transport != testClientPeer || caller.Session.Role != role ||
					!containsCapability(caller.Session.Capabilities, CapabilityProjectRuntimeRepairV1) {
					t.Fatalf("project runtime repair caller = %#v, want authenticated %s caller", caller, role)
				}
			}
		})
	}
}

// rawProjectRuntimeRepairPayload emits adversarial fields without encoding normalization.
type rawProjectRuntimeRepairPayload string

// MarshalJSON returns the exact document needed to exercise strict repair request decoding.
func (payload rawProjectRuntimeRepairPayload) MarshalJSON() ([]byte, error) {
	return []byte(payload), nil
}

// TestProjectRuntimeRepairHandlersRejectUnreviewedJSON proves strict decoding precedes repair authority.
func TestProjectRuntimeRepairHandlersRejectUnreviewedJSON(t *testing.T) {
	authority := &recordingAuthority{}
	running := newRunningControlClient(t, rpc.RoleCLI, authority, nil)
	inspectionID := strings.Repeat("a", projectRuntimeRepairOpaqueHexLength)
	fingerprint := strings.Repeat("b", projectRuntimeRepairOpaqueHexLength)
	for _, test := range []struct {
		method  string
		payload rawProjectRuntimeRepairPayload
	}{
		{
			method:  methodProjectRuntimeRepairInspect,
			payload: `{"project_id":"project-orders","project_id":"project-other"}`,
		},
		{
			method:  methodProjectRuntimeRepairInspect,
			payload: `{"project_id":"project-orders","root_pid":321}`,
		},
		{
			method: methodProjectRuntimeRepairConfirm,
			payload: rawProjectRuntimeRepairPayload(
				`{"project_id":"project-orders","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `","candidate_fingerprint":"` + fingerprint + `"}`,
			),
		},
		{
			method: methodProjectRuntimeRepairConfirm,
			payload: rawProjectRuntimeRepairPayload(
				`{"project_id":"project-orders","inspection_id":"` + inspectionID + `","candidate_fingerprint":"` + fingerprint + `","endpoint":"127.0.0.1:3000"}`,
			),
		},
	} {
		_, err := running.client.session.Call(t.Context(), test.method, test.payload)
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInvalidRequest {
			t.Fatalf("%s payload %s error = %#v, want invalid_request", test.method, test.payload, err)
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("invalid project runtime repair JSON reached authority %d times", len(callers))
	}
}

// TestProjectRuntimeRepairRequiresNegotiatedCapability proves clients and handlers reject the additive methods before dispatch.
func TestProjectRuntimeRepairRequiresNegotiatedCapability(t *testing.T) {
	if CapabilityProjectRuntimeRepairV1 != "control.project-runtime-repair.v1" ||
		methodProjectRuntimeRepairInspect != "control.v1.project.runtime-repair.inspect" ||
		methodProjectRuntimeRepairConfirm != "control.v1.project.runtime-repair.confirm" {
		t.Fatal("project runtime repair protocol identifiers changed")
	}
	confirmable := runtimeRepairContractTestConfirmable(t)
	inspectRequest := InspectProjectRuntimeRepairRequest{ProjectID: "project-orders"}
	confirmRequest := ConfirmProjectRuntimeRepairRequest{
		ProjectID:    inspectRequest.ProjectID,
		InspectionID: confirmable.InspectionID,
		Fingerprint:  confirmable.CandidateFingerprint,
	}
	legacyClient := &Client{peer: DaemonPeer{Session: session.Peer{Capabilities: []rpc.Capability{CapabilityV1}}}}
	for _, call := range []func() error{
		func() error {
			_, err := legacyClient.InspectProjectRuntimeRepair(t.Context(), inspectRequest)
			return err
		},
		func() error {
			_, err := legacyClient.ConfirmProjectRuntimeRepair(t.Context(), confirmRequest)
			return err
		},
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "does not support project runtime repair") {
			t.Fatalf("legacy client capability error = %v", err)
		}
	}

	clientStream, serverStream := net.Pipe()
	authority := &recordingAuthority{}
	controlServer, err := newServer(ServerConfig{Authority: authority, RequestShutdown: func() {}}, testBuild)
	if err != nil {
		t.Fatalf("newServer() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- controlServer.Serve(
			context.Background(),
			&testLocalConn{Conn: serverStream, peer: testClientPeer},
		)
	}()
	client, err := session.NewClient(
		context.Background(),
		&testLocalConn{Conn: clientStream, peer: testDaemonPeer},
		session.ClientConfig{
			Role:           rpc.RoleDesktop,
			ClientVersion:  testBuild.Version,
			ProtocolRanges: protocolRanges(),
			Capabilities:   []rpc.Capability{CapabilityV1},
		},
	)
	if err != nil {
		t.Fatalf("session.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("project runtime repair server did not stop")
		}
	})
	for _, call := range []struct {
		method  string
		request any
	}{
		{method: methodProjectRuntimeRepairInspect, request: inspectRequest},
		{method: methodProjectRuntimeRepairConfirm, request: confirmRequest},
	} {
		_, err := client.Call(t.Context(), call.method, call.request)
		var wireError rpc.WireError
		if !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodePermissionDenied {
			t.Fatalf("%s capability error = %#v, want permission_denied", call.method, err)
		}
	}
	if callers := authority.recordedCallers(); len(callers) != 0 {
		t.Fatalf("unnegotiated project runtime repair reached authority %d times", len(callers))
	}
}

// TestControlClientRejectsUncorrelatedProjectRuntimeRepairResponses proves framed responses remain bound to the selected project.
func TestControlClientRejectsUncorrelatedProjectRuntimeRepairResponses(t *testing.T) {
	t.Run("inspection", func(t *testing.T) {
		client := newTypedResponseTestClient(t, methodProjectRuntimeRepairInspect, projectRuntimeRepairInspectionResponse{
			Inspection: ProjectRuntimeRepairInspection{
				ProjectID:   "project-other",
				Disposition: ProjectRuntimeRepairInspectionUnsupported,
			},
		})
		_, err := client.InspectProjectRuntimeRepair(t.Context(), InspectProjectRuntimeRepairRequest{ProjectID: "project-orders"})
		if err == nil || !strings.Contains(err.Error(), "belongs to another project") {
			t.Fatalf("InspectProjectRuntimeRepair() error = %v, want project correlation failure", err)
		}
	})

	t.Run("confirmation", func(t *testing.T) {
		confirmation := runtimeRepairContractTestConfirmation(t)
		confirmation.Project.ID = "project-other"
		confirmable := runtimeRepairContractTestConfirmable(t)
		client := newTypedResponseTestClient(t, methodProjectRuntimeRepairConfirm, projectRuntimeRepairConfirmationResponse{
			Confirmation: confirmation,
		})
		_, err := client.ConfirmProjectRuntimeRepair(t.Context(), ConfirmProjectRuntimeRepairRequest{
			ProjectID:    "project-orders",
			InspectionID: confirmable.InspectionID,
			Fingerprint:  confirmable.CandidateFingerprint,
		})
		if err == nil || !strings.Contains(err.Error(), "belongs to another project") {
			t.Fatalf("ConfirmProjectRuntimeRepair() error = %v, want project correlation failure", err)
		}
	})
}

// TestProjectRuntimeRepairHandlersRejectUncorrelatedAuthorityOutput proves daemon defects fail before serialization.
func TestProjectRuntimeRepairHandlersRejectUncorrelatedAuthorityOutput(t *testing.T) {
	inspection := ProjectRuntimeRepairInspection{
		ProjectID:   "project-other",
		Disposition: ProjectRuntimeRepairInspectionUnsupported,
	}
	confirmation := runtimeRepairContractTestConfirmation(t)
	confirmation.Project.ID = "project-other"
	authority := &recordingAuthority{
		projectRuntimeRepairInspection:   inspection,
		projectRuntimeRepairConfirmation: confirmation,
	}
	running := newRunningControlClient(t, rpc.RoleDesktop, authority, nil)
	confirmable := runtimeRepairContractTestConfirmable(t)
	for _, call := range []func() error{
		func() error {
			_, err := running.client.InspectProjectRuntimeRepair(t.Context(), InspectProjectRuntimeRepairRequest{
				ProjectID: "project-orders",
			})
			return err
		},
		func() error {
			_, err := running.client.ConfirmProjectRuntimeRepair(t.Context(), ConfirmProjectRuntimeRepairRequest{
				ProjectID:    "project-orders",
				InspectionID: confirmable.InspectionID,
				Fingerprint:  confirmable.CandidateFingerprint,
			})
			return err
		},
	} {
		var wireError rpc.WireError
		if err := call(); !errors.As(err, &wireError) || wireError.Code != rpc.ErrorCodeInternal {
			t.Fatalf("uncorrelated authority output error = %#v, want internal", err)
		}
	}
}

// TestProjectRuntimeRepairErrorConstructorsKeepReviewedWireCodes verifies repair classifications remain at the control boundary.
func TestProjectRuntimeRepairErrorConstructorsKeepReviewedWireCodes(t *testing.T) {
	cause := errors.New("reviewed project runtime repair failure")
	for _, test := range []struct {
		name      string
		construct func(error) error
		want      rpc.ErrorCode
	}{
		{name: "invalid", construct: NewProjectRuntimeRepairInvalidError, want: rpc.ErrorCodeInvalidRequest},
		{name: "not found", construct: NewProjectRuntimeRepairNotFoundError, want: rpc.ErrorCodeNotFound},
		{name: "conflict", construct: NewProjectRuntimeRepairConflictError, want: rpc.ErrorCodeConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.construct(cause)
			var handlerError *session.HandlerError
			if !errors.As(err, &handlerError) || handlerError.Code() != test.want || !errors.Is(err, cause) {
				t.Fatalf("constructor error = %#v, want %q wrapping cause", err, test.want)
			}
		})
	}
}
