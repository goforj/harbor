package authority

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/state"
)

// networkResolverPolicyMigrationCoordinator limits the optional authority to legacy resolver-policy retirement.
type networkResolverPolicyMigrationCoordinator interface {
	Start(context.Context, reconcile.NetworkResolverPolicyMigrationStartRequest) (state.OperationRecord, error)
	Prepare(context.Context, reconcile.NetworkResolverPolicyMigrationPrepareRequest) (ticketissuer.ResolverResult, error)
	Confirm(context.Context, reconcile.NetworkResolverPolicyMigrationConfirmRequest) (state.CompleteNetworkResolverPolicyMigrationResult, error)
}

// NetworkResolverPolicyMigrationAuthority adapts only legacy resolver-policy retirement to the optional control capability.
type NetworkResolverPolicyMigrationAuthority struct {
	coordinator    networkResolverPolicyMigrationCoordinator
	now            func() time.Time
	newOperationID func() (domain.OperationID, error)
}

// _ confirms NetworkResolverPolicyMigrationAuthority exposes the optional control boundary.
var _ control.NetworkResolverPolicyMigrationAuthority = (*NetworkResolverPolicyMigrationAuthority)(nil)

// NewNetworkResolverPolicyMigrationAuthority creates an optional legacy resolver-policy retirement authority.
func NewNetworkResolverPolicyMigrationAuthority(coordinator networkResolverPolicyMigrationCoordinator) *NetworkResolverPolicyMigrationAuthority {
	return newNetworkResolverPolicyMigrationAuthority(coordinator, time.Now, newOpaqueOperationID)
}

// newNetworkResolverPolicyMigrationAuthority keeps time and daemon-owned identity generation deterministic in boundary tests.
func newNetworkResolverPolicyMigrationAuthority(coordinator networkResolverPolicyMigrationCoordinator, now func() time.Time, newOperationID func() (domain.OperationID, error)) *NetworkResolverPolicyMigrationAuthority {
	if nilAuthorityDependency(coordinator) || nilAuthorityDependency(now) || nilAuthorityDependency(newOperationID) {
		panic("authority.newNetworkResolverPolicyMigrationAuthority requires every dependency")
	}
	return &NetworkResolverPolicyMigrationAuthority{
		coordinator:    coordinator,
		now:            now,
		newOperationID: newOperationID,
	}
}

// StartNetworkResolverPolicyMigration assigns daemon operation identity to one authenticated legacy-policy retirement intent.
func (authority *NetworkResolverPolicyMigrationAuthority) StartNetworkResolverPolicyMigration(ctx context.Context, caller control.Caller, request control.StartNetworkResolverPolicyMigrationRequest) (control.NetworkResolverPolicyMigrationOperation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationOperation{}, err
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.NetworkResolverPolicyMigrationOperation{}, fmt.Errorf("generate network resolver policy migration operation identity: %w", err)
	}
	if err := operationID.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationOperation{}, fmt.Errorf("generated network resolver policy migration operation identity is invalid: %w", err)
	}
	started, err := authority.coordinator.Start(normalizeContext(ctx), reconcile.NetworkResolverPolicyMigrationStartRequest{
		OperationID:       operationID,
		IntentID:          request.IntentID,
		RequesterIdentity: caller.Transport.UserID,
	})
	if err != nil {
		return control.NetworkResolverPolicyMigrationOperation{}, classifyNetworkResolverPolicyMigrationError(err)
	}
	result := control.NetworkResolverPolicyMigrationOperation{
		Operation: started.Operation,
		Revision:  started.Revision,
	}
	if err := result.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationOperation{}, fmt.Errorf("network resolver policy migration result: %w", err)
	}
	if result.Operation.IntentID != request.IntentID {
		return control.NetworkResolverPolicyMigrationOperation{}, errors.New("network resolver policy migration result differs from its requested intent")
	}
	return result, nil
}

// PrepareNetworkResolverPolicyMigrationApproval binds one legacy resolver retirement capability to the authenticated transport requester.
func (authority *NetworkResolverPolicyMigrationAuthority) PrepareNetworkResolverPolicyMigrationApproval(ctx context.Context, caller control.Caller, request control.PrepareNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalPreparation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalPreparation{}, err
	}
	prepared, err := authority.coordinator.Prepare(normalizeContext(ctx), reconcile.NetworkResolverPolicyMigrationPrepareRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
	})
	disposition := control.NetworkResolverPolicyMigrationPublicationDurable
	if err != nil {
		if !errors.Is(err, ticketissuer.ErrResolverPublicationIndeterminate) {
			return control.NetworkResolverPolicyMigrationApprovalPreparation{}, classifyNetworkResolverPolicyMigrationError(err)
		}
		disposition = control.NetworkResolverPolicyMigrationPublicationIndeterminate
	}
	if err := prepared.Validate(authority.now().UTC()); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalPreparation{}, fmt.Errorf("network resolver policy migration approval preparation result: %w", err)
	}
	if prepared.OperationID != request.OperationID {
		return control.NetworkResolverPolicyMigrationApprovalPreparation{}, errors.New("network resolver policy migration approval preparation differs from its requested operation")
	}
	result := control.NetworkResolverPolicyMigrationApprovalPreparation{
		OperationID:            request.OperationID,
		OperationRevision:      request.ExpectedOperationRevision,
		PublicationDisposition: disposition,
		Ticket: control.NetworkResolverPolicyMigrationApprovalTicket{
			OperationID:              prepared.OperationID,
			Reference:                prepared.Reference,
			Operation:                prepared.Operation,
			PolicyFingerprint:        prepared.PolicyFingerprint,
			PostOwnershipFingerprint: prepared.OwnershipFingerprint,
			ExpiresAt:                prepared.ExpiresAt,
		},
	}
	if err := result.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalPreparation{}, fmt.Errorf("network resolver policy migration approval preparation result: %w", err)
	}
	return result, nil
}

// ConfirmNetworkResolverPolicyMigrationApproval binds confirmation to the authenticated owner while the coordinator owns native observation.
func (authority *NetworkResolverPolicyMigrationAuthority) ConfirmNetworkResolverPolicyMigrationApproval(ctx context.Context, caller control.Caller, request control.ConfirmNetworkResolverPolicyMigrationApprovalRequest) (control.NetworkResolverPolicyMigrationApprovalConfirmation, error) {
	if err := request.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, err
	}
	confirmed, err := authority.coordinator.Confirm(normalizeContext(ctx), reconcile.NetworkResolverPolicyMigrationConfirmRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
		ResolverEvidence:          request.ResolverEvidence,
	})
	if err != nil {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, classifyNetworkResolverPolicyMigrationError(err)
	}
	if err := confirmed.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, fmt.Errorf("network resolver policy migration approval confirmation result: %w", err)
	}
	result := control.NetworkResolverPolicyMigrationApprovalConfirmation{
		Operation:       confirmed.Operation.Operation,
		Revision:        confirmed.Operation.Revision,
		NetworkRevision: confirmed.NetworkRevision,
	}
	if err := result.Validate(); err != nil {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, fmt.Errorf("network resolver policy migration approval confirmation result: %w", err)
	}
	if result.Operation.ID != request.OperationID {
		return control.NetworkResolverPolicyMigrationApprovalConfirmation{}, errors.New("network resolver policy migration approval confirmation differs from its requested operation")
	}
	return result, nil
}

// classifyNetworkResolverPolicyMigrationError maps reviewed retirement-state failures to existing safe control categories.
func classifyNetworkResolverPolicyMigrationError(err error) error {
	var corruptState *state.CorruptStateError
	if errors.As(err, &corruptState) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ticketspool.ErrNotInstalled) {
		return control.NewNetworkResolverSetupPrivilegedHelperRequiredError(err)
	}
	if errors.Is(err, ticketspool.ErrUnsafePath) {
		return control.NewNetworkResolverSetupPrivilegedHelperUnsafeError(err)
	}
	var intentConflict *state.IntentConflictError
	var staleRevision *state.StaleRevisionError
	var networkMissing *state.NetworkNotInitializedError
	var networkRevision *state.NetworkRevisionConflictError
	var completionConflict *state.NetworkResolverSetupCompletionConflictError
	if errors.As(err, &intentConflict) || errors.As(err, &staleRevision) || errors.As(err, &networkMissing) || errors.As(err, &networkRevision) || errors.As(err, &completionConflict) || errors.Is(err, ticketissuer.ErrResolverPublicationIndeterminate) {
		return control.NewNetworkResolverSetupConflictError(err)
	}
	var operationMissing *state.OperationNotFoundError
	if errors.As(err, &operationMissing) {
		return control.NewNetworkResolverSetupNotFoundError(err)
	}
	return err
}
