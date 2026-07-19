package authority

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/state"
)

// controlState limits daemon authority to the complete durable reads needed by control clients.
type controlState interface {
	// CurrentSequence establishes the diagnostic revision without loading the larger replacement snapshot.
	CurrentSequence(context.Context) (domain.Sequence, error)
	// Snapshot supplies one transactionally consistent replacement for every client projection.
	Snapshot(context.Context) (domain.Snapshot, error)
	// RegisterProject creates or replays one inert project registration atomically.
	RegisterProject(context.Context, domain.ProjectSnapshot) (state.ProjectRegistration, error)
}

// projectDiscoverer isolates filesystem discovery from durable registration policy.
type projectDiscoverer interface {
	// Discover returns one canonical marker-validated checkout and its allowlisted presentation metadata.
	Discover(context.Context, string) (projectdiscovery.Discovery, error)
}

// projectUnregisterCoordinator limits authority to user-initiated methods rather than restart recovery internals.
type projectUnregisterCoordinator interface {
	// Start initiates or replays one daemon-identified unregister operation for a client-owned intent.
	Start(context.Context, reconcile.StartRequest) (state.OperationRecord, error)
	// Prepare returns release progress and at most one caller-bound helper capability.
	Prepare(context.Context, reconcile.PrepareRequest) (reconcile.PrepareResult, error)
	// Confirm independently verifies release effects and completes the unregister operation.
	Confirm(context.Context, reconcile.ConfirmRequest) (state.OperationRecord, error)
}

// Authority projects the daemon's durable state through the bounded control protocol.
type Authority struct {
	store          controlState
	unregister     projectUnregisterCoordinator
	build          buildinfo.Info
	discoverer     projectDiscoverer
	now            func() time.Time
	newProjectID   func() (domain.ProjectID, error)
	newOperationID func() (domain.OperationID, error)
}

var _ control.Authority = (*Authority)(nil)

// NewAuthority creates the production control authority from durable state and unregister coordination.
func NewAuthority(store *state.Store, unregister *reconcile.ProjectUnregisterCoordinator) *Authority {
	if store == nil || unregister == nil {
		panic("authority.NewAuthority requires non-nil state and unregister dependencies")
	}
	return newAuthority(store, unregister, buildinfo.Current())
}

// newAuthority keeps process build metadata deterministic without broadening production injection.
func newAuthority(
	store controlState,
	unregister projectUnregisterCoordinator,
	build buildinfo.Info,
) *Authority {
	return newAuthorityWithRegistration(
		store,
		unregister,
		build,
		projectdiscovery.NewDiscoverer(),
		time.Now,
		newOpaqueProjectID,
	)
}

// newAuthorityWithRegistration keeps discovery and clock behavior deterministic in registration tests.
func newAuthorityWithRegistration(
	store controlState,
	unregister projectUnregisterCoordinator,
	build buildinfo.Info,
	discoverer projectDiscoverer,
	now func() time.Time,
	newProjectID func() (domain.ProjectID, error),
) *Authority {
	return newAuthorityWithIdentityFactories(
		store,
		unregister,
		build,
		discoverer,
		now,
		newProjectID,
		newOpaqueOperationID,
	)
}

// newAuthorityWithIdentityFactories keeps both daemon-owned identity sources deterministic in boundary tests.
func newAuthorityWithIdentityFactories(
	store controlState,
	unregister projectUnregisterCoordinator,
	build buildinfo.Info,
	discoverer projectDiscoverer,
	now func() time.Time,
	newProjectID func() (domain.ProjectID, error),
	newOperationID func() (domain.OperationID, error),
) *Authority {
	if nilAuthorityDependency(store) ||
		nilAuthorityDependency(unregister) ||
		nilAuthorityDependency(discoverer) ||
		nilAuthorityDependency(now) ||
		nilAuthorityDependency(newProjectID) ||
		nilAuthorityDependency(newOperationID) {
		panic("authority.newAuthorityWithIdentityFactories requires every dependency")
	}
	return &Authority{
		store:          store,
		unregister:     unregister,
		build:          build,
		discoverer:     discoverer,
		now:            now,
		newProjectID:   newProjectID,
		newOperationID: newOperationID,
	}
}

// nilAuthorityDependency rejects nil and typed-nil collaborators before request dispatch can panic.
func nilAuthorityDependency(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Status joins session negotiation with one durable sequence so diagnostics identify the exact authority serving the caller.
func (authority *Authority) Status(ctx context.Context, caller control.Caller) (control.DaemonStatus, error) {
	ctx = normalizeContext(ctx)
	capabilities, err := rpc.CanonicalCapabilities(caller.Session.Capabilities)
	if err != nil {
		return control.DaemonStatus{}, fmt.Errorf("canonicalize negotiated capabilities: %w", err)
	}
	sequence, err := authority.store.CurrentSequence(ctx)
	if err != nil {
		return control.DaemonStatus{}, err
	}

	return control.DaemonStatus{
		State: control.DaemonStateReady,
		Build: control.Build{
			Version:  authority.build.Version,
			Revision: authority.build.Revision,
			Modified: authority.build.Modified,
		},
		Protocol:              caller.Session.Protocol,
		Capabilities:          capabilities,
		SnapshotSchemaVersion: domain.SnapshotSchemaVersion,
		Sequence:              sequence,
	}, nil
}

// Snapshot delegates the complete durable replacement so the control layer cannot drift from the Store's transaction boundary.
func (authority *Authority) Snapshot(ctx context.Context, _ control.Caller) (domain.Snapshot, error) {
	return authority.store.Snapshot(normalizeContext(ctx))
}

// RegisterProject discovers one canonical checkout and commits its inert stopped projection.
func (authority *Authority) RegisterProject(
	ctx context.Context,
	_ control.Caller,
	request control.RegisterProjectRequest,
) (control.ProjectRegistration, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectRegistration{}, err
	}
	discovery, err := authority.discoverer.Discover(ctx, request.Path)
	if err != nil {
		var invalidProject *projectdiscovery.InvalidProjectError
		if errors.As(err, &invalidProject) {
			return control.ProjectRegistration{}, control.NewProjectRegistrationInvalidError(err)
		}
		return control.ProjectRegistration{}, err
	}
	projectID, err := authority.newProjectID()
	if err != nil {
		return control.ProjectRegistration{}, fmt.Errorf("generate project identity: %w", err)
	}
	project, err := discovery.ProjectSnapshot(projectID, authority.now())
	if err != nil {
		return control.ProjectRegistration{}, err
	}
	registered, err := authority.store.RegisterProject(ctx, project)
	if err != nil {
		var conflict *state.ProjectRegistrationConflictError
		if errors.As(err, &conflict) {
			return control.ProjectRegistration{}, control.NewProjectRegistrationConflictError(err)
		}
		var releaseActive *state.ProjectNetworkReleaseActiveError
		if errors.As(err, &releaseActive) {
			return control.ProjectRegistration{}, control.NewProjectRegistrationConflictError(err)
		}
		return control.ProjectRegistration{}, err
	}
	result := control.ProjectRegistration{
		Project:  registered.Record.Project,
		Revision: registered.Record.Revision,
		Created:  registered.Created,
	}
	if err := result.Validate(); err != nil {
		return control.ProjectRegistration{}, fmt.Errorf("project registration result: %w", err)
	}
	return result, nil
}

// UnregisterProject assigns daemon operation identity before starting or replaying one client-owned intent.
func (authority *Authority) UnregisterProject(
	ctx context.Context,
	_ control.Caller,
	request control.UnregisterProjectRequest,
) (control.ProjectUnregistration, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectUnregistration{}, err
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("generate project unregister operation identity: %w", err)
	}
	started, err := authority.unregister.Start(ctx, reconcile.StartRequest{
		ProjectID:   request.ProjectID,
		OperationID: operationID,
		IntentID:    request.IntentID,
	})
	if err != nil {
		return control.ProjectUnregistration{}, classifyProjectUnregisterError(err)
	}
	result := control.ProjectUnregistration{
		Operation: started.Operation,
		Revision:  started.Revision,
	}
	if err := result.Validate(); err != nil {
		return control.ProjectUnregistration{}, fmt.Errorf("project unregistration result: %w", err)
	}
	if result.Operation.ProjectID != request.ProjectID || result.Operation.IntentID != request.IntentID {
		return control.ProjectUnregistration{}, errors.New("project unregistration result differs from its requested project and intent")
	}
	return result, nil
}

// PrepareProjectUnregisterApproval binds helper authority exclusively to the authenticated transport identity.
func (authority *Authority) PrepareProjectUnregisterApproval(
	ctx context.Context,
	caller control.Caller,
	request control.PrepareProjectUnregisterApprovalRequest,
) (control.ProjectUnregisterApprovalPreparation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectUnregisterApprovalPreparation{}, err
	}
	prepared, err := authority.unregister.Prepare(ctx, reconcile.PrepareRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
	})
	if err != nil {
		return control.ProjectUnregisterApprovalPreparation{}, classifyProjectUnregisterApprovalError(err)
	}
	if prepared.OperationID != request.OperationID || prepared.OperationRevision != request.ExpectedOperationRevision {
		return control.ProjectUnregisterApprovalPreparation{}, errors.New("project unregister approval preparation differs from its requested operation revision")
	}
	result := control.ProjectUnregisterApprovalPreparation{
		OperationID:       prepared.OperationID,
		OperationRevision: prepared.OperationRevision,
		ProjectID:         prepared.ProjectID,
		TotalLeases:       prepared.TotalLeases,
		ReleasedLeases:    prepared.ReleasedLeases,
		PendingLeases:     prepared.PendingLeases,
	}
	if prepared.Ticket != nil {
		result.Ticket = &control.HelperApprovalTicket{
			OperationID: prepared.Ticket.OperationID,
			LeaseKey: control.HelperApprovalLeaseKey{
				ProjectID:   prepared.Ticket.LeaseKey.ProjectID,
				SecondaryID: prepared.Ticket.LeaseKey.SecondaryID,
			},
			Reference: prepared.Ticket.Reference,
			Operation: prepared.Ticket.Operation,
			Address:   prepared.Ticket.Address.String(),
			ExpiresAt: prepared.Ticket.ExpiresAt,
		}
	}
	if err := result.Validate(); err != nil {
		return control.ProjectUnregisterApprovalPreparation{}, fmt.Errorf("project unregister approval preparation result: %w", err)
	}
	return result, nil
}

// ConfirmProjectUnregisterApproval maps the coordinator's succeeded durable operation into the control result.
func (authority *Authority) ConfirmProjectUnregisterApproval(
	ctx context.Context,
	_ control.Caller,
	request control.ConfirmProjectUnregisterApprovalRequest,
) (control.ProjectUnregisterApprovalConfirmation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectUnregisterApprovalConfirmation{}, err
	}
	confirmed, err := authority.unregister.Confirm(ctx, reconcile.ConfirmRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
	})
	if err != nil {
		return control.ProjectUnregisterApprovalConfirmation{}, classifyProjectUnregisterApprovalError(err)
	}
	if confirmed.Operation.ID != request.OperationID {
		return control.ProjectUnregisterApprovalConfirmation{}, errors.New("project unregister approval confirmation differs from its requested operation")
	}
	result := control.ProjectUnregisterApprovalConfirmation{
		Operation: confirmed.Operation,
		Revision:  confirmed.Revision,
	}
	if err := result.Validate(); err != nil {
		return control.ProjectUnregisterApprovalConfirmation{}, fmt.Errorf("project unregister approval confirmation result: %w", err)
	}
	return result, nil
}

// classifyProjectUnregisterError maps only failures caused by the requested project or intent to stable control categories.
func classifyProjectUnregisterError(err error) error {
	var corruptState *state.CorruptStateError
	if errors.As(err, &corruptState) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var projectMissing *state.ProjectNotFoundError
	if errors.As(err, &projectMissing) {
		return control.NewProjectUnregisterNotFoundError(err)
	}

	var intentConflict *state.IntentConflictError
	var staleRevision *state.StaleRevisionError
	var projectBusy *state.ProjectBusyError
	var projectRevisionConflict *state.ProjectRevisionConflictError
	var networkRevisionConflict *state.NetworkRevisionConflictError
	var networkProjectSetConflict *state.NetworkProjectSetConflictError
	var networkProjectReplacementConflict *state.NetworkProjectReplacementConflictError
	var releaseConflict *state.ProjectNetworkReleaseConflictError
	var durableReleaseIncomplete *state.ProjectNetworkReleaseIncompleteError
	var releaseActive *state.ProjectNetworkReleaseActiveError
	var hostConflict *reconcile.HostStateConflictError
	var releaseIncomplete *reconcile.ReleaseIncompleteError
	if errors.As(err, &intentConflict) ||
		errors.As(err, &staleRevision) ||
		errors.As(err, &projectBusy) ||
		errors.As(err, &projectRevisionConflict) ||
		errors.As(err, &networkRevisionConflict) ||
		errors.As(err, &networkProjectSetConflict) ||
		errors.As(err, &networkProjectReplacementConflict) ||
		errors.As(err, &releaseConflict) ||
		errors.As(err, &durableReleaseIncomplete) ||
		errors.As(err, &releaseActive) ||
		errors.As(err, &hostConflict) ||
		errors.As(err, &releaseIncomplete) ||
		errors.Is(err, harbordruntime.ErrProjectWithdrawalUnverified) {
		return control.NewProjectUnregisterConflictError(err)
	}
	return err
}

// classifyProjectUnregisterApprovalError maps only reviewed lifecycle failures to stable control categories.
func classifyProjectUnregisterApprovalError(err error) error {
	var corruptState *state.CorruptStateError
	if errors.As(err, &corruptState) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var staleRevision *state.StaleRevisionError
	var projectBusy *state.ProjectBusyError
	var projectRevisionConflict *state.ProjectRevisionConflictError
	var networkRevisionConflict *state.NetworkRevisionConflictError
	var releaseConflict *state.ProjectNetworkReleaseConflictError
	var durableReleaseIncomplete *state.ProjectNetworkReleaseIncompleteError
	var releaseActive *state.ProjectNetworkReleaseActiveError
	var hostConflict *reconcile.HostStateConflictError
	var releaseIncomplete *reconcile.ReleaseIncompleteError
	if errors.As(err, &staleRevision) ||
		errors.As(err, &projectBusy) ||
		errors.As(err, &projectRevisionConflict) ||
		errors.As(err, &networkRevisionConflict) ||
		errors.As(err, &releaseConflict) ||
		errors.As(err, &durableReleaseIncomplete) ||
		errors.As(err, &releaseActive) ||
		errors.As(err, &hostConflict) ||
		errors.As(err, &releaseIncomplete) ||
		errors.Is(err, harbordruntime.ErrProjectWithdrawalUnverified) {
		return control.NewProjectUnregisterApprovalConflictError(err)
	}
	var operationMissing *state.OperationNotFoundError
	var projectMissing *state.ProjectNotFoundError
	var releaseMissing *state.ProjectNetworkReleaseNotFoundError
	if errors.As(err, &operationMissing) || errors.As(err, &projectMissing) || errors.As(err, &releaseMissing) {
		return control.NewProjectUnregisterApprovalNotFoundError(err)
	}
	return err
}

// newOpaqueProjectID generates an identity that remains independent of checkout path, slug, and configuration.
func newOpaqueProjectID() (domain.ProjectID, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	projectID := domain.ProjectID("project-" + hex.EncodeToString(random))
	if err := projectID.Validate(); err != nil {
		return "", err
	}
	return projectID, nil
}

// newOpaqueOperationID generates daemon-owned journal identity independently of client idempotency keys.
func newOpaqueOperationID() (domain.OperationID, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	operationID := domain.OperationID("operation-" + hex.EncodeToString(random))
	if err := operationID.Validate(); err != nil {
		return "", err
	}
	return operationID, nil
}

// normalizeContext keeps nil control calls usable while preserving explicit cancellation.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}
