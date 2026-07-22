package authority

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/host/networkpolicy"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/rpc/local"
	"github.com/goforj/harbor/internal/rpc/session"
	"github.com/goforj/harbor/internal/state"
)

// recordingDataPlaneCoordinator records each authenticated mutation before returning its scripted failure.
type recordingDataPlaneCoordinator struct {
	start          reconcile.NetworkDataPlaneSetupStartRequest
	trustPrepare   reconcile.NetworkDataPlaneSetupPrepareTrustRequest
	trustConfirm   reconcile.NetworkDataPlaneSetupConfirmTrustRequest
	lowPortPrepare reconcile.NetworkDataPlaneSetupPrepareLowPortsRequest
	lowPortConfirm reconcile.NetworkDataPlaneSetupConfirmLowPortsRequest
	err            error
}

// Start records the setup start request so the boundary test can inspect it.
func (c *recordingDataPlaneCoordinator) Start(_ context.Context, r reconcile.NetworkDataPlaneSetupStartRequest) (state.OperationRecord, error) {
	c.start = r
	return state.OperationRecord{}, c.err
}

// PrepareTrust records the trust preparation request so the boundary test can inspect it.
func (c *recordingDataPlaneCoordinator) PrepareTrust(_ context.Context, r reconcile.NetworkDataPlaneSetupPrepareTrustRequest) (ticketissuer.TrustResult, error) {
	c.trustPrepare = r
	return ticketissuer.TrustResult{}, c.err
}

// ConfirmTrust records the trust confirmation request so the boundary test can inspect it.
func (c *recordingDataPlaneCoordinator) ConfirmTrust(_ context.Context, r reconcile.NetworkDataPlaneSetupConfirmTrustRequest) (state.OperationRecord, error) {
	c.trustConfirm = r
	return state.OperationRecord{}, c.err
}

// PrepareLowPorts records the low-port preparation request so the boundary test can inspect it.
func (c *recordingDataPlaneCoordinator) PrepareLowPorts(_ context.Context, r reconcile.NetworkDataPlaneSetupPrepareLowPortsRequest) (ticketissuer.LowPortResult, error) {
	c.lowPortPrepare = r
	return ticketissuer.LowPortResult{}, c.err
}

// ConfirmLowPorts records the low-port confirmation request so the boundary test can inspect it.
func (c *recordingDataPlaneCoordinator) ConfirmLowPorts(_ context.Context, r reconcile.NetworkDataPlaneSetupConfirmLowPortsRequest) (reconcile.NetworkDataPlaneSetupResult, error) {
	c.lowPortConfirm = r
	return reconcile.NetworkDataPlaneSetupResult{}, c.err
}

// recordingDataPlaneOperations records operation reads so the boundary test can inspect them.
type recordingDataPlaneOperations struct {
	request domain.OperationID
	err     error
}

// Operation records the requested operation identity for the boundary test.
func (r *recordingDataPlaneOperations) Operation(_ context.Context, id domain.OperationID) (state.OperationRecord, error) {
	r.request = id
	return state.OperationRecord{}, r.err
}

// TestNetworkDataPlaneAuthorityBindsTransportUserToEveryMutation proves only the authenticated local peer selects helper authority.
func TestNetworkDataPlaneAuthorityBindsTransportUserToEveryMutation(t *testing.T) {
	sentinel := errors.New("stop after recording")
	coordinator := &recordingDataPlaneCoordinator{err: sentinel}
	operations := &recordingDataPlaneOperations{err: sentinel}
	authority := newNetworkDataPlaneSetupAuthority(operations, coordinator, time.Now, func() (domain.OperationID, error) { return "operation-generated", nil })
	caller := control.Caller{Transport: local.PeerIdentity{UserID: "authenticated-user"}}
	trust := helper.TrustMutationEvidence{AuthorityFingerprint: strings.Repeat("a", 64), ObservationFingerprint: strings.Repeat("b", 64), Mechanism: networkpolicy.DarwinCurrentUserTrust, Postcondition: helper.TrustPostconditionExact}
	lowPorts := helper.LowPortMutationEvidence{PolicyFingerprint: strings.Repeat("a", 64), OwnershipFingerprint: strings.Repeat("b", 64), ObservationFingerprint: strings.Repeat("c", 64), Postcondition: helper.LowPortPostconditionExact}
	if _, err := authority.StartNetworkDataPlaneSetup(t.Context(), caller, control.StartNetworkDataPlaneSetupRequest{IntentID: "intent-data-plane"}); !errors.Is(err, sentinel) {
		t.Fatalf("start error = %v", err)
	}
	if _, err := authority.ReadNetworkDataPlaneSetup(t.Context(), caller, control.ReadNetworkDataPlaneSetupRequest{OperationID: "operation-data-plane"}); !errors.Is(err, sentinel) {
		t.Fatalf("read error = %v", err)
	}
	if _, err := authority.PrepareNetworkDataPlaneTrustApproval(t.Context(), caller, control.PrepareNetworkDataPlaneTrustApprovalRequest{OperationID: "operation-data-plane", ExpectedOperationRevision: 7}); !errors.Is(err, sentinel) {
		t.Fatalf("trust prepare error = %v", err)
	}
	if _, err := authority.ConfirmNetworkDataPlaneTrustApproval(t.Context(), caller, control.ConfirmNetworkDataPlaneTrustApprovalRequest{OperationID: "operation-data-plane", ExpectedOperationRevision: 7, TrustEvidence: trust}); !errors.Is(err, sentinel) {
		t.Fatalf("trust confirm error = %v", err)
	}
	if _, err := authority.PrepareNetworkDataPlaneLowPortApproval(t.Context(), caller, control.PrepareNetworkDataPlaneLowPortApprovalRequest{OperationID: "operation-data-plane", ExpectedOperationRevision: 7}); !errors.Is(err, sentinel) {
		t.Fatalf("low-port prepare error = %v", err)
	}
	if _, err := authority.ConfirmNetworkDataPlaneLowPortApproval(t.Context(), caller, control.ConfirmNetworkDataPlaneLowPortApprovalRequest{OperationID: "operation-data-plane", ExpectedOperationRevision: 7, LowPortEvidence: lowPorts}); !errors.Is(err, sentinel) {
		t.Fatalf("low-port confirm error = %v", err)
	}
	if operations.request != "operation-data-plane" {
		t.Fatalf("read operation = %q", operations.request)
	}
	for _, user := range []string{coordinator.start.RequesterIdentity, coordinator.trustPrepare.RequesterIdentity, coordinator.trustConfirm.RequesterIdentity, coordinator.lowPortPrepare.RequesterIdentity, coordinator.lowPortConfirm.RequesterIdentity} {
		if user != caller.Transport.UserID {
			t.Fatalf("mutation requester identity = %q, want %q", user, caller.Transport.UserID)
		}
	}
}

// TestNetworkDataPlaneAuthorityRejectsInvalidGeneratedOperationID prevents a broken identity factory from reaching durable state.
func TestNetworkDataPlaneAuthorityRejectsInvalidGeneratedOperationID(t *testing.T) {
	coordinator := &recordingDataPlaneCoordinator{}
	authority := newNetworkDataPlaneSetupAuthority(&recordingDataPlaneOperations{}, coordinator, time.Now, func() (domain.OperationID, error) { return "", nil })
	_, err := authority.StartNetworkDataPlaneSetup(t.Context(), control.Caller{}, control.StartNetworkDataPlaneSetupRequest{IntentID: "intent-data-plane"})
	if err == nil || !strings.Contains(err.Error(), "generated network data-plane setup operation identity is invalid") {
		t.Fatalf("generated identity error = %v", err)
	}
	if coordinator.start.OperationID != "" {
		t.Fatalf("invalid generated operation reached coordinator: %#v", coordinator.start)
	}
}

// TestNetworkDataPlaneAuthorityMapsDurableErrors preserves reviewed stale and missing categories for every optional RPC.
func TestNetworkDataPlaneAuthorityMapsDurableErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want rpc.ErrorCode
	}{
		{"stale", &state.StaleRevisionError{}, rpc.ErrorCodeConflict},
		{"missing", &state.OperationNotFoundError{}, rpc.ErrorCodeNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyNetworkDataPlaneSetupError(test.err)
			var handlerError *session.HandlerError
			if !errors.As(got, &handlerError) || handlerError.Code() != test.want {
				t.Fatalf("%s mapped to %#v, want %s", test.name, got, test.want)
			}
		})
	}
}
