package authority

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"time"

	"github.com/goforj/harbor/internal/buildinfo"
	"github.com/goforj/harbor/internal/control"
	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/harbordruntime"
	"github.com/goforj/harbor/internal/helper/ticketissuer"
	"github.com/goforj/harbor/internal/helper/ticketspool"
	"github.com/goforj/harbor/internal/network/identity"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/reconcile"
	"github.com/goforj/harbor/internal/rpc"
	"github.com/goforj/harbor/internal/state"
)

// controlState limits daemon authority to the complete durable reads needed by control clients.
type controlState interface {
	// CurrentSequence establishes the diagnostic revision without loading the larger replacement snapshot.
	CurrentSequence(context.Context) (domain.Sequence, error)
	// RuntimeState supplies one transactionally consistent client and network replacement.
	RuntimeState(context.Context) (state.RuntimeState, error)
	// RegisterProject creates or replays one inert project registration atomically.
	RegisterProject(context.Context, domain.ProjectSnapshot) (state.ProjectRegistration, error)
}

// httpRouteObserver limits public URL projection to exact routes owned by the ready data plane.
type httpRouteObserver interface {
	// HTTPRouteLive reports whether one exact host-to-upstream route is currently published.
	HTTPRouteLive(string, netip.AddrPort) bool
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

// projectLifecycleCoordinator limits authority to durable managed-process intent submission.
type projectLifecycleCoordinator interface {
	// Start durably records and schedules one idempotent project start.
	Start(context.Context, reconcile.ProjectStartRequest) (state.OperationRecord, error)
	// Stop durably records and schedules one idempotent project stop.
	Stop(context.Context, reconcile.ProjectStopRequest) (state.OperationRecord, error)
	// ProjectActivity reads bounded output only for the current durable session.
	ProjectActivity(context.Context, reconcile.ProjectActivityRequest) (reconcile.ProjectActivity, error)
	// ServiceLogs reads bounded output for one Compose service in the current durable session.
	ServiceLogs(context.Context, reconcile.ProjectServiceLogsRequest) (reconcile.ProjectServiceLogs, error)
}

// networkSetupCoordinator limits authority to the interactive machine-network setup lifecycle.
type networkSetupCoordinator interface {
	// Start stages or replays one daemon-identified network setup operation.
	Start(context.Context, reconcile.NetworkSetupStartRequest) (state.OperationRecord, error)
	// Prepare returns one helper capability bound to an authenticated requester and operation revision.
	Prepare(context.Context, reconcile.NetworkSetupPrepareRequest) (ticketissuer.PoolResult, error)
	// Confirm verifies helper evidence and returns the atomically completed operation and network foundation.
	Confirm(context.Context, reconcile.NetworkSetupConfirmRequest) (state.CompleteNetworkSetupResult, error)
}

// networkResolverSetupCoordinator limits authority to the policy-bound resolver setup lifecycle.
type networkResolverSetupCoordinator interface {
	// Start stages or replays one daemon-identified resolver setup operation.
	Start(context.Context, reconcile.NetworkResolverSetupStartRequest) (state.OperationRecord, error)
	// Prepare returns one resolver helper capability bound to an authenticated requester and operation revision.
	Prepare(context.Context, reconcile.NetworkResolverSetupPrepareRequest) (ticketissuer.ResolverResult, error)
	// Confirm verifies helper and native resolver evidence before returning the atomic durable completion.
	Confirm(context.Context, reconcile.NetworkResolverSetupConfirmRequest) (state.CompleteNetworkResolverSetupResult, error)
}

// Authority projects the daemon's durable state through the bounded control protocol.
type Authority struct {
	store             controlState
	routes            httpRouteObserver
	unregister        projectUnregisterCoordinator
	lifecycle         projectLifecycleCoordinator
	runtimeRepair     projectRuntimeRepairCoordinator
	networkSetup      networkSetupCoordinator
	resolverSetup     networkResolverSetupCoordinator
	build             buildinfo.Info
	discoverer        projectDiscoverer
	now               func() time.Time
	newProjectID      func() (domain.ProjectID, error)
	newOperationID    func() (domain.OperationID, error)
	newInstallationID func() (identity.InstallationID, error)
}

var _ control.Authority = (*Authority)(nil)

// NewAuthority creates the production control authority from durable state and required reconciliation coordinators.
func NewAuthority(
	store *state.Store,
	unregister *reconcile.ProjectUnregisterCoordinator,
	lifecycle *reconcile.ProjectLifecycleCoordinator,
	networkSetup *reconcile.NetworkSetupCoordinator,
	resolverSetup *reconcile.NetworkResolverSetupCoordinator,
	routes *harbordruntime.Controller,
) *Authority {
	if store == nil || unregister == nil || lifecycle == nil || networkSetup == nil || resolverSetup == nil || routes == nil {
		panic("authority.NewAuthority requires non-nil state, unregister, lifecycle, network setup, resolver setup, and HTTP route dependencies")
	}
	authority := newAuthority(store, unregister, buildinfo.Current(), lifecycle, networkSetup, resolverSetup, routes)
	authority.runtimeRepair = reconcile.NewProjectRuntimeRepairCoordinator(store)
	return authority
}

// newAuthority keeps process build metadata deterministic without broadening production injection.
func newAuthority(
	store controlState,
	unregister projectUnregisterCoordinator,
	build buildinfo.Info,
	lifecycle projectLifecycleCoordinator,
	networkSetup networkSetupCoordinator,
	resolverSetup networkResolverSetupCoordinator,
	routes httpRouteObserver,
) *Authority {
	return newAuthorityWithRegistration(
		store,
		unregister,
		build,
		projectdiscovery.NewDiscoverer(),
		time.Now,
		newOpaqueProjectID,
		lifecycle,
		networkSetup,
		resolverSetup,
		routes,
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
	lifecycle projectLifecycleCoordinator,
	networkSetup networkSetupCoordinator,
	resolverSetup networkResolverSetupCoordinator,
	routes httpRouteObserver,
) *Authority {
	return newAuthorityWithIdentityFactories(
		store,
		unregister,
		build,
		discoverer,
		now,
		newProjectID,
		newOpaqueOperationID,
		newOpaqueInstallationID,
		lifecycle,
		networkSetup,
		resolverSetup,
		routes,
	)
}

// newAuthorityWithIdentityFactories keeps every daemon-owned identity source deterministic in boundary tests.
func newAuthorityWithIdentityFactories(
	store controlState,
	unregister projectUnregisterCoordinator,
	build buildinfo.Info,
	discoverer projectDiscoverer,
	now func() time.Time,
	newProjectID func() (domain.ProjectID, error),
	newOperationID func() (domain.OperationID, error),
	newInstallationID func() (identity.InstallationID, error),
	lifecycle projectLifecycleCoordinator,
	networkSetup networkSetupCoordinator,
	resolverSetup networkResolverSetupCoordinator,
	routes httpRouteObserver,
) *Authority {
	if nilAuthorityDependency(store) ||
		nilAuthorityDependency(unregister) ||
		nilAuthorityDependency(lifecycle) ||
		nilAuthorityDependency(networkSetup) ||
		nilAuthorityDependency(resolverSetup) ||
		nilAuthorityDependency(discoverer) ||
		nilAuthorityDependency(now) ||
		nilAuthorityDependency(newProjectID) ||
		nilAuthorityDependency(newOperationID) ||
		nilAuthorityDependency(newInstallationID) ||
		nilAuthorityDependency(routes) {
		panic("authority.newAuthorityWithIdentityFactories requires every dependency")
	}
	return &Authority{
		store:             store,
		routes:            routes,
		unregister:        unregister,
		lifecycle:         lifecycle,
		runtimeRepair:     unsupportedProjectRuntimeRepairCoordinator{},
		networkSetup:      networkSetup,
		resolverSetup:     resolverSetup,
		build:             build,
		discoverer:        discoverer,
		now:               now,
		newProjectID:      newProjectID,
		newOperationID:    newOperationID,
		newInstallationID: newInstallationID,
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

// Snapshot projects live public URLs from one atomic durable aggregate without replacing private runtime targets.
func (authority *Authority) Snapshot(ctx context.Context, _ control.Caller) (domain.Snapshot, error) {
	runtimeState, err := authority.store.RuntimeState(normalizeContext(ctx))
	if err != nil {
		return domain.Snapshot{}, err
	}
	return publicControlSnapshot(runtimeState, authority.routes), nil
}

// publicControlSnapshot clones durable state before substituting routes proven live by this process generation.
func publicControlSnapshot(runtimeState state.RuntimeState, routes httpRouteObserver) domain.Snapshot {
	snapshot := cloneControlSnapshot(runtimeState.Snapshot)
	if !runtimeState.NetworkInitialized || runtimeState.Network.Stage != state.NetworkStageFull {
		return snapshot
	}

	endpoints := make(map[state.EndpointReservationKey]state.EndpointReservation, len(runtimeState.Network.Reservations.Endpoints))
	for _, endpoint := range runtimeState.Network.Reservations.Endpoints {
		if endpoint.Protocol == state.EndpointProtocolHTTP {
			endpoints[endpoint.Key] = endpoint
		}
	}
	for projectIndex := range snapshot.Projects {
		project := &snapshot.Projects[projectIndex]
		for resourceIndex := range project.Resources {
			resource := &project.Resources[resourceIndex]
			key := state.EndpointReservationKey{ProjectID: project.ID, EndpointID: string(resource.ID)}
			endpoint, found := endpoints[key]
			if !found {
				continue
			}
			if resource.ID == "app-http" && endpoint.Host != project.Slug+".test" {
				continue
			}
			upstream, ok := canonicalPrivateHTTPOrigin(resource.URL)
			if !ok || !routes.HTTPRouteLive(endpoint.Host, upstream) {
				continue
			}
			resource.URL = (&url.URL{Scheme: "https", Host: endpoint.Host}).String()
		}
	}
	return snapshot
}

// cloneControlSnapshot keeps returned and durable collection storage independent across projection and callers.
func cloneControlSnapshot(snapshot domain.Snapshot) domain.Snapshot {
	clone := snapshot
	clone.Projects = slices.Clone(snapshot.Projects)
	clone.Operations = slices.Clone(snapshot.Operations)
	clone.RecentResourceIDs = slices.Clone(snapshot.RecentResourceIDs)
	for index := range clone.Projects {
		clone.Projects[index].Apps = slices.Clone(snapshot.Projects[index].Apps)
		clone.Projects[index].Services = slices.Clone(snapshot.Projects[index].Services)
		clone.Projects[index].Resources = slices.Clone(snapshot.Projects[index].Resources)
	}
	return clone
}

// canonicalPrivateHTTPOrigin accepts only the exact IPv4 loopback origin persisted by project startup.
func canonicalPrivateHTTPOrigin(rawURL string) (netip.AddrPort, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return netip.AddrPort{}, false
	}
	upstream, err := netip.ParseAddrPort(parsed.Host)
	if err != nil {
		return netip.AddrPort{}, false
	}
	upstream = netip.AddrPortFrom(upstream.Addr().Unmap(), upstream.Port())
	canonical := (&url.URL{Scheme: "http", Host: upstream.String()}).String()
	if rawURL != canonical || !upstream.Addr().Is4() || !upstream.Addr().IsLoopback() || upstream.Port() == 0 {
		return netip.AddrPort{}, false
	}
	return upstream, true
}

// StartNetworkSetup assigns daemon-owned operation and installation identities to one authenticated setup intent.
func (authority *Authority) StartNetworkSetup(
	ctx context.Context,
	caller control.Caller,
	request control.StartNetworkSetupRequest,
) (control.NetworkSetupOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkSetupOperation{}, err
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("generate network setup operation identity: %w", err)
	}
	if err := operationID.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("generated network setup operation identity is invalid: %w", err)
	}
	installationID, err := authority.newInstallationID()
	if err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("generate network setup installation identity: %w", err)
	}
	if err := installationID.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("generated network setup installation identity is invalid: %w", err)
	}
	coordinatorRequest := reconcile.NetworkSetupStartRequest{
		OperationID:       operationID,
		IntentID:          request.IntentID,
		InstallationID:    installationID,
		RequesterIdentity: caller.Transport.UserID,
	}
	if err := coordinatorRequest.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("network setup coordinator request: %w", err)
	}
	started, err := authority.networkSetup.Start(ctx, coordinatorRequest)
	if err != nil {
		return control.NetworkSetupOperation{}, classifyNetworkSetupError(err)
	}
	result := control.NetworkSetupOperation{
		Operation: started.Operation,
		Revision:  started.Revision,
	}
	if err := result.Validate(); err != nil {
		return control.NetworkSetupOperation{}, fmt.Errorf("network setup result: %w", err)
	}
	if result.Operation.IntentID != request.IntentID {
		return control.NetworkSetupOperation{}, errors.New("network setup result differs from its requested intent")
	}
	return result, nil
}

// PrepareNetworkSetupApproval binds one helper pool capability to the authenticated transport requester.
func (authority *Authority) PrepareNetworkSetupApproval(
	ctx context.Context,
	caller control.Caller,
	request control.PrepareNetworkSetupApprovalRequest,
) (control.NetworkSetupApprovalPreparation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkSetupApprovalPreparation{}, err
	}
	coordinatorRequest := reconcile.NetworkSetupPrepareRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
	}
	if err := coordinatorRequest.Validate(); err != nil {
		return control.NetworkSetupApprovalPreparation{}, fmt.Errorf("network setup approval coordinator request: %w", err)
	}
	prepared, err := authority.networkSetup.Prepare(ctx, coordinatorRequest)
	if err != nil {
		return control.NetworkSetupApprovalPreparation{}, classifyNetworkSetupError(err)
	}
	if err := prepared.Validate(authority.now().UTC()); err != nil {
		return control.NetworkSetupApprovalPreparation{}, fmt.Errorf("network setup approval preparation result: %w", err)
	}
	if prepared.OperationID != request.OperationID {
		return control.NetworkSetupApprovalPreparation{}, errors.New("network setup approval preparation differs from its requested operation")
	}
	result := control.NetworkSetupApprovalPreparation{
		OperationID:       request.OperationID,
		OperationRevision: request.ExpectedOperationRevision,
		Ticket: control.NetworkSetupApprovalTicket{
			OperationID: prepared.OperationID,
			Reference:   prepared.Reference,
			Operation:   prepared.Operation,
			Pool:        prepared.Pool.String(),
			ExpiresAt:   prepared.ExpiresAt,
		},
	}
	if err := result.Validate(); err != nil {
		return control.NetworkSetupApprovalPreparation{}, fmt.Errorf("network setup approval preparation result: %w", err)
	}
	return result, nil
}

// ConfirmNetworkSetupApproval projects one correlated atomic setup completion into the control protocol.
func (authority *Authority) ConfirmNetworkSetupApproval(
	ctx context.Context,
	_ control.Caller,
	request control.ConfirmNetworkSetupApprovalRequest,
) (control.NetworkSetupApprovalConfirmation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkSetupApprovalConfirmation{}, err
	}
	confirmed, err := authority.networkSetup.Confirm(ctx, reconcile.NetworkSetupConfirmRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		HelperPoolEvidence:        request.PoolEvidence,
	})
	if err != nil {
		return control.NetworkSetupApprovalConfirmation{}, classifyNetworkSetupError(err)
	}
	if err := confirmed.Validate(); err != nil {
		return control.NetworkSetupApprovalConfirmation{}, fmt.Errorf("network setup approval confirmation result: %w", err)
	}
	result := control.NetworkSetupApprovalConfirmation{
		Operation:       confirmed.Operation.Operation,
		Revision:        confirmed.Operation.Revision,
		NetworkRevision: confirmed.Network.Record.Revision,
		Pool:            confirmed.Network.Record.Pool.Prefix().String(),
	}
	if err := result.Validate(); err != nil {
		return control.NetworkSetupApprovalConfirmation{}, fmt.Errorf("network setup approval confirmation result: %w", err)
	}
	if result.Operation.ID != request.OperationID ||
		result.NetworkRevision != request.ExpectedOperationRevision+2 ||
		result.Revision != request.ExpectedOperationRevision+3 ||
		result.Pool != request.PoolEvidence.Pool {
		return control.NetworkSetupApprovalConfirmation{}, errors.New("network setup approval confirmation differs from its requested operation revision and pool")
	}
	return result, nil
}

// StartNetworkResolverSetup assigns daemon operation identity to one authenticated resolver setup intent.
func (authority *Authority) StartNetworkResolverSetup(
	ctx context.Context,
	caller control.Caller,
	request control.StartNetworkResolverSetupRequest,
) (control.NetworkResolverSetupOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkResolverSetupOperation{}, err
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.NetworkResolverSetupOperation{}, fmt.Errorf("generate network resolver setup operation identity: %w", err)
	}
	if err := operationID.Validate(); err != nil {
		return control.NetworkResolverSetupOperation{}, fmt.Errorf("generated network resolver setup operation identity is invalid: %w", err)
	}
	coordinatorRequest := reconcile.NetworkResolverSetupStartRequest{
		OperationID:       operationID,
		IntentID:          request.IntentID,
		RequesterIdentity: caller.Transport.UserID,
	}
	if err := coordinatorRequest.Validate(); err != nil {
		return control.NetworkResolverSetupOperation{}, fmt.Errorf("network resolver setup coordinator request: %w", err)
	}
	started, err := authority.resolverSetup.Start(ctx, coordinatorRequest)
	if err != nil {
		return control.NetworkResolverSetupOperation{}, classifyNetworkResolverSetupError(err)
	}
	result := control.NetworkResolverSetupOperation{
		Operation: started.Operation,
		Revision:  started.Revision,
	}
	if err := result.Validate(); err != nil {
		return control.NetworkResolverSetupOperation{}, fmt.Errorf("network resolver setup result: %w", err)
	}
	if result.Operation.IntentID != request.IntentID {
		return control.NetworkResolverSetupOperation{}, errors.New("network resolver setup result differs from its requested intent")
	}
	return result, nil
}

// PrepareNetworkResolverSetupApproval binds one resolver helper capability to the authenticated transport requester.
func (authority *Authority) PrepareNetworkResolverSetupApproval(
	ctx context.Context,
	caller control.Caller,
	request control.PrepareNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalPreparation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkResolverSetupApprovalPreparation{}, err
	}
	coordinatorRequest := reconcile.NetworkResolverSetupPrepareRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		RequesterIdentity:         caller.Transport.UserID,
	}
	if err := coordinatorRequest.Validate(); err != nil {
		return control.NetworkResolverSetupApprovalPreparation{}, fmt.Errorf("network resolver setup approval coordinator request: %w", err)
	}
	prepared, err := authority.resolverSetup.Prepare(ctx, coordinatorRequest)
	if err != nil {
		return control.NetworkResolverSetupApprovalPreparation{}, classifyNetworkResolverSetupError(err)
	}
	if err := prepared.Validate(authority.now().UTC()); err != nil {
		return control.NetworkResolverSetupApprovalPreparation{}, fmt.Errorf("network resolver setup approval preparation result: %w", err)
	}
	if prepared.OperationID != request.OperationID {
		return control.NetworkResolverSetupApprovalPreparation{}, errors.New("network resolver setup approval preparation differs from its requested operation")
	}
	result := control.NetworkResolverSetupApprovalPreparation{
		OperationID:       request.OperationID,
		OperationRevision: request.ExpectedOperationRevision,
		Ticket: control.NetworkResolverSetupApprovalTicket{
			OperationID:                prepared.OperationID,
			Reference:                  prepared.Reference,
			Operation:                  prepared.Operation,
			PolicyFingerprint:          prepared.PolicyFingerprint,
			TargetOwnershipFingerprint: prepared.OwnershipFingerprint,
			ExpiresAt:                  prepared.ExpiresAt,
		},
	}
	if err := result.Validate(); err != nil {
		return control.NetworkResolverSetupApprovalPreparation{}, fmt.Errorf("network resolver setup approval preparation result: %w", err)
	}
	return result, nil
}

// ConfirmNetworkResolverSetupApproval projects one correlated resolver completion into the control protocol.
func (authority *Authority) ConfirmNetworkResolverSetupApproval(
	ctx context.Context,
	_ control.Caller,
	request control.ConfirmNetworkResolverSetupApprovalRequest,
) (control.NetworkResolverSetupApprovalConfirmation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.NetworkResolverSetupApprovalConfirmation{}, err
	}
	confirmed, err := authority.resolverSetup.Confirm(ctx, reconcile.NetworkResolverSetupConfirmRequest{
		OperationID:               request.OperationID,
		ExpectedOperationRevision: request.ExpectedOperationRevision,
		ResolverEvidence:          request.ResolverEvidence,
	})
	if err != nil {
		return control.NetworkResolverSetupApprovalConfirmation{}, classifyNetworkResolverSetupError(err)
	}
	if err := confirmed.Validate(); err != nil {
		return control.NetworkResolverSetupApprovalConfirmation{}, fmt.Errorf("network resolver setup approval confirmation result: %w", err)
	}
	result := control.NetworkResolverSetupApprovalConfirmation{
		Operation:       confirmed.Operation.Operation,
		Revision:        confirmed.Operation.Revision,
		NetworkRevision: confirmed.NetworkRevision,
	}
	if err := result.Validate(); err != nil {
		return control.NetworkResolverSetupApprovalConfirmation{}, fmt.Errorf("network resolver setup approval confirmation result: %w", err)
	}
	if result.Operation.ID != request.OperationID ||
		result.NetworkRevision <= request.ExpectedOperationRevision+1 ||
		result.Revision != result.NetworkRevision+1 {
		return control.NetworkResolverSetupApprovalConfirmation{}, errors.New("network resolver setup approval confirmation differs from its requested operation revision")
	}
	return result, nil
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

// StartProject assigns daemon operation identity before durably scheduling one managed GoForj development process.
func (authority *Authority) StartProject(
	ctx context.Context,
	_ control.Caller,
	request control.StartProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, control.NewProjectLifecycleInvalidError(err)
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("generate project start operation identity: %w", err)
	}
	started, err := authority.lifecycle.Start(ctx, reconcile.ProjectStartRequest{
		ProjectID:   request.ProjectID,
		OperationID: operationID,
		IntentID:    request.IntentID,
	})
	if err != nil {
		return control.ProjectLifecycleOperation{}, classifyProjectLifecycleError(err)
	}
	return projectLifecycleResult(started, request.ProjectID, request.IntentID, domain.OperationKindProjectStart)
}

// StopProject assigns daemon operation identity before durably scheduling exact-session process shutdown.
func (authority *Authority) StopProject(
	ctx context.Context,
	_ control.Caller,
	request control.StopProjectRequest,
) (control.ProjectLifecycleOperation, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, control.NewProjectLifecycleInvalidError(err)
	}
	operationID, err := authority.newOperationID()
	if err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("generate project stop operation identity: %w", err)
	}
	stopped, err := authority.lifecycle.Stop(ctx, reconcile.ProjectStopRequest{
		ProjectID:   request.ProjectID,
		OperationID: operationID,
		IntentID:    request.IntentID,
	})
	if err != nil {
		return control.ProjectLifecycleOperation{}, classifyProjectLifecycleError(err)
	}
	return projectLifecycleResult(stopped, request.ProjectID, request.IntentID, domain.OperationKindProjectStop)
}

// ProjectActivity projects current durable session output without exposing process ownership evidence.
func (authority *Authority) ProjectActivity(
	ctx context.Context,
	_ control.Caller,
	request control.ProjectActivityRequest,
) (control.ProjectActivity, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ProjectActivity{}, control.NewProjectActivityInvalidError(err)
	}
	activity, err := authority.lifecycle.ProjectActivity(ctx, reconcile.ProjectActivityRequest{
		ProjectID: request.ProjectID,
		SessionID: request.SessionID,
		Cursor:    request.Cursor,
		Wait:      time.Duration(request.WaitMilliseconds) * time.Millisecond,
	})
	if err != nil {
		var projectMissing *state.ProjectNotFoundError
		if errors.As(err, &projectMissing) {
			return control.ProjectActivity{}, control.NewProjectActivityNotFoundError(err)
		}
		return control.ProjectActivity{}, err
	}
	result := control.ProjectActivity{ProjectID: activity.ProjectID}
	if activity.Session != nil {
		result.Session = &control.ProjectSessionActivity{
			ID:         activity.Session.ID,
			State:      activity.Session.State,
			Generation: activity.Session.Generation,
			Output: control.ProjectOutputChunk{
				Available:  activity.Session.Output.Available,
				Reset:      activity.Session.Output.Reset,
				Truncated:  activity.Session.Output.Truncated,
				HasMore:    activity.Session.Output.HasMore,
				NextCursor: activity.Session.Output.NextCursor,
				Text:       activity.Session.Output.Text,
			},
		}
	}
	if err := result.Validate(); err != nil {
		return control.ProjectActivity{}, fmt.Errorf("project activity result: %w", err)
	}
	if result.ProjectID != request.ProjectID {
		return control.ProjectActivity{}, errors.New("project activity result differs from its requested project")
	}
	bounded, err := control.BoundProjectActivityResponse(result)
	if err != nil {
		return control.ProjectActivity{}, fmt.Errorf("bound project activity result: %w", err)
	}
	return bounded, nil
}

// ServiceLogs projects one selected Compose service without exposing runtime ownership evidence.
func (authority *Authority) ServiceLogs(
	ctx context.Context,
	_ control.Caller,
	request control.ServiceLogsRequest,
) (control.ServiceLogs, error) {
	ctx = normalizeContext(ctx)
	if err := request.Validate(); err != nil {
		return control.ServiceLogs{}, control.NewServiceLogsInvalidError(err)
	}
	logs, err := authority.lifecycle.ServiceLogs(ctx, reconcile.ProjectServiceLogsRequest{
		ProjectID: request.ProjectID,
		SessionID: request.SessionID,
		ServiceID: request.ServiceID,
		Cursor:    request.Cursor,
		Wait:      time.Duration(request.WaitMilliseconds) * time.Millisecond,
	})
	if err != nil {
		var projectMissing *state.ProjectNotFoundError
		var serviceMissing *reconcile.ProjectServiceNotFoundError
		if errors.As(err, &projectMissing) || errors.As(err, &serviceMissing) {
			return control.ServiceLogs{}, control.NewServiceLogsNotFoundError(err)
		}
		return control.ServiceLogs{}, err
	}
	result := control.ServiceLogs{
		ProjectID: logs.ProjectID,
		ServiceID: logs.ServiceID,
		SessionID: logs.SessionID,
		Supported: logs.Supported,
		Available: logs.Available,
		Ports:     make([]control.ServicePort, 0, len(logs.Ports)),
		Output: control.ServiceLogOutputChunk{
			Available:  logs.Output.Available,
			Reset:      logs.Output.Reset,
			Truncated:  logs.Output.Truncated,
			HasMore:    logs.Output.HasMore,
			NextCursor: logs.Output.NextCursor,
			Text:       logs.Output.Text,
		},
	}
	for _, port := range logs.Ports {
		result.Ports = append(result.Ports, control.ServicePort{
			Address: port.Address, Private: port.Private, Public: port.Public, Protocol: port.Protocol, Replica: port.Replica,
		})
	}
	if logs.Problem != nil {
		result.Problem = &control.ServiceLogProblem{
			Code:      logs.Problem.Code,
			Message:   logs.Problem.Message,
			Retryable: logs.Problem.Retryable,
		}
	}
	if err := result.Validate(); err != nil {
		return control.ServiceLogs{}, fmt.Errorf("service logs result: %w", err)
	}
	if result.ProjectID != request.ProjectID || result.ServiceID != request.ServiceID {
		return control.ServiceLogs{}, errors.New("service logs result differs from its requested project or service")
	}
	bounded, err := control.BoundServiceLogsResponse(result)
	if err != nil {
		return control.ServiceLogs{}, fmt.Errorf("bound service logs result: %w", err)
	}
	return bounded, nil
}

// projectLifecycleResult validates that asynchronous progress still belongs to the requested client intent.
func projectLifecycleResult(
	record state.OperationRecord,
	projectID domain.ProjectID,
	intentID domain.IntentID,
	kind domain.OperationKind,
) (control.ProjectLifecycleOperation, error) {
	result := control.ProjectLifecycleOperation{Operation: record.Operation, Revision: record.Revision}
	if err := result.Validate(); err != nil {
		return control.ProjectLifecycleOperation{}, fmt.Errorf("project lifecycle result: %w", err)
	}
	if result.Operation.ProjectID != projectID || result.Operation.IntentID != intentID || result.Operation.Kind != kind {
		return control.ProjectLifecycleOperation{}, errors.New("project lifecycle result differs from its requested action, project, and intent")
	}
	return result, nil
}

// classifyProjectLifecycleError maps reviewed request-state failures to stable control categories.
func classifyProjectLifecycleError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var projectMissing *state.ProjectNotFoundError
	if errors.As(err, &projectMissing) {
		return control.NewProjectLifecycleNotFoundError(err)
	}
	var sessionMissing *state.ProjectSessionNotFoundError
	var intentConflict *state.IntentConflictError
	var projectBusy *state.ProjectBusyError
	var sessionActive *state.ProjectSessionActiveError
	var staleRevision *state.StaleRevisionError
	if errors.As(err, &sessionMissing) ||
		errors.As(err, &intentConflict) ||
		errors.As(err, &projectBusy) ||
		errors.As(err, &sessionActive) ||
		errors.As(err, &staleRevision) {
		return control.NewProjectLifecycleConflictError(err)
	}
	return err
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
	if errors.Is(err, ticketspool.ErrNotInstalled) {
		return control.NewProjectUnregisterApprovalPrivilegedHelperRequiredError(err)
	}
	if errors.Is(err, ticketspool.ErrUnsafePath) {
		return control.NewProjectUnregisterApprovalPrivilegedHelperUnsafeError(err)
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

// classifyNetworkSetupError maps only reviewed setup-state failures to stable control categories.
func classifyNetworkSetupError(err error) error {
	var corruptState *state.CorruptStateError
	if errors.As(err, &corruptState) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var poolObservation *ticketissuer.PoolObservationError
	if errors.As(err, &poolObservation) {
		if validationErr := poolObservation.Validate(); validationErr != nil {
			return err
		}
		stage := control.NetworkSetupObservationStage("")
		switch poolObservation.Stage() {
		case ticketissuer.PoolObservationAssignment:
			stage = control.NetworkSetupObservationAssignment
		case ticketissuer.PoolObservationHostConflicts:
			stage = control.NetworkSetupObservationHostConflicts
		}
		detail, _ := poolObservation.ReviewedDetail()
		return control.NewNetworkSetupObservationError(err, stage, poolObservation.Address(), detail)
	}
	var prerequisiteMissing *ticketissuer.PoolPrerequisiteMissingError
	if errors.As(err, &prerequisiteMissing) {
		return control.NewNetworkSetupPrivilegedHelperRequiredError(err)
	}
	if errors.Is(err, ticketspool.ErrUnsafePath) {
		return control.NewNetworkSetupPrivilegedHelperUnsafeError(err)
	}
	var intentConflict *state.IntentConflictError
	var staleRevision *state.StaleRevisionError
	var networkConflict *state.NetworkInitializationConflictError
	var poolExhaustion *identity.PoolSelectionExhaustionError
	if errors.As(err, &intentConflict) ||
		errors.As(err, &staleRevision) ||
		errors.As(err, &networkConflict) ||
		errors.As(err, &poolExhaustion) {
		return control.NewNetworkSetupConflictError(err)
	}
	var operationMissing *state.OperationNotFoundError
	if errors.As(err, &operationMissing) {
		return control.NewNetworkSetupNotFoundError(err)
	}
	return err
}

// classifyNetworkResolverSetupError maps only reviewed resolver-state failures to stable control categories.
func classifyNetworkResolverSetupError(err error) error {
	var corruptState *state.CorruptStateError
	if errors.As(err, &corruptState) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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
	var resolverActivation *state.NetworkResolverActivationConflictError
	var completionConflict *state.NetworkResolverSetupCompletionConflictError
	if errors.As(err, &intentConflict) ||
		errors.As(err, &staleRevision) ||
		errors.As(err, &networkMissing) ||
		errors.As(err, &networkRevision) ||
		errors.As(err, &resolverActivation) ||
		errors.As(err, &completionConflict) ||
		errors.Is(err, ticketissuer.ErrResolverPublicationIndeterminate) {
		return control.NewNetworkResolverSetupConflictError(err)
	}
	var operationMissing *state.OperationNotFoundError
	if errors.As(err, &operationMissing) {
		return control.NewNetworkResolverSetupNotFoundError(err)
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

// newOpaqueInstallationID generates 128 bits of installation identity without encoding host or user metadata.
func newOpaqueInstallationID() (identity.InstallationID, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	installationID := identity.InstallationID("installation-" + hex.EncodeToString(random))
	if err := installationID.Validate(); err != nil {
		return "", err
	}
	return installationID, nil
}

// normalizeContext keeps nil control calls usable while preserving explicit cancellation.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}
