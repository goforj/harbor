package authority

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// networkDataPlaneSetupOperationReader limits the optional API to one durable operation read.
type networkDataPlaneSetupOperationReader interface {
	Operation(context.Context, domain.OperationID) (state.OperationRecord, error)
}

// networkDataPlaneSetupCoordinator limits the optional API to trusted-ingress setup transitions.
type networkDataPlaneSetupCoordinator interface {
	Start(context.Context, reconcile.NetworkDataPlaneSetupStartRequest) (state.OperationRecord, error)
	PrepareTrust(context.Context, reconcile.NetworkDataPlaneSetupPrepareTrustRequest) (ticketissuer.TrustResult, error)
	ConfirmTrust(context.Context, reconcile.NetworkDataPlaneSetupConfirmTrustRequest) (state.OperationRecord, error)
	PrepareLowPorts(context.Context, reconcile.NetworkDataPlaneSetupPrepareLowPortsRequest) (ticketissuer.LowPortResult, error)
	ConfirmLowPorts(context.Context, reconcile.NetworkDataPlaneSetupConfirmLowPortsRequest) (reconcile.NetworkDataPlaneSetupResult, error)
}

// NetworkDataPlaneSetupAuthority adapts only trusted-ingress setup to the optional control capability.
type NetworkDataPlaneSetupAuthority struct {
	operations     networkDataPlaneSetupOperationReader
	coordinator    networkDataPlaneSetupCoordinator
	now            func() time.Time
	newOperationID func() (domain.OperationID, error)
}

// _ confirms NetworkDataPlaneSetupAuthority exposes the optional control boundary.
var _ control.NetworkDataPlaneSetupAuthority = (*NetworkDataPlaneSetupAuthority)(nil)

// NewNetworkDataPlaneSetupAuthority creates an optional trusted-ingress control authority with all required narrow dependencies.
func NewNetworkDataPlaneSetupAuthority(operations networkDataPlaneSetupOperationReader, coordinator networkDataPlaneSetupCoordinator) *NetworkDataPlaneSetupAuthority {
	return newNetworkDataPlaneSetupAuthority(operations, coordinator, time.Now, newOpaqueOperationID)
}

// newNetworkDataPlaneSetupAuthority keeps time and daemon-owned identity generation deterministic in boundary tests.
func newNetworkDataPlaneSetupAuthority(operations networkDataPlaneSetupOperationReader, coordinator networkDataPlaneSetupCoordinator, now func() time.Time, newOperationID func() (domain.OperationID, error)) *NetworkDataPlaneSetupAuthority {
	if nilAuthorityDependency(operations) || nilAuthorityDependency(coordinator) || nilAuthorityDependency(now) || nilAuthorityDependency(newOperationID) {
		panic("authority.newNetworkDataPlaneSetupAuthority requires every dependency")
	}
	return &NetworkDataPlaneSetupAuthority{operations: operations, coordinator: coordinator, now: now, newOperationID: newOperationID}
}

// StartNetworkDataPlaneSetup assigns a daemon-owned operation ID before staging one authenticated intent.
func (authority *NetworkDataPlaneSetupAuthority) StartNetworkDataPlaneSetup(ctx context.Context, caller control.Caller, request control.StartNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkDataPlaneSetupOperation{}, err
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.NetworkDataPlaneSetupOperation{}, fmt.Errorf("generate network data-plane setup operation identity: %w", err)
	}
	if err := operationID.Validate(); err != nil {
		return control.NetworkDataPlaneSetupOperation{}, fmt.Errorf("generated network data-plane setup operation identity is invalid: %w", err)
	}
	result, err := authority.coordinator.Start(normalizeContext(ctx), reconcile.NetworkDataPlaneSetupStartRequest{OperationID: operationID, IntentID: request.IntentID, RequesterIdentity: caller.Transport.UserID})
	if err != nil {
		return control.NetworkDataPlaneSetupOperation{}, classifyNetworkDataPlaneSetupError(err)
	}
	return networkDataPlaneSetupOperation(result, request.IntentID)
}

// ReadNetworkDataPlaneSetup returns only the requested global trusted-ingress operation.
func (authority *NetworkDataPlaneSetupAuthority) ReadNetworkDataPlaneSetup(ctx context.Context, _ control.Caller, request control.ReadNetworkDataPlaneSetupRequest) (control.NetworkDataPlaneSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkDataPlaneSetupOperation{}, err
	}
	result, err := authority.operations.Operation(normalizeContext(ctx), request.OperationID)
	if err != nil {
		return control.NetworkDataPlaneSetupOperation{}, classifyNetworkDataPlaneSetupError(err)
	}
	return networkDataPlaneSetupOperation(result, "")
}

// PrepareNetworkDataPlaneTrustApproval binds trust helper authority to the authenticated transport user.
func (authority *NetworkDataPlaneSetupAuthority) PrepareNetworkDataPlaneTrustApproval(ctx context.Context, caller control.Caller, request control.PrepareNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneTrustApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkDataPlaneTrustApprovalPreparation{}, err
	}
	result, err := authority.coordinator.PrepareTrust(normalizeContext(ctx), reconcile.NetworkDataPlaneSetupPrepareTrustRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: caller.Transport.UserID})
	if err != nil {
		return control.NetworkDataPlaneTrustApprovalPreparation{}, classifyNetworkDataPlaneSetupError(err)
	}
	if err := result.Validate(authority.now().UTC()); err != nil {
		return control.NetworkDataPlaneTrustApprovalPreparation{}, fmt.Errorf("network data-plane trust preparation result: %w", err)
	}
	preparation := control.NetworkDataPlaneTrustApprovalPreparation{OperationID: request.OperationID, OperationRevision: request.ExpectedOperationRevision, Ticket: control.NetworkDataPlaneTrustApprovalTicket{OperationID: result.OperationID, Reference: result.Reference, Operation: result.Operation, PolicyFingerprint: result.PolicyFingerprint, TargetOwnershipFingerprint: result.OwnershipFingerprint, AuthorityFingerprint: result.AuthorityFingerprint, Mechanism: result.Mechanism, ExpiresAt: result.ExpiresAt}}
	if err := preparation.Validate(); err != nil {
		return control.NetworkDataPlaneTrustApprovalPreparation{}, fmt.Errorf("network data-plane trust preparation: %w", err)
	}
	return preparation, nil
}

// ConfirmNetworkDataPlaneTrustApproval independently verifies trust and advances to low-port approval.
func (authority *NetworkDataPlaneSetupAuthority) ConfirmNetworkDataPlaneTrustApproval(ctx context.Context, caller control.Caller, request control.ConfirmNetworkDataPlaneTrustApprovalRequest) (control.NetworkDataPlaneSetupOperation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkDataPlaneSetupOperation{}, err
	}
	result, err := authority.coordinator.ConfirmTrust(normalizeContext(ctx), reconcile.NetworkDataPlaneSetupConfirmTrustRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: caller.Transport.UserID, TrustEvidence: request.TrustEvidence})
	if err != nil {
		return control.NetworkDataPlaneSetupOperation{}, classifyNetworkDataPlaneSetupError(err)
	}
	setup, err := networkDataPlaneSetupOperation(result, "")
	if err != nil {
		return control.NetworkDataPlaneSetupOperation{}, err
	}
	if setup.Revision <= request.ExpectedOperationRevision || !control.RequiresNetworkDataPlaneLowPortApproval(setup) {
		return control.NetworkDataPlaneSetupOperation{}, errors.New("network data-plane trust confirmation did not reach low-port approval")
	}
	return setup, nil
}

// PrepareNetworkDataPlaneLowPortApproval binds paired low-port helper authority to the authenticated transport user.
func (authority *NetworkDataPlaneSetupAuthority) PrepareNetworkDataPlaneLowPortApproval(ctx context.Context, caller control.Caller, request control.PrepareNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneLowPortApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkDataPlaneLowPortApprovalPreparation{}, err
	}
	result, err := authority.coordinator.PrepareLowPorts(normalizeContext(ctx), reconcile.NetworkDataPlaneSetupPrepareLowPortsRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: caller.Transport.UserID})
	if err != nil {
		return control.NetworkDataPlaneLowPortApprovalPreparation{}, classifyNetworkDataPlaneSetupError(err)
	}
	if err := result.Validate(authority.now().UTC()); err != nil {
		return control.NetworkDataPlaneLowPortApprovalPreparation{}, fmt.Errorf("network data-plane low-port preparation result: %w", err)
	}
	preparation := control.NetworkDataPlaneLowPortApprovalPreparation{OperationID: request.OperationID, OperationRevision: request.ExpectedOperationRevision, Ticket: control.NetworkDataPlaneLowPortApprovalTicket{OperationID: result.OperationID, Reference: result.Reference, Operation: result.Operation, PolicyFingerprint: result.PolicyFingerprint, TargetOwnershipFingerprint: result.OwnershipFingerprint, ObservationFingerprint: result.ObservationFingerprint, ExpiresAt: result.ExpiresAt}}
	if err := preparation.Validate(); err != nil {
		return control.NetworkDataPlaneLowPortApprovalPreparation{}, fmt.Errorf("network data-plane low-port preparation: %w", err)
	}
	return preparation, nil
}

// ConfirmNetworkDataPlaneLowPortApproval independently verifies low-port authority and completes trusted ingress setup.
func (authority *NetworkDataPlaneSetupAuthority) ConfirmNetworkDataPlaneLowPortApproval(ctx context.Context, caller control.Caller, request control.ConfirmNetworkDataPlaneLowPortApprovalRequest) (control.NetworkDataPlaneSetupConfirmation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkDataPlaneSetupConfirmation{}, err
	}
	result, err := authority.coordinator.ConfirmLowPorts(normalizeContext(ctx), reconcile.NetworkDataPlaneSetupConfirmLowPortsRequest{OperationID: request.OperationID, ExpectedOperationRevision: request.ExpectedOperationRevision, RequesterIdentity: caller.Transport.UserID, LowPortEvidence: request.LowPortEvidence})
	if err != nil {
		return control.NetworkDataPlaneSetupConfirmation{}, classifyNetworkDataPlaneSetupError(err)
	}
	if err := result.Validate(); err != nil {
		return control.NetworkDataPlaneSetupConfirmation{}, fmt.Errorf("network data-plane low-port confirmation result: %w", err)
	}
	confirmation := control.NetworkDataPlaneSetupConfirmation{Operation: result.Operation.Operation, Revision: result.Operation.Revision, NetworkRevision: result.Network.Record.Revision}
	if err := confirmation.Validate(); err != nil {
		return control.NetworkDataPlaneSetupConfirmation{}, fmt.Errorf("network data-plane low-port confirmation: %w", err)
	}
	if confirmation.Operation.ID != request.OperationID || confirmation.Revision <= request.ExpectedOperationRevision {
		return control.NetworkDataPlaneSetupConfirmation{}, errors.New("network data-plane low-port confirmation differs from the requested operation revision")
	}
	return confirmation, nil
}

// networkDataPlaneSetupOperation projects one checked durable operation into its narrow control representation.
func networkDataPlaneSetupOperation(record state.OperationRecord, intentID domain.IntentID) (control.NetworkDataPlaneSetupOperation, error) {
	setup := control.NetworkDataPlaneSetupOperation{Operation: record.Operation, Revision: record.Revision}
	if err := setup.Validate(); err != nil {
		return control.NetworkDataPlaneSetupOperation{}, fmt.Errorf("network data-plane setup operation: %w", err)
	}
	if intentID != "" && setup.Operation.IntentID != intentID {
		return control.NetworkDataPlaneSetupOperation{}, errors.New("network data-plane setup operation differs from the requested intent")
	}
	return setup, nil
}

// classifyNetworkDataPlaneSetupError maps reviewed durable conflicts to stable optional-control categories.
func classifyNetworkDataPlaneSetupError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var stale *state.StaleRevisionError
	var missing *state.OperationNotFoundError
	var intentConflict *state.IntentConflictError
	if errors.As(err, &missing) {
		return control.NewNetworkDataPlaneSetupNotFoundError(err)
	}
	if errors.As(err, &stale) || errors.As(err, &intentConflict) {
		return control.NewNetworkDataPlaneSetupConflictError(err)
	}
	return err
}
